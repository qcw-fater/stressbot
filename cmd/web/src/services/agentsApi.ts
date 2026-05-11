/**
 * Agent 管理 API 封装（对应 docs/api-monitor.md §6）。
 *
 * 升级相关接口已废弃：版本更新通过手动重启 Agent 完成，无需前端介入。
 * 仅保留：列表 / 详情 / 强制注销（仅 offline）。
 */

import { adaptList, del, getJson, postJson } from './api';
import type { AgentBrief, AgentDetail, AgentsListResponse } from '@/types/api';

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

export function shutdownAgent(id: string): Promise<void> {
  return postJson<void>(`/agents/${encodeURIComponent(id)}/shutdown`, {});
}

export async function shutdownAllAgents(): Promise<{ succeeded: string[]; failed: string[] }> {
  return postJson<{ succeeded: string[]; failed: string[] }>('/agents/shutdown-all', {});
}
