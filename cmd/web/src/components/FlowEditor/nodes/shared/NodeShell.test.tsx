import { act, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useEditorStore } from '../../store/editorStore';
import { useFlowStore } from '../../store/flowStore';
import { NodeShell } from './NodeShell';

const metricsBadgeRender = vi.hoisted(() => vi.fn());
vi.mock('./MetricsBadge', () => ({
  useNodeMetrics: () => undefined,
  getNodeApdexLevel: () => 'unknown',
  MetricsBadge: () => {
    metricsBadgeRender();
    return null;
  },
}));

afterEach(() => {
  useEditorStore.setState({
    hoveredListen: null,
    edgeHighlightNodeIds: [],
    edgeHighlightColor: null,
  });
  useFlowStore.setState({ nodesByListen: {} });
  metricsBadgeRender.mockClear();
});

describe('NodeShell highlight subscriptions', () => {
  it('does not rerender a node when another node is highlighted by a listen', () => {
    useFlowStore.setState({ nodesByListen: { push: ['node-a'] } });
    render(<NodeShell nodeId="node-b" nodeType="wait" title="node-b" />);
    const rendersBeforeHover = metricsBadgeRender.mock.calls.length;

    act(() => useEditorStore.getState().setHoveredListen('push'));

    expect(metricsBadgeRender).toHaveBeenCalledTimes(rendersBeforeHover);
  });

  it('updates the node whose listen highlight state actually changes', () => {
    useFlowStore.setState({ nodesByListen: { push: ['node-a'] } });
    const { container } = render(<NodeShell nodeId="node-a" nodeType="wait" title="node-a" />);

    act(() => useEditorStore.getState().setHoveredListen('push'));

    expect(container.querySelector('.node-shell')?.classList.contains('highlight-by-listen')).toBe(
      true,
    );
  });

  it('does not rerender unrelated nodes when the selected edge color changes', () => {
    render(<NodeShell nodeId="node-b" nodeType="wait" title="node-b" />);
    const rendersBeforeHighlight = metricsBadgeRender.mock.calls.length;

    act(() => {
      useEditorStore.getState().setEdgeHighlightNodes(['node-a']);
      useEditorStore.getState().setEdgeHighlightColor('#ff0000');
    });

    expect(metricsBadgeRender).toHaveBeenCalledTimes(rendersBeforeHighlight);
  });
});
