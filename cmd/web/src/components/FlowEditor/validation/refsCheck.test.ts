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

  // ── onError ──

  it('合法 onError 配置无错误', () => {
    const r = validateFlow(baseFlow({
      nodes: {
        main: { type: 'sequence', next: ['act1', 'cleanup'] },
        act1: { type: 'action', action: 'A1', onError: { strategy: 'abort', ignoreCodes: [1001], handler: 'cleanup', retry: { maxRetries: 2, retryDelayMs: 500 } } },
        cleanup: { type: 'action', action: 'A2' },
      },
      actions: {
        A1: { pattern: 'tcpSend', service: 'logic', route: { cmd: 1, act: 1 }, c2sProto: 'X.Foo' },
        A2: { pattern: 'clearState', keys: ['x'] },
      },
    }));
    expect(r.errors.filter((e) => e.code.startsWith('ON_ERROR'))).toEqual([]);
  });

  it('ON_ERROR_STRATEGY_INVALID：非法 strategy 报错', () => {
    const r = validateFlow(baseFlow({ nodes: { main: { type: 'sequence', next: ['act1'] }, act1: { type: 'action', action: 'A1', onError: { strategy: 'bad' as 'abort' } } } }));
    expect(r.errors.find((e) => e.code === 'ON_ERROR_STRATEGY_INVALID')).toBeTruthy();
  });

  it('ON_ERROR_IGNORE_CODE_INVALID：ignoreCodes 必须是正整数', () => {
    const r = validateFlow(baseFlow({ nodes: { main: { type: 'sequence', next: ['act1'] }, act1: { type: 'action', action: 'A1', onError: { ignoreCodes: [0, -1, 1.5] } } } }));
    expect(r.errors.filter((e) => e.code === 'ON_ERROR_IGNORE_CODE_INVALID')).toHaveLength(3);
  });

  it('ON_ERROR_HANDLER_NOT_FOUND：handler 指向不存在节点报错', () => {
    const r = validateFlow(baseFlow({ nodes: { main: { type: 'sequence', next: ['act1'] }, act1: { type: 'action', action: 'A1', onError: { handler: 'ghost' } } } }));
    expect(r.errors.find((e) => e.code === 'ON_ERROR_HANDLER_NOT_FOUND')).toBeTruthy();
  });

  it('ON_ERROR_HANDLER_SELF：handler 指向自身报错', () => {
    const r = validateFlow(baseFlow({ nodes: { main: { type: 'sequence', next: ['act1'] }, act1: { type: 'action', action: 'A1', onError: { handler: 'act1' } } } }));
    expect(r.errors.find((e) => e.code === 'ON_ERROR_HANDLER_SELF')).toBeTruthy();
  });

  it('ON_ERROR_RETRY_*：retry 负数报错', () => {
    const r = validateFlow(baseFlow({ nodes: { main: { type: 'sequence', next: ['act1'] }, act1: { type: 'action', action: 'A1', onError: { retry: { maxRetries: -1, retryDelayMs: -1 } } } } }));
    expect(r.errors.find((e) => e.code === 'ON_ERROR_RETRY_MAX_INVALID')).toBeTruthy();
    expect(r.errors.find((e) => e.code === 'ON_ERROR_RETRY_DELAY_INVALID')).toBeTruthy();
  });

  it('onError.handler 引用的节点不判定为孤立', () => {
    const r = validateFlow(baseFlow({
      nodes: {
        main: { type: 'sequence', next: ['act1'] },
        act1: { type: 'action', action: 'A1', onError: { handler: 'cleanup' } },
        cleanup: { type: 'action', action: 'A2' },
      },
      actions: {
        A1: { pattern: 'tcpSend', service: 'logic', route: { cmd: 1, act: 1 }, c2sProto: 'X.Foo' },
        A2: { pattern: 'clearState', keys: ['x'] },
      },
    }));
    expect(r.warnings.find((e) => e.code === 'NODE_ORPHAN' && e.location?.id === 'cleanup')).toBeFalsy();
  });

  // ── ListenRef ──

  it('LISTEN_SERVER_FORMAT：server 格式错误报错', () => {
    const r = validateFlow(baseFlow({
      nodes: { main: { type: 'action', action: 'A1', listenRefs: [{ server: 'badFormat', listen: null, route: {} }] } },
    }));
    expect(r.errors.find((e) => e.code === 'LISTEN_SERVER_FORMAT')).toBeTruthy();
  });

  it('LISTEN_REF_NOT_FOUND：listenRefs 引用不存在 listen', () => {
    const r = validateFlow(baseFlow({
      nodes: { main: { type: 'action', action: 'A1', listenRefs: [{ server: 'tcp:x', listen: 'ghost', route: {} }] } },
    }));
    expect(r.errors.find((e) => e.code === 'LISTEN_REF_NOT_FOUND')).toBeTruthy();
  });

  it('listenRefs listen=null 合法（静默预注册，不再报 LISTEN_EMPTY_NAME）', () => {
    const r = validateFlow(baseFlow({
      nodes: { main: { type: 'action', action: 'A1', listenRefs: [{ server: 'tcp:x', listen: null, route: {} }] } },
    }));
    expect(r.errors.find((e) => e.code === 'LISTEN_EMPTY_NAME')).toBeFalsy();
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

  it('LISTEN_LUA_NO_SCRIPT：listen 为 lua 模式但缺少 script', () => {
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

  it('FILTER_UNKNOWN_MODE：未知 filter mode 报错', () => {
    const r = validateFlow(baseFlow({ actions: { A1: { pattern: 'tcpSend', service: 'logic', route: {}, c2sProto: 'X.Foo', bindings: [{ field: 'f', type: 'fixed', value: 1, filters: [{ op: 'eq', mode: 'badMode' as 'any' }] }] } } }));
    expect(r.errors.find((e) => e.code === 'FILTER_UNKNOWN_MODE')).toBeTruthy();
  });

  it('FILTER_ARRAY_PATH_NO_MODE：数组通配 path 未设置 mode 报 warning', () => {
    const r = validateFlow(baseFlow({ actions: { A1: { pattern: 'tcpSend', service: 'logic', route: {}, c2sProto: 'X.Foo', bindings: [{ field: 'f', type: 'fixed', value: 1, filters: [{ path: 'shopData[].ID', op: 'eq', value: 1 }] }] } } }));
    expect(r.warnings.find((e) => e.code === 'FILTER_ARRAY_PATH_NO_MODE')).toBeTruthy();
  });

  it('notIn / notContains 是合法 filter op', () => {
    const r = validateFlow(baseFlow({ actions: { A1: { pattern: 'tcpSend', service: 'logic', route: {}, c2sProto: 'X.Foo', bindings: [{ field: 'f', type: 'fixed', value: 1, filters: [{ op: 'notIn', value: [1, 2] }, { op: 'notContains', value: 'x' }] }] } } }));
    expect(r.errors.find((e) => e.code === 'FILTER_UNKNOWN_OP')).toBeFalsy();
  });

  it('randomString 支持字符集别名和自定义字符集', () => {
    const r = validateFlow(baseFlow({
      actions: {
        A1: {
          pattern: 'tcpSend',
          service: 'logic',
          route: {},
          c2sProto: 'X.Foo',
          bindings: [
            { field: 'lowerName', type: 'randomString', length: 8, charset: 'lower' },
            { field: 'upperName', type: 'randomString', length: 8, charset: 'upper' },
            { field: 'inviteCode', type: 'randomString', length: 8, charset: 'ABC-123_' },
          ],
        },
      },
    }));

    expect(r.errors.find((e) => e.code === 'BINDING_INVALID_CHARSET')).toBeFalsy();
  });

  it('randomString 显式空白 charset 报错', () => {
    const r = validateFlow(baseFlow({
      actions: {
        A1: {
          pattern: 'tcpSend',
          service: 'logic',
          route: {},
          c2sProto: 'X.Foo',
          bindings: [{ field: 'name', type: 'randomString', length: 8, charset: '   ' }],
        },
      },
    }));

    expect(r.errors.find((e) => e.code === 'BINDING_INVALID_CHARSET')).toBeTruthy();
  });

  // ── 正向 ──

  it('完整最小合法 flow 0 错误', () => {
    const r = validateFlow(baseFlow());
    expect(r.errors.length).toBe(0);
  });

  // ── map binding 级 ──

  it('BINDING_MAP_NO_ENTRIES：map 缺少 entries 报错', () => {
    const r = validateFlow(baseFlow({
      actions: {
        A1: {
          pattern: 'tcpSend',
          service: 'logic',
          route: {},
          c2sProto: 'X.Foo',
          bindings: [{ field: 'heroMap', type: 'map' }],
        },
      },
    }));
    expect(r.errors.find((e) => e.code === 'BINDING_MAP_NO_ENTRIES')).toBeTruthy();
  });

  it('BINDING_MAP_NO_ENTRIES：map entries 为空数组报错', () => {
    const r = validateFlow(baseFlow({
      actions: {
        A1: {
          pattern: 'tcpSend',
          service: 'logic',
          route: {},
          c2sProto: 'X.Foo',
          bindings: [{ field: 'heroMap', type: 'map', entries: [] }],
        },
      },
    }));
    expect(r.errors.find((e) => e.code === 'BINDING_MAP_NO_ENTRIES')).toBeTruthy();
  });

  it('BINDING_MAP_ENTRY_NO_KEY：map entry 缺少 key 报错', () => {
    const r = validateFlow(baseFlow({
      actions: {
        A1: {
          pattern: 'tcpSend',
          service: 'logic',
          route: {},
          c2sProto: 'X.Foo',
          bindings: [{ field: 'heroMap', type: 'map', entries: [{ value: { type: 'fixed', value: 1 } }] }],
        },
      },
    }));
    expect(r.errors.find((e) => e.code === 'BINDING_MAP_ENTRY_NO_KEY')).toBeTruthy();
  });

  it('BINDING_MAP_ENTRY_NO_VALUE：map entry 缺少 value 报错', () => {
    const r = validateFlow(baseFlow({
      actions: {
        A1: {
          pattern: 'tcpSend',
          service: 'logic',
          route: {},
          c2sProto: 'X.Foo',
          bindings: [{ field: 'heroMap', type: 'map', entries: [{ key: 'name' }] }],
        },
      },
    }));
    expect(r.errors.find((e) => e.code === 'BINDING_MAP_ENTRY_NO_VALUE')).toBeTruthy();
  });

  it('BINDING_MAP_ENTRY_VALUE_MAP：map entry value 不允许嵌套 map 类型', () => {
    const r = validateFlow(baseFlow({
      actions: {
        A1: {
          pattern: 'tcpSend',
          service: 'logic',
          route: {},
          c2sProto: 'X.Foo',
          bindings: [{
            field: 'heroMap',
            type: 'map',
            entries: [{ key: 'nested', value: { type: 'map', entries: [{ key: 'inner', value: { type: 'fixed', value: 1 } }] } }],
          }],
        },
      },
    }));
    expect(r.errors.find((e) => e.code === 'BINDING_MAP_ENTRY_VALUE_MAP')).toBeTruthy();
  });

  it('map entry value 复用 source 校验：entry value 缺 source 报 BINDING_NO_SOURCE', () => {
    const r = validateFlow(baseFlow({
      actions: {
        A1: {
          pattern: 'tcpSend',
          service: 'logic',
          route: {},
          c2sProto: 'X.Foo',
          bindings: [{
            field: 'heroMap',
            type: 'map',
            entries: [{ key: 'name', value: { type: 'state' } }],
          }],
        },
      },
    }));
    expect(r.errors.find((e) => e.code === 'BINDING_NO_SOURCE')).toBeTruthy();
  });

  it('map entry value 不触发 BINDING_NO_FIELD（纯值生成器不需要 field/storeAs）', () => {
    const r = validateFlow(baseFlow({
      actions: {
        A1: {
          pattern: 'tcpSend',
          service: 'logic',
          route: {},
          c2sProto: 'X.Foo',
          bindings: [{
            field: 'params',
            type: 'map',
            entries: [
              { key: 1, value: { type: 'randomInt', min: 0, max: 1 } },
              { key: 2, value: { type: 'randomInt', min: 0, max: 1 } },
              { key: 3, value: { type: 'randomInt', min: 1, max: 200 } },
            ],
          }],
        },
      },
    }));
    expect(r.errors.length).toBe(0);
    expect(r.warnings.find((e) => e.code === 'BINDING_NO_FIELD')).toBeFalsy();
  });

  it('合法 map binding 无错误', () => {
    const r = validateFlow(baseFlow({
      actions: {
        A1: {
          pattern: 'tcpSend',
          service: 'logic',
          route: {},
          c2sProto: 'X.Foo',
          bindings: [{
            field: 'heroMap',
            type: 'map',
            entries: [
              { key: 'name', value: { type: 'fixed', value: 'test' } },
              { key: 'level', value: { type: 'randomInt', min: 1, max: 100 } },
            ],
          }],
        },
      },
    }));
    expect(r.errors.length).toBe(0);
  });

  // ── 已移除心跳 action ──

  it('tcpHeartbeat / udpHeartbeat 已移除，提示迁移到协议连接 heartbeat', () => {
    const tcp = validateFlow(baseFlow({ actions: { A1: { pattern: 'tcpHeartbeat' as 'tcpSend' } } }));
    expect(tcp.errors.find((e) => e.code === 'ACTION_UNKNOWN_PATTERN')?.message).toContain('协议连接配置的 heartbeat');

    const udp = validateFlow(baseFlow({ actions: { A1: { pattern: 'udpHeartbeat' as 'udpSend' } } }));
    expect(udp.errors.find((e) => e.code === 'ACTION_UNKNOWN_PATTERN')?.message).toContain('协议连接配置的 heartbeat');
  });

  // ── ListenRef.queueSize ──

  it('LISTEN_QUEUE_INVALID：listenRefs queueSize <= 0 报错', () => {
    const r = validateFlow(baseFlow({
      nodes: { main: { type: 'action', action: 'A1', listenRefs: [{ server: 'tcp:x', listen: 'cb1', route: {}, queueSize: 0 }] } },
      actions: { A1: { pattern: 'tcpSend', service: 'x', route: {}, c2sProto: 'X.Foo' } },
      listens: { cb1: {} },
    }));
    expect(r.errors.find((e) => e.code === 'LISTEN_QUEUE_INVALID')).toBeTruthy();
  });

  it('listenRefs queueSize = 2（>0）合法', () => {
    const r = validateFlow(baseFlow({
      nodes: { main: { type: 'action', action: 'A1', listenRefs: [{ server: 'tcp:x', listen: 'cb1', route: {}, queueSize: 2 }] } },
      actions: { A1: { pattern: 'tcpSend', service: 'x', route: {}, c2sProto: 'X.Foo' } },
      listens: { cb1: {} },
    }));
    expect(r.errors.find((e) => e.code === 'LISTEN_QUEUE_INVALID')).toBeFalsy();
  });

  // ── ListenDef.script ──

  it('listen 配置非空 script 字段合法', () => {
    const r = validateFlow(baseFlow({
      nodes: { main: { type: 'action', action: 'A1', listenRefs: [{ server: 'tcp:x', listen: 'cb1', route: {} }] } },
      actions: { A1: { pattern: 'tcpSend', service: 'x', route: {}, c2sProto: 'X.Foo' } },
      listens: { cb1: { script: 'foo.lua' } },
    }));
    expect(r.errors.find((e) => e.code === 'LISTEN_LUA_NO_SCRIPT')).toBeFalsy();
  });
});

describe('switch node validation', () => {
  it('accepts valid switch nodes and warns when default is missing', () => {
    const report = validateFlow({
      defaultDelayMs: 1000,
      nodes: {
        main: {
          type: 'switch',
          cases: [{ condition: 'state:level >= 10', next: 'advanced' }],
        },
        advanced: { type: 'action', action: 'advanced' },
      },
      actions: { advanced: { pattern: 'clearState', keys: ['x'] } },
      listens: {},
    });

    expect(report.errors.map((e) => e.code)).not.toContain('NODE_UNKNOWN_TYPE');
    expect(report.errors.map((e) => e.code)).not.toContain('SWITCH_NO_CASES');
    expect(report.warnings.map((e) => e.code)).toContain('SWITCH_NO_DEFAULT');
  });

  it('reports missing cases, empty condition, missing next, and invalid default', () => {
    const report = validateFlow({
      defaultDelayMs: 1000,
      nodes: {
        main: {
          type: 'switch',
          cases: [{ condition: '', next: '' }],
          defaultNext: 'missing',
        },
      },
      actions: {},
      listens: {},
    });

    expect(report.errors.map((e) => e.code)).toEqual(expect.arrayContaining([
      'SWITCH_CASE_NO_CONDITION',
      'SWITCH_CASE_NO_NEXT',
      'NODE_REF_NOT_FOUND',
    ]));
  });

  it('reports SWITCH_NO_CASES when cases is empty or absent', () => {
    const report = validateFlow({
      defaultDelayMs: 1000,
      nodes: {
        main: { type: 'switch', defaultNext: 'fallback' },
        fallback: { type: 'action', action: 'F' },
      },
      actions: { F: { pattern: 'clearState', keys: ['f'] } },
      listens: {},
    });

    expect(report.errors.map((e) => e.code)).toContain('SWITCH_NO_CASES');
  });

  it('does not report switch case and default targets as orphan nodes', () => {
    const report = validateFlow({
      defaultDelayMs: 1000,
      nodes: {
        main: {
          type: 'switch',
          cases: [{ condition: 'state:level >= 10', next: 'advanced' }],
          defaultNext: 'basic',
        },
        advanced: { type: 'action', action: 'advanced' },
        basic: { type: 'action', action: 'basic' },
      },
      actions: {
        advanced: { pattern: 'clearState', keys: ['advanced'] },
        basic: { pattern: 'clearState', keys: ['basic'] },
      },
      listens: {},
    });

    expect(report.warnings.filter((e) => e.code === 'NODE_ORPHAN')).toEqual([]);
  });

  it('accepts break and continue reachable through switch branches inside a loop', () => {
    const report = validateFlow({
      defaultDelayMs: 1000,
      nodes: {
        main: { type: 'loop', body: 'switchInLoop', loopCount: 1 },
        switchInLoop: {
          type: 'switch',
          cases: [{ condition: 'state:done == true', next: 'breakNode' }],
          defaultNext: 'continueNode',
        },
        breakNode: { type: 'break' },
        continueNode: { type: 'continue' },
      },
      actions: {},
      listens: {},
    });

    expect(report.errors.filter((e) => e.code === 'BREAK_CONTINUE_OUTSIDE_LOOP')).toEqual([]);
  });

  it('trims whitespace in case next and defaultNext before resolving refs', () => {
    const report = validateFlow({
      defaultDelayMs: 1000,
      nodes: {
        main: {
          type: 'switch',
          cases: [{ condition: 'state:level >= 10', next: ' advanced ' }],
          defaultNext: ' basic ',
        },
        advanced: { type: 'action', action: 'advanced' },
        basic: { type: 'action', action: 'basic' },
      },
      actions: {
        advanced: { pattern: 'clearState', keys: ['advanced'] },
        basic: { pattern: 'clearState', keys: ['basic'] },
      },
      listens: {},
    });

    expect(report.errors.filter((e) => e.code === 'NODE_REF_NOT_FOUND')).toEqual([]);
    expect(report.errors.filter((e) => e.code === 'NODE_SELF_REF')).toEqual([]);
  });

  // ── 整数契约 / HTTP / required-optional 互斥（前端兜底，对齐 Go int / 白名单） ──

  it('BINDING_NON_INTEGER：min/max/length/count/precision 小数报错', () => {
    const r = validateFlow(baseFlow({
      actions: {
        A1: {
          pattern: 'tcpSend', service: 'logic', route: { cmd: 1 }, c2sProto: 'X.Foo',
          bindings: [
            { field: 'a', type: 'randomInt', min: 1.5, max: 10 },
            { field: 'b', type: 'randomString', length: 8.5 },
          ],
        },
      },
    }));
    expect(r.errors.filter((e) => e.code === 'BINDING_NON_INTEGER').map((e) => e.message)).toEqual([
      'action "A1".bindings[0] 的 min 必须是整数（当前 1.5）',
      'action "A1".bindings[1] 的 length 必须是整数（当前 8.5）',
    ]);
  });

  it('ACTION_TIMEOUT_NON_INTEGER / ACTION_POLLMS_NON_INTEGER：超时/轮询小数报错', () => {
    const r = validateFlow(baseFlow({
      actions: { A1: { pattern: 'tcpListen', service: 'logic', route: { cmd: 1 }, timeout: 1.5, pollMs: 2.5 } },
    }));
    expect(r.errors.find((e) => e.code === 'ACTION_TIMEOUT_NON_INTEGER')).toBeTruthy();
    expect(r.errors.find((e) => e.code === 'ACTION_POLLMS_NON_INTEGER')).toBeTruthy();
  });

  it('HTTP_METHOD_INVALID / HTTP_CONTENT_TYPE_INVALID：非法 method/contentType 报错', () => {
    const r = validateFlow(baseFlow({
      actions: {
        A1: { pattern: 'httpRequest', url: 'http://x', method: 'POTS' as 'POST', contentType: 'jsno' as 'json' },
      },
    }));
    expect(r.errors.find((e) => e.code === 'HTTP_METHOD_INVALID')).toBeTruthy();
    expect(r.errors.find((e) => e.code === 'HTTP_CONTENT_TYPE_INVALID')).toBeTruthy();
  });

  it('BINDING_REQUIRED_OPTIONAL_CONFLICT：required 与 optional 同开报 warning', () => {
    const r = validateFlow(baseFlow({
      actions: {
        A1: {
          pattern: 'tcpSend', service: 'logic', route: { cmd: 1 }, c2sProto: 'X.Foo',
          bindings: [{ field: 'a', type: 'fixed', value: 1, required: true, optional: true }],
        },
      },
    }));
    expect(r.warnings.find((e) => e.code === 'BINDING_REQUIRED_OPTIONAL_CONFLICT')).toBeTruthy();
  });
});
