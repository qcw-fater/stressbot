package engine

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

func init() {
	stresslog.ReplaceLogger(zap.NewNop())
}

func TestSwitchNodeJSONModel(t *testing.T) {
	data := []byte(`{
		"type":"switch",
		"cases":[
			{"condition":"state:level >= 10","next":"advanced","description":"高等级"},
			{"condition":"lua:has_guild.lua","next":"guild"}
		],
		"defaultNext":"normal"
	}`)

	var node Node
	if err := json.Unmarshal(data, &node); err != nil {
		t.Fatalf("unmarshal switch node: %v", err)
	}
	if node.Type != NodeSwitch {
		t.Fatalf("Type = %q, want %q", node.Type, NodeSwitch)
	}
	if len(node.Cases) != 2 {
		t.Fatalf("len(Cases) = %d, want 2", len(node.Cases))
	}
	if node.Cases[0].Condition != "state:level >= 10" {
		t.Fatalf("first condition = %q", node.Cases[0].Condition)
	}
	if node.Cases[0].Next != "advanced" {
		t.Fatalf("first next = %q", node.Cases[0].Next)
	}
	if node.Cases[0].Description != "高等级" {
		t.Fatalf("first description = %q", node.Cases[0].Description)
	}
	if node.DefaultNext != "normal" {
		t.Fatalf("DefaultNext = %q, want normal", node.DefaultNext)
	}
}

type switchTestHandler struct {
	actions        []string
	booleanResults map[string]bool
	booleanCalls   []string
	actionErrors   map[string]error
	sleepCalls     int
}

func (h *switchTestHandler) ExecuteAction(_ context.Context, actionDef *ActionDef) error {
	h.actions = append(h.actions, actionDef.Name)
	if h.actionErrors != nil {
		return h.actionErrors[actionDef.Name]
	}
	return nil
}

func (h *switchTestHandler) ExecuteBoolean(expression string) bool {
	h.booleanCalls = append(h.booleanCalls, expression)
	return h.booleanResults[expression]
}

func (h *switchTestHandler) RegisterListen([]ListenRef) error { return nil }

func (h *switchTestHandler) CooperativeSleep(ctx context.Context, _ time.Duration) error {
	h.sleepCalls++
	return ctx.Err()
}

func newSwitchTestExecutor(handler *switchTestHandler, switchNode *Node) *Executor {
	return NewExecutor(&TaskFlow{
		DefaultDelayMs: -1,
		Nodes: map[string]*Node{
			"main":     switchNode,
			"advanced": {Type: NodeAction, Action: "advanced", DelayMs: -1},
			"normal":   {Type: NodeAction, Action: "normal", DelayMs: -1},
			"fallback": {Type: NodeAction, Action: "fallback", DelayMs: -1},
		},
		Actions: map[string]*ActionDef{
			"advanced": {Name: "advanced", Pattern: PatternClearState},
			"normal":   {Name: "normal", Pattern: PatternClearState},
			"fallback": {Name: "fallback", Pattern: PatternClearState},
		},
	}, handler, "switch-test")
}

func TestExecuteSwitchRunsFirstMatchingCase(t *testing.T) {
	h := &switchTestHandler{booleanResults: map[string]bool{
		"state:level >= 10": true,
		"state:level >= 1":  true,
	}}
	exec := newSwitchTestExecutor(h, &Node{Type: NodeSwitch, Cases: []SwitchCase{
		{Condition: "state:level >= 10", Next: "advanced"},
		{Condition: "state:level >= 1", Next: "normal"},
	}, DefaultNext: "fallback"})

	if err := exec.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !reflect.DeepEqual(h.booleanCalls, []string{"state:level >= 10"}) {
		t.Fatalf("booleanCalls = %#v", h.booleanCalls)
	}
	if !reflect.DeepEqual(h.actions, []string{"advanced"}) {
		t.Fatalf("actions = %#v", h.actions)
	}
}

func TestExecuteSwitchRunsDefaultWhenNoCaseMatches(t *testing.T) {
	h := &switchTestHandler{booleanResults: map[string]bool{
		"state:level >= 10": false,
		"state:level >= 1":  false,
	}}
	exec := newSwitchTestExecutor(h, &Node{Type: NodeSwitch, Cases: []SwitchCase{
		{Condition: "state:level >= 10", Next: "advanced"},
		{Condition: "state:level >= 1", Next: "normal"},
	}, DefaultNext: "fallback"})

	if err := exec.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !reflect.DeepEqual(h.booleanCalls, []string{"state:level >= 10", "state:level >= 1"}) {
		t.Fatalf("booleanCalls = %#v", h.booleanCalls)
	}
	if !reflect.DeepEqual(h.actions, []string{"fallback"}) {
		t.Fatalf("actions = %#v", h.actions)
	}
}

func TestExecuteSwitchEndsWhenNoCaseMatchesAndNoDefault(t *testing.T) {
	h := &switchTestHandler{booleanResults: map[string]bool{"state:ready": false}}
	exec := newSwitchTestExecutor(h, &Node{Type: NodeSwitch, Cases: []SwitchCase{
		{Condition: "state:ready", Next: "advanced"},
	}})

	if err := exec.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !reflect.DeepEqual(h.booleanCalls, []string{"state:ready"}) {
		t.Fatalf("booleanCalls = %#v", h.booleanCalls)
	}
	if len(h.actions) != 0 {
		t.Fatalf("actions = %#v, want none", h.actions)
	}
}

func TestExecuteSwitchPropagatesChildError(t *testing.T) {
	boom := errors.New("boom")
	h := &switchTestHandler{
		booleanResults: map[string]bool{"state:ready": true},
		actionErrors:   map[string]error{"advanced": boom},
	}
	exec := newSwitchTestExecutor(h, &Node{Type: NodeSwitch, Cases: []SwitchCase{
		{Condition: "state:ready", Next: "advanced"},
	}, DefaultNext: "fallback"})
	exec.flow.Nodes["advanced"].OnError = &OnErrorDef{Strategy: StrategyAbort}

	err := exec.Run(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("Run error = %v, want boom", err)
	}
	if !reflect.DeepEqual(h.actions, []string{"advanced"}) {
		t.Fatalf("actions = %#v", h.actions)
	}
}
