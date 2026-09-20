package engine

import (
	"context"
	"errors"
	"stressbot/binding"
	flowdef "stressbot/flow"
	"strings"
	"testing"

	"stressbot/errcode"
	"stressbot/state"
)

func TestPrepareTaskFlowRejectsProtectedClearKey(t *testing.T) {
	for _, key := range []string{"id", "index", "account"} {
		t.Run(key, func(t *testing.T) {
			flow := &flowdef.TaskFlow{Actions: map[string]*flowdef.ActionDef{
				"clear": {Pattern: flowdef.PatternClearState, Keys: []string{"battleId", key}},
			}}
			if err := flowdef.PrepareTaskFlow(flow); err == nil {
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

	_, _, _, err := ae.Execute(context.Background(), &flowdef.ActionDef{
		Name:    "clear",
		Pattern: flowdef.PatternClearState,
		Keys:    []string{"battleId", "id"},
	})
	if err == nil {
		t.Fatal("包含内置状态的 clearState 应失败")
	}
	if actionErr, ok := errors.AsType[*errcode.ActionError](err); !ok || actionErr.Code != errcode.ErrStateConfig {
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

	_, _, _, err := ae.Execute(context.Background(), &flowdef.ActionDef{
		Name:    "clear",
		Pattern: flowdef.PatternClearState,
		Keys:    []string{"battleId", "battleSession", "battleId"},
	})
	if err != nil {
		t.Fatalf("普通状态清除失败: %v", err)
	}
	if store.Has("battleId") || store.Has("battleSession") {
		t.Fatal("普通状态应全部删除")
	}
}

// setState 的 map 绑定必须真的写进 state：resolveFieldValue 不认 map，早期实现让整份
// map 静默变成 nil，配置作者只能靠后续动作取不到值反推。
func TestSetStateWritesMapBinding(t *testing.T) {
	store := state.NewStore()
	bindings := []binding.FieldBind{{
		Field: "guildParams",
		Type:  binding.BindMap,
		Entries: []binding.MapEntryBind{
			{Key: "name", Value: binding.FieldBind{Type: binding.BindFixed, Value: "alpha"}},
			{Key: 2, Value: binding.FieldBind{Type: binding.BindFixed, Value: 7}},
			{Key: "skipped", Value: binding.FieldBind{Type: binding.BindState, Source: "missing", Optional: true}},
		},
	}}
	ae := NewActionExecutor(store, nil, nil, nil, 0)

	_, _, _, err := ae.Execute(context.Background(), &flowdef.ActionDef{
		Name:     "setGuildParams",
		Pattern:  flowdef.PatternSetState,
		Bindings: bindings,
	})
	if err != nil {
		t.Fatalf("setState 含 map 绑定不应失败: %v", err)
	}
	got := store.GetMap("guildParams")
	if got == nil {
		t.Fatal("map 绑定未写入 state（GetMap 返回 nil）")
	}
	if got["name"] != "alpha" {
		t.Errorf(`got["name"] = %#v, want "alpha"`, got["name"])
	}
	if got["2"] != 7 {
		t.Errorf(`got["2"] = %#v, want 7（key 归一为字符串）`, got["2"])
	}
	if _, exists := got["skipped"]; exists {
		t.Errorf("optional 缺失的 entry 不应写入: %#v", got)
	}
}

// 未知 binding type 是配置错误：过去按 fixed 兜底（value 通常为空 → 字段静默为 nil），
// 现在必须以 ErrBindField 打断动作。
func TestSetStateUnknownBindingTypeFailsLoud(t *testing.T) {
	store := state.NewStore()
	ae := NewActionExecutor(store, nil, nil, nil, 0)

	_, _, _, err := ae.Execute(context.Background(), &flowdef.ActionDef{
		Name:    "setBad",
		Pattern: flowdef.PatternSetState,
		Bindings: []binding.FieldBind{{
			Field: "hp",
			Type:  "randomIntt",
			Value: 1,
		}},
	})
	if err == nil {
		t.Fatal("未知 binding type 应返回错误")
	}
	actionErr, ok := errors.AsType[*errcode.ActionError](err)
	if !ok || actionErr.Code != errcode.ErrBindField {
		t.Fatalf("err=%v，期望 ErrBindField", err)
	}
	for _, want := range []string{"action=setBad", "field=hp", "type=randomIntt", "未知 binding type"} {
		if !strings.Contains(actionErr.Detail, want) {
			t.Errorf("detail=%q 应含 %q", actionErr.Detail, want)
		}
	}
	if store.Has("hp") {
		t.Error("配置非法时不应写入任何状态")
	}
}

func TestBindingConditionDoesNotParseUnpreparedSource(t *testing.T) {
	store := state.NewStore()
	store.Set("enabled", true)
	actionExecutor := NewActionExecutor(store, nil, nil, nil, 0)
	binding := &binding.FieldBind{Condition: "state:enabled"}

	if actionExecutor.bindingConditionSatisfied(binding) {
		t.Fatal("unprepared binding condition must fail closed")
	}
}
