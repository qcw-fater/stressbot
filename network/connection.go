package network

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"stressbot/adapter"
	"stressbot/engine"
	"stressbot/errcode"
	"stressbot/monitor"
	"stressbot/utils"
	stresslog "stressbot/utils/log"

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
	listenResp       map[string]ListenCallBack // routeKey → 持久化推送回调
	listenQueues     map[string]*listenQueue   // routeKey → 缓存队列（轮询模式，回调为 nil 时）
	mu               sync.Mutex                // 保护 responseMap / listenResp / listenQueues map 键 / 回调字段（各 listenQueue 自带 mu 串行化 Push/Pop）
	ctx              context.Context           // 连接生命周期上下文
	cancel           context.CancelFunc        // 取消函数，关闭时调用
	isClose          int32                     // 原子标记：0=活跃，1=已关闭
	intentionalClose int32                     // 原子标记：1=主动 Close() 触发，不触发 onDisconnect
	requestTimeout   time.Duration             // RequestResponse 默认超时
	sendFunc         func(data []byte) error   // 底层发送函数（由 Dialer 注入）
	closeFunc        func() error              // 底层关闭函数（由 Dialer 注入）
	onDisconnect     func()                    // 意外断开回调（非主动 Close 触发，业务用于停 robot）
	onClosed         func()                    // 关闭回调（主动/被动均触发，监控用，与 ConnEstablished 配对）

	// connectionPump（每连接一个）替代旧的 decodeLoop + listenLoop + 心跳 goroutine。
	// 详见 connectionPump godoc。pump goroutine 是 inbound decode / listen 分发 / 心跳
	// pump goroutine 独占处理 inbound/control 和心跳 timer；inboundCh/controlCh 由外部投递、pump 消费，
	// pumpDone 由 pump 关闭、外部等待。adp/isUDP 在 StartPump 时一次性注入后只读。
	adp       adapter.Adapter   // decode 用的协议适配器（Go SchemaAdapter），StartPump 时一次性注入
	isUDP     bool              // 该连接是否 UDP（决定调 DecodeUDP / DecodeTCP）
	inboundCh chan inboundFrame // 待解码的 raw msg buffer（OnTraffic 投递→pump 消费）
	controlCh chan pumpCmd      // pump 控制通道（注册/停止心跳、stop）
	pumpDone  chan struct{}     // pump goroutine 退出信号，供 WaitPumpDone/WaitDecodeDone/WaitListenDone 等待
	pumpRun   int32             // 原子标记：1 表示 pump 已启动（CAS 防重复启动）
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
	// defaultListenQueueSize 监听缓存队列默认容量。
	// 容量 1 与旧「单槽 map[string]*Message」语义逐字节等价（同 routeKey 新消息覆盖旧消息）。
	// 2-A2 起可由 ListenRef.queueSize 显式覆盖（本任务不接配置）。
	defaultListenQueueSize = 1
)

type inboundFrame struct {
	Data        []byte
	WireBytes   int
	RecvFrameAt time.Time
}

// NewConnection 创建新的网络连接。
func NewConnection(serviceName, robotName string, requestTimeout time.Duration, timingDetail monitor.TimingDetailLevel) *Connection {
	conn := &Connection{
		serviceName:    serviceName,
		robotName:      robotName,
		responseMap:    make(map[string]chan *Message),
		listenResp:     make(map[string]ListenCallBack),
		listenQueues:   make(map[string]*listenQueue),
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
	c.mu.Unlock()
}

// SetOnClosed 设置连接关闭回调（主动/被动均触发，用于监控计数）。
func (c *Connection) SetOnClosed(fn func()) {
	c.mu.Lock()
	c.onClosed = fn
	c.mu.Unlock()
}

// SetSecretKey 设置通信加密密钥。
// 复制传入的 key 后整体替换（immutable），后续不再修改已存储的切片。
func (c *Connection) SetSecretKey(key []byte) {
	if c == nil || atomic.LoadInt32(&c.isClose) == 1 {
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
		return nil, timing, engine.NewActionError(errcode.ErrConnNotFound, "routeKey="+routeKey)
	}
	if atomic.LoadInt32(&c.isClose) == 1 {
		stresslog.Warn("[NETWORK] RequestResponse 连接已关闭", zap.String("service", c.serviceName), zap.String("routeKey", routeKey), zap.String("robot", c.robotName))
		return nil, timing, engine.NewActionError(errcode.ErrConnClosed, c.serviceName+" routeKey="+routeKey)
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

	sendStart := time.Now()
	n, sendErr := c.Send(sendData)
	sendDone := time.Now()
	timing.SendCost = safeSub(sendDone, sendStart)
	if sendErr != nil {
		stresslog.Error("[NETWORK] RequestResponse 发送失败",
			zap.String("service", c.serviceName), zap.String("routeKey", routeKey),
			zap.String("robot", c.robotName),
			zap.Int("pktLen", len(sendData)))
		return nil, timing, sendErr
	}
	_ = n

	timeout := c.requestTimeout
	if len(timeoutOverride) > 0 && timeoutOverride[0] > 0 {
		timeout = timeoutOverride[0]
	}
	// 用 NewTimer + Stop 而非 time.After：响应通常在毫秒级到达即提前返回，
	// time.After 的底层 timer 要到 timeout（默认 60s）才回收，高 QPS 下会堆积
	// 数十万个悬挂 timer，徒增堆占用与 GC 压力。
	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()
	select {
	case <-c.ctx.Done():
		// ACK 可能先于 ctx cancel 入队但 select 随机选到了此分支，drain channel。
		select {
		case resp := <-ch:
			actionUnblocked := time.Now()
			timing.WireRTT = safeSub(resp.Timing.RecvFrameAt, sendDone)
			timing.DecodeWait = safeSub(resp.Timing.DecodeStart, resp.Timing.RecvFrameAt)
			timing.DecodeCost = safeSub(resp.Timing.DecodeEnd, resp.Timing.DecodeStart)
			timing.DispatchToActionWait = safeSub(actionUnblocked, resp.Timing.DispatchStart)
			return resp, timing, nil
		default:
		}
		elapsed := safeSub(time.Now(), sendDone)
		if atomic.LoadInt32(&c.intentionalClose) == 1 {
			stresslog.Debug("[NETWORK] RequestResponse 因本地关闭被取消",
				zap.String("service", c.serviceName), zap.String("routeKey", routeKey),
				zap.String("robot", c.robotName),
				zap.Duration("elapsed", elapsed))
			return nil, timing, engine.NewActionError(errcode.ErrActionCanceled,
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
		return nil, timing, engine.NewActionError(errcode.ErrConnDropped, detail)
	case resp := <-ch:
		actionUnblocked := time.Now()
		timing.WireRTT = safeSub(resp.Timing.RecvFrameAt, sendDone)
		timing.DecodeWait = safeSub(resp.Timing.DecodeStart, resp.Timing.RecvFrameAt)
		timing.DecodeCost = safeSub(resp.Timing.DecodeEnd, resp.Timing.DecodeStart)
		timing.DispatchToActionWait = safeSub(actionUnblocked, resp.Timing.DispatchStart)
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
		return nil, timing, engine.NewActionError(errcode.ErrRecvTimeout, c.serviceName+" routeKey="+routeKey+" timeout="+timeout.String())
	}
}

// Send 异步发送数据。
func (c *Connection) Send(data []byte) (int, error) {
	if c == nil {
		return 0, engine.NewActionError(errcode.ErrConnNotFound, "")
	}
	if atomic.LoadInt32(&c.isClose) == 1 {
		return 0, engine.NewActionError(errcode.ErrConnClosed, c.serviceName)
	}
	if c.sendFunc == nil {
		stresslog.Warn("[NETWORK] Send sendFunc 未注入", zap.String("service", c.serviceName))
		return 0, engine.NewActionError(errcode.ErrSendFailed, c.serviceName)
	}

	n := len(data)
	err := c.sendFunc(data)
	if err != nil {
		stresslog.Error("[NETWORK] Send 发送失败", zap.String("service", c.serviceName), zap.Error(err))
		return 0, engine.NewActionError(errcode.ErrSendFailed, c.serviceName, err)
	}
	// 全局带宽统计
	monitor.Global().AddBandwidth(int64(n), 0)
	return n, nil
}

// RegisterListen 为指定 routeKey 注册持久化推送监听。是唯一的监听注册入口。
//
// 参数：
//   - routeKey: 路由键（由 adapter.ExpectedRouteKey 计算）。
//   - cb: nil = 缓存模式（消息进 queue，由 GetListenResp/main-flow 消费）；非 nil = 回调模式。
//   - queueSize: 缓存队列容量（>=1，cap<1 由 newListenQueue panic）。首次注册时预创建队列。
//
// 语义：
//   - 新注册：写入 listenResp，预创建 listenQueues[routeKey]（容量 queueSize），返回 nil。
//   - 幂等：同 routeKey 再注册且（queueSize 一致 && cb 是否为 nil 一致）→ 不重建队列、不报错（重写 listenResp[routeKey]=cb 为最新值，队列与 queueSize 不变；nil-cb 即纯 no-op），返回 nil。
//   - 冲突 fail-loud：同 routeKey 但 queueSize 或 cb 模式不一致 → 返回中文 error。
//
// 2-C3 起：listen 分发已并入 connectionPump，注册只是「写两张 map + 预创建队列」的一次性纯 map
// 操作，**不再启动独立 listenLoop goroutine**。pump 在 decode 后命中 listenResp 时直接调
// dispatchListen（cb!=nil 跑回调 / cb==nil 写 listenQueues）；GetListenResp 仍由主流程
// 直接 FIFO Pop（per-queue mu 串行化，与 pump 的 Push 无死锁）。
//
// c.mu 保护 listenResp / listenQueues 两个 map 的读-改 + 冲突判断（与 pump dispatchListen /
// GetListenResp 同样的锁粒度，沿用现状）。
func (c *Connection) RegisterListen(routeKey string, cb ListenCallBack, queueSize int) error {
	if c == nil {
		return fmt.Errorf("监听注册失败：连接为 nil（routeKey=%q）", routeKey)
	}
	if atomic.LoadInt32(&c.isClose) == 1 {
		return fmt.Errorf("监听注册失败：连接已关闭（service=%q routeKey=%q）", c.serviceName, routeKey)
	}

	c.mu.Lock()
	existingCb, hasCb := c.listenResp[routeKey]
	existingQ, hasQ := c.listenQueues[routeKey]

	if hasCb || hasQ {
		// 冲突检测：同 routeKey 跨次注册。
		if hasQ && existingQ.capacity != queueSize {
			c.mu.Unlock()
			return fmt.Errorf("监听注册冲突：service=%q routeKey=%q 已注册（queueSize=%d），与本次（queueSize=%d）不一致",
				c.serviceName, routeKey, existingQ.capacity, queueSize)
		}
		existingCbIsNil := existingCb == nil
		newCbIsNil := cb == nil
		if existingCbIsNil != newCbIsNil {
			c.mu.Unlock()
			return fmt.Errorf("监听注册冲突：service=%q routeKey=%q 已注册（回调=%v），与本次（回调=%v）模式不一致",
				c.serviceName, routeKey, !existingCbIsNil, !newCbIsNil)
		}
		// 幂等 no-op：保持一致写回 cb（无副作用），不重复建队列。
		c.listenResp[routeKey] = cb
		c.mu.Unlock()
		return nil
	}

	// 新注册：写入回调 + 预创建队列。
	c.listenResp[routeKey] = cb
	c.listenQueues[routeKey] = newListenQueue(queueSize)
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

func (c *Connection) dispatchListen(resp *Message) {
	c.mu.Lock()
	cb, exist := c.listenResp[resp.RouteKey]
	c.mu.Unlock()

	if !exist {
		return
	}

	if cb != nil {
		cb(resp)
	} else {
		// 缓存 listen：按需为 routeKey 创建默认容量队列并 Push。
		// c.mu 仅保护 listenQueues map 键的查找/创建；Push 在 c.mu 释放后进行，
		// 由 per-queue mu 串行化，与 GetListenResp 的 Pop 无死锁。
		c.mu.Lock()
		q, ok := c.listenQueues[resp.RouteKey]
		if !ok {
			q = newListenQueue(defaultListenQueueSize)
			c.listenQueues[resp.RouteKey] = q
		}
		c.mu.Unlock()
		if q.Push(resp) {
			// 默认容量 1：从第 2 条起每条都会触发覆盖丢弃（即旧单槽「保最新」语义）。
			// Debug 级，生产默认不刷屏；2-A2 让高频 route 配 queueSize>1 后自然减少。
			// dispatchListen 是推送热路径，按本仓惯例（OnReceive/RequestResponse 同款）
			// 用 DebugEnabled 守卫，避免每条覆盖都构造 zap 字段。
			if stresslog.DebugEnabled() {
				stresslog.Debug("[NETWORK] 监听队列已满，覆盖丢弃最旧消息",
					zap.String("service", c.serviceName),
					zap.String("routeKey", resp.RouteKey))
			}
		}
	}
}

// GetListenResp 轮询获取缓存的监听消息（FIFO pop）。
//
// c.mu 仅查 map；Pop 走队列自身 mu。默认容量 1 时与旧「读 listenMsg[k] + delete」
// 行为一致：返回最近一条 Push 并清空。
func (c *Connection) GetListenResp(routeKey string) *Message {
	if c == nil || atomic.LoadInt32(&c.isClose) == 1 {
		return nil
	}
	c.mu.Lock()
	q, ok := c.listenQueues[routeKey]
	c.mu.Unlock()
	if !ok {
		return nil
	}
	m, _ := q.Pop()
	return m
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
// 解析后注入（nil → 上层已 fail loud）；此后该连接 decode 全程用 c.adp，不再查 resolver。
//
// pump 是 network 内部调度，不泄漏到 flow/engine/Lua：外层只感知 RegisterListen /
// GetListenResp / RegisterHeartbeat 这些已存在的接口。
func (c *Connection) StartPump(adp adapter.Adapter, isUDP bool) {
	if c == nil || adp == nil {
		return
	}
	if !atomic.CompareAndSwapInt32(&c.pumpRun, 0, 1) {
		return
	}
	c.adp = adp
	c.isUDP = isUDP
	c.inboundCh = make(chan inboundFrame, inboundChSize)
	c.controlCh = make(chan pumpCmd, controlChSize)
	c.pumpDone = make(chan struct{})
	utils.GetWorkPool().Go(c.connectionPump)
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
// 唯一执行者（单 goroutine 串行，天然有序）；listenQueues 各 route queue 自带 mu，与主流程
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
	if atomic.LoadInt32(&c.isClose) == 1 {
		return
	}
	packet := c.hb.cfg.Builder()
	if packet == nil {
		return
	}
	n, err := c.Send(packet)
	if err != nil {
		stresslog.Warn("[HEARTBEAT] 发送失败",
			zap.String("service", c.serviceName), zap.Int("pktLen", len(packet)), zap.Error(err))
		return
	}
	stresslog.Debug("[HEARTBEAT] 已发送",
		zap.String("service", c.serviceName), zap.Int("pktLen", n))
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
			zap.Int("bodyLen", len(body)))
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
	if atomic.LoadInt32(&c.isClose) == 1 {
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
// 2-C3 起 decodeLoop 已并入 pump；本方法现在等 pumpDone，与 WaitListenDone 等价。
// 保留方法名是为了让 client.go 的 CloseAllWithTimeout 调用语义保持「先等 decode，再等 listen」
// 的两阶段表达（client.go 不在本任务可改文件清单内）。
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
	if !atomic.CompareAndSwapInt32(&c.isClose, 0, 1) {
		return
	}
	// 关键顺序：先写 closeReason 再 cancel，保证 RequestResponse 在 ctx.Done()
	// 后续读取时 closeReason 一定已就绪（Happens-Before 通过 atomic 保证）。
	if reason != "" {
		c.closeReason.Store(reason)
	}
	c.doClose()

	// 在 mu 下读取回调函数指针，避免与 SetOnDisconnect/SetOnClosed 的数据竞争
	c.mu.Lock()
	disconnectFn := c.onDisconnect
	closedFn := c.onClosed
	c.mu.Unlock()

	// 业务"意外断开"回调：仅非主动关闭时触发（用于 robot 主连接断开 → 停 robot）
	if atomic.LoadInt32(&c.intentionalClose) == 0 && disconnectFn != nil {
		disconnectFn()
	}
	// 监控"关闭"回调：主动/被动均触发；与 ConnEstablished 配对，保证 active = open - close 准确
	if closedFn != nil {
		closedFn()
	}

	stresslog.Debug("[NETWORK] 连接资源已清理", zap.String("service", c.serviceName), zap.String("robot", c.robotName))
}

// Close 主动关闭连接。触发 onClosed 但不触发 onDisconnect（主动关闭不算意外断开）。
func (c *Connection) Close() {
	if c == nil || !atomic.CompareAndSwapInt32(&c.isClose, 0, 1) {
		return
	}
	atomic.StoreInt32(&c.intentionalClose, 1)
	if c.closeFunc != nil {
		_ = c.closeFunc()
	}
	c.doClose()
	// CAS 保证 onClosed 只触发一次
	c.mu.Lock()
	closedFn := c.onClosed
	c.mu.Unlock()
	if closedFn != nil {
		closedFn()
	}
	stresslog.Debug("[NETWORK] 连接资源已清理", zap.String("service", c.serviceName), zap.String("robot", c.robotName))
}

// OnReceive 收到网络消息时分发到 request-response 通道或持久监听回调。
//
// 2-C3 起：本方法在 connectionPump goroutine 内被 decodeAndDispatch 同步调用（pump 是 inbound
// decode 的唯一执行者）。命中 listenResp 时不再投递到独立的 listenCh（listenLoop 已删除），
// 而是**同步直接调用 dispatchListen**——cb!=nil 跑回调、cb==nil 写 listenQueues。
// 这样 listen 分发与 decode 共享同一 pump goroutine，彻底消灭旧的 listenCh/listenLoop 链路。
//
// 热路径：每个入站包都会走一次。高频 Debug 日志构造前先做 atomic level 检查，
// 避免在 info 级别下白白构造 zap.Field 切片（每包 4 个 string field，
// 10000 连接 × 5 包/s 下能省下数百微秒 CPU/s）。
func (c *Connection) OnReceive(routeKey string, body []byte, headerErr uint64, wireBytes int, timing MessageTiming) {
	if atomic.LoadInt32(&c.isClose) == 1 {
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

	resp := NewMessage(routeKey, body, headerErr, wireBytes, timing)

	c.mu.Lock()
	ch, exists := c.responseMap[routeKey]
	if exists {
		// 在锁内发送，防止 RequestResponse 的 defer 在 unlock 和 send 之间 close(ch) 导致 panic。
		if monitor.TimingDetailAtLeast(c.timingDetail, monitor.TimingFullDetail) {
			resp.Timing.DispatchStart = time.Now()
		}
		select {
		case ch <- resp:
		default:
			stresslog.Warn("[NETWORK] OnReceive 响应通道已满", zap.String("key", routeKey))
		}
		c.mu.Unlock()
		return
	}

	_, isListen := c.listenResp[routeKey]
	c.mu.Unlock()

	if isListen {
		if monitor.TimingDetailAtLeast(c.timingDetail, monitor.TimingFullDetail) {
			resp.Timing.DispatchStart = time.Now()
		}
		// 同步分发：cb!=nil 跑回调，cb==nil 写 listenQueues（per-queue mu 串行化）。
		// pump goroutine 独占执行，与主流程 GetListenResp 的 Pop 无死锁。
		c.dispatchListen(resp)
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
