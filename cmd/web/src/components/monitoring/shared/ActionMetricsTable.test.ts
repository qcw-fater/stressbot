import { describe, expect, it } from 'vitest';
import { getTimingBreakdownFields } from './ActionMetricsTable';

describe('getTimingBreakdownFields', () => {
  it('only exposes timing phases collected by the configured detail level', () => {
    expect(getTimingBreakdownFields('rtt')).toEqual(['nonRTTAvgMs', 'sendAvgMs']);
    expect(getTimingBreakdownFields('codec')).toEqual([
      'nonRTTAvgMs',
      'sendAvgMs',
      'encodeAvgMs',
      'decodeAvgMs',
    ]);
    expect(getTimingBreakdownFields('full')).toEqual([
      'nonRTTAvgMs',
      'sendAvgMs',
      'encodeAvgMs',
      'decodeAvgMs',
      'buildAvgMs',
      'decodeWaitAvgMs',
      'dispatchToActionWaitAvgMs',
      'parseStoreAvgMs',
    ]);
  });
});
