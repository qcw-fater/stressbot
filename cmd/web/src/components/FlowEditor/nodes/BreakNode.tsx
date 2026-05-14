import { Handle, Position, type NodeProps } from '@xyflow/react';
import { NodeShell } from './shared/NodeShell';

export function BreakNode({ id, selected }: NodeProps) {
  return (
    <>
      <Handle type="target" position={Position.Left} id="in" />
      <NodeShell
        nodeId={id}
        nodeType="break"
        title="break"
        shape="tag"
        selected={selected}
      />
    </>
  );
}
