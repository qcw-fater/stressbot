package metrics

import (
	"testing"
	"time"

	"stressbot/admin/agent"
	"stressbot/monitor"
)

func aggregatorTestReport(t *testing.T, agentID string, sequence uint64, start, end time.Time, count int, latency time.Duration) StressReport {
	t.Helper()
	collector := monitor.NewCollector(monitor.CollectorConfig{ApdexThresholdMs: 100, TimingDetail: "rtt"})
	for range count {
		collector.RecordActionStart("login")
		collector.RecordAction("login", monitor.ResultSuccess, monitor.ActionTiming{
			Requests: []monitor.RequestTiming{{WireRTT: latency, Observed: monitor.StageRTT}},
		}, latency, 10, 20, nil)
	}
	return StressReport{
		AgentID: agentID,
		TaskID:  "task-1",
		Snapshot: collector.TakeReportSnapshot(monitor.ReportMeta{
			Sequence:                sequence,
			StartedAt:               start,
			EndedAt:                 end,
			ExpectedIntervalSeconds: 5,
		}),
	}
}

func TestAggregatorSeparatesCumulativeFactsFromFreshLiveRates(t *testing.T) {
	clock := &metricStoreTestClock{}
	store := NewWindowStore(clock.Now)
	registry := newTestAgentRegistry()
	for _, id := range []string{"a", "b"} {
		if err := registry.Register(&Node{ID: id, Status: Busy, CurrentTaskID: "task-1"}); err != nil {
			t.Fatalf("register agent %s: %v", id, err)
		}
	}

	clock.Set(time.Unix(25, 0))
	if _, err := store.Accept(
		aggregatorTestReport(t, "b", 1, time.Unix(20, 0), time.Unix(25, 0), 40, 2*time.Second),
		"task-1", 5*time.Second, 100*time.Millisecond,
	); err != nil {
		t.Fatalf("接收节点 b 窗口: %v", err)
	}
	clock.Set(time.Unix(95, 0))
	if _, err := store.Accept(
		aggregatorTestReport(t, "a", 1, time.Unix(90, 0), time.Unix(95, 0), 100, 10*time.Millisecond),
		"task-1", 5*time.Second, 100*time.Millisecond,
	); err != nil {
		t.Fatalf("接收节点 a 窗口: %v", err)
	}

	clock.Set(time.Unix(100, 0))
	aggregator := NewAggregator(registry, store, clock.Now)
	got, err := aggregator.AggregateStress("task-1")
	if err != nil {
		t.Fatalf("AggregateStress() error = %v", err)
	}
	if got.Snapshot.Summary.SampleCount != 140 {
		t.Fatalf("累计样本 = %d, want 140", got.Snapshot.Summary.SampleCount)
	}
	if got.Snapshot.Window == nil {
		t.Fatal("缺少实时聚合窗口")
	}
	if got.Snapshot.Window.Summary.SampleCount != 100 {
		t.Fatalf("实时样本 = %d, want 100", got.Snapshot.Window.Summary.SampleCount)
	}
	if got.Snapshot.Window.Summary.AvgQPS != 20 {
		t.Fatalf("实时 QPS = %v, want 20", got.Snapshot.Window.Summary.AvgQPS)
	}
	if got.FreshAgents != 1 || got.StaleAgents != 1 {
		t.Fatalf("新鲜/过期节点 = %d/%d, want 1/1", got.FreshAgents, got.StaleAgents)
	}
	if got.Snapshot.Summary.RTT.P99Ms == nil || *got.Snapshot.Summary.RTT.P99Ms < 1000 {
		t.Fatalf("累计 P99 = %vms, 应保留过期节点的慢样本", got.Snapshot.Summary.RTT.P99Ms)
	}
}

func TestAggregateSystemUsesAssignedScopeAndFreshAdminReceiptTimes(t *testing.T) {
	now := time.Date(2026, 8, 4, 11, 0, 0, 0, time.UTC)
	registry := newTestAgentRegistry(
		systemAgentNode("a", "node-a", 4, "5s", now.Add(-10*time.Second), systemSnapshotValues(20, 100, 50, 10, 200, 5, 1, 2)),
		systemAgentNode("b", "node-b", 8, "5s", now.Add(-16*time.Second), systemSnapshotValues(90, 100, 90, 80, 900, 90, 9, 9)),
		systemAgentNode("outside", "outside", 64, "5s", now, systemSnapshotValues(99, 1_000, 999, 99, 9_999, 999, 99, 99)),
	)
	aggregator := NewAggregator(registry, nil, func() time.Time { return now })

	got := aggregator.AggregateSystem([]string{"a", "b", "missing"})
	if got.AgentCount != 3 || got.OnlineCount != 2 || got.OfflineCount != 1 {
		t.Fatalf("scope counts = agents:%d online:%d offline:%d, want 3/2/1", got.AgentCount, got.OnlineCount, got.OfflineCount)
	}
	if got.ReportingAgents != 1 || got.StaleAgents != 1 || got.MissingAgents != 1 {
		t.Fatalf("resource coverage = reporting:%d stale:%d missing:%d, want 1/1/1", got.ReportingAgents, got.StaleAgents, got.MissingAgents)
	}
	if got.CoverageRatio != 1.0/3.0 {
		t.Fatalf("coverage = %v, want 1/3", got.CoverageRatio)
	}
	assertAdminFloatPointer(t, "average host CPU", got.AvgHostCPUPercent, 20)
	assertAdminFloatPointer(t, "maximum host CPU", got.MaxHostCPUPercent, 20)
	if got.HotHostCPUAgentID != "a" || got.HotHostCPUAgentName != "node-a" {
		t.Fatalf("host CPU hot node = %q/%q, want a/node-a", got.HotHostCPUAgentID, got.HotHostCPUAgentName)
	}
	assertAdminFloatPointer(t, "average host memory", got.AvgHostMemPercent, 50)
	assertAdminUint64Pointer(t, "total host memory", got.TotalHostMemBytes, 100)
	assertAdminUint64Pointer(t, "used host memory", got.UsedHostMemBytes, 50)
}

func TestAggregateSystemAggregatesEachValidFieldWithExplicitCoverage(t *testing.T) {
	now := time.Date(2026, 8, 4, 11, 0, 0, 0, time.UTC)
	a := systemSnapshotValues(20, 100, 50, 10, 100, 5, 100, 0)
	a.HostNetRecvBytesPerSec = nil
	b := systemSnapshotValues(60, 300, 240, 30, 300, 9, 0, 200)
	b.HostNetSendBytesPerSec = nil
	registry := newTestAgentRegistry(
		systemAgentNode("a", "node-a", 4, "10s", now.Add(-5*time.Second), a),
		systemAgentNode("b", "node-b", 12, "10s", now.Add(-5*time.Second), b),
	)
	aggregator := NewAggregator(registry, nil, func() time.Time { return now })

	got := aggregator.AggregateSystem([]string{"a", "b"})
	if got.ReportingAgents != 2 || got.HostCPUReportingAgents != 2 || got.HostMemoryReportingAgents != 2 {
		t.Fatalf("reporting counts = all:%d cpu:%d memory:%d, want 2/2/2", got.ReportingAgents, got.HostCPUReportingAgents, got.HostMemoryReportingAgents)
	}
	assertAdminFloatPointer(t, "core-weighted host CPU", got.AvgHostCPUPercent, 50)
	assertAdminFloatPointer(t, "memory-weighted host memory", got.AvgHostMemPercent, 72.5)
	if got.HotHostCPUAgentID != "b" || got.HotHostMemAgentID != "b" {
		t.Fatalf("host hot nodes = cpu:%q memory:%q, want b/b", got.HotHostCPUAgentID, got.HotHostMemAgentID)
	}
	assertAdminFloatPointer(t, "core-weighted process CPU", got.AvgProcessCPUPercent, 25)
	assertAdminFloatPointer(t, "maximum process CPU", got.MaxProcessCPUPercent, 30)
	assertAdminUint64Pointer(t, "total process RSS", got.TotalProcessRSSBytes, 400)
	assertAdminUint64Pointer(t, "maximum process RSS", got.MaxProcessRSSBytes, 300)
	assertAdminInt32Pointer(t, "maximum process handles", got.MaxProcessFDs, 9)
	if got.HotProcessCPUAgentID != "b" || got.HotProcessRSSAgentID != "b" || got.HotProcessFDsAgentID != "b" {
		t.Fatalf("process hot nodes = cpu:%q rss:%q handles:%q, want b/b/b", got.HotProcessCPUAgentID, got.HotProcessRSSAgentID, got.HotProcessFDsAgentID)
	}
	if got.HostNetSendReportingAgents != 1 || got.HostNetRecvReportingAgents != 1 {
		t.Fatalf("network reporting = send:%d recv:%d, want 1/1", got.HostNetSendReportingAgents, got.HostNetRecvReportingAgents)
	}
	assertAdminFloatPointer(t, "host send bytes/sec", got.TotalHostNetSendBytesPerSec, 100)
	assertAdminFloatPointer(t, "host receive bytes/sec", got.TotalHostNetRecvBytesPerSec, 200)
}

func TestAggregateSystemReturnsAnEmptyAgentArrayForAnEmptyScope(t *testing.T) {
	registry := newTestAgentRegistry()
	aggregator := NewAggregator(registry, nil, time.Now)

	got := aggregator.AggregateSystem(nil)
	if got.Agents == nil || len(got.Agents) != 0 {
		t.Fatalf("agents = %#v, want a non-nil empty slice", got.Agents)
	}
}

func systemAgentNode(id, name string, cpus int, interval string, receivedAt time.Time, snapshot SystemSnapshot) *Node {
	return &Node{
		ID:              id,
		Name:            name,
		Status:          Busy,
		SystemInterval:  interval,
		StaticInfo:      agent.StaticInfo{NumCPU: cpus},
		LatestSystem:    &snapshot,
		SystemUpdatedAt: receivedAt,
	}
}

func systemSnapshotValues(hostCPU float64, totalMemory, usedMemory uint64, processCPU float64, rss uint64, fds int32, send, recv float64) SystemSnapshot {
	memoryPercent := float64(usedMemory) / float64(totalMemory) * 100
	threads := int32(3)
	return SystemSnapshot{
		HostCPUPercent:         pointer(hostCPU),
		HostMemTotalBytes:      pointer(totalMemory),
		HostMemUsedBytes:       pointer(usedMemory),
		HostMemPercent:         pointer(memoryPercent),
		HostNetSendBytesPerSec: pointer(send),
		HostNetRecvBytesPerSec: pointer(recv),
		ProcessCPUPercent:      pointer(processCPU),
		ProcessRSSBytes:        pointer(rss),
		ProcessHeapBytes:       50,
		ProcessGoroutines:      7,
		ProcessThreads:         &threads,
		ProcessFDs:             pointer(fds),
	}
}

func assertAdminFloatPointer(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

func assertAdminUint64Pointer(t *testing.T, name string, got *uint64, want uint64) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s = %v, want %d", name, got, want)
	}
}

func assertAdminInt32Pointer(t *testing.T, name string, got *int32, want int32) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s = %v, want %d", name, got, want)
	}
}
