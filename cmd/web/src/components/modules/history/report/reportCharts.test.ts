/**
 * 报告图表 option 构建单测。
 *
 * 只断言「画出来的东西会不会被 ECharts 认」和口径筛选，不做像素级校验：
 * markLine 曾经被挂在 option 根上，ECharts 静默忽略，图看着正常但少了阈值基准线。
 */

import { describe, expect, it } from 'vitest';
import type { BarSeriesOption } from 'echarts';
import type { HistoryActionMetric } from '@/types/api';
import { APDEX_THRESHOLDS } from '@/services/metricsBinding';
import { buildApdexOption, buildRankingOption, buildSuccessDonutOption } from './reportCharts';

const emptyHist = {
  count: 0,
  minMs: null,
  maxMs: null,
  avgMs: null,
  p50Ms: null,
  p90Ms: null,
  p95Ms: null,
  p99Ms: null,
};

const hist = (p99: number) => ({
  count: 10,
  minMs: 1,
  maxMs: p99,
  avgMs: p99 / 2,
  p50Ms: p99 / 2,
  p90Ms: p99,
  p95Ms: p99,
  p99Ms: p99,
});

function action(over: Partial<HistoryActionMetric> = {}): HistoryActionMetric {
  return {
    name: 'login',
    sampleCount: 10,
    successCount: 10,
    failureCount: 0,
    timeoutCount: 0,
    canceledCount: 0,
    executing: 0,
    successRate: 1,
    avgSendBytes: 32,
    avgRecvBytes: 64,
    kind: 'networked',
    rttApdex: 0.9,
    rttApdexSampleCount: 10,
    rtt: hist(80),
    listenWait: emptyHist,
    listenWaitSampleCount: 0,
    listenTimeoutRate: 0,
    totalDuration: hist(120),
    nonRTTAvgMs: 1,
    buildAvgMs: 0,
    encodeAvgMs: 0,
    sendAvgMs: 0,
    decodeWaitAvgMs: 0,
    decodeAvgMs: 0,
    dispatchToActionWaitAvgMs: 0,
    parseStoreAvgMs: 0,
    rttSampleCount: 10,
    totalDurationSampleCount: 10,
    avgQps: 5,
    ...over,
  };
}

/** 取第一条 series（构建函数都只产一条柱状 series）。 */
function firstSeries(option: ReturnType<typeof buildApdexOption>): BarSeriesOption {
  const series = option?.series;
  expect(Array.isArray(series)).toBe(true);
  return (series as BarSeriesOption[])[0];
}

describe('buildApdexOption', () => {
  it('阈值参考线挂在 series 上（挂 option 根会被 ECharts 忽略）', () => {
    const option = buildApdexOption([action()]);
    expect(option).not.toBeNull();
    expect(option).not.toHaveProperty('markLine');

    const markLine = firstSeries(option).markLine;
    expect(markLine?.data).toMatchObject([
      { yAxis: APDEX_THRESHOLDS.excellent },
      { yAxis: APDEX_THRESHOLDS.good },
    ]);
  });

  it('只排往返类：没有 RTT 样本的动作不入图，全都没有则返回 null', () => {
    const listenOnly = action({
      name: 'push',
      kind: 'listen',
      rttSampleCount: 0,
      rttApdexSampleCount: 0,
      rtt: emptyHist,
      listenWait: hist(500),
      listenWaitSampleCount: 5,
    });
    expect(buildApdexOption([listenOnly])).toBeNull();

    const option = buildApdexOption([listenOnly, action()]);
    expect(firstSeries(option).data).toHaveLength(1);
  });
});

describe('buildSuccessDonutOption', () => {
  it('取消计入构成，圈心总数与样本数对得上', () => {
    const option = buildSuccessDonutOption([
      action({
        sampleCount: 10,
        successCount: 6,
        failureCount: 1,
        timeoutCount: 1,
        canceledCount: 2,
      }),
    ]);
    const data = (option.series as Array<{ data: Array<{ name: string; value: number }> }>)[0].data;
    expect(data).toMatchObject([
      { name: '成功', value: 6 },
      { name: '失败', value: 1 },
      { name: '超时', value: 1 },
      { name: '取消', value: 2 },
    ]);
    const centerText = (option.graphic as Array<{ style: { text: string } }>)[0].style.text;
    expect(centerText).toBe('10');
  });

  it('零值项不进图例', () => {
    const option = buildSuccessDonutOption([
      action({
        sampleCount: 5,
        successCount: 5,
        failureCount: 0,
        timeoutCount: 0,
        canceledCount: 0,
      }),
    ]);
    const data = (option.series as Array<{ data: Array<{ name: string }> }>)[0].data;
    expect(data.map((d) => d.name)).toEqual(['成功']);
  });
});

describe('buildRankingOption', () => {
  it('轴名写明 RTT：图里筛掉了没有 RTT 样本的动作', () => {
    const option = buildRankingOption([action()]);
    expect(option).not.toBeNull();
    expect(option!.xAxis).toMatchObject({ name: 'RTT p99 (ms)' });
  });

  it('推送回调（callback: 前缀）不参与动作排行', () => {
    expect(buildRankingOption([action({ name: 'callback:onPush' })])).toBeNull();
  });
});
