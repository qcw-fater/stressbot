package flow

import (
	"fmt"
	"stressbot/binding"
	"strings"
	"testing"

	"stressbot/state"
)

func TestCompiledConditionReadsCurrentStore(t *testing.T) {
	condition, err := compileCondition("state:hp > 0")
	if err != nil {
		t.Fatal(err)
	}
	store := state.NewStore()

	if condition.EvalState(store) {
		t.Fatal("missing hp must be false")
	}
	store.Set("hp", int64(1))
	if !condition.EvalState(store) {
		t.Fatal("updated hp must be visible without recompiling")
	}
	store.Set("hp", int64(0))
	if condition.EvalState(store) {
		t.Fatal("latest hp value must be used")
	}
	store.Delete("hp")
	if condition.EvalState(store) {
		t.Fatal("deleted hp must become missing again")
	}
}

func TestCompiledConditionPreservesSemantics(t *testing.T) {
	tests := []struct {
		expr string
		data map[string]any
		want bool
	}{
		{"index % 2 == 0", map[string]any{"index": 4}, true},
		{"7 / 2 == 3", nil, true},
		{"-5 % 3 == -2", nil, true},
		{"!a == b", map[string]any{"a": true, "b": false}, true},
		{"missing || fallback", map[string]any{"fallback": true}, true},
		{"pid == 9007199254740993", map[string]any{"pid": uint64(9007199254740993)}, true},
		{"count", map[string]any{"count": int64(3)}, false},
		{"alive || 9223372036854775808 > 0", map[string]any{"alive": true}, true},
		{"missing && fallback", map[string]any{"fallback": true}, false},
		{"hp > 0 && (alive || admin)", map[string]any{
			"hp": int64(80), "alive": true, "admin": false,
		}, true},
	}

	for _, test := range tests {
		t.Run(test.expr, func(t *testing.T) {
			condition, err := compileCondition(PrefixState + test.expr)
			if err != nil {
				t.Fatal(err)
			}
			store := newCondStore(test.data)
			if got := condition.EvalState(store); got != test.want {
				t.Fatalf("compiled result = %v, want %v", got, test.want)
			}
		})
	}
}

func TestCompileConditionRejectsMalformedExpression(t *testing.T) {
	invalid := []string{
		"state:(",
		"state:hp >",
		"state:!",
		"state:hp > 0)",
		"state:1 < 2 < 3",
	}
	for _, expression := range invalid {
		t.Run(expression, func(t *testing.T) {
			if _, err := compileCondition(expression); err == nil {
				t.Fatal("malformed expression must fail compilation")
			}
		})
	}
}

func TestCompiledConditionCanBeSharedAcrossStores(t *testing.T) {
	condition, err := compileCondition("state:index % 2 == 0")
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 32; i++ {
		i := i
		t.Run(fmt.Sprintf("store_%d", i), func(t *testing.T) {
			t.Parallel()
			store := state.NewStore()
			store.Set("index", i)
			if got, want := condition.EvalState(store), i%2 == 0; got != want {
				t.Fatalf("result = %v, want %v", got, want)
			}
		})
	}
}

func TestCompiledConditionKeepsLuaReferenceOutOfStateProgram(t *testing.T) {
	condition, err := compileCondition(" lua:check_ready.lua ")
	if err != nil {
		t.Fatal(err)
	}
	if script, ok := condition.LuaScript(); !ok || script != "check_ready.lua" {
		t.Fatalf("LuaScript() = %q, %v", script, ok)
	}
}

func TestPrepareTaskFlowCompilesEveryConditionAndDeduplicates(t *testing.T) {
	flow := &TaskFlow{
		Nodes: map[string]*Node{
			"main": {
				Condition:      "state:ready",
				BreakCondition: "state:done",
				Cases:          []SwitchCase{{Condition: "state:ready"}},
			},
		},
		Actions: map[string]*ActionDef{
			"send": {
				Bindings: []binding.FieldBind{{
					Condition: "state:ready",
					Entries: []binding.MapEntryBind{{
						Value: binding.FieldBind{Condition: "state:nested"},
					}},
				}},
			},
		},
	}
	if err := PrepareTaskFlow(flow); err != nil {
		t.Fatal(err)
	}

	node := flow.Nodes["main"]
	fieldBinding := &flow.Actions["send"].Bindings[0]
	if node.compiledCondition == nil ||
		node.compiledBreakCondition == nil ||
		node.Cases[0].compiledCondition == nil ||
		fieldBinding.PreparedCondition() == nil ||
		fieldBinding.Entries[0].Value.PreparedCondition() == nil {
		t.Fatal("not every condition location was prepared")
	}
	if node.compiledCondition != node.Cases[0].compiledCondition ||
		node.compiledCondition != fieldBinding.PreparedCondition() {
		t.Fatal("identical expressions must share one compiled condition")
	}
}

func TestPreparedConditionRejectsChangedSource(t *testing.T) {
	flow := &TaskFlow{Nodes: map[string]*Node{
		"main": {Type: NodeBoolean, Condition: "state:ready"},
	}}
	if err := PrepareTaskFlow(flow); err != nil {
		t.Fatal(err)
	}
	node := flow.Nodes["main"]
	if node.PreparedCondition() == nil {
		t.Fatal("prepared condition is missing")
	}

	node.Condition = "state:other"
	if node.PreparedCondition() != nil {
		t.Fatal("changed source must not execute stale AST")
	}
}

func TestPrepareFieldBindingsCompilesNestedEntries(t *testing.T) {
	bindings := []binding.FieldBind{{
		Condition: "state:enabled",
		Entries: []binding.MapEntryBind{{
			Value: binding.FieldBind{Condition: "state:enabled"},
		}},
	}}
	if err := PrepareFieldBindings(bindings); err != nil {
		t.Fatal(err)
	}
	root := bindings[0].PreparedCondition()
	nested := bindings[0].Entries[0].Value.PreparedCondition()
	if root == nil || nested == nil {
		t.Fatal("nested field binding condition was not prepared")
	}
	if root != nested {
		t.Fatal("identical nested binding conditions must share one program")
	}
}

func TestPrepareTaskFlowReportsConditionLocation(t *testing.T) {
	flow := &TaskFlow{Actions: map[string]*ActionDef{
		"send": {Bindings: []binding.FieldBind{{
			Entries: []binding.MapEntryBind{{Value: binding.FieldBind{Condition: "state:hp >"}}},
		}}},
	}}
	err := PrepareTaskFlow(flow)
	if err == nil {
		t.Fatal("malformed nested condition must fail preparation")
	}
	if got := err.Error(); !strings.Contains(got, `action "send" bindings[0] entries[0]`) {
		t.Fatalf("error lacks binding location: %s", got)
	}
}
