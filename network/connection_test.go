package network

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"stressbot/monitor"

	stresslog "stressbot/utils/log"
)

// TestMain 初始化全局日志（network 包代码在 NewConnection、work_pool 等热路径
// 调用 stresslog.Debug/DebugEnabled，未初始化时 logger/loglevel 为 nil，会 panic）。
// 写入临时文件，级别 error 保持测试输出静默。
func TestMain(m *testing.M) {
	stresslog.InitLog(filepath.Join(os.TempDir(), "stressbot_network_test.log"), "test",
		&stresslog.Config{PrintConsole: false, LogLevel: "error"}, "")
	os.Exit(m.Run())
}

// newTestConnection 构造一个最小可用的 *Connection 供 network 包白盒测试使用。
// 不依赖 gnet/Dialer，只用到 listenRoutes / dispatchListen / GetListenResp。
func newTestConnection(t *testing.T) *Connection {
	t.Helper()
	return NewConnection("test-svc", "test-robot", time.Second, monitor.TimingDetailLevel(""))
}

// dispatchListenForTest 直接驱动 dispatchListen（包内可见），绕开 pump，
// 使缓存 listen 行为可被单线程断言。等价于「服务端推送一条到达 dispatchListen」。
//
// 回调与队列的取出方式与 OnReceive 一致：同一次持锁内查一次 listenRoutes 取齐两者，
// 未注册的 routeKey 静默返回（与改造前 dispatchListen 查不到绑定即 return 等价）。
func dispatchListenForTest(c *Connection, m *Message) {
	c.mu.Lock()
	b, ok := c.listenRoutes[m.RouteKey]
	var (
		cb ListenCallBack
		q  *listenQueue
	)
	if ok {
		cb, q = b.cb, b.queue
	}
	c.mu.Unlock()
	if !ok {
		return
	}
	c.dispatchListen(m, cb, q)
}

// --- 缓存 listen：容量 1 等价单槽 ---

// TestConnection_CachedListen_Capacity1Equivalence 验证：
// 注册 nil 回调（走 dispatchListen 的 else 分支）→ push 2 条 →
// GetListenResp 返回最新 1 条 → 再 GetListenResp 返回 nil。
// 此即「容量 1 环形队列与旧单槽逐字节等价」的行为级断言。
func TestConnection_CachedListen_Capacity1Equivalence(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	const routeKey = "S2C.Push"
	// 注册 nil 回调：进入 dispatchListen 的 else（缓存）分支。
	if err := conn.RegisterListen(routeKey, nil, 1); err != nil {
		t.Fatalf("RegisterListen 失败: %v", err)
	}

	first := NewMessage(routeKey, []byte{'A'}, 0, 1, MessageTiming{})
	second := NewMessage(routeKey, []byte{'B'}, 0, 1, MessageTiming{})

	// push 第一条。
	dispatchListenForTest(conn, first)
	// push 第二条：容量 1 等价单槽覆盖语义，应覆盖第一条。
	// 默认容量 1 时此条会触发 Debug 级「监听队列已满」日志，属预期。
	dispatchListenForTest(conn, second)

	got := conn.GetListenResp(routeKey)
	if got == nil {
		t.Fatal("GetListenResp 返回 nil，期望返回最新一条")
	}
	if len(got.Data) != 1 || got.Data[0] != 'B' {
		t.Fatalf("GetListenResp.Data = %v，期望 ['B']（最新覆盖单槽）", got.Data)
	}

	// 容量 1：取出后应清空，再 GetListenResp 返回 nil。
	if again := conn.GetListenResp(routeKey); again != nil {
		t.Fatalf("容量 1 取出后再 GetListenResp 应为 nil，实际 %v", again)
	}
}

// TestConnection_CachedListen_NoListener 验证：未注册任何监听时，dispatchListen 直接丢弃。
func TestConnection_CachedListen_NoListener(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	dispatchListenForTest(conn, NewMessage("S2C.Unregistered", []byte{'X'}, 0, 1, MessageTiming{}))

	if got := conn.GetListenResp("S2C.Unregistered"); got != nil {
		t.Fatalf("未注册监听不应缓存消息，实际得到 %v", got)
	}
}

// TestConnection_CachedListen_NonNilCallback 验证：注册非 nil 回调时不走缓存，
// 由回调直接消费，GetListenResp 无缓存可取。
func TestConnection_CachedListen_NonNilCallback(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	const routeKey = "S2C.Cb"
	received := make(chan *Message, 4)
	cb := func(m *Message) {
		received <- m
	}
	if err := conn.RegisterListen(routeKey, cb, 1); err != nil {
		t.Fatalf("RegisterListen 失败: %v", err)
	}

	dispatchListenForTest(conn, NewMessage(routeKey, []byte{'A'}, 0, 1, MessageTiming{}))
	dispatchListenForTest(conn, NewMessage(routeKey, []byte{'B'}, 0, 1, MessageTiming{}))

	// 回调收到全部 2 条（不进缓存）。
	for i, want := range []byte{'A', 'B'} {
		select {
		case m := <-received:
			if len(m.Data) != 1 || m.Data[0] != want {
				t.Fatalf("回调第 %d 条 = %v，want %q", i, m.Data, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("回调第 %d 条未到达", i)
		}
	}
	// 非缓存模式：GetListenResp 应返回 nil。
	if got := conn.GetListenResp(routeKey); got != nil {
		t.Fatalf("非 nil 回调路径下 GetListenResp 应为 nil，实际 %v", got)
	}
}

// TestConnection_CachedListen_MultipleRoutes 验证：多个 routeKey 各自独立缓存。
func TestConnection_CachedListen_MultipleRoutes(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	if err := conn.RegisterListen("route.X", nil, 1); err != nil {
		t.Fatalf("RegisterListen route.X 失败: %v", err)
	}
	if err := conn.RegisterListen("route.Y", nil, 1); err != nil {
		t.Fatalf("RegisterListen route.Y 失败: %v", err)
	}

	dispatchListenForTest(conn, NewMessage("route.X", []byte{'x'}, 0, 1, MessageTiming{}))
	dispatchListenForTest(conn, NewMessage("route.Y", []byte{'y'}, 0, 1, MessageTiming{}))

	if m := conn.GetListenResp("route.X"); m == nil || m.Data[0] != 'x' {
		t.Fatalf("route.X GetListenResp = %v，want 'x'", m)
	}
	if m := conn.GetListenResp("route.Y"); m == nil || m.Data[0] != 'y' {
		t.Fatalf("route.Y GetListenResp = %v，want 'y'", m)
	}
	// 互不影响。
	if m := conn.GetListenResp("route.X"); m != nil {
		t.Fatalf("route.X 取出后再取应 nil，实际 %v", m)
	}
}

// TestConnection_GetListenResp_Closed 验证：连接关闭后 GetListenResp 返回 nil。
func TestConnection_GetListenResp_Closed(t *testing.T) {
	conn := newTestConnection(t)
	if err := conn.RegisterListen("k", nil, 1); err != nil {
		t.Fatalf("RegisterListen 失败: %v", err)
	}
	dispatchListenForTest(conn, NewMessage("k", []byte{'a'}, 0, 1, MessageTiming{}))

	conn.Close()

	if got := conn.GetListenResp("k"); got != nil {
		t.Fatalf("Close 后 GetListenResp 应 nil，实际 %v", got)
	}
}

// TestConnection_GetListenResp_NilReceiver 验证：nil receiver 安全返回 nil。
func TestConnection_GetListenResp_NilReceiver(t *testing.T) {
	var conn *Connection
	if got := conn.GetListenResp("k"); got != nil {
		t.Fatalf("nil receiver GetListenResp 应 nil，实际 %v", got)
	}
}

// --- RegisterListen（2-A2.1 接线）---

// TestConnection_RegisterListen_PrecreatesQueue 验证：注册时按 queueSize 预创建队列。
// 注册 routeKey 容量 3 → dispatchListen 连推 3 条 → GetListenResp FIFO 出队 A/B/C → 第 4 次 nil。
func TestConnection_RegisterListen_PrecreatesQueue(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	const routeKey = "S2C.Push3"
	if err := conn.RegisterListen(routeKey, nil, 3); err != nil {
		t.Fatalf("RegisterListen(queueSize=3) 失败: %v", err)
	}

	// 容量 3：push 3 条都进队列（不丢）。
	dispatchListenForTest(conn, NewMessage(routeKey, []byte{'A'}, 0, 1, MessageTiming{}))
	dispatchListenForTest(conn, NewMessage(routeKey, []byte{'B'}, 0, 1, MessageTiming{}))
	dispatchListenForTest(conn, NewMessage(routeKey, []byte{'C'}, 0, 1, MessageTiming{}))

	for i, want := range []byte{'A', 'B', 'C'} {
		m := conn.GetListenResp(routeKey)
		if m == nil {
			t.Fatalf("第 %d 次 GetListenResp 返回 nil", i+1)
		}
		if len(m.Data) != 1 || m.Data[0] != want {
			t.Fatalf("第 %d 次 GetListenResp.Data = %v，want %q", i+1, m.Data, want)
		}
	}
	if m := conn.GetListenResp(routeKey); m != nil {
		t.Fatalf("容量 3 取 3 次后再取应 nil，实际 %v", m)
	}
}

// TestConnection_RegisterListen_DefaultQueueSizeEquivalent 验证：
// queueSize=1（缺省）走 RegisterListen 入口，与 2-A1 容量 1 等价语义一致。
func TestConnection_RegisterListen_DefaultQueueSizeEquivalent(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	const routeKey = "S2C.Default"
	if err := conn.RegisterListen(routeKey, nil, 1); err != nil {
		t.Fatalf("RegisterListen(queueSize=1) 失败: %v", err)
	}
	dispatchListenForTest(conn, NewMessage(routeKey, []byte{'A'}, 0, 1, MessageTiming{}))
	dispatchListenForTest(conn, NewMessage(routeKey, []byte{'B'}, 0, 1, MessageTiming{}))

	m := conn.GetListenResp(routeKey)
	if m == nil || len(m.Data) != 1 || m.Data[0] != 'B' {
		t.Fatalf("容量 1 等价单槽：GetListenResp = %v，want ['B']", m)
	}
	if again := conn.GetListenResp(routeKey); again != nil {
		t.Fatalf("容量 1 取出后再取应 nil，实际 %v", again)
	}
}

// TestConnection_RegisterListen_Idempotent 验证：同 routeKey 同 (cb-nil, queueSize) 再注册
// 是幂等 no-op（nil error，不重复建队列，不丢失已缓存消息）。
func TestConnection_RegisterListen_Idempotent(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	const routeKey = "S2C.Idem"
	if err := conn.RegisterListen(routeKey, nil, 3); err != nil {
		t.Fatalf("首次 RegisterListen 失败: %v", err)
	}
	// 先 push 一条，验证重复注册后队列不被清空。
	dispatchListenForTest(conn, NewMessage(routeKey, []byte{'A'}, 0, 1, MessageTiming{}))

	// 同参数再注册一次：幂等。
	if err := conn.RegisterListen(routeKey, nil, 3); err != nil {
		t.Fatalf("幂等 RegisterListen 返回 error: %v", err)
	}

	// 重复注册不应清空已缓存消息。
	m := conn.GetListenResp(routeKey)
	if m == nil || len(m.Data) != 1 || m.Data[0] != 'A' {
		t.Fatalf("幂等注册后已缓存消息丢失：GetListenResp = %v，want ['A']", m)
	}
}

// TestConnection_RegisterListen_ConflictQueueSize 验证：同 routeKey 不同 queueSize → error。
func TestConnection_RegisterListen_ConflictQueueSize(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	const routeKey = "S2C.Conf"
	if err := conn.RegisterListen(routeKey, nil, 3); err != nil {
		t.Fatalf("首次 RegisterListen(3) 失败: %v", err)
	}
	err := conn.RegisterListen(routeKey, nil, 5)
	if err == nil {
		t.Fatal("queueSize 不一致的重复注册应返回 error，实际 nil")
	}
}

// TestConnection_RegisterListen_ConflictMode 验证：
// 同 routeKey 一 nil-cb（缓存模式）一非 nil-cb（回调模式）→ error。
func TestConnection_RegisterListen_ConflictMode(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	const routeKey = "S2C.Mode"
	if err := conn.RegisterListen(routeKey, nil, 1); err != nil {
		t.Fatalf("首次 RegisterListen(nil-cb) 失败: %v", err)
	}
	cb := func(m *Message) {}
	err := conn.RegisterListen(routeKey, cb, 1)
	if err == nil {
		t.Fatal("cb 模式不一致的重复注册应返回 error，实际 nil")
	}
}

// TestConnection_RegisterListen_ModeConsistentIdempotent 验证：
// 同 routeKey 同为非 nil-cb + 同 queueSize → 幂等 nil error。
func TestConnection_RegisterListen_ModeConsistentIdempotent(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	const routeKey = "S2C.CbIdem"
	cb1 := func(m *Message) {}
	if err := conn.RegisterListen(routeKey, cb1, 1); err != nil {
		t.Fatalf("首次 RegisterListen(cb) 失败: %v", err)
	}
	cb2 := func(m *Message) {}
	if err := conn.RegisterListen(routeKey, cb2, 1); err != nil {
		t.Fatalf("同模式幂等注册应 nil error，实际 %v", err)
	}
}

// TestConnection_RegisterListen_NilReceiver 验证：nil receiver 安全返回 error。
func TestConnection_RegisterListen_NilReceiver(t *testing.T) {
	var conn *Connection
	if err := conn.RegisterListen("k", nil, 1); err == nil {
		t.Fatal("nil receiver RegisterListen 应返回 error，实际 nil")
	}
}

// TestConnection_RegisterListen_Closed 验证：已关闭连接注册返回 error。
func TestConnection_RegisterListen_Closed(t *testing.T) {
	conn := newTestConnection(t)
	conn.Close()
	if err := conn.RegisterListen("k", nil, 1); err == nil {
		t.Fatal("已关闭连接 RegisterListen 应返回 error，实际 nil")
	}
}

// --- OnReceive 路由分派（回调/队列合表后）---

// TestConnection_OnReceive_ResponseMapWinsOverListen 验证：同一 routeKey 既有等待中的请求
// 又有监听绑定时，消息投给请求通道（一发一收优先），不进监听队列。
func TestConnection_OnReceive_ResponseMapWinsOverListen(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	const routeKey = "S2C.Both"
	if err := conn.RegisterListen(routeKey, nil, 1); err != nil {
		t.Fatalf("RegisterListen 失败: %v", err)
	}
	ch := make(chan *Message, 1)
	conn.mu.Lock()
	conn.responseMap[routeKey] = ch
	conn.mu.Unlock()

	conn.OnReceive(routeKey, []byte{'R'}, 0, 1, MessageTiming{})

	select {
	case m := <-ch:
		if len(m.Data) != 1 || m.Data[0] != 'R' {
			t.Fatalf("请求通道收到 %v，want ['R']", m.Data)
		}
	default:
		t.Fatal("请求通道未收到消息（分派优先级被破坏）")
	}
	if m := conn.GetListenResp(routeKey); m != nil {
		t.Fatalf("命中请求通道时不应同时进监听队列，实际 %v", m)
	}
}

// TestConnection_OnReceive_ListenCallbackAndQueue 验证：OnReceive 在一次查找里取齐
// 回调与队列——回调模式直接调 cb，缓存模式进队列。
func TestConnection_OnReceive_ListenCallbackAndQueue(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	got := make(chan *Message, 2)
	if err := conn.RegisterListen("S2C.Cb2", func(m *Message) { got <- m }, 1); err != nil {
		t.Fatalf("RegisterListen(cb) 失败: %v", err)
	}
	if err := conn.RegisterListen("S2C.Q2", nil, 2); err != nil {
		t.Fatalf("RegisterListen(queue) 失败: %v", err)
	}

	conn.OnReceive("S2C.Cb2", []byte{'c'}, 0, 1, MessageTiming{})
	conn.OnReceive("S2C.Q2", []byte{'q'}, 0, 1, MessageTiming{})

	select {
	case m := <-got:
		if len(m.Data) != 1 || m.Data[0] != 'c' {
			t.Fatalf("回调收到 %v，want ['c']", m.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("回调模式未收到消息")
	}
	if m := conn.GetListenResp("S2C.Q2"); m == nil || m.Data[0] != 'q' {
		t.Fatalf("缓存模式 GetListenResp = %v，want ['q']", m)
	}
	// 回调模式不进队列。
	if m := conn.GetListenResp("S2C.Cb2"); m != nil {
		t.Fatalf("回调模式不应进队列，实际 %v", m)
	}
}

// TestConnection_OnReceive_UnmatchedRoute 验证：既无请求等待也无监听绑定的路由被安静丢弃
// （不 panic、不误入任何队列）。
func TestConnection_OnReceive_UnmatchedRoute(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	if err := conn.RegisterListen("S2C.Other", nil, 1); err != nil {
		t.Fatalf("RegisterListen 失败: %v", err)
	}
	conn.OnReceive("S2C.Nobody", []byte{'n'}, 0, 1, MessageTiming{})

	if m := conn.GetListenResp("S2C.Other"); m != nil {
		t.Fatalf("无人认领的消息串到了别的路由：%v", m)
	}
}

// TestConnection_OnReceive_CallbackReplacedByReregister 验证：幂等重注册会把绑定上的回调
// 换成最新的一个，之后 OnReceive 分派到新回调（绑定内 cb 的读写都在 c.mu 下，无竞态）。
func TestConnection_OnReceive_CallbackReplacedByReregister(t *testing.T) {
	conn := newTestConnection(t)
	defer conn.Close()

	const routeKey = "S2C.Replace"
	oldHits := make(chan *Message, 1)
	newHits := make(chan *Message, 1)
	if err := conn.RegisterListen(routeKey, func(m *Message) { oldHits <- m }, 1); err != nil {
		t.Fatalf("首次 RegisterListen 失败: %v", err)
	}
	if err := conn.RegisterListen(routeKey, func(m *Message) { newHits <- m }, 1); err != nil {
		t.Fatalf("幂等重注册失败: %v", err)
	}

	conn.OnReceive(routeKey, []byte{'z'}, 0, 1, MessageTiming{})

	select {
	case <-newHits:
	case <-oldHits:
		t.Fatal("重注册后仍分派到旧回调")
	case <-time.After(time.Second):
		t.Fatal("重注册后没有任何回调被调用")
	}
}
