/**
 * 任务管理 API 封装（对应 docs/api-monitor.md §5）。
 */

import { adaptList, buildQuery, getJson, postJson, postMultipart } from './api';
import { API_PREFIX } from './env';
import type {
  CreateTaskResponse,
  StartTaskResponse,
  TaskBrief,
  TaskDetail,
  TaskState,
  TasksListResponse,
} from '@/types/api';

export interface TasksListParams {
  state?: TaskState;
  limit?: number;
  offset?: number;
}

/**
 * 后端目前直接返回 `[]TaskBrief`，前端在此包装为 `{items, total}`。
 */
export async function listTasks(params: TasksListParams = {}): Promise<TasksListResponse> {
  const resp = await getJson<unknown>('/tasks' + buildQuery(params as Record<string, unknown>));
  return adaptList<TaskBrief>(resp);
}

export function getTask(id: string): Promise<TaskDetail> {
  return getJson<TaskDetail>(`/tasks/${encodeURIComponent(id)}`);
}

/**
 * 创建任务（multipart/form-data）。FormData 由调用方按 §5.3 字段约定组装：
 *   - `name`, `totalBots`, `robotConfig`(JSON)
 *   - `flow.json` 文件
 *   - `proto/<filename>` / `scripts/<filename>` 多个文件
 *   - 可选 `deadline`
 *
 * 后端实际返回完整 task 对象，前端在此提取 id 适配为 `{id}`。
 */
export async function createTask(fd: FormData): Promise<CreateTaskResponse> {
  const resp = await postMultipart<unknown>('/tasks', fd);
  if (resp && typeof resp === 'object') {
    const r = resp as { id?: string; ID?: string };
    return { id: r.id ?? r.ID ?? '' };
  }
  return { id: '' };
}

export function startTask(id: string): Promise<StartTaskResponse> {
  return postJson<StartTaskResponse>(`/tasks/${encodeURIComponent(id)}/start`);
}

/**
 * 后端 stop 返回最新的 TaskBrief（含 state=stopping 等字段）；
 * 前端拿到后立即更新 active task，省一次 5s 轮询等待。
 *
 * 兼容老后端返回 `{status:"stopping"}`：仍 await 但调用方自己用 store 兜底，不会崩。
 */
export function stopTask(id: string): Promise<TaskBrief> {
  return postJson<TaskBrief>(`/tasks/${encodeURIComponent(id)}/stop`);
}

/** 任务配置文件下载链接（用于 a 标签 href，无需走 fetch） */
export function taskConfigUrl(id: string, path: string): string {
  return `${API_PREFIX}/tasks/${encodeURIComponent(id)}/config/${path}`;
}
