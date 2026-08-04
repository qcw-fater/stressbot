package monitor

import (
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	json "stressbot/utils/jsonx"
)

func metricValue(t *testing.T, value *float64) float64 {
	t.Helper()
	if value == nil {
		t.Fatal("非空延迟分布的展示值不应为空")
	}
	return *value
}

func TestLatencyHistogramEmptySnapshotUsesNullLatencyValues(t *testing.T) {
	snap := newLatencyHistogram().Snapshot()
	if snap.Count != 0 || snap.MinMs != nil || snap.MaxMs != nil || snap.AvgMs != nil ||
		snap.P50Ms != nil || snap.P90Ms != nil || snap.P95Ms != nil || snap.P99Ms != nil {
		t.Fatalf("空分布快照包含伪造延迟: %+v", snap)
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"p99Ms":null`) {
		t.Fatalf("空分布 JSON 应显式输出 null: %s", b)
	}
}

func TestLatencyHistogramDDSketchQuantilesStayAccurateAndBounded(t *testing.T) {
	h := newLatencyHistogram()
	values := make([]time.Duration, 0, 20_004)
	for i := 1; i <= 20_000; i++ {
		values = append(values, time.Duration(i*i)*time.Nanosecond)
	}
	values = append(values, time.Nanosecond, 61*time.Second, 10*time.Minute, time.Hour)
	for _, value := range values {
		h.Record(value)
	}

	snap := h.Snapshot()
	if snap.Count != int64(len(values)) {
		t.Fatalf("count=%d, want %d", snap.Count, len(values))
	}
	if metricValue(t, snap.MinMs) != float64(time.Nanosecond)/float64(time.Millisecond) {
		t.Fatalf("minMs=%g, want 1ns", metricValue(t, snap.MinMs))
	}
	if metricValue(t, snap.MaxMs) != float64(time.Hour)/float64(time.Millisecond) {
		t.Fatalf("maxMs=%g, want 1h", metricValue(t, snap.MaxMs))
	}
	p50, p90 := metricValue(t, snap.P50Ms), metricValue(t, snap.P90Ms)
	p95, p99, maxMs := metricValue(t, snap.P95Ms), metricValue(t, snap.P99Ms), metricValue(t, snap.MaxMs)
	if !(p50 <= p90 && p90 <= p95 && p95 <= p99 && p99 <= maxMs) {
		t.Fatalf("quantile order invalid: p50=%g p90=%g p95=%g p99=%g max=%g", p50, p90, p95, p99, maxMs)
	}

	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for name, got := range map[string]float64{
		"p50": p50,
		"p90": p90,
		"p95": p95,
		"p99": p99,
	} {
		q := map[string]float64{"p50": 0.50, "p90": 0.90, "p95": 0.95, "p99": 0.99}[name]
		want := float64(sorted[int(math.Ceil(q*float64(len(sorted))))-1]) / float64(time.Millisecond)
		relativeError := math.Abs(got-want) / want
		if relativeError > 0.011 {
			t.Errorf("%s relative error=%g, got=%gms want=%gms", name, relativeError, got, want)
		}
	}
	if len(snap.Sketch) == 0 {
		t.Fatal("non-empty histogram must carry DDSketch bytes")
	}
}

func TestLatencyHistogramDDSketchMergeMatchesSingleSketch(t *testing.T) {
	all := newLatencyHistogram()
	left := newLatencyHistogram()
	right := newLatencyHistogram()
	for i := 1; i <= 10_000; i++ {
		value := time.Duration(i*i) * time.Microsecond
		all.Record(value)
		if i%2 == 0 {
			left.Record(value)
		} else {
			right.Record(value)
		}
	}

	merged, err := MergeHistograms([]HistogramSnapshot{left.Snapshot(), right.Snapshot()})
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	want := all.Snapshot()
	if merged.Count != want.Count || merged.SumNs != want.SumNs || metricValue(t, merged.MinMs) != metricValue(t, want.MinMs) || metricValue(t, merged.MaxMs) != metricValue(t, want.MaxMs) {
		t.Fatalf("exact fields differ: merged=%+v want=%+v", merged, want)
	}
	for name, pair := range map[string][2]float64{
		"p50": {metricValue(t, merged.P50Ms), metricValue(t, want.P50Ms)},
		"p90": {metricValue(t, merged.P90Ms), metricValue(t, want.P90Ms)},
		"p95": {metricValue(t, merged.P95Ms), metricValue(t, want.P95Ms)},
		"p99": {metricValue(t, merged.P99Ms), metricValue(t, want.P99Ms)},
	} {
		if relative := math.Abs(pair[0]-pair[1]) / pair[1]; relative > 0.000001 {
			t.Errorf("%s differs after merge: got=%g want=%g relative=%g", name, pair[0], pair[1], relative)
		}
	}
}

func TestMergeHistogramsRejectsMissingSketch(t *testing.T) {
	value := 0.000001
	_, err := MergeHistograms([]HistogramSnapshot{{
		Count: 1, SumNs: 1, MinMs: &value, MaxMs: &value, AvgMs: &value,
		P50Ms: &value, P90Ms: &value, P95Ms: &value, P99Ms: &value,
	}})
	if err == nil {
		t.Fatal("missing sketch must be rejected")
	}
}
