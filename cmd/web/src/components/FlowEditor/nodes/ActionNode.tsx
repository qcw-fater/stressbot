/**
 * action 节点：左入 + 右侧 listen 出口 + 底部 onError handler 出口。
 *
 * action 自身在父 sequence 内顺序执行，没有 next 字段：
 *   - listen handle：拖线到 listenCard，追加 listenRefs
 *   - error handle：拖线到普通节点，设置 onError.handler（调用边，不写入 next）
 *
 * 极简化布局：
 *   第一行：节点 ID（独占）
 *   第二行：pattern 标签 + onError 重点标签 + listen 徽章
 *   详细字段（绑定、store、proto 等）在双击后的编辑面板中查看。
 */

import { Handle, Position, type NodeProps } from '@xyflow/react';
import { Tooltip } from 'antd';
import { NodeShell } from './shared/NodeShell';
import type { FlowNode } from '@/types/flow';
import type { ActionDef } from '@/types/action';
import { PATTERN_DESC } from '../editors/ActionEditor/PatternSelector';
import { buildOnErrorBadges } from './onErrorBadges';

interface NodeData {
  nodeId: string;
  node: FlowNode;
  action?: ActionDef;
}

export function ActionNode({ id, data, selected }: NodeProps) {
  const { node, action } = data as unknown as NodeData;
  const pattern = action?.pattern ?? '?';
  const listens = node.listenRefs?.length ?? 0;
  const onErrorBadges = buildOnErrorBadges(node);

  return (
    <>
      <Handle type="target" position={Position.Left} id="in" />
      <NodeShell
        nodeId={id}
        nodeType="action"
        title={id}
        selected={selected}
        minWidth={150}
        description={node.description}
      >
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, alignItems: 'center' }}>
          <Tooltip title={action ? PATTERN_DESC[action.pattern] : pattern}>
            <span className="pattern-badge" data-pattern={pattern}>
              {pattern}
            </span>
          </Tooltip>
          {onErrorBadges.map((badge) => (
            <Tooltip key={badge.label} title={badge.tooltip}>
              <span className="onerror-badge" data-tone={badge.tone}>{badge.label}</span>
            </Tooltip>
          ))}
          {listens > 0 && (
            <Tooltip title={`注册了 ${listens} 个 listen 监听`}>
              <span className="listen-badge">listen {listens}</span>
            </Tooltip>
          )}
        </div>
      </NodeShell>
      <Handle type="source" position={Position.Right} id="listen" />
      <Handle type="source" position={Position.Bottom} id="error" style={{ background: 'var(--color-error)' }} />
    </>
  );
}
