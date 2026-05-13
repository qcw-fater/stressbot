/**
 * 字段绑定列表编辑器（FieldBind[]）。
 *
 * 每行：field（proto 字段下拉）+ type（17 种）+ 类型相关参数（BindingTypeForm）+ 操作
 * 支持递归（nested / nestedList 通过 renderChildren 回调）。
 */

import { Button, Collapse, Input, Select, Space, Switch, Tag, Tooltip } from 'antd';
import { ArrowUpOutlined, ArrowDownOutlined, DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import type { CollapseProps } from 'antd';
import type { BindingType, ConditionDef, FieldBind } from '@/types/action';
import { useState } from 'react';
import { ProtoPathInput } from './ProtoPathInput';
import { StateKeyInput } from './StateKeyInput';
import { BindingTypeForm } from './BindingTypeForm';
import { BindingPreview } from './BindingPreview';

export interface BindingsTableProps {
  /** 当前绑定列表所属的 message 全名（用于字段下拉） */
  messageFullName?: string;
  value?: FieldBind[];
  onChange?: (v: FieldBind[]) => void;
}

const TYPE_GROUPS: { label: string; types: BindingType[] }[] = [
  { label: '固定值', types: ['fixed'] },
  { label: 'state 取值', types: ['state', 'stateFirst', 'stateRandom', 'stateRandomN', 'stateMapKey', 'stateMapValue', 'listSize'] },
  { label: '随机', types: ['randomPick', 'randomPickN', 'randomPickMap', 'randomInt', 'randomBool', 'randomString', 'randomExclude'] },
];

const BINDING_TYPE_DESC: Record<BindingType, string> = {
  fixed: '固定值，直接填入 value 字段',
  state: '从 state 中取出 key 对应的单个值',
  stateFirst: '取 state list 的第一个元素',
  stateRandom: '从 state list 中随机取一个元素',
  stateRandomN: '从 state list 中随机取 N 个元素（返回列表）',
  stateMapKey: '从 state map 中随机取一个 key',
  stateMapValue: '从 state map 中随机取一个 value',
  listSize: '取 state list 的长度',
  randomPick: '从 choices 列表中随机选一个',
  randomPickN: '从 choices 列表中随机选 N 个（返回列表）',
  randomPickMap: '从 map 的 value 列表中随机选一个',
  randomInt: '随机整数，范围 [min, max]',
  randomBool: '随机布尔值，可设 true 概率',
  randomString: '随机字符串，指定长度和字符集',
  randomExclude: '从 choices 中排除 exclude 后随机选一个',
};

const TYPE_OPTIONS = TYPE_GROUPS.map((g) => ({
  label: g.label,
  options: g.types.map((t) => ({ value: t, label: t })),
}));

export function BindingsTable({ messageFullName, value, onChange }: BindingsTableProps) {
  const list = value ?? [];
  const set = (next: FieldBind[]) => onChange?.(next);

  const moveItem = (from: number, to: number) => {
    if (to < 0 || to >= list.length) return;
    const arr = [...list];
    [arr[from], arr[to]] = [arr[to], arr[from]];
    set(arr);
  };

  const items: CollapseProps['items'] = list.map((b, i) => ({
    key: String(i),
    label: (
      <Space>
        <code>{b.field || '(未指定字段)'}</code>
        <Tag color="blue">{b.type}</Tag>
        {b.required && <Tag color="red">required</Tag>}
        {b.optional && <Tag color="default">optional</Tag>}
        {b.wrap && <Tag color="purple">wrap</Tag>}
        {b.storeAs && <Tag color="green">→ {b.storeAs}</Tag>}
	        {b.condition && <Tag color="orange">condition</Tag>}
      </Space>
    ),
    extra: (
      <Space size={2} onClick={(e) => e.stopPropagation()}>
        <Button
          size="small"
          icon={<ArrowUpOutlined />}
          disabled={i === 0}
          onClick={() => moveItem(i, i - 1)}
        />
        <Button
          size="small"
          icon={<ArrowDownOutlined />}
          disabled={i === list.length - 1}
          onClick={() => moveItem(i, i + 1)}
        />
        <Button
          size="small"
          danger
          icon={<DeleteOutlined />}
          onClick={() => set(list.filter((_, j) => j !== i))}
        />
      </Space>
    ),
    children: (
      <BindingRow
        binding={b}
        allBindings={list}
        messageFullName={messageFullName}
        onChange={(next) => {
          const arr = [...list];
          arr[i] = next;
          set(arr);
        }}
      />
    ),
  }));

  return (
    <div style={{ paddingLeft: 0 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
        <strong>bindings ({list.length})</strong>
        <Button
          size="small"
          icon={<PlusOutlined />}
          onClick={() => set([...list, { type: 'fixed', field: '' }])}
        >
          添加绑定
        </Button>
      </div>
      <Collapse items={items} size="small" />
    </div>
  );
}

function BindingRow({
  binding,
  allBindings,
  messageFullName,
  onChange,
}: {
  binding: FieldBind;
  allBindings: FieldBind[];
  messageFullName?: string;
  onChange: (b: FieldBind) => void;
}) {
  const set = (partial: Partial<FieldBind>) => onChange({ ...binding, ...partial });
  const [showPreview, setShowPreview] = useState(false);
  return (
    <Space direction="vertical" style={{ width: '100%' }} size="middle">
      <Space wrap style={{ width: '100%' }}>
        <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>field</span>
        <div style={{ width: 320 }}>
          <ProtoPathInput
            messageFullName={messageFullName}
            value={binding.field}
            onChange={(v) => set({ field: v || undefined })}
            placeholder="填写或选择字段 (如 a.b[0])"
          />
        </div>
        <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>type</span>
        <Select
          value={binding.type}
          onChange={(v) => set({ type: v as BindingType })}
          options={TYPE_OPTIONS.map((g) => ({
            label: g.label,
            options: g.options.map((o) => ({
              value: o.value,
              label: (
                <Tooltip title={BINDING_TYPE_DESC[o.value]} placement="right">
                  <span>{o.label}</span>
                </Tooltip>
              ),
            })),
          }))}
          style={{ width: 180 }}
        />
      </Space>
      <BindingTypeForm
        binding={binding}
        currentBindings={allBindings}
        onChange={onChange}
      />
      <Space wrap>
        <Tooltip title="缺失时报错（隐式必需的类型默认 true，可通过 optional 反转）">
          <Space size={4}>
            required:
            <Switch
              size="small"
              checked={!!binding.required}
              onChange={(v) => set({ required: v || undefined })}
            />
          </Space>
        </Tooltip>
        <Tooltip title="对隐式必需类型反转 → 缺失时跳过本绑定">
          <Space size={4}>
            optional:
            <Switch
              size="small"
              checked={!!binding.optional}
              onChange={(v) => set({ optional: v || undefined })}
            />
          </Space>
        </Tooltip>
        <Tooltip title="repeated 字段赋单值时包成 [v]">
          <Space size={4}>
            wrap:
            <Switch
              size="small"
              checked={!!binding.wrap}
              onChange={(v) => set({ wrap: v || undefined })}
            />
          </Space>
        </Tooltip>
        <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>storeAs:</span>
        <Input
          placeholder="state key"
          value={binding.storeAs ?? ''}
          onChange={(e) => set({ storeAs: e.target.value || undefined })}
          size="small"
          style={{ width: 150 }}
        />
      </Space>
      <ConditionEditor value={binding.condition} onChange={(c) => set({ condition: c })} />
      <div style={{ borderTop: '1px dashed var(--divider-bg)', paddingTop: 6 }}>
        <a onClick={() => setShowPreview(!showPreview)} style={{ fontSize: 11 }}>
          {showPreview ? '隐藏预览' : '显示预览'}
        </a>
        {showPreview && <BindingPreview binding={binding} messageFullName={messageFullName} />}
      </div>
    </Space>
  );
}

const CONDITION_OPS = ['eq', 'neq', 'gt', 'gte', 'lt', 'lte', 'contains', 'in', 'notNil', 'isNil'] as const;

const COND_LABEL: React.CSSProperties = { fontSize: 12, color: 'var(--text-tertiary)' };

function ConditionEditor({ value, onChange }: { value?: ConditionDef; onChange: (c?: ConditionDef) => void }) {
  const [condProto, setCondProto] = useState<string | undefined>(undefined);

  if (!value) {
    return (
      <a onClick={() => onChange({ source: '', op: 'eq' })} style={{ fontSize: 11 }}>
        + 添加条件
      </a>
    );
  }

  const update = (patch: Partial<ConditionDef>) => onChange({ ...value, ...patch });
  const noRhs = value.op === 'notNil' || value.op === 'isNil';

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, width: '100%' }}>
      <span style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>条件（不满足时跳过本绑定）</span>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <span style={COND_LABEL}>source</span>
        <StateKeyInput
          value={value.source}
          onChange={(v) => update({ source: v })}
          onProtoResolved={setCondProto}
          placeholder="state key"
          style={{ flex: 1 }}
        />
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <span style={COND_LABEL}>path</span>
        <ProtoPathInput
          messageFullName={condProto}
          value={value.path}
          onChange={(v) => update({ path: v || undefined })}
          placeholder="可选"
          style={{ flex: 1 }}
        />
        <Select
          value={value.op || 'eq'}
          onChange={(v) => update({ op: v })}
          options={CONDITION_OPS.map((o) => ({ value: o, label: o }))}
          style={{ width: 90 }}
          size="small"
        />
        {!noRhs && (
          <Input
            placeholder="value"
            value={value.valueSource ?? (value.value !== undefined ? String(value.value) : '')}
            onChange={(e) => {
              const raw = e.target.value;
              if (!raw) return update({ value: undefined, valueSource: undefined });
              try { update({ value: JSON.parse(raw), valueSource: undefined }); }
              catch { update({ value: raw, valueSource: undefined }); }
            }}
            style={{ width: 120 }}
            size="small"
          />
        )}
        <a onClick={() => onChange(undefined)} style={{ color: 'var(--color-error)', fontSize: 11, flexShrink: 0 }}>
          删除条件
        </a>
      </div>
    </div>
  );
}
