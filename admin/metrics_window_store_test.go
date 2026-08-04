package admin

import (
	"testing"
	"time"

	"stressbot/monitor"
)

type metricStoreTestClock struct {
	now time.Time
}

func (c *metricStoreTestClock) Now() time.Time    { return c.now }
func (c *metricStoreTestClock) Set(now time.Time) { c.now = now }

func metricStoreTestReport(t *testing.T, taskID, agentID string, sequence uint64, startedAt, endedAt time.Time) StressReport {
	t.Helper()
	collector := monitor.NewCollector(monitor.CollectorConfig{ApdexThresholdMs: 100, TimingDetail: "rtt"})
	collector.RecordActionStart("login")
	collector.RecordAction("login", monitor.ResultSuccess, monitor.ActionTiming{
		Requests: []monitor.RequestTiming{{WireRTT: 10 * time.Millisecond, Observed: monitor.StageRTT}},
	}, 10*time.Millisecond, 10, 20, nil)
	snapshot := collector.TakeReportSnapshot(monitor.ReportMeta{
		Sequence:                sequence,
		StartedAt:               startedAt,
		EndedAt:                 endedAt,
		ExpectedIntervalSeconds: 5,
	})
	return StressReport{
		AgentID:    agentID,
		TaskID:     taskID,
		ReportedAt: endedAt.Add(24 * time.Hour),
		Snapshot:   snapshot,
	}
}

func TestMetricsWindowStoreAcceptIsIdempotentAndUsesReceiveTime(t *testing.T) {
	clock := &metricStoreTestClock{now: time.Unix(100, 0)}
	store := NewMetricsWindowStore(clock.Now)
	report := metricStoreTestReport(t, "task-1", "agent-1", 1, time.Unix(90, 0), time.Unix(95, 0))

	first, err := store.Accept(report, "task-1", 5*time.Second, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("首次接收失败: %v", err)
	}
	if first.Status != MetricWindowAccepted {
		t.Fatalf("首次状态 = %q, want %q", first.Status, MetricWindowAccepted)
	}

	clock.Set(time.Unix(101, 0))
	duplicate, err := store.Accept(report, "task-1", 5*time.Second, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("重复接收失败: %v", err)
	}
	if duplicate.Status != MetricWindowDuplicate {
		t.Fatalf("重复状态 = %q, want %q", duplicate.Status, MetricWindowDuplicate)
	}
	if got := store.PendingHistoryCount("task-1"); got != 1 {
		t.Fatalf("待写历史窗口数 = %d, want 1", got)
	}
	state, ok := store.AgentState("task-1", "agent-1")
	if !ok {
		t.Fatal("未找到节点指标状态")
	}
	if !state.ReceivedAt.Equal(time.Unix(100, 0)) {
		t.Fatalf("接收时间 = %s, want Admin 首次接收时间", state.ReceivedAt)
	}
}

func TestMetricsWindowStoreRejectsSequenceGapAndTaskMismatch(t *testing.T) {
	clock := &metricStoreTestClock{now: time.Unix(100, 0)}
	store := NewMetricsWindowStore(clock.Now)

	gap := metricStoreTestReport(t, "task-1", "agent-1", 2, time.Unix(90, 0), time.Unix(95, 0))
	if _, err := store.Accept(gap, "task-1", 5*time.Second, 100*time.Millisecond); err == nil {
		t.Fatal("首个窗口 sequence=2 应被拒绝")
	}

	mismatch := metricStoreTestReport(t, "task-old", "agent-1", 1, time.Unix(90, 0), time.Unix(95, 0))
	if _, err := store.Accept(mismatch, "task-1", 5*time.Second, 100*time.Millisecond); err == nil {
		t.Fatal("任务不匹配应被拒绝")
	}
}

func TestMetricsWindowStoreRejectsMalformedSketch(t *testing.T) {
	clock := &metricStoreTestClock{now: time.Unix(100, 0)}
	store := NewMetricsWindowStore(clock.Now)
	report := metricStoreTestReport(t, "task-1", "agent-1", 1, time.Unix(90, 0), time.Unix(95, 0))
	report.Snapshot.Window.Actions[0].RTT.Sketch = nil

	if _, err := store.Accept(report, "task-1", 5*time.Second, 100*time.Millisecond); err == nil {
		t.Fatal("缺少 DDSketch 的非空分布应被拒绝")
	}
}

func TestMetricsWindowStoreRejectsDataOnEmptyHistogram(t *testing.T) {
	clock := &metricStoreTestClock{now: time.Unix(100, 0)}
	store := NewMetricsWindowStore(clock.Now)
	report := metricStoreTestReport(t, "task-1", "agent-1", 1, time.Unix(90, 0), time.Unix(95, 0))
	zero := 0.0
	report.Snapshot.Window.Actions[0].ListenWait.MinMs = &zero

	if _, err := store.Accept(report, "task-1", 5*time.Second, 100*time.Millisecond); err == nil {
		t.Fatal("空分布携带 0ms 展示值应被拒绝")
	}
}

func TestMetricsWindowStoreRejectsDerivedAverageMismatch(t *testing.T) {
	clock := &metricStoreTestClock{now: time.Unix(100, 0)}
	store := NewMetricsWindowStore(clock.Now)
	report := metricStoreTestReport(t, "task-1", "agent-1", 1, time.Unix(90, 0), time.Unix(95, 0))
	action := &report.Snapshot.Actions[0]
	action.EncodeSampleCount = 1
	action.EncodeCostSumNs = int64(time.Millisecond)
	action.EncodeAvgMs = 999

	if _, err := store.Accept(report, "task-1", 5*time.Second, 100*time.Millisecond); err == nil {
		t.Fatal("与纳秒总和不一致的编码均值应被拒绝")
	}
}

func TestMetricsWindowStoreHistoryPeekAckKeepsStableBatch(t *testing.T) {
	clock := &metricStoreTestClock{now: time.Unix(100, 0)}
	store := NewMetricsWindowStore(clock.Now)
	first := metricStoreTestReport(t, "task-1", "agent-1", 1, time.Unix(90, 0), time.Unix(95, 0))
	if _, err := store.Accept(first, "task-1", 5*time.Second, 100*time.Millisecond); err != nil {
		t.Fatalf("接收第一窗口: %v", err)
	}

	batch1, ok := store.PeekHistory("task-1")
	if !ok || len(batch1.Windows) != 1 {
		t.Fatalf("第一批窗口数 = %d, want 1", len(batch1.Windows))
	}
	second := metricStoreTestReport(t, "task-1", "agent-1", 2, time.Unix(95, 0), time.Unix(100, 0))
	// 使用同一个 Collector 生成累计分布更接近生产；本测试只验证批次生命周期，
	// 因而将第二个报告作为独立节点，避免手工伪造 sketch 与精确计数不一致。
	second.AgentID = "agent-2"
	second.Snapshot.Window.Sequence = 1
	if _, err := store.Accept(second, "task-1", 5*time.Second, 100*time.Millisecond); err != nil {
		t.Fatalf("接收第二窗口: %v", err)
	}

	retry, ok := store.PeekHistory("task-1")
	if !ok || retry.Token != batch1.Token || len(retry.Windows) != 1 {
		t.Fatal("未确认批次应保持完全稳定")
	}
	if !store.AckHistory("task-1", batch1.Token) {
		t.Fatal("确认第一批失败")
	}
	batch2, ok := store.PeekHistory("task-1")
	if !ok || len(batch2.Windows) != 1 || batch2.Token == batch1.Token {
		t.Fatal("第一批确认后应暴露后续新窗口")
	}
}

func TestMetricsWindowStoreReleasesTerminalTaskAfterHistoryAck(t *testing.T) {
	clock := &metricStoreTestClock{now: time.Unix(100, 0)}
	store := NewMetricsWindowStore(clock.Now)
	report := metricStoreTestReport(t, "task-1", "agent-1", 1, time.Unix(90, 0), time.Unix(95, 0))
	if _, err := store.Accept(report, "task-1", 5*time.Second, 100*time.Millisecond); err != nil {
		t.Fatalf("接收窗口: %v", err)
	}
	batch, ok := store.PeekHistory("task-1")
	if !ok {
		t.Fatal("未取得待归档窗口")
	}

	store.MarkTaskTerminal("task-1")
	if _, ok := store.AgentState("task-1", "agent-1"); !ok {
		t.Fatal("历史批次确认前不应释放累计状态")
	}
	if !store.AckHistory("task-1", batch.Token) {
		t.Fatal("确认历史批次失败")
	}
	if _, ok := store.AgentState("task-1", "agent-1"); ok {
		t.Fatal("历史批次确认后应释放终态任务")
	}
	if got := store.PendingHistoryCount("task-1"); got != 0 {
		t.Fatalf("释放后待归档窗口数 = %d, want 0", got)
	}
}

func TestMetricsWindowStoreDropTaskDiscardsUnpersistedHistory(t *testing.T) {
	clock := &metricStoreTestClock{now: time.Unix(100, 0)}
	store := NewMetricsWindowStore(clock.Now)
	report := metricStoreTestReport(t, "task-1", "agent-1", 1, time.Unix(90, 0), time.Unix(95, 0))
	if _, err := store.Accept(report, "task-1", 5*time.Second, 100*time.Millisecond); err != nil {
		t.Fatalf("接收窗口: %v", err)
	}

	store.DropTask("task-1")
	if _, ok := store.AgentState("task-1", "agent-1"); ok {
		t.Fatal("禁用历史持久化时应直接释放终态任务")
	}
	if got := store.PendingHistoryCount("task-1"); got != 0 {
		t.Fatalf("释放后待归档窗口数 = %d, want 0", got)
	}
}

func TestMetricsWindowStoreAllowsCumulativeResetOnlyWithNewEpoch(t *testing.T) {
	clock := &metricStoreTestClock{now: time.Unix(100, 0)}
	store := NewMetricsWindowStore(clock.Now)
	first := metricStoreTestReport(t, "task-1", "agent-1", 1, time.Unix(90, 0), time.Unix(95, 0))
	first.Snapshot.CollectionEpoch = 7
	if _, err := store.Accept(first, "task-1", 5*time.Second, 100*time.Millisecond); err != nil {
		t.Fatalf("接收第一窗口: %v", err)
	}
	second := metricStoreTestReport(t, "task-1", "agent-1", 2, time.Unix(95, 0), time.Unix(100, 0))
	second.Snapshot.CollectionEpoch = 8
	second.Snapshot.TotalActions = 0
	second.Snapshot.Actions = nil
	second.Snapshot.Summary = monitor.SnapshotSummary{}
	if _, err := store.Accept(second, "task-1", 5*time.Second, 100*time.Millisecond); err != nil {
		t.Fatalf("新 epoch 的累计清零应被接受: %v", err)
	}
}
