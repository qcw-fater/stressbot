/**
 * 简易 Undo/Redo：基于 store 的快照栈，独立于 zundo（避免与现有 store 结构冲突）。
 *
 * 设计文档 §14：撤销/重做仅作用于业务字段（nodes/actions/listens），
 * 不影响视觉位置/选中等 UI 状态。
 */

import type { ActionDef } from '@/types/action';
import type { ListenDef } from '@/types/listen';
import type { FlowNode } from '@/types/flow';
import { useFlowStore } from './flowStore';

interface Snapshot {
  defaultDelayMs: number;
  nodes: Record<string, FlowNode>;
  actions: Record<string, ActionDef>;
  listens: Record<string, ListenDef>;
}

const past: Snapshot[] = [];
const future: Snapshot[] = [];
const MAX_HISTORY = 50;

let suppress = false;
let prev: Snapshot | null = null;

function snapshotNow(): Snapshot {
  const s = useFlowStore.getState();
  return { defaultDelayMs: s.defaultDelayMs, nodes: s.nodes, actions: s.actions, listens: s.listens };
}

/** 启动监听：每次业务字段变化压入 past。 */
export function startHistory(): () => void {
  prev = snapshotNow();
  const unsub = useFlowStore.subscribe((s) => {
    if (suppress) return;
    if (!prev) return;
    const same =
      s.nodes === prev.nodes && s.actions === prev.actions && s.listens === prev.listens && s.defaultDelayMs === prev.defaultDelayMs;
    if (same) return;
    past.push(prev);
    if (past.length > MAX_HISTORY) past.shift();
    future.length = 0;
    prev = snapshotNow();
  });
  return unsub;
}

function applySnapshot(snap: Snapshot) {
  suppress = true;
  useFlowStore.setState(
    {
      defaultDelayMs: snap.defaultDelayMs,
      nodes: snap.nodes,
      actions: snap.actions,
      listens: snap.listens,
    },
    false,
  );
  useFlowStore.getState().syncDerived();
  // 必须在关闭 suppress 之前同步 prev，否则后续任何 store 变化
  // 都会用过期的 prev 生成一条多余的 history 条目，导致撤销行为错乱
  prev = snapshotNow();
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
