package network

import (
	"stressbot/utils"
	stresslog "stressbot/utils/log"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
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
	done    chan struct{} // goroutine 退出时关闭，供 StopHeartbeat 等待
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
		done: make(chan struct{}),
	}
	c.heartbeatMu.Lock()
	c.heartbeat = state
	c.heartbeatMu.Unlock()

	atomic.StoreInt32(&state.running, 1)
	utils.GetWorkPool().GoWithStop(func(stopCh <-chan struct{}) { c.runHeartbeat(state, stopCh) })
}

// StopHeartbeat 停止当前心跳并等待 goroutine 退出。
func (c *Connection) StopHeartbeat() {
	c.heartbeatMu.Lock()
	hb := c.heartbeat
	c.heartbeat = nil
	c.heartbeatMu.Unlock()

	if hb != nil && atomic.CompareAndSwapInt32(&hb.running, 1, 0) {
		close(hb.stop)
		<-hb.done // 等待 goroutine 退出
	}
}

// runHeartbeat 心跳发送循环
func (c *Connection) runHeartbeat(hb *heartbeatState, stopCh <-chan struct{}) {
	defer close(hb.done)
	ticker := time.NewTicker(hb.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-hb.stop:
			return
		case <-stopCh:
			return
		case <-ticker.C:
			if atomic.LoadInt32(&c.isClose) == 1 {
				return
			}
			packet := hb.cfg.Builder()
			if packet == nil {
				continue
			}
			n, err := c.Send(packet)
			if err != nil {
				stresslog.Warn("[HEARTBEAT] 发送失败",
					zap.String("service", c.serviceName), zap.Int("pktLen", len(packet)))
			} else {
				stresslog.Debug("[HEARTBEAT] 已发送",
					zap.String("service", c.serviceName), zap.Int("pktLen", n))
			}
		}
	}
}
