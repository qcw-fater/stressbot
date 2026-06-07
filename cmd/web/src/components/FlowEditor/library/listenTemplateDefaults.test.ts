import { describe, expect, it } from 'vitest';
import type { FlowNode } from '@/types/flow';
import { cloneListenDefaultRef, inferListenDefaultRef } from './listenTemplateDefaults';

describe('listenTemplateDefaults', () => {
  it('无引用时不返回默认注册', () => {
    const nodes: Record<string, FlowNode> = {
      A: { type: 'action', action: 'A' },
    };

    expect(inferListenDefaultRef(nodes, 'listenA')).toEqual({ ambiguous: false });
  });

  it('单引用时返回 server 和 route', () => {
    const nodes: Record<string, FlowNode> = {
      A: {
        type: 'action',
        action: 'A',
        listenRefs: [{ server: 'tcp:logic', route: { cmd: 1, act: 2 }, listen: 'listenA' }],
      },
    };

    expect(inferListenDefaultRef(nodes, 'listenA')).toEqual({
      ambiguous: false,
      defaultRef: { server: 'tcp:logic', route: { cmd: 1, act: 2 } },
    });
  });

  it('多条相同引用不标记歧义', () => {
    const nodes: Record<string, FlowNode> = {
      A: {
        type: 'action',
        action: 'A',
        listenRefs: [{ server: 'tcp:logic', route: { act: 2, cmd: 1 }, listen: 'listenA' }],
      },
      B: {
        type: 'action',
        action: 'B',
        listenRefs: [{ server: 'tcp:logic', route: { cmd: 1, act: 2 }, listen: 'listenA' }],
      },
    };

    expect(inferListenDefaultRef(nodes, 'listenA')).toEqual({
      ambiguous: false,
      defaultRef: { server: 'tcp:logic', route: { act: 2, cmd: 1 } },
    });
  });

  it('多条不同引用返回第一条并标记歧义', () => {
    const nodes: Record<string, FlowNode> = {
      A: {
        type: 'action',
        action: 'A',
        listenRefs: [{ server: 'tcp:logic', route: { cmd: 1, act: 2 }, listen: 'listenA' }],
      },
      B: {
        type: 'action',
        action: 'B',
        listenRefs: [{ server: 'udp:battle', route: { cmd: 3, act: 4 }, listen: 'listenA' }],
      },
    };

    expect(inferListenDefaultRef(nodes, 'listenA')).toEqual({
      ambiguous: true,
      defaultRef: { server: 'tcp:logic', route: { cmd: 1, act: 2 } },
    });
  });

  it('返回深拷贝', () => {
    const route = { cmd: 1, nested: { act: 2 } };
    const cloned = cloneListenDefaultRef({ server: 'tcp:logic', route });

    expect(cloned).toEqual({ server: 'tcp:logic', route });
    expect(cloned?.route).not.toBe(route);
  });
});
