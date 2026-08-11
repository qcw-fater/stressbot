import createClient from 'openapi-fetch';
import type { paths } from '@/generated/admin-api';
import type { ApiErrorBody } from '@/types/api';
import { ApiError, isAbortError } from './api';

const baseUrl = typeof window === 'undefined' ? 'http://localhost' : window.location.origin;

/** 由 OpenAPI 生成的管理面客户端；fetch 延迟读取，便于测试注入且保持浏览器同源策略。 */
export const managementClient = createClient<paths>({
  baseUrl,
  fetch: (request) => globalThis.fetch(request),
});

type GeneratedResult<T> =
  | { data: T; error?: never; response: Response }
  | { data?: never; error: unknown; response: Response };

/** 把 openapi-fetch 的判别联合转换为项目既有的 Promise/ApiError 契约。 */
export async function unwrapGenerated<T>(
  request: () => Promise<GeneratedResult<T | undefined>>,
): Promise<T> {
  let result: GeneratedResult<T | undefined>;
  try {
    result = await request();
  } catch (error) {
    if (isAbortError(error)) throw error;
    throw new ApiError(
      {
        code: 'NETWORK_ERROR',
        message: error instanceof Error ? error.message : 'network error',
      },
      0,
    );
  }

  if ('data' in result && result.data !== undefined) {
    return result.data;
  }
  if (result.response.status === 204) {
    return undefined as T;
  }
  throw generatedApiError(result.error, result.response);
}

function generatedApiError(error: unknown, response: Response): ApiError {
  const value = error && typeof error === 'object' ? (error as Record<string, unknown>) : {};
  const details =
    value.details && typeof value.details === 'object' && !Array.isArray(value.details)
      ? (value.details as Record<string, unknown>)
      : undefined;
  const body: ApiErrorBody = {
    code: typeof value.code === 'string' ? value.code : 'HTTP_ERROR',
    message:
      typeof value.message === 'string'
        ? value.message
        : response.statusText || `HTTP ${response.status}`,
    details,
  };
  return new ApiError(body, response.status);
}
