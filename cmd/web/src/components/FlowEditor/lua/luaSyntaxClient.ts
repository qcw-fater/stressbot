/** Lua 语法 Worker 的主线程封装。 */

import LuaWorker from './luaSyntaxWorker?worker';
import type {
  LuaCheckMode,
  LuaWorkerLike,
  ParseRequest,
  ParseResponse,
  SyntaxIssue,
} from './luaSyntaxProtocol';

export interface LuaSyntaxClient {
  check(code: string, mode: LuaCheckMode): Promise<SyntaxIssue[]>;
  dispose(): void;
}

export function createLuaSyntaxClient(factory: () => LuaWorkerLike): LuaSyntaxClient {
  let worker: LuaWorkerLike | null = null;
  let nextRequestId = 1;
  const pending = new Map<number, (issues: SyntaxIssue[]) => void>();

  const settlePending = (): void => {
    for (const resolve of pending.values()) resolve([]);
    pending.clear();
  };

  const stopWorker = (): void => {
    const current = worker;
    worker = null;
    if (current) {
      current.onmessage = null;
      current.onerror = null;
      current.terminate();
    }
    settlePending();
  };

  const ensureWorker = (): LuaWorkerLike => {
    if (worker) return worker;
    const created = factory();
    worker = created;
    created.onmessage = (event: MessageEvent<ParseResponse>) => {
      if (event.data?.type !== 'result') return;
      const resolve = pending.get(event.data.requestId);
      if (!resolve) return;
      pending.delete(event.data.requestId);
      resolve(event.data.issues);
    };
    created.onerror = stopWorker;
    return created;
  };

  return {
    check(code: string, mode: LuaCheckMode): Promise<SyntaxIssue[]> {
      const requestId = nextRequestId++;
      const current = ensureWorker();
      return new Promise<SyntaxIssue[]>((resolve) => {
        pending.set(requestId, resolve);
        const request: ParseRequest = { type: 'parse', requestId, code, mode };
        current.postMessage(request);
      });
    },
    dispose: stopWorker,
  };
}

const defaultClient = createLuaSyntaxClient(() => new LuaWorker() as LuaWorkerLike);

export function checkLuaSyntax(code: string, mode: LuaCheckMode): Promise<SyntaxIssue[]> {
  return defaultClient.check(code, mode);
}

export type { LuaCheckMode, SyntaxIssue };
