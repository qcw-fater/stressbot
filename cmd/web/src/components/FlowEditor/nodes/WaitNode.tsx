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

function fmtVal(ms: number | undefined): string {
  if (ms == null || !Number.isFinite(ms)) return '?';
  return ms.toLocaleString();
}

export function WaitNode({ id, data, selected }: NodeProps) {
  const { node } = data as unknown as NodeData;
  const hasRandom = typeof node.waitMin === 'number' || typeof node.waitMax === 'number';

  const label = hasRandom
    ? `${fmtVal(node.waitMin)}~${fmtVal(node.waitMax)}ms`
    : `${fmtVal(node.waitMs)}ms`;

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
        <div style={{ fontSize: 11, fontWeight: 600, color: 'var(--node-wait-border-active)' }}>
          {label}
        </div>
      </NodeShell>
      <Handle type="source" position={Position.Right} id="out" />
    </>
  );
}
