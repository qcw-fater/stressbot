/**
 * 日志查看 API 封装
 */

import { buildQuery, getJson } from './api';
import type { LogQueryResult, LogFileInfo } from '@/types/api';

export interface LogQueryParams {
  level?: string;
  search?: string;
  afterSeq?: number;
  limit?: number;
}

export function getAdminLogs(
  params: LogQueryParams = {},
  signal?: AbortSignal,
): Promise<LogQueryResult> {
  return getJson<LogQueryResult>('/logs/admin' + buildQuery(params as Record<string, unknown>), {
    signal,
  });
}

export function getAgentLogs(
  agentId: string,
  params: LogQueryParams = {},
  signal?: AbortSignal,
): Promise<LogQueryResult> {
  return getJson<LogQueryResult>(
    `/logs/agents/${encodeURIComponent(agentId)}${buildQuery(params as Record<string, unknown>)}`,
    { signal },
  );
}

export function getAdminLogFiles(): Promise<LogFileInfo[]> {
  return getJson<LogFileInfo[]>('/logs/admin/files');
}

export function getAgentLogFiles(agentId: string): Promise<LogFileInfo[]> {
  return getJson<LogFileInfo[]>(`/logs/agents/${encodeURIComponent(agentId)}/files`);
}
