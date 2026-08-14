package robot

import (
	"context"
	"fmt"
	flowdef "stressbot/flow"
	"stressbot/internal/stresslog"
	"stressbot/internal/workpool"
	"stressbot/network"
	"stressbot/protocol"
	"stressbot/protocol/protox"
	"stressbot/script"
	"stressbot/state/shared"
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
	StartNumber   int               `json:"startNumber"` // 任务账号编号基数
	StartIndex    int               `json:"startIndex"`  // 本 Manager 的任务全局机器人序号起点（0-based）
	Count         int               `json:"count"`
	ConcurrentNum int               `json:"concurrentNum"`
	StateExtra    map[string]string `json:"stateExtra"`
	// CodecResolver 按「server 串 <proto>:<service>」解析每条连接的 Go SchemaAdapter。
	// 全 codec 路径（dial/decode/encode/心跳/listen/业务 Lua）共享同一份 codec 映射：
	//   - dial/decode：Robot.ConnectTCP/UDP 拨号前 Resolve，nil → fail loud；非 nil 注入 Connection；
	//   - encode/心跳/listen：engine.ActionExecutor / robotActionHandler / netSenderAdapter 各自 Resolve；
	//   - 业务 Lua：经 script.Context.Resolver 在 api_network.go 内 Resolve。
	// 全链路由 main.go/task_runner 启动期 LoadCodecResolver 构造并透传。
	// 业务 codec 不再经 Lua，生产路径只接收 CodecResolver。
	CodecResolver  protocol.CodecResolver `json:"-"`
	RequestTimeout time.Duration          `json:"requestTimeout"`
	MainService    string                 `json:"mainService"`
	HTTPTimeout    time.Duration          `json:"httpTimeout"`
	RampUp         *RampUpConfig          `json:"rampUp"`
	Duration       time.Duration          `json:"duration"` // 运行时长，0 = 一直运行
	// Shared 任务级共享状态后端（可为 nil，表示未启用）。Manager 仅透传给每个 Robot，
	// 不负责 Cleanup/Close：单机由 cmd/agent 负责，分布式由 Agent(Close)+Admin(Cleanup) 负责。
	Shared shared.Store `json:"-"`
}

// Manager 机器人管理器。
type Manager struct {
	cfg     ManagerConfig
	flow    *flowdef.TaskFlow
	factory *protox.Factory
	dialer  *network.Dialer
	luaPool *script.RuntimePool
	robots  []*Robot
	// robotIdx：robot → 其在 robots 切片中的下标。onRobotDone 据此 O(1) swap-delete，
	// 避免大规模机器人同时退出时逐个线性扫描 robots 造成 O(n²) 清理开销。
	robotIdx map[*Robot]int
	mu       sync.RWMutex
	ctx      context.Context
	cancel   context.CancelFunc

	// 生命周期计数（替代旧 started/stopped 对，消除 ramp-up 竞态）：
	//   - active：当前代存活 robot 数（startBatch 创建时 +1，onRobotDone -1）；
	//   - generation：代号，resetBots 递增；onRobotDone 按创建时捕获的代号归属，
	//     旧代回调（gen 不符）不得触碰当前代 active/doneCh，隔离阶段重置的异步回调污染；
	//   - creationDone：创建阶段是否结束（Manager 不再新建 robot）。
	// doneCh 关闭的唯一条件：creationDone==true 且 active==0。这一条同时消灭
	// "错过完成事件（永不结束）""阶段间瞬时归零（提前结束）""旧代回调污染计数"三类问题。
	active       atomic.Int32
	generation   atomic.Int32
	creationDone atomic.Bool
	doneCh       chan struct{} // 创建阶段结束且所有机器人停止后关闭
	stopOnce     sync.Once
	// cleanupSummary 由 m.mu 保护（与 robots 的快照/摘除同锁）：使"机器人仍在 robots 快照里、
	// 未入 summary"与"已出 robots、已入 summary"两态互斥，消除 StopAll 的 prior+closeResult
	// 之间对同一机器人的重复/漏计数。
	cleanupSummary CleanupStatus

	// OnStageReset 进入带 reset 的后续阶段前，上报 reset 前阶段段落指标。
	// 在 resetBots() 完成后调用，用于上报刚结束段落的累计指标并重置采集器。
	// 参数为即将进入的配置阶段下标（0-based，>=1）。
	OnStageReset func(nextStageIdx int)

	// OnStageChange 每个加压阶段开始时调用（含第一阶段）。
	// current 为 1-based 阶段序号，total 为总阶段数。
	// 用于更新监控指标收集器的阶段信息，使前端实时显示正确的加压进度。
	OnStageChange func(current, total int)
}

// NewManager 创建机器人管理器。
//
// parent 是任务级上下文（Agent 模式为 taskCtx，单机模式为 app ctx）：Manager 的 ctx 由它派生，
// 每个 Robot 的 ctx 又由 Manager.ctx 派生，形成 task → manager → robot 取消链。
// 这样任务取消（含 ramp-up 创建阶段进行中）能立即沿链传播，让 startBatch/holdSec 尽快退出，
// 不再依赖"跑完 ramp-up 才在外层 select 命中 ctx.Done"。
func NewManager(parent context.Context, cfg ManagerConfig, flow *flowdef.TaskFlow, factory *protox.Factory,
	dialer *network.Dialer, luaPool *script.RuntimePool) *Manager {
	ctx, cancel := context.WithCancel(parent)
	return &Manager{
		cfg:      cfg,
		flow:     flow,
		factory:  factory,
		dialer:   dialer,
		luaPool:  luaPool,
		robots:   make([]*Robot, 0, cfg.Count),
		robotIdx: make(map[*Robot]int, cfg.Count),
		ctx:      ctx,
		cancel:   cancel,
		doneCh:   make(chan struct{}),
	}
}

// robotIdentity 根据 Manager 分片起点与本地序号计算任务全局身份。
func (c ManagerConfig) robotIdentity(localIndex int) (id, index int, account string) {
	index = c.StartIndex + localIndex
	id = c.StartNumber + index
	account = fmt.Sprintf("%s%d", c.AccountPrefix, id)
	return id, index, account
}

// startBatch 创建 [fromIndex, fromIndex+count) 的 bot，使用 conc 做批量限速。
// 返回实际创建的数量（即使中途被 ctx 取消，调用方也能知道完成了多少）。
func (m *Manager) startBatch(fromIndex, count, conc int) (int, error) {
	created := 0
	// 本批全部归属同一代：generation 只在 resetBots 于阶段之间递增，单次 startBatch 内不变。
	gen := m.generation.Load()
	for i := range count {
		if m.ctx.Err() != nil {
			stresslog.Warn("[MANAGER] 批次创建被取消",
				zap.Int("fromIndex", fromIndex),
				zap.Int("requested", count),
				zap.Int("actuallyCreated", created),
				zap.Error(m.ctx.Err()))
			return created, m.ctx.Err()
		}

		id, index, account := m.cfg.robotIdentity(fromIndex + i)

		r, err := NewRobot(m.ctx, Config{
			ID:             id,
			Index:          index,
			Account:        account,
			StateExtra:     m.cfg.StateExtra,
			HTTPTimeout:    m.cfg.HTTPTimeout,
			RequestTimeout: m.cfg.RequestTimeout,
			MainService:    m.cfg.MainService,
			Shared:         m.cfg.Shared,
		}, m.flow, m.factory, m.cfg.CodecResolver, m.dialer, m.luaPool)
		if err != nil {
			// NewRobot 仅在 resolver nil / LState 不可用时失败（codec 配置错误在
			// 拨号 / 首次 encode 时 fail-loud 上报，便于定位到具体连接）。属于配置 / 资源问题，
			// 重试也没用——跳过这个 robot，日志告警，继续创建其它（避免单个失败拖垮整批）。
			stresslog.Error("[MANAGER] 创建机器人失败，跳过",
				zap.Int("id", id), zap.String("account", account), zap.Error(err))
			continue
		}

		if err := m.addAndStartRobot(gen, r, r.Start); err != nil {
			return created, fmt.Errorf("启动机器人失败 id=%d account=%s: %w", id, account, err)
		}
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

// addAndStartRobot 先登记生命周期状态再启动 Robot；启动失败时原子撤销登记并返回错误。
func (m *Manager) addAndStartRobot(gen int32, r *Robot, start func() error) error {
	m.mu.Lock()
	m.robots = append(m.robots, r)
	m.robotIdx[r] = len(m.robots) - 1
	m.mu.Unlock()

	// 先登记 active 再 Start：杜绝 robot 秒退时 onRobotDone 的 -1 早于本处 +1。
	m.active.Add(1)
	r.onDone = func(rr *Robot, c CleanupStatus) { m.onRobotDone(gen, rr, c) }
	if err := start(); err != nil {
		cleanup := r.cleanup(CleanupReasonNatural, true)
		m.mu.Lock()
		m.removeRobotLocked(r)
		m.recordCleanupLocked(cleanup)
		m.mu.Unlock()
		m.active.Add(-1)
		return err
	}
	return nil
}

// StartAll 一次性创建全部机器人。
func (m *Manager) StartAll() error {
	// defer 保证成功/失败/早退所有路径都标记创建阶段结束，打开 doneCh 关闭门闩。
	defer m.finishCreation()
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
	return m.startDurationTimer()
}

// StartWithRampUp 分阶段创建机器人。
func (m *Manager) StartWithRampUp() error {
	// defer 保证成功/失败/早退所有路径都标记创建阶段结束，打开 doneCh 关闭门闩。
	defer m.finishCreation()
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
		m.mu.RLock()
		curBots := len(m.robots) // 锁内读：onRobotDone 持写锁改 m.robots，锁外读有数据竞争
		m.mu.RUnlock()
		if stage.Reset && curBots > 0 {
			stresslog.Info("[MANAGER] 阶段重置，停止所有机器人",
				zap.Int("nextStage", i+1),
				zap.Int("currentBots", curBots))
			m.resetBots()
			select {
			case <-m.ctx.Done():
				return m.ctx.Err()
			case <-time.After(1 * time.Second):
			}
			if m.OnStageReset != nil {
				m.OnStageReset(i) // i>=1：即将进入的配置阶段下标，作为 reset 边界阶段段落标识
			}
		}

		conc := m.cfg.ConcurrentNum
		if stage.Concurrency > 0 {
			conc = stage.Concurrency
		}
		holdSec := max(stage.HoldSec, 30)

		stresslog.Info("[MANAGER] 启动阶段",
			zap.Int("stage", i+1),
			zap.Int("of", len(stages)),
			zap.Int("count", stage.Count),
			zap.Int("offset", offset),
			zap.Int("concurrency", conc),
			zap.Int("holdSec", holdSec),
			zap.Bool("reset", stage.Reset))

		if m.OnStageChange != nil {
			m.OnStageChange(i+1, len(stages))
		}

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
	return m.startDurationTimer()
}

// closeRobotsTimeout 整体并发关闭机器人的最长时间。
// 单个 robot 内部已有 robotCloseTimeout（10s）兜底，所有 r.Close() 是并发的，
// 因此正常情况下最多 10s 就能完成；这里再加 5s 缓冲覆盖 goroutine 调度延迟。
// 必须显著小于 Admin 的 stopWaitTimeout（60s），避免 Agent 还在 Close 时 Admin
// 已经合成 fake report 把任务标 stopped，导致 Agent 真实上报被丢弃。
const closeRobotsTimeout = 15 * time.Second

// closeRobotsConcurrent 并发关闭一批机器人并等待全部完成。
// 正常提交的任务整体超过 closeRobotsTimeout 后强制返回；协程池拒绝时改为同步清理，
// 确保资源实际释放，不再等待超时后伪造未执行的清理结果。
func closeRobotsConcurrent(robots []*Robot, reason CleanupReason) CleanupStatus {
	return closeRobotsConcurrentWithSubmit(robots, reason, workpool.Default().Submit)
}

func closeRobotsConcurrentWithSubmit(robots []*Robot, reason CleanupReason, submit func(func()) error) CleanupStatus {
	if len(robots) == 0 {
		return emptyCleanupSummary(reason)
	}
	results := make(chan CleanupStatus, len(robots))
	for _, r := range robots {
		if err := submit(func() {
			results <- r.cleanup(reason, false)
		}); err != nil {
			stresslog.Warn("[MANAGER] 批量关闭任务提交失败，同步清理机器人",
				zap.Int("id", r.id), zap.String("account", r.account), zap.Error(err))
			results <- r.cleanup(reason, false)
		}
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
			for range missing {
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
	// 快照 robots 与读取 prior 在同一把 m.mu 内完成：与 onRobotDone 的"摘除 + 记账同锁"配对，
	// 保证快照内的机器人此刻一定尚未进入 cleanupSummary（prior），两部分对同一机器人不重复计数，
	// 也不会漏计（每个机器人要么在 prior、要么在 closeResult）。
	m.mu.Lock()
	robots := make([]*Robot, len(m.robots))
	copy(robots, m.robots)
	prior := m.cleanupSummaryLocked()
	m.mu.Unlock()

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
// 并发 Close：单个 robot 清理卡住（如长时间 Lua action / executor 退出 / 连接清理）
// 不应阻塞阶段切换。
func (m *Manager) resetBots() CleanupStatus {
	// 先递增代号：此后被关闭的旧 robot 的 onRobotDone 归入旧代，被 onRobotDone 的 gen 检查挡下，
	// 不会误改新代 active / 误关 doneCh，隔离异步回调与本处同步调账的竞争。
	m.generation.Add(1)

	m.mu.Lock()
	robots := make([]*Robot, len(m.robots))
	copy(robots, m.robots)
	m.robots = m.robots[:0]
	// 清空下标索引：被重置的旧代机器人已整体摘除，其 onRobotDone(旧代) 再 removeRobotLocked 即 no-op。
	m.robotIdx = make(map[*Robot]int, m.cfg.Count)
	m.mu.Unlock()

	cleanup := closeRobotsConcurrent(robots, CleanupReasonRampReset)
	// 新代 active 基线归零（旧代回调因 gen 不符不会再减 active，故直接 Store 安全）。
	m.active.Store(0)
	stresslog.Info("[MANAGER] 阶段重置完成，已停止机器人",
		zap.Int("count", len(robots)),
		zap.String("cleanup", string(cleanup.Status)))
	return cleanup
}

// onRobotDone 在单个 Robot 执行 goroutine 结束后回调（gen 为该 robot 创建时捕获的代号）。
// 创建阶段结束且当前代所有 Robot 都结束时，关闭 doneCh，使 task_runner 的 select 退出。
func (m *Manager) onRobotDone(gen int32, r *Robot, cleanup CleanupStatus) {
	// 摘除 + 记账在同一临界区内完成（与 StopAll 的"快照 robots + 读 prior"同锁原子），
	// 使该机器人对外只呈现两态之一：仍在 robots 快照且未入 summary，或已出 robots 且已入 summary。
	m.mu.Lock()
	m.removeRobotLocked(r)
	m.recordCleanupLocked(cleanup)
	m.mu.Unlock()

	// 旧代回调：resetBots 已把该代整体清零并递增 generation，此处不得再改当前代 active
	// 或关闭 doneCh，否则会污染新阶段计数。摘除+记账已在上方完成（对已截断的 robots 为 no-op）。
	if gen != m.generation.Load() {
		return
	}
	// 关闭门闩：仅当创建阶段结束（不再新建）且当前代无存活 robot 时关闭。
	if m.active.Add(-1) == 0 && m.creationDone.Load() {
		m.stopOnce.Do(func() { close(m.doneCh) })
	}
}

// removeRobotLocked 从 robots 切片中 O(1) 摘除 r（swap-delete），并维护 robotIdx。
// 调用方必须持有 m.mu 写锁。r 不在索引中（已被 resetBots 整体清空）时为 no-op。
func (m *Manager) removeRobotLocked(r *Robot) {
	idx, ok := m.robotIdx[r]
	if !ok {
		return
	}
	last := len(m.robots) - 1
	if idx != last {
		moved := m.robots[last]
		m.robots[idx] = moved
		m.robotIdx[moved] = idx
	}
	m.robots[last] = nil
	m.robots = m.robots[:last]
	delete(m.robotIdx, r)
}

// finishCreation 标记创建阶段结束（本 Manager 不再新建 robot），打开 doneCh 关闭门闩。
// 若此刻已无存活 robot（全部提前结束），立即关闭 doneCh。
// 语义为"创建阶段已终结"，故 StartAll/StartWithRampUp 的所有返回路径（成功/失败/早退）
// 都应以 defer 触发；与 onRobotDone 的关闭判定交叉覆盖（一方先置 creationDone 再读 active，
// 另一方先减 active 再读 creationDone），stopOnce 去重防双关。
func (m *Manager) finishCreation() {
	m.creationDone.Store(true)
	if m.active.Load() == 0 {
		m.stopOnce.Do(func() { close(m.doneCh) })
	}
}

// recordCleanupLocked 将单个机器人的清理结果并入汇总。调用方必须持有 m.mu 写锁。
func (m *Manager) recordCleanupLocked(cleanup CleanupStatus) {
	if m.cleanupSummary.Status == "" {
		m.cleanupSummary = cleanup
		return
	}
	m.cleanupSummary = MergeCleanupStatus(cleanup.Reason, m.cleanupSummary, cleanup)
}

// cleanupSummaryLocked 返回当前汇总。调用方必须持有 m.mu（读/写皆可，此处由写锁调用）。
func (m *Manager) cleanupSummaryLocked() CleanupStatus {
	if m.cleanupSummary.Status == "" {
		return emptyCleanupSummary(CleanupReasonNatural)
	}
	return m.cleanupSummary
}

// CleanupSummary 返回当前累计的清理汇总（对外只读快照）。
func (m *Manager) CleanupSummary() CleanupStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cleanupSummaryLocked()
}

// Done 返回一个 channel，所有机器人停止后关闭（定时到期或外部 StopAll 均会触发）。
func (m *Manager) Done() <-chan struct{} {
	return m.doneCh
}

// startDurationTimer 启动运行时长定时器，到期后自动 StopAll。
func (m *Manager) startDurationTimer() error {
	return m.startDurationTimerWithSubmit(workpool.Default().Submit)
}

func (m *Manager) startDurationTimerWithSubmit(submit func(func()) error) error {
	if m.cfg.Duration <= 0 {
		return nil
	}
	if err := submit(func() {
		timer := time.NewTimer(m.cfg.Duration)
		defer timer.Stop()
		select {
		case <-m.doneCh:
		case <-timer.C:
			stresslog.Info("[MANAGER] 运行时长已到，自动停止")
			m.StopAll()
		}
	}); err != nil {
		stresslog.Error("[MANAGER] 提交定时停止任务失败",
			zap.Duration("duration", m.cfg.Duration),
			zap.Error(err))
		return fmt.Errorf("提交定时停止任务失败: %w", err)
	}
	stresslog.Info("[MANAGER] 定时停止已设定", zap.Duration("duration", m.cfg.Duration))
	return nil
}
