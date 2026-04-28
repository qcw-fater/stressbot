/**
 * sequence 节点：左入 + 右出（每行对应一个 next，每行右侧 handle 对齐）。
 *
 * 视觉：每行显示「序号 · 子节点 ID」，行高严格固定 22px，等宽数字列防止两位数错位。
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

export function SequenceNode({ id, data, selected }: NodeProps) {
  const { node } = data as unknown as NodeData;
  const next = node.next ?? [];

  return (
    <>
      <Handle type="target" position={Position.Left} id="in" />
      <NodeShell
        nodeId={id}
        nodeType="sequence"
        title={id}
        subtitle={`seq · ${next.length}`}
        selected={selected}
        minWidth={180}
        description={node.description}
      >
        <div className="row-list">
          {next.map((name, i) => (
            <div key={i} className="row-item">
              <span className="row-index">{i + 1}.</span>
              <span className="row-name">{name}</span>
            </div>
          ))}
          {/* "+" 占位行：始终渲染，与 add handle 对齐，便于拖线续接 */}
          <div className="row-item row-item-add" title="从右侧虚线 handle 拖线到目标节点 → 追加 next">
            <span className="row-index">+</span>
            <span className="row-name" style={{ color: 'var(--text-tertiary)', fontStyle: 'italic' }}>
              拖线续接…
            </span>
          </div>
        </div>
      </NodeShell>
      {next.map((_, i) => (
        <Handle
          key={i}
          type="source"
          position={Position.Right}
          id={`seq-${i}`}
          style={{ top: HEADER_OFFSET + i * ROW_HEIGHT }}
        />
      ))}
      {/* "+" handle：拖线到目标节点 → 追加 next */}
      <Handle
        type="source"
        position={Position.Right}
        id="seq-add"
        className="handle-add"
        style={{ top: HEADER_OFFSET + next.length * ROW_HEIGHT }}
        title="拖线到目标节点 → 追加 next"
      />
    </>
  );
}
