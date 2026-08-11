import { queryOptions } from '@tanstack/react-query';
import { getCapabilities } from './capabilitiesApi';
import { listFlowTemplates } from './flowsApi';
import * as agentsApi from './agentsApi';
import * as metricsApi from './metricsApi';
import { queryKeys } from './queryKeys';

export function capabilitiesQueryOptions() {
  return queryOptions({
    queryKey: queryKeys.capabilities,
    queryFn: ({ signal }) => getCapabilities({ signal }),
    staleTime: 60_000,
  });
}

export function flowListQueryOptions() {
  return queryOptions({
    queryKey: queryKeys.flows.all,
    queryFn: ({ signal }) => listFlowTemplates(signal),
    staleTime: 30_000,
  });
}

export function agentListQueryOptions() {
  return queryOptions({
    queryKey: queryKeys.agents.all,
    queryFn: ({ signal }) => agentsApi.listAgents(signal),
  });
}

export function perAgentMetricsQueryOptions(taskId?: string) {
  return queryOptions({
    queryKey: queryKeys.agents.metrics(taskId ?? '__active__'),
    queryFn: ({ signal }) => metricsApi.getPerAgentMetrics(taskId ? { taskId } : {}, signal),
    refetchInterval: 5_000,
  });
}
