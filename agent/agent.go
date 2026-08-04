package agent

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"stressbot/monitor"
	"stressbot/utils"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// Agent 是分布式压测系统的执行节点。
// 启动后向 Admin 注册，等待任务下发，执行压测，上报指标。
//
// 运行时不变量：
//   - 一旦 Agent 进入 shutdown 流程，stopCh 关闭，所有后台循环（心跳/任务/上报）退出；
//   - 任务执行通过 task wg 单独追踪，确保 shutdown 时能等到任务清理完成；
//   - 全部业务 goroutine 走 utils.GetWorkPool()，由协程池统一恢复 panic。
type Agent struct {
	id      string
	cfg     *ResolvedConfig
	started time.Time
	ctx     context.Context
	cancel  context.CancelFunc

	sysmon    *SystemMonitor
	collector *monitor.MetricsCollector
	httpSrv   *http.Server
	httpCli   *AdminClient

	// 任务状态
	mu          sync.Mutex
	status      AgentStatus
	currentTask *TaskAssignment
	taskCancel  context.CancelFunc

	// 任务执行追踪。submitTask 在 a.mu 内 Add(1)，executeTask 结束 Done()；
	// shutdown 流程会等待 taskWG 归零，确保上报/清理完整完成。
	taskWG sync.WaitGroup

	// shuttingDown 标记 Agent 已进入关闭流程（由 a.mu 保护）。置位后 submitTask 一律拒绝新任务。
	// shutdown 在 a.mu 内置位、在 taskWG.Wait 前完成，与 submitTask 的"预占+Add 同锁"互斥，
	// 消除"计数为零时 Add 与 Wait 并发"的 WaitGroup 误用。
	shuttingDown bool

	// 上报循环
	sysReporter    *SystemReporter
	stressReporter *StressReporter

	// 优雅退出
	stopCh   chan struct{}
	stopOnce sync.Once
}

// New 创建 Agent 实例。
func New(cfg *ResolvedConfig, collector *monitor.MetricsCollector) (*Agent, error) {
	id := generateUUID()

	static := CollectStaticInfo()
	sysmon, err := NewSystemMonitor(cfg.MetricsInterval, static)
	if err != nil {
		return nil, fmt.Errorf("创建 SystemMonitor 失败: %w", err)
	}

	httpCli := NewAdminClient(cfg.AdminUrl, id, cfg.RequestTimeout, cfg.HeartbeatTimeout)

	return &Agent{
		id:        id,
		cfg:       cfg,
		started:   time.Now(),
		sysmon:    sysmon,
		collector: collector,
		httpCli:   httpCli,
		status:    StatusIdle,
		stopCh:    make(chan struct{}),
	}, nil
}

// Run 启动 Agent 主循环（阻塞）。
func (a *Agent) Run() (err error) {
	// 顶层 recover：兜底防止未捕获的 panic 让进程崩溃，
	// 同时把 stack trace 写入日志而不只是 stderr。
	defer func() {
		if rec := recover(); rec != nil {
			stresslog.Error("[AGENT] Run panic",
				zap.Any("panic", rec),
				zap.String("stack", string(debug.Stack())))
			err = fmt.Errorf("agent run panic: %v", rec)
		}
	}()

	stresslog.Info("[AGENT] 启动中",
		zap.String("agentID", a.id),
		zap.String("name", a.cfg.Name),
		zap.String("adminUrl", a.cfg.AdminUrl),
		zap.Duration("requestTimeout", a.cfg.RequestTimeout),
		zap.Duration("reconnectInterval", a.cfg.ReconnectInterval),
		zap.Duration("reconnectMaxInterval", a.cfg.ReconnectMaxInterval),
		zap.Int("reconnectMaxRetries", a.cfg.ReconnectMaxRetries))

	// 1. 启动系统监控
	a.sysmon.Start(a.stopCh)

	// 2. 启动 HTTP 服务器（接收 Admin 命令）
	if err := a.startHTTPServer(); err != nil {
		return fmt.Errorf("启动 HTTP 服务失败: %w", err)
	}

	// 3. 注册到 Admin（按配置策略重连，可能永不放弃）
	ctx, cancel := context.WithCancel(context.Background())
	a.ctx = ctx
	a.cancel = cancel

	if err := a.registerWithRetry(ctx); err != nil {
		a.shutdownHTTPServer(ctx)
		return fmt.Errorf("注册失败: %w", err)
	}

	// 4. 启动系统指标上报（常驻）
	a.sysReporter = NewSystemReporter(a.httpCli, a.id, a.cfg.MetricsInterval, a.sysmon)
	a.sysReporter.Start(ctx)

	// 5. 启动心跳循环
	utils.GetWorkPool().Go(func() {
		a.heartbeatLoop(ctx)
	})

	// 6. 启动任务轮询（回退通道）
	utils.GetWorkPool().Go(func() {
		a.taskPollLoop(ctx)
	})

	stresslog.Info("[AGENT] 已就绪，等待任务下发",
		zap.String("agentID", a.id),
		zap.Int("port", a.cfg.Port))

	// 7. 等待退出信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigCh:
		stresslog.Info("[AGENT] 收到退出信号")
	case <-a.stopCh:
		stresslog.Info("[AGENT] 收到停止命令")
	}

	// 优雅退出
	return a.shutdown()
}

// triggerStop 关闭 stopCh 触发 Agent 主循环退出。线程安全（sync.Once 保护）。
func (a *Agent) triggerStop() {
	a.stopOnce.Do(func() {
		close(a.stopCh)
	})
}

// ID 返回 Agent 唯一标识。
func (a *Agent) ID() string {
	return a.id
}

// Status 返回当前状态。
func (a *Agent) Status() AgentStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

// cancelTask 仅在 expectedTaskID 为空或与当前任务一致时取消任务。
// 返回的 taskID 是锁内观察到的当前任务，可用于识别迟到的停止命令。
func (a *Agent) cancelTask(expectedTaskID, reason string) (taskID string, canceled bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.currentTask == nil || a.taskCancel == nil {
		return "", false
	}
	taskID = a.currentTask.TaskID
	if expectedTaskID != "" && taskID != expectedTaskID {
		return taskID, false
	}
	a.taskCancel()
	stresslog.Warn("[AGENT] 取消当前任务",
		zap.String("taskID", taskID),
		zap.String("reason", reason))
	return taskID, true
}

// cancelCurrentTask 取消任意当前任务，供 Agent 自身关闭流程使用。
func (a *Agent) cancelCurrentTask(reason string) (taskID string, canceled bool) {
	return a.cancelTask("", reason)
}

// registerWithRetry 按配置的重连策略重试注册。
//   - ReconnectMaxRetries < 0  → 持续重连，永不放弃
//   - ReconnectMaxRetries >= 0 → 最多重试 N 次，超出返回 error 触发 triggerStop
//
// 协议约定：RegisterRequest 仍向 Admin 发送 stressInterval/systemInterval 两个 JSON 字段
// （Admin 侧 AgentNode 列表分别展示），两者值相同（均取自 MetricsInterval）。
func (a *Agent) registerWithRetry(ctx context.Context) error {
	static := a.sysmon.Static()
	req := RegisterRequest{
		AgentID:        a.id,
		Name:           a.cfg.Name,
		Address:        a.cfg.Address,
		AppVersion:     a.cfg.AppVersion,
		MaxBots:        a.cfg.MaxBots,
		StressInterval: a.cfg.MetricsInterval.String(),
		SystemInterval: a.cfg.MetricsInterval.String(),
		StaticInfo:     static,
	}

	stresslog.Info("[AGENT] 开始注册到 Admin",
		zap.String("adminUrl", a.cfg.AdminUrl),
		zap.Int("maxRetries", a.cfg.ReconnectMaxRetries))

	return RetryWithRetriesAndBackoff(ctx, func() error {
		resp, err := a.httpCli.Register(ctx, req)
		if err != nil {
			stresslog.Warn("[AGENT] 注册失败，将重试", zap.Error(err))
			return err
		}
		stresslog.Info("[AGENT] 注册成功",
			zap.String("agentID", resp.AgentID),
			zap.String("heartbeatTTL", resp.HeartbeatTTL))
		return nil
	}, a.cfg.ReconnectInterval, a.cfg.ReconnectMaxInterval, a.cfg.ReconnectMaxRetries, "register")
}

// heartbeatLoop 心跳循环。
//
// 行为规则（用户需求 §2 + §6）：
//   - 心跳成功用 HeartbeatInterval；失败用 HeartbeatFailInterval（更快重试）
//   - 任意请求收到 404（errNotRegistered）→ 视为 Admin 重启，立即取消任务并重新注册
//   - 心跳连续失败 ≥ HeartbeatFailThreshold 次（默认 3）且处于 Busy 时取消任务
//     （Admin 是唯一指标聚合点，断联后压测流量没有观测价值；
//     容忍窗口是 N×HeartbeatFailInterval，避免本地 ephemeral port 瞬时抖动误伤）
//   - 持续失败不退进程（除非重新注册超出 ReconnectMaxRetries）
func (a *Agent) heartbeatLoop(ctx context.Context) {
	interval := a.cfg.HeartbeatInterval
	timer := time.NewTimer(interval)
	defer timer.Stop()

	var consecutiveFailures int
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopCh:
			return
		case <-timer.C:
			a.mu.Lock()
			status := a.status
			taskID := ""
			bots := 0
			if a.currentTask != nil {
				taskID = a.currentTask.TaskID
				bots = a.currentTask.TotalBots
			}
			a.mu.Unlock()

			req := HeartbeatRequest{
				AgentID:       a.id,
				Timestamp:     time.Now().Format(time.RFC3339),
				Status:        string(status),
				CurrentTaskID: taskID,
				CurrentBots:   bots,
				AppVersion:    a.cfg.AppVersion,
			}

			err := a.httpCli.Heartbeat(ctx, req)
			if err == nil {
				if consecutiveFailures > 0 {
					stresslog.Info("[AGENT] 心跳恢复",
						zap.String("agentID", a.id),
						zap.String("status", string(status)),
						zap.String("taskID", taskID),
						zap.Int("currentBots", bots),
						zap.Int("previousFailures", consecutiveFailures),
						zap.Duration("nextInterval", a.cfg.HeartbeatInterval))
				}
				consecutiveFailures = 0
				interval = a.cfg.HeartbeatInterval
				timer.Reset(interval)
				continue
			}

			// Admin 返回 404：Agent 在 Admin 侧不存在（Admin 重启或主动注销），触发重新注册流程
			if errors.Is(err, errNotRegistered) {
				// 用户需求 §2.2 / §2.3：运行中任务必须丢弃后再走重连
				if status == StatusBusy {
					a.cancelCurrentTask("Admin 报告未注册，可能重启")
				}
				stresslog.Warn("[AGENT] Admin 报告未注册，尝试重新注册",
					zap.String("agentID", a.id),
					zap.String("status", string(status)),
					zap.String("taskID", taskID),
					zap.Int("currentBots", bots))
				if regErr := a.registerWithRetry(ctx); regErr != nil {
					stresslog.Error("[AGENT] 重新注册失败，退出 Agent",
						zap.String("agentID", a.id),
						zap.String("status", string(status)),
						zap.String("taskID", taskID),
						zap.Error(regErr))
					a.triggerStop()
					return
				}
				stresslog.Info("[AGENT] 重新注册成功，继续心跳",
					zap.String("agentID", a.id),
					zap.String("status", string(status)),
					zap.String("taskID", taskID))
				consecutiveFailures = 0
				interval = a.cfg.HeartbeatInterval
				timer.Reset(interval)
				continue
			}

			consecutiveFailures++

			// 触达阈值才放弃任务，给本地网络抖动一个容忍窗口（默认 3×10s=30s）。
			// 实测 Windows 本机 127.0.0.1 在密集 robot 流量下会规律性瞬时阻塞
			// （ephemeral port 短暂耗尽），1 次失败就 cancel 250 robot 损失太大。
			switch {
			case status == StatusBusy && consecutiveFailures >= a.cfg.HeartbeatFailThreshold:
				stresslog.Error("[AGENT] 任务运行中连续心跳失败达到阈值，放弃当前任务",
					zap.String("agentID", a.id),
					zap.String("status", string(status)),
					zap.String("taskID", taskID),
					zap.Int("currentBots", bots),
					zap.Int("consecutive", consecutiveFailures),
					zap.Int("threshold", a.cfg.HeartbeatFailThreshold),
					zap.Duration("nextInterval", a.cfg.HeartbeatFailInterval),
					zap.Error(err))
				a.cancelCurrentTask(fmt.Sprintf("心跳连续失败 %d 次 / Admin 断联", consecutiveFailures))
			case consecutiveFailures <= 3:
				stresslog.Warn("[AGENT] 心跳失败",
					zap.String("agentID", a.id),
					zap.String("status", string(status)),
					zap.String("taskID", taskID),
					zap.Int("currentBots", bots),
					zap.Int("consecutive", consecutiveFailures),
					zap.Int("threshold", a.cfg.HeartbeatFailThreshold),
					zap.Duration("nextInterval", a.cfg.HeartbeatFailInterval),
					zap.Error(err))
			default:
				stresslog.Warn("[AGENT] 心跳连续失败",
					zap.String("agentID", a.id),
					zap.String("status", string(status)),
					zap.String("taskID", taskID),
					zap.Int("currentBots", bots),
					zap.Int("consecutive", consecutiveFailures),
					zap.Int("threshold", a.cfg.HeartbeatFailThreshold),
					zap.Duration("nextInterval", a.cfg.HeartbeatFailInterval),
					zap.Error(err))
			}
			// 失败时使用更短的重试间隔
			interval = a.cfg.HeartbeatFailInterval
			timer.Reset(interval)
		}
	}
}

// taskPollLoop 任务轮询（回退通道，30s 一次）。
func (a *Agent) taskPollLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopCh:
			return
		case <-ticker.C:
			a.mu.Lock()
			busy := a.currentTask != nil
			a.mu.Unlock()
			if busy {
				continue
			}

			task, err := a.httpCli.FetchPendingTask(ctx)
			if err != nil {
				stresslog.Debug("[AGENT] 轮询任务失败", zap.Error(err))
				continue
			}
			if task != nil {
				stresslog.Info("[AGENT] 轮询到任务", zap.String("taskID", task.TaskID))
				// 统一走 submitTask：原子预占 + Add + 提交，与 HTTP 下发共用同一入口，
				// 避免各自维护 taskWG/currentTask 造成 TOCTOU 或提交失败泄漏。
				if err := a.submitTask(task); err != nil {
					stresslog.Warn("[AGENT] 轮询到的任务被拒绝",
						zap.String("taskID", task.TaskID), zap.Error(err))
				}
			}
		}
	}
}

// errAgentShuttingDown 表示 Agent 已进入关闭流程，拒绝接收新任务。
var errAgentShuttingDown = errors.New("agent 正在关闭，拒绝新任务")

// taskBusyError 表示已有任务在运行，携带当前任务 ID 供调用方回传 Admin。
type taskBusyError struct{ currentTaskID string }

func (e *taskBusyError) Error() string { return "已有任务运行: " + e.currentTaskID }

// submitTask 原子预占并提交任务执行，供 HTTP 下发与轮询摄取共用（唯一任务入口）。
//
// 校验（关闭中 / 已有任务）+ 预占（currentTask / status=Busy / taskCancel）+ taskWG.Add 全部
// 在同一临界区（a.mu）内完成，消除两处历史缺陷：
//   - TOCTOU：旧实现在 handler 里检查空闲后解锁，executeTask 才真正占用，并发下发会双双 202、
//     其一被静默忽略；现在检查与占用不可分割。
//   - 无法停止窗口：旧实现先置 status=Busy 再置 taskCancel，其间 cancelCurrentTask（需两者皆非 nil）
//     无法取消；现在三者一起设置。
//
// taskWG.Add 在锁内、shutdown 置 shuttingDown 也在锁内且置位后再 Wait，二者互斥 →
// 不存在"计数为零时 Add 与 Wait 并发"的 WaitGroup 误用，Wait 之后也不再有新 Add。
func (a *Agent) submitTask(task *TaskAssignment) error {
	return a.submitTaskWithSubmit(task, utils.GetWorkPool().Submit)
}

func (a *Agent) submitTaskWithSubmit(task *TaskAssignment, submit func(func()) error) error {
	a.mu.Lock()
	if a.shuttingDown {
		a.mu.Unlock()
		return errAgentShuttingDown
	}
	if a.currentTask != nil {
		cur := a.currentTask.TaskID
		a.mu.Unlock()
		return &taskBusyError{currentTaskID: cur}
	}
	taskCtx, taskCancel := context.WithCancel(a.ctx)
	a.currentTask = task
	a.status = StatusBusy
	a.taskCancel = taskCancel
	a.taskWG.Add(1)
	a.mu.Unlock()

	if err := submit(func() {
		defer a.taskWG.Done()
		a.executeTask(taskCtx, taskCancel, task)
	}); err != nil {
		// 提交失败（池满/已关闭）：任务体不会执行，回滚预占 + 抵消 Add + 释放刚建的 ctx，
		// 否则 currentTask 永占、taskWG 永不归零致 shutdown 挂死。
		a.taskWG.Done()
		a.mu.Lock()
		a.currentTask = nil
		a.status = StatusIdle
		a.taskCancel = nil
		a.mu.Unlock()
		taskCancel()
		return fmt.Errorf("提交任务执行协程失败: %w", err)
	}
	return nil
}

// executeTask 执行任务（异步，仅由 submitTask 调用）。
//
// 进入时 currentTask / status=Busy / taskCancel 已由 submitTask 原子预占；本函数只负责
// 运行任务、上报，并在结束时释放 taskCtx、清理状态回到 idle。
// 函数 defer 中保护性 recover：任务内 panic 不影响 Agent 主循环。
func (a *Agent) executeTask(taskCtx context.Context, taskCancel context.CancelFunc, task *TaskAssignment) {
	defer func() {
		if rec := recover(); rec != nil {
			stresslog.Error("[AGENT] executeTask panic",
				zap.String("taskID", task.TaskID),
				zap.Any("panic", rec),
				zap.String("stack", string(debug.Stack())))
		}
	}()

	startedAt := time.Now()
	stresslog.Info("[AGENT] 任务开始执行",
		zap.String("agentID", a.id),
		zap.String("taskID", task.TaskID),
		zap.String("taskName", task.TaskName),
		zap.Int("totalBots", task.TotalBots),
		zap.Int("startNumber", task.StartNumber),
		zap.Int("concurrentNum", task.ConcurrentNum),
		zap.String("from", string(StatusIdle)),
		zap.String("to", string(StatusBusy)))

	// 任务结束时清理状态并释放 taskCtx（预占在 submitTask 内完成）。
	defer func() {
		a.mu.Lock()
		a.currentTask = nil
		a.taskCancel = nil
		a.status = StatusIdle
		a.mu.Unlock()
	}()
	defer taskCancel()

	// 创建并启动 StressReporter
	stressReporter := NewStressReporter(
		a.httpCli, a.id, task.TaskID,
		a.cfg.MetricsInterval, a.collector,
	)
	a.mu.Lock()
	a.stressReporter = stressReporter
	a.mu.Unlock()
	stressReporter.Start(taskCtx)

	// 创建 TaskRunner 执行
	runner := NewTaskRunner(task, a.cfg, a.httpCli, a.collector)
	// 注入阶段重置回调：在 resetBots() 后被调用，序列为
	//   ① 快照 → ② 立即重置采集器 → ③ 异步上报
	// 这样新阶段第一时间从零计数，且 HTTP 上报（可能 1~3s 网络延迟）不会阻塞 Manager
	// 进入下一阶段；网络往返期间的 bot 末次 IO 即使有少量计数也只落到新阶段的"前几ms"，
	// 不会污染已快照的本阶段数据。
	runner.OnStageReset = func(nextStageIdx int) {
		stresslog.Info("[AGENT] reset 边界阶段段落上报",
			zap.String("agentID", a.id),
			zap.String("taskID", task.TaskID),
			zap.Int("stageIndex", nextStageIdx))

		snap := stressReporter.Snapshot()
		a.collector.Reset()

		report := TaskCompletionReport{
			AgentID:       a.id,
			TaskID:        task.TaskID,
			Result:        TaskCompleted,
			StageIndex:    nextStageIdx,
			FinalSnapshot: snap,
			FinishedAt:    time.Now(),
		}
		utils.GetWorkPool().Go(func() {
			reportCtx, reportCancel := context.WithTimeout(context.Background(), a.cfg.TaskReportTimeout)
			defer reportCancel()
			if err := a.httpCli.ReportTaskDone(reportCtx, report); err != nil {
				stresslog.Warn("[AGENT] reset 阶段段落上报失败",
					zap.String("agentID", a.id),
					zap.String("taskID", task.TaskID),
					zap.Int("stageIndex", nextStageIdx),
					zap.Error(err))
			}
		})
	}

	runResult := runner.Run(taskCtx)
	result := runResult.Result
	errMsg := runResult.ErrorMsg

	if result == TaskFailed {
		stresslog.Error("[AGENT] 任务执行失败",
			zap.String("agentID", a.id),
			zap.String("taskID", task.TaskID),
			zap.String("error", errMsg))
	} else if errMsg != "" {
		stresslog.Warn("[AGENT] 任务完成但有错误",
			zap.String("agentID", a.id),
			zap.String("taskID", task.TaskID),
			zap.String("result", string(result)),
			zap.String("error", errMsg))
	}

	// 停止 StressReporter（内部会同步 flush 最后一帧指标）
	stressReporter.Stop()

	// 采集 finalSnapshot
	finalSnap := a.collector.Snapshot(nil, 0)

	// 上报任务完成。
	//
	// 用户需求 §2.2：任务运行中 Admin 挂掉 → 取消任务并走重连，相当于"丢弃"任务，
	// 不要补档。因此这里：
	//   - 用 context.Background()（脱离已 cancel 的 taskCtx）+ TaskReportTimeout 整体超时
	//   - 仅做一次性提交，失败后退出（让 Agent 直接进入 Idle 等待新任务，不阻塞重连）
	//   - 如果上报时 Admin 仍不可达，Admin 也会通过心跳超时自动给该 Agent 合成 offline report
	reportCtx, reportCancel := context.WithTimeout(context.Background(), a.cfg.TaskReportTimeout)
	defer reportCancel()

	report := TaskCompletionReport{
		AgentID:       a.id,
		TaskID:        task.TaskID,
		Result:        result,
		ErrorMsg:      errMsg,
		FinishedAt:    time.Now(),
		FinalSnapshot: finalSnap,
		CleanupStatus: &runResult.CleanupStatus,
	}

	if err := a.httpCli.ReportTaskDone(reportCtx, report); err != nil {
		stresslog.Warn("[AGENT] 任务完成上报失败（任务已丢弃，由 Admin 心跳超时自动收尾）",
			zap.String("agentID", a.id),
			zap.String("taskID", task.TaskID),
			zap.Error(err))
	} else {
		stresslog.Info("[AGENT] 任务完成已上报",
			zap.String("agentID", a.id),
			zap.String("taskID", task.TaskID),
			zap.String("result", string(result)))
	}

	// 清理临时目录
	runner.Cleanup()
	stresslog.Info("[AGENT] 任务执行状态已恢复",
		zap.String("agentID", a.id),
		zap.String("taskID", task.TaskID),
		zap.String("result", string(result)),
		zap.String("from", string(StatusBusy)),
		zap.String("to", string(StatusIdle)),
		zap.Duration("duration", time.Since(startedAt)))

	// 任务结束、Agent 回到 idle：把 GC 已回收但仍保留在进程内的内存归还给 OS，
	// 避免常驻 Agent 在多任务之间 RSS 单调增长（每个任务会创建/销毁整套 LState 池、
	// gnet 引擎与连接，峰值内存较高）。
	debug.FreeOSMemory()
}

// shutdown 优雅关闭。
//
// 关闭顺序的设计原则：
//  1. 先让任务停下（taskCancel）以便上报 finalSnapshot；
//  2. 等待任务 goroutine 完成上报（taskWG.Wait）；
//  3. 停止上报循环；
//  4. cancel 全局 ctx，让常驻 goroutine 退出；
//  5. 注销（best-effort）→ 关闭 HTTP。
func (a *Agent) shutdown() error {
	stresslog.Info("[AGENT] 正在关闭...")

	// 0. 关闭任务入口：在 a.mu 内置 shuttingDown。此后 submitTask（HTTP 下发 / 轮询摄取）一律被拒。
	//    与 submitTask 的"预占 + taskWG.Add 同锁"互斥，保证下面的 taskWG.Wait 之后不再有并发 Add，
	//    杜绝"计数为零时 Add 与 Wait 并发"的 WaitGroup 误用。
	a.mu.Lock()
	a.shuttingDown = true
	a.mu.Unlock()

	// 1. 停止当前任务，并等待 executeTask 自然结束（含 finalSnapshot 上报）
	if taskID, canceled := a.cancelCurrentTask("agent shutdown"); canceled {
		stresslog.Info("[AGENT] 等待任务完成上报", zap.String("taskID", taskID))
	}

	// 等待任务结束（上报阶段用 context.Background，不会被 ctx cancel 中断）；
	// 最长等待 TaskReportTimeout + 5s 余量。
	waitTaskDone := make(chan struct{})
	utils.GetWorkPool().Go(func() {
		a.taskWG.Wait()
		close(waitTaskDone)
	})
	select {
	case <-waitTaskDone:
	case <-time.After(a.cfg.TaskReportTimeout + 5*time.Second):
		stresslog.Warn("[AGENT] 等待任务退出超时，继续关闭流程")
	}

	// 2. 停止 StressReporter / SystemReporter（StressReporter 通常已被 executeTask 关掉）
	a.mu.Lock()
	sr := a.stressReporter
	sysr := a.sysReporter
	a.mu.Unlock()
	if sr != nil {
		sr.Stop()
	}
	if sysr != nil {
		sysr.Stop()
	}

	// 3. 取消全局 ctx → 心跳/轮询/上报循环退出
	if a.cancel != nil {
		a.cancel()
	}

	// 4. 注销（best-effort，5s 超时）
	deregCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.httpCli.Deregister(deregCtx); err != nil {
		stresslog.Warn("[AGENT] 注销失败（best-effort）", zap.Error(err))
	}

	// 5. 关闭 HTTP 服务器
	a.shutdownHTTPServer(context.Background())

	stresslog.Info("[AGENT] 已退出")
	return nil
}

// getLocalIP 获取本机首选出口 IP（UDP 连接方式，不实际发包）。
func getLocalIP() string {
	conn, err := net.DialTimeout("udp4", "8.8.8.8:53", 1*time.Second)
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	addr := conn.LocalAddr().(*net.UDPAddr)
	return addr.IP.String()
}

// buildAddress 自动获取本机 IP 并构建 HTTP 地址。
// 用于用户未配置 address 时的回退方案。
func buildAddress(port int) string {
	ip := getLocalIP()
	return fmt.Sprintf("http://%s:%d", ip, port)
}

// generateUUID 生成 v4 UUID（不依赖外部库）。
func generateUUID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// fallback to timestamp-based
		return fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
	}
	buf[6] = (buf[6] & 0x0f) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}
