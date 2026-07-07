/**
 * loop 节点编辑器：loopCount / condition / breakCondition / body。
 */

import { Form, InputNumber, Switch } from 'antd';
import { ConditionInput } from './shared/ConditionInput';
import { NodeIdSelect } from './shared/NodeIdSelect';
import { useFlowStore } from '../store/flowStore';

export interface LoopEditorProps {
  nodeId: string;
}

export function LoopEditor({ nodeId }: LoopEditorProps) {
  const node = useFlowStore((s) => s.nodes[nodeId]);
  const updateNode = useFlowStore((s) => s.updateNode);
  if (!node) return null;

  const hasCondition = !!node.condition;
  const hasBreakCondition = !!node.breakCondition;

  return (
    <Form layout="vertical">
      <Form.Item
        label="循环次数 loopCount"
        help="<0 = 无限循环；=0 = 跳过循环体；>0 = 固定次数"
      >
        <InputNumber
          value={node.loopCount ?? -1}
          onChange={(v) => updateNode(nodeId, { loopCount: (v as number) ?? -1 })}
          style={{ width: 160 }}
        />
      </Form.Item>

      <Form.Item label="body 子节点 ID（必填）" required>
        <NodeIdSelect
          value={node.body}
          onChange={(v) => updateNode(nodeId, { body: v })}
          excludeId={nodeId}
          placeholder="选择循环体节点（一般是 sequence）"
        />
      </Form.Item>

      <Form.Item
        label="前置条件 condition"
        help={hasCondition ? '每次迭代开始前求值，false 退出循环' : '未启用，每次迭代不判断'}
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: hasCondition ? 8 : 0 }}>
          <Switch
            size="small"
            checked={hasCondition}
            onChange={(checked) => updateNode(nodeId, { condition: checked ? 'state:' : '' })}
          />
          <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>
            {hasCondition ? '已启用' : '未启用'}
          </span>
        </div>
        {hasCondition && (
          <ConditionInput
            value={node.condition}
            onChange={(v) => updateNode(nodeId, { condition: v })}
          />
        )}
      </Form.Item>

      <Form.Item
        label="后置条件 breakCondition"
        help={hasBreakCondition ? '每次迭代结束后求值，true 退出循环' : '未启用，每次迭代不判断'}
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: hasBreakCondition ? 8 : 0 }}>
          <Switch
            size="small"
            checked={hasBreakCondition}
            onChange={(checked) => updateNode(nodeId, { breakCondition: checked ? 'state:' : '' })}
          />
          <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>
            {hasBreakCondition ? '已启用' : '未启用'}
          </span>
        </div>
        {hasBreakCondition && (
          <ConditionInput
            value={node.breakCondition}
            onChange={(v) => updateNode(nodeId, { breakCondition: v })}
          />
        )}
      </Form.Item>
    </Form>
  );
}
