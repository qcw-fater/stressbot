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
  /** 如 "{cmd}:{act}" */
  routeKeyTemplate: string;
  pipeline: PipelineStep[];
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
  /** 缺省 0；如 udp:battle = 11 */
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

/** when.guards 的一个条件项。 */
export interface Guard {
  field: string;
  /** eq|neq|gt|gte|lt|lte */
  op: string;
  value: number;
}

/** errors.json：code → 中文文案。 */
export type ErrorMap = Record<string, string>;

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
