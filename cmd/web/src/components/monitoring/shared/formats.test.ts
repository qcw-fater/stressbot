import { describe, expect, it } from 'vitest';
import { fmtBandwidthBytesPerSec, fmtByteSize, fmtMs } from './formats';

describe('fmtMs', () => {
  it('缺数据显示 —，如实测到的零显示 0', () => {
    expect(fmtMs(null)).toBe('—');
    expect(fmtMs(undefined)).toBe('—');
    expect(fmtMs(Number.NaN)).toBe('—');
    expect(fmtMs(-1)).toBe('—');
    expect(fmtMs(0)).toBe('0');
  });

  it('按量级选精度', () => {
    expect(fmtMs(0.123)).toBe('0.12');
    expect(fmtMs(3.45)).toBe('3.5');
    expect(fmtMs(123.4)).toBe('123');
  });
});

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
