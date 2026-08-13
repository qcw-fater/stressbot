package metrics

import (
	"testing"
	"time"

	"stressbot/monitor"
)

func TestBuildHistoryTrendPointUsesBackendSummary(t *testing.T) {
	f64 := func(value float64) *float64 { return &value }
	summary := monitor.SnapshotSummary{
		SampleCount:         20,
		RTTApdex:            0.505,
		RTTApdexSampleCount: 20,
		RTT:                 monitor.HistogramSnapshot{Count: 20, AvgMs: f64(12), P95Ms: f64(34), P99Ms: f64(56)},
		ListenWait:          monitor.HistogramSnapshot{Count: 2, P99Ms: f64(78)},
		TotalDuration:       monitor.HistogramSnapshot{Count: 20, AvgMs: f64(90), P95Ms: f64(123), P99Ms: f64(456)},
		ClientAvgMs:         7,
		ClientCostCount:     20,
		EncodeAvgMs:         2,
		EncodeSampleCount:   20,
		DecodeAvgMs:         3,
		DecodeSampleCount:   20,
		AvgQPS:              88,
	}
	stress := &StressAggregate{Snapshot: &monitor.CollectorSnapshot{
		TimingDetail: monitor.TimingCodecDetail,
		Summary:      summary,
		Window: &monitor.ReportWindow{
			StartedAt: time.Unix(0, 0),
			EndedAt:   time.Unix(1, 0),
			Summary:   summary,
		},
		// 故意放入冲突的单动作值，证明 Sampler 不再二次聚合。
		Actions: []monitor.ActionSnapshot{{
			RTTApdex:                  1,
			RTTApdexSampleCount:       10,
			RTT:                       monitor.HistogramSnapshot{AvgMs: f64(1), P95Ms: f64(1), P99Ms: f64(1)},
			BuildAvgMs:                4,
			SendAvgMs:                 5,
			DecodeWaitAvgMs:           6,
			DispatchToActionWaitAvgMs: 7,
			AvgQPS:                    1,
		}},
	}}

	point := buildHistoryTrendPoint(time.Unix(1, 0), 10, stress, ClusterSystemSnapshot{})
	if point.RTTApdex == nil || *point.RTTApdex != 0.505 || point.RTTP99Ms == nil || *point.RTTP99Ms != 56 {
		t.Fatalf("history RTT summary = apdex %v p99 %v, want 0.505/56", point.RTTApdex, point.RTTP99Ms)
	}
	if point.TotalDurationP95Ms == nil || *point.TotalDurationP95Ms != 123 || point.ListenWaitP99Ms == nil || *point.ListenWaitP99Ms != 78 {
		t.Fatalf("history latency summary = total p95 %v listen p99 %v, want 123/78", point.TotalDurationP95Ms, point.ListenWaitP99Ms)
	}
	if point.ClientAvgMs == nil || *point.ClientAvgMs != 7 || point.EncodeAvgMs == nil || *point.EncodeAvgMs != 2 || point.DecodeAvgMs == nil || *point.DecodeAvgMs != 3 || point.TotalQPS != 88 {
		t.Fatalf("history diagnostics = client %v encode %v decode %v qps %v", point.ClientAvgMs, point.EncodeAvgMs, point.DecodeAvgMs, point.TotalQPS)
	}
}
