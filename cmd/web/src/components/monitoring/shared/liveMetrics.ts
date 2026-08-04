import type {
  AgentBrief,
  ClusterSystemSnapshot,
  RampUpSnapshot,
  StressSnapshot,
  TimingDetailLevel,
} from '@/types/api';

export type NodeTone = 'idle' | 'busy' | 'unhealthy' | 'offline' | 'stale';

export interface LiveNodeItem {
  id: string;
  name: string;
  status: string;
  currentBots: number;
  maxBots: number;
  cpuPercent: number | null;
  memPercent: number | null;
  updatedAt?: string;
  tone: NodeTone;
}

export interface LivePanelModel {
  timingDetail: TimingDetailLevel;
  load: {
    runningRobots: number;
    startedRobots: number;
    stoppedRobots: number;
    erroredRobots: number;
    robotPercent: number;
    rampUp: { currentStage: number; totalStages: number } | null;
  };
  throughput: {
    intervalQps: number | null;
    lifetimeQps: number | null;
    totalActions: number;
    actionSendBytesPerSec: number | null;
    actionRecvBytesPerSec: number | null;
  };
  quality: {
    sampleCount: number;
    successCount: number;
    failureCount: number;
    timeoutCount: number;
    canceledCount: number;
    executing: number;
    successRate: number | null;
    /** RTT Apdex（唯一评分指标，只由往返类贡献） */
    rttApdex: number | null;
  };
  latency: {
    rttAvgMs: number | null;
    rttP95Ms: number | null;
    rttP99Ms: number | null;
    totalDurationAvgMs: number | null;
    totalDurationP95Ms: number | null;
    totalDurationP99Ms: number | null;
    nonRTTAvgMs: number | null;
  };
  connections: {
    active: number;
    established: number;
    closed: number;
    failed: number;
    dropped: number;
  };
  resources: {
    avgHostCpuPercent: number | null;
    maxHostCpuPercent: number | null;
    avgHostMemPercent: number | null;
    maxHostMemPercent: number | null;
    hotHostCpuNode?: string;
    hotHostMemNode?: string;
    hostSendBytesPerSec: number | null;
    hostRecvBytesPerSec: number | null;
    hostSendReportingAgents: number;
    hostRecvReportingAgents: number;
    avgProcessCpuPercent: number | null;
    maxProcessCpuPercent: number | null;
    processCpuReportingAgents: number;
    totalProcessRssBytes: number | null;
    maxProcessRssBytes: number | null;
    processRssReportingAgents: number;
    maxProcessFds: number | null;
    processFdsReportingAgents: number;
    hotProcessCpuNode?: string;
    hotProcessRssNode?: string;
    hotProcessFdsNode?: string;
    totalProcessGoroutines: number | null;
    totalProcessThreads: number | null;
    totalProcessFds: number | null;
  };
  nodes: {
    stressReporting: number;
    assigned: number;
    resourceReporting: number;
    resourceScope: number;
    resourceStale: number;
    resourceMissing: number;
    online: number;
    total: number;
    unhealthy: number;
    offline: number;
    capacityCurrent: number;
    capacityMax: number;
    items: LiveNodeItem[];
  };
}

export interface BuildLivePanelModelInput {
  latestStress: StressSnapshot | null;
  latestSystem: ClusterSystemSnapshot | null;
  agents: AgentBrief[];
  reportingAgents: number;
  totalAgents: number;
  offlineAgents: number;
  assignedAgents: number;
}

export function buildLivePanelModel(input: BuildLivePanelModelInput): LivePanelModel {
  const stress = input.latestStress;
  const robots = stress?.robots ?? { started: 0, running: 0, stopped: 0, errored: 0 };
  const connections = stress?.connections ?? { established: 0, active: 0, closed: 0, failed: 0, dropped: 0 };
  const liveSummary = stress?.window?.summary;
  const quality = deriveQuality(liveSummary);

  return {
    timingDetail: stress?.timingDetail ?? 'rtt',
    load: {
      runningRobots: robots.running,
      startedRobots: robots.started,
      stoppedRobots: robots.stopped,
      erroredRobots: robots.errored,
      robotPercent: robots.started > 0 ? clamp((robots.running / robots.started) * 100, 0, 100) : 0,
      rampUp: deriveRampUp(stress?.rampUp),
    },
    throughput: {
      intervalQps: liveSummary ? finiteOrNull(liveSummary.avgQps) : null,
      lifetimeQps: stress ? finiteOrNull(stress.summary.avgQps) : null,
      totalActions: stress?.totalActions ?? 0,
      actionSendBytesPerSec: stress?.window
        ? finiteOrNull(stress.window.bandwidth.sendMBps * 1024 * 1024)
        : null,
      actionRecvBytesPerSec: stress?.window
        ? finiteOrNull(stress.window.bandwidth.recvMBps * 1024 * 1024)
        : null,
    },
    quality,
    latency: deriveLatency(liveSummary),
    connections: {
      active: connections.active,
      established: connections.established,
      closed: connections.closed,
      failed: connections.failed,
      dropped: connections.dropped,
    },
    resources: deriveResources(input.latestSystem),
    nodes: deriveNodes(input),
  };
}

function deriveQuality(summary: StressSnapshot['summary'] | undefined): LivePanelModel['quality'] {
  return {
    sampleCount: safe(summary?.sampleCount),
    successCount: safe(summary?.successCount),
    failureCount: safe(summary?.failureCount),
    timeoutCount: safe(summary?.timeoutCount),
    canceledCount: safe(summary?.canceledCount),
    executing: safe(summary?.executing),
    successRate: safe(summary?.sampleCount) > 0 ? safe(summary?.successRate) : null,
    rttApdex: safe(summary?.rttApdexSampleCount) > 0 ? safe(summary?.rttApdex) : null,
  };
}

function deriveLatency(summary: StressSnapshot['summary'] | undefined): LivePanelModel['latency'] {
  const hasRTT = safe(summary?.rtt?.count) > 0;
  const hasTotal = safe(summary?.totalDuration?.count) > 0;
  return {
    rttAvgMs: hasRTT ? finiteOrNull(summary?.rtt.avgMs) : null,
    rttP95Ms: hasRTT ? finiteOrNull(summary?.rtt.p95Ms) : null,
    rttP99Ms: hasRTT ? finiteOrNull(summary?.rtt.p99Ms) : null,
    totalDurationAvgMs: hasTotal ? finiteOrNull(summary?.totalDuration.avgMs) : null,
    totalDurationP95Ms: hasTotal ? finiteOrNull(summary?.totalDuration.p95Ms) : null,
    totalDurationP99Ms: hasTotal ? finiteOrNull(summary?.totalDuration.p99Ms) : null,
    nonRTTAvgMs: safe(summary?.clientCostCount) > 0 ? finiteOrNull(summary?.nonRTTAvgMs) : null,
  };
}

function deriveNodes(input: BuildLivePanelModelInput): LivePanelModel['nodes'] {
  const system = input.latestSystem;
  const online = system
    ? safe(system.onlineCount)
    : input.agents.filter((a) => a.status === 'idle' || a.status === 'busy').length;
  const capacityCurrent = input.agents.reduce((sum, a) => sum + safe(a.currentBots), 0);
  const capacityMax = input.agents.reduce((sum, a) => sum + safe(a.maxBots), 0);

  const items = input.agents.map<LiveNodeItem>((agent) => {
    const stale = agent.systemStale === true;
    return {
      id: agent.agentId,
      name: agent.name || agent.agentId,
      status: agent.status,
      currentBots: safe(agent.currentBots),
      maxBots: safe(agent.maxBots),
      cpuPercent: finiteOrNull(agent.hostCpuPercent),
      memPercent: finiteOrNull(agent.hostMemPercent),
      updatedAt: agent.systemUpdatedAt ?? agent.stressUpdatedAt ?? agent.lastHeartbeatAt,
      tone: stale && agent.status !== 'offline' ? 'stale' : normalizeNodeTone(agent.status),
    };
  });

  return {
    stressReporting: safe(input.reportingAgents),
    assigned: safe(input.assignedAgents),
    resourceReporting: safe(system?.reportingAgents),
    resourceScope: safe(system?.agentCount),
    resourceStale: safe(system?.staleAgents),
    resourceMissing: safe(system?.missingAgents),
    online,
    total: system ? safe(system.agentCount) : input.agents.length || safe(input.totalAgents),
    unhealthy: system
      ? safe(system.unhealthyCount)
      : input.agents.filter((a) => a.status === 'unhealthy').length,
    offline: system
      ? safe(system.offlineCount)
      : safe(input.offlineAgents) || input.agents.filter((a) => a.status === 'offline').length,
    capacityCurrent,
    capacityMax,
    items,
  };
}

function deriveResources(system: ClusterSystemSnapshot | null): LivePanelModel['resources'] {
  return {
    avgHostCpuPercent: finiteOrNull(system?.avgHostCpuPercent),
    maxHostCpuPercent: finiteOrNull(system?.maxHostCpuPercent),
    avgHostMemPercent: finiteOrNull(system?.avgHostMemPercent),
    maxHostMemPercent: finiteOrNull(system?.maxHostMemPercent),
    hotHostCpuNode: system?.hotHostCpuAgentName,
    hotHostMemNode: system?.hotHostMemAgentName,
    hostSendBytesPerSec: finiteOrNull(system?.totalHostNetSendBytesPerSec),
    hostRecvBytesPerSec: finiteOrNull(system?.totalHostNetRecvBytesPerSec),
    hostSendReportingAgents: safe(system?.hostNetSendReportingAgents),
    hostRecvReportingAgents: safe(system?.hostNetRecvReportingAgents),
    avgProcessCpuPercent: finiteOrNull(system?.avgProcessCpuPercent),
    maxProcessCpuPercent: finiteOrNull(system?.maxProcessCpuPercent),
    processCpuReportingAgents: safe(system?.processCpuReportingAgents),
    totalProcessRssBytes: finiteOrNull(system?.totalProcessRssBytes),
    maxProcessRssBytes: finiteOrNull(system?.maxProcessRssBytes),
    processRssReportingAgents: safe(system?.processRssReportingAgents),
    maxProcessFds: finiteOrNull(system?.maxProcessFds),
    processFdsReportingAgents: safe(system?.processFdsReportingAgents),
    hotProcessCpuNode: system?.hotProcessCpuAgentName,
    hotProcessRssNode: system?.hotProcessRssAgentName,
    hotProcessFdsNode: system?.hotProcessFdsAgentName,
    totalProcessGoroutines: finiteOrNull(system?.totalProcessGoroutines),
    totalProcessThreads: finiteOrNull(system?.totalProcessThreads),
    totalProcessFds: finiteOrNull(system?.totalProcessFds),
  };
}

function deriveRampUp(snapshot: RampUpSnapshot | undefined): LivePanelModel['load']['rampUp'] {
  if (!snapshot || snapshot.totalStages <= 0 || snapshot.currentStage <= 0) return null;
  return {
    currentStage: snapshot.currentStage,
    totalStages: snapshot.totalStages,
  };
}

function normalizeNodeTone(status: string): NodeTone {
  if (status === 'offline') return 'offline';
  if (status === 'unhealthy') return 'unhealthy';
  if (status === 'busy') return 'busy';
  return 'idle';
}

function finiteOrNull(value: number | null | undefined): number | null {
  return value == null || !Number.isFinite(value) ? null : value;
}

function safe(value: number | null | undefined): number {
  return value == null || !Number.isFinite(value) ? 0 : value;
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}
