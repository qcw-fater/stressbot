/**
 * 压测指标 + 系统指标 API 封装（对应 docs/api-monitor.md §7 / §8）。
 *
 * 注意：metrics 接口默认返回当前 active 任务的快照，无需指定 taskId；
 *      taskId 只在历史回放或多任务并存场景才传，本项目暂不需要。
 */

import { buildQuery, getJson } from './api';
import type {
  ClusterSystemSnapshot,
  PerAgentMetrics,
  StressAggregate,
} from '@/types/api';

export interface MetricsParams {
  taskId?: string;
}

// === 压测指标 ===

/**
 * 后端始终返回完整聚合结果；实时指标只存在于 snapshot.window。
 */
export async function getClusterMetrics(params: MetricsParams = {}): Promise<StressAggregate> {
  const value = await getJson<unknown>('/metrics' + buildQuery(params as Record<string, unknown>));
  return parseStressAggregate(value);
}

export function parseStressAggregate(value: unknown): StressAggregate {
  const record = requireRecord(value, 'metrics');
  requireRecord(record.snapshot, 'snapshot');
  for (const field of [
    'reportingAgents',
    'totalAgents',
    'offlineAgents',
    'assignedAgents',
    'freshAgents',
    'staleAgents',
  ]) {
    requireCount(record, field);
  }
  requireRatio(record, 'coverageRatio');
  requireString(record, 'asOf');
  return record as unknown as StressAggregate;
}

/**
 * 后端 per-node metrics API 返回 `[]{agentId, snapshot, updatedAt}`，
 * 前端包装为 `{items}` 并将 agentName 兜底为 agentId。
 */
export async function getPerAgentMetrics(params: MetricsParams = {}): Promise<PerAgentMetrics> {
  return getJson<PerAgentMetrics>(
    '/metrics/agents' + buildQuery(params as Record<string, unknown>),
  );
}

// === 系统指标 ===
export async function getClusterSystem(): Promise<ClusterSystemSnapshot> {
  return parseClusterSystemSnapshot(await getJson<unknown>('/system'));
}

const SYSTEM_COUNT_FIELDS = [
  'agentCount',
  'onlineCount',
  'unhealthyCount',
  'offlineCount',
  'reportingAgents',
  'staleAgents',
  'missingAgents',
  'hostCpuReportingAgents',
  'hostMemoryReportingAgents',
  'hostNetSendReportingAgents',
  'hostNetRecvReportingAgents',
  'processCpuReportingAgents',
  'processRssReportingAgents',
  'processThreadsReportingAgents',
  'processFdsReportingAgents',
] as const;

const SYSTEM_NULLABLE_NUMBER_FIELDS = [
  'avgHostCpuPercent',
  'maxHostCpuPercent',
  'avgHostMemPercent',
  'maxHostMemPercent',
  'totalHostMemBytes',
  'usedHostMemBytes',
  'totalHostNetSendBytesPerSec',
  'totalHostNetRecvBytesPerSec',
  'avgProcessCpuPercent',
  'maxProcessCpuPercent',
  'totalProcessRssBytes',
  'maxProcessRssBytes',
  'totalProcessHeapBytes',
  'totalProcessGoroutines',
  'totalProcessThreads',
  'totalProcessFds',
  'maxProcessFds',
] as const;

export function parseClusterSystemSnapshot(value: unknown): ClusterSystemSnapshot {
  const record = requireRecord(value, 'system');
  requireString(record, 'timestamp');
  SYSTEM_COUNT_FIELDS.forEach((field) => requireCount(record, field));
  requireRatio(record, 'coverageRatio');
  SYSTEM_NULLABLE_NUMBER_FIELDS.forEach((field) => requireNullableNumber(record, field));
  if (!Array.isArray(record.agents)) {
    throw new Error('系统资源响应字段 agents 必须是数组');
  }
  return record as unknown as ClusterSystemSnapshot;
}

function requireRecord(value: unknown, field: string): Record<string, unknown> {
  if (value == null || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(`监控响应字段 ${field} 必须是对象`);
  }
  return value as Record<string, unknown>;
}

function requireString(record: Record<string, unknown>, field: string): void {
  if (typeof record[field] !== 'string' || record[field] === '') {
    throw new Error(`监控响应字段 ${field} 缺失或类型错误`);
  }
}

function requireCount(record: Record<string, unknown>, field: string): void {
  const value = record[field];
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value) || value < 0) {
    throw new Error(`监控响应字段 ${field} 缺失或不是非负整数`);
  }
}

function requireRatio(record: Record<string, unknown>, field: string): void {
  const value = record[field];
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0 || value > 1) {
    throw new Error(`监控响应字段 ${field} 缺失或不在 0 到 1 之间`);
  }
}

function requireNullableNumber(record: Record<string, unknown>, field: string): void {
  const value = record[field];
  if (value !== null && (typeof value !== 'number' || !Number.isFinite(value) || value < 0)) {
    throw new Error(`监控响应字段 ${field} 必须是非负有限数或 null`);
  }
}
