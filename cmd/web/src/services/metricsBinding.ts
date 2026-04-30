/**
 * 把 StressSnapshot 中的 ActionMetric 数组绑定回 FlowEditor 的节点。
 *
 * 命名约定（与 monitor.ActionMetric 对齐）：
 * - 普通动作：`<actionName>`，例如 `CreateNormalTeam`
 * - 推送回调：`callback:<callbackName>`，例如 `callback:OnBattleStart`
 *
 * 一个 action 可被多个 node 引用（"复用"模板节点），所以 `nodeId → ActionMetric` 不是 1:1，
 * 我们直接把同一个 ActionMetric 对象关联给所有引用方，UI 显示一致即可。
 *
 * CallbackCard 这类不在 flow.nodes 里的虚拟节点用 `__cb__<name>` 前缀作 key，
 * 与 jsonToFlow 内 React Flow 节点 ID 命名保持一致。
 */

import type { ActionMetric, StressSnapshot } from '@/types/api';
import type { CallbackDef } from '@/types/callback';
import type { FlowNode } from '@/types/flow';

/** Flow 中需要的部分（避免直接依赖整个 flowStore，便于单测） */
export interface FlowSlice {
  nodes: Record<string, FlowNode>;
  callbacks: Record<string, CallbackDef>;
}

/** 节点上的指标快照（直接传给 MetricsBadge / 节点边框染色） */
export type NodeMetricsMap = Map<string, ActionMetric>;

const CALLBACK_PREFIX = 'callback:';
const CB_NODE_PREFIX = '__cb__';

/**
 * 用当前快照构建 nodeId → ActionMetric 映射。
 * snapshot/flow 任一缺失返回空 Map。
 */
export function buildNodeMetricsMap(
  snapshot: StressSnapshot | undefined,
  flow: FlowSlice | undefined,
): NodeMetricsMap {
  const result: NodeMetricsMap = new Map();
  if (!snapshot || !flow) return result;

  // 按 ActionMetric.name 建索引
  const byName = new Map<string, ActionMetric>();
  for (const m of snapshot.actions) {
    byName.set(m.name, m);
  }

  // action 节点
  for (const [nodeId, node] of Object.entries(flow.nodes)) {
    if (node.type !== 'action' || !node.action) continue;
    const m = byName.get(node.action);
    if (m) result.set(nodeId, m);
  }

  // callback 卡片
  for (const cbName of Object.keys(flow.callbacks)) {
    const m = byName.get(CALLBACK_PREFIX + cbName);
    if (m) result.set(CB_NODE_PREFIX + cbName, m);
  }

  return result;
}

/** 给 FlowEditor 用的 metricsProvider，返回 NodeMetrics（仅 MetricsBadge 关心的字段） */
export function makeMetricsProvider(map: NodeMetricsMap) {
  return (nodeId: string): ActionMetric | undefined => map.get(nodeId);
}

/** Apdex 阈值染色（与 docs/api-monitor §7.5 表对齐） */
export type ApdexLevel = 'excellent' | 'good' | 'fair' | 'poor' | 'danger' | 'unknown';

export function classifyApdex(apdex: number | undefined): ApdexLevel {
  if (apdex === undefined || Number.isNaN(apdex)) return 'unknown';
  if (apdex >= 0.94) return 'excellent';
  if (apdex >= 0.85) return 'good';
  if (apdex >= 0.7) return 'fair';
  if (apdex >= 0.5) return 'poor';
  return 'danger';
}
