/**
 * 简易 Undo/Redo：基于 store 的快照栈，独立于 zundo（避免与现有 store 结构冲突）。
 *
 * 设计文档 §14：撤销/重做仅作用于业务字段（nodes/actions/callbacks），
 * 不影响视觉位置/选中等 UI 状态。
 */

import type { ActionDef } from '@/types/action';
import type { CallbackDef } from '@/types/callback';
import type { FlowNode } from '@/types/flow';
import { useFlowStore } from './flowStore';

interface Snapshot {
  defaultDelayMs: number;
  nodes: Record<string, FlowNode>;
  actions: Record<string, ActionDef>;
  callbacks: Record<string, CallbackDef>;
}

const past: Snapshot[] = [];
const future: Snapshot[] = [];
const MAX_HISTORY = 50;

let suppress = false;

/** 启动监听：每次业务字段变化压入 past。 */
export function startHistory(): () => void {
  let prev = snapshotNow();
  const unsub = useFlowStore.subscribe((s) => {
    if (suppress) return;
    const same =
      s.nodes === prev.nodes && s.actions === prev.actions && s.callbacks === prev.callbacks && s.defaultDelayMs === prev.defaultDelayMs;
    if (same) return;
    past.push(prev);
    if (past.length > MAX_HISTORY) past.shift();
    future.length = 0;
    prev = snapshotNow();
  });
  return unsub;
}

function snapshotNow(): Snapshot {
  const s = useFlowStore.getState();
  return { defaultDelayMs: s.defaultDelayMs, nodes: s.nodes, actions: s.actions, callbacks: s.callbacks };
}

function applySnapshot(snap: Snapshot) {
  suppress = true;
  useFlowStore.setState(
    {
      defaultDelayMs: snap.defaultDelayMs,
      nodes: snap.nodes,
      actions: snap.actions,
      callbacks: snap.callbacks,
    },
    false,
  );
  useFlowStore.getState().syncDerived();
  suppress = false;
}

export function undo(): boolean {
  if (past.length === 0) return false;
  future.push(snapshotNow());
  const target = past.pop()!;
  applySnapshot(target);
  return true;
}

export function redo(): boolean {
  if (future.length === 0) return false;
  past.push(snapshotNow());
  const target = future.pop()!;
  applySnapshot(target);
  return true;
}

export function getHistorySize(): { past: number; future: number } {
  return { past: past.length, future: future.length };
}
