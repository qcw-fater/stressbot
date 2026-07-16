import { act, render } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { ActionMetric } from '@/types/api';
import { useMetricsStore, useNodeMetrics } from './MetricsBadge';

const metric = (successCount: number) => ({ successCount }) as ActionMetric;

describe('node metric selectors', () => {
  beforeEach(() => {
    useMetricsStore.getState().setMetrics(undefined);
  });

  it('does not rerender a node whose selected metric reference is unchanged', () => {
    const renders = { a: vi.fn(), b: vi.fn() };
    const metricA = metric(1);
    const metricB = metric(2);
    useMetricsStore.getState().setMetrics(new Map([['a', metricA], ['b', metricB]]));

    function Probe({ nodeId }: { nodeId: 'a' | 'b' }) {
      const selected = useNodeMetrics(nodeId);
      renders[nodeId](selected);
      return null;
    }

    render(<><Probe nodeId="a" /><Probe nodeId="b" /></>);
    expect(renders.a).toHaveBeenCalledTimes(1);
    expect(renders.b).toHaveBeenCalledTimes(1);

    act(() => {
      useMetricsStore.getState().setMetrics(new Map([['a', metric(3)], ['b', metricB]]));
    });

    expect(renders.a).toHaveBeenCalledTimes(2);
    expect(renders.b).toHaveBeenCalledTimes(1);
  });
});
