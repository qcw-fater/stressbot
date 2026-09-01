import { App as AntApp } from 'antd';
import { render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { HistoryCompareTask, HistoryDetail, TimeseriesResponse } from '@/types/api';
import { HistoryCompareView } from './HistoryCompareView';
import { HistoryDetailView } from './HistoryDetailView';

const mocks = vi.hoisted(() => ({
  compareHistory: vi.fn(),
  getHistory: vi.fn(),
  getHistoryTimeseries: vi.fn(),
  getHistoryConfig: vi.fn(),
  showApiError: vi.fn(),
}));

vi.mock('antd', async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>();
  return { ...actual, Table: () => <div data-testid="history-table" /> };
});

vi.mock('@/services', () => ({
  historyApi: {
    compareHistory: mocks.compareHistory,
    getHistory: mocks.getHistory,
    getHistoryTimeseries: mocks.getHistoryTimeseries,
    getHistoryConfig: mocks.getHistoryConfig,
  },
  isAbortError: (error: unknown) => error instanceof DOMException && error.name === 'AbortError',
  showApiError: mocks.showApiError,
}));

vi.mock('./report/useReportCapture', () => ({
  useReportCapture: () => vi.fn(),
}));

const pending = () => new Promise<never>(() => {});

const emptyHistogram = {
  count: 0,
  minMs: null,
  maxMs: null,
  avgMs: null,
  p50Ms: null,
  p90Ms: null,
  p95Ms: null,
  p99Ms: null,
};

const detailA: HistoryDetail = {
  id: 'history-a',
  name: '历史 A',
  state: 'stopped',
  totalBots: 10,
  agentCount: 1,
  activeAgentCount: 1,
  createdAt: '2026-09-01T00:00:00Z',
  startedAt: '2026-09-01T00:00:00Z',
  stoppedAt: '2026-09-01T00:01:00Z',
  durationSec: 60,
  starred: false,
  tags: [],
  debugMode: false,
  configSummary: {
    concurrency: 10,
    timeoutSec: 60,
    flowSizeKB: 1,
    protoCount: 1,
    scriptCount: 1,
  },
  agentReports: [],
  finalSnapshot: {
    uptimeSeconds: 60,
    totalActions: 0,
    apdexT: 500,
    timingDetail: 'rtt',
    summary: {
      sampleCount: 0,
      successCount: 0,
      failureCount: 0,
      timeoutCount: 0,
      canceledCount: 0,
      executing: 0,
      successRate: 0,
      rttApdex: 0,
      rttApdexSampleCount: 0,
      rtt: emptyHistogram,
      listenWait: emptyHistogram,
      totalDuration: emptyHistogram,
      nonRTTAvgMs: 0,
      clientCostCount: 0,
      buildAvgMs: 0,
      encodeAvgMs: 0,
      sendAvgMs: 0,
      decodeWaitAvgMs: 0,
      decodeAvgMs: 0,
      dispatchToActionWaitAvgMs: 0,
      parseStoreAvgMs: 0,
      buildSampleCount: 0,
      encodeSampleCount: 0,
      sendSampleCount: 0,
      decodeWaitSampleCount: 0,
      decodeSampleCount: 0,
      dispatchWaitSampleCount: 0,
      parseStoreSampleCount: 0,
      avgQps: 0,
    },
    robots: { started: 10, running: 0, stopped: 10, errored: 0 },
    connections: { established: 10, active: 0, closed: 10, failed: 0, dropped: 0 },
    bandwidth: { totalSendBytes: 0, totalRecvBytes: 0, sendMBps: 0, recvMBps: 0 },
    actions: [],
  },
  finalSystem: {
    totalMemMB: 1024,
    usedMemMB: 128,
    avgCpuPercent: 10,
    maxCpuPercent: 20,
    avgMemPercent: 12.5,
    maxMemPercent: 15,
    totalNetSendKBps: 0,
    totalNetRecvKBps: 0,
    totalGoroutines: 20,
    totalThreads: 4,
    totalFds: 8,
  },
};

const timeseriesA: TimeseriesResponse = {
  taskId: 'history-a',
  points: [],
  sampled: false,
  originalCount: 0,
  maxPoints: 1200,
};

const compareA: HistoryCompareTask = {
  id: 'compare-a',
  name: '对比 A',
  startedAt: '2026-09-01T00:00:00Z',
  durationSec: 60,
  totalBots: 10,
  finalSnapshot: { totalActions: 0, actions: [] },
};

beforeEach(() => {
  mocks.compareHistory.mockReturnValue(pending());
  mocks.getHistory.mockReturnValue(pending());
  mocks.getHistoryTimeseries.mockReturnValue(pending());
  mocks.getHistoryConfig.mockReturnValue(pending());
});

afterEach(() => {
  vi.clearAllMocks();
});

describe('history request lifecycle', () => {
  it('aborts detail requests when the detail view unmounts', async () => {
    const view = render(
      <AntApp>
        <HistoryDetailView id="history-1" stageIndex={2} onChange={vi.fn()} />
      </AntApp>,
    );

    await waitFor(() => expect(mocks.getHistory).toHaveBeenCalledTimes(1));
    const signals = [
      mocks.getHistory.mock.calls[0][2]?.signal,
      mocks.getHistoryTimeseries.mock.calls[0][3]?.signal,
      mocks.getHistoryConfig.mock.calls[0][1]?.signal,
    ] as Array<AbortSignal | undefined>;
    expect(signals.every((signal) => signal?.aborted === false)).toBe(true);

    view.unmount();

    expect(signals.every((signal) => signal?.aborted === true)).toBe(true);
  });

  it('aborts compare requests when the compare view unmounts', async () => {
    const view = render(<HistoryCompareView targets={['first', 'second']} />);

    await waitFor(() => expect(mocks.compareHistory).toHaveBeenCalledTimes(1));
    const signal = mocks.compareHistory.mock.calls[0][1]?.signal as AbortSignal | undefined;
    expect(signal?.aborted).toBe(false);

    view.unmount();

    expect(signal?.aborted).toBe(true);
  });

  it('does not render the previous detail when the replacement request fails', async () => {
    mocks.getHistory.mockResolvedValueOnce(detailA).mockRejectedValueOnce(new Error('load B'));
    mocks.getHistoryTimeseries.mockResolvedValueOnce(timeseriesA).mockReturnValueOnce(pending());
    const view = render(
      <AntApp>
        <HistoryDetailView id="history-a" onChange={vi.fn()} />
      </AntApp>,
    );
    await screen.findByText('历史 A');

    view.rerender(
      <AntApp>
        <HistoryDetailView id="history-b" onChange={vi.fn()} />
      </AntApp>,
    );

    await waitFor(() => expect(mocks.showApiError).toHaveBeenCalledTimes(1));
    expect(await screen.findByText('加载失败')).toBeTruthy();
    expect(screen.queryByText('历史 A')).toBeNull();
  });

  it('does not render the previous comparison when the replacement request fails', async () => {
    mocks.compareHistory
      .mockResolvedValueOnce({ tasks: [compareA], diff: { actions: {} } })
      .mockRejectedValueOnce(new Error('load B'));
    const view = render(<HistoryCompareView targets={['first', 'second']} />);
    expect((await screen.findAllByText('对比 A')).length).toBeGreaterThan(0);

    view.rerender(<HistoryCompareView targets={['third', 'fourth']} />);

    await waitFor(() => expect(mocks.showApiError).toHaveBeenCalledTimes(1));
    expect(await screen.findByText('加载失败')).toBeTruthy();
    expect(screen.queryAllByText('对比 A')).toHaveLength(0);
  });
});
