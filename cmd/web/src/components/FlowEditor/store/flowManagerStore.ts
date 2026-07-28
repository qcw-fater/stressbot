/**
 * 流程模板库前端存取（后端 MySQL，经 services/flowsApi）。
 *
 * 保留 saveFlow/getFlow/listFlows/renameFlow/deleteFlow 函数形态，使 FlowManagerModal
 * 逻辑改动最小；底层从浏览器本地存储切换到服务器流程模板库。
 *
 * 流程模板只存 flow + layout，不内嵌 Lua/proto/adapter（资源走独立管理链路）。
 */
import type { FlowJson } from '../codec/flowToJson';
import type { FlowLayout } from '@/types/editor';
import { ApiError } from '@/services/api';
import {
  createFlowTemplate,
  deleteFlowTemplate,
  getFlowTemplate,
  listFlowTemplates,
  updateFlowTemplate,
  type FlowTemplateDetail,
  type FlowTemplateSummary,
} from '@/services/flowsApi';

/** 流程列表条目（与后端 FlowTemplateSummary 对齐，不含 flow/layout）。 */
export interface ManagedFlow {
  id: string;
  name: string;
  nodeCount: number;
  actionCount: number;
  createdAt: string;
  updatedAt: string;
}

/** 流程详情（打开/启动时读取，含 flow/layout）。 */
export interface ManagedFlowDetail {
  flow: FlowJson;
  layout?: FlowLayout;
}

export const FLOW_NAME_MAX_LENGTH = 80;

function normalizeFlowName(name: string): string {
  const nextName = name.trim();
  if (!nextName) throw new Error('流程名称不能为空');
  if (nextName.length > FLOW_NAME_MAX_LENGTH) throw new Error(`流程名称不能超过 ${FLOW_NAME_MAX_LENGTH} 个字符`);
  return nextName;
}

function toManaged(s: FlowTemplateSummary): ManagedFlow {
  return {
    id: s.id,
    name: s.name,
    nodeCount: s.nodeCount,
    actionCount: s.actionCount,
    createdAt: s.createdAt,
    updatedAt: s.updatedAt,
  };
}

/**
 * 保存流程：existingId 提供时覆盖该模板（含 flow/layout），否则新建。
 * 返回摘要（不含 flow/layout）。
 */
export async function saveFlow(
  name: string,
  flow: FlowJson,
  layout: FlowLayout | undefined,
  existingId?: string,
): Promise<ManagedFlow> {
  const req = { name: normalizeFlowName(name), flow, layout };
  const detail: FlowTemplateDetail = existingId
    ? await updateFlowTemplate(existingId, req)
    : await createFlowTemplate(req);
  return toManaged(detail);
}

/** 读取流程详情（flow/layout）。模板不存在返回 undefined；其他错误冒泡给调用方展示真实原因。 */
export async function getFlow(id: string): Promise<ManagedFlowDetail | undefined> {
  try {
    const detail = await getFlowTemplate(id);
    return { flow: detail.flow, layout: detail.layout };
  } catch (e) {
    // 只把"模板不存在"翻译成 undefined；服务器未启用流程库/网络错误等冒泡由调用方展示。
    if (e instanceof ApiError && e.code === 'FLOW_TEMPLATE_NOT_FOUND') return undefined;
    throw e;
  }
}

/** 列出所有流程模板摘要（按更新时间倒序）。 */
export async function listFlows(): Promise<ManagedFlow[]> {
  const items = await listFlowTemplates();
  return items.map(toManaged);
}

/** 重命名流程模板（仅改 name）。 */
export async function renameFlow(id: string, name: string): Promise<ManagedFlow> {
  const nextName = normalizeFlowName(name);
  // PUT 不带 flow → 后端仅重命名，不覆盖内容。
  const detail = await updateFlowTemplate(id, { name: nextName });
  return toManaged(detail);
}

/** 删除流程模板。 */
export async function deleteFlow(id: string): Promise<void> {
  await deleteFlowTemplate(id);
}
