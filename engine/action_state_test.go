package engine

import (
	"context"
	"errors"
	"testing"

	"stressbot/errcode"
	"stressbot/state"
)

func TestPrepareTaskFlowRejectsProtectedClearKey(t *testing.T) {
	for _, key := range []string{"id", "index", "account"} {
		t.Run(key, func(t *testing.T) {
			flow := &TaskFlow{Actions: map[string]*ActionDef{
				"clear": {Pattern: PatternClearState, Keys: []string{"battleId", key}},
			}}
			if err := PrepareTaskFlow(flow); err == nil {
				t.Fatalf("clearState 清除 %s 应失败", key)
			}
		})
	}
}

func TestClearStateProtectedKeyIsAtomic(t *testing.T) {
	store := state.NewStore()
	store.Set("battleId", int64(7))
	store.Set("id", 1)
	ae := NewActionExecutor(store, nil, nil, nil, 0)

	_, _, _, err := ae.Execute(context.Background(), &ActionDef{
		Name:    "clear",
		Pattern: PatternClearState,
		Keys:    []string{"battleId", "id"},
	})
	if err == nil {
		t.Fatal("包含内置状态的 clearState 应失败")
	}
	if actionErr, ok := errors.AsType[*ActionError](err); !ok || actionErr.Code != errcode.ErrStateConfig {
		t.Fatalf("err=%v want ErrStateConfig", err)
	}
	if !store.Has("battleId") || !store.Has("id") {
		t.Fatal("校验失败前不得删除任何状态")
	}
}

func TestClearStateDeletesNormalKeys(t *testing.T) {
	store := state.NewStore()
	store.Set("battleId", int64(7))
	store.Set("battleSession", int64(9))
	ae := NewActionExecutor(store, nil, nil, nil, 0)

	_, _, _, err := ae.Execute(context.Background(), &ActionDef{
		Name:    "clear",
		Pattern: PatternClearState,
		Keys:    []string{"battleId", "battleSession", "battleId"},
	})
	if err != nil {
		t.Fatalf("普通状态清除失败: %v", err)
	}
	if store.Has("battleId") || store.Has("battleSession") {
		t.Fatal("普通状态应全部删除")
	}
}

func TestBindingConditionDoesNotParseUnpreparedSource(t *testing.T) {
	store := state.NewStore()
	store.Set("enabled", true)
	actionExecutor := NewActionExecutor(store, nil, nil, nil, 0)
	binding := &FieldBind{Condition: "state:enabled"}

	if actionExecutor.bindingConditionSatisfied(binding) {
		t.Fatal("unprepared binding condition must fail closed")
	}
}
