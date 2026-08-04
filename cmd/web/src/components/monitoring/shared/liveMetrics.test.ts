import { describe, expect, it } from 'vitest';
import type { AgentBrief, ClusterSystemSnapshot, StressSnapshot } from '@/types/api';
import { buildLivePanelModel } from './liveMetrics';

describe('buildLivePanelModel', () => {
  it('uses the backend reporting window for live rates, quality, and latency', () => {
    const latestStress = {
      timestamp: '2026-08-03T00:00:00Z',
      collectionEpoch: 1,
      uptimeSeconds: 10,
      totalActions: 200,
	  apdexT: 100,
      timingDetail: 'codec',
      summary: {
        sampleCount: 200,
        successCount: 101,
        failureCount: 99,
        timeoutCount: 0,
        canceledCount: 3,
        executing: 4,
        successRate: 0.505,
        rttApdex: 0.505,
        rttApdexSampleCount: 200,
        rtt: {
          count: 101,
          minMs: 1,
          maxMs: 100,
          avgMs: 12,
          p50Ms: 10,
          p90Ms: 20,
          p95Ms: 34,
          p99Ms: 56,
        },
        listenWait: {
          count: 0,
          minMs: null,
          maxMs: null,
          avgMs: null,
          p50Ms: null,
          p90Ms: null,
          p95Ms: null,
          p99Ms: null,
        },
        totalDuration: {
          count: 200,
          minMs: 1,
          maxMs: 500,
          avgMs: 90,
          p50Ms: 80,
          p90Ms: 100,
          p95Ms: 123,
          p99Ms: 456,
        },
        nonRTTAvgMs: 7,
        clientCostCount: 200,
        buildAvgMs: 0,
        encodeAvgMs: 2,
        sendAvgMs: 1,
        decodeWaitAvgMs: 0,
        decodeAvgMs: 3,
        dispatchToActionWaitAvgMs: 0,
        parseStoreAvgMs: 0,
		buildSampleCount: 0,
		encodeSampleCount: 200,
		sendSampleCount: 200,
		decodeWaitSampleCount: 0,
		decodeSampleCount: 200,
		dispatchWaitSampleCount: 0,
		parseStoreSampleCount: 0,
        avgQps: 88,
      },
	  window: {
		startedAt: '2026-08-02T23:59:55Z',
		endedAt: '2026-08-03T00:00:00Z',
		durationSeconds: 5,
		expectedIntervalSeconds: 5,
		summary: {
			sampleCount: 100,
			successCount: 90,
			failureCount: 8,
			timeoutCount: 2,
			canceledCount: 1,
			executing: 3,
			successRate: 0.9,
			rttApdex: 0.85,
			rttApdexSampleCount: 100,
			rtt: { count: 100, minMs: 1, maxMs: 40, avgMs: 8, p50Ms: 5, p90Ms: 15, p95Ms: 25, p99Ms: 40 },
			listenWait: { count: 0, minMs: null, maxMs: null, avgMs: null, p50Ms: null, p90Ms: null, p95Ms: null, p99Ms: null },
			totalDuration: { count: 100, minMs: 2, maxMs: 60, avgMs: 12, p50Ms: 8, p90Ms: 25, p95Ms: 35, p99Ms: 60 },
			nonRTTAvgMs: 4,
			clientCostCount: 100,
			buildAvgMs: 0,
			encodeAvgMs: 2,
			sendAvgMs: 1,
			decodeWaitAvgMs: 0,
			decodeAvgMs: 3,
			dispatchToActionWaitAvgMs: 0,
			parseStoreAvgMs: 0,
			buildSampleCount: 0,
			encodeSampleCount: 100,
			sendSampleCount: 100,
			decodeWaitSampleCount: 0,
			decodeSampleCount: 100,
			dispatchWaitSampleCount: 0,
			parseStoreSampleCount: 0,
			avgQps: 20,
		},
		bandwidth: { sendBytes: 1024, recvBytes: 2048, sendMBps: 0.0001953125, recvMBps: 0.000390625 },
		actions: [],
		invalidMetricSamples: 0,
	  },
      robots: { started: 1, running: 1, stopped: 0, errored: 0 },
      rampUp: { currentStage: 0, totalStages: 0 },
      connections: { established: 1, active: 1, closed: 0, failed: 0, dropped: 0 },
      bandwidth: { totalSendBytes: 0, totalRecvBytes: 0, sendMBps: 0, recvMBps: 0 },
	  invalidMetricSamples: 0,
      actions: [],
    } as StressSnapshot;

    const model = buildLivePanelModel({
      latestStress,
      latestSystem: null,
      agents: [],
      reportingAgents: 0,
      totalAgents: 0,
      offlineAgents: 0,
      assignedAgents: 0,
    });

	expect(model.throughput).toMatchObject({ intervalQps: 20, lifetimeQps: 88 });
    expect(model.quality).toMatchObject({ sampleCount: 100, successRate: 0.9, rttApdex: 0.85 });
    expect(model.latency).toMatchObject({ rttP99Ms: 40, totalDurationP95Ms: 35, nonRTTAvgMs: 4 });
    expect(model.timingDetail).toBe('codec');
  });

  it('keeps stress coverage separate from fresh resource coverage and never exposes undefined', () => {
    const latestSystem = {
      timestamp: '2026-08-04T03:00:00Z',
      agentCount: 2,
      onlineCount: 1,
      unhealthyCount: 0,
      offlineCount: 1,
      reportingAgents: 1,
      staleAgents: 0,
      missingAgents: 1,
      coverageRatio: 0.5,
      hostCpuReportingAgents: 1,
      avgHostCpuPercent: 25,
      maxHostCpuPercent: 40,
      hotHostCpuAgentName: 'node-a',
      hostMemoryReportingAgents: 1,
      avgHostMemPercent: 50,
      maxHostMemPercent: 60,
      hotHostMemAgentName: 'node-a',
      totalHostMemBytes: 1_000,
      usedHostMemBytes: 500,
      hostNetSendReportingAgents: 1,
      hostNetRecvReportingAgents: 1,
      totalHostNetSendBytesPerSec: 2_048,
      totalHostNetRecvBytesPerSec: 4_096,
      processCpuReportingAgents: 1,
      avgProcessCpuPercent: 12.5,
      maxProcessCpuPercent: 12.5,
      hotProcessCpuAgentName: 'node-a',
      processRssReportingAgents: 1,
      totalProcessRssBytes: 512,
      maxProcessRssBytes: 512,
      hotProcessRssAgentName: 'node-a',
      totalProcessHeapBytes: 256,
      totalProcessGoroutines: 10,
      processThreadsReportingAgents: 1,
      totalProcessThreads: 4,
      processFdsReportingAgents: 1,
      totalProcessFds: 9,
      maxProcessFds: 9,
      hotProcessFdsAgentName: 'node-a',
      agents: [],
    } as unknown as ClusterSystemSnapshot;

    const model = buildLivePanelModel({
      latestStress: null,
      latestSystem,
      agents: [],
      reportingAgents: undefined as unknown as number,
      totalAgents: undefined as unknown as number,
      offlineAgents: undefined as unknown as number,
      assignedAgents: 2,
    });

    expect(model.nodes).toMatchObject({
      stressReporting: 0,
      assigned: 2,
      resourceReporting: 1,
      resourceScope: 2,
      resourceStale: 0,
      resourceMissing: 1,
      unhealthy: 0,
      offline: 1,
    });
    expect(model.resources).toMatchObject({
      avgHostCpuPercent: 25,
      maxHostCpuPercent: 40,
      hotHostCpuNode: 'node-a',
      avgHostMemPercent: 50,
      maxHostMemPercent: 60,
      hotHostMemNode: 'node-a',
      hostSendBytesPerSec: 2_048,
      hostRecvBytesPerSec: 4_096,
      avgProcessCpuPercent: 12.5,
      totalProcessRssBytes: 512,
      maxProcessFds: 9,
    });
  });

  it('preserves unavailable resource values as null instead of displaying zero', () => {
    const latestSystem = {
      timestamp: '2026-08-04T03:00:00Z',
      agentCount: 1,
      onlineCount: 1,
      unhealthyCount: 0,
      offlineCount: 0,
      reportingAgents: 1,
      staleAgents: 0,
      missingAgents: 0,
      coverageRatio: 1,
      avgHostCpuPercent: null,
      maxHostCpuPercent: null,
      avgHostMemPercent: null,
      maxHostMemPercent: null,
      totalHostNetSendBytesPerSec: null,
      totalHostNetRecvBytesPerSec: null,
      avgProcessCpuPercent: null,
      totalProcessRssBytes: null,
      maxProcessFds: null,
      agents: [],
    } as unknown as ClusterSystemSnapshot;

    const model = buildLivePanelModel({
      latestStress: null,
      latestSystem,
      agents: [],
      reportingAgents: 0,
      totalAgents: 1,
      offlineAgents: 0,
      assignedAgents: 1,
    });

    expect(model.resources.avgHostCpuPercent).toBeNull();
    expect(model.resources.avgHostMemPercent).toBeNull();
    expect(model.resources.hostSendBytesPerSec).toBeNull();
    expect(model.resources.avgProcessCpuPercent).toBeNull();
    expect(model.resources.totalProcessRssBytes).toBeNull();
    expect(model.resources.maxProcessFds).toBeNull();
  });

  it('uses the backend resource freshness state for node tone', () => {
    const baseAgent: AgentBrief = {
      agentId: 'node-a',
      name: 'node-a',
      address: '127.0.0.1:7719',
      appVersion: 'dev',
      maxBots: 100,
      status: 'idle' as const,
      currentBots: 0,
      staticInfo: {
        hostname: 'node-a',
        os: 'windows',
        arch: 'amd64',
        numCpu: 8,
        memTotalBytes: 1024,
        goVersion: 'go1.24',
        kernelVer: '',
        startedAt: '2026-08-04T00:00:00Z',
      },
      lastHeartbeatAt: '2026-08-04T03:00:00Z',
      stressUpdatedAt: '2020-01-01T00:00:00Z',
    };

    const fresh = buildLivePanelModel({
      latestStress: null,
      latestSystem: null,
      agents: [{ ...baseAgent, systemStale: false }],
      reportingAgents: 0,
      totalAgents: 1,
      offlineAgents: 0,
      assignedAgents: 0,
    });
    const stale = buildLivePanelModel({
      latestStress: null,
      latestSystem: null,
      agents: [{ ...baseAgent, systemStale: true }],
      reportingAgents: 0,
      totalAgents: 1,
      offlineAgents: 0,
      assignedAgents: 0,
    });

    expect(fresh.nodes.items[0].tone).toBe('idle');
    expect(stale.nodes.items[0].tone).toBe('stale');
  });
});
