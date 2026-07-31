import type {
  AgentBrief,
  ClusterSystemSnapshot,
  RampUpSnapshot,
  StressSnapshot,
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
    sendKBps: number | null;
    recvKBps: number | null;
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
    clientAvgMs: number | null;
  };
  connections: {
    active: number;
    established: number;
    failed: number;
    dropped: number;
  };
  resources: {
    avgCpuPercent: number | null;
    maxCpuPercent: number | null;
    avgMemPercent: number | null;
    maxMemPercent: number | null;
    hotCpuNode?: string;
    hotMemNode?: string;
    goroutines: number;
    threads: number;
    fds: number;
  };
  nodes: {
    reporting: number;
    assigned: number;
    online: number;
    total: number;
    offline: number;
    capacityCurrent: number;
    capacityMax: number;
    items: LiveNodeItem[];
  };
}

export interface BuildLivePanelModelInput {
  latestStress: StressSnapshot | null;
  latestSystem: ClusterSystemSnapshot | null;
  stressHistory: StressSnapshot[];
  agents: AgentBrief[];
  reportingAgents: number;
  totalAgents: number;
  offlineAgents: number;
  assignedAgents: number;
}

export function buildLivePanelModel(input: BuildLivePanelModelInput): LivePanelModel {
  const stress = input.latestStress;
  const actions = stress?.actions ?? [];
  const robots = stress?.robots ?? { started: 0, running: 0, stopped: 0, errored: 0 };
  const connections = stress?.connections ?? { established: 0, failed: 0, dropped: 0 };
  const quality = deriveQuality(actions);

  return {
    load: {
      runningRobots: robots.running,
      startedRobots: robots.started,
      stoppedRobots: robots.stopped,
      erroredRobots: robots.errored,
      robotPercent: robots.started > 0 ? clamp((robots.running / robots.started) * 100, 0, 100) : 0,
      rampUp: deriveRampUp(stress?.rampUp),
    },
    throughput: {
      intervalQps: deriveIntervalQps(input.stressHistory),
      lifetimeQps: stress && stress.uptimeSeconds > 0 ? stress.totalActions / stress.uptimeSeconds : null,
      totalActions: stress?.totalActions ?? 0,
      sendKBps: stress?.bandwidth ? finiteOrNull(stress.bandwidth.sendMBps * 1024) : null,
      recvKBps: stress?.bandwidth ? finiteOrNull(stress.bandwidth.recvMBps * 1024) : null,
    },
    quality,
    latency: deriveLatency(actions),
    connections: {
      active: Math.max(0, connections.established - connections.dropped),
      established: connections.established,
      failed: connections.failed,
      dropped: connections.dropped,
    },
    resources: deriveResources(input.latestSystem),
    nodes: deriveNodes(input),
  };
}

export function deriveIntervalQps(history: StressSnapshot[]): number | null {
  if (history.length < 2) return null;
  const prev = history[history.length - 2];
  const next = history[history.length - 1];
  const deltaActions = next.totalActions - prev.totalActions;
  const deltaMs = Date.parse(next.timestamp) - Date.parse(prev.timestamp);
  if (!Number.isFinite(deltaActions) || !Number.isFinite(deltaMs) || deltaActions < 0 || deltaMs <= 0) return null;
  return deltaActions / (deltaMs / 1000);
}

function deriveQuality(actions: StressSnapshot['actions']): LivePanelModel['quality'] {
  let sampleCount = 0;
  let successCount = 0;
  let failureCount = 0;
  let timeoutCount = 0;
  let canceledCount = 0;
  let executing = 0;
  let rttSamples = 0;
  let rttApdex = 0;

  for (const action of actions) {
    const samples = safe(action.sampleCount);
    sampleCount += samples;
    successCount += safe(action.successCount);
    failureCount += safe(action.failureCount);
    timeoutCount += safe(action.timeoutCount);
    canceledCount += safe(action.canceledCount);
    executing += safe(action.executing);

    // 只有往返类贡献 Apdex：其余类别没有可比的统一阈值，掺进来会让总分
    // 随「动作构成」漂移，而不是随服务端表现变化。
    const rttCount = safe(action.rttSampleCount);
    rttSamples += rttCount;
    rttApdex += safe(action.rttApdex) * rttCount;
  }

  return {
    sampleCount,
    successCount,
    failureCount,
    timeoutCount,
    canceledCount,
    executing,
    successRate: sampleCount > 0 ? successCount / sampleCount : null,
    rttApdex: rttSamples > 0 ? rttApdex / rttSamples : null,
  };
}

function deriveLatency(actions: StressSnapshot['actions']): LivePanelModel['latency'] {
  let rttSamples = 0;
  let rttAvg = 0;
  let rttP95 = 0;
  let rttP99 = 0;
  let totalDurationSamples = 0;
  let totalDurationAvg = 0;
  let totalDurationP95 = 0;
  let totalDurationP99 = 0;
  let clientSamples = 0;
  let clientAvg = 0;

  for (const action of actions) {
    const rttCount = safe(action.rttSampleCount);
    rttSamples += rttCount;
    rttAvg += safe(action.rtt?.avgMs) * rttCount;
    rttP95 += safe(action.rtt?.p95Ms) * rttCount;
    rttP99 += safe(action.rtt?.p99Ms) * rttCount;

    const totalCount = safe(action.totalDurationSampleCount);
    totalDurationSamples += totalCount;
    totalDurationAvg += safe(action.totalDuration?.avgMs) * totalCount;
    totalDurationP95 += safe(action.totalDuration?.p95Ms) * totalCount;
    totalDurationP99 += safe(action.totalDuration?.p99Ms) * totalCount;

    const samples = safe(action.sampleCount);
    clientSamples += samples;
    clientAvg += safe(action.clientAvgMs) * samples;
  }

  return {
    rttAvgMs: rttSamples > 0 ? rttAvg / rttSamples : null,
    rttP95Ms: rttSamples > 0 ? rttP95 / rttSamples : null,
    rttP99Ms: rttSamples > 0 ? rttP99 / rttSamples : null,
    totalDurationAvgMs: totalDurationSamples > 0 ? totalDurationAvg / totalDurationSamples : null,
    totalDurationP95Ms: totalDurationSamples > 0 ? totalDurationP95 / totalDurationSamples : null,
    totalDurationP99Ms: totalDurationSamples > 0 ? totalDurationP99 / totalDurationSamples : null,
    clientAvgMs: clientSamples > 0 ? clientAvg / clientSamples : null,
  };
}

function deriveNodes(input: BuildLivePanelModelInput): LivePanelModel['nodes'] {
  const online = input.agents.filter((a) => a.status !== 'offline').length;
  const capacityCurrent = input.agents.reduce((sum, a) => sum + safe(a.currentBots), 0);
  const capacityMax = input.agents.reduce((sum, a) => sum + safe(a.maxBots), 0);

  const items = input.agents.map<LiveNodeItem>((agent) => {
    const stale = isStale(agent.stressUpdatedAt) || isStale(agent.systemUpdatedAt);
    return {
      id: agent.agentId,
      name: agent.name || agent.agentId,
      status: agent.status,
      currentBots: safe(agent.currentBots),
      maxBots: safe(agent.maxBots),
      cpuPercent: finiteOrNull(agent.cpuPercent),
      memPercent: finiteOrNull(agent.memPercent),
      updatedAt: agent.systemUpdatedAt ?? agent.stressUpdatedAt ?? agent.lastHeartbeatAt,
      tone: stale && agent.status !== 'offline' ? 'stale' : normalizeNodeTone(agent.status),
    };
  });

  return {
    reporting: input.reportingAgents,
    assigned: input.assignedAgents,
    online,
    total: input.agents.length || input.totalAgents,
    offline: input.offlineAgents || input.agents.filter((a) => a.status === 'offline').length,
    capacityCurrent,
    capacityMax,
    items,
  };
}

function deriveResources(system: ClusterSystemSnapshot | null): LivePanelModel['resources'] {
  return {
    avgCpuPercent: finiteOrNull(system?.avgCpuPercent),
    maxCpuPercent: finiteOrNull(system?.maxCpuPercent),
    avgMemPercent: finiteOrNull(system?.avgMemPercent),
    maxMemPercent: finiteOrNull(system?.maxMemPercent),
    hotCpuNode: system?.hotAgentName,
    hotMemNode: system?.hotMemAgentName,
    goroutines: safe(system?.totalGoroutines),
    threads: safe(system?.totalThreads),
    fds: safe(system?.totalFds),
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

function isStale(value?: string): boolean {
  if (!value) return false;
  const ts = Date.parse(value);
  if (!Number.isFinite(ts)) return false;
  return Date.now() - ts > 30_000;
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
