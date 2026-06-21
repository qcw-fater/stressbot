package network

import (
	"sync/atomic"
	"time"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// HeartbeatBuilder 心跳包构建函数。
// 每次心跳触发时调用，返回完整的待发送字节数据（已含消息头+消息体+加密）。
// 返回 nil 表示本次跳过发送。
//
// 2-B 起为 Go-only builder（不再保存 Lua function）；
// 2-C3 起由 connectionPump 在其单 goroutine 内同步调用，因此 builder 不必线程安全
// （pump 是唯一执行者），但仍应避免阻塞——若需读取 state，走线程安全 state API。
type HeartbeatBuilder func() []byte

// HeartbeatConfig 心跳配置。
type HeartbeatConfig struct {
	Interval time.Duration    // 发送间隔
	Builder  HeartbeatBuilder // 包构建器
}

// heartbeatRuntime 是 connectionPump 持有的心跳运行时状态。
//
// 2-C3 起：pump 是心跳 timer + cfg 的唯一 owner（在 pump goroutine 内无锁读写），
// 旧的独立 runHeartbeat goroutine / heartbeatState.stop / heartbeatState.done 全部下线。
// hbMu 只保护「pump 外部 goroutine 投递 controlCh 前后的 hb 快照读」（RegisterHeartbeat
// 的 fail-fast 判断），pump 内部读写 hb 不走这把锁。
type heartbeatRuntime struct {
	cfg   HeartbeatConfig
	timer *time.Timer // pump-owned，由 resetHeartbeatTimerLocked / stopHeartbeatTimerLocked 管理
}

// hb 字段保护说明：c.hb 由 pump goroutine 独占写（在 handleControlLocked /
// stopHeartbeatTimerLocked 内）。c.hbMu 仅用于 RegisterHeartbeat / StopHeartbeat 在
// 投递 controlCh 前/后读 c.hb 做 fail-fast 判断（避免无意义投递）；pump 内部访问不走锁。
// 这把锁不保护 timer（timer 只在 pump goroutine 内触碰）。

// RegisterHeartbeat 在连接上注册心跳。
// 若已有心跳则替换之。连接关闭时由 pump 的 ctx.Done→defer 自动停止 timer。
//
// 2-C3 起：本方法**签名保持稳定**（HeartbeatConfig{Interval, Builder func() []byte}），
// 但实现从「启动独立 runHeartbeat goroutine」改为「投递 pumpCmdHeartbeat 给 connectionPump，
// 由 pump 在自己的 select 分支里串行安装 cfg + 重置 timer」。这样心跳发送、inbound decode、
// listen 分发共享同一 pump goroutine，builder 不触碰业务 LState。
//
// 必须在 StartPump 之后调用（pump 未启动时 controlCh 为 nil，本方法降级为直接写 c.hb + 启动
// 临时 timer 的兼容路径——但生产路径 dial 总是先 StartPump 后注册心跳，故该降级路径仅用于测试）。
func (c *Connection) RegisterHeartbeat(cfg HeartbeatConfig) {
	if c == nil || atomic.LoadInt32(&c.isClose) == 1 {
		return
	}
	if cfg.Interval <= 0 || cfg.Builder == nil {
		return
	}

	// pump 未启动（测试或异常路径）：直接写 hb 字段并启动 timer。
	// 此时没有 pump goroutine 消费 timer.C，timer 到期后只是个无人读的 channel，
	// 会在 StopHeartbeat / Close 时被 Stop 回收，无泄漏。生产路径不走这里。
	if c.controlCh == nil {
		c.hbMu.Lock()
		c.hb = &heartbeatRuntime{cfg: cfg}
		c.hb.timer = time.NewTimer(cfg.Interval)
		c.hbMu.Unlock()
		return
	}

	// pump 已启动：投递控制命令。pump 在 handleControlLocked 里 stopHeartbeatTimerLocked
	// 旧的、装上新的、resetHeartbeatTimerLocked，全部在 pump goroutine 内串行完成。
	// 不带 result channel：注册是 fire-and-forget，pump 一定会在下一轮 select 处理。
	// 若紧接着 Close，cancel 先让 pump 走 ctx.Done 分支，defer 也会停 timer，无泄漏。
	cmd := pumpCmd{kind: pumpCmdHeartbeat, hbCfg: cfg}
	select {
	case c.controlCh <- cmd:
	default:
		// controlCh 满（极罕见：Close + RegisterHeartbeat 并发突发）。注册失败，记录 warn。
		// 不阻塞：心跳缺失业务层会感知（服务端可能踢线），优于卡住注册方。
		stresslog.Warn("[HEARTBEAT] 注册失败：pump controlCh 已满",
			zap.String("service", c.serviceName),
			zap.String("robot", c.robotName))
	}
}

// StopHeartbeat 停止当前心跳。
//
// 2-C3 起：投递 pumpCmdStopHeartbeat 给 pump，由 pump 停 timer + 置 hb=nil。
// 不再等待独立 goroutine 退出（已无独立 goroutine），故**无超时等待**——pump 收到命令或
// 通过 ctx.Done→defer 兜底都会停 timer，调用方不必阻塞。
//
// doClose 先 cancel ctx 再调本方法：即便 controlCh 投递丢失，pump 也会在 ctx.Done 分支
// 经 defer stopHeartbeatTimerLocked 停 timer，无泄漏、无卡死。
func (c *Connection) StopHeartbeat() {
	if c == nil {
		return
	}
	// pump 未启动路径（与 RegisterHeartbeat 对称）：直接停 timer + 清 hb。
	if c.controlCh == nil {
		c.hbMu.Lock()
		hb := c.hb
		c.hb = nil
		c.hbMu.Unlock()
		if hb != nil && hb.timer != nil {
			if !hb.timer.Stop() {
				select {
				case <-hb.timer.C:
				default:
				}
			}
		}
		return
	}

	// 同步等待 pump 处理完：用 result channel 让本调用返回时心跳确定已停。
	// 这保留了旧 StopHeartbeat 的「返回即心跳已停」语义，方便 doClose/Robot.Close 后续逻辑。
	result := make(chan error, 1)
	cmd := pumpCmd{kind: pumpCmdStopHeartbeat, result: result}
	select {
	case c.controlCh <- cmd:
		// pump 已收到命令，等待处理完成。pump 处理 stopHeartbeat 是 O(1)，不会卡。
		select {
		case <-result:
		case <-c.ctx.Done():
			// pump 已在退出路径（defer 会停 timer），不必再等。
		}
	default:
		// controlCh 满 或 pump 已退出（channel 仍开但无人读的极端竞态）：依赖 pump defer 兜底。
		// pumpDone 关闭即表示 timer 已被 defer 停掉，等一下即可。
		if c.pumpDone != nil {
			select {
			case <-c.pumpDone:
			case <-time.After(stopHeartbeatFallbackTimeout):
			}
		}
	}
}

// stopHeartbeatFallbackTimeout 是 StopHeartbeat 在 controlCh 投递失败后等待 pumpDone
// 的兜底超时。正常情况下 pump 在 cancel ctx 后几十微秒内退出并 defer 停 timer；
// 这个超时只防 pump 因未知 bug 卡住的极端情况，避免上层 Close 永久挂起。
const stopHeartbeatFallbackTimeout = 2 * time.Second
