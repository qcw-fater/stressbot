/**
 * 统一的 fetch 封装。
 *
 * 设计要点：
 * - 所有非 2xx 响应（包括网络错误）一律抛出 `ApiError` 实例，调用方按 `code` 分支处理；
 * - `204 No Content` 返回 `undefined`，调用方按 `Promise<void>` 接住即可；
 * - 不依赖 axios，使用浏览器原生 fetch；浏览器开发期通过 vite proxy 转到 Admin :7718。
 *
 * 单一事实源：docs/api-monitor.md §3.3 / §14.13.1。
 */

import type { ApiErrorBody, FrameworkCode } from '@/types/api';
import { API_PREFIX } from './env';

/**
 * `ApiError` 是所有非 2xx 响应的统一异常类型。
 *
 * 使用 `instanceof ApiError` 判断；保留原 HTTP 状态码与后端 `details` 便于上层做条件分支
 * （例如 `TASK_CONFLICT` 走 modal、`CAPACITY_EXCEEDED` 取 details.maxBots）。
 */
export class ApiError extends Error {
  readonly code: string;
  readonly status: number;
  readonly details?: Record<string, unknown>;

  constructor(body: ApiErrorBody, status: number) {
    super(body.message);
    this.name = 'ApiError';
    this.code = body.code;
    this.status = status;
    this.details = body.details;
  }
}

/** API 前缀；vite dev server 会代理到 Admin :7718，生产由 Admin 同源托管。 */

/** 内部统一入口：所有方法走它。 */
async function request<T>(method: string, path: string, init?: RequestInit): Promise<T> {
  const url = path.startsWith('http') ? path : API_PREFIX + path;

  let res: Response;
  try {
    res = await fetch(url, { method, ...init });
  } catch (e) {
    // 网络断开 / CORS / 浏览器拒绝 → 统一包装为 NETWORK_ERROR，避免上层各自处理 TypeError
    throw new ApiError(
      { code: 'NETWORK_ERROR', message: (e as Error).message ?? 'network error' },
      0,
    );
  }

  if (!res.ok) {
    const body = await res.json().catch(
      (): ApiErrorBody => ({
        code: res.status === 0 ? 'NETWORK_ERROR' : 'HTTP_ERROR',
        message: res.statusText || `HTTP ${res.status}`,
      }),
    );
    throw new ApiError(body, res.status);
  }

  if (res.status === 204) {
    return undefined as T;
  }

  // 大部分接口都是 application/json，少数（如 /api/metrics/summary）返回 text/plain；
  // 调用侧主动选择走 `requestText` 即可，这里默认按 JSON 解析。
  return (await res.json()) as T;
}

/** GET JSON */
export function getJson<T>(path: string, init?: RequestInit): Promise<T> {
  return request<T>('GET', path, { ...init, headers: { Accept: 'application/json', ...(init?.headers ?? {}) } });
}

/** POST JSON 请求体 */
export function postJson<T>(path: string, body?: unknown, init?: RequestInit): Promise<T> {
  return request<T>('POST', path, {
    ...init,
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
      ...(init?.headers ?? {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

/** PUT JSON 请求体 */
export function putJson<T>(path: string, body?: unknown, init?: RequestInit): Promise<T> {
  return request<T>('PUT', path, {
    ...init,
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
      ...(init?.headers ?? {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

/** DELETE，204 时返回 void */
export function del<T = void>(path: string, init?: RequestInit): Promise<T> {
  return request<T>('DELETE', path, init);
}

/** 上传 multipart/form-data；FormData 由调用方组装（避免重复实现） */
export function postMultipart<T>(path: string, fd: FormData, init?: RequestInit): Promise<T> {
  return request<T>('POST', path, {
    ...init,
    headers: { Accept: 'application/json', ...(init?.headers ?? {}) },
    body: fd,
  });
}

/** 获取工具内置框架错误码（< 100 保留段） */
export function getErrorCodes(): Promise<FrameworkCode[]> {
  return getJson<FrameworkCode[]>('/api/error-codes');
}

/** 文本响应（用于 /api/metrics/summary 等纯文本接口） */
export async function getText(path: string, init?: RequestInit): Promise<string> {
  const url = path.startsWith('http') ? path : API_PREFIX + path;
  let res: Response;
  try {
    res = await fetch(url, { method: 'GET', ...init });
  } catch (e) {
    throw new ApiError(
      { code: 'NETWORK_ERROR', message: (e as Error).message ?? 'network error' },
      0,
    );
  }
  if (!res.ok) {
    throw new ApiError({ code: 'HTTP_ERROR', message: res.statusText }, res.status);
  }
  return res.text();
}

/**
 * 兼容后端"裸数组"或"{items}"两种返回格式，统一吐出 `{items, total}`。
 *
 * 后端 `handleListTasks/handleListAgents/handleListBinaries` 直接返回 `[]T`，
 * 但前端约定 `{items, total}` 形态便于后续追加分页元数据；
 * 这里在 services 层抹平差异，让 store / 组件代码不感知差别。
 */
export function adaptList<T>(resp: unknown): { items: T[]; total: number } {
  if (Array.isArray(resp)) {
    return { items: resp as T[], total: resp.length };
  }
  if (resp && typeof resp === 'object') {
    const obj = resp as { items?: T[]; total?: number };
    const items = Array.isArray(obj.items) ? obj.items : [];
    const total = typeof obj.total === 'number' ? obj.total : items.length;
    return { items, total };
  }
  return { items: [], total: 0 };
}

/** 拼接 query string；undefined / null 会被忽略；数组会展开为重复键。 */
export function buildQuery(params: Record<string, unknown>): string {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null) continue;
    if (Array.isArray(v)) {
      for (const item of v) sp.append(k, String(item));
    } else if (typeof v === 'boolean') {
      sp.set(k, v ? 'true' : 'false');
    } else {
      sp.set(k, String(v));
    }
  }
  const s = sp.toString();
  return s ? `?${s}` : '';
}
