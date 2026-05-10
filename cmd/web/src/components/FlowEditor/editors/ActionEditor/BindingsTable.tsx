/**
 * 字段绑定列表编辑器（FieldBind[]）。
 *
 * 每行：field（proto 字段下拉）+ type（17 种）+ 类型相关参数（BindingTypeForm）+ 操作
 * 支持递归（nested / nestedList 通过 renderChildren 回调）。
 */

import { Button, Collapse, Space, Switch, Tag, Tooltip } from 'antd';
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import type { CollapseProps } from 'antd';
import type { BindingType, FieldBind } from '@/types/action';
import { Select } from 'antd';
import { ProtoFieldPicker } from './ProtoFieldPicker';
import { BindingTypeForm } from './BindingTypeForm';

export interface BindingsTableProps {
  /** 当前绑定列表所属的 message 全名（用于字段下拉） */
  messageFullName?: string;
  value?: FieldBind[];
  onChange?: (v: FieldBind[]) => void;
  depth?: number;
}

const TYPE_GROUPS: { label: string; types: BindingType[] }[] = [
  { label: '固定值', types: ['fixed'] },
  { label: 'state 取值', types: ['state', 'stateFirst', 'stateRandom', 'stateRandomN', 'stateMapKey', 'stateMapValue', 'listSize'] },
  { label: '随机', types: ['randomPick', 'randomPickN', 'randomPickMap', 'randomInt', 'randomBool', 'randomString', 'randomExclude'] },
  { label: '嵌套', types: ['nested', 'nestedList'] },
];

const TYPE_OPTIONS = TYPE_GROUPS.map((g) => ({
  label: g.label,
  options: g.types.map((t) => ({ value: t, label: t })),
}));

export function BindingsTable({ messageFullName, value, onChange, depth = 0 }: BindingsTableProps) {
  const list = value ?? [];
  const set = (next: FieldBind[]) => onChange?.(next);

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
      </Space>
    ),
    extra: (
      <Button
        size="small"
        danger
        icon={<DeleteOutlined />}
        onClick={(e) => {
          e.stopPropagation();
          set(list.filter((_, j) => j !== i));
        }}
      />
    ),
    children: (
      <BindingRow
        binding={b}
        messageFullName={messageFullName}
        depth={depth}
        onChange={(next) => {
          const arr = [...list];
          arr[i] = next;
          set(arr);
        }}
      />
    ),
  }));

  return (
    <div style={{ paddingLeft: depth > 0 ? 16 : 0, borderLeft: depth > 0 ? '2px solid var(--divider-bg)' : 'none' }}>
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
  messageFullName,
  depth,
  onChange,
}: {
  binding: FieldBind;
  messageFullName?: string;
  depth: number;
  onChange: (b: FieldBind) => void;
}) {
  const set = (partial: Partial<FieldBind>) => onChange({ ...binding, ...partial });
  return (
    <Space direction="vertical" style={{ width: '100%' }} size="middle">
      <Space wrap style={{ width: '100%' }}>
        <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>field</span>
        <div style={{ width: 220 }}>
          <ProtoFieldPicker
            messageFullName={messageFullName}
            value={binding.field}
            onChange={(v) => set({ field: v || undefined })}
          />
        </div>
        <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>type</span>
        <Select
          value={binding.type}
          onChange={(v) => set({ type: v as BindingType })}
          options={TYPE_OPTIONS}
          style={{ width: 180 }}
        />
      </Space>
      <BindingTypeForm
        binding={binding}
        onChange={onChange}
        depth={depth}
        renderChildren={(children, onChildren, parentMsg, childDepth) => (
          <BindingsTable
            messageFullName={parentMsg}
            value={children}
            onChange={onChildren}
            depth={childDepth}
          />
        )}
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
        <input
          placeholder="(可选 state key)"
          value={binding.storeAs ?? ''}
          onChange={(e) => set({ storeAs: e.target.value || undefined })}
          style={{ width: 160, padding: 2, border: '1px solid var(--badge-border)', borderRadius: 2 }}
        />
      </Space>
    </Space>
  );
}
