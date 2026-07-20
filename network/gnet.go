package network

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"stressbot/adapter"
	"stressbot/monitor"
	stresslog "stressbot/utils/log"

	"github.com/panjf2000/gnet/v2"
	"go.uber.org/zap"
)

// maxBodyLen 单个包体最大允许长度（16MB），防止畸形/恶意包导致 OOM。
const maxBodyLen = 16 * 1024 * 1024

const (
	gnetReadBufferCap  = 32 * 1024 // gnet 读缓冲区容量
	gnetWriteBufferCap = 32 * 1024 // gnet 写缓冲区容量
	maxConcurrentDials = 512       // 限制同一 Agent 内同时阻塞在 gnet Dial/Enroll 的连接数
)

// connRegistry 管理 gnet 连接与业务层 Connection 的映射。
type connRegistry struct {
	mu      sync.RWMutex
	connMap map[int]*Connection
}

func newConnRegistry() *connRegistry {
	return &connRegistry{
		connMap: make(map[int]*Connection),
	}
}

func (r *connRegistry) register(gconn gnet.Conn, conn *Connection) {
	r.mu.Lock()
	r.connMap[gconn.Fd()] = conn
	r.mu.Unlock()
}

func (r *connRegistry) unregister(gconn gnet.Conn) {
	r.mu.Lock()
	delete(r.connMap, gconn.Fd())
	r.mu.Unlock()
}

func (r *connRegistry) get(gconn gnet.Conn) *Connection {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.connMap[gconn.Fd()]
}

// EventServer gnet 事件处理器。
//
// adp 是 OnTraffic 热路径的元信息源：
//   - HeaderSize / BodyLength（纯 Go 缓存字段，零 Lua 调用）
//   - decode 使用 DialTCP/DialUDP 注入到 Connection 的 Go SchemaAdapter
type EventServer struct {
	gnet.BuiltinEventEngine

	registry     *connRegistry
	adp          adapter.Adapter // 仅用于 OnTraffic 的帧切割元信息（HeaderSize/BodyLength）
	tickInterval time.Duration
}

// Compile-time assertion: EventServer satisfies gnet.EventHandler.
var _ gnet.EventHandler = (*EventServer)(nil)

// NewEventServer 创建 gnet 事件处理器
func NewEventServer(adp adapter.Adapter, heartbeatInterval time.Duration) *EventServer {
	return &EventServer{
		registry:     newConnRegistry(),
		adp:          adp,
		tickInterval: heartbeatInterval,
	}
}

// dialPayload 由 DialContext 透传给 OnOpen 的连接上下文：携带绑定业务层 Connection 所需信息。
// gnet 保证同一连接的 OnOpen 在任何 OnTraffic 之前处理，且 EnrollContext 会等 OnOpen 完成后
// 才从 DialContext 返回，因此在 OnOpen 内完成 bind/register/StartPump 可彻底消除
// "Dial 返回后、注册前首包到达被丢弃" 的竞态（H5）。
type dialPayload struct {
	conn  *Connection
	adp   adapter.Adapter
	isUDP bool
}

// OnOpen gnet 连接建立回调。
//
// 在此完成 bind + 注册 + StartPump：gnet 事件循环串行处理 openConn 与后续 packConn（traffic），
// openConn 一定先于 traffic，故连接在首个 OnTraffic 触发前即已注册且 pump 就绪，首包不再被丢弃。
func (es *EventServer) OnOpen(gconn gnet.Conn) ([]byte, gnet.Action) {
	payload, ok := gconn.Context().(*dialPayload)
	if !ok || payload == nil || payload.conn == nil {
		// 非本 Dialer 经 DialContext 建立（无绑定信息）：无法关联业务 Connection，保持旧的空行为。
		return nil, gnet.None
	}
	bindConn(gconn, payload.conn)
	es.registry.register(gconn, payload.conn)
	// StartPump 用 CAS 幂等 + 经协程池异步起 pump goroutine，从事件循环 goroutine 调用安全非阻塞。
	if err := payload.conn.StartPump(payload.adp, payload.isUDP); err != nil {
		stresslog.Error("[GNET] 启动连接 pump 失败，关闭连接",
			zap.String("service", payload.conn.ServiceName()),
			zap.String("robot", payload.conn.robotName),
			zap.Error(err))
		return nil, gnet.Close
	}
	return nil, gnet.None
}

// OnClose gnet 连接关闭回调。
//
// 把 err 字符串归一化为简短 reason 传给 Connection.onClose，让 inflight RequestResponse
// 命中 ctx.Done() 时能拼到错误 detail（例如 "cause=EOF" / "cause=RST(forcibly closed)"）。
// reason="" 表示无错误的正常关闭（服务端调 close 但底层没报 error，少见）。
func (es *EventServer) OnClose(gconn gnet.Conn, err error) gnet.Action {
	conn := es.registry.get(gconn)
	if conn != nil {
		es.registry.unregister(gconn)
		reason := closeReasonFromErr(err)
		// 日志分级：
		//   - 无 err / EOF / local-close：服务端正常 FIN 或本地主动关闭，info 级别
		//     （短战斗场景每次 BattleEnd 都会触发服务端 EOF，warn 级别每分钟刷 100+ 条噪音）
		//   - RST / broken-pipe / timeout / 其他：真正的异常断开，仍然 warn 级别
		switch reason {
		case "", "EOF", "local-close":
			stresslog.Debug("[GNET] 连接关闭",
				zap.String("service", conn.ServiceName()),
				zap.String("robot", conn.robotName),
				zap.String("reason", reason))
		default:
			stresslog.Warn("[GNET] 连接异常关闭",
				zap.String("service", conn.ServiceName()),
				zap.String("robot", conn.robotName),
				zap.String("reason", reason),
				zap.Error(err))
		}
		conn.onClose(reason)
	}
	return gnet.None
}

// closeReasonFromErr 把 gnet 给的 close error 归一化为短标签字符串。
//
// 设计：保留辨识度但去除冗长的地址/系统调用名，让前端面板能直接展示；
// 同一类断开归一为同一个标签便于按 code 单维聚合，并避免每个连接的
// IP:port 撑出唯一 detail。
func closeReasonFromErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	switch {
	case s == "EOF":
		return "EOF"
	case strings.Contains(s, "forcibly closed"), strings.Contains(s, "connection reset"):
		return "RST"
	case strings.Contains(s, "broken pipe"):
		return "broken-pipe"
	case strings.Contains(s, "use of closed network connection"):
		return "local-close"
	case strings.Contains(s, "timeout"), strings.Contains(s, "deadline"):
		return "timeout"
	default:
		// 兜底：保留原错误但截断防止 detail 过长污染聚合维度
		if len(s) > 64 {
			s = s[:64]
		}
		return s
	}
}

// OnTraffic gnet 收到数据回调。
//
// 设计原则：此函数运行在 gnet 事件循环 goroutine 上，**严禁同步阻塞**
// （阻塞会让该 loop 上所有连接的 I/O 一起冻结，是 CONN_DROPPED 雪崩的源头）。
//
// 因此只做两件纯 Go 操作：
//  1. 用 adapter 缓存的元信息做帧分割（HeaderSize / BodyLength，零 Lua 调用）
//  2. 把 raw msgBuf 投递到 connection 的 inboundCh，由 per-connection 的 connectionPump
//     goroutine 异步完成 decode + request-response/listen 分发 + 心跳
//
// msgBuf 从 sync.Pool 获取，pump 处理完（decodeAndDispatch 的 defer putMsgBuf）归还，
// 避免高频 alloc 触发 GC 抖动。
func (es *EventServer) OnTraffic(gconn gnet.Conn) (action gnet.Action) {
	headSize := es.adp.HeaderSize()

	conn := es.registry.get(gconn)

	for {
		available := gconn.InboundBuffered()
		if available < headSize {
			return gnet.None
		}

		headBuf, err := gconn.Peek(headSize)
		if err != nil || len(headBuf) < headSize {
			return gnet.None
		}

		bodyLen := es.adp.BodyLength(headBuf)
		if bodyLen < 0 || bodyLen > maxBodyLen {
			serviceName := ""
			robotName := ""
			if conn != nil {
				serviceName = conn.ServiceName()
				robotName = conn.robotName
			}
			stresslog.Warn("[NETWORK] 协议头非法或包体过长，关闭连接",
				zap.String("service", serviceName),
				zap.String("robot", robotName),
				zap.Int("fd", gconn.Fd()),
				zap.Int("headSize", headSize),
				zap.Int("available", available),
				zap.Int("bodyLen", bodyLen))
			return gnet.Close
		}

		totalLen := headSize + bodyLen
		if available < totalLen {
			return gnet.None
		}

		recvFrameAt := time.Now()

		msgBuf := getMsgBuf(totalLen)
		if _, err = gconn.Read(msgBuf); err != nil {
			putMsgBuf(msgBuf)
			serviceName := ""
			robotName := ""
			if conn != nil {
				serviceName = conn.ServiceName()
				robotName = conn.robotName
			}
			stresslog.Error("[GNET] 读取消息失败",
				zap.String("service", serviceName),
				zap.String("robot", robotName),
				zap.Int("fd", gconn.Fd()),
				zap.Int("headSize", headSize),
				zap.Int("bodyLen", bodyLen),
				zap.Int("totalLen", totalLen),
				zap.Error(err))
			return gnet.None
		}
		// 全局带宽统计：所有真实入站字节都计入（含心跳应答、监听推送、未匹配响应等）。
		// 与 connection.Send 的出站统计配对，monitor 拿到的是"网卡级"双向流量。
		monitor.Global().AddBandwidth(0, int64(totalLen))

		if conn == nil {
			putMsgBuf(msgBuf)
			// 降到 debug：close 后还有 inbound 字节在路上是 TCP 半关闭/对端 FIN
			// 之前正常排队的帧。registry.unregister 已在 OnClose 同步发生，但
			// gnet 事件循环上之前累积的 buffered 数据仍会触发本回调，是预期行为。
			// 历史一次 4 分钟任务可刷出 6500+ 条 warn 噪音，掩盖真正的问题。
			stresslog.Debug("[NETWORK] 收到消息但连接未注册，消息被丢弃",
				zap.Int("fd", gconn.Fd()), zap.Int("bodyLen", totalLen))
			continue
		}

		switch conn.EnqueueRaw(msgBuf, recvFrameAt) {
		case EnqueueOK:
			// 入队成功，msgBuf 由 connectionPump 在 decodeAndDispatch 处理后归还
		case EnqueueClosed:
			// 连接已关闭或还没启动 pump：这是正常现象（任务停止 / battle_end close_* /
			// 服务端 EOF 后 inbound 字节仍在路上）。归还 buffer 即可，不重复关闭、不报警。
			putMsgBuf(msgBuf)
			stresslog.Debug("[NETWORK] 连接已关闭，丢弃后续 inbound 帧",
				zap.String("service", conn.ServiceName()),
				zap.String("robot", conn.robotName),
				zap.Int("bodyLen", totalLen))
		case EnqueueChFull:
			// inboundCh 真满 = pump decode 严重落后（codec 慢或对端发包速率超出处理能力）。
			// 关闭这条连接释放资源，避免持续累积导致整体雪崩。
			putMsgBuf(msgBuf)
			stresslog.Warn("[NETWORK] inbound 通道已满，关闭连接以释放压力",
				zap.String("service", conn.ServiceName()),
				zap.String("robot", conn.robotName),
				zap.Int("fd", gconn.Fd()),
				zap.Int("bodyLen", bodyLen),
				zap.Int("totalLen", totalLen))
			return gnet.Close
		}
	}
}

// OnTick gnet 定时回调。
func (es *EventServer) OnTick() (delay time.Duration, action gnet.Action) {
	return es.tickInterval, gnet.None
}

// bindConn 将 gnet 连接的发送/关闭函数注入到业务层 Connection。
func bindConn(gconn gnet.Conn, conn *Connection) {
	conn.sendFunc = func(data []byte) error {
		return gconn.AsyncWrite(data, nil)
	}
	conn.closeFunc = func() error {
		return gconn.Close()
	}
}

// Dialer 管理 gnet 客户端。
type Dialer struct {
	client     *gnet.Client
	server     *EventServer
	dialTokens chan struct{}
	closed     atomic.Bool
}

// NewDialer 创建拨号器
func NewDialer(adp adapter.Adapter, heartbeatInterval time.Duration) *Dialer {
	server := NewEventServer(adp, heartbeatInterval)
	return &Dialer{
		server:     server,
		dialTokens: make(chan struct{}, maxConcurrentDials),
	}
}

// Start 启动 gnet 客户端引擎。
//
// 多 eventloop 配置（关键性能项）：
//   - 不显式设置时 gnet 默认 numEventLoop=1（client_windows.go: determineEventLoops）。
//     单 loop 意味着所有连接的 OnTraffic/OnOpen/OnClose 全部串行在一个 goroutine 上跑，
//     当瞬时建连密度高（如 250 个 robot 同时进入"建连 logic"步骤）时，单 loop
//     的 1024 容量 channel 容易堆积，导致 PlayerLogin 等首包反应延迟，
//     落入服务端"建连后 N 秒未鉴权"窗口被 RST。
//   - `WithMulticore(true) + WithNumEventLoop(NumCPU)` 让 gnet 创建 N 个 eventloop，
//     用 leastConnectionsLoadBalancer 把 conn 分散到不同 loop，
//     不同连接的回调真正并发处理。
//   - 并发安全性已审计（见 network/gnet.go 顶部注释）：
//     EventServer 自身无可变共享状态；registry 用 RWMutex；
//     monitor 计数器用 atomic；msgBuf 用 sync.Pool；
//     同一连接由 gnet LB 固定绑定到一个 loop（不会跨 loop 并发），
//     所以多 loop 切换是安全的。
//   - `WithLockOSThread(true)` 与旧工具对齐，把每个 eventloop 绑定到独立的 OS 线程，
//     Windows 上能减少 Go scheduler 把 loop goroutine 切到不同 OS 线程时的 syscall 抖动。
func (d *Dialer) Start() error {
	opts := []gnet.Option{
		gnet.WithTicker(true),
		gnet.WithReadBufferCap(gnetReadBufferCap),
		gnet.WithWriteBufferCap(gnetWriteBufferCap),
		gnet.WithMulticore(true),
		gnet.WithNumEventLoop(runtime.NumCPU()),
		gnet.WithLockOSThread(true),
	}
	client, err := gnet.NewClient(d.server, opts...)
	if err != nil {
		return fmt.Errorf("创建 gnet 客户端失败: %w", err)
	}
	d.client = client

	if err = d.client.Start(); err != nil {
		return fmt.Errorf("启动 gnet 客户端失败: %w", err)
	}

	stresslog.Info("[GNET] 客户端引擎已启动",
		zap.Int("numEventLoop", runtime.NumCPU()))
	return nil
}

// Stop 停止 gnet 客户端引擎
func (d *Dialer) Stop() error {
	d.closed.Store(true)
	if d.client == nil {
		return nil
	}
	if err := d.client.Stop(); err != nil {
		return fmt.Errorf("停止 gnet 客户端失败: %w", err)
	}
	stresslog.Info("[GNET] 客户端引擎已停止")
	return nil
}

// DialTCP 建立 TCP 连接并绑定业务层 Connection。
// ctx 用于超时/取消：如果 ctx 在拨号完成前被取消，返回 context 错误。
//
// adp 是 connectionPump 使用的协议适配器（T2-C1 起由 Robot 在拨号前通过 CodecResolver
// 按 server 串解析后传入的 Go SchemaAdapter）。OnTraffic 走的是 d.server.adp（全局
// 元信息源，仅 HeaderSize/BodyLength 帧切割，纯 Go）；decode 走连接固定 adp。
//
// adp 必须非 nil：上层 Robot.ConnectTCP 已做 Resolve(nil → fail loud)，dial 内仅保留
// 防御性 nil-guard（adp==nil 视为编程错误，直接返回错误，不再回退 d.server.adp 兜底）。
func (d *Dialer) DialTCP(ctx context.Context, address string, conn *Connection, adp adapter.Adapter) (gnet.Conn, error) {
	return d.dial(ctx, "tcp", address, conn, adp)
}

// DialUDP 建立 UDP 连接并绑定业务层 Connection。
// ctx 用于超时/取消：任务停止时不再进入新的 UDP 拨号。
//
// adp 语义同 DialTCP：上层 Robot.ConnectUDP 已 Resolve 非 nil 注入。
func (d *Dialer) DialUDP(ctx context.Context, address string, conn *Connection, adp adapter.Adapter) (gnet.Conn, error) {
	return d.dial(ctx, "udp", address, conn, adp)
}

func (d *Dialer) dial(ctx context.Context, network, address string, conn *Connection, adp adapter.Adapter) (gnet.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d.closed.Load() {
		return nil, context.Canceled
	}
	select {
	case d.dialTokens <- struct{}{}:
		defer func() { <-d.dialTokens }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d.closed.Load() {
		return nil, context.Canceled
	}
	// T2-C1：删除 `if adp == nil { adp = d.server.adp }` 兜底。
	// adp 由上层 Robot.ConnectTCP/UDP 在拨号前经 CodecResolver.Resolve 注入（nil → 已 fail loud）。
	// 此处仅保留防御性 nil-guard：adp==nil 视为编程错误，直接报错而非静默回退默认 codec。
	if adp == nil {
		serviceName := ""
		if conn != nil {
			serviceName = conn.ServiceName()
		}
		return nil, fmt.Errorf("%s 拨号失败 %s：adapter 为 nil（上层应通过 CodecResolver 注入，network=%s service=%q）",
			strings.ToUpper(network), address, network, serviceName)
	}

	// gnet.Client.DialContext 在 EnrollContext 阶段会等待 eventloop 处理完 OnOpen 才返回。
	// 通过 ctx 透传 dialPayload：bind + register + StartPump 全部在 OnOpen 内完成，
	// gnet 保证 OnOpen 先于该连接任何 OnTraffic，从而消除"注册前首包被丢弃"的竞态（H5）。
	// 任务停止后先通过 ctx/closed 快速拒绝新拨号，并用 dialTokens 限制同一时刻卡在 Dial 内的
	// goroutine 数，避免一次停止留下成千上万条拨号 goroutine 持有 Lua action 与 Robot 资源。
	gconn, err := d.client.DialContext(network, address, &dialPayload{
		conn:  conn,
		adp:   adp,
		isUDP: network == "udp",
	})
	if err != nil {
		return nil, fmt.Errorf("%s 拨号失败 %s: %w", strings.ToUpper(network), address, err)
	}
	// OnOpen 已完成 bind/register/StartPump。若此刻发现已取消/关闭，则主动关闭连接：
	// OnClose 会 unregister 并触发 Connection.onClose 停 pump，资源随之回收。
	if err := ctx.Err(); err != nil {
		_ = gconn.Close()
		return nil, err
	}
	if d.closed.Load() {
		_ = gconn.Close()
		return nil, context.Canceled
	}

	if stresslog.DebugEnabled() {
		stresslog.Debug("[GNET] 连接已建立",
			zap.String("network", network),
			zap.String("address", address), zap.String("service", conn.serviceName), zap.String("robot", conn.robotName))
	}
	return gconn, nil
}
