/**
 * 底部停靠监控面板（统一单面板）。
 *
 * 合并原 4 个 Tab（大盘/动作/错误/趋势）为一张面板：
 *   - 顶部：指标区 + CPU% / QPS 迷你趋势图，横向约各占 1/3 宽度
 *   - 底部：完整动作表（含可展开错误明细 + 搜索/过滤）
 *   - 高度可通过顶部拖把手调整（160px ~ 80vh）
 */

import { Alert, Button, Progress, Tooltip } from 'antd';
import { CaretDownOutlined, CaretUpOutlined, LineChartOutlined, WarningOutlined } from '@ant-design/icons';
import { EChartsReact } from './shared/EChartsReact';
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useRuntimeStore } from '@/services';
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';
import { ActionMetricsTable } from './shared/ActionMetricsTable';
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

function sparkOption(series: Array<{ name: string; data: number[]; color: string }>, dark: boolean) {
  const cs = getComputedStyle(document.documentElement);
  const resolve = (v: string, fb: string) => {
    if (!v.startsWith('var(')) return v;
    const name = v.slice(4, -1);
    return cs.getPropertyValue(name).trim() || fb;
  };
  const len = series[0]?.data.length ?? 0;
  const x = Array.from({ length: len }, (_, i) => i);
  return {
    grid: { left: 28, right: 2, top: 4, bottom: 4 },
    xAxis: { type: 'category', data: x, show: false, boundaryGap: false },
    yAxis: { type: 'value', axisLabel: { fontSize: 8, color: cs.getPropertyValue('--text-tertiary').trim() || (dark ? '#888' : '#aaa') }, splitLine: { lineStyle: { color: cs.getPropertyValue('--divider-bg').trim() || (dark ? 'rgba(255,255,255,0.08)' : 'rgba(0,0,0,0.06)') } } },
    tooltip: {
      trigger: 'axis',
      textStyle: { fontSize: 10, color: cs.getPropertyValue('--text-primary').trim() || (dark ? '#e0e0e0' : '#333') },
      backgroundColor: cs.getPropertyValue('--bg-panel').trim() || (dark ? '#2a2a2a' : '#fff'),
      borderColor: cs.getPropertyValue('--border-color').trim() || (dark ? '#444' : '#ddd'),
      valueFormatter: (v: number) => v?.toFixed(2) ?? '—',
    },
    series: series.map((s) => {
      const c = resolve(s.color, dark ? '#1677ff' : '#1677ff');
      return {
        name: s.name,
        type: 'line',
        smooth: true,
        symbol: 'none',
        data: s.data,
        itemStyle: { color: c },
        lineStyle: { color: c, width: 1.5 },
        areaStyle: { color: c, opacity: 0.12 },
      };
    }),
  };
}

/* ── Agent 离线告警横幅 ── */

function AgentOfflineAlert() {
  const agentEvents = useRuntimeStore((s) => s.agentEvents);
  const agents = useRuntimeStore((s) => s.agents);
  const [dismissedKeys, setDismissedKeys] = useState<Set<string>>(new Set());

  // 取最近未关闭的 offline / restarted 事件
  const offlineAlerts = useMemo(() => {
    const onlineIds = new Set(agents.filter((a) => a.status !== 'offline').map((a) => a.agentId));
    return agentEvents
      .filter(
        (e) =>
          (e.type === 'offline' || e.type === 'restarted') &&
          !dismissedKeys.has(e.agentId + e.timestamp + e.type),
      )
      .filter((e) => e.type === 'restarted' || !onlineIds.has(e.agentId)) // offline 已恢复的不显示；restarted 是永久事件
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
          节点{' '}
          {offlineAlerts
            .map((e) => `"${e.agentName || e.agentId}"(${e.type === 'restarted' ? '重启' : '离线'})`)
            .join('、')}{' '}
          异常，任务继续运行中（{onlineCount}/{totalCount} 在线）
        </span>
      }
      closable
      onClose={() => {
        const keys = new Set(dismissedKeys);
        offlineAlerts.forEach((e) => keys.add(e.agentId + e.timestamp + e.type));
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
        <ActionsSection />
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
  const {
    latestStress,
    latestSystem,
    systemHistory,
    stressHistory,
    agents,
    rampUpEnabled,
    rampUpStages,
  } = useRuntimeStore(
    useShallow((s) => ({
      latestStress: s.latestStress,
      latestSystem: s.latestSystem,
      systemHistory: s.systemHistory,
      stressHistory: s.stressHistory,
      agents: s.agents,
      rampUpEnabled: s.rampUpEnabled,
      rampUpStages: s.rampUpStages,
    })),
  );
  const theme = useEditorStore((s) => s.theme);
  const dark = theme === 'dark';

  // 迷你趋势图
  const cpuOption = useMemo(() => {
    if (systemHistory.length < 2) return null;
    return sparkOption([{ name: 'CPU%', data: systemHistory.map((s) => s.avgCpuPercent), color: 'var(--chart-orange)' }], dark);
  }, [systemHistory]);

  const qpsOption = useMemo(() => {
    if (stressHistory.length < 2) return null;
    const totalQps = stressHistory.map((s) => (s.actions ?? []).reduce((sum, a) => sum + a.avgQps, 0));
    return sparkOption([{ name: 'QPS', data: totalQps, color: 'var(--chart-blue)' }], dark);
  }, [stressHistory]);

  // 渐进式加压阶段计算（Hooks 必须在条件返回之前调用）
  const rampUpTotal = rampUpEnabled
    ? rampUpStages.reduce((sum: number, s) => sum + (s.count || 0), 0)
    : 0;
  const rampUpStageInfo = useMemo(() => {
    if (!rampUpEnabled || rampUpStages.length === 0 || rampUpTotal === 0) return null;
    const running = latestStress?.robots?.running ?? 0;
    let cumulative = 0;
    for (let i = 0; i < rampUpStages.length; i++) {
      const prev = cumulative;
      cumulative += rampUpStages[i].count || 0;
      if (running <= cumulative) {
        const progress = Math.min(1, Math.max(0, (running - prev) / Math.max(1, rampUpStages[i].count)));
        return { current: i, progress };
      }
    }
    return { current: rampUpStages.length - 1, progress: 1 };
  }, [rampUpEnabled, rampUpStages, rampUpTotal, latestStress?.robots?.running]);

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

  const r = latestStress.robots ?? { started: 0, running: 0, stopped: 0, errored: 0 };
  const c = latestStress.connections ?? { established: 0, failed: 0, dropped: 0 };
  const b = latestStress.bandwidth ?? { sendMBps: 0, recvMBps: 0 };
  const sys = latestSystem;
  const actions = latestStress.actions ?? [];

  const activeConns = Math.max(0, c.established - c.dropped);
  const onlineAgents = (agents ?? []).filter((a) => a.status !== 'offline').length;
  const totalAgents = (agents ?? []).length;
  const send = fmtBandwidth(b.sendMBps ?? 0);
  const recv = fmtBandwidth(b.recvMBps ?? 0);
  const robotPercent = r.started > 0 ? Math.round((r.running / r.started) * 100) : 0;

  // 加权集群成功率。
  let totalSamples = 0, wSuccess = 0;
  for (const a of actions) {
    totalSamples += a.sampleCount;
    wSuccess += a.successRate * a.sampleCount;
  }
  const clusterSuccess = totalSamples > 0 ? wSuccess / totalSamples : 0;

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

        {/* 负载 / 渐进式阶段进度 */}
        <div className="md-load-row">
          <div className="md-load-header">
            {rampUpStageInfo ? (
              <span className="md-load-title">渐进式 阶段 {rampUpStageInfo.current + 1}/{rampUpStages.length}</span>
            ) : (
              <span className="md-load-title">负载 {robotPercent}%</span>
            )}
            {r.errored > 0 && <span className="md-chip md-chip-errored">错 {r.errored}</span>}
          </div>
          {rampUpStageInfo ? (
            <div className="md-stage-bar">
              {rampUpStages.map((stage: { count: number }, i: number) => {
                const widthPct = rampUpTotal > 0 ? (stage.count / rampUpTotal) * 100 : 100 / rampUpStages.length;
                let fill = 0;
                if (i < rampUpStageInfo.current) fill = 100;
                else if (i === rampUpStageInfo.current) fill = rampUpStageInfo.progress * 100;
                return (
                  <div key={i} className="md-stage-segment" style={{ width: `${widthPct}%` }}>
                    <div
                      className={`md-stage-fill${i === rampUpStageInfo.current ? ' md-stage-fill--active' : ''}`}
                      style={{ width: `${fill}%` }}
                    />
                  </div>
                );
              })}
            </div>
          ) : (
            <div className="md-load-progress">
              <Progress percent={robotPercent} strokeColor="var(--color-success)" showInfo={false} size={4} />
            </div>
          )}
        </div>

        {/* 数据网格 */}
        <div className="md-grid-row">
          <div className="md-grid-item">
            <span className="md-grid-label" style={{ color: 'var(--chart-cyan)' }}>↑ 发送</span>
            <span className="md-grid-value">{send.value.toFixed(send.precision)} {send.suffix}</span>
          </div>
          <div className="md-grid-item">
            <span className="md-grid-label" style={{ color: 'var(--chart-purple)' }}>↓ 接收</span>
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
            <EChartsReact option={cpuOption} style={{ height: 'calc(100% - 16px)' }} notMerge lazyUpdate />
          ) : (
            <div style={{ color: 'var(--text-tertiary)', fontSize: 10, textAlign: 'center', paddingTop: 12 }}>等待数据…</div>
          )}
        </div>
        <div className="monitor-dock__chart-card monitor-dock__chart-card--qps">
          <div className="monitor-dock__chart-title">QPS</div>
          {qpsOption ? (
            <EChartsReact option={qpsOption} style={{ height: 'calc(100% - 16px)' }} notMerge lazyUpdate />
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

function ActionsSection() {
  const latestStress = useRuntimeStore((s) => s.latestStress);
  const containerRef = useRef<HTMLDivElement>(null);
  const [scrollY, setScrollY] = useState(200);

  // ResizeObserver 监听容器实际高度变化，无论触发原因（拖拽 / 折叠 / 窗口缩放 / 首次渲染）
  useLayoutEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    const update = () => {
      const containerH = el.clientHeight;
      const thead = el.querySelector('.ant-table-thead');
      const headerH = thead?.getBoundingClientRect().height ?? 37;
      setScrollY(Math.max(60, containerH - headerH - 2));
    };
    update();
    const ro = new ResizeObserver(update);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  return (
    <div className="monitor-dock__actions" ref={containerRef}>
      {latestStress ? (
        <ActionMetricsTable
          rows={latestStress.actions ?? []}
          compact
          size="small"
          scrollY={scrollY}
          popupZIndex={1200}
          showCanceledColumn={false}
        />
      ) : (
        <div style={{ color: 'var(--text-tertiary)', fontSize: 12, textAlign: 'center', paddingTop: 20 }}>
          暂无数据
        </div>
      )}
    </div>
  );
}
