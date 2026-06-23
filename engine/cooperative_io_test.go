package engine

import (
	"sync/atomic"
	"testing"

	"stressbot/state"
)

// fakeIONetSender 复用 fakeHeartbeatNetSender，仅记录 connect/http 调用次数。
type fakeIONetSender struct {
	*fakeHeartbeatNetSender
	connectTCP int32
	connectUDP int32
	httpReq    int32
}

func (f *fakeIONetSender) ConnectTCP(string, string) error {
	atomic.AddInt32(&f.connectTCP, 1)
	return nil
}
func (f *fakeIONetSender) ConnectUDP(string, string) error {
	atomic.AddInt32(&f.connectUDP, 1)
	return nil
}
func (f *fakeIONetSender) HTTPRequest(string, string, string, []byte) (*HTTPExchange, error) {
	atomic.AddInt32(&f.httpReq, 1)
	return &HTTPExchange{StatusCode: 200}, nil
}

// TestDeclarativeIO_RoutesThroughCoopIO 声明式 httpRequest / tcpConnect / udpConnect 的阻塞调用
// 应全部经注入的 coopIO（协作式：后台跑 + drain mailbox），与 Lua await_* 同一原语。
func TestDeclarativeIO_RoutesThroughCoopIO(t *testing.T) {
	fake := &fakeIONetSender{fakeHeartbeatNetSender: &fakeHeartbeatNetSender{}}
	ae := &ActionExecutor{netSender: fake, store: state.NewStore()}

	var coopCalls int32
	ae.SetCooperativeIO(func(job func()) {
		atomic.AddInt32(&coopCalls, 1)
		job() // 模拟调度器：实际执行作业（真实实现里在后台 goroutine + drain mailbox）
	})

	if err := ae.execTCPConnect(&ActionDef{Name: "c", Service: "logic", Address: "127.0.0.1:1"}); err != nil {
		t.Fatalf("execTCPConnect err: %v", err)
	}
	if err := ae.execUDPConnect(&ActionDef{Name: "c", Service: "battle", Address: "127.0.0.1:2"}); err != nil {
		t.Fatalf("execUDPConnect err: %v", err)
	}
	if _, _, _, err := ae.execHTTPRequest(&ActionDef{Name: "h", URL: "http://x/y"}); err != nil {
		t.Fatalf("execHTTPRequest err: %v", err)
	}

	if got := atomic.LoadInt32(&coopCalls); got != 3 {
		t.Fatalf("声明式 http/connect 应全部经 coopIO，实际 %d/3", got)
	}
	if atomic.LoadInt32(&fake.connectTCP) != 1 || atomic.LoadInt32(&fake.connectUDP) != 1 || atomic.LoadInt32(&fake.httpReq) != 1 {
		t.Fatalf("底层网络方法应各被调用一次：tcp=%d udp=%d http=%d",
			fake.connectTCP, fake.connectUDP, fake.httpReq)
	}

	// 未注入 coopIO（engine 独立运行/测试）时回退同步调用，仍能完成。
	ae2 := &ActionExecutor{netSender: fake, store: state.NewStore()}
	if err := ae2.execTCPConnect(&ActionDef{Name: "c", Service: "logic", Address: "127.0.0.1:1"}); err != nil {
		t.Fatalf("无 coopIO 回退应同步成功: %v", err)
	}
	if atomic.LoadInt32(&fake.connectTCP) != 2 {
		t.Fatalf("无 coopIO 回退应仍调底层 ConnectTCP，实际 connectTCP=%d", fake.connectTCP)
	}
}
