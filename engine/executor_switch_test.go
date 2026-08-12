package engine

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestSwitchNodeJSONModel(t *testing.T) {
	data := []byte(`{
		"type":"switch",
		"cases":[
			{"condition":"state:level >= 10","next":"advanced"},
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
	onBoolean      func() // 每次 ExecuteCondition 的副作用钩子（测试用，如求值时取消 ctx）
}

func (h *switchTestHandler) ExecuteAction(_ context.Context, actionDef *ActionDef) error {
	h.actions = append(h.actions, actionDef.Name)
	if h.actionErrors != nil {
		return h.actionErrors[actionDef.Name]
	}
	return nil
}

func (h *switchTestHandler) ExecuteCondition(condition *CompiledCondition) bool {
	if h.onBoolean != nil {
		h.onBoolean()
	}
	expression := condition.Source()
	h.booleanCalls = append(h.booleanCalls, expression)
	return h.booleanResults[expression]
}

func (h *switchTestHandler) RegisterListen([]ListenRef) error { return nil }

func (h *switchTestHandler) CooperativeSleep(ctx context.Context, _ time.Duration) error {
	h.sleepCalls++
	return ctx.Err()
}

func newSwitchTestExecutor(handler *switchTestHandler, switchNode *Node) *Executor {
	flow := &TaskFlow{
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
	}
	if err := PrepareTaskFlow(flow); err != nil {
		panic(err)
	}
	return NewExecutor(flow, handler, "switch-test")
}

func TestExecutorDoesNotParseUnpreparedCondition(t *testing.T) {
	handler := &switchTestHandler{booleanResults: map[string]bool{"state:ready": true}}
	flow := &TaskFlow{Nodes: map[string]*Node{
		"main": {Type: NodeBoolean, Condition: "state:ready", TrueNext: "yes"},
		"yes":  {Type: NodeSequence},
	}}
	executor := NewExecutor(flow, handler, "test")
	if err := executor.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(handler.booleanCalls) != 0 {
		t.Fatal("unprepared condition must fail before reaching handler")
	}
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

// 命中 case 但 Next 为空：视为本分支正常结束，不回退 default，也不执行任何 action。
func TestExecuteSwitchMatchedEmptyNextEndsWithoutDefault(t *testing.T) {
	h := &switchTestHandler{booleanResults: map[string]bool{"state:ready": true}}
	exec := newSwitchTestExecutor(h, &Node{Type: NodeSwitch, Cases: []SwitchCase{
		{Condition: "state:ready", Next: ""},
	}, DefaultNext: "fallback"})

	if err := exec.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !reflect.DeepEqual(h.booleanCalls, []string{"state:ready"}) {
		t.Fatalf("booleanCalls = %#v", h.booleanCalls)
	}
	if len(h.actions) != 0 {
		t.Fatalf("actions = %#v, want none（命中空 Next 不应回退到 default）", h.actions)
	}
}

// 子节点返回 errSkip（onError.strategy=skip）：switch 视为分支正常结束，Run 返回 nil。
func TestExecuteSwitchNormalizesErrSkip(t *testing.T) {
	h := &switchTestHandler{
		booleanResults: map[string]bool{"state:ready": true},
		actionErrors:   map[string]error{"advanced": errors.New("fail")},
	}
	exec := newSwitchTestExecutor(h, &Node{Type: NodeSwitch, Cases: []SwitchCase{
		{Condition: "state:ready", Next: "advanced"},
	}, DefaultNext: "fallback"})
	exec.flow.Nodes["advanced"].OnError = &OnErrorDef{Strategy: StrategySkip}

	if err := exec.Run(context.Background()); err != nil {
		t.Fatalf("Run error = %v, want nil（errSkip 应被归一化为分支正常结束）", err)
	}
	if !reflect.DeepEqual(h.actions, []string{"advanced"}) {
		t.Fatalf("actions = %#v", h.actions)
	}
}

// 空 cases + 有 defaultNext：前端校验会拦，但后端不应 panic，直接走 default。
func TestExecuteSwitchEmptyCasesRunsDefault(t *testing.T) {
	h := &switchTestHandler{}
	exec := newSwitchTestExecutor(h, &Node{Type: NodeSwitch, Cases: nil, DefaultNext: "fallback"})

	if err := exec.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(h.booleanCalls) != 0 {
		t.Fatalf("booleanCalls = %#v, want none（无 case 可求值）", h.booleanCalls)
	}
	if !reflect.DeepEqual(h.actions, []string{"fallback"}) {
		t.Fatalf("actions = %#v, want [fallback]", h.actions)
	}
}

// case 之间 ctx 取消：求值第 1 个 case 时取消 ctx，第 2 个 case 不再求值，Run 返回 ctx.Err。
func TestExecuteSwitchStopsOnContextCancelBetweenCases(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &switchTestHandler{
		booleanResults: map[string]bool{"c0": false, "c1": false},
		onBoolean:      func() { cancel() }, // 求值 c0 时取消 ctx
	}
	exec := newSwitchTestExecutor(h, &Node{Type: NodeSwitch, Cases: []SwitchCase{
		{Condition: "c0", Next: "advanced"},
		{Condition: "c1", Next: "normal"},
	}, DefaultNext: "fallback"})

	err := exec.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	// c0 已求值（求值时取消 ctx），c1 因 ctx 取消在迭代入口被拦
	if !reflect.DeepEqual(h.booleanCalls, []string{"c0"}) {
		t.Fatalf("booleanCalls = %#v, want [c0]", h.booleanCalls)
	}
	if len(h.actions) != 0 {
		t.Fatalf("actions = %#v, want none", h.actions)
	}
}
