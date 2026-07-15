# State-Key Options Dedup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate redundant async Lua-script loading across state-key consumers by introducing a shared `StateKeyOptionsProvider`, and migrate the last self-loading consumer (`SetterPathInput`) onto the shared hook — removing its now-vestigial prop-passing chain.

**Architecture:** The expensive part of state-key collection is the async `getScript` IDB read loop; `collectStateKeys` itself is cheap and synchronous. A `StateKeyOptionsProvider` mounted once near the top of the FlowEditor tree runs that load exactly once and shares `{ scripts, ready }` via React context. `useStateKeyOptions(currentBindings?)` reads scripts from context when a provider is present and otherwise self-loads (graceful fallback, so the hook still works in isolation/tests). Consumers that pass `currentBindings` (SetStateEditor, BindingTypeForm) still recompute their own `keys` cheaply over the shared scripts. `SetterPathInput` then drops its `actions/listens/stateExtra/luaScripts` props (always store-derived at its callers) and its duplicate loader, consuming `useStateKeyOptions()` like its siblings.

**Tech Stack:** React 18, TypeScript 5.6, Zustand, Ant Design 5, Vitest 2 + Testing Library + jsdom.

## Global Constraints

- No change to `ActionDef.bindings`/`keys` JSON contract or to `collectStateKeys`/`collectUsedScriptNames` signatures.
- No behavior change to existing state-key suggestions for DeclarativeForm/ListenEditor consumers. ONE intentional, flagged change: `TemplateEditorDrawer`'s setter browse goes from empty → showing the flow's known state keys (it previously passed no data props), which is an improvement, not a regression.
- `useStateKeyOptions(currentBindings?)` public signature and `{ keys, ready }` return shape are UNCHANGED — all existing callers (StateKeyInput, StateExprInput, SetStateEditor, ClearStateEditor, ValidationReport, Toolbar) keep working unmodified.
- The provider is OPTIONAL: `useStateKeyOptions` must still work with no provider above it (self-load fallback). Existing component tests mock the hook directly and must keep passing.
- UI text rule still holds: no leaking technical terms (SetterPathInput already uses shared `STATE_SOURCE_LABEL` from the prior fix).
- Working-tree isolation: the repo has unrelated dirty items (modified `conf/flow/flow.json`, `conf/config.json`; untracked `.playwright-mcp/`, `.superpowers/`, `*.png`, `agent-v2.puml`, `conf/scripts/listen_guild_update.lua`). Stage ONLY each task's listed files. Commit on master (user rule: no self-branching).

## File map

- Rename + Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/useStateKeyOptions.ts` → `useStateKeyOptions.tsx` (now contains JSX for the provider). Adds `StateKeyOptionsContext`, `StateKeyOptionsProvider`, internal `useLoadedStateKeyScripts(enabled)`, and refactors `useStateKeyOptions` to read context-with-fallback.
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/StateKeyOptionsContext.test.tsx` — dedup + fallback tests.
- Modify: `cmd/web/src/components/FlowEditor/index.tsx` — mount `<StateKeyOptionsProvider>` inside `FlowReadOnlyContext.Provider`, wrapping the editor tree.
- Modify: `cmd/web/src/components/FlowEditor/editors/shared/SetterPathInput.tsx` — drop data props + duplicate loader; consume `useStateKeyOptions()`.
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/StoreTable.tsx` — drop `actions/listens/stateExtra/luaScripts` props + their passthrough to SetterPathInput.
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/DeclarativeForm.tsx` — drop `allActions`/`allListens`/`stateExtra` subscriptions + the props passed to StoreTable.
- Modify: `cmd/web/src/components/FlowEditor/listens/ListenEditor.tsx` — drop `allActions`/`allListens`/`stateExtra` subscriptions + the props passed to StoreTable.
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/StoreTable.test.tsx` — StoreTable renders a setter row and onChange fires after the prop removal (useStateKeyOptions mocked).

---

### Task 1: StateKeyOptionsProvider + context-aware useStateKeyOptions

**Files:**
- Rename + Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/useStateKeyOptions.ts` → `useStateKeyOptions.tsx`
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/StateKeyOptionsContext.test.tsx`
- Modify: `cmd/web/src/components/FlowEditor/index.tsx` (mount the provider in `FlowEditorInner`)

**Interfaces:**
- Produces: `StateKeyOptionsProvider({ children }: { children: ReactNode })` (named export from `useStateKeyOptions`); `useStateKeyOptions(currentBindings?: FieldBind[]): { keys: StateKeyInfo[]; ready: boolean }` (UNCHANGED signature/shape).

- [ ] **Step 1: Rename the file so JSX compiles**

```bash
cd "D:/Gitee/stressbot"
git mv cmd/web/src/components/FlowEditor/editors/ActionEditor/useStateKeyOptions.ts cmd/web/src/components/FlowEditor/editors/ActionEditor/useStateKeyOptions.tsx
```

All existing `import ... from './useStateKeyOptions'` / `'../ActionEditor/useStateKeyOptions'` specifiers resolve to the `.tsx` file unchanged — no import edits needed.

- [ ] **Step 2: Write the failing dedup + fallback tests**

Create `cmd/web/src/components/FlowEditor/editors/ActionEditor/StateKeyOptionsContext.test.tsx`:

```tsx
import { describe, expect, it, beforeEach, vi } from 'vitest';
import { render, waitFor } from '@testing-library/react';
import * as React from 'react';
import { useStateKeyOptions, StateKeyOptionsProvider } from './useStateKeyOptions';
import { useFlowStore } from '../../store/flowStore';

// 必须用提升（hoisted）的 vi.mock 拦截 resourcesStore.getScript —— useStateKeyOptions
// 在模块顶层静态 import getScript，vi.doMock 对已加载的 ESM 模块无效。
const getScriptMock = vi.fn();
vi.mock('@/services/resourcesStore', () => ({
  getScript: (...args: unknown[]) => getScriptMock(...args),
  subscribe: () => () => {},
}));

// 造一个引用脚本 'demo.lua' 的 lua action，使 collectUsedScriptNames 返回 { 'demo.lua' }。
function seedScriptAction() {
  useFlowStore.getState().loadFromTaskFlow({
    defaultDelayMs: 1000,
    nodes: { main: { type: 'sequence', next: ['act1'] }, act1: { type: 'action', action: 'A1' } },
    actions: { A1: { pattern: 'lua', script: 'demo.lua' } },
    listens: {},
  });
}

const Consumer = React.forwardRef<{ keys: unknown[]; ready: boolean }>((_, ref) => {
  const r = useStateKeyOptions();
  React.useImperativeHandle(ref, () => r, [r]);
  return null;
});
Consumer.displayName = 'Consumer';

describe('StateKeyOptionsProvider', () => {
  beforeEach(() => {
    getScriptMock.mockReset();
    getScriptMock.mockResolvedValue({ name: 'demo.lua', content: 'robot.set("demoKey", 1)' });
    seedScriptAction();
  });

  it('provider 下多个消费方只触发一次脚本加载（去重）', async () => {
    const refA = React.createRef<{ keys: unknown[]; ready: boolean }>();
    const refB = React.createRef<{ keys: unknown[]; ready: boolean }>();
    render(
      <StateKeyOptionsProvider>
        <Consumer ref={refA} />
        <Consumer ref={refB} />
      </StateKeyOptionsProvider>,
    );

    await waitFor(() => expect(refA.current?.ready).toBe(true));
    await waitFor(() => expect(refB.current?.ready).toBe(true));

    // 1 个脚本名 → getScript 恰好调用 1 次（去重生效）；无 provider 两消费方会是 2 次。
    expect(getScriptMock).toHaveBeenCalledTimes(1);
  });

  it('无 provider 时回退到自行加载，仍能拿到 keys/ready', async () => {
    const ref = React.createRef<{ keys: unknown[]; ready: boolean }>();
    render(<Consumer ref={ref} />);

    await waitFor(() => expect(ref.current?.ready).toBe(true));
    expect(getScriptMock).toHaveBeenCalledTimes(1); // 单消费方回退也加载一次
    expect(ref.current?.keys.map((k) => (k as { key: string }).key)).toContain('demoKey');
  });
});
```

The hoisted `vi.mock` factory cannot close over the `getScriptMock` variable directly (hoisting runs before the const is initialized), so the factory delegates to `(...args) => getScriptMock(...args)` — the indirection reads the live mock at call time. `beforeEach` resets and re-seeds it.

- [ ] **Step 3: Run the tests to verify they fail**

```bash
cd "D:/Gitee/stressbot"
npm --prefix cmd/web run test -- src/components/FlowEditor/editors/ActionEditor/StateKeyOptionsContext.test.tsx
```

Expected: FAIL — `StateKeyOptionsProvider` is not exported (does not exist yet).

- [ ] **Step 4: Implement the provider and context-aware hook**

Replace the entire contents of `cmd/web/src/components/FlowEditor/editors/ActionEditor/useStateKeyOptions.tsx` with:

```tsx
/**
 * useStateKeyOptions —— 状态候选数据源的统一异步加载钩子 + 共享 Provider。
 *
 * 成本集中在「异步拉取流程引用的 Lua 脚本」（getScript IDB 读）；
 * collectStateKeys 本身是廉价同步扫描。StateKeyOptionsProvider 在 FlowEditor
 * 顶层挂载一次，加载脚本并经 context 共享 { scripts, ready }；消费方经
 * useStateKeyOptions(currentBindings?) 读取：在 Provider 下复用共享脚本，
 * 无 Provider 时回退到自行加载（保持独立可用 / 测试友好）。
 *
 * ready：脚本加载完成（或无需加载脚本）后置 true，调用方可据此显示骨架/占位。
 */

import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import type { FieldBind } from '@/types/action';
import { useRuntimeStore } from '@/services/runtimeStore';
import { getScript } from '@/services/resourcesStore';
import { useFlowStore } from '../../store/flowStore';
import { collectStateKeys, collectUsedScriptNames, type StateKeyInfo } from './stateRegistry';

type LuaScript = { name: string; content: string };

export interface StateKeyOptionsResult {
  keys: StateKeyInfo[];
  ready: boolean;
}

interface LoadedScripts {
  scripts: LuaScript[];
  ready: boolean;
}

const StateKeyOptionsContext = createContext<LoadedScripts | null>(null);

/**
 * 异步加载流程引用的 Lua 脚本。enabled=false 时不加载（Provider 已加载时由
 * 消费方短路），保证遵守 Rules of Hooks（始终调用本钩子），同时在 Provider 下零重复加载。
 */
function useLoadedStateKeyScripts(enabled: boolean): LoadedScripts {
  const actions = useFlowStore((s) => s.actions);
  const listens = useFlowStore((s) => s.listens);
  const nodes = useFlowStore((s) => s.nodes);
  const scriptNames = useMemo(
    () => collectUsedScriptNames(actions, listens, nodes),
    [actions, listens, nodes],
  );
  const [scripts, setScripts] = useState<LuaScript[]>([]);
  const [ready, setReady] = useState(!enabled || scriptNames.size === 0);

  useEffect(() => {
    if (!enabled) {
      setScripts([]);
      setReady(true);
      return;
    }
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
      setScripts(loaded.filter((it): it is LuaScript => it !== null));
      setReady(true);
    });
    return () => { cancelled = true; };
  }, [enabled, scriptNames]);

  return { scripts, ready };
}

/** FlowEditor 顶层挂载一次，共享脚本加载结果。 */
export function StateKeyOptionsProvider({ children }: { children: ReactNode }) {
  const loaded = useLoadedStateKeyScripts(true);
  return <StateKeyOptionsContext.Provider value={loaded}>{children}</StateKeyOptionsContext.Provider>;
}

/**
 * 读取状态候选。在 StateKeyOptionsProvider 下复用共享脚本（去重）；
 * 无 Provider 时回退到自行加载（独立可用）。currentBindings 仅影响当次 keys 合并，
 * 不触发额外脚本加载。
 */
export function useStateKeyOptions(currentBindings?: FieldBind[]): StateKeyOptionsResult {
  const ctx = useContext(StateKeyOptionsContext);
  const local = useLoadedStateKeyScripts(ctx === null);
  const scripts = ctx ? ctx.scripts : local.scripts;
  const ready = ctx ? ctx.ready : local.ready;

  const actions = useFlowStore((s) => s.actions);
  const listens = useFlowStore((s) => s.listens);
  const stateExtra = useRuntimeStore((s) => s.robotConfig.stateExtra);
  const keys = useMemo(
    () => collectStateKeys(actions, listens, stateExtra, currentBindings, scripts),
    [actions, listens, stateExtra, currentBindings, scripts],
  );
  return { keys, ready };
}
```

- [ ] **Step 5: Run the provider tests to verify they pass**

```bash
npm --prefix cmd/web run test -- src/components/FlowEditor/editors/ActionEditor/StateKeyOptionsContext.test.tsx
```

Expected: PASS (both dedup and fallback). The hoisted `vi.mock` factory must intercept `getScript`; if the dedup test reports 2 calls instead of 1, the provider isn't sharing context (re-check `useContext`/`ctx === null` wiring), not the mock.

- [ ] **Step 6: Mount the provider in FlowEditor**

In `cmd/web/src/components/FlowEditor/index.tsx`:

Add the import (alongside the existing imports near the top):

```tsx
import { StateKeyOptionsProvider } from './editors/ActionEditor/useStateKeyOptions';
```

Wrap the inner tree. The current `FlowEditorInner` return starts:

```tsx
  return (
    <FlowReadOnlyContext.Provider value={readOnly}>
      <div style={{ display: 'flex', flexDirection: 'column', height: '100%', width: '100%', position: 'relative' }}>
        <Toolbar onOpenValidation={() => setValidationOpen(true)} extra={topbarExtra} />
        ...
      </div>
    </FlowReadOnlyContext.Provider>
  );
```

Change it to nest `StateKeyOptionsProvider` immediately inside `FlowReadOnlyContext.Provider`, wrapping the `<div>`:

```tsx
  return (
    <FlowReadOnlyContext.Provider value={readOnly}>
      <StateKeyOptionsProvider>
        <div style={{ display: 'flex', flexDirection: 'column', height: '100%', width: '100%', position: 'relative' }}>
          <Toolbar onOpenValidation={() => setValidationOpen(true)} extra={topbarExtra} />
          {/* ...rest unchanged... */}
          <ValidationReportDrawer open={validationOpen} onClose={() => setValidationOpen(false)} />
        </div>
      </StateKeyOptionsProvider>
    </FlowReadOnlyContext.Provider>
  );
```

Only add the `<StateKeyOptionsProvider>` wrapper and the import; do not otherwise change the JSX or indentation of the children beyond the one extra nesting level.

- [ ] **Step 7: Run the full frontend suite + tsc**

```bash
cd "D:/Gitee/stressbot"
(cd cmd/web && npx tsc -b)
npm --prefix cmd/web run test
```

Expected: tsc exit 0; full suite passes (existing SetStateEditor/ClearStateEditor/DeclarativeForm tests mock `useStateKeyOptions` directly and remain green; stateRegistry/refsCheck tests unaffected). The new provider test file adds 2 passing tests.

- [ ] **Step 8: Commit**

Stage ONLY these paths (note the rename — stage both the deletion of the old name and the new file; `git add` of the new path plus the rename records automatically):

```bash
cd "D:/Gitee/stressbot"
git add -- cmd/web/src/components/FlowEditor/editors/ActionEditor/useStateKeyOptions.tsx cmd/web/src/components/FlowEditor/editors/ActionEditor/StateKeyOptionsContext.test.tsx cmd/web/src/components/FlowEditor/index.tsx
git add -u -- cmd/web/src/components/FlowEditor/editors/ActionEditor/useStateKeyOptions.ts
```

(`git add -u` stages the deletion of the old `.ts` path from the rename. Verify `git diff --cached --name-status` shows one rename pair + the new test + index.tsx, and nothing unrelated.) Then:

```bash
git commit -m "refactor(web): StateKeyOptionsProvider 共享脚本加载

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 2: Migrate SetterPathInput onto useStateKeyOptions; drop the prop chain

**Files:**
- Modify: `cmd/web/src/components/FlowEditor/editors/shared/SetterPathInput.tsx`
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/StoreTable.tsx`
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/DeclarativeForm.tsx`
- Modify: `cmd/web/src/components/FlowEditor/listens/ListenEditor.tsx`
- Create: `cmd/web/src/components/FlowEditor/editors/ActionEditor/StoreTable.test.tsx`

**Interfaces:**
- Consumes: `useStateKeyOptions()` from Task 1 (no `currentBindings` — SetterPathInput browses existing keys only).
- Produces: `SetterPathInput({ value, onChange, style })` (data props removed); `StoreTable({ s2cProto?, value?, onChange?, label? })` (data props removed).

- [ ] **Step 1: Write the failing StoreTable test**

Create `cmd/web/src/components/FlowEditor/editors/ActionEditor/StoreTable.test.tsx`:

```tsx
import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { StoreTable } from './StoreTable';

vi.mock('./useStateKeyOptions', () => ({
  useStateKeyOptions: () => ({
    keys: [
      { key: 'loginResp', sourceType: 'store', sourceName: 'login' },
      { key: 'battleId', sourceType: 'setState', sourceName: 'setBattle' },
    ],
    ready: true,
  }),
}));

describe('StoreTable', () => {
  it('渲染 setter 行并在输入 setter 时回调', async () => {
    const onChange = vi.fn();
    render(
      <StoreTable
        s2cProto="X.S2C"
        value={[{ field: 'token', setter: 'loginResp' }]}
        onChange={onChange}
      />,
    );
    // setter 输入框存在并可编辑（证明数据 props 移除后仍正常渲染）
    const setterInput = screen.getByDisplayValue('loginResp');
    await userEvent.clear(setterInput);
    await userEvent.type(setterInput, '.token');
    expect(onChange).toHaveBeenCalled();
    expect(onChange).toHaveBeenLastCalledWith([expect.objectContaining({ field: 'token', setter: 'loginResp.token' })]);
  });
});
```

Run to verify it fails (StoreTable currently passes the data props through and is fine functionally, but this test asserts the post-migration contract — it will FAIL only if `useStateKeyOptions` isn't the data source OR if the prop removal breaks rendering; with the mock in place it should actually pass even before migration because SetterPathInput still renders the input. To make it a true TDD red→green, note: this test is a regression guard that the StoreTable→SetterPathInput path still works AFTER props are removed. Run it now; if it already passes, that's acceptable — it becomes the guard. The real failure mode it guards is a compile break or a runtime crash after the prop removal in Step 2–4.)

```bash
npm --prefix cmd/web run test -- src/components/FlowEditor/editors/ActionEditor/StoreTable.test.tsx
```

- [ ] **Step 2: Migrate SetterPathInput onto the hook**

In `cmd/web/src/components/FlowEditor/editors/shared/SetterPathInput.tsx`:

Remove imports that are no longer needed: `useEffect` (from the `react` import — keep `useCallback, useMemo, useRef, useState`), `useFlowStore`, `getScript`, `collectStateKeys`, `collectUsedScriptNames`, the `ActionDef`/`ListenDef` type imports. Add the hook import:

```tsx
import { useStateKeyOptions } from '../ActionEditor/useStateKeyOptions';
```

Replace the `SetterPathInputProps` interface — remove `actions`, `listens`, `stateExtra`, `luaScripts`:

```tsx
export interface SetterPathInputProps {
  value: string;
  onChange: (v: string) => void;
  style?: React.CSSProperties;
}
```

Update the function signature to destructure only `{ value, onChange, style }`. Replace the entire data-collection block (the `nodes`/`usedScriptNames`/`loadedLuaScripts` effect/the `luaScripts`/`allKeys` memos, currently roughly lines 57–84) with a single hook call:

```tsx
  const { keys: allKeys } = useStateKeyOptions();
```

Keep `filteredKeys`, `selectKey`, `toggleExpand`, the `browseContent` JSX, and the `SetterKeyRow`/`SetterFieldRow` subcomponents exactly as they are. The `allKeys` variable name is unchanged so `filteredKeys` and the `.map` keep working.

- [ ] **Step 3: Drop the prop chain in StoreTable**

In `cmd/web/src/components/FlowEditor/editors/ActionEditor/StoreTable.tsx`:

Remove the now-unused type imports (`ActionDef`, `ListenDef`) and the four passthrough props. The interface becomes:

```tsx
export interface StoreTableProps {
  s2cProto?: string;
  value?: StoreMapping[];
  onChange?: (v: StoreMapping[]) => void;
  label?: string;
}
```

Update the destructure to `{ s2cProto, value, onChange, label }`. In the `<SetterPathInput>` usage (inside the Collapse item children), remove the `actions`, `listens`, `stateExtra`, `luaScripts` props so it reads:

```tsx
          <SetterPathInput
            value={s.setter ?? ''}
            onChange={(v) => {
              const arr = [...list];
              arr[i] = { ...arr[i], setter: v };
              set(arr);
            }}
          />
```

- [ ] **Step 4: Drop the unused subscriptions in DeclarativeForm and ListenEditor**

In `cmd/web/src/components/FlowEditor/editors/ActionEditor/DeclarativeForm.tsx`:
- Remove `const stateExtra = useRuntimeStore((s) => s.robotConfig.stateExtra);`, `const allActions = useFlowStore((s) => s.actions);`, `const allListens = useFlowStore((s) => s.listens);` (they are used ONLY for StoreTable — verified).
- Remove the now-unused imports (`useRuntimeStore`, and `useFlowStore` ONLY if it has no other use in this file — check first; `useFlowStore` is likely still used elsewhere, so leave its import).
- In the `<StoreTable>` render, remove the `actions={allActions}`, `listens={allListens}`, `stateExtra={stateExtra}` props.

In `cmd/web/src/components/FlowEditor/listens/ListenEditor.tsx`:
- Remove `const allActions = useFlowStore((s) => s.actions);`, `const allListens = useFlowStore((s) => s.listens);`, `const stateExtra = useRuntimeStore((s) => s.robotConfig.stateExtra);` (lines ~237–239; used ONLY for StoreTable at ~258–260 — verified).
- Remove the now-unused `useRuntimeStore` import ONLY if it has no other use in this file (check first).
- In the `<StoreTable>` render, remove the `actions`, `listens`, `stateExtra` props.

`TemplateEditorDrawer.tsx` needs NO change — it never passed the data props; after migration its `SetterPathInput` consumes `useStateKeyOptions()` and its setter browse will show the flow's known state keys (intentional improvement — note in the report).

- [ ] **Step 5: Run StoreTable test + full suite + tsc**

```bash
cd "D:/Gitee/stressbot"
npm --prefix cmd/web run test -- src/components/FlowEditor/editors/ActionEditor/StoreTable.test.tsx
(cd cmd/web && npx tsc -b)
npm --prefix cmd/web run test
```

Expected: StoreTable test passes; tsc exit 0 (the prop removal must not leave dangling references — tsc enforces this); full suite passes. If tsc reports an unused import you removed that was actually still used, restore just that import.

- [ ] **Step 6: Commit**

```bash
cd "D:/Gitee/stressbot"
git add -- cmd/web/src/components/FlowEditor/editors/shared/SetterPathInput.tsx cmd/web/src/components/FlowEditor/editors/ActionEditor/StoreTable.tsx cmd/web/src/components/FlowEditor/editors/ActionEditor/DeclarativeForm.tsx cmd/web/src/components/FlowEditor/listens/ListenEditor.tsx cmd/web/src/components/FlowEditor/editors/ActionEditor/StoreTable.test.tsx
git diff --cached --name-only   # verify exactly these 5 paths
git commit -m "refactor(web): SetterPathInput 改用统一状态候选源，移除透传 props

Co-Authored-By: Claude <noreply@anthropic.com>"
```
