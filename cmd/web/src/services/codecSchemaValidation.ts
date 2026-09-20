/**
 * codec 配置结构校验（镜像后端 codec/schema.go 的 Validate）。
 *
 * 从 resourcesStore 拆出来的原因：这里全是不碰本地存储的纯函数，
 * 与资源文件的读写、基线同步是两件事，放一起只是让那个文件长到难以通读。
 * 读取全部 *_codec.json 再逐份校验的 collectCodecSchemaErrors 仍留在 resourcesStore，
 * 因为它要走存储。
 */

import { validateCodecStructure } from './schemaValidator';
import type { CodecSchema } from '@/types/codec';
import type {
  BindingType,
  FieldBind,
  FilterDef,
  HeartbeatField,
  HeartbeatFieldSource,
  HeartbeatFieldType,
} from '@/types/action';
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
  for (const issue of validateCodecStructure(parsed)) {
    ec.add(`codec ${issue.path}：${issue.message}`);
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
      ec.add(
        `${prefix}：物理区间 [offset=${offset}, offset+size=${offset + size}) 越界（headerSize=${headerSize}）`,
      );
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
      ec.add(
        `flags 字段 "${f.name}" 的 bit ${bit} 重复（与 "${seenBit.get(bit)}" 冲突，命名位不能重复）`,
      );
    } else {
      seenBit.set(bit, bname);
    }
    if (!bname || bname.trim() === '') {
      ec.add(`flags 字段 "${f.name}" 的 bit ${bit} 名称为空`);
    } else if (seenName.has(bname)) {
      ec.add(
        `flags 字段 "${f.name}" 的命名位 "${bname}" 重复（与 bit ${seenName.get(bname)} 冲突）`,
      );
    } else {
      seenName.set(bname, bit);
    }
  }
}

function validateChecksumOut(
  f: NonNullable<CodecSchema['header'][number]>,
  ec: ErrCollector,
): void {
  const from = typeof f.from === 'string' ? f.from : '';
  if (from === '') {
    ec.add(`checksumOut 字段 "${f.name}" 缺少 from（需 <step>.<output>）`);
    return;
  }
  if (!CHECKSUM_FROM_RE.test(from)) {
    ec.add(`checksumOut 字段 "${f.name}" 的 from "${from}" 不合法（需匹配 <step>.<output>）`);
  }
}

function validateValueSource(
  f: NonNullable<CodecSchema['header'][number]>,
  ec: ErrCollector,
): void {
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
    ec.add(
      `value 字段 "${f.name}" 的 source.kind="${kind}" 不支持：v1 不支持的头字段取值源 kind="${kind}"，留待 v1.1`,
    );
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
  if (hb.requireSecretKey !== undefined && typeof hb.requireSecretKey !== 'boolean') {
    ec.add('heartbeat.requireSecretKey 必须是布尔值');
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

function validateFieldBindings(
  prefix: string,
  bindings: FieldBind[],
  ec: ErrCollector,
  isMapEntryValue = false,
): void {
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
        if (t === 'randomPickN' && (!b.count || b.count <= 0))
          ec.add(`${label} type=randomPickN count 必须 > 0`);
        break;
      case 'randomPickMap':
        if (!b.values || b.values.length === 0) ec.add(`${label} type=randomPickMap 缺少 values`);
        if (!b.keySource) ec.add(`${label} type=randomPickMap 缺少 keySource`);
        break;
      case 'randomExclude':
        if ((!b.values || b.values.length === 0) && !b.source)
          ec.add(`${label} type=randomExclude 缺少 values 和 source`);
        break;
      case 'randomInt':
      case 'randomFloat':
        if (b.min != null && b.max != null && b.min >= b.max)
          ec.add(`${label} type=${t} min 必须小于 max`);
        break;
      case 'randomString':
        if (!b.length || b.length <= 0) ec.add(`${label} type=randomString length 必须 > 0`);
        if (b.charset != null && b.charset.trim().length === 0)
          ec.add(`${label} type=randomString charset 不能为空`);
        break;
      case 'map':
        if (!b.entries || b.entries.length === 0) {
          ec.add(`${label} type=map 缺少 entries`);
        } else {
          for (let ei = 0; ei < b.entries.length; ei++) {
            const entry = b.entries[ei];
            const entryLabel = `${label}.entries[${ei}]`;
            if (entry.key === undefined || entry.key === null || entry.key === '')
              ec.add(`${entryLabel} 缺少 key`);
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
  const validOps = new Set([
    '',
    '==',
    '!=',
    '>',
    '>=',
    '<',
    '<=',
    'eq',
    'neq',
    'gt',
    'gte',
    'lt',
    'lte',
    'contains',
    'notContains',
    'in',
    'notIn',
    'notNil',
    'isNil',
  ]);
  const validModes = new Set(['any', 'all', 'none']);
  for (let i = 0; i < filters.length; i++) {
    const f = filters[i];
    if (f.op && !validOps.has(f.op)) ec.add(`${prefix}.filters[${i}] 未知的 op "${f.op}"`);
    if (f.mode && !validModes.has(f.mode))
      ec.add(`${prefix}.filters[${i}] 未知的 mode "${f.mode}"`);
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
    if (isFloat && source !== 'fixed' && source !== 'state')
      ec.add(`${label} 浮点字段仅支持 fixed/state source`);
    switch (source as HeartbeatFieldSource) {
      case 'fixed':
        if (isFloat) {
          if (f.floatValue === undefined || f.floatValue === null)
            ec.add(`${label} source=fixed 缺少 floatValue`);
        } else if (f.value === undefined || f.value === null) {
          ec.add(`${label} source=fixed 缺少 value`);
        }
        break;
      case 'state':
      case 'stateCounter':
        if (!f.key) ec.add(`${label} source=${source} 缺少 key`);
        break;
      case 'randomInt':
        if (f.min === undefined || f.max === undefined)
          ec.add(`${label} source=randomInt 缺少 min/max`);
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
      ec.add(
        `pipeline 步骤 name "${st!.name}" 重复（与 index ${stepNames.get(st!.name)} 冲突，name 必须唯一）`,
      );
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
        ec.add(
          `${prefix}：produces "${p.name}" 的 region "${p.region ?? ''}" 不合法（合法值：ciphered|bodyPlain|bodyFinal|header|frame）`,
        );
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
    ec.add(
      `${prefix}：over.kind "${o.kind ?? ''}" 不合法（合法值：bodyPlain|bodyFinal|header|frame|range）`,
    );
    return;
  }
  if (o.kind === 'range') {
    const start = typeof o.rangeStart === 'number' ? o.rangeStart : 0;
    const end = typeof o.rangeEnd === 'number' ? o.rangeEnd : 0;
    if (start < 0 || end < 0 || end < start) {
      ec.add(
        `${prefix}：over range 区间非法 [rangeStart=${start}, rangeEnd=${end}]（需 >=0 且 rangeEnd>=rangeStart）`,
      );
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
      ec.add(
        `${prefix}：guard (field="${g?.field ?? ''}") 的 op "${g?.op ?? ''}" 不合法（合法值：eq|neq|gt|gte|lt|lte）`,
      );
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
      ec.add(
        `pipeline 步骤 "${st.name}" 的 flag "${st.flag}" 未在任何 role:"flags" 字段的命名位中声明`,
      );
      continue;
    }
    if (boundFlag.has(st.flag)) {
      ec.add(
        `pipeline 步骤 "${st.name}" 的 flag "${st.flag}" 已被步骤 "${boundFlag.get(st.flag)}" 绑定（同一命名 flag 位至多被一个 step 绑定）`,
      );
    } else {
      boundFlag.set(st.flag, st.name);
    }
  }

  // 凡带 when 的 step 必须绑定 flag
  for (const st of pipeline) {
    if (st?.when && (!st.flag || st.flag === '')) {
      ec.add(
        `pipeline 步骤 "${st.name}" 带有 when 但未绑定 flag（带 when 的步骤必须绑定 flag，否则 decode 无法复现 encode 决策）`,
      );
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
      ec.add(
        `checksumOut 字段 "${f.name}" 的 from "${f.from}" 指向 step "${stepName}" 中不存在的 produce "${produceName}"`,
      );
    }
  }
}
