package agent

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	agenttask "stressbot/agent/task"
	"sync"
	"sync/atomic"
	"time"

	"stressbot/agent/bundle"
	"stressbot/agent/command"
	"stressbot/agent/metrics"
	"stressbot/agent/session"
	"stressbot/internal/stresslog"
	"stressbot/internal/timerpool"
	"stressbot/internal/workpool"
	"stressbot/monitor"

	"go.uber.org/zap"
)

type Agent struct {
	id      string
	cfg     *ResolvedConfig
	started time.Time
	ctx     context.Context
	cancel  context.CancelFunc

	sysmon        *metrics.SystemMonitor
	collector     *monitor.MetricsCollector
	metrics       *metrics.LatestMetrics
	executor      *command.Executor
	bundleCache   *bundle.Cache
	reportOutbox  *session.ReportOutbox
	leaseDeadline atomic.Int64

	mu              sync.Mutex
	status          Status
	currentTask     *agenttask.Assignment
	taskCancel      context.CancelFunc
	taskCancelCause context.CancelCauseFunc
	shuttingDown    bool
	taskWG          sync.WaitGroup

	sysReporter    *metrics.SystemReporter
	stressReporter *metrics.StressReporter
	stopCh         chan struct{}
	stopOnce       sync.Once
	shutdownMu     sync.Mutex
	shutdownID     string
	shutdownSeq    uint64
}

func New(cfg *ResolvedConfig, collector *monitor.MetricsCollector) (*Agent, error) {
	static := CollectStaticInfo()
	sysmon, err := metrics.NewSystemMonitor(cfg.MetricsInterval, static)
	if err != nil {
		return nil, fmt.Errorf("创建 SystemMonitor 失败: %w", err)
	}
	bundleCache, err := bundle.NewCache(cfg.TaskWorkDir)
	if err != nil {
		return nil, err
	}
	agent := &Agent{
		id: cfg.ID, cfg: cfg, started: time.Now(), sysmon: sysmon, collector: collector,
		metrics: metrics.NewLatestMetrics(), bundleCache: bundleCache,
		reportOutbox: session.NewReportOutbox(128), status: StatusIdle, stopCh: make(chan struct{}),
	}
	agent.executor = command.NewExecutor(agent, bundleCache)
	return agent, nil
}

func (a *Agent) Run(ctx context.Context) error {
	var runErr error
	stresslog.Info("[AGENT] 启动中", zap.String("agentID", a.id), zap.String("name", a.cfg.Name),
		zap.String("adminAddress", a.cfg.AdminAddress), zap.Duration("reconnectInterval", a.cfg.ReconnectInterval),
		zap.Duration("reconnectMaxInterval", a.cfg.ReconnectMaxInterval), zap.Int("reconnectMaxRetries", a.cfg.ReconnectMaxRetries))

	runtimeCtx, cancel := context.WithCancel(context.Background())
	a.ctx, a.cancel = runtimeCtx, cancel
	a.sysmon.Start(a.stopCh)
	a.sysReporter = metrics.NewSystemReporter(a.metrics, a.cfg.MetricsInterval, a.sysmon)
	a.sysReporter.Start(runtimeCtx)
	if err := a.executor.Start(runtimeCtx); err != nil {
		return fmt.Errorf("启动命令执行器失败: %w", err)
	}
	workpool.Default().Go(func() { a.leaseLoop(runtimeCtx) })
	connectionDone := make(chan error, 1)
	if err := workpool.Default().Submit(func() { connectionDone <- a.connectionSupervisor(runtimeCtx) }); err != nil {
		return fmt.Errorf("启动 gRPC 连接管理器失败: %w", err)
	}

	select {
	case <-ctx.Done():
		stresslog.Info("[AGENT] 收到退出请求", zap.Error(ctx.Err()))
	case <-a.stopCh:
		stresslog.Info("[AGENT] 收到关闭命令")
	case err := <-connectionDone:
		if err != nil {
			stresslog.Error("[AGENT] gRPC 控制面停止", zap.Error(err))
			runErr = err
		}
	}
	a.shutdown()
	return runErr
}

func (a *Agent) triggerStop() { a.stopOnce.Do(func() { close(a.stopCh) }) }

func (a *Agent) prepareShutdown(commandID string, sequence uint64) {
	a.shutdownMu.Lock()
	a.shutdownID, a.shutdownSeq = commandID, sequence
	a.shutdownMu.Unlock()
}

func (a *Agent) confirmShutdown(commandID string, sequence uint64) {
	a.shutdownMu.Lock()
	matched := a.shutdownID == commandID && a.shutdownSeq == sequence
	if matched {
		a.shutdownID, a.shutdownSeq = "", 0
	}
	a.shutdownMu.Unlock()
	if matched {
		a.triggerStop()
	}
}

func (a *Agent) ID() string { return a.id }

// AgentID 实现内部命令执行器的节点身份接口。
func (a *Agent) AgentID() string { return a.id }

// CancelTask 实现内部命令执行器的任务取消接口。
func (a *Agent) CancelTask(expectedTaskID, reason string) (string, bool) {
	return a.cancelTask(expectedTaskID, reason)
}

// ReserveTask 为开始命令预留唯一任务槽位。
func (a *Agent) ReserveTask(task *agenttask.Assignment) (context.Context, context.CancelCauseFunc, error) {
	return a.reserveTask(task)
}

// LaunchReservedTask 启动已经预留的任务。
func (a *Agent) LaunchReservedTask(taskCtx context.Context, taskCancel context.CancelCauseFunc, task *agenttask.Assignment, submit func(func()) error) error {
	return a.launchReservedTask(taskCtx, taskCancel, task, submit)
}

// ReleaseReservedTask 释放启动失败或会话中断的任务预留。
func (a *Agent) ReleaseReservedTask(task *agenttask.Assignment, cancel context.CancelCauseFunc, cause error) {
	a.releaseReservedTask(task, cancel, cause)
}

// PrepareShutdown 记录待 Admin 确认的关闭命令。
func (a *Agent) PrepareShutdown(commandID string, sequence uint64) {
	a.prepareShutdown(commandID, sequence)
}

// CancelControlPlane 在命令结果无法可靠排队时终止控制会话。
func (a *Agent) CancelControlPlane() {
	if a.cancel != nil {
		a.cancel()
	}
}

func (a *Agent) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

func (a *Agent) cancelTask(expectedTaskID, reason string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.currentTask == nil || a.taskCancel == nil {
		return "", false
	}
	taskID := a.currentTask.TaskID
	if expectedTaskID != "" && expectedTaskID != taskID {
		return taskID, false
	}
	if a.taskCancelCause != nil {
		a.taskCancelCause(fmt.Errorf("%w: %s", errTaskStopRequested, reason))
	} else {
		a.taskCancel()
	}
	stresslog.Warn("[AGENT] 取消当前任务", zap.String("taskID", taskID), zap.String("reason", reason))
	return taskID, true
}

func (a *Agent) cancelCurrentTask(reason string) (string, bool) { return a.cancelTask("", reason) }

var errAgentShuttingDown = errors.New("agent 正在关闭，拒绝新任务")
var errTaskStopRequested = errors.New("任务被控制命令取消")

type taskBusyError struct{ currentTaskID string }

func (e *taskBusyError) Error() string { return "已有任务运行: " + e.currentTaskID }

func (a *Agent) submitTaskWithSubmit(task *agenttask.Assignment, submit func(func()) error) error {
	taskCtx, taskCancel, err := a.reserveTask(task)
	if err != nil {
		return err
	}
	return a.launchReservedTask(taskCtx, taskCancel, task, submit)
}

func (a *Agent) reserveTask(task *agenttask.Assignment) (context.Context, context.CancelCauseFunc, error) {
	a.mu.Lock()
	if a.shuttingDown {
		a.mu.Unlock()
		return nil, nil, errAgentShuttingDown
	}
	if a.currentTask != nil {
		current := a.currentTask.TaskID
		a.mu.Unlock()
		return nil, nil, &taskBusyError{currentTaskID: current}
	}
	taskCtx, taskCancel := context.WithCancelCause(a.ctx)
	a.currentTask, a.status, a.taskCancelCause = task, StatusBusy, taskCancel
	a.taskCancel = func() { taskCancel(context.Canceled) }
	a.mu.Unlock()
	return taskCtx, taskCancel, nil
}

func (a *Agent) launchReservedTask(taskCtx context.Context, taskCancel context.CancelCauseFunc, task *agenttask.Assignment, submit func(func()) error) error {
	a.mu.Lock()
	if a.currentTask != task {
		a.mu.Unlock()
		return fmt.Errorf("任务启动预留已失效")
	}
	if cause := context.Cause(taskCtx); cause != nil {
		a.mu.Unlock()
		return cause
	}
	a.taskWG.Add(1)
	a.mu.Unlock()
	if err := submit(func() {
		defer a.taskWG.Done()
		a.executeTask(taskCtx, func() { taskCancel(context.Canceled) }, task)
	}); err != nil {
		a.taskWG.Done()
		a.releaseReservedTask(task, taskCancel, err)
		return fmt.Errorf("提交任务执行协程失败: %w", err)
	}
	return nil
}

func (a *Agent) releaseReservedTask(task *agenttask.Assignment, cancel context.CancelCauseFunc, cause error) {
	cancel(cause)
	a.mu.Lock()
	if a.currentTask == task {
		a.currentTask, a.status, a.taskCancel, a.taskCancelCause = nil, StatusIdle, nil, nil
	}
	a.mu.Unlock()
}

func (a *Agent) executeTask(taskCtx context.Context, taskCancel context.CancelFunc, task *agenttask.Assignment) {
	defer func() {
		if recovered := recover(); recovered != nil {
			stresslog.Error("[AGENT] executeTask panic", zap.String("taskID", task.TaskID), zap.Any("panic", recovered), zap.String("stack", string(debug.Stack())))
		}
	}()
	startedAt := time.Now()
	defer func() {
		a.mu.Lock()
		if a.currentTask == task {
			a.currentTask, a.taskCancel, a.taskCancelCause, a.status, a.stressReporter = nil, nil, nil, StatusIdle, nil
		}
		a.mu.Unlock()
		taskCancel()
	}()

	stresslog.Info("[AGENT] 任务开始执行", zap.String("agentID", a.id), zap.String("taskID", task.TaskID),
		zap.String("taskName", task.TaskName), zap.Int("totalBots", task.TotalBots), zap.Int("startNumber", task.StartNumber),
		zap.Int("concurrentNum", task.ConcurrentNum))
	stressReporter := metrics.NewStressReporter(a.metrics, task.TaskID, a.cfg.MetricsInterval, a.collector)
	a.mu.Lock()
	a.stressReporter = stressReporter
	a.mu.Unlock()
	stressReporter.Start(taskCtx)

	runner := agenttask.NewRunner(task, a.collector)
	runner.OnStageReset = func(nextStageIndex int) {
		snapshot := stressReporter.Snapshot()
		a.collector.Reset()
		report := agenttask.CompletionReport{AgentID: a.id, TaskID: task.TaskID, Result: agenttask.Completed,
			StageIndex: nextStageIndex, FinalSnapshot: snapshot, FinishedAt: time.Now()}
		if _, err := a.offerTaskReport(report); err != nil {
			stresslog.Warn("[AGENT] reset 阶段报告进入待确认队列失败", zap.String("taskID", task.TaskID), zap.Int("stageIndex", nextStageIndex), zap.Error(err))
		}
	}
	runResult := runner.Run(taskCtx)
	stressReporter.Stop()
	finalSnapshot := a.collector.Snapshot(nil, 0)
	report := agenttask.CompletionReport{AgentID: a.id, TaskID: task.TaskID, Result: runResult.Result, ErrorMsg: runResult.ErrorMsg,
		FinishedAt: time.Now(), FinalSnapshot: finalSnapshot, CleanupStatus: &runResult.CleanupStatus}
	reportCtx, reportCancel := context.WithTimeout(context.Background(), a.cfg.TaskReportTimeout)
	done, err := a.offerTaskReport(report)
	if err == nil {
		select {
		case err = <-done:
		case <-reportCtx.Done():
			err = reportCtx.Err()
		}
	}
	reportCancel()
	if err != nil {
		stresslog.Warn("[AGENT] 任务完成报告未在期限内得到确认", zap.String("agentID", a.id), zap.String("taskID", task.TaskID), zap.Error(err))
	} else {
		stresslog.Info("[AGENT] 任务完成报告已确认", zap.String("agentID", a.id), zap.String("taskID", task.TaskID), zap.String("result", string(runResult.Result)))
	}
	runner.Cleanup()
	stresslog.Info("[AGENT] 任务执行结束", zap.String("taskID", task.TaskID), zap.Duration("duration", time.Since(startedAt)))
	debug.FreeOSMemory()
}

func (a *Agent) offerTaskReport(report agenttask.CompletionReport) (<-chan error, error) {
	reportID := fmt.Sprintf("%s-%s-%d-%d", a.id, report.TaskID, report.StageIndex, time.Now().UnixNano())
	return a.reportOutbox.Offer(finalReportToProto(reportID, report))
}

func (a *Agent) shutdown() {
	stresslog.Info("[AGENT] 正在关闭...")
	a.triggerStop()
	a.mu.Lock()
	a.shuttingDown = true
	a.mu.Unlock()
	if taskID, canceled := a.cancelCurrentTask("agent shutdown"); canceled {
		stresslog.Info("[AGENT] 等待当前任务完成清理与报告", zap.String("taskID", taskID))
	}
	done := make(chan struct{})
	workpool.Default().Go(func() { a.taskWG.Wait(); close(done) })
	timer := timerpool.GetTimer(a.cfg.TaskReportTimeout + 5*time.Second)
	select {
	case <-done:
	case <-timer.C:
		stresslog.Warn("[AGENT] 等待任务退出超时")
	}
	timerpool.PutTimer(timer)
	a.mu.Lock()
	stressReporter, systemReporter := a.stressReporter, a.sysReporter
	a.mu.Unlock()
	if stressReporter != nil {
		stressReporter.Stop()
	}
	if systemReporter != nil {
		systemReporter.Stop()
	}
	if a.cancel != nil {
		a.cancel()
	}
	stresslog.Info("[AGENT] 已退出")
}
