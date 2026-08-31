import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { startAutoPersist } from './persistDraft';
import { useFlowStore } from './flowStore';

beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear();
  useFlowStore.getState().reset();
});

afterEach(() => {
  vi.useRealTimers();
  localStorage.clear();
});

describe('startAutoPersist', () => {
  it('does not serialize a draft for visual-only React Flow updates', () => {
    const stop = startAutoPersist();
    useFlowStore.setState((state) => ({ rfNodes: [...state.rfNodes] }));

    vi.advanceTimersByTime(301);

    expect(localStorage.getItem('stressbot-editor-draft-flow-v2')).toBeNull();
    expect(localStorage.getItem('stressbot-editor-draft-layout-v2')).toBeNull();
    stop();
  });

  it('persists business flow changes after the debounce window', () => {
    const stop = startAutoPersist();
    useFlowStore.getState().addNode('main', { type: 'wait', waitMs: 100 });

    vi.advanceTimersByTime(301);

    const flow = JSON.parse(localStorage.getItem('stressbot-editor-draft-flow-v2') ?? '{}') as {
      nodes?: Record<string, unknown>;
    };
    expect(flow.nodes).toHaveProperty('main');
    stop();
  });
});
