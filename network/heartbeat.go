package network

import (
	stresslog "stressbot/log"
	"sync/atomic"
	"time"
)

// HeartbeatBuilder 心跳包构建函数。
// 每次心跳触发时调用，返回完整的待发送字节数据（已含消息头+消息体+加密）。
// 返回 nil 表示本次跳过发送。
type HeartbeatBuilder func() []byte

// HeartbeatConfig 心跳配置。
type HeartbeatConfig struct {
	Interval time.Duration    // 发送间隔
	Builder  HeartbeatBuilder // 包构建器
}

// heartbeatState 心跳运行时状态
type heartbeatState struct {
	cfg     HeartbeatConfig
	stop    chan struct{}
	running int32
}

// RegisterHeartbeat 在连接上注册心跳。
// 若已有心跳则替换之。连接关闭时自动停止。
func (c *Connection) RegisterHeartbeat(cfg HeartbeatConfig) {
	if c == nil || atomic.LoadInt32(&c.isClose) == 1 {
		return
	}
	if cfg.Interval <= 0 || cfg.Builder == nil {
		return
	}

	// 停止已有心跳
	c.StopHeartbeat()

	state := &heartbeatState{
		cfg:  cfg,
		stop: make(chan struct{}),
	}
	c.heartbeatMu.Lock()
	c.heartbeat = state
	c.heartbeatMu.Unlock()

	atomic.StoreInt32(&state.running, 1)
	go c.runHeartbeat(state)
}

// StopHeartbeat 停止当前心跳
func (c *Connection) StopHeartbeat() {
	c.heartbeatMu.Lock()
	hb := c.heartbeat
	c.heartbeat = nil
	c.heartbeatMu.Unlock()

	if hb != nil && atomic.CompareAndSwapInt32(&hb.running, 1, 0) {
		close(hb.stop)
	}
}

// runHeartbeat 心跳发送循环
func (c *Connection) runHeartbeat(hb *heartbeatState) {
	ticker := time.NewTicker(hb.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-hb.stop:
			return
		case <-ticker.C:
			if atomic.LoadInt32(&c.isClose) == 1 {
				return
			}
			packet := hb.cfg.Builder()
			if packet == nil {
				continue
			}
			ok, _ := c.Send(packet)
			if !ok {
				stresslog.DebugF("[HEARTBEAT] 发送失败 serviceName=%s", c.serviceName)
			}
		}
	}
}
