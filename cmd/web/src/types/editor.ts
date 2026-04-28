/**
 * 编辑器内部视图状态类型（不参与 flow.json 序列化）。
 */

/** 节点画布坐标（dagre 计算或用户拖动后） */
export interface NodeLayoutMeta {
  x: number;
  y: number;
}

/**
 * flow.json 之外的视觉副产物，独立存档为 flow.layout.json。
 * 详见设计文档 §10.3。
 *
 * 注意：CallbackCard 节点的位置也存在 nodePositions 中（key 为 `__cb__<name>`），
 * 不再单独维护 callbackPositions 字段。
 */
export interface FlowLayout {
  nodePositions: Record<string, NodeLayoutMeta>;
  /** Toolbar 中各开关状态 */
  showListenEdges?: boolean;
}

export function emptyFlowLayout(): FlowLayout {
  return {
    nodePositions: {},
    showListenEdges: true,
  };
}
