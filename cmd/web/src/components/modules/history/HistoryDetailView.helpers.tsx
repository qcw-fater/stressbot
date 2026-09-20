/**
 * HistoryDetailView 的展示子组件与纯计算函数：
 * 指标卡片/区块包装/趋势卡、ECharts option 构建、汇总派生与格式化工具。
 * 从 HistoryDetailView 拆出，保持纯函数与受控展示组件，不持有数据获取逻辑。
 */

import { Empty, Tag, Tooltip } from 'antd';
import type { ReactNode } from 'react';
import type { EChartsOption } from 'echarts';
import { EChartsReact } from '@/components/monitoring/shared/EChartsReact';
import { resolveKind } from '@/components/monitoring/shared/actionKind';
import {
  fmtMs,
  fmtPercent as fmtPercentShared,
  fmtScore as fmtScoreShared,
} from '@/components/monitoring/shared/formats';
import type {
  CleanupStatus,
  HistoryActionMetric,
  HistoryDetail,
  HistoryTrendPoint,
} from '@/types/api';

export interface ActionInsight {
  label: string;
  name: string;
  value: string;
  tone?: 'good' | 'warn' | 'bad' | 'blue' | 'purple';
}

export interface DerivedSummary {
  totalSamples: number;
  totalSuccess: number;
  totalFailures: number;
  totalTimeouts: number;
  totalCanceled: number;
  totalErrors: number;
  successRate: number | null;
  rttApdex: number | null;
  avgQps: number;
  peakQps: number;
  peakCpu: number;
  peakAvgMem: number;
  peakMaxMem: number;
  peakBotsRunning: number;
  peakBotsErrored: number;
  offlinePoints: number;
  cleanupIssueCount: number;
  failedAgents: number;
  slowestAction?: HistoryActionMetric;
  worstApdexAction?: HistoryActionMetric;
  mostFailedAction?: HistoryActionMetric;
  busiestAction?: HistoryActionMetric;
}

interface MetricTileProps {
  label: string;
  value: string;
  sub?: string;
  tone?: 'good' | 'warn' | 'bad' | 'blue' | 'purple';
}

export function MetricTile({ label, value, sub, tone = 'blue' }: MetricTileProps) {
  return (
    <div className={`hp-kpi-tile hp-kpi-tile--${tone}`}>
      <div className="hp-kpi-tile__label">{label}</div>
      <div className="hp-kpi-tile__value">{value}</div>
      {sub && <div className="hp-kpi-tile__sub">{sub}</div>}
    </div>
  );
}

/**
 * 无数据时给中性色。
 *
 * 这两个指标的值都可能是 null（没有样本 / 没有往返类动作），显示成 "—"；
 * 若染色时把 null 当 0，格子会变成刺眼的红色，和"不适用"这个文案对不上。
 */
export function successTone(rate: number | null): MetricTileProps['tone'] {
  if (rate == null) return 'blue';
  return rate >= 0.95 ? 'good' : rate >= 0.8 ? 'warn' : 'bad';
}

export function apdexTone(score: number | null): MetricTileProps['tone'] {
  if (score == null) return 'blue';
  return score >= 0.9 ? 'good' : score >= 0.75 ? 'warn' : 'bad';
}

interface ReportSectionProps {
  title: string;
  subtitle?: string;
  children: ReactNode;
}

export function ReportSection({ title, subtitle, children }: ReportSectionProps) {
  return (
    <section className="hp-glass hp-glass-thin hp-report-section">
      <div className="hp-report-section__header">
        <div>
          <div className="hp-report-section__title">{title}</div>
          {subtitle && <div className="hp-report-section__subtitle">{subtitle}</div>}
        </div>
      </div>
      {children}
    </section>
  );
}

interface TrendCardProps {
  title: string;
  option: EChartsOption | null;
  value?: string;
}

export function TrendCard({ title, option, value }: TrendCardProps) {
  return (
    <div className="hp-chart-card">
      <div className="hp-chart-card__header">
        <span>{title}</span>
        {value && <code>{value}</code>}
      </div>
      {option ? (
        <EChartsReact option={option} style={{ height: 170 }} notMerge lazyUpdate />
      ) : (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无数据" />
      )}
    </div>
  );
}

interface FactProps {
  label: string;
  value: string;
}

export function Fact({ label, value }: FactProps) {
  return (
    <div className="hp-config-fact">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

export interface StageMark {
  x: string;
  label: string;
  reset: boolean;
}

// computeStageLines 计算趋势图阶段切换线 / reset 段落断点。
//   - 阶段段落视图（已按段过滤）：不画线。
//   - 有 reset 整体视图：按时序 stageIndex 变化点画 RESET 断点。
//   - 无 reset 渐进式：忽略时序 stageIndex，按 rampUp 配置累计 holdSec 近似画阶段切换线。
export function computeStageLines(
  points: HistoryTrendPoint[],
  rampUp: { stages: Array<{ count?: number; holdSec?: number; reset?: boolean }> } | null,
  isStageView: boolean,
  stageRange?: { from?: number; to?: number },
): StageMark[] {
  if (points.length === 0) return [];

  const nearestX = (elapsed: number): string | null => {
    let best: HistoryTrendPoint | null = null;
    let bestDiff = Infinity;
    for (const p of points) {
      const diff = Math.abs(p.elapsedSec - elapsed);
      if (diff < bestDiff) {
        bestDiff = diff;
        best = p;
      }
    }
    return best ? `${best.elapsedSec}s` : null;
  };

  const hasReset = rampUp?.stages.some((s) => s.reset) ?? false;
  const rangeFrom = stageRange?.from ?? 0;
  const rangeTo = stageRange?.to ?? 0;

  // 1) 阶段段落详情已按「轮」过滤，但同一轮可能覆盖多个配置阶段，例如第 2 轮 · S2-S3。
  // 这种情况下仍需要在该轮内部画 S3 这类阶段切换线。
  if (isStageView) {
    if (!rampUp || rangeFrom <= 0 || rangeTo <= rangeFrom) return [];
    const marks: StageMark[] = [];
    let cum = points[0]?.elapsedSec ?? 0;
    for (let stageNo = rangeFrom; stageNo < rangeTo; stageNo++) {
      cum += Math.max(rampUp.stages[stageNo - 1]?.holdSec || 0, 30);
      const x = nearestX(cum);
      if (x) marks.push({ x, label: `S${stageNo + 1}`, reset: false });
    }
    return marks;
  }

  // 2) 只有 reset 任务才把时序 stageIndex 当作「第 N 轮」段落号。
  // 无 reset 任务的历史数据即使残留了 stageIndex，也不能显示成第 52/53 轮。
  const tagged = hasReset && points.some((p) => (p.stageIndex ?? -1) > 0);
  if (tagged) {
    const marks: StageMark[] = [];
    let prev = points[0]?.stageIndex ?? -1;
    for (const p of points) {
      const cur = p.stageIndex ?? -1;
      if (cur > 0 && cur !== prev) {
        marks.push({ x: `${p.elapsedSec}s`, label: `第 ${cur} 轮`, reset: true });
        prev = cur;
      }
    }
    return marks;
  }

  // 3) 非 reset ramp-up：按配置累计 holdSec 近似画阶段切换线。
  if (!hasReset && rampUp && rampUp.stages.length > 1) {
    const marks: StageMark[] = [];
    let cum = 0;
    for (let i = 0; i < rampUp.stages.length; i++) {
      if (i > 0) {
        const x = nearestX(cum);
        if (x) marks.push({ x, label: `S${i + 1}`, reset: !!rampUp.stages[i].reset });
      }
      cum += Math.max(rampUp.stages[i].holdSec || 0, 30);
    }
    return marks;
  }
  return [];
}

export function buildChartOptions(points: HistoryTrendPoint[], stageMarks: StageMark[] = []) {
  const hasPoints = points.length > 0;
  const x = points.map((p) => `${p.elapsedSec}s`);
  const root = document.documentElement;
  const css = (v: string, fb: string) => getComputedStyle(root).getPropertyValue(v).trim() || fb;
  const axisLine = css('--chart-axis', 'rgba(0, 0, 0, 0.15)');
  const labelClr = css('--chart-label', 'rgba(0, 0, 0, 0.65)');
  const splitClr = css('--chart-split', 'rgba(0, 0, 0, 0.06)');
  const tipBg = css('--chart-tip-bg', 'rgba(255, 255, 255, 0.96)');
  const tipBorder = css('--chart-tip-border', 'rgba(0, 0, 0, 0.09)');
  const tipText = css('--chart-tip-text', '#333');
  const tooltip = {
    trigger: 'axis' as const,
    backgroundColor: tipBg,
    borderColor: tipBorder,
    textStyle: { color: tipText, fontSize: 11 },
  };
  const palette = {
    blue: css('--chart-blue', '#1677ff'),
    cyan: css('--chart-cyan', '#13c2c2'),
    green: css('--chart-green', '#52c41a'),
    orange: css('--chart-orange', '#fa8c16'),
    purple: css('--chart-purple', '#722ed1'),
    red: css('--chart-red', '#f5222d'),
    lime: css('--chart-lime', '#bae637'),
  };
  const resetClr = css('--chart-orange', '#fa8c16');
  const markLine = stageMarks.length
    ? {
        silent: true,
        symbol: 'none' as const,
        data: stageMarks.map((m) => ({
          xAxis: m.x,
          label: { formatter: m.label, fontSize: 9, color: m.reset ? resetClr : labelClr },
          lineStyle: {
            color: m.reset ? resetClr : axisLine,
            type: 'dashed' as const,
            width: m.reset ? 1.5 : 1,
          },
        })),
      }
    : undefined;
  const line = (
    series: Array<{
      name: string;
      data: Array<number | null>;
      color: string;
      dashed?: boolean;
      area?: number;
    }>,
    yMax?: number,
  ): EChartsOption => ({
    // 归档数据是静态的，入场动画只是让八张图同时跑一遍补间。
    // 报告侧（reportCharts）一直是关的，这里跟上。
    animation: false,
    tooltip,
    legend: { right: 0, top: 0, textStyle: { fontSize: 10, color: labelClr } },
    grid: { left: 34, right: 8, top: 26, bottom: 22 },
    xAxis: {
      type: 'category',
      data: x,
      axisLabel: { fontSize: 10, color: labelClr },
      axisLine: { lineStyle: { color: axisLine } },
    },
    yAxis: {
      type: 'value',
      max: yMax,
      axisLabel: { fontSize: 10, color: labelClr },
      splitLine: { lineStyle: { color: splitClr } },
    },
    series: series.map((s, i) => ({
      name: s.name,
      type: 'line',
      smooth: true,
      symbol: 'none',
      data: s.data,
      connectNulls: false,
      emphasis: { focus: 'series' },
      areaStyle: { opacity: s.area ?? 0.045 },
      itemStyle: { color: s.color },
      lineStyle: { width: s.dashed ? 1.5 : 1.8, type: s.dashed ? 'dashed' : 'solid' },
      ...(i === 0 && markLine ? { markLine } : {}),
    })),
  });

  const point = (v: number | null | undefined, digits = 2): number | null =>
    typeof v === 'number' && Number.isFinite(v) ? +v.toFixed(digits) : null;
  const rttApdex = points.map((p) => point(p.rttApdex, 3));
  return {
    qpsOption: hasPoints
      ? line([
          {
            name: 'QPS',
            data: points.map((p) => point(p.totalQps)),
            color: palette.blue,
            area: 0.06,
          },
        ])
      : null,
    // Apdex 只有 RTT 一种口径。监听等待另出分位数图——它是 ms 量纲，与 0~1 的分数同轴无意义。
    apdexOption: hasPoints
      ? line([{ name: 'RTT Apdex', data: rttApdex, color: palette.cyan, area: 0.055 }], 1)
      : null,
    listenWaitOption: hasPoints
      ? line([
          {
            name: '监听等待 P99',
            data: points.map((p) => point(p.listenWaitP99Ms)),
            color: palette.green,
            area: 0.055,
          },
        ])
      : null,
    loadOption: hasPoints
      ? line([
          {
            name: '运行',
            data: points.map((p) => point(p.botsRunning, 0)),
            color: palette.blue,
            area: 0.055,
          },
          {
            name: '异常',
            data: points.map((p) => point(p.botsErrored, 0)),
            color: palette.red,
            dashed: true,
          },
        ])
      : null,
    costOption: hasPoints
      ? line([
          {
            name: '客户端',
            data: points.map((p) => point(p.nonRTTAvgMs)),
            color: palette.purple,
            area: 0.05,
          },
          {
            name: '编码',
            data: points.map((p) => point(p.encodeAvgMs)),
            color: palette.orange,
            dashed: true,
          },
          {
            name: '解码',
            data: points.map((p) => point(p.decodeAvgMs)),
            color: palette.cyan,
            dashed: true,
          },
        ])
      : null,
    bandwidthOption: hasPoints
      ? line([
          {
            name: '↑发送',
            data: points.map((p) => point(p.sendKBps)),
            color: palette.cyan,
            area: 0.055,
          },
          {
            name: '↓接收',
            data: points.map((p) => point(p.recvKBps)),
            color: palette.purple,
            area: 0.035,
          },
        ])
      : null,
    cpuOption: hasPoints
      ? line(
          [
            {
              name: '集群均值',
              data: points.map((p) => point(p.avgCpuPercent)),
              color: palette.orange,
              area: 0.045,
            },
            {
              name: '最高节点',
              data: points.map((p) => point(p.maxCpuPercent)),
              color: palette.red,
              dashed: true,
            },
          ],
          100,
        )
      : null,
    memOption: hasPoints
      ? line(
          [
            {
              name: '集群使用率',
              data: points.map((p) => point(p.avgMemPercent)),
              color: palette.lime,
              area: 0.045,
            },
            {
              name: '最高节点',
              data: points.map((p) => point(p.maxMemPercent)),
              color: palette.purple,
              dashed: true,
            },
          ],
          100,
        )
      : null,
  };
}

export function deriveSummary(detail: HistoryDetail, points: HistoryTrendPoint[]): DerivedSummary {
  const actions = detail.finalSnapshot.actions;
  const metrics = detail.finalSnapshot.summary;
  const totalSuccess = metrics.successCount;
  const totalFailures = metrics.failureCount;
  const totalTimeouts = metrics.timeoutCount;
  const totalCanceled = metrics.canceledCount;
  const totalSamples = metrics.sampleCount;
  const totalErrors = totalFailures + totalTimeouts + totalCanceled;
  const actionErrorScore = (a: HistoryActionMetric) =>
    (a.failureCount || 0) +
    (a.timeoutCount || 0) +
    (a.canceledCount || 0) +
    sum(a.errors ?? [], (e) => e.count);
  return {
    totalSamples,
    totalSuccess,
    totalFailures,
    totalTimeouts,
    totalCanceled,
    totalErrors,
    // 没有样本时给 null 而不是 0（与下一行的 rttApdex 同口径）：
    // 0 会被显示成 "0.00%"，读起来像全军覆没，而实际是这一场根本没跑起来
    successRate: totalSamples > 0 ? metrics.successRate : null,
    rttApdex: metrics.rttApdexSampleCount > 0 ? metrics.rttApdex : null,
    avgQps: metrics.avgQps,
    peakQps: max(points, (p) => p.totalQps),
    peakCpu: Math.max(
      detail.finalSystem.maxCpuPercent,
      max(points, (p) => p.maxCpuPercent || p.avgCpuPercent),
    ),
    peakAvgMem: Math.max(
      detail.finalSystem.avgMemPercent || 0,
      max(points, (p) => p.avgMemPercent),
    ),
    peakMaxMem: Math.max(
      detail.finalSystem.maxMemPercent || 0,
      max(points, (p) => p.maxMemPercent || p.avgMemPercent),
    ),
    peakBotsRunning: Math.max(
      detail.finalSnapshot.robots?.running ?? 0,
      max(points, (p) => p.botsRunning),
    ),
    peakBotsErrored: Math.max(
      detail.finalSnapshot.robots?.errored ?? 0,
      max(points, (p) => p.botsErrored),
    ),
    offlinePoints: points.filter((p) => p.offlineCount > 0).length,
    cleanupIssueCount: detail.agentReports.filter(
      (r) => r.cleanupStatus && r.cleanupStatus.status && r.cleanupStatus.status !== 'ok',
    ).length,
    failedAgents: detail.agentReports.filter((r) => r.result === 'failed').length,
    slowestAction: maxBy(
      actions.filter((a) => a.totalDurationSampleCount > 0 && a.totalDuration.p99Ms != null),
      (a) => a.totalDuration.p99Ms ?? -1,
    ),
    // 「最差 Apdex」只在往返类里找：其余类别没有分，参与比较等于拿 0 分去当最差。
    worstApdexAction: minBy(
      actions.filter((a) => resolveKind(a) === 'networked' && a.rttApdexSampleCount > 0),
      (a) => a.rttApdex,
    ),
    mostFailedAction: maxBy(
      actions.filter((a) => actionErrorScore(a) > 0),
      actionErrorScore,
    ),
    busiestAction: maxBy(actions, (a) => a.sampleCount),
  };
}

export function buildActionInsights(summary: DerivedSummary): ActionInsight[] {
  const out: ActionInsight[] = [];
  if (summary.slowestAction)
    out.push({
      label: '最慢动作',
      name: summary.slowestAction.name,
      value: `P99 ${fmtMs(summary.slowestAction.totalDuration.p99Ms!)}`,
      tone: 'warn',
    });
  if (summary.worstApdexAction)
    out.push({
      label: '最差 RTT Apdex',
      name: summary.worstApdexAction.name,
      value: fmtScore(summary.worstApdexAction.rttApdex),
      tone: summary.worstApdexAction.rttApdex < 0.75 ? 'bad' : 'warn',
    });
  if (summary.mostFailedAction)
    out.push({
      label: '错误最多',
      name: summary.mostFailedAction.name,
      value: `${summary.mostFailedAction.failureCount + summary.mostFailedAction.timeoutCount + (summary.mostFailedAction.canceledCount ?? 0)} 次`,
      tone: 'bad',
    });
  if (summary.busiestAction)
    out.push({
      label: '样本最多',
      name: summary.busiestAction.name,
      value: summary.busiestAction.sampleCount.toLocaleString(),
      tone: 'blue',
    });
  return out;
}

export function buildDiagnostics(
  detail: HistoryDetail,
  summary: DerivedSummary,
  points: HistoryTrendPoint[],
) {
  const items: Array<{ label: string; value: string; tone: 'good' | 'warn' | 'bad' | 'blue' }> = [];
  if (summary.totalErrors > 0)
    items.push({
      label: '失败/超时/取消',
      value: `${summary.totalErrors.toLocaleString()} 次`,
      tone: summary.totalErrors > 100 ? 'bad' : 'warn',
    });
  if (detail.finalSnapshot.connections.failed + detail.finalSnapshot.connections.dropped > 0)
    items.push({
      label: '连接异常',
      value: `失败 ${detail.finalSnapshot.connections.failed} / 断开 ${detail.finalSnapshot.connections.dropped}`,
      tone: 'warn',
    });
  if (summary.peakCpu >= 80)
    items.push({ label: 'CPU 高水位', value: `${summary.peakCpu.toFixed(1)}%`, tone: 'bad' });
  if (summary.peakMaxMem >= 85)
    items.push({ label: 'MEM 高水位', value: `${summary.peakMaxMem.toFixed(1)}%`, tone: 'bad' });
  if (summary.offlinePoints > 0)
    items.push({ label: '节点离线采样', value: `${summary.offlinePoints} 个点`, tone: 'bad' });
  if (summary.cleanupIssueCount > 0)
    items.push({ label: '清理异常', value: `${summary.cleanupIssueCount} 个节点`, tone: 'warn' });
  if (summary.slowestAction && (summary.slowestAction.totalDuration.p99Ms ?? 0) > 1000)
    items.push({
      label: '慢动作',
      value: `${summary.slowestAction.name} P99 ${fmtMs(summary.slowestAction.totalDuration.p99Ms!)}`,
      tone: 'warn',
    });
  if (detail.agentEvents?.some((e) => e.type === 'restarted'))
    items.push({
      label: '节点重启',
      value: `${detail.agentEvents.filter((e) => e.type === 'restarted').length} 次`,
      tone: 'bad',
    });
  if (items.length === 0)
    items.push({
      label: '稳定性',
      value: points.length > 0 ? '未发现明显异常' : '无采样异常',
      tone: 'good',
    });
  return items;
}

export function renderCleanup(cleanup: CleanupStatus | undefined) {
  if (!cleanup || !cleanup.status)
    return <span style={{ color: 'var(--text-tertiary)' }}>未记录</span>;
  const map: Record<string, { color: string; label: string }> = {
    ok: { color: 'success', label: '清理完成' },
    partial: { color: 'warning', label: '部分清理' },
    timeout: { color: 'error', label: '清理超时' },
    unknown: { color: 'default', label: '未知' },
  };
  const info = map[cleanup.status as string] ?? { color: 'default', label: cleanup.status };
  const detailLines: string[] = [];
  if (cleanup.message) detailLines.push(cleanup.message);
  if (cleanup.timeoutRobots) detailLines.push(`超时机器人 ${cleanup.timeoutRobots}`);
  if (cleanup.luaSkipped) detailLines.push(`脚本运行时未归还 ${cleanup.luaSkipped}`);
  return (
    <Tooltip title={detailLines.join('；') || info.label}>
      <Tag color={info.color}>{info.label}</Tag>
    </Tooltip>
  );
}

export function fmtKBpsPeak(points: HistoryTrendPoint[], key: 'sendKBps' | 'recvKBps') {
  const peak = max(points, (p) => p[key]);
  if (peak >= 1024) return `${(peak / 1024).toFixed(2)} MB/s`;
  return `${peak.toFixed(1)} KB/s`;
}

// 报告正文比监控面板多留一位小数（Apdex 三位、成功率两位），其余语义与 shared/formats
// 完全一致，所以这里只是定死位数的薄封装，不再自己实现一遍格式化。
export function fmtScore(v: number | null | undefined) {
  return fmtScoreShared(v, 3);
}

export function fmtPercent(v: number | null | undefined) {
  return fmtPercentShared(v, 2);
}

export function sum<T>(items: T[], pick: (item: T) => number) {
  return items.reduce((acc, item) => acc + (Number.isFinite(pick(item)) ? pick(item) : 0), 0);
}

export function max<T>(items: T[], pick: (item: T) => number) {
  return items.reduce(
    (acc, item) => Math.max(acc, Number.isFinite(pick(item)) ? pick(item) : 0),
    0,
  );
}

export function maxBy<T>(items: T[], pick: (item: T) => number): T | undefined {
  let best: T | undefined;
  let bestValue = -Infinity;
  for (const item of items) {
    const value = pick(item);
    if (Number.isFinite(value) && value > bestValue) {
      best = item;
      bestValue = value;
    }
  }
  return best;
}

function minBy<T>(items: T[], pick: (item: T) => number): T | undefined {
  let best: T | undefined;
  let bestValue = Infinity;
  for (const item of items) {
    const value = pick(item);
    if (Number.isFinite(value) && value < bestValue) {
      best = item;
      bestValue = value;
    }
  }
  return best;
}

export function eventLabel(type: string) {
  if (type === 'offline') return '离线';
  if (type === 'restarted') return '重启丢任务';
  if (type === 'reconnected') return '恢复';
  return '注销';
}

export function eventColor(type: string) {
  if (type === 'offline') return 'error';
  if (type === 'restarted') return 'warning';
  if (type === 'reconnected') return 'success';
  return 'default';
}

export function resultLabel(v: string) {
  if (v === 'completed') return '完成';
  if (v === 'stopped') return '停止';
  if (v === 'failed') return '失败';
  return v;
}

export function resultColor(v: string) {
  if (v === 'completed') return 'success';
  if (v === 'failed') return 'error';
  return 'default';
}

export function formatDuration(sec: number): string {
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m${sec % 60}s`;
  return `${(sec / 3600).toFixed(1)}h`;
}
