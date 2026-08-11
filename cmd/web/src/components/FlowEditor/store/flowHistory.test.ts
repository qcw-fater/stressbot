import { beforeEach, describe, expect, it } from 'vitest';

import type { FlowJson } from '../codec/flowToJson';
import { useFlowStore } from './flowStore';
import { getHistorySize, redo, undo } from './flowHistory';

const flow: FlowJson = {
  defaultDelayMs: 1000,
  nodes: {
    main: { type: 'sequence', next: ['send'] },
    send: { type: 'action', action: 'send' },
  },
  actions: {
    send: { pattern: 'tcpSend', service: 'logic', route: 'Ping', c2sProto: 'demo.Ping' },
  },
  listens: {},
};

describe('flow history', () => {
  beforeEach(() => {
    useFlowStore.getState().loadFromTaskFlow(flow, {
      nodePositions: {
        main: { x: 10, y: 20 },
        send: { x: 100, y: 20 },
      },
    });
  });

  it('undoes and redoes business state, then synchronizes derived graph state', () => {
    useFlowStore.getState().updateNode('main', { next: [] });
    expect(useFlowStore.getState().rfEdges).toHaveLength(0);

    expect(undo()).toBe(true);
    expect(useFlowStore.getState().nodes.main.next).toEqual(['send']);
    expect(useFlowStore.getState().rfEdges).toContainEqual(
      expect.objectContaining({
        source: 'main',
        target: 'send',
      }),
    );

    expect(redo()).toBe(true);
    expect(useFlowStore.getState().nodes.main.next).toEqual([]);
    expect(useFlowStore.getState().rfEdges).toHaveLength(0);
  });

  it('does not track visual-only changes', () => {
    useFlowStore.getState().setNodePositions({ send: { x: 220, y: 110 } });

    expect(getHistorySize()).toEqual({ past: 0, future: 0 });
    expect(undo()).toBe(false);
  });

  it('clears history when a flow is loaded or reset', () => {
    useFlowStore.getState().updateAction('send', { timeout: 2500 });
    expect(getHistorySize().past).toBe(1);

    useFlowStore.getState().loadFromTaskFlow({ ...flow, defaultDelayMs: 2000 });
    expect(getHistorySize()).toEqual({ past: 0, future: 0 });

    useFlowStore.getState().updateNode('main', { description: 'changed' });
    expect(getHistorySize().past).toBe(1);
    useFlowStore.getState().reset();
    expect(getHistorySize()).toEqual({ past: 0, future: 0 });
  });

  it('caps retained history at fifty entries', () => {
    for (let timeout = 1; timeout <= 60; timeout++) {
      useFlowStore.getState().updateAction('send', { timeout });
    }

    expect(getHistorySize()).toEqual({ past: 50, future: 0 });
  });

  it('clears redo history after a new business edit', () => {
    useFlowStore.getState().updateAction('send', { timeout: 1000 });
    expect(undo()).toBe(true);
    expect(getHistorySize().future).toBe(1);

    useFlowStore.getState().updateAction('send', { timeout: 2000 });

    expect(getHistorySize().future).toBe(0);
    expect(redo()).toBe(false);
  });
});
