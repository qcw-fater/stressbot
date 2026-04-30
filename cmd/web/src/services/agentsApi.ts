/**
 * Agent 管理 + 升级 API 封装（对应 docs/api-monitor.md §6 / §9）。
 */

import { adaptList, del, getJson, postJson } from './api';
import type {
  AgentBrief,
  AgentDetail,
  AgentsListResponse,
  UpgradeAllRequest,
  UpgradeAllResponse,
  UpgradeRequest,
  UpgradeResponse,
  UpgradeStatus,
} from '@/types/api';

/**
 * 后端目前直接返回 `[]AgentBrief`，前端包装为 `{items}`。
 */
export async function listAgents(): Promise<AgentsListResponse> {
  const resp = await getJson<unknown>('/agents');
  return { items: adaptList<AgentBrief>(resp).items };
}

export function getAgent(id: string): Promise<AgentDetail> {
  return getJson<AgentDetail>(`/agents/${encodeURIComponent(id)}`);
}

export function deleteAgent(id: string): Promise<void> {
  return del<void>(`/agents/${encodeURIComponent(id)}`);
}

export function upgradeAgent(id: string, req: UpgradeRequest): Promise<UpgradeResponse> {
  return postJson<UpgradeResponse>(`/agents/${encodeURIComponent(id)}/upgrade`, req);
}

export function upgradeAll(req: UpgradeAllRequest): Promise<UpgradeAllResponse> {
  return postJson<UpgradeAllResponse>('/agents/upgrade-all', req);
}

export function getUpgradeStatus(): Promise<UpgradeStatus> {
  return getJson<UpgradeStatus>('/agents/upgrade-status');
}

export function cancelUpgrade(): Promise<void> {
  return postJson<void>('/agents/upgrade-cancel');
}
