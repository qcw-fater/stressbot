// Package network 提供基于 gnet 的高性能网络层。
// Client 管理压测机器人的所有 TCP/UDP 连接，每个连接对应一个远程服务。
package network

import (
	"sync"
)

// Client 管理压测机器人的所有网络连接。
// 支持多条 TCP 连接（按服务名索引）和一条 UDP 连接。
type Client struct {
	name     string                 // 客户端标识名称（即机器人名称）
	TCPConn  map[string]*Connection // TCP 连接池，key 为服务名
	UDPConn  *Connection            // UDP 连接（仅一条）
	protocol *Protocol              // 消息头编解码器
	mu       sync.RWMutex           // 保护 TCPConn 并发读写
}

// NewClient 创建新的网络客户端。
// name 为机器人名称，protocol 为消息头编解码器。
func NewClient(name string, protocol *Protocol) *Client {
	return &Client{
		name:     name,
		TCPConn:  make(map[string]*Connection),
		protocol: protocol,
	}
}

// Connect 建立到指定服务的 TCP 连接。
// 仅创建 Connection 对象占位，实际的 gnet 拨号和 sendFunc 注入
// 由 Robot.ConnectTCP → Dialer.DialTCP 完成。
func (c *Client) Connect(serviceName string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 服务名已存在，不允许重复连接
	if _, ok := c.TCPConn[serviceName]; ok {
		return false
	}

	conn := NewConnection(serviceName, c.name, c.protocol)
	c.TCPConn[serviceName] = conn
	return true
}

// ConnectUDP 建立 UDP 连接。
// address：远端地址（host:port）。
// 如果 UDP 连接已存在则返回 false。
func (c *Client) ConnectUDP() bool {
	// 已存在 UDP 连接则拒绝
	if c.UDPConn != nil {
		return false
	}

	conn := NewConnection("udp", c.name, c.protocol)
	// 实际 gnet UDP 拨号由 Robot.ConnectUDP → Dialer.DialUDP 完成
	c.UDPConn = conn
	return true
}

// GetTCPConn 获取指定服务的 TCP 连接。
// 未找到时返回 nil。
func (c *Client) GetTCPConn(serviceName string) *Connection {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.TCPConn[serviceName]
}

// GetUDPConn 获取 UDP 连接。
// 未建立时返回 nil。
func (c *Client) GetUDPConn() *Connection {
	return c.UDPConn
}

// Close 关闭指定服务的 TCP 连接并从连接池中移除。
// 同时关闭底层 gnet 连接。
func (c *Client) Close(serviceName string) {
	c.mu.Lock()
	conn, ok := c.TCPConn[serviceName]
	if !ok {
		c.mu.Unlock()
		return
	}
	delete(c.TCPConn, serviceName)
	c.mu.Unlock()

	conn.Close()
}

// GetProtocol 返回消息头编解码器
func (c *Client) GetProtocol() *Protocol {
	return c.protocol
}

// CloseUDP 关闭 UDP 连接。
// 返回 true 表示本次调用执行了关闭。
func (c *Client) CloseUDP() bool {
	conn := c.UDPConn
	if conn == nil {
		return false
	}
	c.UDPConn = nil
	conn.Close()
	return true
}

// CloseAll 关闭所有连接（TCP + UDP）。
func (c *Client) CloseAll() {
	c.mu.Lock()
	conns := c.TCPConn
	c.TCPConn = make(map[string]*Connection)
	c.mu.Unlock()

	for _, conn := range conns {
		conn.Close()
	}

	c.CloseUDP()
}
