/**
 * FrameScalars — frame 物理布局 + 顶层标量编辑器。
 *
 * 字段：version / endianDefault（le|be）/ frame.headerSize / frame.trailerSize /
 *      frame.lengthIncludesHeader / frame.lengthIncludesTrailer。
 * 修改经 setCodecScalar → onEdit 回灌 content。
 */

import { InputNumber, Radio, Space, Tooltip, Typography } from 'antd';
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
    <div className="frame-controls">
      <LabeledNumber
        label="配置格式版本"
        tooltip="用于后端识别 codec.json 结构；不是工具版本，也不是业务协议字段。"
        value={schema.version}
        min={0}
        onChange={(v) => v != null && editScalar('version', v)}
      />
      <div className="frame-control-item">
        <Typography.Text type="secondary" className="frame-control-label">
          默认字节序
        </Typography.Text>
        <Radio.Group
          size="small"
          value={schema.endianDefault}
          onChange={(e) => editScalar('endianDefault', e.target.value as string)}
        >
          <Radio.Button value="le">le 小端</Radio.Button>
          <Radio.Button value="be">be 大端</Radio.Button>
        </Radio.Group>
      </div>
      <LabeledNumber
        label="header bytes"
        value={frame?.headerSize ?? 0}
        min={0}
        onChange={(v) => v != null && editScalar('frame.headerSize', v)}
      />
      <LabeledNumber
        label="trailer bytes"
        value={frame?.trailerSize ?? 0}
        min={0}
        onChange={(v) => v != null && editScalar('frame.trailerSize', v)}
      />
      <div className="frame-control-item frame-length-scope">
        <Typography.Text type="secondary" className="frame-control-label">
          length 计数范围
        </Typography.Text>
        <Space size={8} wrap>
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
    </div>
  );
}

function LabeledNumber({
  label,
  tooltip,
  value,
  min,
  onChange,
}: {
  label: string;
  tooltip?: string;
  value: number;
  min?: number;
  onChange: (v: number | null) => void;
}) {
  const labelNode = (
    <Typography.Text type="secondary" className="frame-control-label">
      {label}
    </Typography.Text>
  );
  return (
    <div className="frame-control-item">
      {tooltip ? <Tooltip title={tooltip}>{labelNode}</Tooltip> : labelNode}
      <InputNumber size="small" value={value} min={min} onChange={onChange} />
    </div>
  );
}
