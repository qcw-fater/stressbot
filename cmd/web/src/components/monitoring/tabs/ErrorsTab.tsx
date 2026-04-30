/**
 * 跨动作错误汇总：把所有 ActionMetric.errors 平铺，按 count 倒排。
 *
 * 这是排查"为什么这么多失败"的第一站：先看错误集中在哪几条 msg 上。
 */

import { Empty, Table, Tag } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useMemo } from 'react';
import { useRuntimeStore } from '@/services';

interface ErrorRow {
  key: string;
  action: string;
  msg: string;
  count: number;
}

export function ErrorsTab() {
  const latestStress = useRuntimeStore((s) => s.latestStress);

  const rows = useMemo<ErrorRow[]>(() => {
    if (!latestStress) return [];
    const out: ErrorRow[] = [];
    for (const a of latestStress.actions) {
      if (!a.errors) continue;
      for (const e of a.errors) {
        out.push({ key: `${a.name}::${e.msg}`, action: a.name, msg: e.msg, count: e.count });
      }
    }
    out.sort((a, b) => b.count - a.count);
    return out;
  }, [latestStress]);

  if (!latestStress) return <Empty description="暂无压测数据" />;
  if (rows.length === 0) return <Empty description="目前没有错误，运行良好" />;

  const total = rows.reduce((sum, r) => sum + r.count, 0);

  const columns: ColumnsType<ErrorRow> = [
    { title: '#', key: 'idx', width: 40, render: (_, __, i) => i + 1 },
    {
      title: '动作',
      dataIndex: 'action',
      key: 'action',
      width: 220,
      render: (v: string) =>
        v.startsWith('callback:') ? (
          <span>
            <Tag color="orange">推送</Tag>
            <code>{v.slice('callback:'.length)}</code>
          </span>
        ) : (
          <code>{v}</code>
        ),
    },
    {
      title: '次数',
      dataIndex: 'count',
      key: 'count',
      width: 100,
      render: (v: number) => (
        <Tag color="error" style={{ fontVariantNumeric: 'tabular-nums', minWidth: 56, textAlign: 'center' }}>
          ×{v}
        </Tag>
      ),
      sorter: (a, b) => a.count - b.count,
      defaultSortOrder: 'descend',
    },
    {
      title: '错误信息',
      dataIndex: 'msg',
      key: 'msg',
      ellipsis: true,
      render: (v: string) => <span style={{ fontFamily: 'monospace', fontSize: 12 }}>{v}</span>,
    },
  ];

  return (
    <div>
      <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginBottom: 8 }}>
        共 {rows.length} 类错误，累计 <strong>{total}</strong> 次
      </div>
      <Table<ErrorRow>
        rowKey="key"
        size="small"
        dataSource={rows}
        columns={columns}
        pagination={{ pageSize: 50, showSizeChanger: false }}
        scroll={{ y: 'calc(70vh - 220px)' }}
      />
    </div>
  );
}
