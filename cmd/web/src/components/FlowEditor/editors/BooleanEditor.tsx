/**
 * boolean 节点编辑器：condition / trueNext / falseNext。
 */

import { Form } from 'antd';
import { ConditionInput } from './shared/ConditionInput';
import { NodeIdSelect } from './shared/NodeIdSelect';
import { useFlowStore } from '../store/flowStore';

export interface BooleanEditorProps {
  nodeId: string;
}

export function BooleanEditor({ nodeId }: BooleanEditorProps) {
  const node = useFlowStore((s) => s.nodes[nodeId]);
  const updateNode = useFlowStore((s) => s.updateNode);
  if (!node) return null;
  return (
    <Form layout="vertical">
      <Form.Item label="条件 condition（必填）" required>
        <ConditionInput
          value={node.condition}
          onChange={(v) => updateNode(nodeId, { condition: v })}
        />
      </Form.Item>
      <Form.Item label="true 分支">
        <NodeIdSelect
          value={node.trueNext}
          onChange={(v) => updateNode(nodeId, { trueNext: v })}
          excludeId={nodeId}
          placeholder="留空 = 不跳转"
        />
      </Form.Item>
      <Form.Item label="false 分支">
        <NodeIdSelect
          value={node.falseNext}
          onChange={(v) => updateNode(nodeId, { falseNext: v })}
          excludeId={nodeId}
          placeholder="留空 = 不跳转"
        />
      </Form.Item>
    </Form>
  );
}
