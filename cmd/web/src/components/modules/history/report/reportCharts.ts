/**
 * 报告图表：4 个趋势 + 2 个排行 + 2 个饼图的 ECharts option 构建函数 + 离屏渲染为 PNG。
 *
 * 所有图表使用固定浅色配色，不依赖 CSS 变量。
 * captureChartAsPng 利用 echarts.init 在离屏 canvas 上渲染，导出 base64 PNG。
 */

import * as echarts from 'echarts/core';
import { BarChart, LineChart, PieChart } from 'echarts/charts';
import {
  GraphicComponent,
  GridComponent,
  LegendComponent,
  MarkLineComponent,
  TooltipComponent,
} from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';
import type { DefaultLabelFormatterCallbackParams, EChartsOption, GraphicComponentOption } from 'echarts';
import type {
  HistoryActionMetric,
  HistoryTrendPoint,
} from '@/types/api';
import { classifyApdex } from '@/services/metricsBinding';
import { resolveKind } from '@/components/monitoring/shared/ActionMetricsTable';

echarts.use([
  BarChart,
  LineChart,
  PieChart,
  GraphicComponent,
  GridComponent,
  LegendComponent,
  MarkLineComponent,
  TooltipComponent,
  CanvasRenderer,
]);

/* ── 离屏渲染工具 ── */

export function captureChartAsPng(
  option: EChartsOption,
  width: number,
  height: number,
): string {
  const canvas = document.createElement('canvas');
  const chart = echarts.init(canvas as unknown as HTMLElement, undefined, {
    width,
    height,
    renderer: 'canvas',
  });
  chart.setOption(option);
  const dataUrl = chart.getDataURL({ type: 'png', pixelRatio: 2 });
  chart.dispose();
  return dataUrl;
}

/* ── 公共样式 ── */

const FONT = "12px -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', sans-serif";
const COLORS = {
  blue: '#1677ff',
  green: '#52c41a',
  yellow: '#faad14',
  orange: '#fa8c16',
  red: '#f5222d',
  purple: '#722ed1',
  cyan: '#13c2c2',
  gray: '#8c8c8c',
  lightGray: '#d9d9d9',
};

/* ── 1. QPS 趋势 ── */

export function buildQpsTrendOption(
  points: HistoryTrendPoint[],
): EChartsOption | null {
  if (points.length === 0) return null;

  const x = points.map((p) => `${p.elapsedSec}s`);
  const data = points.map((p) => p.totalQps);

  return {
    animation: false,
    tooltip: { trigger: 'axis', textStyle: { fontSize: 11 } },
    grid: { left: 44, right: 8, top: 12, bottom: 24 },
    xAxis: {
      type: 'category',
      data: x,
      axisLabel: { fontSize: 10, hideOverlap: true },
    },
    yAxis: { type: 'value', axisLabel: { fontSize: 10 } },
    series: [{
      name: 'QPS',
      type: 'line',
      smooth: true,
      symbol: 'none',
      data,
      itemStyle: { color: COLORS.blue },
      areaStyle: { opacity: 0.08 },
      lineStyle: { width: 2 },
    }],
  };
}

/* ── 2. Apdex 趋势 ── */

export function buildApdexTrendOption(
  points: HistoryTrendPoint[],
): EChartsOption | null {
  if (points.length === 0) return null;

  const x = points.map((p) => `${p.elapsedSec}s`);
  // Apdex 只有 RTT 一种口径：监听等待是 ms 量纲、且没有普遍阈值，同轴画在 0~1 上无意义。
  const hasRttApdex = points.some((p) => p.rttApdex !== null && Number.isFinite(p.rttApdex));
  if (!hasRttApdex) return null;
  const series = [
    {
      name: 'RTT Apdex',
      type: 'line' as const,
      smooth: true,
      symbol: 'none',
      connectNulls: false,
      data: points.map((p) => p.rttApdex !== null && Number.isFinite(p.rttApdex) ? p.rttApdex : null),
      itemStyle: { color: COLORS.red },
      areaStyle: { opacity: 0.08 },
      lineStyle: { width: 2 },
    },
  ];

  return {
    animation: false,
    tooltip: { trigger: 'axis', textStyle: { fontSize: 11 } },
    legend: { right: 0, top: 0, textStyle: { fontSize: 10, fontFamily: FONT } },
    grid: { left: 36, right: 8, top: 28, bottom: 24 },
    xAxis: {
      type: 'category',
      data: x,
      axisLabel: { fontSize: 10, hideOverlap: true },
    },
    yAxis: { type: 'value', max: 1, min: 0, axisLabel: { fontSize: 10 } },
    series,
  };
}

/* ── 3. CPU 趋势 ── */

export function buildCpuTrendOption(
  points: HistoryTrendPoint[],
): EChartsOption | null {
  if (points.length === 0) return null;

  const x = points.map((p) => `${p.elapsedSec}s`);
  const data = points.map((p) => +p.avgCpuPercent.toFixed(2));

  return {
    animation: false,
    tooltip: { trigger: 'axis', textStyle: { fontSize: 11 } },
    grid: { left: 40, right: 8, top: 12, bottom: 24 },
    xAxis: {
      type: 'category',
      data: x,
      axisLabel: { fontSize: 10, hideOverlap: true },
    },
    yAxis: { type: 'value', max: 100, axisLabel: { fontSize: 10 } },
    series: [{
      name: 'CPU%',
      type: 'line',
      smooth: true,
      symbol: 'none',
      data,
      itemStyle: { color: COLORS.orange },
      areaStyle: { opacity: 0.08 },
      lineStyle: { width: 2 },
    }],
  };
}

/* ── 4. 带宽趋势 ── */

export function buildBwTrendOption(
  points: HistoryTrendPoint[],
): EChartsOption | null {
  if (points.length === 0) return null;

  const x = points.map((p) => `${p.elapsedSec}s`);
  const send = points.map((p) => +p.sendKBps.toFixed(2));
  const recv = points.map((p) => +p.recvKBps.toFixed(2));

  return {
    animation: false,
    tooltip: { trigger: 'axis', textStyle: { fontSize: 11 } },
    legend: { right: 0, top: 0, textStyle: { fontSize: 10, fontFamily: FONT } },
    grid: { left: 44, right: 8, top: 28, bottom: 24 },
    xAxis: {
      type: 'category',
      data: x,
      axisLabel: { fontSize: 10, hideOverlap: true },
    },
    yAxis: {
      type: 'value',
      name: 'KB/s',
      axisLabel: { fontSize: 10 },
      nameTextStyle: { fontSize: 10 },
    },
    series: [
      {
        name: '↑ 发送',
        type: 'line',
        smooth: true,
        symbol: 'none',
        data: send,
        itemStyle: { color: COLORS.cyan },
        areaStyle: { opacity: 0.08 },
        lineStyle: { width: 2 },
      },
      {
        name: '↓ 接收',
        type: 'line',
        smooth: true,
        symbol: 'none',
        data: recv,
        itemStyle: { color: COLORS.purple },
        areaStyle: { opacity: 0.08 },
        lineStyle: { width: 2 },
      },
    ],
  };
}

/* ── 5. 动作性能排行（p99 水平条形图） ── */

export function buildRankingOption(
  actions: HistoryActionMetric[],
): EChartsOption | null {
  const top = [...actions]
    .filter((a) => !a.name.startsWith('callback:') && (a.rttSampleCount ?? 0) > 0 && a.rtt)
    .sort((a, b) => a.rtt.p99Ms - b.rtt.p99Ms)
    .slice(-15);
  if (top.length === 0) return null;

  const names = top.map((a) => a.name.length > 22 ? a.name.slice(0, 20) + '…' : a.name);
  const values = top.map((a) => a.rtt.p99Ms);
  const colors = values.map((v) =>
    v < 100 ? COLORS.green : v < 500 ? COLORS.yellow : COLORS.red,
  );

  return {
    animation: false,
    tooltip: { trigger: 'axis', textStyle: { fontSize: 11 } },
    grid: { left: 160, right: 40, top: 8, bottom: 8 },
    xAxis: { type: 'value', name: 'p99 (ms)', axisLabel: { fontSize: 10 } },
    yAxis: {
      type: 'category',
      data: names,
      axisLabel: { fontSize: 10, width: 140, overflow: 'truncate' },
    },
    series: [{
      type: 'bar',
      data: values.map((v, i) => ({
        value: v,
        itemStyle: { color: colors[i] },
      })),
      barMaxWidth: 18,
      label: {
        show: true,
        position: 'right',
        fontSize: 10,
        formatter: (p: DefaultLabelFormatterCallbackParams) => `${formatterNumber(p.value).toFixed(0)}ms`,
      },
    }],
  };
}

/* ── 6. 延迟分布（分组柱状图） ── */

export function buildLatencyOption(
  actions: HistoryActionMetric[],
): EChartsOption | null {
  const top = [...actions]
    .filter((a) => !a.name.startsWith('callback:') && (a.rttSampleCount ?? 0) > 0 && a.rtt)
    .sort((a, b) => b.rtt.p99Ms - a.rtt.p99Ms)
    .slice(0, 10);
  if (top.length === 0) return null;

  const names = top.map((a) => a.name.length > 16 ? a.name.slice(0, 14) + '…' : a.name);

  return {
    animation: false,
    tooltip: { trigger: 'axis', textStyle: { fontSize: 11 } },
    legend: { top: 0, textStyle: { fontSize: 10 } },
    grid: { left: 50, right: 20, top: 32, bottom: 28 },
    xAxis: { type: 'category', data: names, axisLabel: { fontSize: 9, rotate: 20 } },
    yAxis: { type: 'value', name: 'ms', axisLabel: { fontSize: 10 } },
    series: [
      { name: 'p50', type: 'bar', data: top.map((a) => a.rtt.p50Ms), itemStyle: { color: COLORS.green }, barMaxWidth: 14 },
      { name: 'p95', type: 'bar', data: top.map((a) => a.rtt.p95Ms), itemStyle: { color: COLORS.yellow }, barMaxWidth: 14 },
      { name: 'p99', type: 'bar', data: top.map((a) => a.rtt.p99Ms), itemStyle: { color: COLORS.red }, barMaxWidth: 14 },
    ],
  };
}

/* ── 7. 成功/失败构成（环形图） ── */

export function buildSuccessDonutOption(
  actions: HistoryActionMetric[],
): EChartsOption {
  let success = 0, failure = 0, timeout = 0;
  for (const a of actions) {
    success += a.successCount;
    failure += a.failureCount;
    timeout += a.timeoutCount;
  }
  const total = success + failure + timeout;

  return {
    animation: false,
    tooltip: { trigger: 'item', textStyle: { fontSize: 11 } },
    legend: { bottom: 0, textStyle: { fontSize: 11 } },
    series: [{
      type: 'pie',
      radius: ['40%', '70%'],
      center: ['50%', '45%'],
      avoidLabelOverlap: true,
      label: { show: true, fontSize: 11, formatter: '{b}: {d}%' },
      data: [
        { value: success, name: '成功', itemStyle: { color: COLORS.green } },
        { value: failure, name: '失败', itemStyle: { color: COLORS.red } },
        { value: timeout, name: '超时', itemStyle: { color: COLORS.orange } },
      ].filter((d) => d.value > 0),
    }],
    graphic: total > 0 ? [{
      type: 'text',
      left: 'center',
      top: '38%',
      style: {
        text: total.toLocaleString(),
        fontSize: 16,
        fontWeight: 700,
        fill: '#1a1a1a',
        align: 'center' as const,
      },
    }, {
      type: 'text',
      left: 'center',
      top: '50%',
      style: {
        text: '总样本',
        fontSize: 10,
        fill: '#8c8c8c',
        align: 'center' as const,
      },
    }] as GraphicComponentOption[] : [],
  };
}

/* ── 8. Apdex 分布（彩色柱状图） ── */

const APDEX_COLORS: Record<string, string> = {
  excellent: COLORS.green,
  good: '#bae637',
  fair: COLORS.yellow,
  poor: COLORS.orange,
  danger: COLORS.red,
  unknown: COLORS.lightGray,
};

export function buildApdexOption(
  actions: HistoryActionMetric[],
): EChartsOption | null {
  // 只排往返类：其余类别没有 Apdex，混进来会以 0 分霸占「最差」的位置。
  const sorted = [...actions]
    .filter((a) => !a.name.startsWith('callback:') && resolveKind(a) === 'networked' && (a.rttSampleCount ?? 0) > 0)
    .sort((a, b) => a.rttApdex - b.rttApdex);
  if (sorted.length === 0) return null;

  const names = sorted.map((a) => a.name.length > 18 ? a.name.slice(0, 16) + '…' : a.name);

  return {
    animation: false,
    tooltip: { trigger: 'axis', textStyle: { fontSize: 11 } },
    grid: { left: 50, right: 20, top: 16, bottom: 28 },
    xAxis: { type: 'category', data: names, axisLabel: { fontSize: 9, rotate: 20 } },
    yAxis: { type: 'value', max: 1, min: 0, axisLabel: { fontSize: 10 } },
    series: [{
      type: 'bar',
      data: sorted.map((a) => ({
        value: a.rttApdex,
        itemStyle: { color: APDEX_COLORS[classifyApdex(a.rttApdex)] },
      })),
      barMaxWidth: 24,
    }],
    markLine: {
      silent: true,
      lineStyle: { type: 'dashed' },
      data: [
        { yAxis: 0.94, label: { formatter: 'excellent', fontSize: 9 }, lineStyle: { color: COLORS.green } },
        { yAxis: 0.85, label: { formatter: 'good', fontSize: 9 }, lineStyle: { color: '#bae637' } },
      ],
    },
  };
}

/* ── 9. 错误分布（环形图） ── */

export function buildErrorOption(
  actions: HistoryActionMetric[],
): EChartsOption | null {
  const errorMap = new Map<string, { count: number; label: string }>();
  for (const a of actions) {
    for (const e of a.errors ?? []) {
      const key = `${e.code}`;
      const label = e.codeName || `#${e.code}`;
      const existing = errorMap.get(key);
      if (existing) {
        existing.count += e.count;
      } else {
        errorMap.set(key, { count: e.count, label });
      }
    }
  }
  if (errorMap.size === 0) return null;

  const sorted = [...errorMap.entries()]
    .sort((a, b) => b[1].count - a[1].count)
    .slice(0, 8);

  const errorColors = ['#f5222d', '#fa541c', '#fa8c16', '#faad14', '#fadb14', '#d48806', '#ad6800', '#874d00'];

  return {
    animation: false,
    tooltip: { trigger: 'item', textStyle: { fontSize: 11 } },
    series: [{
      type: 'pie',
      radius: ['30%', '65%'],
      center: ['50%', '50%'],
      label: {
        show: true,
        fontSize: 10,
        formatter: (p: DefaultLabelFormatterCallbackParams) => {
          const name = p.name.length > 20 ? p.name.slice(0, 18) + '…' : p.name;
          const percent = 'percent' in p && typeof p.percent === 'number' ? p.percent : 0;
          return `{cnt|×${formatterNumber(p.value)}}  {name|${name}}\n{pct|${percent}%}`;
        },
        rich: {
          cnt: { fontSize: 10, fontWeight: 700, color: '#cf1322', lineHeight: 16 },
          name: { fontSize: 10, color: '#333', lineHeight: 16 },
          pct: { fontSize: 9, color: '#8c8c8c', lineHeight: 14 },
        },
      },
      labelLayout: { hideOverlap: true },
      data: sorted.map(([, info], i) => ({
        value: info.count,
        name: info.label,
        itemStyle: { color: errorColors[i % errorColors.length] },
      })),
    }],
  };
}

function formatterNumber(value: DefaultLabelFormatterCallbackParams['value']): number {
  const scalar = Array.isArray(value) ? value[0] : value;
  return typeof scalar === 'number' ? scalar : Number(scalar ?? 0);
}

/* ── 统一捕获所有图表 ── */

export interface ChartImages {
  qps: string | null;
  apdexTrend: string | null;
  cpu: string | null;
  bandwidth: string | null;
  ranking: string | null;
  rtt: string | null;
  successDonut: string | null;
  apdexBar: string | null;
  errors: string | null;
}

export function captureAllCharts(
  actions: HistoryActionMetric[],
  points: HistoryTrendPoint[],
): ChartImages {
  const safeCapture = (
    fn: () => EChartsOption | null,
    w: number,
    h: number,
  ): string | null => {
    const opt = fn();
    if (!opt) return null;
    return captureChartAsPng(opt, w, h);
  };

  const trendW = 480;
  const trendH = 240;

  const qps = safeCapture(() => buildQpsTrendOption(points), trendW, trendH);
  const apdexTrend = safeCapture(() => buildApdexTrendOption(points), trendW, trendH);
  const cpu = safeCapture(() => buildCpuTrendOption(points), trendW, trendH);
  const bandwidth = safeCapture(() => buildBwTrendOption(points), trendW, trendH);

  const ranking = safeCapture(() => buildRankingOption(actions), 700, Math.max(200, Math.min(actions.length, 15) * 32));
  const rtt = safeCapture(() => buildLatencyOption(actions), 700, 300);
  const successDonut = actions.length > 0
    ? captureChartAsPng(buildSuccessDonutOption(actions), 350, 280)
    : null;
  const apdexBar = safeCapture(() => buildApdexOption(actions), 700, 260);
  const errors = safeCapture(() => buildErrorOption(actions), 500, 300);

  return { qps, apdexTrend, cpu, bandwidth, ranking, rtt, successDonut, apdexBar, errors };
}
