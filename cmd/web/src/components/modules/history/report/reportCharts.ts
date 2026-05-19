/**
 * 报告图表：6 个 ECharts option 构建函数 + 离屏渲染为 PNG 的工具。
 *
 * 所有图表使用固定浅色配色，不依赖 CSS 变量。
 * captureChartAsPng 利用 echarts.init 在离屏 canvas 上渲染，导出 base64 PNG。
 */

import * as echarts from 'echarts';
import type { ActionMetric, TimeseriesPoint, StressSnapshot } from '@/types/api';
import { classifyApdex } from '@/services/metricsBinding';

/* ── 离屏渲染工具 ── */

export function captureChartAsPng(
  option: echarts.EChartsOption,
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
  gray: '#8c8c8c',
  lightGray: '#d9d9d9',
};

/* ── 1. QPS & Apdex 趋势 ── */

export function buildTrendOption(
  stressTs: TimeseriesPoint[],
): echarts.EChartsOption | null {
  if (stressTs.length === 0) return null;

  const x = stressTs.map((p) => `${p.elapsedSec}s`);
  const qpsData = stressTs.map((p) => {
    const snap = (p.snapshot ?? {}) as Partial<StressSnapshot>;
    return (snap.actions ?? []).reduce((sum, a) => sum + a.avgQps, 0);
  });
  const apdexData = stressTs.map((p) => {
    const snap = (p.snapshot ?? {}) as Partial<StressSnapshot>;
    let total = 0, w = 0;
    for (const a of snap.actions ?? []) {
      total += a.apdex * a.sampleCount;
      w += a.sampleCount;
    }
    return w > 0 ? +(total / w).toFixed(3) : 0;
  });

  return {
    animation: false,
    tooltip: { trigger: 'axis', textStyle: { fontSize: 11 } },
    legend: { right: 0, textStyle: { fontSize: 11, fontFamily: FONT } },
    grid: { left: 50, right: 50, top: 32, bottom: 28 },
    xAxis: {
      type: 'category',
      data: x,
      axisLabel: { fontSize: 10, hideOverlap: true },
    },
    yAxis: [
      { type: 'value', name: 'QPS', axisLabel: { fontSize: 10 } },
      { type: 'value', name: 'Apdex', max: 1, min: 0, axisLabel: { fontSize: 10 } },
    ],
    series: [
      {
        name: 'QPS',
        type: 'line',
        smooth: true,
        symbol: 'none',
        data: qpsData,
        itemStyle: { color: COLORS.blue },
        lineStyle: { width: 2 },
      },
      {
        name: 'Apdex',
        type: 'line',
        smooth: true,
        symbol: 'none',
        data: apdexData,
        yAxisIndex: 1,
        itemStyle: { color: COLORS.green },
        lineStyle: { width: 2 },
      },
    ],
  };
}

/* ── 2. 动作性能排行（p99 水平条形图） ── */

export function buildRankingOption(
  actions: ActionMetric[],
): echarts.EChartsOption {
  const top = [...actions]
    .filter((a) => !a.name.startsWith('callback:'))
    .sort((a, b) => b.latency.p99Ms - a.latency.p99Ms)
    .slice(0, 15);

  const names = top.map((a) => a.name.length > 22 ? a.name.slice(0, 20) + '…' : a.name);
  const values = top.map((a) => a.latency.p99Ms);
  const colors = values.map((v) =>
    v < 100 ? COLORS.green : v < 500 ? COLORS.yellow : COLORS.red,
  );

  return {
    animation: false,
    tooltip: { trigger: 'axis', textStyle: { fontSize: 11 } },
    grid: { left: 160, right: 40, top: 8, bottom: 8 },
    xAxis: { type: 'value', name: 'p99 (ms)', axisLabel: { fontSize: 10 } },
    yAxis: { type: 'category', data: names.reverse(), axisLabel: { fontSize: 10, width: 140, overflow: 'truncate' } },
    series: [{
      type: 'bar',
      data: values.reverse().map((v, i) => ({
        value: v,
        itemStyle: { color: colors.reverse()[i] },
      })),
      barMaxWidth: 18,
      label: {
        show: true,
        position: 'right',
        fontSize: 10,
        formatter: (p: { value: number }) => `${p.value.toFixed(0)}ms`,
      },
    }],
  };
}

/* ── 3. 延迟分布（分组柱状图） ── */

export function buildLatencyOption(
  actions: ActionMetric[],
): echarts.EChartsOption {
  const top = [...actions]
    .filter((a) => !a.name.startsWith('callback:'))
    .sort((a, b) => b.latency.p99Ms - a.latency.p99Ms)
    .slice(0, 10);

  const names = top.map((a) => a.name.length > 16 ? a.name.slice(0, 14) + '…' : a.name);

  return {
    animation: false,
    tooltip: { trigger: 'axis', textStyle: { fontSize: 11 } },
    legend: { top: 0, textStyle: { fontSize: 10 } },
    grid: { left: 50, right: 20, top: 32, bottom: 28 },
    xAxis: { type: 'category', data: names, axisLabel: { fontSize: 9, rotate: 20 } },
    yAxis: { type: 'value', name: 'ms', axisLabel: { fontSize: 10 } },
    series: [
      { name: 'p50', type: 'bar', data: top.map((a) => a.latency.p50Ms), itemStyle: { color: COLORS.green }, barMaxWidth: 14 },
      { name: 'p90', type: 'bar', data: top.map((a) => a.latency.p90Ms), itemStyle: { color: '#bae637' }, barMaxWidth: 14 },
      { name: 'p95', type: 'bar', data: top.map((a) => a.latency.p95Ms), itemStyle: { color: COLORS.yellow }, barMaxWidth: 14 },
      { name: 'p99', type: 'bar', data: top.map((a) => a.latency.p99Ms), itemStyle: { color: COLORS.red }, barMaxWidth: 14 },
    ],
  };
}

/* ── 4. 成功/失败构成（环形图） ── */

export function buildSuccessDonutOption(
  actions: ActionMetric[],
): echarts.EChartsOption {
  let success = 0, failure = 0, timeout = 0, skipped = 0;
  for (const a of actions) {
    success += a.successCount;
    failure += a.failureCount;
    timeout += a.timeoutCount;
    skipped += a.skippedCount;
  }
  const total = success + failure + timeout + skipped;

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
        { value: skipped, name: '跳过', itemStyle: { color: COLORS.lightGray } },
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
        textAlign: 'center',
      },
    }, {
      type: 'text',
      left: 'center',
      top: '50%',
      style: {
        text: '总样本',
        fontSize: 10,
        fill: '#8c8c8c',
        textAlign: 'center',
      },
    }] : [],
  };
}

/* ── 5. Apdex 分布（彩色柱状图） ── */

const APDEX_COLORS: Record<string, string> = {
  excellent: COLORS.green,
  good: '#bae637',
  fair: COLORS.yellow,
  poor: COLORS.orange,
  danger: COLORS.red,
  unknown: COLORS.lightGray,
};

export function buildApdexOption(
  actions: ActionMetric[],
): echarts.EChartsOption {
  const sorted = [...actions]
    .filter((a) => !a.name.startsWith('callback:'))
    .sort((a, b) => a.apdex - b.apdex);

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
        value: a.apdex,
        itemStyle: { color: APDEX_COLORS[classifyApdex(a.apdex)] },
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

/* ── 6. 错误分布（环形图） ── */

export function buildErrorOption(
  actions: ActionMetric[],
): echarts.EChartsOption | null {
  const allErrors: Map<string, number> = new Map();
  for (const a of actions) {
    for (const e of a.errors ?? []) {
      for (const msg of e.msgs) {
        allErrors.set(msg, (allErrors.get(msg) ?? 0) + e.count);
      }
    }
  }
  if (allErrors.size === 0) return null;

  const sorted = [...allErrors.entries()]
    .sort((a, b) => b[1] - a[1])
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
        formatter: (p: { name: string; percent: number }) => {
          const name = p.name.length > 30 ? p.name.slice(0, 28) + '…' : p.name;
          return `${name} (${p.percent}%)`;
        },
      },
      labelLayout: { hideOverlap: true },
      data: sorted.map(([msg, count], i) => ({
        value: count,
        name: msg,
        itemStyle: { color: errorColors[i % errorColors.length] },
      })),
    }],
  };
}

/* ── 统一捕获所有图表 ── */

export interface ChartImages {
  trend: string | null;
  ranking: string | null;
  latency: string | null;
  successDonut: string | null;
  apdex: string | null;
  errors: string | null;
}

export function captureAllCharts(
  actions: ActionMetric[],
  stressTs: TimeseriesPoint[],
): ChartImages {
  const safeCapture = (
    fn: () => echarts.EChartsOption | null,
    w: number,
    h: number,
  ): string | null => {
    const opt = fn();
    if (!opt) return null;
    return captureChartAsPng(opt, w, h);
  };

  const trend = safeCapture(() => buildTrendOption(stressTs), 700, 280);
  const ranking = actions.length > 0
    ? captureChartAsPng(buildRankingOption(actions), 700, Math.max(200, Math.min(actions.length, 15) * 32))
    : null;
  const latency = safeCapture(() => buildLatencyOption(actions), 700, 300);
  const successDonut = actions.length > 0
    ? captureChartAsPng(buildSuccessDonutOption(actions), 350, 280)
    : null;
  const apdex = safeCapture(() => buildApdexOption(actions), 700, 260);
  const errors = safeCapture(() => buildErrorOption(actions), 500, 300);

  return { trend, ranking, latency, successDonut, apdex, errors };
}
