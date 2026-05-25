package network

import (
	"sync"
	"time"

	"go.uber.org/zap"
	stresslog "stressbot/utils/log"
)

// Client 管理压测机器人的所有网络连接。
type Client struct {
	name           string
	tcpConn        map[string]*Connection
	udpConn        map[string]*Connection
	requestTimeout time.Duration
	mu             sync.RWMutex
}

// NewClient 创建新的网络客户端。
func NewClient(name string, requestTimeout time.Duration) *Client {
	return &Client{
		name:           name,
		tcpConn:        make(map[string]*Connection),
		udpConn:        make(map[string]*Connection),
		requestTimeout: requestTimeout,
	}
}

// ConnectTCP 建立到指定服务的 TCP 连接占位。
func (c *Client) ConnectTCP(serviceName string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.tcpConn[serviceName]; ok {
		stresslog.Debug("[CLIENT] TCP 连接已存在，跳过", zap.String("service", serviceName), zap.String("robot", c.name))
		return false
	}

	conn := NewConnection(serviceName, c.name, c.requestTimeout)
	c.tcpConn[serviceName] = conn
	stresslog.Debug("[CLIENT] TCP 连接占位已创建", zap.String("service", serviceName), zap.String("robot", c.name))
	return true
}

// ConnectUDP 建立指定服务的 UDP 连接占位。
func (c *Client) ConnectUDP(serviceName string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.udpConn[serviceName]; ok {
		stresslog.Debug("[CLIENT] UDP 连接已存在，跳过", zap.String("service", serviceName), zap.String("robot", c.name))
		return false
	}

	conn := NewConnection(serviceName, c.name, c.requestTimeout)
	c.udpConn[serviceName] = conn
	stresslog.Debug("[CLIENT] UDP 连接占位已创建", zap.String("service", serviceName), zap.String("robot", c.name))
	return true
}

// GetTCPConn 获取指定服务的 TCP 连接。
func (c *Client) GetTCPConn(serviceName string) *Connection {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tcpConn[serviceName]
}

// GetUDPConn 获取指定服务的 UDP 连接。
func (c *Client) GetUDPConn(serviceName string) *Connection {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.udpConn[serviceName]
}

// CloseTCP 关闭指定服务的 TCP 连接并从连接池中移除。
func (c *Client) CloseTCP(serviceName string) {
	c.mu.Lock()
	conn, ok := c.tcpConn[serviceName]
	if !ok {
		c.mu.Unlock()
		stresslog.Debug("[CLIENT] TCP 连接不存在，跳过关闭", zap.String("service", serviceName), zap.String("robot", c.name))
		return
	}
	delete(c.tcpConn, serviceName)
	c.mu.Unlock()

	conn.Close()
}

// CloseUDP 关闭指定服务的 UDP 连接。
func (c *Client) CloseUDP(serviceName string) {
	c.mu.Lock()
	conn, ok := c.udpConn[serviceName]
	if !ok {
		c.mu.Unlock()
		stresslog.Debug("[CLIENT] UDP 连接不存在，跳过关闭", zap.String("service", serviceName), zap.String("robot", c.name))
		return
	}
	delete(c.udpConn, serviceName)
	c.mu.Unlock()

	conn.Close()
}

// CloseAll 关闭所有连接，并等待所有监听循环退出（确保回调不再使用 Robot 资源）。
func (c *Client) CloseAll() {
	c.mu.Lock()
	tcpConns := c.tcpConn
	udpConns := c.udpConn
	c.tcpConn = make(map[string]*Connection)
	c.udpConn = make(map[string]*Connection)
	c.mu.Unlock()

	stresslog.Info("[CLIENT] 关闭所有连接", zap.String("robot", c.name),
		zap.Int("tcp", len(tcpConns)), zap.Int("udp", len(udpConns)))

	for _, conn := range tcpConns {
		conn.Close()
	}
	for _, conn := range udpConns {
		conn.Close()
	}
	// 等待所有 listenLoop 退出。回调在 listenLoop 内同步执行，
	// loop 退出后不会有任何回调仍在使用 Robot 的 LState。
	for _, conn := range tcpConns {
		conn.WaitListenDone()
	}
	for _, conn := range udpConns {
		conn.WaitListenDone()
	}
}
