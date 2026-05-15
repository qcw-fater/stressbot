/**
 * wait 节点编辑器：固定等待 / 随机等待。
 */

import { Form, InputNumber, Radio, Space } from 'antd';
import { useFlowStore } from '../store/flowStore';

export interface WaitEditorProps {
  nodeId: string;
}

type WaitMode = 'fixed' | 'random';

export function WaitEditor({ nodeId }: WaitEditorProps) {
  const node = useFlowStore((s) => s.nodes[nodeId]);
  const updateNode = useFlowStore((s) => s.updateNode);
  if (!node) return null;

  const hasRandom = typeof node.waitMin === 'number' || typeof node.waitMax === 'number';
  const mode: WaitMode = hasRandom ? 'random' : 'fixed';

  const switchMode = (newMode: WaitMode) => {
    if (newMode === 'random') {
      updateNode(nodeId, {
        waitMs: undefined,
        waitMin: node.waitMin ?? 500,
        waitMax: node.waitMax ?? 2000,
      });
    } else {
      updateNode(nodeId, {
        waitMin: undefined,
        waitMax: undefined,
        waitMs: node.waitMs ?? 1000,
      });
    }
  };

  return (
    <Form layout="vertical">
      <Form.Item label="等待模式">
        <Radio.Group value={mode} onChange={(e) => switchMode(e.target.value)} optionType="button" buttonStyle="solid" size="small">
          <Radio.Button value="fixed">固定</Radio.Button>
          <Radio.Button value="random">随机</Radio.Button>
        </Radio.Group>
      </Form.Item>

      {mode === 'fixed' ? (
        <Form.Item label="等待时长">
          <Space.Compact>
            <InputNumber
              min={1}
              step={100}
              value={node.waitMs || undefined}
              onChange={(v) => updateNode(nodeId, { waitMs: (v as number) ?? undefined })}
              style={{ width: 160 }}
            />
            <InputNumber style={{ width: 50 }} value="ms" readOnly variant="borderless" />
          </Space.Compact>
        </Form.Item>
      ) : (
        <Form.Item label="随机区间">
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <Space.Compact>
              <InputNumber
                min={1}
                step={100}
                value={node.waitMin ?? undefined}
                onChange={(v) => updateNode(nodeId, { waitMin: (v as number) ?? undefined })}
                style={{ width: 120 }}
                placeholder="最小"
              />
              <InputNumber style={{ width: 40 }} value="ms" readOnly variant="borderless" />
            </Space.Compact>
            <span style={{ color: 'var(--text-tertiary)' }}>~</span>
            <Space.Compact>
              <InputNumber
                min={1}
                step={100}
                value={node.waitMax ?? undefined}
                onChange={(v) => updateNode(nodeId, { waitMax: (v as number) ?? undefined })}
                style={{ width: 120 }}
                placeholder="最大"
              />
              <InputNumber style={{ width: 40 }} value="ms" readOnly variant="borderless" />
            </Space.Compact>
          </div>
        </Form.Item>
      )}
    </Form>
  );
}
