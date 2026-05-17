/**
 * 底部停靠监控面板（统一单面板）。
 *
 * 合并原 4 个 Tab（大盘/动作/错误/趋势）为一张面板：
 *   - 顶部：指标区 + CPU% / QPS 迷你趋势图，横向约各占 1/3 宽度
 *   - 底部：完整动作表（含可展开错误明细 + 搜索/过滤）
 *   - 高度可通过顶部拖把手调整（160px ~ 80vh）
 */

import { Alert, Button, Input, Progress, Space, Switch, Table, Tag, Tooltip } from 'antd';
import { CaretDownOutlined, CaretUpOutlined, LineChartOutlined, ArrowUpOutlined, ArrowDownOutlined, WarningOutlined } from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';
import ReactECharts from 'echarts-for-react';
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useRuntimeStore, classifyApdex } from '@/services';
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';
import { ApdexCell } from './shared/ApdexCell';
import { fmtBytesPlain, fmtMs, NUMERIC_STYLE } from './shared/formats';
import type { ActionMetric } from '@/types/api';
import './MonitorDock.css';

const MIN_H = 160;
const MAX_H_RATIO = 0.8;
const DEFAULT_H = 360;

/* ── helpers ── */

function fmtBandwidth(mbps: number) {
  const v = Number.isFinite(mbps) ? mbps : 0;
  if (v < 1) return { value: v * 1024, suffix: 'KB/s', precision: 1 };
  return { value: v, suffix: 'MB/s', precision: 2 };
}

function gaugeColor(v: number): string {
  if (v > 80) return 'var(--color-error)';
  if (v > 60) return 'var(--color-warning)';
  return 'var(--color-blue)';
}

function successColor(rate: number): string {
  if (rate >= 0.95) return 'var(--color-success)';
  if (rate >= 0.8) return 'var(--color-warning)';
  return 'var(--color-error)';
}

const APDEX_COLOR: Record<string, string> = {
  excellent: 'var(--color-success)',
  good: '#bae637',
  fair: 'var(--color-warning)',
  poor: 'var(--color-orange, #fa8c16)',
  danger: 'var(--color-error)',
  unknown: 'var(--text-tertiary)',
};

function sparkOption(series: Array<{ name: string; data: number[]; color: string }>, dark: boolean) {
  const len = series[0]?.data.length ?? 0;
  const x = Array.from({ length: len }, (_, i) => i);
  return {
    grid: { left: 28, right: 4, top: 4, bottom: 14 },
    xAxis: { type: 'category', data: x, show: false },
    yAxis: { type: 'value', axisLabel: { fontSize: 8, color: dark ? '#888' : '#aaa' }, splitLine: { lineStyle: { color: dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.06)' } } },
    tooltip: {
      trigger: 'axis',
      textStyle: { fontSize: 10, color: dark ? '#e0e0e0' : '#333' },
      backgroundColor: dark ? '#2a2a2a' : '#fff',
      borderColor: dark ? '#444' : '#ddd',
      valueFormatter: (v: number) => v?.toFixed(2) ?? '—',
    },
    series: series.map((s) => ({
      name: s.name,
      type: 'line',
      smooth: true,
      symbol: 'none',
      data: s.data,
      itemStyle: { color: s.color },
      areaStyle: { opacity: 0.12 },
      lineStyle: { width: 1.5 },
    })),
  };
}

/* ── 动作表列定义 ── */

const ACTION_COLUMNS: ColumnsType<ActionMetric> = [
  {
    title: '动作',
    dataIndex: 'name',
    key: 'name',
    width: 180,
    fixed: 'left',
    ellipsis: true,
    sorter: (a, b) => a.name.localeCompare(b.name),
    render: (v: string) => {
      const isCb = v.startsWith('callback:');
      const display = isCb ? v.slice('callback:'.length) : v;
      return (
        <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
          {isCb && <Tag color="orange" style={{ marginInlineEnd: 0 }}>推送</Tag>}
          <Tooltip title={display} mouseEnterDelay={0.4}>
            <code style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {display}
            </code>
          </Tooltip>
        </div>
      );
    },
  },
  { title: '并发', dataIndex: 'executing', key: 'executing', width: 56, sorter: (a, b) => a.executing - b.executing, render: (v: number) => <span style={NUMERIC_STYLE}>{v}</span> },
  { title: '样本', dataIndex: 'sampleCount', key: 'sampleCount', width: 64, sorter: (a, b) => a.sampleCount - b.sampleCount, defaultSortOrder: 'descend' as const, render: (v: number) => <span style={NUMERIC_STYLE}>{v}</span> },
  { title: '成功', dataIndex: 'successCount', key: 'successCount', width: 64, sorter: (a, b) => a.successCount - b.successCount, render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: 'var(--color-success)' }}>{v}</span> },
  { title: '失败', dataIndex: 'failureCount', key: 'failureCount', width: 64, sorter: (a, b) => a.failureCount - b.failureCount, render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: v > 0 ? 'var(--color-error)' : 'var(--text-tertiary)' }}>{v}</span> },
  { title: '超时', dataIndex: 'timeoutCount', key: 'timeoutCount', width: 64, sorter: (a, b) => a.timeoutCount - b.timeoutCount, render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: v > 0 ? 'var(--color-orange)' : 'var(--text-tertiary)' }}>{v}</span> },
  { title: '跳过', dataIndex: 'skippedCount', key: 'skippedCount', width: 56, sorter: (a, b) => a.skippedCount - b.skippedCount, render: (v: number) => <span style={NUMERIC_STYLE}>{v}</span> },
  { title: 'Apdex', dataIndex: 'apdex', key: 'apdex', width: 72, sorter: (a, b) => a.apdex - b.apdex, render: (v: number) => <ApdexCell value={v} /> },
  { title: 'QPS', dataIndex: 'avgQps', key: 'avgQps', width: 64, sorter: (a, b) => a.avgQps - b.avgQps, render: (v: number) => <span style={NUMERIC_STYLE}>{v.toFixed(1)}</span> },
  { title: '↑avg(B)', dataIndex: 'avgSendBytes', key: 'avgSendBytes', width: 74, sorter: (a, b) => a.avgSendBytes - b.avgSendBytes, render: (v: number) => <span style={NUMERIC_STYLE}>{fmtBytesPlain(v)}</span> },
  { title: '↓avg(B)', dataIndex: 'avgRecvBytes', key: 'avgRecvBytes', width: 74, sorter: (a, b) => a.avgRecvBytes - b.avgRecvBytes, render: (v: number) => <span style={NUMERIC_STYLE}>{fmtBytesPlain(v)}</span> },
  { title: 'avg(ms)', key: 'avgMs', width: 68, sorter: (a, b) => a.latency.avgMs - b.latency.avgMs, render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.avgMs)}</span> },
  { title: 'p50(ms)', key: 'p50Ms', width: 68, sorter: (a, b) => a.latency.p50Ms - b.latency.p50Ms, render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.p50Ms)}</span> },
  { title: 'p95(ms)', key: 'p95Ms', width: 68, sorter: (a, b) => a.latency.p95Ms - b.latency.p95Ms, render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.p95Ms)}</span> },
  { title: 'p99(ms)', key: 'p99Ms', width: 68, sorter: (a, b) => a.latency.p99Ms - b.latency.p99Ms, render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.p99Ms)}</span> },
  { title: 'max(ms)', key: 'maxMs', width: 68, sorter: (a, b) => a.latency.maxMs - b.latency.maxMs, render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.maxMs)}</span> },
  { title: '超均(ms)', dataIndex: 'timeoutAvgMs', key: 'timeoutAvgMs', width: 74, sorter: (a, b) => a.timeoutAvgMs - b.timeoutAvgMs, render: (v: number) => <span style={NUMERIC_STYLE}>{fmtMs(v)}</span> },
  {
    title: '错误', key: 'errors', width: 56, fixed: 'right',
    sorter: (a, b) => (a.errors?.length ?? 0) - (b.errors?.length ?? 0),
    render: (_, r) => r.errors?.length ? <Tag color="error" style={{ marginInlineEnd: 0 }}>{r.errors.length}</Tag> : <span style={{ color: 'var(--text-tertiary)' }}>—</span>,
  },
];

/* ── Agent 离线告警横幅 ── */

function AgentOfflineAlert() {
  const agentEvents = useRuntimeStore((s) => s.agentEvents);
  const agents = useRuntimeStore((s) => s.agents);
  const [dismissedKeys, setDismissedKeys] = useState<Set<string>>(new Set());

  // 取最近未关闭的 offline 事件
  const offlineAlerts = useMemo(() => {
    const onlineIds = new Set(agents.filter((a) => a.status !== 'offline').map((a) => a.agentId));
    return agentEvents
      .filter((e) => e.type === 'offline' && !dismissedKeys.has(e.agentId + e.timestamp))
      .filter((e) => !onlineIds.has(e.agentId)) // 已恢复的不显示
      .slice(-3); // 最多显示 3 条
  }, [agentEvents, agents, dismissedKeys]);

  if (offlineAlerts.length === 0) return null;

  const onlineCount = agents.filter((a) => a.status !== 'offline').length;
  const totalCount = agents.length;

  return (
    <Alert
      type="warning"
      showIcon
      icon={<WarningOutlined />}
      message={
        <span style={{ fontSize: 12 }}>
          节点 {offlineAlerts.map((e) => `"${e.agentName || e.agentId}"`).join('、')} 已离线，
          任务继续运行中（{onlineCount}/{totalCount} 在线）
        </span>
      }
      closable
      onClose={() => {
        const keys = new Set(dismissedKeys);
        offlineAlerts.forEach((e) => keys.add(e.agentId + e.timestamp));
        setDismissedKeys(keys);
      }}
      style={{ marginBottom: 4, padding: '4px 12px', borderRadius: 6, fontSize: 12 }}
    />
  );
}

/* ══════════════════════════════════════════════════════════════ */

export function MonitorDock() {
  const mode = useRuntimeStore((s) => s.mode);
  const dockOpen = useEditorStore((s) => s.monitorDockOpen);
  const setDockOpen = useEditorStore((s) => s.setMonitorDockOpen);
  const [height, setHeight] = useState<number>(() => {
    const saved = Number(localStorage.getItem('stressbot.monitorDock.h'));
    return saved >= MIN_H ? saved : DEFAULT_H;
  });
  const [topCollapsed, setTopCollapsed] = useState(false);
  const dragRef = useRef<{ startY: number; startH: number } | null>(null);

  // auto toggle: edit→closed; running→open
  const lastModeRef = useRef(mode);
  useEffect(() => {
    if (lastModeRef.current === mode) return;
    if (lastModeRef.current === 'edit' && mode !== 'edit') setDockOpen(true);
    if (mode === 'edit') setDockOpen(false);
    lastModeRef.current = mode;
  }, [mode, setDockOpen]);

  const onDragStart = useCallback(
    (e: React.MouseEvent) => {
      e.preventDefault();
      dragRef.current = { startY: e.clientY, startH: height };
      const onMove = (ev: MouseEvent) => {
        if (!dragRef.current) return;
        const max = window.innerHeight * MAX_H_RATIO;
        const next = Math.max(MIN_H, Math.min(max, dragRef.current.startH + (dragRef.current.startY - ev.clientY)));
        setHeight(next);
      };
      const onUp = () => {
        if (dragRef.current) localStorage.setItem('stressbot.monitorDock.h', String(height));
        dragRef.current = null;
        window.removeEventListener('mousemove', onMove);
        window.removeEventListener('mouseup', onUp);
      };
      window.addEventListener('mousemove', onMove);
      window.addEventListener('mouseup', onUp);
    },
    [height],
  );

  if (mode === 'edit') return null;

  if (!dockOpen) {
    return (
      <div className="monitor-dock__collapsed">
        <Button type="text" size="small" icon={<LineChartOutlined />} onClick={() => setDockOpen(true)}>
          展开监控
        </Button>
        <span style={{ color: 'var(--text-tertiary)' }}>点击展开实时监控</span>
        <Button type="text" size="small" icon={<CaretUpOutlined />} onClick={() => setDockOpen(true)} />
      </div>
    );
  }

  return (
    <div className="monitor-dock" style={{ height }}>
      <Tooltip title="拖动调整高度">
        <div className="monitor-dock__handle" onMouseDown={onDragStart} />
      </Tooltip>
      <div className="monitor-dock__body">
        <AgentOfflineAlert />
        {!topCollapsed && <TopSection />}
        <ActionsSection dockHeight={height} topCollapsed={topCollapsed} />
      </div>
      <div style={{ position: 'absolute', top: 6, right: 12, display: 'flex', gap: 2, alignItems: 'center' }}>
        <Tooltip title={topCollapsed ? '展开指标和趋势图' : '收起指标和趋势图'}>
          <Button type="text" size="small" icon={<LineChartOutlined />} onClick={() => setTopCollapsed(v => !v)} style={{ opacity: topCollapsed ? 0.5 : 1 }} />
        </Tooltip>
        <Tooltip title="折叠监控">
          <Button type="text" size="small" icon={<CaretDownOutlined />} onClick={() => setDockOpen(false)} />
        </Tooltip>
      </div>
    </div>
  );
}

/* ──────────────────────────────────────────────────
   顶部指标 + 趋势图
   ────────────────────────────────────────────────── */

function TopSection() {
  const { latestStress, latestSystem, systemHistory, stressHistory, agents } = useRuntimeStore(
    useShallow((s) => ({
      latestStress: s.latestStress,
      latestSystem: s.latestSystem,
      systemHistory: s.systemHistory,
      stressHistory: s.stressHistory,
      agents: s.agents,
    })),
  );
  const theme = useEditorStore((s) => s.theme);
  const dark = theme === 'dark';

  // 迷你趋势图
  const cpuOption = useMemo(() => {
    if (systemHistory.length < 2) return null;
    return sparkOption([{ name: 'CPU%', data: systemHistory.map((s) => s.avgCpuPercent), color: '#fa8c16' }], dark);
  }, [systemHistory]);

  const qpsOption = useMemo(() => {
    if (stressHistory.length < 2) return null;
    const totalQps = stressHistory.map((s) => s.actions.reduce((sum, a) => sum + a.avgQps, 0));
    return sparkOption([{ name: 'QPS', data: totalQps, color: '#1677ff' }], dark);
  }, [stressHistory]);

  if (!latestStress) {
    return (
      <div className="monitor-dock__top">
        <div className="monitor-dock__metrics" style={{ display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          <span style={{ color: 'var(--text-tertiary)', fontSize: 12 }}>暂无压测数据；启动任务后实时显示</span>
        </div>
        <div className="monitor-dock__charts">
          <div className="monitor-dock__chart-card monitor-dock__chart-card--cpu">
            <div className="monitor-dock__chart-title">CPU%</div>
            <div style={{ color: 'var(--text-tertiary)', fontSize: 10, textAlign: 'center', paddingTop: 20 }}>等待数据…</div>
          </div>
          <div className="monitor-dock__chart-card monitor-dock__chart-card--qps">
            <div className="monitor-dock__chart-title">QPS</div>
            <div style={{ color: 'var(--text-tertiary)', fontSize: 10, textAlign: 'center', paddingTop: 20 }}>等待数据…</div>
          </div>
        </div>
      </div>
    );
  }

  const r = latestStress.robots;
  const c = latestStress.connections;
  const b = latestStress.bandwidth;
  const sys = latestSystem;
  const actions = latestStress.actions;

  const activeConns = Math.max(0, c.established - c.dropped);
  const onlineAgents = (agents ?? []).filter((a) => a.status !== 'offline').length;
  const totalAgents = (agents ?? []).length;
  const send = fmtBandwidth(b.sendMBps ?? 0);
  const recv = fmtBandwidth(b.recvMBps ?? 0);
  const robotPercent = r.started > 0 ? Math.round((r.running / r.started) * 100) : 0;

  // 加权集群 apdex / 成功率
  let totalSamples = 0, wApdex = 0, wSuccess = 0;
  for (const a of actions) {
    totalSamples += a.sampleCount;
    wApdex += a.apdex * a.sampleCount;
    wSuccess += a.successRate * a.sampleCount;
  }
  const clusterApdex = totalSamples > 0 ? wApdex / totalSamples : 0;
  const clusterSuccess = totalSamples > 0 ? wSuccess / totalSamples : 0;
  const apdexLevel = classifyApdex(clusterApdex);

  return (
    <div className="monitor-dock__top">
      {/* 指标区 */}
      <div className="md-metrics-panel">
        {/* 核心指标 Hero：机器人 + 连接 */}
        <div className="md-hero-row">
          <div className="md-hero-box">
            <div className="md-hero-title">机器人</div>
            <div className="md-hero-value" style={{ color: 'var(--color-success)' }}>{r.running}<span style={{ fontSize: 12, fontWeight: 400, color: 'var(--text-secondary)' }}> / {r.started}</span></div>
          </div>
          <div className="md-hero-divider" />
          <div className="md-hero-box">
            <div className="md-hero-title">活跃连接</div>
            <div className="md-hero-value" style={{ color: 'var(--color-blue)' }}>{activeConns}</div>
          </div>
          <div className="md-hero-divider" />
          <div className="md-hero-box">
            <div className="md-hero-title">QPS</div>
            <div className="md-hero-value" style={{ color: 'var(--color-purple)' }}>
              {actions.reduce((sum, a) => sum + a.avgQps, 0).toFixed(1)}
            </div>
          </div>
        </div>

        {/* 负载进度条 */}
        <div className="md-load-row">
          <div className="md-load-header">
            <span className="md-load-title">负载 {r.running}/{r.started}</span>
            <span className="md-load-stats">
              <span className="md-load-running">{robotPercent}%</span>
            </span>
          </div>
          <div className="md-load-progress">
            <Progress percent={robotPercent} strokeColor="var(--color-success)" showInfo={false} size={4} />
          </div>
          {(r.stopped > 0 || r.errored > 0) && (
            <div className="md-load-chips">
              {r.stopped > 0 && <span className="md-chip md-chip-stopped">停 {r.stopped}</span>}
              {r.errored > 0 && <span className="md-chip md-chip-errored">错 {r.errored}</span>}
            </div>
          )}
        </div>

        {/* 数据网格 */}
        <div className="md-grid-row">
          <div className="md-grid-item">
            <span className="md-grid-label">↑ 发送</span>
            <span className="md-grid-value">{send.value.toFixed(send.precision)} {send.suffix}</span>
          </div>
          <div className="md-grid-item">
            <span className="md-grid-label">↓ 接收</span>
            <span className="md-grid-value">{recv.value.toFixed(recv.precision)} {recv.suffix}</span>
          </div>
          <div className="md-grid-item">
            <span className="md-grid-label">CPU</span>
            <span className="md-grid-value" style={{ color: gaugeColor(sys?.avgCpuPercent ?? 0) }}>
              {sys?.avgCpuPercent.toFixed(0)}%
            </span>
          </div>
          <div className="md-grid-item">
            <span className="md-grid-label">节点</span>
            <span
              className="md-grid-value"
              style={
                onlineAgents < totalAgents ? { color: 'var(--color-warning)' } : undefined
              }
            >
              {onlineAgents}/{totalAgents}
            </span>
          </div>
          <div className="md-grid-item">
            <span className="md-grid-label">成功率</span>
            <span className="md-grid-value" style={{ color: successColor(clusterSuccess) }}>
              {(clusterSuccess * 100).toFixed(1)}%
            </span>
          </div>
          <div className="md-grid-item">
            <span className="md-grid-label">动作</span>
            <span className="md-grid-value">{latestStress.totalActions.toLocaleString()}</span>
          </div>
        </div>
      </div>

      {/* 趋势图 */}
      <div className="monitor-dock__charts">
        <div className="monitor-dock__chart-card monitor-dock__chart-card--cpu">
          <div className="monitor-dock__chart-title">CPU%</div>
          {cpuOption ? (
            <ReactECharts option={cpuOption} style={{ height: 'calc(100% - 16px)' }} notMerge lazyUpdate />
          ) : (
            <div style={{ color: 'var(--text-tertiary)', fontSize: 10, textAlign: 'center', paddingTop: 12 }}>等待数据…</div>
          )}
        </div>
        <div className="monitor-dock__chart-card monitor-dock__chart-card--qps">
          <div className="monitor-dock__chart-title">QPS</div>
          {qpsOption ? (
            <ReactECharts option={qpsOption} style={{ height: 'calc(100% - 16px)' }} notMerge lazyUpdate />
          ) : (
            <div style={{ color: 'var(--text-tertiary)', fontSize: 10, textAlign: 'center', paddingTop: 12 }}>等待数据…</div>
          )}
        </div>
      </div>
    </div>
  );
}

/* ──────────────────────────────────────────────────
   动作表（含搜索 + 过滤 + 可展开错误）
   ────────────────────────────────────────────────── */

function ActionsSection({ dockHeight, topCollapsed }: { dockHeight: number; topCollapsed: boolean }) {
  const latestStress = useRuntimeStore((s) => s.latestStress);
  const [search, setSearch] = useState('');
  const [actionsOnly, setActionsOnly] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const [scrollY, setScrollY] = useState(200);

  // dockHeight 或 topCollapsed 变化时同步重算 scrollY
  useLayoutEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    const containerH = el.clientHeight;
    const thead = el.querySelector('.ant-table-thead');
    const headerH = thead?.getBoundingClientRect().height ?? 37;
    setScrollY(Math.max(60, containerH - headerH - 2));
  }, [dockHeight, topCollapsed]);

  const dataSource = useMemo(() => {
    if (!latestStress) return [];
    let rows = latestStress.actions ?? [];
    if (actionsOnly) rows = rows.filter((a) => !a.name.startsWith('callback:'));
    if (search) {
      const lo = search.toLowerCase();
      rows = rows.filter((a) => a.name.toLowerCase().includes(lo));
    }
    return rows;
  }, [latestStress, search, actionsOnly]);

  if (!latestStress) return null;

  return (
    <div className="monitor-dock__actions">
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4, flexShrink: 0 }}>
        <Input.Search
          placeholder="按动作名搜索"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          allowClear
          style={{ width: 260 }}
          size="small"
        />
        <Space size={4}>
          <span style={{ fontSize: 11, color: 'var(--text-secondary)' }}>仅动作</span>
          <Switch checked={actionsOnly} onChange={setActionsOnly} size="small" />
        </Space>
        <span style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>
          {dataSource.length} 条
        </span>
      </div>
      <div ref={containerRef} style={{ flex: 1, minHeight: 0 }}>
        <Table<ActionMetric>
          rowKey="name"
          size="small"
          dataSource={dataSource}
          columns={ACTION_COLUMNS}
          pagination={false}
          scroll={{ x: 'max-content', y: scrollY }}
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
      </div>
    </div>
  );
}
