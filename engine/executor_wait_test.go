package engine

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	flowdef "stressbot/flow"
	"testing"
	"time"
)

type waitTestHandler struct {
	actions    []string
	sleepCalls []time.Duration
	sleepErr   error
}

func (h *waitTestHandler) ExecuteAction(_ context.Context, actionDef *flowdef.ActionDef) error {
	h.actions = append(h.actions, actionDef.Name)
	return nil
}

func (h *waitTestHandler) ExecuteCondition(*flowdef.CompiledCondition) bool { return false }

func (h *waitTestHandler) RegisterListen([]flowdef.ListenRef) error { return nil }

func (h *waitTestHandler) CooperativeSleep(_ context.Context, d time.Duration) error {
	h.sleepCalls = append(h.sleepCalls, d)
	return h.sleepErr
}

func waitNodeFromJSON(t *testing.T, raw string) *flowdef.Node {
	t.Helper()
	var node flowdef.Node
	if err := json.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatalf("unmarshal wait node: %v", err)
	}
	return &node
}

func newWaitTestExecutor(handler *waitTestHandler, waitNode *flowdef.Node, afterNode *flowdef.Node) *Executor {
	nodes := map[string]*flowdef.Node{"main": waitNode}
	actions := map[string]*flowdef.ActionDef{}
	if afterNode != nil {
		nodes["after"] = afterNode
		if afterNode.Type == flowdef.NodeAction && afterNode.Action != "" {
			actions[afterNode.Action] = &flowdef.ActionDef{Name: afterNode.Action, Pattern: flowdef.PatternClearState}
		}
	}
	return NewExecutor(&flowdef.TaskFlow{
		DefaultDelayMs: -1,
		Nodes:          nodes,
		Actions:        actions,
	}, handler, "wait-test")
}

func TestWaitNodeJSONPreservesThen(t *testing.T) {
	node := waitNodeFromJSON(t, `{"type":"wait","waitMs":10,"then":"after"}`)
	encoded, err := json.Marshal(node)
	if err != nil {
		t.Fatalf("marshal wait node: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("unmarshal wait node fields: %v", err)
	}
	if fields["then"] != "after" {
		t.Fatalf("then = %#v, want %q", fields["then"], "after")
	}
}

func TestExecuteWaitRunsThenAfterSleep(t *testing.T) {
	h := &waitTestHandler{}
	exec := newWaitTestExecutor(
		h,
		waitNodeFromJSON(t, `{"type":"wait","waitMs":10,"then":"after"}`),
		&flowdef.Node{Type: flowdef.NodeAction, Action: "after", DelayMs: -1},
	)

	if err := exec.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !reflect.DeepEqual(h.sleepCalls, []time.Duration{10 * time.Millisecond}) {
		t.Fatalf("sleepCalls = %#v", h.sleepCalls)
	}
	if !reflect.DeepEqual(h.actions, []string{"after"}) {
		t.Fatalf("actions = %#v, want [after]", h.actions)
	}
}

func TestExecuteWaitRunsThenWhenDurationIsMissing(t *testing.T) {
	h := &waitTestHandler{}
	exec := newWaitTestExecutor(
		h,
		waitNodeFromJSON(t, `{"type":"wait","then":"after"}`),
		&flowdef.Node{Type: flowdef.NodeAction, Action: "after", DelayMs: -1},
	)

	if err := exec.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(h.sleepCalls) != 0 {
		t.Fatalf("sleepCalls = %#v, want none", h.sleepCalls)
	}
	if !reflect.DeepEqual(h.actions, []string{"after"}) {
		t.Fatalf("actions = %#v, want [after]", h.actions)
	}
}

func TestExecuteWaitDoesNotRunThenWhenSleepIsCanceled(t *testing.T) {
	h := &waitTestHandler{sleepErr: context.Canceled}
	exec := newWaitTestExecutor(
		h,
		waitNodeFromJSON(t, `{"type":"wait","waitMs":10,"then":"after"}`),
		&flowdef.Node{Type: flowdef.NodeAction, Action: "after", DelayMs: -1},
	)

	err := exec.Run(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if len(h.actions) != 0 {
		t.Fatalf("actions = %#v, want none", h.actions)
	}
}

func TestExecuteWaitPropagatesThenError(t *testing.T) {
	h := &waitTestHandler{}
	exec := newWaitTestExecutor(h, waitNodeFromJSON(t, `{"type":"wait","waitMs":10,"then":"missing"}`), nil)

	if err := exec.Run(context.Background()); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("Run error = %v, want ErrNodeNotFound", err)
	}
}

func TestExecuteWaitPropagatesThenControlSignal(t *testing.T) {
	h := &waitTestHandler{}
	exec := newWaitTestExecutor(
		h,
		waitNodeFromJSON(t, `{"type":"wait","waitMs":10,"then":"after"}`),
		&flowdef.Node{Type: flowdef.NodeBreak},
	)

	if err := exec.Run(context.Background()); !errors.Is(err, errBreak) {
		t.Fatalf("Run error = %v, want errBreak", err)
	}
}

func TestExecuteWaitWithoutThenKeepsLeafBehavior(t *testing.T) {
	h := &waitTestHandler{}
	exec := newWaitTestExecutor(h, waitNodeFromJSON(t, `{"type":"wait","waitMs":10}`), nil)

	if err := exec.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(h.actions) != 0 {
		t.Fatalf("actions = %#v, want none", h.actions)
	}
}
