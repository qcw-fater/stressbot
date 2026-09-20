/**
 * 用户上传的定义文件、脚本、协议配置资源管理（本地存储）。
 *
 * 设计要点：
 * - 不直接依赖 idb 库，使用 idb-keyval 提供的 createStore + key-value API；
 * - **每个 DB 只能挂一个 object store**（idb-keyval 不会触发 version upgrade 加 store），
 *   因此定义文件、脚本、协议配置各用一个独立 DB（与 library/templateStore.ts 保持一致）；
 * - 文件内容以 utf-8 字符串存储（定义文件 / 脚本 / 协议配置都是文本），不存 ArrayBuffer，
 *   方便 JSON.stringify 调试与 Monaco 直接拿到 string；
 * - ResourceFile.baseHash 记录上次确认同步到的服务器内容 hash，按 Git/SVN 工作副本语义做三方判断；
 * - 暴露 `subscribe` 给 React 组件订阅"资源变更"事件，配合 useSyncExternalStore。
 *
 * 基线同步：本地/基线/服务器三方比较，只有双方都修改且内容不同时才需要用户处理冲突。
 */

import { clear, createStore, del, delMany, get, getMany, keys, set, setMany } from 'idb-keyval';
import {
  fetchBaselineCodec,
  fetchBaselineCodecIndex,
  fetchBaselineProtoContent,
  fetchBaselineProtoIndex,
  fetchBaselineResource,
  fetchBaselineScript,
  fetchBaselineScriptIndex,
} from './baselineApi';
import { validateCodecSchema } from './codecSchemaValidation';

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

export type ResourceType = 'proto' | 'script' | 'adapter';

const ADAPTER_DB = 'stressbot-resources-adapter';

const protoStore = createStore(PROTO_DB, 'data');
const scriptStore = createStore(SCRIPT_DB, 'data');
const adapterStore = createStore(ADAPTER_DB, 'data');

async function listResourceStore(
  store: ReturnType<typeof createStore>,
  includeKey: (key: IDBValidKey) => boolean = () => true,
): Promise<ResourceFile[]> {
  const selectedKeys = ((await keys(store)) as IDBValidKey[]).filter(includeKey);
  if (selectedKeys.length === 0) return [];
  const items = (await getMany<ResourceFile | undefined>(selectedKeys, store)).filter(
    (item): item is ResourceFile => Boolean(item),
  );
  items.sort((a, b) => a.name.localeCompare(b.name));
  return items;
}

async function replaceResourceStore(
  store: ReturnType<typeof createStore>,
  files: readonly ResourceFile[],
): Promise<void> {
  const existingKeys = await keys(store);
  if (files.length > 0) {
    await setMany(
      files.map((file) => [file.name, { ...file }]),
      store,
    );
  }
  const nextNames = new Set(files.map((file) => file.name));
  const obsoleteKeys = existingKeys.filter((key) => typeof key !== 'string' || !nextNames.has(key));
  if (obsoleteKeys.length > 0) await delMany(obsoleteKeys, store);
}

export async function hashResourceContent(content: string): Promise<string> {
  const buf = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(content));
  const hex = Array.from(new Uint8Array(buf))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('');
  return `sha256:${hex}`;
}

async function localResourceFile(
  name: string,
  content: string,
  previous?: ResourceFile,
  uploadedAt = new Date().toISOString(),
): Promise<ResourceFile> {
  return {
    name,
    content,
    size: byteLength(content),
    uploadedAt,
    baseHash: previous?.baseHash ?? null,
  };
}

async function serverResourceFile(
  name: string,
  content: string,
  uploadedAt = new Date().toISOString(),
): Promise<ResourceFile> {
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
  const file = await storeBaselineResource('proto', name, content);
  notify();
  return file;
}

export async function addProtos(files: Array<{ name: string; content: string }>): Promise<void> {
  if (files.length === 0) return;
  const now = new Date().toISOString();
  const entries: Array<[string, ResourceFile]> = await Promise.all(
    files.map(async ({ name, content }) => [
      name,
      await localResourceFile(name, content, await getProto(name), now),
    ]),
  );
  await setMany(entries, protoStore);
  notify();
}

export async function getProto(name: string): Promise<ResourceFile | undefined> {
  return get<ResourceFile>(name, protoStore);
}

export async function listProto(): Promise<ResourceFile[]> {
  return listResourceStore(protoStore);
}

export async function removeProto(name: string): Promise<void> {
  await del(name, protoStore);
  notify();
}

export async function clearProto(): Promise<void> {
  await clear(protoStore);
  notify();
}

export async function replaceProtoFiles(files: readonly ResourceFile[]): Promise<void> {
  await replaceResourceStore(protoStore, files);
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
  const file = await storeBaselineResource('script', name, content);
  notify();
  return file;
}

export async function addScripts(files: Array<{ name: string; content: string }>): Promise<void> {
  if (files.length === 0) return;
  const now = new Date().toISOString();
  const entries: Array<[string, ResourceFile]> = await Promise.all(
    files.map(async ({ name, content }) => [
      name,
      await localResourceFile(name, content, await getScript(name), now),
    ]),
  );
  await setMany(entries, scriptStore);
  notify();
}

export async function getScript(name: string): Promise<ResourceFile | undefined> {
  return get<ResourceFile>(name, scriptStore);
}

export async function listScript(): Promise<ResourceFile[]> {
  return listResourceStore(scriptStore);
}

export async function removeScript(name: string): Promise<void> {
  await del(name, scriptStore);
  notify();
}

export async function clearScript(): Promise<void> {
  await clear(scriptStore);
  notify();
}

export async function replaceScriptFiles(files: readonly ResourceFile[]): Promise<void> {
  await replaceResourceStore(scriptStore, files);
  notify();
}

// === Codec（按连接多份 <protocol>_<service>_codec.json）===
//
// 协议配置采用按连接多份 `<protocol>_<service>_codec.json` + 共享 errors.json。
// 底层沿用历史命名的 adapterStore 作为本地存储命名空间；每份 codec = 一个 key（文件名），
// 共享 errors.json 单独一份 key。

const CODEC_FILE_SUFFIX = '_codec.json';
const ERRORS_JSON_KEY = 'errors.json';

/** 校验 name 是否符合 `<protocol>_<service>_codec.json` 命名（防误把 errors.json 当 *_codec.json 存）。 */
function assertCodecFileName(name: string): void {
  if (!name.endsWith(CODEC_FILE_SUFFIX)) {
    throw new Error(`codec 文件名必须以 "${CODEC_FILE_SUFFIX}" 结尾（当前：${name}）`);
  }
}

/** 每连接一份 codec（key = 文件名，如 'tcp_logic_codec.json'）。 */
export async function getCodecSchema(name: string): Promise<ResourceFile | undefined> {
  assertCodecFileName(name);
  return get<ResourceFile>(name, adapterStore);
}

export async function setCodecSchema(name: string, content: string): Promise<ResourceFile> {
  assertCodecFileName(name);
  const previous = await get<ResourceFile>(name, adapterStore);
  const file = await localResourceFile(name, content, previous);
  await set(name, file, adapterStore);
  notify();
  return file;
}

export async function setCodecSchemaFromBaseline(
  name: string,
  content: string,
): Promise<ResourceFile> {
  const file = await storeBaselineResource('adapter', name, content);
  notify();
  return file;
}

export async function clearCodecSchema(name: string): Promise<void> {
  assertCodecFileName(name);
  await del(name, adapterStore);
  notify();
}

/** 列出所有 *_codec.json（不含 errors.json），按 name 排序。 */
export async function listCodecFiles(): Promise<ResourceFile[]> {
  return listResourceStore(adapterStore, (key) => String(key).endsWith(CODEC_FILE_SUFFIX));
}

export async function replaceCodecFiles(files: readonly ResourceFile[]): Promise<void> {
  const errorMap = await getErrorMap();
  await replaceResourceStore(adapterStore, errorMap ? [...files, errorMap] : files);
  notify();
}

// === 共享错误表 errors.json（单份，key = 'errors.json'）===

export async function getErrorMap(): Promise<ResourceFile | undefined> {
  return get<ResourceFile>(ERRORS_JSON_KEY, adapterStore);
}

export async function setErrorMap(content: string): Promise<ResourceFile> {
  const previous = await getErrorMap();
  const file = await localResourceFile(ERRORS_JSON_KEY, content, previous);
  await set(ERRORS_JSON_KEY, file, adapterStore);
  notify();
  return file;
}

export async function setErrorMapFromBaseline(content: string): Promise<ResourceFile> {
  const file = await storeBaselineResource('adapter', ERRORS_JSON_KEY, content);
  notify();
  return file;
}

export async function clearErrorMap(): Promise<void> {
  await del(ERRORS_JSON_KEY, adapterStore);
  notify();
}

export async function replaceErrorMap(file: ResourceFile | null): Promise<void> {
  if (file) {
    await set(ERRORS_JSON_KEY, { ...file }, adapterStore);
  } else {
    await del(ERRORS_JSON_KEY, adapterStore);
  }
  notify();
}

/** 读取所有 *_codec.json，逐份校验，汇总错误（每条带文件名前缀）。 */
export async function collectCodecSchemaErrors(): Promise<string[]> {
  const files = await listCodecFiles();
  const out: string[] = [];
  for (const f of files) {
    const errs = validateCodecSchema(f.content);
    for (const e of errs) {
      out.push(`[${f.name}] ${e}`);
    }
  }
  return out;
}

// === 统一基线同步 ===

const LAST_BASELINE_KEY = 'stressbot:baseline:lastIndex';

interface LastBaselineIndex {
  proto: string[];
  script: string[];
  adapter: string[];
}

function saveLastBaseline(index: LastBaselineIndex): void {
  try {
    localStorage.setItem(LAST_BASELINE_KEY, JSON.stringify(index));
  } catch {
    // localStorage 不可用，静默
  }
}

export interface SyncDiff {
  type: ResourceType;
  name: string;
  localContent: string;
  baselineContent: string;
}

export interface BaselineSyncResult {
  /** 基线有、本地存储没有 → 已自动写入本地存储 */
  added: Array<{ type: ResourceType; name: string }>;
  /** 基线有、本地存储有、内容相同 */
  unchanged: Array<{ type: ResourceType; name: string }>;
  /** 基线有、本地存储有、内容不同 → 需要用户确认 */
  conflicts: SyncDiff[];
  /** 基线没有、本地存储有 → 需要用户确认 */
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

export function subtractSyncResult(
  base: BaselineSyncResult | null,
  handled: BaselineSyncResult,
): BaselineSyncResult | null {
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

export async function compareResourceThreeWay(
  local: ResourceFile,
  serverContent: string | null,
): Promise<ThreeWayDecision> {
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
    return {
      kind: baseHash === localHash ? 'serverRemovedOnly' : 'removedConflict',
      localHash,
      serverHash,
    };
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
  return name === ERRORS_JSON_KEY ? getErrorMap() : getCodecSchema(name);
}

async function writeBaselineResource(
  type: ResourceType,
  name: string,
  content: string,
  shouldNotify = true,
): Promise<void> {
  await storeBaselineResource(type, name, content);
  if (shouldNotify) notify();
}

async function deleteResource(
  type: ResourceType,
  name: string,
  shouldNotify = true,
): Promise<void> {
  if (type === 'proto') await del(name, protoStore);
  else if (type === 'script') await del(name, scriptStore);
  else await del(name, adapterStore);
  if (shouldNotify) notify();
}

async function setResourceBaseHash(
  type: ResourceType,
  name: string,
  baseHash: string | null,
): Promise<void> {
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
  shouldNotify = true,
): Promise<boolean> {
  const decision = await compareResourceThreeWay(local, serverContent);
  switch (decision.kind) {
    case 'unchanged':
      result.unchanged.push({ type, name });
      return false;
    case 'legacyRepair':
      await setResourceBaseHash(type, name, decision.serverHash);
      result.unchanged.push({ type, name });
      return false;
    case 'localOnlyChanged':
      return false;
    case 'serverOnlyChanged':
      if (serverContent !== null) {
        await writeBaselineResource(type, name, serverContent, shouldNotify);
        return true;
      }
      return false;
    case 'serverRemovedOnly':
      await deleteResource(type, name, shouldNotify);
      return true;
    case 'removedConflict':
      result.removed.push({ type, name, localContent: local.content, baselineContent: '' });
      return false;
    case 'conflict':
      result.conflicts.push({
        type,
        name,
        localContent: local.content,
        baselineContent: serverContent ?? '',
      });
      return false;
  }
}

export async function markResourcesAsBaselineSynced(input: {
  protos?: ResourceFile[];
  scripts?: ResourceFile[];
  codecs?: ResourceFile[];
  errorMap?: ResourceFile | null;
}): Promise<void> {
  const writes: Array<Promise<void>> = [];
  for (const f of input.protos ?? [])
    writes.push(setResourceBaseHash('proto', f.name, await hashResourceContent(f.content)));
  for (const f of input.scripts ?? [])
    writes.push(setResourceBaseHash('script', f.name, await hashResourceContent(f.content)));
  for (const f of input.codecs ?? [])
    writes.push(setResourceBaseHash('adapter', f.name, await hashResourceContent(f.content)));
  if (input.errorMap)
    writes.push(
      setResourceBaseHash(
        'adapter',
        ERRORS_JSON_KEY,
        await hashResourceContent(input.errorMap.content),
      ),
    );
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
 *
 * **失败保护**：任一资源组的索引请求失败（断网 / 非 2xx / 解析异常）都会抛出
 * `BaselineIndexFetchError` 并中止本次同步——绝不能把失败的空结果折叠成「服务器删光了」，
 * 否则会按 serverRemovedOnly 静默删除本地已同步的资源。
 */
export async function syncResourcesFromBaseline(): Promise<BaselineSyncResult> {
  const result: BaselineSyncResult = {
    added: [],
    unchanged: [],
    conflicts: [],
    removed: [],
  };

  // 并行拉取各资源组基线索引。null 表示该组请求失败——直接中止，不做任何删除对账。
  const [protoIndex, scriptIndex, codecIndex] = await Promise.all([
    fetchBaselineProtoIndex(),
    fetchBaselineScriptIndex(),
    fetchBaselineCodecIndex(),
  ]);
  if (protoIndex === null || scriptIndex === null || codecIndex === null) {
    throw new BaselineIndexFetchError();
  }

  // --- Proto ---
  let changed = await syncFileGroup(
    protoIndex,
    'proto',
    protoStore,
    fetchBaselineProtoContent,
    result,
  );

  // --- Scripts ---
  if (await syncFileGroup(scriptIndex, 'script', scriptStore, fetchBaselineScript, result)) {
    changed = true;
  }

  // --- Adapter（多 codec + errors.json，与 proto/scripts 同款走 syncFileGroup）---
  if (await syncFileGroup(codecIndex, 'adapter', adapterStore, fetchBaselineCodec, result)) {
    changed = true;
  }

  // 同步完成后保存当前基线快照
  saveLastBaseline({
    proto: protoIndex,
    script: scriptIndex,
    adapter: codecIndex,
  });

  if (changed) notify();

  return result;
}

/** 基线索引请求失败（用于上层给出中文提示，区别于普通业务错误）。 */
export class BaselineIndexFetchError extends Error {
  constructor() {
    super('基线索引请求失败，已中止本次同步以保护本地资源');
    this.name = 'BaselineIndexFetchError';
  }
}

/** 基线单文件请求失败（同样中止处理，避免把网络抖动当成服务器删除）。 */
export class BaselineFileFetchError extends Error {
  constructor(name: string) {
    super(`资源「${name}」拉取失败，已中止处理以保护本地内容，请稍后重试`);
    this.name = 'BaselineFileFetchError';
  }
}

/**
 * 应用用户的冲突解决决策。
 *
 * 逐项拉取服务器版本：拉取失败会立刻抛错中止，因为此时无法判断服务器上到底有没有这个
 * 文件——「采用服务器」分支会把未知读成删除、「保留本地」分支会把 baseHash 清成 null
 * 假装已对齐，两条都会造成数据损失。已处理的决策保持生效，用户重试即可续上。
 */
export async function applyConflictResolution(decisions: ConflictDecision[]): Promise<void> {
  let changed = false;
  for (const d of decisions) {
    const baseline = await fetchBaselineResource(d.type, d.name);
    if (baseline.status === 'error') {
      if (changed) notify();
      throw new BaselineFileFetchError(d.name);
    }
    const content = baseline.status === 'ok' ? baseline.content : null;
    if (d.keepLocal) {
      await setResourceBaseHash(
        d.type,
        d.name,
        content === null ? null : await hashResourceContent(content),
      );
    } else if (content !== null) {
      await writeBaselineResource(d.type, d.name, content, false);
    } else {
      await deleteResource(d.type, d.name, false);
    }
    changed = true;
  }
  if (changed) notify();
}

// --- 内部辅助 ---

async function storeBaselineResource(
  type: ResourceType,
  name: string,
  content: string,
): Promise<ResourceFile> {
  if (type === 'adapter' && name !== ERRORS_JSON_KEY) assertCodecFileName(name);
  const file = await serverResourceFile(name, content);
  if (type === 'proto') await set(name, file, protoStore);
  else if (type === 'script') await set(name, file, scriptStore);
  else await set(name, file, adapterStore);
  return file;
}

async function syncFileGroup(
  baselineNames: string[],
  type: ResourceType,
  store: ReturnType<typeof createStore>,
  fetchBaseline: (name: string) => Promise<string | null>,
  result: BaselineSyncResult,
): Promise<boolean> {
  // 收集本地存储中已有的所有 key
  const idbKeys = ((await keys(store)) as IDBValidKey[]).map(String);
  const localEntries = await Promise.all(
    idbKeys.map(async (name) => [name, await get<ResourceFile>(name, store)] as const),
  );
  const localByName = new Map(localEntries);
  const baselineSet = new Set(baselineNames);

  // 批量 fetch 基线内容
  const baselineContents = new Map<string, string>();
  await Promise.all(
    baselineNames.map(async (name) => {
      const text = await fetchBaseline(name);
      if (text !== null) baselineContents.set(name, text);
    }),
  );

  let changed = false;
  for (const name of baselineNames) {
    const baseline = baselineContents.get(name);
    if (baseline === undefined) continue; // fetch 失败，跳过

    const existing = localByName.get(name);
    if (!existing) {
      await writeBaselineResource(type, name, baseline, false);
      result.added.push({ type, name });
      changed = true;
    } else {
      if (await reconcileResourceWithServer(result, type, name, existing, baseline, false)) {
        changed = true;
      }
    }
  }

  // 服务器没有、本地有：由 baseHash 判断是本地新增、服务器删除，还是旧数据未知历史。
  for (const key of idbKeys) {
    if (baselineSet.has(key)) continue;
    const existing = localByName.get(key);
    if (existing) {
      if (await reconcileResourceWithServer(result, type, key, existing, null, false)) {
        changed = true;
      }
    }
  }

  return changed;
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
        // 把刚被我们意外创建的空 DB 删掉，避免污染浏览器本地数据库列表
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
