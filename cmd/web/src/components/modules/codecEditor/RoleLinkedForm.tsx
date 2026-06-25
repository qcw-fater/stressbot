/**
 * RoleLinkedForm — 选中字段的 role 联动表单。
 *
 * 按 role 显示不同输入：
 *   - route：只读提示（routeKey 模板编辑见同面板 RouteKeyEditor 卡片）。
 *   - flags：命名位编辑器（bits:[{name,bit}]，bit∈[0,size*8) 客户端提示）。
 *   - checksumOut：from 输入（<step>.<output>，指向 pipeline 某步 produces 的产物；管线编辑见同面板 PipelineEditor 卡片）。
 *   - value：source.kind 下拉（v1 仅 const/route 可选；state/counter/timestamp 置灰标 v1.1）+ const→value 数字、route→key 文本。
 *   - errorCode：提示（绑定后启用服务端错误码识别与 errors.json）。
 *   - length / reserved：无额外配置。
 *
 * 修改经 updateHeaderField → onEdit 回灌 content。
 */

import { InputNumber, Select, Space, Tag, Typography } from 'antd';
import type { CodecSchema, Field, FlagBit, ValueSource } from '@/types/codec';
import { VALUE_SOURCE_KINDS_SUPPORTED } from '@/types/codec';
import { updateHeaderField } from './codecEdit';

export interface RoleLinkedFormProps {
  raw: Record<string, unknown>;
  schema: CodecSchema;
  /** 选中的字段在 schema.header 中的 index。 */
  fieldIndex: number;
  field: Field;
  onEdit: (nextContent: string) => void;
}

export function RoleLinkedForm({ schema, fieldIndex, field, raw, onEdit }: RoleLinkedFormProps) {
  const patch = (p: Partial<Field>) => onEdit(updateHeaderField(raw, fieldIndex, p));
  const bits: FlagBit[] = Array.isArray(field.bits) ? field.bits : [];
  const source: ValueSource =
    (field.source as ValueSource | undefined) ?? ({ kind: 'const' } as ValueSource);
  const maxBit = Math.max(0, (field.size ?? 0) * 8);

  const fieldName = field.name || '未命名';
  return (
    <div className="field-detail-inline">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8, marginBottom: 8, flexWrap: 'wrap' }}>
        <div>
          <Typography.Text className="pce-bench-title">字段详情</Typography.Text>
          <Typography.Text className="pce-bench-meta">{fieldName}</Typography.Text>
        </div>
        <Tag>{field.role}</Tag>
      </div>

      {/* 字节序统一在帧布局的「默认字节序」(endianDefault) 选一次，覆盖所有字段。
          个别字段需不同字节序属罕见例外（混合字节序协议），切源码手编 field.endian。 */}

      {field.role === 'route' && (
        <Typography.Text type="secondary" className="field-inspector-text">
          这个字段参与路由键模板占位（<code>{schema.routeKeyTemplate ?? ''}</code>）。模板在下方 ROUTE KEY 区域编辑。
        </Typography.Text>
      )}

      {field.role === 'flags' && (
        <FlagsEditor bits={bits} maxBit={maxBit} onChange={(next) => patch({ bits: next })} />
      )}

      {field.role === 'checksumOut' && (
        <Space direction="vertical" size={4}>
          <Typography.Text type="secondary" className="field-inspector-text">
            写入某个 pipeline step 的输出，格式 <code>{'<step>.<output>'}</code>。
          </Typography.Text>
          <input
            className="flet-input"
            value={field.from ?? ''}
            placeholder="例如 compress.bcc"
            onChange={(e) => patch({ from: e.target.value })}
          />
        </Space>
      )}

      {field.role === 'value' && <ValueSourceEditor source={source} onChange={(next) => patch({ source: next })} />}

      {field.role === 'errorCode' && (
        <Typography.Text type="secondary" className="field-inspector-text">
          这个字段用于识别服务端错误码，并从共享错误码映射里显示中文文案。
        </Typography.Text>
      )}

      {field.role === 'length' && (
        <Typography.Text type="secondary" className="field-inspector-text">
          这个字段表示消息长度。计数是否包含 header / trailer 由上方 length 计数范围决定。
        </Typography.Text>
      )}

      {field.role === 'reserved' && (
        <Typography.Text type="secondary" className="field-inspector-text">保留字段，无额外配置。</Typography.Text>
      )}
    </div>
  );
}

// ─── flags 命名位编辑器 ─────────────────────────────────────────────

function FlagsEditor({
  bits,
  maxBit,
  onChange,
}: {
  bits: FlagBit[];
  maxBit: number;
  onChange: (next: FlagBit[]) => void;
}) {
  const update = (i: number, p: Partial<FlagBit>) => {
    const next = bits.map((b, idx) => (idx === i ? { ...b, ...p } : b));
    onChange(next);
  };
  return (
    <Space direction="vertical" size={6} style={{ width: '100%' }}>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        命名位（bit 范围 0–{Math.max(0, maxBit - 1)}，即 size×8 = {maxBit} 位）
      </Typography.Text>
      {bits.map((b, i) => (
        <Space key={i} size={6} align="center">
          <input
            className="flet-input"
            style={{ width: 140 }}
            value={b.name ?? ''}
            placeholder="位名"
            onChange={(e) => update(i, { name: e.target.value })}
          />
          <InputNumber
            size="small"
            min={0}
            max={Math.max(0, maxBit - 1)}
            value={b.bit}
            style={{ width: 70 }}
            onChange={(v) => update(i, { bit: typeof v === 'number' ? v : 0 })}
          />
          <a
            style={{ fontSize: 12 }}
            onClick={() => onChange(bits.filter((_, idx) => idx !== i))}
          >
            删除
          </a>
        </Space>
      ))}
      <a style={{ fontSize: 12 }} onClick={() => onChange([...bits, { name: '', bit: 0 }])}>
        + 添加命名位
      </a>
    </Space>
  );
}

// ─── value source 编辑器 ───────────────────────────────────────────

function ValueSourceEditor({
  source,
  onChange,
}: {
  source: ValueSource;
  onChange: (next: ValueSource) => void;
}) {
  const kindOptions = Object.keys(VALUE_SOURCE_KINDS_SUPPORTED).map((k) => ({
    value: k,
    label: VALUE_SOURCE_KINDS_SUPPORTED[k] ? k : `${k}（v1.1）`,
    disabled: !VALUE_SOURCE_KINDS_SUPPORTED[k],
  }));

  return (
    <Space direction="vertical" size={6} style={{ width: '100%' }}>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        source.kind：v1 仅支持 const / route；其余选项留待 v1.1。
      </Typography.Text>
      <Select
        size="small"
        value={source.kind}
        style={{ width: 180 }}
        options={kindOptions}
        onChange={(v) => onChange({ kind: v } as ValueSource)}
      />
      {source.kind === 'const' && (
        <Space size={6} align="center">
          <Typography.Text style={{ fontSize: 12 }}>value</Typography.Text>
          <InputNumber
            size="small"
            value={source.value}
            onChange={(v) => onChange({ ...source, value: typeof v === 'number' ? v : 0 })}
          />
        </Space>
      )}
      {source.kind === 'route' && (
        <Space size={6} align="center">
          <Typography.Text style={{ fontSize: 12 }}>key</Typography.Text>
          <input
            className="flet-input"
            style={{ width: 160 }}
            value={source.key ?? ''}
            placeholder="route 中的字段名"
            onChange={(e) => onChange({ ...source, key: e.target.value })}
          />
        </Space>
      )}
    </Space>
  );
}
