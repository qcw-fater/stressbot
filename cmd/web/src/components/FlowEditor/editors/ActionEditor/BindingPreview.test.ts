import { describe, expect, it, vi } from 'vitest';
import { simulateBinding } from './BindingPreview';

describe('simulateBinding randomString preview', () => {
  it('uses upper charset alias for samples', () => {
    vi.spyOn(Math, 'random').mockReturnValue(0);

    const result = simulateBinding({ type: 'randomString', length: 4, charset: 'upper' });

    expect(result.kind).toBe('concrete');
    if (result.kind === 'concrete') {
      expect(result.display).toContain('"AAAA"');
      expect(result.display).toContain('charset=upper alias');
    }

    vi.restoreAllMocks();
  });

  it('uses custom literal charset for samples', () => {
    vi.spyOn(Math, 'random').mockReturnValue(0.99);

    const result = simulateBinding({ type: 'randomString', length: 3, charset: 'XY' });

    expect(result.kind).toBe('concrete');
    if (result.kind === 'concrete') {
      expect(result.display).toContain('"YYY"');
      expect(result.display).toContain('charset=custom literal');
    }

    vi.restoreAllMocks();
  });
});
