/**
 * Store 状态 → flow.json
 *
 * 与 jsonToFlow 互逆。剪除空字段（避免与 Go 端 JSON 解码语义冲突）。
 */

import type { ActionDef, FieldBind, FilterDef, StoreMapping } from '@/types/action';
import type { CallbackDef } from '@/types/callback';
import type { FlowNode, ListenRef, TaskFlow, WeightedOption } from '@/types/flow';

export interface ExportInput {
  defaultDelayMs: number;
  nodes: Record<string, FlowNode>;
  actions: Record<string, ActionDef>;
  callbacks: Record<string, CallbackDef>;
}

export function flowToJson(input: ExportInput): TaskFlow {
  return {
    defaultDelayMs: input.defaultDelayMs,
    nodes: mapValues(input.nodes, cleanNode),
    actions: mapValues(input.actions, cleanAction),
    callbacks: mapValues(input.callbacks, cleanCallback),
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
      if (n.breakOff) out.breakOff = true;
      if (n.listenCallbacks?.length) out.listenCallbacks = n.listenCallbacks.map(cleanListenRef);
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
      if (typeof n.waitMs === 'number') out.waitMs = n.waitMs;
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
    callback: r.callback,
  };
}

function cleanAction(a: ActionDef): ActionDef {
  const out: ActionDef = { pattern: a.pattern };
  if (a.service) out.service = a.service;
  if (a.route !== undefined && a.route !== null) out.route = a.route;
  if (a.script) out.script = a.script;
  if (a.address) out.address = a.address;
  if (a.c2sProto) out.c2sProto = a.c2sProto;
  if (a.s2cProto) out.s2cProto = a.s2cProto;
  if (a.bindings?.length) out.bindings = a.bindings.map(cleanFieldBind);
  if (a.store?.length) out.store = a.store.map(cleanStoreMapping);
  if (typeof a.timeout === 'number' && a.timeout > 0) out.timeout = a.timeout;
  if (typeof a.pollMs === 'number' && a.pollMs > 0) out.pollMs = a.pollMs;
  if (a.target) out.target = a.target;
  if (a.keys?.length) out.keys = [...a.keys];
  if (a.optional) out.optional = true;
  if (a.secretArg) out.secretArg = a.secretArg;
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
  if (b.message) out.message = b.message;
  if (b.bindings?.length) out.bindings = b.bindings.map(cleanFieldBind);
  if (b.storeAs) out.storeAs = b.storeAs;
  if (b.keySource) out.keySource = b.keySource;
  if (b.items?.length) out.items = b.items.map(cleanFieldBind);
  return out;
}

function cleanFilter(f: FilterDef): FilterDef {
  const out: FilterDef = { op: f.op };
  if (f.path) out.path = f.path;
  if (f.value !== undefined) out.value = f.value;
  if (f.source) out.source = f.source;
  return out;
}

function cleanStoreMapping(s: StoreMapping): StoreMapping {
  const out: StoreMapping = { setter: s.setter };
  if (s.field) out.field = s.field;
  if (s.path) out.path = s.path;
  return out;
}

function cleanCallback(c: CallbackDef): CallbackDef {
  const out: CallbackDef = {};
  if (c.s2cProto) out.s2cProto = c.s2cProto;
  if (c.store?.length) out.store = c.store.map(cleanStoreMapping);
  if (c.script) out.script = c.script;
  return out;
}

function mapValues<V, R>(obj: Record<string, V>, fn: (v: V) => R): Record<string, R> {
  const out: Record<string, R> = {};
  for (const [k, v] of Object.entries(obj)) {
    out[k] = fn(v);
  }
  return out;
}
