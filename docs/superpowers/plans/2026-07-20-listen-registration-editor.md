# Listen Registration Route Track Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep action and listen registration rows compact while making route fields directly visible and editable, with a thin floating editor for larger routes and queue-size edits synchronized through the existing `ListenRef` source of truth.

**Architecture:** Add a shared route field track that derives ordered fields from `routeKeyTemplate`, renders them as a fixed-width horizontal scroller, and owns temporary per-field edit drafts. Both registration tables reuse this track and open the existing non-modal `FloatingWindow` around the full inline `RouteEditor`. The tables continue mutating `FlowNode.listenRefs`; the listen panel remains a back-reference view over that same data.

**Tech Stack:** React 18, TypeScript 5.6, Ant Design 5, Zustand, React RND, Vitest, Testing Library

---

### Task 1: Build the shared route field track with TDD

**Files:**
- Create: `cmd/web/src/components/FlowEditor/listens/RouteFieldTrack.tsx`
- Create: `cmd/web/src/components/FlowEditor/listens/RouteFieldTrack.css`
- Create: `cmd/web/src/components/FlowEditor/listens/RouteFieldTrack.test.tsx`
- Reuse: `cmd/web/src/components/FlowEditor/listens/routeFormModel.ts`

- [ ] Write a failing test that renders `cmd=12` and `act=3` as text, then clicks `cmd=12` and expects only `route cmd` to become an input.
- [ ] Run `npm.cmd run test -- src/components/FlowEditor/listens/RouteFieldTrack.test.tsx` and confirm RED because the component does not exist.
- [ ] Implement a fixed-width track with `overflow-x: auto`, a reserved slim scrollbar, and a fixed pop-out icon outside the scrolling region.
- [ ] Implement local edit state: click enters edit, Enter/blur commits through `updateRouteTemplateField`, Escape cancels, invalid drafts remain in edit mode with an error tooltip.
- [ ] Add accessible names for the field buttons, active input, and pop-out button.
- [ ] Re-run the focused test and confirm GREEN.

### Task 2: Build the thin route floating editor with TDD

**Files:**
- Create: `cmd/web/src/components/FlowEditor/listens/RouteFloatingEditor.tsx`
- Create: `cmd/web/src/components/FlowEditor/listens/RouteFloatingEditor.test.tsx`
- Modify: `cmd/web/src/components/FlowEditor/listens/RouteEditor.css`
- Reuse: `cmd/web/src/components/FloatingWindow/FloatingWindow.tsx`

- [ ] Write a failing test that opens the editor and expects a non-modal window titled `编辑 route` with both route fields in one horizontal row.
- [ ] Run the focused test and confirm RED because the wrapper does not exist.
- [ ] Wrap `RouteEditor layout="inline"` in `FloatingWindow` with default size `560 x 112`, minimum size `360 x 96`, no footer, and immediate `onChange` updates.
- [ ] Make the inline route form a single horizontal scroller so many fields never wrap the window taller.
- [ ] Re-run focused tests and confirm GREEN.

### Task 3: Integrate the action registration table with TDD

**Files:**
- Modify: `cmd/web/src/components/FlowEditor/listens/ListenRefsTable.tsx`
- Modify: `cmd/web/src/components/FlowEditor/listens/ListenRefsTable.test.tsx`

- [ ] Extend the existing test to expect the `route` column, directly visible field values, a pop-out control, and a fixed table minimum width of 600px.
- [ ] Run the focused test and confirm RED against the current all-input route cell.
- [ ] Replace the route cell with `RouteFieldTrack` and render one shared `RouteFloatingEditor` for the selected row.
- [ ] Keep the selected row stable across move-up/move-down; close or reindex it on deletion.
- [ ] Keep queue size, target connection, paste, reorder, and delete behavior unchanged.
- [ ] Re-run the focused test and confirm GREEN.

### Task 4: Integrate the listen back-reference table with TDD

**Files:**
- Modify: `cmd/web/src/components/FlowEditor/listens/BackrefList.tsx`
- Modify: `cmd/web/src/components/FlowEditor/listens/BackrefList.test.tsx`

- [ ] Extend the existing test to expect `route`, direct field editing, opening the floating window, and queue-size synchronization into the referenced action node.
- [ ] Run the focused test and confirm RED.
- [ ] Replace the route cell with the shared track and render a shared floating editor keyed by `{nodeId, refIndex}`.
- [ ] Close the editor when its registration is deleted; keep all updates pointed at the same action `ListenRef`.
- [ ] Re-run the focused test and confirm GREEN.

### Task 5: Verify the complete frontend behavior

**Files:**
- Verify only

- [ ] Run focused route and registration tests.
- [ ] Run `npx.cmd tsc -b` in `cmd/web`.
- [ ] Run `npm.cmd run test` in `cmd/web`.
- [ ] Run `go build ./...` at the repository root as required by `AGENTS.md`.
- [ ] Start the Vite development server and inspect the action/listen editors at desktop and narrow widths: row height stays consistent, route scrolls independently, inline editing works, the floating editor is draggable/resizable and non-modal, and queue changes stay synchronized.

This plan is intentionally executed in the current `master` worktree without staging or committing, per the user's instruction.

### Task 6: Unify node and listen editor widths

**Files:**
- Create: `cmd/web/src/components/FlowEditor/store/editorStore.test.ts`
- Modify: `cmd/web/src/components/FlowEditor/store/editorStore.ts`
- Modify: `cmd/web/src/components/FlowEditor/editors/NodeEditorDrawer.tsx`
- Modify: `cmd/web/src/components/FlowEditor/listens/ListenEditor.tsx`

- [ ] Add a failing store test that opens both panel kinds and expects an actual width of 720px:

```ts
it.each([
  [{ kind: 'nodeEdit', nodeId: 'node-a' } as const, 'nodeEdit'],
  [{ kind: 'listenEdit', listenName: 'listen-a' } as const, 'listenEdit'],
])('opens %s at the shared editor width', (panel, windowId) => {
  useEditorStore.getState().setActivePanel(panel);
  expect(useFloatingWindowStore.getState().windows[windowId]?.size.width).toBe(720);
});
```

- [ ] Run `node node_modules/vitest/vitest.mjs run src/components/FlowEditor/store/editorStore.test.ts` in `cmd/web` and confirm RED: `nodeEdit` is 640 and `listenEdit` is 680.
- [ ] Export `EDITOR_PANEL_WIDTH = 720` from `editorStore.ts`, use it for both `DEFAULT_SIZES.nodeEdit` and `DEFAULT_SIZES.listenEdit`, and use the same constant in `NodeEditorDrawer` and `ListenEditor` `defaultSize` props.
- [ ] Remove the action-only `drawerWidth` branch so sequence, loop, boolean, switch, weighted, wait, break, continue, action, and listen editors all open at 720px.
- [ ] Re-run the focused test and confirm GREEN, then run TypeScript, the full Vitest suite, and `go build ./...`.
- [ ] Inspect action, listen, sequence, and boolean panels in the browser; confirm consistent widths and no default horizontal scrollbar in the action listen-registration table.

Per the user's instruction, keep these changes uncommitted on the current branch.
