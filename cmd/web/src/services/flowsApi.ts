/**
 * 流程模板库 API 封装（对应后端 /sbot/flows CRUD）。
 *
 * 流程模板保存在服务器 MySQL，前端流程（flowManagerStore）经此模块读写，
 * 不再使用浏览器本地存储。服务器未配置 MySQL 时接口返回 FLOW_LIBRARY_DISABLED，
 * 由调用方（flowManagerStore）翻译为用户可理解的提示。
 */
import type { FlowJson } from '@/components/FlowEditor/codec/flowToJson';
import type { FlowLayout } from '@/types/editor';
import { del, getJson, postJson, putJson } from './api';

/** 流程模板摘要（列表用，不含 flow/layout，避免传输大字段）。 */
export interface FlowTemplateSummary {
  id: string;
  name: string;
  nodeCount: number;
  actionCount: number;
  createdAt: string;
  updatedAt: string;
}

/** 流程模板详情（含完整 flow/layout，供打开与启动读取）。 */
export interface FlowTemplateDetail extends FlowTemplateSummary {
  flow: FlowJson;
  layout?: FlowLayout;
}

/** 创建/覆盖流程模板请求。flow 省略时后端仅重命名，不覆盖内容。 */
export interface FlowTemplateSaveRequest {
  name: string;
  flow?: FlowJson;
  layout?: FlowLayout;
}

export interface FlowSnapshot {
  revision: string;
  items: FlowTemplateDetail[];
}

export interface ReplaceFlowSnapshotRequest {
  expectedRevision: string;
  items: FlowTemplateDetail[];
}

export interface ReplaceFlowSnapshotResponse {
  revision: string;
  count: number;
}

/** 列表（按更新时间倒序）。 */
export function listFlowTemplates(signal?: AbortSignal): Promise<FlowTemplateSummary[]> {
  return getJson<FlowTemplateSummary[]>('/flows', { signal });
}

/** 读取完整流程模板。 */
export function getFlowTemplate(id: string): Promise<FlowTemplateDetail> {
  return getJson<FlowTemplateDetail>(`/flows/${encodeURIComponent(id)}`);
}

/** 创建流程模板（flow 必填）。 */
export function createFlowTemplate(req: FlowTemplateSaveRequest): Promise<FlowTemplateDetail> {
  return postJson<FlowTemplateDetail>('/flows', req);
}

/** 覆盖流程模板：flow 非空时覆盖 flow/layout 并重新计数，flow 省略时仅重命名。 */
export function updateFlowTemplate(
  id: string,
  req: FlowTemplateSaveRequest,
): Promise<FlowTemplateDetail> {
  return putJson<FlowTemplateDetail>(`/flows/${encodeURIComponent(id)}`, req);
}

/** 删除流程模板。 */
export function deleteFlowTemplate(id: string): Promise<void> {
  return del<void>(`/flows/${encodeURIComponent(id)}`);
}

export function getFlowSnapshot(): Promise<FlowSnapshot> {
  return getJson<FlowSnapshot>('/flows/snapshot');
}

export function replaceFlowSnapshot(
  request: ReplaceFlowSnapshotRequest,
): Promise<ReplaceFlowSnapshotResponse> {
  return putJson<ReplaceFlowSnapshotResponse>('/flows/snapshot', request);
}
