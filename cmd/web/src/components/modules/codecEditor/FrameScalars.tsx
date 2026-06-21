/**
 * FrameScalars — frame 物理布局 + 顶层标量编辑器。
 *
 * 字段：version / endianDefault（le|be）/ frame.headerSize / frame.trailerSize /
 *      frame.lengthIncludesHeader / frame.lengthIncludesTrailer。
 * 修改经 setCodecScalar → onEdit 回灌 content。
 */

import { Card, InputNumber, Radio, Space, Typography } from 'antd';
import type { CodecSchema } from '@/types/codec';
import { setCodecScalar, type CodecScalarPath } from './codecEdit';

export interface FrameScalarsProps {
  raw: Record<string, unknown>;
  schema: CodecSchema;
  onEdit: (nextContent: string) => void;
}

export function FrameScalars({ raw, schema, onEdit }: FrameScalarsProps) {
  const frame = schema.frame;

  const editScalar = (path: CodecScalarPath, value: number | string | boolean) => {
    onEdit(setCodecScalar(raw, path, value));
  };

  return (
    <Card size="small" title="帧布局参数" styles={{ body: { padding: 12 } }}>
      <Space size={16} wrap align="center">
        <LabeledNumber
          label="version"
          value={schema.version}
          min={0}
          onChange={(v) => v != null && editScalar('version', v)}
        />
        <div>
          <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 2 }}>
            字节序（endianDefault）
          </Typography.Text>
          <Radio.Group
            size="small"
            value={schema.endianDefault}
            onChange={(e) => editScalar('endianDefault', e.target.value as string)}
          >
            <Radio.Button value="le">le（小端）</Radio.Button>
            <Radio.Button value="be">be（大端）</Radio.Button>
          </Radio.Group>
        </div>
        <LabeledNumber
          label="headerSize"
          value={frame?.headerSize ?? 0}
          min={0}
          onChange={(v) => v != null && editScalar('frame.headerSize', v)}
        />
        <LabeledNumber
          label="trailerSize"
          value={frame?.trailerSize ?? 0}
          min={0}
          onChange={(v) => v != null && editScalar('frame.trailerSize', v)}
        />
        <div>
          <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 2 }}>
            length 字段范围
          </Typography.Text>
          <Space size={8}>
            <Radio.Group
              size="small"
              value={frame?.lengthIncludesHeader ? 'h' : 'no-h'}
              onChange={(e) => editScalar('frame.lengthIncludesHeader', e.target.value === 'h')}
            >
              <Radio.Button value="no-h">不含 header</Radio.Button>
              <Radio.Button value="h">含 header</Radio.Button>
            </Radio.Group>
            <Radio.Group
              size="small"
              value={frame?.lengthIncludesTrailer ? 't' : 'no-t'}
              onChange={(e) => editScalar('frame.lengthIncludesTrailer', e.target.value === 't')}
            >
              <Radio.Button value="no-t">不含 trailer</Radio.Button>
              <Radio.Button value="t">含 trailer</Radio.Button>
            </Radio.Group>
          </Space>
        </div>
      </Space>
    </Card>
  );
}

function LabeledNumber({
  label,
  value,
  min,
  onChange,
}: {
  label: string;
  value: number;
  min?: number;
  onChange: (v: number | null) => void;
}) {
  return (
    <div>
      <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 2 }}>
        {label}
      </Typography.Text>
      <InputNumber size="small" value={value} min={min} onChange={onChange} />
    </div>
  );
}
