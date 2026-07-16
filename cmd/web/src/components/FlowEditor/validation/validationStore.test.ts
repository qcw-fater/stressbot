import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createValidationScheduler } from './validationStore';
import type { FlowValidationContext } from './refsCheck';
import type { TaskFlow } from '@/types/flow';

const makeFlow = (delay: number): TaskFlow => ({
  defaultDelayMs: delay,
  nodes: {},
  actions: {},
  listens: {},
});

describe('validation scheduler', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('runs once with the latest snapshot after 150ms of inactivity', () => {
    const run = vi.fn();
    const scheduler = createValidationScheduler(run, 150);
    const context: FlowValidationContext = { stateKeysReady: false };

    scheduler.schedule(makeFlow(100), context);
    vi.advanceTimersByTime(100);
    scheduler.schedule(makeFlow(200), context);
    vi.advanceTimersByTime(149);
    expect(run).not.toHaveBeenCalled();

    vi.advanceTimersByTime(1);
    expect(run).toHaveBeenCalledOnce();
    expect(run.mock.calls[0][0].defaultDelayMs).toBe(200);
  });

  it('flushes the latest snapshot synchronously and cancels the timer', () => {
    const run = vi.fn();
    const scheduler = createValidationScheduler(run, 150);
    const context: FlowValidationContext = { stateKeysReady: true };

    scheduler.schedule(makeFlow(100), context);
    scheduler.flush(makeFlow(300), context);
    vi.advanceTimersByTime(150);

    expect(run).toHaveBeenCalledOnce();
    expect(run.mock.calls[0][0].defaultDelayMs).toBe(300);
  });

  it('cancels pending validation on cleanup', () => {
    const run = vi.fn();
    const scheduler = createValidationScheduler(run, 150);

    scheduler.schedule(makeFlow(100), {});
    scheduler.cancel();
    vi.advanceTimersByTime(150);

    expect(run).not.toHaveBeenCalled();
  });
});
