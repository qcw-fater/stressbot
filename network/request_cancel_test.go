package network

import (
	"context"
	"errors"
	"testing"
	"time"

	"stressbot/errcode"
)

func requestErrCode(t *testing.T, err error) errcode.ErrorCode {
	t.Helper()
	if err == nil {
		t.Fatal("期望 error，实际 nil")
	}
	actionErr, ok := errors.AsType[*errcode.ActionError](err)
	if !ok {
		t.Fatalf("err=%v（%T），期望 *errcode.ActionError", err, err)
	}
	return actionErr.Code
}

// responseMapLen 读取当前登记的在途请求数（取消路径不得残留条目）。
func responseMapLen(c *Connection) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.responseMap)
}

// TestSendRequest_CallerCancelUnblocksAndCleansUp 在途请求随调用方 ctx 取消立即退出且不残留
// 登记：连接还活着（c.ctx 未取消），若取消不能唤醒等待方，停止任务的响应时间就会被整个超时
// 窗口拖住。等待与注销由调用方持有（见 robotScheduler.awaitResponse），这里按同样的写法验证
// network 侧的两个前提：在途期间 responseMap 有登记，Close 之后一条不剩。
func TestSendRequest_CallerCancelUnblocksAndCleansUp(t *testing.T) {
	conn, _ := newSendableConn(t)
	defer conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 超时窗口 30s：取消若不生效，这个用例只会以整体超时收场。
	pr, err := conn.SendRequest(ctx, []byte("req"), "C2S.Slow", 30*time.Second)
	if err != nil {
		t.Fatalf("SendRequest 失败: %v", err)
	}
	if n := responseMapLen(conn); n != 1 {
		t.Fatalf("在途请求登记数=%d，期望 1", n)
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	guard := time.NewTimer(5 * time.Second)
	defer guard.Stop()
	select {
	case resp := <-pr.C():
		t.Fatalf("未收到过响应，不应从通道取出: %+v", resp)
	case <-ctx.Done():
	case <-guard.C:
		t.Fatal("取消未唤醒等待方，仍挂在 30s 超时窗口上")
	}

	pr.Close()
	if n := responseMapLen(conn); n != 0 {
		t.Fatalf("取消后 responseMap 残留 %d 条（泄漏）", n)
	}
}

// TestSendRequest_CanceledCtxRejects 已取消时连包都不发，也不注册 responseMap：停止阶段再发
// 一个没人等的请求只会给服务端和统计添噪。
//
// 错误码必须是 ErrActionCanceled 而非 ErrRecvTimeout——前者是压测端自己停机，后者是服务端没
// 按时回包，混同会让每次停止都记一批假超时；并保留 context.Canceled 供 errors.Is 判定。
func TestSendRequest_CanceledCtxRejects(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()
	sent := 0
	conn.sendFunc = func([]byte, WriteDoneFunc) error {
		sent++
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pr, err := conn.SendRequest(ctx, []byte("req"), "C2S.X", time.Second)

	if pr != nil {
		t.Fatalf("取消应返回 nil 句柄，实际 %+v", pr)
	}
	code := requestErrCode(t, err)
	if code != errcode.ErrActionCanceled {
		t.Fatalf("错误码=%d，期望 ErrActionCanceled(%d)", code, errcode.ErrActionCanceled)
	}
	if code == errcode.ErrRecvTimeout {
		t.Fatal("取消不应被判成超时")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v 未保留 context.Canceled", err)
	}
	if sent != 0 {
		t.Fatalf("取消后仍发出 %d 个包", sent)
	}
	if n := responseMapLen(conn); n != 0 {
		t.Fatalf("取消后 responseMap 残留 %d 条（泄漏）", n)
	}
}

// TestSendRequest_ResponseSurvivesCallerCancel 响应与取消同时发生时不丢已到达的响应：
// 投递只看 responseMap 登记，与调用方的取消域无关，响应留在容量 1 的通道里等调用方 drain，
// 否则一次成功的往返会被记成取消。
func TestSendRequest_ResponseSurvivesCallerCancel(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const routeKey = "C2S.Race"
	conn.sendFunc = func(_ []byte, onWritten WriteDoneFunc) error {
		if onWritten != nil {
			onWritten(time.Now())
		}
		// 发送完成的同一时刻：响应入队 + 调用方取消。
		now := time.Now()
		conn.OnReceive(routeKey, []byte("resp"), 0, 4, MessageTiming{
			RecvFrameAt: now, DecodeStart: now, DecodeEnd: now, DispatchStart: now,
		})
		cancel()
		return nil
	}

	pr, err := conn.SendRequest(ctx, []byte("req"), routeKey, time.Second)
	if err != nil {
		t.Fatalf("SendRequest 失败: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("用例前提不成立：调用方 ctx 应已取消")
	}

	select {
	case resp := <-pr.C():
		if resp == nil || string(resp.Data) != "resp" {
			t.Fatalf("响应=%+v，期望 resp", resp)
		}
	default:
		t.Fatal("已到达的响应不应被取消吞掉")
	}

	pr.Close()
	if n := responseMapLen(conn); n != 0 {
		t.Fatalf("responseMap 残留 %d 条", n)
	}
}

// TestRegisterResponseChan_ReportsOverwrite 同 routeKey 重复注册必须被检出（SendRequest 据此
// 打 Warn）。协议没有序列号，routeKey 是响应的唯一匹配依据，所以「一个 routeKey 同时只能有
// 一个在途请求」是协议的固有约束，静默覆盖会让这种流程编排错误查无可查。
//
// 直接断言检测结果而不注入 logger：stresslog 的全局 logger 无锁替换，而本包的测试连接会留下
// 仍在运行的 pump goroutine（同样读该全局），注入会被 -race 判成数据竞争。
func TestRegisterResponseChan_ReportsOverwrite(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	const routeKey = "C2S.Dup"
	if replaced := conn.registerResponseChan(routeKey, make(chan *Message, 1)); replaced {
		t.Fatal("首次注册不应报告覆盖")
	}
	if replaced := conn.registerResponseChan(routeKey, make(chan *Message, 1)); !replaced {
		t.Fatal("同 routeKey 重复注册未被检出，覆盖会静默发生")
	}
	if replaced := conn.registerResponseChan("C2S.Other", make(chan *Message, 1)); replaced {
		t.Fatal("不同 routeKey 不应报告覆盖")
	}
}

// TestSendRequest_DuplicateRouteKeyOverwrites 重复注册的既有行为不变：后者覆盖前者，响应投给
// 后注册的等待者，前一个等待者一条都收不到、只能等满自己的超时窗口。
func TestSendRequest_DuplicateRouteKeyOverwrites(t *testing.T) {
	conn, _ := newSendableConn(t)
	defer conn.Close()

	const routeKey = "C2S.Dup"
	first, err := conn.SendRequest(t.Context(), []byte("first"), routeKey, time.Second)
	if err != nil {
		t.Fatalf("首次 SendRequest 失败: %v", err)
	}
	defer first.Close()

	second, err := conn.SendRequest(t.Context(), []byte("second"), routeKey, 2*time.Second)
	if err != nil {
		t.Fatalf("重复 SendRequest 失败: %v", err)
	}
	defer second.Close()

	now := time.Now()
	conn.OnReceive(routeKey, []byte("resp"), 0, 4, MessageTiming{
		RecvFrameAt: now, DecodeStart: now, DecodeEnd: now, DispatchStart: now,
	})
	select {
	case resp := <-second.C():
		if resp == nil || string(resp.Data) != "resp" {
			t.Fatalf("响应=%+v，期望 resp", resp)
		}
	default:
		t.Fatal("响应未投给后注册的等待者")
	}
	select {
	case resp := <-first.C():
		t.Fatalf("前一个等待者不应收到响应: %+v", resp)
	default:
	}
	if n := responseMapLen(conn); n != 1 {
		t.Fatalf("同 routeKey 只应登记 1 条在途请求，实际 %d 条", n)
	}
}
