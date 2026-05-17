/**
 * delayMs 三态输入：>0 使用此值 / 0 使用默认 / -1 禁用。
 *
 * 设计文档 §5.2：仅 action / boolean 节点支持。
 */

import { InputNumber, Radio, Space } from 'antd';

export interface DelayInputProps {
  value?: number;
  onChange?: (v: number | undefined) => void;
}

type Mode = 'default' | 'custom' | 'disabled';

function modeOf(v: number | undefined): Mode {
  if (v === undefined || v === 0) return 'default';
  if (v < 0) return 'disabled';
  return 'custom';
}

export function DelayInput({ value, onChange }: DelayInputProps) {
  const mode = modeOf(value);
  const set = (next: Mode) => {
    if (next === 'default') onChange?.(0);
    else if (next === 'disabled') onChange?.(-1);
    else if (next === 'custom') onChange?.(typeof value === 'number' && value > 0 ? value : 1000);
  };

  return (
    <Space>
      <Radio.Group value={mode} onChange={(e) => set(e.target.value)} buttonStyle="solid" size="small">
        <Radio.Button value="default">使用默认</Radio.Button>
        <Radio.Button value="custom">自定义</Radio.Button>
        <Radio.Button value="disabled">禁用</Radio.Button>
      </Radio.Group>
      {mode === 'custom' && (
        <Space.Compact>
          <InputNumber
            min={1}
            max={600000}
            step={100}
            value={typeof value === 'number' && value > 0 ? value : 1000}
            onChange={(v) => onChange?.((v as number) ?? 1000)}
            style={{ width: 120 }}
          />
          <span style={{ display: 'flex', alignItems: 'center', padding: '0 8px', background: 'var(--container-bg)', border: '1px solid var(--border-color)', borderRadius: '0 6px 6px 0', fontSize: 13 }}>ms</span>
        </Space.Compact>
      )}
      {mode === 'default' && <span style={{ color: 'var(--text-tertiary)', fontSize: 12 }}>= TaskFlow.defaultDelayMs</span>}
      {mode === 'disabled' && <span style={{ color: 'var(--text-tertiary)', fontSize: 12 }}>立即执行下一节点</span>}
    </Space>
  );
}
