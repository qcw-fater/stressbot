// Package network 提供基于 gnet 的高性能网络层。
// gnet.go 实现 gnet 事件循环集成，管理 TCP/UDP 连接的生命周期、
// 消息帧拆包、心跳发送。通过 connRegistry 将 gnet 连接与业务层 Connection 绑定。
package network

import (
	"context"
	"fmt"
	"net"
	stresslog "stressbot/log"
	"sync"
	"time"

	"github.com/panjf2000/gnet/v2"
)

// connRegistry 管理 gnet 连接与业务层 Connection 的映射。
// gnet OnOpen 时注册，OnClose 时注销。
type connRegistry struct {
	mu      sync.RWMutex
	connMap map[int]*Connection // gnet.Conn Fd -> 业务 Connection
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
// 负责连接管理、消息帧拆包、心跳发送。
type EventServer struct {
	gnet.BuiltinEventEngine

	registry     *connRegistry
	protocol     *Protocol
	tickInterval time.Duration // 心跳间隔
}

// NewEventServer 创建 gnet 事件处理器
func NewEventServer(protocol *Protocol, heartbeatInterval time.Duration) *EventServer {
	return &EventServer{
		registry:     newConnRegistry(),
		protocol:     protocol,
		tickInterval: heartbeatInterval,
	}
}

// OnOpen gnet 连接建立回调
func (es *EventServer) OnOpen(gconn gnet.Conn) ([]byte, gnet.Action) {
	return nil, gnet.None
}

// OnClose gnet 连接关闭回调。
// 触发业务层 Connection 的 onClose 清理。
func (es *EventServer) OnClose(gconn gnet.Conn, err error) gnet.Action {
	conn := es.registry.get(gconn)
	if conn != nil {
		es.registry.unregister(gconn)
		conn.onClose()
	}
	return gnet.None
}

// OnTraffic gnet 收到数据回调。
// 按消息头格式拆包，将完整消息分发到业务层 Connection.OnReceive。
func (es *EventServer) OnTraffic(gconn gnet.Conn) (action gnet.Action) {
	headSize := es.protocol.HeadSize()

	for {
		// 检查缓冲区中可用字节数
		available := gconn.InboundBuffered()
		if available < headSize {
			return gnet.None
		}

		// 预览消息头（不消费缓冲区）
		headBuf, err := gconn.Peek(headSize)
		if err != nil || len(headBuf) < headSize {
			return gnet.None
		}

		// 解码消息头获取消息体长度
		head := es.protocol.DecodeHead(headBuf)
		if head == nil {
			// 协议解析失败，丢弃一个字节尝试恢复
			gconn.Discard(1)
			continue
		}

		// 检查是否有完整的消息（头 + 体）
		totalLen := headSize + int(head.Len)
		if available < totalLen {
			return gnet.None
		}

		// 消费完整消息
		msgBuf := make([]byte, totalLen)
		_, err = gconn.Read(msgBuf)
		if err != nil {
			stresslog.ErrorF("[GNET] 读取消息失败: %v", err)
			return gnet.None
		}
		// dispatch with decryption
		conn := es.registry.get(gconn)
		if conn != nil {
			secretKey := conn.GetSecretKey()
			decHead, decBody := es.protocol.DecodePacket(msgBuf, secretKey)
			if decHead != nil {
				conn.OnReceive(decHead, decBody)
			}
		}
	}
}

// OnTick gnet 定时回调。
// 心跳现在由每个连接独立的 RegisterHeartbeat 管理（per-connection 配置），
// 这里不再做全局广播。仍保留 Ticker 以便 gnet 事件循环活跃。
func (es *EventServer) OnTick() (delay time.Duration, action gnet.Action) {
	return es.tickInterval, gnet.None
}

// Dialer 管理 gnet 客户端，提供 TCP/UDP 拨号能力。
// 所有出站连接共享同一个 gnet 客户端实例。
type Dialer struct {
	client   *gnet.Client
	server   *EventServer
	protocol *Protocol
}

// NewDialer 创建拨号器（gnet 客户端模式）
func NewDialer(protocol *Protocol, heartbeatInterval time.Duration) *Dialer {
	server := NewEventServer(protocol, heartbeatInterval)
	return &Dialer{
		server:   server,
		protocol: protocol,
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

	stresslog.InfoF("[GNET] 客户端引擎已启动")
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
	stresslog.InfoF("[GNET] 客户端引擎已停止")
	return nil
}

// DialTCP 建立 TCP 连接并绑定业务层 Connection。
// address 格式为 host:port。conn 为预创建的业务层 Connection。
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

	stresslog.InfoF("[GNET] TCP 连接已建立 address=%s serviceName=%s robotName=%s",
		address, conn.serviceName, conn.robotName)
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

	stresslog.InfoF("[GNET] UDP 连接已建立 address=%s robotName=%s", address, conn.robotName)
	return gconn, nil
}

// RemoteAddr 获取 gnet 连接的远端地址
func RemoteAddr(gconn gnet.Conn) net.Addr {
	if gconn == nil {
		return nil
	}
	return gconn.RemoteAddr()
}
