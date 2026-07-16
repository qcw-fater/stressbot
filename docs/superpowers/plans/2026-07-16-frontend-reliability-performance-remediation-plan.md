# Frontend Reliability and Performance Remediation Implementation Plan

> **For Codex:** Execute this plan sequentially in the current workspace. Use test-driven development for behavior changes and preserve unrelated user changes.

**Goal:** Fix the reviewed frontend correctness, input, performance, terminology, and lint issues without changing backend contracts or `flow.json` semantics.

**Architecture:** Keep `nodes/actions/listens` as the immediate source of truth. Split React Flow derivation into full topology sync and targeted visual patching, centralize validation behind a 150ms scheduler, and make save/export/start perform synchronous validation. Replace broad subscriptions and full text rebuilds with keyed selectors and incremental processing.

**Tech Stack:** React 18, TypeScript 5.6, Zustand/Zundo, React Flow 12, Ant Design 5, Monaco, Vitest, Testing Library.

---

## Task 1: Protect Previous Runtime Data During Failed Starts

**Files:**
- Create: `cmd/web/src/services/__tests__/taskActions.test.ts`
- Modify: `cmd/web/src/services/taskActions.ts`

1. Mock flow validation, resource stores, task APIs, runtime store, metrics store, and canvas replacement.
2. Add a failing test proving a missing script or missing codec does not call `clearMonitorData` or clear node metrics.
3. Add a failing test proving `createTask` or `startTask` failure leaves previous monitor data and canvas untouched.
4. Add a success test proving cleanup and canvas replacement occur exactly once after the remote start succeeds.
5. Move monitor cleanup and metric reset from the preflight section to the successful commit section immediately before runtime state installation.
6. Update comments so the documented transaction boundary matches the implementation.
7. Run `npm run test -- src/services/__tests__/taskActions.test.ts`.

## Task 2: Fix Polling Health, Conditional Hooks, and JSON Draft Input

**Files:**
- Create: `cmd/web/src/services/connectionHealth.ts`
- Create: `cmd/web/src/services/__tests__/connectionHealth.test.ts`
- Modify: `cmd/web/src/pages/EditorPage.tsx`
- Create: `cmd/web/src/components/modules/BaselineSyncModal.test.tsx`
- Modify: `cmd/web/src/components/modules/BaselineSyncModal.tsx`
- Modify: `cmd/web/src/components/FlowEditor/editors/shared/JsonDraftInput.test.ts`
- Modify: `cmd/web/src/components/FlowEditor/editors/shared/JsonDraftInput.tsx`

1. Add tests for a failed-source set: two sources fail, one recovers, banner remains; all recover, banner clears; disabling a source removes only that source.
2. Implement a small immutable `ConnectionHealthTracker` or equivalent hook helper and wire each EditorPage poller with a stable source key.
3. Keep boot-time connection failure in its own source slot and clear it after a successful relevant request.
4. Add a render/rerender test for `BaselineSyncModal` that transitions between empty and non-empty conflicts without a Hook-order error.
5. Move all BaselineSyncModal hooks above the conditional return and change user-facing resource labels from implementation terms to `协议`, `脚本`, and `连接配置`.
6. Add Testing Library user-event tests for incomplete JSON, valid completion, parent rerender during a dirty draft, and invalid blur.
7. Keep invalid `json`/`jsonArray` drafts visible after blur with error state; commit raw text on blur only for `jsonOrString`.
8. Run the three focused test files.

## Task 3: Split Flow Derivation and Centralize Validation

**Files:**
- Create: `cmd/web/src/components/FlowEditor/store/flowStore.test.ts`
- Create: `cmd/web/src/components/FlowEditor/validation/validationStore.ts`
- Create: `cmd/web/src/components/FlowEditor/validation/validationStore.test.ts`
- Create: `cmd/web/src/components/FlowEditor/validation/FlowValidationCoordinator.tsx`
- Modify: `cmd/web/src/components/FlowEditor/store/flowStore.ts`
- Modify: `cmd/web/src/components/FlowEditor/validation/ValidationReport.tsx`
- Modify: `cmd/web/src/components/FlowEditor/panels/Toolbar.tsx`
- Modify: `cmd/web/src/components/FlowEditor/index.tsx`
- Modify: `cmd/web/src/components/FlowEditor/panels/useFlowFileIO.ts`
- Modify: `cmd/web/src/services/taskActions.ts`

1. Add store tests proving ordinary action/listen/node presentation edits preserve unrelated `rfNodes` and `rfEdges` object identities.
2. Add tests proving structural node references, add/remove/rename, load, reset, import, undo, and redo still rebuild topology correctly.
3. Extract full derivation into one internal helper. Index existing RF nodes by ID with a `Map` before restoring positions.
4. Implement targeted patch helpers for a node, nodes referencing an action, and the listen card or referencing nodes for a listen.
5. Classify node fields that affect topology; call full derivation only when one of those fields changes. Treat full replacement as structural unless proven otherwise.
6. Remove validation from full graph derivation and load paths. Store only the latest externally supplied `issuesByNodeId`.
7. Add a validation store/coordinator with a 150ms trailing timer, monotonic request version, immediate validation method, and cleanup.
8. Have the coordinator subscribe once to the flow snapshot, state-key options, and route-template version. Publish a single report plus grouped node issues.
9. Make Toolbar and ValidationReport consume the shared report. Remove their direct `validateFlow` calls and duplicate flow subscriptions.
10. Ensure export and task start call an immediate full validation against `useFlowStore.getState().toTaskFlow()` and the latest validation context.
11. Run focused flow store, validation, refsCheck, and codec tests.

## Task 4: Reduce State-Key and Drag Layout Work

**Files:**
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/StateKeyOptionsContext.test.tsx`
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/useStateKeyOptions.tsx`
- Modify: `cmd/web/src/components/FlowEditor/store/flowStore.test.ts`
- Modify: `cmd/web/src/components/FlowEditor/store/flowStore.ts`
- Modify: `cmd/web/src/components/FlowEditor/FlowCanvas.tsx`

1. Add a test proving an unrelated action field edit does not call `getScript` again, while a script reference-set change does.
2. Compute a sorted script-name signature and make script loading depend on that stable primitive rather than Set identity.
3. Make the Provider publish stable base keys and loading state. Remove duplicate fallback scanning for production consumers; retain a deterministic empty-context behavior for isolated tests.
4. Add store actions for committing one or multiple node positions immutably.
5. Add tests proving position changes do not mutate the previous layout object and batched commits clone the position table once.
6. Keep drag-time coordinates in React Flow and commit them on drag stop. Replace all direct `layout.nodePositions` mutations in add, paste, and layout paths with store actions.
7. Run focused state-key and flow-store tests.

## Task 5: Optimize Logs, Metrics, and Window Lifecycle

**Files:**
- Create: `cmd/web/src/components/monitoring/tabs/logViewModel.ts`
- Create: `cmd/web/src/components/monitoring/tabs/logViewModel.test.ts`
- Modify: `cmd/web/src/components/monitoring/tabs/LogsTab.tsx`
- Create: `cmd/web/src/components/FlowEditor/nodes/shared/MetricsBadge.test.tsx`
- Modify: `cmd/web/src/components/FlowEditor/nodes/shared/MetricsBadge.tsx`
- Modify: `cmd/web/src/components/FlowEditor/nodes/shared/NodeShell.tsx`
- Modify: `cmd/web/src/pages/EditorPage.tsx`
- Modify: `cmd/web/src/components/FlowEditor/panels/FloatingWindow.tsx`

1. Extract pure log filtering and append-decision helpers. Test matching, incremental append, source/filter reset, and ring-buffer truncation.
2. Filter entries once per state update. Append only newly matching lines to Monaco when filter/source/cursor conditions are stable; otherwise do one full replacement.
3. Replace the `useMemo` side effect and silent empty catches with explicit effects or named no-op error handling.
4. Change metrics state from a provider function to a keyed metric map. Update maps with per-node reference reuse so unchanged selector results remain stable.
5. Make NodeShell subscribe once to its metric and pass it to pure badge and health-level functions.
6. Add a render-count or selector test proving one node metric update does not change another node's selected value.
7. Replace permanent `LazyMount` usage for read-only heavy windows with conditional mounting. Add `keepMounted` to FloatingWindow only for windows with uncommitted local drafts.
8. Keep notepad or protocol editor drafts explicitly preserved; unmount system, logs, history, and node lists when closed.
9. Run focused log, metric, and smoke tests.

## Task 6: Correct Terminology, Comments, Deprecated APIs, and Lint Errors

**Files:**
- Modify reviewed files under `cmd/web/src/components` and `cmd/web/src/services`
- Primary targets: `RouteEditor.tsx`, `EditorPage.tsx`, `ActionMetricsTable.tsx`, `TaskStartModal.tsx`, `ResourcesDrawer.tsx`, `HistoryDetailView.tsx`, `HistoryModal.tsx`, `ValidationReport.tsx`, `TemplateEditorDrawer.tsx`, `JsonPreviewModal.tsx`, `FlowEditor/index.tsx`, `undoRedo.ts`, `flowStore.ts`, `refsCheck.ts`, `jsonToFlow.ts`, `TrendsTab.tsx`, `ClearStateEditor.tsx`
- Lint targets: `ActionNode.tsx`, `HistoryDetailView.tsx`, `reportCharts.ts`, `FileEditor.tsx`, `runtimeStore.ts`, `ActionPreview.tsx`, `BindingsTable.tsx`, `ProtoLoader.ts`, `logLanguage.ts`, `main.tsx`, and files reported by the final lint run

1. Replace user-facing implementation terms according to repository conventions: Agent to node, Admin to server, IDB to local storage, Lua to script, callback to listen, service/server to connection service.
2. Rename local callback aliases for `flow.listens` and update stale comments without changing JSON field names.
3. Replace `Card.bodyStyle` with `styles.body` and native title hints with Ant Design Tooltip.
4. Resolve missing Hook dependencies by stabilizing callbacks or including the true dependency; do not suppress Hook lint rules.
5. Replace avoidable `any`, invalid regex escapes, empty catch blocks, undescribed `@ts-expect-error`, and type-only import warnings.
6. Run `npm run lint` and iterate until it exits successfully.

## Task 7: Full Verification and Manual Flow Validation

**Files:**
- Modify only files required by failures found during verification.

1. Run `go build ./...` from repository root.
2. Run `npx tsc -b` in `cmd/web`.
3. Run `npm run test` in `cmd/web` and record file/test counts.
4. Run `npm run lint` in `cmd/web`.
5. Start the Vite development server on an available localhost port.
6. Open the editor with current `conf/flow/flow.json`, confirm the validation report has no errors, and exercise field typing, incomplete JSON, add/connect/drag, undo/redo, export, logs, metrics, and window reopen behavior.
7. Capture browser console errors and check the editor remains responsive during continuous input. Browser automation is optional only if the repository browser client is unavailable; in that case report the exact manual verification gap.
8. Stop the development server and confirm no background command remains running.
9. Review `git diff --check`, `git diff --stat`, and `git status --short`; do not stage or commit without explicit user confirmation.
