package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"stressbot/errcode"
	flowdef "stressbot/flow"
	"stressbot/protocol"
	"stressbot/state"
)

// canceledSpySender 任何一次网络调用都记账：取消后 Execute 不应再碰网络。
type canceledSpySender struct {
	fakeNetSender
	calls []string
}

func (s *canceledSpySender) TCPSend(string, []byte) (int, error) {
	s.calls = append(s.calls, "TCPSend")
	return 0, nil
}

func (s *canceledSpySender) UDPSend(string, []byte) (int, error) {
	s.calls = append(s.calls, "UDPSend")
	return 0, nil
}

func (s *canceledSpySender) TCPRequest(context.Context, string, []byte, string, ...time.Duration) (*NetExchange, error) {
	s.calls = append(s.calls, "TCPRequest")
	return nil, nil
}

func (s *canceledSpySender) UDPRequest(context.Context, string, []byte, string, ...time.Duration) (*NetExchange, error) {
	s.calls = append(s.calls, "UDPRequest")
	return nil, nil
}

func (s *canceledSpySender) TCPListen(context.Context, string, string, time.Duration) (*NetExchange, error) {
	s.calls = append(s.calls, "TCPListen")
	return nil, nil
}

func (s *canceledSpySender) UDPListen(context.Context, string, string, time.Duration) (*NetExchange, error) {
	s.calls = append(s.calls, "UDPListen")
	return nil, nil
}

func (s *canceledSpySender) ConnectTCP(string, string) error {
	s.calls = append(s.calls, "ConnectTCP")
	return nil
}

func (s *canceledSpySender) ConnectUDP(string, string) error {
	s.calls = append(s.calls, "ConnectUDP")
	return nil
}

func (s *canceledSpySender) HTTPRequest(string, string, string, []byte) (*HTTPExchange, error) {
	s.calls = append(s.calls, "HTTPRequest")
	return &HTTPExchange{StatusCode: 200}, nil
}

func (s *canceledSpySender) CloseTCP(string) { s.calls = append(s.calls, "CloseTCP") }
func (s *canceledSpySender) CloseUDP(string) { s.calls = append(s.calls, "CloseUDP") }

// wantCanceled 断言错误是取消：码为 ErrActionCanceled，且 Unwrap 到 context.Canceled。
// 两条一起断言才有意义——上层 monitor 按码分流，executor 按 errors.Is 短路。
func wantCanceled(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("期望取消错误，实际 nil")
	}
	actionErr, ok := errors.AsType[*errcode.ActionError](err)
	if !ok {
		t.Fatalf("err=%v（%T），期望 *errcode.ActionError", err, err)
	}
	if actionErr.Code != errcode.ErrActionCanceled {
		t.Fatalf("错误码=%d，期望 ErrActionCanceled(%d)", actionErr.Code, errcode.ErrActionCanceled)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v 未保留 context.Canceled", err)
	}
}

// TestExecuteCanceledCtxShortCircuitsEveryPattern 全部 pattern 在 ctx 已取消时都必须
// 立即返回取消错误，且一次网络调用都不发生——包括 setState/clearState/close 这类本地动作
// （停止阶段不再制造新的副作用）。
func TestExecuteCanceledCtxShortCircuitsEveryPattern(t *testing.T) {
	patterns := []string{
		flowdef.PatternTCPSend, flowdef.PatternTCPRequest, flowdef.PatternTCPConnect,
		flowdef.PatternTCPClose, flowdef.PatternTCPListen,
		flowdef.PatternUDPSend, flowdef.PatternUDPRequest, flowdef.PatternUDPConnect,
		flowdef.PatternUDPClose, flowdef.PatternUDPListen,
		flowdef.PatternHTTPRequest, flowdef.PatternClearState, flowdef.PatternSetState,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, pattern := range patterns {
		t.Run(pattern, func(t *testing.T) {
			sender := &canceledSpySender{}
			store := state.NewStore()
			ae := NewActionExecutor(store, sender, nil, nil, TimingLevelRTTOnly)

			sendBytes, recvBytes, timing, err := ae.Execute(ctx, &flowdef.ActionDef{
				Name: "act", Pattern: pattern, Service: "logic",
				Address: "127.0.0.1:1", URL: "http://x/y", Keys: []string{"k"},
			})

			wantCanceled(t, err)
			if len(sender.calls) != 0 {
				t.Fatalf("取消后仍调用了网络方法: %v", sender.calls)
			}
			if sendBytes != 0 || recvBytes != 0 {
				t.Fatalf("取消后不应产生字节统计: send=%d recv=%d", sendBytes, recvBytes)
			}
			if timing.FailedRequests != 0 || timing.ListenTimeouts != 0 {
				t.Fatalf("取消不是服务端表现，不应产出失败/超时样本: %+v", timing)
			}
		})
	}
}

// blockingRequestSender 模拟「请求已发出、还没等到响应」：阻塞到 ctx 取消才返回，
// 与 robot 层 awaitResponse 的取消返回值口径一致（ErrActionCanceled）。
type blockingRequestSender struct {
	fakeNetSender
	entered chan struct{}
	gotCtx  context.Context
}

func (s *blockingRequestSender) TCPRequest(ctx context.Context, service string, packet []byte, routeKey string, _ ...time.Duration) (*NetExchange, error) {
	s.gotCtx = ctx
	close(s.entered)
	<-ctx.Done()
	return &NetExchange{SendWireBytes: len(packet)},
		errcode.NewActionError(errcode.ErrActionCanceled, "service="+service+" route="+routeKey, ctx.Err())
}

// TestExecuteRequestUnblocksOnCtxCancel 在途 tcpRequest 必须随流程 ctx 取消立刻退出，
// 而不是等满动作超时窗口；并且取消不计入 FailedRequests（Apdex frustrated 分母）。
func TestExecuteRequestUnblocksOnCtxCancel(t *testing.T) {
	adp := &encodeSpyAdapter{encodeReturned: []byte("pkt"), routeKeyReturned: "S2C.Login"}
	sender := &blockingRequestSender{entered: make(chan struct{})}
	ae := NewActionExecutor(state.NewStore(), sender, nil,
		&stubResolverE{byServer: map[string]protocol.Adapter{"tcp:logic": adp}}, TimingLevelRTTOnly)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		timing ActionTiming
		err    error
	}
	done := make(chan result, 1)
	go func() {
		_, _, timing, err := ae.Execute(ctx, &flowdef.ActionDef{
			Name: "login", Pattern: flowdef.PatternTCPRequest, Service: "logic",
			Timeout: 600, // 10 分钟：取消若不生效，这个用例只会以超时失败告终
		})
		done <- result{timing: timing, err: err}
	}()

	select {
	case <-sender.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("请求未进入网络委托")
	}
	cancel()

	select {
	case got := <-done:
		wantCanceled(t, got.err)
		if got.timing.FailedRequests != 0 {
			t.Fatalf("客户端取消不应记 frustrated 样本，FailedRequests=%d", got.timing.FailedRequests)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 Execute 未及时返回（仍在等动作超时窗口）")
	}

	if sender.gotCtx == nil || sender.gotCtx.Err() == nil {
		t.Fatal("网络委托拿到的不是流程 ctx（取消未透传）")
	}
}

// TestExecuteRequestTimeoutStaysTimeout 未取消时超时仍归超时：取消与超时的错误码不能混同，
// 否则停止任务会在监控面堆出假超时。
func TestExecuteRequestTimeoutStaysTimeout(t *testing.T) {
	adp := &encodeSpyAdapter{encodeReturned: []byte("pkt"), routeKeyReturned: "S2C.Login"}
	sender := &timeoutRequestSender{}
	ae := NewActionExecutor(state.NewStore(), sender, nil,
		&stubResolverE{byServer: map[string]protocol.Adapter{"tcp:logic": adp}}, TimingLevelRTTOnly)

	_, _, timing, err := ae.Execute(context.Background(), &flowdef.ActionDef{
		Name: "login", Pattern: flowdef.PatternTCPRequest, Service: "logic",
	})

	actionErr, ok := errors.AsType[*errcode.ActionError](err)
	if !ok || actionErr.Code != errcode.ErrRecvTimeout {
		t.Fatalf("err=%v，期望 ErrRecvTimeout", err)
	}
	if timing.FailedRequests != 1 {
		t.Fatalf("超时应记 frustrated 样本，FailedRequests=%d", timing.FailedRequests)
	}
}

type timeoutRequestSender struct {
	fakeNetSender
}

func (s *timeoutRequestSender) TCPRequest(_ context.Context, service string, packet []byte, routeKey string, _ ...time.Duration) (*NetExchange, error) {
	return &NetExchange{SendWireBytes: len(packet)},
		errcode.NewActionError(errcode.ErrRecvTimeout, "service="+service+" route="+routeKey)
}
