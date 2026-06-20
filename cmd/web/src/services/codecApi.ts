/**
 * codecApi — codec 编辑器的两个 admin 端点封装（T3 Batch-3 任务 A）。
 *
 *   - GET  /sbot/codec/algorithms  → AlgoMeta[]（算法下拉元数据）
 *   - POST /sbot/codec/preview     → PreviewResult（单次 encode/decode 解释）
 *
 * 设计约束（与 t3-b3a-algorithms-brief §3.2 对齐）：
 *   - **请求收拢 services**：复用 `services/api.ts` 的 getJson/postJson（自动拼 API_PREFIX='/sbot' +
 *     处理非 2xx），**组件不直接 fetch**。
 *   - **算法清单失败语义**：HTTP 非 2xx / 网络异常 → 抛中文 Error（前缀「算法清单加载失败：…」）；
 *     调用方负责 message.error + 空下拉，**禁止本地伪清单兜底**。
 *   - **预览编辑器语义**：HTTP 200 即使 PreviewResult.error 非空也照常返回 result（前端据 Error 字段提示）；
 *     仅 HTTP 非 2xx（坏 schema/坏 JSON）抛中文 Error（前缀「预览失败：…」）。
 *
 * preview 端点的 UI（实时预览面板）在 B3-B 接；本任务只封装。
 */

import type { AlgoMeta, PreviewRequest, PreviewResult } from '@/types/codec';
import { getJson, postJson } from './api';

/**
 * GET /sbot/codec/algorithms —— 返回所有已注册算法的元数据（按 op 分组稳定排序）。
 *
 * 失败（HTTP 非 2xx / 网络异常 / 解析失败）→ 抛中文 Error。
 */
export async function fetchCodecAlgorithms(): Promise<AlgoMeta[]> {
  try {
    const data = await getJson<AlgoMeta[]>('/codec/algorithms');
    // 后端契约：直接返回 AlgoMeta[]（裸数组）。防御性校验非数组 → 抛中文（不静默兜底）。
    if (!Array.isArray(data)) {
      throw new Error('算法清单响应不是数组');
    }
    return data;
  } catch (e) {
    const reason = e instanceof Error ? e.message : String(e);
    throw new Error(`算法清单加载失败：${reason}`);
  }
}

/**
 * POST /sbot/codec/preview —— 单次 encode/decode 帧解释。
 *
 * 编辑器语义：HTTP 200 即使 result.error 非空也照常返回 result（调用方据 error 字段提示）；
 * 仅 HTTP 非 2xx（坏 schema/坏 JSON）抛中文 Error。
 */
export async function previewCodec(req: PreviewRequest): Promise<PreviewResult> {
  try {
    return await postJson<PreviewResult>('/codec/preview', req);
  } catch (e) {
    const reason = e instanceof Error ? e.message : String(e);
    throw new Error(`预览失败：${reason}`);
  }
}
