import { Button, Input, Popover, Segmented, Space, Switch, Table, Tag, Tooltip } from 'antd';
import { DownloadOutlined } from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';
import { useCallback, useMemo, useState } from 'react';
import type { ErrorEntry, TimingDetailLevel } from '@/types/api';
import { ApdexCell } from './ApdexCell';
import { fmtBytes, fmtMs, NUMERIC_STYLE } from './formats';

/**
 * primary=按动作类别显示它的主指标（往返类看 RTT、监听类看等待时长、其余看执行耗时）；
 * totalDuration=一律看 wallClock，仅作诊断。
 *
 * 之所以不是「RTT / 总耗时」二选一：不同类别的动作根本没有可比的同一个数。
 * 强行让监听类显示 RTT 只会得到一整列 —，让往返类显示总耗时则混进了施压机排队时间。
 */
export type ActionLatencyMode = 'primary' | 'totalDuration';

/** 动作的网络语义分类，由后端按运行期实际行为判定。 */
export type ActionKindLike = 'networked' | 'listen' | 'send' | 'local';

export interface ActionHistogramLike {
  maxMs: number | null;
  avgMs: number | null;
  p50Ms: number | null;
  p95Ms: number | null;
  p99Ms: number | null;
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

  kind?: ActionKindLike;

  rtt?: ActionHistogramLike;
  rttSampleCount?: number;
  rttApdexSampleCount?: number;
  rttApdex?: number;

  listenWait?: ActionHistogramLike;
  listenWaitSampleCount?: number;
  listenTimeoutRate?: number;

  totalDuration?: ActionHistogramLike;
  totalDurationSampleCount?: number;

  nonRTTAvgMs?: number;
  buildAvgMs?: number;
  encodeAvgMs?: number;
  sendAvgMs?: number;
  decodeWaitAvgMs?: number;
  decodeAvgMs?: number;
  dispatchToActionWaitAvgMs?: number;
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
  showCsvExport?: boolean;
  searchWidth?: number;
  timingDetail?: TimingDetailLevel;
}

export type TimingBreakdownField =
  | 'nonRTTAvgMs'
  | 'sendAvgMs'
  | 'encodeAvgMs'
  | 'decodeAvgMs'
  | 'buildAvgMs'
  | 'decodeWaitAvgMs'
  | 'dispatchToActionWaitAvgMs'
  | 'parseStoreAvgMs';

export function getTimingBreakdownFields(level: TimingDetailLevel): TimingBreakdownField[] {
  const fields: TimingBreakdownField[] = ['nonRTTAvgMs', 'sendAvgMs'];
  if (level === 'codec' || level === 'full') fields.push('encodeAvgMs', 'decodeAvgMs');
  if (level === 'full')
    fields.push('buildAvgMs', 'decodeWaitAvgMs', 'dispatchToActionWaitAvgMs', 'parseStoreAvgMs');
  return fields;
}

const TIMING_FIELD_META: Record<
  TimingBreakdownField,
  { title: string; hint: string; width: number }
> = {
  nonRTTAvgMs: {
    title: '非RTT(ms)',
    hint: '动作总耗时扣除已记录 RTT 后的平均剩余耗时。',
    width: 92,
  },
  sendAvgMs: { title: 'send(ms)', hint: '发送阶段平均耗时。', width: 84 },
  encodeAvgMs: { title: 'encode(ms)', hint: '协议编码平均耗时。', width: 92 },
  decodeAvgMs: {
    title: 'decode(ms)',
    hint: '收到完整响应帧后的协议解码平均耗时，不计入 RTT。',
    width: 92,
  },
  buildAvgMs: { title: 'build(ms)', hint: '请求消息构建平均耗时。', width: 88 },
  decodeWaitAvgMs: {
    title: 'decode等待(ms)',
    hint: '收到帧后进入协议解码前的平均排队耗时。',
    width: 112,
  },
  dispatchToActionWaitAvgMs: {
    title: '分发等待(ms)',
    hint: '网络分发完成到动作恢复执行之间的平均等待耗时。',
    width: 112,
  },
  parseStoreAvgMs: { title: 'parse/store(ms)', hint: '响应解析与状态写入平均耗时。', width: 120 },
};

interface SelectedLatencyMetric {
  histogram?: ActionHistogramLike;
  sampleCount: number;
  /** 该指标的口径标签，用于列头与 CSV 表头 */
  label: string;
}

/**
 * 类别不单独占一列，而是给动作名染色——色板沿用流程编辑器 pattern 徽章的语义分组
 * （tcpRequest 金黄 / tcpListen 橙 / tcpSend 蓝 / setState 青），两处对同一个动作同色。
 */
const KIND_META: Record<ActionKindLike, { label: string; color: string; hint: string }> = {
  networked: {
    label: '往返',
    color: 'var(--node-boolean-border-active)',
    hint: '往返类：发起过请求-响应。主指标是 RTT，打 Apdex。',
  },
  listen: {
    label: '监听',
    color: 'var(--node-break-border-active)',
    hint: '监听类：只等服务端推送。主指标是等待时长（开始等待 → 帧被内核收到），不打 Apdex——这段时长的主体是服务端业务，没有普遍阈值。',
  },
  send: {
    label: '发送',
    color: 'var(--node-sequence-border-active)',
    hint: '发送类：只发不等（即发即忘）。主指标是执行耗时，不含服务端成分，不打 Apdex。',
  },
  local: {
    label: '本地',
    color: 'var(--node-continue-border-active)',
    hint: '本地类：无网络行为。主指标是执行耗时，不打 Apdex。',
  },
};

/**
 * 取行的类别。kind 是后端新增字段，改造之前归档的历史快照里没有，
 * 缺失时按与后端 classifyActionKind 相同的判据就地推断——否则老报告会整表退化成
 * 无色 + 主指标一律显示 wallClock，连本来有 RTT 的往返动作也看不到 RTT。
 */
export function resolveKind(row: {
  kind?: ActionKindLike;
  rttSampleCount?: number;
  rttApdexSampleCount?: number;
  listenWaitSampleCount?: number;
  avgSendBytes: number;
}): ActionKindLike {
  if (row.kind) return row.kind;
  if ((row.rttApdexSampleCount ?? 0) > 0) return 'networked';
  if ((row.rttSampleCount ?? 0) > 0) return 'networked';
  if ((row.listenWaitSampleCount ?? 0) > 0) return 'listen';
  if (row.avgSendBytes > 0) return 'send';
  return 'local';
}

/** 动作名的 tooltip：名字 + 该类别的口径说明（监听类补上超时率）。 */
function kindHint(row: ActionMetricsTableRow): string {
  const kind = resolveKind(row);
  const meta = KIND_META[kind];
  return kind === 'listen' && row.listenTimeoutRate != null
    ? `${meta.hint} 超时率 ${(row.listenTimeoutRate * 100).toFixed(1)}%。`
    : meta.hint;
}

/** 按动作类别取它的主指标口径。 */
function selectPrimaryMetric(row: ActionMetricsTableRow): SelectedLatencyMetric {
  switch (resolveKind(row)) {
    case 'networked':
      return { histogram: row.rtt, sampleCount: row.rttSampleCount ?? 0, label: 'RTT' };
    case 'listen':
      return {
        histogram: row.listenWait,
        sampleCount: row.listenWaitSampleCount ?? 0,
        label: '等待',
      };
    default:
      return {
        histogram: row.totalDuration,
        sampleCount: row.totalDurationSampleCount ?? 0,
        label: '执行',
      };
  }
}

function selectLatencyMetric(
  row: ActionMetricsTableRow,
  mode: ActionLatencyMode,
): SelectedLatencyMetric {
  if (mode === 'totalDuration') {
    return {
      histogram: row.totalDuration,
      sampleCount: row.totalDurationSampleCount ?? 0,
      label: '总耗时',
    };
  }
  return selectPrimaryMetric(row);
}

function latencySortValue(
  row: ActionMetricsTableRow,
  mode: ActionLatencyMode,
  key: keyof ActionHistogramLike,
): number {
  const selected = selectLatencyMetric(row, mode);
  if (selected.sampleCount <= 0 || !selected.histogram) return -1;
  return selected.histogram[key] ?? -1;
}

function latencyTooltip(mode: ActionLatencyMode) {
  if (mode === 'totalDuration') {
    return '总耗时：动作从开始执行到结束的 wallClock，含施压机排队与脚本内故意 sleep，仅作诊断，不用于评分';
  }
  return '主指标按动作类别选取：往返类=RTT（发送完成 → 收到完整响应帧）；监听类=等待时长（开始等待 → 帧被内核收到）；发送/本地类=执行耗时';
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
  showCsvExport = false,
  searchWidth,
  timingDetail = 'rtt',
}: ActionMetricsTableProps<T>) {
  const [innerMode, setInnerMode] = useState<ActionLatencyMode>('primary');
  const [search, setSearch] = useState('');
  const [actionsOnly, setActionsOnly] = useState(false);

  const mode = latencyMode === undefined ? innerMode : latencyMode;
  const timingBreakdownFields = useMemo(
    () => getTimingBreakdownFields(timingDetail),
    [timingDetail],
  );
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

  // ── CSV helpers ──
  function csvEscape(v: string): string {
    if (v.includes(',') || v.includes('"') || v.includes('\n')) {
      return '"' + v.replace(/"/g, '""') + '"';
    }
    return v;
  }
  const latencyCsv = useCallback(
    (row: T, key: keyof ActionHistogramLike): string => {
      const sel = selectLatencyMetric(row, mode);
      const value = sel.histogram?.[key];
      return sel.sampleCount > 0 && value != null ? fmtMs(value) : '—';
    },
    [mode],
  );

  const exportCsv = useCallback(() => {
    // 根据当前可见列状态构建 CSV 列定义
    const csvCols: { header: string; getValue: (row: T) => string }[] = [
      { header: '动作', getValue: (r) => csvEscape(r.name) },
      { header: '样本', getValue: (r) => String(r.sampleCount) },
      { header: '成功', getValue: (r) => String(r.successCount) },
      { header: '失败', getValue: (r) => String(r.failureCount) },
      { header: '超时', getValue: (r) => String(r.timeoutCount) },
    ];
    if (showCanceledColumn) {
      csvCols.push({ header: '取消', getValue: (r) => String(r.canceledCount ?? 0) });
    }
    // 表格里类别靠动作名的颜色表达，CSV 没有颜色这个通道，只能单出一列，
    // 否则「主指标」列里混着 RTT / 等待 / 执行三种口径而无从分辨。
    const avgHeader = mode === 'totalDuration' ? '总耗时 avg(ms)' : '主指标 avg(ms)';
    if (mode !== 'totalDuration') {
      csvCols.push({ header: '类别', getValue: (r) => KIND_META[resolveKind(r)].label });
    }
    csvCols.push(
      { header: avgHeader, getValue: (r) => latencyCsv(r, 'avgMs') },
      { header: 'p50(ms)', getValue: (r) => latencyCsv(r, 'p50Ms') },
      { header: 'p95(ms)', getValue: (r) => latencyCsv(r, 'p95Ms') },
      { header: 'p99(ms)', getValue: (r) => latencyCsv(r, 'p99Ms') },
      { header: 'max(ms)', getValue: (r) => latencyCsv(r, 'maxMs') },
    );
    if (showClientBreakdown) {
      for (const field of timingBreakdownFields) {
        csvCols.push({
          header: TIMING_FIELD_META[field].title,
          getValue: (r) => fmtMs(r[field] ?? 0),
        });
      }
    }
    if (showBandwidthColumns) {
      csvCols.push(
        { header: '发送(均)', getValue: (r) => fmtBytes(r.avgSendBytes) },
        { header: '接收(均)', getValue: (r) => fmtBytes(r.avgRecvBytes) },
      );
    }
    if (showExecutingColumn) {
      csvCols.push({ header: '并发', getValue: (r) => String(r.executing) });
    }
    if (showQpsColumn) {
      csvCols.push({ header: 'QPS', getValue: (r) => r.avgQps.toFixed(1) });
    }
    // Apdex 只对往返类有意义；其余类别留空而非 0，避免被当成差分。
    csvCols.push({
      header: 'Apdex(RTT)',
      getValue: (r) =>
        resolveKind(r) === 'networked' && (r.rttApdexSampleCount ?? 0) > 0 && r.rttApdex != null
          ? r.rttApdex.toFixed(2)
          : '',
    });
    // 错误列不导出

    const header = csvCols.map((c) => c.header).join(',');
    const body = dataSource.map((row) => csvCols.map((c) => c.getValue(row)).join(',')).join('\n');
    const csv = '﻿' + header + '\n' + body; // BOM 确保 Excel 正确识别 UTF-8
    const blob = new Blob([csv], { type: 'text/csv;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `action-metrics-${new Date().toISOString().slice(0, 19).replace(/[:.]/g, '-')}.csv`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  }, [
    dataSource,
    mode,
    showCanceledColumn,
    showClientBreakdown,
    showBandwidthColumns,
    showExecutingColumn,
    showQpsColumn,
    latencyCsv,
    timingBreakdownFields,
  ]);

  const latencyTitle = mode === 'totalDuration' ? '总耗时 avg(ms)' : '主指标 avg(ms)';
  const nameWidth = compact ? 160 : 200;
  const countWidth = compact ? 60 : 70;
  const latencyWidth = compact ? 64 : 76;

  const columns: ColumnsType<T> = useMemo(() => {
    const latencyValue = (row: T, key: keyof ActionHistogramLike) =>
      latencySortValue(row, mode, key);
    const renderLatency = (row: T, key: keyof ActionHistogramLike) => {
      const selected = selectLatencyMetric(row, mode);
      const value = selected.histogram?.[key];
      return (
        <span style={NUMERIC_STYLE}>
          {selected.sampleCount > 0 && value != null ? fmtMs(value) : '—'}
        </span>
      );
    };

    const cols: ColumnsType<T> = [
      {
        title: '动作',
        dataIndex: 'name',
        key: 'name',
        width: nameWidth,
        fixed: 'left',
        ellipsis: true,
        sorter: (a, b) => a.name.localeCompare(b.name),
        render: (v: string, r) => {
          const isCallback = v.startsWith('callback:');
          const display = isCallback ? v.slice('callback:'.length) : v;
          // 类别只体现为动作名的颜色，省掉一整列；口径说明并进名字的 tooltip。
          return (
            <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
              {isCallback && (
                <Tag color="orange" style={{ marginInlineEnd: 0 }}>
                  推送
                </Tag>
              )}
              <Tooltip
                title={
                  <>
                    {display}
                    <br />
                    {kindHint(r)}
                  </>
                }
                mouseEnterDelay={0.4}
              >
                <code
                  style={{
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                    color: KIND_META[resolveKind(r)].color,
                  }}
                >
                  {display}
                </code>
              </Tooltip>
            </div>
          );
        },
      },
      {
        title: '样本',
        dataIndex: 'sampleCount',
        key: 'sampleCount',
        width: countWidth,
        sorter: (a, b) => a.sampleCount - b.sampleCount,
        defaultSortOrder: 'descend' as const,
        render: (v: number) => <span style={NUMERIC_STYLE}>{v}</span>,
      },
      {
        title: '成功',
        dataIndex: 'successCount',
        key: 'successCount',
        width: countWidth,
        sorter: (a, b) => a.successCount - b.successCount,
        render: (v: number) => (
          <span style={{ ...NUMERIC_STYLE, color: 'var(--color-success)' }}>{v}</span>
        ),
      },
      {
        title: '失败',
        dataIndex: 'failureCount',
        key: 'failureCount',
        width: compact ? 52 : 70,
        sorter: (a, b) => a.failureCount - b.failureCount,
        render: (v: number) => (
          <span
            style={{
              ...NUMERIC_STYLE,
              color: v > 0 ? 'var(--color-error)' : 'var(--text-tertiary)',
            }}
          >
            {v}
          </span>
        ),
      },
      {
        title: '超时',
        dataIndex: 'timeoutCount',
        key: 'timeoutCount',
        width: compact ? 52 : 70,
        sorter: (a, b) => a.timeoutCount - b.timeoutCount,
        render: (v: number) => (
          <span
            style={{
              ...NUMERIC_STYLE,
              color: v > 0 ? 'var(--color-orange)' : 'var(--text-tertiary)',
            }}
          >
            {v}
          </span>
        ),
      },
    ];

    if (showCanceledColumn) {
      cols.push({
        title: '取消',
        dataIndex: 'canceledCount',
        key: 'canceledCount',
        width: 70,
        sorter: (a, b) => (a.canceledCount || 0) - (b.canceledCount || 0),
        render: (v: number | undefined) => (
          <span style={NUMERIC_STYLE}>{typeof v === 'number' ? v : 0}</span>
        ),
      });
    }

    cols.push(
      {
        title: <Tooltip title={latencyTooltip(mode)}>{latencyTitle}</Tooltip>,
        key: 'avgMs',
        width: compact ? 68 : 92,
        sorter: (a, b) => latencyValue(a, 'avgMs') - latencyValue(b, 'avgMs'),
        render: (_, r) => renderLatency(r, 'avgMs'),
      },
      {
        title: 'p50(ms)',
        key: 'p50Ms',
        width: latencyWidth,
        sorter: (a, b) => latencyValue(a, 'p50Ms') - latencyValue(b, 'p50Ms'),
        render: (_, r) => renderLatency(r, 'p50Ms'),
      },
      {
        title: 'p95(ms)',
        key: 'p95Ms',
        width: latencyWidth,
        sorter: (a, b) => latencyValue(a, 'p95Ms') - latencyValue(b, 'p95Ms'),
        render: (_, r) => renderLatency(r, 'p95Ms'),
      },
      {
        title: 'p99(ms)',
        key: 'p99Ms',
        width: latencyWidth,
        sorter: (a, b) => latencyValue(a, 'p99Ms') - latencyValue(b, 'p99Ms'),
        render: (_, r) => renderLatency(r, 'p99Ms'),
      },
      {
        title: 'max(ms)',
        key: 'maxMs',
        width: latencyWidth,
        sorter: (a, b) => latencyValue(a, 'maxMs') - latencyValue(b, 'maxMs'),
        render: (_, r) => renderLatency(r, 'maxMs'),
      },
    );

    if (showClientBreakdown) {
      for (const field of timingBreakdownFields) {
        const meta = TIMING_FIELD_META[field];
        cols.push({
          title: <Tooltip title={meta.hint}>{meta.title}</Tooltip>,
          dataIndex: field,
          key: field,
          width: meta.width,
          sorter: (a, b) => (a[field] ?? 0) - (b[field] ?? 0),
          render: (v: number | undefined) => <span style={NUMERIC_STYLE}>{fmtMs(v ?? 0)}</span>,
        });
      }
    }

    if (showBandwidthColumns) {
      cols.push(
        {
          title: <Tooltip title="平均每次成功发送的字节数">↑发送(均)</Tooltip>,
          dataIndex: 'avgSendBytes',
          key: 'avgSendBytes',
          width: compact ? 72 : 80,
          sorter: (a, b) => a.avgSendBytes - b.avgSendBytes,
          render: (v: number) => (
            <span style={{ ...NUMERIC_STYLE, color: 'var(--chart-cyan)' }}>{fmtBytes(v)}</span>
          ),
        },
        {
          title: <Tooltip title="平均每次成功接收的字节数">↓接收(均)</Tooltip>,
          dataIndex: 'avgRecvBytes',
          key: 'avgRecvBytes',
          width: compact ? 72 : 80,
          sorter: (a, b) => a.avgRecvBytes - b.avgRecvBytes,
          render: (v: number) => (
            <span style={{ ...NUMERIC_STYLE, color: 'var(--chart-purple)' }}>{fmtBytes(v)}</span>
          ),
        },
      );
    }
    if (showExecutingColumn)
      cols.push({
        title: '并发',
        dataIndex: 'executing',
        key: 'executing',
        width: compact ? 52 : 64,
        sorter: (a, b) => a.executing - b.executing,
        render: (v: number) => <span style={NUMERIC_STYLE}>{v}</span>,
      });
    if (showQpsColumn)
      cols.push({
        title: 'QPS',
        dataIndex: 'avgQps',
        key: 'avgQps',
        width: compact ? 60 : 78,
        sorter: (a, b) => a.avgQps - b.avgQps,
        render: (v: number) => <span style={NUMERIC_STYLE}>{v.toFixed(1)}</span>,
      });

    // Apdex 只对往返类打分。其余类别走 sampleCount=0 那条分支，渲染成和「无样本」
    // 完全一样的灰底 — 标签：都是"这里没有分"，没必要让读者分辨两种没有。
    const apdexScore = (r: T) =>
      resolveKind(r) === 'networked' && (r.rttApdexSampleCount ?? 0) > 0 ? (r.rttApdex ?? -1) : -1;
    cols.push({
      title: (
        <Tooltip title="RTT Apdex，仅往返类适用。监听/发送/本地类没有可比的统一阈值，看各自的主指标分布。">
          Apdex
        </Tooltip>
      ),
      key: 'apdex',
      width: compact ? 68 : 80,
      sorter: (a, b) => apdexScore(a) - apdexScore(b),
      render: (_, r) => {
        const scored = resolveKind(r) === 'networked';
        return (
          <ApdexCell
            value={scored ? r.rttApdex : undefined}
            sampleCount={scored ? (r.rttApdexSampleCount ?? 0) : 0}
          />
        );
      },
    });

    if (showErrorsColumn) {
      cols.push({
        title: '错误',
        key: 'errors',
        width: compact ? 52 : 70,
        fixed: 'right',
        sorter: (a, b) => (a.errors?.length || 0) - (b.errors?.length || 0),
        render: (_, r) => {
          if (!r.errors?.length) return <span style={{ color: 'var(--text-tertiary)' }}>—</span>;
          return (
            <Popover
              overlayStyle={popupZIndex ? { zIndex: popupZIndex } : undefined}
              content={
                <div style={{ maxWidth: 360 }}>
                  {r.errors.map((e) => {
                    const isFramework = e.code < 100;
                    return (
                      <div key={e.code} style={{ marginTop: 3, fontSize: 11, lineHeight: '16px' }}>
                        <span
                          style={{
                            color: 'var(--color-error)',
                            fontWeight: 700,
                            fontSize: 10,
                            fontVariantNumeric: 'tabular-nums',
                            marginRight: 6,
                          }}
                        >
                          ×{e.count}
                        </span>
                        <Tag
                          color={isFramework ? 'default' : 'blue'}
                          style={{ fontSize: 10, marginInlineEnd: 4 }}
                        >
                          {isFramework ? '框架' : '业务'}
                        </Tag>
                        <span style={{ fontWeight: 500 }}>{e.codeName || `#${e.code}`}</span>
                        {e.msgs.length > 0 && (
                          <span style={{ color: 'var(--text-tertiary)', marginLeft: 6 }}>
                            {e.msgs.join('; ')}
                          </span>
                        )}
                      </div>
                    );
                  })}
                </div>
              }
              title={<span style={{ fontSize: 12 }}>错误明细</span>}
              mouseEnterDelay={0.3}
            >
              <Tag color="error" style={{ marginInlineEnd: 0, cursor: 'pointer' }}>
                {r.errors.length}
              </Tag>
            </Popover>
          );
        },
      });
    }
    return cols;
  }, [
    compact,
    latencyTitle,
    latencyWidth,
    mode,
    nameWidth,
    countWidth,
    popupZIndex,
    showBandwidthColumns,
    showCanceledColumn,
    showClientBreakdown,
    showErrorsColumn,
    showExecutingColumn,
    showQpsColumn,
    timingBreakdownFields,
  ]);

  return (
    <div
      className="action-metrics-table"
      style={{ width: '100%', minHeight: 0, display: 'flex', flexDirection: 'column', gap: 8 }}
    >
      {showToolbar && (
        <div
          className="action-metrics-table__toolbar"
          style={{
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
            flexWrap: 'wrap',
            gap: 8,
          }}
        >
          <Space size={12} wrap>
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
                options={[
                  { label: '主指标', value: 'primary' },
                  { label: '总耗时', value: 'totalDuration' },
                ]}
                onChange={(v) => setMode(v)}
              />
            )}
            <span style={{ fontSize: compact ? 11 : 12, color: 'var(--text-tertiary)' }}>
              {dataSource.length} 条
            </span>
          </Space>
          <Space size={12}>
            <Space size={6}>
              <span style={{ fontSize: compact ? 11 : 12, color: 'var(--text-secondary)' }}>
                隐藏推送
              </span>
              <Switch checked={actionsOnly} onChange={setActionsOnly} size="small" />
            </Space>
            {showCsvExport && (
              <Button type="text" size="small" icon={<DownloadOutlined />} onClick={exportCsv}>
                导出 CSV
              </Button>
            )}
          </Space>
        </div>
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
