/**
 * 本地模板库（idb-keyval 实现）：
 *   - actions/{id}     ActionTemplate
 *   - listens/{id}     ListenTemplate
 *
 * 设计文档 §11：用户保存常用 action / listen，跨流程复用。
 */

import { createStore, get, set, del, keys } from 'idb-keyval';
import { nanoid } from 'nanoid';
import type { ActionDef } from '@/types/action';
import type { ListenDef } from '@/types/listen';

export interface ListenTemplateDefaultRef {
  server: string;
  route: unknown;
  queueSize?: number;
}

// idb-keyval 的 createStore 在同一 DB 名下只能注册一个 objectStore（浏览器本地数据库限制）。
// 用两个独立 DB 隔离 action / listen 模板，避免 NotFoundError。
const actionStore = createStore('stressbot-action-templates', 'data');
const listenStore = createStore('stressbot-listen-templates', 'data');

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

export interface ListenTemplate {
  id: string;
  name: string;
  description?: string;
  kind: string;
  data: ListenDef;
  defaultRef?: ListenTemplateDefaultRef;
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

// ── Listen ────────────────────────────────────────────────
export async function saveListenTemplate(t: Omit<ListenTemplate, 'id' | 'createdAt'>): Promise<ListenTemplate> {
  const tpl: ListenTemplate = { ...t, id: nanoid(8), createdAt: Date.now() };
  await set(tpl.id, tpl, listenStore);
  emitTemplateChange();
  return tpl;
}

export async function updateListenTemplate(t: ListenTemplate): Promise<void> {
  await set(t.id, t, listenStore);
  emitTemplateChange();
}

export async function listListenTemplates(): Promise<ListenTemplate[]> {
  const ks = await keys(listenStore);
  const list: ListenTemplate[] = [];
  for (const k of ks) {
    const v = await get<ListenTemplate>(k as string, listenStore);
    if (v) list.push(v);
  }
  return list.sort((a, b) => b.createdAt - a.createdAt);
}

export async function removeListenTemplate(id: string): Promise<void> {
  await del(id, listenStore);
  emitTemplateChange();
}

// ── 单条读取（编辑模板时用） ────────────────────────────────────────
export async function getActionTemplate(id: string): Promise<ActionTemplate | undefined> {
  return get<ActionTemplate>(id, actionStore);
}

export async function getListenTemplate(id: string): Promise<ListenTemplate | undefined> {
  return get<ListenTemplate>(id, listenStore);
}

// ── 按名称查找（覆盖保存前检测） ────────────────────────────────────
export async function findActionTemplateByName(name: string): Promise<ActionTemplate | undefined> {
  const list = await listActionTemplates();
  return list.find((t) => t.name === name);
}

export async function findListenTemplateByName(name: string): Promise<ListenTemplate | undefined> {
  const list = await listListenTemplates();
  return list.find((t) => t.name === name);
}

// ── 整体导入/导出 ────────────────────────────────────────────
export interface TemplateBundle {
  version: 1;
  exportedAt: number;
  actions: ActionTemplate[];
  listens: ListenTemplate[];
}

export async function exportAllTemplates(): Promise<TemplateBundle> {
  return {
    version: 1,
    exportedAt: Date.now(),
    actions: await listActionTemplates(),
    listens: await listListenTemplates(),
  };
}

export async function importTemplates(bundle: TemplateBundle): Promise<{ actions: number; listens: number }> {
  let aCount = 0;
  let lCount = 0;
  for (const a of bundle.actions ?? []) {
    await set(a.id, a, actionStore);
    aCount++;
  }
  for (const c of bundle.listens ?? []) {
    await set(c.id, c, listenStore);
    lCount++;
  }
  emitTemplateChange();
  return { actions: aCount, listens: lCount };
}
