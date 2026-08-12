package engine

import (
	"errors"
	"strings"
	"testing"

	"stressbot/errcode"
	"stressbot/state"
)

func TestResolveMapBindingFixedKeysDynamicValues(t *testing.T) {
	ae := &ActionExecutor{store: state.NewStore()}

	fb := &FieldBind{
		Field: "params",
		Type:  BindMap,
		Entries: []MapEntryBind{
			{Key: 1, Value: FieldBind{Type: BindFixed, Value: 9}},
			{Key: 2, Value: FieldBind{Type: BindRandomInt, Min: 0, Max: 0}},
			{Key: 3, Value: FieldBind{Type: BindRandomBool}},
		},
	}

	got, err := ae.resolveFieldValueStrict(fb, "GuildEditInfo", "params")
	if err != nil {
		t.Fatalf("resolveFieldValueStrict returned error: %v", err)
	}

	m, ok := got.(map[any]any)
	if !ok {
		t.Fatalf("got %T, want map[any]any", got)
	}
	if m[1] != 9 {
		t.Fatalf("m[1] = %#v, want 9", m[1])
	}
	if m[2] != 0 {
		t.Fatalf("m[2] = %#v, want 0", m[2])
	}
	if _, ok := m[3].(bool); !ok {
		t.Fatalf("m[3] = %#v (%T), want bool", m[3], m[3])
	}
}

func TestResolveMapBindingSkipsOptionalNilEntry(t *testing.T) {
	ae := &ActionExecutor{store: state.NewStore()}

	fb := &FieldBind{
		Field: "params",
		Type:  BindMap,
		Entries: []MapEntryBind{
			{Key: 1, Value: FieldBind{Type: BindState, Source: "missing", Optional: true}},
			{Key: 2, Value: FieldBind{Type: BindFixed, Value: 7}},
		},
	}

	got, err := ae.resolveFieldValueStrict(fb, "GuildEditInfo", "params")
	if err != nil {
		t.Fatalf("resolveFieldValueStrict returned error: %v", err)
	}

	m := got.(map[any]any)
	if _, exists := m[1]; exists {
		t.Fatalf("optional nil entry key=1 should be skipped, got map %#v", m)
	}
	if m[2] != 7 {
		t.Fatalf("m[2] = %#v, want 7", m[2])
	}
}

func TestResolveMapBindingRequiredNilEntryReturnsError(t *testing.T) {
	ae := &ActionExecutor{store: state.NewStore()}

	fb := &FieldBind{
		Field: "params",
		Type:  BindMap,
		Entries: []MapEntryBind{
			{Key: 1, Value: FieldBind{Type: BindState, Source: "missing", Required: true}},
		},
	}

	_, err := ae.resolveFieldValueStrict(fb, "GuildEditInfo", "params")
	if err == nil {
		t.Fatal("resolveFieldValueStrict expected error for required nil entry")
	}
	actionErr, ok := errors.AsType[*ActionError](err)
	if !ok {
		t.Fatalf("err = %T, want *ActionError", err)
	}
	if actionErr.Code != errcode.ErrBindField {
		t.Fatalf("code = %v, want %v", actionErr.Code, errcode.ErrBindField)
	}
	for _, want := range []string{"action=GuildEditInfo", "field=params", "mapKey=1", "value 缺失"} {
		if !strings.Contains(actionErr.Detail, want) {
			t.Fatalf("detail = %q, want contains %q", actionErr.Detail, want)
		}
	}
}

func TestResolveMapBindingSkipsConditionFalseEntry(t *testing.T) {
	s := state.NewStore()
	s.Set("enabled", false)
	ae := &ActionExecutor{store: s}

	bindings := []FieldBind{{
		Field: "params",
		Type:  BindMap,
		Entries: []MapEntryBind{
			{Key: 1, Value: FieldBind{Type: BindFixed, Value: 9, Condition: "state:enabled"}},
			{Key: 2, Value: FieldBind{Type: BindFixed, Value: 7}},
		},
	}}
	if err := PrepareFieldBindings(bindings); err != nil {
		t.Fatal(err)
	}
	fb := &bindings[0]

	got, err := ae.resolveFieldValueStrict(fb, "GuildEditInfo", "params")
	if err != nil {
		t.Fatalf("resolveFieldValueStrict returned error: %v", err)
	}
	m := got.(map[any]any)
	if _, exists := m[1]; exists {
		t.Fatalf("condition false entry key=1 should be skipped, got map %#v", m)
	}
	if m[2] != 7 {
		t.Fatalf("m[2] = %#v, want 7", m[2])
	}
}

func TestResolveMapBindingNonComparableKeyReturnsError(t *testing.T) {
	ae := &ActionExecutor{store: state.NewStore()}

	fb := &FieldBind{
		Field: "params",
		Type:  BindMap,
		Entries: []MapEntryBind{
			{Key: []any{1}, Value: FieldBind{Type: BindFixed, Value: 9}},
		},
	}

	_, err := ae.resolveFieldValueStrict(fb, "GuildEditInfo", "params")
	if err == nil {
		t.Fatal("resolveFieldValueStrict expected error for non-comparable key")
	}
	actionErr, ok := errors.AsType[*ActionError](err)
	if !ok {
		t.Fatalf("err = %T, want *ActionError", err)
	}
	if actionErr.Code != errcode.ErrBindField {
		t.Fatalf("code = %v, want %v", actionErr.Code, errcode.ErrBindField)
	}
	for _, want := range []string{"action=GuildEditInfo", "field=params", "key 不可比较"} {
		if !strings.Contains(actionErr.Detail, want) {
			t.Fatalf("detail = %q, want contains %q", actionErr.Detail, want)
		}
	}
}

func TestResolveMapBindingNestedMapReturnsError(t *testing.T) {
	ae := &ActionExecutor{store: state.NewStore()}

	fb := &FieldBind{
		Field: "params",
		Type:  BindMap,
		Entries: []MapEntryBind{
			{
				Key: 1,
				Value: FieldBind{
					Type: BindMap,
					Entries: []MapEntryBind{
						{Key: 2, Value: FieldBind{Type: BindFixed, Value: 9}},
					},
				},
			},
		},
	}

	_, err := ae.resolveFieldValueStrict(fb, "GuildEditInfo", "params")
	if err == nil {
		t.Fatal("resolveFieldValueStrict expected error for nested map")
	}
	actionErr, ok := errors.AsType[*ActionError](err)
	if !ok {
		t.Fatalf("err = %T, want *ActionError", err)
	}
	if actionErr.Code != errcode.ErrBindField {
		t.Fatalf("code = %v, want %v", actionErr.Code, errcode.ErrBindField)
	}
	for _, want := range []string{"action=GuildEditInfo", "field=params", "mapKey=1", "不支持嵌套 map"} {
		if !strings.Contains(actionErr.Detail, want) {
			t.Fatalf("detail = %q, want contains %q", actionErr.Detail, want)
		}
	}
}
