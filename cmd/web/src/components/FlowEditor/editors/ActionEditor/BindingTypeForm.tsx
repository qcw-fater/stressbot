/**
 * 单个 FieldBind 编辑器：根据 type 切换不同输入控件。
 *
 * 通过 `section` prop 控制渲染区段：
 *   - 'all'（默认）= primary + advanced 纵向组合，与历史行为一致；BindingsTable 与 map entry
 *     的 `<BindingTypeForm valueOnly />` 走此路径，外观/行为不变。
 *   - 'primary' 仅核心取值控件；'advanced' 仅类型高级字段（path/filters/excludeSource），无则返回 null。
 *
 * 17 种 type 的 primary / advanced 字段对照（设计文档 §4.1 / §6.3，task-4-brief.md Step 4）：
 *   fixed         : primary=value                        advanced=none
 *   state         : primary=source                        advanced=path
 *   stateFirst    : primary=source                        advanced=path
 *   stateRandom   : primary=source                        advanced=path + filters
 *   stateRandomN  : primary=source + count                advanced=path + filters
 *   stateMapKey   : primary=source                        advanced=filters
 *   stateMapValue : primary=source                        advanced=path + filters
 *   randomPick    : primary=values                        advanced=none
 *   randomPickN   : primary=values + count                advanced=none
 *   randomPickMap : primary=keySource + 映射表             advanced=none
 *   randomInt     : primary=min/max                       advanced=none
 *   randomFloat   : primary=min/max/precision             advanced=none
 *   randomBool    : primary=无参数提示                     advanced=none
 *   randomString  : primary=length/charset                advanced=none
 *   randomExclude : primary=values/source                 advanced=excludeSource
 *   listSize      : primary=source                        advanced=none
 *   map           : primary=entries                       advanced=none
 *
 * 通用高级字段（required/optional/wrap/storeAs/condition）由
 * BindingsTable.BindingCommonAdvancedFields 统一渲染，不归本组件管。
 */

import { Button, Input, InputNumber, Select, Space, Tag, Tooltip } from 'antd';
import { DeleteOutlined, SwapOutlined } from '@ant-design/icons';
import { useMemo, useState } from 'react';
import type { FieldBind, FilterDef, MapEntryBind } from '@/types/action';
import { ALL_BINDING_TYPES } from '@/types/action';
import { useFlowStore } from '../../store/flowStore';
import { useRuntimeStore } from '@/services/runtimeStore';
import { JsonDraftInput } from '../shared/JsonDraftInput';
import { ProtoPathInput } from './ProtoPathInput';
import { StateKeyInput } from './StateKeyInput';
import { collectStateKeys, resolveProtoForStateKey } from './stateRegistry';
import { RANDOM_STRING_CHARSET_OPTIONS } from './randomStringCharset';

export interface BindingTypeFormProps {
  binding: FieldBind;
  /** 当前 action 的全部 bindings，用于收集 storeAs */
  currentBindings?: FieldBind[];
  /** valueOnly 模式：用于 map entry 的 value 编辑，禁止嵌套 map */
  valueOnly?: boolean;
  /**
   * 区段选择：
   *   - 'all'（默认）：渲染 primary + advanced，纵向小间距组合，等价于历史行为。
   *   - 'primary'：仅渲染主参数（source/values/min-max 等取值方式必备项）。
   *   - 'advanced'：仅渲染类型高级字段（path/filters/excludeSource），无则返回 null。
   *
   * setState/clearState 行用 'primary' 渲染紧凑主体，把通用高级配置与类型高级字段
   * 收进折叠区；其它 pattern（tcpSend/httpRequest 等）继续使用默认 'all'，外观不变。
   */
  section?: 'all' | 'primary' | 'advanced';
  onChange: (b: FieldBind) => void;
}

const LABEL: React.CSSProperties = { fontSize: 12, color: 'var(--text-tertiary)', flexShrink: 0 };

/** source 单行（primary 段单元） */
function SourceRow({
  binding,
  currentBindings,
  set,
}: {
  binding: FieldBind;
  currentBindings?: FieldBind[];
  set: (p: Partial<FieldBind>) => void;
}) {
  return (
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
  );
}

/** path 单行（advanced 段单元） */
function PathRow({
  binding,
  set,
  sourceProto,
}: {
  binding: FieldBind;
  set: (p: Partial<FieldBind>) => void;
  sourceProto: string | undefined;
}) {
  return (
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
  );
}

export function BindingTypeForm({
  binding,
  currentBindings,
  onChange,
  valueOnly = false,
  section = 'all',
}: BindingTypeFormProps) {
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

  if (section === 'primary') {
    return (
      <BindingPrimaryFields
        binding={binding}
        currentBindings={currentBindings}
        set={set}
        sourceProto={sourceProto}
        valueOnly={valueOnly}
      />
    );
  }
  if (section === 'advanced') {
    return (
      <BindingTypeAdvancedFields
        binding={binding}
        currentBindings={currentBindings}
        set={set}
        sourceProto={sourceProto}
      />
    );
  }
  // section === 'all'：先渲染 primary，再渲染 advanced；保持与历史调用一致的纵向组合。
  // advanced 对没有「类型高级字段」的类型返回 null，Space 会自动跳过。
  return (
    <Space direction="vertical" style={{ width: '100%' }} size="small">
      <BindingPrimaryFields
        binding={binding}
        currentBindings={currentBindings}
        set={set}
        sourceProto={sourceProto}
        valueOnly={valueOnly}
      />
      <BindingTypeAdvancedFields
        binding={binding}
        currentBindings={currentBindings}
        set={set}
        sourceProto={sourceProto}
      />
    </Space>
  );
}

/**
 * 主参数渲染器（primary 段）：每个 binding type 的「核心取值方式」控件。
 *
 * 字段归属严格按设计表（task-4-brief.md Step 4）的 primary 列；不要在此处放入
 * path/filters/excludeSource 等高级字段。
 */
function BindingPrimaryFields({
  binding,
  currentBindings,
  set,
  sourceProto: _sourceProto,
  valueOnly,
}: {
  binding: FieldBind;
  currentBindings?: FieldBind[];
  set: (p: Partial<FieldBind>) => void;
  sourceProto: string | undefined;
  valueOnly: boolean;
}) {
  // sourceProto 仅 advanced 段的 path/filters 需要；primary 段目前不直接使用，
  // 但为了保持调用签名对称、未来扩展（如带 proto 提示的 source 选择）保留入参。
  void _sourceProto;

  switch (binding.type) {
    case 'fixed':
      return <FixedValueInput value={binding.value} onChange={(v) => set({ value: v })} />;
    case 'state':
    case 'stateFirst':
    case 'stateRandom':
    case 'stateMapKey':
    case 'stateMapValue':
      return <SourceRow binding={binding} currentBindings={currentBindings} set={set} />;
    case 'stateRandomN':
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, width: '100%' }}>
          <SourceRow binding={binding} currentBindings={currentBindings} set={set} />
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={LABEL}>count</span>
            <InputNumber
              min={1}
              precision={0}
              step={1}
              placeholder="随机取几个"
              value={binding.count}
              onChange={(v) => set({ count: (v as number) ?? undefined })}
            />
          </div>
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
            precision={0}
            step={1}
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
            precision={0}
            step={1}
            value={binding.min}
            onChange={(v) => set({ min: (v as number) ?? undefined })}
            style={{ width: 120 }}
          />
          <span>~</span>
          <InputNumber
            placeholder="max"
            precision={0}
            step={1}
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
            precision={0}
            step={1}
            value={binding.min}
            onChange={(v) => set({ min: (v as number) ?? undefined })}
            style={{ width: 120 }}
          />
          <span>~</span>
          <InputNumber
            placeholder="max"
            precision={0}
            step={1}
            value={binding.max}
            onChange={(v) => set({ max: (v as number) ?? undefined })}
            style={{ width: 120 }}
          />
          <span style={LABEL}>精度</span>
          <InputNumber
            min={1}
            max={10}
            precision={0}
            step={1}
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
            precision={0}
            step={1}
            placeholder="length"
            value={binding.length}
            onChange={(v) => set({ length: (v as number) ?? undefined })}
            style={{ width: 120 }}
          />
          <Select
            mode="tags"
            placeholder="charset"
            value={binding.charset ? [binding.charset] : undefined}
            onChange={(v) => set({ charset: v.slice(-1)[0] || undefined })}
            options={RANDOM_STRING_CHARSET_OPTIONS}
            allowClear
            maxTagCount={1}
            style={{ width: 240 }}
          />
        </Space>
      );
    case 'randomExclude':
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, width: '100%' }}>
          <SourceRow binding={binding} currentBindings={currentBindings} set={set} />
          <ValuesField binding={binding} set={set} />
        </div>
      );
    case 'listSize':
      return <SourceRow binding={binding} currentBindings={currentBindings} set={set} />;
    case 'map':
      if (valueOnly) {
        return <span style={{ color: 'var(--color-error)' }}>map value 不支持嵌套 map</span>;
      }
      return <MapEntriesField binding={binding} currentBindings={currentBindings} set={set} />;
    default:
      return <span style={{ color: 'var(--color-error)' }}>未知 binding type: {binding.type}</span>;
  }
}

/**
 * 类型高级字段渲染器（advanced 段）：仅渲染 path/filters/excludeSource 等该 type 的「高级」控件。
 *
 * 字段归属严格按设计表（task-4-brief.md Step 4）的 advanced 列；没有高级字段的 type 返回 null。
 * 注意：通用高级字段（required/optional/wrap/storeAs/condition）不在此处，由
 * BindingsTable.BindingCommonAdvancedFields 统一渲染。
 */
function BindingTypeAdvancedFields({
  binding,
  currentBindings,
  set,
  sourceProto,
}: {
  binding: FieldBind;
  currentBindings?: FieldBind[];
  set: (p: Partial<FieldBind>) => void;
  sourceProto: string | undefined;
}) {
  switch (binding.type) {
    case 'state':
    case 'stateFirst':
      return <PathRow binding={binding} set={set} sourceProto={sourceProto} />;
    case 'stateRandom':
    case 'stateRandomN':
    case 'stateMapValue':
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, width: '100%' }}>
          <PathRow binding={binding} set={set} sourceProto={sourceProto} />
          <FiltersField binding={binding} set={set} sourceProto={sourceProto} />
        </div>
      );
    case 'stateMapKey':
      return <FiltersField binding={binding} set={set} sourceProto={sourceProto} />;
    case 'randomExclude':
      return (
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
      );
    default:
      // 该类型没有「类型高级字段」：advanced 段不渲染任何内容。
      return null;
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
      <Tooltip title="从 state 列表中先按条件筛选保留，再从保留结果中随机取值" mouseEnterDelay={0.4}>
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

const FILTER_OPS = ['eq', 'neq', 'gt', 'gte', 'lt', 'lte', 'contains', 'notContains', 'in', 'notIn', 'notNil', 'isNil'] as const;
const FILTER_OP_META: Record<string, { label: string; desc: string }> = {
  eq:              { label: '= 等于',         desc: '字段值等于指定值' },
  neq:             { label: '≠ 不等于',       desc: '字段值不等于指定值' },
  gt:              { label: '> 大于',         desc: '字段值大于指定值' },
  gte:             { label: '≥ 大于等于',     desc: '字段值大于或等于指定值' },
  lt:              { label: '< 小于',         desc: '字段值小于指定值' },
  lte:             { label: '≤ 小于等于',     desc: '字段值小于或等于指定值' },
  contains:        { label: '包含',           desc: '字符串或列表包含指定值' },
  notContains:     { label: '不包含',         desc: '字符串或列表不包含指定值' },
  in:              { label: '在列表中',       desc: '字段值在指定列表中' },
  notIn:           { label: '不在列表中',     desc: '字段值不在指定列表中' },
  notNil:          { label: '不为空',         desc: '字段值不为 nil' },
  isNil:           { label: '为空',           desc: '字段值为 nil' },
};

const FILTER_MODE_OPTIONS = [
  { value: 'any', label: '任意' },
  { value: 'all', label: '全部' },
  { value: 'none', label: '无任意' },
] as const;

const NO_VALUE_OPS = new Set(['notNil', 'isNil']);
const LIST_OPS = new Set(['in', 'notIn']);

function FilterRow({ filter, sourceProto, onChange, onRemove }: {
  filter: FilterDef;
  sourceProto?: string;
  onChange: (patch: Partial<FilterDef>) => void;
  onRemove: () => void;
}) {
  const op = (filter.op as string) || 'eq';
  // 模式与 source 真值解耦：useSource 由「是否有非空 source」派生，但切换到 state 模式时写入空串会让
  // !!source 仍为 false，StateKeyInput 永远不挂载、用户无法进入 state 配置。
  // 引入 local sourceMode：一旦用户点了切到 state，就显示输入框，直到切回 value 或切到无值 op。
  const [sourceMode, setSourceMode] = useState(false);
  // 已有非空 source（如导入数据）默认进入 source 模式，保证可查看/编辑。
  const useSource = sourceMode || !!filter.source;
  const noValue = NO_VALUE_OPS.has(op);
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
        <Tooltip title="数组通配路径（如 items[].id）使用：任意 / 全部 / 无任意；普通路径可留空">
          <Select
            value={filter.mode}
            allowClear
            placeholder="单值"
            onChange={(v) => onChange({ mode: v })}
            options={FILTER_MODE_OPTIONS.map((m) => ({ value: m.value, label: m.label }))}
            style={{ width: 88 }}
            size="small"
          />
        </Tooltip>
        <Select
          value={op}
          onChange={(v) => {
            const patch: Record<string, unknown> = { op: v };
            if (NO_VALUE_OPS.has(v)) {
              patch.value = undefined;
              patch.source = undefined;
              setSourceMode(false);
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
        {!noValue && (
          <Tooltip title={useSource ? '从 state 读取比较值' : '切换为从 state 读取'}>
            <Button
              size="small"
              type={useSource ? 'primary' : 'text'}
              icon={<SwapOutlined />}
              onClick={() => {
                if (useSource) {
                  setSourceMode(false);
                  onChange({ source: undefined });
                } else {
                  setSourceMode(true);
                  onChange({ value: undefined });
                  // 不写 source:'' —— 空串会让派生 useSource 回退；交给用户在 StateKeyInput 填写。
                }
              }}
            />
          </Tooltip>
        )}
        <Button size="small" type="text" danger icon={<DeleteOutlined />} onClick={onRemove} />
      </div>
      {!noValue && !isList && (
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

/** proto map 字段编辑器：entries[{key, value}] */
function MapEntriesField({ binding, currentBindings, set }: { binding: FieldBind; currentBindings?: FieldBind[]; set: (p: Partial<FieldBind>) => void }) {
  const entries: MapEntryBind[] = binding.entries ?? [];

  const updateEntry = (i: number, patch: Partial<MapEntryBind>) => {
    const arr = [...entries];
    arr[i] = { ...arr[i], ...patch };
    set({ entries: arr });
  };

  const addEntry = () => set({ entries: [...entries, { key: '', value: { type: 'fixed', value: '' } }] });
  const removeEntry = (i: number) => set({ entries: entries.filter((_, j) => j !== i) });

  /** 可选的 binding 类型（排除 map） */
  const MAP_VALUE_TYPES = ALL_BINDING_TYPES.filter((t) => t !== 'map');

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6, width: '100%' }}>
      <span style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>map entries（固定 key + 动态 value）</span>
      {entries.map((entry, i) => (
        <div
          key={i}
          style={{
            padding: '6px 8px',
            background: 'var(--hover-bg, rgba(0,0,0,0.02))',
            borderRadius: 6,
            display: 'flex',
            flexDirection: 'column',
            gap: 4,
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <Tag color="blue" style={{ margin: 0 }}>key</Tag>
            <JsonDraftInput
              mode="jsonOrString"
              value={entry.key}
              emptyValue=""
              onChange={(v) => updateEntry(i, { key: v })}
              placeholder="map key（字符串直接写，数字直接写）"
              style={{ flex: 1 }}
              size="small"
            />
            <Tag color="green" style={{ margin: 0 }}>value</Tag>
            <Select
              value={entry.value?.type}
              onChange={(v) => updateEntry(i, { value: { type: v, value: '' } as FieldBind })}
              options={MAP_VALUE_TYPES.map((t) => ({ value: t, label: t }))}
              style={{ width: 130 }}
              size="small"
            />
            <Tooltip title="删除此 entry">
              <Button size="small" type="text" danger icon={<DeleteOutlined />} onClick={() => removeEntry(i)} />
            </Tooltip>
          </div>
          {entry.value && (
            <div style={{ paddingLeft: 8 }}>
              <BindingTypeForm
                binding={entry.value}
                currentBindings={currentBindings}
                onChange={(v) => updateEntry(i, { value: v })}
                valueOnly
              />
            </div>
          )}
        </div>
      ))}
      <a onClick={addEntry} style={{ fontSize: 11 }}>+ 添加 entry</a>
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
