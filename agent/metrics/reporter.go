// Package metrics 实现 Agent 侧指标采集与上报：压测指标窗口化上报、
// 系统资源周期采样，以及 gRPC 流发送用的最新帧缓冲。
package metrics

import (
	"context"
	"sync"
	"time"

	"stressbot/internal/workpool"
	"stressbot/monitor"
)

// Sink 接收 Agent 采集的压测指标与系统指标。
type Sink interface {
	OfferStress(taskID string, reportedAt time.Time, snapshot *monitor.CollectorSnapshot)
	OfferSystem(snapshot SystemSnapshot)
}

// StressReporter 按固定周期从 MetricsCollector 切出压测指标窗口并递交给 Sink，
// 窗口序列号从 1 递增；Stop 时同步冲刷最后一帧。
type StressReporter struct {
	sink         Sink
	taskID       string
	interval     time.Duration
	src          *monitor.MetricsCollector
	stopCh       chan struct{}
	stopOnce     sync.Once
	reportMu     sync.Mutex
	nextSequence uint64
	windowStart  time.Time
}

// NewStressReporter 创建压测指标上报器，首个上报窗口从当前时刻开始。
func NewStressReporter(sink Sink, taskID string, interval time.Duration, src *monitor.MetricsCollector) *StressReporter {
	return &StressReporter{sink: sink, taskID: taskID, interval: interval, src: src, stopCh: make(chan struct{}), nextSequence: 1, windowStart: time.Now()}
}

// Start 启动上报循环；ctx 取消或 Stop 调用后退出。
func (r *StressReporter) Start(ctx context.Context) {
	workpool.Default().Go(func() { r.run(ctx) })
}

// Snapshot 返回当前累计快照，不切换活动窗口、不影响上报序列。
func (r *StressReporter) Snapshot() *monitor.CollectorSnapshot { return r.src.Snapshot(nil, 0) }

// Stop 停止上报循环并立即冲刷最后一帧窗口，保证任务结束前数据完整；幂等。
func (r *StressReporter) Stop() {
	r.stopOnce.Do(func() {
		close(r.stopCh)
		r.reportOnce(time.Now())
	})
}

func (r *StressReporter) run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case now := <-ticker.C:
			r.reportOnce(now)
		}
	}
}

func (r *StressReporter) reportOnce(now time.Time) {
	r.reportMu.Lock()
	snapshot := r.src.TakeReportSnapshot(monitor.ReportMeta{
		Sequence: r.nextSequence, StartedAt: r.windowStart, EndedAt: now, ExpectedIntervalSeconds: r.interval.Seconds(),
	})
	r.nextSequence++
	r.windowStart = now
	r.reportMu.Unlock()
	r.sink.OfferStress(r.taskID, now, snapshot)
}

// SystemReporter 按固定周期把系统资源快照递交给 Sink。
type SystemReporter struct {
	sink     Sink
	interval time.Duration
	src      *SystemMonitor
	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewSystemReporter 创建系统指标上报器。
func NewSystemReporter(sink Sink, interval time.Duration, src *SystemMonitor) *SystemReporter {
	return &SystemReporter{sink: sink, interval: interval, src: src, stopCh: make(chan struct{})}
}

// Start 启动上报循环；ctx 取消或 Stop 调用后退出。
func (r *SystemReporter) Start(ctx context.Context) { workpool.Default().Go(func() { r.run(ctx) }) }

// Stop 停止上报循环；幂等，不额外冲刷数据。
func (r *SystemReporter) Stop() { r.stopOnce.Do(func() { close(r.stopCh) }) }

func (r *SystemReporter) run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.sink.OfferSystem(r.src.Snapshot())
		}
	}
}
