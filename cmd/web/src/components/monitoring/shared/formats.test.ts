import { describe, expect, it } from 'vitest';
import { fmtBandwidthBytesPerSec, fmtByteSize } from './formats';

describe('resource formatters', () => {
  it('distinguishes an unavailable rate from a measured zero rate', () => {
    expect(fmtBandwidthBytesPerSec(null)).toBe('—');
    expect(fmtBandwidthBytesPerSec(0)).toBe('0 B/s');
    expect(fmtBandwidthBytesPerSec(1024)).toBe('1.00 KB/s');
  });

  it('formats exact byte values without turning zero into missing data', () => {
    expect(fmtByteSize(null)).toBe('—');
    expect(fmtByteSize(0)).toBe('0 B');
    expect(fmtByteSize(1024 * 1024)).toBe('1.00 MB');
  });
});
