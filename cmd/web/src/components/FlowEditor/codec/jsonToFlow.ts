/**
 * flow.json (TaskFlow) → React Flow 节点 + 边
 *
 * 设计文档 §10.4 / 18.1。
 *
 * 转换规则：
 *   sequence.next[i]            → edge[source=id, sourceHandle=`seq-${i}`, target=next[i], type='seq']
 *   loop.body                   → edge[source=id, sourceHandle='body', target=body, type='loopBody']
 *   boolean.trueNext/falseNext  → edge[source=id, sourceHandle='true'/'false', target=*, type='branch']
 *   weighted.options[i]         → edge[source=id, sourceHandle=`opt-${i}`, target=*, type='weight', label=weight]
 *   action.listenRefs[i]   → edge[source=id, sourceHandle='listen', target=`__cb__${callback}`, type='listen']（callback=null 不画边）
 */

import type { Edge, Node as RFNode } from '@xyflow/react';
import type { FlowNode, ListenRef, WeightedOption } from '@/types/flow';
import type { ListenDef } from '@/types/listen';
import type { ActionDef } from '@/types/action';

/** flow.json 原始格式 */
export interface FlowJsonInput {
  defaultDelayMs: number;
  nodes: Record<string, FlowNode>;
  actions: Record<string, ActionDef>;
  listens: Record<string, ListenDef>;
}

export interface ConvertResult {
  rfNodes: RFNode[];
  rfEdges: Edge[];
  /** listen name → 引用计数（来自所有 action 的 listenRefs） */
  listenRefCount: Record<string, number>;
  /** listen name → 注册它的 action 节点 ID 列表（用于反向悬停高亮） */
  nodesByListen: Record<string, string[]>;
}

/** 把 flow.json 转换为 React Flow 节点 / 边数组（位置由后续 dagre 计算） */
export function jsonToFlow(flow: FlowJsonInput): ConvertResult {
  const rfNodes: RFNode[] = [];
  const rfEdges: Edge[] = [];
  const listenRefCount: Record<string, number> = {};
  const nodesByListen: Record<string, string[]> = {};

  // 1. 主 DAG 节点
  // 兼容旧 breakOff → errorStrategy
  for (const node of Object.values(flow.nodes) as Record<string, unknown>[]) {
    if ((node as Record<string, unknown>).breakOff && !(node as Record<string, unknown>).errorStrategy) {
      (node as Record<string, unknown>).errorStrategy = 'abort';
    }
    delete (node as Record<string, unknown>).breakOff;
  }
  for (const [id, node] of Object.entries(flow.nodes)) {
    rfNodes.push({
      id,
      type: node.type,
      position: { x: 0, y: 0 },
      data: {
        nodeId: id,
        node,
        action: node.action ? flow.actions[node.action] : undefined,
      },
    });
  }

  // 2. ListenCard 节点（事件区，独立节点类型 'listenCard'）
  // 在事件区按 listen 名字母序竖排
  let cardIdx = 0;
  for (const [cbName, cb] of Object.entries(flow.listens)) {
    rfNodes.push({
      id: `__cb__${cbName}`,
      type: 'listenCard',
      position: { x: 0, y: 0 },
      data: {
        listenName: cbName,
        listen: cb,
        index: cardIdx++,
      },
    });
  }

  // 3. 边：根据 node.type 派发；额外把 sourceNodeType 写入 data，让 edge 渲染按起点节点配色
  for (const [id, node] of Object.entries(flow.nodes)) {
    const edges = emitEdgesFor(id, node, flow);
    for (const e of edges) {
      e.data = { ...(e.data as Record<string, unknown> | undefined), sourceNodeType: node.type };
    }
    rfEdges.push(...edges);

    // 同时统计 listen 引用计数 + 反向索引（节点 → listen）
    if (node.type === 'action' && node.listenRefs) {
      for (const ref of node.listenRefs) {
        if (ref.listen != null) {
          listenRefCount[ref.listen] = (listenRefCount[ref.listen] ?? 0) + 1;
          (nodesByListen[ref.listen] ??= []).push(id);
        }
      }
    }
  }

  return { rfNodes, rfEdges, listenRefCount, nodesByListen };
}

function emitEdgesFor(id: string, node: FlowNode, flow: FlowJsonInput): Edge[] {
  switch (node.type) {
    case 'sequence':
      return emitSequenceEdges(id, node.next ?? []);
    case 'action':
      return emitListenEdges(id, node.listenRefs ?? [], flow);
    case 'loop':
      return node.body ? [makeEdge(`${id}->body->${node.body}`, id, node.body, 'loopBody', 'body')] : [];
    case 'boolean': {
      const out: Edge[] = [];
      if (node.trueNext) {
        out.push(makeEdge(`${id}->true->${node.trueNext}`, id, node.trueNext, 'branch', 'true', { branch: 'true' }));
      }
      if (node.falseNext) {
        out.push(makeEdge(`${id}->false->${node.falseNext}`, id, node.falseNext, 'branch', 'false', { branch: 'false' }));
      }
      return out;
    }
    case 'weighted':
      return emitWeightedEdges(id, node.options ?? []);
    case 'wait':
    case 'break':
    case 'continue':
      return [];
    default:
      return [];
  }
}

function emitSequenceEdges(id: string, next: string[]): Edge[] {
  return next.map((target, i) =>
    makeEdge(`${id}->seq[${i}]->${target}`, id, target, 'seq', `seq-${i}`, { order: i }),
  );
}

function emitWeightedEdges(id: string, options: WeightedOption[]): Edge[] {
  const total = options.reduce((s, o) => s + Math.max(0, o.weight), 0);
  return options.map((opt, i) =>
    makeEdge(`${id}->opt[${i}]->${opt.node}`, id, opt.node, 'weight', `opt-${i}`, {
      weight: opt.weight,
      ratio: total > 0 ? opt.weight / total : 0,
    }),
  );
}

function emitListenEdges(id: string, refs: ListenRef[], flow: FlowJsonInput): Edge[] {
  const out: Edge[] = [];
  refs.forEach((ref, i) => {
    if (ref.listen == null) return; // null = 静默丢弃，不画边
    if (!(ref.listen in flow.listens)) return; // 引用不存在则忽略（校验阶段会报错）
    out.push(
      makeEdge(
        `${id}->listen[${i}]->${ref.listen}`,
        id,
        `__cb__${ref.listen}`,
        'listen',
        'listen',
        { route: ref.route, server: ref.server, refIndex: i },
      ),
    );
  });
  return out;
}

function makeEdge(
  id: string,
  source: string,
  target: string,
  type: string,
  sourceHandle?: string,
  data?: Record<string, unknown>,
): Edge {
  return {
    id,
    source,
    target,
    type,
    sourceHandle,
    data: data ?? {},
  };
}
