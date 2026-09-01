import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  getJson: vi.fn(),
}));

vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>();
  return { ...actual, getJson: mocks.getJson };
});

import { compareHistory, getHistory, getHistoryConfig, getHistoryTimeseries } from './historyApi';

beforeEach(() => {
  mocks.getJson.mockReset().mockResolvedValue({});
});

describe('history query cancellation', () => {
  it('forwards RequestInit through detail queries', async () => {
    const init = { signal: new AbortController().signal };

    await getHistory('history/1', 2, init);
    await getHistoryTimeseries('history/1', 1200, 2, init);
    await getHistoryConfig('history/1', init);

    expect(mocks.getJson.mock.calls).toEqual([
      ['/history/history%2F1?stageIndex=2', init],
      ['/history/history%2F1/timeseries?maxPoints=1200&stageIndex=2', init],
      ['/history/history%2F1/config', init],
    ]);
  });

  it('forwards RequestInit through compare queries', async () => {
    const init = { signal: new AbortController().signal };

    await compareHistory(['first', 'second'], init);

    expect(mocks.getJson).toHaveBeenCalledWith('/history/compare?ids=first%2Csecond', init);
  });
});
