/**
 * Lua API 元数据完整性测试。
 *
 * 这些断言保证：
 *   - 每个模块的函数列表无重复名；
 *   - 每个函数定义了 summary 和 returns；
 *   - 每个 LuaParam 有 name/type/doc；
 *   - renderSignature / renderDoc 生成的字符串格式正确。
 */

import { describe, expect, it } from 'vitest';
import {
  LUA_MODULES,
  getLuaFunction,
  getLuaModule,
  renderDoc,
  renderSignature,
} from '../luaApiSpec';

describe('luaApiSpec metadata', () => {
  it('每个模块都有 functions 数组', () => {
    expect(LUA_MODULES.length).toBeGreaterThanOrEqual(6);
    for (const m of LUA_MODULES) {
      expect(m.name).toBeTruthy();
      expect(m.summary).toBeTruthy();
      expect(Array.isArray(m.functions)).toBe(true);
    }
  });

  it('每个函数 summary / returns 都不为空，参数有 name/type/doc', () => {
    for (const m of LUA_MODULES) {
      for (const fn of m.functions) {
        expect(fn.name, `${m.name}.${fn.name}`).toBeTruthy();
        expect(fn.summary, `${m.name}.${fn.name}.summary`).toBeTruthy();
        expect(fn.returns, `${m.name}.${fn.name}.returns`).toBeTruthy();
        for (const p of fn.params) {
          expect(p.name).toBeTruthy();
          expect(p.type).toBeTruthy();
          expect(p.doc).toBeTruthy();
        }
      }
    }
  });

  it('模块内函数名唯一', () => {
    for (const m of LUA_MODULES) {
      const names = m.functions.map((f) => f.name);
      const unique = new Set(names);
      expect(unique.size, `${m.name} 函数有重名`).toBe(names.length);
    }
  });

  it('getLuaModule / getLuaFunction 能正确查找', () => {
    expect(getLuaModule('robot')?.name).toBe('robot');
    expect(getLuaModule('xyz')).toBeUndefined();
    expect(getLuaFunction('robot', 'get')?.params[0].name).toBe('key');
    expect(getLuaFunction('robot', 'nonexistent')).toBeUndefined();
  });
});

describe('renderSignature / renderDoc', () => {
  it('renderSignature 用方括号包装可选参数', () => {
    const fn = getLuaFunction('robot', 'clear')!;
    expect(renderSignature(fn)).toBe('([key])');
  });

  it('renderSignature 多参数用逗号分隔', () => {
    const fn = getLuaFunction('network', 'tcp_request')!;
    expect(renderSignature(fn)).toBe('(service, route, msg, [s2c_proto], [timeout_sec])');
  });

  it('renderSignature 支持请求/响应路由分离接口', () => {
    const fn = getLuaFunction('network', 'tcp_request_route')!;
    expect(renderSignature(fn)).toBe('(service, request_route, response_route, msg, [s2c_proto], [timeout_sec])');
  });

  it('network API 不再向脚本返回 sent/recv 字节数', () => {
    const names = ['tcp_request', 'tcp_request_route', 'udp_request', 'udp_request_route', 'tcp_send', 'udp_send', 'tcp_listen', 'udp_listen', 'http_request'];
    for (const name of names) {
      const fn = getLuaFunction('network', name)!;
      expect(fn.returns, `network.${name}.returns`).not.toMatch(/sent|recv/);
      expect(fn.detail ?? '', `network.${name}.detail`).not.toMatch(/底层|自动计入|自动统计/);
    }
  });

  it('renderDoc 包含函数签名、summary、参数和返回值', () => {
    const fn = getLuaFunction('robot', 'set')!;
    const doc = renderDoc(fn);
    expect(doc).toContain('robot.set(key, value)');
    expect(doc).toContain('写入状态键');
    expect(doc).toContain('**key**');
    expect(doc).toContain('**value**');
  });

  it('renderDoc 在有 example 时包含示例代码块', () => {
    const fn = getLuaFunction('proto', 'create')!;
    const doc = renderDoc(fn);
    expect(doc).toContain('**示例**');
    expect(doc).toContain('proto.create');
  });
});

describe('coverage：所有 stressbot 已暴露的核心 Lua API 都被记录', () => {
  it('robot 模块覆盖关键函数', () => {
    const m = getLuaModule('robot')!;
    const names = m.functions.map((f) => f.name);
    for (const expected of ['get', 'set', 'has', 'delete', 'clear', 'increment', 'get_path', 'get_id', 'get_account', 'get_context', 'keys']) {
      expect(names, `robot.${expected}`).toContain(expected);
    }
  });

  it('network 模块覆盖关键函数', () => {
    const m = getLuaModule('network')!;
    const names = m.functions.map((f) => f.name);
    for (const expected of [
      'connect_tcp',
      'connect_udp',
      'close_tcp',
      'close_udp',
      'tcp_request',
      'tcp_request_route',
      'udp_request',
      'udp_request_route',
      'tcp_send',
      'udp_send',
      'http_request',
      'tcp_listen',
      'udp_listen',
      'set_tcp_secret_key',
      'set_udp_secret_key',
      'get_tcp_secret_key',
      'get_udp_secret_key',
      'ensure_tcp_listener',
      'ensure_udp_listener',
      'register_tcp_heartbeat',
      'register_udp_heartbeat',
    ]) {
      expect(names, `network.${expected}`).toContain(expected);
    }
  });

  it('proto 模块覆盖关键函数', () => {
    const m = getLuaModule('proto')!;
    const names = m.functions.map((f) => f.name);
    for (const expected of ['create', 'set_field', 'get_field', 'get_path', 'serialize', 'parse', 'get_field_map', 'iter_list', 'list_size', 'list_get']) {
      expect(names, `proto.${expected}`).toContain(expected);
    }
  });

  it('utils / json / log / adapter 模块都存在', () => {
    for (const name of ['utils', 'json', 'log', 'adapter']) {
      expect(getLuaModule(name), `module ${name}`).toBeDefined();
    }
  });
});
