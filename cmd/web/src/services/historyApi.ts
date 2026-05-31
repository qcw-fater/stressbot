/**
 * 历史压测记录 API 封装（对应 docs/api-monitor.md §10）。
 */

import { buildQuery, del, getJson, postJson, putJson } from './api';
import type {
  HistoryCloneRequest,
  HistoryCompareResponse,
  HistoryConfigArchive,
  HistoryConfigSummary,
  HistoryDetail,
  HistoryFilter,
  HistoryListResponse,
  TimeseriesResponse,
  UpdateHistoryRequest,
} from '@/types/api';

export function listHistory(filter: HistoryFilter = {}): Promise<HistoryListResponse> {
  return getJson<HistoryListResponse>('/history' + buildQuery(filter as Record<string, unknown>));
}

export function getHistory(id: string): Promise<HistoryDetail> {
  return getJson<HistoryDetail>(`/history/${encodeURIComponent(id)}`);
}

export function updateHistory(id: string, req: UpdateHistoryRequest): Promise<HistoryDetail> {
  return putJson<HistoryDetail>(`/history/${encodeURIComponent(id)}`, req);
}

export function deleteHistory(id: string, force = false): Promise<void> {
  return del<void>(`/history/${encodeURIComponent(id)}${buildQuery({ force })}`);
}

export function getHistoryTimeseries(id: string, maxPoints?: number): Promise<TimeseriesResponse> {
  return getJson<TimeseriesResponse>(
    `/history/${encodeURIComponent(id)}/timeseries${buildQuery({ maxPoints })}`,
  );
}

export function getHistoryConfig(id: string): Promise<HistoryConfigSummary> {
  return getJson<HistoryConfigSummary>(`/history/${encodeURIComponent(id)}/config`);
}

export function getHistoryConfigArchive(id: string): Promise<HistoryConfigArchive> {
  return getJson<HistoryConfigArchive>(`/history/${encodeURIComponent(id)}/config/archive`);
}

export function cloneHistory(
  id: string,
  req: HistoryCloneRequest = {},
): Promise<{ id: string }> {
  return postJson<{ id: string }>(`/history/${encodeURIComponent(id)}/clone`, req);
}

export function compareHistory(ids: string[]): Promise<HistoryCompareResponse> {
  if (ids.length < 2 || ids.length > 5) {
    return Promise.reject(new Error('对比记录数量必须在 2~5 之间'));
  }
  return getJson<HistoryCompareResponse>(
    '/history/compare' + buildQuery({ ids: ids.join(',') }),
  );
}
