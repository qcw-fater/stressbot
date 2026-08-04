package monitor

import (
	"errors"
	"testing"
	"time"
)

type monitorTestCodedError struct {
	code   uint64
	detail string
}

func (e monitorTestCodedError) Error() string       { return e.detail }
func (e monitorTestCodedError) ErrorCode() uint64   { return e.code }
func (e monitorTestCodedError) ErrorDetail() string { return e.detail }

func newMonitorTestCollector() *MetricsCollector {
	c := &MetricsCollector{enabled: true}
	c.startTime.Store(time.Now().UnixNano())
	c.apdexT.Store(100)
	return c
}

func findMonitorTestAction(t *testing.T, snap *CollectorSnapshot, name string) ActionSnapshot {
	t.Helper()
	for _, action := range snap.Actions {
		if action.Name == name {
			return action
		}
	}
	t.Fatalf("未找到 action 指标：%s", name)
	return ActionSnapshot{}
}

func TestCollectorConnectionLifecycleCounters(t *testing.T) {
	c := newMonitorTestCollector()
	c.ConnEstablished()
	c.ConnClosed()
	c.ConnEstablished()
	c.ConnDropped()
	c.ConnClosed()

	got := c.Snapshot(nil, 0).Connections
	if got.Active != 0 || got.Established != 2 || got.Closed != 2 || got.Dropped != 1 {
		t.Fatalf("连接计数 active/established/closed/dropped = %d/%d/%d/%d, want 0/2/2/1",
			got.Active, got.Established, got.Closed, got.Dropped)
	}
}

func TestRecordActionStartAndRecordActionPairExecuting(t *testing.T) {
	c := newMonitorTestCollector()

	c.RecordActionStart("a")
	c.RecordAction("a", ResultSuccess, ActionTiming{}, 10*time.Millisecond, 12, 34, nil)

	snap := c.Snapshot(nil, 0)
	action := findMonitorTestAction(t, snap, "a")
	if snap.TotalActions != 1 {
		t.Fatalf("TotalActions=%d，期望 1", snap.TotalActions)
	}
	if action.Executing != 0 {
		t.Fatalf("Executing=%d，期望 0", action.Executing)
	}
	if action.SuccessCount != 1 || action.SampleCount != 1 {
		t.Fatalf("成功样本计数错误：success=%d sample=%d", action.SuccessCount, action.SampleCount)
	}
}

func TestRecordActionCanceledDoesNotRecordErrorDistribution(t *testing.T) {
	c := newMonitorTestCollector()

	c.RecordActionStart("cancel")
	c.RecordAction("cancel", ResultCanceled, ActionTiming{}, 5*time.Millisecond, 1, 2, monitorTestCodedError{code: 9, detail: "canceled"})

	snap := c.Snapshot(nil, 0)
	action := findMonitorTestAction(t, snap, "cancel")
	if snap.TotalActions != 0 {
		t.Fatalf("取消不应进入 TotalActions，实际 %d", snap.TotalActions)
	}
	if action.Executing != 0 {
		t.Fatalf("Executing=%d，期望 0", action.Executing)
	}
	if action.CanceledCount != 1 || action.FailureCount != 0 || action.TimeoutCount != 0 {
		t.Fatalf("取消样本分类错误：canceled=%d failure=%d timeout=%d",
			action.CanceledCount, action.FailureCount, action.TimeoutCount)
	}
	if len(action.Errors) != 0 {
		t.Fatalf("取消样本不应记录错误分布，实际 %+v", action.Errors)
	}
}

func TestRecordActionRealCanceledKeepsTimingSamples(t *testing.T) {
	c := newMonitorTestCollector()
	timing := ActionTiming{Client: ClientTiming{BuildCost: 2 * time.Millisecond}}

	c.RecordActionStart("real-cancel")
	c.RecordAction("real-cancel", ResultCanceled, timing, 5*time.Millisecond, 0, 0, nil)

	action := findMonitorTestAction(t, c.Snapshot(nil, 0), "real-cancel")
	if action.CanceledCount != 1 {
		t.Fatalf("CanceledCount = %d, want 1", action.CanceledCount)
	}
	if action.TotalDurationSampleCount != 1 || action.ClientCostCount != 1 {
		t.Fatalf("real canceled timing counts: total=%d client=%d, want 1/1",
			action.TotalDurationSampleCount, action.ClientCostCount)
	}
	if action.BuildAvgMs != 2 {
		t.Fatalf("BuildAvgMs = %v, want 2", action.BuildAvgMs)
	}
}

func TestRecordActionSyntheticCanceledExcludesTimingSamples(t *testing.T) {
	c := newMonitorTestCollector()

	c.RecordPendingCanceled("synthetic-cancel")

	action := findMonitorTestAction(t, c.Snapshot(nil, 0), "synthetic-cancel")
	if action.CanceledCount != 1 {
		t.Fatalf("CanceledCount = %d, want 1", action.CanceledCount)
	}
	if action.TotalDurationSampleCount != 0 || action.ClientCostCount != 0 {
		t.Fatalf("synthetic canceled timing counts: total=%d client=%d, want 0/0",
			action.TotalDurationSampleCount, action.ClientCostCount)
	}
	if action.TotalDuration.Count != 0 {
		t.Fatalf("synthetic canceled polluted duration metrics: %+v", action.TotalDuration)
	}
}

func TestRecordActionMultipleRequestsRemainSeparateRTTSamples(t *testing.T) {
	c := newMonitorTestCollector()
	timing := ActionTiming{Requests: []RequestTiming{
		{WireRTT: 10 * time.Millisecond},
		{WireRTT: 20 * time.Millisecond},
	}}

	c.RecordActionStart("multi")
	c.RecordAction("multi", ResultSuccess, timing, 40*time.Millisecond, 0, 0, nil)

	snap := c.Snapshot(nil, 0)
	action := findMonitorTestAction(t, snap, "multi")
	if action.RTTSampleCount != 2 {
		t.Fatalf("RTT 样本数=%d，期望 2", action.RTTSampleCount)
	}
	if action.TotalDurationSampleCount != 1 {
		t.Fatalf("总耗时样本数=%d，期望 1", action.TotalDurationSampleCount)
	}
	if action.ClientCostCount != 1 || action.ClientCostSumNs != int64(10*time.Millisecond) {
		t.Fatalf("clientCost 应为 wallClock-RTTSum=10ms，count=%d sum=%d",
			action.ClientCostCount, action.ClientCostSumNs)
	}
}

func TestRecordActionCountsZeroClientCostSample(t *testing.T) {
	c := newMonitorTestCollector()
	timing := ActionTiming{
		Requests: []RequestTiming{{WireRTT: 10 * time.Millisecond}},
		Client:   ClientTiming{BuildCost: time.Millisecond},
	}

	c.RecordActionStart("zero-client-cost")
	c.RecordAction("zero-client-cost", ResultSuccess, timing, 10*time.Millisecond, 0, 0, nil)

	action := findMonitorTestAction(t, c.Snapshot(nil, 0), "zero-client-cost")
	if action.ClientCostCount != 1 {
		t.Fatalf("ClientCostCount = %d, want 1", action.ClientCostCount)
	}
	if action.ClientAvgMs != 0 {
		t.Fatalf("ClientAvgMs = %v, want 0", action.ClientAvgMs)
	}
	if action.BuildAvgMs != 1 {
		t.Fatalf("BuildAvgMs = %v, want 1", action.BuildAvgMs)
	}
}

func TestRecordActionFailureRecordsCodedErrorsOnly(t *testing.T) {
	c := newMonitorTestCollector()

	c.RecordActionStart("plain")
	c.RecordAction("plain", ResultFailure, ActionTiming{}, 0, 0, 0, errors.New("plain"))
	c.RecordActionStart("coded")
	c.RecordAction("coded", ResultFailure, ActionTiming{}, 0, 0, 0, monitorTestCodedError{code: 12, detail: "coded"})

	snap := c.Snapshot(nil, 0)
	plain := findMonitorTestAction(t, snap, "plain")
	coded := findMonitorTestAction(t, snap, "coded")
	if len(plain.Errors) != 0 {
		t.Fatalf("普通 error 不应进入错误分布，实际 %+v", plain.Errors)
	}
	if len(coded.Errors) != 1 || coded.Errors[0].Code != 12 || coded.Errors[0].Count != 1 {
		t.Fatalf("coded error 应进入错误分布，实际 %+v", coded.Errors)
	}
}

func TestObservedTimingStagesUseIndependentSampleCounts(t *testing.T) {
	c := newMonitorTestCollector()
	timing := ActionTiming{Client: ClientTiming{
		Observed:   StageBuild | StageEncode,
		BuildCost:  0,
		EncodeCost: 0,
	}}

	c.RecordActionStart("observed-zero")
	c.RecordAction("observed-zero", ResultSuccess, timing, 0, 0, 0, nil)

	action := findMonitorTestAction(t, c.Snapshot(nil, 0), "observed-zero")
	if action.BuildSampleCount != 1 || action.EncodeSampleCount != 1 {
		t.Fatalf("observed zero stages lost: build=%d encode=%d", action.BuildSampleCount, action.EncodeSampleCount)
	}
	if action.SendSampleCount != 0 || action.DecodeSampleCount != 0 {
		t.Fatalf("unobserved stages counted: send=%d decode=%d", action.SendSampleCount, action.DecodeSampleCount)
	}
	if action.BuildAvgMs != 0 || action.EncodeAvgMs != 0 {
		t.Fatalf("zero-duration stage average must remain zero: build=%g encode=%g", action.BuildAvgMs, action.EncodeAvgMs)
	}
}

func TestPendingCanceledDoesNotPolluteThroughputOrByteDenominator(t *testing.T) {
	c := newMonitorTestCollector()

	c.RecordActionStart("request")
	c.RecordAction("request", ResultSuccess, ActionTiming{}, time.Millisecond, 100, 200, nil)
	c.RecordPendingCanceled("request")

	snap := c.Snapshot(nil, 0)
	action := findMonitorTestAction(t, snap, "request")
	if action.SampleCount != 1 || snap.TotalActions != 1 || action.CanceledCount != 1 {
		t.Fatalf("pending cancel polluted result counts: sample=%d total=%d canceled=%d", action.SampleCount, snap.TotalActions, action.CanceledCount)
	}
	if action.ByteSampleCount != 1 || action.AvgSendBytes != 100 || action.AvgRecvBytes != 200 {
		t.Fatalf("pending cancel polluted byte denominator: count=%d send=%g recv=%g", action.ByteSampleCount, action.AvgSendBytes, action.AvgRecvBytes)
	}
}

func TestRealCanceledKeepsActualBytesButNotThroughput(t *testing.T) {
	c := newMonitorTestCollector()

	c.RecordActionStart("request")
	c.RecordAction("request", ResultCanceled, ActionTiming{}, time.Millisecond, 30, 40, nil)

	snap := c.Snapshot(nil, 0)
	action := findMonitorTestAction(t, snap, "request")
	if action.SampleCount != 0 || snap.TotalActions != 0 || action.CanceledCount != 1 {
		t.Fatalf("real cancel result counts: sample=%d total=%d canceled=%d", action.SampleCount, snap.TotalActions, action.CanceledCount)
	}
	if action.ByteSampleCount != 1 || action.AvgSendBytes != 30 || action.AvgRecvBytes != 40 {
		t.Fatalf("real cancel bytes lost: count=%d send=%g recv=%g", action.ByteSampleCount, action.AvgSendBytes, action.AvgRecvBytes)
	}
}
