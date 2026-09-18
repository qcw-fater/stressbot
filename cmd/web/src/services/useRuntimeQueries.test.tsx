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

interface RuntimeHookProps {
  nextMode: RuntimeMode;
  nextTaskId?: string;
  /** 编辑态下仍在远端运行的任务；不传即没有脱离监控的任务 */
  nextDetachedTaskId?: string;
}

function renderRuntimeHook(
  mode: RuntimeMode,
  taskId: string | undefined,
  reportConnectionHealth = vi.fn(),
  onTerminal = vi.fn(),
  intervalMs: number | null = 100,
  detachedTaskId: string | undefined = undefined,
) {
  const client = createTestQueryClient();
  const wrapper = ({ children }: PropsWithChildren) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  const rendered = renderHook<ReturnType<typeof useRuntimeQueries>, RuntimeHookProps>(
    ({ nextMode, nextTaskId, nextDetachedTaskId }) =>
      useRuntimeQueries({
        mode: nextMode,
        taskId: nextTaskId,
        detachedTaskId: nextDetachedTaskId,
        intervalMs: intervalMs ?? undefined,
        reportConnectionHealth,
        onTerminal,
      }),
    {
      wrapper,
      initialProps: {
        nextMode: mode,
        nextTaskId: taskId,
        nextDetachedTaskId: detachedTaskId,
      },
    },
  );
  return { ...rendered, client, reportConnectionHealth, onTerminal };
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.clearAllMocks();
  useRuntimeStore.setState({
    mode: 'running',
    activeTask: runningTask,
    detachedActiveTask: null,
    ownedTaskId: null,
    latestStress: null,
    latestSystem: null,
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
        await client
          .fetchQuery({
            queryKey: queryKeys.agents.all,
            queryFn: () => Promise.reject(new Error(`共享查询失败 ${attempt + 1}`)),
            retry: false,
          })
          .catch(() => undefined);
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
    // 数 push 调用次数而不是看 store 末态：同一份回包重复应用会写进同一个快照对象，
    // 末态上分辨不出来。
    const pushStress = vi.fn(useRuntimeStore.getState().pushStress);
    const pushSystem = vi.fn(useRuntimeStore.getState().pushSystem);
    useRuntimeStore.setState({ pushStress, pushSystem });
    const { onTerminal, rerender } = renderRuntimeHook('running', 'task-a');

    await advance();
    expect(onTerminal).not.toHaveBeenCalled();
    expect(pushStress).toHaveBeenCalledTimes(1);
    expect(pushSystem).toHaveBeenCalledTimes(1);
    expect(useRuntimeStore.getState().latestStress).toBe(stress.snapshot);

    // 下一拍任务已转终态，同拍到的指标/系统换成可区分的新回包
    getTaskMock.mockResolvedValue(terminal);
    getClusterMetricsMock.mockResolvedValue({
      ...stress,
      snapshot: { ...stress.snapshot, uptimeSeconds: 2 },
    } as unknown as StressAggregate);
    getClusterSystemMock.mockResolvedValue({
      ...system,
      timestamp: '2026-08-11T00:00:05Z',
    } as unknown as ClusterSystemSnapshot);
    await advance(100);
    await advance(1);
    expect(getTaskMock).toHaveBeenCalledTimes(2);
    expect(onTerminal).toHaveBeenCalledTimes(1);
    // 任务一结束服务器就清 activeID，之后的快照是零值，覆盖上去会让终态报告瞬间变空。
    // store 在 finalReport 下守门丢弃，这里确认末次运行中数据仍在。
    expect(useRuntimeStore.getState().latestStress).toBe(stress.snapshot);
    expect(useRuntimeStore.getState().latestSystem).toBe(system);

    const appliedStress = pushStress.mock.calls.length;
    const appliedSystem = pushSystem.mock.calls.length;
    rerender({ nextMode: 'running', nextTaskId: 'task-a' });
    await advance();
    expect(onTerminal).toHaveBeenCalledTimes(1);
    // 重渲染不会把已经处理过的那份回包再应用一遍
    expect(pushStress).toHaveBeenCalledTimes(appliedStress);
    expect(pushSystem).toHaveBeenCalledTimes(appliedSystem);
  });

  // 「返回编辑」后任务仍在远端跑，activeTask 已清空、常规任务轮询关着。
  // 不单独盯着它的话，顶部栏会永远停在「运行中」，「启动」按钮也一直被挡住。

  it('编辑态下跟踪脱离监控的任务状态', async () => {
    useRuntimeStore.setState({
      mode: 'edit',
      activeTask: null,
      detachedActiveTask: { ...runningTask },
    });
    const stopping = { ...runningTask, state: 'stopping' } as TaskDetail;
    getTaskMock.mockResolvedValue(stopping);
    renderRuntimeHook('edit', undefined, vi.fn(), vi.fn(), 100, 'task-a');

    await advance();
    expect(getTaskMock).toHaveBeenCalledWith('task-a', expect.anything());
    expect(useRuntimeStore.getState().detachedActiveTask?.state).toBe('stopping');
  });

  it('脱离监控的任务在远端结束后清掉引用，不弹终态报告', async () => {
    useRuntimeStore.setState({
      mode: 'edit',
      activeTask: null,
      detachedActiveTask: { ...runningTask },
      ownedTaskId: 'task-a',
    });
    getTaskMock.mockResolvedValue({ ...runningTask, state: 'stopped' } as TaskDetail);
    const { onTerminal } = renderRuntimeHook('edit', undefined, vi.fn(), vi.fn(), 100, 'task-a');

    await advance();
    expect(useRuntimeStore.getState().detachedActiveTask).toBeNull();
    // ownedTaskId 一并清掉，否则下次 attach 会把别人的任务当成自己发起的
    expect(useRuntimeStore.getState().ownedTaskId).toBeNull();
    // 用户当时选择了返回编辑，不该被这个任务的终态弹窗打断
    expect(onTerminal).not.toHaveBeenCalled();
  });

  it('进入监控（非 edit 模式）后停止脱离任务的轮询', async () => {
    useRuntimeStore.setState({ mode: 'edit', activeTask: null, detachedActiveTask: runningTask });
    const { rerender } = renderRuntimeHook('edit', undefined, vi.fn(), vi.fn(), 100, 'task-a');
    await advance();
    const callsInEdit = getTaskMock.mock.calls.length;

    rerender({ nextMode: 'running', nextTaskId: 'task-a', nextDetachedTaskId: 'task-a' });
    await advance();
    await advance(100);
    // 切到 running 后由常规任务轮询接手，两条轮询不会同时打同一个任务
    expect(getTaskMock.mock.calls.length).toBeLessThanOrEqual(callsInEdit + 2);
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
