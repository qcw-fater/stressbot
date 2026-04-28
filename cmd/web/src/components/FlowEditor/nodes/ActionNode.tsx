/**
 * action 节点：左入 + 右出（专用于监听 callback）。
 *
 * action 自身在父 sequence 内顺序执行，没有 next 字段，因此右侧 handle 仅承担"注册监听"语义：
 *   - 拖线到 callbackCard → 追加 listenCallbacks
 *   - 拖线到普通节点不会建立任何业务关系（onConnect 中无视）
 *
 * 极简化布局：
 *   第一行：节点 ID（独占）
 *   第二行：pattern 标签 + breakOff 徽章 + listen 徽章
 *   详细字段（绑定、store、proto 等）在双击后的编辑面板中查看。
 */

import { Handle, Position, type NodeProps } from '@xyflow/react';
import { NodeShell } from './shared/NodeShell';
import type { FlowNode } from '@/types/flow';
import type { ActionDef } from '@/types/action';

interface NodeData {
  nodeId: string;
  node: FlowNode;
  action?: ActionDef;
}

export function ActionNode({ id, data, selected }: NodeProps) {
  const { node, action } = data as unknown as NodeData;
  const pattern = action?.pattern ?? '?';
  const listens = node.listenCallbacks?.length ?? 0;

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
          <span className="pattern-badge" data-pattern={pattern}>
            {pattern}
          </span>
          {node.breakOff && <span className="breakoff-badge">breakOff</span>}
          {listens > 0 && <span className="listen-badge">listen {listens}</span>}
        </div>
      </NodeShell>
      <Handle type="source" position={Position.Right} id="listen" />
    </>
  );
}
