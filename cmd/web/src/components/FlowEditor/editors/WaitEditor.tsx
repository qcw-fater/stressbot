/**
 * wait 节点编辑器：单个 waitMs。
 */

import { Form, InputNumber } from 'antd';
import { useFlowStore } from '../store/flowStore';

export interface WaitEditorProps {
  nodeId: string;
}

export function WaitEditor({ nodeId }: WaitEditorProps) {
  const node = useFlowStore((s) => s.nodes[nodeId]);
  const updateNode = useFlowStore((s) => s.updateNode);
  if (!node) return null;
  const ms = node.waitMs ?? 0;
  return (
    <Form layout="vertical">
      <Form.Item label="等待时长 waitMs">
        <InputNumber
          min={0}
          step={100}
          value={ms}
          onChange={(v) => updateNode(nodeId, { waitMs: (v as number) ?? 0 })}
          addonAfter="ms"
          style={{ width: 200 }}
        />
        <span style={{ marginLeft: 12, color: 'var(--text-tertiary)' }}>≈ {(ms / 1000).toFixed(2)} 秒</span>
      </Form.Item>
    </Form>
  );
}
