package engine

import (
	"testing"

	"stressbot/state"
)

func BenchmarkConditionEvaluation(b *testing.B) {
	store := state.NewStore()
	store.Set("hp", int64(80))
	store.Set("index", 8)
	store.Set("alive", true)
	store.Set("admin", false)
	store.Set("profile", map[string]any{"level": int64(12)})

	cases := []struct {
		name string
		expr string
	}{
		{"simple_compare", "state:hp > 0"},
		{"arithmetic", "state:(index + 2) % 5 == 0"},
		{"logical", "state:hp > 0 && (alive || admin)"},
		{"short_circuit", "state:alive || missing.path > 0"},
		{"nested_path", "state:profile.level >= 10"},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			condition, err := compileCondition(tc.expr)
			if err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			b.ReportAllocs()
			for b.Loop() {
				if !condition.EvalState(store) {
					b.Fatal("condition unexpectedly false")
				}
			}
		})
	}
}
