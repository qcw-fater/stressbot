import { describe, expect, it } from 'vitest';

import { createLuaSyntaxClient } from './luaSyntaxClient';
import type {
  LuaWorkerLike,
  ParseRequest,
  ParseResponse,
  SyntaxIssue,
} from './luaSyntaxProtocol';

function issue(message: string): SyntaxIssue {
  return {
    line: 1,
    column: 1,
    endLine: 1,
    endColumn: 2,
    severity: 'error',
    message,
    source: 'syntax',
  };
}

class FakeWorker implements LuaWorkerLike {
  onmessage: ((event: MessageEvent<ParseResponse>) => void) | null = null;
  onerror: ((event: ErrorEvent) => void) | null = null;
  readonly requests: ParseRequest[] = [];
  terminated = false;

  postMessage(request: ParseRequest): void {
    this.requests.push(request);
  }

  terminate(): void {
    this.terminated = true;
  }

  reply(requestId: number, issues: SyntaxIssue[]): void {
    this.onmessage?.({
      data: { type: 'result', requestId, issues },
    } as MessageEvent<ParseResponse>);
  }

  fail(): void {
    this.onerror?.(new ErrorEvent('error'));
  }
}

describe('createLuaSyntaxClient', () => {
  it('按 requestId 关联乱序返回的检查结果', async () => {
    const worker = new FakeWorker();
    const client = createLuaSyntaxClient(() => worker);

    const first = client.check('first', 'action');
    const second = client.check('second', 'listen');

    expect(worker.requests.map((request) => request.requestId)).toEqual([1, 2]);
    worker.reply(2, [issue('second')]);
    worker.reply(1, [issue('first')]);

    await expect(first).resolves.toEqual([issue('first')]);
    await expect(second).resolves.toEqual([issue('second')]);
  });

  it('Worker 崩溃时完成全部 pending，并为下一次检查创建新 Worker', async () => {
    const firstWorker = new FakeWorker();
    const secondWorker = new FakeWorker();
    const workers = [firstWorker, secondWorker];
    const client = createLuaSyntaxClient(() => workers.shift()!);

    const first = client.check('first', 'action');
    const second = client.check('second', 'listen');
    firstWorker.fail();

    await expect(first).resolves.toEqual([]);
    await expect(second).resolves.toEqual([]);
    expect(firstWorker.terminated).toBe(true);

    const afterRestart = client.check('third', 'free');
    expect(secondWorker.requests).toHaveLength(1);
    secondWorker.reply(secondWorker.requests[0].requestId, [issue('third')]);
    await expect(afterRestart).resolves.toEqual([issue('third')]);
  });

  it('dispose 完成全部 pending 并终止 Worker', async () => {
    const worker = new FakeWorker();
    const client = createLuaSyntaxClient(() => worker);
    const pending = client.check('pending', 'action');

    client.dispose();

    await expect(pending).resolves.toEqual([]);
    expect(worker.terminated).toBe(true);
  });
});
