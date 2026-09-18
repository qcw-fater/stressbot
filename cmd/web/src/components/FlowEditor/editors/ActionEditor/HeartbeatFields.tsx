/**
 * 心跳二进制布局字段表（HeartbeatField[]）。
 *
 * 严格镜像 Go 端 `engine/heartbeat.go:HeartbeatField`：
 *   - type:   u8/i8/u16/i16/u32/i32/u64/i64（小端整数）/ f32/f64（小端 IEEE754 浮点）
 *   - source: fixed/state/stateCounter/counter/timestamp/randomInt（f32/f64 仅 fixed/state）
 *   - 按 source 动态展示配套字段（value / floatValue / key / min,max / start,step / unit）
 *
 * 每行一个字段，按顺序拼接成小端 body；增删 / 排序。
 */

import { Button, Input, InputNumber, Select, Space, Table, Tooltip } from 'antd';
import {
  ArrowDownOutlined,
  ArrowUpOutlined,
  DeleteOutlined,
  PlusOutlined,
} from '@ant-design/icons';
import type { HeartbeatField, HeartbeatFieldType, HeartbeatFieldSource } from '@/types/action';
import { ALL_HEARTBEAT_FIELD_TYPES } from '@/types/action';
import {
  changeHeartbeatFieldSource,
  changeHeartbeatFieldType,
  isFloatHeartbeatType,
  isSourceAllowedForType,
} from './heartbeatFieldModel';

export interface HeartbeatFieldsProps {
  value?: HeartbeatField[];
  onChange?: (v: HeartbeatField[]) => void;
  /** 区块标题 */
  label?: string;
}

const TYPE_OPTIONS = ALL_HEARTBEAT_FIELD_TYPES.map((t) => ({ value: t, label: t }));

const SOURCE_GROUPS: { label: string; sources: HeartbeatFieldSource[] }[] = [
  { label: '固定', sources: ['fixed'] },
  { label: 'state', sources: ['state', 'stateCounter'] },
  { label: '计数器/时间', sources: ['counter', 'timestamp'] },
  { label: '随机', sources: ['randomInt'] },
];

/**
 * 浮点类型下不支持的 source 置灰而不是从列表里去掉：去掉会让人以为自己记错了，
 * 置灰 + 说明能直接告诉他为什么这条不能选。
 */
function sourceOptionsFor(type: HeartbeatFieldType) {
  return SOURCE_GROUPS.map((g) => ({
    label: g.label,
    options: g.sources.map((s) => {
      const allowed = isSourceAllowedForType(s, type);
      return {
        value: s,
        disabled: !allowed,
        label: allowed ? s : `${s}（${type} 不支持）`,
      };
    }),
  }));
}

function emptyField(): HeartbeatField {
  return { type: 'u16', source: 'fixed', value: 0 };
}

export function HeartbeatFields({ value, onChange, label }: HeartbeatFieldsProps) {
  const list = value ?? [];
  const set = (next: HeartbeatField[]) => onChange?.(next);
  const update = (i: number, partial: Partial<HeartbeatField>) => {
    const next = [...list];
    next[i] = { ...next[i], ...partial };
    set(next);
  };
  /** 换 type / source 要按新组合裁掉旧参数，不能像改参数那样直接合并。 */
  const replace = (i: number, field: HeartbeatField) => {
    const next = [...list];
    next[i] = field;
    set(next);
  };

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
        <span style={{ fontSize: 13, color: 'var(--text-secondary)' }}>
          {label ?? 'heartbeatFields（小端二进制布局）'}
        </span>
        <Button size="small" icon={<PlusOutlined />} onClick={() => set([...list, emptyField()])}>
          添加字段
        </Button>
      </div>
      <Table<HeartbeatField & { _i: number }>
        size="small"
        dataSource={list.map((f, i) => ({ ...f, _i: i }))}
        rowKey="_i"
        pagination={false}
        locale={{ emptyText: '无字段（空 body = 静态心跳）' }}
        scroll={{ x: 720 }}
        columns={[
          {
            title: 'type',
            dataIndex: 'type',
            width: 90,
            render: (_, r) => (
              <Select
                size="small"
                value={r.type}
                // 传 list[r._i] 而不是 r：r 是表格行，带着渲染用的 _i，写回去会进 codec JSON
                onChange={(v: HeartbeatFieldType) =>
                  replace(r._i, changeHeartbeatFieldType(list[r._i], v))
                }
                options={TYPE_OPTIONS}
                style={{ width: 80 }}
              />
            ),
          },
          {
            title: 'source',
            dataIndex: 'source',
            width: 140,
            render: (_, r) => (
              <Select
                size="small"
                value={r.source}
                onChange={(v: HeartbeatFieldSource) =>
                  replace(r._i, changeHeartbeatFieldSource(list[r._i], v))
                }
                options={sourceOptionsFor(r.type)}
                style={{ width: 130 }}
                showSearch
                optionFilterProp="value"
              />
            ),
          },
          {
            title: '参数',
            render: (_, r) => <SourceParams field={r} onChange={(p) => update(r._i, p)} />,
          },
          {
            title: '操作',
            width: 100,
            render: (_, r) => (
              <Space size={4}>
                <Tooltip title="上移">
                  <Button
                    size="small"
                    icon={<ArrowUpOutlined />}
                    disabled={r._i === 0}
                    onClick={() => {
                      const next = [...list];
                      [next[r._i - 1], next[r._i]] = [next[r._i], next[r._i - 1]];
                      set(next);
                    }}
                  />
                </Tooltip>
                <Tooltip title="下移">
                  <Button
                    size="small"
                    icon={<ArrowDownOutlined />}
                    disabled={r._i === list.length - 1}
                    onClick={() => {
                      const next = [...list];
                      [next[r._i], next[r._i + 1]] = [next[r._i + 1], next[r._i]];
                      set(next);
                    }}
                  />
                </Tooltip>
                <Tooltip title="删除">
                  <Button
                    size="small"
                    danger
                    icon={<DeleteOutlined />}
                    onClick={() => set(list.filter((_, j) => j !== r._i))}
                  />
                </Tooltip>
              </Space>
            ),
          },
        ]}
      />
      <div style={{ fontSize: 11, color: 'var(--text-tertiary)', marginTop: 4 }}>
        字段按顺序以小端字节序拼接。source=fixed 填 value（f32/f64 填
        floatValue）；state/stateCounter 填 key；randomInt 填 min/max。f32/f64
        只能用 fixed 或 state：计数器、时间戳、随机整数对浮点没有意义，引擎会拒绝。
      </div>
    </div>
  );
}

/** min/max 都填了才谈得上大小关系；缺值由引擎按「缺 min/max」单独报错。 */
function hasRange(field: HeartbeatField): boolean {
  return typeof field.min === 'number' && typeof field.max === 'number';
}

/** 按 source 动态展示配套字段。 */
interface SourceParamsProps {
  field: HeartbeatField;
  onChange: (p: Partial<HeartbeatField>) => void;
}

function SourceParams({ field, onChange }: SourceParamsProps) {
  switch (field.source) {
    case 'fixed': {
      // f32/f64 走 floatValue（支持小数）；整型走 value。两者同属 source=fixed，按 type 切换绑定字段。
      const isFloat = isFloatHeartbeatType(field.type);
      return (
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
          <span style={{ color: 'var(--text-tertiary)', fontSize: 12 }}>
            {isFloat ? 'floatValue' : 'value'}
          </span>
          <InputNumber
            size="small"
            value={isFloat ? field.floatValue : field.value}
            onChange={(v) =>
              onChange(
                isFloat
                  ? { floatValue: (v as number) ?? undefined }
                  : { value: (v as number) ?? undefined },
              )
            }
            step={isFloat ? 0.1 : 1}
            // 整型槽在 Go 端是 *int64，小数会让 codec 配置反序列化失败（schema 也按 integer 卡）
            precision={isFloat ? undefined : 0}
            style={{ width: 140 }}
          />
        </span>
      );
    }
    case 'state':
    case 'stateCounter':
      return (
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
          <span style={{ color: 'var(--text-tertiary)', fontSize: 12 }}>key</span>
          <Input
            size="small"
            value={field.key ?? ''}
            onChange={(e) => onChange({ key: e.target.value })}
            placeholder="state 键名"
            style={{ width: 220 }}
          />
        </span>
      );
    case 'randomInt':
      return (
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
          <span style={{ color: 'var(--text-tertiary)', fontSize: 12 }}>min</span>
          <InputNumber
            size="small"
            value={field.min}
            onChange={(v) => onChange({ min: (v as number) ?? undefined })}
            precision={0}
            status={hasRange(field) && field.min! > field.max! ? 'error' : undefined}
            style={{ width: 100 }}
          />
          <span style={{ color: 'var(--text-tertiary)', fontSize: 12 }}>max</span>
          <InputNumber
            size="small"
            value={field.max}
            onChange={(v) => onChange({ max: (v as number) ?? undefined })}
            precision={0}
            status={hasRange(field) && field.min! > field.max! ? 'error' : undefined}
            style={{ width: 100 }}
          />
        </span>
      );
    case 'counter':
      return (
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
          <span style={{ color: 'var(--text-tertiary)', fontSize: 12 }}>start</span>
          <InputNumber
            size="small"
            value={field.start}
            onChange={(v) => onChange({ start: (v as number) ?? undefined })}
            placeholder="0"
            precision={0}
            style={{ width: 90 }}
          />
          <span style={{ color: 'var(--text-tertiary)', fontSize: 12 }}>step</span>
          <InputNumber
            size="small"
            value={field.step}
            onChange={(v) => onChange({ step: (v as number) ?? undefined })}
            placeholder="1"
            precision={0}
            style={{ width: 90 }}
          />
        </span>
      );
    case 'timestamp':
      return (
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
          <span style={{ color: 'var(--text-tertiary)', fontSize: 12 }}>unit</span>
          <Select
            size="small"
            value={field.unit ?? 'ms'}
            onChange={(v: 'ms' | 's') => onChange({ unit: v })}
            options={[
              { value: 'ms', label: 'ms' },
              { value: 's', label: 's' },
            ]}
            style={{ width: 90 }}
          />
        </span>
      );
    default:
      return null;
  }
}
