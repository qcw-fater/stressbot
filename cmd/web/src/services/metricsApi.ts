/**
 * 压测指标 + 系统指标 API 封装（对应 docs/api-monitor.md §7 / §8）。
 *
 * 注意：metrics 接口默认返回当前 active 任务的快照，无需指定 taskId；
 *      taskId 只在历史回放或多任务并存场景才传，本项目暂不需要。
 */

import { adaptList, buildQuery, getJson } from './api';
import type {
  ClusterSystemSnapshot,
  PerAgentMetrics,
  PerAgentMetricsItem,
  StressAggregate,
  StressSnapshot,
} from '@/types/api';

export interface MetricsParams {
  taskId?: string;
}

/** 空快照，作为 `/api/metrics` 在 active=null 时的兜底值。 */
const EMPTY_STRESS: StressSnapshot = {
  timestamp: new Date(0).toISOString(),
  uptimeSeconds: 0,
  totalActions: 0,
  apdexT: 0,
  robots: { started: 0, running: 0, stopped: 0, errored: 0 },
  connections: { established: 0, failed: 0, dropped: 0 },
  bandwidth: { totalSendBytes: 0, totalRecvBytes: 0, sendMBps: 0, recvMBps: 0 },
  actions: [],
};

const EMPTY_AGGREGATE: StressAggregate = {
  snapshot: EMPTY_STRESS,
  reportingAgents: 0,
  totalAgents: 0,
  offlineAgents: 0,
  assignedAgents: 0,
};

// === 压测指标 ===

/**
 * 后端 `/api/metrics` 返回 `{snapshot, reportingAgents, totalAgents}`：
 *   - active=null: snapshot 是空的 CollectorSnapshot
 *   - active!=null: snapshot 是该任务的 StressSnapshot
 *
 * 前端这里统一抽出完整聚合结果；缺字段时用空值兜底，避免 UI 空指针。
 */
export async function getClusterMetrics(params: MetricsParams = {}): Promise<StressAggregate> {
  const resp = await getJson<unknown>(
    '/metrics' + buildQuery(params as Record<string, unknown>),
  );
  if (!resp || typeof resp !== 'object') return EMPTY_AGGREGATE;
  const wrapper = resp as Partial<StressAggregate> & { snapshot?: StressSnapshot } & Partial<StressSnapshot>;
  // 新格式：{snapshot, reportingAgents, totalAgents}
  if (wrapper.snapshot && typeof wrapper.snapshot === 'object') {
    return {
      snapshot: mergeSnapshot(wrapper.snapshot),
      reportingAgents: wrapper.reportingAgents ?? 0,
      totalAgents: wrapper.totalAgents ?? 0,
      offlineAgents: wrapper.offlineAgents ?? 0,
      assignedAgents: wrapper.assignedAgents ?? 0,
    };
  }
  // 旧格式兼容：直接返回 StressSnapshot
  if (typeof (resp as StressSnapshot).timestamp === 'string') {
    return {
      snapshot: mergeSnapshot(resp as StressSnapshot),
      reportingAgents: 0,
      totalAgents: 0,
      offlineAgents: 0,
      assignedAgents: 0,
    };
  }
  return EMPTY_AGGREGATE;
}

function mergeSnapshot(s: Partial<StressSnapshot>): StressSnapshot {
  return {
    ...EMPTY_STRESS,
    ...s,
    robots: { ...EMPTY_STRESS.robots, ...(s.robots ?? {}) },
    connections: { ...EMPTY_STRESS.connections, ...(s.connections ?? {}) },
    bandwidth: { ...EMPTY_STRESS.bandwidth, ...(s.bandwidth ?? {}) },
    actions: Array.isArray(s.actions) ? s.actions : [],
  };
}

/**
 * 后端 `/api/metrics/agents` 返回 `[]{agentId, snapshot, updatedAt}`，
 * 前端包装为 `{items}` 并将 agentName 兜底为 agentId。
 */
export async function getPerAgentMetrics(params: MetricsParams = {}): Promise<PerAgentMetrics> {
  const resp = await getJson<unknown>(
    '/metrics/agents' + buildQuery(params as Record<string, unknown>),
  );
  const arr = adaptList<Partial<PerAgentMetricsItem> & { agentId: string }>(resp).items;
  return {
    items: arr.map((it) => ({
      agentId: it.agentId,
      agentName: it.agentName ?? it.agentId,
      snapshot: it.snapshot ?? EMPTY_STRESS,
      updatedAt: it.updatedAt ?? new Date(0).toISOString(),
    })),
  };
}

// === 系统指标 ===
export function getClusterSystem(): Promise<ClusterSystemSnapshot> {
  return getJson<ClusterSystemSnapshot>('/system');
}

