package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"stressbot/monitor"
	"stressbot/utils"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

type Agent struct {
	id      string
	cfg     *ResolvedConfig
	started time.Time
	ctx     context.Context
	cancel  context.CancelFunc

	sysmon        *SystemMonitor
	collector     *monitor.MetricsCollector
	telemetry     *LatestTelemetry
	executor      *CommandExecutor
	bundleCache   *BundleCache
	reportOutbox  *ReportOutbox
	leaseDeadline atomic.Int64

	mu              sync.Mutex
	status          AgentStatus
	currentTask     *TaskAssignment
	taskCancel      context.CancelFunc
	taskCancelCause context.CancelCauseFunc
	shuttingDown    bool
	taskWG          sync.WaitGroup

	sysReporter    *SystemReporter
	stressReporter *StressReporter
	stopCh         chan struct{}
	stopOnce       sync.Once
	shutdownMu     sync.Mutex
	shutdownID     string
	shutdownSeq    uint64
}

func New(cfg *ResolvedConfig, collector *monitor.MetricsCollector) (*Agent, error) {
	static := CollectStaticInfo()
	sysmon, err := NewSystemMonitor(cfg.MetricsInterval, static)
	if err != nil {
		return nil, fmt.Errorf("创建 SystemMonitor 失败: %w", err)
	}
	bundleCache, err := NewBundleCache(cfg.TaskWorkDir)
	if err != nil {
		return nil, err
	}
	agent := &Agent{
		id: cfg.ID, cfg: cfg, started: time.Now(), sysmon: sysmon, collector: collector,
		telemetry: NewLatestTelemetry(), bundleCache: bundleCache,
		reportOutbox: NewReportOutbox(128), status: StatusIdle, stopCh: make(chan struct{}),
	}
	agent.executor = NewCommandExecutor(agent, bundleCache)
	return agent, nil
}

func (a *Agent) Run() (runErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			stresslog.Error("[AGENT] Run panic", zap.Any("panic", recovered), zap.String("stack", string(debug.Stack())))
			runErr = fmt.Errorf("agent run panic: %v", recovered)
		}
	}()
	stresslog.Info("[AGENT] 启动中", zap.String("agentID", a.id), zap.String("name", a.cfg.Name),
		zap.String("adminAddress", a.cfg.AdminAddress), zap.Duration("reconnectInterval", a.cfg.ReconnectInterval),
		zap.Duration("reconnectMaxInterval", a.cfg.ReconnectMaxInterval), zap.Int("reconnectMaxRetries", a.cfg.ReconnectMaxRetries))

	ctx, cancel := context.WithCancel(context.Background())
	a.ctx, a.cancel = ctx, cancel
	a.sysmon.Start(a.stopCh)
	a.sysReporter = NewSystemReporter(a.telemetry, a.cfg.MetricsInterval, a.sysmon)
	a.sysReporter.Start(ctx)
	if err := a.executor.Start(ctx); err != nil {
		return fmt.Errorf("启动命令执行器失败: %w", err)
	}
	utils.GetWorkPool().Go(func() { a.leaseLoop(ctx) })
	connectionDone := make(chan error, 1)
	if err := utils.GetWorkPool().Submit(func() { connectionDone <- a.connectionSupervisor(ctx) }); err != nil {
		return fmt.Errorf("启动 gRPC 连接管理器失败: %w", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	select {
	case signalValue := <-sigCh:
		stresslog.Info("[AGENT] 收到退出信号", zap.String("signal", signalValue.String()))
	case <-a.stopCh:
		stresslog.Info("[AGENT] 收到关闭命令")
	case err := <-connectionDone:
		if err != nil {
			stresslog.Error("[AGENT] gRPC 控制面停止", zap.Error(err))
			runErr = err
		}
	}
	return errors.Join(runErr, a.shutdown())
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

func (a *Agent) Status() AgentStatus {
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
var errTaskSessionInterrupted = errors.New("启动任务时控制会话中断")

type taskBusyError struct{ currentTaskID string }

func (e *taskBusyError) Error() string { return "已有任务运行: " + e.currentTaskID }

func (a *Agent) submitTask(task *TaskAssignment) error {
	return a.submitTaskWithSubmit(task, utils.GetWorkPool().Submit)
}

func (a *Agent) submitTaskWithSubmit(task *TaskAssignment, submit func(func()) error) error {
	taskCtx, taskCancel, err := a.reserveTask(task)
	if err != nil {
		return err
	}
	return a.launchReservedTask(taskCtx, taskCancel, task, submit)
}

func (a *Agent) reserveTask(task *TaskAssignment) (context.Context, context.CancelCauseFunc, error) {
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

func (a *Agent) launchReservedTask(taskCtx context.Context, taskCancel context.CancelCauseFunc, task *TaskAssignment, submit func(func()) error) error {
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

func (a *Agent) releaseReservedTask(task *TaskAssignment, cancel context.CancelCauseFunc, cause error) {
	cancel(cause)
	a.mu.Lock()
	if a.currentTask == task {
		a.currentTask, a.status, a.taskCancel, a.taskCancelCause = nil, StatusIdle, nil, nil
	}
	a.mu.Unlock()
}

func (a *Agent) executeTask(taskCtx context.Context, taskCancel context.CancelFunc, task *TaskAssignment) {
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
	stressReporter := NewStressReporter(a.telemetry, task.TaskID, a.cfg.MetricsInterval, a.collector)
	a.mu.Lock()
	a.stressReporter = stressReporter
	a.mu.Unlock()
	stressReporter.Start(taskCtx)

	runner := NewTaskRunner(task, a.cfg, a.collector)
	runner.OnStageReset = func(nextStageIndex int) {
		snapshot := stressReporter.Snapshot()
		a.collector.Reset()
		report := TaskCompletionReport{AgentID: a.id, TaskID: task.TaskID, Result: TaskCompleted,
			StageIndex: nextStageIndex, FinalSnapshot: snapshot, FinishedAt: time.Now()}
		if _, err := a.offerTaskReport(report); err != nil {
			stresslog.Warn("[AGENT] reset 阶段报告进入待确认队列失败", zap.String("taskID", task.TaskID), zap.Int("stageIndex", nextStageIndex), zap.Error(err))
		}
	}
	runResult := runner.Run(taskCtx)
	stressReporter.Stop()
	finalSnapshot := a.collector.Snapshot(nil, 0)
	report := TaskCompletionReport{AgentID: a.id, TaskID: task.TaskID, Result: runResult.Result, ErrorMsg: runResult.ErrorMsg,
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

func (a *Agent) offerTaskReport(report TaskCompletionReport) (<-chan error, error) {
	reportID := fmt.Sprintf("%s-%s-%d-%d", a.id, report.TaskID, report.StageIndex, time.Now().UnixNano())
	return a.reportOutbox.Offer(finalReportToProto(reportID, report))
}

func (a *Agent) shutdown() error {
	stresslog.Info("[AGENT] 正在关闭...")
	a.triggerStop()
	a.mu.Lock()
	a.shuttingDown = true
	a.mu.Unlock()
	if taskID, canceled := a.cancelCurrentTask("agent shutdown"); canceled {
		stresslog.Info("[AGENT] 等待当前任务完成清理与报告", zap.String("taskID", taskID))
	}
	done := make(chan struct{})
	utils.GetWorkPool().Go(func() { a.taskWG.Wait(); close(done) })
	timer := utils.GetTimer(a.cfg.TaskReportTimeout + 5*time.Second)
	select {
	case <-done:
	case <-timer.C:
		stresslog.Warn("[AGENT] 等待任务退出超时")
	}
	utils.PutTimer(timer)
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
	return nil
}
