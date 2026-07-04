/**
 * codecEdit — 帧布局编辑器的同步 helper（纯函数，无 React 依赖）。
 *
 * 核心设计：**raw-object 无损 round-trip**。
 *   - `parseCodecForEdit(content)` 用 `JSON.parse` 保留全部键（含未知键）与原始键序，
 *     返回 `{raw, schema, error}`；raw = lossless 原对象，schema = 同一对象的 typed 宽松视图。
 *   - 结构化编辑（header 增删改/移动 + frame 标量）全部走「深拷贝 → 改对应位置 → serializeCodec」，
 *     **不 mutate 入参**、不重排整个文档，只动预期部分。
 *   - `serializeCodec(raw)` = `JSON.stringify(raw, null, 2)` —— 确定性输出。
 *
 * 不做兼容兜底：非法 JSON 直接提示切源码，不静默。
 * 不做结构校验：字段是否合法交给 `validateCodecSchema`，这里只管「能否解析成对象」+ 字段定位。
 */

import type { CodecHeartbeat, CodecSchema, Field, PipelineStep } from '@/types/codec';

/** parseCodecForEdit 的返回。 */
export interface ParsedCodec {
  /** JSON.parse 结果（lossless，保留全部键与序）；非法 JSON 时为 null。 */
  raw: Record<string, unknown> | null;
  /** 同一对象的 typed 宽松视图；非对象或非法 JSON 时为 null。 */
  schema: CodecSchema | null;
  /** JSON 解析错误信息（中文）；解析成功时为 null。 */
  error: string | null;
}

/** 把 content 解析为 raw 对象 + typed 视图；非法 JSON → raw/schema=null + error。 */
export function parseCodecForEdit(content: string): ParsedCodec {
  let parsed: unknown;
  try {
    parsed = JSON.parse(content);
  } catch (e) {
    return { raw: null, schema: null, error: `协议配置不是合法 JSON：${(e as Error).message}` };
  }
  // raw 永远是 JSON.parse 的原结果（保留 lossless 特性）；但 schema 视图只对「对象」有意义。
  const isObj = parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed);
  return {
    raw: parsed as Record<string, unknown> | null,
    schema: isObj ? (parsed as CodecSchema) : null,
    error: null,
  };
}

/** 把 raw 对象序列化回 content（2 空格缩进，保留键序）—— 确定性输出。 */
export function serializeCodec(raw: Record<string, unknown>): string {
  return JSON.stringify(raw, null, 2);
}

// ─── 内部：深拷贝 + 读改 raw.header ───────────────────────────────────

/** 深拷贝 raw 并返回其 header 数组（若 raw.header 不是数组则返回 null）。 */
function cloneWithHeader(raw: Record<string, unknown>): {
  next: Record<string, unknown>;
  header: unknown[] | null;
} {
  const next: Record<string, unknown> = JSON.parse(JSON.stringify(raw));
  const h = next['header'];
  return { next, header: Array.isArray(h) ? (h as unknown[]) : null };
}

/** 把 raw.header 写回 next.header 后序列化。header 为 null（非数组）时返回原 content（安全降级）。 */
function commitHeader(
  base: { next: Record<string, unknown>; header: unknown[] | null },
  originalRaw: Record<string, unknown>,
): string {
  if (base.header === null) {
    // raw.header 不是数组 —— 结构化编辑无法定位，返回原 content（结构合法性交给 validateCodecSchema）。
    return serializeCodec(originalRaw);
  }
  base.next['header'] = base.header;
  return serializeCodec(base.next);
}

// ─── header 字段增删改 / 移动（纯函数，不 mutate 入参）───────────────

/** 追加一个 header 字段到末尾。 */
export function addHeaderField(raw: Record<string, unknown>, field: Field): string {
  const base = cloneWithHeader(raw);
  if (base.header === null) return commitHeader(base, raw);
  base.header.push(field);
  return commitHeader(base, raw);
}

/** 局部更新某 header 字段（patch 合并，保留该字段其他键）。 */
export function updateHeaderField(raw: Record<string, unknown>, index: number, patch: Partial<Field>): string {
  const base = cloneWithHeader(raw);
  if (base.header === null) return commitHeader(base, raw);
  if (index < 0 || index >= base.header.length) return commitHeader(base, raw);
  const cur = base.header[index];
  base.header[index] = typeof cur === 'object' && cur !== null ? { ...cur, ...patch } : { ...patch };
  return commitHeader(base, raw);
}

/** 删除某 header 字段。 */
export function removeHeaderField(raw: Record<string, unknown>, index: number): string {
  const base = cloneWithHeader(raw);
  if (base.header === null) return commitHeader(base, raw);
  if (index < 0 || index >= base.header.length) return commitHeader(base, raw);
  base.header.splice(index, 1);
  return commitHeader(base, raw);
}

/** 移动某 header 字段的顺序（dir=-1 上移 / +1 下移）；越界保持原序。 */
export function moveHeaderField(raw: Record<string, unknown>, index: number, dir: -1 | 1): string {
  const base = cloneWithHeader(raw);
  if (base.header === null) return commitHeader(base, raw);
  const target = index + dir;
  if (index < 0 || index >= base.header.length) return commitHeader(base, raw);
  if (target < 0 || target >= base.header.length) return commitHeader(base, raw);
  const arr = base.header;
  [arr[index], arr[target]] = [arr[target], arr[index]];
  return commitHeader(base, raw);
}

// ─── frame / endianDefault / version 等标量编辑 ──────────────────────

/** setCodecScalar 支持的路径（顶层 + frame 嵌套）。 */
export type CodecScalarPath =
  | 'version'
  | 'endianDefault'
  | 'frame.headerSize'
  | 'frame.trailerSize'
  | 'frame.lengthIncludesHeader'
  | 'frame.lengthIncludesTrailer';

/** 编辑一个标量（顶层或 frame.*）。不 mutate 入参。 */
export function setCodecScalar(
  raw: Record<string, unknown>,
  path: CodecScalarPath,
  value: number | string | boolean,
): string {
  const next: Record<string, unknown> = JSON.parse(JSON.stringify(raw));
  if (path === 'version' || path === 'endianDefault') {
    next[path] = value;
  } else {
    // frame.* 嵌套：frame 可能不存在或不是对象，确保它是对象后再写。
    const frameVal = next['frame'];
    const frame: Record<string, unknown> =
      frameVal !== null && typeof frameVal === 'object' && !Array.isArray(frameVal)
        ? (frameVal as Record<string, unknown>)
        : {};
    const key = path.slice('frame.'.length);
    frame[key] = value;
    next['frame'] = frame;
  }
  return serializeCodec(next);
}

// ─── pipeline 步骤增删改 / 移动 + routeKeyTemplate（不 mutate 入参）────

/** 深拷贝 raw 并返回其 pipeline 数组；若不是数组，cloneWithPipeline 会新建空数组。 */
function cloneWithPipeline(raw: Record<string, unknown>): {
  next: Record<string, unknown>;
  pipeline: unknown[];
} {
  const next: Record<string, unknown> = JSON.parse(JSON.stringify(raw));
  const p = next['pipeline'];
  // raw.pipeline 非数组时按空数组处理（add 创建数组；最终结构合法性交给 validateCodecSchema）。
  const pipeline: unknown[] = Array.isArray(p) ? (p as unknown[]) : [];
  return { next, pipeline };
}

/** 新增 pipeline 步骤的默认值：op=compress（合法集合内最小语义）、name/algo 空（校验交给 validateCodecSchema）。 */
const DEFAULT_NEW_STEP: PipelineStep = { op: 'compress', name: '', algo: '' };

/** 追加一个 pipeline 步骤到末尾；step 缺省时用 DEFAULT_NEW_STEP。返回新 content。 */
export function addPipelineStep(
  raw: Record<string, unknown>,
  step?: Partial<PipelineStep>,
): string {
  const { next, pipeline } = cloneWithPipeline(raw);
  pipeline.push({ ...DEFAULT_NEW_STEP, ...step });
  next['pipeline'] = pipeline;
  return serializeCodec(next);
}

/** 局部更新某 pipeline 步骤（patch 合并，保留该步其他键）。越界安全降级（返回原 content 序列化）。 */
export function updatePipelineStep(
  raw: Record<string, unknown>,
  index: number,
  patch: Partial<PipelineStep>,
): string {
  const { next, pipeline } = cloneWithPipeline(raw);
  if (index < 0 || index >= pipeline.length) {
    return serializeCodec(next);
  }
  const cur = pipeline[index];
  pipeline[index] =
    typeof cur === 'object' && cur !== null ? { ...cur, ...patch } : { ...patch };
  next['pipeline'] = pipeline;
  return serializeCodec(next);
}

/** 删除某 pipeline 步骤。越界安全降级。 */
export function removePipelineStep(raw: Record<string, unknown>, index: number): string {
  const { next, pipeline } = cloneWithPipeline(raw);
  if (index < 0 || index >= pipeline.length) {
    return serializeCodec(next);
  }
  pipeline.splice(index, 1);
  next['pipeline'] = pipeline;
  return serializeCodec(next);
}

/** 移动某 pipeline 步骤的顺序（dir=-1 上移 / +1 下移）；越界保持原序。 */
export function movePipelineStep(raw: Record<string, unknown>, index: number, dir: -1 | 1): string {
  const { next, pipeline } = cloneWithPipeline(raw);
  const target = index + dir;
  if (index < 0 || index >= pipeline.length) return serializeCodec(next);
  if (target < 0 || target >= pipeline.length) return serializeCodec(next);
  [pipeline[index], pipeline[target]] = [pipeline[target], pipeline[index]];
  next['pipeline'] = pipeline;
  return serializeCodec(next);
}

/** 设置 routeKeyTemplate。不 mutate 入参，保留未知键。 */
export function setRouteKeyTemplate(raw: Record<string, unknown>, template: string): string {
  const next: Record<string, unknown> = JSON.parse(JSON.stringify(raw));
  next['routeKeyTemplate'] = template;
  return serializeCodec(next);
}

/** 设置或删除连接级 heartbeat。heartbeat=null 表示删除配置（不启用心跳）。 */
export function setHeartbeat(raw: Record<string, unknown>, heartbeat: CodecHeartbeat | null): string {
  const next: Record<string, unknown> = JSON.parse(JSON.stringify(raw));
  if (heartbeat === null) {
    delete next['heartbeat'];
  } else {
    next['heartbeat'] = heartbeat;
  }
  return serializeCodec(next);
}

/** 局部更新连接级 heartbeat；若原来没有 heartbeat，则从 patch 创建。 */
export function updateHeartbeat(raw: Record<string, unknown>, patch: Partial<CodecHeartbeat>): string {
  const next: Record<string, unknown> = JSON.parse(JSON.stringify(raw));
  const cur = next['heartbeat'];
  const base = cur !== null && typeof cur === 'object' && !Array.isArray(cur) ? cur as Record<string, unknown> : {};
  next['heartbeat'] = { ...base, ...patch };
  return serializeCodec(next);
}
