package network

import (
	"context"
	"maps"
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

// Connection 业务层网络连接封装。
// 封装 request-response 匹配、持久化推送监听、心跳和连接生命周期回调。
type Connection struct {
	serviceName string // 所属服务名称（如 "logic"、"battle"）
	robotName   string // 所属机器人账号名
	// secretKey 通信加密密钥，存 []byte。按 immutable 约定使用：SetSecretKey 整体替换为
	// 新副本，GetSecretKey 返回的切片只读、不得修改。避免每个收发包都加锁复制一次密钥。
	secretKey atomic.Value

	responseMap      map[string]chan *Message  // routeKey → 临时响应通道（RequestResponse 用）
	listenResp       map[string]ListenCallBack // routeKey → 持久化推送回调
	listenMsg        map[string]*Message       // routeKey → 缓存消息（轮询模式，回调为 nil 时）
	listenCh         chan *Message             // 推送消息分发通道
	listenDone       chan struct{}             // listenLoop 退出信号，用于 Close 时等待回调完成
	mu               sync.Mutex                // 保护 responseMap / listenResp / listenMsg / 回调字段
	ctx              context.Context           // 连接生命周期上下文
	cancel           context.CancelFunc        // 取消函数，关闭时调用
	isClose          int32                     // 原子标记：0=活跃，1=已关闭
	intentionalClose int32                     // 原子标记：1=主动 Close() 触发，不触发 onDisconnect
	listenRunning    int32                     // 原子标记：listenLoop 是否运行中
	requestTimeout   time.Duration             // RequestResponse 默认超时
	sendFunc         func(data []byte) error   // 底层发送函数（由 Dialer 注入）
	closeFunc        func() error              // 底层关闭函数（由 Dialer 注入）
	heartbeat        *heartbeatState           // 心跳运行时状态
	heartbeatMu      sync.Mutex                // 保护 heartbeat 字段的替换
	onDisconnect     func()                    // 意外断开回调（非主动 Close 触发，业务用于停 robot）
	onClosed         func()                    // 关闭回调（主动/被动均触发，监控用，与 ConnEstablished 配对）

	// 异步解码（gnet 事件循环 → per-connection goroutine）
	// 设计意图：把 Lua Decode 从 gnet event loop 摘除，避免少数慢 decode
	// 阻塞同 loop 上其他连接的 I/O 处理。详见 decodeLoop 注释。
	adp        adapter.Adapter   // decode 用的协议适配器，StartDecodeLoop 时注入
	isUDP      bool              // 该连接是否 UDP（决定调 DecodeUDP / DecodeTCP）
	decodeCh   chan inboundFrame // 待解码的 raw msg buffer（OnTraffic 投递→decode goroutine 消费）
	decodeDone chan struct{}     // decode goroutine 退出信号
	decodeRun  int32             // 原子标记：1 表示 decodeLoop 已启动（CAS 防重复启动）

	// closeReason 记录连接关闭原因，供 inflight RequestResponse 命中 ctx.Done() 时归因。
	// 由 onClose() 写入（gnet 给的 err 字符串），RequestResponse 读取后拼到错误 detail。
	// atomic.Value 保证读写无锁；写入只发生一次（与 isClose CAS 在同一路径下）。
	closeReason atomic.Value // string

	timingDetail monitor.TimingDetailLevel // 计时细分级别，控制 decode/dispatch 时间点是否记录
}

const (
	listenChSize = 128 // 监听推送消息通道缓冲区大小
	decodeChSize = 256 // decode 通道缓冲区大小，满则反压（关闭连接）
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
		listenMsg:      make(map[string]*Message),
		listenCh:       make(chan *Message, listenChSize),
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
// 返回的切片为只读快照，调用方不得修改其内容（adapter 仅将其传给 Lua 并 stringify 复制）。
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

// AddListener 动态添加单个监听器。
func (c *Connection) AddListener(routeKey string, cb ListenCallBack) {
	if c == nil || atomic.LoadInt32(&c.isClose) == 1 {
		return
	}

	c.mu.Lock()
	c.listenResp[routeKey] = cb
	needStart := atomic.LoadInt32(&c.listenRunning) == 0
	c.mu.Unlock()

	if needStart {
		if atomic.CompareAndSwapInt32(&c.listenRunning, 0, 1) {
			c.listenDone = make(chan struct{})
			utils.GetWorkPool().Go(c.listenLoop)
		}
	}
}

func (c *Connection) listenLoop() {
	defer atomic.StoreInt32(&c.listenRunning, 0)
	defer func() {
		c.mu.Lock()
		ch := c.listenDone
		c.listenDone = nil
		c.mu.Unlock()
		if ch != nil {
			close(ch)
		}
	}()
	for {
		select {
		case <-c.ctx.Done():
			c.mu.Lock()
			clear(c.listenResp)
			clear(c.listenMsg)
			c.mu.Unlock()
			return
		case resp, ok := <-c.listenCh:
			if !ok {
				return
			}
			c.dispatchListen(resp)
		}
	}
}

// WaitListenDone 等待 listenLoop 退出（即所有已分发的回调执行完毕）。
// 必须在 Close/CloseAll 之后调用，此时 ctx 已取消，listenLoop 会尽快退出。
func (c *Connection) WaitListenDone() {
	if c == nil {
		return
	}
	c.mu.Lock()
	ch := c.listenDone
	c.mu.Unlock()
	if ch != nil {
		<-ch
	}
}

// WaitListenDoneTimeout 带超时等待 listenLoop 退出。
func (c *Connection) WaitListenDoneTimeout(timeout time.Duration) bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	ch := c.listenDone
	c.mu.Unlock()
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

// ListenResponse 注册持久化推送消息监听。
func (c *Connection) ListenResponse(listenRespMap map[string]ListenCallBack) {
	if c == nil || atomic.LoadInt32(&c.isClose) == 1 {
		return
	}

	c.mu.Lock()
	maps.Copy(c.listenResp, listenRespMap)
	c.mu.Unlock()

	if atomic.CompareAndSwapInt32(&c.listenRunning, 0, 1) {
		c.listenDone = make(chan struct{})
		utils.GetWorkPool().Go(c.listenLoop)
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
		c.mu.Lock()
		c.listenMsg[resp.RouteKey] = resp
		c.mu.Unlock()
	}
}

// GetListenResp 轮询获取缓存的监听消息。
func (c *Connection) GetListenResp(routeKey string) *Message {
	if c == nil || atomic.LoadInt32(&c.isClose) == 1 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	resp, exist := c.listenMsg[routeKey]
	if exist && resp != nil {
		delete(c.listenMsg, routeKey)
		return resp
	}
	return nil
}

// StartDecodeLoop 启动异步 decode goroutine（每个连接独占一个）。
//
// 作用：把 Lua Decode 从 gnet 事件循环上摘除。
// 历史问题：OnTraffic 在 gnet event loop goroutine 上同步调 adp.DecodeTCP/UDP（走 Lua），
// 单帧解密在 gopher-lua 上耗时百微秒~毫秒级；在 10000+ 连接稳态下，少数慢 decode 会让
// event loop 串行卡顿，该 loop 上所有连接的心跳响应/请求响应被延迟，最终被服务端判定为
// 掉线 → CONN_DROPPED 雪崩。
//
// 现行设计：
//   - OnTraffic 只做纯 Go 帧切割，把 raw msgBuf 投递到本连接的 decodeCh
//   - decodeLoop 在独立 goroutine 内串行消费 channel（保证同连接消息顺序）
//   - 多连接之间 decode 完全并行，能充分压榨 LState 池
//   - event loop 永不接触 Lua，吞吐量提升一个数量级
//
// 必须在 Dial 成功且 conn 注册到 registry 之后、首次 OnTraffic 可能到达之前调用。
// CAS 防止 reconnect 场景下重复启动。
func (c *Connection) StartDecodeLoop(adp adapter.Adapter, isUDP bool) {
	if c == nil || adp == nil {
		return
	}
	if !atomic.CompareAndSwapInt32(&c.decodeRun, 0, 1) {
		return
	}
	c.adp = adp
	c.isUDP = isUDP
	c.decodeCh = make(chan inboundFrame, decodeChSize)
	c.decodeDone = make(chan struct{})
	utils.GetWorkPool().Go(c.decodeLoop)
}

// decodeLoop 异步消费 decodeCh，逐帧调用 Lua decode 并分发到 OnReceive。
//
// 退出条件：ctx.Done()（连接主动/被动关闭都会触发 cancel）。
// 退出时：尽量排空通道并归还 buffer 池，避免泄漏；OnReceive 在 isClose==1 后会直接 return，
// 排空的几个包丢弃即可（连接已关，调用方不再期待这些响应）。
func (c *Connection) decodeLoop() {
	defer close(c.decodeDone)
	for {
		select {
		case <-c.ctx.Done():
			for {
				select {
				case frame, ok := <-c.decodeCh:
					if !ok {
						return
					}
					putMsgBuf(frame.Data)
				default:
					return
				}
			}
		case frame, ok := <-c.decodeCh:
			if !ok {
				return
			}
			c.decodeAndDispatch(frame)
		}
	}
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
	EnqueueClosed                      // 连接已关闭 / 尚未启动 decode：静默丢包，调用方不应再 Close
	EnqueueChFull                      // decodeCh 真满：触发反压，调用方应关闭连接释放资源
)

// EnqueueRaw 由 gnet OnTraffic 调用，把 raw 包投递到 decode goroutine。
//
// 三态返回让调用方区分"连接已关"和"真反压"，避免停止阶段把 close 后还在路上的
// inbound 字节当成 decode 满。只有 EnqueueChFull 是需要警示的真问题。
func (c *Connection) EnqueueRaw(msgBuf []byte, recvFrameAt time.Time) EnqueueResult {
	if c == nil {
		return EnqueueClosed
	}
	if atomic.LoadInt32(&c.isClose) == 1 {
		return EnqueueClosed
	}
	if c.decodeCh == nil {
		// StartDecodeLoop 还没调用（极端启动竞态）：帧丢弃，业务层会超时/重发。
		return EnqueueClosed
	}
	select {
	case c.decodeCh <- inboundFrame{Data: msgBuf, WireBytes: len(msgBuf), RecvFrameAt: recvFrameAt}:
		return EnqueueOK
	default:
		return EnqueueChFull
	}
}

// WaitDecodeDone 等待 decode goroutine 退出。
// 必须在 Close()/onClose() 之后调用（ctx 已 cancel，decodeLoop 会尽快退出）。
func (c *Connection) WaitDecodeDone() {
	if c == nil {
		return
	}
	if c.decodeDone != nil {
		<-c.decodeDone
	}
}

// WaitDecodeDoneTimeout 带超时等待 decode goroutine 退出。
func (c *Connection) WaitDecodeDoneTimeout(timeout time.Duration) bool {
	if c == nil || c.decodeDone == nil {
		return true
	}
	if timeout <= 0 {
		select {
		case <-c.decodeDone:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-c.decodeDone:
		return true
	case <-timer.C:
		return false
	}
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
// **关键顺序**：必须先 cancel 再 StopHeartbeat。
// 原因：心跳 Builder 内部会重新进入 Lua VM 抢 luaMu，
// 如果 cancel 在后面，Builder 不知道连接已在关闭，可能与持有 luaMu 的执行器
// 形成"executor 等心跳退出 ↔ 心跳等 luaMu"循环死锁。
// cancel 优先后：
//  1. Builder 入口的 `ctx.Err() != nil` 会立即返回 nil，跳过 Lua 调用；
//  2. listenLoop 的 select 看到 ctx.Done 立即退出，回调不再分发；
//  3. decodeLoop 的 select 看到 ctx.Done 立即退出，排空通道归还 buffer。
//
// 双重保险（参见 heartbeat.go 的 TryLock 兜底）后，StopHeartbeat 不会被卡死。
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

	_, exists = c.listenResp[routeKey]
	if exists {
		c.mu.Unlock()
		if monitor.TimingDetailAtLeast(c.timingDetail, monitor.TimingFullDetail) {
			resp.Timing.DispatchStart = time.Now()
		}
		select {
		case c.listenCh <- resp:
		default:
			stresslog.Warn("[NETWORK] OnReceive 监听通道已满", zap.String("key", routeKey))
		}
		return
	}

	c.mu.Unlock()
	if stresslog.DebugEnabled() {
		stresslog.Debug("[NETWORK] OnReceive 未匹配任何请求或监听",
			zap.String("service", c.serviceName),
			zap.String("routeKey", routeKey),
			zap.String("robot", c.robotName),
			zap.Int("bodyLen", len(body)))
	}
}
