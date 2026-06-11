# Proto Map Binding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add declarative proto `map<K,V>` binding with fixed keys and dynamic values, including backend execution, proto map reflection writes, frontend editing, export, validation, and map type display.

**Architecture:** Add `type: "map"` to the existing `FieldBind` model and resolve each map entry value through the existing binding resolver. Extend `protox.Factory.SetField` with a map-specific branch that converts Go map keys/values through protobuf descriptors. Keep key binding out of scope: keys are fixed values, values are dynamic binding expressions.

**Tech Stack:** Go dynamic protobuf (`google.golang.org/protobuf/reflect/protoreflect`, `dynamicpb`), existing `engine` declarative action executor, React 18 + TypeScript + Ant Design 5 + Vitest.

---

## File Structure

### Backend

- Modify `engine/flow.go`
  - Add `BindMap` constant.
  - Add `MapEntryBind` struct.
  - Add `Entries []MapEntryBind` to `FieldBind`.
- Modify `engine/action.go`
  - Change binding value resolution to support returning errors from nested map entries.
  - Add `BindMap` resolution that builds `map[any]any` from fixed entry keys and dynamically resolved entry values.
- Add `engine/action_map_test.go`
  - Unit tests for map binding resolution behavior, including dynamic values and nil entry handling.
- Modify `protox/factory.go`
  - Add `setMapField`, key conversion, and map value conversion helpers.
  - Add `field.IsMap()` branch before repeated/scalar assignment.
- Add `protox/factory_map_test.go`
  - Unit tests using a temporary `.proto` with `map<int32,int32>`, `map<string,int32>`, and `map<int64,string>`.

### Frontend

- Modify `cmd/web/src/types/action.ts`
  - Add `'map'` to `BindingType` and `ALL_BINDING_TYPES`.
  - Add `MapEntryBind` and `entries?: MapEntryBind[]`.
- Modify `cmd/web/src/components/FlowEditor/editors/ActionEditor/ProtoPathInput.tsx`
  - Display map fields as `map<key,value>`.
- Modify `cmd/web/src/components/FlowEditor/editors/ActionEditor/BindingsTable.tsx`
  - Add `map` to type groups and description metadata if present in this file.
- Modify `cmd/web/src/components/FlowEditor/editors/ActionEditor/BindingTypeForm.tsx`
  - Add a map entry editor.
  - Add value-expression rendering mode that hides field/storeAs/condition outer controls for entry values.
- Modify `cmd/web/src/components/FlowEditor/codec/flowToJson.ts`
  - Preserve and recursively clean `entries`.
- Modify `cmd/web/src/components/FlowEditor/validation/refsCheck.ts`
  - Validate map entries and nested value bindings.
- Add or modify frontend tests:
  - `cmd/web/src/components/FlowEditor/codec/codec.test.ts`
  - `cmd/web/src/components/FlowEditor/validation/refsCheck.test.ts`
  - `cmd/web/src/components/FlowEditor/editors/ActionEditor/ProtoPathInput.test.tsx` if a test harness exists for React component rendering. If no React test renderer setup exists, cover map display through a small exported pure helper instead.

### Documentation

- Existing spec: `docs/superpowers/specs/2026-06-11-proto-map-binding-design.md`
- This plan: `docs/superpowers/plans/2026-06-11-proto-map-binding.md`

---

## Task 1: Add backend data model and failing map binding resolver tests

**Files:**
- Modify: `engine/flow.go`
- Add: `engine/action_map_test.go`

- [ ] **Step 1: Add the backend `map` model fields**

In `engine/flow.go`, update the binding constants near the existing `BindFixed` block:

```go
const (
    BindFixed         = "fixed"
    BindState         = "state"
    BindStateFirst    = "stateFirst"
    BindStateRandom   = "stateRandom"
    BindStateRandomN  = "stateRandomN"
    BindStateMapKey   = "stateMapKey"
    BindStateMapValue = "stateMapValue"
    BindRandomPick    = "randomPick"
    BindRandomPickN   = "randomPickN"
    BindRandomPickMap = "randomPickMap"
    BindRandomExclude = "randomExclude"
    BindRandomInt     = "randomInt"
    BindRandomFloat   = "randomFloat"
    BindRandomBool    = "randomBool"
    BindRandomString  = "randomString"
    BindListSize      = "listSize"
    BindMap           = "map"
)
```

Add the new struct before `FieldBind`:

```go
// MapEntryBind 定义 proto map 字段中的单个 entry。
// Key 是固定 map key；Value 是动态 value 表达式 binding，忽略其 Field 字段。
type MapEntryBind struct {
    Key   any       `json:"key"`
    Value FieldBind `json:"value"`
}
```

Add `Entries` to `FieldBind` after `Condition` or near `Values`:

```go
Entries []MapEntryBind `json:"entries"` // map: 固定 key + 动态 value 的 entry 列表
```

Update the `FieldBind.Type` comment to include `map`.

- [ ] **Step 2: Write failing tests for map value resolution**

Create `engine/action_map_test.go`:

```go
package engine

import (
    "testing"

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
}
```

- [ ] **Step 3: Run the focused test and verify it fails**

Run:

```powershell
go test ./engine -run TestResolveMapBinding -count=1
```

Expected: FAIL because `resolveFieldValueStrict`, `BindMap`, `MapEntryBind`, or `Entries` is not fully implemented yet.

- [ ] **Step 4: Checkpoint**

Do not commit unless the user explicitly asks. If a commit is requested, invoke the project `git-management` skill before running git commands.

---

## Task 2: Implement backend map binding resolution

**Files:**
- Modify: `engine/action.go`
- Test: `engine/action_map_test.go`

- [ ] **Step 1: Add strict value resolver wrapper**

In `engine/action.go`, replace the existing call in `bindFields`:

```go
value := ae.resolveFieldValue(fb)
```

with:

```go
value, err := ae.resolveFieldValueStrict(fb, actionName, fb.Field)
if err != nil {
    return err
}
```

Then add this helper immediately above the existing `resolveFieldValue` function:

```go
// resolveFieldValueStrict 解析字段绑定值，并为需要错误上下文的复合 binding 返回结构化错误。
func (ae *ActionExecutor) resolveFieldValueStrict(fb *FieldBind, actionName, fieldName string) (any, error) {
    if fb.Type != BindMap {
        return ae.resolveFieldValue(fb), nil
    }

    out := make(map[any]any, len(fb.Entries))
    for _, entry := range fb.Entries {
        val, err := ae.resolveFieldValueStrict(&entry.Value, actionName, fieldName)
        if err != nil {
            return nil, err
        }
        if val == nil {
            if entry.Value.Optional {
                continue
            }
            if entry.Value.Required || isImplicitRequired(entry.Value.Type) {
                return nil, NewActionError(
                    errcode.ErrBindField,
                    fmt.Sprintf("action=%s field=%s mapKey=%v value 缺失", actionName, fieldName, entry.Key),
                )
            }
            continue
        }
        out[entry.Key] = val
    }
    return out, nil
}
```

Ensure `engine/action.go` already imports `fmt` and `errcode`; if not, add them to the existing import block. This file already uses both elsewhere in current code, so duplicate imports should not be added.

- [ ] **Step 2: Add `BindMap` case to non-strict resolver**

In `resolveFieldValue`, add this case before `default`:

```go
case BindMap:
    // map 需要错误上下文，由 resolveFieldValueStrict 处理。
    result := make(map[any]any, len(fb.Entries))
    for _, entry := range fb.Entries {
        val := ae.resolveFieldValue(&entry.Value)
        if val != nil {
            result[entry.Key] = val
        }
    }
    return result
```

This keeps legacy callers of `resolveFieldValue` safe, while `bindFields` uses strict error handling.

- [ ] **Step 3: Run backend map resolver tests**

Run:

```powershell
go test ./engine -run TestResolveMapBinding -count=1
```

Expected: PASS.

- [ ] **Step 4: Run existing engine tests**

Run:

```powershell
go test ./engine -count=1
```

Expected: PASS.

- [ ] **Step 5: Checkpoint**

Do not commit unless the user explicitly asks. If a commit is requested, invoke `git-management` first.

---

## Task 3: Add proto map reflection tests

**Files:**
- Add: `protox/factory_map_test.go`

- [ ] **Step 1: Write failing proto map tests**

Create `protox/factory_map_test.go`:

```go
package protox

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func newMapTestFactory(t *testing.T) *Factory {
    t.Helper()

    dir := t.TempDir()
    protoPath := filepath.Join(dir, "map_test.proto")
    src := `syntax = "proto3";
package maptest;

message MapHolder {
  map<int32, int32> params = 1;
  map<string, int32> scores = 2;
  map<int64, string> names = 3;
}
`
    if err := os.WriteFile(protoPath, []byte(src), 0o644); err != nil {
        t.Fatalf("write proto: %v", err)
    }

    loader := NewLoader([]string{dir}, []string{"map_test.proto"})
    files, err := loader.Load()
    if err != nil {
        t.Fatalf("load proto: %v", err)
    }
    return NewFactory(NewRegistry(files))
}

func TestSetFieldMapInt32Int32(t *testing.T) {
    f := newMapTestFactory(t)
    msg, err := f.Create("maptest.MapHolder")
    if err != nil {
        t.Fatalf("create message: %v", err)
    }

    err = f.SetField(msg, "params", map[any]any{
        1: 11,
        "2": 22,
        int64(3): int64(33),
    })
    if err != nil {
        t.Fatalf("SetField params returned error: %v", err)
    }

    got, err := f.GetField(msg, "params")
    if err != nil {
        t.Fatalf("GetField params returned error: %v", err)
    }
    m, ok := got.(map[string]any)
    if !ok {
        t.Fatalf("got %T, want map[string]any", got)
    }
    if m["1"] != int64(11) || m["2"] != int64(22) || m["3"] != int64(33) {
        t.Fatalf("unexpected params map: %#v", m)
    }
}

func TestSetFieldMapStringInt32(t *testing.T) {
    f := newMapTestFactory(t)
    msg, err := f.Create("maptest.MapHolder")
    if err != nil {
        t.Fatalf("create message: %v", err)
    }

    err = f.SetField(msg, "scores", map[string]any{"alice": 10, "bob": 20})
    if err != nil {
        t.Fatalf("SetField scores returned error: %v", err)
    }

    got, err := f.GetField(msg, "scores")
    if err != nil {
        t.Fatalf("GetField scores returned error: %v", err)
    }
    m := got.(map[string]any)
    if m["alice"] != int64(10) || m["bob"] != int64(20) {
        t.Fatalf("unexpected scores map: %#v", m)
    }
}

func TestSetFieldMapInt64String(t *testing.T) {
    f := newMapTestFactory(t)
    msg, err := f.Create("maptest.MapHolder")
    if err != nil {
        t.Fatalf("create message: %v", err)
    }

    err = f.SetField(msg, "names", map[any]any{int64(1001): "knight"})
    if err != nil {
        t.Fatalf("SetField names returned error: %v", err)
    }

    got, err := f.GetField(msg, "names")
    if err != nil {
        t.Fatalf("GetField names returned error: %v", err)
    }
    m := got.(map[string]any)
    if m["1001"] != "knight" {
        t.Fatalf("unexpected names map: %#v", m)
    }
}

func TestSetFieldMapRejectsNonMapValue(t *testing.T) {
    f := newMapTestFactory(t)
    msg, err := f.Create("maptest.MapHolder")
    if err != nil {
        t.Fatalf("create message: %v", err)
    }

    err = f.SetField(msg, "params", []any{1, 2, 3})
    if err == nil {
        t.Fatal("expected error for non-map value")
    }
    if !strings.Contains(err.Error(), "字段 params 是 map") {
        t.Fatalf("error = %q, want map field message", err.Error())
    }
}
```

- [ ] **Step 2: Run focused protox tests and verify failure**

Run:

```powershell
go test ./protox -run TestSetFieldMap -count=1
```

Expected: FAIL because `Factory.SetField` does not yet support map writes.

- [ ] **Step 3: Checkpoint**

Do not commit unless the user explicitly asks. If a commit is requested, invoke `git-management` first.

---

## Task 4: Implement proto map reflection writes

**Files:**
- Modify: `protox/factory.go`
- Test: `protox/factory_map_test.go`

- [ ] **Step 1: Add map branch before repeated branch**

In `protox/factory.go`, update the final assignment block in `setNestedField` from:

```go
if len(parts) == 1 {
    if field.IsList() {
        return setRepeatedField(ref, field, value)
    }
    val, err := toFieldValue(field, value)
    if err != nil {
        return err
    }
    ref.Set(field, val)
    return nil
}
```

to:

```go
if len(parts) == 1 {
    if field.IsMap() {
        return setMapField(ref, field, value)
    }
    if field.IsList() {
        return setRepeatedField(ref, field, value)
    }
    val, err := toFieldValue(field, value)
    if err != nil {
        return err
    }
    ref.Set(field, val)
    return nil
}
```

- [ ] **Step 2: Add map conversion helpers**

In `protox/factory.go`, add these helpers after `setRepeatedField`:

```go
func setMapField(ref protoreflect.Message, field protoreflect.FieldDescriptor, value any) error {
    entries, err := normalizeMapEntries(value)
    if err != nil {
        return fmt.Errorf("字段 %s 是 map，绑定值需要 map 类型", field.Name())
    }

    mp := ref.Mutable(field).Map()
    mp.Clear()

    keyDesc := field.MapKey()
    valDesc := field.MapValue()
    for _, entry := range entries {
        key, err := toMapKey(keyDesc, entry.key)
        if err != nil {
            return fmt.Errorf("字段 %s 的 map key=%v 无法转换为 %s: %w", field.Name(), entry.key, keyDesc.Kind(), err)
        }
        val, err := toFieldValue(valDesc, entry.value)
        if err != nil {
            return fmt.Errorf("字段 %s 的 map value(key=%v) 转换失败: %w", field.Name(), entry.key, err)
        }
        mp.Set(key, val)
    }
    return nil
}

type mapEntryValue struct {
    key   any
    value any
}

func normalizeMapEntries(value any) ([]mapEntryValue, error) {
    switch m := value.(type) {
    case map[any]any:
        out := make([]mapEntryValue, 0, len(m))
        for k, v := range m {
            out = append(out, mapEntryValue{key: k, value: v})
        }
        return out, nil
    case map[string]any:
        out := make([]mapEntryValue, 0, len(m))
        for k, v := range m {
            out = append(out, mapEntryValue{key: k, value: v})
        }
        return out, nil
    default:
        return nil, fmt.Errorf("unsupported map input %T", value)
    }
}

func toMapKey(field protoreflect.FieldDescriptor, value any) (protoreflect.MapKey, error) {
    switch field.Kind() {
    case protoreflect.BoolKind:
        v, ok := value.(bool)
        if !ok {
            return protoreflect.MapKey{}, fmt.Errorf("需要 bool 类型")
        }
        return protoreflect.ValueOfBool(v).MapKey(), nil
    case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
        return protoreflect.ValueOfInt32(int32(toInt64Value(value))).MapKey(), nil
    case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
        return protoreflect.ValueOfInt64(toInt64Value(value)).MapKey(), nil
    case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
        return protoreflect.ValueOfUint32(uint32(toUint64Value(value))).MapKey(), nil
    case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
        return protoreflect.ValueOfUint64(toUint64Value(value)).MapKey(), nil
    case protoreflect.StringKind:
        return protoreflect.ValueOfString(fmt.Sprintf("%v", value)).MapKey(), nil
    default:
        return protoreflect.MapKey{}, fmt.Errorf("不支持的 map key 类型: %s", field.Kind())
    }
}
```

`protox/factory.go` already imports `fmt` and `protoreflect`; do not add duplicate imports.

- [ ] **Step 3: Run protox map tests**

Run:

```powershell
go test ./protox -run TestSetFieldMap -count=1
```

Expected: PASS.

- [ ] **Step 4: Run protox full tests**

Run:

```powershell
go test ./protox -count=1
```

Expected: PASS.

- [ ] **Step 5: Run backend packages touched so far**

Run:

```powershell
go test ./engine ./protox -count=1
```

Expected: PASS.

- [ ] **Step 6: Checkpoint**

Do not commit unless the user explicitly asks. If a commit is requested, invoke `git-management` first.

---

## Task 5: Add frontend types, export cleanup, and validation tests

**Files:**
- Modify: `cmd/web/src/types/action.ts`
- Modify: `cmd/web/src/components/FlowEditor/codec/flowToJson.ts`
- Modify: `cmd/web/src/components/FlowEditor/validation/refsCheck.ts`
- Modify: `cmd/web/src/components/FlowEditor/codec/codec.test.ts`
- Modify: `cmd/web/src/components/FlowEditor/validation/refsCheck.test.ts`

- [ ] **Step 1: Add TypeScript map binding model**

In `cmd/web/src/types/action.ts`, add `'map'` to `BindingType` and `ALL_BINDING_TYPES`:

```ts
export type BindingType =
  | 'fixed'
  | 'state'
  | 'stateFirst'
  | 'stateRandom'
  | 'stateRandomN'
  | 'stateMapKey'
  | 'stateMapValue'
  | 'randomPick'
  | 'randomPickN'
  | 'randomPickMap'
  | 'randomInt'
  | 'randomFloat'
  | 'randomBool'
  | 'randomString'
  | 'randomExclude'
  | 'listSize'
  | 'map';
```

Add `'map'` to `ALL_BINDING_TYPES` after `listSize`.

Add this interface before `FieldBind`:

```ts
export interface MapEntryBind {
  key?: unknown;
  value?: FieldBind;
}
```

Add this field to `FieldBind`:

```ts
entries?: MapEntryBind[];
```

- [ ] **Step 2: Write failing codec export test**

Append this test to `cmd/web/src/components/FlowEditor/codec/codec.test.ts` inside `describe('codec round-trip', ...)`:

```ts
it('map binding entries 在导出时被保留并清理 value.field', () => {
  const exported = flowToJson({
    defaultDelayMs: raw.defaultDelayMs,
    nodes: raw.nodes,
    actions: {
      A1: {
        pattern: 'tcpSend',
        service: 'logic',
        route: {},
        c2sProto: 'Game.GuildEditInfoC2S',
        bindings: [
          {
            field: 'params',
            type: 'map',
            entries: [
              { key: 1, value: { field: 'ignored', type: 'randomInt', min: 0, max: 1 } },
              { key: 3, value: { type: 'randomInt', min: 1, max: 200 } },
            ],
          },
        ],
      },
    },
    listens: raw.listens,
  });

  expect(exported.actions.A1.bindings?.[0]).toEqual({
    field: 'params',
    type: 'map',
    entries: [
      { key: 1, value: { type: 'randomInt', min: 0, max: 1 } },
      { key: 3, value: { type: 'randomInt', min: 1, max: 200 } },
    ],
  });
});
```

- [ ] **Step 3: Write failing validation tests**

Append these tests to `cmd/web/src/components/FlowEditor/validation/refsCheck.test.ts` inside `describe('validateFlow', ...)`:

```ts
it('map binding 缺少 entries 报错', () => {
  const r = validateFlow(baseFlow({
    actions: {
      A1: {
        pattern: 'tcpSend',
        service: 'logic',
        route: {},
        c2sProto: 'X.Foo',
        bindings: [{ field: 'params', type: 'map' }],
      },
    },
  }));
  expect(r.errors.find((e) => e.code === 'BINDING_MAP_NO_ENTRIES')).toBeTruthy();
});

it('map binding entry 缺少 key 报错', () => {
  const r = validateFlow(baseFlow({
    actions: {
      A1: {
        pattern: 'tcpSend',
        service: 'logic',
        route: {},
        c2sProto: 'X.Foo',
        bindings: [{ field: 'params', type: 'map', entries: [{ value: { type: 'randomInt', min: 1, max: 2 } }] }],
      },
    },
  }));
  expect(r.errors.find((e) => e.code === 'BINDING_MAP_ENTRY_NO_KEY')).toBeTruthy();
});

it('map binding entry value 不能继续使用 map 类型', () => {
  const r = validateFlow(baseFlow({
    actions: {
      A1: {
        pattern: 'tcpSend',
        service: 'logic',
        route: {},
        c2sProto: 'X.Foo',
        bindings: [{ field: 'params', type: 'map', entries: [{ key: 1, value: { type: 'map', entries: [] } }] }],
      },
    },
  }));
  expect(r.errors.find((e) => e.code === 'BINDING_MAP_ENTRY_VALUE_MAP')).toBeTruthy();
});

it('map binding entry value 复用已有 source 校验', () => {
  const r = validateFlow(baseFlow({
    actions: {
      A1: {
        pattern: 'tcpSend',
        service: 'logic',
        route: {},
        c2sProto: 'X.Foo',
        bindings: [{ field: 'params', type: 'map', entries: [{ key: 1, value: { type: 'state' } }] }],
      },
    },
  }));
  expect(r.errors.find((e) => e.code === 'BINDING_NO_SOURCE')).toBeTruthy();
});
```

- [ ] **Step 4: Run focused frontend tests and verify failure**

Run:

```powershell
npm --prefix cmd/web run test -- src/components/FlowEditor/codec/codec.test.ts src/components/FlowEditor/validation/refsCheck.test.ts
```

Expected: FAIL because export cleanup and validation do not yet support `entries`.

- [ ] **Step 5: Checkpoint**

Do not commit unless the user explicitly asks. If a commit is requested, invoke `git-management` first.

---

## Task 6: Implement frontend export cleanup and validation

**Files:**
- Modify: `cmd/web/src/components/FlowEditor/codec/flowToJson.ts`
- Modify: `cmd/web/src/components/FlowEditor/validation/refsCheck.ts`
- Test: `cmd/web/src/components/FlowEditor/codec/codec.test.ts`
- Test: `cmd/web/src/components/FlowEditor/validation/refsCheck.test.ts`

- [ ] **Step 1: Preserve and clean map entries during export**

In `cmd/web/src/components/FlowEditor/codec/flowToJson.ts`, update `cleanFieldBind` by adding this block before `return out;`:

```ts
if (b.entries?.length) {
  out.entries = b.entries.map((entry) => ({
    key: entry.key,
    value: entry.value ? cleanMapEntryValueBind(entry.value) : undefined,
  }));
}
```

Add this helper after `cleanFieldBind`:

```ts
function cleanMapEntryValueBind(b: FieldBind): FieldBind {
  const out = cleanFieldBind({ ...b, field: undefined, storeAs: undefined, condition: undefined, wrap: undefined });
  delete out.field;
  delete out.storeAs;
  delete out.condition;
  delete out.wrap;
  return out;
}
```

- [ ] **Step 2: Add map validation branch**

In `cmd/web/src/components/FlowEditor/validation/refsCheck.ts`, add a `case 'map':` branch inside `checkBindings` switch:

```ts
case 'map':
  if (!b.entries || b.entries.length === 0) {
    issues.push({ severity: 'error', code: 'BINDING_MAP_NO_ENTRIES', message: `${label} type=map 缺少 entries`, location: loc });
    break;
  }
  b.entries.forEach((entry, entryIndex) => {
    const entryLabel = `${label}.entries[${entryIndex}]`;
    if (entry.key === undefined || entry.key === null) {
      issues.push({ severity: 'error', code: 'BINDING_MAP_ENTRY_NO_KEY', message: `${entryLabel} 缺少 key`, location: loc });
    }
    if (!entry.value) {
      issues.push({ severity: 'error', code: 'BINDING_MAP_ENTRY_NO_VALUE', message: `${entryLabel} 缺少 value`, location: loc });
      return;
    }
    if (entry.value.type === 'map') {
      issues.push({ severity: 'error', code: 'BINDING_MAP_ENTRY_VALUE_MAP', message: `${entryLabel}.value 不支持嵌套 map binding`, location: loc });
      return;
    }
    issues.push(...checkBindings(entryLabel, [{ ...entry.value, field: '__mapValue' }], loc));
  });
  break;
```

This reuses existing source/range/values/filter validation for map entry values and avoids `BINDING_NO_FIELD` by injecting a synthetic field for validation only.

- [ ] **Step 3: Run focused frontend tests**

Run:

```powershell
npm --prefix cmd/web run test -- src/components/FlowEditor/codec/codec.test.ts src/components/FlowEditor/validation/refsCheck.test.ts
```

Expected: PASS.

- [ ] **Step 4: Run TypeScript type check for changed frontend types**

Run:

```powershell
npm --prefix cmd/web exec tsc -b --noEmit
```

Expected: PASS. If the project script already uses build mode, this is equivalent to the project `type-check` script.

- [ ] **Step 5: Checkpoint**

Do not commit unless the user explicitly asks. If a commit is requested, invoke `git-management` first.

---

## Task 7: Implement frontend map editor and map field display

**Files:**
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/ProtoPathInput.tsx`
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/BindingsTable.tsx`
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/BindingTypeForm.tsx`

- [ ] **Step 1: Fix map type display in proto path input**

In `cmd/web/src/components/FlowEditor/editors/ActionEditor/ProtoPathInput.tsx`, replace:

```tsx
const typeLabel = shortType(f.type);
```

with:

```tsx
const typeLabel = f.kind === 'map' ? `map<${f.mapKey},${f.mapValue}>` : shortType(f.type);
```

- [ ] **Step 2: Add map to binding type groups**

In `cmd/web/src/components/FlowEditor/editors/ActionEditor/BindingsTable.tsx`, update `TYPE_GROUPS`:

```tsx
const TYPE_GROUPS: { label: string; types: BindingType[] }[] = [
  { label: '固定值', types: ['fixed'] },
  { label: 'state 取值', types: ['state', 'stateFirst', 'stateRandom', 'stateRandomN', 'stateMapKey', 'stateMapValue', 'listSize'] },
  { label: '随机', types: ['randomPick', 'randomPickN', 'randomPickMap', 'randomInt', 'randomFloat', 'randomBool', 'randomString', 'randomExclude'] },
  { label: '复合结构', types: ['map'] },
];
```

If `BINDING_TYPE_DESC` exists in this file, add:

```tsx
map: '构造 proto map 字段：固定 key，value 使用现有 binding 动态生成',
```

- [ ] **Step 3: Add value-expression mode props**

In `cmd/web/src/components/FlowEditor/editors/ActionEditor/BindingTypeForm.tsx`, update props:

```tsx
export interface BindingTypeFormProps {
  binding: FieldBind;
  /** 当前 action 的全部 bindings，用于收集 storeAs */
  currentBindings?: FieldBind[];
  onChange: (b: FieldBind) => void;
  valueOnly?: boolean;
}
```

Update function signature:

```tsx
export function BindingTypeForm({ binding, currentBindings, onChange, valueOnly = false }: BindingTypeFormProps) {
```

- [ ] **Step 4: Add `map` case in `BindingTypeForm`**

Inside the `switch (t)` in `BindingTypeForm`, add this case before `default`:

```tsx
case 'map':
  if (valueOnly) {
    return <span style={{ color: 'var(--color-error)' }}>map value 不支持嵌套 map</span>;
  }
  return <MapEntriesField binding={binding} currentBindings={currentBindings} set={set} />;
```

- [ ] **Step 5: Add map entries editor component**

In `BindingTypeForm.tsx`, add this component after `ValuesField`:

```tsx
function MapEntriesField({
  binding,
  currentBindings,
  set,
}: {
  binding: FieldBind;
  currentBindings?: FieldBind[];
  set: (p: Partial<FieldBind>) => void;
}) {
  const entries = binding.entries ?? [];

  const updateEntry = (index: number, patch: NonNullable<FieldBind['entries']>[number]) => {
    const next = [...entries];
    next[index] = patch;
    set({ entries: next });
  };

  const removeEntry = (index: number) => {
    set({ entries: entries.filter((_, i) => i !== index) });
  };

  const addEntry = () => {
    set({ entries: [...entries, { key: entries.length + 1, value: { type: 'fixed', value: 0 } }] });
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8, width: '100%' }}>
      <span style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>map entries（固定 key，动态 value）</span>
      {entries.map((entry, index) => (
        <div
          key={index}
          style={{
            border: '1px solid var(--border-color)',
            borderRadius: 6,
            padding: 8,
            display: 'flex',
            flexDirection: 'column',
            gap: 8,
          }}
        >
          <Space align="start" wrap>
            <span style={LABEL}>key</span>
            <JsonDraftInput
              mode="jsonOrString"
              value={entry.key}
              emptyValue={undefined}
              onChange={(v) => updateEntry(index, { ...entry, key: v })}
              placeholder="固定 key，如 1"
              style={{ width: 160 }}
            />
            <span style={LABEL}>value type</span>
            <Select
              value={entry.value?.type ?? 'fixed'}
              onChange={(v) => {
                if (v === 'map') return;
                updateEntry(index, { ...entry, value: { type: v as FieldBind['type'] } });
              }}
              options={[
                { value: 'fixed', label: 'fixed' },
                { value: 'state', label: 'state' },
                { value: 'stateFirst', label: 'stateFirst' },
                { value: 'stateRandom', label: 'stateRandom' },
                { value: 'stateRandomN', label: 'stateRandomN' },
                { value: 'stateMapKey', label: 'stateMapKey' },
                { value: 'stateMapValue', label: 'stateMapValue' },
                { value: 'randomPick', label: 'randomPick' },
                { value: 'randomPickN', label: 'randomPickN' },
                { value: 'randomPickMap', label: 'randomPickMap' },
                { value: 'randomInt', label: 'randomInt' },
                { value: 'randomFloat', label: 'randomFloat' },
                { value: 'randomBool', label: 'randomBool' },
                { value: 'randomString', label: 'randomString' },
                { value: 'randomExclude', label: 'randomExclude' },
                { value: 'listSize', label: 'listSize' },
              ]}
              style={{ width: 180 }}
            />
            <Button size="small" danger icon={<DeleteOutlined />} onClick={() => removeEntry(index)}>
              删除
            </Button>
          </Space>
          {entry.value && (
            <BindingTypeForm
              binding={entry.value}
              currentBindings={currentBindings}
              valueOnly
              onChange={(value) => updateEntry(index, { ...entry, value: { ...value, field: undefined } })}
            />
          )}
        </div>
      ))}
      <Button size="small" onClick={addEntry}>添加 entry</Button>
    </div>
  );
}
```

- [ ] **Step 6: Run frontend type check**

Run:

```powershell
npm --prefix cmd/web exec tsc -b --noEmit
```

Expected: PASS.

- [ ] **Step 7: Run focused frontend tests again**

Run:

```powershell
npm --prefix cmd/web run test -- src/components/FlowEditor/codec/codec.test.ts src/components/FlowEditor/validation/refsCheck.test.ts
```

Expected: PASS.

- [ ] **Step 8: Checkpoint**

Do not commit unless the user explicitly asks. If a commit is requested, invoke `git-management` first.

---

## Task 8: End-to-end backend test through ActionExecutor and Factory

**Files:**
- Add or modify: `engine/action_map_test.go`
- Optionally add: `engine/testdata/map_test.proto` only if temporary proto setup becomes too large for a single test helper.

- [ ] **Step 1: Add integration-style test that builds a proto message body**

Append to `engine/action_map_test.go`:

```go
func TestActionExecutorBuildBodyWithMapBinding(t *testing.T) {
    // This test should use protox.NewLoader with a temp proto file, then create
    // an ActionExecutor with a real protox.Factory and a map binding.
    // Keep it in this file so the binding behavior and proto write behavior are checked together.
}
```

Replace the placeholder body immediately with complete code adapted from `protox/factory_map_test.go`. The final test body must:

1. Create a temp proto with:

```proto
syntax = "proto3";
package maptest;
message GuildEditInfoC2S { map<int32, int32> params = 1; }
```

2. Load it with `protox.NewLoader`.
3. Build `protox.NewFactory(protox.NewRegistry(files))`.
4. Create `ActionExecutor{store: state.NewStore(), factory: factory}`.
5. Call `ae.buildBody(&ActionDef{ Name: "GuildEditInfo", C2SProto: "maptest.GuildEditInfoC2S", Bindings: []FieldBind{...}})`.
6. Parse the body with `factory.Parse`.
7. Assert `factory.GetField(parsed, "params")` returns keys `"1"`, `"2"`, `"3"` with expected int64 values.

Use `randomInt` ranges where min equals max for deterministic assertions:

```go
Bindings: []FieldBind{
    {
        Field: "params",
        Type:  BindMap,
        Entries: []MapEntryBind{
            {Key: 1, Value: FieldBind{Type: BindRandomInt, Min: 0, Max: 0}},
            {Key: 2, Value: FieldBind{Type: BindRandomInt, Min: 1, Max: 1}},
            {Key: 3, Value: FieldBind{Type: BindRandomInt, Min: 200, Max: 200}},
        },
    },
},
```

- [ ] **Step 2: Run the integration test**

Run:

```powershell
go test ./engine -run TestActionExecutorBuildBodyWithMapBinding -count=1
```

Expected: PASS.

- [ ] **Step 3: Run backend tests**

Run:

```powershell
go test ./engine ./protox -count=1
```

Expected: PASS.

- [ ] **Step 4: Checkpoint**

Do not commit unless the user explicitly asks. If a commit is requested, invoke `git-management` first.

---

## Task 9: Full validation and cleanup

**Files:**
- Review all modified files from previous tasks.
- No new files unless a test failure exposes a missing focused test.

- [ ] **Step 1: Run Go package tests**

Run:

```powershell
go test ./...
```

Expected: PASS.

- [ ] **Step 2: Run Go build**

Run:

```powershell
go build ./...
```

Expected: PASS.

- [ ] **Step 3: Run frontend type check**

Run:

```powershell
npm --prefix cmd/web exec tsc -b --noEmit
```

Expected: PASS.

- [ ] **Step 4: Run frontend tests**

Run:

```powershell
npm --prefix cmd/web run test
```

Expected: PASS.

- [ ] **Step 5: Inspect generated JSON shape manually**

Use the frontend test fixture or a local flow object and confirm exported JSON keeps this exact shape:

```json
{
  "field": "params",
  "type": "map",
  "entries": [
    { "key": 1, "value": { "type": "randomInt", "min": 0, "max": 1 } },
    { "key": 2, "value": { "type": "randomInt", "min": 0, "max": 1 } },
    { "key": 3, "value": { "type": "randomInt", "min": 1, "max": 200 } }
  ]
}
```

- [ ] **Step 6: Update user-facing documentation if requested**

If the user asks for docs beyond the spec, update `docs/flow-node-system.md` or the flow-config skill source with the new `map` binding example. Do not change skill files unless explicitly requested.

- [ ] **Step 7: Final checkpoint**

Report:

- Files changed.
- Tests run and exact results.
- Any skipped validation and why.
- Whether git commit was intentionally skipped due project git-management rules.

Do not commit unless the user explicitly asks. If a commit is requested, invoke `git-management` first.

---

## Self-Review Notes

- Spec coverage:
  - `type: "map"` model: Tasks 1 and 5.
  - Fixed key + dynamic value backend resolution: Tasks 1, 2, and 8.
  - Proto map reflection write: Tasks 3 and 4.
  - Frontend map display: Task 7.
  - Export cleanup and validation: Tasks 5 and 6.
  - Tests and verification: Tasks 1-9.
- Placeholder scan:
  - No implementation step is left as “TBD” or “TODO”. Task 8 includes an explicit warning not to leave the initial skeleton body; the step defines the exact required final body behavior.
- Type consistency:
  - Backend uses `BindMap`, `MapEntryBind`, `Entries`.
  - Frontend uses `BindingType = 'map'`, `MapEntryBind`, `entries`.
  - JSON shape matches the approved spec.
- Project rules:
  - Commit steps are replaced with checkpoints because project memory requires git operations to go through the `git-management` skill and the user has not asked for a commit.
