/**
 * setState 专用编辑器（方案 B：摘要卡片折叠）。
 *
 * 与通用 BindingsTable 编辑同一个 `ActionDef.bindings` 数组，但面向 setState 场景重新组织：
 *   - 每条 binding 一张 Collapse 摘要卡片：折叠态显示「目标状态 + 取值方式 + 值摘要 + 高级标签」。
 *   - 展开态：目标状态输入（StateKeyInput，候选不存在时标记「新状态」）、取值方式下拉，
 *     随后取值字段（BindingTypeForm 全字段）+ 通用高级字段（required/optional/wrap/storeAs/condition）
 *     全部内联 —— 与 BindingsTable 的展开行一致，不再套内层折叠。
 *
 * 设计约束：
 *   - 不自动创建、改名、去重、迁移任何 state key；只通过 onChange 原地编辑数组。
 *   - isNew 判定取 binding.field 的顶层段（按 '.' 与 '[' 切分），与 stateRegistry 内置 key
 *     （id/index/account）一致算作「已存在」。
 */

import { Button, Collapse, Form, Select, Space, Tag, Tooltip } from 'antd';
import {
  ArrowDownOutlined,
  ArrowUpOutlined,
  DeleteOutlined,
  PlusOutlined,
} from '@ant-design/icons';
import type { BindingType, FieldBind } from '@/types/action';
import { BindingCommonAdvancedFields, BINDING_TYPE_DESC } from './BindingsTable';
import { BindingTypeForm } from './BindingTypeForm';
import {
  SET_STATE_TYPE_GROUPS,
  bindingValueSummary,
  changeBindingType,
  moveBinding,
} from './stateActionEditorModel';
import { StateKeyInput } from './StateKeyInput';
import { useStateKeyOptions } from './useStateKeyOptions';

export interface SetStateEditorProps {
  value?: FieldBind[];
  onChange: (bindings: FieldBind[]) => void;
}

export function SetStateEditor({ value, onChange }: SetStateEditorProps) {
  const list = value ?? [];
  // isNew 判定的候选数据源：与目标状态输入共享同一份键集合。
  const { keys } = useStateKeyOptions(list);

  const setBinding = (i: number, next: FieldBind) => {
    const arr = [...list];
    arr[i] = next;
    onChange(arr);
  };

  /** binding.field 的顶层段是否不在已知 state 候选中（空白 field 既非新也非旧）。 */
  const isNewTarget = (field: string | undefined): boolean => {
    const trimmed = field?.trim();
    if (!trimmed) return false;
    const top = trimmed.split('.')[0].split('[')[0];
    return !keys.some((k) => k.key === top);
  };

  const items = list.map((binding, i) => {
    const isNew = isNewTarget(binding.field);
    return {
      key: String(i),
      label: (
        <Space wrap>
          <code>{binding.field || '(未指定目标状态)'}</code>
          <Tooltip title={BINDING_TYPE_DESC[binding.type]} mouseEnterDelay={0.4}>
            <Tag color="blue">{binding.type}</Tag>
          </Tooltip>
          <span style={{ color: 'var(--text-tertiary)' }}>{bindingValueSummary(binding)}</span>
          {binding.required && (
            <Tooltip title="缺失时报错" mouseEnterDelay={0.4}>
              <Tag color="red">required</Tag>
            </Tooltip>
          )}
          {binding.optional && (
            <Tooltip title="缺失时跳过" mouseEnterDelay={0.4}>
              <Tag>optional</Tag>
            </Tooltip>
          )}
          {binding.wrap && (
            <Tooltip title="单值包成 [v]" mouseEnterDelay={0.4}>
              <Tag color="purple">wrap</Tag>
            </Tooltip>
          )}
          {binding.storeAs && (
            <Tooltip title={`存入 state：${binding.storeAs}`} mouseEnterDelay={0.4}>
              <Tag color="green">→ {binding.storeAs}</Tag>
            </Tooltip>
          )}
          {binding.condition && (
            <Tooltip title="有条件绑定" mouseEnterDelay={0.4}>
              <Tag color="orange">condition</Tag>
            </Tooltip>
          )}
        </Space>
      ),
      extra: (
        <Space size={2} onClick={(e) => e.stopPropagation()}>
          <Button
            size="small"
            icon={<ArrowUpOutlined />}
            disabled={i === 0}
            onClick={() => onChange(moveBinding(list, i, i - 1))}
          />
          <Button
            size="small"
            icon={<ArrowDownOutlined />}
            disabled={i === list.length - 1}
            onClick={() => onChange(moveBinding(list, i, i + 1))}
          />
          <Button
            size="small"
            danger
            icon={<DeleteOutlined />}
            onClick={() => onChange(list.filter((_, j) => j !== i))}
          />
        </Space>
      ),
      children: (
        <Space direction="vertical" style={{ width: '100%' }} size="middle">
          <Form.Item label="目标状态" extra={isNew ? '运行时将创建这个新状态' : undefined}>
            <Space.Compact style={{ width: '100%' }}>
              <StateKeyInput
                value={binding.field}
                onChange={(field) => setBinding(i, { ...binding, field })}
                currentBindings={list}
                placeholder="选择已有状态或输入新名称"
                style={{ flex: 1 }}
              />
              {isNew && <Tag color="green" style={{ marginInlineStart: 8 }}>新状态</Tag>}
            </Space.Compact>
          </Form.Item>
          <Form.Item label="取值方式">
            <Select
              value={binding.type}
              onChange={(v) => setBinding(i, changeBindingType(binding, v as BindingType))}
              options={SET_STATE_TYPE_GROUPS.map((g) => ({
                label: g.label,
                options: g.types.map((t) => ({
                  value: t,
                  label: t,
                  title: BINDING_TYPE_DESC[t],
                })),
              }))}
              style={{ width: 220 }}
            />
          </Form.Item>
          <BindingTypeForm
            binding={binding}
            currentBindings={list}
            onChange={(b) => setBinding(i, b)}
          />
          <BindingCommonAdvancedFields
            binding={binding}
            onChange={(b) => setBinding(i, b)}
          />
        </Space>
      ),
    };
  });

  return (
    <div>
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          marginBottom: 8,
        }}
      >
        <strong>设置状态 ({list.length})</strong>
        <Button
          size="small"
          icon={<PlusOutlined />}
          onClick={() => onChange([...list, { type: 'fixed', field: '' }])}
        >
          添加状态
        </Button>
      </div>
      <Collapse items={items} size="small" />
    </div>
  );
}
