import { describe, expect, it } from 'vitest';
import { LatestRequestGate } from './latestRequestGate';

describe('LatestRequestGate', () => {
  it('新目标会取消旧请求，且只有最新请求可以提交结果', () => {
    const gate = new LatestRequestGate();
    const first = gate.begin('agent-a');
    const second = gate.begin('agent-b');

    expect(first.signal.aborted).toBe(true);
    expect(gate.isCurrent(first, 'agent-a')).toBe(false);
    expect(second.signal.aborted).toBe(false);
    expect(gate.isCurrent(second, 'agent-b')).toBe(true);

    gate.cancel();
    expect(second.signal.aborted).toBe(true);
    expect(gate.isCurrent(second, 'agent-b')).toBe(false);
  });
});
