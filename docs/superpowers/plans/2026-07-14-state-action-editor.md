# State Action Editor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the generic setState/clearState controls with state-aware editors while preserving the existing JSON contract and preventing `id`/`index`/`account` from ever being cleared.

**Architecture:** Keep `ActionDef.bindings` and `ActionDef.keys` unchanged. Add dedicated React editors that consume one shared state-key registry/hook, keep the existing Collapse interaction, and pass registry readiness into flow validation. Add a shared Go state-action validator used both at flow-load time and at action execution time so protected-key rejection is atomic and cannot be bypassed with hand-written JSON.

**Tech Stack:** Go 1.x, React 18, TypeScript 5.6, Ant Design 5, Zustand, Vitest 2, Testing Library, jsdom.

---

## File map

### Backend

- Modify: `errcode/codes.go` — allocate and register `ErrStateConfig = 50`.
- Modify: `errcode/codes_test.go` — pin the new code/name and registry uniqueness.
- Create: `engine/state_action.go` — own protected state-key constants and shared clearState validation.
- Create: `engine/action_state_test.go` — test normal clear, protected keys, and no-partial-delete atomicity.
- Modify: `engine/action.go:754-760` — validate the whole key list before deleting.
- Modify: `cmd/agent/main.go:473-485` — reject invalid state actions while loading standalone flow files.
- Modify: `agent/task_runner.go:339-351` — reject the same invalid state actions in distributed mode.

### Frontend shared state model

- Modify: `cmd/web/package.json` and `cmd/web/package-lock.json` — add the existing project’s missing DOM component-test dependencies.
- Modify: `cmd/web/vite.config.ts` — configure Vitest jsdom setup.
- Create: `cmd/web/src/test/setup.ts` — browser API shims required by Ant Design.
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/stateRegistry.ts` — export built-in-key helpers and collect all setState/storeAs outputs.
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/stateRegistry.test.ts` — cover new registry sources and built-in precedence.
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/useStateKeyOptions.ts` — shared asynchronous registry hook with a readiness bit.
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/stateKeyPresentation.tsx` — one source/type option renderer used by all state selectors.
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/StateKeyInput.tsx` — consume the hook/presentation component instead of loading scripts itself.
- Modify: `cmd/web/src/components/FlowEditor/editors/shared/StateExprInput.tsx` — consume the same hook and shared labels.

### Frontend editors

- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/stateActionEditorModel.ts` — pure binding summaries, advanced counts, type-change pruning, movement, and clear-option merging.
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/stateActionEditorModel.test.ts` — exhaustive pure-model tests, including all 17 binding types.
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/BindingsTable.tsx` — export shared binding metadata/common advanced controls without changing existing pattern rendering.
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/BindingTypeForm.tsx` — expose primary/type-advanced sections while retaining `section="all"` as the existing default.
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/SetStateEditor.tsx` — Collapse summary-card editor for `bindings`.
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/SetStateEditor.test.tsx` — component behavior tests.
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/ClearStateEditor.tsx` — searchable multiple selector for known keys, with protected/unknown handling.
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/ClearStateEditor.test.tsx` — component behavior tests.
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/DeclarativeForm.tsx` — route state patterns to the dedicated editors.

### Frontend validation

- Modify: `cmd/web/src/components/FlowEditor/validation/refsCheck.ts` — add setState/clearState semantic rules and optional ready registry context.
- Modify: `cmd/web/src/components/FlowEditor/validation/refsCheck.test.ts` — test empty/duplicate/protected/unknown behavior.
- Modify: `cmd/web/src/components/FlowEditor/validation/ValidationReport.tsx` — pass shared registry state to `validateFlow`.
- Modify: `cmd/web/src/components/FlowEditor/panels/Toolbar.tsx` — pass the same registry state to the toolbar validation badge.

## Working-tree isolation rule

The repository already contains unrelated modified/deleted/untracked files. Before every commit in this plan:

```bash
git status --short
git diff --cached --name-only
```

Stage only the exact paths printed in each task’s commit step. Never run `git add .`, `git add -A`, checkout/reset unrelated files, or include the current guild/robot/work-pool work in a feature commit.

---

### Task 1: Add DOM component-test support

**Files:**
- Modify: `cmd/web/package.json`
- Modify: `cmd/web/package-lock.json`
- Modify: `cmd/web/vite.config.ts`
- Create: `cmd/web/src/test/setup.ts`
- Create: `cmd/web/src/test/smoke.test.tsx`

- [ ] **Step 1: Install the exact test dependencies**

Run:

```bash
npm --prefix cmd/web install --save-dev @testing-library/react@16.3.0 @testing-library/user-event@14.6.1 jsdom@26.1.0
```

Expected: `package.json` and `package-lock.json` change; no production dependency changes.

- [ ] **Step 2: Add a failing jsdom smoke test**

Create `cmd/web/src/test/smoke.test.tsx`:

```tsx
import { render, screen } from '@testing-library/react';
import { Button } from 'antd';
import { describe, expect, it } from 'vitest';

describe('component test environment', () => {
  it('renders Ant Design controls in jsdom', () => {
    render(<Button>状态编辑器</Button>);
    expect(screen.getByRole('button', { name: '状态编辑器' })).toBeTruthy();
  });
});
```

- [ ] **Step 3: Run the smoke test and verify the environment fails before setup**

Run:

```bash
npm --prefix cmd/web run test -- src/test/smoke.test.tsx
```

Expected: FAIL because the current Vitest environment is Node and `document` is undefined.

- [ ] **Step 4: Configure Vitest and browser shims**

Change the config imports so Vitest’s extended config type accepts `test`:

```ts
import { defineConfig } from 'vitest/config';
import { loadEnv } from 'vite';
```

Then add to the `defineConfig` object:

```ts
test: {
  environment: 'jsdom',
  setupFiles: ['./src/test/setup.ts'],
},
```

Create `cmd/web/src/test/setup.ts`:

```ts
import { afterEach, vi } from 'vitest';
import { cleanup } from '@testing-library/react';

afterEach(() => cleanup());

Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: vi.fn().mockImplementation((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  })),
});

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}

globalThis.ResizeObserver = ResizeObserverStub;
```

If TypeScript requires a structural cast for `ResizeObserverStub`, use:

```ts
globalThis.ResizeObserver = ResizeObserverStub as unknown as typeof ResizeObserver;
```

- [ ] **Step 5: Run the smoke test and type-check**

Run:

```bash
npm --prefix cmd/web run test -- src/test/smoke.test.tsx
npm --prefix cmd/web exec -- tsc -b
```

Expected: one Vitest file passes; TypeScript exits 0.

- [ ] **Step 6: Commit only test infrastructure**

```bash
git add -- cmd/web/package.json cmd/web/package-lock.json cmd/web/vite.config.ts cmd/web/src/test/setup.ts cmd/web/src/test/smoke.test.tsx
git commit -m "test(web): 添加状态编辑器组件测试环境

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 2: Protect built-in state keys in the backend

**Files:**
- Modify: `errcode/codes.go`
- Modify: `errcode/codes_test.go`
- Create: `engine/state_action.go`
- Create: `engine/action_state_test.go`
- Modify: `engine/action.go:754-760`
- Modify: `cmd/agent/main.go:473-485`
- Modify: `agent/task_runner.go:339-351`

- [ ] **Step 1: Write failing error-code tests**

Append to `errcode/codes_test.go`:

```go
func TestStateConfigCodeRegistered(t *testing.T) {
	if ErrStateConfig != 50 {
		t.Fatalf("ErrStateConfig=%d want 50", ErrStateConfig)
	}
	if got := ErrStateConfig.String(); got != "STATE_CONFIG" {
		t.Fatalf("ErrStateConfig.String()=%q want STATE_CONFIG", got)
	}
}

func TestAllCodesUnique(t *testing.T) {
	seen := make(map[uint64]string)
	for _, code := range AllCodes() {
		if previous, ok := seen[code.Code]; ok {
			t.Fatalf("框架错误码 %d 重复注册: %s / %s", code.Code, previous, code.Name)
		}
		seen[code.Code] = code.Name
	}
}
```

Run:

```bash
go test ./errcode -run 'TestStateConfigCodeRegistered|TestAllCodesUnique' -count=1
```

Expected: FAIL because `ErrStateConfig` is undefined.

- [ ] **Step 2: Allocate code 50 in the single registry source**

In `errcode/codes.go`, add to the configuration range:

```go
ErrStateConfig ErrorCode = 50 // 状态动作配置错误（如 clearState 清除内置状态）
```

Add to `codeRegistry` immediately after `ErrHeartbeatConfig`:

```go
{uint64(ErrStateConfig), "STATE_CONFIG"},
```

Run:

```bash
go test ./errcode -count=1
```

Expected: PASS.

- [ ] **Step 3: Write failing engine tests for shared validation and atomic execution**

Create `engine/action_state_test.go`:

```go
package engine

import (
	"context"
	"errors"
	"testing"

	"stressbot/errcode"
	"stressbot/state"
)

func TestValidateStateActionsRejectsProtectedClearKey(t *testing.T) {
	for _, key := range []string{"id", "index", "account"} {
		t.Run(key, func(t *testing.T) {
			flow := &TaskFlow{Actions: map[string]*ActionDef{
				"clear": {Pattern: PatternClearState, Keys: []string{"battleId", key}},
			}}
			if err := ValidateStateActions(flow); err == nil {
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
		Name: "clear",
		Pattern: PatternClearState,
		Keys: []string{"battleId", "id"},
	})
	if err == nil {
		t.Fatal("包含内置状态的 clearState 应失败")
	}
	var actionErr *ActionError
	if !errors.As(err, &actionErr) || actionErr.Code != errcode.ErrStateConfig {
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
		Name: "clear",
		Pattern: PatternClearState,
		Keys: []string{"battleId", "battleSession", "battleId"},
	})
	if err != nil {
		t.Fatalf("普通状态清除失败: %v", err)
	}
	if store.Has("battleId") || store.Has("battleSession") {
		t.Fatal("普通状态应全部删除")
	}
}
```

Run:

```bash
go test ./engine -run 'TestValidateStateActions|TestClearState' -count=1
```

Expected: FAIL because `ValidateStateActions` is undefined and execution still deletes `battleId` before seeing `id`.

- [ ] **Step 4: Implement one shared protected-key validator**

Create `engine/state_action.go`:

```go
package engine

import "fmt"

var protectedStateKeys = map[string]struct{}{
	"id": {},
	"index": {},
	"account": {},
}

// IsProtectedStateKey reports keys injected for every robot and required for its lifecycle.
func IsProtectedStateKey(key string) bool {
	_, ok := protectedStateKeys[key]
	return ok
}

func validateClearStateKeys(actionName string, keys []string) error {
	for _, key := range keys {
		if IsProtectedStateKey(key) {
			return fmt.Errorf("action %q clearState 不允许清除内置状态 %q", actionName, key)
		}
	}
	return nil
}

// ValidateStateActions checks state-action invariants immediately after flow decoding.
func ValidateStateActions(flow *TaskFlow) error {
	if flow == nil {
		return nil
	}
	for name, def := range flow.Actions {
		if def == nil || def.Pattern != PatternClearState {
			continue
		}
		if err := validateClearStateKeys(name, def.Keys); err != nil {
			return err
		}
	}
	return nil
}
```

Update `execClearState` before its loop:

```go
func (ae *ActionExecutor) execClearState(def *ActionDef) error {
	if err := validateClearStateKeys(def.Name, def.Keys); err != nil {
		return NewActionError(errcode.ErrStateConfig, "action="+def.Name, err)
	}
	for _, key := range def.Keys {
		ae.store.Delete(key)
	}
	stresslog.Debug("[ACTION] ClearState 成功", zap.String("action", def.Name), zap.Strings("keys", def.Keys))
	return nil
}
```

- [ ] **Step 5: Enforce the same validation in both flow loaders**

In both `loadFlow` and `loadTaskFlow`, after JSON unmarshal and before return, add:

```go
if err := engine.ValidateStateActions(flow); err != nil {
	return nil, fmt.Errorf("校验流程配置失败: %w", err)
}
```

This is deliberately explicit in both loaders; do not add an automatic migration or silently remove protected keys.

- [ ] **Step 6: Run focused and repository tests**

```bash
go test ./errcode ./engine ./agent ./cmd/agent -count=1
go build ./...
```

Expected: all tests and build pass.

- [ ] **Step 7: Commit only backend protection files**

```bash
git add -- errcode/codes.go errcode/codes_test.go engine/state_action.go engine/action_state_test.go engine/action.go cmd/agent/main.go agent/task_runner.go
git commit -m "fix(engine): 禁止清除机器人内置状态

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 3: Complete and centralize the state-key registry

**Files:**
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/stateRegistry.ts`
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/stateRegistry.test.ts`
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/useStateKeyOptions.ts`
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/stateKeyPresentation.tsx`
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/StateKeyInput.tsx`
- Modify: `cmd/web/src/components/FlowEditor/editors/shared/StateExprInput.tsx`

- [ ] **Step 1: Write failing registry tests for real state writers**

Append tests to `stateRegistry.test.ts`:

```ts
it('收集所有 setState 目标的顶层 key', () => {
  const actions: Record<string, ActionDef> = {
    setMatch: {
      pattern: 'setState',
      bindings: [
        { field: 'matchInfo.id', type: 'fixed', value: 1 },
        { field: 'rankedMatchStarted', type: 'fixed', value: true },
      ],
    },
  };

  const keys = collectStateKeys(actions, {}, undefined, undefined, undefined);
  expect(findKey(keys, 'matchInfo')).toMatchObject({ sourceType: 'setState', sourceName: 'setMatch' });
  expect(findKey(keys, 'rankedMatchStarted')).toMatchObject({ sourceType: 'setState', sourceName: 'setMatch' });
});

it('收集所有 action 的 storeAs，而非只收当前 bindings', () => {
  const actions: Record<string, ActionDef> = {
    login: {
      pattern: 'tcpSend', service: 'logic', route: {}, c2sProto: 'X.Login',
      bindings: [{ field: 'uid', type: 'fixed', value: 1, storeAs: 'resolvedUid' }],
    },
  };
  const keys = collectStateKeys(actions, {}, undefined, undefined, undefined);
  expect(findKey(keys, 'resolvedUid')).toMatchObject({ sourceType: 'storeAs', sourceName: 'login' });
});

it('任何流程写入都不能覆盖内置 key 的来源信息', () => {
  const actions: Record<string, ActionDef> = {
    bad: {
      pattern: 'setState',
      bindings: [{ field: 'id', type: 'fixed', value: 99, storeAs: 'account' }],
      store: [{ setter: 'index' }],
    },
  };
  const keys = collectStateKeys(actions, {}, undefined, undefined, undefined);
  for (const key of ['id', 'index', 'account']) {
    expect(findKey(keys, key)?.sourceType).toBe('builtin');
  }
});
```

Run:

```bash
npm --prefix cmd/web run test -- src/components/FlowEditor/editors/ActionEditor/stateRegistry.test.ts
```

Expected: FAIL because `setState` and global `storeAs` are not collected and current store mappings can replace built-ins.

- [ ] **Step 2: Export built-in helpers and add `setState` as a source**

Change the source union and exports in `stateRegistry.ts`:

```ts
export type StateKeySourceType =
  | 'store' | 'listenStore' | 'stateExtra' | 'storeAs' | 'setState' | 'lua' | 'builtin';

export interface StateKeyInfo {
  key: string;
  sourceType: StateKeySourceType;
  sourceName: string;
  s2cProto?: string;
  storeField?: string;
  builtinType?: string;
  builtinDesc?: string;
}

export const BUILTIN_STATE_KEYS = ['id', 'index', 'account'] as const;
export const BUILTIN_KEYS: StateKeyInfo[] = [
  { key: 'id', sourceType: 'builtin', sourceName: '内置', builtinType: 'int', builtinDesc: '机器人编号（= startNumber + index）' },
  { key: 'index', sourceType: 'builtin', sourceName: '内置', builtinType: 'int', builtinDesc: '任务全局序号（0-based，不含 startNumber 偏移）' },
  { key: 'account', sourceType: 'builtin', sourceName: '内置', builtinType: 'string', builtinDesc: '完整账号名（如 bot_100）' },
];

export function isBuiltinStateKey(key: string): boolean {
  return (BUILTIN_STATE_KEYS as readonly string[]).includes(key);
}
```

Inside `collectStateKeys`, use one helper that never overwrites built-ins:

```ts
const register = (info: StateKeyInfo) => {
  if (isBuiltinStateKey(info.key) && map.has(info.key)) return;
  if (!map.has(info.key)) map.set(info.key, info);
};
```

After store/listen collection, scan every action:

```ts
for (const [name, def] of Object.entries(actions)) {
  for (const binding of def.bindings ?? []) {
    if (binding.storeAs) {
      register({ key: binding.storeAs, sourceType: 'storeAs', sourceName: name });
    }
    if (def.pattern === 'setState' && binding.field) {
      const key = setterTopKey(binding.field);
      if (key) register({ key, sourceType: 'setState', sourceName: name });
    }
  }
}
```

Keep `currentBindings` collection for an action that has not yet been inserted into the store, but route it through `register`.

- [ ] **Step 3: Run registry tests**

```bash
npm --prefix cmd/web run test -- src/components/FlowEditor/editors/ActionEditor/stateRegistry.test.ts
```

Expected: PASS.

- [ ] **Step 4: Extract the asynchronous registry hook**

Create `useStateKeyOptions.ts` with this public contract:

```ts
import { useEffect, useMemo, useState } from 'react';
import type { FieldBind } from '@/types/action';
import { useRuntimeStore } from '@/services/runtimeStore';
import { getScript } from '@/services/resourcesStore';
import { useFlowStore } from '../../store/flowStore';
import { collectStateKeys, collectUsedScriptNames, type StateKeyInfo } from './stateRegistry';

export interface StateKeyOptionsResult {
  keys: StateKeyInfo[];
  ready: boolean;
}

export function useStateKeyOptions(currentBindings?: FieldBind[]): StateKeyOptionsResult {
  const actions = useFlowStore((s) => s.actions);
  const listens = useFlowStore((s) => s.listens);
  const nodes = useFlowStore((s) => s.nodes);
  const stateExtra = useRuntimeStore((s) => s.robotConfig.stateExtra);
  const scriptNames = useMemo(
    () => collectUsedScriptNames(actions, listens, nodes),
    [actions, listens, nodes],
  );
  const [scripts, setScripts] = useState<Array<{ name: string; content: string }>>([]);
  const [ready, setReady] = useState(scriptNames.size === 0);

  useEffect(() => {
    let cancelled = false;
    setReady(scriptNames.size === 0);
    if (scriptNames.size === 0) {
      setScripts([]);
      return () => { cancelled = true; };
    }
    Promise.all([...scriptNames].map(async (name) => {
      try {
        const file = await getScript(name);
        return file ? { name: file.name, content: file.content } : null;
      } catch {
        return null;
      }
    })).then((loaded) => {
      if (cancelled) return;
      setScripts(loaded.filter((it): it is { name: string; content: string } => it !== null));
      setReady(true);
    });
    return () => { cancelled = true; };
  }, [scriptNames]);

  const keys = useMemo(
    () => collectStateKeys(actions, listens, stateExtra, currentBindings, scripts),
    [actions, listens, stateExtra, currentBindings, scripts],
  );
  return { keys, ready };
}
```

- [ ] **Step 5: Centralize option presentation**

Create `stateKeyPresentation.tsx` exporting:

```tsx
export const STATE_SOURCE_LABEL = {
  store: { label: '响应', color: 'blue' },
  listenStore: { label: '推送', color: 'orange' },
  stateExtra: { label: '启动', color: 'volcano' },
  storeAs: { label: '中间值', color: 'green' },
  setState: { label: '状态动作', color: 'geekblue' },
  lua: { label: '脚本', color: 'purple' },
  builtin: { label: '内置', color: 'cyan' },
} satisfies Record<StateKeySourceType, { label: string; color: string }>;

export function stateKeyTypeLabel(info: StateKeyInfo): string | undefined {
  return info.builtinType ?? resolveStateKeyDisplayType(info);
}

export function StateKeyOptionLabel({ info }: { info: StateKeyInfo }) {
  const source = STATE_SOURCE_LABEL[info.sourceType];
  const type = stateKeyTypeLabel(info);
  return (
    <Space size={4}>
      <code style={{ fontSize: 12 }}>{info.key}</code>
      <Tag color={source.color} style={{ fontSize: 10, lineHeight: '16px', padding: '0 4px', marginRight: 0 }}>
        {source.label}
      </Tag>
      {type && <span style={{ fontSize: 10, color: 'var(--text-tertiary)' }}>← {type}</span>}
      {!['stateExtra', 'storeAs', 'builtin'].includes(info.sourceType) && (
        <span style={{ fontSize: 10, color: 'var(--text-tertiary)' }}>({info.sourceName})</span>
      )}
    </Space>
  );
}
```

- [ ] **Step 6: Refactor both existing state controls to the hook**

In `StateKeyInput`, remove direct flow/runtime/script loading and use:

```ts
const { keys: allKeys } = useStateKeyOptions(currentBindings);
```

Build labels with `<StateKeyOptionLabel info={k} />`.

In `StateExprInput`, replace its duplicate loading block with:

```ts
const { keys: allKeys } = useStateKeyOptions();
```

Use `STATE_SOURCE_LABEL` in `StateKeyRow`; update comments that still claim each component loads scripts itself.

- [ ] **Step 7: Run focused tests and type-check**

```bash
npm --prefix cmd/web run test -- src/components/FlowEditor/editors/ActionEditor/stateRegistry.test.ts
npm --prefix cmd/web exec -- tsc -b
```

Expected: PASS.

- [ ] **Step 8: Commit the registry/hook refactor**

```bash
git add -- cmd/web/src/components/FlowEditor/editors/ActionEditor/stateRegistry.ts cmd/web/src/components/FlowEditor/editors/ActionEditor/stateRegistry.test.ts cmd/web/src/components/FlowEditor/editors/ActionEditor/useStateKeyOptions.ts cmd/web/src/components/FlowEditor/editors/ActionEditor/stateKeyPresentation.tsx cmd/web/src/components/FlowEditor/editors/ActionEditor/StateKeyInput.tsx cmd/web/src/components/FlowEditor/editors/shared/StateExprInput.tsx
git commit -m "refactor(web): 统一状态候选数据源

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 4: Extract shared binding presentation and state-editor model

**Files:**
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/stateActionEditorModel.ts`
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/stateActionEditorModel.test.ts`
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/BindingsTable.tsx`
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/BindingTypeForm.tsx`

- [ ] **Step 1: Write failing pure-model tests**

Create `stateActionEditorModel.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import { ALL_BINDING_TYPES, type FieldBind } from '@/types/action';
import {
  SET_STATE_TYPE_GROUPS,
  bindingAdvancedCount,
  bindingValueSummary,
  changeBindingType,
  moveBinding,
} from './stateActionEditorModel';

describe('state action editor model', () => {
  it('取值方式完整覆盖且不重复 17 种 binding type', () => {
    const flattened = SET_STATE_TYPE_GROUPS.flatMap((group) => group.types);
    expect(new Set(flattened).size).toBe(ALL_BINDING_TYPES.length);
    expect([...flattened].sort()).toEqual([...ALL_BINDING_TYPES].sort());
  });

  it('摘要显示固定值和状态来源', () => {
    expect(bindingValueSummary({ type: 'fixed', value: true })).toBe('true');
    expect(bindingValueSummary({ type: 'state', source: 'matchInfo', path: 'id' })).toBe('matchInfo.id');
  });

  it('切换类型删除旧类型参数但保留目标和通用高级配置', () => {
    const before: FieldBind = {
      field: 'battleId', type: 'stateRandomN', source: 'matches', path: 'id', count: 2,
      required: true, condition: 'state:ready', storeAs: 'picked',
    };
    expect(changeBindingType(before, 'fixed')).toEqual({
      field: 'battleId', type: 'fixed', required: true, condition: 'state:ready', storeAs: 'picked',
    });
  });

  it('高级配置数量包含通用字段和类型高级字段', () => {
    expect(bindingAdvancedCount({
      type: 'stateRandom', source: 'items', path: 'id', filters: [{ op: 'eq', value: 1 }], optional: true,
    })).toBe(3);
  });

  it('移动条目保持不可变并守住边界', () => {
    const list: FieldBind[] = [{ type: 'fixed', field: 'a' }, { type: 'fixed', field: 'b' }];
    expect(moveBinding(list, 0, 1).map((b) => b.field)).toEqual(['b', 'a']);
    expect(moveBinding(list, 0, -1)).toBe(list);
  });
});
```

Run:

```bash
npm --prefix cmd/web run test -- src/components/FlowEditor/editors/ActionEditor/stateActionEditorModel.test.ts
```

Expected: FAIL because the model does not exist.

- [ ] **Step 2: Implement the pure model with explicit type ownership**

Create `stateActionEditorModel.ts`. The type groups must be:

```ts
export const SET_STATE_TYPE_GROUPS: Array<{ label: string; types: BindingType[] }> = [
  { label: '常用', types: ['fixed', 'state', 'randomInt', 'randomFloat', 'randomBool', 'randomString'] },
  { label: '从已有状态取值', types: ['stateFirst', 'stateRandom', 'stateRandomN', 'stateMapKey', 'stateMapValue', 'listSize'] },
  { label: '从候选值取值', types: ['randomPick', 'randomPickN', 'randomPickMap', 'randomExclude'] },
  { label: '复合值', types: ['map'] },
];
```

Define common keys that survive a type change:

```ts
const COMMON_BINDING_KEYS = ['field', 'required', 'optional', 'wrap', 'storeAs', 'condition'] as const;

export function changeBindingType(binding: FieldBind, type: BindingType): FieldBind {
  const next: FieldBind = { type };
  for (const key of COMMON_BINDING_KEYS) {
    const value = binding[key];
    if (value !== undefined) (next as Record<string, unknown>)[key] = value;
  }
  return next;
}
```

Implement summaries without implicit object-to-string conversion:

```ts
export function bindingValueSummary(binding: FieldBind): string {
  switch (binding.type) {
    case 'fixed':
      return typeof binding.value === 'string' ? binding.value : JSON.stringify(binding.value) ?? '未设置';
    case 'state': case 'stateFirst': case 'stateRandom': case 'stateRandomN':
    case 'stateMapKey': case 'stateMapValue': case 'listSize':
      return [binding.source, binding.path].filter(Boolean).join('.') || '未选择来源';
    case 'randomInt': case 'randomFloat':
      return `${binding.min ?? '?'} ~ ${binding.max ?? '?'}`;
    case 'randomBool':
      return '每次随机 true / false';
    case 'randomString':
      return `长度 ${binding.length ?? '?'}`;
    case 'randomPick': case 'randomPickN': case 'randomPickMap': case 'randomExclude':
      return `${binding.values?.length ?? 0} 个候选值`;
    case 'map':
      return `${binding.entries?.length ?? 0} 个键值对`;
  }
}
```

`bindingAdvancedCount` counts each configured common advanced field (`required`, `optional`, `wrap`, `storeAs`, `condition`) plus `path`, non-empty `filters`, `excludeSource`, `keySource`, and non-empty `entries` as one configured item each. `moveBinding` returns the original array for out-of-range moves and a copied/reordered array for valid moves.

- [ ] **Step 3: Export shared type metadata and common controls**

From `BindingsTable.tsx`, export:

```ts
export const BINDING_TYPE_DESC: Record<BindingType, string> = { /* keep the existing complete map */ };
export const BINDING_TYPE_OPTIONS = TYPE_GROUPS.map(/* keep current grouping */);
```

Extract the existing required/optional/wrap/storeAs/condition JSX into:

```tsx
export function BindingCommonAdvancedFields({
  binding,
  onChange,
}: {
  binding: FieldBind;
  onChange: (binding: FieldBind) => void;
}) { /* move the existing controls here unchanged */ }
```

Have the existing `BindingRow` render this exported component in the same location so non-state patterns do not change behavior or appearance.

- [ ] **Step 4: Split type-specific primary and advanced rendering**

Change `BindingTypeFormProps`:

```ts
export interface BindingTypeFormProps {
  binding: FieldBind;
  currentBindings?: FieldBind[];
  valueOnly?: boolean;
  section?: 'all' | 'primary' | 'advanced';
  onChange: (b: FieldBind) => void;
}
```

Keep `section = 'all'` as default. Implement the split with these exact responsibilities:

| Type | Primary section | Type-advanced section |
|---|---|---|
| fixed | fixed value | none |
| state/stateFirst | source | path |
| stateRandom | source | path + filters |
| stateRandomN | source + count | path + filters |
| stateMapKey | source | filters |
| stateMapValue | source | path + filters |
| randomPick | values | none |
| randomPickN | values + count | none |
| randomPickMap | keySource + mapping table | none |
| randomInt | min/max | none |
| randomFloat | min/max/precision | none |
| randomBool | no-parameter hint | none |
| randomString | length/charset | none |
| randomExclude | values/source | excludeSource |
| listSize | source | none |
| map | entries | none |

The default composition must preserve current callers:

```tsx
if (section === 'primary') return <BindingPrimaryFields ... />;
if (section === 'advanced') return <BindingTypeAdvancedFields ... />;
return (
  <Space direction="vertical" style={{ width: '100%' }} size="small">
    <BindingPrimaryFields ... />
    <BindingTypeAdvancedFields ... />
  </Space>
);
```

Do not remove any binding type or alter value semantics.

- [ ] **Step 5: Run model, existing action-editor tests, and type-check**

```bash
npm --prefix cmd/web run test -- src/components/FlowEditor/editors/ActionEditor
npm --prefix cmd/web exec -- tsc -b
```

Expected: PASS, including existing `actionPrune`, `BindingPreview`, random-string, and registry tests.

- [ ] **Step 6: Commit the shared binding/model layer**

```bash
git add -- cmd/web/src/components/FlowEditor/editors/ActionEditor/stateActionEditorModel.ts cmd/web/src/components/FlowEditor/editors/ActionEditor/stateActionEditorModel.test.ts cmd/web/src/components/FlowEditor/editors/ActionEditor/BindingsTable.tsx cmd/web/src/components/FlowEditor/editors/ActionEditor/BindingTypeForm.tsx
git commit -m "refactor(web): 拆分状态绑定的基础与高级配置

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 5: Build the setState summary-card editor

**Files:**
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/SetStateEditor.tsx`
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/SetStateEditor.test.tsx`

- [ ] **Step 1: Write failing component tests**

Create `SetStateEditor.test.tsx` with a small wrapper that stores `bindings` in React state. Cover these behaviors:

```tsx
it('摘要显示目标状态、取值方式和值', () => {
  render(<Harness initial={[{ field: 'battleId', type: 'state', source: 'matchInfo', path: 'id' }]} />);
  expect(screen.getByText('battleId')).toBeTruthy();
  expect(screen.getByText('state')).toBeTruthy();
  expect(screen.getByText('matchInfo.id')).toBeTruthy();
});

it('添加状态创建空目标的 fixed binding', async () => {
  const onChange = vi.fn();
  render(<SetStateEditor value={[]} onChange={onChange} />);
  await userEvent.click(screen.getByRole('button', { name: /添加状态/ }));
  expect(onChange).toHaveBeenLastCalledWith([{ type: 'fixed', field: '' }]);
});

it('可输入候选中不存在的新状态名称', async () => {
  render(<Harness initial={[{ field: '', type: 'fixed', value: 1 }]} />);
  await userEvent.click(screen.getByText('(未指定目标状态)'));
  const target = screen.getByPlaceholderText('选择已有状态或输入新名称');
  await userEvent.clear(target);
  await userEvent.type(target, 'newBattleState');
  expect(screen.getByText('新状态')).toBeTruthy();
});

it('已配置高级字段时显示标签和配置数量', () => {
  render(<Harness initial={[{
    field: 'battleId', type: 'state', source: 'matchInfo', path: 'id', required: true, condition: 'state:ready',
  }]} />);
  expect(screen.getByText('required')).toBeTruthy();
  expect(screen.getByText(/高级设置（3）/)).toBeTruthy();
});
```

Mock `useStateKeyOptions` to return deterministic keys; do not depend on IndexedDB in this component test.

Run:

```bash
npm --prefix cmd/web run test -- src/components/FlowEditor/editors/ActionEditor/SetStateEditor.test.tsx
```

Expected: FAIL because `SetStateEditor` does not exist.

- [ ] **Step 2: Implement the editor shell and Collapse summaries**

Public props:

```ts
export interface SetStateEditorProps {
  value?: FieldBind[];
  onChange: (bindings: FieldBind[]) => void;
}
```

The header must read `设置状态 (N)` and provide `添加状态`. Build each Collapse label from:

```tsx
<Space wrap>
  <code>{binding.field || '(未指定目标状态)'}</code>
  <Tooltip title={BINDING_TYPE_DESC[binding.type]}><Tag color="blue">{binding.type}</Tag></Tooltip>
  <span style={{ color: 'var(--text-tertiary)' }}>{bindingValueSummary(binding)}</span>
  {binding.required && <Tag color="red">required</Tag>}
  {binding.optional && <Tag>optional</Tag>}
  {binding.wrap && <Tag color="purple">wrap</Tag>}
  {binding.storeAs && <Tag color="green">→ {binding.storeAs}</Tag>}
  {binding.condition && <Tag color="orange">condition</Tag>}
</Space>
```

Use the same up/down/delete button structure as `BindingsTable` and `moveBinding` for ordering.

- [ ] **Step 3: Implement target-state and type/value editing**

Inside each expanded card:

```tsx
<Form.Item label="目标状态" extra={isNew ? '运行时将创建这个新状态' : undefined}>
  <StateKeyInput
    value={binding.field}
    onChange={(field) => update({ ...binding, field })}
    currentBindings={list}
    placeholder="选择已有状态或输入新名称"
    style={{ width: '100%' }}
  />
  {isNew && <Tag color="green">新状态</Tag>}
</Form.Item>
<Form.Item label="取值方式">
  <Select
    value={binding.type}
    options={SET_STATE_TYPE_GROUPS.map(/* grouped options using BINDING_TYPE_DESC */)}
    onChange={(type: BindingType) => update(changeBindingType(binding, type))}
  />
</Form.Item>
<BindingTypeForm section="primary" binding={binding} currentBindings={list} onChange={update} />
```

Determine `isNew` by comparing the top-level portion of `binding.field` with `useStateKeyOptions(list).keys`; `id/index/account` therefore count as existing. Do not auto-create, rename, or migrate keys.

- [ ] **Step 4: Implement the advanced Collapse**

Render only when the type has advanced controls or common advanced controls are possible:

```tsx
<Collapse
  size="small"
  items={[{
    key: 'advanced',
    label: `高级设置（${bindingAdvancedCount(binding)}）`,
    children: (
      <Space direction="vertical" style={{ width: '100%' }} size="middle">
        <BindingTypeForm section="advanced" binding={binding} currentBindings={list} onChange={update} />
        <BindingCommonAdvancedFields binding={binding} onChange={update} />
      </Space>
    ),
  }]}
/>
```

Do not hide configured advanced fields: count and summary tags remain visible while collapsed.

- [ ] **Step 5: Run component tests and type-check**

```bash
npm --prefix cmd/web run test -- src/components/FlowEditor/editors/ActionEditor/SetStateEditor.test.tsx src/components/FlowEditor/editors/ActionEditor/stateActionEditorModel.test.ts
npm --prefix cmd/web exec -- tsc -b
```

Expected: PASS.

- [ ] **Step 6: Commit the setState editor**

```bash
git add -- cmd/web/src/components/FlowEditor/editors/ActionEditor/SetStateEditor.tsx cmd/web/src/components/FlowEditor/editors/ActionEditor/SetStateEditor.test.tsx
git commit -m "feat(web): 添加状态写入专用编辑器

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 6: Build the clearState known-key multi-selector

**Files:**
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/ClearStateEditor.tsx`
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/ClearStateEditor.test.tsx`

- [ ] **Step 1: Write failing protected/unknown/component tests**

Mock `useStateKeyOptions` with `id`, `index`, `account`, `battleId`, and `battleSession`. Tests must cover:

```tsx
it.each(['id', 'index', 'account'])('内置状态 %s 可见但禁用', async (key) => {
  render(<Harness initial={[]} />);
  await userEvent.click(screen.getByRole('combobox'));
  expect(screen.getByText(key).closest('[aria-disabled="true"]')).toBeTruthy();
});

it('批量选择保持选择顺序且不产生重复项', async () => {
  const onChange = vi.fn();
  const user = userEvent.setup();
  render(<ClearStateEditor value={[]} onChange={onChange} />);
  await user.click(screen.getByRole('combobox'));
  await user.click(await screen.findByTitle('battleSession'));
  await user.click(screen.getByRole('combobox'));
  await user.click(await screen.findByTitle('battleId'));
  expect(onChange).toHaveBeenLastCalledWith(['battleSession', 'battleId']);
});

it('保留并标记导入的未知 key', () => {
  render(<ClearStateEditor value={['legacyBattle']} onChange={vi.fn()} />);
  expect(screen.getByText('legacyBattle')).toBeTruthy();
  expect(screen.getByText('当前流程未识别')).toBeTruthy();
});

it('未知 key 可移除但不出现在候选项中', async () => {
  const onChange = vi.fn();
  render(<ClearStateEditor value={['legacyBattle']} onChange={onChange} />);
  await userEvent.click(screen.getByLabelText('移除 legacyBattle'));
  expect(onChange).toHaveBeenLastCalledWith([]);
  await userEvent.click(screen.getByRole('combobox'));
  expect(screen.queryByRole('option', { name: /legacyBattle/ })).toBeNull();
});
```

Give each option wrapper `title={info.key}` in the mocked/real label so the test uses the stable `findByTitle` queries shown above.

Run:

```bash
npm --prefix cmd/web run test -- src/components/FlowEditor/editors/ActionEditor/ClearStateEditor.test.tsx
```

Expected: FAIL because the component does not exist.

- [ ] **Step 2: Implement known/protected/unknown option modeling**

Public props:

```ts
export interface ClearStateEditorProps {
  value?: string[];
  onChange: (keys: string[]) => void;
}
```

Use `useStateKeyOptions()` and derive:

```ts
const knownByKey = new Map(keys.map((info) => [info.key, info]));
const unknownSelected = selected.filter((key) => !knownByKey.has(key));
const options = keys.map((info) => ({
  value: info.key,
  disabled: isBuiltinStateKey(info.key),
  label: <StateKeyOptionLabel info={info} />,
}));
```

Use `mode="multiple"`, not `mode="tags"`, so arbitrary creation is impossible. Keep selection order from the Select callback and defensively deduplicate with:

```ts
onChange([...new Set(nextKeys)])
```

- [ ] **Step 3: Implement visible protected and unknown states**

- Built-ins remain in the dropdown with `disabled: true` and the suffix `内置状态不可清除`.
- Unknown imported values remain selected through custom `tagRender`; their tag contains `当前流程未识别` and a close control with `aria-label={`移除 ${value}`}`.
- Unknown values are not appended to normal selectable options.
- When `ready` is false, show `正在加载状态列表…` and do not label selected values unknown yet.
- When `ready` is true and there are no non-built-in candidates, render `当前流程没有可清除的状态` below the Select; do not switch to free text.

- [ ] **Step 4: Run component tests and type-check**

```bash
npm --prefix cmd/web run test -- src/components/FlowEditor/editors/ActionEditor/ClearStateEditor.test.tsx
npm --prefix cmd/web exec -- tsc -b
```

Expected: PASS.

- [ ] **Step 5: Commit the clearState editor**

```bash
git add -- cmd/web/src/components/FlowEditor/editors/ActionEditor/ClearStateEditor.tsx cmd/web/src/components/FlowEditor/editors/ActionEditor/ClearStateEditor.test.tsx
git commit -m "feat(web): 添加状态清理专用编辑器

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 7: Add semantic flow validation for state actions

**Files:**
- Modify: `cmd/web/src/components/FlowEditor/validation/refsCheck.ts`
- Modify: `cmd/web/src/components/FlowEditor/validation/refsCheck.test.ts`
- Modify: `cmd/web/src/components/FlowEditor/validation/ValidationReport.tsx`
- Modify: `cmd/web/src/components/FlowEditor/panels/Toolbar.tsx`

- [ ] **Step 1: Write failing validation tests**

Add a helper in `refsCheck.test.ts`:

```ts
const stateContext = (keys: string[]) => ({
  stateKeys: keys.map((key) => ({ key, sourceType: 'setState' as const, sourceName: 'test' })),
  stateKeysReady: true,
});
```

Add tests:

```ts
it('SETSTATE_TARGET_MISSING：setState 空目标是 error', () => {
  const r = validateFlow(baseFlow({ actions: { A1: {
    pattern: 'setState', bindings: [{ type: 'fixed', value: 1 }],
  } } }));
  expect(r.errors.find((e) => e.code === 'SETSTATE_TARGET_MISSING')).toBeTruthy();
});

it('SETSTATE_TARGET_DUPLICATE：后项覆盖前项时 warning', () => {
  const r = validateFlow(baseFlow({ actions: { A1: {
    pattern: 'setState', bindings: [
      { field: 'battleId', type: 'fixed', value: 1 },
      { field: 'battleId', type: 'fixed', value: 2 },
    ],
  } } }));
  expect(r.warnings.find((e) => e.code === 'SETSTATE_TARGET_DUPLICATE')).toBeTruthy();
});

it.each(['id', 'index', 'account'])('CLEARSTATE_PROTECTED_KEY：%s 是 error', (key) => {
  const r = validateFlow(baseFlow({ actions: { A1: { pattern: 'clearState', keys: ['battleId', key] } } }), stateContext(['battleId']));
  expect(r.errors.find((e) => e.code === 'CLEARSTATE_PROTECTED_KEY')).toBeTruthy();
});

it('CLEARSTATE_DUPLICATE_KEY：导入重复 key 时 warning', () => {
  const r = validateFlow(baseFlow({ actions: { A1: { pattern: 'clearState', keys: ['battleId', 'battleId'] } } }), stateContext(['battleId']));
  expect(r.warnings.find((e) => e.code === 'CLEARSTATE_DUPLICATE_KEY')).toBeTruthy();
});

it('CLEARSTATE_UNKNOWN_KEY：注册表 ready 后未知 key warning', () => {
  const r = validateFlow(baseFlow({ actions: { A1: { pattern: 'clearState', keys: ['legacyBattle'] } } }), stateContext(['battleId']));
  expect(r.warnings.find((e) => e.code === 'CLEARSTATE_UNKNOWN_KEY')).toBeTruthy();
});

it('状态注册表未 ready 时不误报未知 key', () => {
  const r = validateFlow(baseFlow({ actions: { A1: { pattern: 'clearState', keys: ['scriptState'] } } }), { stateKeys: [], stateKeysReady: false });
  expect(r.warnings.find((e) => e.code === 'CLEARSTATE_UNKNOWN_KEY')).toBeFalsy();
});
```

Run:

```bash
npm --prefix cmd/web run test -- src/components/FlowEditor/validation/refsCheck.test.ts
```

Expected: FAIL because `validateFlow` has no context and the new codes are absent.

- [ ] **Step 2: Add an optional validation context and semantic checks**

In `refsCheck.ts`:

```ts
export interface FlowValidationContext {
  stateKeys?: StateKeyInfo[];
  stateKeysReady?: boolean;
}

export function validateFlow(flow: TaskFlow, context: FlowValidationContext = {}): ValidationReport {
  // existing setup
  const knownStateKeys = context.stateKeysReady
    ? new Set((context.stateKeys ?? []).map((info) => info.key))
    : undefined;
  // pass knownStateKeys into checkAction
}
```

In `checkAction`, before generic `checkBindings`:

```ts
if (p === 'setState' && def.bindings) {
  const firstByTarget = new Map<string, number>();
  def.bindings.forEach((binding, index) => {
    const target = binding.field?.trim();
    if (!target) {
      issues.push({ severity: 'error', code: 'SETSTATE_TARGET_MISSING', message: `action "${name}" 的第 ${index + 1} 条状态写入缺少目标状态`, location: loc });
      return;
    }
    const previous = firstByTarget.get(target);
    if (previous !== undefined) {
      issues.push({ severity: 'warning', code: 'SETSTATE_TARGET_DUPLICATE', message: `action "${name}" 的第 ${index + 1} 条写入会覆盖第 ${previous + 1} 条对状态 "${target}" 的写入`, location: loc });
    } else {
      firstByTarget.set(target, index);
    }
  });
}

if (p === 'clearState' && def.keys) {
  const seen = new Set<string>();
  for (const key of def.keys) {
    if (isBuiltinStateKey(key)) {
      issues.push({ severity: 'error', code: 'CLEARSTATE_PROTECTED_KEY', message: `action "${name}" 不允许清除内置状态 "${key}"`, location: loc });
    }
    if (seen.has(key)) {
      issues.push({ severity: 'warning', code: 'CLEARSTATE_DUPLICATE_KEY', message: `action "${name}" 重复清除状态 "${key}"`, location: loc });
    }
    seen.add(key);
    if (knownStateKeys && !knownStateKeys.has(key) && !isBuiltinStateKey(key)) {
      issues.push({ severity: 'warning', code: 'CLEARSTATE_UNKNOWN_KEY', message: `action "${name}" 要清除的状态 "${key}" 当前流程未识别`, location: loc });
    }
  }
}
```

For setState, suppress the old generic `BINDING_NO_FIELD` warning when `p === 'setState'`; the dedicated error replaces it. Pass an option into `checkBindings` rather than filtering issues afterward:

```ts
checkBindings(prefix, bindings, loc, false, { requireTargetField: p !== 'setState' })
```

- [ ] **Step 3: Feed the ready registry to both validation UIs**

In both `ValidationReport.tsx` and `Toolbar.tsx`:

```ts
const { keys: stateKeys, ready: stateKeysReady } = useStateKeyOptions();
const validation = useMemo(
  () => validateFlow(flowSnapshot, { stateKeys, stateKeysReady }),
  [flowSnapshot, routeKeyTemplatesVersion, stateKeys, stateKeysReady],
);
```

Use the local variable names already present in each component (`flow` vs `flowSnap`) rather than introducing a second snapshot. Do not call hooks inside `useMemo`.

- [ ] **Step 4: Run validation tests and type-check**

```bash
npm --prefix cmd/web run test -- src/components/FlowEditor/validation/refsCheck.test.ts
npm --prefix cmd/web exec -- tsc -b
```

Expected: PASS.

- [ ] **Step 5: Commit validation changes**

```bash
git add -- cmd/web/src/components/FlowEditor/validation/refsCheck.ts cmd/web/src/components/FlowEditor/validation/refsCheck.test.ts cmd/web/src/components/FlowEditor/validation/ValidationReport.tsx cmd/web/src/components/FlowEditor/panels/Toolbar.tsx
git commit -m "feat(web): 校验状态写入与清理配置

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 8: Wire dedicated editors into DeclarativeForm

**Files:**
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/DeclarativeForm.tsx`
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/DeclarativeForm.test.tsx`

- [ ] **Step 1: Write failing pattern-routing tests**

Mock codec hooks and child editors so the test only checks routing:

```tsx
it('setState 只渲染 SetStateEditor，不渲染通用 BindingsTable', () => {
  render(<DeclarativeForm action={{ pattern: 'setState', bindings: [] }} onChange={vi.fn()} />);
  expect(screen.getByTestId('set-state-editor')).toBeTruthy();
  expect(screen.queryByTestId('bindings-table')).toBeNull();
});

it('clearState 渲染 ClearStateEditor，不渲染自由 tags 输入', () => {
  render(<DeclarativeForm action={{ pattern: 'clearState', keys: [] }} onChange={vi.fn()} />);
  expect(screen.getByTestId('clear-state-editor')).toBeTruthy();
  expect(screen.queryByPlaceholderText('输入 state key，回车确认')).toBeNull();
});

it('tcpSend 仍使用通用 BindingsTable', () => {
  render(<DeclarativeForm action={{ pattern: 'tcpSend', service: 'logic', route: {}, c2sProto: 'X.Foo', bindings: [] }} onChange={vi.fn()} />);
  expect(screen.getByTestId('bindings-table')).toBeTruthy();
});
```

Use module mocks to give the child editors the stated test IDs; do not add test-only IDs to production components solely for this routing test.

Run:

```bash
npm --prefix cmd/web run test -- src/components/FlowEditor/editors/ActionEditor/DeclarativeForm.test.tsx
```

Expected: FAIL because setState still routes through `BindingsTable` and clearState still uses tags Select.

- [ ] **Step 2: Replace the two state-pattern branches**

Import the editors and render before the generic bindings section:

```tsx
{pattern === 'setState' && (
  <div style={{ marginBottom: 24 }}>
    <SetStateEditor value={action.bindings} onChange={(bindings) => set({ bindings })} />
  </div>
)}
{pattern === 'clearState' && (
  <div style={{ marginBottom: 24 }}>
    <ClearStateEditor value={action.keys} onChange={(keys) => set({ keys })} />
  </div>
)}
```

Change the generic condition to exclude setState:

```tsx
{showBindings && pattern !== 'setState' && (/* existing BindingsTable unchanged */)}
```

Delete `showKeys` and the old `Select mode="tags"` block. Keep `setState: ['bindings']` in `patternHas`; it remains the data contract. Keep pattern topbar, preview, node-level fields, and listen registration untouched.

- [ ] **Step 3: Run routing, editor, and serialization tests**

```bash
npm --prefix cmd/web run test -- src/components/FlowEditor/editors/ActionEditor src/components/FlowEditor/codec/codec.test.ts
npm --prefix cmd/web exec -- tsc -b
```

Expected: PASS; flow serialization still emits unchanged `bindings` and `keys` JSON.

- [ ] **Step 4: Commit the integration**

```bash
git add -- cmd/web/src/components/FlowEditor/editors/ActionEditor/DeclarativeForm.tsx cmd/web/src/components/FlowEditor/editors/ActionEditor/DeclarativeForm.test.tsx
git commit -m "feat(web): 接入状态动作专用编辑界面

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 9: Full verification and browser acceptance

**Files:**
- Modify only if verification finds a feature-local defect; do not touch unrelated working-tree files.

- [ ] **Step 1: Run formatting/diff hygiene checks**

```bash
git diff --check
git status --short
```

Expected: no whitespace errors. Confirm unrelated pre-existing files are still present and unstaged.

- [ ] **Step 2: Run complete backend verification**

```bash
go test ./...
go vet ./...
go build ./...
```

Expected: all exit 0. If unrelated pre-existing work causes a failure, record the exact package/output and rerun feature-focused commands (`go test ./engine ./errcode ./agent ./cmd/agent`) without altering unrelated files.

- [ ] **Step 3: Run complete frontend verification**

```bash
npm --prefix cmd/web exec -- tsc -b
npm --prefix cmd/web run test
npm --prefix cmd/web run build
```

Expected: TypeScript passes, all Vitest files pass, and Vite builds `cmd/web/dist`.

- [ ] **Step 4: Verify real FlowEditor behavior in the browser**

Start the frontend if not already running:

```bash
npm --prefix cmd/web run dev -- --host 127.0.0.1
```

Using the browser, open a real setState node and verify:

1. The full existing 720×560 node window remains intact.
2. Summary cards match the approved B layout.
3. Existing state selection shows source/type.
4. Typing a new target shows `新状态`.
5. All 17 binding types are reachable.
6. Configured advanced values remain visible through count/tags when collapsed.
7. Reorder/delete updates preview JSON without changing field names.

Open a real clearState node and verify:

1. Multiple existing keys can be selected in order.
2. `id`, `index`, `account` are visible, disabled, and annotated.
3. Unknown imported keys remain visible and removable.
4. No free-text key creation is possible.
5. At 400px minimum width there is no horizontal overflow; labels wrap rather than clip.

- [ ] **Step 5: Verify validation and backend protection**

Import or temporarily paste configurations for these cases, then inspect the validation report:

```json
{"pattern":"setState","bindings":[{"type":"fixed","value":1}]}
{"pattern":"setState","bindings":[{"field":"x","type":"fixed","value":1},{"field":"x","type":"fixed","value":2}]}
{"pattern":"clearState","keys":["legacyBattle"]}
{"pattern":"clearState","keys":["battleId","battleId"]}
{"pattern":"clearState","keys":["battleId","id"]}
```

Expected: missing target error, duplicate target warning, unknown warning, duplicate clear warning, and protected-key error respectively. Run the backend’s focused atomicity test again:

```bash
go test ./engine -run TestClearStateProtectedKeyIsAtomic -count=1
```

Expected: PASS.

- [ ] **Step 6: Review comments and user-facing terminology**

Search feature files for stale labels:

```bash
rg -n 'bindings（State|keys（要清除|输入 state key|StateStore|Lua' \
  cmd/web/src/components/FlowEditor/editors/ActionEditor \
  cmd/web/src/components/FlowEditor/validation
```

Expected: no stale setState/clearState UI text. Technical identifiers may remain in code/tests, but user-facing labels use `状态`, `目标状态`, `取值方式`, and `选择要清除的状态`.

- [ ] **Step 7: Commit any verification-only fixes explicitly**

If no fixes were required, do not create an empty commit. If fixes were required, first inspect `git diff --name-only`, then stage only the feature files that the inspection lists (never `git add .`):

```bash
git diff --name-only
git add -- path/to/first-feature-file path/to/second-feature-file
git commit -m "fix(web): 修正状态动作编辑器验收问题

Co-Authored-By: Claude <noreply@anthropic.com>"
```

Replace the two illustrative `path/to/...` arguments with the exact feature paths from `git diff --name-only`; if any listed path belongs to unrelated pre-existing work, leave it unstaged.

- [ ] **Step 8: Report final status without pushing**

Report:

- commits created;
- automated verification results;
- browser acceptance results;
- any skipped live runtime test and why;
- unrelated working-tree changes still untouched.

Do not push unless the user explicitly requests it.
