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

export async function deleteFlow(id: string): Promise<void> {
  await del(id, store);
}
