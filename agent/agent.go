package agent

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v4/mem"
	stresslog "stressbot/utils/log"
	"stressbot/utils"

	"stressbot/monitor"

	"go.uber.org/zap"
)

// Agent 是分布式压测系统的执行节点。
// 启动后向 Admin 注册，等待任务下发，执行压测，上报指标。
type Agent struct {
	id      string
	cfg     *ResolvedConfig
	started time.Time
	ctx     context.Context

	sysmon    *SystemMonitor
	collector *monitor.MetricsCollector
	httpSrv   *http.Server
	httpCli   *AdminClient

	// 任务状态
	mu          sync.Mutex
	status      AgentStatus
	currentTask *TaskAssignment
	taskCancel  context.CancelFunc
	runner      *TaskRunner

	// 上报循环
	sysReporter    *SystemReporter
	stressReporter *StressReporter

	// 优雅退出
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// New 创建 Agent 实例。
func New(cfg *ResolvedConfig, collector *monitor.MetricsCollector) (*Agent, error) {
	id := generateUUID()

	static := CollectStaticInfo()
	sysmon, err := NewSystemMonitor(cfg.SystemInterval, static)
	if err != nil {
		return nil, fmt.Errorf("创建 SystemMonitor 失败: %w", err)
	}

	// 用 gopsutil 补充更精确的内存值
	if vm, err := mem.VirtualMemory(); err == nil {
		static.MemTotalMB = vm.Total / 1024 / 1024
	}

	httpCli := NewAdminClient(cfg.AdminAddr, id)

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
func (a *Agent) Run() error {
	stresslog.Info("[AGENT] 启动中",
		zap.String("agentID", a.id),
		zap.String("name", a.cfg.Name),
		zap.String("adminAddr", a.cfg.AdminAddr))

	// 1. 启动系统监控
	a.sysmon.Start(a.stopCh)

	// 2. 启动 HTTP 服务器（接收 Admin 命令）
	if err := a.startHTTPServer(); err != nil {
		return fmt.Errorf("启动 HTTP 服务失败: %w", err)
	}

	// 3. 注册到 Admin（永不放弃）
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.ctx = ctx

	if err := a.registerWithRetry(ctx); err != nil {
		a.shutdownHTTPServer(ctx)
		return fmt.Errorf("注册失败: %w", err)
	}

	// 4. 启动系统指标上报（常驻）
	a.sysReporter = NewSystemReporter(a.httpCli, a.id, a.cfg.SystemInterval, a.sysmon, &a.wg)
	a.sysReporter.Start(ctx)

	// 5. 启动心跳循环
	a.wg.Add(1)
	utils.GetWorkPool().Go(func() {
		defer a.wg.Done()
		a.heartbeatLoop(ctx)
	})

	// 6. 启动任务轮询（回退通道）
	a.wg.Add(1)
	utils.GetWorkPool().Go(func() {
		defer a.wg.Done()
		a.taskPollLoop(ctx)
	})

	stresslog.Info("[AGENT] 已就绪，等待任务下发",
		zap.String("agentID", a.id),
		zap.String("listenAddr", a.cfg.ListenAddr))

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

// registerWithRetry 指数退避重试注册，永不放弃。
func (a *Agent) registerWithRetry(ctx context.Context) error {
	static := a.sysmon.Static()
	req := RegisterRequest{
		AgentID:        a.id,
		Name:           a.cfg.Name,
		Address:        buildAddress(a.cfg.ListenAddr),
		AppVersion:     a.cfg.AppVersion,
		MaxBots:        a.cfg.MaxBots,
		StressInterval: a.cfg.StressInterval.String(),
		SystemInterval: a.cfg.SystemInterval.String(),
		StaticInfo:     static,
	}

	stresslog.Info("[AGENT] 开始注册到 Admin", zap.String("adminAddr", a.cfg.AdminAddr))

	return RetryWithBackoff(ctx, func() error {
		resp, err := a.httpCli.Register(ctx, req)
		if err != nil {
			stresslog.Warn("[AGENT] 注册失败，将重试", zap.Error(err))
			return err
		}
		stresslog.Info("[AGENT] 注册成功",
			zap.String("agentID", resp.AgentID),
			zap.String("heartbeatTTL", resp.HeartbeatTTL))
		return nil
	}, a.cfg.RegisterRetryMax, 0, "register")
}

// heartbeatLoop 心跳循环（10s 一次）。
func (a *Agent) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(a.cfg.HBInterval)
	defer ticker.Stop()

	var consecutiveFailures int
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopCh:
			return
		case <-ticker.C:
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

			if err := a.httpCli.Heartbeat(ctx, req); err != nil {
				consecutiveFailures++
				if consecutiveFailures <= 3 {
					stresslog.Warn("[AGENT] 心跳失败", zap.Int("consecutive", consecutiveFailures), zap.Error(err))
				} else {
					stresslog.Error("[AGENT] 心跳连续失败", zap.Int("consecutive", consecutiveFailures), zap.Error(err))
				}
			} else {
				consecutiveFailures = 0
			}
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
			if a.currentTask != nil {
				a.mu.Unlock()
				continue // 已有任务，跳过
			}
			a.mu.Unlock()

			task, err := a.httpCli.FetchPendingTask(ctx)
			if err != nil {
				stresslog.Debug("[AGENT] 轮询任务失败", zap.Error(err))
				continue
			}
			if task != nil {
				stresslog.Info("[AGENT] 轮询到任务", zap.String("taskID", task.TaskID))
				go a.executeTask(ctx, task)
			}
		}
	}
}

// executeTask 执行任务（异步）。
func (a *Agent) executeTask(ctx context.Context, task *TaskAssignment) {
	a.mu.Lock()
	if a.currentTask != nil {
		a.mu.Unlock()
		return
	}
	a.currentTask = task
	a.status = StatusBusy
	a.mu.Unlock()

	// 任务结束时清理状态
	defer func() {
		a.mu.Lock()
		a.currentTask = nil
		a.taskCancel = nil
		a.status = StatusIdle
		a.mu.Unlock()
	}()

	// 创建任务 context
	taskCtx, taskCancel := context.WithCancel(ctx)
	a.taskCancel = taskCancel
	defer taskCancel()

	// 创建并启动 StressReporter
	a.stressReporter = NewStressReporter(
		a.httpCli, a.id, task.TaskID,
		a.cfg.StressInterval, a.collector, &a.wg,
	)
	a.stressReporter.Start(taskCtx)

	// 创建 TaskRunner 执行
	runner := NewTaskRunner(task, a.cfg, a.httpCli, a.collector)
	a.mu.Lock()
	a.runner = runner
	a.mu.Unlock()

	result, errMsg := runner.Run(taskCtx)

	if result == TaskFailed {
		stresslog.Error("[AGENT] 任务执行失败",
			zap.String("taskID", task.TaskID),
			zap.String("error", errMsg))
	} else if errMsg != "" {
		stresslog.Warn("[AGENT] 任务完成但有错误",
			zap.String("taskID", task.TaskID),
			zap.String("result", string(result)),
			zap.String("error", errMsg))
	}

	// 停止 StressReporter
	a.stressReporter.Stop()

	// 等一小段时间确保最后的指标已采集
	time.Sleep(500 * time.Millisecond)

	// 采集 finalSnapshot
	finalSnap := a.collector.Snapshot(nil, 0)

	// 上报任务完成（最多重试 30 分钟）
	report := TaskCompletionReport{
		AgentID:       a.id,
		TaskID:        task.TaskID,
		Result:        result,
		ErrorMsg:      errMsg,
		FinishedAt:    time.Now(),
		FinalSnapshot: finalSnap,
	}

	reportCtx, reportCancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer reportCancel()

	if err := RetryWithBackoff(reportCtx, func() error {
		return a.httpCli.ReportTaskDone(reportCtx, report)
	}, 60*time.Second, 30*time.Minute, "report-task-done"); err != nil {
		stresslog.Error("[AGENT] 最终上报失败（已重试 30 分钟）",
			zap.String("taskID", task.TaskID),
			zap.Error(err))
	} else {
		stresslog.Info("[AGENT] 任务完成已上报",
			zap.String("taskID", task.TaskID),
			zap.String("result", string(result)))
	}

	// 清理临时目录
	runner.Cleanup()
}

// shutdown 优雅关闭。
func (a *Agent) shutdown() error {
	stresslog.Info("[AGENT] 正在关闭...")

	// 取消当前任务
	a.mu.Lock()
	if a.taskCancel != nil {
		a.taskCancel()
	}
	a.mu.Unlock()

	// 注销（best-effort）
	deregCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a.httpCli.Deregister(deregCtx)

	// 关闭 HTTP 服务器
	a.shutdownHTTPServer(context.Background())

	// 等待所有 goroutine 退出
	done := make(chan struct{})
	utils.GetWorkPool().Go(func() {
		a.wg.Wait()
		close(done)
	})

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		stresslog.Warn("[AGENT] 等待 goroutine 退出超时")
	}

	stresslog.Info("[AGENT] 已退出")
	return nil
}

// buildAddress 根据监听地址构建完整 HTTP 地址。
// ":7070" → "http://hostname:7070"
func buildAddress(listenAddr string) string {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "127.0.0.1"
	}
	addr := listenAddr
	if addr[0] == ':' {
		return "http://" + hostname + addr
	}
	return "http://" + addr
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
