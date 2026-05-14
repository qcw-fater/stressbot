import { Handle, Position, type NodeProps } from '@xyflow/react';
import { NodeShell } from './shared/NodeShell';

export function ContinueNode({ id, selected }: NodeProps) {
  return (
    <>
      <Handle type="target" position={Position.Left} id="in" />
      <NodeShell
        nodeId={id}
        nodeType="continue"
        title="continue"
        shape="tag"
        selected={selected}
      />
    </>
  );
}
