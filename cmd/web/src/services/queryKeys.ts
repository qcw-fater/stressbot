export const queryKeys = {
  capabilities: ['capabilities'] as const,
  flows: {
    all: ['flows'] as const,
    detail: (id: string) => ['flows', 'detail', id] as const,
  },
  tasks: {
    all: ['tasks'] as const,
    detail: (id: string) => ['tasks', 'detail', id] as const,
  },
  agents: {
    all: ['agents'] as const,
    metricsRoot: ['agents', 'metrics'] as const,
    metrics: (taskId: string) => ['agents', 'metrics', taskId] as const,
  },
  metrics: {
    cluster: (taskId: string) => ['metrics', 'cluster', taskId] as const,
    system: ['metrics', 'system'] as const,
  },
} as const;
