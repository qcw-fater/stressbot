package admin

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"stressbot/admin/agent"
	"stressbot/admin/apierror"
	"stressbot/admin/bundle"
	"stressbot/admin/grpcapi"
	"stressbot/admin/history"
	"stressbot/admin/metrics"
	"stressbot/admin/mysql"
	admintask "stressbot/admin/task"
	"stressbot/admin/template"
	"stressbot/config"
	"stressbot/controlplane/pb"
	"sync"
	"sync/atomic"
	"time"

	"stressbot/admin/command"
	"stressbot/internal/stresslog"
	"stressbot/internal/workpool"
	"stressbot/monitor"
	"stressbot/robot"
	"stressbot/state/shared"

	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// AdminServer 同时承载浏览器管理面与 Admin-Agent gRPC 控制面。
type AdminServer struct {
	cfg Config

	tasks           *admintask.TaskStore
	agents          *agent.AgentRegistry
	aggregator      *metrics.Aggregator
	metricsWindows  *metrics.MetricsWindowStore
	assigner        *admintask.Assigner
	bundles         *bundle.Store
	sessions        *grpcapi.SessionRegistry
	commandStore    command.Store
	commandBus      *command.Bus
	commandDispatch *command.Dispatcher
	completion      *admintask.CompletionService
	metricsAccept   *metrics.AcceptanceService
	metricsIngestor *metrics.Ingestor

	history         *history.Store                // 可选
	flows           *template.FlowTemplateStore   // 可选（流程模板库，依赖全局 MySQL）
	actionTemplates *template.ActionTemplateStore // 可选（共享动作模板库，依赖全局 MySQL）
	listenTemplates *template.ListenTemplateStore // 可选（共享监听模板库，依赖全局 MySQL）
	sampler         *metrics.Sampler              // 可选

	db *sql.DB // 全局共享 MySQL 连接池（HistoryStore 复用，由 AdminServer 统一 Close）

	sharedCleanup *admintask.SharedCleanup // 共享状态待清理队列（可选）

	managementSrv *http.Server
	grpcSrv       *grpc.Server
	runtimeCtx    context.Context
	runtimeCancel context.CancelFunc
	stopCh        chan struct{}
	shutdownOnce  sync.Once
	shutdownErr   error
	shutdownPool  func()
}

func NewAdminServer(cfg Config) (*AdminServer, error) {
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	s := &AdminServer{
		cfg:           cfg,
		runtimeCtx:    runtimeCtx,
		runtimeCancel: runtimeCancel,
		stopCh:        make(chan struct{}),
		shutdownPool:  func() { workpool.GetWorkPool().Shutdown() },
	}
	assembled := false
	defer func() {
		if !assembled {
			runtimeCancel()
			if s.db != nil {
				_ = s.db.Close()
			}
		}
	}()

	// 1. MySQL 必须在装配任何运行时 store 和开放 listener 前完成当前结构初始化。
	// 任一步失败都关闭连接并让进程以非零状态退出，不提供半健康 HTTP 服务。
	if cfg.MySQLEnabled() {
		db, err := mysql.Open(cfg.MySQL.DSN(), mysql.PoolConfig{
			MaxOpenConns: cfg.MySQL.Pool.MaxOpenConns, MaxIdleConns: cfg.MySQL.Pool.MaxIdleConns,
			ConnMaxLifetime: cfg.MySQL.Pool.ConnMaxLifetime,
		})
		if err != nil {
			return nil, fmt.Errorf("connect mysql: %w", err)
		}
		s.db = db
		if err := mysql.InitializeSchema(context.Background(), db); err != nil {
			return nil, fmt.Errorf("initialize mysql schema: %w", err)
		}
		stresslog.Info("[ADMIN] MySQL 已连接且数据库结构初始化完成",
			zap.String("addr", fmt.Sprintf("%s:%d", cfg.MySQL.Host, cfg.MySQL.Port)),
			zap.String("database", cfg.MySQL.Database))
	}

	// 2. TaskStore
	tasks, err := admintask.NewTaskStore("data")
	if err != nil {
		return nil, fmt.Errorf("init task store: %w", err)
	}
	s.tasks = tasks
	bundles, err := bundle.NewStore("data/bundles", 128)
	if err != nil {
		return nil, err
	}
	s.bundles = bundles
	s.sessions = grpcapi.NewSessionRegistry()
	s.commandStore = command.NewMemoryStore(100_000)
	s.commandBus = command.NewBus(s.commandStore, s.sessions, 8192, generateID)
	s.commandDispatch = command.NewDispatcher(s.bundles, s.commandBus, cfg.Redis)

	// 3. AgentRegistry
	s.agents = agent.NewRegistry(agent.Config{
		UnhealthyThreshold: config.ParseDurationDefault(cfg.ControlPlane.UnhealthyAfter, 30*time.Second, "controlPlane.unhealthyAfter"),
		OfflineThreshold:   config.ParseDurationDefault(cfg.ControlPlane.OfflineAfter, 60*time.Second, "controlPlane.offlineAfter"),
		NotFoundError:      apierror.ErrAgentNotFound,
	}, s.onAgentStatusChange)
	s.agents.SetOnRestart(s.onAgentRestart)
	s.completion = admintask.NewCompletionService(s.tasks, s.agents)

	// 4. MetricsAggregator
	s.metricsWindows = metrics.NewMetricsWindowStore(time.Now)
	s.aggregator = metrics.NewAggregator(s.agents, s.metricsWindows, time.Now)
	s.metricsAccept = metrics.NewAcceptanceService(s.agents, s.tasks, s.metricsWindows)
	s.metricsIngestor = metrics.NewIngestor(
		s.sessions.WithCurrent,
		s.metricsAccept.Accept,
		func(agentID string, snapshot *controlpb.SystemMetricSnapshot) {
			s.agents.Touch(agentID, "")
			s.agents.UpdateSystem(agentID, grpcapi.SystemSnapshotFromProto(snapshot), time.Now())
		},
	)

	// 7. Assigner
	s.assigner = admintask.NewAssigner()

	// 8. HistoryStore（可选）及模板 store 复用已初始化的全局 MySQL 连接池。
	if s.db != nil {
		if cfg.MySQLEnabled() {
			s.history = history.NewStore(s.db, cfg.MySQL.RetentionDays, apierror.ErrHistoryNotFound, func(message string) error {
				return apierror.ErrStarredProtected.WithMessage(message)
			})
			sampler := metrics.NewSampler(
				10*time.Second,
				s.aggregator, s.history, s.agents, s.tasks, s.metricsWindows,
			)
			s.sampler = sampler
		}
		// 流程模板库依赖全局 MySQL，与 history 相互独立：未配 history 时流程库仍可用。
		s.flows = template.NewFlowTemplateStore(s.db, generateID)
		s.actionTemplates = template.NewActionTemplateStore(s.db, generateID)
		s.listenTemplates = template.NewListenTemplateStore(s.db, generateID)
	} else {
		stresslog.Info("[ADMIN] MySQL 未配置：历史归档模块未启用，" +
			"所有 /api/history* 接口将返回 HISTORY_DISABLED")
	}

	// 9. 终态回调
	s.tasks.SetOnTerminal(s.onTaskTerminal)

	// 10. 共享状态（Redis）：验证连通性 + 初始化待清理队列
	if cfg.RedisEnabled() {
		resolved, rerr := cfg.Redis.Resolve()
		if rerr != nil {
			return nil, fmt.Errorf("Redis 共享状态配置无效: %w", rerr)
		}
		// 启动时 PING 验证 Redis 连通性（不持久占用连接）
		pingStore, perr := shared.NewRedisStore(resolved, "admin-ping")
		if perr != nil {
			return nil, fmt.Errorf("连接 Redis 共享状态失败 (addr=%s): %w", fmt.Sprintf("%s:%d", resolved.Host, resolved.Port), perr)
		}
		_ = pingStore.Close()
		stresslog.Info("[ADMIN] Redis 共享状态能力已就绪",
			zap.String("addr", fmt.Sprintf("%s:%d", resolved.Host, resolved.Port)),
			zap.String("keyPrefix", resolved.KeyPrefix))
		s.sharedCleanup = admintask.NewSharedCleanup("data", cfg.Redis)
	} else {
		stresslog.Info("[ADMIN] 共享状态未启用：未配置 Redis（redis.host 为空），" +
			"脚本中使用 require(\"share\") 将报错")
	}

	assembled = true
	return s, nil
}

// Run 启动 Admin 服务器（阻塞）。
func (s *AdminServer) Run(ctx context.Context) error {
	// 初始化协程池
	workpool.InitWorkPool()

	// 启动心跳检测
	runtimeCtx := s.runtimeCtx
	defer s.runtimeCancel()
	if err := s.metricsIngestor.Start(runtimeCtx); err != nil {
		return fmt.Errorf("启动指标摄取器失败: %w", err)
	}
	s.agents.StartHealthChecker(runtimeCtx)

	// 启动定时清理（可选）
	if s.history != nil {
		s.history.StartPruneLoop(runtimeCtx)
	}

	// 启动 deadline 看门狗
	s.startDeadlineWatchdog(runtimeCtx)

	// 启动共享状态清理重试（恢复上次残留 + 定时重试）
	if s.sharedCleanup != nil {
		s.sharedCleanup.Start(runtimeCtx)
	}

	// 注册路由（已包裹 recover 中间件）。
	//
	// timeout 配置：
	//   - ReadHeaderTimeout 防止慢客户端攻击型连接占住 server goroutine 不放，
	//     避免堆积导致 accept queue 满；
	//   - IdleTimeout 让长期空闲的 keep-alive 连接主动关闭，
	//     防止 agent → admin 的 client-side 池保留大量已不再使用的连接；
	//   - ReadTimeout/WriteTimeout 故意不设：history 导出/日志下载可能很长。
	s.managementSrv = s.newManagementServer()
	s.grpcSrv = grpcapi.NewServer(grpcapi.Dependencies{
		Sessions:               s.sessions,
		Agents:                 s.agents,
		Commands:               s.commandBus,
		Bundles:                s.bundles,
		Metrics:                s.metricsIngestor,
		HeartbeatInterval:      config.ParseDurationDefault(s.cfg.ControlPlane.HeartbeatInterval, 10*time.Second, "controlPlane.heartbeatInterval"),
		LeaseDuration:          config.ParseDurationDefault(s.cfg.ControlPlane.UnhealthyAfter, 30*time.Second, "controlPlane.unhealthyAfter"),
		OwnsActiveTask:         s.completion.OwnsActiveTask,
		ScheduleStop:           s.commandDispatch.ScheduleStop,
		AcceptTaskReport:       s.completion.Accept,
		IsPermanentReportError: admintask.IsPermanentReportError,
	})

	stresslog.Info("admin 启动",
		zap.String("managementAddr", s.managementSrv.Addr),
		zap.String("controlPlaneAddr", net.JoinHostPort(s.cfg.ControlPlane.ListenHost, strconv.Itoa(s.cfg.ControlPlane.Port))),
		zap.Bool("history", s.history != nil),
		zap.Bool("redis", s.cfg.RedisEnabled()))

	return s.serveServers(ctx)
}

// Shutdown 优雅关闭。
func (s *AdminServer) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		s.shutdownErr = s.shutdown(ctx)
	})
	return s.shutdownErr
}

func (s *AdminServer) shutdown(ctx context.Context) error {
	// 先停止所有由 Run 启动的长生命周期任务，再等待全局工作池。
	// 若把 cancel 留在 Run 的 defer 中，Shutdown 会先等待指标 worker，
	// 而 worker 又只能在 Shutdown 返回后收到 cancel，形成关闭死锁。
	if s.runtimeCancel != nil {
		s.runtimeCancel()
	}
	if s.stopCh != nil {
		close(s.stopCh)
	}

	shutdownErr := s.shutdownServers(ctx)
	if s.shutdownPool != nil {
		s.shutdownPool()
	}

	// 后台任务全部退出后再释放其可能使用的存储资源。
	if s.history != nil {
		shutdownErr = errors.Join(shutdownErr, s.history.Close())
	}
	// 全局共享 MySQL 连接池：HistoryStore 不再 Close，由 AdminServer 统一关闭。
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
		}
	}
	return shutdownErr
}

func (s *AdminServer) onTaskTerminal(task *admintask.Task) {
	// 停止 Sampler
	if s.sampler != nil {
		s.sampler.Stop(task.ID)
	}

	// 共享状态统一清理：任务已到终态（所有 Agent 已上报或超时），此时删除该 runId
	// 下的所有 key 是安全的。由 Admin 统一做，避免多 Agent 各自清理时的竞态。
	s.cleanupSharedState(task)

	// 异步归档
	if s.history == nil {
		if s.metricsWindows != nil {
			s.metricsWindows.DropTask(task.ID)
		}
		return
	}
	taskID := task.ID
	workpool.GetWorkPool().Go(func() {
		if s.metricsWindows != nil {
			defer s.metricsWindows.MarkTaskTerminal(taskID)
		}
		// 优先用 agent 终止报告聚合，兜底用心跳聚合
		finalStress, mergeErr := buildFinalStressFromReports(task)
		if mergeErr != nil {
			stresslog.Error("合并任务最终指标失败", zap.String("taskID", taskID), zap.Error(mergeErr))
			return
		}
		if finalStress == nil || len(finalStress.Actions) == 0 {
			aggregated, err := s.aggregator.AggregateStress(taskID)
			if err != nil {
				stresslog.Error("聚合任务最终指标失败", zap.String("taskID", taskID), zap.Error(err))
				return
			}
			finalStress = aggregated.Snapshot
		}
		finalSys := s.aggregator.AggregateSystem(assignedAgentIDs(task))
		if err := s.history.Archive(context.Background(), task, finalStress, finalSys); err != nil {
			stresslog.Error("任务归档失败",
				zap.String("taskID", taskID),
				zap.Error(err))
			return
		}
		stresslog.Info("[ADMIN] 任务归档完成",
			zap.String("taskID", taskID),
			zap.String("state", string(task.State)),
			zap.Int("reports", len(task.Reports)))
	})
}

// cleanupSharedState 在任务终态时统一清理该任务的共享状态命名空间。
// 仅当任务使用了共享状态且服务器配置了 Redis 时执行。清理登记到待清理队列：
// 立即尝试一次，失败则持久化并由定时任务/重启后重试，避免无 TTL 的 key 永久泄漏。
func (s *AdminServer) cleanupSharedState(task *admintask.Task) {
	if task == nil || !task.SharedUsed || !s.cfg.RedisEnabled() {
		return
	}
	runID := task.SharedRunID
	if runID == "" {
		runID = task.ID
	}
	if s.sharedCleanup != nil {
		s.sharedCleanup.Enqueue(runID)
	}
}

// buildFinalStressFromReports 从 agent 终止报告聚合最终快照（优先于心跳聚合）。
func buildFinalStressFromReports(task *admintask.Task) (*monitor.CollectorSnapshot, error) {
	if task == nil || len(task.Reports) == 0 {
		return nil, nil
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
		return nil, nil
	}
	return monitor.MergeSnapshots(snaps)
}

func assignedAgentIDs(task *admintask.Task) []string {
	if task == nil {
		return nil
	}
	ids := make([]string, 0, len(task.Assignments))
	for _, assignment := range task.Assignments {
		ids = append(ids, assignment.AgentID)
	}
	return ids
}

func (s *AdminServer) onAgentStatusChange(agentID string, from, to agent.AgentStatus) {
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

	// 节点恢复（offline → idle/busy）：记录 reconnected 事件
	if from == agent.AgentOffline && (to == agent.AgentIdle || to == agent.AgentBusy) {
		s.tasks.Update(task.ID, func(t *admintask.Task) {
			t.AgentEvents = append(t.AgentEvents, admintask.AgentEvent{
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
	if to != agent.AgentOffline {
		return
	}

	s.tasks.Update(task.ID, func(t *admintask.Task) {
		t.AgentEvents = append(t.AgentEvents, admintask.AgentEvent{
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
	if task.State == admintask.TaskStopping {
		complete := false
		s.tasks.Update(task.ID, func(t *admintask.Task) {
			if t.Reports == nil {
				t.Reports = make(map[string]admintask.TaskCompletionReport)
			}
			if _, exists := t.Reports[agentID]; !exists {
				cleanup := robot.UnknownCleanupStatus(robot.CleanupReasonOfflineSynthetic, "节点离线，清理状态未知")
				t.Reports[agentID] = admintask.TaskCompletionReport{
					AgentID:       agentID,
					TaskID:        task.ID,
					Result:        admintask.ResultFailed,
					ErrorMsg:      "节点离线",
					FinishedAt:    time.Now(),
					CleanupStatus: &cleanup,
				}
			}
			if len(t.Assignments) > 0 && len(t.Reports) >= len(t.Assignments) {
				t.CleanupSummary = admintask.AggregateCleanup(t)
				complete = true
			}
		})
		if complete {
			if _, err := s.tasks.Transition(task.ID, admintask.TaskStopping, admintask.TaskStopped); err != nil {
				stresslog.Warn("[ADMIN] 状态转换失败", zap.String("taskID", task.ID), zap.Error(err))
			}
		}
		return
	}

	// 任务 running 时节点离线 → 检查是否所有分配节点都已失效（offline 或已合成 report）
	s.checkAndStopIfAllLost(task.ID)
}

func (s *AdminServer) onAgentRestart(agentID, lostTaskID string) {
	task := s.tasks.ActiveTask()
	if task == nil || task.ID != lostTaskID {
		return
	}
	assignment, ok := taskExpectedAssignment(task, agentID)
	if !ok {
		return
	}
	_ = s.tasks.Update(task.ID, func(t *admintask.Task) {
		t.AgentEvents = append(t.AgentEvents, admintask.AgentEvent{AgentID: agentID, AgentName: assignment.AgentName, Type: "restarted", Timestamp: time.Now(), Detail: "节点进程实例已变化，原任务已丢失"})
		if t.Reports == nil {
			t.Reports = make(map[string]admintask.TaskCompletionReport)
		}
		if _, exists := t.Reports[agentID]; !exists {
			cleanup := robot.UnknownCleanupStatus(robot.CleanupReasonOfflineSynthetic, "节点进程重启，清理状态未知")
			t.Reports[agentID] = admintask.TaskCompletionReport{AgentID: agentID, TaskID: t.ID, Result: admintask.ResultFailed, ErrorMsg: "节点进程重启，任务已丢失", FinishedAt: time.Now(), CleanupStatus: &cleanup}
		}
	})
	stresslog.Warn("[ADMIN] 节点进程重启，任务在该节点已丢失", zap.String("taskID", task.ID), zap.String("agentID", agentID), zap.String("agentName", assignment.AgentName))
	s.checkAndStopIfAllLost(task.ID)
}

func taskExpectedAssignment(task *admintask.Task, agentID string) (admintask.Assignment, bool) {
	for _, assignment := range task.Assignments {
		if assignment.AgentID == agentID {
			return assignment, true
		}
	}
	return admintask.Assignment{}, false
}

// synthesizeOfflineReports 为已离线且未上报的分配节点合成 stopped report。
// 返回 true 表示所有分配节点都已有 report（可以立刻转 stopped）。
func (s *AdminServer) synthesizeOfflineReports(taskID string) bool {
	if _, ok := s.tasks.Get(taskID); !ok {
		return false
	}
	allReported := true
	s.tasks.Update(taskID, func(t *admintask.Task) {
		if t.Reports == nil {
			t.Reports = make(map[string]admintask.TaskCompletionReport)
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
			if !nodeOk || node.Status == agent.AgentOffline {
				cleanup := robot.UnknownCleanupStatus(robot.CleanupReasonOfflineSynthetic, "节点离线，未上报清理结果")
				t.Reports[agentID] = admintask.TaskCompletionReport{
					AgentID:       agentID,
					TaskID:        taskID,
					Result:        admintask.ResultStopped,
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
			t.CleanupSummary = admintask.AggregateCleanup(t)
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
	workpool.GetWorkPool().Go(func() {
		time.Sleep(stopWaitTimeout)
		task, ok := s.tasks.Get(taskID)
		if !ok || task.State != admintask.TaskStopping {
			return
		}
		stresslog.Warn("[ADMIN] 停止超时，合成未上报节点的 report",
			zap.String("taskID", taskID),
			zap.Int("reported", len(task.Reports)),
			zap.Int("total", len(task.SucceededAgents)))
		s.tasks.Update(taskID, func(t *admintask.Task) {
			if t.Reports == nil {
				t.Reports = make(map[string]admintask.TaskCompletionReport)
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
					t.Reports[agentID] = admintask.TaskCompletionReport{
						AgentID:       agentID,
						TaskID:        taskID,
						Result:        admintask.ResultStopped,
						ErrorMsg:      "停止超时，节点未响应",
						FinishedAt:    time.Now(),
						CleanupStatus: &cleanup,
					}
				}
			}
			t.CleanupSummary = admintask.AggregateCleanup(t)
		})
		if _, err := s.tasks.Transition(taskID, admintask.TaskStopping, admintask.TaskStopped); err != nil {
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
	if !ok || !admintask.IsActiveState(task.State) {
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
		if nodeOk && node.Status != agent.AgentOffline && node.CurrentTaskID == taskID {
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
	if !ok || !admintask.IsActiveState(task.State) {
		return
	}

	if task.State == admintask.TaskStarting {
		// TaskStarting 阶段所有节点失效：直接转 TaskFailed，无需发送停止 RPC
		if _, err := s.tasks.Transition(taskID, admintask.TaskStarting, admintask.TaskFailed); err != nil {
			stresslog.Error("[ADMIN] 自动停止任务状态转换失败", zap.Error(err))
		}
		return
	}

	if task.State == admintask.TaskRunning {
		if _, err := s.tasks.Transition(taskID, admintask.TaskRunning, admintask.TaskStopping); err != nil {
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := s.commandDispatch.ScheduleStop(ctx, taskID, targets, reason); err != nil {
		stresslog.Warn("[ADMIN] 创建自动停止命令失败", zap.String("taskID", taskID), zap.Error(err))
	}
	cancel()

	s.tasks.Update(taskID, func(t *admintask.Task) {
		if t.Reports == nil {
			t.Reports = make(map[string]admintask.TaskCompletionReport)
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
				t.Reports[agentID] = admintask.TaskCompletionReport{
					AgentID:       agentID,
					TaskID:        taskID,
					Result:        admintask.ResultFailed,
					ErrorMsg:      reason,
					FinishedAt:    time.Now(),
					CleanupStatus: &cleanup,
				}
			}
		}
		t.CleanupSummary = admintask.AggregateCleanup(t)
	})

	if _, err := s.tasks.Transition(taskID, admintask.TaskStopping, admintask.TaskFailed); err != nil {
		stresslog.Warn("[ADMIN] 状态转换失败", zap.String("taskID", taskID), zap.Error(err))
	}
}

var idCounter atomic.Uint64

func generateID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		binary.BigEndian.PutUint64(b, uint64(time.Now().UnixNano()))
		binary.BigEndian.PutUint64(b[8:], idCounter.Add(1))
	}
	return hex.EncodeToString(b)
}

// startDeadlineWatchdog 定期检查活跃任务是否超时。
func (s *AdminServer) startDeadlineWatchdog(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	workpool.GetWorkPool().GoWithStop(func(stopCh <-chan struct{}) {
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
					if task == nil || task.State != admintask.TaskRunning {
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
