import type { PropsWithChildren } from 'react';
import { act, renderHook } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type {
  AgentsListResponse,
  ClusterSystemSnapshot,
  StressAggregate,
  TaskDetail,
} from '@/types/api';
import { createTestQueryClient } from './queryClient';
import { queryKeys } from './queryKeys';
import { useRuntimeQueries } from './useRuntimeQueries';
import { useRuntimeStore, type RuntimeMode } from './runtimeStore';
import * as agentsApi from './agentsApi';
import * as metricsApi from './metricsApi';
import * as tasksApi from './tasksApi';

vi.mock('./agentsApi', () => ({ listAgents: vi.fn() }));
vi.mock('./metricsApi', () => ({ getClusterMetrics: vi.fn(), getClusterSystem: vi.fn() }));
vi.mock('./tasksApi', () => ({ getTask: vi.fn() }));

const listAgentsMock = vi.mocked(agentsApi.listAgents);
const getClusterMetricsMock = vi.mocked(metricsApi.getClusterMetrics);
const getClusterSystemMock = vi.mocked(metricsApi.getClusterSystem);
const getTaskMock = vi.mocked(tasksApi.getTask);

const agents = { items: [] } as AgentsListResponse;
const stress = {
  snapshot: { timestamp: '2026-08-11T00:00:00Z', uptimeSeconds: 1, actions: [] },
  reportingAgents: 1,
  totalAgents: 1,
  offlineAgents: 0,
  assignedAgents: 1,
  freshAgents: 1,
} as unknown as StressAggregate;
const system = {
  timestamp: '2026-08-11T00:00:00Z',
  agents: [],
} as unknown as ClusterSystemSnapshot;
const runningTask = {
  id: 'task-a',
  name: 'task-a',
  state: 'running',
  config: {},
  assignments: [],
} as unknown as TaskDetail;

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

async function advance(ms = 0): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

function renderRuntimeHook(
  mode: RuntimeMode,
  taskId: string | undefined,
  reportConnectionHealth = vi.fn(),
  onTerminal = vi.fn(),
  intervalMs: number | null = 100,
) {
  const client = createTestQueryClient();
  const wrapper = ({ children }: PropsWithChildren) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  const rendered = renderHook(
    ({ nextMode, nextTaskId }) =>
      useRuntimeQueries({
        mode: nextMode,
        taskId: nextTaskId,
        intervalMs: intervalMs ?? undefined,
        reportConnectionHealth,
        onTerminal,
      }),
    { wrapper, initialProps: { nextMode: mode, nextTaskId: taskId } },
  );
  return { ...rendered, client, reportConnectionHealth, onTerminal };
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.clearAllMocks();
  useRuntimeStore.setState({
    mode: 'running',
    activeTask: runningTask,
    latestStress: null,
    latestSystem: null,
    stressHistory: [],
    systemHistory: [],
    agents: [],
    agentsLoaded: false,
    connectionLost: false,
  });
  listAgentsMock.mockResolvedValue(agents);
  getClusterMetricsMock.mockResolvedValue(stress);
  getClusterSystemMock.mockResolvedValue(system);
  getTaskMock.mockResolvedValue(runningTask);
});

afterEach(() => {
  vi.useRealTimers();
});

describe('useRuntimeQueries', () => {
  it('同一查询不并发，停用后取消在途请求并停止后续轮询', async () => {
    const pending = deferred<StressAggregate>();
    getClusterMetricsMock.mockReturnValue(pending.promise);
    const { rerender } = renderRuntimeHook('running', 'task-a');

    await advance(1);
    expect(getClusterMetricsMock).toHaveBeenCalledTimes(1);
    await advance(500);
    expect(getClusterMetricsMock).toHaveBeenCalledTimes(1);

    rerender({ nextMode: 'finalReport', nextTaskId: 'task-a' });
    await advance();
    const signal = getClusterMetricsMock.mock.calls[0]?.[1];
    expect(signal?.aborted).toBe(true);
    await advance(500);
    expect(getClusterMetricsMock).toHaveBeenCalledTimes(1);
  });

  it('任务切换会取消旧请求，并只接受新任务结果', async () => {
    const first = deferred<TaskDetail>();
    getTaskMock.mockImplementation((id) =>
      id === 'task-a' ? first.promise : Promise.resolve({ ...runningTask, id }),
    );
    const { rerender } = renderRuntimeHook('running', 'task-a');
    await advance();

    rerender({ nextMode: 'running', nextTaskId: 'task-b' });
    await advance();
    expect(getTaskMock).toHaveBeenCalledTimes(2);
    const oldSignal = getTaskMock.mock.calls[0]?.[1];
    expect(oldSignal?.aborted).toBe(true);
    expect(useRuntimeStore.getState().activeTask?.id).toBe('task-b');
  });

  it('连续三次失败后标记断线，成功后恢复，AbortError 不计失败', async () => {
    const abort = new DOMException('已取消', 'AbortError');
    getClusterMetricsMock
      .mockRejectedValueOnce(abort)
      .mockRejectedValueOnce(new Error('失败 1'))
      .mockRejectedValueOnce(new Error('失败 2'))
      .mockRejectedValueOnce(new Error('失败 3'))
      .mockResolvedValue(stress);
    const { reportConnectionHealth, result } = renderRuntimeHook('running', 'task-a');

    await advance();
    await advance(100);
    await advance(100);
    expect(reportConnectionHealth).not.toHaveBeenCalledWith('stress', true);
    await advance(100);
    await advance(1);
    expect(result.current.stressQuery.errorUpdateCount).toBe(4);
    expect(getClusterMetricsMock).toHaveBeenCalledTimes(4);
    expect(reportConnectionHealth).toHaveBeenCalledWith('stress', true);
    await advance(100);
    await advance(1);
    expect(reportConnectionHealth).toHaveBeenCalledWith('stress', false);
  });

  it('切换任务时清空旧任务的失败计数和断线状态', async () => {
    getClusterMetricsMock.mockRejectedValue(new Error('指标失败'));
    const { reportConnectionHealth, rerender } = renderRuntimeHook('running', 'task-a');

    await advance();
    await advance(100);
    expect(reportConnectionHealth).not.toHaveBeenCalledWith('stress', true);

    rerender({ nextMode: 'running', nextTaskId: 'task-b' });
    await advance();
    expect(getClusterMetricsMock).toHaveBeenCalledTimes(3);
    expect(reportConnectionHealth).not.toHaveBeenCalledWith('stress', true);
    expect(reportConnectionHealth).toHaveBeenCalledWith('stress', false);
  });

  it('共享 query 由其他 observer 发起失败时仍更新运行态连接健康', async () => {
    const { client, reportConnectionHealth } = renderRuntimeHook('edit', undefined);
    await advance();
    reportConnectionHealth.mockClear();

    for (let attempt = 0; attempt < 3; attempt += 1) {
      await act(async () => {
        await client.fetchQuery({
          queryKey: queryKeys.agents.all,
          queryFn: () => Promise.reject(new Error(`共享查询失败 ${attempt + 1}`)),
          retry: false,
        }).catch(() => undefined);
      });
      await advance();
    }

    expect(reportConnectionHealth).toHaveBeenCalledWith('agents', true);
  });

  it('同一份指标和系统数据各只写入一次，终态任务只通知一次', async () => {
    const terminal = {
      ...runningTask,
      state: 'stopped',
      cleanupSummary: { status: 'partial' },
    } as TaskDetail;
    getTaskMock.mockResolvedValue(runningTask);
    const { onTerminal, rerender } = renderRuntimeHook('running', 'task-a');

    await advance();
    expect(onTerminal).not.toHaveBeenCalled();
    expect(useRuntimeStore.getState().stressHistory).toHaveLength(1);
    expect(useRuntimeStore.getState().systemHistory).toHaveLength(1);

    getTaskMock.mockResolvedValue(terminal);
    await advance(100);
    await advance(1);
    expect(getTaskMock).toHaveBeenCalledTimes(2);
    expect(onTerminal).toHaveBeenCalledTimes(1);
    expect(useRuntimeStore.getState().stressHistory).toHaveLength(1);
    expect(useRuntimeStore.getState().systemHistory).toHaveLength(1);

    rerender({ nextMode: 'running', nextTaskId: 'task-a' });
    await advance();
    expect(onTerminal).toHaveBeenCalledTimes(1);
    expect(useRuntimeStore.getState().stressHistory).toHaveLength(1);
    expect(useRuntimeStore.getState().systemHistory).toHaveLength(1);
  });

  it('60 秒请求调度与运行模式一致，不产生重复轮询', async () => {
    const idle = renderRuntimeHook('edit', undefined, vi.fn(), vi.fn(), null);
    await advance(60_000);
    expect(listAgentsMock).toHaveBeenCalledTimes(7);
    expect(getClusterSystemMock).toHaveBeenCalledTimes(7);
    expect(getClusterMetricsMock).not.toHaveBeenCalled();
    expect(getTaskMock).not.toHaveBeenCalled();
    idle.unmount();

    vi.clearAllMocks();
    listAgentsMock.mockResolvedValue(agents);
    getClusterMetricsMock.mockResolvedValue(stress);
    getClusterSystemMock.mockResolvedValue(system);
    getTaskMock.mockResolvedValue(runningTask);
    const running = renderRuntimeHook('running', 'task-a', vi.fn(), vi.fn(), null);
    await advance(60_000);
    expect(listAgentsMock).toHaveBeenCalledTimes(13);
    expect(getClusterSystemMock).toHaveBeenCalledTimes(13);
    expect(getClusterMetricsMock).toHaveBeenCalledTimes(13);
    expect(getTaskMock).toHaveBeenCalledTimes(13);
    running.unmount();

    vi.clearAllMocks();
    const finalReport = renderRuntimeHook('finalReport', 'task-a', vi.fn(), vi.fn(), null);
    await advance(60_000);
    expect(listAgentsMock).not.toHaveBeenCalled();
    expect(getClusterSystemMock).not.toHaveBeenCalled();
    expect(getClusterMetricsMock).not.toHaveBeenCalled();
    expect(getTaskMock).not.toHaveBeenCalled();
    finalReport.unmount();
  });
});
