package monitor

import (
	"testing"
	"time"
)

// TestTakeReportSnapshot_WindowCarriesExecuting 区间报告必须带上「窗口收口那一刻的在执行数」。
//
// 区间采集器每周期换新，executing 这个瞬时量从不在它身上累计（只减不加会算成负数），
// 所以区间面的该列曾经结构性恒为 0——前端摘要卡片显示「执行中 N」而动作表「并发」列全 0。
func TestTakeReportSnapshot_WindowCarriesExecuting(t *testing.T) {
	c := NewCollector(CollectorConfig{ApdexThresholdMs: 100})

	// done：本窗口内跑完一次，区间里有样本行。
	c.RecordActionStart("done")
	c.RecordAction("done", ResultSuccess, ActionTiming{}, 10*time.Millisecond, 1, 2, nil)
	// inflight：开始了但没结束，累计面 executing=1，区间面没有样本行。
	c.RecordActionStart("inflight")

	start := time.Now().Add(-time.Second)
	snap := c.TakeReportSnapshot(ReportMeta{
		Sequence: 1, StartedAt: start, EndedAt: time.Now(), ExpectedIntervalSeconds: 1,
	})
	if snap.Window == nil {
		t.Fatal("区间报告缺失")
	}

	// 累计面：done 已配平归零，inflight 仍在执行。
	if got := actionExecutingByName(t, snap.Actions, "done"); got != 0 {
		t.Fatalf("累计面 done.Executing = %d，期望 0", got)
	}
	if got := actionExecutingByName(t, snap.Actions, "inflight"); got != 1 {
		t.Fatalf("累计面 inflight.Executing = %d，期望 1", got)
	}

	// 区间面逐行：口径与累计面一致，不再恒为 0。
	if got := actionExecutingByName(t, snap.Window.Actions, "done"); got != 0 {
		t.Fatalf("区间面 done.Executing = %d，期望 0", got)
	}

	// 区间汇总取累计面总数：inflight 在区间里没有样本行，逐行求和会漏掉它。
	if snap.Window.Summary.Executing != 1 {
		t.Fatalf("区间汇总 Executing = %d，期望 1（含无样本行的 inflight）", snap.Window.Summary.Executing)
	}
	if snap.Window.Summary.Executing != snap.Summary.Executing {
		t.Fatalf("区间汇总 %d 与累计汇总 %d 口径不一致",
			snap.Window.Summary.Executing, snap.Summary.Executing)
	}
}

// TestTakeReportSnapshot_WindowExecutingReflectsConcurrency 多个并发在途时逐行数值正确。
func TestTakeReportSnapshot_WindowExecutingReflectsConcurrency(t *testing.T) {
	c := NewCollector(CollectorConfig{ApdexThresholdMs: 100})

	for range 3 {
		c.RecordActionStart("hot")
		c.RecordAction("hot", ResultSuccess, ActionTiming{}, time.Millisecond, 1, 1, nil)
	}
	// 再压两个在途的，累计面 executing 应为 2。
	c.RecordActionStart("hot")
	c.RecordActionStart("hot")

	start := time.Now().Add(-time.Second)
	snap := c.TakeReportSnapshot(ReportMeta{
		Sequence: 1, StartedAt: start, EndedAt: time.Now(), ExpectedIntervalSeconds: 1,
	})
	if got := actionExecutingByName(t, snap.Window.Actions, "hot"); got != 2 {
		t.Fatalf("区间面 hot.Executing = %d，期望 2", got)
	}
	if snap.Window.Summary.Executing != 2 {
		t.Fatalf("区间汇总 Executing = %d，期望 2", snap.Window.Summary.Executing)
	}
}

func actionExecutingByName(t *testing.T, actions []ActionSnapshot, name string) int64 {
	t.Helper()
	for _, action := range actions {
		if action.Name == name {
			return action.Executing
		}
	}
	t.Fatalf("未找到 action 指标：%s", name)
	return 0
}
