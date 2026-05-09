package robot

import (
	"testing"

	"stressbot/state"
)

func newStore(data map[string]any) *state.Store {
	s := state.NewStore()
	for k, v := range data {
		s.Set(k, v)
	}
	return s
}

func TestParseExpr_SingleKey(t *testing.T) {
	s := newStore(map[string]any{"alive": true})
	if !parseExpr("alive", s) {
		t.Error("alive=true should be true")
	}

	s2 := newStore(map[string]any{"alive": false})
	if parseExpr("alive", s2) {
		t.Error("alive=false should be false")
	}
}

func TestParseExpr_SingleComparison(t *testing.T) {
	s := newStore(map[string]any{"hp": int64(50)})
	if !parseExpr("hp > 0", s) {
		t.Error("hp=50 > 0 should be true")
	}
	if parseExpr("hp > 100", s) {
		t.Error("hp=50 > 100 should be false")
	}
	if !parseExpr("hp >= 50", s) {
		t.Error("hp=50 >= 50 should be true")
	}
}

func TestParseExpr_SingleKey_Nil(t *testing.T) {
	s := newStore(map[string]any{})
	if parseExpr("missing", s) {
		t.Error("missing key should be false")
	}
}

func TestParseExpr_SingleKey_NonZeroInt(t *testing.T) {
	s := newStore(map[string]any{"count": int64(3)})
	if !parseExpr("count", s) {
		t.Error("count=3 should be truthy")
	}
}

func TestParseExpr_SingleKey_ZeroInt(t *testing.T) {
	s := newStore(map[string]any{"count": int64(0)})
	if parseExpr("count", s) {
		t.Error("count=0 should be falsy")
	}
}

func TestParseExpr_And(t *testing.T) {
	s := newStore(map[string]any{"a": true, "b": true})
	if !parseExpr("a && b", s) {
		t.Error("a && b (both true) should be true")
	}

	s2 := newStore(map[string]any{"a": true, "b": false})
	if parseExpr("a && b", s2) {
		t.Error("a && b (b=false) should be false")
	}
}

func TestParseExpr_Or(t *testing.T) {
	s := newStore(map[string]any{"a": false, "b": true})
	if !parseExpr("a || b", s) {
		t.Error("a || b (b=true) should be true")
	}

	s2 := newStore(map[string]any{"a": false, "b": false})
	if parseExpr("a || b", s2) {
		t.Error("a || b (both false) should be false")
	}
}

func TestParseExpr_AndOr(t *testing.T) {
	s := newStore(map[string]any{"a": true, "b": true, "c": false})
	// a && b || c → (true && true) || false → true
	if !parseExpr("a && b || c", s) {
		t.Error("a && b || c should be true")
	}

	s2 := newStore(map[string]any{"a": false, "b": true, "c": false})
	// a && b || c → (false && true) || false → false
	if parseExpr("a && b || c", s2) {
		t.Error("a && b || c (a=false,c=false) should be false")
	}
}

func TestParseExpr_Parens(t *testing.T) {
	s := newStore(map[string]any{"a": true, "b": false, "c": false})
	// a || (b && c) → true || (false && false) → true
	if !parseExpr("a || (b && c)", s) {
		t.Error("a || (b && c) should be true")
	}

	s2 := newStore(map[string]any{"a": false, "b": true, "c": true})
	// (a || b) && c → (false || true) && true → true
	if !parseExpr("(a || b) && c", s2) {
		t.Error("(a || b) && c should be true")
	}
}

func TestParseExpr_Not(t *testing.T) {
	s := newStore(map[string]any{"dead": true})
	if parseExpr("!dead", s) {
		t.Error("!dead (dead=true) should be false")
	}

	s2 := newStore(map[string]any{"dead": false})
	if !parseExpr("!dead", s2) {
		t.Error("!dead (dead=false) should be true")
	}
}

func TestParseExpr_NotWithComparison(t *testing.T) {
	s := newStore(map[string]any{"hp": int64(0)})
	if !parseExpr("!(hp > 0)", s) {
		t.Error("!(hp > 0) with hp=0 should be true")
	}
}

func TestParseExpr_Complex(t *testing.T) {
	s := newStore(map[string]any{
		"hp":     int64(80),
		"alive":  true,
		"admin":  false,
		"level":  int64(10),
	})
	// hp > 0 && (alive || admin) → true && (true || false) → true
	if !parseExpr("hp > 0 && (alive || admin)", s) {
		t.Error("complex expr 1 should be true")
	}

	// level >= 10 || (alive && admin) → true || (true && false) → true
	if !parseExpr("level >= 10 || (alive && admin)", s) {
		t.Error("complex expr 2 should be true")
	}

	// !admin && hp > 50 → true && true → true
	if !parseExpr("!admin && hp > 50", s) {
		t.Error("complex expr 3 should be true")
	}
}

func TestParseExpr_Empty(t *testing.T) {
	s := newStore(map[string]any{})
	if !parseExpr("", s) {
		t.Error("empty expr should be true")
	}
	if !parseExpr("  ", s) {
		t.Error("whitespace-only expr should be true")
	}
}

func TestParseExpr_MultipleAnd(t *testing.T) {
	s := newStore(map[string]any{"a": true, "b": true, "c": true})
	if !parseExpr("a && b && c", s) {
		t.Error("a && b && c (all true) should be true")
	}

	s2 := newStore(map[string]any{"a": true, "b": true, "c": false})
	if parseExpr("a && b && c", s2) {
		t.Error("a && b && c (c=false) should be false")
	}
}

func TestParseExpr_MultipleOr(t *testing.T) {
	s := newStore(map[string]any{"a": false, "b": false, "c": true})
	if !parseExpr("a || b || c", s) {
		t.Error("a || b || c (c=true) should be true")
	}
}
