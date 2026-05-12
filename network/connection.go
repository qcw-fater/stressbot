package network

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"stressbot/monitor"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// ListenCallBack 持久化推送消息的回调函数类型
type ListenCallBack func(message *Message)

// Connection 业务层网络连接封装。
type Connection struct {
	serviceName string
	robotName   string
	secretKey   []byte

	responseMap      map[string]chan *Message  // responseKey → 临时响应通道
	listenResp       map[string]ListenCallBack // responseKey → 持久回调
	listenMsg        map[string]*Message       // responseKey → 缓存消息（轮询）
	listenCh         chan *Message
	listenDone       chan struct{} // listenLoop 退出时关闭，用于 Close 时等待回调完成
	mu               sync.Mutex
	ctx              context.Context
	cancel           context.CancelFunc
	isClose          int32
	intentionalClose int32 // 1 = 由 Close() 主动关闭，不触发断开回调
	listenRunning    int32
	requestTimeout   time.Duration
	sendFunc         func(data []byte) error
	closeFunc        func() error
	heartbeat        *heartbeatState
	heartbeatMu      sync.Mutex
	onDisconnect     func() // 连接"意外"断开时回调（非主动 Close 触发，业务用于停 robot 等）
	onClosed         func() // 连接关闭时**总是**触发的回调（主动/被动都触发，监控用）
}

// NewConnection 创建新的网络连接。
func NewConnection(serviceName, robotName string, requestTimeout time.Duration) *Connection {
	conn := &Connection{
		serviceName:    serviceName,
		robotName:      robotName,
		secretKey:      nil,
		responseMap:    make(map[string]chan *Message),
		listenResp:     make(map[string]ListenCallBack),
		listenMsg:      make(map[string]*Message),
		listenCh:       make(chan *Message, 128),
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
func (c *Connection) RequestResponse(sendData []byte, responseKey string, timeoutOverride ...time.Duration) (*Message, int) {
	if c == nil {
		return nil, 0
	}
	if atomic.LoadInt32(&c.isClose) == 1 {
		stresslog.Warn("[NETWORK] RequestResponse 连接已关闭", zap.String("service", c.serviceName), zap.String("responseKey", responseKey))
		return nil, 0
	}

	ch := make(chan *Message, 1)
	c.mu.Lock()
	c.responseMap[responseKey] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.responseMap, responseKey)
		c.mu.Unlock()
		close(ch)
	}()

	ok, n := c.Send(sendData)
	if !ok {
		stresslog.Error("[NETWORK] RequestResponse 发送失败",
			zap.String("service", c.serviceName), zap.String("responseKey", responseKey),
			zap.Int("pktLen", len(sendData)))
		return nil, 0
	}

	start := time.Now()
	timeout := c.requestTimeout
	if len(timeoutOverride) > 0 && timeoutOverride[0] > 0 {
		timeout = timeoutOverride[0]
	}
	timeoutTimer := time.After(timeout)
	select {
	case <-c.ctx.Done():
		elapsed := time.Since(start)
		stresslog.Warn("[NETWORK] RequestResponse 连接已断开",
			zap.String("service", c.serviceName), zap.String("responseKey", responseKey),
			zap.Duration("elapsed", elapsed))
		return nil, 0
	case resp := <-ch:
		elapsed := time.Since(start)
		stresslog.Debug("[NETWORK] RequestResponse 收到响应",
			zap.String("service", c.serviceName), zap.String("responseKey", responseKey),
			zap.Int("bodyLen", len(resp.Data)), zap.Duration("elapsed", elapsed))
		return resp, n
	case <-timeoutTimer:
		elapsed := time.Since(start)
		stresslog.Warn("[NETWORK] RequestResponse 等待超时",
			zap.String("service", c.serviceName), zap.String("responseKey", responseKey),
			zap.String("robot", c.robotName), zap.Duration("elapsed", elapsed),
			zap.Duration("timeout", timeout))
		return nil, 0
	}
}

// Send 异步发送数据。
func (c *Connection) Send(data []byte) (bool, int) {
	if c == nil || atomic.LoadInt32(&c.isClose) == 1 {
		return false, 0
	}
	if c.sendFunc == nil {
		stresslog.Warn("[NETWORK] Send sendFunc 未注入", zap.String("service", c.serviceName))
		return false, 0
	}

	n := len(data)
	err := c.sendFunc(data)
	if err != nil {
		stresslog.Error("[NETWORK] Send 发送失败", zap.String("service", c.serviceName), zap.Error(err))
		return false, 0
	}
	// 全局带宽统计
	monitor.Global().AddBandwidth(int64(n), 0)
	return true, n
}

// AddListener 动态添加单个监听器。
func (c *Connection) AddListener(responseKey string, cb ListenCallBack) {
	if c == nil || atomic.LoadInt32(&c.isClose) == 1 {
		return
	}

	c.mu.Lock()
	c.listenResp[responseKey] = cb
	needStart := atomic.LoadInt32(&c.listenRunning) == 0
	c.mu.Unlock()

	if needStart {
		if atomic.CompareAndSwapInt32(&c.listenRunning, 0, 1) {
			c.listenDone = make(chan struct{})
			go c.listenLoop()
		}
	}
}

func (c *Connection) listenLoop() {
	defer atomic.StoreInt32(&c.listenRunning, 0)
	defer func() {
		if c.listenDone != nil {
			close(c.listenDone)
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
	if c.listenDone != nil {
		<-c.listenDone
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
		go c.listenLoop()
	}
}

func (c *Connection) dispatchListen(resp *Message) {
	c.mu.Lock()
	cb, exist := c.listenResp[resp.ResponseKey]
	c.mu.Unlock()

	if !exist {
		return
	}

	if cb != nil {
		cb(resp)
	} else {
		c.mu.Lock()
		c.listenMsg[resp.ResponseKey] = resp
		c.mu.Unlock()
	}
}

// GetListenResp 轮询获取缓存的监听消息。
func (c *Connection) GetListenResp(responseKey string) *Message {
	if c == nil || atomic.LoadInt32(&c.isClose) == 1 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	resp, exist := c.listenMsg[responseKey]
	if exist && resp != nil {
		delete(c.listenMsg, responseKey)
		return resp
	}
	return nil
}

func (c *Connection) onClose() {
	if !atomic.CompareAndSwapInt32(&c.isClose, 0, 1) {
		return
	}
	c.StopHeartbeat()
	c.cancel()

	// 业务"意外断开"回调：仅非主动关闭时触发（用于 robot 主连接断开 → 停 robot）
	if atomic.LoadInt32(&c.intentionalClose) == 0 && c.onDisconnect != nil {
		go c.onDisconnect()
	}
	// 监控"关闭"回调：主动/被动均触发；与 ConnEstablished 配对，保证 active = open - close 准确
	if c.onClosed != nil {
		go c.onClosed()
	}

	stresslog.Debug("[NETWORK] 连接资源已清理", zap.String("service", c.serviceName), zap.String("robot", c.robotName))
}

// Close 主动关闭连接。触发 onClosed 但不触发 onDisconnect（主动关闭不算意外断开）。
func (c *Connection) Close() {
	if c == nil || !atomic.CompareAndSwapInt32(&c.isClose, 0, 1) {
		return
	}
	atomic.StoreInt32(&c.intentionalClose, 1)
	c.StopHeartbeat()
	if c.closeFunc != nil {
		_ = c.closeFunc()
	}
	c.cancel()
	// CAS 保证 onClosed 只触发一次
	if c.onClosed != nil {
		go c.onClosed()
	}
	stresslog.Debug("[NETWORK] 连接资源已清理", zap.String("service", c.serviceName), zap.String("robot", c.robotName))
}

// OnReceive 收到网络消息时分发到 request-response 通道或持久监听回调。
func (c *Connection) OnReceive(responseKey string, body []byte, headerErr uint64) {
	if atomic.LoadInt32(&c.isClose) == 1 {
		return
	}
	if headerErr != 0 {
		stresslog.Error("[NETWORK] 服务端协议头错误码非零",
			zap.String("service", c.serviceName),
			zap.String("key", responseKey),
			zap.Uint64("headerErr", headerErr))
	}

	stresslog.Debug("[NETWORK] OnReceive",
		zap.String("service", c.serviceName), zap.String("responseKey", responseKey),
		zap.Int("bodyLen", len(body)))

	resp := NewMessage(responseKey, body)

	c.mu.Lock()
	ch, exists := c.responseMap[responseKey]
	if exists {
		// 在锁内发送，防止 RequestResponse 的 defer 在 unlock 和 send 之间 close(ch) 导致 panic。
		select {
		case ch <- resp:
		default:
			stresslog.Warn("[NETWORK] OnReceive 响应通道已满", zap.String("key", responseKey))
		}
		c.mu.Unlock()
		return
	}

	_, exists = c.listenResp[responseKey]
	if exists {
		c.mu.Unlock()
		select {
		case c.listenCh <- resp:
		default:
			stresslog.Warn("[NETWORK] OnReceive 监听通道已满", zap.String("key", responseKey))
		}
		return
	}

	c.mu.Unlock()
}
