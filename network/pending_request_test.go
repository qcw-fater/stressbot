package network

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newSendableConn 构造一个注入了 sendFunc 的测试连接，记录最近一次发送的数据。
func newSendableConn(t *testing.T) (*Connection, *atomic.Value) {
	t.Helper()
	conn := newTestConnection(t)
	var sent atomic.Value
	conn.sendFunc = func(data []byte, onWritten WriteDoneFunc) error {
		cp := make([]byte, len(data))
		copy(cp, data)
		sent.Store(cp)
		// 模拟事件循环：数据交给内核后回调写完成时刻。
		if onWritten != nil {
			onWritten(time.Now())
		}
		return nil
	}
	return conn, &sent
}

// TestSendRequest_RegisterSendAndDeliver 验证 SendRequest 注册响应通道 + 发送请求，
// OnReceive 投递的响应能从 C() 收到，Timing 计算出正向 SendCost/WireRTT，Close 注销通道。
func TestSendRequest_RegisterSendAndDeliver(t *testing.T) {
	conn, sent := newSendableConn(t)
	defer conn.Close()

	const routeKey = "C2S.Login"
	pr, err := conn.SendRequest([]byte("hello"), routeKey, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("SendRequest 失败: %v", err)
	}
	if got, _ := sent.Load().([]byte); string(got) != "hello" {
		t.Fatalf("sendFunc 收到 %q，want hello", got)
	}
	if pr.Timeout() != 500*time.Millisecond {
		t.Fatalf("Timeout=%v，want 500ms", pr.Timeout())
	}

	// 模拟 pump 投递响应。
	now := time.Now()
	conn.OnReceive(routeKey, []byte("world"), 0, 12, MessageTiming{
		RecvFrameAt: now, DecodeStart: now, DecodeEnd: now, DispatchStart: now,
	})

	select {
	case resp := <-pr.C():
		if string(resp.Data) != "world" {
			t.Fatalf("响应 Data=%q，want world", resp.Data)
		}
		timing := pr.Timing(resp)
		if timing.SendCost < 0 {
			t.Fatalf("SendCost 应 >=0，实际 %v", timing.SendCost)
		}
	case <-time.After(time.Second):
		t.Fatal("未从 C() 收到响应")
	}

	// Close 后 responseMap 注销，通道关闭。
	pr.Close()
	conn.mu.Lock()
	_, exists := conn.responseMap[routeKey]
	conn.mu.Unlock()
	if exists {
		t.Fatal("Close 后 responseMap 仍存在该 routeKey")
	}
	// 幂等：再 Close 不 panic。
	pr.Close()
}

// TestSendRequest_SendError 发送失败时返回 error 且不泄漏 responseMap 条目。
func TestSendRequest_SendError(t *testing.T) {
	conn := newTestConnection(t) // 无 sendFunc → Send 返回 ErrSendFailed
	defer conn.Close()

	const routeKey = "C2S.Fail"
	pr, err := conn.SendRequest([]byte("x"), routeKey, time.Second)
	if err == nil {
		t.Fatal("无 sendFunc 时 SendRequest 应返回 error")
	}
	if pr != nil {
		t.Fatalf("发送失败应返回 nil 句柄，实际 %+v", pr)
	}
	conn.mu.Lock()
	_, exists := conn.responseMap[routeKey]
	conn.mu.Unlock()
	if exists {
		t.Fatal("发送失败后 responseMap 不应残留条目（泄漏）")
	}
}

// TestSendRequest_Closed 连接已关闭时 SendRequest 返回 error。
func TestSendRequest_Closed(t *testing.T) {
	conn, _ := newSendableConn(t)
	conn.Close()

	pr, err := conn.SendRequest([]byte("x"), "k", time.Second)
	if err == nil {
		t.Fatal("已关闭连接 SendRequest 应返回 error")
	}
	if pr != nil {
		t.Fatalf("已关闭连接应返回 nil 句柄，实际 %+v", pr)
	}
}

// TestSendRequest_DefaultTimeout timeout<=0 时回退到连接默认超时。
func TestSendRequest_DefaultTimeout(t *testing.T) {
	conn, _ := newSendableConn(t) // requestTimeout = time.Second（newTestConnection）
	defer conn.Close()

	pr, err := conn.SendRequest([]byte("x"), "k", 0)
	if err != nil {
		t.Fatalf("SendRequest 失败: %v", err)
	}
	defer pr.Close()
	if pr.Timeout() != time.Second {
		t.Fatalf("timeout=0 应回退默认 1s，实际 %v", pr.Timeout())
	}
}

// TestPendingRequest_ConcurrentClose 并发多次 Close 不应 double-close panic（sync.Once 保证）。
func TestPendingRequest_ConcurrentClose(t *testing.T) {
	conn, _ := newSendableConn(t)
	defer conn.Close()
	pr, err := conn.SendRequest([]byte("x"), "k", time.Second)
	if err != nil {
		t.Fatalf("SendRequest 失败: %v", err)
	}

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			pr.Close()
		})
	}
	wg.Wait() // 若非并发安全会 panic close of closed channel

	conn.mu.Lock()
	_, exists := conn.responseMap["k"]
	conn.mu.Unlock()
	if exists {
		t.Fatal("并发 Close 后 responseMap 应已注销")
	}
}

// TestPendingRequest_TimingNilResp Timing(nil) 仅返回 SendCost，不 panic。
func TestPendingRequest_TimingNilResp(t *testing.T) {
	conn, _ := newSendableConn(t)
	defer conn.Close()
	pr, err := conn.SendRequest([]byte("x"), "k", time.Second)
	if err != nil {
		t.Fatalf("SendRequest 失败: %v", err)
	}
	defer pr.Close()
	timing := pr.Timing(nil)
	if timing.WireRTT != 0 {
		t.Fatalf("nil resp 不应有 WireRTT，实际 %v", timing.WireRTT)
	}
}
