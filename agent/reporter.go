package agent

import (
	"context"
	"sync"
	"time"

	"stressbot/monitor"
	"stressbot/utils"
)

type TelemetrySink interface {
	OfferStress(taskID string, reportedAt time.Time, snapshot *monitor.CollectorSnapshot)
	OfferSystem(snapshot SystemSnapshot)
}

type StressReporter struct {
	sink         TelemetrySink
	taskID       string
	interval     time.Duration
	src          *monitor.MetricsCollector
	stopCh       chan struct{}
	stopOnce     sync.Once
	reportMu     sync.Mutex
	nextSequence uint64
	windowStart  time.Time
}

func NewStressReporter(sink TelemetrySink, taskID string, interval time.Duration, src *monitor.MetricsCollector) *StressReporter {
	return &StressReporter{sink: sink, taskID: taskID, interval: interval, src: src, stopCh: make(chan struct{}), nextSequence: 1, windowStart: time.Now()}
}

func (r *StressReporter) Start(ctx context.Context) {
	utils.GetWorkPool().Go(func() { r.run(ctx) })
}

func (r *StressReporter) Snapshot() *monitor.CollectorSnapshot { return r.src.Snapshot(nil, 0) }

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

type SystemReporter struct {
	sink     TelemetrySink
	interval time.Duration
	src      *SystemMonitor
	stopCh   chan struct{}
	stopOnce sync.Once
}

func NewSystemReporter(sink TelemetrySink, interval time.Duration, src *SystemMonitor) *SystemReporter {
	return &SystemReporter{sink: sink, interval: interval, src: src, stopCh: make(chan struct{})}
}

func (r *SystemReporter) Start(ctx context.Context) { utils.GetWorkPool().Go(func() { r.run(ctx) }) }

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
