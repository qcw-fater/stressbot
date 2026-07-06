# Switch Node Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `switch` flow node that evaluates ordered conditions and executes the first matching branch, with an optional recommended default branch.

**Architecture:** The backend keeps switch as a first-class `Node` type with `cases` and `defaultNext`, reusing `ActionHandler.ExecuteBoolean` for condition evaluation. The frontend mirrors the Go model, emits switch edges from `cases[].next` and `defaultNext`, and adds editor/rendering/validation support without a second value-match model.

**Tech Stack:** Go flow engine, React 18 + TypeScript + React Flow + Ant Design, Vitest, existing FlowEditor validation/codec/store patterns.

---

## File Structure

- `engine/flow.go` — add backend `switch` node model and constants.
- `engine/executor.go` — dispatch and execute switch nodes.
- `engine/executor_switch_test.go` — backend switch execution tests.
- `cmd/web/src/types/flow.ts` — mirror Go model with `SwitchCase`, `switch`, `cases`, `defaultNext`.
- `cmd/web/src/components/FlowEditor/codec/flowToJson.ts` — export switch fields cleanly.
- `cmd/web/src/components/FlowEditor/codec/jsonToFlow.ts` — emit React Flow edges for case/default branches.
- `cmd/web/src/components/FlowEditor/codec/switchNode.test.ts` — frontend codec coverage for switch edges/export.
- `cmd/web/src/components/FlowEditor/validation/refsCheck.ts` — validate switch cases and default branch.
- `cmd/web/src/components/FlowEditor/validation/refsCheck.test.ts` — validation tests for switch.
- `cmd/web/src/components/FlowEditor/store/flowStore.ts` — rename/remove references inside switch branches.
- `cmd/web/src/components/FlowEditor/nodes/SwitchNode.tsx` — canvas rendering with row handles.
- `cmd/web/src/components/FlowEditor/nodes/registry.ts` — register the switch node renderer.
- `cmd/web/src/components/FlowEditor/editors/SwitchEditor.tsx` — edit cases/default branch.
- `cmd/web/src/components/FlowEditor/editors/NodeEditorDrawer.tsx` — route switch nodes to the editor and tag color.
- `cmd/web/src/components/FlowEditor/panels/NodePalette.tsx` — add switch to the palette.
- `cmd/web/src/components/FlowEditor/FlowCanvas.tsx` — connect/delete switch branch edges.
- `cmd/web/src/components/FlowEditor/edges/edgeStyle.ts` — add switch color tokens mapping.
- Optional stylesheet/theme file if node color tokens are centrally declared; update only if TypeScript/CSS reveals missing tokens.

## Git Safety

The working tree already contains unrelated changes. Each task's commit step must first run the project `git-management` process: show `git status --short`, show the diff for only files touched by that task, stage only those files, show `git diff --cached`, then ask the user before committing. Do not include unrelated modified files.

---

### Task 1: Backend Model

**Files:**
- Modify: `engine/flow.go`
- Test: `engine/executor_switch_test.go`

- [ ] **Step 1: Write the model compile test**

Create `engine/executor_switch_test.go` with this initial content:

```go
package engine

import (
	"encoding/json"
	"testing"
)

func TestSwitchNodeJSONModel(t *testing.T) {
	data := []byte(`{
		"type":"switch",
		"cases":[
			{"condition":"state:level >= 10","next":"advanced","description":"高等级"},
			{"condition":"lua:has_guild.lua","next":"guild"}
		],
		"defaultNext":"normal"
	}`)

	var node Node
	if err := json.Unmarshal(data, &node); err != nil {
		t.Fatalf("unmarshal switch node: %v", err)
	}
	if node.Type != NodeSwitch {
		t.Fatalf("Type = %q, want %q", node.Type, NodeSwitch)
	}
	if len(node.Cases) != 2 {
		t.Fatalf("len(Cases) = %d, want 2", len(node.Cases))
	}
	if node.Cases[0].Condition != "state:level >= 10" {
		t.Fatalf("first condition = %q", node.Cases[0].Condition)
	}
	if node.Cases[0].Next != "advanced" {
		t.Fatalf("first next = %q", node.Cases[0].Next)
	}
	if node.Cases[0].Description != "高等级" {
		t.Fatalf("first description = %q", node.Cases[0].Description)
	}
	if node.DefaultNext != "normal" {
		t.Fatalf("DefaultNext = %q, want normal", node.DefaultNext)
	}
}
```

- [ ] **Step 2: Run the model test to verify it fails**

Run:

```bash
go test ./engine -run TestSwitchNodeJSONModel -count=1
```

Expected: FAIL because `NodeSwitch`, `Node.Cases`, `Node.DefaultNext`, or `SwitchCase` is not defined.

- [ ] **Step 3: Add the backend model**

Modify `engine/flow.go`:

1. In `Node`, insert after the boolean fields:

```go
	// ── switch 专用 ──────────────────────────────────────────────
	Cases       []SwitchCase `json:"cases"`       // 按顺序匹配的条件分支
	DefaultNext string       `json:"defaultNext"` // 所有 case 未命中时跳转的节点 ID（空 = 正常结束）
```

2. Add this struct near `WeightedOption` definitions:

```go
// SwitchCase 表示 switch 节点的一条条件分支。
type SwitchCase struct {
	Condition   string `json:"condition"`   // 条件表达式，语法同 boolean/loop
	Next        string `json:"next"`        // 条件命中后执行的节点 ID
	Description string `json:"description"` // 可选说明，仅用于配置可读性
}
```

3. In node type constants, add:

```go
	NodeSwitch  = "switch"
```

Keep the constants grouped with the other node types.

- [ ] **Step 4: Run the model test to verify it passes**

Run:

```bash
go test ./engine -run TestSwitchNodeJSONModel -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit backend model only after approval**

Run:

```bash
git status --short
git diff -- engine/flow.go engine/executor_switch_test.go
```

Show the output to the user. If approved, run:

```bash
git add engine/flow.go engine/executor_switch_test.go
git diff --cached -- engine/flow.go engine/executor_switch_test.go
git commit -m "feat(engine): add switch node model"
```

---

### Task 2: Backend Switch Execution

**Files:**
- Modify: `engine/executor.go`
- Modify: `engine/executor_switch_test.go`

- [ ] **Step 1: Extend the switch test handler**

Append this helper code to `engine/executor_switch_test.go`:

```go
type switchTestHandler struct {
	actions          []string
	booleanResults   map[string]bool
	booleanCalls     []string
	actionErrors     map[string]error
	sleepCalls       int
}

func (h *switchTestHandler) ExecuteAction(_ context.Context, actionDef *ActionDef) error {
	h.actions = append(h.actions, actionDef.Name)
	if h.actionErrors != nil {
		return h.actionErrors[actionDef.Name]
	}
	return nil
}

func (h *switchTestHandler) ExecuteBoolean(expression string) bool {
	h.booleanCalls = append(h.booleanCalls, expression)
	return h.booleanResults[expression]
}

func (h *switchTestHandler) RegisterListen([]ListenRef) error { return nil }

func (h *switchTestHandler) CooperativeSleep(ctx context.Context, _ time.Duration) error {
	h.sleepCalls++
	return ctx.Err()
}

func newSwitchTestExecutor(handler *switchTestHandler, switchNode *Node) *Executor {
	return NewExecutor(&TaskFlow{
		DefaultDelayMs: -1,
		Nodes: map[string]*Node{
			"main":     switchNode,
			"advanced": {Type: NodeAction, Action: "advanced", DelayMs: -1},
			"normal":   {Type: NodeAction, Action: "normal", DelayMs: -1},
			"fallback": {Type: NodeAction, Action: "fallback", DelayMs: -1},
		},
		Actions: map[string]*ActionDef{
			"advanced": {Name: "advanced", Pattern: PatternClearState},
			"normal":   {Name: "normal", Pattern: PatternClearState},
			"fallback": {Name: "fallback", Pattern: PatternClearState},
		},
	}, handler, "switch-test")
}
```

Add imports to `engine/executor_switch_test.go` so the import block is:

```go
import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)
```

- [ ] **Step 2: Add failing execution tests**

Append these tests to `engine/executor_switch_test.go`:

```go
func TestExecuteSwitchRunsFirstMatchingCase(t *testing.T) {
	h := &switchTestHandler{booleanResults: map[string]bool{
		"state:level >= 10": true,
		"state:level >= 1":  true,
	}}
	exec := newSwitchTestExecutor(h, &Node{Type: NodeSwitch, Cases: []SwitchCase{
		{Condition: "state:level >= 10", Next: "advanced"},
		{Condition: "state:level >= 1", Next: "normal"},
	}, DefaultNext: "fallback"})

	if err := exec.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !reflect.DeepEqual(h.booleanCalls, []string{"state:level >= 10"}) {
		t.Fatalf("booleanCalls = %#v", h.booleanCalls)
	}
	if !reflect.DeepEqual(h.actions, []string{"advanced"}) {
		t.Fatalf("actions = %#v", h.actions)
	}
}

func TestExecuteSwitchRunsDefaultWhenNoCaseMatches(t *testing.T) {
	h := &switchTestHandler{booleanResults: map[string]bool{
		"state:level >= 10": false,
		"state:level >= 1":  false,
	}}
	exec := newSwitchTestExecutor(h, &Node{Type: NodeSwitch, Cases: []SwitchCase{
		{Condition: "state:level >= 10", Next: "advanced"},
		{Condition: "state:level >= 1", Next: "normal"},
	}, DefaultNext: "fallback"})

	if err := exec.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !reflect.DeepEqual(h.booleanCalls, []string{"state:level >= 10", "state:level >= 1"}) {
		t.Fatalf("booleanCalls = %#v", h.booleanCalls)
	}
	if !reflect.DeepEqual(h.actions, []string{"fallback"}) {
		t.Fatalf("actions = %#v", h.actions)
	}
}

func TestExecuteSwitchEndsWhenNoCaseMatchesAndNoDefault(t *testing.T) {
	h := &switchTestHandler{booleanResults: map[string]bool{"state:ready": false}}
	exec := newSwitchTestExecutor(h, &Node{Type: NodeSwitch, Cases: []SwitchCase{
		{Condition: "state:ready", Next: "advanced"},
	}})

	if err := exec.Run(context.Background()); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !reflect.DeepEqual(h.booleanCalls, []string{"state:ready"}) {
		t.Fatalf("booleanCalls = %#v", h.booleanCalls)
	}
	if len(h.actions) != 0 {
		t.Fatalf("actions = %#v, want none", h.actions)
	}
}

func TestExecuteSwitchPropagatesChildError(t *testing.T) {
	boom := errors.New("boom")
	h := &switchTestHandler{
		booleanResults: map[string]bool{"state:ready": true},
		actionErrors:   map[string]error{"advanced": boom},
	}
	exec := newSwitchTestExecutor(h, &Node{Type: NodeSwitch, Cases: []SwitchCase{
		{Condition: "state:ready", Next: "advanced"},
	}, DefaultNext: "fallback"})

	err := exec.Run(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("Run error = %v, want boom", err)
	}
	if !reflect.DeepEqual(h.actions, []string{"advanced"}) {
		t.Fatalf("actions = %#v", h.actions)
	}
}
```

- [ ] **Step 3: Run execution tests to verify they fail**

Run:

```bash
go test ./engine -run 'TestExecuteSwitch|TestSwitchNodeJSONModel' -count=1
```

Expected: FAIL because `executeNode` does not dispatch `NodeSwitch` and `executeSwitch` does not exist.

- [ ] **Step 4: Implement switch dispatch and execution**

Modify `engine/executor.go`:

1. In `executeNode`, add after `NodeBoolean`:

```go
	case NodeSwitch:
		return e.executeSwitch(ctx, node)
```

2. Add this function near `executeBoolean`:

```go
// executeSwitch 多条件分支节点：按顺序执行第一条命中的 case。
func (e *Executor) executeSwitch(ctx context.Context, node *Node) error {
	for i, c := range node.Cases {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		matched := e.handler.ExecuteBoolean(c.Condition)
		stresslog.Debug("[ENGINE] switch 条件判断",
			zap.String("caller", e.caller),
			zap.Int("case", i),
			zap.String("condition", c.Condition),
			zap.Bool("matched", matched),
			zap.String("next", c.Next))
		if !matched {
			continue
		}
		if c.Next == "" {
			return nil
		}
		err := e.executeNode(ctx, c.Next)
		if errors.Is(err, errSkip) {
			return nil
		}
		return err
	}

	if node.DefaultNext == "" {
		return nil
	}
	stresslog.Debug("[ENGINE] switch 执行默认分支",
		zap.String("caller", e.caller),
		zap.String("next", node.DefaultNext))
	err := e.executeNode(ctx, node.DefaultNext)
	if errors.Is(err, errSkip) {
		return nil
	}
	return err
}
```

- [ ] **Step 5: Run backend switch tests to verify they pass**

Run:

```bash
go test ./engine -run 'TestExecuteSwitch|TestSwitchNodeJSONModel' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit backend execution only after approval**

Run:

```bash
git status --short
git diff -- engine/executor.go engine/executor_switch_test.go
```

Show the output to the user. If approved, run:

```bash
git add engine/executor.go engine/executor_switch_test.go
git diff --cached -- engine/executor.go engine/executor_switch_test.go
git commit -m "feat(engine): execute switch nodes"
```

---

### Task 3: Frontend Types and Codec

**Files:**
- Modify: `cmd/web/src/types/flow.ts`
- Modify: `cmd/web/src/components/FlowEditor/codec/flowToJson.ts`
- Modify: `cmd/web/src/components/FlowEditor/codec/jsonToFlow.ts`
- Create: `cmd/web/src/components/FlowEditor/codec/switchNode.test.ts`

- [ ] **Step 1: Write frontend codec tests**

Create `cmd/web/src/components/FlowEditor/codec/switchNode.test.ts`:

```typescript
import { describe, expect, it } from 'vitest';
import { jsonToFlow } from './jsonToFlow';
import { flowToJson } from './flowToJson';
import type { FlowJson } from './flowToJson';

const baseFlow: FlowJson = {
  defaultDelayMs: 1000,
  nodes: {
    main: {
      type: 'switch',
      cases: [
        { condition: 'state:level >= 10', next: 'advanced', description: '高等级' },
        { condition: 'lua:has_guild.lua', next: 'guild' },
      ],
      defaultNext: 'normal',
    },
    advanced: { type: 'action', action: 'advanced' },
    guild: { type: 'action', action: 'guild' },
    normal: { type: 'action', action: 'normal' },
  },
  actions: {
    advanced: { pattern: 'clearState', keys: ['a'] },
    guild: { pattern: 'clearState', keys: ['g'] },
    normal: { pattern: 'clearState', keys: ['n'] },
  },
  listens: {},
};

describe('switch node codec', () => {
  it('emits case and default edges', () => {
    const { rfEdges } = jsonToFlow(baseFlow);
    expect(rfEdges.map((e) => ({ sourceHandle: e.sourceHandle, target: e.target, type: e.type, data: e.data }))).toEqual([
      { sourceHandle: 'case-0', target: 'advanced', type: 'branch', data: { branch: 'case', caseIndex: 0, sourceNodeType: 'switch' } },
      { sourceHandle: 'case-1', target: 'guild', type: 'branch', data: { branch: 'case', caseIndex: 1, sourceNodeType: 'switch' } },
      { sourceHandle: 'default', target: 'normal', type: 'branch', data: { branch: 'default', sourceNodeType: 'switch' } },
    ]);
  });

  it('exports switch fields without empty values', () => {
    const exported = flowToJson({
      defaultDelayMs: 1000,
      nodes: {
        main: {
          type: 'switch',
          cases: [
            { condition: 'state:level >= 10', next: 'advanced', description: '高等级' },
            { condition: '', next: '', description: '' },
          ],
          defaultNext: '',
        },
        advanced: { type: 'action', action: 'advanced' },
      },
      actions: { advanced: { pattern: 'clearState', keys: ['a'] } },
      listens: {},
    });

    expect(exported.nodes.main).toEqual({
      type: 'switch',
      cases: [{ condition: 'state:level >= 10', next: 'advanced', description: '高等级' }],
    });
  });
});
```

- [ ] **Step 2: Run the codec tests to verify they fail**

Run:

```bash
cd cmd/web && npm run test -- src/components/FlowEditor/codec/switchNode.test.ts
```

Expected: FAIL because TypeScript does not know `switch`, `cases`, or `defaultNext`, and codec functions do not handle switch.

- [ ] **Step 3: Add frontend type model**

Modify `cmd/web/src/types/flow.ts`:

1. Add `'switch'` to `NodeType` after `'boolean'`:

```typescript
  | 'switch'
```

2. Add switch fields to `FlowNode` after boolean fields:

```typescript
  // switch 专用
  cases?: SwitchCase[];
  defaultNext?: string;
```

3. Add this interface after `WeightedOption`:

```typescript
export interface SwitchCase {
  condition: string;
  next: string;
  description?: string;
}
```

- [ ] **Step 4: Update export cleaning**

Modify `cmd/web/src/components/FlowEditor/codec/flowToJson.ts`:

1. Import `SwitchCase` with existing flow types.
2. Add this `case` inside `cleanNode` after boolean:

```typescript
    case 'switch': {
      const cases = (n.cases ?? [])
        .filter((c) => c.condition?.trim() || c.next?.trim() || c.description?.trim())
        .map((c): SwitchCase => {
          const out: SwitchCase = { condition: c.condition?.trim() ?? '', next: c.next?.trim() ?? '' };
          if (c.description?.trim()) out.description = c.description.trim();
          return out;
        });
      if (cases.length) out.cases = cases;
      if (n.defaultNext?.trim()) out.defaultNext = n.defaultNext.trim();
      break;
    }
```

- [ ] **Step 5: Update import edge emission**

Modify `cmd/web/src/components/FlowEditor/codec/jsonToFlow.ts`:

1. Import `SwitchCase` with existing flow types.
2. Update the top conversion comment with:

```typescript
 *   switch.cases[i].next       → edge[source=id, sourceHandle=`case-${i}`, target=*, type='branch']
 *   switch.defaultNext         → edge[source=id, sourceHandle='default', target=*, type='branch']
```

3. Add this branch in `emitEdgesFor` after boolean:

```typescript
    case 'switch':
      return emitSwitchEdges(id, node.cases ?? [], node.defaultNext);
```

4. Add this helper near `emitWeightedEdges`:

```typescript
function emitSwitchEdges(id: string, cases: SwitchCase[], defaultNext?: string): Edge[] {
  const out: Edge[] = [];
  cases.forEach((c, i) => {
    if (!c.next) return;
    out.push(makeEdge(`${id}->case[${i}]->${c.next}`, id, c.next, 'branch', `case-${i}`, {
      branch: 'case',
      caseIndex: i,
    }));
  });
  if (defaultNext) {
    out.push(makeEdge(`${id}->default->${defaultNext}`, id, defaultNext, 'branch', 'default', { branch: 'default' }));
  }
  return out;
}
```

- [ ] **Step 6: Run codec tests to verify they pass**

Run:

```bash
cd cmd/web && npm run test -- src/components/FlowEditor/codec/switchNode.test.ts
```

Expected: PASS.

- [ ] **Step 7: Commit frontend codec only after approval**

Run:

```bash
git status --short
git diff -- cmd/web/src/types/flow.ts cmd/web/src/components/FlowEditor/codec/flowToJson.ts cmd/web/src/components/FlowEditor/codec/jsonToFlow.ts cmd/web/src/components/FlowEditor/codec/switchNode.test.ts
```

Show the output to the user. If approved, run:

```bash
git add cmd/web/src/types/flow.ts cmd/web/src/components/FlowEditor/codec/flowToJson.ts cmd/web/src/components/FlowEditor/codec/jsonToFlow.ts cmd/web/src/components/FlowEditor/codec/switchNode.test.ts
git diff --cached -- cmd/web/src/types/flow.ts cmd/web/src/components/FlowEditor/codec/flowToJson.ts cmd/web/src/components/FlowEditor/codec/jsonToFlow.ts cmd/web/src/components/FlowEditor/codec/switchNode.test.ts
git commit -m "feat(web): support switch node codec"
```

---

### Task 4: Frontend Validation and Store References

**Files:**
- Modify: `cmd/web/src/components/FlowEditor/validation/refsCheck.ts`
- Modify: `cmd/web/src/components/FlowEditor/validation/refsCheck.test.ts`
- Modify: `cmd/web/src/components/FlowEditor/store/flowStore.ts`

- [ ] **Step 1: Add validation tests**

Append to `cmd/web/src/components/FlowEditor/validation/refsCheck.test.ts`:

```typescript
describe('switch node validation', () => {
  it('accepts valid switch nodes and warns when default is missing', () => {
    const report = validateFlow({
      defaultDelayMs: 1000,
      nodes: {
        main: {
          type: 'switch',
          cases: [{ condition: 'state:level >= 10', next: 'advanced' }],
        },
        advanced: { type: 'action', action: 'advanced' },
      },
      actions: { advanced: { pattern: 'clearState', keys: ['x'] } },
      listens: {},
    });

    expect(report.errors.map((e) => e.code)).not.toContain('NODE_UNKNOWN_TYPE');
    expect(report.errors.map((e) => e.code)).not.toContain('SWITCH_NO_CASES');
    expect(report.warnings.map((e) => e.code)).toContain('SWITCH_NO_DEFAULT');
  });

  it('reports missing cases, empty condition, missing next, and invalid default', () => {
    const report = validateFlow({
      defaultDelayMs: 1000,
      nodes: {
        main: {
          type: 'switch',
          cases: [{ condition: '', next: '' }],
          defaultNext: 'missing',
        },
      },
      actions: {},
      listens: {},
    });

    expect(report.errors.map((e) => e.code)).toEqual(expect.arrayContaining([
      'SWITCH_CASE_NO_CONDITION',
      'SWITCH_CASE_NO_NEXT',
      'NODE_REF_NOT_FOUND',
    ]));
  });
});
```

- [ ] **Step 2: Run validation tests to verify they fail**

Run:

```bash
cd cmd/web && npm run test -- src/components/FlowEditor/validation/refsCheck.test.ts
```

Expected: FAIL because `switch` is not in `VALID_NODE_TYPES` and validation logic is missing.

- [ ] **Step 3: Implement validation rules**

Modify `cmd/web/src/components/FlowEditor/validation/refsCheck.ts`:

1. Add `'switch'` to `VALID_NODE_TYPES`:

```typescript
const VALID_NODE_TYPES = new Set(['sequence', 'action', 'loop', 'boolean', 'switch', 'weighted', 'wait', 'break', 'continue']);
```

2. Add this branch after boolean validation:

```typescript
    } else if (node.type === 'switch') {
      const cases = node.cases ?? [];
      if (cases.length === 0) {
        issues.push({
          severity: 'error', code: 'SWITCH_NO_CASES',
          message: `switch 节点 "${id}" 缺少 cases`,
          location: { kind: 'node', id },
        });
      }
      cases.forEach((c, i) => {
        if (!c.condition?.trim()) {
          issues.push({
            severity: 'error', code: 'SWITCH_CASE_NO_CONDITION',
            message: `switch 节点 "${id}" cases[${i}] 缺少 condition`,
            location: { kind: 'node', id },
          });
        }
        if (!c.next?.trim()) {
          issues.push({
            severity: 'error', code: 'SWITCH_CASE_NO_NEXT',
            message: `switch 节点 "${id}" cases[${i}] 缺少 next`,
            location: { kind: 'node', id },
          });
        }
        ref(c.next, `cases[${i}].next`);
      });
      ref(node.defaultNext, 'defaultNext');
      if (!node.defaultNext?.trim()) {
        issues.push({
          severity: 'warning', code: 'SWITCH_NO_DEFAULT',
          message: `switch 节点 "${id}" 未配置 defaultNext，所有条件未命中时将直接结束`,
          location: { kind: 'node', id },
        });
      }
```

Do not add static mutual-exclusion checks.

- [ ] **Step 4: Update store remove/rename references**

Modify `cmd/web/src/components/FlowEditor/store/flowStore.ts`:

1. In `removeNode`, inside the loop that builds `partial`, add after weighted cleanup:

```typescript
        if (n.cases?.some((c) => c.next === id)) {
          partial.cases = n.cases.filter((c) => c.next !== id);
        }
        if (n.defaultNext === id) partial.defaultNext = '';
```

2. In `renameRefsInNode`, add before action onError handling:

```typescript
  if (node.cases?.length) {
    out.cases = node.cases.map((c) => (c.next === oldId ? { ...c, next: newId } : c));
  }
  if (node.defaultNext === oldId) out.defaultNext = newId;
```

- [ ] **Step 5: Run validation tests to verify they pass**

Run:

```bash
cd cmd/web && npm run test -- src/components/FlowEditor/validation/refsCheck.test.ts
```

Expected: PASS.

- [ ] **Step 6: Commit validation/store only after approval**

Run:

```bash
git status --short
git diff -- cmd/web/src/components/FlowEditor/validation/refsCheck.ts cmd/web/src/components/FlowEditor/validation/refsCheck.test.ts cmd/web/src/components/FlowEditor/store/flowStore.ts
```

Show the output to the user. If approved, run:

```bash
git add cmd/web/src/components/FlowEditor/validation/refsCheck.ts cmd/web/src/components/FlowEditor/validation/refsCheck.test.ts cmd/web/src/components/FlowEditor/store/flowStore.ts
git diff --cached -- cmd/web/src/components/FlowEditor/validation/refsCheck.ts cmd/web/src/components/FlowEditor/validation/refsCheck.test.ts cmd/web/src/components/FlowEditor/store/flowStore.ts
git commit -m "feat(web): validate switch node refs"
```

---

### Task 5: Switch Node Canvas and Editor

**Files:**
- Create: `cmd/web/src/components/FlowEditor/nodes/SwitchNode.tsx`
- Modify: `cmd/web/src/components/FlowEditor/nodes/registry.ts`
- Create: `cmd/web/src/components/FlowEditor/editors/SwitchEditor.tsx`
- Modify: `cmd/web/src/components/FlowEditor/editors/NodeEditorDrawer.tsx`
- Modify: `cmd/web/src/components/FlowEditor/panels/NodePalette.tsx`
- Modify: `cmd/web/src/components/FlowEditor/FlowCanvas.tsx`
- Modify: `cmd/web/src/components/FlowEditor/edges/edgeStyle.ts`

- [ ] **Step 1: Create the SwitchNode renderer**

Create `cmd/web/src/components/FlowEditor/nodes/SwitchNode.tsx`:

```tsx
/**
 * switch 节点：左入 + 右出，每行一个 case，最后一行为 default。
 */

import { Handle, Position, type NodeProps } from '@xyflow/react';
import { Tag, Tooltip } from 'antd';
import { NodeShell } from './shared/NodeShell';
import type { FlowNode } from '@/types/flow';

interface NodeData {
  nodeId: string;
  node: FlowNode;
}

const ROW_HEIGHT = 22;
const HEADER_OFFSET = 30;

function trim(s: string, n: number): string {
  return s.length > n ? s.slice(0, n) + '…' : s;
}

function formatCondition(cond: string): { display: string; tag: string } {
  let display = cond.trim();
  let tag = 'state';
  while (display.startsWith('state:') || display.startsWith('lua:')) {
    if (display.startsWith('state:')) {
      tag = 'state';
      display = display.slice(6).trimStart();
      continue;
    }
    tag = 'lua';
    display = display.slice(4).trimStart();
  }
  return { display, tag };
}

export function SwitchNode({ id, data, selected }: NodeProps) {
  const { node } = data as unknown as NodeData;
  const cases = node.cases ?? [];
  const hasDefault = !!node.defaultNext;

  return (
    <>
      <Handle type="target" position={Position.Left} id="in" />
      <NodeShell
        nodeId={id}
        nodeType="switch"
        title={id}
        subtitle={`switch · ${cases.length}`}
        selected={selected}
        minWidth={260}
        description={node.description}
      >
        <div className="row-list">
          {cases.map((c, i) => {
            const cond = formatCondition(c.condition ?? '');
            return (
              <Tooltip key={i} title={`${c.condition || '未配置条件'} → ${c.next || '未配置目标'}`} mouseEnterDelay={0.4}>
                <div className="row-item">
                  <span className="row-index">{i + 1}.</span>
                  <Tag color={cond.tag === 'lua' ? 'purple' : 'blue'} style={{ fontSize: 9, lineHeight: '14px', padding: '0 3px', margin: 0 }}>
                    {cond.tag}
                  </Tag>
                  <span className="row-name">{trim(cond.display || '未配置条件', 24)}</span>
                  <span className="row-tail">{c.next || '未配置'}</span>
                </div>
              </Tooltip>
            );
          })}
          <Tooltip title={hasDefault ? `default → ${node.defaultNext}` : '未配置默认分支'} mouseEnterDelay={0.4}>
            <div className="row-item">
              <span className="row-index">*</span>
              <span className="row-name" style={{ color: hasDefault ? undefined : 'var(--text-tertiary)', fontStyle: hasDefault ? undefined : 'italic' }}>
                default
              </span>
              <span className="row-tail">{node.defaultNext || '未配置'}</span>
            </div>
          </Tooltip>
        </div>
      </NodeShell>
      {cases.map((_, i) => (
        <Handle key={i} type="source" position={Position.Right} id={`case-${i}`} style={{ top: HEADER_OFFSET + i * ROW_HEIGHT }} />
      ))}
      <Handle type="source" position={Position.Right} id="default" style={{ top: HEADER_OFFSET + cases.length * ROW_HEIGHT }} />
    </>
  );
}
```

- [ ] **Step 2: Register SwitchNode and edge colors**

Modify `cmd/web/src/components/FlowEditor/nodes/registry.ts`:

```typescript
import { SwitchNode } from './SwitchNode';
```

Add to `nodeTypes`:

```typescript
  switch: SwitchNode,
```

Modify `cmd/web/src/components/FlowEditor/edges/edgeStyle.ts` and add to `NODE_COLOR_MAP`:

```typescript
  switch: { main: 'var(--node-boolean)', deep: 'var(--node-boolean-border-active)' },
```

Using boolean color keeps switch visually grouped with conditional control flow.

- [ ] **Step 3: Create SwitchEditor**

Create `cmd/web/src/components/FlowEditor/editors/SwitchEditor.tsx`:

```tsx
import { Button, Card, Form, Input, Select, Space } from 'antd';
import { DeleteOutlined, DownOutlined, PlusOutlined, UpOutlined } from '@ant-design/icons';
import { ConditionInput } from './shared/ConditionInput';
import { useFlowStore } from '../store/flowStore';
import type { SwitchCase } from '@/types/flow';

interface SwitchEditorProps {
  nodeId: string;
}

export function SwitchEditor({ nodeId }: SwitchEditorProps) {
  const node = useFlowStore((s) => s.nodes[nodeId]);
  const nodes = useFlowStore((s) => s.nodes);
  const updateNode = useFlowStore((s) => s.updateNode);
  const cases = node?.cases ?? [];
  const nodeOptions = Object.keys(nodes)
    .filter((id) => id !== nodeId)
    .map((id) => ({ value: id, label: id }));

  const setCases = (next: SwitchCase[]) => updateNode(nodeId, { cases: next });
  const patchCase = (index: number, patch: Partial<SwitchCase>) => {
    const next = cases.slice();
    next[index] = { ...next[index], ...patch };
    setCases(next);
  };
  const moveCase = (index: number, dir: -1 | 1) => {
    const target = index + dir;
    if (target < 0 || target >= cases.length) return;
    const next = cases.slice();
    const [item] = next.splice(index, 1);
    next.splice(target, 0, item);
    setCases(next);
  };

  return (
    <Form layout="vertical">
      <Form.Item label="分支列表">
        <Space direction="vertical" style={{ width: '100%' }}>
          {cases.map((c, i) => (
            <Card key={i} size="small" title={`Case ${i + 1}`} extra={
              <Space size={4}>
                <Button size="small" icon={<UpOutlined />} disabled={i === 0} onClick={() => moveCase(i, -1)} />
                <Button size="small" icon={<DownOutlined />} disabled={i === cases.length - 1} onClick={() => moveCase(i, 1)} />
                <Button size="small" danger icon={<DeleteOutlined />} onClick={() => setCases(cases.filter((_, idx) => idx !== i))} />
              </Space>
            }>
              <Form.Item label="条件">
                <ConditionInput
                  value={c.condition}
                  onChange={(condition) => patchCase(i, { condition })}
                  placeholder="如 level >= 10 || vipLevel >= 5"
                />
              </Form.Item>
              <Form.Item label="目标节点">
                <Select
                  allowClear
                  showSearch
                  value={c.next || undefined}
                  options={nodeOptions}
                  onChange={(next) => patchCase(i, { next: next ?? '' })}
                  placeholder="选择命中后执行的节点"
                />
              </Form.Item>
              <Form.Item label="说明">
                <Input
                  value={c.description ?? ''}
                  onChange={(e) => patchCase(i, { description: e.target.value })}
                  placeholder="可选，例如：高等级流程"
                />
              </Form.Item>
            </Card>
          ))}
          <Button
            icon={<PlusOutlined />}
            onClick={() => setCases([...cases, { condition: 'state:', next: '' }])}
          >
            添加 Case
          </Button>
        </Space>
      </Form.Item>
      <Form.Item label="默认分支（可选但推荐）">
        <Select
          allowClear
          showSearch
          value={node?.defaultNext || undefined}
          options={nodeOptions}
          onChange={(defaultNext) => updateNode(nodeId, { defaultNext: defaultNext ?? '' })}
          placeholder="所有条件未命中时执行的节点"
        />
      </Form.Item>
    </Form>
  );
}
```

- [ ] **Step 4: Wire the editor drawer**

Modify `cmd/web/src/components/FlowEditor/editors/NodeEditorDrawer.tsx`:

1. Import:

```typescript
import { SwitchEditor } from './SwitchEditor';
```

2. Add to `nodeTypeTagColor`:

```typescript
  switch: 'gold',
```

3. Add to the editor switch after boolean:

```tsx
      case 'switch':
        return <SwitchEditor nodeId={nodeId} />;
```

- [ ] **Step 5: Add switch to palette and selection colors**

Modify `cmd/web/src/components/FlowEditor/panels/NodePalette.tsx` where `PALETTE` is declared. Add an entry:

```typescript
{ type: 'switch', label: 'Switch', color: 'var(--node-boolean)' },
```

Modify `cmd/web/src/components/FlowEditor/FlowCanvas.tsx` in `NODE_COLOR` inside `onSelectionChange`:

```typescript
          switch: 'var(--node-boolean)',
```

- [ ] **Step 6: Add switch connect/delete behavior**

Modify `cmd/web/src/components/FlowEditor/FlowCanvas.tsx`:

1. In `deleteRfEdge`, after boolean handling, add:

```typescript
      } else if (src.type === 'switch' && handleId.startsWith('case-')) {
        const idx = Number(handleId.slice(5));
        if (Number.isFinite(idx) && src.cases) {
          const cases = src.cases.slice();
          cases.splice(idx, 1);
          updateNode(e.source, { cases });
        }
      } else if (src.type === 'switch' && handleId === 'default') {
        updateNode(e.source, { defaultNext: '' });
```

2. In `onConnect`, after boolean handling, add:

```typescript
      if (src.type === 'switch') {
        if (handle === 'default' && targetNodeId) {
          updateNode(params.source, { defaultNext: targetNodeId });
          return;
        }
        if (handle.startsWith('case-') && targetNodeId) {
          const idx = Number(handle.slice(5));
          if (Number.isFinite(idx)) {
            const cases = (src.cases ?? []).slice();
            if (cases[idx]) {
              cases[idx] = { ...cases[idx], next: targetNodeId };
              updateNode(params.source, { cases });
            }
          }
          return;
        }
      }
```

Do not add a `case-add` handle in this first implementation; case creation stays in the editor so every case has an explicit condition.

- [ ] **Step 7: Run TypeScript build**

Run:

```bash
cd cmd/web && npx tsc -b
```

Expected: PASS. If CSS variables for switch are missing, the implementation uses boolean variables and no new theme token is required.

- [ ] **Step 8: Commit UI only after approval**

Run:

```bash
git status --short
git diff -- cmd/web/src/components/FlowEditor/nodes/SwitchNode.tsx cmd/web/src/components/FlowEditor/nodes/registry.ts cmd/web/src/components/FlowEditor/editors/SwitchEditor.tsx cmd/web/src/components/FlowEditor/editors/NodeEditorDrawer.tsx cmd/web/src/components/FlowEditor/panels/NodePalette.tsx cmd/web/src/components/FlowEditor/FlowCanvas.tsx cmd/web/src/components/FlowEditor/edges/edgeStyle.ts
```

Show the output to the user. If approved, run:

```bash
git add cmd/web/src/components/FlowEditor/nodes/SwitchNode.tsx cmd/web/src/components/FlowEditor/nodes/registry.ts cmd/web/src/components/FlowEditor/editors/SwitchEditor.tsx cmd/web/src/components/FlowEditor/editors/NodeEditorDrawer.tsx cmd/web/src/components/FlowEditor/panels/NodePalette.tsx cmd/web/src/components/FlowEditor/FlowCanvas.tsx cmd/web/src/components/FlowEditor/edges/edgeStyle.ts
git diff --cached -- cmd/web/src/components/FlowEditor/nodes/SwitchNode.tsx cmd/web/src/components/FlowEditor/nodes/registry.ts cmd/web/src/components/FlowEditor/editors/SwitchEditor.tsx cmd/web/src/components/FlowEditor/editors/NodeEditorDrawer.tsx cmd/web/src/components/FlowEditor/panels/NodePalette.tsx cmd/web/src/components/FlowEditor/FlowCanvas.tsx cmd/web/src/components/FlowEditor/edges/edgeStyle.ts
git commit -m "feat(web): add switch node editor"
```

---

### Task 6: Full Verification

**Files:**
- Read-only verification unless failures require targeted fixes.

- [ ] **Step 1: Run backend tests for engine**

Run:

```bash
go test ./engine -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full Go build**

Run:

```bash
go build ./...
```

Expected: PASS.

- [ ] **Step 3: Run frontend typecheck**

Run:

```bash
cd cmd/web && npx tsc -b
```

Expected: PASS.

- [ ] **Step 4: Run frontend tests**

Run:

```bash
cd cmd/web && npm run test
```

Expected: PASS.

- [ ] **Step 5: Manually verify the editor flow**

Run the frontend:

```bash
cd cmd/web && npm run dev
```

Open the app and verify:

1. Switch appears in the node palette.
2. Dragging Switch creates a node with a left input and right case/default handles.
3. Opening the node editor allows adding two cases and one default target.
4. Connecting a case handle updates `cases[index].next` in JSON preview.
5. Connecting the default handle updates `defaultNext` in JSON preview.
6. Deleting a case edge removes that case.
7. Deleting the default edge clears `defaultNext`.
8. Validation shows `SWITCH_NO_DEFAULT` as warning when default is absent.

- [ ] **Step 6: Backend runtime smoke test if switch is added to sample flow**

If a sample `flow.json` is changed to include switch, run:

```bash
rm -f log/stressbot.log
go run ./cmd/agent -config conf/config.json
```

Let it run for 2 to 5 minutes, then stop it and run:

```bash
grep -i "error\|warn\|失败" log/stressbot.log | grep -v "headError"
```

Expected: no unexpected output. If the sample flow is not changed, skip this smoke test and state that no runtime flow uses switch yet.

- [ ] **Step 7: Final status review**

Run:

```bash
git status --short
```

Expected: only unrelated pre-existing files remain modified, or a clean tree if all tasks were committed and unrelated files were handled separately by the user.

## Self-Review

- Spec coverage: backend model, ordered first-match execution, optional `defaultNext`, B-via-A example, frontend canvas/editor, refs/validation, and verification are all mapped to tasks.
- Placeholder scan: no placeholder tasks or unspecified implementation steps remain.
- Type consistency: Go uses `SwitchCase`, `Cases`, `DefaultNext`; TypeScript uses `SwitchCase`, `cases`, `defaultNext`; React Flow handles are `case-N` and `default` consistently across codec, node renderer, and canvas connect/delete logic.
