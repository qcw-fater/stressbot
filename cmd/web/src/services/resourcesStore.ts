/**
 * 用户上传的 proto / lua 资源管理（IndexedDB）。
 *
 * 设计要点：
 * - 不直接依赖 idb 库，使用 idb-keyval 提供的 createStore + key-value API；
 * - proto / lua 各占一个 store，避免混杂导致 listProto 返回 lua；
 * - 文件内容以 utf-8 字符串存储（proto / lua 都是文本），不存 ArrayBuffer，
 *   方便 JSON.stringify 调试与 Monaco 直接拿到 string；
 * - 资源版本号 hash 在 ProtoLoader 端基于内容拼接计算，store 不维护；
 * - 暴露 `subscribe` 给 React 组件订阅"资源变更"事件，配合 useSyncExternalStore。
 *
 * T1 阶段先建好基础 API；T2 阶段会基于此实现 ResourcesDrawer 与 ProtoLoader 改造。
 */

import { clear, createStore, del, get, keys, set, setMany } from 'idb-keyval';

const DB_NAME = 'stressbot-resources';

export interface ResourceFile {
  name: string;
  content: string;
  size: number;
  uploadedAt: string;
}

const protoStore = createStore(DB_NAME, 'proto');
const scriptStore = createStore(DB_NAME, 'scripts');

// === Proto ===

export async function addProto(name: string, content: string): Promise<ResourceFile> {
  const file: ResourceFile = {
    name,
    content,
    size: byteLength(content),
    uploadedAt: new Date().toISOString(),
  };
  await set(name, file, protoStore);
  notify();
  return file;
}

export async function addProtos(files: Array<{ name: string; content: string }>): Promise<void> {
  if (files.length === 0) return;
  const now = new Date().toISOString();
  const entries: Array<[string, ResourceFile]> = files.map(({ name, content }) => [
    name,
    { name, content, size: byteLength(content), uploadedAt: now },
  ]);
  await setMany(entries, protoStore);
  notify();
}

export async function getProto(name: string): Promise<ResourceFile | undefined> {
  return get<ResourceFile>(name, protoStore);
}

export async function listProto(): Promise<ResourceFile[]> {
  const allKeys = (await keys(protoStore)) as IDBValidKey[];
  const items: ResourceFile[] = [];
  for (const k of allKeys) {
    const v = await get<ResourceFile>(k, protoStore);
    if (v) items.push(v);
  }
  items.sort((a, b) => a.name.localeCompare(b.name));
  return items;
}

export async function removeProto(name: string): Promise<void> {
  await del(name, protoStore);
  notify();
}

export async function clearProto(): Promise<void> {
  await clear(protoStore);
  notify();
}

// === Script (Lua) ===

export async function addScript(name: string, content: string): Promise<ResourceFile> {
  const file: ResourceFile = {
    name,
    content,
    size: byteLength(content),
    uploadedAt: new Date().toISOString(),
  };
  await set(name, file, scriptStore);
  notify();
  return file;
}

export async function addScripts(files: Array<{ name: string; content: string }>): Promise<void> {
  if (files.length === 0) return;
  const now = new Date().toISOString();
  const entries: Array<[string, ResourceFile]> = files.map(({ name, content }) => [
    name,
    { name, content, size: byteLength(content), uploadedAt: now },
  ]);
  await setMany(entries, scriptStore);
  notify();
}

export async function getScript(name: string): Promise<ResourceFile | undefined> {
  return get<ResourceFile>(name, scriptStore);
}

export async function listScript(): Promise<ResourceFile[]> {
  const allKeys = (await keys(scriptStore)) as IDBValidKey[];
  const items: ResourceFile[] = [];
  for (const k of allKeys) {
    const v = await get<ResourceFile>(k, scriptStore);
    if (v) items.push(v);
  }
  items.sort((a, b) => a.name.localeCompare(b.name));
  return items;
}

export async function removeScript(name: string): Promise<void> {
  await del(name, scriptStore);
  notify();
}

export async function clearScript(): Promise<void> {
  await clear(scriptStore);
  notify();
}

// === 变更订阅 ===

type Listener = () => void;
const listeners = new Set<Listener>();

export function subscribe(fn: Listener): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

function notify(): void {
  for (const fn of listeners) fn();
}

function byteLength(s: string): number {
  return new Blob([s]).size;
}
