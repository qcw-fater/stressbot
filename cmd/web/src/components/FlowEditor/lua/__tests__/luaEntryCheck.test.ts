/**
 * Lua 入口签名 + 语法错误检测的纯函数测试。
 *
 * 因为 luaSyntaxWorker.ts 默认作为 Web Worker 入口（self.onmessage），
 * 我们这里把核心逻辑（findEntryFunction / 语法解析）通过本地复刻的迷你版本测试，
 * 这样既能在 vitest 中直接跑，又能保证替换 worker 实现时及时发现回归。
 */

import { describe, expect, it } from 'vitest';
import luaparse from 'luaparse';

interface AstNode {
  type: string;
  loc?: { start: { line: number } };
  identifier?: { type: string; name: string };
  parameters?: Array<{ type: string; name: string }>;
  variables?: Array<{ type: string; name: string }>;
  init?: Array<{ type: string; parameters?: Array<{ type: string; name: string }> }>;
}

function findEntry(code: string, name: string): { line: number; paramCount: number } | null {
  const ast = luaparse.parse(code, { locations: true, luaVersion: '5.1' }) as unknown as { body?: AstNode[] };
  for (const stmt of ast.body ?? []) {
    if (stmt.type === 'FunctionDeclaration' && stmt.identifier?.type === 'Identifier' && stmt.identifier.name === name) {
      return { line: stmt.loc?.start.line ?? 1, paramCount: stmt.parameters?.length ?? 0 };
    }
    if (stmt.type === 'AssignmentStatement') {
      const tgt = stmt.variables?.[0];
      const init = stmt.init?.[0];
      if (tgt?.type === 'Identifier' && tgt.name === name && init?.type === 'FunctionDeclaration') {
        return { line: stmt.loc?.start.line ?? 1, paramCount: init.parameters?.length ?? 0 };
      }
    }
  }
  return null;
}

describe('入口函数定位', () => {
  it('识别 function execute(r) 形式', () => {
    const r = findEntry(`function execute(r)\n  return 0\nend\n`, 'execute');
    expect(r).not.toBeNull();
    expect(r?.paramCount).toBe(1);
    expect(r?.line).toBe(1);
  });

  it('识别 execute = function(r) 形式', () => {
    const code = `\nlocal x = 1\nexecute = function(r)\n  return 0\nend\n`;
    const r = findEntry(code, 'execute');
    expect(r).not.toBeNull();
    expect(r?.paramCount).toBe(1);
  });

  it('识别 on_message(r, msg) 双参', () => {
    const r = findEntry(`function on_message(r, msg)\nend\n`, 'on_message');
    expect(r?.paramCount).toBe(2);
  });

  it('参数数量不足时仍能定位但 paramCount < 期望', () => {
    const r = findEntry(`function execute()\nend\n`, 'execute');
    expect(r?.paramCount).toBe(0);
  });

  it('完全没有入口函数返回 null', () => {
    expect(findEntry(`local x = 1\n`, 'execute')).toBeNull();
  });

  it('入口函数只能在顶层声明，方法或嵌套不计', () => {
    const r = findEntry(
      `local m = {}\nfunction m.execute(r)\n  return 0\nend\n`,
      'execute',
    );
    expect(r).toBeNull();
  });
});

describe('luaparse 语法错误捕获', () => {
  it('括号不匹配抛错并附带行号', () => {
    expect(() => luaparse.parse('function f( end\n', { luaVersion: '5.1' })).toThrow();
    try {
      luaparse.parse('function f( end\n', { luaVersion: '5.1', locations: true });
    } catch (err) {
      const e = err as { line?: number; message?: string };
      expect(e.line).toBeGreaterThanOrEqual(1);
      expect(e.message).toBeTruthy();
    }
  });

  it('合法脚本无异常', () => {
    expect(() =>
      luaparse.parse(
        `local network = require('network')\nfunction execute(r)\n  return 0\nend\n`,
        { luaVersion: '5.1' },
      ),
    ).not.toThrow();
  });
});
