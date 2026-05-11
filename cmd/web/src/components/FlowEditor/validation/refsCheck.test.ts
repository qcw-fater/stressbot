/**
 * 校验规则单测：覆盖错误检测的核心场景。
 */

import { describe, it, expect } from 'vitest';
import { validateFlow } from './refsCheck';
import type { TaskFlow } from '@/types/flow';

describe('validateFlow', () => {
  it('NO_MAIN：缺少 main 节点报错', () => {
    const flow: TaskFlow = { defaultDelayMs: 1000, nodes: {}, actions: {}, listens: {} };
    const r = validateFlow(flow);
    expect(r.errors.find((e) => e.code === 'NO_MAIN')).toBeTruthy();
  });

  it('NODE_REF_NOT_FOUND：sequence next 指向不存在节点', () => {
    const flow: TaskFlow = {
      defaultDelayMs: 1000,
      nodes: { main: { type: 'sequence', next: ['ghost'] } },
      actions: {},
      listens: {},
    };
    const r = validateFlow(flow);
    expect(r.errors.find((e) => e.code === 'NODE_REF_NOT_FOUND')).toBeTruthy();
  });

  it('LOOP_BODY_MISSING：loop 缺少 body 报错', () => {
    const flow: TaskFlow = {
      defaultDelayMs: 1000,
      nodes: { main: { type: 'loop' } },
      actions: {},
      listens: {},
    };
    const r = validateFlow(flow);
    expect(r.errors.find((e) => e.code === 'LOOP_BODY_MISSING')).toBeTruthy();
  });

  it('BOOLEAN_NO_CONDITION：boolean 缺少 condition 报错', () => {
    const flow: TaskFlow = {
      defaultDelayMs: 1000,
      nodes: { main: { type: 'boolean', trueNext: 'x' }, x: { type: 'sequence' } },
      actions: {},
      listens: {},
    };
    const r = validateFlow(flow);
    expect(r.errors.find((e) => e.code === 'BOOLEAN_NO_CONDITION')).toBeTruthy();
  });

  it('WEIGHTED_ALL_ZERO：所有权重为 0 报错', () => {
    const flow: TaskFlow = {
      defaultDelayMs: 1000,
      nodes: {
        main: { type: 'weighted', options: [{ node: 'x', weight: 0 }] },
        x: { type: 'sequence' },
      },
      actions: {},
      listens: {},
    };
    const r = validateFlow(flow);
    expect(r.errors.find((e) => e.code === 'WEIGHTED_ALL_ZERO')).toBeTruthy();
  });

  it('ACTION_REF_NOT_FOUND：action 节点引用不存在的 ActionDef', () => {
    const flow: TaskFlow = {
      defaultDelayMs: 1000,
      nodes: { main: { type: 'action', action: 'NotExist' } },
      actions: {},
      listens: {},
    };
    const r = validateFlow(flow);
    expect(r.errors.find((e) => e.code === 'ACTION_REF_NOT_FOUND')).toBeTruthy();
  });

  it('LISTEN_CB_NOT_FOUND：listenCallbacks 引用不存在 callback', () => {
    const flow: TaskFlow = {
      defaultDelayMs: 1000,
      nodes: {
        main: {
          type: 'action',
          action: 'A1',
          listenCallbacks: [{ route: { cmd: 1, act: 1 }, server: 'tcp:x', callback: 'ghost' }],
        },
      },
      actions: { A1: { pattern: 'tcpSend', service: 'x' } },
      listens: {},
    };
    const r = validateFlow(flow);
    expect(r.errors.find((e) => e.code === 'LISTEN_CB_NOT_FOUND')).toBeTruthy();
  });

  it('CALLBACK_ORPHAN：未被引用的 callback 报 warning', () => {
    const flow: TaskFlow = {
      defaultDelayMs: 1000,
      nodes: { main: { type: 'sequence' } },
      actions: {},
      listens: { orphan: {} },
    };
    const r = validateFlow(flow);
    expect(r.warnings.find((e) => e.code === 'LISTEN_ORPHAN')).toBeTruthy();
  });

  it('LUA_NO_SCRIPT：pattern=lua 但没有 script', () => {
    const flow: TaskFlow = {
      defaultDelayMs: 1000,
      nodes: { main: { type: 'action', action: 'A1' } },
      actions: { A1: { pattern: 'lua' } },
      listens: {},
    };
    const r = validateFlow(flow);
    expect(r.errors.find((e) => e.code === 'LUA_NO_SCRIPT')).toBeTruthy();
  });

  it('ACTION_ORPHAN：未被任何节点引用的 action 报 warning 而非 error', () => {
    const flow: TaskFlow = {
      defaultDelayMs: 1000,
      nodes: { main: { type: 'sequence' } },
      actions: { orphan: { pattern: 'lua' } },
      listens: {},
    };
    const r = validateFlow(flow);
    // orphaned action 不应触发 LUA_NO_SCRIPT error，只报 ACTION_ORPHAN warning
    expect(r.errors.find((e) => e.code === 'LUA_NO_SCRIPT')).toBeFalsy();
    expect(r.warnings.find((e) => e.code === 'ACTION_ORPHAN')).toBeTruthy();
  });

  it('CALLBACK_LUA_NO_SCRIPT：lua callback 缺少 script', () => {
    const flow: TaskFlow = {
      defaultDelayMs: 1000,
      nodes: {
        main: {
          type: 'action',
          action: 'A1',
          listenCallbacks: [{ route: { cmd: 1, act: 1 }, server: 'tcp:x', callback: 'cb1' }],
        },
      },
      actions: { A1: { pattern: 'tcpSend', service: 'x' } },
      listens: { cb1: { script: '' } },
    };
    const r = validateFlow(flow);
    expect(r.errors.find((e) => e.code === 'LISTEN_LUA_NO_SCRIPT')).toBeTruthy();
  });

  it('完整最小合法 flow 0 错误', () => {
    const flow: TaskFlow = {
      defaultDelayMs: 1000,
      nodes: {
        main: { type: 'sequence', next: ['act1'] },
        act1: { type: 'action', action: 'A1' },
      },
      actions: { A1: { pattern: 'tcpSend', service: 'logic', c2sProto: 'X.Foo' } },
      listens: {},
    };
    const r = validateFlow(flow);
    expect(r.errors.length).toBe(0);
  });
});
