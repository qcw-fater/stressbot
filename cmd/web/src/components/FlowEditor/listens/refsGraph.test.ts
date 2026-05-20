/**
 * refsGraph 单测：覆盖反查、孤儿、悬空、重复注册四个分支。
 */

import { describe, it, expect } from 'vitest';
import { buildRefsGraph, routeKey } from './refsGraph';
import type { TaskFlow } from '@/types/flow';

const baseFlow: TaskFlow = {
  defaultDelayMs: 1000,
  nodes: {
    main: { type: 'sequence', next: ['act1', 'act2'] },
    act1: {
      type: 'action',
      action: 'A1',
      listenRefs: [
        { route: { cmd: 1, act: 1 }, server: 'tcp:logic', listen: 'cbA' },
        { route: { cmd: 2, act: 2 }, server: 'tcp:logic', listen: null },
      ],
    },
    act2: {
      type: 'action',
      action: 'A2',
      listenRefs: [
        { route: { cmd: 1, act: 1 }, server: 'tcp:logic', listen: 'cbB' }, // 与 act1 同 route 不同 cb → 重复注册
        { route: { cmd: 9, act: 9 }, server: 'tcp:logic', listen: 'ghost' }, // 引用不存在 → 悬空
      ],
    },
  },
  actions: {
    A1: { pattern: 'tcpSend', service: 'logic' },
    A2: { pattern: 'tcpSend', service: 'logic' },
  },
  listens: {
    cbA: {},
    cbB: { script: 'x.lua' },
    cbOrphan: {}, // 没有 action 引用 → 孤儿
  },
};

describe('refsGraph', () => {
  it('listenToRefs 反查正确', () => {
    const g = buildRefsGraph(baseFlow);
    expect(g.listenToRefs.get('cbA')?.length).toBe(1);
    expect(g.listenToRefs.get('cbA')?.[0].nodeId).toBe('act1');
    expect(g.listenToRefs.get('cbB')?.[0].nodeId).toBe('act2');
  });

  it('refCount 计数正确（cbA=1, cbB=1, ghost=1, cbOrphan 缺席）', () => {
    const g = buildRefsGraph(baseFlow);
    expect(g.refCount.get('cbA')).toBe(1);
    expect(g.refCount.get('cbB')).toBe(1);
    expect(g.refCount.has('cbOrphan')).toBe(false);
  });

  it('danglingRefs 包含 ghost 引用', () => {
    const g = buildRefsGraph(baseFlow);
    expect(g.danglingRefs.length).toBe(1);
    expect(g.danglingRefs[0].ref.listen).toBe('ghost');
  });

  it('duplicateRegisters 检测同 server+route 不同 listen', () => {
    const g = buildRefsGraph(baseFlow);
    expect(g.duplicateRegisters.length).toBe(1);
    const dup = g.duplicateRegisters[0];
    expect(dup.server).toBe('tcp:logic');
    expect(dup.refs.length).toBe(2);
  });

  it('null listen 不参与 refCount，但仍参与 routeKey 分组', () => {
    const flow: TaskFlow = {
      ...baseFlow,
      nodes: {
        main: { type: 'sequence', next: ['n1'] },
        n1: {
          type: 'action',
          action: 'A1',
          listenRefs: [{ route: { cmd: 5, act: 5 }, server: 'udp:battle', listen: null }],
        },
      },
    };
    const g = buildRefsGraph(flow);
    expect(g.refCount.size).toBe(0);
    expect(g.danglingRefs.length).toBe(0);
  });

  it('routeKey 在键序不同时仍稳定', () => {
    expect(routeKey({ cmd: 1, act: 2 })).toBe(routeKey({ act: 2, cmd: 1 }));
    expect(routeKey({ cmd: 1, act: 2 })).not.toBe(routeKey({ cmd: 1, act: 3 }));
  });
});
