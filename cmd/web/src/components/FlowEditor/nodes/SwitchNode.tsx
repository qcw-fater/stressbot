/**
 * switch 节点：左入 + 右出（每 case 一个 handle + 右下 case-add 拖线新增）+ 底部 default handle。
 *
 * - 右侧 `case-${i}`：命中分支；`case-add`（虚线）：拖线到目标 → 追加 case。
 * - 底部 `default`：默认分支。default 目标也同步在节点底部页脚显示。
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
        minWidth={240}
        description={node.description}
      >
        <div className="row-list">
          {cases.map((c, i) => {
            const { display, tag } = formatCondition(c.condition ?? '');
            const title = `${tag}:${display}`;
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
                  <span className="row-name">{display ? trim(display, 20) : '未配置条件'}</span>
                  <span className="row-tail">{c.next || '未选择'}</span>
                </div>
              </Tooltip>
            );
          })}
          {/* 拖线续接 case：从右侧虚线 case-add handle 拖到目标节点 → 追加 case */}
          <Tooltip title="从右侧虚线 handle 拖线到目标节点 → 追加 case" mouseEnterDelay={0.4}>
            <div className="row-item row-item-add">
              <span className="row-name" style={{ color: 'var(--text-tertiary)', fontStyle: 'italic' }}>
                + 拖线续接…
              </span>
            </div>
          </Tooltip>
        </div>
        {/* default 分支页脚：目标节点 + 底部 handle */}
        <Tooltip
          title={node.defaultNext ? `default → ${node.defaultNext}` : '从底部 handle 拖线到目标 → 设置默认分支'}
          mouseEnterDelay={0.4}
        >
          <div
            className="row-item"
            style={{ borderTop: '1px dashed var(--border-color, rgba(0,0,0,0.12))', marginTop: 3, paddingTop: 3 }}
          >
            <span className="row-index">↳</span>
            <span
              className="row-name"
              style={{
                color: node.defaultNext ? 'var(--text-secondary)' : 'var(--text-tertiary)',
                fontStyle: node.defaultNext ? 'normal' : 'italic',
                fontSize: 10,
              }}
            >
              default
            </span>
            <span className="row-tail" style={{ fontSize: 10 }}>
              {node.defaultNext || '未选择'}
            </span>
          </div>
        </Tooltip>
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
      {/* case-add：拖线到目标 → 追加 case（condition 默认 state:，进编辑面板再填） */}
      <Tooltip title="拖线到目标节点 → 追加 case（默认条件 state:，可在编辑面板修改）" mouseEnterDelay={0.4}>
        <Handle
          type="source"
          position={Position.Right}
          id="case-add"
          className="handle-add"
          style={{ top: HEADER_OFFSET + cases.length * ROW_HEIGHT }}
        />
      </Tooltip>
      {/* default：底部 handle */}
      <Handle type="source" position={Position.Bottom} id="default" />
    </>
  );
}

