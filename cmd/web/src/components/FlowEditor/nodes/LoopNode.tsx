/**
 * loop 节点：胶囊形（强调"循环"语义），左入 + 右出（body 唯一）。
 *
 * body 跑完后由外层 sequence 接管，loop 自身不出 exit。
 */

import { Handle, Position, type NodeProps } from '@xyflow/react';
import { NodeShell } from './shared/NodeShell';
import type { FlowNode } from '@/types/flow';

interface NodeData {
  nodeId: string;
  node: FlowNode;
}

export function LoopNode({ id, data, selected }: NodeProps) {
  const { node } = data as unknown as NodeData;
  const count = node.loopCount;
  const countText =
    typeof count === 'number' && count <= 0 ? '∞' : typeof count === 'number' ? `×${count}` : '×1';

  return (
    <>
      <Handle type="target" position={Position.Left} id="in" />
      <NodeShell
        nodeId={id}
        nodeType="loop"
        title={id}
        subtitle={`loop ${countText}`}
        shape="pill"
        selected={selected}
        minWidth={140}
        description={node.description}
      >
        {node.body && (
          <div style={{ fontFamily: 'monospace' }}>
            <span style={{ color: 'var(--text-tertiary)' }}>body:</span> {node.body}
          </div>
        )}
      </NodeShell>
      <Handle type="source" position={Position.Right} id="body" />
    </>
  );
}
