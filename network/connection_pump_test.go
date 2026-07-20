package network

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"stressbot/adapter"

	stresslog "stressbot/utils/log"
)

// fakeAdapter 是 connectionPump 测试用的最小 adapter.Adapter 实现。
//
// 只关心 DecodeTCP / DecodeUDP（pump 的 decode 热路径）；其余方法返回零值即可，
// 因为 pump 测试不涉及 encode/routeKey/BodyLength（那些由 OnTraffic 在 gnet 侧已处理，
// 测试直接投递 inboundCh 绕过 OnTraffic）。
type fakeAdapter struct {
	mu          sync.Mutex
	decodeCalls int32
	// decodeRouteKey / decodeBody 控制下一次 decode 返回值；默认返回 ("test.route", body, 0)。
	decodeRouteKey string
}

func newFakeAdapter() *fakeAdapter {
	return &fakeAdapter{decodeRouteKey: "test.route"}
}

func (a *fakeAdapter) HeaderSize() int                                     { return 4 }
func (a *fakeAdapter) BodyLength(headerData []byte) int                    { return 0 }
func (a *fakeAdapter) EncodeTCP(route any, body []byte, key []byte) []byte { return nil }
func (a *fakeAdapter) EncodeUDP(route any, body []byte, key []byte) []byte { return nil }

func (a *fakeAdapter) DecodeTCP(data []byte, key []byte) (string, []byte, uint64) {
	atomic.AddInt32(&a.decodeCalls, 1)
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.decodeRouteKey, data, 0
}

func (a *fakeAdapter) DecodeUDP(data []byte, key []byte) (string, []byte, uint64) {
	atomic.AddInt32(&a.decodeCalls, 1)
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.decodeRouteKey, data, 0
}

func (a *fakeAdapter) ExpectedRouteKey(route any) string { return "test.route" }
func (a *fakeAdapter) Close()                            {}
func (a *fakeAdapter) DescribeError(code uint64) string  { return "" }

// 编译期断言：fakeAdapter 实现 adapter.Adapter。
var _ adapter.Adapter = (*fakeAdapter)(nil)

// startPumpedConnection 构造一个已启动 connectionPump 的测试连接（带可控 sendFunc）。
// 返回连接与一个记录所有发送字节原子计数器。
func startPumpedConnection(t *testing.T, adp adapter.Adapter, isUDP bool) (*Connection, *int32) {
	t.Helper()
	conn := newTestConnection(t)
	var sent int32
	conn.sendFunc = func(data []byte) error {
		atomic.AddInt32(&sent, 1)
		return nil
	}
	if err := conn.StartPump(adp, isUDP); err != nil {
		t.Fatalf("StartPump() error = %v", err)
	}
	return conn, &sent
}

func TestStartPumpRollsBackWhenPoolRejects(t *testing.T) {
	conn := newTestConnection(t)
	adp := newFakeAdapter()
	sentinel := errors.New("pool rejected")
	submissions := 0

	err := conn.startPumpWithSubmit(adp, false, func(func()) error {
		submissions++
		return sentinel
	})

	if !errors.Is(err, sentinel) {
		t.Fatalf("startPumpWithSubmit() error = %v, want %v", err, sentinel)
	}
	if atomic.LoadInt32(&conn.pumpRun) != 0 {
		t.Fatal("pumpRun was not rolled back")
	}
	if conn.inboundCh != nil || conn.controlCh != nil || conn.pumpDone != nil {
		t.Fatalf("pump channels were not rolled back: inbound=%v control=%v done=%v",
			conn.inboundCh != nil, conn.controlCh != nil, conn.pumpDone != nil)
	}

	_ = conn.startPumpWithSubmit(adp, false, func(func()) error {
		submissions++
		return sentinel
	})
	if submissions != 2 {
		t.Fatalf("submissions = %d, want retry submission", submissions)
	}
}

func TestStartPumpBuffersFirstFrameBeforeWorkerRuns(t *testing.T) {
	conn := newTestConnection(t)
	adp := newFakeAdapter()
	var pumpTask func()

	if err := conn.startPumpWithSubmit(adp, false, func(task func()) error {
		pumpTask = task
		return nil
	}); err != nil {
		t.Fatalf("startPumpWithSubmit() error = %v", err)
	}
	if pumpTask == nil {
		t.Fatal("pump task was not submitted")
	}

	const routeKey = "test.route"
	responseCh := make(chan *Message, 1)
	conn.mu.Lock()
	conn.responseMap[routeKey] = responseCh
	conn.mu.Unlock()
	if got := conn.EnqueueRaw([]byte("first"), time.Now()); got != EnqueueOK {
		t.Fatalf("EnqueueRaw() = %v, want %v", got, EnqueueOK)
	}

	go pumpTask()
	defer conn.Close()
	select {
	case msg := <-responseCh:
		if string(msg.Data) != "first" {
			t.Fatalf("first frame data = %q, want %q", msg.Data, "first")
		}
	case <-time.After(time.Second):
		t.Fatal("first frame was not dispatched after pump worker started")
	}
}

// --- pump 消费 inbound → dispatch（request-response）---

// TestPump_InboundDispatch_RequestResponse 验证：pump 消费 inboundCh → decode → 命中
// responseMap → 响应投递到 RequestResponse 的 channel。
func TestPump_InboundDispatch_RequestResponse(t *testing.T) {
	adp := newFakeAdapter()
	conn, _ := startPumpedConnection(t, adp, false)
	defer conn.Close()

	// 预埋 responseMap（模拟 inflight RequestResponse）。
	const routeKey = "test.route"
	ch := make(chan *Message, 1)
	conn.mu.Lock()
	conn.responseMap[routeKey] = ch
	conn.mu.Unlock()

	// 投递一条 inbound：fakeAdapter decode 出 routeKey="test.route" → 命中 responseMap。
	conn.inboundCh <- inboundFrame{Data: []byte("hello"), WireBytes: 5, RecvFrameAt: time.Now()}

	select {
	case msg := <-ch:
		if msg.RouteKey != routeKey {
			t.Fatalf("msg.RouteKey = %q, want %q", msg.RouteKey, routeKey)
		}
		if string(msg.Data) != "hello" {
			t.Fatalf("msg.Data = %q, want %q", msg.Data, "hello")
		}
	case <-time.After(time.Second):
		t.Fatal("pump 未在 1s 内把 inbound 分发到 responseMap channel")
	}
}

// TestPump_InboundDispatch_ListenQueue 验证：pump 消费 inbound → decode → 命中 listenResp（nil cb）
// → 同步 dispatchListen → Push 到 listenQueues → GetListenResp FIFO pop。
func TestPump_InboundDispatch_ListenQueue(t *testing.T) {
	adp := newFakeAdapter()
	conn, _ := startPumpedConnection(t, adp, false)
	defer conn.Close()

	const routeKey = "test.route"
	if err := conn.RegisterListen(routeKey, nil, 2); err != nil {
		t.Fatalf("RegisterListen 失败: %v", err)
	}

	// 投递两条 inbound → pump decode + dispatchListen → listenQueues FIFO。
	conn.inboundCh <- inboundFrame{Data: []byte("A"), WireBytes: 1, RecvFrameAt: time.Now()}
	conn.inboundCh <- inboundFrame{Data: []byte("B"), WireBytes: 1, RecvFrameAt: time.Now()}

	// 等 pump 处理完（轮询 GetListenResp 直到拿到 B）。
	want := []byte{'A', 'B'}
	for i, w := range want {
		var got *Message
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if m := conn.GetListenResp(routeKey); m != nil {
				got = m
				break
			}
			time.Sleep(time.Millisecond)
		}
		if got == nil {
			t.Fatalf("第 %d 条 listen 消息未在 1s 内到达（want %q）", i+1, w)
		}
		if len(got.Data) != 1 || got.Data[0] != w {
			t.Fatalf("第 %d 条 listen.Data = %v, want %q", i+1, got.Data, w)
		}
	}
}

// TestPump_InboundDispatch_ListenCallback 验证：命中 listenResp（非 nil cb）→ 同步调 cb。
func TestPump_InboundDispatch_ListenCallback(t *testing.T) {
	adp := newFakeAdapter()
	conn, _ := startPumpedConnection(t, adp, false)
	defer conn.Close()

	const routeKey = "test.route"
	received := make(chan *Message, 2)
	cb := func(m *Message) { received <- m }
	if err := conn.RegisterListen(routeKey, cb, 1); err != nil {
		t.Fatalf("RegisterListen 失败: %v", err)
	}

	conn.inboundCh <- inboundFrame{Data: []byte("X"), WireBytes: 1, RecvFrameAt: time.Now()}

	select {
	case m := <-received:
		if string(m.Data) != "X" {
			t.Fatalf("cb 收到 %q, want %q", m.Data, "X")
		}
	case <-time.After(time.Second):
		t.Fatal("pump 未在 1s 内把 inbound 同步分发到 listen 回调")
	}
}

// --- controlCh 注册心跳 ---

// TestPump_Control_RegisterHeartbeat 验证：RegisterHeartbeat 经 controlCh 驱动 pump 安装心跳，
// 到期时调 builder → Send。
func TestPump_Control_RegisterHeartbeat(t *testing.T) {
	adp := newFakeAdapter()
	conn, sent := startPumpedConnection(t, adp, false)
	defer conn.Close()

	var builderCalls int32
	builder := func() []byte {
		atomic.AddInt32(&builderCalls, 1)
		return []byte("ping")
	}
	conn.RegisterHeartbeat(HeartbeatConfig{Interval: 30 * time.Millisecond, Builder: builder})

	// 等 2 个 interval，期望至少发送一次心跳。
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(sent) >= 1 && atomic.LoadInt32(&builderCalls) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&builderCalls); got < 1 {
		t.Fatalf("builder 调用次数 = %d, want >= 1（pump 未触发心跳到期）", got)
	}
	if got := atomic.LoadInt32(sent); got < 1 {
		t.Fatalf("Send 调用次数 = %d, want >= 1（pump 未发送心跳）", got)
	}
}

// TestPump_Control_StopHeartbeat 验证：StopHeartbeat 经 controlCh 让 pump 停止心跳，之后不再发送。
func TestPump_Control_StopHeartbeat(t *testing.T) {
	adp := newFakeAdapter()
	conn, sent := startPumpedConnection(t, adp, false)
	defer conn.Close()

	builder := func() []byte { return []byte("ping") }
	conn.RegisterHeartbeat(HeartbeatConfig{Interval: 20 * time.Millisecond, Builder: builder})

	// 等至少发送一次。
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) && atomic.LoadInt32(sent) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	before := atomic.LoadInt32(sent)
	if before == 0 {
		t.Fatal("停止前未发送任何心跳")
	}

	conn.StopHeartbeat()
	sentAtStop := atomic.LoadInt32(sent)

	// 再等 3 个 interval，确认不再有新的心跳发送。
	time.Sleep(80 * time.Millisecond)
	if got := atomic.LoadInt32(sent); got != sentAtStop {
		t.Fatalf("StopHeartbeat 后仍有心跳发送：stop 时=%d，80ms 后=%d", sentAtStop, got)
	}
}

// --- ctx.Done → pump 退出无 goroutine 泄漏 ---

// TestPump_Close_NoLeak 验证：Close（cancel ctx）后 pump 退出，pumpDone 关闭，
// WaitPumpDone 在合理时间内返回。
func TestPump_Close_NoLeak(t *testing.T) {
	adp := newFakeAdapter()
	conn, _ := startPumpedConnection(t, adp, false)

	// 注册心跳，确认 Close 时 pump 同时停心跳 timer。
	builder := func() []byte { return []byte("ping") }
	conn.RegisterHeartbeat(HeartbeatConfig{Interval: 50 * time.Millisecond, Builder: builder})

	conn.Close()

	done := make(chan struct{})
	go func() {
		conn.WaitPumpDone()
		close(done)
	}()
	select {
	case <-done:
		// pump 已退出，无泄漏。
	case <-time.After(time.Second):
		t.Fatal("Close 后 pump 未在 1s 内退出（goroutine 泄漏）")
	}
}

// TestPump_BoundedBatch_DoesNotStarveHeartbeat 验证硬约束 2：inbound backlog 不会饿死心跳。
//
// 构造：向 inboundCh 投递远超 pumpInboundBatchSize 的消息，同时注册短间隔心跳；
// 期望在 inbound 处理过程中，心跳仍按间隔被触发（因为每处理 batch 上限就回外层检查 heartbeat due）。
func TestPump_BoundedBatch_DoesNotStarveHeartbeat(t *testing.T) {
	if !stresslog.DebugEnabled() {
		// 本测试不依赖日志，这里仅避免 logger nil；newTestConnection 已 InitLog。
	}
	adp := newFakeAdapter()
	conn, sent := startPumpedConnection(t, adp, false)
	defer conn.Close()

	// 先注册短间隔心跳。
	builder := func() []byte { return []byte("ping") }
	conn.RegisterHeartbeat(HeartbeatConfig{Interval: 15 * time.Millisecond, Builder: builder})

	// 灌入大量 inbound（远超 inboundCh 缓冲 + pumpInboundBatchSize）。
	// 用 fakeAdapter.decodeRouteKey = "test.route"，但未注册该 routeKey 的 listen/responseMap，
	// 所以 decode 后走 OnReceive 的「未匹配」分支丢弃——但仍消耗 pump CPU（decode+查 map）。
	const frames = 200
	for i := 0; i < frames; i++ {
		conn.inboundCh <- inboundFrame{Data: []byte{byte(i)}, WireBytes: 1, RecvFrameAt: time.Now()}
	}

	// 等待足够长（远超心跳 interval × 多次），期望心跳至少发送数次。
	time.Sleep(150 * time.Millisecond)
	if got := atomic.LoadInt32(sent); got < 2 {
		t.Fatalf("inbound backlog 期间心跳发送次数 = %d, want >= 2（bounded batch 未能防止心跳饿死）", got)
	}

	// 排空剩余 inbound 让 pump 安静退出（Close 会 drain，这里只是辅助）。
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		// 等待 fakeAdapter.decodeCalls 接近 frames，说明 pump 已处理完大部分。
		if atomic.LoadInt32(&adp.decodeCalls) >= int32(frames) {
			break
		}
		time.Sleep(time.Millisecond)
	}
}

// --- RegisterHeartbeat 在 pump 未启动时的兼容路径 ---

// TestRegisterHeartbeat_PumpNotStarted 验证：pump 未启动（controlCh==nil）时，
// RegisterHeartbeat 走降级路径直接写 hb + 启动 timer，不 panic、不泄漏。
// 生产路径 dial 总是先 StartPump，此分支主要服务于测试与异常态。
func TestRegisterHeartbeat_PumpNotStarted(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	builder := func() []byte { return []byte("ping") }
	// 不调 StartPump；RegisterHeartbeat 走 controlCh==nil 分支。
	conn.RegisterHeartbeat(HeartbeatConfig{Interval: 50 * time.Millisecond, Builder: builder})

	// hb 字段应已写入（带 timer）。
	conn.hbMu.Lock()
	hb := conn.hb
	conn.hbMu.Unlock()
	if hb == nil {
		t.Fatal("pump 未启动时 RegisterHeartbeat 应直接写 hb 字段")
	}
	if hb.timer == nil {
		t.Fatal("hb.timer 未创建")
	}

	// StopHeartbeat 也走 controlCh==nil 分支，应停 timer。
	conn.StopHeartbeat()
	conn.hbMu.Lock()
	hb2 := conn.hb
	conn.hbMu.Unlock()
	if hb2 != nil {
		t.Fatal("StopHeartbeat 后 hb 应为 nil")
	}
}
