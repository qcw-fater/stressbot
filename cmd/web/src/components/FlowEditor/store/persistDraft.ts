/**
 * LocalStorage 编辑稿持久化：每次业务字段变更后 debounce 写入。
 *
 * 设计文档 §14：刷新页面后能恢复未保存的工作。
 */

import type { ActionDef } from '@/types/action';
import type { CallbackDef } from '@/types/callback';
import type { FlowLayout } from '@/types/editor';
import type { FlowNode, TaskFlow } from '@/types/flow';
import { useFlowStore } from './flowStore';

const KEY_FLOW = 'stressbot-editor-draft-flow-v1';
const KEY_LAYOUT = 'stressbot-editor-draft-layout-v1';

interface Draft {
  defaultDelayMs: number;
  nodes: Record<string, FlowNode>;
  actions: Record<string, ActionDef>;
  callbacks: Record<string, CallbackDef>;
  savedAt: number;
}

let timer: number | null = null;

/** 立即写盘（同步）。供卸载/关闭页面时调用，避免 debounce 内的修改丢失。 */
function flushNow(): void {
  try {
    const s = useFlowStore.getState();
    const draft: Draft = {
      defaultDelayMs: s.defaultDelayMs,
      nodes: s.nodes,
      actions: s.actions,
      callbacks: s.callbacks,
      savedAt: Date.now(),
    };
    localStorage.setItem(KEY_FLOW, JSON.stringify(draft));
    localStorage.setItem(KEY_LAYOUT, JSON.stringify(s.layout));
  } catch (e) {
    console.warn('[persistDraft] 写入失败：', (e as Error).message);
  }
}

/** 注册自动持久化。debounce 300ms 写盘 + beforeunload 同步 flush。返回反订阅函数。 */
export function startAutoPersist(): () => void {
  const unsub = useFlowStore.subscribe(() => {
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
    if (timer) clearTimeout(timer);
  };
}

export function loadDraft(): { flow: TaskFlow; layout: FlowLayout; savedAt: number } | null {
  try {
    const flowStr = localStorage.getItem(KEY_FLOW);
    if (!flowStr) return null;
    const draft = JSON.parse(flowStr) as Draft;
    const layoutStr = localStorage.getItem(KEY_LAYOUT);
    const layout = layoutStr ? (JSON.parse(layoutStr) as FlowLayout) : { nodePositions: {}, viewport: { x: 0, y: 0, zoom: 1 } };
    return {
      flow: {
        defaultDelayMs: draft.defaultDelayMs,
        nodes: draft.nodes,
        actions: draft.actions,
        callbacks: draft.callbacks,
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
