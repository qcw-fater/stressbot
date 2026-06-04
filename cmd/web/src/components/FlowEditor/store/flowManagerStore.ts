import { createStore, del, get, keys, set } from 'idb-keyval';
import { nanoid } from 'nanoid';
import type { FlowJson } from '../codec/flowToJson';
import type { FlowLayout } from '@/types/editor';

const FLOW_MANAGER_DB = 'stressbot-flows-manager';
const store = createStore(FLOW_MANAGER_DB, 'data');

export interface ManagedFlow {
  id: string;
  name: string;
  flow: FlowJson;
  layout: FlowLayout;
  updatedAt: number;
}

export const FLOW_NAME_MAX_LENGTH = 80;

function normalizeFlowName(name: string): string {
  const nextName = name.trim();
  if (!nextName) throw new Error('流程名称不能为空');
  if (nextName.length > FLOW_NAME_MAX_LENGTH) throw new Error(`流程名称不能超过 ${FLOW_NAME_MAX_LENGTH} 个字符`);
  return nextName;
}

export async function saveFlow(name: string, flow: FlowJson, layout: FlowLayout, existingId?: string): Promise<ManagedFlow> {
  const id = existingId || nanoid();
  const entry: ManagedFlow = {
    id,
    name,
    flow,
    layout,
    updatedAt: Date.now(),
  };
  await set(id, entry, store);
  return entry;
}

export async function getFlow(id: string): Promise<ManagedFlow | undefined> {
  return get<ManagedFlow>(id, store);
}

export async function listFlows(): Promise<ManagedFlow[]> {
  const allKeys = await keys(store);
  const items: ManagedFlow[] = [];
  for (const k of allKeys) {
    const v = await get<ManagedFlow>(k as string, store);
    if (v) items.push(v);
  }
  items.sort((a, b) => b.updatedAt - a.updatedAt);
  return items;
}

export async function renameFlow(id: string, name: string): Promise<ManagedFlow> {
  const nextName = normalizeFlowName(name);
  const current = await getFlow(id);
  if (!current) throw new Error('流程不存在或已损坏');

  const items = await listFlows();
  const duplicated = items.some((item) => item.id !== id && item.name.trim() === nextName);
  if (duplicated) throw new Error('已存在同名流程');

  const entry: ManagedFlow = {
    ...current,
    name: nextName,
    updatedAt: Date.now(),
  };
  await set(id, entry, store);
  return entry;
}

export async function deleteFlow(id: string): Promise<void> {
  await del(id, store);
}
