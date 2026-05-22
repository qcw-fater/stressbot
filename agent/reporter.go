package agent

import (
	"context"
	"sync"
	"time"

	"stressbot/monitor"
	"stressbot/utils"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// StressReporter 压测指标推送循环。仅任务运行时存在。
//
// 生命周期由 executeTask 控制：任务开始时 Start，任务结束 / shutdown 时 Stop。
// Stop 幂等，重复调用安全。
type StressReporter struct {
	cli      *AdminClient
	agentID  string
	taskID   string
	interval time.Duration
	src      *monitor.MetricsCollector

	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewStressReporter 创建压测指标上报器。
func NewStressReporter(cli *AdminClient, agentID, taskID string, interval time.Duration, src *monitor.MetricsCollector) *StressReporter {
	return &StressReporter{
		cli:      cli,
		agentID:  agentID,
		taskID:   taskID,
		interval: interval,
		src:      src,
		stopCh:   make(chan struct{}),
	}
}

// Start 启动推送循环（非阻塞）。
func (r *StressReporter) Start(ctx context.Context) {
	utils.GetWorkPool().Go(func() {
		r.run(ctx)
	})
}

// Stop 停止推送循环（幂等）。先做一次同步 flush 确保最后一帧指标已推送。
func (r *StressReporter) Stop() {
	r.stopOnce.Do(func() {
		close(r.stopCh)
		// 同步 flush 最后一帧指标
		snap := r.src.Snapshot(nil, 0)
		report := StressReport{
			AgentID:    r.agentID,
			TaskID:     r.taskID,
			ReportedAt: time.Now(),
			Snapshot:   snap,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.cli.PostStress(ctx, report); err != nil {
			stresslog.Warn("[AGENT] 最终指标上报失败", zap.Error(err))
		}
	})
}

func (r *StressReporter) run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	var backoff time.Duration

	for {
		if backoff > 0 {
			select {
			case <-ctx.Done():
				return
			case <-r.stopCh:
				return
			case <-time.After(backoff):
			}
		} else {
			select {
			case <-ctx.Done():
				return
			case <-r.stopCh:
				return
			case <-ticker.C:
			}
		}

		snap := r.src.Snapshot(nil, 0)
		report := StressReport{
			AgentID:    r.agentID,
			TaskID:     r.taskID,
			ReportedAt: time.Now(),
			Snapshot:   snap,
		}
		if err := r.cli.PostStress(ctx, report); err != nil {
			backoff = nextBackoff(backoff, 30*time.Second)
			stresslog.Warn("[AGENT] 压测指标上报失败",
				zap.String("taskID", r.taskID),
				zap.Duration("backoff", backoff),
				zap.Error(err))
		} else {
			backoff = 0
		}
	}
}

// SystemReporter 系统指标推送循环。常驻运行（idle 期间也上报）。
type SystemReporter struct {
	cli      *AdminClient
	agentID  string
	interval time.Duration
	src      *SystemMonitor

	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewSystemReporter 创建系统指标上报器。
func NewSystemReporter(cli *AdminClient, agentID string, interval time.Duration, src *SystemMonitor) *SystemReporter {
	return &SystemReporter{
		cli:      cli,
		agentID:  agentID,
		interval: interval,
		src:      src,
		stopCh:   make(chan struct{}),
	}
}

// Start 启动推送循环（非阻塞）。
func (r *SystemReporter) Start(ctx context.Context) {
	utils.GetWorkPool().Go(func() {
		r.run(ctx)
	})
}

// Stop 停止推送循环（幂等）。
func (r *SystemReporter) Stop() {
	r.stopOnce.Do(func() {
		close(r.stopCh)
	})
}

func (r *SystemReporter) run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	var backoff time.Duration

	for {
		if backoff > 0 {
			select {
			case <-ctx.Done():
				return
			case <-r.stopCh:
				return
			case <-time.After(backoff):
			}
		} else {
			select {
			case <-ctx.Done():
				return
			case <-r.stopCh:
				return
			case <-ticker.C:
			}
		}

		snap := r.src.Snapshot()
		report := SystemReport{
			AgentID:    r.agentID,
			ReportedAt: time.Now(),
			Snapshot:   snap,
		}
		if err := r.cli.PostSystem(ctx, report); err != nil {
			backoff = nextBackoff(backoff, 30*time.Second)
			stresslog.Warn("[AGENT] 系统指标上报失败",
				zap.Duration("backoff", backoff),
				zap.Error(err))
		} else {
			backoff = 0
		}
	}
}
