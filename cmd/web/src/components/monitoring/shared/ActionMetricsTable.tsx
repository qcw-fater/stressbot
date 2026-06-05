import { Input, Popover, Segmented, Space, Switch, Table, Tag, Tooltip } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useMemo, useState } from 'react';
import type { ErrorEntry } from '@/types/api';
import { ApdexCell } from './ApdexCell';
import { fmtBytes, fmtMs, NUMERIC_STYLE } from './formats';

export type ActionLatencyMode = 'totalDuration' | 'rtt';

export interface ActionHistogramLike {
  maxMs: number;
  avgMs: number;
  p50Ms: number;
  p95Ms: number;
  p99Ms: number;
}

export interface ActionMetricsTableRow {
  name: string;
  sampleCount: number;
  successCount: number;
  failureCount: number;
  timeoutCount: number;
  canceledCount?: number;
  executing: number;
  successRate: number;
  avgSendBytes: number;
  avgRecvBytes: number;
  avgQps: number;
  errors?: ErrorEntry[];

  rtt?: ActionHistogramLike;
  rttSampleCount?: number;
  rttApdex?: number;

  totalDuration?: ActionHistogramLike;
  totalDurationSampleCount?: number;
  totalDurationApdex?: number;

  clientAvgMs?: number;
  encodeAvgMs?: number;
  decodeAvgMs?: number;
  parseStoreAvgMs?: number;
}

export interface ActionMetricsTableProps<T extends ActionMetricsTableRow> {
  rows: T[];
  loading?: boolean;
  compact?: boolean;
  size?: 'small' | 'middle';
  scrollY?: number | string;
  popupZIndex?: number;

  latencyMode?: ActionLatencyMode;
  onLatencyModeChange?: (mode: ActionLatencyMode) => void;
  showLatencyModeSwitch?: boolean;

  showToolbar?: boolean;
  showCanceledColumn?: boolean;
  showClientBreakdown?: boolean;
  showBandwidthColumns?: boolean;
  showExecutingColumn?: boolean;
  showQpsColumn?: boolean;
  showErrorsColumn?: boolean;
  searchWidth?: number;
  actionsOnlyLabel?: string;
}

interface SelectedLatencyMetric {
  histogram?: ActionHistogramLike;
  sampleCount: number;
  apdex?: number;
}

function selectLatencyMetric(row: ActionMetricsTableRow, mode: ActionLatencyMode): SelectedLatencyMetric {
  if (mode === 'rtt') {
    return { histogram: row.rtt, sampleCount: row.rttSampleCount ?? 0, apdex: row.rttApdex };
  }
  return { histogram: row.totalDuration, sampleCount: row.totalDurationSampleCount ?? 0, apdex: row.totalDurationApdex };
}

function latencySortValue(row: ActionMetricsTableRow, mode: ActionLatencyMode, key: keyof ActionHistogramLike): number {
  const selected = selectLatencyMetric(row, mode);
  if (selected.sampleCount <= 0 || !selected.histogram) return -1;
  return selected.histogram[key];
}

function latencyTooltip(mode: ActionLatencyMode) {
  if (mode === 'rtt') {
    return 'RTT：从客户端请求发送完成，到客户端收到完整响应帧；不包含客户端解码、解析和状态写入耗时';
  }
  return '总耗时：动作从开始执行到结束的耗时，包含请求 RTT、编码/解码、监听等待、Lua 逻辑、解析与状态写入等';
}

export function ActionMetricsTable<T extends ActionMetricsTableRow>({
  rows,
  loading = false,
  compact = false,
  size = 'small',
  scrollY,
  popupZIndex,
  latencyMode,
  onLatencyModeChange,
  showLatencyModeSwitch = true,
  showToolbar = true,
  showCanceledColumn = true,
  showClientBreakdown = false,
  showBandwidthColumns = true,
  showExecutingColumn = true,
  showQpsColumn = true,
  showErrorsColumn = true,
  searchWidth,
  actionsOnlyLabel,
}: ActionMetricsTableProps<T>) {
  const [innerMode, setInnerMode] = useState<ActionLatencyMode>('totalDuration');
  const [search, setSearch] = useState('');
  const [actionsOnly, setActionsOnly] = useState(false);
  const [advancedDiagnostics, setAdvancedDiagnostics] = useState(false);

  const mode = latencyMode === undefined ? innerMode : latencyMode;
  const hasAdvancedDiagnostics = showCanceledColumn || showClientBreakdown;
  const setMode = (next: ActionLatencyMode) => {
    if (latencyMode === undefined) setInnerMode(next);
    onLatencyModeChange?.(next);
  };

  const dataSource = useMemo(() => {
    let out = rows;
    if (actionsOnly) out = out.filter((a) => !a.name.startsWith('callback:'));
    if (search) {
      const lo = search.toLowerCase();
      out = out.filter((a) => a.name.toLowerCase().includes(lo));
    }
    return out;
  }, [rows, search, actionsOnly]);

  const latencyTitle = mode === 'rtt' ? 'RTT avg(ms)' : '总耗时 avg(ms)';
  const nameWidth = compact ? 160 : 200;
  const countWidth = compact ? 60 : 70;
  const latencyWidth = compact ? 64 : 76;

  const columns: ColumnsType<T> = useMemo(() => {
    const latencyValue = (row: T, key: keyof ActionHistogramLike) => latencySortValue(row, mode, key);
    const renderLatency = (row: T, key: keyof ActionHistogramLike) => {
      const selected = selectLatencyMetric(row, mode);
      return <span style={NUMERIC_STYLE}>{selected.sampleCount > 0 && selected.histogram ? fmtMs(selected.histogram[key]) : '—'}</span>;
    };

    const cols: ColumnsType<T> = [
      {
        title: '动作', dataIndex: 'name', key: 'name', width: nameWidth, fixed: 'left', ellipsis: true,
        sorter: (a, b) => a.name.localeCompare(b.name),
        render: (v: string) => {
          const isCallback = v.startsWith('callback:');
          const display = isCallback ? v.slice('callback:'.length) : v;
          return (
            <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
              {isCallback && <Tag color="orange" style={{ marginInlineEnd: 0 }}>推送</Tag>}
              <Tooltip title={display} mouseEnterDelay={0.4}>
                <code style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{display}</code>
              </Tooltip>
            </div>
          );
        },
      },
      { title: '样本', dataIndex: 'sampleCount', key: 'sampleCount', width: countWidth, sorter: (a, b) => a.sampleCount - b.sampleCount, defaultSortOrder: 'descend' as const, render: (v: number) => <span style={NUMERIC_STYLE}>{v}</span> },
      { title: '成功', dataIndex: 'successCount', key: 'successCount', width: countWidth, sorter: (a, b) => a.successCount - b.successCount, render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: 'var(--color-success)' }}>{v}</span> },
      { title: '失败', dataIndex: 'failureCount', key: 'failureCount', width: compact ? 52 : 70, sorter: (a, b) => a.failureCount - b.failureCount, render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: v > 0 ? 'var(--color-error)' : 'var(--text-tertiary)' }}>{v}</span> },
      { title: '超时', dataIndex: 'timeoutCount', key: 'timeoutCount', width: compact ? 52 : 70, sorter: (a, b) => a.timeoutCount - b.timeoutCount, render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: v > 0 ? 'var(--color-orange)' : 'var(--text-tertiary)' }}>{v}</span> },
    ];

    if (showCanceledColumn && advancedDiagnostics) {
      cols.push({ title: '取消', dataIndex: 'canceledCount', key: 'canceledCount', width: 70, sorter: (a, b) => (a.canceledCount || 0) - (b.canceledCount || 0), render: (v: number | undefined) => <span style={NUMERIC_STYLE}>{typeof v === 'number' ? v : 0}</span> });
    }

    cols.push(
      {
        title: <Tooltip title={latencyTooltip(mode)}>{latencyTitle}</Tooltip>,
        key: 'avgMs', width: compact ? 68 : 92,
        sorter: (a, b) => latencyValue(a, 'avgMs') - latencyValue(b, 'avgMs'),
        render: (_, r) => renderLatency(r, 'avgMs'),
      },
      { title: 'p50(ms)', key: 'p50Ms', width: latencyWidth, sorter: (a, b) => latencyValue(a, 'p50Ms') - latencyValue(b, 'p50Ms'), render: (_, r) => renderLatency(r, 'p50Ms') },
      { title: 'p95(ms)', key: 'p95Ms', width: latencyWidth, sorter: (a, b) => latencyValue(a, 'p95Ms') - latencyValue(b, 'p95Ms'), render: (_, r) => renderLatency(r, 'p95Ms') },
      { title: 'p99(ms)', key: 'p99Ms', width: latencyWidth, sorter: (a, b) => latencyValue(a, 'p99Ms') - latencyValue(b, 'p99Ms'), render: (_, r) => renderLatency(r, 'p99Ms') },
      { title: 'max(ms)', key: 'maxMs', width: latencyWidth, sorter: (a, b) => latencyValue(a, 'maxMs') - latencyValue(b, 'maxMs'), render: (_, r) => renderLatency(r, 'maxMs') },
    );

    if (showClientBreakdown && advancedDiagnostics) {
      cols.push(
        { title: <Tooltip title="非 RTT 平均耗时，约等于动作总耗时扣除已记录 RTT 后的剩余耗时。">非RTT(ms)</Tooltip>, dataIndex: 'clientAvgMs', key: 'clientAvgMs', width: 92, sorter: (a, b) => (a.clientAvgMs || 0) - (b.clientAvgMs || 0), render: (v: number | undefined) => <span style={NUMERIC_STYLE}>{fmtMs(v || 0)}</span> },
        { title: <Tooltip title="协议编码平均耗时。">encode(ms)</Tooltip>, dataIndex: 'encodeAvgMs', key: 'encodeAvgMs', width: 92, sorter: (a, b) => (a.encodeAvgMs || 0) - (b.encodeAvgMs || 0), render: (v: number | undefined) => <span style={NUMERIC_STYLE}>{fmtMs(v || 0)}</span> },
        { title: <Tooltip title="收到完整响应帧后的协议解码平均耗时，不计入 RTT。">decode(ms)</Tooltip>, dataIndex: 'decodeAvgMs', key: 'decodeAvgMs', width: 92, sorter: (a, b) => (a.decodeAvgMs || 0) - (b.decodeAvgMs || 0), render: (v: number | undefined) => <span style={NUMERIC_STYLE}>{fmtMs(v || 0)}</span> },
        { title: <Tooltip title="响应 protobuf 解析与状态写入平均耗时。">parse/store(ms)</Tooltip>, dataIndex: 'parseStoreAvgMs', key: 'parseStoreAvgMs', width: 120, sorter: (a, b) => (a.parseStoreAvgMs || 0) - (b.parseStoreAvgMs || 0), render: (v: number | undefined) => <span style={NUMERIC_STYLE}>{fmtMs(v || 0)}</span> },
      );
    }

    if (showBandwidthColumns) {
      cols.push(
        { title: <Tooltip title="平均每次成功发送的字节数">↑发送(均)</Tooltip>, dataIndex: 'avgSendBytes', key: 'avgSendBytes', width: compact ? 72 : 80, sorter: (a, b) => a.avgSendBytes - b.avgSendBytes, render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: 'var(--chart-cyan)' }}>{fmtBytes(v)}</span> },
        { title: <Tooltip title="平均每次成功接收的字节数">↓接收(均)</Tooltip>, dataIndex: 'avgRecvBytes', key: 'avgRecvBytes', width: compact ? 72 : 80, sorter: (a, b) => a.avgRecvBytes - b.avgRecvBytes, render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: 'var(--chart-purple)' }}>{fmtBytes(v)}</span> },
      );
    }
    if (showExecutingColumn) cols.push({ title: '并发', dataIndex: 'executing', key: 'executing', width: compact ? 52 : 64, sorter: (a, b) => a.executing - b.executing, render: (v: number) => <span style={NUMERIC_STYLE}>{v}</span> });
    if (showQpsColumn) cols.push({ title: 'QPS', dataIndex: 'avgQps', key: 'avgQps', width: compact ? 60 : 78, sorter: (a, b) => a.avgQps - b.avgQps, render: (v: number) => <span style={NUMERIC_STYLE}>{v.toFixed(1)}</span> });

    cols.push({ title: 'Apdex', key: 'apdex', width: compact ? 68 : 80, sorter: (a, b) => (selectLatencyMetric(a, mode).apdex ?? -1) - (selectLatencyMetric(b, mode).apdex ?? -1), render: (_, r) => {
      const selected = selectLatencyMetric(r, mode);
      return <ApdexCell value={selected.apdex} sampleCount={selected.sampleCount} />;
    } });

    if (showErrorsColumn) {
      cols.push({
        title: '错误', key: 'errors', width: compact ? 52 : 70, fixed: 'right', sorter: (a, b) => (a.errors?.length || 0) - (b.errors?.length || 0),
        render: (_, r) => {
          if (!r.errors?.length) return <span style={{ color: 'var(--text-tertiary)' }}>—</span>;
          return (
            <Popover
              overlayStyle={popupZIndex ? { zIndex: popupZIndex } : undefined}
              content={(
                <div style={{ maxWidth: 360 }}>
                  {r.errors.map((e) => (
                    <div key={`${e.kind}:${e.code}`} style={{ marginTop: 3, fontSize: 11, lineHeight: '16px' }}>
                      <span style={{ color: 'var(--color-error)', fontWeight: 700, fontSize: 10, fontVariantNumeric: 'tabular-nums', marginRight: 6 }}>×{e.count}</span>
                      <span style={{ fontWeight: 500 }}>{e.codeName || `${e.kind}#${e.code}`}</span>
                      {e.msgs.length > 0 && <span style={{ color: 'var(--text-tertiary)', marginLeft: 6 }}>{e.msgs.join('; ')}</span>}
                    </div>
                  ))}
                </div>
              )}
              title={<span style={{ fontSize: 12 }}>错误明细</span>}
              mouseEnterDelay={0.3}
            >
              <Tag color="error" style={{ marginInlineEnd: 0, cursor: 'pointer' }}>{r.errors.length}</Tag>
            </Popover>
          );
        },
      });
    }
    return cols;
  }, [advancedDiagnostics, compact, latencyTitle, latencyWidth, mode, nameWidth, countWidth, popupZIndex, showBandwidthColumns, showCanceledColumn, showClientBreakdown, showErrorsColumn, showExecutingColumn, showQpsColumn]);

  return (
    <div className="action-metrics-table" style={{ width: '100%', minHeight: 0, display: 'flex', flexDirection: 'column', gap: 8 }}>
      {showToolbar && (
        <Space className="action-metrics-table__toolbar" size={12} wrap>
          <Input.Search
            placeholder="按动作名搜索"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            allowClear
            style={{ width: searchWidth || (compact ? 260 : 320) }}
            size={size === 'small' ? 'small' : 'middle'}
          />
          {showLatencyModeSwitch && (
            <Segmented<ActionLatencyMode>
              size={size === 'small' ? 'small' : 'middle'}
              value={mode}
              options={[{ label: '总耗时', value: 'totalDuration' }, { label: 'RTT', value: 'rtt' }]}
              onChange={(v) => setMode(v)}
            />
          )}
          <Space size={6}>
            <span style={{ fontSize: compact ? 11 : 12, color: 'var(--text-secondary)' }}>{actionsOnlyLabel || (compact ? '仅动作' : '仅展示动作（隐藏推送）')}</span>
            <Switch checked={actionsOnly} onChange={setActionsOnly} size="small" />
          </Space>
          {hasAdvancedDiagnostics && (
            <Space size={6}>
              <span style={{ fontSize: compact ? 11 : 12, color: 'var(--text-secondary)' }}>高级诊断</span>
              <Switch checked={advancedDiagnostics} onChange={setAdvancedDiagnostics} size="small" />
            </Space>
          )}
          <span style={{ fontSize: compact ? 11 : 12, color: 'var(--text-tertiary)' }}>{dataSource.length} 条</span>
        </Space>
      )}
      <div className="action-metrics-table__body" style={{ minHeight: 0 }}>
        <Table<T>
          rowKey="name"
          size={size}
          loading={loading}
          dataSource={dataSource}
          columns={columns}
          pagination={false}
          scroll={{ x: 'max-content', y: scrollY }}
        />
      </div>
    </div>
  );
}
