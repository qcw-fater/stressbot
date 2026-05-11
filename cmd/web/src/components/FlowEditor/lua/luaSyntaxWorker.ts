/**
 * Lua 语法分析 Web Worker。
 *
 * 主线程发送 `{ type: 'parse', code, mode }`：
 *   - mode='action'   : 期望存在 `function execute(r) ... end`（return code[, send, recv]）
 *   - mode='boolean'  : 期望存在 `function execute(r) ... end`（return true / false，签名同 action）
 *   - mode='callback' : 期望存在 `function onMessage(r, msg) ... end`
 *   - mode='free'     : 不做入口签名校验
 *
 * Worker 返回 `{ type: 'result', errors: SyntaxError[] }`，errors 由 Monaco 转成 markers。
 */

import luaparse from 'luaparse';

export type LuaCheckMode = 'action' | 'boolean' | 'listen' | 'free';

export interface SyntaxIssue {
  line: number;
  column: number;
  endLine: number;
  endColumn: number;
  severity: 'error' | 'warning' | 'info';
  message: string;
  source: 'syntax' | 'entry';
}

export interface ParseRequest {
  type: 'parse';
  code: string;
  mode: LuaCheckMode;
}

export interface ParseResponse {
  type: 'result';
  issues: SyntaxIssue[];
}

self.onmessage = (e: MessageEvent<ParseRequest>) => {
  if (e.data?.type !== 'parse') return;
  const { code, mode } = e.data;
  const issues: SyntaxIssue[] = [];

  let ast: unknown = null;
  try {
    ast = luaparse.parse(code, {
      locations: true,
      ranges: false,
      luaVersion: '5.1',
      comments: false,
      wait: false,
    });
  } catch (err) {
    const e = err as { line?: number; column?: number; message?: string };
    issues.push({
      line: e.line ?? 1,
      column: (e.column ?? 0) + 1,
      endLine: e.line ?? 1,
      endColumn: (e.column ?? 0) + 80,
      severity: 'error',
      message: e.message ?? String(err),
      source: 'syntax',
    });
  }

  if (ast && mode !== 'free') {
    // action / boolean 共用 execute(r) 签名；listen 用 onMessage(r, msg)
    const expected =
      mode === 'listen'
        ? { name: 'onMessage', params: 2 }
        : { name: 'execute', params: 1 };
    const found = findEntryFunction(ast, expected.name);
    if (!found) {
      issues.push({
        line: 1,
        column: 1,
        endLine: 1,
        endColumn: 1,
        severity: 'error',
        message: `脚本必须定义入口函数：function ${expected.name}(${
          mode === 'listen' ? 'r, msg' : 'r'
        }) ... end`,
        source: 'entry',
      });
    } else if (found.paramCount < expected.params) {
      issues.push({
        line: found.line,
        column: 1,
        endLine: found.line,
        endColumn: 80,
        severity: 'warning',
        message: `${expected.name} 期望 ${expected.params} 个参数，实际 ${found.paramCount} 个`,
        source: 'entry',
      });
    }
  }

  const resp: ParseResponse = { type: 'result', issues };
  (self as unknown as { postMessage: (m: ParseResponse) => void }).postMessage(resp);
};

interface EntryInfo {
  line: number;
  paramCount: number;
}

/**
 * 在 luaparse AST 中查找全局函数声明：
 *   function name(...) ... end
 * 或赋值声明：
 *   name = function(...) end
 */
function findEntryFunction(ast: unknown, name: string): EntryInfo | null {
  const body = (ast as { body?: AstNode[] }).body ?? [];
  for (const stmt of body) {
    // function name(...)  → FunctionDeclaration with identifier
    if (stmt.type === 'FunctionDeclaration') {
      const id = stmt.identifier;
      if (id?.type === 'Identifier' && id.name === name) {
        return {
          line: stmt.loc?.start.line ?? 1,
          paramCount: stmt.parameters?.length ?? 0,
        };
      }
    }
    // name = function(...)  → AssignmentStatement
    if (stmt.type === 'AssignmentStatement') {
      const target = stmt.variables?.[0];
      const init = stmt.init?.[0];
      if (
        target?.type === 'Identifier' &&
        target.name === name &&
        init?.type === 'FunctionDeclaration'
      ) {
        return {
          line: stmt.loc?.start.line ?? 1,
          paramCount: init.parameters?.length ?? 0,
        };
      }
    }
  }
  return null;
}

// 简化 AST 类型（仅覆盖本文件用到的字段）
interface AstNode {
  type: string;
  loc?: { start: { line: number; column: number }; end: { line: number; column: number } };
  identifier?: { type: string; name: string };
  parameters?: Array<{ type: string; name: string }>;
  variables?: Array<{ type: string; name: string }>;
  init?: Array<{ type: string; parameters?: Array<{ type: string; name: string }> }>;
}
