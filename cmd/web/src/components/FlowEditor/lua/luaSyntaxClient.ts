/**
 * Lua 语法 Worker 的客户端封装（主线程使用）。
 *
 * 单例 Worker，多个 LuaForm 实例共享。每次 check() 会取消上次未完成的请求。
 * Vite 的 `?worker` import 自动构建为独立 chunk。
 */

import LuaWorker from './luaSyntaxWorker?worker';
import type { LuaCheckMode, ParseRequest, ParseResponse, SyntaxIssue } from './luaSyntaxWorker';

let worker: Worker | null = null;
let pending: ((issues: SyntaxIssue[]) => void) | null = null;

function ensureWorker(): Worker {
  if (worker) return worker;
  worker = new LuaWorker();
  worker.onmessage = (e: MessageEvent<ParseResponse>) => {
    if (e.data?.type === 'result' && pending) {
      pending(e.data.issues);
      pending = null;
    }
  };
  worker.onerror = () => {
    if (pending) {
      pending([]);
      pending = null;
    }
  };
  return worker;
}

export function checkLuaSyntax(code: string, mode: LuaCheckMode): Promise<SyntaxIssue[]> {
  const w = ensureWorker();
  return new Promise<SyntaxIssue[]>((resolve) => {
    // 替换 pending：上一次请求会被无声丢弃（仍在 Worker 里跑，结果到达后被忽略）
    pending = resolve;
    const req: ParseRequest = { type: 'parse', code, mode };
    w.postMessage(req);
  });
}

export type { LuaCheckMode, SyntaxIssue };
