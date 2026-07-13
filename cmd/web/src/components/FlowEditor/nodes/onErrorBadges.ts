import type { FlowNode } from '@/types/flow';

export interface OnErrorBadge {
  label: string;
  tooltip: string;
  tone: 'warning' | 'continue' | 'error';
}

export function buildOnErrorBadges(node: FlowNode): OnErrorBadge[] {
  const badges: OnErrorBadge[] = [];
  const maxRetries = node.onError?.retry?.maxRetries ?? 0;
  if (maxRetries > 0) {
    badges.push({
      label: `retry:${maxRetries}`,
      tooltip: `失败后最多额外重试 ${maxRetries} 次`,
      tone: 'warning',
    });
  }

  const strategy = node.onError?.strategy;
  if (strategy === 'skip') {
    badges.push({ label: 'skip', tooltip: '错误处理和重试结束后，跳过当前层级', tone: 'continue' });
  } else if (strategy === 'abort') {
    badges.push({ label: 'abort', tooltip: '错误处理和重试结束后，中止当前流程', tone: 'error' });
  }

  return badges;
}
