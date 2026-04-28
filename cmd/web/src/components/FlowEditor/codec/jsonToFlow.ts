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
 *   action.listenCallbacks[i]   → edge[source=id, sourceHandle='listen', target=`__cb__${callback}`, type='listen']（callback=null 不画边）
 */

import type { Edge, Node as RFNode } from '@xyflow/react';
import type { FlowNode, ListenRef, TaskFlow, WeightedOption } from '@/types/flow';

export interface ConvertResult {
  rfNodes: RFNode[];
  rfEdges: Edge[];
  /** callback name → 引用计数（来自所有 action 的 listenCallbacks） */
  callbackRefCount: Record<string, number>;
  /** callback name → 注册它的 action 节点 ID 列表（用于反向悬停高亮） */
  nodesByCallback: Record<string, string[]>;
}

/** 把 TaskFlow 转换为 React Flow 节点 / 边数组（位置由后续 dagre 计算） */
export function jsonToFlow(flow: TaskFlow): ConvertResult {
  const rfNodes: RFNode[] = [];
  const rfEdges: Edge[] = [];
  const callbackRefCount: Record<string, number> = {};
  const nodesByCallback: Record<string, string[]> = {};

  // 1. 主 DAG 节点
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

  // 2. CallbackCard 节点（事件区，独立节点类型 'callbackCard'）
  // 在事件区按 callback 名字母序竖排
  let cardIdx = 0;
  for (const [cbName, cb] of Object.entries(flow.callbacks)) {
    rfNodes.push({
      id: `__cb__${cbName}`,
      type: 'callbackCard',
      position: { x: 0, y: 0 },
      data: {
        callbackName: cbName,
        callback: cb,
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

    // 同时统计 callback 引用计数 + 反向索引（节点 → callback）
    if (node.type === 'action' && node.listenCallbacks) {
      for (const ref of node.listenCallbacks) {
        if (ref.callback != null) {
          callbackRefCount[ref.callback] = (callbackRefCount[ref.callback] ?? 0) + 1;
          (nodesByCallback[ref.callback] ??= []).push(id);
        }
      }
    }
  }

  return { rfNodes, rfEdges, callbackRefCount, nodesByCallback };
}

function emitEdgesFor(id: string, node: FlowNode, flow: TaskFlow): Edge[] {
  switch (node.type) {
    case 'sequence':
      return emitSequenceEdges(id, node.next ?? []);
    case 'action':
      return emitListenEdges(id, node.listenCallbacks ?? [], flow);
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

function emitListenEdges(id: string, refs: ListenRef[], flow: TaskFlow): Edge[] {
  const out: Edge[] = [];
  refs.forEach((ref, i) => {
    if (ref.callback == null) return; // null = 静默丢弃，不画边
    if (!(ref.callback in flow.callbacks)) return; // 引用不存在则忽略（校验阶段会报错）
    out.push(
      makeEdge(
        `${id}->listen[${i}]->${ref.callback}`,
        id,
        `__cb__${ref.callback}`,
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
