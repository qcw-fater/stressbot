import { describe, expect, it } from 'vitest';
import { edgeTypes } from './registry';
import { WAIT_THEN_LABEL, WaitThenEdge } from './WaitThenEdge';

describe('WaitThenEdge', () => {
  it('uses an independent edge component with a then label', () => {
    expect(WAIT_THEN_LABEL).toBe('then');
    expect(edgeTypes.waitThen).toBe(WaitThenEdge);
    expect(edgeTypes.waitThen).not.toBe(edgeTypes.seq);
  });
});
