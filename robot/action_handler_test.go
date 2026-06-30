package robot

import (
	"context"
	"errors"
	"testing"

	"stressbot/engine"
	"stressbot/errcode"
	"stressbot/monitor"
	"stressbot/state"
)

func newActionHandlerTestRobot(ctx context.Context) *Robot {
	return &Robot{
		ctx:        ctx,
		actionExec: engine.NewActionExecutor(state.NewStore(), nil, nil, nil, engine.TimingLevelRTTOnly),
	}
}

func resetActionHandlerMonitor(t *testing.T) *monitor.MetricsCollector {
	t.Helper()
	mc := monitor.Global()
	if mc == nil {
		t.Fatal("monitor collector 未初始化")
	}
	mc.Reset()
	return mc
}

func findRobotActionSnapshot(t *testing.T, snap *monitor.CollectorSnapshot, name string) monitor.ActionSnapshot {
	t.Helper()
	for _, action := range snap.Actions {
		if action.Name == name {
			return action
		}
	}
	t.Fatalf("未找到 action 指标：%s", name)
	return monitor.ActionSnapshot{}
}

func TestRobotActionHandlerDeclarativeSuccessRecordsOnce(t *testing.T) {
	mc := resetActionHandlerMonitor(t)
	h := &robotActionHandler{robot: newActionHandlerTestRobot(context.Background())}

	err := h.ExecuteAction(context.Background(), &engine.ActionDef{
		Name:    "clear_ok",
		Pattern: engine.PatternClearState,
		Keys:    []string{"unused"},
	})
	if err != nil {
		t.Fatalf("声明式成功动作不应返回错误: %v", err)
	}

	snap := mc.Snapshot(nil, 0)
	action := findRobotActionSnapshot(t, snap, "clear_ok")
	if snap.TotalActions != 1 {
		t.Fatalf("TotalActions=%d，期望 1", snap.TotalActions)
	}
	if action.Executing != 0 {
		t.Fatalf("Executing=%d，期望 0", action.Executing)
	}
	if action.SuccessCount != 1 || action.FailureCount != 0 || action.TimeoutCount != 0 || action.CanceledCount != 0 {
		t.Fatalf("成功动作分类错误：success=%d failure=%d timeout=%d canceled=%d",
			action.SuccessCount, action.FailureCount, action.TimeoutCount, action.CanceledCount)
	}
	if action.TotalDurationSampleCount != 1 {
		t.Fatalf("总耗时样本数=%d，期望 1", action.TotalDurationSampleCount)
	}
}

func TestRobotActionHandlerDeclarativeFailureRecordsOnce(t *testing.T) {
	mc := resetActionHandlerMonitor(t)
	h := &robotActionHandler{robot: newActionHandlerTestRobot(context.Background())}

	err := h.ExecuteAction(context.Background(), &engine.ActionDef{
		Name:    "bad_pattern",
		Pattern: "badPattern",
	})
	if err == nil {
		t.Fatal("未知 pattern 应返回错误")
	}

	snap := mc.Snapshot(nil, 0)
	action := findRobotActionSnapshot(t, snap, "bad_pattern")
	if snap.TotalActions != 1 {
		t.Fatalf("TotalActions=%d，期望 1", snap.TotalActions)
	}
	if action.Executing != 0 {
		t.Fatalf("Executing=%d，期望 0", action.Executing)
	}
	if action.FailureCount != 1 || action.SuccessCount != 0 || action.TimeoutCount != 0 || action.CanceledCount != 0 {
		t.Fatalf("失败动作分类错误：success=%d failure=%d timeout=%d canceled=%d",
			action.SuccessCount, action.FailureCount, action.TimeoutCount, action.CanceledCount)
	}
	if len(action.Errors) != 1 || action.Errors[0].Code != uint64(errcode.ErrUnknownPattern) {
		t.Fatalf("错误分布应记录 ErrUnknownPattern，实际 %+v", action.Errors)
	}
}

func TestRobotActionHandlerCancelSideEffectRecordsCanceled(t *testing.T) {
	mc := resetActionHandlerMonitor(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := &robotActionHandler{robot: newActionHandlerTestRobot(ctx)}

	err := h.ExecuteAction(ctx, &engine.ActionDef{
		Name:    "cancel_side_effect",
		Pattern: "badPattern",
	})
	var actionErr *engine.ActionError
	if !errors.As(err, &actionErr) || actionErr.Code != errcode.ErrActionCanceled {
		t.Fatalf("取消期副作用错误应归一化为 ErrActionCanceled，实际 %v", err)
	}

	snap := mc.Snapshot(nil, 0)
	action := findRobotActionSnapshot(t, snap, "cancel_side_effect")
	if snap.TotalActions != 1 {
		t.Fatalf("TotalActions=%d，期望 1", snap.TotalActions)
	}
	if action.Executing != 0 {
		t.Fatalf("Executing=%d，期望 0", action.Executing)
	}
	if action.CanceledCount != 1 || action.SuccessCount != 0 || action.FailureCount != 0 || action.TimeoutCount != 0 {
		t.Fatalf("取消动作分类错误：success=%d failure=%d timeout=%d canceled=%d",
			action.SuccessCount, action.FailureCount, action.TimeoutCount, action.CanceledCount)
	}
	if len(action.Errors) != 0 {
		t.Fatalf("取消动作不应进入错误分布，实际 %+v", action.Errors)
	}
}

func TestNormalizeActionCancelKeepsNonActionError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := &robotActionHandler{robot: newActionHandlerTestRobot(ctx)}
	plainErr := errors.New("plain")

	if got := h.normalizeActionCancel(ctx, plainErr); !errors.Is(got, plainErr) {
		t.Fatalf("非 ActionError 不应被取消归一化掩盖，实际 %v", got)
	}
}
