/**
 * HeaderFieldTable — header 字段表（每行一字段）。
 *
 * 列：name / offset / size / type（下拉） / endian（le|be|默认） / role（下拉） / 操作（↑↓ 移序、删除）。
 * 表底「+ 添加字段」。type 宽度即时提示用 FIELD_TYPE_WIDTH（不阻塞输入；最终校验交给 validateCodecSchema）。
 * 修改经 codecEdit helper → onEdit 回灌 content。
 */

import { ArrowDownOutlined, ArrowUpOutlined, DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import { Button, InputNumber, Select, Space, Table, Tooltip, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import type { CodecSchema, Field } from '@/types/codec';
import { FIELD_ROLES, FIELD_TYPES, FIELD_TYPE_WIDTH } from '@/types/codec';
import {
  addHeaderField,
  moveHeaderField,
  removeHeaderField,
  updateHeaderField,
} from './codecEdit';
import { computeByteRanges } from './byteLayout';

export interface HeaderFieldTableProps {
  raw: Record<string, unknown>;
  schema: CodecSchema;
  selectedIndex: number | null;
  onSelect: (index: number | null) => void;
  onEdit: (nextContent: string) => void;
}

export function HeaderFieldTable({ raw, schema, selectedIndex, onSelect, onEdit }: HeaderFieldTableProps) {
  const fields: Field[] = schema.header ?? [];
  const headerSize: number = schema.frame?.headerSize ?? 0;
  const ranges = computeByteRanges(fields, headerSize);

  const typeOptions = FIELD_TYPES.map((t) => {
    const w = FIELD_TYPE_WIDTH[t];
    return { value: t, label: w > 0 ? `${t}（${w} 字节）` : `${t}（变长）` };
  });

  const roleOptions = FIELD_ROLES.map((r) => ({ value: r, label: r }));

  const columns: ColumnsType<Field> = [
    {
      title: 'name',
      dataIndex: 'name',
      width: 140,
      render: (_, record, idx) => (
        <input
          className="flet-input"
          value={record.name ?? ''}
          placeholder="字段名"
          onChange={(e) => onEdit(updateHeaderField(raw, idx, { name: e.target.value }))}
        />
      ),
    },
    {
      title: 'offset',
      dataIndex: 'offset',
      width: 80,
      render: (_, record, idx) => (
        <InputNumber
          size="small"
          min={0}
          value={record.offset}
          style={{ width: 64 }}
          onChange={(v) => onEdit(updateHeaderField(raw, idx, { offset: typeof v === 'number' ? v : 0 }))}
        />
      ),
    },
    {
      title: 'size',
      dataIndex: 'size',
      width: 90,
      render: (_, record, idx) => (
        <Space size={4} direction="vertical" style={{ lineHeight: 1 }}>
          <InputNumber
            size="small"
            min={0}
            value={record.size}
            style={{ width: 64 }}
            onChange={(v) => onEdit(updateHeaderField(raw, idx, { size: typeof v === 'number' ? v : 0 }))}
          />
          {record.type && FIELD_TYPE_WIDTH[record.type] > 0 && record.size !== FIELD_TYPE_WIDTH[record.type] && (
            <Typography.Text type="warning" style={{ fontSize: 10 }}>
              {record.type} 应为 {FIELD_TYPE_WIDTH[record.type]}
            </Typography.Text>
          )}
        </Space>
      ),
    },
    {
      title: 'type',
      dataIndex: 'type',
      width: 130,
      render: (_, record, idx) => (
        <Select
          size="small"
          value={record.type}
          style={{ width: 116 }}
          options={typeOptions}
          onChange={(v) => onEdit(updateHeaderField(raw, idx, { type: v }))}
        />
      ),
    },
    {
      title: 'endian',
      dataIndex: 'endian',
      width: 110,
      render: (_, record, idx) => (
        <Select
          size="small"
          value={record.endian ?? '__default__'}
          style={{ width: 96 }}
          options={[
            { value: '__default__', label: `默认（${schema.endianDefault ?? 'le'}）` },
            { value: 'le', label: 'le' },
            { value: 'be', label: 'be' },
          ]}
          onChange={(v) =>
            onEdit(
              updateHeaderField(raw, idx, { endian: v === '__default__' ? undefined : (v as string) }),
            )
          }
        />
      ),
    },
    {
      title: 'role',
      dataIndex: 'role',
      width: 130,
      render: (_, record, idx) => (
        <Select
          size="small"
          value={record.role}
          style={{ width: 116 }}
          options={roleOptions}
          onChange={(v) => onEdit(updateHeaderField(raw, idx, { role: v }))}
        />
      ),
    },
    {
      title: '操作',
      key: 'op',
      width: 110,
      render: (_, _record, idx) => (
        <Space size={2}>
          <Tooltip title="上移">
            <Button
              type="text"
              size="small"
              icon={<ArrowUpOutlined />}
              disabled={idx === 0}
              onClick={() => {
                onEdit(moveHeaderField(raw, idx, -1));
                // 选中态跟随被移动的字段；越界（按钮已 disabled，防御）时不变。
                if (idx - 1 >= 0) onSelect(idx - 1);
              }}
            />
          </Tooltip>
          <Tooltip title="下移">
            <Button
              type="text"
              size="small"
              icon={<ArrowDownOutlined />}
              disabled={idx === fields.length - 1}
              onClick={() => {
                onEdit(moveHeaderField(raw, idx, 1));
                if (idx + 1 < fields.length) onSelect(idx + 1);
              }}
            />
          </Tooltip>
          <Tooltip title="删除">
            <Button
              type="text"
              size="small"
              danger
              icon={<DeleteOutlined />}
              onClick={() => {
                onEdit(removeHeaderField(raw, idx));
                if (selectedIndex === idx) onSelect(null);
              }}
            />
          </Tooltip>
        </Space>
      ),
    },
  ];

  return (
    <div>
      <Table<Field>
        rowKey={(_record, idx) => String(idx)}
        size="small"
        dataSource={fields}
        columns={columns}
        pagination={false}
        scroll={{ y: 280 }}
        onRow={(_record, idx) => ({
          onClick: () => onSelect(typeof idx === 'number' ? idx : null),
          style: {
            cursor: 'pointer',
            background: typeof idx === 'number' && selectedIndex === idx ? 'var(--primary-color, #e6f4ff)' : undefined,
          },
        })}
        rowClassName={(_record, idx) => (ranges[idx]?.bad ? 'flet-row-bad' : '')}
      />
      <Button
        size="small"
        type="dashed"
        icon={<PlusOutlined />}
        style={{ marginTop: 8 }}
        onClick={() => {
          const next: Field = {
            name: `field${fields.length}`,
            offset: headerSize,
            size: 1,
            type: 'u8',
            role: 'reserved',
          };
          onEdit(addHeaderField(raw, next));
          onSelect(fields.length);
        }}
      >
        添加字段
      </Button>
    </div>
  );
}
