/**
 * weighted 节点：矩形外形（与 sequence 一致），左入 + 右出（每行一个 option，handle 对齐行）。
 * 内嵌横向条形图直观显示权重比例。
 */

import { Handle, Position, type NodeProps } from '@xyflow/react';
import { NodeShell } from './shared/NodeShell';
import type { FlowNode } from '@/types/flow';

interface NodeData {
  nodeId: string;
  node: FlowNode;
}

const ROW_HEIGHT = 22;
const HEADER_OFFSET = 30;

export function WeightedNode({ id, data, selected }: NodeProps) {
  const { node } = data as unknown as NodeData;
  const options = node.options ?? [];
  const total = options.reduce((s, o) => s + Math.max(0, o.weight), 0);

  return (
    <>
      <Handle type="target" position={Position.Left} id="in" />
      <NodeShell
        nodeId={id}
        nodeType="weighted"
        title={id}
        subtitle={`weighted · ${options.length}`}
        selected={selected}
        minWidth={240}
        description={node.description}
      >
        <div className="row-list">
          {options.map((o, i) => {
            const ratio = total > 0 ? o.weight / total : 0;
            return (
              <div className="row-item" key={i}>
                <span className="row-name" title={o.node}>
                  {o.node}
                </span>
                <div className="weight-bar">
                  <div className="weight-bar-fill" style={{ width: `${ratio * 100}%` }} />
                </div>
                <span className="row-tail">{(ratio * 100).toFixed(1)}%</span>
              </div>
            );
          })}
          <div className="row-item row-item-add" title="从右侧虚线 handle 拖线到目标节点 → 追加 option（默认权重 1）">
            <span className="row-name" style={{ color: 'var(--text-tertiary)', fontStyle: 'italic' }}>
              + 拖线续接…
            </span>
          </div>
        </div>
      </NodeShell>
      {options.map((_, i) => (
        <Handle
          key={i}
          type="source"
          position={Position.Right}
          id={`opt-${i}`}
          style={{ top: HEADER_OFFSET + i * ROW_HEIGHT }}
        />
      ))}
      <Handle
        type="source"
        position={Position.Right}
        id="opt-add"
        className="handle-add"
        style={{ top: HEADER_OFFSET + options.length * ROW_HEIGHT }}
        title="拖线到目标节点 → 追加 option（默认权重 1）"
      />
    </>
  );
}
