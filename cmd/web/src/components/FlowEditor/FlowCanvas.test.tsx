import { act, fireEvent, render } from '@testing-library/react';
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
import { useEditorStore } from './store/editorStore';
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
    useEditorStore.setState({ clipboard: null });
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

  it('does not paste a node when Ctrl+V originates outside the canvas', () => {
    useEditorStore.setState({
      clipboard: {
        kind: 'node',
        nodeId: 'copied',
        node: { type: 'wait', waitMs: 100 },
      },
    });
    const view = render(<FlowCanvas />);
    const canvas = view.container.firstElementChild as HTMLElement;
    fireEvent.mouseMove(canvas, { clientX: 120, clientY: 80 });

    const editorSurface = document.createElement('div');
    document.body.appendChild(editorSurface);
    fireEvent.keyDown(editorSurface, { key: 'v', ctrlKey: true });

    expect(Object.keys(useFlowStore.getState().nodes)).toEqual(['main']);
    editorSurface.remove();
    view.unmount();
  });

  it('focuses the canvas when the user presses it', () => {
    const view = render(<FlowCanvas />);
    const canvas = view.container.firstElementChild as HTMLElement;

    fireEvent.mouseDown(canvas);

    expect(document.activeElement).toBe(canvas);
    view.unmount();
  });

  it('leaves text paste to the browser even when a node was copied', () => {
    useEditorStore.setState({
      clipboard: {
        kind: 'node',
        nodeId: 'copied',
        node: { type: 'wait', waitMs: 100 },
      },
    });
    const view = render(<FlowCanvas />);
    const input = document.createElement('input');
    document.body.appendChild(input);
    input.focus();
    const event = new KeyboardEvent('keydown', {
      key: 'v',
      ctrlKey: true,
      bubbles: true,
      cancelable: true,
    });

    input.dispatchEvent(event);

    expect(event.defaultPrevented).toBe(false);
    expect(Object.keys(useFlowStore.getState().nodes)).toEqual(['main']);
    input.remove();
    view.unmount();
  });

  it('pastes the structured clipboard when Ctrl+V originates from the canvas', () => {
    useEditorStore.setState({
      clipboard: {
        kind: 'node',
        nodeId: 'copied',
        node: { type: 'wait', waitMs: 100 },
      },
    });
    const view = render(<FlowCanvas />);
    const canvas = view.container.firstElementChild as HTMLElement;
    fireEvent.mouseMove(canvas, { clientX: 120, clientY: 80 });
    fireEvent.mouseDown(canvas);
    const event = new KeyboardEvent('keydown', {
      key: 'v',
      ctrlKey: true,
      bubbles: true,
      cancelable: true,
    });

    canvas.dispatchEvent(event);

    expect(event.defaultPrevented).toBe(true);
    expect(Object.keys(useFlowStore.getState().nodes)).toHaveLength(2);
    view.unmount();
  });
});
