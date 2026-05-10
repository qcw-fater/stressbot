/**
 * 单个 FieldBind 编辑器：根据 type 切换不同输入控件。
 *
 * 17 种 type 的具体字段对照（设计文档 §4.1 / §6.3）：
 *   fixed         : value
 *   state         : source + path?
 *   stateFirst    : source + path?
 *   stateRandom   : source + path? + filters?
 *   stateRandomN  : source + count + path?
 *   stateMapKey   : source + filters?
 *   stateMapValue : source + path? + filters?
 *   randomPick    : values[]
 *   randomPickN   : values[] + count
 *   randomPickMap : keySource + count
 *   randomInt     : min + max
 *   randomBool    : (无)
 *   randomString  : length + charset
 *   randomExclude : (values[] | source) + excludeSource
 *   listSize      : source
 *   nested        : message + bindings (递归)
 *   nestedList    : items[] (递归 message + bindings)
 */

import { Input, InputNumber, Select, Space, Tag } from 'antd';
import type { BindingType, FieldBind } from '@/types/action';

export interface BindingTypeFormProps {
  binding: FieldBind;
  onChange: (b: FieldBind) => void;
  /** 嵌套深度（用于显式着色 / 防止过深） */
  depth?: number;
  /** 递归回调：渲染子绑定列表（nested / nestedList 调用） */
  renderChildren?: (
    bindings: FieldBind[],
    onChildren: (next: FieldBind[]) => void,
    parentMessage: string | undefined,
    depth: number,
  ) => React.ReactNode;
}

export function BindingTypeForm({ binding, onChange, depth = 0, renderChildren }: BindingTypeFormProps) {
  const t = binding.type;
  const set = (partial: Partial<FieldBind>) => onChange({ ...binding, ...partial });

  switch (t) {
    case 'fixed':
      return <FixedValueInput value={binding.value} onChange={(v) => set({ value: v })} />;
    case 'state':
    case 'stateFirst':
      return (
        <Space style={{ width: '100%' }}>
          <Input
            placeholder="source (state key)"
            value={binding.source ?? ''}
            onChange={(e) => set({ source: e.target.value })}
            style={{ width: 220 }}
          />
          <Input
            placeholder="path（可选，如 heroList[].id）"
            value={binding.path ?? ''}
            onChange={(e) => set({ path: e.target.value })}
            style={{ width: 240 }}
          />
        </Space>
      );
    case 'stateRandom':
      return (
        <Space direction="vertical" style={{ width: '100%' }}>
          <Space>
            <Input
              placeholder="source (state key)"
              value={binding.source ?? ''}
              onChange={(e) => set({ source: e.target.value })}
              style={{ width: 220 }}
            />
            <Input
              placeholder="path（可选）"
              value={binding.path ?? ''}
              onChange={(e) => set({ path: e.target.value })}
              style={{ width: 200 }}
            />
          </Space>
          <FiltersField binding={binding} set={set} />
        </Space>
      );
    case 'stateRandomN':
      return (
        <Space>
          <Input
            placeholder="source (state key)"
            value={binding.source ?? ''}
            onChange={(e) => set({ source: e.target.value })}
            style={{ width: 200 }}
          />
          <InputNumber
            min={1}
            placeholder="count"
            value={binding.count}
            onChange={(v) => set({ count: (v as number) ?? undefined })}
          />
          <Input
            placeholder="path（可选）"
            value={binding.path ?? ''}
            onChange={(e) => set({ path: e.target.value })}
            style={{ width: 180 }}
          />
        </Space>
      );
    case 'stateMapKey':
      return (
        <Space direction="vertical" style={{ width: '100%' }}>
          <Input
            placeholder="source (state map key)"
            value={binding.source ?? ''}
            onChange={(e) => set({ source: e.target.value })}
            style={{ width: 220 }}
          />
          <FiltersField binding={binding} set={set} />
        </Space>
      );
    case 'stateMapValue':
      return (
        <Space direction="vertical" style={{ width: '100%' }}>
          <Space>
            <Input
              placeholder="source (state map key)"
              value={binding.source ?? ''}
              onChange={(e) => set({ source: e.target.value })}
              style={{ width: 220 }}
            />
            <Input
              placeholder="path（取嵌套字段）"
              value={binding.path ?? ''}
              onChange={(e) => set({ path: e.target.value })}
              style={{ width: 200 }}
            />
          </Space>
          <FiltersField binding={binding} set={set} />
        </Space>
      );
    case 'randomPick':
      return <ValuesField binding={binding} set={set} />;
    case 'randomPickN':
      return (
        <Space direction="vertical" style={{ width: '100%' }}>
          <ValuesField binding={binding} set={set} />
          <InputNumber
            min={1}
            placeholder="count"
            value={binding.count}
            onChange={(v) => set({ count: (v as number) ?? undefined })}
          />
        </Space>
      );
    case 'randomPickMap':
      return (
        <Space>
          <Input
            placeholder="keySource (state map key)"
            value={binding.keySource ?? ''}
            onChange={(e) => set({ keySource: e.target.value })}
            style={{ width: 240 }}
          />
          <InputNumber
            min={1}
            placeholder="count"
            value={binding.count}
            onChange={(v) => set({ count: (v as number) ?? undefined })}
          />
        </Space>
      );
    case 'randomInt':
      return (
        <Space>
          <InputNumber
            placeholder="min"
            value={binding.min}
            onChange={(v) => set({ min: (v as number) ?? undefined })}
            style={{ width: 120 }}
          />
          <span>~</span>
          <InputNumber
            placeholder="max"
            value={binding.max}
            onChange={(v) => set({ max: (v as number) ?? undefined })}
            style={{ width: 120 }}
          />
        </Space>
      );
    case 'randomBool':
      return <span style={{ color: 'var(--text-tertiary)' }}>无参数（每次随机 true/false）</span>;
    case 'randomString':
      return (
        <Space>
          <InputNumber
            min={1}
            max={4096}
            placeholder="length"
            value={binding.length}
            onChange={(v) => set({ length: (v as number) ?? undefined })}
            style={{ width: 120 }}
          />
          <Select
            placeholder="charset"
            value={binding.charset}
            onChange={(v) => set({ charset: v })}
            options={[
              { value: 'alpha', label: 'alpha (a-zA-Z)' },
              { value: 'numeric', label: 'numeric (0-9)' },
              { value: 'alphanum', label: 'alphanum' },
            ]}
            allowClear
            style={{ width: 200 }}
          />
        </Space>
      );
    case 'randomExclude':
      return (
        <Space direction="vertical" style={{ width: '100%' }}>
          <Space>
            <Input
              placeholder="source (state list)"
              value={binding.source ?? ''}
              onChange={(e) => set({ source: e.target.value })}
              style={{ width: 200 }}
            />
            <Input
              placeholder="excludeSource (state list)"
              value={binding.excludeSource ?? ''}
              onChange={(e) => set({ excludeSource: e.target.value })}
              style={{ width: 240 }}
            />
          </Space>
          <ValuesField binding={binding} set={set} />
        </Space>
      );
    case 'listSize':
      return (
        <Input
          placeholder="source (state list key)"
          value={binding.source ?? ''}
          onChange={(e) => set({ source: e.target.value })}
          style={{ width: 280 }}
        />
      );
    case 'nested':
      return (
        <Space direction="vertical" style={{ width: '100%' }}>
          <Input
            placeholder="子消息 proto 全名（如 Game.HeroData）"
            value={binding.message ?? ''}
            onChange={(e) => set({ message: e.target.value })}
            style={{ width: '100%' }}
          />
          {renderChildren?.(
            binding.bindings ?? [],
            (next) => set({ bindings: next }),
            binding.message,
            depth + 1,
          )}
        </Space>
      );
    case 'nestedList':
      return (
        <Space direction="vertical" style={{ width: '100%' }}>
          <Tag color="purple">每个 item = {`{ message + bindings }`}</Tag>
          {(binding.items ?? []).map((sub, i) => (
            <div key={i} style={{ border: '1px dashed var(--divider-bg)', padding: 8, borderRadius: 4 }}>
              <Space style={{ marginBottom: 6 }}>
                <span style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>item #{i + 1}</span>
                <Input
                  size="small"
                  placeholder="子消息 proto 全名"
                  value={sub.message ?? ''}
                  onChange={(e) => {
                    const arr = [...(binding.items ?? [])];
                    arr[i] = { ...arr[i], message: e.target.value };
                    set({ items: arr });
                  }}
                  style={{ width: 240 }}
                />
                <a
                  onClick={() => set({ items: (binding.items ?? []).filter((_, j) => j !== i) })}
                  style={{ color: 'var(--color-error)' }}
                >
                  删除 item
                </a>
              </Space>
              {renderChildren?.(
                sub.bindings ?? [],
                (next) => {
                  const arr = [...(binding.items ?? [])];
                  arr[i] = { ...arr[i], bindings: next };
                  set({ items: arr });
                },
                sub.message,
                depth + 1,
              )}
            </div>
          ))}
          <a
            onClick={() =>
              set({ items: [...(binding.items ?? []), { type: 'nested' as BindingType, message: '', bindings: [] }] })
            }
          >
            + 添加 item
          </a>
        </Space>
      );
    default:
      return <span style={{ color: 'var(--color-error)' }}>未知 binding type: {t}</span>;
  }
}

function FixedValueInput({ value, onChange }: { value: unknown; onChange: (v: unknown) => void }) {
  const text = typeof value === 'string' ? value : JSON.stringify(value ?? '');
  return (
    <Input
      placeholder='固定值（字符串直接写，数字/布尔/JSON 直接写，如 5、true、["a","b"]）'
      value={text}
      onChange={(e) => {
        const raw = e.target.value;
        // 尝试 JSON 解析
        try {
          if (raw === '') return onChange('');
          const parsed = JSON.parse(raw);
          onChange(parsed);
        } catch {
          onChange(raw);
        }
      }}
    />
  );
}

function ValuesField({
  binding,
  set,
}: {
  binding: FieldBind;
  set: (p: Partial<FieldBind>) => void;
}) {
  const text = JSON.stringify(binding.values ?? []);
  return (
    <Input
      placeholder='values (JSON 数组，如 [1,2,3] 或 ["a","b"])'
      value={text === '[]' ? '' : text}
      onChange={(e) => {
        const raw = e.target.value;
        if (!raw) return set({ values: undefined });
        try {
          const parsed = JSON.parse(raw);
          if (Array.isArray(parsed)) set({ values: parsed });
        } catch {
          // 输入未完成，暂不更新
        }
      }}
    />
  );
}

function FiltersField({ binding, set }: { binding: FieldBind; set: (p: Partial<FieldBind>) => void }) {
  const text = JSON.stringify(binding.filters ?? []);
  return (
    <Input
      placeholder='filters (JSON 数组，如 [{"path":"id","op":"neq","value":0}])'
      value={text === '[]' ? '' : text}
      onChange={(e) => {
        const raw = e.target.value;
        if (!raw) return set({ filters: undefined });
        try {
          const parsed = JSON.parse(raw);
          if (Array.isArray(parsed)) set({ filters: parsed });
        } catch {
          // 输入中
        }
      }}
    />
  );
}
