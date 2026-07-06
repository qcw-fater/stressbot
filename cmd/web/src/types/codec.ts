/**
 * codec.json 的 TypeScript 类型定义，镜像后端 `codec/schema.go`。
 *
 * 设计要点：
 * - JSON 键名用 camelCase（与 Go json tag 一致）；
 * - 字段与 Go 结构体一一对应；Go `omitempty` 字段在 TS 用 `?` 可选；
 * - 合法值集合从 schema.go 126-162 照搬为 `const` 数组（供校验与后续 UI 下拉复用）；
 * - 仅类型与常量，**无运行时副作用**（纯声明模块）。
 *
 * 与后端的语义约定（schema.go 注释）：
 *   - endianness 缺省回退 endianDefault，由编译层处理（TS 校验层不强求每个字段都给 endian）；
 *   - role:"value" 的 source.kind 在 v1 仅支持 const/route；state/counter/timestamp 留 v1.1。
 */

/** CodecSchema 是 codec.json 的根类型。 */
export interface CodecSchema {
  version: number;
  /** "le" | "be" */
  endianDefault: string;
  frame: FrameSpec;
  header: Field[];
  /** routeKeyTemplate，用 header 中 role=route 的字段名作为占位 */
  routeKeyTemplate: string;
  pipeline: PipelineStep[];
  /** 连接级可选心跳；没有该对象表示不启用心跳 */
  heartbeat?: CodecHeartbeat;
}

/** 帧物理布局（不含字段类型——类型由 Header 中唯一的 role:"length" 字段携带）。 */
export interface FrameSpec {
  headerSize: number;
  /** 默认 0 */
  trailerSize: number;
  /** length 字段是否含 header */
  lengthIncludesHeader: boolean;
  /** length 字段是否含 trailer */
  lengthIncludesTrailer: boolean;
}

/** Header 中的一个字段。 */
export interface Field {
  name: string;
  type: string;
  /** le|be；缺省回退 endianDefault */
  endian?: string;
  offset: number;
  size: number;
  /** length|route|errorCode|flags|checksumOut|value|reserved */
  role: string;
  bits?: FlagBit[];
  /** role=checksumOut: "<step>.<output>" */
  from?: string;
  /** role=value */
  source?: ValueSource;
  /** type=bytes: hex|base64|ascii */
  repr?: string;
}

/** role:"flags" 字段的一个命名位。 */
export interface FlagBit {
  name: string;
  bit: number;
}

/**
 * ValueSource 决定 role:"value" 字段的 encode 取值。
 * v1 仅 const、route 支持；state/counter/timestamp 留 v1.1（校验阶段直接报「v1 不支持」）。
 */
export interface ValueSource {
  kind: string;
  /** const */
  value?: number;
  /** state / route */
  key?: string;
  /** counter (v1.1) */
  start?: number;
  /** counter (v1.1) */
  step?: number;
  /** counter 回绕 (v1.1) */
  wrap?: number;
  /** timestamp: s|ms (v1.1) */
  unit?: string;
}

/** encode/decode 管线的一步。 */
export interface PipelineStep {
  /** compress|encrypt|checksum|hash */
  op: string;
  /** 供 flag/from/appliesWith 引用 */
  name: string;
  /** 注册表键（存在性由后端 §3.4 算法清单端点校验，前端只校验非空） */
  algo: string;
  flag?: string;
  params?: Record<string, unknown>;
  /** encrypt */
  keyLen?: number;
  /** encrypt */
  offset?: StepOffset;
  produces?: StepProduce[];
  /** 独立 checksum/hash 步 */
  over?: OverSpec;
  /** fail(默认)|keep */
  onError?: string;
  when?: StepCond;
}

/** encrypt 步的单向偏移（每份 codec 单 transport）。 */
export interface StepOffset {
  /** 缺省 0；发送方向加密偏移 */
  encode: number;
  /** 缺省 0 */
  decode: number;
}

/** 某步声明的派生产物（如 bcc）。 */
export interface StepProduce {
  /** 产物名 */
  name: string;
  /** 计算算法（如 xor8） */
  algo: string;
  /** ciphered|bodyPlain|bodyFinal|header|frame */
  region: string;
}

/** 独立 checksum/hash 步的作用域。 */
export interface OverSpec {
  /** bodyPlain|bodyFinal|header|frame|range */
  kind: string;
  rangeStart?: number;
  rangeEnd?: number;
}

/** pipeline 步的结构化条件（encode 决策；decode 不重算）。 */
export interface StepCond {
  minBodyLen?: number;
  onlySmaller?: boolean;
  requireKey?: boolean;
  appliesWith?: string;
  guards?: Guard[];
}

export interface CodecHeartbeat {
  intervalMs: number;
  route?: unknown;
  c2sProto?: string;
  bindings?: import('@/types/action').FieldBind[];
  heartbeatFields?: import('@/types/action').HeartbeatField[];
  skipWhenMissing?: boolean;
  requireSecretKey?: boolean;
}

/** when.guards 的一个条件项。 */
export interface Guard {
  field: string;
  /** eq|neq|gt|gte|lt|lte */
  op: string;
  value: number;
}

/** errors.json：code → 中文文案。 */
export type ErrorMap = Record<string, string>;

// ---------- 算法元数据（镜像 codec/registry.go 172-186） ----------

/**
 * AlgoParam 描述算法的一个可配参数（供编辑器动态表单）。
 * 镜像后端 `codec.AlgoParam`（registry.go:172）。
 */
export interface AlgoParam {
  name: string;
  /** int|string|bool|bytes */
  type: 'int' | 'string' | 'bool' | 'bytes';
  /** 缺省值（惰性 placeholder，不强制写入 step.params） */
  default?: unknown;
  description?: string;
}

/**
 * AlgoMeta 描述一个已注册算法的元数据。
 * 镜像后端 `codec.AlgoMeta`（registry.go:180）。
 *
 * **op 映射 gotcha**：`AlgoMeta.op` 用 `cipher`，而 `PipelineStep.op` 用 `encrypt`——
 * 二者指同一类算法，前端下拉过滤时需做 encrypt↔cipher 映射（见 `algosForStepOp`）。
 */
export interface AlgoMeta {
  name: string;
  /** cipher|compress|checksum|hash */
  op: 'cipher' | 'compress' | 'checksum' | 'hash';
  description?: string;
  params?: AlgoParam[];
}

// ---------- 预览（镜像 codec/preview.go 24-41 + admin/codec_handlers.go 29-37） ----------

/** header 中一个字段的解释结果。镜像 `codec.PreviewField`（preview.go:24）。 */
export interface PreviewField {
  name: string;
  /** 数值化后的字段值 */
  value: number;
  /** 在 header 中的字节偏移 */
  offset: number;
  /** 字段字节数 */
  size: number;
}

/**
 * Preview 结果。镜像 `codec.PreviewResult`（preview.go:32）。
 * **Error 非空时其它字段为零值**——编辑器语义：HTTP 200 仍照常返回（由前端据 Error 提示）。
 */
export interface PreviewResult {
  mode: 'encode' | 'decode';
  /** encode 出参：完整帧 hex */
  frameHex?: string;
  /** decode 出参：解出 body hex */
  bodyHex?: string;
  /** decode 出参：routeKey */
  routeKey?: string;
  /** decode 出参：头 errorCode */
  headerErr?: number;
  /** header 字段逐项解释 */
  fields?: PreviewField[];
  /** schema 编译/运行错误（中文） */
  error?: string;
}

/**
 * POST /sbot/codec/preview 的请求体。镜像 `admin.codecPreviewRequest`。
 * schema 用 unknown 承载（编辑器当前 content 的 JSON.parse 结果），由后端二次解析。
 */
export interface PreviewRequest {
  /** 完整 codec.json 内容（对象形式） */
  schema: unknown;
  mode: 'encode' | 'decode';
  transport?: 'tcp' | 'udp';
  /** encode 入参：route 字段 map */
  route?: Record<string, unknown>;
  /** encode 入参：body hex */
  bodyHex?: string;
  /** encode/decode 入参：secretKey hex */
  keyHex?: string;
  /** decode 入参：完整帧 hex */
  frameHex?: string;
}

// ---------- v1 冻结合法值集合（照搬 schema.go 126-162） ----------

/** type → 固定宽度字节数；-1 表示需显式 size（bytes）。 */
export const FIELD_TYPE_WIDTH: Record<string, number> = {
  u8: 1,
  u16: 2,
  u24: 3,
  u32: 4,
  u64: 8,
  i8: 1,
  i16: 2,
  i24: 3,
  i32: 4,
  i64: 8,
  f32: 4,
  f64: 8,
  bytes: -1,
};

/** 所有合法 field type 名（= FIELD_TYPE_WIDTH 的 keys）。 */
export const FIELD_TYPES = Object.keys(FIELD_TYPE_WIDTH);

export const FIELD_ROLES = [
  'length',
  'route',
  'errorCode',
  'flags',
  'checksumOut',
  'value',
  'reserved',
] as const;

export const PIPELINE_OPS = ['compress', 'encrypt', 'checksum', 'hash'] as const;

export const PRODUCE_REGIONS = [
  'ciphered',
  'bodyPlain',
  'bodyFinal',
  'header',
  'frame',
] as const;

export const OVER_KINDS = [
  'bodyPlain',
  'bodyFinal',
  'header',
  'frame',
  'range',
] as const;

export const GUARD_OPS = ['eq', 'neq', 'gt', 'gte', 'lt', 'lte'] as const;

export const ON_ERROR = ['fail', 'keep'] as const;

/**
 * value source.kind 合法值；value=false 表示 v1 不支持（留 v1.1）。
 * 与 schema.go validValueSourceKinds 对齐。
 */
export const VALUE_SOURCE_KINDS_SUPPORTED: Record<string, boolean> = {
  const: true,
  route: true,
  state: false,
  counter: false,
  timestamp: false,
};
