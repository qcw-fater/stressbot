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
	cli      stressPoster
	agentID  string
	taskID   string
	interval time.Duration
	src      *monitor.MetricsCollector

	stopOnce     sync.Once
	stopCh       chan struct{}
	reportMu     sync.Mutex
	pending      *StressReport
	nextSequence uint64
	windowStart  time.Time
}

type stressPoster interface {
	PostStress(context.Context, StressReport) error
}

// NewStressReporter 创建压测指标上报器。
func NewStressReporter(cli *AdminClient, agentID, taskID string, interval time.Duration, src *monitor.MetricsCollector) *StressReporter {
	now := time.Now()
	return &StressReporter{
		cli:          cli,
		agentID:      agentID,
		taskID:       taskID,
		interval:     interval,
		src:          src,
		stopCh:       make(chan struct{}),
		nextSequence: 1,
		windowStart:  now,
	}
}

// Start 启动推送循环（非阻塞）。
func (r *StressReporter) Start(ctx context.Context) {
	utils.GetWorkPool().Go(func() {
		r.run(ctx)
	})
}

// Snapshot 采集当前指标快照（同步，不停止上报循环）。
// 用于阶段重置时获取当前阶段的最终指标。
func (r *StressReporter) Snapshot() *monitor.CollectorSnapshot {
	return r.src.Snapshot(nil, 0)
}

// Stop 停止推送循环（幂等）。先做一次同步 flush 确保最后一帧指标已推送。
func (r *StressReporter) Stop() {
	r.stopOnce.Do(func() {
		close(r.stopCh)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r.reportMu.Lock()
		hadPending := r.pending != nil
		r.reportMu.Unlock()
		if err := r.reportUntilAcknowledged(ctx, time.Now()); err != nil {
			stresslog.Warn("[AGENT] 最终指标上报失败", zap.Error(err))
			return
		}
		// 确认旧 pending 窗口后，失败期间积累的新样本仍在活动窗口中，
		// 必须再旋转并上报一次才能完整冲刷。
		if hadPending {
			if err := r.reportUntilAcknowledged(ctx, time.Now()); err != nil {
				stresslog.Warn("[AGENT] 最终新增指标上报失败", zap.Error(err))
			}
		}
	})
}

func (r *StressReporter) reportUntilAcknowledged(ctx context.Context, now time.Time) error {
	backoff := 50 * time.Millisecond
	for {
		err := r.reportOnce(ctx, now)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 500*time.Millisecond {
			backoff = 500 * time.Millisecond
		}
	}
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

		if err := r.reportOnce(ctx, time.Now()); err != nil {
			backoff = nextBackoff(backoff, 30*time.Second)
			stresslog.Warn("[AGENT] 压测指标上报失败",
				zap.String("agentID", r.agentID),
				zap.String("taskID", r.taskID),
				zap.Duration("backoff", backoff),
				zap.Error(err))
		} else {
			backoff = 0
		}
	}
}

func (r *StressReporter) reportOnce(ctx context.Context, now time.Time) error {
	r.reportMu.Lock()
	defer r.reportMu.Unlock()
	if r.pending == nil {
		snap := r.src.TakeReportSnapshot(monitor.ReportMeta{
			Sequence:                r.nextSequence,
			StartedAt:               r.windowStart,
			EndedAt:                 now,
			ExpectedIntervalSeconds: r.interval.Seconds(),
		})
		r.windowStart = now
		r.pending = &StressReport{
			AgentID:    r.agentID,
			TaskID:     r.taskID,
			ReportedAt: now,
			Snapshot:   snap,
		}
	}
	if err := r.cli.PostStress(ctx, *r.pending); err != nil {
		return err
	}
	r.pending = nil
	r.nextSequence++
	return nil
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
			AgentID:  r.agentID,
			Snapshot: snap,
		}
		if err := r.cli.PostSystem(ctx, report); err != nil {
			backoff = nextBackoff(backoff, 30*time.Second)
			stresslog.Warn("[AGENT] 系统指标上报失败",
				zap.String("agentID", r.agentID),
				zap.Duration("backoff", backoff),
				zap.Error(err))
		} else {
			backoff = 0
		}
	}
}
