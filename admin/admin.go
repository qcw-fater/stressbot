package admin

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"stressbot/monitor"
	"stressbot/robot"
	"stressbot/sharedstate"
	"stressbot/utils"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// AdminServer Admin HTTP 服务器。
type AdminServer struct {
	cfg Config

	tasks      *TaskStore
	agents     *AgentRegistry
	aggregator *MetricsAggregator
	dispatcher *AgentDispatcher
	assigner   *Assigner

	logsProxyClient *http.Client // Agent 日志代理（5s 超时）

	history *HistoryStore      // 可选
	flows   *FlowTemplateStore // 可选（流程模板库，依赖全局 MySQL）
	sampler *Sampler           // 可选

	db *sql.DB // 全局共享 MySQL 连接池（HistoryStore 复用，由 AdminServer 统一 Close）

	sharedCleanup *sharedCleanupQueue // 共享状态待清理队列（可选）

	httpSrv *http.Server
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

func NewAdminServer(cfg Config) (*AdminServer, error) {
	s := &AdminServer{cfg: cfg, stopCh: make(chan struct{})}

	// 1. TaskStore
	tasks, err := NewTaskStore("data")
	if err != nil {
		return nil, fmt.Errorf("init task store: %w", err)
	}
	s.tasks = tasks

	// 2. AgentRegistry
	s.agents = NewAgentRegistry(cfg.AgentRegistry, s.onAgentStatusChange)

	// 3. MetricsAggregator
	s.aggregator = NewMetricsAggregator(s.agents)

	// 4. AgentDispatcher
	s.dispatcher = NewAgentDispatcher()

	// 5. Logs proxy client
	s.logsProxyClient = &http.Client{Timeout: 5 * time.Second}

	// 6. Assigner
	s.assigner = NewAssigner()

	// 7. HistoryStore（可选）+ 共享全局 MySQL 连接池
	//    MySQL 由 Config.MySQL 统一配置，HistoryStore 不再自管 *sql.DB 生命周期。
	//    装配顺序：openDB → initMySQLSchema → NewHistoryStore(db, cfg)。
	if cfg.MySQL != nil {
		db, err := openDB(*cfg.MySQL)
		if err != nil {
			return nil, fmt.Errorf("connect mysql: %w", err)
		}
		if err := initMySQLSchema(db); err != nil {
			db.Close()
			return nil, fmt.Errorf("init mysql schema: %w", err)
		}
		stresslog.Info("[ADMIN] MySQL 已连接",
			zap.String("addr", fmt.Sprintf("%s:%d", cfg.MySQL.Host, cfg.MySQL.Port)),
			zap.String("database", cfg.MySQL.Database))

		if cfg.History != nil {
			s.history = NewHistoryStore(db, cfg.History)
			sampler := NewSampler(
				10*time.Second,
				s.aggregator, s.history, s.agents, s.tasks,
			)
			s.sampler = sampler
		}
		s.db = db // 由 AdminServer 统一 Close（Shutdown）
		// 流程模板库依赖全局 MySQL，与 history 相互独立：未配 history 时流程库仍可用。
		s.flows = NewFlowTemplateStore(db)
	} else {
		stresslog.Info("[ADMIN] MySQL 未配置：历史归档模块未启用，" +
			"所有 /api/history* 接口将返回 HISTORY_DISABLED")
	}

	// 8. 终态回调
	s.tasks.SetOnTerminal(s.onTaskTerminal)

	// 9. 共享状态（Redis）：验证连通性 + 初始化待清理队列
	if cfg.RedisEnabled() {
		resolved, rerr := cfg.Redis.Resolve()
		if rerr != nil {
			return nil, fmt.Errorf("共享状态配置无效: %w", rerr)
		}
		// 启动时 PING 验证 Redis 连通性（不持久占用连接）
		pingStore, perr := sharedstate.NewRedisStore(resolved, "admin-ping")
		if perr != nil {
			return nil, fmt.Errorf("连接共享状态(Redis)失败 (addr=%s): %w", resolved.AddrMasked(), perr)
		}
		_ = pingStore.Close()
		stresslog.Info("[ADMIN] 共享状态已启用",
			zap.String("addr", resolved.AddrMasked()),
			zap.Int("dbIndex", resolved.DBIndex),
			zap.String("keyPrefix", resolved.KeyPrefix))
		s.sharedCleanup = newSharedCleanupQueue("data")
	} else {
		stresslog.Info("[ADMIN] 共享状态未启用：未配置 Redis（redis.host 为空），" +
			"脚本中使用 require(\"share\") 将报错")
	}

	return s, nil
}

// Run 启动 Admin 服务器（阻塞）。
func (s *AdminServer) Run() error {
	// 初始化协程池
	utils.InitWorkPool(nil)

	// 启动心跳检测
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.agents.StartHealthChecker(ctx)

	// 启动定时清理（可选）
	if s.history != nil {
		s.history.StartPruneLoop(ctx)
	}

	// 启动 deadline 看门狗
	s.startDeadlineWatchdog(ctx)

	// 启动共享状态清理重试（恢复上次残留 + 定时重试）
	s.startSharedCleanupRetry(ctx)

	// 注册路由（已包裹 recover 中间件）。
	//
	// timeout 配置：
	//   - ReadHeaderTimeout 防止慢客户端攻击型连接占住 server goroutine 不放，
	//     避免堆积导致 accept queue 满；
	//   - IdleTimeout 让长期空闲的 keep-alive 连接主动关闭，
	//     防止 agent → admin 的 client-side 池保留大量已不再使用的连接；
	//   - ReadTimeout/WriteTimeout 故意不设：history 导出/日志下载可能很长。
	s.httpSrv = &http.Server{
		Addr:              fmt.Sprintf(":%d", s.cfg.Port),
		Handler:           s.registerRoutes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	stresslog.Info("admin 启动",
		zap.Int("port", s.cfg.Port),
		zap.Bool("history", s.history != nil),
		zap.Bool("redis", s.cfg.RedisEnabled()))

	// 信号处理
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	utils.GetWorkPool().Go(func() {
		<-sigCh
		stresslog.Info("收到退出信号，开始关闭...")
		s.Shutdown(context.Background())
	})

	if err := s.httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// Shutdown 优雅关闭。
func (s *AdminServer) Shutdown(ctx context.Context) error {
	if s.httpSrv != nil {
		// 先禁用 keep-alive，让空闲连接自行关闭，减少 Windows 上 Closesocket 竞争窗口
		s.httpSrv.SetKeepAlivesEnabled(false)
		shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		s.httpSrv.Shutdown(shutdownCtx)
	}
	if s.history != nil {
		s.history.Close()
	}
	// 全局共享 MySQL 连接池：HistoryStore 不再 Close，由 AdminServer 统一关闭。
	if s.db != nil {
		_ = s.db.Close()
	}
	utils.GetWorkPool().Shutdown()
	close(s.stopCh)
	return nil
}

func (s *AdminServer) onTaskTerminal(task *Task) {
	// 停止 Sampler
	if s.sampler != nil {
		s.sampler.Stop(task.ID)
	}

	// 共享状态统一清理：任务已到终态（所有 Agent 已上报或超时），此时删除该 runId
	// 下的所有 key 是安全的。由 Admin 统一做，避免多 Agent 各自清理时的竞态。
	s.cleanupSharedState(task)

	// 异步归档
	if s.history == nil {
		return
	}
	taskID := task.ID
	utils.GetWorkPool().Go(func() {
		// 优先用 agent 终止报告聚合，兜底用心跳聚合
		finalStress := buildFinalStressFromReports(task)
		if finalStress == nil || len(finalStress.Actions) == 0 {
			finalStress = s.aggregator.AggregateStress(taskID).Snapshot
		}
		finalSys := s.aggregator.AggregateSystem()
		if err := s.history.Archive(context.Background(), task, finalStress, finalSys); err != nil {
			stresslog.Error("任务归档失败",
				zap.String("taskID", taskID),
				zap.Error(err))
		}
	})
}

// cleanupSharedState 在任务终态时统一清理该任务的共享状态命名空间。
// 仅当任务使用了共享状态且服务器配置了 Redis 时执行。清理登记到待清理队列：
// 立即尝试一次，失败则持久化并由定时任务/重启后重试，避免无 TTL 的 key 永久泄漏。
func (s *AdminServer) cleanupSharedState(task *Task) {
	if task == nil || !task.SharedUsed || !s.cfg.RedisEnabled() {
		return
	}
	runID := task.SharedRunID
	if runID == "" {
		runID = task.ID
	}
	s.enqueueSharedCleanup(runID)
}

// buildFinalStressFromReports 从 agent 终止报告聚合最终快照（优先于心跳聚合）。
func buildFinalStressFromReports(task *Task) *monitor.CollectorSnapshot {
	if task == nil || len(task.Reports) == 0 {
		return nil
	}
	snaps := make([]*monitor.CollectorSnapshot, 0, len(task.Reports))
	for _, r := range task.Reports {
		// 过滤 nil 或零值快照
		if r.FinalSnapshot == nil {
			continue
		}
		if r.FinalSnapshot.UptimeSec == 0 && len(r.FinalSnapshot.Actions) == 0 {
			continue
		}
		snaps = append(snaps, r.FinalSnapshot)
	}
	if len(snaps) == 0 {
		return nil
	}
	return monitor.MergeSnapshots(snaps)
}

func (s *AdminServer) onAgentStatusChange(agentID string, from, to AgentStatus) {
	task := s.tasks.ActiveTask()
	if task == nil {
		return
	}

	// 检查是否是活跃任务的分配节点
	isAssigned := false
	var agentName string
	for _, a := range task.Assignments {
		if a.AgentID == agentID {
			isAssigned = true
			agentName = a.AgentName
			break
		}
	}
	if !isAssigned {
		return
	}

	// 已分配节点重新注册（busy → idle / unhealthy → idle 等异常路径）：
	// 视为 Agent 进程重启，任务在该节点上已丢失（用户需求 §2.3：重连后是新连接，不再补档）。
	// 立即为该 Agent 合成 failed report，避免任务因等待"永远不会到来的"完成上报而卡死。
	if (from == AgentBusy || from == AgentUnhealthy) && to == AgentIdle {
		s.tasks.Update(task.ID, func(t *Task) {
			t.AgentEvents = append(t.AgentEvents, AgentEvent{
				AgentID:   agentID,
				AgentName: agentName,
				Type:      "restarted",
				Timestamp: time.Now(),
				Detail:    "Agent 重新注册，已分配任务在该节点丢失",
			})
			if t.Reports == nil {
				t.Reports = make(map[string]TaskCompletionReport)
			}
			if _, exists := t.Reports[agentID]; !exists {
				cleanup := robot.UnknownCleanupStatus(robot.CleanupReasonOfflineSynthetic, "节点重新注册，清理状态未知")
				t.Reports[agentID] = TaskCompletionReport{
					AgentID:       agentID,
					TaskID:        t.ID,
					Result:        ResultFailed,
					ErrorMsg:      "Agent 重新注册，任务已丢失",
					FinishedAt:    time.Now(),
					CleanupStatus: &cleanup,
				}
			}
		})
		stresslog.Warn("[ADMIN] 分配节点重新注册，任务在该节点已丢失",
			zap.String("taskID", task.ID),
			zap.String("agentID", agentID),
			zap.String("agentName", agentName),
			zap.String("from", string(from)))

		// 检查是否所有分配节点都已"失效"（offline 或已合成 report），
		// 是则触发 autoStopTask；否则任务继续。
		s.checkAndStopIfAllLost(task.ID)
		return
	}

	// 节点恢复（offline → idle/busy）：记录 reconnected 事件
	if from == AgentOffline && (to == AgentIdle || to == AgentBusy) {
		s.tasks.Update(task.ID, func(t *Task) {
			t.AgentEvents = append(t.AgentEvents, AgentEvent{
				AgentID:   agentID,
				AgentName: agentName,
				Type:      "reconnected",
				Timestamp: time.Now(),
			})
		})
		stresslog.Info("[ADMIN] 分配节点恢复",
			zap.String("taskID", task.ID),
			zap.String("agentID", agentID),
			zap.String("agentName", agentName))
		return
	}

	// 节点离线：记录事件
	if to != AgentOffline {
		return
	}

	s.tasks.Update(task.ID, func(t *Task) {
		t.AgentEvents = append(t.AgentEvents, AgentEvent{
			AgentID:   agentID,
			AgentName: agentName,
			Type:      "offline",
			Timestamp: time.Now(),
			Detail:    "心跳超时",
		})
	})

	stresslog.Warn("[ADMIN] 分配节点离线",
		zap.String("taskID", task.ID),
		zap.String("agentID", agentID),
		zap.String("agentName", agentName))

	// 任务正在 stopping 时节点离线 → 立刻合成 report
	if task.State == TaskStopping {
		complete := false
		s.tasks.Update(task.ID, func(t *Task) {
			if t.Reports == nil {
				t.Reports = make(map[string]TaskCompletionReport)
			}
			if _, exists := t.Reports[agentID]; !exists {
				cleanup := robot.UnknownCleanupStatus(robot.CleanupReasonOfflineSynthetic, "节点离线，清理状态未知")
				t.Reports[agentID] = TaskCompletionReport{
					AgentID:       agentID,
					TaskID:        task.ID,
					Result:        ResultFailed,
					ErrorMsg:      "节点离线",
					FinishedAt:    time.Now(),
					CleanupStatus: &cleanup,
				}
			}
			if len(t.Reports) == len(t.Assignments) {
				t.CleanupSummary = aggregateTaskCleanup(t)
				complete = true
			}
		})
		if complete {
			if _, err := s.tasks.Transition(task.ID, TaskStopping, TaskStopped); err != nil {
				stresslog.Warn("[ADMIN] 状态转换失败", zap.String("taskID", task.ID), zap.Error(err))
			}
		}
		return
	}

	// 任务 running 时节点离线 → 检查是否所有分配节点都已失效（offline 或已合成 report）
	s.checkAndStopIfAllLost(task.ID)
}

// aggregateTaskCleanup 把任务所有节点的 report.CleanupStatus 合并为任务级清理摘要。
// 缺失 CleanupStatus 的节点（如旧 Agent 或未上报）按 unknown 计入，避免误判为"清理完成"。
func aggregateTaskCleanup(t *Task) *robot.CleanupStatus {
	if t == nil || len(t.Reports) == 0 {
		return nil
	}
	statuses := make([]robot.CleanupStatus, 0, len(t.Reports))
	for _, report := range t.Reports {
		if report.CleanupStatus != nil {
			statuses = append(statuses, *report.CleanupStatus)
		} else {
			statuses = append(statuses, robot.UnknownCleanupStatus(robot.CleanupReasonStopWaitTimeout, "节点未上报清理结果"))
		}
	}
	cleanup := robot.MergeCleanupStatus(robot.CleanupReasonAdminStop, statuses...)
	return &cleanup
}

// synthesizeOfflineReports 为已离线且未上报的分配节点合成 stopped report。
// 返回 true 表示所有分配节点都已有 report（可以立刻转 stopped）。
func (s *AdminServer) synthesizeOfflineReports(taskID string) bool {
	if _, ok := s.tasks.Get(taskID); !ok {
		return false
	}
	allReported := true
	s.tasks.Update(taskID, func(t *Task) {
		if t.Reports == nil {
			t.Reports = make(map[string]TaskCompletionReport)
		}
		targets := t.SucceededAgents
		if len(targets) == 0 {
			targets = make([]string, 0, len(t.Assignments))
			for _, a := range t.Assignments {
				targets = append(targets, a.AgentID)
			}
		}
		for _, agentID := range targets {
			if _, exists := t.Reports[agentID]; exists {
				continue
			}
			node, nodeOk := s.agents.Get(agentID)
			if !nodeOk || node.Status == AgentOffline {
				cleanup := robot.UnknownCleanupStatus(robot.CleanupReasonOfflineSynthetic, "节点离线，未上报清理结果")
				t.Reports[agentID] = TaskCompletionReport{
					AgentID:       agentID,
					TaskID:        taskID,
					Result:        ResultStopped,
					ErrorMsg:      "节点离线，未上报",
					FinishedAt:    time.Now(),
					CleanupStatus: &cleanup,
				}
				stresslog.Info("[ADMIN] 合成离线节点报告",
					zap.String("taskID", taskID), zap.String("agentID", agentID),
					zap.String("reason", func() string {
						if !nodeOk {
							return "节点未找到"
						}
						return "节点离线"
					}()))
			} else {
				allReported = false
			}
		}
		expected := len(targets)
		if allReported && len(t.Reports) < expected {
			allReported = false
		}
		if allReported {
			t.CleanupSummary = aggregateTaskCleanup(t)
		}
	})
	return allReported
}

// stopWaitTimeout 停止超时安全网时长。
// 必须 ≥ Agent 端 Manager.closeRobotsTimeout（15s）+ Robot.Close 兜底（10s）
// + 网络上报延迟，否则 Agent 真实上报到达前 Admin 已合成 fake report 并归档，
// 导致 Agent 实际指标快照被丢弃。当前设为 60s 提供充足缓冲。
const stopWaitTimeout = 60 * time.Second

// startStopTimeout 启动停止超时安全网。
// stopWaitTimeout 后如果任务仍在 stopping，为剩余未上报节点合成 report 并转 stopped。
func (s *AdminServer) startStopTimeout(taskID string) {
	utils.GetWorkPool().Go(func() {
		time.Sleep(stopWaitTimeout)
		task, ok := s.tasks.Get(taskID)
		if !ok || task.State != TaskStopping {
			return
		}
		stresslog.Warn("[ADMIN] 停止超时，合成未上报节点的 report",
			zap.String("taskID", taskID),
			zap.Int("reported", len(task.Reports)),
			zap.Int("total", len(task.SucceededAgents)))
		s.tasks.Update(taskID, func(t *Task) {
			if t.Reports == nil {
				t.Reports = make(map[string]TaskCompletionReport)
			}
			targets := t.SucceededAgents
			if len(targets) == 0 {
				targets = make([]string, 0, len(t.Assignments))
				for _, a := range t.Assignments {
					targets = append(targets, a.AgentID)
				}
			}
			for _, agentID := range targets {
				if _, exists := t.Reports[agentID]; !exists {
					cleanup := robot.UnknownCleanupStatus(robot.CleanupReasonStopWaitTimeout, "停止等待超时，节点未响应，清理状态未知")
					t.Reports[agentID] = TaskCompletionReport{
						AgentID:       agentID,
						TaskID:        taskID,
						Result:        ResultStopped,
						ErrorMsg:      "停止超时，节点未响应",
						FinishedAt:    time.Now(),
						CleanupStatus: &cleanup,
					}
				}
			}
			t.CleanupSummary = aggregateTaskCleanup(t)
		})
		if _, err := s.tasks.Transition(taskID, TaskStopping, TaskStopped); err != nil {
			stresslog.Warn("[ADMIN] 状态转换失败", zap.String("taskID", taskID), zap.Error(err))
		}
	})
}

// checkAndStopIfAllLost 检查任务的所有分配节点是否都已 offline 或已合成 report。
// 是 → 调用 autoStopTask 收尾；否则不动作。
//
// 用户需求 §3.2：单节点丢失不停止任务，全部节点丢失才停止。
func (s *AdminServer) checkAndStopIfAllLost(taskID string) {
	task, ok := s.tasks.Get(taskID)
	if !ok || !IsActiveState(task.State) {
		return
	}
	anyAlive := false
	targets := task.SucceededAgents
	if len(targets) == 0 {
		targets = make([]string, 0, len(task.Assignments))
		for _, a := range task.Assignments {
			targets = append(targets, a.AgentID)
		}
	}
	for _, agentID := range targets {
		if _, hasReport := task.Reports[agentID]; hasReport {
			continue
		}
		node, nodeOk := s.agents.Get(agentID)
		if nodeOk && node.Status != AgentOffline && node.CurrentTaskID == taskID {
			anyAlive = true
			break
		}
	}
	if !anyAlive {
		stresslog.Error("[ADMIN] 所有分配节点已失效（offline 或 restarted），自动停止任务",
			zap.String("taskID", taskID))
		s.autoStopTask(taskID, "所有分配节点已失效")
	}
}

// autoStopTask 自动停止任务（deadline 超时或全部节点失效）。
func (s *AdminServer) autoStopTask(taskID string, reason string) {
	task, ok := s.tasks.Get(taskID)
	if !ok || !IsActiveState(task.State) {
		return
	}

	if task.State == TaskStarting {
		// TaskStarting 阶段所有节点失效：直接转 TaskFailed，无需发送停止 RPC
		if _, err := s.tasks.Transition(taskID, TaskStarting, TaskFailed); err != nil {
			stresslog.Error("[ADMIN] 自动停止任务状态转换失败", zap.Error(err))
		}
		return
	}

	if task.State == TaskRunning {
		if _, err := s.tasks.Transition(taskID, TaskRunning, TaskStopping); err != nil {
			stresslog.Error("[ADMIN] 自动停止任务状态转换失败", zap.Error(err))
			return
		}
	}

	task, _ = s.tasks.Get(taskID)
	targets := task.SucceededAgents
	if len(targets) == 0 {
		targets = make([]string, 0, len(task.Assignments))
		for _, a := range task.Assignments {
			targets = append(targets, a.AgentID)
		}
	}
	for _, agentID := range targets {
		node, ok := s.agents.Get(agentID)
		if ok && node.Status != AgentOffline {
			if err := s.dispatcher.Stop(node.Address, taskID); err != nil {
				stresslog.Warn("[ADMIN] 停止节点任务失败",
					zap.String("agentID", agentID), zap.Error(err))
			}
		}
	}

	s.tasks.Update(taskID, func(t *Task) {
		if t.Reports == nil {
			t.Reports = make(map[string]TaskCompletionReport)
		}
		targets := t.SucceededAgents
		if len(targets) == 0 {
			targets = make([]string, 0, len(t.Assignments))
			for _, a := range t.Assignments {
				targets = append(targets, a.AgentID)
			}
		}
		for _, agentID := range targets {
			if _, ok := t.Reports[agentID]; !ok {
				cleanup := robot.UnknownCleanupStatus(robot.CleanupReasonStopWaitTimeout, "节点已失效，清理状态未知")
				t.Reports[agentID] = TaskCompletionReport{
					AgentID:       agentID,
					TaskID:        taskID,
					Result:        ResultFailed,
					ErrorMsg:      reason,
					FinishedAt:    time.Now(),
					CleanupStatus: &cleanup,
				}
			}
		}
		t.CleanupSummary = aggregateTaskCleanup(t)
	})

	if _, err := s.tasks.Transition(taskID, TaskStopping, TaskFailed); err != nil {
		stresslog.Warn("[ADMIN] 状态转换失败", zap.String("taskID", taskID), zap.Error(err))
	}
}

var idCounter uint64

func generateID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		binary.BigEndian.PutUint64(b, uint64(time.Now().UnixNano()))
		binary.BigEndian.PutUint64(b[8:], atomic.AddUint64(&idCounter, 1))
	}
	return hex.EncodeToString(b)
}

// startDeadlineWatchdog 定期检查活跃任务是否超时。
func (s *AdminServer) startDeadlineWatchdog(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	utils.GetWorkPool().GoWithStop(func(stopCh <-chan struct{}) {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-ticker.C:
				func() {
					task := s.tasks.ActiveTask()
					if task == nil || task.State != TaskRunning {
						return
					}
					if task.Config.Deadline != nil && time.Now().After(*task.Config.Deadline) {
						stresslog.Info("[ADMIN] 任务超时，自动停止",
							zap.String("taskID", task.ID),
							zap.Time("deadline", *task.Config.Deadline))
						s.autoStopTask(task.ID, "任务超时")
					}
				}()
			}
		}
	})
}
