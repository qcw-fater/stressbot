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
