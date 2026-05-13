/**
 * StoreMapping 列表编辑器（S2C 字段 → state key）。
 *
 * 复用：ActionEditor（tcpRequest/udpRequest/tcpListen/udpListen 的 store）
 *      ListenEditor（declarative 形态的 store）
 */

import { Button, Input, Space, Table, Tooltip } from 'antd';
import { ArrowDownOutlined, ArrowUpOutlined, DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import type { StoreMapping } from '@/types/action';
import { ProtoPathInput } from './ProtoPathInput';

export interface StoreTableProps {
  s2cProto?: string;
  value?: StoreMapping[];
  onChange?: (v: StoreMapping[]) => void;
}

export function StoreTable({ s2cProto, value, onChange }: StoreTableProps) {
  const list = value ?? [];
  const set = (next: StoreMapping[]) => onChange?.(next);

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
        <strong>store: S2C 响应字段 → state key</strong>
        <Button icon={<PlusOutlined />} size="small" onClick={() => set([...list, { setter: '' }])}>
          添加
        </Button>
      </div>
      <Table
        size="small"
        dataSource={list.map((s, i) => ({ ...s, _i: i }))}
        rowKey="_i"
        pagination={false}
        locale={{ emptyText: '尚无映射' }}
        columns={[
          {
            title: (
              <Tooltip title="留空 = 存储整个 S2C 消息">
                提取字段 (支持嵌套)
              </Tooltip>
            ),
            dataIndex: 'field',
            width: 380,
            render: (_, r) => (
              <ProtoPathInput
                messageFullName={s2cProto}
                value={r.field}
                onChange={(v) => {
                  const arr = [...list];
                  arr[r._i] = { ...arr[r._i], field: v || undefined };
                  set(arr);
                }}
                placeholder="填写或选择字段 (留空存整体)"
              />
            ),
          },
          {
            title: 'setter（state key）',
            dataIndex: 'setter',
            render: (_, r) => (
              <Input
                value={r.setter ?? ''}
                onChange={(e) => {
                  const arr = [...list];
                  arr[r._i] = { ...arr[r._i], setter: e.target.value };
                  set(arr);
                }}
              />
            ),
          },
          {
            title: '操作',
            width: 100,
            render: (_, r) => (
              <Space size={4}>
                <Button
                  size="small"
                  icon={<ArrowUpOutlined />}
                  disabled={r._i === 0}
                  onClick={() => {
                    const arr = [...list];
                    [arr[r._i - 1], arr[r._i]] = [arr[r._i], arr[r._i - 1]];
                    set(arr);
                  }}
                />
                <Button
                  size="small"
                  icon={<ArrowDownOutlined />}
                  disabled={r._i === list.length - 1}
                  onClick={() => {
                    const arr = [...list];
                    [arr[r._i], arr[r._i + 1]] = [arr[r._i + 1], arr[r._i]];
                    set(arr);
                  }}
                />
                <Button
                  size="small"
                  danger
                  icon={<DeleteOutlined />}
                  onClick={() => set(list.filter((_, j) => j !== r._i))}
                />
              </Space>
            ),
          },
        ]}
      />
    </div>
  );
}
