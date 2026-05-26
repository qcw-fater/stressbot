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

// stopHeartbeatTimeout 等待心跳 goroutine 退出的最长时间。
// 正常路径下：cancel 先于 StopHeartbeat（见 doClose）+ Builder 用 TryLock 兜底，
// 心跳通常在 1 个 tick 周期内自然退出。这个超时只为防御"未来某个 lua API
// 在持 luaMu 时长时间阻塞而忘了 withReleasedMu"导致 Builder TryLock 始终
// 失败、但心跳 goroutine 卡在 Builder 之后某行的极端情况。
const stopHeartbeatTimeout = 2 * time.Second

// StopHeartbeat 停止当前心跳并等待 goroutine 退出。
// 必须有限超时：如果 Builder 已经成功获得 luaMu 并进入 Lua 调用，
// 此时即便 ctx 已经取消（cancel 提前），仍要等 Lua 执行完成才会回到 select。
// 在病态情况下（Lua 死循环 / 极慢的 builder），用超时强制返回以避免上层 Close 永久挂起。
func (c *Connection) StopHeartbeat() {
	c.heartbeatMu.Lock()
	hb := c.heartbeat
	c.heartbeat = nil
	c.heartbeatMu.Unlock()

	if hb != nil && atomic.CompareAndSwapInt32(&hb.running, 1, 0) {
		close(hb.stop)
		t := time.NewTimer(stopHeartbeatTimeout)
		defer t.Stop()
		select {
		case <-hb.done:
		case <-t.C:
			stresslog.Warn("[HEARTBEAT] 停止超时，强制推进（goroutine 后台收敛）",
				zap.String("service", c.serviceName),
				zap.String("robot", c.robotName),
				zap.Duration("timeout", stopHeartbeatTimeout))
		}
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
					zap.String("service", c.serviceName), zap.Int("pktLen", len(packet)), zap.Error(err))
			} else {
				stresslog.Debug("[HEARTBEAT] 已发送",
					zap.String("service", c.serviceName), zap.Int("pktLen", n))
			}
		}
	}
}
