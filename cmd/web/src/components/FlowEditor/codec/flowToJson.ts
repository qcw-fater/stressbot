/**
 * Store 状态 → flow.json
 *
 * 与 jsonToFlow 互逆。剪除空字段（避免与 Go 端 JSON 解码语义冲突）。
 */

import type { ActionDef, FieldBind, FilterDef, StoreMapping } from '@/types/action';
import type { ListenDef } from '@/types/listen';
import type { FlowNode, ListenRef, WeightedOption } from '@/types/flow';
import { pruneActionByPattern } from '../editors/ActionEditor/actionPrune';

export interface ExportInput {
  defaultDelayMs: number;
  nodes: Record<string, FlowNode>;
  actions: Record<string, ActionDef>;
  listens: Record<string, ListenDef>;
}

/** flow.json 格式 */
export interface FlowJson {
  defaultDelayMs: number;
  nodes: Record<string, FlowNode>;
  actions: Record<string, ActionDef>;
  listens: Record<string, ListenDef>;
}

export function flowToJson(input: ExportInput): FlowJson {
  return {
    defaultDelayMs: input.defaultDelayMs,
    nodes: mapValues(input.nodes, cleanNode),
    actions: mapValues(input.actions, cleanAction),
    listens: mapValues(input.listens, cleanListen),
  };
}

function cleanNode(n: FlowNode): FlowNode {
  const out: FlowNode = { type: n.type };
  // 节点描述：UI 注释字段，Go 端不识别但 json.Unmarshal 会忽略未知字段，安全保留
  if (n.description?.trim()) out.description = n.description.trim();
  switch (n.type) {
    case 'sequence':
      if (n.next?.length) out.next = [...n.next];
      break;
    case 'action':
      if (n.action) out.action = n.action;
      if (n.errorStrategy && n.errorStrategy !== 'ignore') out.errorStrategy = n.errorStrategy;
      if (n.listenRefs?.length) out.listenRefs = n.listenRefs.map(cleanListenRef);
      if (typeof n.delayMs === 'number' && n.delayMs !== 0) out.delayMs = n.delayMs;
      break;
    case 'loop':
      if (n.body) out.body = n.body;
      if (typeof n.loopCount === 'number') out.loopCount = n.loopCount;
      if (n.condition) out.condition = n.condition;
      if (n.breakCondition) out.breakCondition = n.breakCondition;
      break;
    case 'boolean':
      if (n.condition) out.condition = n.condition;
      if (n.trueNext) out.trueNext = n.trueNext;
      if (n.falseNext) out.falseNext = n.falseNext;
      if (typeof n.delayMs === 'number' && n.delayMs !== 0) out.delayMs = n.delayMs;
      break;
    case 'weighted':
      if (n.options?.length) out.options = n.options.map((o): WeightedOption => ({ node: o.node, weight: o.weight }));
      break;
    case 'wait':
      if (typeof n.waitMs === 'number' && n.waitMs !== 0) out.waitMs = n.waitMs;
      if (typeof n.waitMin === 'number') out.waitMin = n.waitMin;
      if (typeof n.waitMax === 'number') out.waitMax = n.waitMax;
      break;
    case 'break':
    case 'continue':
      break;
  }
  return out;
}

function cleanListenRef(r: ListenRef): ListenRef {
  return {
    route: r.route,
    server: r.server,
    listen: r.listen,
  };
}

function cleanAction(a: ActionDef): ActionDef {
  const pruned = pruneActionByPattern(a);
  const out: ActionDef = { pattern: pruned.pattern };
  if (pruned.service) out.service = pruned.service;
  if (pruned.route !== undefined && pruned.route !== null) out.route = pruned.route;
  if (pruned.script) out.script = pruned.script;
  if (pruned.address) out.address = pruned.address;
  if (pruned.c2sProto) out.c2sProto = pruned.c2sProto;
  if (pruned.s2cProto) out.s2cProto = pruned.s2cProto;
  if (pruned.bindings?.length) out.bindings = pruned.bindings.map(cleanFieldBind);
  if (pruned.store?.length) out.store = pruned.store.map(cleanStoreMapping);
  if (typeof pruned.timeout === 'number' && pruned.timeout > 0) out.timeout = pruned.timeout;
  if (typeof pruned.pollMs === 'number' && pruned.pollMs > 0) out.pollMs = pruned.pollMs;
  if (pruned.url) out.url = pruned.url;
  if (pruned.method) out.method = pruned.method;
  if (pruned.contentType) out.contentType = pruned.contentType;
  if (pruned.keys?.length) out.keys = [...pruned.keys];
  return out;
}

function cleanFieldBind(b: FieldBind): FieldBind {
  const out: FieldBind = { type: b.type };
  if (b.field) out.field = b.field;
  if (b.value !== undefined) out.value = b.value;
  if (b.source) out.source = b.source;
  if (b.path) out.path = b.path;
  if (b.values?.length) out.values = [...b.values];
  if (b.required) out.required = true;
  if (b.filters?.length) out.filters = b.filters.map(cleanFilter);
  if (typeof b.min === 'number') out.min = b.min;
  if (typeof b.max === 'number') out.max = b.max;
  if (typeof b.length === 'number') out.length = b.length;
  if (typeof b.count === 'number') out.count = b.count;
  if (b.charset) out.charset = b.charset;
  if (b.excludeSource) out.excludeSource = b.excludeSource;
  if (b.optional) out.optional = true;
  if (b.wrap) out.wrap = true;
  // if (b.message) out.message = b.message;
  // if (b.bindings?.length) out.bindings = b.bindings.map(cleanFieldBind);
  if (b.storeAs) out.storeAs = b.storeAs;
  if (b.keySource) out.keySource = b.keySource;
  // if (b.items?.length) out.items = b.items.map(cleanFieldBind);
  if (b.condition) out.condition = b.condition;
  if (b.entries?.length) {
    out.entries = b.entries.map((entry) => ({
      key: entry.key,
      value: entry.value ? cleanMapEntryValueBind(entry.value) : undefined,
    }));
  }
  return out;
}

function cleanMapEntryValueBind(b: FieldBind): FieldBind {
  const out = cleanFieldBind({ ...b, field: undefined, storeAs: undefined, condition: undefined, wrap: undefined });
  delete out.field;
  delete out.storeAs;
  delete out.condition;
  delete out.wrap;
  return out;
}

function cleanFilter(f: FilterDef): FilterDef {
  const out: FilterDef = { op: f.op };
  if (f.path) out.path = f.path;
  if (f.value !== undefined) out.value = f.value;
  if (f.source) out.source = f.source;
  if (f.mode) out.mode = f.mode;
  return out;
}

function cleanStoreMapping(s: StoreMapping): StoreMapping {
  const out: StoreMapping = { setter: s.setter };
  if (s.field) out.field = s.field;
  return out;
}

function cleanListen(c: ListenDef): ListenDef {
  const out: ListenDef = {};
  if (c.s2cProto) out.s2cProto = c.s2cProto;
  if (c.store?.length) out.store = c.store.map(cleanStoreMapping);
  if (c.script) out.script = c.script;
  if (c.description?.trim()) out.description = c.description.trim();
  return out;
}

function mapValues<V, R>(obj: Record<string, V>, fn: (v: V) => R): Record<string, R> {
  const out: Record<string, R> = {};
  for (const [k, v] of Object.entries(obj)) {
    out[k] = fn(v);
  }
  return out;
}
