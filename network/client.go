package network

import (
	"stressbot/monitor"
	stresslog "stressbot/utils/log"
	"sync"
	"time"

	"go.uber.org/zap"
)

// CloseAllResult 表示一次连接池关闭的收尾结果。
type CloseAllResult struct {
	Done           bool
	TCPCount       int
	UDPCount       int
	DecodeTimeouts int
	ListenTimeouts int
	Message        string
}

// Client 管理压测机器人的所有网络连接。
type Client struct {
	name           string
	tcpConn        map[string]*Connection
	udpConn        map[string]*Connection
	requestTimeout time.Duration
	timingDetail   monitor.TimingDetailLevel
	mu             sync.RWMutex
}

// NewClient 创建新的网络客户端。
func NewClient(name string, requestTimeout time.Duration, timingDetail monitor.TimingDetailLevel) *Client {
	return &Client{
		name:           name,
		tcpConn:        make(map[string]*Connection),
		udpConn:        make(map[string]*Connection),
		requestTimeout: requestTimeout,
		timingDetail:   timingDetail,
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

	conn := NewConnection(serviceName, c.name, c.requestTimeout, c.timingDetail)
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

	conn := NewConnection(serviceName, c.name, c.requestTimeout, c.timingDetail)
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

// CloseAll 关闭所有连接，并等待所有后台 goroutine 退出。
// 等待顺序：先 decode（OnReceive 的源头）再 listen（OnReceive 的下游），
// 确保任何回调离开后不再有数据流入 Robot 资源。
func (c *Client) CloseAll() {
	_ = c.CloseAllWithTimeout(0)
}

// CloseAllWithTimeout 关闭所有连接并带超时等待后台 goroutine 退出。
// timeout<=0 表示不设超时，保持旧 CloseAll 的阻塞语义。
func (c *Client) CloseAllWithTimeout(timeout time.Duration) CloseAllResult {
	c.mu.Lock()
	tcpConns := c.tcpConn
	udpConns := c.udpConn
	c.tcpConn = make(map[string]*Connection)
	c.udpConn = make(map[string]*Connection)
	c.mu.Unlock()

	result := CloseAllResult{Done: true, TCPCount: len(tcpConns), UDPCount: len(udpConns)}
	stresslog.Info("[CLIENT] 关闭所有连接", zap.String("robot", c.name),
		zap.Int("tcp", len(tcpConns)), zap.Int("udp", len(udpConns)))

	for _, conn := range tcpConns {
		conn.Close()
	}
	for _, conn := range udpConns {
		conn.Close()
	}

	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	remaining := func() time.Duration {
		if deadline.IsZero() {
			return 0
		}
		d := time.Until(deadline)
		if d <= 0 {
			return time.Nanosecond
		}
		return d
	}

	for _, conn := range tcpConns {
		if !conn.WaitDecodeDoneTimeout(remaining()) {
			result.Done = false
			result.DecodeTimeouts++
		}
	}
	for _, conn := range udpConns {
		if !conn.WaitDecodeDoneTimeout(remaining()) {
			result.Done = false
			result.DecodeTimeouts++
		}
	}
	for _, conn := range tcpConns {
		if !conn.WaitListenDoneTimeout(remaining()) {
			result.Done = false
			result.ListenTimeouts++
		}
	}
	for _, conn := range udpConns {
		if !conn.WaitListenDoneTimeout(remaining()) {
			result.Done = false
			result.ListenTimeouts++
		}
	}
	if result.Done {
		result.Message = "连接清理完成"
	} else {
		result.Message = "连接清理超时"
	}
	return result
}
