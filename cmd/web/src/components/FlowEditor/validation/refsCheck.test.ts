/**
 * 校验规则单测：覆盖错误检测的核心场景。
 */

import { describe, it, expect } from 'vitest';
import { validateFlow } from './refsCheck';
import type { TaskFlow } from '@/types/flow';

/** 造一个最小合法 flow，用于叠加错误场景 */
function baseFlow(overrides: Partial<TaskFlow> = {}): TaskFlow {
  return {
    defaultDelayMs: 1000,
    nodes: { main: { type: 'sequence', next: ['act1'] }, act1: { type: 'action', action: 'A1' } },
    actions: { A1: { pattern: 'tcpSend', service: 'logic', route: { cmd: 1, act: 1 }, c2sProto: 'X.Foo' } },
    listens: {},
    ...overrides,
  };
}

describe('validateFlow', () => {
  // ── 节点级 ──

  it('NO_MAIN：缺少 main 节点报错', () => {
    const r = validateFlow({ defaultDelayMs: 1000, nodes: {}, actions: {}, listens: {} });
    expect(r.errors.find((e) => e.code === 'NO_MAIN')).toBeTruthy();
  });

  it('NODE_REF_NOT_FOUND：sequence next 指向不存在节点', () => {
    const r = validateFlow(baseFlow({ nodes: { main: { type: 'sequence', next: ['ghost'] } } }));
    expect(r.errors.find((e) => e.code === 'NODE_REF_NOT_FOUND')).toBeTruthy();
  });

  it('NODE_UNKNOWN_TYPE：未知节点类型报错', () => {
    const r = validateFlow(baseFlow({ nodes: { main: { type: 'sequence', next: ['x'] }, x: { type: 'unknownType' as 'sequence' } } }));
    expect(r.errors.find((e) => e.code === 'NODE_UNKNOWN_TYPE')).toBeTruthy();
  });

  it('NODE_ORPHAN：不可达节点报 warning', () => {
    const r = validateFlow(baseFlow({ nodes: { main: { type: 'sequence' }, orphan: { type: 'sequence' } } }));
    expect(r.warnings.find((e) => e.code === 'NODE_ORPHAN')).toBeTruthy();
  });

  it('LOOP_BODY_MISSING：loop 缺少 body 报错', () => {
    const r = validateFlow(baseFlow({ nodes: { main: { type: 'loop' } } }));
    expect(r.errors.find((e) => e.code === 'LOOP_BODY_MISSING')).toBeTruthy();
  });

  it('BOOLEAN_NO_CONDITION：boolean 缺少 condition 报错', () => {
    const r = validateFlow(baseFlow({ nodes: { main: { type: 'boolean', trueNext: 'x' }, x: { type: 'sequence' } } }));
    expect(r.errors.find((e) => e.code === 'BOOLEAN_NO_CONDITION')).toBeTruthy();
  });

  it('WEIGHTED_ALL_ZERO：所有权重为 0 报错', () => {
    const r = validateFlow(baseFlow({ nodes: { main: { type: 'weighted', options: [{ node: 'x', weight: 0 }] }, x: { type: 'sequence' } } }));
    expect(r.errors.find((e) => e.code === 'WEIGHTED_ALL_ZERO')).toBeTruthy();
  });

  // ── ListenRef ──

  it('LISTEN_SERVER_FORMAT：server 格式错误报错', () => {
    const r = validateFlow(baseFlow({
      nodes: { main: { type: 'action', action: 'A1', listenRefs: [{ server: 'badFormat', listen: null, route: {} }] } },
    }));
    expect(r.errors.find((e) => e.code === 'LISTEN_SERVER_FORMAT')).toBeTruthy();
  });

  it('LISTEN_CB_NOT_FOUND：listenRefs 引用不存在 listen', () => {
    const r = validateFlow(baseFlow({
      nodes: { main: { type: 'action', action: 'A1', listenRefs: [{ server: 'tcp:x', listen: 'ghost', route: {} }] } },
    }));
    expect(r.errors.find((e) => e.code === 'LISTEN_CB_NOT_FOUND')).toBeTruthy();
  });

  // ── Action 级 ──

  it('ACTION_REF_NOT_FOUND：action 节点引用不存在的 ActionDef', () => {
    const r = validateFlow(baseFlow({ actions: {} }));
    expect(r.errors.find((e) => e.code === 'ACTION_REF_NOT_FOUND')).toBeTruthy();
  });

  it('ACTION_UNKNOWN_PATTERN：未知 pattern 报错', () => {
    const r = validateFlow(baseFlow({ actions: { A1: { pattern: 'badPattern' as 'tcpSend' } } }));
    expect(r.errors.find((e) => e.code === 'ACTION_UNKNOWN_PATTERN')).toBeTruthy();
  });

  it('ACTION_NO_ROUTE：缺少 route 报错', () => {
    const r = validateFlow(baseFlow({ actions: { A1: { pattern: 'tcpSend', service: 'x', c2sProto: 'X.Foo' } } }));
    expect(r.errors.find((e) => e.code === 'ACTION_NO_ROUTE')).toBeTruthy();
  });

  it('ACTION_NO_ADDRESS：tcpConnect 缺 address 报错', () => {
    const r = validateFlow(baseFlow({ actions: { A1: { pattern: 'tcpConnect', service: 'x' } } }));
    expect(r.errors.find((e) => e.code === 'ACTION_NO_ADDRESS')).toBeTruthy();
  });

  it('ACTION_NO_KEYS：clearState 缺 keys 报错', () => {
    const r = validateFlow(baseFlow({ actions: { A1: { pattern: 'clearState' } } }));
    expect(r.errors.find((e) => e.code === 'ACTION_NO_KEYS')).toBeTruthy();
  });

  it('ACTION_NO_URL：httpRequest 缺 url 报错', () => {
    const r = validateFlow(baseFlow({ actions: { A1: { pattern: 'httpRequest' } } }));
    expect(r.errors.find((e) => e.code === 'ACTION_NO_URL')).toBeTruthy();
  });

  it('LUA_NO_SCRIPT：pattern=lua 但没有 script', () => {
    const r = validateFlow(baseFlow({ actions: { A1: { pattern: 'lua' } } }));
    expect(r.errors.find((e) => e.code === 'LUA_NO_SCRIPT')).toBeTruthy();
  });

  it('ACTION_ORPHAN：未被任何节点引用的 action 报 warning', () => {
    const r = validateFlow(baseFlow({ actions: { A1: { pattern: 'tcpSend', service: 'logic', route: {}, c2sProto: 'X.Foo' }, orphan: { pattern: 'lua' } } }));
    expect(r.warnings.find((e) => e.code === 'ACTION_ORPHAN')).toBeTruthy();
  });

  // ── Listen 级 ──

  it('LISTEN_ORPHAN：未被引用的 callback 报 warning', () => {
    const r = validateFlow(baseFlow({ listens: { orphan: {} } }));
    expect(r.warnings.find((e) => e.code === 'LISTEN_ORPHAN')).toBeTruthy();
  });

  it('LISTEN_LUA_NO_SCRIPT：lua callback 缺少 script', () => {
    const r = validateFlow(baseFlow({
      nodes: { main: { type: 'action', action: 'A1', listenRefs: [{ server: 'tcp:x', listen: 'cb1', route: {} }] } },
      actions: { A1: { pattern: 'tcpSend', service: 'x', route: {}, c2sProto: 'X.Foo' } },
      listens: { cb1: { script: '' } },
    }));
    expect(r.errors.find((e) => e.code === 'LISTEN_LUA_NO_SCRIPT')).toBeTruthy();
  });

  // ── Binding 级 ──

  it('BINDING_UNKNOWN_TYPE：未知 binding type 报错', () => {
    const r = validateFlow(baseFlow({ actions: { A1: { pattern: 'tcpSend', service: 'logic', route: {}, c2sProto: 'X.Foo', bindings: [{ field: 'f', type: 'badType' as 'fixed' }] } } }));
    expect(r.errors.find((e) => e.code === 'BINDING_UNKNOWN_TYPE')).toBeTruthy();
  });

  it('BINDING_NO_SOURCE：state 类型缺 source 报错', () => {
    const r = validateFlow(baseFlow({ actions: { A1: { pattern: 'tcpSend', service: 'logic', route: {}, c2sProto: 'X.Foo', bindings: [{ field: 'f', type: 'state' }] } } }));
    expect(r.errors.find((e) => e.code === 'BINDING_NO_SOURCE')).toBeTruthy();
  });

  it('BINDING_NO_COUNT：randomPickN 缺 count 报错', () => {
    const r = validateFlow(baseFlow({ actions: { A1: { pattern: 'tcpSend', service: 'logic', route: {}, c2sProto: 'X.Foo', bindings: [{ field: 'f', type: 'randomPickN', values: ['a', 'b'] }] } } }));
    expect(r.errors.find((e) => e.code === 'BINDING_NO_COUNT')).toBeTruthy();
  });

  it('FILTER_UNKNOWN_OP：未知 filter op 报错', () => {
    const r = validateFlow(baseFlow({ actions: { A1: { pattern: 'tcpSend', service: 'logic', route: {}, c2sProto: 'X.Foo', bindings: [{ field: 'f', type: 'fixed', value: 1, filters: [{ op: 'badOp' }] }] } } }));
    expect(r.errors.find((e) => e.code === 'FILTER_UNKNOWN_OP')).toBeTruthy();
  });

  // ── 正向 ──

  it('完整最小合法 flow 0 错误', () => {
    const r = validateFlow(baseFlow());
    expect(r.errors.length).toBe(0);
  });
});
