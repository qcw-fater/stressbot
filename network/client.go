package network

import (
	"sync"
	"time"
)

// Client 管理压测机器人的所有网络连接。
type Client struct {
	name           string
	TCPConn        map[string]*Connection
	UDPConn        map[string]*Connection
	requestTimeout time.Duration
	mu             sync.RWMutex
}

// NewClient 创建新的网络客户端。
func NewClient(name string, requestTimeout time.Duration) *Client {
	return &Client{
		name:           name,
		TCPConn:        make(map[string]*Connection),
		UDPConn:        make(map[string]*Connection),
		requestTimeout: requestTimeout,
	}
}

// ConnectTCP 建立到指定服务的 TCP 连接占位。
func (c *Client) ConnectTCP(serviceName string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.TCPConn[serviceName]; ok {
		return false
	}

	conn := NewConnection(serviceName, c.name, c.requestTimeout)
	c.TCPConn[serviceName] = conn
	return true
}

// ConnectUDP 建立指定服务的 UDP 连接占位。
func (c *Client) ConnectUDP(serviceName string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.UDPConn[serviceName]; ok {
		return false
	}

	conn := NewConnection(serviceName, c.name, c.requestTimeout)
	c.UDPConn[serviceName] = conn
	return true
}

// GetTCPConn 获取指定服务的 TCP 连接。
func (c *Client) GetTCPConn(serviceName string) *Connection {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.TCPConn[serviceName]
}

// GetUDPConn 获取指定服务的 UDP 连接。
func (c *Client) GetUDPConn(serviceName string) *Connection {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.UDPConn[serviceName]
}

// CloseTCP 关闭指定服务的 TCP 连接并从连接池中移除。
func (c *Client) CloseTCP(serviceName string) {
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

// CloseUDP 关闭指定服务的 UDP 连接。
func (c *Client) CloseUDP(serviceName string) bool {
	c.mu.Lock()
	conn, ok := c.UDPConn[serviceName]
	if !ok {
		c.mu.Unlock()
		return false
	}
	delete(c.UDPConn, serviceName)
	c.mu.Unlock()

	conn.Close()
	return true
}

// CloseAll 关闭所有连接。
func (c *Client) CloseAll() {
	c.mu.Lock()
	tcpConns := c.TCPConn
	udpConns := c.UDPConn
	c.TCPConn = make(map[string]*Connection)
	c.UDPConn = make(map[string]*Connection)
	c.mu.Unlock()

	for _, conn := range tcpConns {
		conn.Close()
	}
	for _, conn := range udpConns {
		conn.Close()
	}
}
