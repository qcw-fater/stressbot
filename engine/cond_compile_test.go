package engine

import (
	"fmt"
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

func TestCompiledConditionMatchesInterpreter(t *testing.T) {
	tests := []struct {
		expr string
		data map[string]any
	}{
		{"index % 2 == 0", map[string]any{"index": 4}},
		{"7 / 2 == 3", nil},
		{"-5 % 3 == -2", nil},
		{"!a == b", map[string]any{"a": true, "b": false}},
		{"missing || fallback", map[string]any{"fallback": true}},
		{"pid == 9007199254740993", map[string]any{"pid": uint64(9007199254740993)}},
		{"count", map[string]any{"count": int64(3)}},
		{"alive || 9223372036854775808 > 0", map[string]any{"alive": true}},
		{"missing && fallback", map[string]any{"fallback": true}},
		{"hp > 0 && (alive || admin)", map[string]any{
			"hp": int64(80), "alive": true, "admin": false,
		}},
	}

	for _, test := range tests {
		t.Run(test.expr, func(t *testing.T) {
			condition, err := compileCondition(PrefixState + test.expr)
			if err != nil {
				t.Fatal(err)
			}
			store := newCondStore(test.data)
			want := parseExpr(test.expr, store)
			if got := condition.EvalState(store); got != want {
				t.Fatalf("compiled result = %v, interpreter = %v", got, want)
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
