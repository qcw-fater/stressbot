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

import { Input, InputNumber, Select, Space } from 'antd';
import { useMemo } from 'react';
import type { FieldBind } from '@/types/action';
import { useFlowStore } from '../../store/flowStore';
import { useRuntimeStore } from '@/services/runtimeStore';
import { ProtoPathInput } from './ProtoPathInput';
import { StateKeyInput } from './StateKeyInput';
import { collectStateKeys, resolveProtoForStateKey } from './stateRegistry';

export interface BindingTypeFormProps {
  binding: FieldBind;
  /** 当前 action 的全部 bindings，用于收集 storeAs */
  currentBindings?: FieldBind[];
  onChange: (b: FieldBind) => void;
}

const LABEL: React.CSSProperties = { fontSize: 12, color: 'var(--text-tertiary)', flexShrink: 0 };

/** source + path 两行布局 */
function SourcePathRows({
  binding,
  currentBindings,
  set,
  sourceProto,
  showPath,
}: {
  binding: FieldBind;
  currentBindings?: FieldBind[];
  set: (p: Partial<FieldBind>) => void;
  sourceProto: string | undefined;
  showPath: boolean;
}) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6, width: '100%' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <span style={LABEL}>source</span>
        <StateKeyInput
          value={binding.source}
          onChange={(v) => set({ source: v || undefined })}
          currentBindings={currentBindings}
          placeholder="state key"
          style={{ flex: 1 }}
        />
      </div>
      {showPath && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <span style={LABEL}>path</span>
          <ProtoPathInput
            messageFullName={sourceProto}
            value={binding.path}
            onChange={(v) => set({ path: v || undefined })}
            placeholder="可选，从 state 值中取子字段"
            style={{ flex: 1 }}
          />
        </div>
      )}
    </div>
  );
}

export function BindingTypeForm({ binding, currentBindings, onChange }: BindingTypeFormProps) {
  const t = binding.type;
  const set = (partial: Partial<FieldBind>) => onChange({ ...binding, ...partial });

  const actions = useFlowStore((s) => s.actions);
  const listens = useFlowStore((s) => s.listens);
  const stateExtra = useRuntimeStore((s) => s.robotConfig.stateExtra);

  // 直接计算 sourceProto（不用 useEffect，避免 currentBindings 引用不稳定导致死循环）
  const sourceProto = useMemo(() => {
    if (!binding.source) return undefined;
    const keys = collectStateKeys(actions, listens, stateExtra, currentBindings, undefined);
    return resolveProtoForStateKey(keys, binding.source);
  }, [binding.source, actions, listens, stateExtra, currentBindings]);

  switch (t) {
    case 'fixed':
      return <FixedValueInput value={binding.value} onChange={(v) => set({ value: v })} />;
    case 'state':
    case 'stateFirst':
      return (
        <SourcePathRows binding={binding} currentBindings={currentBindings} set={set} sourceProto={sourceProto} showPath />
      );
    case 'stateRandom':
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, width: '100%' }}>
          <SourcePathRows binding={binding} currentBindings={currentBindings} set={set} sourceProto={sourceProto} showPath />
          <FiltersField binding={binding} set={set} />
        </div>
      );
    case 'stateRandomN':
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, width: '100%' }}>
          <SourcePathRows binding={binding} currentBindings={currentBindings} set={set} sourceProto={sourceProto} showPath />
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={LABEL}>count</span>
            <InputNumber
              min={1}
              placeholder="随机取几个"
              value={binding.count}
              onChange={(v) => set({ count: (v as number) ?? undefined })}
            />
          </div>
        </div>
      );
    case 'stateMapKey':
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, width: '100%' }}>
          <SourcePathRows binding={binding} currentBindings={currentBindings} set={set} sourceProto={sourceProto} showPath={false} />
          <FiltersField binding={binding} set={set} />
        </div>
      );
    case 'stateMapValue':
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, width: '100%' }}>
          <SourcePathRows binding={binding} currentBindings={currentBindings} set={set} sourceProto={sourceProto} showPath />
          <FiltersField binding={binding} set={set} />
        </div>
      );
    case 'randomPick':
      return <ValuesField binding={binding} set={set} />;
    case 'randomPickN':
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, width: '100%' }}>
          <ValuesField binding={binding} set={set} />
          <InputNumber
            min={1}
            placeholder="count"
            value={binding.count}
            onChange={(v) => set({ count: (v as number) ?? undefined })}
          />
        </div>
      );
    case 'randomPickMap':
      return <PickMapValuesField binding={binding} currentBindings={currentBindings} set={set} />;
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
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, width: '100%' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={LABEL}>source</span>
            <StateKeyInput
              value={binding.source}
              onChange={(v) => set({ source: v || undefined })}
              currentBindings={currentBindings}
              placeholder="state list"
              style={{ flex: 1 }}
            />
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={LABEL}>exclude</span>
            <StateKeyInput
              value={binding.excludeSource}
              onChange={(v) => set({ excludeSource: v || undefined })}
              currentBindings={currentBindings}
              placeholder="排除列表 (state key)"
              style={{ flex: 1 }}
            />
          </div>
          <ValuesField binding={binding} set={set} />
        </div>
      );
    case 'listSize':
      return (
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <span style={LABEL}>source</span>
          <StateKeyInput
            value={binding.source}
            onChange={(v) => set({ source: v || undefined })}
            currentBindings={currentBindings}
            placeholder="state list key"
            style={{ flex: 1 }}
          />
        </div>
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
  const filters = binding.filters ?? [];
  const ops = ['eq', 'neq', 'gt', 'gte', 'lt', 'lte', 'contains', 'in', 'timeWindow', 'dailyTimeWindow', 'notNil', 'isNil'] as const;

  const updateFilter = (i: number, patch: Record<string, unknown>) => {
    const arr = [...filters];
    arr[i] = { ...arr[i], ...patch };
    set({ filters: arr });
  };

  const addFilter = () => set({ filters: [...filters, { path: '', op: 'eq', value: '' }] });
  const removeFilter = (i: number) => set({ filters: filters.filter((_, j) => j !== i) });

  return (
    <Space direction="vertical" style={{ width: '100%' }} size={4}>
      <span style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>filters</span>
      {filters.map((f, i) => (
        <Space key={i} wrap size={4}>
          <Input
            placeholder="path（可选，如 items[0].id）"
            value={f.path ?? ''}
            onChange={(e) => updateFilter(i, { path: e.target.value })}
            style={{ width: 220 }}
            size="small"
          />
          <Select
            value={f.op || 'eq'}
            onChange={(v) => updateFilter(i, { op: v })}
            options={ops.map((o) => ({ value: o, label: o }))}
            style={{ width: 100 }}
            size="small"
          />
          {!['notNil', 'isNil'].includes(f.op) && (
            <Input
              placeholder="value"
              value={typeof f.value === 'undefined' ? (f.source ?? '') : String(f.value)}
              onChange={(e) => {
                const raw = e.target.value;
                try { updateFilter(i, { value: JSON.parse(raw), source: undefined }); }
                catch { updateFilter(i, { value: raw, source: undefined }); }
              }}
              style={{ width: 120 }}
              size="small"
            />
          )}
          <a onClick={() => removeFilter(i)} style={{ color: 'var(--color-error)', fontSize: 11 }}>删除</a>
        </Space>
      ))}
      <a onClick={addFilter} style={{ fontSize: 11 }}>+ 添加 filter</a>
    </Space>
  );
}

/** randomPickMap 结构化编辑器：keySource + [{key, values}] */
function PickMapValuesField({ binding, currentBindings, set }: { binding: FieldBind; currentBindings?: FieldBind[]; set: (p: Partial<FieldBind>) => void }) {
  const entries: Array<{ key: string; values: unknown[] }> = (binding.values ?? []) as Array<{ key: string; values: unknown[] }>;

  const updateEntry = (i: number, patch: Record<string, unknown>) => {
    const arr = [...entries];
    arr[i] = { ...arr[i], ...patch };
    set({ values: arr });
  };

  const addEntry = () => set({ values: [...entries, { key: '', values: [] }] });
  const removeEntry = (i: number) => set({ values: entries.filter((_, j) => j !== i) });

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6, width: '100%' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <span style={LABEL}>keySource</span>
        <StateKeyInput
          value={binding.keySource}
          onChange={(v) => set({ keySource: v || undefined })}
          currentBindings={currentBindings}
          placeholder="state 中 map 的 key"
          style={{ flex: 1 }}
        />
      </div>
      <span style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>映射表 (key → values[])</span>
      {entries.map((entry, i) => (
        <Space key={i} wrap size={4} style={{ width: '100%' }}>
          <Input
            placeholder="key"
            value={entry.key ?? ''}
            onChange={(e) => updateEntry(i, { key: e.target.value })}
            style={{ width: 100 }}
            size="small"
          />
          <Input
            placeholder="values (JSON 数组)"
            value={JSON.stringify(entry.values ?? [])}
            onChange={(e) => {
              try {
                const parsed = JSON.parse(e.target.value);
                if (Array.isArray(parsed)) updateEntry(i, { values: parsed });
              } catch { /* 输入中 */ }
            }}
            style={{ width: 200 }}
            size="small"
          />
          <a onClick={() => removeEntry(i)} style={{ color: 'var(--color-error)', fontSize: 11 }}>删除</a>
        </Space>
      ))}
      <a onClick={addEntry} style={{ fontSize: 11 }}>+ 添加映射</a>
    </div>
  );
}
