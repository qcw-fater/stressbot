package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"stressbot/monitor"
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

	history *HistoryStore // 可选
	sampler *Sampler      // 可选

	httpSrv *http.Server
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

func NewAdminServer(cfg Config) (*AdminServer, error) {
	s := &AdminServer{cfg: cfg, stopCh: make(chan struct{})}

	// 1. TaskStore
	tasks, err := NewTaskStore(cfg.DataDir)
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

	// 7. HistoryStore（可选）
	if cfg.History.Enabled {
		history, err := NewHistoryStore(cfg.History)
		if err != nil {
			return nil, fmt.Errorf("init history store: %w", err)
		}
		s.history = history

		sampler := NewSampler(
			utils.ParseDurationDefault(cfg.History.SamplerInterval, 10*time.Second, "history.samplerInterval"),
			s.aggregator, s.history, s.agents,
		)
		s.sampler = sampler
	} else {
		stresslog.Info("[ADMIN] history 模块未启用：所有 /api/history* 接口将返回 HISTORY_DISABLED；" +
			"如需启用，请在 config.json 设置 history.enabled=true 且填写 history.mysql.dsn")
	}

	// 8. 终态回调
	s.tasks.SetOnTerminal(s.onTaskTerminal)

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

	// 注册路由（已包裹 recover 中间件）
	s.httpSrv = &http.Server{
		Addr:    s.cfg.ListenAddr,
		Handler: s.registerRoutes(),
	}

	stresslog.Info("admin 启动",
		zap.String("addr", s.cfg.ListenAddr),
		zap.Bool("history", s.cfg.History.Enabled))

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
	utils.GetWorkPool().Shutdown()
	close(s.stopCh)
	return nil
}

func (s *AdminServer) onTaskTerminal(task *Task) {
	// 停止 Sampler
	if s.sampler != nil {
		s.sampler.Stop(task.ID)
	}

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
		if err := s.history.Archive(task, finalStress, finalSys); err != nil {
			stresslog.Error("任务归档失败",
				zap.String("taskId", taskID),
				zap.Error(err))
		}
	})
}

// buildFinalStressFromReports 从 agent 终止报告聚合最终快照（优先于心跳聚合）。
func buildFinalStressFromReports(task *Task) *monitor.CollectorSnapshot {
	if task == nil || len(task.Reports) == 0 {
		return nil
	}
	snaps := make([]*monitor.CollectorSnapshot, 0, len(task.Reports))
	for _, r := range task.Reports {
		// 过滤零值快照
		if r.FinalSnapshot.UptimeSec == 0 && len(r.FinalSnapshot.Actions) == 0 {
			continue
		}
		snap := r.FinalSnapshot
		snaps = append(snaps, &snap)
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
				t.Reports[agentID] = TaskCompletionReport{
					AgentID:    agentID,
					TaskID:     t.ID,
					Result:     ResultFailed,
					ErrorMsg:   "Agent 重新注册，任务已丢失",
					FinishedAt: time.Now(),
				}
			}
		})
		stresslog.Warn("[ADMIN] 分配节点重新注册，任务在该节点已丢失",
			zap.String("taskId", task.ID),
			zap.String("agentId", agentID),
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
			zap.String("taskId", task.ID),
			zap.String("agentId", agentID),
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
		zap.String("taskId", task.ID),
		zap.String("agentId", agentID),
		zap.String("agentName", agentName))

	// 任务正在 stopping 时节点离线 → 立刻合成 report
	if task.State == TaskStopping {
		s.tasks.Update(task.ID, func(t *Task) {
			if t.Reports == nil {
				t.Reports = make(map[string]TaskCompletionReport)
			}
			if _, exists := t.Reports[agentID]; !exists {
				t.Reports[agentID] = TaskCompletionReport{
					AgentID:    agentID,
					TaskID:     task.ID,
					Result:     ResultFailed,
					ErrorMsg:   "节点离线",
					FinishedAt: time.Now(),
				}
			}
		})
		task, _ = s.tasks.Get(task.ID)
		if len(task.Reports) == len(task.Assignments) {
			if _, err := s.tasks.Transition(task.ID, TaskStopping, TaskStopped); err != nil {
				stresslog.Warn("[ADMIN] 状态转换失败", zap.String("taskId", task.ID), zap.Error(err))
			}
		}
		return
	}

	// 任务 running 时节点离线 → 检查是否所有分配节点都已失效（offline 或已合成 report）
	s.checkAndStopIfAllLost(task.ID)
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
		for _, a := range t.Assignments {
			if _, exists := t.Reports[a.AgentID]; exists {
				continue
			}
			node, nodeOk := s.agents.Get(a.AgentID)
			if !nodeOk || node.Status == AgentOffline {
				t.Reports[a.AgentID] = TaskCompletionReport{
					AgentID:    a.AgentID,
					TaskID:     taskID,
					Result:     ResultStopped,
					ErrorMsg:   "节点离线，未上报",
					FinishedAt: time.Now(),
				}
			} else {
				allReported = false
			}
		}
		if allReported && len(t.Reports) < len(t.Assignments) {
			allReported = false
		}
	})
	return allReported
}

// startStopTimeout 启动停止超时安全网。
// 30s 后如果任务仍在 stopping，为剩余未上报节点合成 report 并转 stopped。
func (s *AdminServer) startStopTimeout(taskID string) {
	utils.GetWorkPool().Go(func() {
		time.Sleep(30 * time.Second)
		task, ok := s.tasks.Get(taskID)
		if !ok || task.State != TaskStopping {
			return
		}
		stresslog.Warn("[ADMIN] 停止超时，合成未上报节点的 report",
			zap.String("taskId", taskID),
			zap.Int("reported", len(task.Reports)),
			zap.Int("total", len(task.Assignments)))
		s.tasks.Update(taskID, func(t *Task) {
			if t.Reports == nil {
				t.Reports = make(map[string]TaskCompletionReport)
			}
			for _, a := range t.Assignments {
				if _, exists := t.Reports[a.AgentID]; !exists {
					t.Reports[a.AgentID] = TaskCompletionReport{
						AgentID:    a.AgentID,
						TaskID:     taskID,
						Result:     ResultStopped,
						ErrorMsg:   "停止超时，节点未响应",
						FinishedAt: time.Now(),
					}
				}
			}
		})
		if _, err := s.tasks.Transition(taskID, TaskStopping, TaskStopped); err != nil {
			stresslog.Warn("[ADMIN] 状态转换失败", zap.String("taskId", taskID), zap.Error(err))
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
	for _, a := range task.Assignments {
		if _, hasReport := task.Reports[a.AgentID]; hasReport {
			continue // 已合成 report 视为该槽位失效
		}
		node, nodeOk := s.agents.Get(a.AgentID)
		if nodeOk && node.Status != AgentOffline && node.CurrentTaskID == taskID {
			anyAlive = true
			break
		}
	}
	if !anyAlive {
		stresslog.Error("[ADMIN] 所有分配节点已失效（offline 或 restarted），自动停止任务",
			zap.String("taskId", taskID))
		s.autoStopTask(taskID, "所有分配节点已失效")
	}
}

// autoStopTask 自动停止任务（deadline 超时或全部节点失效）。
func (s *AdminServer) autoStopTask(taskID string, reason string) {
	task, ok := s.tasks.Get(taskID)
	if !ok || !IsActiveState(task.State) {
		return
	}

	if task.State == TaskRunning {
		if _, err := s.tasks.Transition(taskID, TaskRunning, TaskStopping); err != nil {
			stresslog.Error("[ADMIN] 自动停止任务状态转换失败", zap.Error(err))
			return
		}
	}

	task, _ = s.tasks.Get(taskID)
	for _, a := range task.Assignments {
		node, ok := s.agents.Get(a.AgentID)
		if ok && node.Status != AgentOffline {
			if err := s.dispatcher.Stop(node.Address, taskID); err != nil {
				stresslog.Warn("[ADMIN] 停止节点任务失败",
					zap.String("agentId", a.AgentID), zap.Error(err))
			}
		}
	}

	s.tasks.Update(taskID, func(t *Task) {
		if t.Reports == nil {
			t.Reports = make(map[string]TaskCompletionReport)
		}
		for _, a := range t.Assignments {
			if _, ok := t.Reports[a.AgentID]; !ok {
				t.Reports[a.AgentID] = TaskCompletionReport{
					AgentID:    a.AgentID,
					TaskID:     taskID,
					Result:     ResultFailed,
					ErrorMsg:   reason,
					FinishedAt: time.Now(),
				}
			}
		}
	})

	if _, err := s.tasks.Transition(taskID, TaskStopping, TaskFailed); err != nil {
		stresslog.Warn("[ADMIN] 状态转换失败", zap.String("taskId", taskID), zap.Error(err))
	}
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
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
							zap.String("taskId", task.ID),
							zap.Time("deadline", *task.Config.Deadline))
						s.autoStopTask(task.ID, "任务超时")
					}
				}()
			}
		}
	})
}
