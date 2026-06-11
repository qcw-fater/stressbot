/**
 * StoreMapping 列表编辑器（S2C 字段 → state key）。
 *
 * 复用：ActionEditor（tcpRequest/udpRequest/tcpListen/udpListen 的 store）
 *      ListenEditor（declarative 形态的 store）
 */

import { Button, Collapse, Space, Tag, Tooltip } from 'antd';
import { ArrowUpOutlined, ArrowDownOutlined, DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import type { CollapseProps } from 'antd';
import type { StoreMapping } from '@/types/action';
import type { ActionDef } from '@/types/action';
import type { ListenDef } from '@/types/listen';
import { ProtoPathInput } from './ProtoPathInput';
import { SetterPathInput } from '../shared/SetterPathInput';

export interface StoreTableProps {
  s2cProto?: string;
  value?: StoreMapping[];
  onChange?: (v: StoreMapping[]) => void;
  /** 区块标题 */
  label?: string;
  /** 以下 props 透传给 SetterPathInput 以浏览 state key 树 */
  actions?: Record<string, ActionDef>;
  listens?: Record<string, ListenDef>;
  stateExtra?: Record<string, string>;
  luaScripts?: Array<{ name: string; content: string }>;
}

export function StoreTable({ s2cProto, value, onChange, label, actions, listens, stateExtra, luaScripts }: StoreTableProps) {
  const list = value ?? [];
  const set = (next: StoreMapping[]) => onChange?.(next);

  const moveItem = (from: number, to: number) => {
    if (to < 0 || to >= list.length) return;
    const arr = [...list];
    [arr[from], arr[to]] = [arr[to], arr[from]];
    set(arr);
  };

  const items: CollapseProps['items'] = list.map((s, i) => ({
    key: String(i),
    label: (
      <Space>
        {s.field ? (
          <code>{s.field}</code>
        ) : (
          <span style={{ color: 'var(--text-tertiary)' }}>(整体)</span>
        )}
        <span style={{ color: 'var(--text-tertiary)' }}>→</span>
        {s.setter ? (
          <Tag color="green">{s.setter}</Tag>
        ) : (
          <span style={{ color: 'var(--color-error)' }}>(未指定)</span>
        )}
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
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, width: '100%', minWidth: 0 }}>
        <Tooltip title="留空 = 存储整个 S2C 消息" mouseEnterDelay={0.4}>
          <span style={{ fontSize: 12, color: 'var(--text-tertiary)', flexShrink: 0 }}>field</span>
        </Tooltip>
        <div style={{ flex: '1 1 260px', minWidth: 180 }}>
          <ProtoPathInput
            messageFullName={s2cProto}
            value={s.field}
            onChange={(v) => {
              const arr = [...list];
              arr[i] = { ...arr[i], field: v || undefined };
              set(arr);
            }}
            placeholder="填写或选择字段 (留空存整体)"
          />
        </div>
        <span style={{ fontSize: 12, color: 'var(--text-tertiary)', flexShrink: 0 }}>→</span>
        <span style={{ fontSize: 12, color: 'var(--text-tertiary)', flexShrink: 0 }}>setter</span>
        <div style={{ flex: '1 1 220px', minWidth: 170 }}>
          <SetterPathInput
            value={s.setter ?? ''}
            onChange={(v) => {
              const arr = [...list];
              arr[i] = { ...arr[i], setter: v };
              set(arr);
            }}
            actions={actions ?? {}}
            listens={listens ?? {}}
            stateExtra={stateExtra}
            luaScripts={luaScripts}
          />
        </div>
      </div>
    ),
  }));

  return (
    <div style={{ paddingLeft: 0 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
        <strong>{label ?? 'store'} ({list.length})</strong>
        <Button
          size="small"
          icon={<PlusOutlined />}
          onClick={() => set([...list, { setter: '' }])}
        >
          添加映射
        </Button>
      </div>
      <Collapse items={items} size="small" />
    </div>
  );
}
