/**
 * boolean 节点：胶囊形（与 loop 一致），左入 + 右出 true（上）+ 右出 false（下）。
 */

import { Handle, Position, type NodeProps } from '@xyflow/react';
import { NodeShell } from './shared/NodeShell';
import type { FlowNode } from '@/types/flow';

interface NodeData {
  nodeId: string;
  node: FlowNode;
}

function trim(s: string, n: number): string {
  return s.length > n ? s.slice(0, n) + '…' : s;
}

export function BooleanNode({ id, data, selected }: NodeProps) {
  const { node } = data as unknown as NodeData;
  return (
    <>
      <Handle type="target" position={Position.Left} id="in" />
      <NodeShell
        nodeId={id}
        nodeType="boolean"
        title={id}
        subtitle="if"
        shape="pill"
        selected={selected}
        minWidth={160}
        description={node.description}
      >
        {node.condition ? (
          <div title={node.condition} style={{ fontFamily: 'monospace', fontSize: 10 }}>
            {trim(node.condition, 32)}
          </div>
        ) : (
          <em style={{ color: 'var(--text-tertiary)' }}>未配置</em>
        )}
      </NodeShell>
      <Handle
        type="source"
        position={Position.Right}
        id="true"
        style={{ top: '35%', background: 'var(--edge-true)' }}
      />
      <Handle
        type="source"
        position={Position.Right}
        id="false"
        style={{ top: '70%', background: 'var(--edge-false)' }}
      />
    </>
  );
}
