import { describe, expect, it } from 'vitest';
import type { FlowNode } from '@/types/flow';
import { buildOnErrorBadges } from './onErrorBadges';

function actionNode(onError?: FlowNode['onError']): FlowNode {
  return { type: 'action', action: 'test', onError };
}

describe('buildOnErrorBadges', () => {
  it('无配置或只有隐藏配置时不显示标签', () => {
    expect(buildOnErrorBadges(actionNode())).toEqual([]);
    expect(buildOnErrorBadges(actionNode({
      handler: 'recover',
      ignoreCodes: [1001],
      retry: { retryDelayMs: 500 },
    }))).toEqual([]);
  });

  it('显示额外重试次数及其说明', () => {
    expect(buildOnErrorBadges(actionNode({ retry: { maxRetries: 3 } }))).toEqual([
      { label: 'retry:3', tooltip: '失败后最多额外重试 3 次', tone: 'warning' },
    ]);
  });

  it('隐藏默认 resume，显示非默认最终策略', () => {
    expect(buildOnErrorBadges(actionNode({ strategy: 'resume' }))).toEqual([]);
    expect(buildOnErrorBadges(actionNode({ strategy: 'skip' }))).toEqual([
      { label: 'skip', tooltip: '错误处理和重试结束后，跳过当前层级', tone: 'continue' },
    ]);
    expect(buildOnErrorBadges(actionNode({ strategy: 'abort' }))).toEqual([
      { label: 'abort', tooltip: '错误处理和重试结束后，中止当前流程', tone: 'error' },
    ]);
  });

  it('将重试和最终策略拆分为独立标签', () => {
    expect(buildOnErrorBadges(actionNode({
      retry: { maxRetries: 2, retryDelayMs: 100 },
      strategy: 'skip',
    }))).toEqual([
      { label: 'retry:2', tooltip: '失败后最多额外重试 2 次', tone: 'warning' },
      { label: 'skip', tooltip: '错误处理和重试结束后，跳过当前层级', tone: 'continue' },
    ]);
  });
});
