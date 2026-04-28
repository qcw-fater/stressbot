/**
 * 基于 dagre 的 DAG 自动布局。
 *
 * 输入：React Flow 的 nodes/edges
 * 输出：每个节点的 {x, y} 坐标
 *
 * 设计文档 §10.3 / 14：导入时自动布局，编辑过程中由用户手动触发。
 */

import dagre from 'dagre';
import type { Edge, Node as RFNode } from '@xyflow/react';

export interface LayoutOptions {
  /** 主轴方向：LR=左到右，TB=上到下 */
  direction?: 'LR' | 'TB';
  nodeWidth?: number;
  nodeHeight?: number;
  rankSep?: number;
  nodeSep?: number;
}

const DEFAULT_OPTIONS: Required<LayoutOptions> = {
  direction: 'LR',
  nodeWidth: 220,
  nodeHeight: 80,
  rankSep: 80,
  nodeSep: 40,
};

/**
 * 计算每个节点的位置（不修改原数组，返回新坐标 map）。
 */
export function dagreLayout(
  nodes: RFNode[],
  edges: Edge[],
  options: LayoutOptions = {},
): Record<string, { x: number; y: number }> {
  const opts = { ...DEFAULT_OPTIONS, ...options };
  const g = new dagre.graphlib.Graph({ multigraph: true });
  g.setGraph({
    rankdir: opts.direction,
    ranksep: opts.rankSep,
    nodesep: opts.nodeSep,
    marginx: 32,
    marginy: 32,
  });
  g.setDefaultEdgeLabel(() => ({}));

  for (const n of nodes) {
    const w =
      typeof n.width === 'number' && n.width > 0
        ? n.width
        : (n.measured?.width ?? opts.nodeWidth);
    const h =
      typeof n.height === 'number' && n.height > 0
        ? n.height
        : (n.measured?.height ?? opts.nodeHeight);
    g.setNode(n.id, { width: w, height: h });
  }
  for (const e of edges) {
    // 跳过 ListenEdge（不参与主 DAG 布局）
    if (e.type === 'listen') continue;
    g.setEdge(e.source, e.target, {}, e.id);
  }

  dagre.layout(g);

  const result: Record<string, { x: number; y: number }> = {};
  for (const n of nodes) {
    const node = g.node(n.id);
    if (!node) continue;
    // dagre 给的是节点中心坐标，React Flow 用左上角，需要减去半宽/半高
    result[n.id] = {
      x: node.x - node.width / 2,
      y: node.y - node.height / 2,
    };
  }
  return result;
}
