import { describe, expect, it } from 'vitest';
import { resolveWaitConnection } from './waitConnection';

describe('resolveWaitConnection', () => {
  it('writes then for a wait out connection', () => {
    expect(resolveWaitConnection({ type: 'wait', waitMs: 10 }, 'wait', 'out', 'after')).toEqual({
      matched: true,
      patch: { then: 'after' },
    });
  });

  it('rejects listen cards and self references', () => {
    expect(resolveWaitConnection({ type: 'wait', waitMs: 10 }, 'wait', 'out', null)).toEqual({
      matched: true,
      error: 'wait 节点只能连接普通节点',
    });
    expect(resolveWaitConnection({ type: 'wait', waitMs: 10 }, 'wait', 'out', 'wait')).toEqual({
      matched: true,
      error: 'wait 节点不能指向自身',
    });
  });

  it('ignores other node types and handles', () => {
    expect(resolveWaitConnection({ type: 'action' }, 'action', 'out', 'after')).toEqual({ matched: false });
    expect(resolveWaitConnection({ type: 'wait', waitMs: 10 }, 'wait', 'in', 'after')).toEqual({ matched: false });
  });
});
