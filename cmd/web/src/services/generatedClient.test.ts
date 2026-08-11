import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from './api';
import { getCapabilities } from './capabilitiesApi';

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => vi.unstubAllGlobals());

describe('generated management API client', () => {
  it('uses the generated path and response type', async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          sharedState: true,
          sharedAddr: '***:6379',
          flowLibrary: true,
          templateLibrary: false,
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    );

    await expect(getCapabilities()).resolves.toEqual({
      sharedState: true,
      sharedAddr: '***:6379',
      flowLibrary: true,
      templateLibrary: false,
    });
    const request = fetchMock.mock.calls[0][0] as Request;
    expect(new URL(request.url).pathname).toBe('/sbot/capabilities');
    expect(request.method).toBe('GET');
  });

  it('maps generated error responses to the existing ApiError contract', async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          code: 'INTERNAL_ERROR',
          message: '服务器异常',
          details: { traceId: 'trace-1' },
        }),
        {
          status: 500,
          statusText: 'Internal Server Error',
          headers: { 'Content-Type': 'application/json' },
        },
      ),
    );

    const error = await getCapabilities().catch((value) => value);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({
      code: 'INTERNAL_ERROR',
      status: 500,
      details: { traceId: 'trace-1' },
    });
  });

  it('preserves AbortError so callers can distinguish cancellation from network failure', async () => {
    const abort = new DOMException('aborted', 'AbortError');
    fetchMock.mockRejectedValueOnce(abort);
    const controller = new AbortController();

    await expect(getCapabilities({ signal: controller.signal })).rejects.toBe(abort);
  });
});
