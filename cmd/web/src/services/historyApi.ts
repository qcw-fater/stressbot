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

export function getHistory(id: string, stageIndex?: number): Promise<HistoryDetail> {
  return getJson<HistoryDetail>(
    `/history/${encodeURIComponent(id)}${buildQuery({ stageIndex })}`,
  );
}

export function updateHistory(
  id: string,
  req: UpdateHistoryRequest,
  stageIndex?: number,
): Promise<HistoryDetail> {
  const query = (stageIndex ?? -1) > 0 ? buildQuery({ stageIndex }) : '';
  return putJson<HistoryDetail>(`/history/${encodeURIComponent(id)}${query}`, req);
}

export function deleteHistory(id: string, force = false): Promise<void> {
  return del<void>(`/history/${encodeURIComponent(id)}${buildQuery({ force })}`);
}

export function getHistoryTimeseries(
  id: string,
  maxPoints?: number,
  stageIndex?: number,
): Promise<TimeseriesResponse> {
  return getJson<TimeseriesResponse>(
    `/history/${encodeURIComponent(id)}/timeseries${buildQuery({ maxPoints, stageIndex })}`,
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

/** 对比目标：整体（仅 id）或阶段段落（id + stageIndex）。 */
export type CompareTarget = string | { id: string; stageIndex?: number };

export function compareHistory(targets: CompareTarget[]): Promise<HistoryCompareResponse> {
  if (targets.length < 2 || targets.length > 5) {
    return Promise.reject(new Error('对比记录数量必须在 2~5 之间'));
  }
  // 任一目标带阶段段落号则走新入口 targets=a:-1,b:2，否则沿用旧 ids=a,b。
  const hasStage = targets.some((t) => typeof t === 'object' && (t.stageIndex ?? -1) > 0);
  if (hasStage) {
    const encoded = targets
      .map((t) => (typeof t === 'string' ? `${t}:-1` : `${t.id}:${t.stageIndex ?? -1}`))
      .join(',');
    return getJson<HistoryCompareResponse>('/history/compare' + buildQuery({ targets: encoded }));
  }
  const ids = targets.map((t) => (typeof t === 'string' ? t : t.id)).join(',');
  return getJson<HistoryCompareResponse>('/history/compare' + buildQuery({ ids }));
}
