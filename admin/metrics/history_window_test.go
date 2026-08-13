package metrics

import (
	"context"
	"testing"
	"time"
)

type historyWindowRecorder struct {
	points []HistoryTrendPoint
	err    error
}

func (r *historyWindowRecorder) AppendTimeseries(_ context.Context, _ string, point HistoryTrendPoint) error {
	r.points = append(r.points, point)
	return r.err
}

func TestSamplerPersistsAcceptedWindowsExactlyOnce(t *testing.T) {
	clock := &metricStoreTestClock{now: time.Unix(100, 0)}
	store := NewMetricsWindowStore(clock.Now)
	report := metricStoreTestReport(t, "task-1", "agent-1", 1, time.Unix(90, 0), time.Unix(95, 0))
	if _, err := store.Accept(report, "task-1", 5*time.Second, 100*time.Millisecond); err != nil {
		t.Fatalf("接收窗口: %v", err)
	}
	if _, err := store.Accept(report, "task-1", 5*time.Second, 100*time.Millisecond); err != nil {
		t.Fatalf("接收重复窗口: %v", err)
	}

	recorder := &historyWindowRecorder{}
	tasks, err := NewTaskStore(t.TempDir())
	if err != nil {
		t.Fatalf("create task store: %v", err)
	}
	if err := tasks.Create(&Task{ID: "task-1", SucceededAgents: []string{"agent-1"}}); err != nil {
		t.Fatalf("seed task store: %v", err)
	}
	sampler := &Sampler{
		history: recorder,
		windows: store,
		tasks:   tasks,
	}
	if saved, err := sampler.sampleOnce(context.Background(), "task-1", time.Unix(100, 0), 10); err != nil || !saved {
		t.Fatalf("首次采样 saved/error = %v/%v", saved, err)
	}
	if saved, err := sampler.sampleOnce(context.Background(), "task-1", time.Unix(110, 0), 20); err != nil || saved {
		t.Fatalf("重复采样 saved/error = %v/%v", saved, err)
	}
	if len(recorder.points) != 1 {
		t.Fatalf("写入行数 = %d, want 1", len(recorder.points))
	}
	point := recorder.points[0]
	if point.SampleCount != 1 || point.TotalQPS != 0.2 {
		t.Fatalf("样本/QPS = %d/%v, want 1/0.2", point.SampleCount, point.TotalQPS)
	}
	if point.RTTP99Ms == nil || *point.RTTP99Ms <= 0 {
		t.Fatalf("P99 = %v, want non-null positive", point.RTTP99Ms)
	}
	if len(point.HistoryBatchToken) != 32 {
		t.Fatalf("批次 token 长度 = %d, want 32", len(point.HistoryBatchToken))
	}
	if point.AssignedAgents == nil || *point.AssignedAgents != 1 || point.ReportingCoverage == nil || *point.ReportingCoverage != 1 {
		t.Fatalf("结束时节点覆盖率 = assigned %v / coverage %v, want 1/1", point.AssignedAgents, point.ReportingCoverage)
	}
}

func TestSamplerStopFlushesFinalAcceptedWindow(t *testing.T) {
	clock := &metricStoreTestClock{now: time.Unix(100, 0)}
	store := NewMetricsWindowStore(clock.Now)
	report := metricStoreTestReport(t, "task-1", "agent-1", 1, time.Unix(90, 0), time.Unix(95, 0))
	if _, err := store.Accept(report, "task-1", 5*time.Second, 100*time.Millisecond); err != nil {
		t.Fatalf("接收窗口: %v", err)
	}

	recorder := &historyWindowRecorder{}
	done := make(chan struct{})
	close(done)
	sampler := &Sampler{
		history: recorder,
		windows: store,
		current: &samplerJob{
			taskID: "task-1", startedAt: time.Now().Add(-time.Second),
			cancel: func() {}, done: done,
		},
	}
	sampler.Stop("task-1")

	if len(recorder.points) != 1 || store.PendingHistoryCount("task-1") != 0 {
		t.Fatalf("终态冲刷 rows=%d pending=%d, want 1/0", len(recorder.points), store.PendingHistoryCount("task-1"))
	}
}
