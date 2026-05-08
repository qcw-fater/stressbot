/**
 * 用户上传的 proto / lua 资源管理（IndexedDB）。
 *
 * 设计要点：
 * - 不直接依赖 idb 库，使用 idb-keyval 提供的 createStore + key-value API；
 * - **每个 DB 只能挂一个 object store**（idb-keyval 不会触发 version upgrade 加 store），
 *   因此 proto / lua 各用一个独立 DB（与 library/templateStore.ts 保持一致）；
 * - 文件内容以 utf-8 字符串存储（proto / lua 都是文本），不存 ArrayBuffer，
 *   方便 JSON.stringify 调试与 Monaco 直接拿到 string；
 * - 资源版本号 hash 在 ProtoLoader 端基于内容拼接计算，store 不维护；
 * - 暴露 `subscribe` 给 React 组件订阅"资源变更"事件，配合 useSyncExternalStore。
 *
 * 历史：v0 用同一 `stressbot-resources` DB 同时挂 proto / scripts 两个 store，
 * 触发了 IDB "One of the specified object stores was not found" —— scripts store
 * 永远没有被 onupgradeneeded 真正创建。本文件 init 时会做一次轻量迁移把旧 DB
 * 的 proto 数据搬入新 DB，再删掉旧 DB。
 */

import { clear, createStore, del, get, keys, set, setMany } from 'idb-keyval';

const PROTO_DB = 'stressbot-resources-proto';
const SCRIPT_DB = 'stressbot-resources-scripts';
const LEGACY_DB = 'stressbot-resources';

/**
 * Lua 脚本基线 schema 版本号。
 *
 * 引擎对 Lua 脚本返回值的契约**升级**时（不向后兼容的破坏性变更）必须 bump 此常量，
 * 例如：v1 → v2 切换为 `return code, send, recv` 三元组、boolean 脚本强制 `return true/false`。
 *
 * 浏览器首次加载 / 刷新时会比对 LocalStorage 中保存的版本号：
 *   - 不匹配 → 一次性清空 lua scriptStore（IDB 中所有用户脚本副本失效）
 *   - 用户下次进入 LuaForm 时，由"IDB miss → fetch /conf/scripts/<name>"路径自动拉新版基线
 *   - 然后写入 LocalStorage 标记新版本，避免重复清空
 *
 * 设计取舍：清空策略会丢失"用户编辑过但未导入到 flow 的本地稿"，但这种数据在用户视角下
 * 本就是临时缓存（IDB 不是云端），换取"基线升级零手工操作"的体验。
 */
const SCRIPT_BASELINE_VERSION = 'v2-bytes-return-2026-05-08';
const SCRIPT_BASELINE_VERSION_KEY = 'stressbot.scriptBaselineVersion';

export interface ResourceFile {
  name: string;
  content: string;
  size: number;
  uploadedAt: string;
}

const protoStore = createStore(PROTO_DB, 'data');
const scriptStore = createStore(SCRIPT_DB, 'data');

// 模块加载时异步触发迁移；失败静默（旧 DB 不存在 / 已损坏都按"无需迁移"处理）。
void migrateLegacyResources();
// Lua 脚本基线 schema 失效检测；不阻塞模块加载，失败静默。
void migrateScriptBaseline();

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

// === Legacy 迁移：v0 同 DB 双 store → v1 双 DB 单 store ===

/**
 * 一次性把旧 `stressbot-resources` DB 中可能存在的 proto 数据搬到新 DB；
 * 完成后删除旧 DB。安全保证：
 *   - 用 onupgradeneeded 不动（不传 version），打开当前已存在的 DB；
 *   - 检查 objectStoreNames 是否含 'proto'，没有就直接删旧 DB；
 *   - 任意一步抛错都吞掉，不影响主流程。
 */
async function migrateLegacyResources(): Promise<void> {
  if (typeof indexedDB === 'undefined') return;
  try {
    const legacy = await openExistingDb(LEGACY_DB);
    if (!legacy) return;
    try {
      if (legacy.objectStoreNames.contains('proto')) {
        const entries = await readAll(legacy, 'proto');
        if (entries.length > 0) {
          const ts = new Date().toISOString();
          const adapted: Array<[string, ResourceFile]> = entries.map(([k, v]) => {
            const name = String(k);
            if (isResourceFile(v)) return [name, v];
            const content = typeof v === 'string' ? v : '';
            return [name, { name, content, size: byteLength(content), uploadedAt: ts }];
          });
          await setMany(adapted, protoStore);
          notify();
        }
      }
    } finally {
      legacy.close();
    }
    await deleteDb(LEGACY_DB);
  } catch {
    // 静默：用户没旧数据 / 浏览器拒绝访问都按无需迁移处理
  }
}

function openExistingDb(name: string): Promise<IDBDatabase | null> {
  return new Promise((resolve) => {
    let needCreate = false;
    const req = indexedDB.open(name);
    req.onupgradeneeded = () => {
      // 触发 upgrade 说明 DB 之前不存在，直接放弃迁移
      needCreate = true;
    };
    req.onsuccess = () => {
      if (needCreate) {
        req.result.close();
        // 把刚被我们意外创建的空 DB 删掉，避免污染浏览器 IDB 列表
        indexedDB.deleteDatabase(name);
        resolve(null);
        return;
      }
      resolve(req.result);
    };
    req.onerror = () => resolve(null);
    req.onblocked = () => resolve(null);
  });
}

function readAll(db: IDBDatabase, storeName: string): Promise<Array<[IDBValidKey, unknown]>> {
  return new Promise((resolve) => {
    try {
      const tx = db.transaction(storeName, 'readonly');
      const store = tx.objectStore(storeName);
      const out: Array<[IDBValidKey, unknown]> = [];
      const req = store.openCursor();
      req.onsuccess = () => {
        const cursor = req.result;
        if (cursor) {
          out.push([cursor.key, cursor.value]);
          cursor.continue();
        } else {
          resolve(out);
        }
      };
      req.onerror = () => resolve(out);
      tx.onerror = () => resolve(out);
    } catch {
      resolve([]);
    }
  });
}

function deleteDb(name: string): Promise<void> {
  return new Promise((resolve) => {
    const req = indexedDB.deleteDatabase(name);
    req.onsuccess = () => resolve();
    req.onerror = () => resolve();
    req.onblocked = () => resolve();
  });
}

function isResourceFile(v: unknown): v is ResourceFile {
  if (!v || typeof v !== 'object') return false;
  const o = v as Record<string, unknown>;
  return typeof o.name === 'string' && typeof o.content === 'string';
}

// === Lua 脚本基线 schema 失效迁移 ===

/**
 * 当 SCRIPT_BASELINE_VERSION 与 LocalStorage 中保存的版本号不一致时：
 *   1. 清空 lua scriptStore 中所有副本（用户编辑稿与基线副本一并丢弃）；
 *   2. 写入新版本号；
 *   3. 触发 notify 让订阅 LuaForm 的"IDB miss → fetch /conf/scripts/<name>"路径生效。
 *
 * 不阻塞主流程，失败静默；浏览器无 LocalStorage（如老旧内核）时直接当作版本一致跳过。
 */
async function migrateScriptBaseline(): Promise<void> {
  if (typeof localStorage === 'undefined') return;
  try {
    const saved = localStorage.getItem(SCRIPT_BASELINE_VERSION_KEY);
    if (saved === SCRIPT_BASELINE_VERSION) return;
    await clear(scriptStore);
    localStorage.setItem(SCRIPT_BASELINE_VERSION_KEY, SCRIPT_BASELINE_VERSION);
    notify();
    if (typeof console !== 'undefined') {
      console.info(
        `[resourcesStore] Lua 脚本基线已升级到 ${SCRIPT_BASELINE_VERSION}，` +
          `本地缓存已清空，下次进入编辑器或启动任务时会自动从 /conf/scripts/ 拉新版。`,
      );
    }
  } catch {
    // 静默：用户禁用 IDB / LocalStorage，下次进入 LuaForm 走默认 fetch 路径
  }
}
