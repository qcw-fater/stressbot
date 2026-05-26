package network

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

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
	serviceName string          // 所属服务名称（如 "logic"、"battle"）
	robotName   string          // 所属机器人账号名
	secretKey   []byte          // 通信加密密钥

	responseMap      map[string]chan *Message  // routeKey → 临时响应通道（RequestResponse 用）
	listenResp       map[string]ListenCallBack // routeKey → 持久化推送回调
	listenMsg        map[string]*Message       // routeKey → 缓存消息（轮询模式，回调为 nil 时）
	listenCh         chan *Message              // 推送消息分发通道
	listenDone       chan struct{}              // listenLoop 退出信号，用于 Close 时等待回调完成
	mu               sync.Mutex                 // 保护 responseMap / listenResp / listenMsg / 回调字段
	ctx              context.Context            // 连接生命周期上下文
	cancel           context.CancelFunc         // 取消函数，关闭时调用
	isClose          int32                      // 原子标记：0=活跃，1=已关闭
	intentionalClose int32                      // 原子标记：1=主动 Close() 触发，不触发 onDisconnect
	listenRunning    int32                      // 原子标记：listenLoop 是否运行中
	requestTimeout   time.Duration              // RequestResponse 默认超时
	sendFunc         func(data []byte) error    // 底层发送函数（由 Dialer 注入）
	closeFunc        func() error               // 底层关闭函数（由 Dialer 注入）
	heartbeat        *heartbeatState            // 心跳运行时状态
	heartbeatMu      sync.Mutex                 // 保护 heartbeat 字段的替换
	onDisconnect     func()                     // 意外断开回调（非主动 Close 触发，业务用于停 robot）
	onClosed         func()                     // 关闭回调（主动/被动均触发，监控用，与 ConnEstablished 配对）
}

const listenChSize = 128 // 监听推送消息通道缓冲区大小

// NewConnection 创建新的网络连接。
func NewConnection(serviceName, robotName string, requestTimeout time.Duration) *Connection {
	conn := &Connection{
		serviceName:    serviceName,
		robotName:      robotName,
		secretKey:      nil,
		responseMap:    make(map[string]chan *Message),
		listenResp:     make(map[string]ListenCallBack),
		listenMsg:      make(map[string]*Message),
		listenCh:       make(chan *Message, listenChSize),
		requestTimeout: requestTimeout,
		sendFunc:       nil,
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
func (c *Connection) SetSecretKey(key []byte) {
	if c == nil || atomic.LoadInt32(&c.isClose) == 1 {
		return
	}
	if len(key) == 0 {
		return
	}
	c.mu.Lock()
	if c.secretKey == nil || len(c.secretKey) != len(key) {
		c.secretKey = make([]byte, len(key))
	}
	copy(c.secretKey, key)
	c.mu.Unlock()
}

// GetSecretKey 获取通信加密密钥的副本。
func (c *Connection) GetSecretKey() []byte {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.secretKey == nil {
		c.mu.Unlock()
		return nil
	}
	key := make([]byte, len(c.secretKey))
	copy(key, c.secretKey)
	c.mu.Unlock()
	return key
}

// RequestResponse 发送请求并同步等待响应。
//
// 返回值 netLatency 是"Send 完成 → 收到响应（或超时/断连）"的纯网络往返时长，
// 不含调用方在 Go 侧的 proto 构建 / 加密编码 / 响应解析等客户端开销。
// 所有结果分支（成功 / 超时 / ctx 取消）均返回有效的 netLatency；发送阶段失败时返回 0。
func (c *Connection) RequestResponse(sendData []byte, routeKey string, timeoutOverride ...time.Duration) (*Message, time.Duration, error) {
	if c == nil {
		return nil, 0, engine.NewActionError(errcode.ErrConnNotFound, "routeKey="+routeKey)
	}
	if atomic.LoadInt32(&c.isClose) == 1 {
		stresslog.Warn("[NETWORK] RequestResponse 连接已关闭", zap.String("service", c.serviceName), zap.String("routeKey", routeKey), zap.String("robot", c.robotName))
		return nil, 0, engine.NewActionError(errcode.ErrConnClosed, c.serviceName+" routeKey="+routeKey)
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

	// netStart 紧贴 Send 之前；后续所有结果分支测量到此为止的 elapsed
	// 作为该次请求的"纯网络往返时间"上报给 monitor。
	netStart := time.Now()
	n, sendErr := c.Send(sendData)
	if sendErr != nil {
		stresslog.Error("[NETWORK] RequestResponse 发送失败",
			zap.String("service", c.serviceName), zap.String("routeKey", routeKey),
			zap.String("robot", c.robotName),
			zap.Int("pktLen", len(sendData)))
		return nil, 0, sendErr
	}
	_ = n

	timeout := c.requestTimeout
	if len(timeoutOverride) > 0 && timeoutOverride[0] > 0 {
		timeout = timeoutOverride[0]
	}
	timeoutTimer := time.After(timeout)
	select {
	case <-c.ctx.Done():
		elapsed := time.Since(netStart)
		stresslog.Warn("[NETWORK] RequestResponse 连接已断开",
			zap.String("service", c.serviceName), zap.String("routeKey", routeKey),
			zap.String("robot", c.robotName),
			zap.Duration("elapsed", elapsed))
		return nil, elapsed, engine.NewActionError(errcode.ErrConnDropped, c.serviceName+" routeKey="+routeKey)
	case resp := <-ch:
		elapsed := time.Since(netStart)
		stresslog.Debug("[NETWORK] RequestResponse 收到响应",
			zap.String("service", c.serviceName), zap.String("routeKey", routeKey),
			zap.String("robot", c.robotName),
			zap.Int("bodyLen", len(resp.Data)), zap.Duration("elapsed", elapsed))
		return resp, elapsed, nil
	case <-timeoutTimer:
		elapsed := time.Since(netStart)
		stresslog.Warn("[NETWORK] RequestResponse 等待超时",
			zap.String("service", c.serviceName), zap.String("routeKey", routeKey),
			zap.String("robot", c.robotName),
			zap.Duration("timeout", timeout))
		return nil, elapsed, engine.NewTimeoutError(errcode.ErrRecvTimeout, c.serviceName+" routeKey="+routeKey+" timeout="+timeout.String())
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

// ListenResponse 注册持久化推送消息监听。
func (c *Connection) ListenResponse(listenRespMap map[string]ListenCallBack) {
	if c == nil || atomic.LoadInt32(&c.isClose) == 1 {
		return
	}

	c.mu.Lock()
	for k, v := range listenRespMap {
		c.listenResp[k] = v
	}
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

// doClose 执行连接关闭的共享逻辑：取消上下文 + 停止心跳。
// 调用方负责触发回调。
//
// **关键顺序**：必须先 cancel 再 StopHeartbeat。
// 原因：心跳 Builder 内部会重新进入 Lua VM 抢 luaMu，
// 如果 cancel 在后面，Builder 不知道连接已在关闭，可能与持有 luaMu 的执行器
// 形成"executor 等心跳退出 ↔ 心跳等 luaMu"循环死锁。
// cancel 优先后：
//  1. Builder 入口的 `ctx.Err() != nil` 会立即返回 nil，跳过 Lua 调用；
//  2. listenLoop 的 select 看到 ctx.Done 立即退出，回调不再分发。
// 双重保险（参见 heartbeat.go 的 TryLock 兜底）后，StopHeartbeat 不会被卡死。
func (c *Connection) doClose() {
	c.cancel()
	c.StopHeartbeat()
}

// onClose gnet 异步触发的关闭回调（OnClose 事件）。
// 与 Close() 构成双路径：
//   - Close()：主动关闭，先 CAS 设置 isClose=1，再执行 doClose + 触发 onClosed（不触发 onDisconnect）
//   - onClose()：被动关闭（gnet OnClose），同样 CAS，执行 doClose + 触发 onDisconnect + onClosed
// 两者通过 isClose CAS 互斥，保证 doClose 和回调只执行一次。
func (c *Connection) onClose() {
	if !atomic.CompareAndSwapInt32(&c.isClose, 0, 1) {
		return
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
func (c *Connection) OnReceive(routeKey string, body []byte, headerErr uint64) {
	if atomic.LoadInt32(&c.isClose) == 1 {
		return
	}
	if headerErr != 0 {
		stresslog.Error("[NETWORK] 服务端协议头错误码非零",
			zap.String("service", c.serviceName),
			zap.String("key", routeKey),
			zap.Uint64("headerErr", headerErr))
	}

	stresslog.Debug("[NETWORK] OnReceive",
		zap.String("service", c.serviceName), zap.String("routeKey", routeKey),
			zap.String("robot", c.robotName),
		zap.Int("bodyLen", len(body)))

	resp := NewMessage(routeKey, body, headerErr)

	c.mu.Lock()
	ch, exists := c.responseMap[routeKey]
	if exists {
		// 在锁内发送，防止 RequestResponse 的 defer 在 unlock 和 send 之间 close(ch) 导致 panic。
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
		select {
		case c.listenCh <- resp:
		default:
			stresslog.Warn("[NETWORK] OnReceive 监听通道已满", zap.String("key", routeKey))
		}
		return
	}

	c.mu.Unlock()
	stresslog.Debug("[NETWORK] OnReceive 未匹配任何请求或监听",
		zap.String("service", c.serviceName),
		zap.String("routeKey", routeKey),
			zap.String("robot", c.robotName),
		zap.Int("bodyLen", len(body)))
}

