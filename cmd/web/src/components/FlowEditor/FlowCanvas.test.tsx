import { act, render } from '@testing-library/react';
import type * as XYFlow from '@xyflow/react';
import type { PropsWithChildren } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  reactFlowRenderCount: 0,
  setCenter: vi.fn(),
}));

vi.mock('@xyflow/react', async (importOriginal) => {
  const actual = await importOriginal<typeof XYFlow>();
  return {
    ...actual,
    Background: () => null,
    Controls: () => null,
    MiniMap: () => null,
    ReactFlowProvider: ({ children }: PropsWithChildren) => <>{children}</>,
    ReactFlow: ({ children }: PropsWithChildren) => {
      mocks.reactFlowRenderCount += 1;
      return <>{children}</>;
    },
    useReactFlow: () => ({
      screenToFlowPosition: ({ x, y }: { x: number; y: number }) => ({ x, y }),
      setCenter: mocks.setCenter,
    }),
  };
});

vi.mock('antd', () => ({
  App: {
    useApp: () => ({
      message: {
        error: vi.fn(),
        success: vi.fn(),
      },
    }),
  },
}));

vi.mock('./nodes/registry', () => ({ nodeTypes: {} }));
vi.mock('./edges/registry', () => ({ edgeTypes: {} }));
vi.mock('./CanvasContextMenu', () => ({
  CanvasContextMenu: () => null,
  QUICK_NODE_TYPES: [],
}));
vi.mock('./library/templateStore', () => ({
  saveActionTemplate: vi.fn(),
  saveListenTemplate: vi.fn(),
}));

import { FlowCanvas } from './FlowCanvas';
import { useFlowStore } from './store/flowStore';

describe('FlowCanvas subscriptions', () => {
  beforeEach(() => {
    mocks.reactFlowRenderCount = 0;
    mocks.setCenter.mockReset();
    useFlowStore.setState({
      nodes: { main: { type: 'sequence', next: [] } },
      rfNodes: [],
      rfEdges: [],
      needsFitView: false,
    });
  });

  it('does not render the canvas when only the semantic node map reference changes', () => {
    const view = render(<FlowCanvas />);
    const initialRenderCount = mocks.reactFlowRenderCount;

    act(() => {
      useFlowStore.setState((state) => ({
        nodes: {
          ...state.nodes,
          main: { ...state.nodes.main, description: '只改变业务节点数据' },
        },
      }));
    });

    expect(mocks.reactFlowRenderCount).toBe(initialRenderCount);
    view.unmount();
  });
});
