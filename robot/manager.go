package robot

import (
	"context"
	"fmt"
	"stressbot/adapter"
	"stressbot/engine"
	"stressbot/network"
	"stressbot/protox"
	"stressbot/script"
	"stressbot/utils"
	stresslog "stressbot/utils/log"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// RampUpConfig 渐进式加压配置。
type RampUpConfig struct {
	Stages []RampUpStage `json:"stages"`
}

// RampUpStage 单个加压阶段。
type RampUpStage struct {
	Count       int  `json:"count"`                 // 本阶段新增 bot 数
	Concurrency int  `json:"concurrency,omitempty"` // 覆盖全局并发数，0 则用全局值
	HoldSec     int  `json:"holdSec,omitempty"`     // 阶段间等待秒数
	Reset       bool `json:"reset,omitempty"`       // 开始本阶段前清空所有已有机器人
}

// ManagerConfig 机器人管理器配置
type ManagerConfig struct {
	AccountPrefix string            `json:"accountPrefix"`
	StartNumber   int               `json:"startNumber"`
	Count         int               `json:"count"`
	ConcurrentNum int               `json:"concurrentNum"`
	StateExtra    map[string]string `json:"stateExtra"`
	// Adapter 是进程级共享的 LuaAdapter（持有 codec.lua 字节码 + 元信息 + 错误描述缓存）。
	// Manager 创建每个 Robot 时通过它派生 RobotAdapter，让 Robot 在自己的 LState 上做编解码。
	// 类型从 adapter.Adapter 接口收窄到 *adapter.LuaAdapter，是因为只有 LuaAdapter 提供
	// NewRobotAdapter 工厂方法。
	Adapter        *adapter.LuaAdapter `json:"-"`
	RequestTimeout time.Duration       `json:"requestTimeout"`
	MainService    string              `json:"mainService"`
	HTTPTimeout    time.Duration       `json:"httpTimeout"`
	RampUp         *RampUpConfig       `json:"rampUp"`
	Duration       time.Duration       `json:"duration"` // 运行时长，0 = 一直运行
}

// Manager 机器人管理器。
type Manager struct {
	cfg            ManagerConfig
	flow           *engine.TaskFlow
	factory        *protox.Factory
	dialer         *network.Dialer
	luaPool        *script.RuntimePool
	robots         []*Robot
	mu             sync.RWMutex
	ctx            context.Context
	cancel         context.CancelFunc
	started        atomic.Int32
	stopped        atomic.Int32
	doneCh         chan struct{} // 所有机器人停止后关闭
	stopOnce       sync.Once
	cleanupMu      sync.Mutex
	cleanupSummary CleanupStatus

	// OnStageReset 阶段重置回调，由 TaskRunner 注入。
	// 在 resetBots() 完成后调用，用于上报当前阶段指标并重置采集器。
	OnStageReset func(completedStageIdx int)
}

// NewManager 创建机器人管理器
func NewManager(cfg ManagerConfig, flow *engine.TaskFlow, factory *protox.Factory,
	dialer *network.Dialer, luaPool *script.RuntimePool) *Manager {

	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		cfg:     cfg,
		flow:    flow,
		factory: factory,
		dialer:  dialer,
		luaPool: luaPool,
		robots:  make([]*Robot, 0, cfg.Count),
		ctx:     ctx,
		cancel:  cancel,
		doneCh:  make(chan struct{}),
	}
}

// startBatch 创建 [fromIndex, fromIndex+count) 的 bot，使用 conc 做批量限速。
// 返回实际创建的数量（即使中途被 ctx 取消，调用方也能知道完成了多少）。
func (m *Manager) startBatch(fromIndex, count, conc int) (int, error) {
	created := 0
	for i := 0; i < count; i++ {
		if m.ctx.Err() != nil {
			stresslog.Warn("[MANAGER] 批次创建被取消",
				zap.Int("fromIndex", fromIndex),
				zap.Int("requested", count),
				zap.Int("actuallyCreated", created),
				zap.Error(m.ctx.Err()))
			return created, m.ctx.Err()
		}

		id := m.cfg.StartNumber + fromIndex + i
		account := fmt.Sprintf("%s%d", m.cfg.AccountPrefix, id)

		r, err := NewRobot(Config{
			ID:             id,
			Account:        account,
			StateExtra:     m.cfg.StateExtra,
			HTTPTimeout:    m.cfg.HTTPTimeout,
			RequestTimeout: m.cfg.RequestTimeout,
			MainService:    m.cfg.MainService,
		}, m.flow, m.factory, m.cfg.Adapter, m.dialer, m.luaPool)
		if err != nil {
			// codec.lua 加载失败属于配置问题，重试也没用。
			// 跳过这个 robot，日志告警，继续创建其它（避免单个失败拖垮整批）。
			stresslog.Error("[MANAGER] 创建机器人失败，跳过",
				zap.Int("id", id), zap.String("account", account), zap.Error(err))
			continue
		}

		m.mu.Lock()
		m.robots = append(m.robots, r)
		m.mu.Unlock()

		r.onDone = m.onRobotDone
		r.Start()
		m.started.Add(1)
		created++

		// 仅在批次中间触发限速等待；最后一个 robot 启动后不再等，避免阶段切换时多等 1s。
		if conc > 0 && (i+1)%conc == 0 && (i+1) < count {
			stresslog.Info("[MANAGER] 批量进度",
				zap.Int("batchCreated", i+1),
				zap.Int("batchTotal", count))
			select {
			case <-m.ctx.Done():
				return created, m.ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	return created, nil
}

// StartAll 一次性创建全部机器人。
func (m *Manager) StartAll() error {
	stresslog.Info("[MANAGER] 开始创建机器人",
		zap.Int("count", m.cfg.Count),
		zap.Int("concurrent", m.cfg.ConcurrentNum))

	created, err := m.startBatch(0, m.cfg.Count, m.cfg.ConcurrentNum)
	if err != nil {
		return err
	}

	stresslog.Info("[MANAGER] 全部机器人已启动",
		zap.Int("count", created),
		zap.Int("requested", m.cfg.Count))
	m.startDurationTimer()
	return nil
}

// StartWithRampUp 分阶段创建机器人。
func (m *Manager) StartWithRampUp() error {
	stages := m.cfg.RampUp.Stages
	total := 0
	for _, s := range stages {
		total += s.Count
	}

	stresslog.Info("[MANAGER] 开始渐进式加压",
		zap.Int("stages", len(stages)),
		zap.Int("totalBots", total),
		zap.Int("defaultConcurrent", m.cfg.ConcurrentNum))

	offset := 0
	for i, stage := range stages {
		// 提前检查 ctx：第一阶段就有可能被取消（用户极快地点了停止）
		if m.ctx.Err() != nil {
			return m.ctx.Err()
		}
		// 阶段重置：清空已有机器人，上报当前阶段指标。
		// resetBots 后短暂 sleep 给"机器人末次 IO + 计数器最终值"留出 flush 窗口，
		// 同时 select ctx.Done() 以便用户中途停止任务能立即返回。
		if stage.Reset && len(m.robots) > 0 {
			stresslog.Info("[MANAGER] 阶段重置，停止所有机器人",
				zap.Int("nextStage", i+1),
				zap.Int("currentBots", len(m.robots)))
			m.resetBots()
			select {
			case <-m.ctx.Done():
				return m.ctx.Err()
			case <-time.After(1 * time.Second):
			}
			if m.OnStageReset != nil {
				m.OnStageReset(i) // >= 1, 阶段报告标识
			}
		}

		conc := m.cfg.ConcurrentNum
		if stage.Concurrency > 0 {
			conc = stage.Concurrency
		}
		holdSec := stage.HoldSec
		if holdSec < 30 {
			holdSec = 30
		}

		stresslog.Info("[MANAGER] 启动阶段",
			zap.Int("stage", i+1),
			zap.Int("of", len(stages)),
			zap.Int("count", stage.Count),
			zap.Int("offset", offset),
			zap.Int("concurrency", conc),
			zap.Int("holdSec", holdSec),
			zap.Bool("reset", stage.Reset))

		created, err := m.startBatch(offset, stage.Count, conc)
		if err != nil {
			stresslog.Warn("[MANAGER] 阶段启动中断",
				zap.Int("stage", i+1),
				zap.Int("requested", stage.Count),
				zap.Int("created", created),
				zap.Error(err))
			return err
		}
		offset += stage.Count

		m.mu.RLock()
		activeBots := len(m.robots)
		m.mu.RUnlock()
		stresslog.Info("[MANAGER] 阶段完成",
			zap.Int("stage", i+1),
			zap.Int("created", created),
			zap.Int("offset", offset),
			zap.Int("activeBots", activeBots))

		// 阶段间等待（最后一个阶段不等）
		if i < len(stages)-1 {
			stresslog.Info("[MANAGER] 阶段保持",
				zap.Int("stage", i+1),
				zap.Int("holdSec", holdSec),
				zap.Int("activeBots", activeBots))
			select {
			case <-m.ctx.Done():
				return m.ctx.Err()
			case <-time.After(time.Duration(holdSec) * time.Second):
			}
		}
	}

	m.mu.RLock()
	finalBots := len(m.robots)
	m.mu.RUnlock()
	stresslog.Info("[MANAGER] 渐进式加压完成",
		zap.Int("plannedOffset", offset),
		zap.Int("activeBots", finalBots))
	m.startDurationTimer()
	return nil
}

// closeRobotsTimeout 整体并发关闭机器人的最长时间。
// 单个 robot 内部已有 robotCloseTimeout（10s）兜底，所有 r.Close() 是并发的，
// 因此正常情况下最多 10s 就能完成；这里再加 5s 缓冲覆盖 goroutine 调度延迟。
// 必须显著小于 Admin 的 stopWaitTimeout（60s），避免 Agent 还在 Close 时 Admin
// 已经合成 fake report 把任务标 stopped，导致 Agent 真实上报被丢弃。
const closeRobotsTimeout = 15 * time.Second

// closeRobotsConcurrent 并发关闭一批机器人并等待全部完成。
// 单个 robot 卡死不会阻塞其他 robot 的关闭，整体超过 closeRobotsTimeout 后强制返回。
func closeRobotsConcurrent(robots []*Robot, reason CleanupReason) CleanupStatus {
	if len(robots) == 0 {
		return emptyCleanupSummary(reason)
	}
	var wg sync.WaitGroup
	results := make(chan CleanupStatus, len(robots))
	for _, r := range robots {
		r := r
		wg.Add(1)
		utils.GetWorkPool().Go(func() {
			defer wg.Done()
			results <- r.cleanup(reason, false)
		})
	}

	statuses := make([]CleanupStatus, 0, len(robots))
	timer := time.NewTimer(closeRobotsTimeout)
	defer timer.Stop()
	for len(statuses) < len(robots) {
		select {
		case cleanup := <-results:
			statuses = append(statuses, cleanup)
		case <-timer.C:
			stresslog.Error("[MANAGER] 批量关闭机器人超时，强制继续推进",
				zap.Int("total", len(robots)),
				zap.Int("done", len(statuses)),
				zap.Duration("timeout", closeRobotsTimeout))
			missing := len(robots) - len(statuses)
			for i := 0; i < missing; i++ {
				statuses = append(statuses, CleanupStatus{
					Status:        CleanupTimeout,
					Reason:        reason,
					Message:       "批量关闭等待超时，机器人清理结果未返回",
					TotalRobots:   1,
					TimeoutRobots: 1,
					LuaSkipped:    1,
				})
			}
		}
	}
	return MergeCleanupStatus(reason, statuses...)
}

// StopAll 停止所有机器人并等待执行 goroutine 结束。
//
// 返回的 CleanupStatus 由两部分合并而成：
//  1. prior：在本次 StopAll 之前就已"自然完成"的机器人，其清理结果由各自的
//     Start goroutine 经 onRobotDone → recordCleanup 累积进 m.cleanupSummary。
//     定时停止 / 流程自然跑完时，机器人会先从 m.robots 中摘除，若不读这里就会丢失它们的清理状态。
//  2. closeResult：本次 StopAll 主动关闭的、仍在 m.robots 中的机器人。
//
// prior 必须在触发关闭"之前"读取：此刻仍在 m.robots 里的机器人都还没结束，
// 因而尚未进入 m.cleanupSummary，两部分天然不重叠，避免重复计数。
func (m *Manager) StopAll() CleanupStatus {
	m.cancel()
	m.mu.RLock()
	robots := make([]*Robot, len(m.robots))
	copy(robots, m.robots)
	m.mu.RUnlock()

	prior := m.CleanupSummary()

	closeResult := closeRobotsConcurrent(robots, CleanupReasonAdminStop)
	m.stopOnce.Do(func() { close(m.doneCh) })

	cleanup := MergeCleanupStatus(CleanupReasonAdminStop, prior, closeResult)
	stresslog.Info("[MANAGER] 全部机器人已停止",
		zap.Int("count", len(robots)),
		zap.String("cleanup", string(cleanup.Status)),
		zap.Int("totalRobots", cleanup.TotalRobots),
		zap.Int("timeoutRobots", cleanup.TimeoutRobots),
		zap.Int("luaSkipped", cleanup.LuaSkipped))
	return cleanup
}

// resetBots 停止并清空所有已有机器人，但保持 Manager 可继续创建新机器人。
// 与 StopAll 不同：不 cancel context、不关闭 doneCh。
// 并发 Close：单个 robot 卡死（如 lua 嵌套回调死锁）不应阻塞阶段切换。
func (m *Manager) resetBots() CleanupStatus {
	m.mu.Lock()
	robots := make([]*Robot, len(m.robots))
	copy(robots, m.robots)
	m.robots = m.robots[:0]
	m.mu.Unlock()

	cleanup := closeRobotsConcurrent(robots, CleanupReasonRampReset)
	// 重置计数器，使后续阶段的 onRobotDone 匹配逻辑正确
	m.started.Add(int32(-len(robots)))
	m.stopped.Store(0)
	stresslog.Info("[MANAGER] 阶段重置完成，已停止机器人",
		zap.Int("count", len(robots)),
		zap.String("cleanup", string(cleanup.Status)))
	return cleanup
}

// onRobotDone 在单个 Robot 执行 goroutine 结束后回调。
// 当所有已启动的 Robot 都结束时，自动关闭 doneCh，使 task_runner 的 select 退出。
func (m *Manager) onRobotDone(r *Robot, cleanup CleanupStatus) {
	m.mu.Lock()
	for i, item := range m.robots {
		if item == r {
			copy(m.robots[i:], m.robots[i+1:])
			m.robots[len(m.robots)-1] = nil
			m.robots = m.robots[:len(m.robots)-1]
			break
		}
	}
	m.mu.Unlock()
	m.recordCleanup(cleanup)
	if m.stopped.Add(1) == m.started.Load() {
		m.stopOnce.Do(func() { close(m.doneCh) })
	}
}

func (m *Manager) recordCleanup(cleanup CleanupStatus) {
	m.cleanupMu.Lock()
	defer m.cleanupMu.Unlock()
	if m.cleanupSummary.Status == "" {
		m.cleanupSummary = cleanup
		return
	}
	m.cleanupSummary = MergeCleanupStatus(cleanup.Reason, m.cleanupSummary, cleanup)
}

func (m *Manager) CleanupSummary() CleanupStatus {
	m.cleanupMu.Lock()
	defer m.cleanupMu.Unlock()
	if m.cleanupSummary.Status == "" {
		return emptyCleanupSummary(CleanupReasonNatural)
	}
	return m.cleanupSummary
}

// Done 返回一个 channel，所有机器人停止后关闭（定时到期或外部 StopAll 均会触发）。
func (m *Manager) Done() <-chan struct{} {
	return m.doneCh
}

// startDurationTimer 启动运行时长定时器，到期后自动 StopAll。
func (m *Manager) startDurationTimer() {
	if m.cfg.Duration <= 0 {
		return
	}
	stresslog.Info("[MANAGER] 定时停止已设定", zap.Duration("duration", m.cfg.Duration))
	utils.GetWorkPool().Go(func() {
		select {
		case <-m.doneCh:
		case <-time.After(m.cfg.Duration):
			stresslog.Info("[MANAGER] 运行时长已到，自动停止")
			m.StopAll()
		}
	})
}
