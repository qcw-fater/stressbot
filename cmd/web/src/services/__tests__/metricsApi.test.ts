import { describe, expect, it } from 'vitest';
import { parseClusterSystemSnapshot, parseStressAggregate } from '../metricsApi';

describe('metrics API contracts', () => {
  it('rejects a stress aggregate that omits required coverage fields', () => {
    expect(() =>
      parseStressAggregate({
        snapshot: {},
        reportingAgents: 1,
        totalAgents: 1,
        offlineAgents: 0,
        assignedAgents: 1,
        staleAgents: 0,
        coverageRatio: 1,
        asOf: '2026-08-04T03:00:00Z',
      }),
    ).toThrow(/freshAgents/);
  });

  it('accepts explicit null resource values but rejects missing or non-finite values', () => {
    const valid = {
      timestamp: '2026-08-04T03:00:00Z',
      agentCount: 1,
      onlineCount: 1,
      unhealthyCount: 0,
      offlineCount: 0,
      reportingAgents: 1,
      staleAgents: 0,
      missingAgents: 0,
      coverageRatio: 1,
      hostCpuReportingAgents: 0,
      avgHostCpuPercent: null,
      maxHostCpuPercent: null,
      hostMemoryReportingAgents: 0,
      avgHostMemPercent: null,
      maxHostMemPercent: null,
      totalHostMemBytes: null,
      usedHostMemBytes: null,
      hostNetSendReportingAgents: 0,
      hostNetRecvReportingAgents: 0,
      totalHostNetSendBytesPerSec: null,
      totalHostNetRecvBytesPerSec: null,
      processCpuReportingAgents: 0,
      avgProcessCpuPercent: null,
      maxProcessCpuPercent: null,
      processRssReportingAgents: 0,
      totalProcessRssBytes: null,
      maxProcessRssBytes: null,
      totalProcessHeapBytes: null,
      totalProcessGoroutines: null,
      processThreadsReportingAgents: 0,
      totalProcessThreads: null,
      processFdsReportingAgents: 0,
      totalProcessFds: null,
      maxProcessFds: null,
      agents: [],
    };

    expect(parseClusterSystemSnapshot(valid)).toMatchObject(valid);
    expect(() => parseClusterSystemSnapshot({ ...valid, avgHostCpuPercent: Number.NaN })).toThrow(
      /avgHostCpuPercent/,
    );
    const { reportingAgents: _, ...missing } = valid;
    expect(() => parseClusterSystemSnapshot(missing)).toThrow(/reportingAgents/);
  });
});
