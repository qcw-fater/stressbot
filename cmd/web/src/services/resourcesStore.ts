/**
 * 用户上传的 proto / lua / adapter 资源管理（IndexedDB）。
 *
 * 设计要点：
 * - 不直接依赖 idb 库，使用 idb-keyval 提供的 createStore + key-value API；
 * - **每个 DB 只能挂一个 object store**（idb-keyval 不会触发 version upgrade 加 store），
 *   因此 proto / lua / adapter 各用一个独立 DB（与 library/templateStore.ts 保持一致）；
 * - 文件内容以 utf-8 字符串存储（proto / lua 都是文本），不存 ArrayBuffer，
 *   方便 JSON.stringify 调试与 Monaco 直接拿到 string；
 * - ResourceFile.baseHash 记录上次确认同步到的服务器内容 hash，按 Git/SVN 工作副本语义做三方判断；
 * - 暴露 `subscribe` 给 React 组件订阅"资源变更"事件，配合 useSyncExternalStore。
 *
 * 基线同步：本地/基线/服务器三方比较，只有双方都修改且内容不同时才需要用户处理冲突。
 */

import { clear, createStore, del, get, keys, set, setMany } from 'idb-keyval';
import { BASELINE_PREFIX } from './env';

const PROTO_DB = 'stressbot-resources-proto';
const SCRIPT_DB = 'stressbot-resources-scripts';
const LEGACY_DB = 'stressbot-resources';

export interface ResourceFile {
  name: string;
  content: string;
  size: number;
  uploadedAt: string;
  /** 上次确认同步到的服务器内容 hash；null 表示确认时服务器没有该资源。 */
  baseHash?: string | null;
}

const ADAPTER_DB = 'stressbot-resources-adapter';

const protoStore = createStore(PROTO_DB, 'data');
const scriptStore = createStore(SCRIPT_DB, 'data');
const adapterStore = createStore(ADAPTER_DB, 'data');

export async function hashResourceContent(content: string): Promise<string> {
  const buf = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(content));
  const hex = Array.from(new Uint8Array(buf))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('');
  return `sha256:${hex}`;
}

async function localResourceFile(name: string, content: string, previous?: ResourceFile, uploadedAt = new Date().toISOString()): Promise<ResourceFile> {
  return {
    name,
    content,
    size: byteLength(content),
    uploadedAt,
    baseHash: previous?.baseHash ?? null,
  };
}

async function serverResourceFile(name: string, content: string, uploadedAt = new Date().toISOString()): Promise<ResourceFile> {
  return {
    name,
    content,
    size: byteLength(content),
    uploadedAt,
    baseHash: await hashResourceContent(content),
  };
}

// 模块加载时异步触发迁移；失败静默（旧 DB 不存在 / 已损坏都按"无需迁移"处理）。
void migrateLegacyResources();

// === Proto ===

export async function addProto(name: string, content: string): Promise<ResourceFile> {
  const previous = await getProto(name);
  const file = await localResourceFile(name, content, previous);
  await set(name, file, protoStore);
  notify();
  return file;
}

export async function addProtoFromBaseline(name: string, content: string): Promise<ResourceFile> {
  const file = await serverResourceFile(name, content);
  await set(name, file, protoStore);
  notify();
  return file;
}

export async function addProtos(files: Array<{ name: string; content: string }>): Promise<void> {
  if (files.length === 0) return;
  const now = new Date().toISOString();
  const entries: Array<[string, ResourceFile]> = await Promise.all(files.map(async ({ name, content }) => [
    name,
    await localResourceFile(name, content, await getProto(name), now),
  ]));
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
  const previous = await getScript(name);
  const file = await localResourceFile(name, content, previous);
  await set(name, file, scriptStore);
  notify();
  return file;
}

export async function addScriptFromBaseline(name: string, content: string): Promise<ResourceFile> {
  const file = await serverResourceFile(name, content);
  await set(name, file, scriptStore);
  notify();
  return file;
}

export async function addScripts(files: Array<{ name: string; content: string }>): Promise<void> {
  if (files.length === 0) return;
  const now = new Date().toISOString();
  const entries: Array<[string, ResourceFile]> = await Promise.all(files.map(async ({ name, content }) => [
    name,
    await localResourceFile(name, content, await getScript(name), now),
  ]));
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

// === Adapter (codec.lua) ===

const CODEC_LUA_KEY = 'codec.lua';
const CODEC_BASELINE_URL = `${BASELINE_PREFIX}/adapter/codec.lua`;

export async function getAdapterScript(): Promise<ResourceFile | undefined> {
  return get<ResourceFile>(CODEC_LUA_KEY, adapterStore);
}

export async function setAdapterScript(content: string): Promise<ResourceFile> {
  const previous = await getAdapterScript();
  const file = await localResourceFile(CODEC_LUA_KEY, content, previous);
  await set(CODEC_LUA_KEY, file, adapterStore);
  notify();
  return file;
}

export async function setAdapterScriptFromBaseline(content: string): Promise<ResourceFile> {
  const file = await serverResourceFile(CODEC_LUA_KEY, content);
  await set(CODEC_LUA_KEY, file, adapterStore);
  notify();
  return file;
}

export async function clearAdapterScript(): Promise<void> {
  await del(CODEC_LUA_KEY, adapterStore);
  notify();
}

// === Adapter (error.lua) ===

const ERROR_LUA_KEY = 'error.lua';

export async function getErrorMapScript(): Promise<ResourceFile | undefined> {
  return get<ResourceFile>(ERROR_LUA_KEY, adapterStore);
}

export async function setErrorMapScript(content: string): Promise<ResourceFile> {
  const previous = await getErrorMapScript();
  const file = await localResourceFile(ERROR_LUA_KEY, content, previous);
  await set(ERROR_LUA_KEY, file, adapterStore);
  notify();
  return file;
}

export async function setErrorMapScriptFromBaseline(content: string): Promise<ResourceFile> {
  const file = await serverResourceFile(ERROR_LUA_KEY, content);
  await set(ERROR_LUA_KEY, file, adapterStore);
  notify();
  return file;
}

export async function clearErrorMapScript(): Promise<void> {
  await del(ERROR_LUA_KEY, adapterStore);
  notify();
}

const REQUIRED_ADAPTER_FUNCTIONS = [
  'header_size',
  'body_length',
  'encode_tcp',
  'encode_udp',
  'decode_tcp',
  'decode_udp',
  'expected_route_key',
];

/** 检查适配器是否实现了所有必需函数，返回缺失的函数名列表 */
export async function validateAdapter(): Promise<string[]> {
  const file = await getAdapterScript();
  if (!file || !file.content.trim()) return REQUIRED_ADAPTER_FUNCTIONS;
  return REQUIRED_ADAPTER_FUNCTIONS.filter((fn) => {
    const content = file.content;
    return content.includes(`function ${fn}(`) || new RegExp(`\\b${fn}\\s*=`).test(content);
  }).length === 0
    ? []
    : REQUIRED_ADAPTER_FUNCTIONS.filter((fn) => {
        const content = file.content;
        return !content.includes(`function ${fn}(`) && !new RegExp(`\\b${fn}\\s*=`).test(content);
      });
}

// === 统一基线同步 ===

const LAST_BASELINE_KEY = 'stressbot:baseline:lastIndex';

interface LastBaselineIndex {
  proto: string[];
  script: string[];
  adapter: boolean;
}

function saveLastBaseline(index: LastBaselineIndex): void {
  try {
    localStorage.setItem(LAST_BASELINE_KEY, JSON.stringify(index));
  } catch {
    // localStorage 不可用，静默
  }
}

export type ResourceType = 'proto' | 'script' | 'adapter';

export interface SyncDiff {
  type: ResourceType;
  name: string;
  localContent: string;
  baselineContent: string;
}

export interface BaselineSyncResult {
  /** 基线有、IDB 没有 → 已自动写入 IDB */
  added: Array<{ type: ResourceType; name: string }>;
  /** 基线有、IDB 有、内容相同 */
  unchanged: Array<{ type: ResourceType; name: string }>;
  /** 基线有、IDB 有、内容不同 → 需要用户确认 */
  conflicts: SyncDiff[];
  /** 基线没有、IDB 有 → 需要用户确认 */
  removed: SyncDiff[];
}

export interface ConflictDecision {
  type: ResourceType;
  name: string;
  /** true = 保留本地版本，false = 采用基线版本（对 removed = false 则删除本地） */
  keepLocal: boolean;
}

export function hasSyncDiff(result: BaselineSyncResult | null | undefined): boolean {
  return !!result && (result.conflicts.length > 0 || result.removed.length > 0);
}

export function syncDiffIdentity(diff: SyncDiff): string {
  return `${diff.type}:${diff.name}`;
}

export function subtractSyncResult(base: BaselineSyncResult | null, handled: BaselineSyncResult): BaselineSyncResult | null {
  if (!base) return null;
  const handledKeys = new Set([...handled.conflicts, ...handled.removed].map(syncDiffIdentity));
  const conflicts = base.conflicts.filter((it) => !handledKeys.has(syncDiffIdentity(it)));
  const removed = base.removed.filter((it) => !handledKeys.has(syncDiffIdentity(it)));
  if (conflicts.length === 0 && removed.length === 0) return null;
  return { ...base, conflicts, removed };
}

export type ThreeWayKind =
  | 'unchanged'
  | 'legacyRepair'
  | 'localOnlyChanged'
  | 'serverOnlyChanged'
  | 'conflict'
  | 'serverRemovedOnly'
  | 'removedConflict';

export interface ThreeWayDecision {
  kind: ThreeWayKind;
  localHash: string;
  serverHash: string | null;
}

export async function compareResourceThreeWay(local: ResourceFile, serverContent: string | null): Promise<ThreeWayDecision> {
  const localHash = await hashResourceContent(local.content);
  const serverHash = serverContent === null ? null : await hashResourceContent(serverContent);
  const baseHash = local.baseHash;

  if (serverHash !== null && localHash === serverHash) {
    return { kind: baseHash === serverHash ? 'unchanged' : 'legacyRepair', localHash, serverHash };
  }
  if (baseHash === undefined) {
    return { kind: serverHash === null ? 'removedConflict' : 'conflict', localHash, serverHash };
  }
  if (serverHash === null) {
    if (baseHash === null) return { kind: 'localOnlyChanged', localHash, serverHash };
    return { kind: baseHash === localHash ? 'serverRemovedOnly' : 'removedConflict', localHash, serverHash };
  }
  if (baseHash === serverHash) {
    return { kind: 'localOnlyChanged', localHash, serverHash };
  }
  if (baseHash === localHash) {
    return { kind: 'serverOnlyChanged', localHash, serverHash };
  }
  return { kind: 'conflict', localHash, serverHash };
}

async function getResource(type: ResourceType, name: string): Promise<ResourceFile | undefined> {
  if (type === 'proto') return getProto(name);
  if (type === 'script') return getScript(name);
  return name === ERROR_LUA_KEY ? getErrorMapScript() : getAdapterScript();
}

async function writeBaselineResource(type: ResourceType, name: string, content: string): Promise<void> {
  if (type === 'proto') {
    await addProtoFromBaseline(name, content);
  } else if (type === 'script') {
    await addScriptFromBaseline(name, content);
  } else if (name === ERROR_LUA_KEY) {
    await setErrorMapScriptFromBaseline(content);
  } else {
    await setAdapterScriptFromBaseline(content);
  }
}

async function deleteResource(type: ResourceType, name: string): Promise<void> {
  if (type === 'proto') await del(name, protoStore);
  else if (type === 'script') await del(name, scriptStore);
  else if (name === ERROR_LUA_KEY) await del(ERROR_LUA_KEY, adapterStore);
  else await del(CODEC_LUA_KEY, adapterStore);
  notify();
}

async function setResourceBaseHash(type: ResourceType, name: string, baseHash: string | null): Promise<void> {
  const existing = await getResource(type, name);
  if (!existing) return;
  const next: ResourceFile = { ...existing, baseHash };
  if (type === 'proto') await set(name, next, protoStore);
  else if (type === 'script') await set(name, next, scriptStore);
  else await set(name, next, adapterStore);
}

export async function reconcileResourceWithServer(
  result: BaselineSyncResult,
  type: ResourceType,
  name: string,
  local: ResourceFile,
  serverContent: string | null,
): Promise<void> {
  const decision = await compareResourceThreeWay(local, serverContent);
  switch (decision.kind) {
    case 'unchanged':
      result.unchanged.push({ type, name });
      return;
    case 'legacyRepair':
      await setResourceBaseHash(type, name, decision.serverHash);
      result.unchanged.push({ type, name });
      return;
    case 'localOnlyChanged':
      return;
    case 'serverOnlyChanged':
      if (serverContent !== null) await writeBaselineResource(type, name, serverContent);
      return;
    case 'serverRemovedOnly':
      await deleteResource(type, name);
      return;
    case 'removedConflict':
      result.removed.push({ type, name, localContent: local.content, baselineContent: '' });
      return;
    case 'conflict':
      result.conflicts.push({ type, name, localContent: local.content, baselineContent: serverContent ?? '' });
      return;
  }
}

export async function markResourcesAsBaselineSynced(input: {
  protos?: ResourceFile[];
  scripts?: ResourceFile[];
  adapter?: ResourceFile | null;
  errorMap?: ResourceFile | null;
}): Promise<void> {
  const writes: Array<Promise<void>> = [];
  for (const f of input.protos ?? []) writes.push(setResourceBaseHash('proto', f.name, await hashResourceContent(f.content)));
  for (const f of input.scripts ?? []) writes.push(setResourceBaseHash('script', f.name, await hashResourceContent(f.content)));
  if (input.adapter) writes.push(setResourceBaseHash('adapter', CODEC_LUA_KEY, await hashResourceContent(input.adapter.content)));
  if (input.errorMap) writes.push(setResourceBaseHash('adapter', ERROR_LUA_KEY, await hashResourceContent(input.errorMap.content)));
  await Promise.all(writes);
  if (writes.length > 0) notify();
}

/**
 * 统一基线同步：按本地/基线/服务器三方判断资源状态。
 *
 * 算法：
 * 1. 并行 fetch proto/scripts index + adapter
 * 2. 本地没有、服务器有 → 自动写入本地并记录 baseHash
 * 3. 仅服务器修改 → 自动采用服务器版本
 * 4. 仅本地修改 → 保留本地，不提示冲突
 * 5. 双方都修改且内容不同 / 服务器删除但本地已修改 → 返回 conflicts/removed
 */
export async function syncResourcesFromBaseline(): Promise<BaselineSyncResult> {
  const result: BaselineSyncResult = {
    added: [],
    unchanged: [],
    conflicts: [],
    removed: [],
  };

  // 并行拉取基线索引和 adapter 文件
  const [protoIndex, scriptIndex, adapterText, errorMapText] = await Promise.all([
    fetchIndex(`${BASELINE_PREFIX}/proto/index.json`),
    fetchIndex(`${BASELINE_PREFIX}/scripts/index.json`),
    fetchFileText(CODEC_BASELINE_URL),
    fetchFileText(`${BASELINE_PREFIX}/adapter/error.lua`),
  ]);

  // --- Proto ---
  await syncFileGroup(protoIndex, 'proto', protoStore, `${BASELINE_PREFIX}/proto/`, result);

  // --- Scripts ---
  await syncFileGroup(scriptIndex, 'script', scriptStore, `${BASELINE_PREFIX}/scripts/`, result);

  // --- Adapter ---
  const existingAdapter = await getAdapterScript();
  if (!existingAdapter && adapterText !== null) {
    await setAdapterScriptFromBaseline(adapterText);
    result.added.push({ type: 'adapter', name: CODEC_LUA_KEY });
  } else if (existingAdapter) {
    await reconcileResourceWithServer(result, 'adapter', CODEC_LUA_KEY, existingAdapter, adapterText);
  }

  const existingErrorMap = await getErrorMapScript();
  if (!existingErrorMap && errorMapText !== null) {
    await setErrorMapScriptFromBaseline(errorMapText);
    result.added.push({ type: 'adapter', name: ERROR_LUA_KEY });
  } else if (existingErrorMap) {
    await reconcileResourceWithServer(result, 'adapter', ERROR_LUA_KEY, existingErrorMap, errorMapText);
  }

  // 同步完成后保存当前基线快照
  saveLastBaseline({
    proto: protoIndex,
    script: scriptIndex,
    adapter: adapterText !== null,
  });

  return result;
}

/**
 * 应用用户的冲突解决决策。
 */
export async function applyConflictResolution(decisions: ConflictDecision[]): Promise<void> {
  let changed = false;
  for (const d of decisions) {
    const baseline = await fetchResourceBaseline(d.type, d.name);
    if (d.keepLocal) {
      await setResourceBaseHash(d.type, d.name, baseline === null ? null : await hashResourceContent(baseline));
    } else if (baseline !== null) {
      await writeBaselineResource(d.type, d.name, baseline);
    } else {
      await deleteResource(d.type, d.name);
    }
    changed = true;
  }
  if (changed) notify();
}

// --- 内部辅助 ---

async function fetchIndex(url: string): Promise<string[]> {
  try {
    const resp = await fetch(url, { cache: 'no-cache' });
    if (!resp.ok) {
      console.warn(`[baseline] fetchIndex ${url} returned ${resp.status}`);
      return [];
    }
    return (await resp.json()) as string[];
  } catch (e) {
    console.warn(`[baseline] fetchIndex ${url} failed:`, e);
    return [];
  }
}

async function fetchFileText(url: string): Promise<string | null> {
  try {
    const resp = await fetch(url, { cache: 'no-cache' });
    if (!resp.ok) return null;
    return await resp.text();
  } catch {
    return null;
  }
}

async function fetchResourceBaseline(type: ResourceType, name: string): Promise<string | null> {
  if (type === 'proto') return fetchFileText(`${BASELINE_PREFIX}/proto/${encodeURIComponent(name)}`);
  if (type === 'script') return fetchFileText(`${BASELINE_PREFIX}/scripts/${encodeURIComponent(name)}`);
  if (name === ERROR_LUA_KEY) return fetchFileText(`${BASELINE_PREFIX}/adapter/error.lua`);
  return fetchFileText(CODEC_BASELINE_URL);
}

async function syncFileGroup(
  baselineNames: string[],
  type: ResourceType,
  store: ReturnType<typeof createStore>,
  urlPrefix: string,
  result: BaselineSyncResult,
): Promise<void> {
  // 收集 IDB 中已有的所有 key
  const idbKeys = new Set(
    ((await keys(store)) as IDBValidKey[]).map(String),
  );
  const baselineSet = new Set(baselineNames);

  // 基线有 → 对比
  const toFetch: string[] = [];
  for (const name of baselineNames) {
    const existing = await get<ResourceFile>(name, store);
    if (!existing) {
      toFetch.push(name); // IDB 没有，后面批量 fetch 再写入
    } else if (existing.content === '') {
      // 无法区分"内容就是空"和"元数据损坏"，fetch 基线对比
      toFetch.push(name);
    } else {
      // 需要基线内容来对比
      toFetch.push(name);
    }
  }

  // 批量 fetch 基线内容
  const baselineContents = new Map<string, string>();
  await Promise.all(
    toFetch.map(async (name) => {
      const text = await fetchFileText(urlPrefix + encodeURIComponent(name));
      if (text !== null) baselineContents.set(name, text);
    }),
  );

  for (const name of baselineNames) {
    const baseline = baselineContents.get(name);
    if (baseline === undefined) continue; // fetch 失败，跳过

    const existing = await get<ResourceFile>(name, store);
    if (!existing) {
      await writeBaselineResource(type, name, baseline);
      result.added.push({ type, name });
    } else {
      await reconcileResourceWithServer(result, type, name, existing, baseline);
    }
  }

  // 服务器没有、本地有：由 baseHash 判断是本地新增、服务器删除，还是旧数据未知历史。
  for (const key of idbKeys) {
    if (baselineSet.has(key)) continue;
    const existing = await get<ResourceFile>(key, store);
    if (existing) {
      await reconcileResourceWithServer(result, type, key, existing, null);
    }
  }

  if (result.added.length > 0) notify();
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

// === 基线回写 ===

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
