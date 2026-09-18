/**
 * 动作分类与主指标选取单测。
 *
 * 这是节点徽章、实时动作表、历史详情、历史对比、离线报告共用的口径，
 * 任一处自己另选直方图都会让同一个动作在两个面板上读出不同的 p99。
 */

import { describe, expect, it } from 'vitest';
import {
  PRIMARY_METRIC_LABEL,
  primaryMetricOfKind,
  resolveKind,
  selectPrimaryMetric,
  strongestKind,
  type ActionKindSource,
} from './actionKind';

const hist = (p99: number) => ({ maxMs: p99, avgMs: 1, p50Ms: 1, p95Ms: p99, p99Ms: p99 });

function action(over: Partial<ActionKindSource> = {}): ActionKindSource {
  return { avgSendBytes: 0, ...over };
}

describe('resolveKind', () => {
  it('优先用后端给的 kind', () => {
    expect(resolveKind(action({ kind: 'listen', rttSampleCount: 10 }))).toBe('listen');
  });

  it('老快照没有 kind 时按样本就地推断：RTT → 往返', () => {
    expect(resolveKind(action({ rttSampleCount: 3 }))).toBe('networked');
    expect(resolveKind(action({ rttApdexSampleCount: 3 }))).toBe('networked');
  });

  it('只有监听等待样本 → 监听；只有发送字节 → 发送；都没有 → 本地', () => {
    expect(resolveKind(action({ listenWaitSampleCount: 2 }))).toBe('listen');
    expect(resolveKind(action({ avgSendBytes: 64 }))).toBe('send');
    expect(resolveKind(action())).toBe('local');
  });
});

describe('selectPrimaryMetric', () => {
  it('往返类取 RTT，监听类取等待时长，发送/本地类取总耗时', () => {
    const full = {
      rtt: hist(90),
      rttSampleCount: 5,
      listenWait: hist(500),
      listenWaitSampleCount: 7,
      totalDuration: hist(120),
      totalDurationSampleCount: 9,
      avgSendBytes: 10,
    };
    expect(selectPrimaryMetric({ ...full, kind: 'networked' })).toEqual({
      histogram: full.rtt,
      sampleCount: 5,
      label: 'RTT',
    });
    expect(selectPrimaryMetric({ ...full, kind: 'listen' })).toEqual({
      histogram: full.listenWait,
      sampleCount: 7,
      label: '等待',
    });
    for (const kind of ['send', 'local'] as const) {
      expect(selectPrimaryMetric({ ...full, kind })).toEqual({
        histogram: full.totalDuration,
        sampleCount: 9,
        label: '执行',
      });
    }
  });

  it('标签表与选取结果一致（展示处直接用标签表时不会漂移）', () => {
    for (const kind of ['networked', 'listen', 'send', 'local'] as const) {
      expect(primaryMetricOfKind(action(), kind).label).toBe(PRIMARY_METRIC_LABEL[kind]);
    }
  });
});

describe('strongestKind', () => {
  it('多次运行取语义最强的类别，避免某次没留下 RTT 就换口径比较', () => {
    expect(strongestKind([action({ kind: 'send' }), action({ kind: 'networked' })])).toBe(
      'networked',
    );
    expect(strongestKind([action({ kind: 'local' }), action({ kind: 'listen' })])).toBe('listen');
    expect(strongestKind([action({ kind: 'local' }), action({ kind: 'send' })])).toBe('send');
  });

  it('空集合返回 undefined（调用方自己决定兜底类别）', () => {
    expect(strongestKind([])).toBeUndefined();
  });
});
