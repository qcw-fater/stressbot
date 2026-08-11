import { describe, expect, it, vi } from 'vitest';
import { createTestQueryClient } from './queryClient';
import { flowListQueryOptions, perAgentMetricsQueryOptions } from './queryOptions';
import { queryKeys } from './queryKeys';
import { listFlowTemplates } from './flowsApi';

vi.mock('./flowsApi', () => ({ listFlowTemplates: vi.fn() }));

const listFlowsMock = vi.mocked(listFlowTemplates);

describe('query options', () => {
  it('所有动态参数都进入稳定 query key', () => {
    expect(queryKeys.tasks.detail('a')).toEqual(['tasks', 'detail', 'a']);
    expect(queryKeys.tasks.detail('b')).not.toEqual(queryKeys.tasks.detail('a'));
    expect(queryKeys.metrics.cluster('task-a')).toEqual(['metrics', 'cluster', 'task-a']);
    expect(queryKeys.agents.metrics('task-a')).toEqual(['agents', 'metrics', 'task-a']);
  });

  it('逐节点指标只有面板 observer 按运行态频率轮询', () => {
    expect(perAgentMetricsQueryOptions('task-a').refetchInterval).toBe(5_000);
  });

  it('相同 key 的并发读取合并成一个请求', async () => {
    let resolve!: (value: []) => void;
    listFlowsMock.mockReturnValue(
      new Promise((done) => {
        resolve = done;
      }),
    );
    const client = createTestQueryClient();
    const options = flowListQueryOptions();

    const first = client.fetchQuery(options);
    const second = client.fetchQuery(options);
    expect(listFlowsMock).toHaveBeenCalledTimes(1);
    resolve([]);
    await expect(Promise.all([first, second])).resolves.toEqual([[], []]);
  });

  it('取消查询会把 AbortSignal 传到 service', async () => {
    let capturedSignal: AbortSignal | undefined;
    listFlowsMock.mockImplementation((signal) => {
      capturedSignal = signal;
      return new Promise((_, reject) => {
        signal?.addEventListener('abort', () => reject(new DOMException('已取消', 'AbortError')));
      });
    });
    const client = createTestQueryClient();
    const pending = client.fetchQuery(flowListQueryOptions());

    await client.cancelQueries({ queryKey: queryKeys.flows.all });
    expect(capturedSignal?.aborted).toBe(true);
    await expect(pending).rejects.toMatchObject({ message: 'CancelledError' });
  });
});
