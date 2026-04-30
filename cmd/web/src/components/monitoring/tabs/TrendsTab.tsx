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
import ReactECharts from 'echarts-for-react';
import { useMemo } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useRuntimeStore } from '@/services';

function lineOption(title: string, x: string[], series: Array<{ name: string; data: number[]; color?: string }>) {
  return {
    title: { text: title, left: 0, top: 0, textStyle: { fontSize: 12, fontWeight: 600 } },
    tooltip: { trigger: 'axis' },
    legend: { right: 0, top: 0, textStyle: { fontSize: 11 } },
    grid: { left: 36, right: 12, top: 28, bottom: 24 },
    xAxis: { type: 'category', data: x, axisLabel: { fontSize: 10, hideOverlap: true } },
    yAxis: { type: 'value', axisLabel: { fontSize: 10 } },
    series: series.map((s) => ({
      name: s.name,
      type: 'line',
      smooth: true,
      symbol: 'none',
      data: s.data,
      itemStyle: s.color ? { color: s.color } : undefined,
      areaStyle: { opacity: 0.08 },
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
      { name: 'running', data: stressHistory.map((s) => s.robots.running), color: '#52c41a' },
      { name: 'errored', data: stressHistory.map((s) => s.robots.errored), color: '#f5222d' },
    ]);
  }, [stressHistory]);

  const qpsOption = useMemo(() => {
    if (stressHistory.length === 0) return null;
    const x = stressHistory.map((s) => new Date(s.timestamp).toLocaleTimeString());
    const totalQps = stressHistory.map((s) => s.actions.reduce((sum, a) => sum + a.avgQps, 0));
    return lineOption('集群 QPS', x, [{ name: 'qps', data: totalQps, color: '#1677ff' }]);
  }, [stressHistory]);

  const cpuOption = useMemo(() => {
    if (systemHistory.length === 0) return null;
    const x = systemHistory.map((s) => new Date(s.timestamp).toLocaleTimeString());
    return lineOption('平均 CPU%', x, [
      { name: 'avg cpu', data: systemHistory.map((s) => s.avgCpuPercent), color: '#fa8c16' },
    ]);
  }, [systemHistory]);

  const bwOption = useMemo(() => {
    if (stressHistory.length === 0) return null;
    const x = stressHistory.map((s) => new Date(s.timestamp).toLocaleTimeString());
    return lineOption('带宽 MB/s', x, [
      { name: '↑ send', data: stressHistory.map((s) => +s.bandwidth.sendMBps.toFixed(2)), color: '#13c2c2' },
      { name: '↓ recv', data: stressHistory.map((s) => +s.bandwidth.recvMBps.toFixed(2)), color: '#722ed1' },
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
            <ReactECharts option={robotsOption} style={{ height: 200 }} notMerge lazyUpdate />
          </Card>
        </Col>
      )}
      {qpsOption && (
        <Col span={12}>
          <Card size="small" bodyStyle={{ padding: 8 }}>
            <ReactECharts option={qpsOption} style={{ height: 200 }} notMerge lazyUpdate />
          </Card>
        </Col>
      )}
      {cpuOption && (
        <Col span={12}>
          <Card size="small" bodyStyle={{ padding: 8 }}>
            <ReactECharts option={cpuOption} style={{ height: 200 }} notMerge lazyUpdate />
          </Card>
        </Col>
      )}
      {bwOption && (
        <Col span={12}>
          <Card size="small" bodyStyle={{ padding: 8 }}>
            <ReactECharts option={bwOption} style={{ height: 200 }} notMerge lazyUpdate />
          </Card>
        </Col>
      )}
    </Row>
  );
}
