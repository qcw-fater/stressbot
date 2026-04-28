/**
 * ActionDef / FieldBind / FilterDef / StoreMapping
 *
 * 严格镜像 Go 端 `engine/flow.go`。
 */

export type ActionPattern =
  | 'tcpSend'
  | 'tcpRequest'
  | 'lua'
  | 'connect'
  | 'connectUDP'
  | 'exchangeKey'
  | 'close'
  | 'clearState'
  | 'udpSendProto'
  | 'waitListen'
  | 'setState';

export const ALL_ACTION_PATTERNS: ActionPattern[] = [
  'tcpSend',
  'tcpRequest',
  'lua',
  'connect',
  'connectUDP',
  'exchangeKey',
  'close',
  'clearState',
  'udpSendProto',
  'waitListen',
  'setState',
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
  | 'randomBool'
  | 'randomString'
  | 'randomExclude'
  | 'listSize'
  | 'nested'
  | 'nestedList';

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
  'randomBool',
  'randomString',
  'randomExclude',
  'listSize',
  'nested',
  'nestedList',
];

export interface FilterDef {
  path?: string;
  op: string; // eq / neq / gt / gte / lt / lte / contains / in / timeWindow / dailyTimeWindow / notNil / isNil
  value?: unknown;
  source?: string;
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
  length?: number;
  count?: number;
  charset?: string;
  excludeSource?: string;
  optional?: boolean;
  wrap?: boolean;
  message?: string;
  bindings?: FieldBind[];
  storeAs?: string;
  keySource?: string;
  items?: FieldBind[];
}

export interface StoreMapping {
  field?: string;
  path?: string;
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
  target?: string; // close: tcp / udp
  keys?: string[]; // clearState
  optional?: boolean;
  secretArg?: string;
}
