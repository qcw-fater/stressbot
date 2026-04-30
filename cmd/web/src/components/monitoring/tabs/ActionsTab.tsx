/**
 * 动作明细表：每条 ActionMetric 一行，按 sampleCount 倒排。
 */

import { Empty, Input, Space, Table, Tag } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useMemo, useState } from 'react';
import { useRuntimeStore } from '@/services';
import type { ActionMetric } from '@/types/api';
import { ApdexCell } from '../shared/ApdexCell';
import { LatencyHistogram } from '../shared/LatencyHistogram';

export function ActionsTab() {
  const latestStress = useRuntimeStore((s) => s.latestStress);
  const [search, setSearch] = useState('');

  const dataSource = useMemo(() => {
    if (!latestStress) return [];
    const rows = latestStress.actions;
    if (!search) return rows;
    const lo = search.toLowerCase();
    return rows.filter((a) => a.name.toLowerCase().includes(lo));
  }, [latestStress, search]);

  const columns: ColumnsType<ActionMetric> = [
    {
      title: '动作名',
      dataIndex: 'name',
      key: 'name',
      render: (v: string) => {
        if (v.startsWith('callback:')) {
          return (
            <span>
              <Tag color="orange">推送</Tag>
              <code>{v.slice('callback:'.length)}</code>
            </span>
          );
        }
        return <code>{v}</code>;
      },
    },
    { title: '并发', dataIndex: 'executing', key: 'executing', width: 70, sorter: (a, b) => a.executing - b.executing },
    { title: '样本', dataIndex: 'sampleCount', key: 'sampleCount', width: 80, sorter: (a, b) => a.sampleCount - b.sampleCount, defaultSortOrder: 'descend' },
    {
      title: '成功率',
      dataIndex: 'successRate',
      key: 'successRate',
      width: 90,
      render: (v: number) => (
        <span style={{ color: v >= 0.99 ? '#52c41a' : v >= 0.9 ? '#faad14' : '#f5222d', fontVariantNumeric: 'tabular-nums' }}>
          {(v * 100).toFixed(1)}%
        </span>
      ),
      sorter: (a, b) => a.successRate - b.successRate,
    },
    {
      title: 'Apdex',
      dataIndex: 'apdex',
      key: 'apdex',
      width: 90,
      render: (v: number) => <ApdexCell value={v} />,
      sorter: (a, b) => a.apdex - b.apdex,
    },
    { title: 'QPS', dataIndex: 'avgQps', key: 'avgQps', width: 80, render: (v: number) => v.toFixed(1) },
    {
      title: '延迟分布',
      key: 'latency',
      width: 200,
      render: (_, r) => <LatencyHistogram hist={r.latency} />,
    },
    {
      title: 'p99',
      key: 'p99',
      width: 80,
      render: (_, r) => (r.latency.count > 0 ? `${r.latency.p99Ms.toFixed(0)}ms` : '—'),
      sorter: (a, b) => a.latency.p99Ms - b.latency.p99Ms,
    },
    {
      title: '错误',
      key: 'errors',
      width: 80,
      render: (_, r) =>
        r.errors && r.errors.length > 0 ? <Tag color="error">{r.errors.length}</Tag> : <span style={{ color: 'var(--text-tertiary)' }}>—</span>,
    },
  ];

  if (!latestStress) {
    return <Empty description="暂无压测数据" />;
  }

  return (
    <Space direction="vertical" size={8} style={{ width: '100%' }}>
      <Input.Search
        placeholder="按动作名搜索"
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        allowClear
        style={{ maxWidth: 320 }}
      />
      <Table<ActionMetric>
        rowKey="name"
        size="small"
        dataSource={dataSource}
        columns={columns}
        pagination={false}
        scroll={{ y: 'calc(70vh - 200px)' }}
        expandable={{
          expandedRowRender: (r) => (
            <div style={{ fontSize: 12 }}>
              <div>
                成功 {r.successCount} · 失败 {r.failureCount} · 超时 {r.timeoutCount} · 跳过 {r.skippedCount}
              </div>
              <div style={{ marginTop: 4 }}>
                avgSend {r.avgSendBytes}B · avgRecv {r.avgRecvBytes}B · timeoutAvgMs {r.timeoutAvgMs}
              </div>
              {r.errors && r.errors.length > 0 && (
                <div style={{ marginTop: 6 }}>
                  <strong>错误：</strong>
                  {r.errors.map((e) => (
                    <div key={e.msg}>
                      <Tag color="error">×{e.count}</Tag>
                      {e.msg}
                    </div>
                  ))}
                </div>
              )}
            </div>
          ),
        }}
      />
    </Space>
  );
}
