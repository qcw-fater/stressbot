/**
 * 用户上传的 proto / lua / adapter 资源管理（IndexedDB）。
 *
 * 设计要点：
 * - 不直接依赖 idb 库，使用 idb-keyval 提供的 createStore + key-value API；
 * - **每个 DB 只能挂一个 object store**（idb-keyval 不会触发 version upgrade 加 store），
 *   因此 proto / lua / adapter 各用一个独立 DB（与 library/templateStore.ts 保持一致）；
 * - 文件内容以 utf-8 字符串存储（proto / lua 都是文本），不存 ArrayBuffer，
 *   方便 JSON.stringify 调试与 Monaco 直接拿到 string；
 * - 资源版本号 hash 在 ProtoLoader 端基于内容拼接计算，store 不维护；
 * - 暴露 `subscribe` 给 React 组件订阅"资源变更"事件，配合 useSyncExternalStore。
 *
 * 基线同步：所有资源统一通过内容对比与基线同步，变更时由 UI 组件展示冲突面板供用户确认。
 */

import { clear, createStore, del, get, keys, set, setMany } from 'idb-keyval';
import { API_PREFIX, BASELINE_PREFIX } from './env';

const PROTO_DB = 'stressbot-resources-proto';
const SCRIPT_DB = 'stressbot-resources-scripts';
const LEGACY_DB = 'stressbot-resources';

export interface ResourceFile {
  name: string;
  content: string;
  size: number;
  uploadedAt: string;
}

const ADAPTER_DB = 'stressbot-resources-adapter';

const protoStore = createStore(PROTO_DB, 'data');
const scriptStore = createStore(SCRIPT_DB, 'data');
const adapterStore = createStore(ADAPTER_DB, 'data');

// 模块加载时异步触发迁移；失败静默（旧 DB 不存在 / 已损坏都按"无需迁移"处理）。
void migrateLegacyResources();

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

// === Adapter (codec.lua) ===

const CODEC_LUA_KEY = 'codec.lua';
const CODEC_BASELINE_URL = `${BASELINE_PREFIX}/adapter/codec.lua`;

export async function getAdapterScript(): Promise<ResourceFile | undefined> {
  return get<ResourceFile>(CODEC_LUA_KEY, adapterStore);
}

export async function setAdapterScript(content: string): Promise<ResourceFile> {
  const file: ResourceFile = {
    name: CODEC_LUA_KEY,
    content,
    size: byteLength(content),
    uploadedAt: new Date().toISOString(),
  };
  await set(CODEC_LUA_KEY, file, adapterStore);
  notify();
  return file;
}

export async function clearAdapterScript(): Promise<void> {
  await clear(adapterStore);
  notify();
}

const REQUIRED_ADAPTER_FUNCTIONS = [
  'header_size',
  'body_length',
  'encode_tcp',
  'encode_udp',
  'decode_tcp',
  'decode_udp',
  'expected_response_key',
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

/**
 * 确保 IDB 中有 codec.lua：有则保留，没有则从基线 `/conf/adapter/codec.lua` 拉取。
 * 返回最终的文件内容（无论来源）。
 */
export async function ensureAdapterScript(): Promise<string | null> {
  const existing = await getAdapterScript();
  if (existing) return existing.content;
  try {
    const resp = await fetch(CODEC_BASELINE_URL);
    if (!resp.ok) return null;
    const text = await resp.text();
    await setAdapterScript(text);
    return text;
  } catch {
    return null;
  }
}

// === 统一基线同步 ===

const LAST_BASELINE_KEY = 'stressbot:baseline:lastIndex';

interface LastBaselineIndex {
  proto: string[];
  script: string[];
  adapter: boolean;
}

function loadLastBaseline(): LastBaselineIndex | null {
  try {
    const raw = localStorage.getItem(LAST_BASELINE_KEY);
    return raw ? JSON.parse(raw) : null;
  } catch {
    return null;
  }
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

/**
 * 统一基线同步：对比 IDB 与服务端基线，自动新增，冲突/删除返回给调用方处理。
 *
 * 算法：
 * 1. 并行 fetch proto/scripts index + adapter
 * 2. 对每个基线文件与 IDB 内容对比
 * 3. 新增 → 自动写入 IDB
 * 4. 内容不同 / 基线已删除 → 返回 conflicts/removed
 */
export async function syncResourcesFromBaseline(): Promise<BaselineSyncResult> {
  const result: BaselineSyncResult = {
    added: [],
    unchanged: [],
    conflicts: [],
    removed: [],
  };

  // 并行拉取三个基线索引
  const [protoIndex, scriptIndex, adapterText] = await Promise.all([
    fetchIndex(`${BASELINE_PREFIX}/proto/index.json`),
    fetchIndex(`${BASELINE_PREFIX}/scripts/index.json`),
    fetchFileText(CODEC_BASELINE_URL),
  ]);

  // 加载上次基线快照，用于区分"本地新建"和"远端已删除"
  const lastBaseline = loadLastBaseline();
  const lastProtoSet = new Set(lastBaseline?.proto ?? []);
  const lastScriptSet = new Set(lastBaseline?.script ?? []);
  const hadAdapter = lastBaseline?.adapter ?? false;

  // --- Proto ---
  await syncFileGroup(protoIndex, 'proto', protoStore, `${BASELINE_PREFIX}/proto/`, result, lastProtoSet);

  // --- Scripts ---
  await syncFileGroup(scriptIndex, 'script', scriptStore, `${BASELINE_PREFIX}/scripts/`, result, lastScriptSet);

  // --- Adapter ---
  const existingAdapter = await getAdapterScript();
  if (adapterText !== null) {
    if (!existingAdapter) {
      await setAdapterScript(adapterText);
      result.added.push({ type: 'adapter', name: CODEC_LUA_KEY });
    } else if (existingAdapter.content === adapterText) {
      result.unchanged.push({ type: 'adapter', name: CODEC_LUA_KEY });
    } else {
      result.conflicts.push({
        type: 'adapter',
        name: CODEC_LUA_KEY,
        localContent: existingAdapter.content,
        baselineContent: adapterText,
      });
    }
  } else if (existingAdapter && hadAdapter) {
    // 仅当上次快照中 adapter 存在时才算"远端已删除"
    result.removed.push({
      type: 'adapter',
      name: CODEC_LUA_KEY,
      localContent: existingAdapter.content,
      baselineContent: '',
    });
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
    if (d.keepLocal) continue; // 保留本地，不需要操作

    if (d.type === 'adapter') {
      // adapter removed + keepLocal=false → 删除
      const baselineText = await fetchFileText(CODEC_BASELINE_URL);
      if (baselineText !== null) {
        await setAdapterScript(baselineText);
      } else {
        await clear(adapterStore);
      }
    } else if (d.type === 'proto') {
      const baseline = await fetchFileText(`${BASELINE_PREFIX}/proto/${encodeURIComponent(d.name)}`);
      if (baseline !== null) {
        await addProto(d.name, baseline);
      } else {
        await del(d.name, protoStore);
      }
    } else {
      const baseline = await fetchFileText(`${BASELINE_PREFIX}/scripts/${encodeURIComponent(d.name)}`);
      if (baseline !== null) {
        await addScript(d.name, baseline);
      } else {
        await del(d.name, scriptStore);
      }
    }
    changed = true;
  }
  if (changed) notify();
}

// --- 内部辅助 ---

async function fetchIndex(url: string): Promise<string[]> {
  try {
    const resp = await fetch(url);
    if (!resp.ok) return [];
    return (await resp.json()) as string[];
  } catch {
    return [];
  }
}

async function fetchFileText(url: string): Promise<string | null> {
  try {
    const resp = await fetch(url);
    if (!resp.ok) return null;
    return await resp.text();
  } catch {
    return null;
  }
}

async function syncFileGroup(
  baselineNames: string[],
  type: ResourceType,
  store: ReturnType<typeof createStore>,
  urlPrefix: string,
  result: BaselineSyncResult,
  lastBaselineNames: Set<string>,
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
      // 新增 → 自动写入
      const file: ResourceFile = {
        name,
        content: baseline,
        size: byteLength(baseline),
        uploadedAt: new Date().toISOString(),
      };
      await set(name, file, store);
      result.added.push({ type, name });
    } else if (existing.content === baseline) {
      result.unchanged.push({ type, name });
    } else {
      result.conflicts.push({
        type,
        name,
        localContent: existing.content,
        baselineContent: baseline,
      });
    }
  }

  // 基线没有、IDB 有、且上次基线快照中存在 → 远端已删除（真冲突）
  // 基线没有、IDB 有、但上次快照中也不存在 → 本地新建，不算冲突
  for (const key of idbKeys) {
    if (!baselineSet.has(key) && lastBaselineNames.has(key)) {
      const existing = await get<ResourceFile>(key, store);
      if (existing) {
        result.removed.push({
          type,
          name: key,
          localContent: existing.content,
          baselineContent: '',
        });
      }
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

/**
 * 将 IDB 中所有资源推送到后端磁盘基线（svn commit）。
 * 仅在启动任务时由后端 writeBaselineFiles 调用；前端不主动调用。
 */
export async function pushResourcesToBaseline(): Promise<void> {
  try {
    const [protos, scripts, adapter] = await Promise.all([listProto(), listScript(), getAdapterScript()]);
    const fd = new FormData();

    for (const p of protos) {
      fd.append(`proto/${p.name}`, new Blob([p.content]), p.name);
    }
    for (const s of scripts) {
      fd.append(`scripts/${s.name}`, new Blob([s.content]), s.name);
    }
    if (adapter) {
      fd.append('adapter/codec.lua', new Blob([adapter.content]), 'codec.lua');
    }

    await fetch(`${API_PREFIX}/resources/baseline`, { method: 'POST', body: fd });
  } catch {
    // 静默：基线不可用时不阻塞编辑器
  }
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
