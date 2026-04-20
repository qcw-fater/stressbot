// Package network 提供基于 gnet 的高性能网络层。
// Connection 管理业务层网络连接，通过 channel 实现 RequestResponse 同步等待模式。
// 底层 I/O 由 gnet 事件循环驱动，通过 sendFunc 注入实现解耦。
package network

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// ListenCallBack 持久化推送消息的回调函数类型
type ListenCallBack func(message *Message)

// Connection 业务层网络连接封装。
// 底层 I/O 由 gnet 事件循环驱动，业务层通过 channel 实现 RequestResponse 同步等待。
// 与 Robot 版本的区别：使用泛型 Protocol 进行消息头编解码，而非硬编码消息格式。
type Connection struct {
	serviceName   string                  // 连接的服务名称
	robotName     string                  // 机器人名称
	secretKey     []byte                  // 通信加密密钥（32 字节）
	responseMap   map[int]chan *Message   // CmdAct -> 响应通道，用于 RequestResponse 模式
	listenResp    map[int]ListenCallBack  // CmdAct -> 回调函数，用于持久化推送监听
	listenCh      chan *Message           // 监听消息通道（缓冲 128）
	listenMsg     map[int]*Message        // 缓存的监听消息（轮询获取）
	mu            sync.Mutex              // 保护 responseMap、listenResp、listenMsg
	ctx           context.Context         // 上下文控制
	cancel        context.CancelFunc      // 取消函数
	isClose       int32                   // 连接是否已关闭（原子操作，CAS 保护）
	listenRunning int32                   // ListenResponse 协程是否已启动（原子操作）
	protocol      *Protocol               // 消息头编解码器
	sendFunc      func(data []byte) error // 发送函数，由 gnet 层注入
	closeFunc     func() error            // 关闭底层 gnet 连接的函数
	heartbeat     *heartbeatState         // 当前心跳状态（可选）
	heartbeatMu   sync.Mutex              // 保护 heartbeat
}

// NewConnection 创建新的网络连接（外部使用）。
// robotName 为机器人名称，serviceName 为目标服务名称。
// sendFunc 暂时为 nil，后续由 gnet OnOpen 或 Dial 时注入。
func NewConnection(serviceName, robotName string, protocol *Protocol) *Connection {
	conn := &Connection{
		serviceName: serviceName,
		robotName:   robotName,
		// 密钥在 SetSecretKey 被显式调用后才会分配。
		// 避免初始 32 字节零值被误判为"已设置密钥"而触发加密。
		secretKey:   nil,
		responseMap: make(map[int]chan *Message),
		listenResp:  make(map[int]ListenCallBack),
		listenMsg:   make(map[int]*Message),
		listenCh:    make(chan *Message, 128),
		protocol:    protocol,
		sendFunc:    nil,
	}
	conn.ctx, conn.cancel = context.WithCancel(context.Background())
	stresslog.Debug("[NETWORK] NewConnection", zap.String("service", serviceName), zap.String("robot", robotName))
	return conn
}

// SetSecretKey 设置通信加密密钥。
// 密钥长度必须为 32 字节，超出部分截断。
// 空密钥（len==0）被忽略以防止误清除已有密钥。
func (c *Connection) SetSecretKey(key []byte) {
	if c == nil || atomic.LoadInt32(&c.isClose) == 1 {
		return
	}
	if len(key) == 0 {
		return
	}
	if c.secretKey == nil {
		c.secretKey = make([]byte, 32)
	}
	copy(c.secretKey, key)
}

// GetSecretKey 获取通信加密密钥的副本。
// 未设置密钥时返回 nil（而非全零），供上层按 len==0 判定跳过加密。
func (c *Connection) GetSecretKey() []byte {
	if c == nil || c.secretKey == nil {
		return nil
	}
	key := make([]byte, len(c.secretKey))
	copy(key, c.secretKey)
	return key
}

// RequestResponse 发送请求并同步等待响应。
// 通过 channel + select 实现同步等待，超时时间为 1 分钟。
// sendData 为完整的待发送数据（含消息头 + 消息体）。
// responseId 为期望响应的 CmdAct 路由键。
// 返回响应消息和发送字节数；超时或连接关闭返回 nil, 0。
func (c *Connection) RequestResponse(sendData []byte, responseId int) (*Message, int) {
	if c == nil || atomic.LoadInt32(&c.isClose) == 1 {
		stresslog.Warn("[NETWORK] RequestResponse 连接已关闭", zap.String("service", c.serviceName))
		return nil, 0
	}

	// 创建并注册临时响应通道
	ch := make(chan *Message, 1)
	c.mu.Lock()
	c.responseMap[responseId] = ch
	c.mu.Unlock()

	// 保证清理资源
	defer func() {
		c.mu.Lock()
		delete(c.responseMap, responseId)
		c.mu.Unlock()
		close(ch)
	}()

	// 发送请求
	ok, n := c.Send(sendData)
	if !ok {
		stresslog.Error("[NETWORK] RequestResponse 发送失败", zap.String("service", c.serviceName), zap.Int("responseId", responseId))
		return nil, 0
	}

	// 超时等待响应
	timeout := time.After(1 * time.Minute)
	select {
	case <-c.ctx.Done():
		// 连接已关闭
		return nil, 0
	case resp := <-ch:
		// 收到目标响应
		return resp, n
	case <-timeout:
		cmd := responseId >> 8
		act := responseId & 0xFF
		stresslog.Warn("[NETWORK] RequestResponse 等待超时",
			zap.String("service", c.serviceName), zap.Int("cmd", cmd), zap.Int("act", act), zap.String("robot", c.robotName))
		return nil, 0
	}
}

// Send 异步发送数据。
// data 为完整的待发送字节数据（含消息头 + 消息体）。
// 返回是否发送成功和发送字节数。
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
	return true, n
}

// AddListener 动态添加单个监听器。
// 如果 ListenResponse 协程尚未启动，自动启动。
// 用于在连接建立后动态注册 battle 等连接的推送消息监听。
func (c *Connection) AddListener(cmdAct int, cb ListenCallBack) {
	if c == nil || atomic.LoadInt32(&c.isClose) == 1 {
		return
	}

	c.mu.Lock()
	c.listenResp[cmdAct] = cb
	needStart := atomic.LoadInt32(&c.listenRunning) == 0
	c.mu.Unlock()

	if needStart {
		if atomic.CompareAndSwapInt32(&c.listenRunning, 0, 1) {
			go c.listenLoop()
		}
	}
}

// listenLoop 监听消息分发循环（内部方法）。
// 由 ListenResponse 或 AddListener 首次触发启动。
func (c *Connection) listenLoop() {
	defer atomic.StoreInt32(&c.listenRunning, 0)
	for {
		select {
		case <-c.ctx.Done():
			c.mu.Lock()
			close(c.listenCh)
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

// ListenResponse 注册持久化推送消息监听。
// listenRespMap 为 CmdAct -> 回调函数的映射，收到对应消息后通过回调处理。
// 启动 listenLoop 协程处理消息分发。
func (c *Connection) ListenResponse(listenRespMap map[int]ListenCallBack) {
	if c == nil || atomic.LoadInt32(&c.isClose) == 1 {
		return
	}

	// 注册持久化监听回调
	c.mu.Lock()
	for k, v := range listenRespMap {
		c.listenResp[k] = v
	}
	c.mu.Unlock()

	// 启动监听协程（仅启动一次）
	if atomic.CompareAndSwapInt32(&c.listenRunning, 0, 1) {
		go c.listenLoop()
	}
}

// dispatchListen 分发监听消息到对应的回调函数。
// 在 ListenResponse 的循环中被调用。
func (c *Connection) dispatchListen(resp *Message) {
	c.mu.Lock()
	cb, exist := c.listenResp[resp.CmdAct()]
	c.mu.Unlock()

	if !exist {
		return
	}

	if cb != nil {
		// 执行回调
		cb(resp)
	} else {
		// 回调为 nil，缓存当前监听消息（供 GetListenResp 轮询获取）
		c.mu.Lock()
		c.listenMsg[resp.CmdAct()] = resp
		c.mu.Unlock()
	}
}

// GetListenResp 轮询获取缓存的监听消息。
// responseId 为 CmdAct 路由键。
// 返回缓存的 Message，获取后从缓存中移除；不存在返回 nil。
func (c *Connection) GetListenResp(responseId int) *Message {
	if c == nil || atomic.LoadInt32(&c.isClose) == 1 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	resp, exist := c.listenMsg[responseId]
	if exist && resp != nil {
		delete(c.listenMsg, responseId)
		return resp
	}
	return nil
}

// onClose 连接关闭时的内部清理逻辑。
// 使用 CAS 保证只执行一次，取消上下文通知所有等待者。
func (c *Connection) onClose() {
	if !atomic.CompareAndSwapInt32(&c.isClose, 0, 1) {
		return // 已经关闭过
	}
	c.StopHeartbeat()
	c.cancel()
	stresslog.Debug("[NETWORK] 连接资源已清理", zap.String("service", c.serviceName), zap.String("robot", c.robotName))
}

// Close 主动关闭连接（由业务层调用）。
// 触发底层 gnet 连接关闭，随后 OnClose 回调会清理资源。
func (c *Connection) Close() {
	if c == nil || atomic.LoadInt32(&c.isClose) == 1 {
		return
	}
	// 先停止心跳和标记
	c.StopHeartbeat()
	// 主动标记关闭，防止后续操作
	if c.closeFunc != nil {
		_ = c.closeFunc()
	}
	c.onClose()
}

// BuildPacket 便捷方法：使用本连接的密钥构建报文
func (c *Connection) BuildPacket(cmd, act uint8, body []byte) []byte {
	return c.protocol.BuildPacket(cmd, act, body, c.secretKey)
}

// OnReceive 收到网络消息时的分发入口。
// 由 gnet OnTraffic 回调调用，将消息分发到 responseMap 或 listenCh。
// head 为解码后的消息头，body 为消息体原始字节。
func (c *Connection) OnReceive(head *HeadDecode, body []byte) {
	if head == nil {
		return
	}

	resp := NewMessage(head, body)
	responseId := head.CmdAct

	c.mu.Lock()
	// 优先检查是否为 RequestResponse 的等待响应
	ch, exists := c.responseMap[responseId]
	if exists {
		c.mu.Unlock()
		// 投递到临时响应通道
		select {
		case ch <- resp:
		default:
			// 防止通道阻塞（理论上不会发生，ch 容量为 1）
			stresslog.Warn("[NETWORK] OnReceive 响应通道已满",
				zap.Uint8("cmd", head.Cmd), zap.Uint8("act", head.Act), zap.String("robot", c.robotName))
		}
		return
	}

	// 检查是否为持久化监听消息
	_, exists = c.listenResp[responseId]
	if exists {
		c.mu.Unlock()
		select {
		case c.listenCh <- resp:
		default:
			// 监听通道已满，丢弃消息
			stresslog.Warn("[NETWORK] OnReceive 监听通道已满",
				zap.Uint8("cmd", head.Cmd), zap.Uint8("act", head.Act), zap.String("robot", c.robotName))
		}
		return
	}

	c.mu.Unlock()
}
