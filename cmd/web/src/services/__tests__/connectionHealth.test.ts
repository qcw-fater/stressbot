import { describe, expect, it } from 'vitest';
import { updateFailedSources } from '../connectionHealth';

describe('updateFailedSources', () => {
  it('keeps the connection lost while any polling source is still failing', () => {
    let failed = new Set<string>();
    failed = updateFailedSources(failed, 'task', true);
    failed = updateFailedSources(failed, 'metrics', true);
    failed = updateFailedSources(failed, 'task', false);

    expect([...failed]).toEqual(['metrics']);
  });

  it('clears the lost state after every failed source recovers', () => {
    let failed = new Set(['task', 'metrics']);
    failed = updateFailedSources(failed, 'task', false);
    failed = updateFailedSources(failed, 'metrics', false);

    expect(failed.size).toBe(0);
  });

  it('does not mutate the previous set', () => {
    const previous = new Set(['task']);
    const next = updateFailedSources(previous, 'metrics', true);

    expect([...previous]).toEqual(['task']);
    expect([...next]).toEqual(['task', 'metrics']);
  });
});
