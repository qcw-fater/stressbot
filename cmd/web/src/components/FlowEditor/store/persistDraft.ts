/**
 * LocalStorage 编辑稿持久化：每次业务字段变更后 debounce 写入。
 *
 * 设计文档 §14：刷新页面后能恢复未保存的工作。
 */

import type { ActionDef } from '@/types/action';
import type { ListenDef } from '@/types/listen';
import type { FlowLayout } from '@/types/editor';
import type { FlowNode } from '@/types/flow';
import type { FlowJson } from '../codec/flowToJson';
import { useFlowStore, type FlowState } from './flowStore';

const KEY_FLOW = 'stressbot-editor-draft-flow-v2';
const KEY_LAYOUT = 'stressbot-editor-draft-layout-v2';

interface PersistedDraft {
  nodes: Record<string, FlowNode>;
  actions: Record<string, ActionDef>;
  listens: Record<string, ListenDef>;
  savedAt: number;
}

export interface DraftSnapshot {
  flow: FlowJson;
  layout: FlowLayout;
  savedAt: number;
}

let timer: number | null = null;

type PersistedSource = Pick<
  FlowState,
  'nodes' | 'actions' | 'listens' | 'layout'
>;

function selectPersistedSource(state: FlowState): PersistedSource {
  return {
    nodes: state.nodes,
    actions: state.actions,
    listens: state.listens,
    layout: state.layout,
  };
}

function isSamePersistedSource(left: PersistedSource, right: PersistedSource): boolean {
  return (
    left.nodes === right.nodes &&
    left.actions === right.actions &&
    left.listens === right.listens &&
    left.layout === right.layout
  );
}

/** 立即写盘（同步）。供卸载/关闭页面时调用，避免 debounce 内的修改丢失。 */
function flushNow(): void {
  try {
    saveDraftSnapshot(captureCurrentDraft());
  } catch (e) {
    console.warn('[persistDraft] 写入失败：', (e as Error).message);
  }
}

/** 注册自动持久化。debounce 300ms 写盘 + beforeunload 同步 flush。返回反订阅函数。 */
export function startAutoPersist(): () => void {
  let previous = selectPersistedSource(useFlowStore.getState());
  const unsub = useFlowStore.subscribe((state) => {
    const next = selectPersistedSource(state);
    if (isSamePersistedSource(previous, next)) return;
    previous = next;
    if (timer) clearTimeout(timer);
    timer = window.setTimeout(() => {
      flushNow();
      timer = null;
    }, 300);
  });
  // 关闭/刷新页面前同步 flush，避免用户改完立刻 F5 时 debounce 还没到
  const onBeforeUnload = () => {
    if (timer) {
      clearTimeout(timer);
      timer = null;
    }
    flushNow();
  };
  window.addEventListener('beforeunload', onBeforeUnload);
  return () => {
    unsub();
    window.removeEventListener('beforeunload', onBeforeUnload);
    if (timer) {
      clearTimeout(timer);
      timer = null;
    }
  };
}

export function captureCurrentDraft(): DraftSnapshot {
  const state = useFlowStore.getState();
  return {
    flow: state.toTaskFlow(),
    layout: structuredClone(state.layout),
    savedAt: Date.now(),
  };
}

export function saveDraftSnapshot(snapshot: DraftSnapshot | null): void {
  if (!snapshot) {
    clearDraft();
    return;
  }
  localStorage.setItem(KEY_FLOW, JSON.stringify({ ...snapshot.flow, savedAt: snapshot.savedAt }));
  localStorage.setItem(KEY_LAYOUT, JSON.stringify(snapshot.layout));
}

export function refreshDraftSnapshot(snapshot: DraftSnapshot | null): void {
  if (snapshot === null) {
    useFlowStore.getState().reset();
    return;
  }
  useFlowStore.getState().loadFromTaskFlow(snapshot.flow, snapshot.layout);
}

export function loadDraft(): DraftSnapshot | null {
  try {
    const flowStr = localStorage.getItem(KEY_FLOW);
    if (!flowStr) return null;
    const draft = JSON.parse(flowStr) as PersistedDraft;
    const layoutStr = localStorage.getItem(KEY_LAYOUT);
    const layout = layoutStr
      ? (JSON.parse(layoutStr) as FlowLayout)
      : { nodePositions: {}, viewport: { x: 0, y: 0, zoom: 1 } };
    return {
      flow: {
        nodes: draft.nodes,
        actions: draft.actions,
        listens: draft.listens,
      },
      layout,
      savedAt: draft.savedAt,
    };
  } catch (e) {
    console.warn('[persistDraft] 读取失败：', (e as Error).message);
    return null;
  }
}

export function clearDraft(): void {
  localStorage.removeItem(KEY_FLOW);
  localStorage.removeItem(KEY_LAYOUT);
}
