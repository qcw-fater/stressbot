package monitor

import (
	"strings"
	"testing"
	"time"

	json "stressbot/internal/jsonx"
)

func TestTakeReportSnapshotPartitionsActionsExactlyOnce(t *testing.T) {
	c := newMonitorTestCollector()
	start := time.Unix(100, 0)
	for range 100 {
		c.RecordActionStart("login")
		c.RecordAction("login", ResultSuccess, ActionTiming{}, time.Millisecond, 10, 20, nil)
	}

	first := c.TakeReportSnapshot(ReportMeta{
		Sequence:                1,
		StartedAt:               start,
		EndedAt:                 start.Add(5 * time.Second),
		ExpectedIntervalSeconds: 5,
	})
	if first.Window == nil {
		t.Fatal("report snapshot missing window")
	}
	if got := findMonitorTestAction(t, &CollectorSnapshot{Actions: first.Window.Actions}, "login").SampleCount; got != 100 {
		t.Fatalf("first window sampleCount=%d, want 100", got)
	}
	if got := findMonitorTestAction(t, first, "login").SampleCount; got != 100 {
		t.Fatalf("first cumulative sampleCount=%d, want 100", got)
	}

	// Read-only snapshots must not consume the active interval.
	_ = c.Snapshot(nil, 0)
	for range 25 {
		c.RecordActionStart("login")
		c.RecordAction("login", ResultSuccess, ActionTiming{}, 2*time.Millisecond, 30, 40, nil)
	}
	_ = c.Snapshot(nil, 0)

	second := c.TakeReportSnapshot(ReportMeta{
		Sequence:                2,
		StartedAt:               start.Add(5 * time.Second),
		EndedAt:                 start.Add(10 * time.Second),
		ExpectedIntervalSeconds: 5,
	})
	windowAction := findMonitorTestAction(t, &CollectorSnapshot{Actions: second.Window.Actions}, "login")
	if windowAction.SampleCount != 25 || windowAction.TotalSendBytes != 750 || windowAction.TotalRecvBytes != 1000 {
		t.Fatalf("second window differs: samples=%d send=%d recv=%d", windowAction.SampleCount, windowAction.TotalSendBytes, windowAction.TotalRecvBytes)
	}
	if got := findMonitorTestAction(t, second, "login").SampleCount; got != 125 {
		t.Fatalf("second cumulative sampleCount=%d, want 125", got)
	}
	if second.Window.Summary.AvgQPS != 5 {
		t.Fatalf("window QPS=%g, want 5", second.Window.Summary.AvgQPS)
	}
	if second.Window.Sequence != 2 || second.Window.DurationSeconds != 5 {
		t.Fatalf("window meta=%+v", second.Window)
	}
}

func TestCollectorSnapshotPublicCopyRemovesSketchBytes(t *testing.T) {
	c := newMonitorTestCollector()
	c.RecordActionStart("login")
	c.RecordAction("login", ResultSuccess, ActionTiming{Requests: []RequestTiming{{WireRTT: time.Millisecond}}}, 2*time.Millisecond, 0, 0, nil)
	start := time.Unix(300, 0)
	snap := c.TakeReportSnapshot(ReportMeta{Sequence: 1, StartedAt: start, EndedAt: start.Add(time.Second), ExpectedIntervalSeconds: 1})
	if len(snap.Actions[0].RTT.Sketch) == 0 || len(snap.Window.Actions[0].RTT.Sketch) == 0 {
		t.Fatal("internal report must retain sketch bytes")
	}
	b, err := json.Marshal(snap.PublicCopy())
	if err != nil {
		t.Fatal(err)
	}
	for _, internalField := range []string{"sketch", "sumNs", "clientCostSumNs", "timeoutTotalNs", "encodeCostSumNs"} {
		if strings.Contains(string(b), internalField) {
			t.Fatalf("public JSON leaked internal field %q: %s", internalField, b)
		}
	}
}

func TestMergeSnapshotsRejectsMalformedDistribution(t *testing.T) {
	_, err := MergeSnapshots([]*CollectorSnapshot{{
		Actions: []ActionSnapshot{{
			Name:           "login",
			SampleCount:    1,
			SuccessCount:   1,
			RTTSampleCount: 1,
			RTT: HistogramSnapshot{
				Count: 1,
				SumNs: int64(time.Millisecond),
				MinMs: float64Pointer(1),
				MaxMs: float64Pointer(1),
				AvgMs: float64Pointer(1),
				P50Ms: float64Pointer(1),
				P90Ms: float64Pointer(1),
				P95Ms: float64Pointer(1),
				P99Ms: float64Pointer(1),
			},
		}},
	}})
	if err == nil {
		t.Fatal("缺少 DDSketch 的快照合并应返回错误")
	}
}

func TestMergeSnapshotsUsesExactNanosecondDurationSums(t *testing.T) {
	makeSnapshot := func(timeout, encode time.Duration) *CollectorSnapshot {
		collector := newMonitorTestCollector()
		collector.RecordActionStart("login")
		collector.RecordAction("login", ResultTimeout, ActionTiming{
			Client: ClientTiming{EncodeCost: encode, Observed: StageEncode},
		}, timeout, 0, 0, nil)
		return collector.Snapshot(nil, 0)
	}

	merged, err := MergeSnapshots([]*CollectorSnapshot{
		makeSnapshot(1500*time.Microsecond, 1001*time.Nanosecond),
		makeSnapshot(2500*time.Microsecond, 2002*time.Nanosecond),
	})
	if err != nil {
		t.Fatalf("合并快照: %v", err)
	}
	action := findMonitorTestAction(t, merged, "login")
	if action.TimeoutAvgMs != 2 {
		t.Fatalf("超时均值 = %.9fms, want 2ms", action.TimeoutAvgMs)
	}
	if action.EncodeAvgMs != 0.0015015 {
		t.Fatalf("编码均值 = %.9fms, want 0.0015015ms", action.EncodeAvgMs)
	}
}

func TestCollectorResetAdvancesCollectionEpoch(t *testing.T) {
	collector := newMonitorTestCollector()
	first := collector.TakeReportSnapshot(ReportMeta{
		Sequence: 1, StartedAt: time.Unix(1, 0), EndedAt: time.Unix(2, 0), ExpectedIntervalSeconds: 1,
	})
	collector.Reset()
	second := collector.TakeReportSnapshot(ReportMeta{
		Sequence: 2, StartedAt: time.Unix(2, 0), EndedAt: time.Unix(3, 0), ExpectedIntervalSeconds: 1,
	})
	if second.CollectionEpoch != first.CollectionEpoch+1 {
		t.Fatalf("collection epoch = %d -> %d, want +1", first.CollectionEpoch, second.CollectionEpoch)
	}
}

func TestTakeReportSnapshotKeepsInvalidSamplesPerWindow(t *testing.T) {
	c := newMonitorTestCollector()
	c.RecordActionStart("bad")
	c.RecordAction("bad", ResultSuccess, ActionTiming{}, -time.Nanosecond, 0, 0, nil)
	start := time.Unix(200, 0)
	first := c.TakeReportSnapshot(ReportMeta{Sequence: 1, StartedAt: start, EndedAt: start.Add(time.Second), ExpectedIntervalSeconds: 1})
	second := c.TakeReportSnapshot(ReportMeta{Sequence: 2, StartedAt: start.Add(time.Second), EndedAt: start.Add(2 * time.Second), ExpectedIntervalSeconds: 1})
	if first.InvalidMetricSamples != 1 || first.Window.InvalidMetricSamples != 1 {
		t.Fatalf("first invalid counts cumulative=%d window=%d", first.InvalidMetricSamples, first.Window.InvalidMetricSamples)
	}
	if second.InvalidMetricSamples != 1 || second.Window.InvalidMetricSamples != 0 {
		t.Fatalf("second invalid counts cumulative=%d window=%d", second.InvalidMetricSamples, second.Window.InvalidMetricSamples)
	}
}
