/**
 * wait 节点：圆形（小巧），左入 + 右出。
 */

import { Handle, Position, type NodeProps } from '@xyflow/react';
import { NodeShell } from './shared/NodeShell';
import type { FlowNode } from '@/types/flow';

interface NodeData {
  nodeId: string;
  node: FlowNode;
}

export function WaitNode({ id, data, selected }: NodeProps) {
  const { node } = data as unknown as NodeData;
  const ms = node.waitMs ?? 0;
  const seconds = (ms / 1000).toFixed(ms % 1000 === 0 ? 0 : 2);

  return (
    <>
      <Handle type="target" position={Position.Left} id="in" />
      <NodeShell
        nodeId={id}
        nodeType="wait"
        title={id}
        shape="circle"
        selected={selected}
        description={node.description}
      >
        <div style={{ fontSize: 13, fontWeight: 700, color: 'var(--node-wait-border-active)' }}>{seconds}s</div>
      </NodeShell>
      <Handle type="source" position={Position.Right} id="out" />
    </>
  );
}
