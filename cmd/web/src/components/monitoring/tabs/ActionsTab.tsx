/**
 * 动作明细表（v5）。
 *
 * 列设计原则：
 *   - 关键计数（成 / 败 / 超 / 跳 / avgSend / avgRecv / timeoutAvgMs）**全部独立列**，
 *     用户要直接对比这些字段，塞在动作名下方小字里不够直观；
 *   - 动作名列收紧到 200px，留出横向空间给数值列；
 *   - 所有数值列、动作名、QPS 等都加 `sorter`；
 *   - 延迟分布 avg / p50 / p95 / p99 / max 五列单独可排序；
 *   - 延迟列表头统一加 `(ms)` 后缀，单元格保持纯数字（避免每格重复单位拖宽列）；
 *   - 移除"成功率"列：成功 / 失败 / 超时已按计数独立展示，比例可口算或在 Apdex 中体现，
 *     去掉冗余列让横向空间留给延迟分布；
 *   - 顶部加"仅展示动作"开关，过滤掉 callback:* 行（监听/推送），
 *     适用于关心业务请求耗时、不关心异步推送的场景。
 *   - 表格 horizontal scroll：列多了不会挤压，宽度可控。
 */

import { Empty, Input, Space, Switch, Table, Tag } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useMemo, useState } from 'react';
import { useRuntimeStore } from '@/services';
import type { ActionMetric } from '@/types/api';
import { ApdexCell } from '../shared/ApdexCell';
import { fmtBytesPlain, fmtMs, NUMERIC_STYLE } from '../shared/formats';

export function ActionsTab() {
  const latestStress = useRuntimeStore((s) => s.latestStress);
  const [search, setSearch] = useState('');
  const [actionsOnly, setActionsOnly] = useState(false);

  const dataSource = useMemo(() => {
    if (!latestStress) return [];
    let rows = latestStress.actions ?? [];
    if (actionsOnly) {
      // callback:<name> 是 listen / 推送回调，actionsOnly 时过滤掉
      rows = rows.filter((a) => !a.name.startsWith('callback:'));
    }
    if (search) {
      const lo = search.toLowerCase();
      rows = rows.filter((a) => a.name.toLowerCase().includes(lo));
    }
    return rows;
  }, [latestStress, search, actionsOnly]);

  const columns: ColumnsType<ActionMetric> = [
    {
      title: '动作',
      dataIndex: 'name',
      key: 'name',
      width: 200,
      fixed: 'left',
      ellipsis: true,
      sorter: (a, b) => a.name.localeCompare(b.name),
      render: (v: string) => {
        const isCallback = v.startsWith('callback:');
        const display = isCallback ? v.slice('callback:'.length) : v;
        return (
          <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
            {isCallback && <Tag color="orange" style={{ marginInlineEnd: 0 }}>推送</Tag>}
            <code style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={display}>
              {display}
            </code>
          </div>
        );
      },
    },
    {
      title: '并发',
      dataIndex: 'executing',
      key: 'executing',
      width: 64,
      sorter: (a, b) => a.executing - b.executing,
      render: (v: number) => <span style={NUMERIC_STYLE}>{v}</span>,
    },
    {
      title: '样本',
      dataIndex: 'sampleCount',
      key: 'sampleCount',
      width: 70,
      sorter: (a, b) => a.sampleCount - b.sampleCount,
      defaultSortOrder: 'descend',
      render: (v: number) => <span style={NUMERIC_STYLE}>{v}</span>,
    },
    // ── 计数：独立列 ───────────────────────
    {
      title: '成功',
      dataIndex: 'successCount',
      key: 'successCount',
      width: 70,
      sorter: (a, b) => a.successCount - b.successCount,
      render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: 'var(--color-success)' }}>{v}</span>,
    },
    {
      title: '失败',
      dataIndex: 'failureCount',
      key: 'failureCount',
      width: 70,
      sorter: (a, b) => a.failureCount - b.failureCount,
      render: (v: number) => (
        <span style={{ ...NUMERIC_STYLE, color: v > 0 ? 'var(--color-error)' : 'var(--text-tertiary)' }}>{v}</span>
      ),
    },
    {
      title: '超时',
      dataIndex: 'timeoutCount',
      key: 'timeoutCount',
      width: 70,
      sorter: (a, b) => a.timeoutCount - b.timeoutCount,
      render: (v: number) => (
        <span style={{ ...NUMERIC_STYLE, color: v > 0 ? 'var(--color-orange)' : 'var(--text-tertiary)' }}>{v}</span>
      ),
    },
    {
      title: '跳过',
      dataIndex: 'skippedCount',
      key: 'skippedCount',
      width: 70,
      sorter: (a, b) => a.skippedCount - b.skippedCount,
      render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: 'var(--text-tertiary)' }}>{v}</span>,
    },
    {
      title: 'Apdex',
      dataIndex: 'apdex',
      key: 'apdex',
      width: 80,
      sorter: (a, b) => a.apdex - b.apdex,
      render: (v: number) => <ApdexCell value={v} />,
    },
    {
      title: 'QPS',
      dataIndex: 'avgQps',
      key: 'avgQps',
      width: 78,
      sorter: (a, b) => a.avgQps - b.avgQps,
      render: (v: number) => <span style={NUMERIC_STYLE}>{v.toFixed(1)}</span>,
    },
    // ── 字节数：独立列，表头统一标 (B)，单元格只显示纯整数（避免 16B / 1.2KB 单位混排） ────
    {
      title: '↑avg(B)',
      dataIndex: 'avgSendBytes',
      key: 'avgSendBytes',
      width: 86,
      sorter: (a, b) => a.avgSendBytes - b.avgSendBytes,
      render: (v: number) => <span style={NUMERIC_STYLE}>{fmtBytesPlain(v)}</span>,
    },
    {
      title: '↓avg(B)',
      dataIndex: 'avgRecvBytes',
      key: 'avgRecvBytes',
      width: 86,
      sorter: (a, b) => a.avgRecvBytes - b.avgRecvBytes,
      render: (v: number) => <span style={NUMERIC_STYLE}>{fmtBytesPlain(v)}</span>,
    },
    // ── 延迟分布：独立列，表头统一标 (ms)，单元格只显示数字 ───────────────
    {
      title: 'avg(ms)',
      key: 'avgMs',
      width: 76,
      sorter: (a, b) => a.latency.avgMs - b.latency.avgMs,
      render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.avgMs)}</span>,
    },
    {
      title: 'p50(ms)',
      key: 'p50Ms',
      width: 76,
      sorter: (a, b) => a.latency.p50Ms - b.latency.p50Ms,
      render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.p50Ms)}</span>,
    },
    {
      title: 'p95(ms)',
      key: 'p95Ms',
      width: 76,
      sorter: (a, b) => a.latency.p95Ms - b.latency.p95Ms,
      render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.p95Ms)}</span>,
    },
    {
      title: 'p99(ms)',
      key: 'p99Ms',
      width: 76,
      sorter: (a, b) => a.latency.p99Ms - b.latency.p99Ms,
      render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.p99Ms)}</span>,
    },
    {
      title: 'max(ms)',
      key: 'maxMs',
      width: 76,
      sorter: (a, b) => a.latency.maxMs - b.latency.maxMs,
      render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.maxMs)}</span>,
    },
    {
      title: '超均(ms)',
      dataIndex: 'timeoutAvgMs',
      key: 'timeoutAvgMs',
      width: 84,
      sorter: (a, b) => a.timeoutAvgMs - b.timeoutAvgMs,
      render: (v: number) => <span style={NUMERIC_STYLE}>{fmtMs(v)}</span>,
    },
    {
      title: '错误',
      key: 'errors',
      width: 70,
      fixed: 'right',
      sorter: (a, b) => (a.errors?.length ?? 0) - (b.errors?.length ?? 0),
      render: (_, r) =>
        r.errors && r.errors.length > 0 ? (
          <Tag color="error" style={{ marginInlineEnd: 0 }}>{r.errors.length}</Tag>
        ) : (
          <span style={{ color: 'var(--text-tertiary)' }}>—</span>
        ),
    },
  ];

  if (!latestStress) {
    return <Empty description="暂无压测数据" />;
  }

  return (
    <Space direction="vertical" size={8} style={{ width: '100%' }}>
      <Space size={12}>
        <Input.Search
          placeholder="按动作名搜索"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          allowClear
          style={{ width: 320 }}
        />
        <Space size={6}>
          <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>仅展示动作（隐藏推送）</span>
          <Switch checked={actionsOnly} onChange={setActionsOnly} size="small" />
        </Space>
        <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>
          共 {dataSource.length} 条
        </span>
      </Space>
      <Table<ActionMetric>
        rowKey="name"
        size="small"
        dataSource={dataSource}
        columns={columns}
        pagination={false}
        scroll={{ x: 'max-content', y: 'calc(70vh - 220px)' }}
        expandable={{
          rowExpandable: (r) => !!r.errors && r.errors.length > 0,
          expandedRowRender: (r) => (
            <div style={{ fontSize: 12 }}>
              <strong>错误明细：</strong>
              {r.errors!.map((e) => (
                <div key={e.msg} style={{ marginTop: 2 }}>
                  <Tag color="error">×{e.count}</Tag>
                  {e.msg}
                </div>
              ))}
            </div>
          ),
        }}
      />
    </Space>
  );
}
