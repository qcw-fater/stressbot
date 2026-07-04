/**
 * ActionDef / FieldBind / FilterDef / StoreMapping
 *
 * 严格镜像 Go 端 `engine/flow.go`。
 */

export type ActionPattern =
  | 'tcpSend' | 'tcpRequest' | 'tcpConnect' | 'tcpClose' | 'tcpListen'
  | 'udpSend' | 'udpRequest' | 'udpConnect' | 'udpClose' | 'udpListen'
  | 'httpRequest' | 'setState' | 'clearState' | 'lua';

export const ALL_ACTION_PATTERNS: ActionPattern[] = [
  'tcpSend', 'tcpRequest', 'tcpConnect', 'tcpClose', 'tcpListen',
  'udpSend', 'udpRequest', 'udpConnect', 'udpClose', 'udpListen',
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
  type: HeartbeatFieldType;              // u8/u16/u32/u64/i8/i16/i32/i64（小端整数）/ f32/f64（小端 IEEE754 浮点）
  source: HeartbeatFieldSource;          // fixed/state/stateCounter/counter/timestamp/randomInt（f32/f64 仅 fixed/state）
  /** source=fixed 且整型：固定值（nil → Go 报错，不静默默认） */
  value?: number;
  /** source=fixed 且 f32/f64：固定浮点值（缺失 → Go 报错） */
  floatValue?: number;
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

/** 心跳字段支持的类型（小端宽度）：整数 u8..i64 + 浮点 f32/f64（IEEE754）。镜像 Go heartbeatTypeWidth 的 key 集合。 */
export type HeartbeatFieldType = 'u8' | 'i8' | 'u16' | 'i16' | 'u32' | 'i32' | 'u64' | 'i64' | 'f32' | 'f64';

export const ALL_HEARTBEAT_FIELD_TYPES: HeartbeatFieldType[] = [
  'u8', 'i8', 'u16', 'i16', 'u32', 'i32', 'u64', 'i64', 'f32', 'f64',
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
}
