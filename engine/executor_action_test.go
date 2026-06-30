package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"stressbot/errcode"
)

type executorActionTestHandler struct {
	err        error
	gotCtx     context.Context
	sleepCalls int
}

func (h *executorActionTestHandler) ExecuteAction(ctx context.Context, actionDef *ActionDef) error {
	h.gotCtx = ctx
	return h.err
}

func (h *executorActionTestHandler) ExecuteBoolean(string) bool { return true }

func (h *executorActionTestHandler) RegisterListen([]ListenRef) error { return nil }

func (h *executorActionTestHandler) CooperativeSleep(context.Context, time.Duration) error {
	h.sleepCalls++
	return nil
}

func newExecutorActionTest(handler *executorActionTestHandler) *Executor {
	return NewExecutor(&TaskFlow{
		DefaultDelayMs: 1,
		Actions: map[string]*ActionDef{
			"a": {Name: "a", Pattern: PatternClearState},
		},
	}, handler, "test")
}

func TestExecutorExecuteActionPassesContextToHandler(t *testing.T) {
	ctx := t.Context()
	handler := &executorActionTestHandler{}
	ex := newExecutorActionTest(handler)

	if err := ex.executeAction(ctx, &Node{Type: NodeAction, Action: "a", DelayMs: -1}); err != nil {
		t.Fatalf("executeAction 返回错误: %v", err)
	}
	if handler.gotCtx != ctx {
		t.Fatal("ExecuteAction 未收到流程 ctx")
	}
}

func TestExecutorExecuteActionCanceledBypassesDelayAndStrategy(t *testing.T) {
	handler := &executorActionTestHandler{err: context.Canceled}
	ex := newExecutorActionTest(handler)

	err := ex.executeAction(context.Background(), &Node{
		Type:          NodeAction,
		Action:        "a",
		ErrorStrategy: StrategyAbort,
		DelayMs:       1,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误应原样返回，实际 %v", err)
	}
	if handler.sleepCalls != 0 {
		t.Fatalf("取消路径不应执行节点延迟，实际 sleepCalls=%d", handler.sleepCalls)
	}
}

func TestExecutorExecuteActionActionCanceledBypassesDelayAndStrategy(t *testing.T) {
	handler := &executorActionTestHandler{err: NewActionError(errcode.ErrActionCanceled, "stopping")}
	ex := newExecutorActionTest(handler)

	err := ex.executeAction(context.Background(), &Node{
		Type:          NodeAction,
		Action:        "a",
		ErrorStrategy: StrategyAbort,
		DelayMs:       1,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ACTION_CANCELED 应映射为 context.Canceled，实际 %v", err)
	}
	if handler.sleepCalls != 0 {
		t.Fatalf("ACTION_CANCELED 不应执行节点延迟，实际 sleepCalls=%d", handler.sleepCalls)
	}
}

func TestExecutorExecuteActionFailureAppliesDelayAndStrategy(t *testing.T) {
	handler := &executorActionTestHandler{err: NewActionError(errcode.ErrUnknownPattern, "pattern=bad")}
	ex := newExecutorActionTest(handler)

	err := ex.executeAction(context.Background(), &Node{
		Type:          NodeAction,
		Action:        "a",
		ErrorStrategy: StrategyAbort,
		DelayMs:       1,
	})
	var actionErr *ActionError
	if !errors.As(err, &actionErr) || actionErr.Code != errcode.ErrExecFailed {
		t.Fatalf("失败路径应按 abort 包装 ErrExecFailed，实际 %v", err)
	}
	if handler.sleepCalls != 1 {
		t.Fatalf("非取消失败应执行一次节点延迟，实际 sleepCalls=%d", handler.sleepCalls)
	}
}
