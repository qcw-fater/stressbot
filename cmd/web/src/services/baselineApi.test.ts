import { afterEach, describe, expect, it, vi } from 'vitest';
import { fetchBaselineProtoIndex } from './baselineApi';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('baseline index validation', () => {
  it('returns null when an index response is not a string array', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        json: vi.fn().mockResolvedValue({ files: ['game.proto'] }),
      }),
    );

    await expect(fetchBaselineProtoIndex()).resolves.toBeNull();
  });
});
