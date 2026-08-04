package monitor

import (
	"testing"
	"time"
)

// TestRTTApdexCountsFailedRequestsAsFrustrated 验证「无响应帧的请求进分母记 frustrated」。
//
// 回归的是一个方向性失真：超时请求算不出 WireRTT，旧实现于是让它完全不进 RTT Apdex，
// 结果服务端越是超时、最慢的样本越是集体缺席，Apdex 反而越高。
func TestRTTApdexCountsFailedRequestsAsFrustrated(t *testing.T) {
	c := newMonitorTestCollector()

	c.RecordActionStart("probe")
	c.RecordAction("probe", ResultSuccess,
		ActionTiming{Requests: []RequestTiming{{WireRTT: 10 * time.Millisecond}}},
		10*time.Millisecond, 0, 0, nil)

	c.RecordActionStart("probe")
	c.RecordAction("probe", ResultTimeout,
		ActionTiming{FailedRequests: 1},
		60*time.Second, 0, 0, nil)

	a := findMonitorTestAction(t, c.Snapshot(nil, 0), "probe")

	// 一个 satisfied + 一个 frustrated → 0.5。旧实现会给出 1.0。
	if a.RTTApdex != 0.5 {
		t.Fatalf("RTTApdex = %v, want 0.5（1 satisfied + 1 frustrated）", a.RTTApdex)
	}
	// 直方图必须保持干净：超时没有时延值，不能进 P50/P99 的分母。
	if a.RTTSampleCount != 1 {
		t.Fatalf("RTTSampleCount = %d, want 1（超时不进直方图）", a.RTTSampleCount)
	}
	if a.RTTFailedCount != 1 {
		t.Fatalf("RTTFailedCount = %d, want 1", a.RTTFailedCount)
	}
	if a.RTT.Count != 1 {
		t.Fatalf("RTT.Count = %d, want 1（超时不进直方图）", a.RTT.Count)
	}
	if a.Kind != ActionKindNetworked {
		t.Fatalf("Kind = %q, want %q", a.Kind, ActionKindNetworked)
	}
}

// TestActionKindLocalWhenNoRoundTrip 无往返语义的动作标为 local 且不给 RTT Apdex 分。
func TestActionKindLocalWhenNoRoundTrip(t *testing.T) {
	c := newMonitorTestCollector()

	c.RecordActionStart("setState")
	c.RecordAction("setState", ResultSuccess, ActionTiming{}, time.Millisecond, 0, 0, nil)

	a := findMonitorTestAction(t, c.Snapshot(nil, 0), "setState")
	if a.Kind != ActionKindLocal {
		t.Fatalf("Kind = %q, want %q", a.Kind, ActionKindLocal)
	}
	if a.RTTApdex != 0 {
		t.Fatalf("RTTApdex = %v, want 0（无往返语义不打分）", a.RTTApdex)
	}
}

// TestActionKindNetworkedEvenIfAllRequestsFailed 请求全失败的动作仍属 networked：
// 它确实在和服务端交互，只是全都没回来——这正是 Apdex 该反映的最坏情况，
// 不能因为「一个成功样本都没有」就被当成本地动作排除在外。
func TestActionKindNetworkedEvenIfAllRequestsFailed(t *testing.T) {
	c := newMonitorTestCollector()

	c.RecordActionStart("dead")
	c.RecordAction("dead", ResultTimeout, ActionTiming{FailedRequests: 1}, time.Second, 0, 0, nil)

	a := findMonitorTestAction(t, c.Snapshot(nil, 0), "dead")
	if a.Kind != ActionKindNetworked {
		t.Fatalf("Kind = %q, want %q", a.Kind, ActionKindNetworked)
	}
	if a.RTTApdex != 0 {
		t.Fatalf("RTTApdex = %v, want 0（全 frustrated）", a.RTTApdex)
	}
}

// TestListenWaitIsSeparateFromRTTAndUnscored 监听等待自成一列：出分布、不打 Apdex、不污染 RTT。
func TestListenWaitIsSeparateFromRTTAndUnscored(t *testing.T) {
	c := newMonitorTestCollector()

	c.RecordActionStart("MatchSucceed")
	c.RecordAction("MatchSucceed", ResultSuccess,
		ActionTiming{ListenWaits: []time.Duration{3 * time.Second}},
		3*time.Second, 0, 0, nil)

	a := findMonitorTestAction(t, c.Snapshot(nil, 0), "MatchSucceed")

	if a.Kind != ActionKindListen {
		t.Fatalf("Kind = %q, want %q", a.Kind, ActionKindListen)
	}
	if a.ListenWaitSampleCount != 1 || a.ListenWait.Count != 1 {
		t.Fatalf("等待时长样本 = %d/%d, want 1/1", a.ListenWaitSampleCount, a.ListenWait.Count)
	}
	// 等待 3s 远超 apdexT，若误入 Apdex 会得 0 分并把整体拉低——监听类根本不该有分。
	if a.RTTApdex != 0 {
		t.Fatalf("RTTApdex = %v, want 0（监听类不打分）", a.RTTApdex)
	}
	if a.RTT.Count != 0 || a.RTTSampleCount != 0 {
		t.Fatalf("监听等待污染了 RTT 直方图: count=%d samples=%d", a.RTT.Count, a.RTTSampleCount)
	}
}

// TestListenReadyAndTimeoutStayOutOfDistribution 「已就绪」与超时都不进等待时长分布。
//
// 前者记 0ms 会把 P50 拉向 0（测量起点选错被伪装成服务端快），
// 后者记超时上限会把 P99 顶死（掩盖真实分布）。两者都只计次。
func TestListenReadyAndTimeoutStayOutOfDistribution(t *testing.T) {
	c := newMonitorTestCollector()

	c.RecordActionStart("StartGame")
	c.RecordAction("StartGame", ResultSuccess,
		ActionTiming{ListenWaits: []time.Duration{2 * time.Second}, ListenReady: 3},
		2*time.Second, 0, 0, nil)
	c.RecordActionStart("StartGame")
	c.RecordAction("StartGame", ResultTimeout,
		ActionTiming{ListenTimeouts: 1},
		180*time.Second, 0, 0, nil)

	a := findMonitorTestAction(t, c.Snapshot(nil, 0), "StartGame")

	if a.ListenWait.Count != 1 {
		t.Fatalf("等待时长分布应只含 1 个可测样本，实际 %d", a.ListenWait.Count)
	}
	if a.ListenReadyCount != 3 {
		t.Fatalf("ListenReadyCount = %d, want 3", a.ListenReadyCount)
	}
	if a.ListenTimeoutCount != 1 {
		t.Fatalf("ListenTimeoutCount = %d, want 1", a.ListenTimeoutCount)
	}
	// 1 可测 + 3 已就绪 + 1 超时 = 5
	if want := 1.0 / 5.0; a.ListenTimeoutRate != want {
		t.Fatalf("ListenTimeoutRate = %v, want %v", a.ListenTimeoutRate, want)
	}
}

// TestActionKindPrefersStrongestSemantics 一个动作兼有多种行为时按最强语义归类，
// 保证主指标不被弱语义盖掉（先请求再监听的 Lua 脚本很常见）。
func TestActionKindPrefersStrongestSemantics(t *testing.T) {
	cases := []struct {
		name      string
		timing    ActionTiming
		sendBytes int
		want      ActionKind
	}{
		{"往返压过监听", ActionTiming{
			Requests:    []RequestTiming{{WireRTT: time.Millisecond}},
			ListenWaits: []time.Duration{time.Second},
		}, 10, ActionKindNetworked},
		{"监听压过发送", ActionTiming{ListenWaits: []time.Duration{time.Second}}, 10, ActionKindListen},
		{"只发不等即发送类", ActionTiming{}, 10, ActionKindSend},
		{"无网络行为即本地类", ActionTiming{}, 0, ActionKindLocal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newMonitorTestCollector()
			c.RecordActionStart("act")
			c.RecordAction("act", ResultSuccess, tc.timing, time.Millisecond, tc.sendBytes, 0, nil)
			a := findMonitorTestAction(t, c.Snapshot(nil, 0), "act")
			if a.Kind != tc.want {
				t.Fatalf("Kind = %q, want %q", a.Kind, tc.want)
			}
		})
	}
}

// TestMergeSnapshotsMergesListenMetrics 分布式合并保留监听类分列与分类。
func TestMergeSnapshotsMergesListenMetrics(t *testing.T) {
	mk := func(samples, ready, timeouts int64) *CollectorSnapshot {
		return &CollectorSnapshot{
			ApdexT: 100,
			Actions: []ActionSnapshot{{
				Name:                  "MatchSucceed",
				ListenWaitSampleCount: samples,
				ListenReadyCount:      ready,
				ListenTimeoutCount:    timeouts,
			}},
		}
	}

	merged, err := MergeSnapshots([]*CollectorSnapshot{mk(3, 1, 1), mk(3, 1, 1)})
	if err != nil {
		t.Fatalf("MergeSnapshots() error = %v", err)
	}
	a := findMonitorTestAction(t, merged, "MatchSucceed")

	if a.Kind != ActionKindListen {
		t.Fatalf("Kind = %q, want %q", a.Kind, ActionKindListen)
	}
	if a.ListenWaitSampleCount != 6 || a.ListenReadyCount != 2 || a.ListenTimeoutCount != 2 {
		t.Fatalf("合并计数 = %d/%d/%d, want 6/2/2",
			a.ListenWaitSampleCount, a.ListenReadyCount, a.ListenTimeoutCount)
	}
	if want := 2.0 / 10.0; a.ListenTimeoutRate != want {
		t.Fatalf("ListenTimeoutRate = %v, want %v", a.ListenTimeoutRate, want)
	}
}

// TestMergeSnapshotsRTTApdexSameDenominator 分布式合并与单机同口径：
// 分母都是「有响应帧的样本 + 无响应帧的失败请求」。
func TestMergeSnapshotsRTTApdexSameDenominator(t *testing.T) {
	mk := func(satisfied, rttSamples, failed int64) *CollectorSnapshot {
		return &CollectorSnapshot{
			ApdexT: 100,
			Actions: []ActionSnapshot{{
				Name:           "probe",
				RTTSampleCount: rttSamples,
				RTTFailedCount: failed,
				ApdexSatisfied: satisfied,
			}},
		}
	}

	merged, err := MergeSnapshots([]*CollectorSnapshot{mk(1, 1, 1), mk(1, 1, 1)})
	if err != nil {
		t.Fatalf("MergeSnapshots() error = %v", err)
	}
	a := findMonitorTestAction(t, merged, "probe")

	if a.RTTFailedCount != 2 {
		t.Fatalf("RTTFailedCount = %d, want 2", a.RTTFailedCount)
	}
	// 2 satisfied + 2 frustrated → 0.5，与单机模式一致。
	if a.RTTApdex != 0.5 {
		t.Fatalf("RTTApdex = %v, want 0.5", a.RTTApdex)
	}
	if a.Kind != ActionKindNetworked {
		t.Fatalf("Kind = %q, want %q", a.Kind, ActionKindNetworked)
	}
	if a.RTTApdexSampleCount != 4 {
		t.Fatalf("RTTApdexSampleCount = %d, want 4", a.RTTApdexSampleCount)
	}
}

// TestMergeSnapshotsBuildsOneCrossActionSummary 验证跨动作总指标仍使用后端原始分母和
// 合并后的直方图。前端不能从单动作分数或单动作百分位再次加权得到这些值。
func TestMergeSnapshotsBuildsOneCrossActionSummary(t *testing.T) {
	rttA := histogramForMonitorSummaryTest(1, 1)
	rttB := histogramForMonitorSummaryTest(100, 15)
	totalA := histogramForMonitorSummaryTest(100, 2)
	totalB := histogramForMonitorSummaryTest(100, 10)

	merged, err := MergeSnapshots([]*CollectorSnapshot{
		{
			ApdexT:       100,
			TimingDetail: TimingFullDetail,
			Actions: []ActionSnapshot{{
				Name:                     "mostly-failed",
				SampleCount:              100,
				SuccessCount:             1,
				FailureCount:             99,
				RTTSampleCount:           1,
				RTTFailedCount:           99,
				ApdexSatisfied:           1,
				RTT:                      rttA,
				TotalDuration:            totalA,
				TotalDurationSampleCount: 100,
			}},
		},
		{
			ApdexT:       100,
			TimingDetail: TimingCodecDetail,
			Actions: []ActionSnapshot{{
				Name:                     "healthy",
				SampleCount:              100,
				SuccessCount:             100,
				RTTSampleCount:           100,
				ApdexSatisfied:           100,
				RTT:                      rttB,
				TotalDuration:            totalB,
				TotalDurationSampleCount: 100,
			}},
		},
	})
	if err != nil {
		t.Fatalf("MergeSnapshots() error = %v", err)
	}

	if merged.TimingDetail != TimingCodecDetail {
		t.Fatalf("TimingDetail = %q, want %q", merged.TimingDetail, TimingCodecDetail)
	}
	if merged.Summary.RTTApdexSampleCount != 200 {
		t.Fatalf("summary denominator = %d, want 200", merged.Summary.RTTApdexSampleCount)
	}
	if merged.Summary.RTTApdex != 0.505 {
		t.Fatalf("summary RTT Apdex = %v, want 0.505", merged.Summary.RTTApdex)
	}
	wantRTT, err := MergeHistograms([]HistogramSnapshot{rttA, rttB})
	if err != nil {
		t.Fatalf("merge RTT: %v", err)
	}
	if metricValue(t, merged.Summary.RTT.P99Ms) != metricValue(t, wantRTT.P99Ms) {
		t.Fatalf("summary RTT p99 = %v, want merged histogram %v", merged.Summary.RTT.P99Ms, wantRTT.P99Ms)
	}
	wantTotal, err := MergeHistograms([]HistogramSnapshot{totalA, totalB})
	if err != nil {
		t.Fatalf("merge total duration: %v", err)
	}
	if metricValue(t, merged.Summary.TotalDuration.P95Ms) != metricValue(t, wantTotal.P95Ms) {
		t.Fatalf("summary total p95 = %v, want merged histogram %v", merged.Summary.TotalDuration.P95Ms, wantTotal.P95Ms)
	}
}

func histogramForMonitorSummaryTest(count int64, bucket int) HistogramSnapshot {
	h := newLatencyHistogram()
	value := time.Duration(bucket+1) * time.Millisecond
	for range count {
		if err := h.Record(value); err != nil {
			panic(err)
		}
	}
	return h.Snapshot()
}
