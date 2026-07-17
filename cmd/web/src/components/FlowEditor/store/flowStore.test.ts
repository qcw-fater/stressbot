import { beforeEach, describe, expect, it } from 'vitest';
import { useFlowStore } from './flowStore';
import type { FlowJson } from '../codec/flowToJson';

const flow: FlowJson = {
  defaultDelayMs: 1000,
  nodes: {
    main: { type: 'sequence' as const, next: ['send'] },
    send: { type: 'action' as const, action: 'sendMessage' },
  },
  actions: {
    sendMessage: {
      pattern: 'tcpSend' as const,
      service: 'logic',
      route: 'Ping',
      c2sProto: 'demo.Ping',
    },
  },
  listens: {
    notices: { description: 'old' },
  },
};

describe('flowStore incremental derivation', () => {
  beforeEach(() => {
    useFlowStore.getState().loadFromTaskFlow(flow, {
      nodePositions: {
        main: { x: 10, y: 20 },
        send: { x: 100, y: 20 },
        __cb__notices: { x: 300, y: 20 },
      },
    });
  });

  it('patches only action nodes for ordinary action field updates', () => {
    const before = useFlowStore.getState();
    const mainNode = before.rfNodes.find((node) => node.id === 'main');
    const actionNode = before.rfNodes.find((node) => node.id === 'send');

    before.updateAction('sendMessage', { timeout: 2500 });

    const after = useFlowStore.getState();
    expect(after.rfEdges).toBe(before.rfEdges);
    expect(after.rfNodes.find((node) => node.id === 'main')).toBe(mainNode);
    expect(after.rfNodes.find((node) => node.id === 'send')).not.toBe(actionNode);
    expect(
      (after.rfNodes.find((node) => node.id === 'send')?.data as { action?: { timeout?: number } }).action?.timeout,
    ).toBe(2500);
  });

  it('patches only the edited node for presentation fields', () => {
    const before = useFlowStore.getState();
    const mainNode = before.rfNodes.find((node) => node.id === 'main');

    before.updateNode('send', { description: 'updated' });

    const after = useFlowStore.getState();
    expect(after.rfEdges).toBe(before.rfEdges);
    expect(after.rfNodes.find((node) => node.id === 'main')).toBe(mainNode);
    expect(
      (after.rfNodes.find((node) => node.id === 'send')?.data as { node?: { description?: string } }).node?.description,
    ).toBe('updated');
  });

  it('patches only the edited listen card for ordinary listen fields', () => {
    const before = useFlowStore.getState();
    const mainNode = before.rfNodes.find((node) => node.id === 'main');
    const card = before.rfNodes.find((node) => node.id === '__cb__notices');

    before.updateListen('notices', { description: 'updated' });

    const after = useFlowStore.getState();
    expect(after.rfEdges).toBe(before.rfEdges);
    expect(after.rfNodes.find((node) => node.id === 'main')).toBe(mainNode);
    expect(after.rfNodes.find((node) => node.id === '__cb__notices')).not.toBe(card);
  });

  it('rebuilds graph edges immediately for topology fields', () => {
    const beforeEdges = useFlowStore.getState().rfEdges;

    useFlowStore.getState().updateNode('main', { next: [] });

    const after = useFlowStore.getState();
    expect(after.rfEdges).not.toBe(beforeEdges);
    expect(after.rfEdges).toHaveLength(0);
  });

  it('keeps drag-time positions out of persisted layout until commit', () => {
    const beforeLayout = useFlowStore.getState().layout;

    useFlowStore.getState().onNodesChange([{
      id: 'send',
      type: 'position',
      position: { x: 180, y: 90 },
      dragging: true,
    }]);

    expect(useFlowStore.getState().layout).toBe(beforeLayout);
    expect(useFlowStore.getState().rfNodes.find((node) => node.id === 'send')?.position).toEqual({ x: 180, y: 90 });
  });

  it('commits final positions without mutating the previous layout', () => {
    const beforeLayout = useFlowStore.getState().layout;
    const beforePositions = beforeLayout.nodePositions;

    useFlowStore.getState().setNodePositions({ send: { x: 220, y: 110 } });

    const after = useFlowStore.getState();
    expect(after.layout).not.toBe(beforeLayout);
    expect(after.layout.nodePositions).not.toBe(beforePositions);
    expect(beforePositions.send).toEqual({ x: 100, y: 20 });
    expect(after.layout.nodePositions.send).toEqual({ x: 220, y: 110 });
    expect(after.rfNodes.find((node) => node.id === 'send')?.position).toEqual({ x: 220, y: 110 });
  });
});

describe('flowStore wait then references', () => {
  const waitFlow: FlowJson = {
    defaultDelayMs: 1000,
    nodes: {
      main: { type: 'sequence', next: ['wait'] },
      wait: { type: 'wait', waitMs: 10 },
      after: { type: 'action', action: 'after' },
    },
    actions: { after: { pattern: 'clearState', keys: ['done'] } },
    listens: {},
  };

  beforeEach(() => {
    useFlowStore.getState().loadFromTaskFlow(waitFlow);
  });

  it('rebuilds the derived edge when wait.then changes', () => {
    const beforeEdges = useFlowStore.getState().rfEdges;

    useFlowStore.getState().updateNode('wait', { then: 'after' });

    const after = useFlowStore.getState();
    expect(after.rfEdges).not.toBe(beforeEdges);
    expect(after.rfEdges).toContainEqual(expect.objectContaining({
      source: 'wait',
      target: 'after',
      sourceHandle: 'out',
      type: 'waitThen',
    }));
  });

  it('clears wait.then when the target node is removed', () => {
    useFlowStore.getState().updateNode('wait', { then: 'after' });

    useFlowStore.getState().removeNode('after');

    expect(useFlowStore.getState().nodes.wait.then).toBe('');
  });

  it('updates wait.then when the target node is renamed', () => {
    useFlowStore.getState().updateNode('wait', { then: 'after' });

    useFlowStore.getState().renameNode('after', 'renamed');

    expect(useFlowStore.getState().nodes.wait.then).toBe('renamed');
  });
});
