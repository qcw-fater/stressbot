package network

import (
	"context"
	"fmt"
	"sync"
	"time"

	stresslog "stressbot/utils/log"

	"github.com/panjf2000/gnet/v2"
	"go.uber.org/zap"

	"stressbot/adapter"
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
type EventServer struct {
	gnet.BuiltinEventEngine

	registry     *connRegistry
	adp          adapter.Adapter
	tickInterval time.Duration
}

// NewEventServer 创建 gnet 事件处理器
func NewEventServer(adp adapter.Adapter, heartbeatInterval time.Duration) *EventServer {
	return &EventServer{
		registry:     newConnRegistry(),
		adp:          adp,
		tickInterval: heartbeatInterval,
	}
}

// OnOpen gnet 连接建立回调
func (es *EventServer) OnOpen(gconn gnet.Conn) ([]byte, gnet.Action) {
	return nil, gnet.None
}

// OnClose gnet 连接关闭回调。
func (es *EventServer) OnClose(gconn gnet.Conn, err error) gnet.Action {
	conn := es.registry.get(gconn)
	if conn != nil {
		es.registry.unregister(gconn)
		conn.onClose()
	}
	return gnet.None
}

// OnTraffic gnet 收到数据回调。
// 使用 adapter 接口的纯 Go 方法做帧分割，Lua 仅在 Decode 时调用。
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
		if bodyLen < 0 {
			if conn != nil {
				stresslog.Warn("[NETWORK] 协议头非法，关闭连接",
					zap.String("service", conn.ServiceName()))
			}
			return gnet.Close
		}

		totalLen := headSize + bodyLen
		if available < totalLen {
			return gnet.None
		}

		msgBuf := make([]byte, totalLen)
		if _, err = gconn.Read(msgBuf); err != nil {
			stresslog.Error("[GNET] 读取消息失败", zap.Error(err))
			return gnet.None
		}

		if conn != nil {
			secretKey := conn.GetSecretKey()
			responseKey, body, headerErr := es.adp.Decode(msgBuf, secretKey)
			if responseKey != "" {
				conn.OnReceive(responseKey, body, headerErr)
			}
		}
	}
}

// OnTick gnet 定时回调。
func (es *EventServer) OnTick() (delay time.Duration, action gnet.Action) {
	return es.tickInterval, gnet.None
}

// Dialer 管理 gnet 客户端。
type Dialer struct {
	client *gnet.Client
	server *EventServer
}

// NewDialer 创建拨号器
func NewDialer(adp adapter.Adapter, heartbeatInterval time.Duration) *Dialer {
	server := NewEventServer(adp, heartbeatInterval)
	return &Dialer{
		server: server,
	}
}

// Start 启动 gnet 客户端引擎
func (d *Dialer) Start() error {
	opts := []gnet.Option{
		gnet.WithTicker(true),
		gnet.WithReadBufferCap(64 * 1024),
		gnet.WithWriteBufferCap(64 * 1024),
	}
	client, err := gnet.NewClient(d.server, opts...)
	if err != nil {
		return fmt.Errorf("创建 gnet 客户端失败: %w", err)
	}
	d.client = client

	if err = d.client.Start(); err != nil {
		return fmt.Errorf("启动 gnet 客户端失败: %w", err)
	}

	stresslog.Debug("[GNET] 客户端引擎已启动")
	return nil
}

// Stop 停止 gnet 客户端引擎
func (d *Dialer) Stop() error {
	if d.client == nil {
		return nil
	}
	if err := d.client.Stop(); err != nil {
		return fmt.Errorf("停止 gnet 客户端失败: %w", err)
	}
	stresslog.Debug("[GNET] 客户端引擎已停止")
	return nil
}

// DialTCP 建立 TCP 连接并绑定业务层 Connection。
func (d *Dialer) DialTCP(ctx context.Context, address string, conn *Connection) (gnet.Conn, error) {
	gconn, err := d.client.Dial("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("TCP 拨号失败 %s: %w", address, err)
	}

	conn.sendFunc = func(data []byte) error {
		return gconn.AsyncWrite(data, nil)
	}
	conn.closeFunc = func() error {
		return gconn.Close()
	}

	d.server.registry.register(gconn, conn)

	stresslog.Info("[GNET] TCP 连接已建立",
		zap.String("address", address), zap.String("service", conn.serviceName), zap.String("robot", conn.robotName))
	return gconn, nil
}

// DialUDP 建立 UDP 连接并绑定业务层 Connection。
func (d *Dialer) DialUDP(address string, conn *Connection) (gnet.Conn, error) {
	gconn, err := d.client.Dial("udp", address)
	if err != nil {
		return nil, fmt.Errorf("UDP 拨号失败 %s: %w", address, err)
	}

	conn.sendFunc = func(data []byte) error {
		return gconn.AsyncWrite(data, nil)
	}
	conn.closeFunc = func() error {
		return gconn.Close()
	}

	d.server.registry.register(gconn, conn)

	stresslog.Info("[GNET] UDP 连接已建立", zap.String("address", address), zap.String("robot", conn.robotName))
	return gconn, nil
}
