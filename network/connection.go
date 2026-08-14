package network

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"stressbot/engine"
	"stressbot/errcode"
	"stressbot/internal/stresslog"
	"stressbot/internal/timerpool"
	"stressbot/internal/workpool"
	"stressbot/monitor"
	"stressbot/protocol"

	"go.uber.org/zap"
)

// ListenCallBack 持久化推送消息的回调函数类型
type ListenCallBack func(message *Message)

// pumpInboundBatchSize 是 connectionPump 单次内层循环最多连续处理的 inbound 帧数。
//
// 两条硬约束之一（02-track §2-C 伪代码）：禁止一直 drain inbound 饿死 heartbeatTimer/controlCh。
// 每处理一条 inbound 后检查计数，达到本上限后强制回外层 select（重新优先检查 heartbeat due +
// 给 controlCh/ctx.Done 机会），从而保证心跳到期不被海量入站 backlog 拖延。
const pumpInboundBatchSize = 16

// controlChSize 是 connectionPump 控制通道缓冲大小。
// 控制消息（注册/停止心跳、stop）频率极低，容量 16 足以吸收 Close + RegisterHeartbeat
// 并发的少量突发，且永远不会成为热路径瓶颈。
const controlChSize = 16

// pumpCmd 是投递到 connectionPump.controlCh 的控制命令。
//
// pump 是连接唯一的 owner goroutine：心跳 timer + cfg 都由 pump 持有并在 pump goroutine
// 内读写，故 RegisterHeartbeat/StopHeartbeat 不再启动独立 goroutine，而是把命令投递给 pump，
// 由 pump 在自己的 select 分支里串行更新 runtime。心跳 builder 是 Go-only，
// 不触碰业务 LState；pump 进一步去掉独立心跳 goroutine。
type pumpCmd struct {
	kind   pumpCmdKind
	hbCfg  HeartbeatConfig // kind == pumpCmdHeartbeat 时有效
	result chan<- error    // 可选：pump 处理完回写结果（nil chan 表示无需回执）
}

type pumpCmdKind int

const (
	pumpCmdHeartbeat     pumpCmdKind = iota // 注册/替换心跳（hbCfg 有效）
	pumpCmdStopHeartbeat                    // 停止心跳（hbCfg 无效）
	pumpCmdStop                             // 主动请求 pump 退出（Close 内部用）
)

// Connection 业务层网络连接封装。
// 封装 request-response 匹配、持久化推送监听、心跳和连接生命周期回调。
//
// 调度模型（2-C3 起）：每条连接只有一个 connectionPump goroutine，统一处理
//   - inbound decode → request-response 通道 / listen queue / store 分发
//   - heartbeat timer 到期 → 调 builder → Send
//   - controlCh 命令 → 注册/停止心跳、stop
//   - ctx.Done → drain inbound buffer、停 timer、关 done
//
// 旧三协程模型（decodeLoop + listenLoop + 独立 runHeartbeat goroutine）已下线，
// 全部并入 pump。pump 是 network 内部调度细节，不泄漏到 flow/engine/Lua。
type Connection struct {
	serviceName string // 所属服务名称（如 "logic"、"battle"）
	robotName   string // 所属机器人账号名
	// secretKey 通信加密密钥，存 []byte。按 immutable 约定使用：SetSecretKey 整体替换为
	// 新副本，GetSecretKey 返回的切片只读、不得修改。避免每个收发包都加锁复制一次密钥。
	secretKey atomic.Value

	responseMap      map[string]chan *Message  // routeKey → 临时响应通道（RequestResponse 用）
	listenRoutes     map[string]*listenBinding // routeKey → 监听绑定（回调 + 缓存队列，注册时定型）
	mu               sync.Mutex                // 保护 responseMap / listenRoutes map 键 + binding.cb / 回调字段（各 listenQueue 自带 mu 串行化 Push/Pop）
	ctx              context.Context           // 连接生命周期上下文
	cancel           context.CancelFunc        // 取消函数，关闭时调用
	isClose          atomic.Int32              // 原子标记：0=活跃，1=已关闭
	intentionalClose atomic.Int32              // 原子标记：1=主动 Close() 触发，不触发 onDisconnect
	requestTimeout   time.Duration             // RequestResponse 默认超时
	sendFunc         sendBackend               // 底层发送函数（由 Dialer 注入）
	closeFunc        func() error              // 底层关闭函数（由 Dialer 注入）
	onDisconnect     func()                    // 意外断开回调（非主动 Close 触发，业务用于停 robot）
	onClosed         func()                    // 关闭回调（主动/被动均触发，监控用，与 ConnEstablished 配对）
	disconnectEvent  lifecycleEvent            // 允许事件先于回调注册发生，且全生命周期只交付一次
	closedEvent      lifecycleEvent            // 主动/被动关闭共享的终态事件

	// connectionPump（每连接一个）替代旧的 decodeLoop + listenLoop + 心跳 goroutine。
	// 详见 connectionPump godoc。pump goroutine 是 inbound decode / listen 分发 / 心跳
	// pump goroutine 独占处理 inbound/control 和心跳 timer；inboundCh/controlCh 由外部投递、pump 消费，
	// pumpDone 由 pump 关闭、外部等待。adp/isUDP 在 StartPump 时一次性注入后只读。
	adp       protocol.Adapter  // decode 用的协议适配器（Go SchemaAdapter），StartPump 时一次性注入
	isUDP     bool              // 该连接是否 UDP（决定调 DecodeUDP / DecodeTCP）
	inboundCh chan inboundFrame // 待解码的 raw msg buffer（OnTraffic 投递→pump 消费）
	controlCh chan pumpCmd      // pump 控制通道（注册/停止心跳、stop）
	pumpDone  chan struct{}     // pump goroutine 退出信号，供 WaitPumpDone/WaitDecodeDone/WaitListenDone 等待
	pumpRun   atomic.Int32      // 原子标记：1 表示 pump 已启动（CAS 防重复启动）
	// hbMu 保护 hb 字段的替换（RegisterHeartbeat/StopHeartbeat 投递 controlCh 前/后读取）。
	// pump goroutine 内部读写 hb 不需要这把锁（pump 是唯一执行者）；这把锁只保护
	// 「pump 外部 goroutine 在投递 controlCh 前快速判断当前是否已注册心跳」这类只读快照。
	hbMu sync.Mutex
	hb   *heartbeatRuntime // pump 持有的心跳 runtime（cfg + timer），nil 表示未注册

	// closeReason 记录连接关闭原因，供 inflight RequestResponse 命中 ctx.Done() 时归因。
	// 由 onClose() 写入（gnet 给的 err 字符串），RequestResponse 读取后拼到错误 detail。
	// atomic.Value 保证读写无锁；写入只发生一次（与 isClose CAS 在同一路径下）。
	closeReason atomic.Value // string

	timingDetail monitor.TimingDetailLevel // 计时细分级别，控制 decode/dispatch 时间点是否记录
}

const (
	inboundChSize = 256 // inbound 通道缓冲区大小，满则反压（关闭连接）
)

type inboundFrame struct {
	Data        []byte
	WireBytes   int
	RecvFrameAt time.Time
}

type lifecycleEvent struct {
	occurred  bool
	delivered bool
}

// listenBinding 一个 routeKey 的监听绑定：回调 + 缓存队列，注册时一次性定型。
//
// 合表动机（收包路由分派）：旧实现把回调与队列分放 listenResp / listenQueues 两张
// map，每条推送要查 3~4 次字符串 map、加 2~3 次 c.mu——OnReceive 查一次 listenResp
// 判断「是不是监听」却丢掉查到的回调，dispatchListen 再加锁重查一次拿回调，缓存模式
// 还要第三次加锁查队列。绑定合成一条后，pump 在 OnReceive 的同一次持锁期内一次查找
// 就取齐回调与队列，分发路径不再回查 map。
//
// 并发约定：
//   - cb 由 c.mu 保护（RegisterListen 幂等重注册会写回最新值，pump 在持锁期内拷出局部变量）；
//   - queue 在 binding 发布进 map 之前创建，之后只读；队列自身的 Push/Pop 由 per-queue mu
//     串行化，故 pump 在释放 c.mu 之后再 Push，与主流程 GetListenResp 的 Pop 无死锁（沿用现状）。
type listenBinding struct {
	cb    ListenCallBack // nil = 缓存模式（消息进 queue，由 GetListenResp 消费）；非 nil = 回调模式
	queue *listenQueue   // 注册时按 queueSize 预创建，恒非 nil
}

// NewConnection 创建新的网络连接。
func NewConnection(serviceName, robotName string, requestTimeout time.Duration, timingDetail monitor.TimingDetailLevel) *Connection {
	conn := &Connection{
		serviceName:    serviceName,
		robotName:      robotName,
		responseMap:    make(map[string]chan *Message),
		listenRoutes:   make(map[string]*listenBinding),
		requestTimeout: requestTimeout,
		sendFunc:       nil,
		timingDetail:   timingDetail,
	}
	conn.ctx, conn.cancel = context.WithCancel(context.Background())
	stresslog.Debug("[NETWORK] NewConnection", zap.String("service", serviceName), zap.String("robot", robotName))
	return conn
}

// ServiceName 返回连接的服务名称
func (c *Connection) ServiceName() string { return c.serviceName }

// SetOnDisconnect 设置连接意外断开回调。
// 仅在非主动 Close 导致的断开时触发（如服务端关闭连接、网络异常）。
func (c *Connection) SetOnDisconnect(fn func()) {
	c.mu.Lock()
	c.onDisconnect = fn
	call := fn != nil && c.disconnectEvent.occurred && !c.disconnectEvent.delivered
	if call {
		c.disconnectEvent.delivered = true
	}
	c.mu.Unlock()
	if call {
		fn()
	}
}

// SetOnClosed 设置连接关闭回调（主动/被动均触发，用于监控计数）。
func (c *Connection) SetOnClosed(fn func()) {
	c.mu.Lock()
	c.onClosed = fn
	call := fn != nil && c.closedEvent.occurred && !c.closedEvent.delivered
	if call {
		c.closedEvent.delivered = true
	}
	c.mu.Unlock()
	if call {
		fn()
	}
}

func (c *Connection) publishDisconnected() {
	c.mu.Lock()
	c.disconnectEvent.occurred = true
	fn := c.onDisconnect
	call := fn != nil && !c.disconnectEvent.delivered
	if call {
		c.disconnectEvent.delivered = true
	}
	c.mu.Unlock()
	if call {
		fn()
	}
}

func (c *Connection) publishClosed() {
	c.mu.Lock()
	c.closedEvent.occurred = true
	fn := c.onClosed
	call := fn != nil && !c.closedEvent.delivered
	if call {
		c.closedEvent.delivered = true
	}
	c.mu.Unlock()
	if call {
		fn()
	}
}

// SetSecretKey 设置通信加密密钥。
// 复制传入的 key 后整体替换（immutable），后续不再修改已存储的切片。
func (c *Connection) SetSecretKey(key []byte) {
	if c == nil || c.isClose.Load() == 1 {
		return
	}
	if len(key) == 0 {
		return
	}
	cp := make([]byte, len(key))
	copy(cp, key)
	c.secretKey.Store(cp)
}

// GetSecretKey 获取通信加密密钥。
// 返回的切片为只读快照，调用方不得修改其内容；生产 codec 只读使用该快照。
func (c *Connection) GetSecretKey() []byte {
	if c == nil {
		return nil
	}
	v := c.secretKey.Load()
	if v == nil {
		return nil
	}
	return v.([]byte)
}

// RequestResponse 发送请求并同步等待响应。
func (c *Connection) RequestResponse(sendData []byte, routeKey string, timeoutOverride ...time.Duration) (*Message, RequestTiming, error) {
	var timing RequestTiming
	if c == nil {
		return nil, timing, errcode.NewActionError(errcode.ErrConnNotFound, "routeKey="+routeKey)
	}
	if c.isClose.Load() == 1 {
		stresslog.Warn("[NETWORK] RequestResponse 连接已关闭", zap.String("service", c.serviceName), zap.String("routeKey", routeKey), zap.String("robot", c.robotName))
		return nil, timing, errcode.NewActionError(errcode.ErrConnClosed, c.serviceName+" routeKey="+routeKey)
	}

	ch := make(chan *Message, 1)
	c.mu.Lock()
	c.responseMap[routeKey] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.responseMap, routeKey)
		c.mu.Unlock()
		close(ch)
	}()

	var stamp writeStamp
	sendStart := time.Now()
	n, sendErr := c.sendTimed(sendData, stamp.mark)
	stamp.enqueuedAt = time.Now()
	timing.SendCost = safeSub(stamp.enqueuedAt, sendStart)
	timing.Observed |= engine.TimingStageSend
	if sendErr != nil {
		stresslog.Error("[NETWORK] RequestResponse 发送失败",
			zap.String("service", c.serviceName), zap.String("routeKey", routeKey),
			zap.String("robot", c.robotName),
			zap.Int("pktLen", len(sendData)),
			zap.Error(sendErr))
		return nil, timing, sendErr
	}
	_ = n

	timeout := c.requestTimeout
	if len(timeoutOverride) > 0 && timeoutOverride[0] > 0 {
		timeout = timeoutOverride[0]
	}
	// 池化 timer 而非 time.After：响应通常在毫秒级到达即提前返回，
	// time.After 的底层 timer 要到 timeout（默认 60s）才回收，高 QPS 下会堆积
	// 数十万个悬挂 timer；池化后连每请求一次的 timer 分配也一并消除。
	timeoutTimer := timerpool.GetTimer(timeout)
	defer timerpool.PutTimer(timeoutTimer)
	select {
	case <-c.ctx.Done():
		// ACK 可能先于 ctx cancel 入队但 select 随机选到了此分支，drain channel。
		select {
		case resp := <-ch:
			actionUnblocked := time.Now()
			timing.WireRTT = safeSub(resp.Timing.RecvFrameAt, stamp.start())
			timing.Observed |= engine.TimingStageRTT
			timing.DecodeWait = safeSub(resp.Timing.DecodeStart, resp.Timing.RecvFrameAt)
			timing.DecodeCost = safeSub(resp.Timing.DecodeEnd, resp.Timing.DecodeStart)
			if monitor.TimingDetailAtLeast(c.timingDetail, monitor.TimingCodecDetail) {
				timing.Observed |= engine.TimingStageDecodeWait | engine.TimingStageDecode
			}
			timing.DispatchToActionWait = safeSub(actionUnblocked, resp.Timing.DispatchStart)
			if monitor.TimingDetailAtLeast(c.timingDetail, monitor.TimingFullDetail) {
				timing.Observed |= engine.TimingStageDispatchWait
			}
			return resp, timing, nil
		default:
		}
		elapsed := safeSub(time.Now(), stamp.enqueuedAt)
		if c.intentionalClose.Load() == 1 {
			stresslog.Debug("[NETWORK] RequestResponse 因本地关闭被取消",
				zap.String("service", c.serviceName), zap.String("routeKey", routeKey),
				zap.String("robot", c.robotName),
				zap.Duration("elapsed", elapsed))
			return nil, timing, errcode.NewActionError(errcode.ErrActionCanceled,
				c.serviceName+" routeKey="+routeKey+" (local close)")
		}
		reason := c.loadCloseReason()
		detail := c.serviceName + " routeKey=" + routeKey
		if reason != "" {
			detail += " cause=" + reason
		}
		stresslog.Warn("[NETWORK] RequestResponse 连接已断开",
			zap.String("service", c.serviceName), zap.String("routeKey", routeKey),
			zap.String("robot", c.robotName),
			zap.String("cause", reason),
			zap.Duration("elapsed", elapsed))
		return nil, timing, errcode.NewActionError(errcode.ErrConnDropped, detail)
	case resp := <-ch:
		actionUnblocked := time.Now()
		timing.WireRTT = safeSub(resp.Timing.RecvFrameAt, stamp.start())
		timing.Observed |= engine.TimingStageRTT
		timing.DecodeWait = safeSub(resp.Timing.DecodeStart, resp.Timing.RecvFrameAt)
		timing.DecodeCost = safeSub(resp.Timing.DecodeEnd, resp.Timing.DecodeStart)
		if monitor.TimingDetailAtLeast(c.timingDetail, monitor.TimingCodecDetail) {
			timing.Observed |= engine.TimingStageDecodeWait | engine.TimingStageDecode
		}
		timing.DispatchToActionWait = safeSub(actionUnblocked, resp.Timing.DispatchStart)
		if monitor.TimingDetailAtLeast(c.timingDetail, monitor.TimingFullDetail) {
			timing.Observed |= engine.TimingStageDispatchWait
		}
		if stresslog.DebugEnabled() {
			stresslog.Debug("[NETWORK] RequestResponse 收到响应",
				zap.String("service", c.serviceName), zap.String("routeKey", routeKey),
				zap.String("robot", c.robotName),
				zap.Int("bodyLen", len(resp.Data)), zap.Duration("wireRTT", timing.WireRTT))
		}
		return resp, timing, nil
	case <-timeoutTimer.C:
		stresslog.Warn("[NETWORK] RequestResponse 等待超时",
			zap.String("service", c.serviceName), zap.String("routeKey", routeKey),
			zap.String("robot", c.robotName),
			zap.Duration("timeout", timeout))
		return nil, timing, errcode.NewActionError(errcode.ErrRecvTimeout, c.serviceName+" routeKey="+routeKey+" timeout="+timeout.String())
	}
}

// PendingRequest 一次异步请求-响应的待响应句柄（协作式 await 用）。
//
// 与 RequestResponse 共用 responseMap + pump 投递通路，但把「发送」与「等待」拆开：
// SendRequest 注册响应通道并立即发出请求；调用方随后自行 select C()（可同时 drain 其他
// 协作式工作），命中后用 Timing 计算耗时；无论命中 / 超时 / 取消都**必须**调 Close 注销通道。
type PendingRequest struct {
	conn      *Connection
	routeKey  string
	ch        chan *Message
	sendCost  time.Duration
	stamp     writeStamp // WireRTT 计时起点：写完成时刻，回调未到则回退入队时刻
	timeout   time.Duration
	closeOnce sync.Once
}

// SendRequest 注册响应通道 + 立即发送请求，返回待响应句柄（不阻塞等待）。
// 发送失败时内部已 Close（不泄漏 responseMap 条目），返回 error。
// timeout<=0 时回退到连接默认超时。
func (c *Connection) SendRequest(sendData []byte, routeKey string, timeout time.Duration) (*PendingRequest, error) {
	if c == nil {
		return nil, errcode.NewActionError(errcode.ErrConnNotFound, "routeKey="+routeKey)
	}
	if c.isClose.Load() == 1 {
		return nil, errcode.NewActionError(errcode.ErrConnClosed, c.serviceName+" routeKey="+routeKey)
	}
	if timeout <= 0 {
		timeout = c.requestTimeout
	}

	ch := make(chan *Message, 1)
	c.mu.Lock()
	c.responseMap[routeKey] = ch
	c.mu.Unlock()

	pr := &PendingRequest{conn: c, routeKey: routeKey, ch: ch, timeout: timeout}

	sendStart := time.Now()
	_, sendErr := c.sendTimed(sendData, pr.stamp.mark)
	pr.stamp.enqueuedAt = time.Now()
	pr.sendCost = safeSub(pr.stamp.enqueuedAt, sendStart)
	if sendErr != nil {
		stresslog.Error("[NETWORK] SendRequest 发送失败",
			zap.String("service", c.serviceName), zap.String("routeKey", routeKey),
			zap.String("robot", c.robotName), zap.Int("pktLen", len(sendData)),
			zap.Duration("timeout", timeout), zap.Error(sendErr))
		pr.Close()
		return nil, sendErr
	}
	return pr, nil
}

// C 返回响应通道，供调用方 select（命中时收到一个 *Message）。
func (pr *PendingRequest) C() <-chan *Message {
	if pr == nil {
		return nil
	}
	return pr.ch
}

// Timeout 返回有效超时。
func (pr *PendingRequest) Timeout() time.Duration {
	if pr == nil {
		return 0
	}
	return pr.timeout
}

// Timing 据发送时刻与响应入站计时点计算 RequestTiming（与 RequestResponse 同口径）。
func (pr *PendingRequest) Timing(resp *Message) RequestTiming {
	var t RequestTiming
	if pr == nil {
		return t
	}
	t.SendCost = pr.sendCost
	t.Observed |= engine.TimingStageSend
	if resp == nil {
		return t
	}
	actionUnblocked := time.Now()
	t.WireRTT = safeSub(resp.Timing.RecvFrameAt, pr.stamp.start())
	t.Observed |= engine.TimingStageRTT
	t.DecodeWait = safeSub(resp.Timing.DecodeStart, resp.Timing.RecvFrameAt)
	t.DecodeCost = safeSub(resp.Timing.DecodeEnd, resp.Timing.DecodeStart)
	if monitor.TimingDetailAtLeast(pr.conn.timingDetail, monitor.TimingCodecDetail) {
		t.Observed |= engine.TimingStageDecodeWait | engine.TimingStageDecode
	}
	t.DispatchToActionWait = safeSub(actionUnblocked, resp.Timing.DispatchStart)
	if monitor.TimingDetailAtLeast(pr.conn.timingDetail, monitor.TimingFullDetail) {
		t.Observed |= engine.TimingStageDispatchWait
	}
	return t
}

// Close 注销 responseMap 条目并关闭通道。并发安全且幂等（sync.Once 保证 close(ch) 仅一次）。
// 完成 / 超时 / 取消都必须调用。与 OnReceive 同样在 c.mu 内删除，保证不会向已关闭通道发送
// （pump 删后查不到即不发）。
func (pr *PendingRequest) Close() {
	if pr == nil {
		return
	}
	pr.closeOnce.Do(func() {
		pr.conn.mu.Lock()
		if cur, ok := pr.conn.responseMap[pr.routeKey]; ok && cur == pr.ch {
			delete(pr.conn.responseMap, pr.routeKey)
		}
		pr.conn.mu.Unlock()
		close(pr.ch)
	})
}

// sendBackend 底层发送函数。onWritten 非 nil 时，在数据真正交给内核后以写完成时刻回调。
//
// 即发即忘路径（心跳、帧同步、tcpSend）传 nil：它们不需要 RTT 起点，也就不必为每次发送
// 多付一个闭包分配——这条路径的量级是每秒十万级，请求-响应路径是每秒千级。
type sendBackend func(data []byte, onWritten WriteDoneFunc) error

// Send 异步发送数据（不登记写完成时刻）。
func (c *Connection) Send(data []byte) (int, error) {
	return c.send(data, nil)
}

// sendTimed 异步发送并登记写完成时刻，供请求-响应路径计算 WireRTT。
func (c *Connection) sendTimed(data []byte, onWritten WriteDoneFunc) (int, error) {
	return c.send(data, onWritten)
}

func (c *Connection) send(data []byte, onWritten WriteDoneFunc) (int, error) {
	if c == nil {
		return 0, errcode.NewActionError(errcode.ErrConnNotFound, "")
	}
	if c.isClose.Load() == 1 {
		return 0, errcode.NewActionError(errcode.ErrConnClosed, c.serviceName)
	}
	if c.sendFunc == nil {
		stresslog.Warn("[NETWORK] Send sendFunc 未注入",
			zap.String("service", c.serviceName),
			zap.String("robot", c.robotName),
			zap.Int("pktLen", len(data)))
		return 0, errcode.NewActionError(errcode.ErrSendFailed, c.serviceName)
	}

	n := len(data)
	err := c.sendFunc(data, onWritten)
	if err != nil {
		stresslog.Error("[NETWORK] Send 发送失败",
			zap.String("service", c.serviceName),
			zap.String("robot", c.robotName),
			zap.Int("pktLen", n),
			zap.Error(err))
		return 0, errcode.NewActionError(errcode.ErrSendFailed, c.serviceName, err)
	}
	// 全局带宽统计
	monitor.Global().AddBandwidth(int64(n), 0)
	return n, nil
}

// RegisterListen 为指定 routeKey 注册持久化推送监听。是唯一的监听注册入口。
//
// 参数：
//   - routeKey: 路由键（由 protocol.ExpectedRouteKey 计算）。
//   - cb: nil = 缓存模式（消息进 queue，由 GetListenResp/main-flow 消费）；非 nil = 回调模式。
//   - queueSize: 缓存队列容量（>=1，cap<1 由 newListenQueue panic）。首次注册时预创建队列。
//
// 语义：
//   - 新注册：写入 listenRoutes[routeKey]（回调 + 预创建容量 queueSize 的队列），返回 nil。
//   - 幂等：同 routeKey 再注册且（queueSize 一致 && cb 是否为 nil 一致）→ 不重建队列、不报错（回写 binding.cb 为最新值，队列与 queueSize 不变；nil-cb 即纯 no-op），返回 nil。
//   - 冲突 fail-loud：同 routeKey 但 queueSize 或 cb 模式不一致 → 返回中文 error。
//
// 2-C3 起：listen 分发已并入 connectionPump，注册只是「写一张 map + 预创建队列」的一次性纯 map
// 操作，**不再启动独立 listenLoop goroutine**。pump 在 decode 后命中 listenRoutes 时直接调
// dispatchListen（cb!=nil 跑回调 / cb==nil 写队列）；GetListenResp 仍由主流程
// 直接 FIFO Pop（per-queue mu 串行化，与 pump 的 Push 无死锁）。
//
// c.mu 保护 listenRoutes 的读-改 + 冲突判断 + binding.cb 的回写（与 pump dispatchListen /
// GetListenResp 同样的锁粒度，沿用现状）。回调与队列同属一个 binding 后，
// 「有回调却没队列」这种旧双表可能的偏斜状态在结构上不再存在。
func (c *Connection) RegisterListen(routeKey string, cb ListenCallBack, queueSize int) error {
	if c == nil {
		return fmt.Errorf("监听注册失败：连接为 nil（routeKey=%q）", routeKey)
	}
	if c.isClose.Load() == 1 {
		return fmt.Errorf("监听注册失败：连接已关闭（service=%q routeKey=%q）", c.serviceName, routeKey)
	}

	c.mu.Lock()
	if b, exist := c.listenRoutes[routeKey]; exist {
		// 冲突检测：同 routeKey 跨次注册。
		if b.queue.capacity != queueSize {
			c.mu.Unlock()
			return fmt.Errorf("监听注册冲突：service=%q routeKey=%q 已注册（queueSize=%d），与本次（queueSize=%d）不一致",
				c.serviceName, routeKey, b.queue.capacity, queueSize)
		}
		existingCbIsNil := b.cb == nil
		newCbIsNil := cb == nil
		if existingCbIsNil != newCbIsNil {
			c.mu.Unlock()
			return fmt.Errorf("监听注册冲突：service=%q routeKey=%q 已注册（回调=%v），与本次（回调=%v）模式不一致",
				c.serviceName, routeKey, !existingCbIsNil, !newCbIsNil)
		}
		// 幂等 no-op：保持一致回写 cb（无副作用），不重复建队列。
		b.cb = cb
		c.mu.Unlock()
		return nil
	}

	// 新注册：绑定回调 + 预创建队列（队列在发布进 map 前建好，此后只读）。
	c.listenRoutes[routeKey] = &listenBinding{cb: cb, queue: newListenQueue(queueSize)}
	c.mu.Unlock()
	return nil
}

// WaitListenDone 等待 connectionPump 退出（pump 已包含旧 listenLoop 的所有工作）。
//
// 2-C3 起 listenLoop 已并入 pump：pump 退出时所有已分发的回调均已执行完毕（dispatchListen
// 是同步调用）。本方法现在与 WaitDecodeDone 等价，都等 pumpDone；保留方法名是为了让
// client.go 的 CloseAllWithTimeout 调用语义保持「先等 decode，再等 listen」的两阶段表达
// （client.go 不在本任务可改文件清单内）。
func (c *Connection) WaitListenDone() {
	if c == nil {
		return
	}
	if c.pumpDone != nil {
		<-c.pumpDone
	}
}

// WaitListenDoneTimeout 带超时等待 connectionPump 退出。
func (c *Connection) WaitListenDoneTimeout(timeout time.Duration) bool {
	if c == nil {
		return true
	}
	ch := c.pumpDone
	if ch == nil {
		return true
	}
	if timeout <= 0 {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
		return false
	}
}

// dispatchListen 把已解码消息交给该 routeKey 的监听绑定。
//
// cb / q 由调用方（OnReceive）在同一次 c.mu 持有期内从 binding 取出后传入：分发路径
// 不再回查 map、不再二次加锁。旧实现在这里重查一次 listenResp（OnReceive 刚查过却
// 丢掉了结果），缓存模式还要第三次加锁查队列——每条推送 2 次多余的锁往返 + 字符串
// map 查找，在广播密集的压测里是纯开销。
//
// Push 在 c.mu 之外进行（per-queue mu 串行化），与主流程 GetListenResp 的 Pop 无死锁。
func (c *Connection) dispatchListen(resp *Message, cb ListenCallBack, q *listenQueue) {
	if cb != nil {
		cb(resp)
		return
	}
	if q == nil {
		// 队列在注册时预创建，绑定存在即队列存在；真出现只能是簿记被写坏，
		// 不静默丢弃，报出来。
		stresslog.Warn("[NETWORK] 监听绑定缺队列，丢弃推送消息",
			zap.String("service", c.serviceName),
			zap.String("robot", c.robotName),
			zap.String("routeKey", resp.RouteKey))
		return
	}
	if q.Push(resp) {
		// 默认容量 1：从第 2 条起每条都会触发覆盖丢弃，保最新的消息。
		stresslog.Warn("[NETWORK] 监听队列已满，覆盖丢弃最旧消息",
			zap.String("service", c.serviceName),
			zap.String("robot", c.robotName),
			zap.String("routeKey", resp.RouteKey),
			zap.Uint64("dropped", q.Dropped()))
	}
}

// GetListenResp 非阻塞获取缓存的监听消息（FIFO pop）。
//
// c.mu 仅查 map；Pop 走队列自身 mu。默认容量 1 时与旧「读 listenMsg[k] + delete」
// 行为一致：返回最近一条 Push 并清空。
func (c *Connection) GetListenResp(routeKey string) *Message {
	if c == nil || c.isClose.Load() == 1 {
		return nil
	}
	c.mu.Lock()
	b, ok := c.listenRoutes[routeKey]
	c.mu.Unlock()
	if !ok {
		return nil
	}
	m, _ := b.queue.Pop()
	return m
}

// ListenNotify 返回缓存监听 routeKey 的只读唤醒通道。
//
// 通道只做边沿通知，消息仍必须通过 GetListenResp 从环形队列消费。未注册、回调模式、
// nil 或已关闭连接返回 nil；select nil channel 会自然禁用该分支并等待 ctx/deadline。
func (c *Connection) ListenNotify(routeKey string) <-chan struct{} {
	if c == nil || c.isClose.Load() == 1 {
		return nil
	}
	c.mu.Lock()
	b, ok := c.listenRoutes[routeKey]
	if !ok || b.cb != nil || b.queue == nil {
		c.mu.Unlock()
		return nil
	}
	notify := b.queue.Notify()
	c.mu.Unlock()
	return notify
}

// StartPump 启动 connectionPump（每连接一个），统一接管 inbound decode / listen 分发 /
// 心跳 timer + control。替代旧三协程模型（decodeLoop + listenLoop + 独立 runHeartbeat goroutine）。
//
// 设计动机（02-track §2-C）：Go SchemaAdapter 后 decode 是纯 Go（无 Lua），但 codec pipeline
// 仍含解压/校验/hash/加密等线性 CPU 步，inline 进 gnet event loop 会重新引入 loop 卡顿风险；
// 同时把 inbound decode / listen 分发 / 心跳 timer 合并进同一 goroutine；心跳
// builder 是 Go-only，不触碰业务 LState，pump 进一步去掉独立心跳 goroutine。
//
// 必须在 Dial 成功且 conn 注册到 registry 之后、首次 OnTraffic 可能到达之前调用。
// CAS 防止 reconnect 场景下重复启动。adp 由上层 Robot 经 CodecResolver.Resolve 在拨号前
// 解析后注入（nil → 上层已 fail loud）；提交失败会回滚全部 pump 状态并返回错误，允许重试。
// 提交成功后该连接 decode 全程用 c.adp，不再查 resolver。
//
// pump 是 network 内部调度，不泄漏到 flow/engine/Lua：外层只感知 RegisterListen /
// GetListenResp / RegisterHeartbeat 这些已存在的接口。
func (c *Connection) StartPump(adp protocol.Adapter, isUDP bool) error {
	return c.startPumpWithSubmit(adp, isUDP, workpool.Default().Submit)
}

func (c *Connection) startPumpWithSubmit(adp protocol.Adapter, isUDP bool, submit func(func()) error) error {
	if c == nil {
		return fmt.Errorf("connection 不能为空")
	}
	if adp == nil {
		return fmt.Errorf("adapter 不能为空")
	}
	if !c.pumpRun.CompareAndSwap(0, 1) {
		return nil
	}
	c.adp = adp
	c.isUDP = isUDP
	c.inboundCh = make(chan inboundFrame, inboundChSize)
	c.controlCh = make(chan pumpCmd, controlChSize)
	pumpDone := make(chan struct{})
	c.pumpDone = pumpDone
	if err := submit(c.connectionPump); err != nil {
		close(pumpDone)
		c.adp = nil
		c.inboundCh = nil
		c.controlCh = nil
		c.pumpDone = nil
		c.pumpRun.Store(0)
		return fmt.Errorf("提交 connection pump 失败: %w", err)
	}
	return nil
}

// connectionPump 是每条连接唯一的 owner goroutine，串行处理：
//   - inbound decode → request-response 通道 / listen queue / store 分发
//   - heartbeat timer 到期 → 调 builder → Send
//   - controlCh 命令 → 注册/停止心跳、stop
//   - ctx.Done → drain inbound buffer 归还、停 timer、关 done
//
// 两条硬约束（02-track §2-C 伪代码）：
//  1. heartbeat-due 优先：每轮循环开头先检查 hb.timer 是否已到期（非阻塞），
//     到期立即发送心跳，避免 inbound backlog 饿死心跳导致服务端判掉线。
//  2. inbound bounded batch：进入 inbound 分支后最多连续处理 pumpInboundBatchSize 条，
//     强制回外层 select 给 heartbeatTimer/controlCh/ctx.Done 机会，防止一直 drain inbound。
//
// 并发安全：c.adp 是 Go SchemaAdapter（无可变状态，并发安全）；pump 是 inbound decode 的
// 唯一执行者（单 goroutine 串行，天然有序）；各监听绑定的 queue 自带 mu，与主流程
// 的 GetListenResp Pop 无死锁；responseMap 由 c.mu 保护（与 RequestResponse 同锁粒度）。
//
// 退出时（defer）：drain inboundCh 归还 buffer 池避免泄漏；停止心跳 timer；关闭 pumpDone
// 通知 WaitPumpDone / WaitDecodeDone / WaitListenDone 调用方。
func (c *Connection) connectionPump() {
	defer close(c.pumpDone)
	defer c.stopHeartbeatTimerLocked()

	for {
		// 硬约束 1：heartbeat-due 优先。timer 已到期时立即发送，避免下文 select 因持续有
		// inbound 可读而长期随机不到 timer.C 分支（select 是伪随机，hot inbound 通道会饿死 timer）。
		// heartbeatDueLocked 会非阻塞消费掉一次 timer.C 触发。
		if c.heartbeatDueLocked() {
			c.sendHeartbeatLocked()
			c.resetHeartbeatTimerLocked()
			continue
		}

		// 心跳 timer 的 channel 动态拼进 select：未注册心跳时为 nil，select 自动忽略 nil channel。
		// 必须把 timer.C 放进 select 才能在 timer 到期时唤醒 pump（否则 pump 会一直阻塞在
		// inbound/control/ctx 三路 select 上，即便心跳到期也无法触发）。timer.C 的实际处理
		// 交给下一轮循环顶部的 heartbeatDueLocked（消费 + 发送 + 重置），这里只是「唤醒」。
		var heartbeatC <-chan time.Time
		if c.hb != nil && c.hb.timer != nil {
			heartbeatC = c.hb.timer.C
		}

		select {
		case <-c.ctx.Done():
			c.drainInboundLocked()
			return
		case <-heartbeatC:
			// timer 到期：发心跳 + 重置 timer。直接在这里处理（而非依赖下一轮 heartbeatDueLocked）
			// 避免 timer.C 在 select 随机选择中被反复跳过（消费掉这次触发）。
			c.sendHeartbeatLocked()
			c.resetHeartbeatTimerLocked()
		case cmd := <-c.controlCh:
			c.handleControlLocked(cmd)
		case frame, ok := <-c.inboundCh:
			if !ok {
				c.drainInboundLocked()
				return
			}
			c.decodeAndDispatch(frame)
			// 硬约束 2：bounded batch。最多再连取 pumpInboundBatchSize-1 条（非阻塞），
			// 处理完强制回外层重新检查 heartbeat due + select，防止海量 inbound 饿死 timer/control。
			batched := 1
		batchLoop:
			for batched < pumpInboundBatchSize {
				select {
				case frame2, ok := <-c.inboundCh:
					if !ok {
						c.drainInboundLocked()
						return
					}
					c.decodeAndDispatch(frame2)
					batched++
				default:
					// inboundCh 当前为空，跳出回外层让 heartbeat/control/ctx 有机会被选中。
					break batchLoop
				}
			}
		}
	}
}

// drainInboundLocked 排空 inboundCh 并归还 buffer 池。
// 仅在 pump 退出路径（ctx.Done / inboundCh 关闭）调用。连接已关闭，调用方不再期待这些响应，
// 丢弃即可，只需归还 buffer 避免泄漏。
func (c *Connection) drainInboundLocked() {
	for {
		select {
		case frame, ok := <-c.inboundCh:
			if !ok {
				return
			}
			putMsgBuf(frame.Data)
		default:
			return
		}
	}
}

// handleControlLocked 在 pump goroutine 内串行处理 controlCh 命令。
// 「Locked」后缀表示本方法只能由 pump goroutine 调用（hb / timer 字段无锁访问）。
func (c *Connection) handleControlLocked(cmd pumpCmd) {
	switch cmd.kind {
	case pumpCmdHeartbeat:
		// 替换/注册心跳：停止旧 timer（若有），装上新 cfg + 新 timer。
		c.stopHeartbeatTimerLocked()
		c.hb = &heartbeatRuntime{cfg: cmd.hbCfg}
		c.resetHeartbeatTimerLocked()
		if cmd.result != nil {
			cmd.result <- nil
		}
	case pumpCmdStopHeartbeat:
		c.stopHeartbeatTimerLocked()
		c.hb = nil
		if cmd.result != nil {
			cmd.result <- nil
		}
	case pumpCmdStop:
		// 主动请求退出。pump 主循环不直接 break（需 drain），改 cancel ctx 让主循环走 ctx.Done 分支。
		c.cancel()
		if cmd.result != nil {
			cmd.result <- nil
		}
	}
}

// heartbeatDueLocked 非阻塞检查心跳 timer 是否已到期。
// hb==nil 或 timer==nil 表示未注册心跳，返回 false。否则尝试非阻塞读 timer.C：
// 能读到说明已到期，返回 true（并消费掉这次的触发，避免 reset 后立即又触发）。
func (c *Connection) heartbeatDueLocked() bool {
	if c.hb == nil || c.hb.timer == nil {
		return false
	}
	select {
	case <-c.hb.timer.C:
		return true
	default:
		return false
	}
}

// sendHeartbeatLocked 发送一次心跳。仅在 pump goroutine 调用。
// 跳过条件：连接已关闭（isClose==1）或 builder 返回 nil。
func (c *Connection) sendHeartbeatLocked() {
	if c.hb == nil {
		return
	}
	if c.isClose.Load() == 1 {
		return
	}
	packet := c.hb.cfg.Builder()
	if packet == nil {
		return
	}
	n, err := c.Send(packet)
	if err != nil {
		stresslog.Warn("[HEARTBEAT] 发送失败",
			zap.String("service", c.serviceName),
			zap.String("robot", c.robotName),
			zap.Bool("udp", c.isUDP),
			zap.Int("pktLen", len(packet)), zap.Error(err))
		return
	}
	if stresslog.DebugEnabled() {
		stresslog.Debug("[HEARTBEAT] 已发送",
			zap.String("service", c.serviceName),
			zap.String("robot", c.robotName),
			zap.Bool("udp", c.isUDP),
			zap.Int("pktLen", n))
	}
}

// resetHeartbeatTimerLocked （重新）设置心跳 timer 为 cfg.Interval 后到期。
// hb==nil 时无操作。timer 复用：若已存在先 Stop 再 Reset，避免 timer 泄漏。
func (c *Connection) resetHeartbeatTimerLocked() {
	if c.hb == nil {
		return
	}
	if c.hb.timer == nil {
		c.hb.timer = time.NewTimer(c.hb.cfg.Interval)
		return
	}
	if !c.hb.timer.Stop() {
		// 排空可能已在 C 里的触发，避免 reset 后立即旧触发被消费。
		select {
		case <-c.hb.timer.C:
		default:
		}
	}
	c.hb.timer.Reset(c.hb.cfg.Interval)
}

// stopHeartbeatTimerLocked 停止心跳 timer 并清空 hb runtime。
// 仅 pump goroutine 调用。hb 字段置 nil，让后续 heartbeatDueLocked/sendHeartbeatLocked 自然 no-op。
func (c *Connection) stopHeartbeatTimerLocked() {
	if c.hb == nil {
		return
	}
	if c.hb.timer != nil {
		if !c.hb.timer.Stop() {
			select {
			case <-c.hb.timer.C:
			default:
			}
		}
		c.hb.timer = nil
	}
	c.hb = nil
}

// decodeAndDispatch 执行单帧的 Lua 解码 + 分发，归还 buffer。
// 拆出独立方法便于 panic recover 边界明确（utils 池已带 recover，这里不再加一层）。
func (c *Connection) decodeAndDispatch(frame inboundFrame) {
	defer putMsgBuf(frame.Data)
	secretKey := c.GetSecretKey()

	var decodeStart, decodeEnd time.Time
	if monitor.TimingDetailAtLeast(c.timingDetail, monitor.TimingCodecDetail) {
		decodeStart = time.Now()
	}
	var routeKey string
	var body []byte
	var headerErr uint64
	if c.isUDP {
		routeKey, body, headerErr = c.adp.DecodeUDP(frame.Data, secretKey)
	} else {
		routeKey, body, headerErr = c.adp.DecodeTCP(frame.Data, secretKey)
	}
	if monitor.TimingDetailAtLeast(c.timingDetail, monitor.TimingCodecDetail) {
		decodeEnd = time.Now()
	}
	if routeKey == "" {
		stresslog.Warn("[NETWORK] 解码返回空 routeKey，响应被丢弃",
			zap.String("service", c.serviceName),
			zap.String("robot", c.robotName),
			zap.Bool("udp", c.isUDP),
			zap.Int("wireBytes", frame.WireBytes),
			zap.Int("bodyLen", len(body)),
			zap.Uint64("headerErr", headerErr))
		return
	}
	c.OnReceive(routeKey, body, headerErr, frame.WireBytes, MessageTiming{
		RecvFrameAt: frame.RecvFrameAt,
		DecodeStart: decodeStart,
		DecodeEnd:   decodeEnd,
	})
}

// EnqueueResult 表示 EnqueueRaw 的结果，区分"连接已关"和"通道真满"两种失败原因。
// 早期版本用 bool 返回，OnTraffic 把两者混为一谈都关连接 + 打 decode 满 warn，
// 导致任务停止时（500 robot 几乎同时 Close）服务端残余 inbound 字节走 OnTraffic 时
// 命中 isClose 分支被误报为反压（一次实测 567 条，95% 集中在 stopping 后 5 秒内）。
type EnqueueResult int

const (
	EnqueueOK     EnqueueResult = iota // 成功入队
	EnqueueClosed                      // 连接已关闭 / 尚未启动 pump：静默丢包，调用方不应再 Close
	EnqueueChFull                      // inboundCh 真满：触发反压，调用方应关闭连接释放资源
)

// EnqueueRaw 由 gnet OnTraffic 调用，把 raw 包投递到 connectionPump 的 inboundCh。
//
// 2-C3 起：OnTraffic 只做纯 Go 帧切割 + 投递，不接触 decode（decode 在 pump 内做）。
// 三态返回让调用方区分"连接已关"和"真反压"，避免停止阶段把 close 后还在路上的
// inbound 字节当成 decode 满。只有 EnqueueChFull 是需要警示的真问题。
func (c *Connection) EnqueueRaw(msgBuf []byte, recvFrameAt time.Time) EnqueueResult {
	if c == nil {
		return EnqueueClosed
	}
	if c.isClose.Load() == 1 {
		return EnqueueClosed
	}
	if c.inboundCh == nil {
		// StartPump 还没调用（极端启动竞态）：帧丢弃，业务层会超时/重发。
		return EnqueueClosed
	}
	select {
	case c.inboundCh <- inboundFrame{Data: msgBuf, WireBytes: len(msgBuf), RecvFrameAt: recvFrameAt}:
		return EnqueueOK
	default:
		return EnqueueChFull
	}
}

// WaitDecodeDone 等待 connectionPump 退出（pump 已包含旧 decodeLoop 的所有工作）。
//
// decode/listen 已合并到 connectionPump；本方法为兼容旧调用名保留，等价于 WaitPumpDone。
// 新代码请直接使用 WaitPumpDone。
func (c *Connection) WaitDecodeDone() {
	if c == nil {
		return
	}
	if c.pumpDone != nil {
		<-c.pumpDone
	}
}

// WaitDecodeDoneTimeout 带超时等待 connectionPump 退出。
func (c *Connection) WaitDecodeDoneTimeout(timeout time.Duration) bool {
	if c == nil {
		return true
	}
	ch := c.pumpDone
	if ch == nil {
		return true
	}
	if timeout <= 0 {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
		return false
	}
}

// WaitPumpDone 等待 connectionPump 退出。
// 与 WaitDecodeDone / WaitListenDone 等价（pump 是三者合并后的唯一 owner goroutine）。
// 推荐新代码用本方法表达「等连接所有后台 goroutine 退出」的语义。
func (c *Connection) WaitPumpDone() {
	if c == nil {
		return
	}
	if c.pumpDone != nil {
		<-c.pumpDone
	}
}

// WaitPumpDoneTimeout 带超时等待 connectionPump 退出。
func (c *Connection) WaitPumpDoneTimeout(timeout time.Duration) bool {
	return c.WaitDecodeDoneTimeout(timeout)
}

// loadCloseReason 读取 onClose 时写入的关闭原因字符串。
// 返回空串表示：没被 gnet OnClose 触发过（如纯本地 Close 或还未真正关闭）。
func (c *Connection) loadCloseReason() string {
	v := c.closeReason.Load()
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// doClose 执行连接关闭的共享逻辑：取消上下文 + 停止心跳。
// 调用方负责触发回调。
//
// 2-C3 起（connectionPump 模型）：cancel ctx 是 pump 退出的唯一权威信号——pump 主循环
// 看到 ctx.Done 立即 drain inbound 归还 buffer，并通过 defer stopHeartbeatTimerLocked 停止
// 心跳 timer。因此 StopHeartbeat 在此处主要是「主动让 pump 尽快丢掉心跳 runtime」的快速路径：
// 投递一条 pumpCmdStopHeartbeat 到 controlCh，pump 在下一轮 select 收到后立即停 timer + 置 hb=nil，
// 不必等到 pump 自己 ctx.Done 分支。即便投递丢失（pump 正卡在 inbound batch / 已退出），
// defer 兜底也会停 timer，无泄漏。
//
// 2-B（builder Go-only）+ 2-C3（pump 单 goroutine，builder 在 pump 内同步调用）后，
// 心跳 builder 不再接触业务 LState。此处保留 cancel-first 顺序只是为了语义清晰。
func (c *Connection) doClose() {
	c.cancel()
	c.StopHeartbeat()
}

// onClose gnet 异步触发的关闭回调（OnClose 事件）。
// 与 Close() 构成双路径：
//   - Close()：主动关闭，先 CAS 设置 isClose=1，再执行 doClose + 触发 onClosed（不触发 onDisconnect）
//   - onClose()：被动关闭（gnet OnClose），同样 CAS，执行 doClose + 触发 onDisconnect + onClosed
//
// 两者通过 isClose CAS 互斥，保证 doClose 和回调只执行一次。
//
// reason 来自 gnet OnClose 给的 error 字符串（"EOF" / "wsarecv: ... forcibly closed" / "" 等），
// 在 cancel ctx 之前写入 closeReason，让 inflight RequestResponse 命中 ctx.Done() 时能拿到归因。
func (c *Connection) onClose(reason string) {
	if !c.isClose.CompareAndSwap(0, 1) {
		return
	}
	// 关键顺序：先写 closeReason 再 cancel，保证 RequestResponse 在 ctx.Done()
	// 后续读取时 closeReason 一定已就绪（Happens-Before 通过 atomic 保证）。
	if reason != "" {
		c.closeReason.Store(reason)
	}
	c.doClose()

	// 业务"意外断开"回调：仅非主动关闭时触发（用于 robot 主连接断开 → 停 robot）
	if c.intentionalClose.Load() == 0 {
		c.publishDisconnected()
	}
	// 监控"关闭"回调：主动/被动均触发；与 ConnEstablished 配对，保证 active = open - close 准确
	c.publishClosed()

	stresslog.Debug("[NETWORK] 连接资源已清理", zap.String("service", c.serviceName), zap.String("robot", c.robotName))
}

// Close 主动关闭连接。触发 onClosed 但不触发 onDisconnect（主动关闭不算意外断开）。
func (c *Connection) Close() {
	if c == nil || !c.isClose.CompareAndSwap(0, 1) {
		return
	}
	c.intentionalClose.Store(1)
	if c.closeFunc != nil {
		_ = c.closeFunc()
	}
	c.doClose()
	// CAS 保证关闭事件只发布一次；事件状态允许回调稍后注册时补交付。
	c.publishClosed()
	stresslog.Debug("[NETWORK] 连接资源已清理", zap.String("service", c.serviceName), zap.String("robot", c.robotName))
}

// OnReceive 收到网络消息时分发到 request-response 通道或持久监听回调。
//
// 2-C3 起：本方法在 connectionPump goroutine 内被 decodeAndDispatch 同步调用（pump 是 inbound
// decode 的唯一执行者）。命中监听绑定时不再投递到独立的 listenCh（listenLoop 已删除），
// 而是**同步直接调用 dispatchListen**——cb!=nil 跑回调、cb==nil 进缓存队列。
// 这样 listen 分发与 decode 共享同一 pump goroutine，彻底消灭旧的 listenCh/listenLoop 链路。
//
// 分派代价：一次持锁 + 至多两次 map 查找（responseMap 常为空表，Go 对空 map 查找有快路径），
// 命中监听时回调与队列在同一次查找中取齐，分发不再回查。
//
// 热路径：每个入站包都会走一次。高频 Debug 日志构造前先做 atomic level 检查，
// 避免在 info 级别下白白构造 zap.Field 切片（每包 4 个 string field，
// 10000 连接 × 5 包/s 下能省下数百微秒 CPU/s）。
func (c *Connection) OnReceive(routeKey string, body []byte, headerErr uint64, wireBytes int, timing MessageTiming) {
	if c.isClose.Load() == 1 {
		return
	}

	if stresslog.DebugEnabled() {
		stresslog.Debug("[NETWORK] OnReceive",
			zap.String("service", c.serviceName),
			zap.String("routeKey", routeKey),
			zap.String("robot", c.robotName),
			zap.Int("bodyLen", len(body)),
			zap.Int("wireBytes", wireBytes),
			zap.Uint64("headerErr", headerErr))
	}

	// 路由分派：一次持锁内决出「等待中的请求 / 监听绑定 / 无人认领」，
	// 命中监听时把回调与队列一并取出（dispatchListen 不再回查 map）。
	// Message 在确定有消费方之后才构造——无人认领的广播不再白白分配。
	c.mu.Lock()
	ch, exists := c.responseMap[routeKey]
	if exists {
		resp := NewMessage(routeKey, body, headerErr, wireBytes, timing)
		// 在锁内发送，防止 RequestResponse 的 defer 在 unlock 和 send 之间 close(ch) 导致 panic。
		if monitor.TimingDetailAtLeast(c.timingDetail, monitor.TimingFullDetail) {
			resp.Timing.DispatchStart = time.Now()
		}
		select {
		case ch <- resp:
		default:
			stresslog.Warn("[NETWORK] OnReceive 响应通道已满",
				zap.String("service", c.serviceName),
				zap.String("routeKey", routeKey),
				zap.String("robot", c.robotName),
				zap.Int("bodyLen", len(body)),
				zap.Int("wireBytes", wireBytes),
				zap.Uint64("headerErr", headerErr))
		}
		c.mu.Unlock()
		return
	}

	var (
		cb ListenCallBack
		q  *listenQueue
	)
	b, isListen := c.listenRoutes[routeKey]
	if isListen {
		cb, q = b.cb, b.queue
	}
	c.mu.Unlock()

	if isListen {
		resp := NewMessage(routeKey, body, headerErr, wireBytes, timing)
		if monitor.TimingDetailAtLeast(c.timingDetail, monitor.TimingFullDetail) {
			resp.Timing.DispatchStart = time.Now()
		}
		// 同步分发：cb!=nil 跑回调，cb==nil 进队列（per-queue mu 串行化）。
		// pump goroutine 独占执行，与主流程 GetListenResp 的 Pop 无死锁。
		c.dispatchListen(resp, cb, q)
		return
	}

	if stresslog.DebugEnabled() {
		stresslog.Debug("[NETWORK] OnReceive 未匹配任何请求或监听",
			zap.String("service", c.serviceName),
			zap.String("routeKey", routeKey),
			zap.String("robot", c.robotName),
			zap.Int("bodyLen", len(body)))
	}
}
