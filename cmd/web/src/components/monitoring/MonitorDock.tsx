import { Button, Tooltip } from 'antd';
import { CaretDownOutlined, CaretUpOutlined, LineChartOutlined } from '@ant-design/icons';
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useRuntimeStore } from '@/services';
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';
import { ActionMetricsTable } from './shared/ActionMetricsTable';
import { buildLivePanelModel } from './shared/liveMetrics';
import {
  fmtBandwidthBytesPerSec,
  fmtByteSize,
  fmtCompactNumber,
  fmtDuration,
  fmtMs,
  fmtPercent,
  fmtPercentValue,
  fmtRate,
  fmtScore,
} from './shared/formats';
import './MonitorDock.css';

const MIN_H = 160;
const MAX_H_RATIO = 0.8;
const DEFAULT_H = 360;

const STATE_TEXT: Record<string, string> = {
  pending: '等待中',
  starting: '启动中',
  running: '运行中',
  stopping: '停止中',
  stopped: '已停止',
  failed: '失败',
};

function compactRatio(current: number, total: number) {
  if (total <= 0) return '—';
  return `${fmtCompactNumber(current)} / ${fmtCompactNumber(total)}`;
}

function fmtOptionalMs(value: number | null) {
  return value == null ? '—' : fmtMs(value);
}

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
      let lastHeight = height;
      const onMove = (ev: MouseEvent) => {
        if (!dragRef.current) return;
        const max = window.innerHeight * MAX_H_RATIO;
        const next = Math.max(
          MIN_H,
          Math.min(max, dragRef.current.startH + (dragRef.current.startY - ev.clientY)),
        );
        lastHeight = next;
        setHeight(next);
      };
      const onUp = () => {
        if (dragRef.current) localStorage.setItem('stressbot.monitorDock.h', String(lastHeight));
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
        <Button
          type="text"
          size="small"
          icon={<LineChartOutlined />}
          onClick={() => setDockOpen(true)}
        >
          展开监控
        </Button>
        <span style={{ color: 'var(--text-tertiary)' }}>点击展开实时监控</span>
        <Button
          type="text"
          size="small"
          icon={<CaretUpOutlined />}
          onClick={() => setDockOpen(true)}
        />
      </div>
    );
  }

  return (
    <div className="monitor-dock" style={{ height }}>
      <Tooltip title="拖动调整高度">
        <div className="monitor-dock__handle" onMouseDown={onDragStart} />
      </Tooltip>
      <div className="monitor-dock__body">
        {!topCollapsed && <TopSection />}
        <ActionsSection />
      </div>
      <div className="monitor-dock__controls">
        <Tooltip title={topCollapsed ? '展开指标区' : '收起指标区'}>
          <Button
            type="text"
            size="small"
            icon={<LineChartOutlined />}
            onClick={() => setTopCollapsed((v) => !v)}
            style={{ opacity: topCollapsed ? 0.5 : 1 }}
          />
        </Tooltip>
        <Tooltip title="折叠监控">
          <Button
            type="text"
            size="small"
            icon={<CaretDownOutlined />}
            onClick={() => setDockOpen(false)}
          />
        </Tooltip>
      </div>
    </div>
  );
}

function TopSection() {
  const {
    activeTask,
    latestStress,
    latestSystem,
    agents,
    reportingAgents,
    totalAgents,
    offlineAgents,
    assignedAgents,
  } = useRuntimeStore(
    useShallow((s) => ({
      activeTask: s.activeTask,
      latestStress: s.latestStress,
      latestSystem: s.latestSystem,
      agents: s.agents,
      reportingAgents: s.reportingAgents,
      totalAgents: s.totalAgents,
      offlineAgents: s.offlineAgents,
      assignedAgents: s.assignedAgents,
    })),
  );

  const model = useMemo(
    () =>
      buildLivePanelModel({
        latestStress,
        latestSystem,
        agents,
        reportingAgents,
        totalAgents,
        offlineAgents,
        assignedAgents,
      }),
    [
      latestStress,
      latestSystem,
      agents,
      reportingAgents,
      totalAgents,
      offlineAgents,
      assignedAgents,
    ],
  );

  if (!latestStress) {
    return <EmptyRuntimePanel activeTaskName={activeTask?.name} nodes={model.nodes} />;
  }

  return (
    <div className="monitor-dock__top monitor-dock__top--runtime">
      <TaskLoadCard
        taskName={activeTask?.name}
        taskState={activeTask?.state}
        uptimeSeconds={latestStress.uptimeSeconds}
        model={model}
      />
      <ThroughputQualityCard model={model} />
      <LatencyConnectionCard model={model} />
      <ResourceNodeCard model={model} />
    </div>
  );
}

function EmptyRuntimePanel({
  activeTaskName,
  nodes,
}: {
  activeTaskName?: string;
  nodes: ReturnType<typeof buildLivePanelModel>['nodes'];
}) {
  return (
    <div className="monitor-dock__top monitor-dock__top--empty">
      <div className="md-runtime-empty">
        <div>
          <div className="md-runtime-empty__title">等待任务采样</div>
          <div className="md-runtime-empty__desc">
            {activeTaskName
              ? `任务「${activeTaskName}」启动后显示实时监控指标。`
              : '启动任务后显示实时监控指标。'}
          </div>
        </div>
        <div className="md-runtime-empty__nodes">
          <span>
            在线节点 {nodes.online}/{nodes.total || '—'}
          </span>
          <span>容量 {compactRatio(nodes.capacityCurrent, nodes.capacityMax)}</span>
        </div>
      </div>
    </div>
  );
}

function TaskLoadCard({
  taskName,
  taskState,
  uptimeSeconds,
  model,
}: {
  taskName?: string;
  taskState?: string;
  uptimeSeconds: number;
  model: ReturnType<typeof buildLivePanelModel>;
}) {
  const rampUp = model.load.rampUp;

  return (
    <MetricGroup title="任务 / 负载" subtitle={taskName || '运行任务'}>
      <div className="md-load-card">
        <div className="md-load-prime">
          <div className="md-load-bots">
            <small>机器人</small>
            <strong>{compactRatio(model.load.runningRobots, model.load.startedRobots)}</strong>
          </div>
          <div className="md-load-meta">
            <span className={`md-load-state md-load-state--${taskState ?? 'running'}`}>
              {STATE_TEXT[taskState ?? ''] ?? '运行中'}
            </span>
            <span className="md-load-time">
              运行 <b>{fmtDuration(uptimeSeconds)}</b>
            </span>
          </div>
        </div>

        {rampUp ? <RampUpLoadView model={model} /> : <NormalLoadView model={model} />}
      </div>
    </MetricGroup>
  );
}

function NormalLoadView({ model }: { model: ReturnType<typeof buildLivePanelModel> }) {
  const progress = Math.round(model.load.robotPercent);
  return (
    <div className="md-load-main md-load-main--normal">
      <div className="md-load-progress-head">
        <span>负载进度</span>
        <b>{progress}%</b>
      </div>
      <LoadProgress percent={progress} />
    </div>
  );
}

function LoadProgress({ percent }: { percent: number }) {
  const safePercent = Math.max(0, Math.min(100, percent));
  return (
    <div className="md-load-progress" aria-label={`负载进度 ${safePercent}%`}>
      <div className="md-load-progress__fill" style={{ width: `${safePercent}%` }} />
    </div>
  );
}

function RampUpLoadView({ model }: { model: ReturnType<typeof buildLivePanelModel> }) {
  const rampUp = model.load.rampUp;
  const stages = useRuntimeStore(useShallow((s) => s.rampUpStages));
  if (!rampUp) return null;
  const totalTarget = stages.reduce((sum, stage) => sum + (stage.count || 0), 0);
  const stageStart = stages
    .slice(0, rampUp.currentStage - 1)
    .reduce((sum, stage) => sum + (stage.count || 0), 0);
  const currentStage = stages[rampUp.currentStage - 1];
  const stageTarget = currentStage?.count || 0;
  const stageRunning = Math.max(0, model.load.runningRobots - stageStart);
  const cumulativePercent =
    totalTarget > 0 ? Math.min(100, Math.round((model.load.runningRobots / totalTarget) * 100)) : 0;

  return (
    <div className="md-load-main md-load-main--ramp">
      <div className="md-ramp-line">
        <span>
          阶段{' '}
          <b>
            {rampUp.currentStage}/{rampUp.totalStages}
          </b>
        </span>
        <span>
          本阶段{' '}
          <b>
            {fmtCompactNumber(Math.min(stageRunning, stageTarget))}/{fmtCompactNumber(stageTarget)}
          </b>
        </span>
        <span>
          累计{' '}
          <b>
            {fmtCompactNumber(model.load.runningRobots)}/{fmtCompactNumber(totalTarget)}
          </b>
        </span>
      </div>
      <Tooltip
        title={`累计进度 ${cumulativePercent}% · ${currentStage?.concurrency ? `并发 ${currentStage.concurrency}` : '并发 —'} · ${currentStage?.holdSec ? `等待 ${currentStage.holdSec}s` : '等待 —'}`}
        mouseEnterDelay={0.4}
      >
        <div className="md-stage-bar">
          {stages.map((stage, i) => {
            const count = stage.count || 0;
            const widthPct =
              totalTarget > 0 ? (count / totalTarget) * 100 : 100 / Math.max(1, stages.length);
            const prev = stages.slice(0, i).reduce((sum, s) => sum + (s.count || 0), 0);
            const fill =
              count > 0
                ? Math.min(100, Math.max(0, ((model.load.runningRobots - prev) / count) * 100))
                : 0;
            return (
              <div key={i} className="md-stage-segment" style={{ width: `${widthPct}%` }}>
                <div
                  className={`md-stage-fill${i + 1 === rampUp.currentStage ? ' md-stage-fill--active' : ''}`}
                  style={{ width: `${fill}%` }}
                />
              </div>
            );
          })}
        </div>
      </Tooltip>
    </div>
  );
}

function ThroughputQualityCard({ model }: { model: ReturnType<typeof buildLivePanelModel> }) {
  return (
    <MetricGroup
      title="吞吐 / 结果"
      subtitle={`累计动作 ${fmtCompactNumber(model.throughput.totalActions)} · 压测采样 ${compactRatio(model.nodes.stressReporting, model.nodes.assigned)}`}
    >
      <div className="md-kpi-grid md-kpi-grid--three">
        <MetricCell
          label="当前 QPS"
          value={fmtRate(model.throughput.intervalQps)}
          title={model.throughput.intervalQps == null ? '等待下一轮采样' : undefined}
        />
        <MetricCell label="全程均值" value={fmtRate(model.throughput.lifetimeQps)} unit="/s" />
        <MetricCell label="成功率" value={fmtPercent(model.quality.successRate)} />
        <MetricCell label="样本" value={fmtCompactNumber(model.quality.sampleCount)} />
        <MetricCell label="成功" value={fmtCompactNumber(model.quality.successCount)} />
        <MetricCell label="失败" value={fmtCompactNumber(model.quality.failureCount)} />
        <MetricCell label="超时" value={fmtCompactNumber(model.quality.timeoutCount)} />
        <MetricCell label="取消" value={fmtCompactNumber(model.quality.canceledCount)} />
        <MetricCell label="执行中" value={fmtCompactNumber(model.quality.executing)} />
      </div>
    </MetricGroup>
  );
}

function LatencyConnectionCard({ model }: { model: ReturnType<typeof buildLivePanelModel> }) {
  return (
    <MetricGroup title="耗时 / 连接" subtitle={`RTT Apdex ${fmtScore(model.quality.rttApdex)}`}>
      <div className="md-kpi-grid md-kpi-grid--three">
        <MetricCell
          label="总 avg"
          value={fmtOptionalMs(model.latency.totalDurationAvgMs)}
          unit="ms"
        />
        <MetricCell
          label="总 p95"
          value={fmtOptionalMs(model.latency.totalDurationP95Ms)}
          unit="ms"
        />
        <MetricCell
          label="总 p99"
          value={fmtOptionalMs(model.latency.totalDurationP99Ms)}
          unit="ms"
        />
        <MetricCell label="RTT avg" value={fmtOptionalMs(model.latency.rttAvgMs)} unit="ms" />
        <MetricCell label="RTT p95" value={fmtOptionalMs(model.latency.rttP95Ms)} unit="ms" />
        <MetricCell label="非 RTT" value={fmtOptionalMs(model.latency.nonRTTAvgMs)} unit="ms" />
        <MetricCell label="活跃连接" value={fmtCompactNumber(model.connections.active)} />
        <MetricCell label="连接失败" value={fmtCompactNumber(model.connections.failed)} />
        <MetricCell label="已断开" value={fmtCompactNumber(model.connections.dropped)} />
      </div>
    </MetricGroup>
  );
}

function ResourceNodeCard({ model }: { model: ReturnType<typeof buildLivePanelModel> }) {
  const resourceCoverage = compactRatio(model.nodes.resourceReporting, model.nodes.resourceScope);
  return (
    <MetricGroup
      title="资源 / 节点"
      subtitle={`资源采样 ${resourceCoverage} · 在线 ${compactRatio(model.nodes.online, model.nodes.resourceScope)}`}
    >
      <div className="md-resource-hotline">
        <span>当前最高节点</span>
        <b>CPU {model.resources.hotHostCpuNode || '—'}</b>
        <b>内存 {model.resources.hotHostMemNode || '—'}</b>
      </div>
      <div className="md-kpi-grid md-kpi-grid--three md-resource-grid">
        <MetricCell label="主机 CPU 均值" value={fmtPercentValue(model.resources.avgHostCpuPercent)} />
        <MetricCell
          label="主机 CPU 最高"
          value={fmtPercentValue(model.resources.maxHostCpuPercent)}
          title={model.resources.hotHostCpuNode}
        />
        <MetricCell label="主机内存均值" value={fmtPercentValue(model.resources.avgHostMemPercent)} />
        <MetricCell
          label="主机内存最高"
          value={fmtPercentValue(model.resources.maxHostMemPercent)}
          title={model.resources.hotHostMemNode}
        />
        <MetricCell
          label="主机网卡发送"
          value={fmtBandwidthBytesPerSec(model.resources.hostSendBytesPerSec)}
          title={`全部网络接口合计 · 有效节点 ${model.resources.hostSendReportingAgents}/${model.nodes.resourceReporting || '—'}`}
        />
        <MetricCell
          label="主机网卡接收"
          value={fmtBandwidthBytesPerSec(model.resources.hostRecvBytesPerSec)}
          title={`全部网络接口合计 · 有效节点 ${model.resources.hostRecvReportingAgents}/${model.nodes.resourceReporting || '—'}`}
        />
        <MetricCell
          label="节点进程 CPU 均值"
          value={fmtPercentValue(model.resources.avgProcessCpuPercent)}
          title={`当前最高 ${model.resources.hotProcessCpuNode || '—'} · ${fmtPercentValue(model.resources.maxProcessCpuPercent)}`}
        />
        <MetricCell
          label="节点进程 RSS 总计"
          value={fmtByteSize(model.resources.totalProcessRssBytes)}
          title={`当前最高 ${model.resources.hotProcessRssNode || '—'} · ${fmtByteSize(model.resources.maxProcessRssBytes)}`}
        />
        <MetricCell
          label="单节点句柄 / FD 最高"
          value={fmtCompactNumber(model.resources.maxProcessFds)}
          title={`当前最高 ${model.resources.hotProcessFdsNode || '—'} · 总计 ${fmtCompactNumber(model.resources.totalProcessFds)}`}
        />
        <MetricCell
          label="集群容量"
          value={compactRatio(model.nodes.capacityCurrent, model.nodes.capacityMax)}
        />
        <MetricCell
          label="异常 / 离线"
          value={`${fmtCompactNumber(model.nodes.unhealthy)} / ${fmtCompactNumber(model.nodes.offline)}`}
        />
        <MetricCell
          label="资源缺采样"
          value={fmtCompactNumber(model.nodes.resourceMissing)}
          title={`过期 ${fmtCompactNumber(model.nodes.resourceStale)} · 协程 ${fmtCompactNumber(model.resources.totalProcessGoroutines)} · 线程 ${fmtCompactNumber(model.resources.totalProcessThreads)}`}
        />
      </div>
    </MetricGroup>
  );
}

function MetricGroup({
  title,
  subtitle,
  children,
}: {
  title: string;
  subtitle?: string;
  children: ReactNode;
}) {
  return (
    <section className="md-metric-group">
      <div className="md-metric-group__head">
        <span className="md-metric-group__title">{title}</span>
        {subtitle && (
          <Tooltip title={subtitle} mouseEnterDelay={0.4}>
            <span className="md-metric-group__subtitle">{subtitle}</span>
          </Tooltip>
        )}
      </div>
      {children}
    </section>
  );
}

function MetricCell({
  label,
  value,
  unit,
  title,
}: {
  label: string;
  value: string;
  unit?: string;
  title?: string;
}) {
  return (
    <Tooltip title={title} mouseEnterDelay={0.4}>
      <span className="md-kpi-cell">
        <span className="md-kpi-cell__label">{label}</span>
        <span className="md-kpi-cell__value">
          {value}
          {unit && value !== '—' ? <em>{unit}</em> : null}
        </span>
      </span>
    </Tooltip>
  );
}

function ActionsSection() {
  const latestStress = useRuntimeStore((s) => s.latestStress);
  const containerRef = useRef<HTMLDivElement>(null);
  const [scrollY, setScrollY] = useState(200);

  useLayoutEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    const update = () => {
      const containerH = el.clientHeight;
      const toolbar = el.querySelector('.action-metrics-table__toolbar');
      const thead = el.querySelector('.ant-table-thead');
      const toolbarH = toolbar?.getBoundingClientRect().height ?? 0;
      const headerH = thead?.getBoundingClientRect().height ?? 37;
      setScrollY(Math.max(60, containerH - toolbarH - headerH - 12));
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
          showClientBreakdown
          timingDetail={latestStress.timingDetail}
        />
      ) : (
        <div
          style={{
            color: 'var(--text-tertiary)',
            fontSize: 12,
            textAlign: 'center',
            paddingTop: 20,
          }}
        >
          暂无数据
        </div>
      )}
    </div>
  );
}
