/**
 * 单个 FieldBind 编辑器：根据 type 切换不同输入控件。
 *
 * 16 种 type 的具体字段对照（设计文档 §4.1 / §6.3）：
 *   fixed         : value
 *   state         : source + path?
 *   stateFirst    : source + path?
 *   stateRandom   : source + path? + filters?
 *   stateRandomN  : source + count + path?
 *   stateMapKey   : source + filters?
 *   stateMapValue : source + path? + filters?
 *   randomPick    : values[]
 *   randomPickN   : values[] + count
 *   randomPickMap : keySource + values[{key, values}]
 *   randomInt     : min + max
 *   randomFloat   : min + max + precision?
 *   randomBool    : (无)
 *   randomString  : length + charset
 *   randomExclude : (values[] | source) + excludeSource
 *   listSize      : source
 */

import { Button, Input, InputNumber, Select, Space, Tag, Tooltip } from 'antd';
import { DeleteOutlined, SwapOutlined } from '@ant-design/icons';
import { useMemo, useState } from 'react';
import type { FieldBind, FilterDef } from '@/types/action';
import { useFlowStore } from '../../store/flowStore';
import { useRuntimeStore } from '@/services/runtimeStore';
import { JsonDraftInput } from '../shared/JsonDraftInput';
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
          <FiltersField binding={binding} set={set} sourceProto={sourceProto} />
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
          <FiltersField binding={binding} set={set} sourceProto={sourceProto} />
        </div>
      );
    case 'stateMapKey':
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, width: '100%' }}>
          <SourcePathRows binding={binding} currentBindings={currentBindings} set={set} sourceProto={sourceProto} showPath={false} />
          <FiltersField binding={binding} set={set} sourceProto={sourceProto} />
        </div>
      );
    case 'stateMapValue':
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, width: '100%' }}>
          <SourcePathRows binding={binding} currentBindings={currentBindings} set={set} sourceProto={sourceProto} showPath />
          <FiltersField binding={binding} set={set} sourceProto={sourceProto} />
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
    case 'randomFloat':
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
          <span style={LABEL}>精度</span>
          <InputNumber
            min={1}
            max={10}
            placeholder="2"
            value={binding.precision}
            onChange={(v) => set({ precision: (v as number) ?? undefined })}
            style={{ width: 80 }}
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
  return (
    <JsonDraftInput
      mode="jsonOrString"
      value={value}
      emptyValue=""
      onChange={onChange}
      placeholder='固定值（字符串直接写，数字/布尔/JSON 直接写，如 5、true、["a","b"]）'
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
  return (
    <JsonDraftInput
      mode="jsonArray"
      value={binding.values}
      emptyValue={undefined}
      onChange={(v) => set({ values: Array.isArray(v) ? v : undefined })}
      placeholder='values (JSON 数组，如 [1,2,3] 或 ["a","b"])'
    />
  );
}

function FiltersField({ binding, set, sourceProto }: { binding: FieldBind; set: (p: Partial<FieldBind>) => void; sourceProto?: string }) {
  const filters = binding.filters ?? [];
  const updateFilter = (i: number, patch: Record<string, unknown>) => {
    const arr = [...filters];
    arr[i] = { ...arr[i], ...patch };
    set({ filters: arr });
  };
  const addFilter = () => set({ filters: [...filters, { path: '', op: 'eq', value: '' }] });
  const removeFilter = (i: number) => set({ filters: filters.filter((_, j) => j !== i) });

  if (!filters.length) {
    return (
      <Tooltip title="从 state 列表中先按条件筛选，再从结果中随机取值" mouseEnterDelay={0.4}>
        <a onClick={addFilter} style={{ fontSize: 11 }}>+ 添加 filter</a>
      </Tooltip>
    );
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6, width: '100%' }}>
      <Tooltip title="多个 filter 之间为 AND 关系（全部满足才保留）" mouseEnterDelay={0.4}>
        <span style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>filters（全部满足才保留）</span>
      </Tooltip>
      {filters.map((f, i) => (
        <FilterRow
          key={i}
          filter={f}
          sourceProto={sourceProto}
          onChange={(patch) => updateFilter(i, patch)}
          onRemove={() => removeFilter(i)}
        />
      ))}
      <a onClick={addFilter} style={{ fontSize: 11 }}>+ 继续添加</a>
    </div>
  );
}

const FILTER_OPS = ['eq', 'neq', 'gt', 'gte', 'lt', 'lte', 'contains', 'in', 'timeWindow', 'dailyTimeWindow', 'notNil', 'isNil'] as const;
const FILTER_OP_META: Record<string, { label: string; desc: string }> = {
  eq:              { label: '= 等于',         desc: '字段值等于指定值' },
  neq:             { label: '≠ 不等于',       desc: '字段值不等于指定值' },
  gt:              { label: '> 大于',         desc: '字段值大于指定值' },
  gte:             { label: '≥ 大于等于',     desc: '字段值大于或等于指定值' },
  lt:              { label: '< 小于',         desc: '字段值小于指定值' },
  lte:             { label: '≤ 小于等于',     desc: '字段值小于或等于指定值' },
  contains:        { label: '包含',           desc: '字符串包含指定子串' },
  in:              { label: '在列表中',       desc: '字段值在指定列表中' },
  timeWindow:      { label: '时间窗口',       desc: '检查字段值（分钟数）是否在指定时间范围内' },
  dailyTimeWindow: { label: '每日时间窗口',   desc: '检查字段值是否在每日开放时间段内' },
  notNil:          { label: '不为空',         desc: '字段值不为 nil' },
  isNil:           { label: '为空',           desc: '字段值为 nil' },
};

const NO_VALUE_OPS = new Set(['notNil', 'isNil']);
const STRUCTURED_OPS = new Set(['timeWindow', 'dailyTimeWindow']);
const LIST_OPS = new Set(['in']);

function FilterRow({ filter, sourceProto, onChange, onRemove }: {
  filter: FilterDef;
  sourceProto?: string;
  onChange: (patch: Partial<FilterDef>) => void;
  onRemove: () => void;
}) {
  const op = (filter.op as string) || 'eq';
  const useSource = !!filter.source;
  const noValue = NO_VALUE_OPS.has(op);
  const isStructured = STRUCTURED_OPS.has(op);
  const isList = LIST_OPS.has(op);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, padding: '6px 8px', background: 'var(--hover-bg, rgba(0,0,0,0.02))', borderRadius: 6 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
        <ProtoPathInput
          messageFullName={sourceProto}
          value={(filter.path as string) ?? ''}
          onChange={(v) => onChange({ path: v })}
          placeholder="字段路径"
          style={{ flex: 1 }}
        />
        <Select
          value={op}
          onChange={(v) => {
            const patch: Record<string, unknown> = { op: v };
            if (NO_VALUE_OPS.has(v)) {
              patch.value = undefined;
              patch.source = undefined;
            }
            if (v === 'timeWindow') {
              patch.value = { startTime: 0, endTime: 1440 };
              patch.source = undefined;
            }
            if (v === 'dailyTimeWindow') {
              patch.value = [{ startHour: 9, startMinute: 0, endHour: 18, endMinute: 0 }];
              patch.source = undefined;
            }
            onChange(patch);
          }}
          options={FILTER_OPS.map((o) => ({ value: o, label: FILTER_OP_META[o]?.label ?? o, title: '' }))}
          optionRender={(opt) => {
            const m = FILTER_OP_META[opt.value as string];
            return (
              <Tooltip title={m?.desc} mouseEnterDelay={0.3} placement="right">
                <div>{opt.label}</div>
              </Tooltip>
            );
          }}
          style={{ width: 110 }}
          size="small"
        />
        {!noValue && !isStructured && (
          <Tooltip title={useSource ? '从 state 读取比较值' : '切换为从 state 读取'}>
            <Button
              size="small"
              type={useSource ? 'primary' : 'text'}
              icon={<SwapOutlined />}
              onClick={() => {
                if (useSource) onChange({ source: undefined });
                else onChange({ source: '', value: undefined });
              }}
            />
          </Tooltip>
        )}
        <Button size="small" type="text" danger icon={<DeleteOutlined />} onClick={onRemove} />
      </div>
      {!noValue && !isStructured && !isList && (
        useSource ? (
          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <span style={LABEL}>state</span>
            <StateKeyInput
              value={filter.source as string}
              onChange={(v) => onChange({ source: v || undefined })}
              placeholder="比较值来自 state key"
              style={{ flex: 1 }}
            />
          </div>
        ) : (
          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <span style={LABEL}>value</span>
            <JsonDraftInput
              mode="jsonOrString"
              value={filter.value}
              emptyValue={undefined}
              onChange={(v) => onChange({ value: v })}
              placeholder="比较值（数字/字符串/JSON）"
              style={{ flex: 1 }}
              size="small"
            />
          </div>
        )
      )}
      {!noValue && isList && (
        <TagListInput
          values={Array.isArray(filter.value) ? filter.value as unknown[] : []}
          onChange={(v) => onChange({ value: v })}
        />
      )}
      {op === 'timeWindow' && <TimeWindowInput value={filter.value as Record<string, number> | undefined} onChange={(v) => onChange({ value: v })} />}
      {op === 'dailyTimeWindow' && <DailyTimeWindowInput value={filter.value as Array<Record<string, number>> | undefined} onChange={(v) => onChange({ value: v })} />}
    </div>
  );
}

function TagListInput({ values, onChange }: { values: unknown[]; onChange: (v: unknown[]) => void }) {
  const [inputVal, setInputVal] = useState('');
  const addTag = () => {
    const v = inputVal.trim();
    if (!v) return;
    let parsed: unknown = v;
    try { parsed = JSON.parse(v); } catch { /* keep string */ }
    onChange([...values, parsed]);
    setInputVal('');
  };
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      <div style={{ display: 'flex', gap: 4 }}>
        <Input
          placeholder="输入值后回车添加"
          value={inputVal}
          onChange={(e) => setInputVal(e.target.value)}
          onPressEnter={addTag}
          size="small"
          style={{ flex: 1 }}
        />
        <Button size="small" onClick={addTag} disabled={!inputVal.trim()}>添加</Button>
      </div>
      {values.length > 0 && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
          {values.map((v, i) => (
            <Tag
              key={i}
              closable
              onClose={() => onChange(values.filter((_, j) => j !== i))}
              style={{ margin: 0 }}
            >
              {JSON.stringify(v)}
            </Tag>
          ))}
        </div>
      )}
    </div>
  );
}

function TimeWindowInput({ value, onChange }: { value?: Record<string, number>; onChange: (v: Record<string, number>) => void }) {
  const start = value?.startTime ?? 0;
  const end = value?.endTime ?? 1440;
  const fmt = (m: number) => `${String(Math.floor(m / 60)).padStart(2, '0')}:${String(m % 60).padStart(2, '0')}`;
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
      <span style={LABEL}>开始</span>
      <InputNumber min={0} max={1440} value={start} onChange={(v) => onChange({ startTime: v ?? 0, endTime: end })} style={{ width: 80 }} size="small" />
      <span style={{ color: 'var(--text-tertiary)', fontSize: 11 }}>{fmt(start)}</span>
      <span style={LABEL}>结束</span>
      <InputNumber min={0} max={1440} value={end} onChange={(v) => onChange({ startTime: start, endTime: v ?? 1440 })} style={{ width: 80 }} size="small" />
      <span style={{ color: 'var(--text-tertiary)', fontSize: 11 }}>{fmt(end)}</span>
    </div>
  );
}

function DailyTimeWindowInput({ value, onChange }: { value?: Array<Record<string, number>>; onChange: (v: Array<Record<string, number>>) => void }) {
  const entries = value ?? [{ startHour: 9, startMinute: 0, endHour: 18, endMinute: 0 }];
  const updateEntry = (i: number, patch: Record<string, number>) => {
    const arr = [...entries];
    arr[i] = { ...arr[i], ...patch };
    onChange(arr);
  };
  const addEntry = () => onChange([...entries, { startHour: 0, startMinute: 0, endHour: 23, endMinute: 59 }]);
  const removeEntry = (i: number) => onChange(entries.filter((_, j) => j !== i));
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      {entries.map((e, i) => (
        <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
          <InputNumber min={0} max={23} value={e.startHour} onChange={(v) => updateEntry(i, { startHour: v ?? 0 })} style={{ width: 56 }} size="small" placeholder="时" />
          <span style={LABEL}>:</span>
          <InputNumber min={0} max={59} value={e.startMinute} onChange={(v) => updateEntry(i, { startMinute: v ?? 0 })} style={{ width: 56 }} size="small" placeholder="分" />
          <span style={{ margin: '0 2px' }}>~</span>
          <InputNumber min={0} max={23} value={e.endHour} onChange={(v) => updateEntry(i, { endHour: v ?? 23 })} style={{ width: 56 }} size="small" placeholder="时" />
          <span style={LABEL}>:</span>
          <InputNumber min={0} max={59} value={e.endMinute} onChange={(v) => updateEntry(i, { endMinute: v ?? 59 })} style={{ width: 56 }} size="small" placeholder="分" />
          {entries.length > 1 && <a onClick={() => removeEntry(i)} style={{ color: 'var(--color-error)', fontSize: 11 }}>删除</a>}
        </div>
      ))}
      <a onClick={addEntry} style={{ fontSize: 11 }}>+ 添加时段</a>
    </div>
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
          <JsonDraftInput
            mode="jsonArray"
            placeholder="values (JSON 数组)"
            value={entry.values ?? []}
            onChange={(v) => {
              if (Array.isArray(v)) updateEntry(i, { values: v });
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
