import { describe, expect, it } from 'vitest';
import { getTimingBreakdownFields } from './ActionMetricsTable';

describe('getTimingBreakdownFields', () => {
  it('does not expose the send timing as a table column', () => {
    expect(getTimingBreakdownFields('rtt')).toEqual(['nonRTTAvgMs']);
    expect(getTimingBreakdownFields('codec')).toEqual(['nonRTTAvgMs', 'encodeAvgMs', 'decodeAvgMs']);
    expect(getTimingBreakdownFields('full')).toEqual([
      'nonRTTAvgMs',
      'encodeAvgMs',
      'decodeAvgMs',
      'buildAvgMs',
      'decodeWaitAvgMs',
      'dispatchToActionWaitAvgMs',
      'parseStoreAvgMs',
    ]);
  });
});
