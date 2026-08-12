package engine

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"stressbot/errcode"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

func TestMain(m *testing.M) {
	stresslog.ReplaceLogger(zap.NewNop())
	os.Exit(m.Run())
}

type executorActionTestHandler struct {
	err          error
	errsByAction map[string][]error
	gotCtx       context.Context
	actionCalls  int
	actionNames  []string

	registerCalls int
	registerErr   error

	sleepCalls     int
	sleepDurations []time.Duration
}

func (h *executorActionTestHandler) ExecuteAction(ctx context.Context, actionDef *ActionDef) error {
	h.gotCtx = ctx
	h.actionCalls++
	h.actionNames = append(h.actionNames, actionDef.Name)
	if len(h.errsByAction[actionDef.Name]) > 0 {
		err := h.errsByAction[actionDef.Name][0]
		h.errsByAction[actionDef.Name] = h.errsByAction[actionDef.Name][1:]
		return err
	}
	return h.err
}

func (h *executorActionTestHandler) ExecuteCondition(*CompiledCondition) bool { return true }

func (h *executorActionTestHandler) RegisterListen([]ListenRef) error {
	h.registerCalls++
	return h.registerErr
}

func (h *executorActionTestHandler) CooperativeSleep(ctx context.Context, d time.Duration) error {
	h.sleepCalls++
	h.sleepDurations = append(h.sleepDurations, d)
	return ctx.Err()
}

func newExecutorActionTest(handler *executorActionTestHandler) *Executor {
	return NewExecutor(&TaskFlow{
		DefaultDelayMs: 1,
		Nodes: map[string]*Node{
			"cleanup": {Type: NodeAction, Action: "cleanup", DelayMs: -1},
		},
		Actions: map[string]*ActionDef{
			"a":       {Name: "a", Pattern: PatternClearState},
			"cleanup": {Name: "cleanup", Pattern: PatternClearState},
		},
	}, handler, "test")
}

func countActionCalls(names []string, want string) int {
	count := 0
	for _, name := range names {
		if name == want {
			count++
		}
	}
	return count
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

func TestExecutorExecuteActionNodeDelayCancellationPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	handler := &executorActionTestHandler{}
	ex := newExecutorActionTest(handler)

	err := ex.executeAction(ctx, &Node{Type: NodeAction, Action: "a", DelayMs: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("nodeDelay 期间取消应向上传播，实际 %v", err)
	}
	if handler.sleepCalls != 1 {
		t.Fatalf("应执行一次 nodeDelay，实际 sleepCalls=%d", handler.sleepCalls)
	}
}

func TestExecutorExecuteActionCanceledBypassesOnErrorDelayAndStrategy(t *testing.T) {
	handler := &executorActionTestHandler{err: context.Canceled}
	ex := newExecutorActionTest(handler)

	err := ex.executeAction(context.Background(), &Node{
		Type:    NodeAction,
		Action:  "a",
		OnError: &OnErrorDef{Strategy: StrategyAbort, Handler: "cleanup", Retry: &RetryDef{MaxRetries: 1}},
		DelayMs: 1,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误应原样返回，实际 %v", err)
	}
	if handler.sleepCalls != 0 {
		t.Fatalf("取消路径不应执行节点延迟，实际 sleepCalls=%d", handler.sleepCalls)
	}
	if got := countActionCalls(handler.actionNames, "cleanup"); got != 0 {
		t.Fatalf("取消路径不应执行 handler，实际 cleanup calls=%d", got)
	}
}

func TestExecutorExecuteActionActionCanceledBypassesOnErrorDelayAndStrategy(t *testing.T) {
	handler := &executorActionTestHandler{err: NewActionError(errcode.ErrActionCanceled, "stopping")}
	ex := newExecutorActionTest(handler)

	err := ex.executeAction(context.Background(), &Node{
		Type:    NodeAction,
		Action:  "a",
		OnError: &OnErrorDef{Strategy: StrategyAbort, Handler: "cleanup", Retry: &RetryDef{MaxRetries: 1}},
		DelayMs: 1,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ACTION_CANCELED 应映射为 context.Canceled，实际 %v", err)
	}
	if handler.sleepCalls != 0 {
		t.Fatalf("ACTION_CANCELED 不应执行节点延迟，实际 sleepCalls=%d", handler.sleepCalls)
	}
	if got := countActionCalls(handler.actionNames, "cleanup"); got != 0 {
		t.Fatalf("ACTION_CANCELED 不应执行 handler，实际 cleanup calls=%d", got)
	}
}

func TestExecutorExecuteActionAbortWrapsErrExecFailed(t *testing.T) {
	handler := &executorActionTestHandler{err: NewActionError(errcode.ErrUnknownPattern, "pattern=bad")}
	ex := newExecutorActionTest(handler)

	err := ex.executeAction(context.Background(), &Node{
		Type:    NodeAction,
		Action:  "a",
		OnError: &OnErrorDef{Strategy: StrategyAbort},
		DelayMs: 1,
	})
	if actionErr, ok := errors.AsType[*ActionError](err); !ok || actionErr.Code != errcode.ErrExecFailed {
		t.Fatalf("失败路径应按 abort 包装 ErrExecFailed，实际 %v", err)
	}
	if handler.sleepCalls != 1 {
		t.Fatalf("非取消失败应执行一次节点延迟，实际 sleepCalls=%d", handler.sleepCalls)
	}
}

func TestExecutorExecuteActionSkipReturnsErrSkip(t *testing.T) {
	handler := &executorActionTestHandler{err: NewActionError(errcode.ErrUnknownPattern, "pattern=bad")}
	ex := newExecutorActionTest(handler)

	err := ex.executeAction(context.Background(), &Node{
		Type:    NodeAction,
		Action:  "a",
		OnError: &OnErrorDef{Strategy: StrategySkip},
		DelayMs: -1,
	})
	if !errors.Is(err, errSkip) {
		t.Fatalf("skip strategy 应返回 errSkip，实际 %v", err)
	}
}

func TestExecutorExecuteActionResumeReturnsNil(t *testing.T) {
	for _, tc := range []struct {
		name    string
		onError *OnErrorDef
	}{
		{name: "empty", onError: nil},
		{name: "resume", onError: &OnErrorDef{Strategy: StrategyResume}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := &executorActionTestHandler{err: NewActionError(errcode.ErrUnknownPattern, "pattern=bad")}
			ex := newExecutorActionTest(handler)

			err := ex.executeAction(context.Background(), &Node{Type: NodeAction, Action: "a", OnError: tc.onError, DelayMs: -1})
			if err != nil {
				t.Fatalf("resume/空 strategy 应返回 nil，实际 %v", err)
			}
		})
	}
}

func TestExecutorExecuteActionIgnoreCodesReturnsNilAndRunsDelayAndListen(t *testing.T) {
	handler := &executorActionTestHandler{err: NewActionError(errcode.ErrRecvTimeout, "timeout")}
	ex := newExecutorActionTest(handler)

	err := ex.executeAction(context.Background(), &Node{
		Type:       NodeAction,
		Action:     "a",
		OnError:    &OnErrorDef{IgnoreCodes: []errcode.ErrorCode{errcode.ErrRecvTimeout}},
		ListenRefs: []ListenRef{{Server: "tcp:logic", Listen: "push"}},
		DelayMs:    1,
	})
	if err != nil {
		t.Fatalf("ignoreCodes 命中应继续流程，实际 %v", err)
	}
	if handler.registerCalls != 1 {
		t.Fatalf("ignoreCodes 命中应注册 listenRefs，实际 registerCalls=%d", handler.registerCalls)
	}
	if handler.sleepCalls != 1 {
		t.Fatalf("ignoreCodes 命中应执行节点延迟，实际 sleepCalls=%d", handler.sleepCalls)
	}
}

func TestExecutorExecuteActionIgnoreCodesSkipsHandlerRetryStrategy(t *testing.T) {
	handler := &executorActionTestHandler{err: NewActionError(errcode.ErrRecvTimeout, "timeout")}
	ex := newExecutorActionTest(handler)

	err := ex.executeAction(context.Background(), &Node{
		Type:    NodeAction,
		Action:  "a",
		OnError: &OnErrorDef{IgnoreCodes: []errcode.ErrorCode{errcode.ErrRecvTimeout}, Handler: "cleanup", Retry: &RetryDef{MaxRetries: 2}, Strategy: StrategyAbort},
		DelayMs: -1,
	})
	if err != nil {
		t.Fatalf("ignoreCodes 命中不应进入 abort，实际 %v", err)
	}
	if handler.actionCalls != 1 {
		t.Fatalf("ignoreCodes 命中不应 retry，实际 actionCalls=%d", handler.actionCalls)
	}
	if got := countActionCalls(handler.actionNames, "cleanup"); got != 0 {
		t.Fatalf("ignoreCodes 命中不应执行 handler，实际 cleanup calls=%d", got)
	}
}

func TestExecutorExecuteActionRetrySuccess(t *testing.T) {
	handler := &executorActionTestHandler{errsByAction: map[string][]error{
		"a": {NewActionError(errcode.ErrRecvTimeout, "timeout"), nil},
	}}
	ex := newExecutorActionTest(handler)

	err := ex.executeAction(context.Background(), &Node{
		Type:    NodeAction,
		Action:  "a",
		OnError: &OnErrorDef{Retry: &RetryDef{MaxRetries: 1, RetryDelayMs: 5}},
		DelayMs: 1,
	})
	if err != nil {
		t.Fatalf("retry 成功应返回 nil，实际 %v", err)
	}
	if got := countActionCalls(handler.actionNames, "a"); got != 2 {
		t.Fatalf("应执行原 action 两次，实际 %d", got)
	}
	if handler.sleepCalls != 2 {
		t.Fatalf("应执行一次 retryDelay 和一次 nodeDelay，实际 sleepCalls=%d", handler.sleepCalls)
	}
	if handler.sleepDurations[0] != 5*time.Millisecond || handler.sleepDurations[1] != time.Millisecond {
		t.Fatalf("sleepDurations=%v，期望 [5ms 1ms]", handler.sleepDurations)
	}
}

func TestExecutorExecuteActionRetryExhausted(t *testing.T) {
	handler := &executorActionTestHandler{errsByAction: map[string][]error{
		"a": {
			NewActionError(errcode.ErrRecvTimeout, "timeout1"),
			NewActionError(errcode.ErrRecvTimeout, "timeout2"),
			NewActionError(errcode.ErrRecvTimeout, "timeout3"),
		},
	}}
	ex := newExecutorActionTest(handler)

	err := ex.executeAction(context.Background(), &Node{
		Type:    NodeAction,
		Action:  "a",
		OnError: &OnErrorDef{Retry: &RetryDef{MaxRetries: 2, RetryDelayMs: 5}},
		DelayMs: 1,
	})
	if err != nil {
		t.Fatalf("默认 resume 下 retry 耗尽应返回 nil，实际 %v", err)
	}
	if got := countActionCalls(handler.actionNames, "a"); got != 3 {
		t.Fatalf("maxRetries=2 应执行原 action 三次，实际 %d", got)
	}
	if handler.sleepCalls != 3 {
		t.Fatalf("应执行两次 retryDelay 和一次 nodeDelay，实际 sleepCalls=%d", handler.sleepCalls)
	}
}

func TestExecutorExecuteActionHandlerRunsForEachFailedAttempt(t *testing.T) {
	handler := &executorActionTestHandler{errsByAction: map[string][]error{
		"a":       {NewActionError(errcode.ErrRecvTimeout, "timeout1"), NewActionError(errcode.ErrRecvTimeout, "timeout2"), nil},
		"cleanup": {nil, nil},
	}}
	ex := newExecutorActionTest(handler)

	err := ex.executeAction(context.Background(), &Node{
		Type:    NodeAction,
		Action:  "a",
		OnError: &OnErrorDef{Handler: "cleanup", Retry: &RetryDef{MaxRetries: 2}},
		DelayMs: -1,
	})
	if err != nil {
		t.Fatalf("retry 成功应返回 nil，实际 %v", err)
	}
	if got := countActionCalls(handler.actionNames, "cleanup"); got != 2 {
		t.Fatalf("每次失败都应执行 handler，实际 cleanup calls=%d", got)
	}
}

func TestExecutorExecuteActionHandlerFailureStopsRetry(t *testing.T) {
	handler := &executorActionTestHandler{errsByAction: map[string][]error{
		"a":       {NewActionError(errcode.ErrRecvTimeout, "timeout")},
		"cleanup": {NewActionError(errcode.ErrUnknownPattern, "cleanup failed")},
	}}
	ex := newExecutorActionTest(handler)
	ex.flow.Nodes["cleanup"] = &Node{Type: NodeAction, Action: "cleanup", OnError: &OnErrorDef{Strategy: StrategyAbort}, DelayMs: -1}

	err := ex.executeAction(context.Background(), &Node{
		Type:    NodeAction,
		Action:  "a",
		OnError: &OnErrorDef{Handler: "cleanup", Retry: &RetryDef{MaxRetries: 2}},
		DelayMs: -1,
	})
	if err == nil {
		t.Fatal("handler 失败应直接返回错误")
	}
	if got := countActionCalls(handler.actionNames, "a"); got != 1 {
		t.Fatalf("handler 失败不应继续 retry 原 action，实际 a calls=%d", got)
	}
}

func TestExecutorExecuteActionHandlerSkipIsNormalCompletion(t *testing.T) {
	handler := &executorActionTestHandler{errsByAction: map[string][]error{
		"a":       {NewActionError(errcode.ErrRecvTimeout, "timeout"), nil},
		"cleanup": {NewActionError(errcode.ErrUnknownPattern, "skip cleanup")},
	}}
	ex := newExecutorActionTest(handler)
	ex.flow.Nodes["cleanup"] = &Node{Type: NodeAction, Action: "cleanup", OnError: &OnErrorDef{Strategy: StrategySkip}, DelayMs: -1}

	err := ex.executeAction(context.Background(), &Node{
		Type:    NodeAction,
		Action:  "a",
		OnError: &OnErrorDef{Handler: "cleanup", Retry: &RetryDef{MaxRetries: 1}},
		DelayMs: -1,
	})
	if err != nil {
		t.Fatalf("handler 内 errSkip 应视为正常完成，实际 %v", err)
	}
	if got := countActionCalls(handler.actionNames, "a"); got != 2 {
		t.Fatalf("handler skip 后仍应 retry 原 action，实际 a calls=%d", got)
	}
}

func TestExecutorExecuteActionListenRefsOnlyOnSuccessOrIgnore(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		handler := &executorActionTestHandler{}
		ex := newExecutorActionTest(handler)
		err := ex.executeAction(context.Background(), &Node{Type: NodeAction, Action: "a", ListenRefs: []ListenRef{{Server: "tcp:logic", Listen: "push"}}, DelayMs: -1})
		if err != nil || handler.registerCalls != 1 {
			t.Fatalf("成功应注册 listenRefs，err=%v registerCalls=%d", err, handler.registerCalls)
		}
	})

	t.Run("failure", func(t *testing.T) {
		handler := &executorActionTestHandler{err: NewActionError(errcode.ErrRecvTimeout, "timeout")}
		ex := newExecutorActionTest(handler)
		err := ex.executeAction(context.Background(), &Node{Type: NodeAction, Action: "a", ListenRefs: []ListenRef{{Server: "tcp:logic", Listen: "push"}}, DelayMs: -1})
		if err != nil || handler.registerCalls != 0 {
			t.Fatalf("普通失败不应注册 listenRefs，err=%v registerCalls=%d", err, handler.registerCalls)
		}
	})
}

func TestExecutorExecuteActionListenRegisterFailureDoesNotRetry(t *testing.T) {
	handler := &executorActionTestHandler{registerErr: NewActionError(errcode.ErrListenRegister, "listen failed")}
	ex := newExecutorActionTest(handler)

	err := ex.executeAction(context.Background(), &Node{
		Type:       NodeAction,
		Action:     "a",
		OnError:    &OnErrorDef{Retry: &RetryDef{MaxRetries: 2}, Strategy: StrategyAbort},
		ListenRefs: []ListenRef{{Server: "tcp:logic", Listen: "push"}},
		DelayMs:    -1,
	})
	if actionErr, ok := errors.AsType[*ActionError](err); !ok || actionErr.Code != errcode.ErrListenRegister {
		t.Fatalf("监听注册失败应按 ErrListenRegister abort，实际 %v", err)
	}
	if got := countActionCalls(handler.actionNames, "a"); got != 1 {
		t.Fatalf("监听注册失败不应 retry 原 action，实际 a calls=%d", got)
	}
}
