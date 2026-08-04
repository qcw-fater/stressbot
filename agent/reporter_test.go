package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"stressbot/monitor"
	json "stressbot/utils/jsonx"
)

type scriptedStressPoster struct {
	errs  []error
	calls []StressReport
}

func TestStressReporterStopFlushesSamplesCollectedBehindPendingWindow(t *testing.T) {
	collector := newAgentReporterCollector()
	poster := &scriptedStressPoster{errs: []error{errors.New("temporary"), nil, errors.New("temporary"), nil}}
	start := time.Now().Add(-10 * time.Second)
	reporter := &StressReporter{
		cli:          poster,
		agentID:      "agent-1",
		taskID:       "task-1",
		interval:     5 * time.Second,
		src:          collector,
		stopCh:       make(chan struct{}),
		nextSequence: 1,
		windowStart:  start,
	}

	recordAgentReporterSuccess(collector, "login", time.Millisecond)
	if err := reporter.reportOnce(context.Background(), start.Add(5*time.Second)); err == nil {
		t.Fatal("首次上报应按脚本失败")
	}
	recordAgentReporterSuccess(collector, "login", 2*time.Millisecond)
	reporter.Stop()

	if len(poster.calls) != 4 {
		t.Fatalf("上报次数 = %d, want 4（失败、确认旧窗口、新窗口失败并重试）", len(poster.calls))
	}
	failedFinal, _ := json.Marshal(poster.calls[2].Snapshot.Window)
	retriedFinal, _ := json.Marshal(poster.calls[3].Snapshot.Window)
	if string(failedFinal) != string(retriedFinal) {
		t.Fatalf("最终窗口重试时发生变化:\n%s\n%s", failedFinal, retriedFinal)
	}
	last := poster.calls[3].Snapshot.Window
	if last.Sequence != 2 || last.Summary.SampleCount != 1 {
		t.Fatalf("最终窗口 sequence/samples = %d/%d, want 2/1", last.Sequence, last.Summary.SampleCount)
	}
}

func (p *scriptedStressPoster) PostStress(_ context.Context, report StressReport) error {
	p.calls = append(p.calls, report)
	if len(p.errs) == 0 {
		return nil
	}
	err := p.errs[0]
	p.errs = p.errs[1:]
	return err
}

func TestStressReporterRetriesIdenticalWindowUntilAcknowledged(t *testing.T) {
	collector := newAgentReporterCollector()
	poster := &scriptedStressPoster{errs: []error{errors.New("temporary"), errors.New("temporary"), nil, nil}}
	start := time.Unix(100, 0)
	reporter := &StressReporter{
		cli:          poster,
		agentID:      "agent-1",
		taskID:       "task-1",
		interval:     5 * time.Second,
		src:          collector,
		stopCh:       make(chan struct{}),
		nextSequence: 1,
		windowStart:  start,
	}

	recordAgentReporterSuccess(collector, "login", time.Millisecond)
	for _, now := range []time.Time{start.Add(5 * time.Second), start.Add(10 * time.Second), start.Add(15 * time.Second)} {
		_ = reporter.reportOnce(context.Background(), now)
	}
	if len(poster.calls) != 3 {
		t.Fatalf("calls=%d, want 3", len(poster.calls))
	}
	want, _ := json.Marshal(poster.calls[0].Snapshot.Window)
	for i := 1; i < 3; i++ {
		got, _ := json.Marshal(poster.calls[i].Snapshot.Window)
		if string(got) != string(want) {
			t.Fatalf("retry %d changed window:\n%s\n%s", i, want, got)
		}
	}
	if poster.calls[0].Snapshot.Window.Sequence != 1 {
		t.Fatalf("first sequence=%d, want 1", poster.calls[0].Snapshot.Window.Sequence)
	}

	recordAgentReporterSuccess(collector, "login", 2*time.Millisecond)
	if err := reporter.reportOnce(context.Background(), start.Add(20*time.Second)); err != nil {
		t.Fatalf("next report failed: %v", err)
	}
	if poster.calls[3].Snapshot.Window.Sequence != 2 {
		t.Fatalf("next sequence=%d, want 2", poster.calls[3].Snapshot.Window.Sequence)
	}
	if got := poster.calls[3].Snapshot.Window.Summary.SampleCount; got != 1 {
		t.Fatalf("next window samples=%d, want 1", got)
	}
}

func newAgentReporterCollector() *monitor.MetricsCollector {
	return monitor.NewCollector(monitor.CollectorConfig{ApdexThresholdMs: 100, TimingDetail: "rtt"})
}

func recordAgentReporterSuccess(c *monitor.MetricsCollector, name string, d time.Duration) {
	c.RecordActionStart(name)
	c.RecordAction(name, monitor.ResultSuccess, monitor.ActionTiming{}, d, 0, 0, nil)
}
