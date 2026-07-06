/**
 * switch 节点：左入 + 右出（每个 case / default 各一条分支）。
 */

import { Handle, Position, type NodeProps } from '@xyflow/react';
import { Tag, Tooltip } from 'antd';
import { NodeShell } from './shared/NodeShell';
import type { FlowNode } from '@/types/flow';

interface NodeData {
  nodeId: string;
  node: FlowNode;
}

const ROW_HEIGHT = 22;
const HEADER_OFFSET = 30;

function trim(s: string, n: number): string {
  return s.length > n ? s.slice(0, n) + '…' : s;
}

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

export function SwitchNode({ id, data, selected }: NodeProps) {
  const { node } = data as unknown as NodeData;
  const cases = node.cases ?? [];

  return (
    <>
      <Handle type="target" position={Position.Left} id="in" />
      <NodeShell
        nodeId={id}
        nodeType="switch"
        title={id}
        subtitle={`switch · ${cases.length}`}
        selected={selected}
        minWidth={280}
        description={node.description}
      >
        <div className="row-list">
          {cases.map((c, i) => {
            const { display, tag } = formatCondition(c.condition ?? '');
            const title = c.description
              ? `${c.description}\n${tag}:${display}`
              : `${tag}:${display}`;
            return (
              <Tooltip key={i} title={title} mouseEnterDelay={0.4}>
                <div className="row-item">
                  <span className="row-index">{i + 1}.</span>
                  <Tag
                    color={tag === 'lua' ? 'purple' : 'blue'}
                    style={{ fontSize: 9, lineHeight: '14px', padding: '0 3px', margin: 0, flexShrink: 0 }}
                  >
                    {tag || 'state'}
                  </Tag>
                  <span className="row-name">{display ? trim(display, 24) : '未配置条件'}</span>
                  <span className="row-tail">{c.next || '未选择'}</span>
                </div>
              </Tooltip>
            );
          })}
          <div className="row-item">
            <span className="row-index">def</span>
            <span className="row-name" style={{ color: 'var(--text-tertiary)' }}>
              默认分支
            </span>
            <span className="row-tail">{node.defaultNext || '未选择'}</span>
          </div>
        </div>
      </NodeShell>
      {cases.map((_, i) => (
        <Handle
          key={i}
          type="source"
          position={Position.Right}
          id={`case-${i}`}
          style={{ top: HEADER_OFFSET + i * ROW_HEIGHT }}
        />
      ))}
      <Handle
        type="source"
        position={Position.Right}
        id="default"
        style={{ top: HEADER_OFFSET + cases.length * ROW_HEIGHT }}
      />
    </>
  );
}
