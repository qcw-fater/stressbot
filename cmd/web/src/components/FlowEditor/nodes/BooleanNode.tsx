/**
 * boolean 节点：胶囊形（与 loop 一致），左入 + 右出 true（上）+ 右出 false（下）。
 */

import { Handle, Position, type NodeProps } from '@xyflow/react';
import { Tag, Tooltip } from 'antd';
import { NodeShell } from './shared/NodeShell';
import type { FlowNode } from '@/types/flow';

interface NodeData {
  nodeId: string;
  node: FlowNode;
}

function trim(s: string, n: number): string {
  return s.length > n ? s.slice(0, n) + '…' : s;
}

/** 将条件文本格式化为画布显示：剥离重复 state:/lua: 前缀，保留类型标记 */
function formatCondition(cond: string): { display: string; tag: string } {
  let display = cond.trim();
  let tag = '';

  while (display.startsWith('state:') || display.startsWith('lua:')) {
    if (display.startsWith('state:')) {
      tag = 'state';
      display = display.slice(6).trimStart();
      continue;
    }
    tag = 'lua';
    display = display.slice(4).trimStart();
  }

  return { display, tag };
}

export function BooleanNode({ id, data, selected }: NodeProps) {
  const { node } = data as unknown as NodeData;
  const normalizedCondition = node.condition ? formatCondition(node.condition) : null;
  const conditionTitle = normalizedCondition
    ? `${normalizedCondition.tag}:${normalizedCondition.display}`
    : '';

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
          <Tooltip title={conditionTitle} mouseEnterDelay={0.4}>
            <div style={{ fontFamily: 'monospace', fontSize: 10, display: 'flex', alignItems: 'center', gap: 4 }}>
              <ConditionDisplay condition={node.condition} />
            </div>
          </Tooltip>
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

/** 条件文本显示组件 */
function ConditionDisplay({ condition }: { condition: string }) {
  const { display, tag } = formatCondition(condition);
  const tagColor = tag === 'lua' ? 'purple' : 'blue';
  return (
    <>
      <Tag color={tagColor} style={{ fontSize: 9, lineHeight: '14px', padding: '0 3px', margin: 0, flexShrink: 0 }}>
        {tag}
      </Tag>
      {trim(display, 28)}
    </>
  );
}
