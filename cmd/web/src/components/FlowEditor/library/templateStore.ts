/**
 * IndexedDB 模板库（idb-keyval 实现）：
 *   - actions/{id}     ActionTemplate
 *   - callbacks/{id}   CallbackTemplate
 *
 * 设计文档 §11：用户保存常用 action / callback，跨流程复用。
 */

import { createStore, get, set, del, keys } from 'idb-keyval';
import { nanoid } from 'nanoid';
import type { ActionDef } from '@/types/action';
import type { CallbackDef } from '@/types/callback';

// idb-keyval 的 createStore 在同一 DB 名下只能注册一个 objectStore（IndexedDB 限制）。
// 用两个独立 DB 隔离 action / callback 模板，避免 NotFoundError。
const actionStore = createStore('stressbot-action-templates', 'data');
const callbackStore = createStore('stressbot-callback-templates', 'data');

// ── 变更通知 ────────────────────────────────────────────
// 当模板增删时通过 EventTarget 广播，订阅者（NodePalette）自动刷新。
const templateBus = new EventTarget();
const TEMPLATE_CHANGE_EVENT = 'template-change';

export function onTemplateChange(handler: () => void): () => void {
  const wrapped = () => handler();
  templateBus.addEventListener(TEMPLATE_CHANGE_EVENT, wrapped);
  return () => templateBus.removeEventListener(TEMPLATE_CHANGE_EVENT, wrapped);
}

function emitTemplateChange() {
  templateBus.dispatchEvent(new Event(TEMPLATE_CHANGE_EVENT));
}

export interface ActionTemplate {
  id: string;
  name: string;
  description?: string;
  pattern: string;
  data: ActionDef;
  createdAt: number;
}

export interface CallbackTemplate {
  id: string;
  name: string;
  description?: string;
  kind: string;
  data: CallbackDef;
  createdAt: number;
}

// ── Action ────────────────────────────────────────────────
export async function saveActionTemplate(t: Omit<ActionTemplate, 'id' | 'createdAt'>): Promise<ActionTemplate> {
  const tpl: ActionTemplate = { ...t, id: nanoid(8), createdAt: Date.now() };
  await set(tpl.id, tpl, actionStore);
  emitTemplateChange();
  return tpl;
}

export async function updateActionTemplate(t: ActionTemplate): Promise<void> {
  await set(t.id, t, actionStore);
  emitTemplateChange();
}

export async function listActionTemplates(): Promise<ActionTemplate[]> {
  const ks = await keys(actionStore);
  const list: ActionTemplate[] = [];
  for (const k of ks) {
    const v = await get<ActionTemplate>(k as string, actionStore);
    if (v) list.push(v);
  }
  return list.sort((a, b) => b.createdAt - a.createdAt);
}

export async function removeActionTemplate(id: string): Promise<void> {
  await del(id, actionStore);
  emitTemplateChange();
}

// ── Callback ────────────────────────────────────────────────
export async function saveCallbackTemplate(t: Omit<CallbackTemplate, 'id' | 'createdAt'>): Promise<CallbackTemplate> {
  const tpl: CallbackTemplate = { ...t, id: nanoid(8), createdAt: Date.now() };
  await set(tpl.id, tpl, callbackStore);
  emitTemplateChange();
  return tpl;
}

export async function updateCallbackTemplate(t: CallbackTemplate): Promise<void> {
  await set(t.id, t, callbackStore);
  emitTemplateChange();
}

export async function listCallbackTemplates(): Promise<CallbackTemplate[]> {
  const ks = await keys(callbackStore);
  const list: CallbackTemplate[] = [];
  for (const k of ks) {
    const v = await get<CallbackTemplate>(k as string, callbackStore);
    if (v) list.push(v);
  }
  return list.sort((a, b) => b.createdAt - a.createdAt);
}

export async function removeCallbackTemplate(id: string): Promise<void> {
  await del(id, callbackStore);
  emitTemplateChange();
}

// ── 单条读取（编辑模板时用） ────────────────────────────────────────
export async function getActionTemplate(id: string): Promise<ActionTemplate | undefined> {
  return get<ActionTemplate>(id, actionStore);
}

export async function getCallbackTemplate(id: string): Promise<CallbackTemplate | undefined> {
  return get<CallbackTemplate>(id, callbackStore);
}

// ── 整体导入/导出 ────────────────────────────────────────────
export interface TemplateBundle {
  version: 1;
  exportedAt: number;
  actions: ActionTemplate[];
  callbacks: CallbackTemplate[];
}

export async function exportAllTemplates(): Promise<TemplateBundle> {
  return {
    version: 1,
    exportedAt: Date.now(),
    actions: await listActionTemplates(),
    callbacks: await listCallbackTemplates(),
  };
}

export async function importTemplates(bundle: TemplateBundle): Promise<{ actions: number; callbacks: number }> {
  let aCount = 0;
  let cCount = 0;
  for (const a of bundle.actions ?? []) {
    await set(a.id, a, actionStore);
    aCount++;
  }
  for (const c of bundle.callbacks ?? []) {
    await set(c.id, c, callbackStore);
    cCount++;
  }
  emitTemplateChange();
  return { actions: aCount, callbacks: cCount };
}
