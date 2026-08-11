import { useEffect, useMemo, useRef } from 'react';
import { useQuery, useQueryClient, type UseQueryResult } from '@tanstack/react-query';
import type {
  AgentsListResponse,
  ClusterSystemSnapshot,
  StressAggregate,
  TaskDetail,
} from '@/types/api';
import { isAbortError } from './api';
import * as agentsApi from './agentsApi';
import * as metricsApi from './metricsApi';
import { agentListQueryOptions } from './queryOptions';
import { queryKeys } from './queryKeys';
import { pollingPolicy, useRuntimeStore, type RuntimeMode } from './runtimeStore';
import * as tasksApi from './tasksApi';

type HealthReporter = (source: string, failed: boolean) => void;

export interface UseRuntimeQueriesOptions {
  mode: RuntimeMode;
  taskId?: string;
  /** 测试可覆盖；生产使用 pollingPolicy 的 5s/10s。 */
  intervalMs?: number;
  reportConnectionHealth: HealthReporter;
  onTerminal?: (detail: TaskDetail) => void;
}

function useQueryHealthTracker<T>(
  source: string,
  enabled: boolean,
  identity: string,
  query: UseQueryResult<T, Error>,
  report: HealthReporter,
): void {
  const failCountRef = useRef(0);
  const lostRef = useRef(false);
  const lastErrorUpdateCountRef = useRef(0);
  const lastDataUpdatedAtRef = useRef(0);
  const reportRef = useRef(report);
  const queryRef = useRef(query);
  reportRef.current = report;
  queryRef.current = query;

  useEffect(() => {
    failCountRef.current = 0;
    lostRef.current = false;
    lastErrorUpdateCountRef.current = queryRef.current.errorUpdateCount;
    lastDataUpdatedAtRef.current = queryRef.current.dataUpdatedAt;
    reportRef.current(source, false);
  }, [enabled, identity, source]);

  useEffect(() => {
    if (!enabled || query.dataUpdatedAt === 0) return;
    if (query.dataUpdatedAt === lastDataUpdatedAtRef.current) return;
    lastDataUpdatedAtRef.current = query.dataUpdatedAt;
    failCountRef.current = 0;
    if (!lostRef.current) return;
    lostRef.current = false;
    reportRef.current(source, false);
  }, [enabled, query.dataUpdatedAt, source]);

  useEffect(() => {
    if (!enabled || query.errorUpdateCount <= lastErrorUpdateCountRef.current) return;
    const failures = query.errorUpdateCount - lastErrorUpdateCountRef.current;
    lastErrorUpdateCountRef.current = query.errorUpdateCount;
    if (isAbortError(query.error)) return;
    failCountRef.current += failures;
    if (lostRef.current || failCountRef.current < 3) return;
    lostRef.current = true;
    reportRef.current(source, true);
  }, [enabled, query.error, query.errorUpdateCount, source]);
}

function useApplyData<T>(
  query: UseQueryResult<T, Error>,
  identity: string,
  apply: (data: T) => void,
): void {
  const lastAppliedAtRef = useRef(-1);
  const lastIdentityRef = useRef('');
  const applyRef = useRef(apply);
  applyRef.current = apply;

  useEffect(() => {
    if (
      query.data === undefined ||
      (identity === lastIdentityRef.current && query.dataUpdatedAt === lastAppliedAtRef.current)
    ) {
      return;
    }
    lastIdentityRef.current = identity;
    lastAppliedAtRef.current = query.dataUpdatedAt;
    applyRef.current(query.data);
  }, [identity, query.data, query.dataUpdatedAt]);
}

/**
 * TanStack Query 负责请求去重、取消与调度；Zustand 只保留运行时状态机和趋势滑窗。
 * 四组查询都关闭内部 retry，确保“连续三次轮询失败”仍按三个观测窗口计算。
 */
export function useRuntimeQueries({
  mode,
  taskId,
  intervalMs: intervalOverride,
  reportConnectionHealth,
  onTerminal,
}: UseRuntimeQueriesOptions) {
  const client = useQueryClient();
  const policy = useMemo(() => pollingPolicy(mode), [mode]);
  const intervalMs = intervalOverride ?? policy.intervalMs;
  const taskEnabled = policy.pollActiveTask && !!taskId;
  const stressKey = useMemo(() => queryKeys.metrics.cluster(taskId ?? '__active__'), [taskId]);

  const taskQuery = useQuery({
    queryKey: queryKeys.tasks.detail(taskId ?? '__disabled__'),
    queryFn: ({ signal }) => tasksApi.getTask(taskId!, signal),
    enabled: taskEnabled,
    retry: false,
    refetchInterval: taskEnabled ? intervalMs : false,
  });
  const stressQuery = useQuery({
    queryKey: stressKey,
    queryFn: ({ signal }) => metricsApi.getClusterMetrics({}, signal),
    enabled: policy.pollStress,
    retry: false,
    refetchInterval: policy.pollStress ? intervalMs : false,
  });
  const systemQuery = useQuery({
    queryKey: queryKeys.metrics.system,
    queryFn: ({ signal }) => metricsApi.getClusterSystem(signal),
    enabled: policy.pollSystem,
    retry: false,
    refetchInterval: policy.pollSystem ? intervalMs : false,
  });
  const agentsQuery = useQuery({
    ...agentListQueryOptions(),
    queryFn: ({ signal }) => agentsApi.listAgents(signal),
    enabled: policy.pollAgents,
    retry: false,
    refetchInterval: policy.pollAgents ? intervalMs : false,
  });

  useQueryHealthTracker(
    'task',
    taskEnabled,
    taskId ?? '__disabled__',
    taskQuery,
    reportConnectionHealth,
  );
  useQueryHealthTracker(
    'stress',
    policy.pollStress,
    taskId ?? '__active__',
    stressQuery,
    reportConnectionHealth,
  );
  useQueryHealthTracker('system', policy.pollSystem, 'system', systemQuery, reportConnectionHealth);
  useQueryHealthTracker('agents', policy.pollAgents, 'agents', agentsQuery, reportConnectionHealth);

  useEffect(() => {
    if (!taskEnabled)
      void client.cancelQueries({
        queryKey: queryKeys.tasks.detail(taskId ?? '__disabled__'),
        exact: true,
      });
    if (!policy.pollStress) void client.cancelQueries({ queryKey: stressKey, exact: true });
    if (!policy.pollSystem)
      void client.cancelQueries({ queryKey: queryKeys.metrics.system, exact: true });
    if (!policy.pollAgents)
      void client.cancelQueries({ queryKey: queryKeys.agents.all, exact: true });
  }, [
    client,
    policy.pollAgents,
    policy.pollStress,
    policy.pollSystem,
    stressKey,
    taskEnabled,
    taskId,
  ]);

  const terminalRef = useRef<string | null>(null);
  const onTerminalRef = useRef(onTerminal);
  onTerminalRef.current = onTerminal;

  useApplyData<TaskDetail>(taskQuery, taskId ?? '__disabled__', (detail) => {
    const store = useRuntimeStore.getState();
    reportConnectionHealth('boot', false);
    store.setActiveTask(detail);
    if (detail.agentEvents?.length) store.appendAgentEvents(detail.agentEvents);
    if (detail.state !== 'stopped' && detail.state !== 'failed') return;
    store.onTaskFinished();
    if (terminalRef.current === detail.id) return;
    terminalRef.current = detail.id;
    onTerminalRef.current?.(detail);
  });

  useApplyData<StressAggregate>(stressQuery, taskId ?? '__active__', (aggregate) => {
    const store = useRuntimeStore.getState();
    reportConnectionHealth('boot', false);
    store.pushStress(aggregate.snapshot);
    store.setAgentHealth(
      aggregate.freshAgents,
      aggregate.totalAgents,
      aggregate.offlineAgents,
      aggregate.assignedAgents,
    );
  });

  useApplyData<ClusterSystemSnapshot>(systemQuery, 'system', (snapshot) => {
    reportConnectionHealth('boot', false);
    useRuntimeStore.getState().pushSystem(snapshot);
  });

  useApplyData<AgentsListResponse>(agentsQuery, 'agents', (response) => {
    reportConnectionHealth('boot', false);
    useRuntimeStore.getState().setAgents(response.items);
  });

  return { taskQuery, stressQuery, systemQuery, agentsQuery };
}
