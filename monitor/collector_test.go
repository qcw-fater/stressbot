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
	c := &MetricsCollector{enabled: true, startTime: time.Now()}
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
	if snap.TotalActions != 1 {
		t.Fatalf("TotalActions=%d，期望 1", snap.TotalActions)
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
