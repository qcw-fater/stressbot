/**
 * ActionDef / FieldBind / FilterDef / StoreMapping
 *
 * 严格镜像 Go 端 `engine/flow.go`。
 */

export type ActionPattern =
  | 'tcpSend' | 'tcpRequest' | 'tcpConnect' | 'tcpClose' | 'tcpListen'
  | 'udpSend' | 'udpRequest' | 'udpConnect' | 'udpClose' | 'udpListen'
  | 'tcpHeartbeat' | 'udpHeartbeat'
  | 'httpRequest' | 'setState' | 'clearState' | 'lua';

export const ALL_ACTION_PATTERNS: ActionPattern[] = [
  'tcpSend', 'tcpRequest', 'tcpConnect', 'tcpClose', 'tcpListen',
  'udpSend', 'udpRequest', 'udpConnect', 'udpClose', 'udpListen',
  'tcpHeartbeat', 'udpHeartbeat',
  'httpRequest', 'setState', 'clearState', 'lua',
];

/**
 * 字段绑定类型（17 种）。
 * 详见设计文档 §6.6 / Go 端 FieldBind 注释。
 */
export type BindingType =
  | 'fixed'
  | 'state'
  | 'stateFirst'
  | 'stateRandom'
  | 'stateRandomN'
  | 'stateMapKey'
  | 'stateMapValue'
  | 'randomPick'
  | 'randomPickN'
  | 'randomPickMap'
  | 'randomInt'
  | 'randomFloat'
  | 'randomBool'
  | 'randomString'
  | 'randomExclude'
  | 'listSize'
  | 'map';

export const ALL_BINDING_TYPES: BindingType[] = [
  'fixed',
  'state',
  'stateFirst',
  'stateRandom',
  'stateRandomN',
  'stateMapKey',
  'stateMapValue',
  'randomPick',
  'randomPickN',
  'randomPickMap',
  'randomInt',
  'randomFloat',
  'randomBool',
  'randomString',
  'randomExclude',
  'listSize',
  'map',
];

export type FilterMode = 'any' | 'all' | 'none';

export interface FilterDef {
  path?: string;
  op: string; // eq / neq / gt / gte / lt / lte / contains / notContains / in / notIn / notNil / isNil
  value?: unknown;
  source?: string;
  mode?: FilterMode;
}

export interface MapEntryBind {
  key?: unknown;
  value?: FieldBind;
}

export interface FieldBind {
  field?: string;
  type: BindingType;
  value?: unknown;
  source?: string;
  path?: string;
  values?: unknown[];
  required?: boolean;
  filters?: FilterDef[];
  min?: number;
  max?: number;
  precision?: number;
  length?: number;
  count?: number;
  charset?: string;
  excludeSource?: string;
  optional?: boolean;
  wrap?: boolean;
  storeAs?: string;
  keySource?: string;
  condition?: string;
  entries?: MapEntryBind[];
}

export interface StoreMapping {
  field?: string;
  setter: string;
}

/**
 * 声明式心跳字段（raw-binary 小端布局）。
 *
 * 严格镜像 Go 端 `engine/heartbeat.go:HeartbeatField`。
 * 字段名 / json tag / 类型逐一对齐，勿臆测。
 */
export interface HeartbeatField {
  type: HeartbeatFieldType;              // u8/u16/u32/u64/i8/i16/i32/i64（小端打包）
  source: HeartbeatFieldSource;          // fixed/state/stateCounter/counter/timestamp/randomInt
  /** source=fixed：固定值（nil → Go 报错，不静默默认） */
  value?: number;
  /** source=state|stateCounter：state 键名（缺失 → Go 报错） */
  key?: string;
  /** source=randomInt：下界（含）；缺失 → Go 报错 */
  min?: number;
  /** source=randomInt：上界（含）；缺失 → Go 报错 */
  max?: number;
  /** source=counter：私有计数器初值（缺省 0） */
  start?: number;
  /** source=counter：递增步长（缺省 1，由调用方应用） */
  step?: number;
  /** source=timestamp："ms"|"s"，缺省 ms */
  unit?: 'ms' | 's';
}

/** 心跳字段支持的整数类型（小端宽度）。镜像 Go heartbeatTypeWidth 的 key 集合。 */
export type HeartbeatFieldType = 'u8' | 'i8' | 'u16' | 'i16' | 'u32' | 'i32' | 'u64' | 'i64';

export const ALL_HEARTBEAT_FIELD_TYPES: HeartbeatFieldType[] = [
  'u8', 'i8', 'u16', 'i16', 'u32', 'i32', 'u64', 'i64',
];

/**
 * 心跳字段 source。
 * 镜像 Go 端 `engine/heartbeat.go` 的 HeartbeatSource* 常量。
 */
export type HeartbeatFieldSource =
  | 'fixed'
  | 'state'
  | 'stateCounter'
  | 'counter'
  | 'timestamp'
  | 'randomInt';

export const ALL_HEARTBEAT_FIELD_SOURCES: HeartbeatFieldSource[] = [
  'fixed', 'state', 'stateCounter', 'counter', 'timestamp', 'randomInt',
];

export interface ActionDef {
  pattern: ActionPattern;
  service?: string;
  route?: unknown;
  script?: string;
  address?: string;
  c2sProto?: string;
  s2cProto?: string;
  bindings?: FieldBind[];
  store?: StoreMapping[];
  timeout?: number;
  pollMs?: number;
  url?: string;           // httpRequest: 请求 URL
  method?: 'POST' | 'GET'; // httpRequest: HTTP 方法
  contentType?: 'json' | 'form'; // httpRequest: Content-Type
  keys?: string[]; // clearState
  // ── tcpHeartbeat / udpHeartbeat 专用 ─────────────────────────────
  // 声明式二进制心跳（Go-only builder）。heartbeatFields 为空 = 空 body（静态心跳）。
  // 心跳每 tick 在 Go 内按布局打包（读 state/计数器/时间/随机），不触碰业务 LState。
  /** 心跳间隔（毫秒），>0。镜像 Go ActionDef.IntervalMs。 */
  intervalMs?: number;
  /** 二进制布局（小端），空 = 空 body。镜像 Go ActionDef.HeartbeatFields。 */
  heartbeatFields?: HeartbeatField[];
  /** state 源缺失时跳过本 tick（true）而非报错。仅 raw-binary 模式有意义。镜像 Go ActionDef.SkipWhenMissing。 */
  skipWhenMissing?: boolean;
}
