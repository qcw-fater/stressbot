/**
 * break 节点：标签形（小巧），左入（无出）。
 */

import { Handle, Position, type NodeProps } from '@xyflow/react';
import { NodeShell } from './shared/NodeShell';
import type { FlowNode } from '@/types/flow';

export function BreakNode({ id, data, selected }: NodeProps) {
  const { node } = data as unknown as { node: FlowNode };
  return (
    <>
      <Handle type="target" position={Position.Left} id="in" />
      <NodeShell
        nodeId={id}
        nodeType="break"
        title={`break · ${id}`}
        shape="tag"
        selected={selected}
        description={node?.description}
      />
    </>
  );
}
