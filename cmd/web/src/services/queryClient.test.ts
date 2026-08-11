import { describe, expect, it } from 'vitest';
import { isAbortError } from './api';
import {
  cancelInactiveQuery,
  createAppQueryClient,
  createTestQueryClient,
  shouldRetryQuery,
} from './queryClient';
import { queryKeys } from './queryKeys';

describe('query client', () => {
  it('测试客户端关闭查询和变更重试', () => {
    const client = createTestQueryClient();
    expect(client.getDefaultOptions().queries?.retry).toBe(false);
    expect(client.getDefaultOptions().mutations?.retry).toBe(false);
  });

  it('生产查询最多重试两次，取消不重试', () => {
    expect(shouldRetryQuery(0, new Error('暂时失败'))).toBe(true);
    expect(shouldRetryQuery(1, new Error('仍然失败'))).toBe(true);
    expect(shouldRetryQuery(2, new Error('停止重试'))).toBe(false);

    const abort = new DOMException('已取消', 'AbortError');
    expect(isAbortError(abort)).toBe(true);
    expect(shouldRetryQuery(0, abort)).toBe(false);
  });

  it('生产客户端不因窗口聚焦意外刷新，并设置有界缓存时间', () => {
    const options = createAppQueryClient().getDefaultOptions().queries;
    expect(options?.refetchOnWindowFocus).toBe(false);
    expect(options?.gcTime).toBe(10 * 60 * 1000);
  });

  it('关闭最后一个 observer 后取消仍在途的查询', async () => {
    let signal: AbortSignal | undefined;
    const client = createTestQueryClient();
    const pending = client.fetchQuery({
      queryKey: queryKeys.flows.all,
      queryFn: ({ signal: requestSignal }) => {
        signal = requestSignal;
        return new Promise<never>((_, reject) => {
          requestSignal.addEventListener('abort', () => reject(new DOMException('已取消', 'AbortError')));
        });
      },
    });

    await cancelInactiveQuery(client, queryKeys.flows.all);

    expect(signal?.aborted).toBe(true);
    await expect(pending).rejects.toMatchObject({ message: 'CancelledError' });
  });
});
