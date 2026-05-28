/**
 * 动作明细表（v5）。
 *
 * 列设计原则：
 *   - 关键计数（成 / 败 / 超 / 取消）**全部独立列**，
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

import { Empty, Input, Popover, Space, Switch, Table, Tag, Tooltip } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useMemo, useState } from 'react';
import { useRuntimeStore } from '@/services';
import type { ActionMetric } from '@/types/api';
import { ApdexCell } from '../shared/ApdexCell';
import { fmtBytes, fmtMs, NUMERIC_STYLE } from '../shared/formats';

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
            <Tooltip title={display} mouseEnterDelay={0.4}>
              <code style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {display}
              </code>
            </Tooltip>
          </div>
        );
      },
    },
    { title: '样本', dataIndex: 'sampleCount', key: 'sampleCount', width: 70, sorter: (a, b) => a.sampleCount - b.sampleCount, defaultSortOrder: 'descend' as const, render: (v: number) => <span style={NUMERIC_STYLE}>{v}</span> },
    { title: '成功', dataIndex: 'successCount', key: 'successCount', width: 70, sorter: (a, b) => a.successCount - b.successCount, render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: 'var(--color-success)' }}>{v}</span> },
    { title: '失败', dataIndex: 'failureCount', key: 'failureCount', width: 70, sorter: (a, b) => a.failureCount - b.failureCount, render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: v > 0 ? 'var(--color-error)' : 'var(--text-tertiary)' }}>{v}</span> },
    { title: '超时', dataIndex: 'timeoutCount', key: 'timeoutCount', width: 70, sorter: (a, b) => a.timeoutCount - b.timeoutCount, render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: v > 0 ? 'var(--color-orange)' : 'var(--text-tertiary)' }}>{v}</span> },
    { title: '取消', dataIndex: 'canceledCount', key: 'canceledCount', width: 70, sorter: (a, b) => a.canceledCount - b.canceledCount, render: (v: number) => <span style={NUMERIC_STYLE}>{v}</span> },
    // 延迟列均反映"纯网络往返"耗时（不含客户端构建/解析）。
    // 当 rttSampleCount=0（如纯本地 setState / Lua 内仅做 connect 等）时显示 — 以避免误导。
    {
      title: <Tooltip title="从客户端请求发送完成，到客户端收到完整响应帧；不包含客户端解码、解析和状态写入耗时">RTT avg(ms)</Tooltip>,
      key: 'avgMs', width: 90,
      sorter: (a, b) => a.rtt.avgMs - b.rtt.avgMs,
      render: (_, r) => <span style={NUMERIC_STYLE}>{r.rttSampleCount > 0 ? fmtMs(r.rtt.avgMs) : '—'}</span>,
    },
    { title: 'p50(ms)', key: 'p50Ms', width: 76, sorter: (a, b) => a.rtt.p50Ms - b.rtt.p50Ms, render: (_, r) => <span style={NUMERIC_STYLE}>{r.rttSampleCount > 0 ? fmtMs(r.rtt.p50Ms) : '—'}</span> },
    { title: 'p95(ms)', key: 'p95Ms', width: 76, sorter: (a, b) => a.rtt.p95Ms - b.rtt.p95Ms, render: (_, r) => <span style={NUMERIC_STYLE}>{r.rttSampleCount > 0 ? fmtMs(r.rtt.p95Ms) : '—'}</span> },
    { title: 'p99(ms)', key: 'p99Ms', width: 76, sorter: (a, b) => a.rtt.p99Ms - b.rtt.p99Ms, render: (_, r) => <span style={NUMERIC_STYLE}>{r.rttSampleCount > 0 ? fmtMs(r.rtt.p99Ms) : '—'}</span> },
    { title: 'max(ms)', key: 'maxMs', width: 76, sorter: (a, b) => a.rtt.maxMs - b.rtt.maxMs, render: (_, r) => <span style={NUMERIC_STYLE}>{r.rttSampleCount > 0 ? fmtMs(r.rtt.maxMs) : '—'}</span> },
    {
      title: <Tooltip title="压测工具端平均开销，约等于动作总耗时扣除 RTT 后的客户端处理时间。">client(ms)</Tooltip>,
      dataIndex: 'clientAvgMs', key: 'clientAvgMs', width: 90,
      sorter: (a, b) => a.clientAvgMs - b.clientAvgMs,
      render: (v: number) => <span style={NUMERIC_STYLE}>{fmtMs(v)}</span>,
    },
    { title: <Tooltip title="协议编码平均耗时。">encode(ms)</Tooltip>, dataIndex: 'encodeAvgMs', key: 'encodeAvgMs', width: 92, sorter: (a, b) => a.encodeAvgMs - b.encodeAvgMs, render: (v: number) => <span style={NUMERIC_STYLE}>{fmtMs(v)}</span> },
    { title: <Tooltip title="收到完整响应帧后的协议解码平均耗时，不计入 RTT。">decode(ms)</Tooltip>, dataIndex: 'decodeAvgMs', key: 'decodeAvgMs', width: 92, sorter: (a, b) => a.decodeAvgMs - b.decodeAvgMs, render: (v: number) => <span style={NUMERIC_STYLE}>{fmtMs(v)}</span> },
    { title: <Tooltip title="响应 protobuf 解析与状态写入平均耗时。">parse/store(ms)</Tooltip>, dataIndex: 'parseStoreAvgMs', key: 'parseStoreAvgMs', width: 120, sorter: (a, b) => a.parseStoreAvgMs - b.parseStoreAvgMs, render: (v: number) => <span style={NUMERIC_STYLE}>{fmtMs(v)}</span> },
    {
      title: <Tooltip title="平均每次成功发送的字节数">↑发送(均)</Tooltip>,
      dataIndex: 'avgSendBytes', key: 'avgSendBytes', width: 80,
      sorter: (a, b) => a.avgSendBytes - b.avgSendBytes,
      render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: 'var(--chart-cyan)' }}>{fmtBytes(v)}</span>,
    },
    {
      title: <Tooltip title="平均每次成功接收的字节数">↓接收(均)</Tooltip>,
      dataIndex: 'avgRecvBytes', key: 'avgRecvBytes', width: 80,
      sorter: (a, b) => a.avgRecvBytes - b.avgRecvBytes,
      render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: 'var(--chart-purple)' }}>{fmtBytes(v)}</span>,
    },
    { title: '并发', dataIndex: 'executing', key: 'executing', width: 64, sorter: (a, b) => a.executing - b.executing, render: (v: number) => <span style={NUMERIC_STYLE}>{v}</span> },
    { title: 'QPS', dataIndex: 'avgQps', key: 'avgQps', width: 78, sorter: (a, b) => a.avgQps - b.avgQps, render: (v: number) => <span style={NUMERIC_STYLE}>{v.toFixed(1)}</span> },
    { title: 'Apdex', dataIndex: 'apdex', key: 'apdex', width: 80, sorter: (a, b) => a.apdex - b.apdex, render: (_, r) => <ApdexCell value={r.apdex} rttSampleCount={r.rttSampleCount} /> },
    {
      title: '错误',
      key: 'errors',
      width: 70,
      fixed: 'right',
      sorter: (a, b) => (a.errors?.length ?? 0) - (b.errors?.length ?? 0),
      render: (_, r) => {
        if (!r.errors?.length) return <span style={{ color: 'var(--text-tertiary)' }}>—</span>;
        return (
          <Popover
            content={
              <div style={{ maxWidth: 360 }}>
                {r.errors.map((e) => (
                  <div key={`${e.kind}:${e.code}`} style={{ marginTop: 3, fontSize: 11, lineHeight: '16px' }}>
                    <span style={{ color: 'var(--color-error)', fontWeight: 700, fontSize: 10, fontVariantNumeric: 'tabular-nums', marginRight: 6 }}>×{e.count}</span>
                    <span style={{ fontWeight: 500 }}>{e.codeName || `${e.kind}#${e.code}`}</span>
                    {e.msgs.length > 0 && (
                      <span style={{ color: 'var(--text-tertiary)', marginLeft: 6 }}>{e.msgs.join('; ')}</span>
                    )}
                  </div>
                ))}
              </div>
            }
            title={<span style={{ fontSize: 12 }}>错误明细</span>}
            mouseEnterDelay={0.3}
          >
            <Tag color="error" style={{ marginInlineEnd: 0, cursor: 'pointer' }}>{r.errors.length}</Tag>
          </Popover>
        );
      },
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
      />
    </Space>
  );
}
