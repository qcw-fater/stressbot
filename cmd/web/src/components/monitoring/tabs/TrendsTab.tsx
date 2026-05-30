/**
 * 趋势折线图：消费 runtimeStore 维护的滑动窗口（最多 60 个 stress / system snapshot）。
 *
 * 4 组关键趋势（轻量 echarts 双 y 轴线图）：
 *   1. 机器人状态：running / errored
 *   2. 总 QPS（所有动作 avgQps 求和）
 *   3. 集群 CPU%
 *   4. 带宽 MB/s（双向）
 */

import { Card, Col, Empty, Row } from 'antd';
import { EChartsReact } from '../shared/EChartsReact';
import { useMemo } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useRuntimeStore } from '@/services';

const cssVar = (v: string, fallback: string) =>
  getComputedStyle(document.documentElement).getPropertyValue(v).trim() || fallback;

const COLORS = {
  green: () => cssVar('--chart-green', '#52c41a'),
  red: () => cssVar('--chart-red', '#ff4d4f'),
  blue: () => cssVar('--chart-blue', '#1677ff'),
  orange: () => cssVar('--chart-orange', '#fa8c16'),
  cyan: () => cssVar('--chart-cyan', '#13c2c2'),
  purple: () => cssVar('--chart-purple', '#722ed1'),
  yellow: () => cssVar('--chart-yellow', '#faad14'),
};

const textColor = () => cssVar('--text-primary', '#333');
const splitColor = () => cssVar('--border-color', '#e8e8e8');
const tipBg = () => cssVar('--bg-elevated', '#fff');
const tipBorder = () => cssVar('--border-color', '#e8e8e8');

function lineOption(title: string, x: string[], series: Array<{ name: string; data: number[]; color: string }>) {
  return {
    title: { text: title, left: 0, top: 0, textStyle: { fontSize: 12, fontWeight: 600, color: textColor() } },
    tooltip: {
      trigger: 'axis',
      backgroundColor: tipBg(),
      borderColor: tipBorder(),
      textStyle: { color: textColor(), fontSize: 11 },
    },
    legend: { right: 0, top: 0, textStyle: { fontSize: 11, color: textColor() } },
    grid: { left: 36, right: 12, top: 28, bottom: 24 },
    xAxis: { type: 'category', data: x, axisLabel: { fontSize: 10, color: textColor(), hideOverlap: true }, splitLine: { lineStyle: { color: splitColor() } } },
    yAxis: { type: 'value', axisLabel: { fontSize: 10, color: textColor() }, splitLine: { lineStyle: { color: splitColor() } } },
    series: series.map((s) => ({
      name: s.name,
      type: 'line',
      smooth: true,
      symbol: 'none',
      data: s.data,
      itemStyle: { color: s.color },
      lineStyle: { color: s.color },
      areaStyle: { color: s.color, opacity: 0.08 },
    })),
  };
}

export function TrendsTab() {
  const { stressHistory, systemHistory } = useRuntimeStore(
    useShallow((s) => ({ stressHistory: s.stressHistory, systemHistory: s.systemHistory })),
  );

  const robotsOption = useMemo(() => {
    if (stressHistory.length === 0) return null;
    const x = stressHistory.map((s) => new Date(s.timestamp).toLocaleTimeString());
    return lineOption('机器人状态', x, [
      { name: 'running', data: stressHistory.map((s) => s.robots.running), color: COLORS.green() },
      { name: 'errored', data: stressHistory.map((s) => s.robots.errored), color: COLORS.red() },
    ]);
  }, [stressHistory]);

  const qpsOption = useMemo(() => {
    if (stressHistory.length === 0) return null;
    const x = stressHistory.map((s) => new Date(s.timestamp).toLocaleTimeString());
    const totalQps = stressHistory.map((s) => s.actions.reduce((sum, a) => sum + a.avgQps, 0));
    return lineOption('集群 QPS', x, [{ name: 'qps', data: totalQps, color: COLORS.blue() }]);
  }, [stressHistory]);

  const cpuOption = useMemo(() => {
    if (systemHistory.length === 0) return null;
    const x = systemHistory.map((s) => new Date(s.timestamp).toLocaleTimeString());
    return lineOption('平均 CPU%', x, [
      { name: 'avg cpu', data: systemHistory.map((s) => s.avgCpuPercent), color: COLORS.orange() },
    ]);
  }, [systemHistory]);

  const bwOption = useMemo(() => {
    if (stressHistory.length === 0) return null;
    const x = stressHistory.map((s) => new Date(s.timestamp).toLocaleTimeString());
    return lineOption('带宽 KB/s', x, [
      { name: '↑ send', data: stressHistory.map((s) => +(s.bandwidth.sendMBps * 1024).toFixed(2)), color: COLORS.cyan() },
      { name: '↓ recv', data: stressHistory.map((s) => +(s.bandwidth.recvMBps * 1024).toFixed(2)), color: COLORS.purple() },
    ]);
  }, [stressHistory]);

  const timingOption = useMemo(() => {
    if (stressHistory.length === 0) return null;
    const x = stressHistory.map((s) => new Date(s.timestamp).toLocaleTimeString());
    const weighted = (pick: (a: typeof stressHistory[number]['actions'][number]) => number, weightPick: (a: typeof stressHistory[number]['actions'][number]) => number) =>
      stressHistory.map((s) => {
        let sum = 0;
        let weight = 0;
        for (const a of s.actions) {
          const w = weightPick(a);
          if (w <= 0) continue;
          sum += pick(a) * w;
          weight += w;
        }
        return weight > 0 ? +(sum / weight).toFixed(2) : 0;
      });
    return lineOption('RTT 与客户端成本', x, [
      { name: 'RTT p95', data: weighted((a) => a.rtt.p95Ms, (a) => a.rttSampleCount), color: COLORS.red() },
      { name: 'client', data: weighted((a) => a.clientAvgMs, (a) => a.sampleCount), color: COLORS.blue() },
      { name: 'encode', data: weighted((a) => a.encodeAvgMs, (a) => a.sampleCount), color: COLORS.yellow() },
      { name: 'decode', data: weighted((a) => a.decodeAvgMs, (a) => a.rttSampleCount), color: COLORS.purple() },
    ]);
  }, [stressHistory]);

  if (stressHistory.length === 0) {
    return <Empty description="暂无数据；运行至少 1 个采集间隔后展示趋势" />;
  }

  return (
    <Row gutter={[12, 12]}>
      {robotsOption && (
        <Col span={12}>
          <Card size="small" bodyStyle={{ padding: 8 }}>
            <EChartsReact option={robotsOption} style={{ height: 200 }} notMerge lazyUpdate />
          </Card>
        </Col>
      )}
      {qpsOption && (
        <Col span={12}>
          <Card size="small" bodyStyle={{ padding: 8 }}>
            <EChartsReact option={qpsOption} style={{ height: 200 }} notMerge lazyUpdate />
          </Card>
        </Col>
      )}
      {cpuOption && (
        <Col span={12}>
          <Card size="small" bodyStyle={{ padding: 8 }}>
            <EChartsReact option={cpuOption} style={{ height: 200 }} notMerge lazyUpdate />
          </Card>
        </Col>
      )}
      {bwOption && (
        <Col span={12}>
          <Card size="small" bodyStyle={{ padding: 8 }}>
            <EChartsReact option={bwOption} style={{ height: 200 }} notMerge lazyUpdate />
          </Card>
        </Col>
      )}
      {timingOption && (
        <Col span={12}>
          <Card size="small" bodyStyle={{ padding: 8 }}>
            <EChartsReact option={timingOption} style={{ height: 200 }} notMerge lazyUpdate />
          </Card>
        </Col>
      )}
    </Row>
  );
}
