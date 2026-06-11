/**
 * loop 节点：胶囊形（强调"循环"语义），左入 + 右出（body 唯一）。
 *
 * body 跑完后由外层 sequence 接管，loop 自身不出 exit。
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

/** 条件文本行：前缀 Tag + 截断文本 */
function ConditionLine({ label, condition }: { label: string; condition: string }) {
  const { display, tag } = formatCondition(condition);
  const tagColor = tag === 'lua' ? 'purple' : 'blue';
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 3 }}>
      <span style={{ color: 'var(--text-tertiary)', flexShrink: 0 }}>{label}</span>
      <Tag color={tagColor} style={{ fontSize: 9, lineHeight: '14px', padding: '0 3px', margin: 0, flexShrink: 0 }}>
        {tag}
      </Tag>
      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {trim(display, 22)}
      </span>
    </div>
  );
}

export function LoopNode({ id, data, selected }: NodeProps) {
  const { node } = data as unknown as NodeData;
  const count = node.loopCount;
  const countText =
    typeof count === 'number' && count <= 0 ? '∞' : typeof count === 'number' ? `×${count}` : '×1';

  const hasCond = !!node.condition;
  const hasBreak = !!node.breakCondition;
  const normalizedCondition = hasCond ? formatCondition(node.condition!) : null;
  const normalizedBreakCondition = hasBreak ? formatCondition(node.breakCondition!) : null;
  const conditionTitle =
    (normalizedCondition ? `前置: ${normalizedCondition.tag}${normalizedCondition.display}` : '') +
    (normalizedCondition && normalizedBreakCondition ? ' / ' : '') +
    (normalizedBreakCondition ? `后置: ${normalizedBreakCondition.tag}${normalizedBreakCondition.display}` : '');

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
          <div style={{ fontFamily: 'monospace', fontSize: 10 }}>
            <span style={{ color: 'var(--text-tertiary)' }}>body:</span> {node.body}
          </div>
        )}
        {(hasCond || hasBreak) && (
          <Tooltip title={conditionTitle} mouseEnterDelay={0.4}>
            <div style={{ fontFamily: 'monospace', fontSize: 10, marginTop: 2 }}>
              {hasCond && <ConditionLine label="前:" condition={node.condition!} />}
              {hasBreak && <ConditionLine label="后:" condition={node.breakCondition!} />}
            </div>
          </Tooltip>
        )}
      </NodeShell>
      <Handle type="source" position={Position.Right} id="body" />
    </>
  );
}
