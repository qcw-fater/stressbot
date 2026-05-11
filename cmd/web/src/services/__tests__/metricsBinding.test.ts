/**
 * metricsBinding 单测：覆盖 action 节点 / callback 卡片 / 多节点共享同一 action / 缺失场景。
 */

import { describe, expect, it } from 'vitest';
import {
  buildNodeMetricsMap,
  classifyApdex,
  makeMetricsProvider,
  type FlowSlice,
} from '../metricsBinding';
import type { ActionMetric, StressSnapshot } from '@/types/api';

function action(name: string, overrides: Partial<ActionMetric> = {}): ActionMetric {
  return {
    name,
    sampleCount: 100,
    successCount: 95,
    failureCount: 5,
    timeoutCount: 0,
    skippedCount: 0,
    executing: 3,
    successRate: 0.95,
    apdex: 0.92,
    avgQps: 10,
    avgSendBytes: 100,
    avgRecvBytes: 200,
    timeoutAvgMs: 0,
    latency: { count: 95, minMs: 1, maxMs: 100, avgMs: 20, p50Ms: 18, p90Ms: 50, p95Ms: 70, p99Ms: 90 },
    ...overrides,
  };
}

function snapshot(actions: ActionMetric[]): StressSnapshot {
  return {
    timestamp: new Date().toISOString(),
    uptimeSeconds: 60,
    totalActions: actions.reduce((sum, a) => sum + a.sampleCount, 0),
    apdexT: 100,
    robots: { started: 100, running: 100, stopped: 0, errored: 0 },
    connections: { established: 100, failed: 0, dropped: 0 },
    bandwidth: { totalSendBytes: 0, totalRecvBytes: 0, sendMBps: 0, recvMBps: 0 },
    actions,
  };
}

describe('buildNodeMetricsMap', () => {
  it('action 节点按 action 名映射到 ActionMetric', () => {
    const flow: FlowSlice = {
      nodes: {
        n1: { type: 'action', action: 'CreateTeam' },
        n2: { type: 'action', action: 'SelectHero' },
      },
      listens: {},
    };
    const snap = snapshot([action('CreateTeam'), action('SelectHero')]);
    const map = buildNodeMetricsMap(snap, flow);
    expect(map.size).toBe(2);
    expect(map.get('n1')?.name).toBe('CreateTeam');
    expect(map.get('n2')?.name).toBe('SelectHero');
  });

  it('多个节点共享同一个 action 时都能拿到指标', () => {
    const flow: FlowSlice = {
      nodes: {
        n1: { type: 'action', action: 'SendHeartbeat' },
        n2: { type: 'action', action: 'SendHeartbeat' },
      },
      listens: {},
    };
    const m = action('SendHeartbeat');
    const map = buildNodeMetricsMap(snapshot([m]), flow);
    expect(map.get('n1')).toBe(m);
    expect(map.get('n2')).toBe(m);
  });

  it('callback 用 callback:<name> 命名匹配，nodeId 用 __cb__<name>', () => {
    const flow: FlowSlice = {
      nodes: {},
      listens: { OnBattleStart: { s2cProto: 'BattleStartS2C' } },
    };
    const snap = snapshot([action('callback:OnBattleStart')]);
    const map = buildNodeMetricsMap(snap, flow);
    expect(map.get('__cb__OnBattleStart')?.name).toBe('callback:OnBattleStart');
  });

  it('snapshot 缺失或没有匹配 action 时返回空 Map', () => {
    const flow: FlowSlice = { nodes: { n1: { type: 'action', action: 'X' } }, listens: {} };
    expect(buildNodeMetricsMap(undefined, flow).size).toBe(0);
    expect(buildNodeMetricsMap(snapshot([]), flow).size).toBe(0);
    expect(buildNodeMetricsMap(snapshot([action('Y')]), flow).size).toBe(0);
  });

  it('makeMetricsProvider 按 nodeId 返回 ActionMetric', () => {
    const flow: FlowSlice = { nodes: { n1: { type: 'action', action: 'A' } }, listens: {} };
    const map = buildNodeMetricsMap(snapshot([action('A', { executing: 7 })]), flow);
    const provider = makeMetricsProvider(map);
    expect(provider('n1')?.executing).toBe(7);
    expect(provider('nonexistent')).toBeUndefined();
  });
});

describe('classifyApdex', () => {
  it.each([
    [undefined, 'unknown'],
    [Number.NaN, 'unknown'],
    [1.0, 'excellent'],
    [0.94, 'excellent'],
    [0.93, 'good'],
    [0.85, 'good'],
    [0.84, 'fair'],
    [0.7, 'fair'],
    [0.69, 'poor'],
    [0.5, 'poor'],
    [0.49, 'danger'],
    [0, 'danger'],
  ])('classifyApdex(%s) === %s', (input, expected) => {
    expect(classifyApdex(input)).toBe(expected);
  });
});
