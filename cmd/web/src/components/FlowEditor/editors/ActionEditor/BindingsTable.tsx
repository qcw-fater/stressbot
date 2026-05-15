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
import type { ProtoField } from '@/types/proto';
import { useState } from 'react';
import { ProtoPathInput } from './ProtoPathInput';
import { StateKeyInput } from './StateKeyInput';
import { BindingTypeForm } from './BindingTypeForm';
import { protoRegistry } from '../../proto/ProtoRegistry';

export interface BindingsTableProps {
  /** 当前绑定列表所属的 message 全名（用于字段下拉） */
  messageFullName?: string;
  value?: FieldBind[];
  onChange?: (v: FieldBind[]) => void;
}

const TYPE_GROUPS: { label: string; types: BindingType[] }[] = [
  { label: '固定值', types: ['fixed'] },
  { label: 'state 取值', types: ['state', 'stateFirst', 'stateRandom', 'stateRandomN', 'stateMapKey', 'stateMapValue', 'listSize'] },
  { label: '随机', types: ['randomPick', 'randomPickN', 'randomPickMap', 'randomInt', 'randomFloat', 'randomBool', 'randomString', 'randomExclude'] },
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
  randomPick: '从 values 列表中随机选一个',
  randomPickN: '从 values 列表中随机选 N 个（返回列表）',
  randomPickMap: '按 keySource 查表，从匹配的 values 中随机选一个',
  randomInt: '随机整数，范围 [min, max]',
  randomFloat: '随机浮点数，范围 [min, max]，可设精度',
  randomBool: '随机布尔值',
  randomString: '随机字符串，指定长度',
  randomExclude: '从 values 中排除指定 state key 后随机选一个',
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

  const items: CollapseProps['items'] = list.map((b, i) => {
    const fieldComment = messageFullName && b.field
      ? resolveFieldPath(messageFullName, b.field)?.comment
      : undefined;
    return {
    key: String(i),
    label: (
      <Space>
        {fieldComment ? (
          <Tooltip title={fieldComment} mouseEnterDelay={0.4}>
            <code>{b.field || '(未指定字段)'}</code>
          </Tooltip>
        ) : (
          <code>{b.field || '(未指定字段)'}</code>
        )}
        <Tooltip title={BINDING_TYPE_DESC[b.type]} mouseEnterDelay={0.4}>
          <Tag color="blue">{b.type}</Tag>
        </Tooltip>
        {b.required && <Tooltip title="缺失时报错" mouseEnterDelay={0.4}><Tag color="red">required</Tag></Tooltip>}
        {b.optional && <Tooltip title="缺失时跳过" mouseEnterDelay={0.4}><Tag color="default">optional</Tag></Tooltip>}
        {b.wrap && <Tooltip title="单值包成 [v]" mouseEnterDelay={0.4}><Tag color="purple">wrap</Tag></Tooltip>}
        {b.storeAs && <Tooltip title={`存入 state：${b.storeAs}`} mouseEnterDelay={0.4}><Tag color="green">→ {b.storeAs}</Tag></Tooltip>}
        {b.condition && <Tooltip title="有条件绑定" mouseEnterDelay={0.4}><Tag color="orange">condition</Tag></Tooltip>}
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
  }});

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
    </Space>
  );
}

const CONDITION_OPS = ['eq', 'neq', 'gt', 'gte', 'lt', 'lte', 'contains', 'in', 'notNil', 'isNil'] as const;
const CONDITION_OP_META: Record<string, { label: string; desc: string }> = {
  eq:       { label: '= 等于',       desc: '字段值等于指定值' },
  neq:      { label: '≠ 不等于',     desc: '字段值不等于指定值' },
  gt:       { label: '> 大于',       desc: '字段值大于指定值' },
  gte:      { label: '≥ 大于等于',   desc: '字段值大于或等于指定值' },
  lt:       { label: '< 小于',       desc: '字段值小于指定值' },
  lte:      { label: '≤ 小于等于',   desc: '字段值小于或等于指定值' },
  contains: { label: '包含',         desc: '字符串包含指定子串' },
  in:       { label: '在列表中',     desc: '字段值在指定列表中' },
  notNil:   { label: '不为空',       desc: '无需填 value' },
  isNil:    { label: '为空',         desc: '无需填 value' },
};

const COND_LABEL: React.CSSProperties = { fontSize: 12, color: 'var(--text-tertiary)' };

function ConditionEditor({ value, onChange }: { value?: ConditionDef; onChange: (c?: ConditionDef) => void }) {
  const [condProto, setCondProto] = useState<string | undefined>(undefined);

  if (!value) {
    return (
      <Tooltip title="运行时先检查条件，不满足则跳过此 binding" mouseEnterDelay={0.4}>
        <a onClick={() => onChange({ source: '', op: 'eq' })} style={{ fontSize: 11 }}>
          + 添加条件
        </a>
      </Tooltip>
    );
  }

  const update = (patch: Partial<ConditionDef>) => onChange({ ...value, ...patch });
  const noRhs = value.op === 'notNil' || value.op === 'isNil';

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, width: '100%' }}>
      <Tooltip title="运行时先检查条件，不满足则跳过此 binding" mouseEnterDelay={0.4}>
        <span style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>条件（不满足时跳过本绑定）</span>
      </Tooltip>
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
          options={CONDITION_OPS.map((o) => ({ value: o, label: CONDITION_OP_META[o]?.label ?? o, title: '' }))}
          optionRender={(opt) => {
            const m = CONDITION_OP_META[opt.value as string];
            return (
              <Tooltip title={m?.desc} mouseEnterDelay={0.3} placement="right">
                <div>{opt.label}</div>
              </Tooltip>
            );
          }}
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

function resolveFieldPath(messageFullName: string, path: string): ProtoField | undefined {
  const segments = path
    .split(/[\.\[]/)
    .map(s => s.replace(/\]$/, ''))
    .filter(s => s && !/^\d+$/.test(s));
  let currentMsg = messageFullName;
  let field: ProtoField | undefined;
  for (const seg of segments) {
    field = protoRegistry.resolveField(currentMsg, seg);
    if (!field) return undefined;
    if (field.messageName) {
      currentMsg = field.messageName;
    }
  }
  return field;
}
