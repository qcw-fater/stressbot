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

import { clear, createStore, del, get, keys, set, setMany } from 'idb-keyval';
import { BASELINE_PREFIX } from './env';
import { fetchBaselineCodecIndex, fetchBaselineCodec } from './baselineApi';
import type { CodecSchema } from '@/types/codec';
import type { BindingType, FieldBind, FilterDef, HeartbeatField, HeartbeatFieldSource, HeartbeatFieldType } from '@/types/action';
import {
  ALL_BINDING_TYPES,
  ALL_HEARTBEAT_FIELD_SOURCES,
  ALL_HEARTBEAT_FIELD_TYPES,
} from '@/types/action';
import {
  FIELD_TYPE_WIDTH,
  FIELD_ROLES,
  PIPELINE_OPS,
  PRODUCE_REGIONS,
  OVER_KINDS,
  GUARD_OPS,
  ON_ERROR,
  VALUE_SOURCE_KINDS_SUPPORTED,
} from '@/types/codec';

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

export async function setCodecSchemaFromBaseline(name: string, content: string): Promise<ResourceFile> {
  assertCodecFileName(name);
  const file = await serverResourceFile(name, content);
  await set(name, file, adapterStore);
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
  const allKeys = (await keys(adapterStore)) as IDBValidKey[];
  const items: ResourceFile[] = [];
  for (const k of allKeys) {
    const name = String(k);
    if (!name.endsWith(CODEC_FILE_SUFFIX)) continue;
    const v = await get<ResourceFile>(k, adapterStore);
    if (v) items.push(v);
  }
  items.sort((a, b) => a.name.localeCompare(b.name));
  return items;
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
  const file = await serverResourceFile(ERRORS_JSON_KEY, content);
  await set(ERRORS_JSON_KEY, file, adapterStore);
  notify();
  return file;
}

export async function clearErrorMap(): Promise<void> {
  await del(ERRORS_JSON_KEY, adapterStore);
  notify();
}

// === validateCodecSchema（镜像 codec/schema.go 的 Validate）===
//
// 纯结构校验、同步、聚合所有错误一次性返回（空数组=通过）。逐条对齐 Go Validate，
// 避免前后端漂移。**注意**：algo 是否在注册表中这一条**前端不做**——由后端
// 算法清单端点（GET /sbot/codec/algorithms）权威，此处仅校验 algo 非空。

const CHECKSUM_FROM_RE = /^([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)$/;
const ROUTE_KEY_PLACEHOLDER_RE = /\{([A-Za-z_][A-Za-z0-9_]*)\}/g;

class ErrCollector {
  msgs: string[] = [];
  add(msg: string): void {
    this.msgs.push(msg);
  }
}

function isFieldTypeKnown(t: string): boolean {
  return t in FIELD_TYPE_WIDTH;
}

/** 对单份 codec.json 文本做结构校验，返回中文错误数组（空=通过）。纯函数、同步。 */
export function validateCodecSchema(content: string): string[] {
  let parsed: unknown;
  try {
    parsed = JSON.parse(content);
  } catch (e) {
    return [`codec 配置不是合法 JSON：${(e as Error).message}`];
  }
  const ec = new ErrCollector();
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return ['codec 配置不是合法 JSON 对象'];
  }
  const s = parsed as CodecSchema;
  validateBase(s, ec);
  validateHeader(s, ec);
  validateRouteKeyTemplate(s, ec);
  validatePipeline(s, ec);
  validateHeartbeat(s, ec);
  return ec.msgs;
}

function validateBase(s: CodecSchema, ec: ErrCollector): void {
  if (s.version !== 1) {
    ec.add(`codec schema version 必须为 1（当前 ${s.version}）`);
  }
  if (s.endianDefault !== 'le' && s.endianDefault !== 'be') {
    ec.add(`endianDefault 必须为 le 或 be（当前 ${JSON.stringify(s.endianDefault ?? '')}）`);
  }
  const headerSize = s.frame?.headerSize;
  if (typeof headerSize !== 'number' || headerSize <= 0) {
    ec.add(`frame.headerSize 必须大于 0（当前 ${headerSize ?? ''}）`);
  }
  const trailerSize = s.frame?.trailerSize;
  if (typeof trailerSize === 'number' && trailerSize < 0) {
    ec.add(`frame.trailerSize 不能为负（当前 ${trailerSize}）`);
  }
  if (!s.routeKeyTemplate || s.routeKeyTemplate.trim() === '') {
    ec.add('routeKeyTemplate 不能为空');
  }
}

function validateHeader(s: CodecSchema, ec: ErrCollector): void {
  const headerSize = s.frame?.headerSize ?? 0;
  const header = Array.isArray(s.header) ? s.header : [];

  // 字段名唯一 + 基础属性 + type/role/endian
  const names = new Map<string, number>(); // name → first index
  for (let i = 0; i < header.length; i++) {
    const f = header[i] ?? {};
    let prefix = `header 字段 "${f.name ?? ''}" (index ${i})`;
    const name = typeof f.name === 'string' ? f.name : '';
    if (!name || name.trim() === '') {
      ec.add(`header 字段名不能为空 (index ${i})`);
      prefix = `header 字段 (index ${i})`;
    } else if (names.has(name)) {
      ec.add(`header 字段名 "${name}" 重复（与 index ${names.get(name)} 冲突，字段名必须唯一）`);
    } else {
      names.set(name, i);
    }
    const offset = typeof f.offset === 'number' ? f.offset : 0;
    const size = typeof f.size === 'number' ? f.size : 0;
    if (offset < 0) {
      ec.add(`${prefix}：offset 不能为负（当前 ${offset}）`);
    }
    if (size <= 0) {
      ec.add(`${prefix}：size 必须大于 0（当前 ${size}）`);
    }
    if (offset < 0 || offset + size > headerSize) {
      ec.add(`${prefix}：物理区间 [offset=${offset}, offset+size=${offset + size}) 越界（headerSize=${headerSize}）`);
    }
    const t = typeof f.type === 'string' ? f.type : '';
    if (!isFieldTypeKnown(t)) {
      ec.add(`${prefix}：未知 type "${t}"`);
    } else {
      const width = FIELD_TYPE_WIDTH[t];
      if (width > 0) {
        if (size !== width) {
          ec.add(`${prefix}：type "${t}" 的 size 必须为 ${width}（当前 ${size}）`);
        }
      } else {
        // bytes
        if (size <= 0) {
          ec.add(`${prefix}：type bytes 必须显式指定 size>0`);
        }
      }
    }
    if (f.endian && f.endian !== '' && f.endian !== 'le' && f.endian !== 'be') {
      ec.add(`${prefix}：endian 必须为 le 或 be（当前 "${f.endian}"）`);
    }
    if (!(FIELD_ROLES as readonly string[]).includes(f.role ?? '')) {
      ec.add(`${prefix}：未知 role "${f.role ?? ''}"`);
    }
  }

  // 物理区间不重叠
  interface Span {
    name: string;
    start: number;
    end: number;
  }
  const spans: Span[] = [];
  for (const f of header) {
    const offset = typeof f?.offset === 'number' ? f.offset : 0;
    const size = typeof f?.size === 'number' ? f.size : 0;
    if (offset < 0 || size <= 0) continue; // 已报错
    spans.push({ name: f.name ?? '', start: offset, end: offset + size });
  }
  spans.sort((a, b) => a.start - b.start);
  for (let i = 1; i < spans.length; i++) {
    if (spans[i].start < spans[i - 1].end) {
      ec.add(
        `header 字段物理区间重叠：${spans[i - 1].name} [offset=${spans[i - 1].start},+${spans[i - 1].end - spans[i - 1].start}) 与 ${spans[i].name} [offset=${spans[i].start},+${spans[i].end - spans[i].start})`,
      );
    }
  }

  // role 统计 + flags/checksumOut/value 细则
  let lengthCount = 0;
  let routeCount = 0;
  const routeNames = new Set<string>();
  const flagsBits = new Map<string, FlagBit[]>();
  for (const f of header) {
    const role = f?.role;
    if (role === 'length') lengthCount++;
    else if (role === 'route') {
      routeCount++;
      routeNames.add(f!.name ?? '');
    } else if (role === 'flags') {
      flagsBits.set(f!.name ?? '', Array.isArray(f!.bits) ? f!.bits : []);
      validateFlagBits(f!, ec);
    } else if (role === 'checksumOut') {
      validateChecksumOut(f!, ec);
    } else if (role === 'value') {
      validateValueSource(f!, ec);
    }
  }
  if (lengthCount === 0) {
    ec.add('header 缺少 role:"length" 字段（必须有且仅有 1 个）');
  } else if (lengthCount > 1) {
    ec.add(`header 有 ${lengthCount} 个 role:"length" 字段（必须有且仅有 1 个）`);
  }
  if (routeCount === 0) {
    ec.add('header 缺少 role:"route" 字段（至少 1 个）');
  }

  validatePipelineRefs(s, ec, routeNames, flagsBits);
}

interface FlagBit {
  name: string;
  bit: number;
}

function validateFlagBits(f: NonNullable<CodecSchema['header'][number]>, ec: ErrCollector): void {
  const bitWidth = (typeof f.size === 'number' ? f.size : 0) * 8;
  const seenBit = new Map<number, string>();
  const seenName = new Map<string, number>();
  const bits = Array.isArray(f.bits) ? f.bits : [];
  for (const b of bits) {
    const bit = typeof b?.bit === 'number' ? b.bit : 0;
    const bname = typeof b?.name === 'string' ? b.name : '';
    if (bit < 0 || bit >= bitWidth) {
      ec.add(`flags 字段 "${f.name}" 的 bit ${bit} 超出 [0,${bitWidth})（bit 位非法）`);
    }
    if (seenBit.has(bit)) {
      ec.add(`flags 字段 "${f.name}" 的 bit ${bit} 重复（与 "${seenBit.get(bit)}" 冲突，命名位不能重复）`);
    } else {
      seenBit.set(bit, bname);
    }
    if (!bname || bname.trim() === '') {
      ec.add(`flags 字段 "${f.name}" 的 bit ${bit} 名称为空`);
    } else if (seenName.has(bname)) {
      ec.add(`flags 字段 "${f.name}" 的命名位 "${bname}" 重复（与 bit ${seenName.get(bname)} 冲突）`);
    } else {
      seenName.set(bname, bit);
    }
  }
}

function validateChecksumOut(f: NonNullable<CodecSchema['header'][number]>, ec: ErrCollector): void {
  const from = typeof f.from === 'string' ? f.from : '';
  if (from === '') {
    ec.add(`checksumOut 字段 "${f.name}" 缺少 from（需 <step>.<output>）`);
    return;
  }
  if (!CHECKSUM_FROM_RE.test(from)) {
    ec.add(`checksumOut 字段 "${f.name}" 的 from "${from}" 不合法（需匹配 <step>.<output>）`);
  }
}

function validateValueSource(f: NonNullable<CodecSchema['header'][number]>, ec: ErrCollector): void {
  if (!f.source) {
    // v1 不强制：value 字段缺 source 仅提示性（与 Go schema.go 行为一致）
    return;
  }
  const kind = f.source.kind;
  if (!(kind in VALUE_SOURCE_KINDS_SUPPORTED)) {
    ec.add(`value 字段 "${f.name}" 的 source.kind "${kind}" 未知`);
    return;
  }
  if (!VALUE_SOURCE_KINDS_SUPPORTED[kind]) {
    ec.add(`value 字段 "${f.name}" 的 source.kind="${kind}" 不支持：v1 不支持的头字段取值源 kind="${kind}"，留待 v1.1`);
  }
}

// ---------- routeKeyTemplate ----------

function validateRouteKeyTemplate(s: CodecSchema, ec: ErrCollector): void {
  if (!s.routeKeyTemplate) return; // 已在 base 报
  const matches = s.routeKeyTemplate.matchAll(ROUTE_KEY_PLACEHOLDER_RE);
  for (const m of matches) {
    if (!isRouteField(s, m[1])) {
      ec.add(`routeKeyTemplate 占位 {${m[1]}} 必须指向某个 role:"route" 字段（未知占位）`);
    }
  }
}

function isRouteField(s: CodecSchema, name: string): boolean {
  const header = Array.isArray(s.header) ? s.header : [];
  for (const f of header) {
    if (f?.role === 'route' && f.name === name) return true;
  }
  return false;
}

// ---------- pipeline ----------

function validateHeartbeat(s: CodecSchema, ec: ErrCollector): void {
  const hb = s.heartbeat;
  if (hb === undefined) return;
  if (!hb || typeof hb !== 'object' || Array.isArray(hb)) {
    ec.add('heartbeat 必须是对象；没有 heartbeat 对象表示不启用心跳');
    return;
  }
  if (typeof hb.intervalMs !== 'number' || hb.intervalMs <= 0) {
    ec.add(`heartbeat.intervalMs 必须大于 0（当前 ${hb.intervalMs ?? ''}）`);
  }

  const placeholders = extractRoutePlaceholders(s.routeKeyTemplate ?? '');
  if (placeholders.length > 0) {
    if (!isPlainObject(hb.route)) {
      ec.add('heartbeat.route 必须按 routeKeyTemplate 填写对象字段');
    } else {
      for (const name of placeholders) {
        if (hb.route[name] === undefined || hb.route[name] === null) {
          ec.add(`heartbeat.route 缺少字段 "${name}"（来自 routeKeyTemplate 占位）`);
        }
      }
    }
  }

  const hasC2SProto = typeof hb.c2sProto === 'string' && hb.c2sProto.trim() !== '';
  const heartbeatFields = Array.isArray(hb.heartbeatFields) ? hb.heartbeatFields : [];
  if (hasC2SProto && heartbeatFields.length > 0) {
    ec.add('heartbeat 不能同时配置 c2sProto 与 heartbeatFields，须二选一');
  }
  if (!hasC2SProto && Array.isArray(hb.bindings) && hb.bindings.length > 0) {
    ec.add('heartbeat.bindings 只能在配置 c2sProto 时使用');
  }
  if (Array.isArray(hb.bindings)) {
    validateFieldBindings('heartbeat', hb.bindings, ec);
  }
  if (Array.isArray(hb.heartbeatFields)) {
    validateHeartbeatFields(hb.heartbeatFields, ec);
  }
}

function validateFieldBindings(prefix: string, bindings: FieldBind[], ec: ErrCollector, isMapEntryValue = false): void {
  const validTypes = new Set<string>(ALL_BINDING_TYPES);
  for (let i = 0; i < bindings.length; i++) {
    const b = bindings[i];
    const label = `${prefix}.bindings[${i}]`;
    const t = b?.type ?? '';
    if (!isMapEntryValue && !b.field && !b.storeAs) {
      ec.add(`${label} 缺少 field 和 storeAs`);
    }
    if (t && !validTypes.has(t)) {
      ec.add(`${label} 未知的 binding type "${t}"`);
      continue;
    }
    switch (t as BindingType | '') {
      case 'state':
      case 'stateFirst':
      case 'stateRandom':
      case 'stateMapKey':
      case 'stateMapValue':
      case 'listSize':
        if (!b.source) ec.add(`${label} type=${t} 缺少 source`);
        break;
      case 'stateRandomN':
        if (!b.source) ec.add(`${label} type=stateRandomN 缺少 source`);
        if (!b.count || b.count <= 0) ec.add(`${label} type=stateRandomN count 必须 > 0`);
        break;
      case 'randomPick':
      case 'randomPickN':
        if (!b.values || b.values.length === 0) ec.add(`${label} type=${t} 缺少 values`);
        if (t === 'randomPickN' && (!b.count || b.count <= 0)) ec.add(`${label} type=randomPickN count 必须 > 0`);
        break;
      case 'randomPickMap':
        if (!b.values || b.values.length === 0) ec.add(`${label} type=randomPickMap 缺少 values`);
        if (!b.keySource) ec.add(`${label} type=randomPickMap 缺少 keySource`);
        break;
      case 'randomExclude':
        if ((!b.values || b.values.length === 0) && !b.source) ec.add(`${label} type=randomExclude 缺少 values 和 source`);
        break;
      case 'randomInt':
      case 'randomFloat':
        if (b.min != null && b.max != null && b.min >= b.max) ec.add(`${label} type=${t} min 必须小于 max`);
        break;
      case 'randomString':
        if (!b.length || b.length <= 0) ec.add(`${label} type=randomString length 必须 > 0`);
        if (b.charset != null && b.charset.trim().length === 0) ec.add(`${label} type=randomString charset 不能为空`);
        break;
      case 'map':
        if (!b.entries || b.entries.length === 0) {
          ec.add(`${label} type=map 缺少 entries`);
        } else {
          for (let ei = 0; ei < b.entries.length; ei++) {
            const entry = b.entries[ei];
            const entryLabel = `${label}.entries[${ei}]`;
            if (entry.key === undefined || entry.key === null || entry.key === '') ec.add(`${entryLabel} 缺少 key`);
            if (!entry.value) {
              ec.add(`${entryLabel} 缺少 value`);
            } else if (entry.value.type === 'map') {
              ec.add(`${entryLabel} value 不允许嵌套 map 类型`);
            } else {
              validateFieldBindings(entryLabel, [entry.value], ec, true);
            }
          }
        }
        break;
    }
    if (b.filters) validateCodecFilters(label, b.filters, ec);
  }
}

function validateCodecFilters(prefix: string, filters: FilterDef[], ec: ErrCollector): void {
  const validOps = new Set(['', '==', '!=', '>', '>=', '<', '<=', 'eq', 'neq', 'gt', 'gte', 'lt', 'lte', 'contains', 'notContains', 'in', 'notIn', 'notNil', 'isNil']);
  const validModes = new Set(['any', 'all', 'none']);
  for (let i = 0; i < filters.length; i++) {
    const f = filters[i];
    if (f.op && !validOps.has(f.op)) ec.add(`${prefix}.filters[${i}] 未知的 op "${f.op}"`);
    if (f.mode && !validModes.has(f.mode)) ec.add(`${prefix}.filters[${i}] 未知的 mode "${f.mode}"`);
  }
}

function validateHeartbeatFields(fields: HeartbeatField[], ec: ErrCollector): void {
  const validTypes = new Set<string>(ALL_HEARTBEAT_FIELD_TYPES);
  const validSources = new Set<string>(ALL_HEARTBEAT_FIELD_SOURCES);
  for (let i = 0; i < fields.length; i++) {
    const f = fields[i];
    const label = `heartbeat.heartbeatFields[${i}]`;
    const t = f?.type ?? '';
    const source = f?.source ?? '';
    if (!validTypes.has(t)) ec.add(`${label} 未知 type "${t}"`);
    if (!validSources.has(source)) {
      ec.add(`${label} 未知 source "${source}"`);
      continue;
    }
    const isFloat = (t as HeartbeatFieldType) === 'f32' || (t as HeartbeatFieldType) === 'f64';
    if (isFloat && source !== 'fixed' && source !== 'state') ec.add(`${label} 浮点字段仅支持 fixed/state source`);
    switch (source as HeartbeatFieldSource) {
      case 'fixed':
        if (isFloat) {
          if (f.floatValue === undefined || f.floatValue === null) ec.add(`${label} source=fixed 缺少 floatValue`);
        } else if (f.value === undefined || f.value === null) {
          ec.add(`${label} source=fixed 缺少 value`);
        }
        break;
      case 'state':
      case 'stateCounter':
        if (!f.key) ec.add(`${label} source=${source} 缺少 key`);
        break;
      case 'randomInt':
        if (f.min === undefined || f.max === undefined) ec.add(`${label} source=randomInt 缺少 min/max`);
        break;
    }
  }
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function extractRoutePlaceholders(template: string): string[] {
  const re = /\{([A-Za-z_][A-Za-z0-9_]*)\}/g;
  const out: string[] = [];
  const seen = new Set<string>();
  let m: RegExpExecArray | null;
  while ((m = re.exec(template)) !== null) {
    if (seen.has(m[1])) continue;
    seen.add(m[1]);
    out.push(m[1]);
  }
  return out;
}

function validatePipeline(s: CodecSchema, ec: ErrCollector): void {
  const pipeline = Array.isArray(s.pipeline) ? s.pipeline : [];

  // step name 唯一
  const stepNames = new Map<string, number>();
  for (let i = 0; i < pipeline.length; i++) {
    const st = pipeline[i];
    const name = typeof st?.name === 'string' ? st.name.trim() : '';
    if (name === '') {
      ec.add(`pipeline 步骤 (index ${i}) 缺少 name`);
    } else if (stepNames.has(st!.name)) {
      ec.add(`pipeline 步骤 name "${st!.name}" 重复（与 index ${stepNames.get(st!.name)} 冲突，name 必须唯一）`);
    } else {
      stepNames.set(st!.name, i);
    }
  }

  for (const st of pipeline) {
    if (!st) continue;
    const prefix = `pipeline 步骤 "${st.name}"`;
    if (!(PIPELINE_OPS as readonly string[]).includes(st.op ?? '')) {
      ec.add(`${prefix}：未知 op "${st.op ?? ''}"（合法值：compress|encrypt|checksum|hash）`);
    }
    if (!st.algo || st.algo.trim() === '') {
      ec.add(`${prefix}：algo 不能为空`);
    }
    if (st.onError && st.onError !== '' && !(ON_ERROR as readonly string[]).includes(st.onError)) {
      ec.add(`${prefix}：onError "${st.onError}" 不合法（合法值：fail|keep，空视为 fail）`);
    }
    // produces：name 唯一 + region 合法
    const pn = new Set<string>();
    const produces = Array.isArray(st.produces) ? st.produces : [];
    for (const p of produces) {
      if (pn.has(p.name)) {
        ec.add(`${prefix}：produces 名称 "${p.name}" 在该步内重复（必须唯一）`);
      } else {
        pn.add(p.name);
      }
      if (!(PRODUCE_REGIONS as readonly string[]).includes(p.region ?? '')) {
        ec.add(`${prefix}：produces "${p.name}" 的 region "${p.region ?? ''}" 不合法（合法值：ciphered|bodyPlain|bodyFinal|header|frame）`);
      }
    }
    // offset（encrypt）
    if (st.op === 'encrypt' && st.offset) {
      if (st.offset.encode < 0) {
        ec.add(`${prefix}：encrypt offset.encode 不能为负（当前 ${st.offset.encode}）`);
      }
      if (st.offset.decode < 0) {
        ec.add(`${prefix}：encrypt offset.decode 不能为负（当前 ${st.offset.decode}）`);
      }
    }
    // over（独立 checksum/hash 步）
    if (st.over) {
      validateOver(ec, prefix, st.over);
    }
    // when
    if (st.when) {
      validateWhen(ec, prefix, st.when, stepNames);
    }
  }
}

function validateOver(ec: ErrCollector, prefix: string, o: OverSpecInput): void {
  if (!(OVER_KINDS as readonly string[]).includes(o.kind ?? '')) {
    ec.add(`${prefix}：over.kind "${o.kind ?? ''}" 不合法（合法值：bodyPlain|bodyFinal|header|frame|range）`);
    return;
  }
  if (o.kind === 'range') {
    const start = typeof o.rangeStart === 'number' ? o.rangeStart : 0;
    const end = typeof o.rangeEnd === 'number' ? o.rangeEnd : 0;
    if (start < 0 || end < 0 || end < start) {
      ec.add(`${prefix}：over range 区间非法 [rangeStart=${start}, rangeEnd=${end}]（需 >=0 且 rangeEnd>=rangeStart）`);
    }
  }
}

interface OverSpecInput {
  kind?: string;
  rangeStart?: number;
  rangeEnd?: number;
}

function validateWhen(
  ec: ErrCollector,
  prefix: string,
  w: NonNullable<NonNullable<CodecSchema['pipeline'][number]>['when']>,
  stepNames: Map<string, number>,
): void {
  if (w.appliesWith && w.appliesWith !== '') {
    if (!stepNames.has(w.appliesWith)) {
      ec.add(`${prefix}：when.appliesWith "${w.appliesWith}" 指向不存在的 step`);
    }
  }
  const guards = Array.isArray(w.guards) ? w.guards : [];
  for (const g of guards) {
    if (!(GUARD_OPS as readonly string[]).includes(g?.op ?? '')) {
      ec.add(`${prefix}：guard (field="${g?.field ?? ''}") 的 op "${g?.op ?? ''}" 不合法（合法值：eq|neq|gt|gte|lt|lte）`);
    }
  }
}

/** 校验跨 header↔pipeline 的引用：flag、checksumOut.from、when.appliesWith(已在 when 内)。 */
function validatePipelineRefs(
  s: CodecSchema,
  ec: ErrCollector,
  _routeNames: Set<string>,
  flagsBits: Map<string, FlagBit[]>,
): void {
  // flagName → flagField 反查（全局 flag 名空间）
  const flagNameToField = new Map<string, string>();
  for (const [fName, bits] of flagsBits) {
    for (const b of bits) {
      flagNameToField.set(b.name, fName);
    }
  }

  // 每个 flag 命名位至多被一个 step 绑定；flag 引用必须存在
  const boundFlag = new Map<string, string>();
  const pipeline = Array.isArray(s.pipeline) ? s.pipeline : [];
  for (const st of pipeline) {
    if (!st?.flag || st.flag === '') continue;
    if (!flagNameToField.has(st.flag)) {
      ec.add(`pipeline 步骤 "${st.name}" 的 flag "${st.flag}" 未在任何 role:"flags" 字段的命名位中声明`);
      continue;
    }
    if (boundFlag.has(st.flag)) {
      ec.add(`pipeline 步骤 "${st.name}" 的 flag "${st.flag}" 已被步骤 "${boundFlag.get(st.flag)}" 绑定（同一命名 flag 位至多被一个 step 绑定）`);
    } else {
      boundFlag.set(st.flag, st.name);
    }
  }

  // 凡带 when 的 step 必须绑定 flag
  for (const st of pipeline) {
    if (st?.when && (!st.flag || st.flag === '')) {
      ec.add(`pipeline 步骤 "${st.name}" 带有 when 但未绑定 flag（带 when 的步骤必须绑定 flag，否则 decode 无法复现 encode 决策）`);
    }
  }

  // checksumOut.from 指向的 <step>.<output>：step 必须存在、produce 必须存在
  const stepProduces = new Map<string, Set<string>>();
  for (const st of pipeline) {
    const set = new Set<string>();
    const produces = Array.isArray(st?.produces) ? st!.produces : [];
    for (const p of produces) set.add(p.name);
    stepProduces.set(st!.name, set);
  }
  const header = Array.isArray(s.header) ? s.header : [];
  for (const f of header) {
    if (f?.role !== 'checksumOut' || !f.from) continue;
    const m = f.from.match(CHECKSUM_FROM_RE);
    if (!m) continue; // 已在 validateChecksumOut 报错
    const stepName = m[1];
    const produceName = m[2];
    const produces = stepProduces.get(stepName);
    if (!produces) {
      ec.add(`checksumOut 字段 "${f.name}" 的 from "${f.from}" 指向不存在的 step "${stepName}"`);
      continue;
    }
    if (!produces.has(produceName)) {
      ec.add(`checksumOut 字段 "${f.name}" 的 from "${f.from}" 指向 step "${stepName}" 中不存在的 produce "${produceName}"`);
    }
  }
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

export type ResourceType = 'proto' | 'script' | 'adapter';

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
  return name === ERRORS_JSON_KEY ? getErrorMap() : getCodecSchema(name);
}

async function writeBaselineResource(type: ResourceType, name: string, content: string): Promise<void> {
  if (type === 'proto') {
    await addProtoFromBaseline(name, content);
  } else if (type === 'script') {
    await addScriptFromBaseline(name, content);
  } else if (name === ERRORS_JSON_KEY) {
    await setErrorMapFromBaseline(content);
  } else {
    await setCodecSchemaFromBaseline(name, content);
  }
}

async function deleteResource(type: ResourceType, name: string): Promise<void> {
  if (type === 'proto') await del(name, protoStore);
  else if (type === 'script') await del(name, scriptStore);
  else await del(name, adapterStore);
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
  codecs?: ResourceFile[];
  errorMap?: ResourceFile | null;
}): Promise<void> {
  const writes: Array<Promise<void>> = [];
  for (const f of input.protos ?? []) writes.push(setResourceBaseHash('proto', f.name, await hashResourceContent(f.content)));
  for (const f of input.scripts ?? []) writes.push(setResourceBaseHash('script', f.name, await hashResourceContent(f.content)));
  for (const f of input.codecs ?? []) writes.push(setResourceBaseHash('adapter', f.name, await hashResourceContent(f.content)));
  if (input.errorMap) writes.push(setResourceBaseHash('adapter', ERRORS_JSON_KEY, await hashResourceContent(input.errorMap.content)));
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

  // 并行拉取各资源组基线索引
  const [protoIndex, scriptIndex, codecIndex] = await Promise.all([
    fetchIndex(`${BASELINE_PREFIX}/proto/index.json`),
    fetchIndex(`${BASELINE_PREFIX}/scripts/index.json`),
    fetchBaselineCodecIndex(),
  ]);

  // --- Proto ---
  await syncFileGroup(protoIndex, 'proto', protoStore, `${BASELINE_PREFIX}/proto/`, result);

  // --- Scripts ---
  await syncFileGroup(scriptIndex, 'script', scriptStore, `${BASELINE_PREFIX}/scripts/`, result);

  // --- Adapter（多 codec + errors.json，与 proto/scripts 同款走 syncFileGroup）---
  await syncFileGroup(codecIndex, 'adapter', adapterStore, `${BASELINE_PREFIX}/adapter/`, result);

  // 同步完成后保存当前基线快照
  saveLastBaseline({
    proto: protoIndex,
    script: scriptIndex,
    adapter: codecIndex,
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
  return fetchBaselineCodec(name);
}

async function syncFileGroup(
  baselineNames: string[],
  type: ResourceType,
  store: ReturnType<typeof createStore>,
  urlPrefix: string,
  result: BaselineSyncResult,
): Promise<void> {
  // 收集本地存储中已有的所有 key
  const idbKeys = new Set(
    ((await keys(store)) as IDBValidKey[]).map(String),
  );
  const baselineSet = new Set(baselineNames);

  // 基线有 → 对比
  const toFetch: string[] = [];
  for (const name of baselineNames) {
    const existing = await get<ResourceFile>(name, store);
    if (!existing) {
      toFetch.push(name); // 本地存储没有，后面批量 fetch 再写入
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
