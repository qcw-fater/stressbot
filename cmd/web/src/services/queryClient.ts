import { QueryClient, type QueryKey } from '@tanstack/react-query';
import { isAbortError } from './api';

const QUERY_GC_TIME = 10 * 60 * 1000;

/** 普通只读请求允许两次短暂重试；主动取消永远不重试。 */
export function shouldRetryQuery(failureCount: number, error: unknown): boolean {
  return !isAbortError(error) && failureCount < 2;
}

export function createAppQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: shouldRetryQuery,
        retryDelay: (attempt) => Math.min(500 * 2 ** attempt, 3000),
        gcTime: QUERY_GC_TIME,
        refetchOnWindowFocus: false,
        refetchOnReconnect: true,
      },
      mutations: {
        retry: false,
      },
    },
  });
}

/** 单元测试必须可预测：不做隐式重试，也不把失败留到后续测试。 */
export function createTestQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
        gcTime: 0,
        refetchOnWindowFocus: false,
      },
      mutations: {
        retry: false,
      },
    },
  });
}

export const appQueryClient = createAppQueryClient();

/**
 * disabled observer 仍挂载时，TanStack Query 不会自动取消在途请求。
 * 只在该 key 已没有 active observer 时取消，避免一个弹窗关闭误伤另一个共享读取方。
 */
export async function cancelInactiveQuery(client: QueryClient, queryKey: QueryKey): Promise<void> {
  const query = client.getQueryCache().find({ queryKey, exact: true });
  if (!query || query.isActive()) return;
  await client.cancelQueries({ queryKey, exact: true });
}
