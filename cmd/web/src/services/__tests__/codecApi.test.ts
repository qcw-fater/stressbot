/**
 * T3 Batch-3 任务 A — codecApi 单测。
 *
 * 断言：
 *   - fetchCodecAlgorithms：GET /sbot/codec/algorithms（经 API_PREFIX 拼接），返回 AlgoMeta[]。
 *   - previewCodec：POST /sbot/codec/preview，请求体 JSON 化；**HTTP 200 即使 result.error 非空也照常返回**
 *     （编辑器语义）；HTTP 非 2xx 抛中文 Error。
 *
 * codecApi 内部走 services/api.ts 的 getJson/postJson（自动加 API_PREFIX='/sbot' + 处理非 2xx），
 * 因此这里 mock 全局 fetch、断言落到「URL / 方法 / 请求体 / 响应解析」。
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fetchCodecAlgorithms, previewCodec } from '../codecApi';
import type { PreviewRequest } from '@/types/codec';

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

/** 构造一个最小可用的 Response-like 对象（足够 services/api.ts 读 status/json）。 */
function mockResponse(opts: { ok: boolean; status: number; body: unknown }): Response {
  const text = JSON.stringify(opts.body);
  return {
    ok: opts.ok,
    status: opts.status,
    statusText: opts.ok ? 'OK' : `HTTP ${opts.status}`,
    headers: new Headers({ 'Content-Type': 'application/json' }),
    json: async () => JSON.parse(text),
    text: async () => text,
  } as Response;
}

describe('fetchCodecAlgorithms', () => {
  it('GET /sbot/codec/algorithms 返回 AlgoMeta[]', async () => {
    const algos = [
      { name: 'aes', op: 'cipher', params: [{ name: 'keyLen', type: 'int', default: 16 }] },
      { name: 'gzip', op: 'compress' },
    ];
    fetchMock.mockResolvedValueOnce(mockResponse({ ok: true, status: 200, body: algos }));

    const r = await fetchCodecAlgorithms();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/sbot/codec/algorithms');
    expect(init.method).toBe('GET');
    expect(r).toEqual(algos);
    expect(r[0].op).toBe('cipher');
  });

  it('空清单时返回空数组（不报错、不伪清单兜底）', async () => {
    fetchMock.mockResolvedValueOnce(mockResponse({ ok: true, status: 200, body: [] }));
    const r = await fetchCodecAlgorithms();
    expect(r).toEqual([]);
  });

  it('HTTP 非 2xx 抛中文 Error（含原 message）', async () => {
    fetchMock.mockResolvedValueOnce(
      mockResponse({
        ok: false,
        status: 500,
        body: { code: 'HTTP_ERROR', message: 'Internal Server Error' },
      }),
    );
    await expect(fetchCodecAlgorithms()).rejects.toThrow(/算法清单加载失败/);
  });

  it('网络异常抛中文 Error', async () => {
    fetchMock.mockRejectedValueOnce(new Error('network down'));
    await expect(fetchCodecAlgorithms()).rejects.toThrow(/算法清单加载失败/);
  });
});

describe('previewCodec', () => {
  const req: PreviewRequest = {
    schema: { version: 1, endianDefault: 'le' },
    mode: 'encode',
    bodyHex: 'deadbeef',
  };

  it('POST /sbot/codec/preview，请求体 JSON 化，返回 PreviewResult', async () => {
    const result = { mode: 'encode', frameHex: 'cafe' };
    fetchMock.mockResolvedValueOnce(mockResponse({ ok: true, status: 200, body: result }));

    const r = await previewCodec(req);

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/sbot/codec/preview');
    expect(init.method).toBe('POST');
    // 请求体是 JSON 字符串，包含 schema 对象 + mode + bodyHex。
    const sent = JSON.parse(init.body as string);
    expect(sent.mode).toBe('encode');
    expect(sent.bodyHex).toBe('deadbeef');
    expect(sent.schema.version).toBe(1);
    expect(r).toEqual(result);
  });

  it('HTTP 200 且 result.error 非空时照常返回 result（不抛）', async () => {
    const result = { mode: 'encode', error: 'schema 编译失败：xxx' };
    fetchMock.mockResolvedValueOnce(mockResponse({ ok: true, status: 200, body: result }));

    const r = await previewCodec(req);
    expect(r).toEqual(result);
    expect(r.error).toBe('schema 编译失败：xxx');
  });

  it('HTTP 非 2xx（坏 schema/坏 JSON）抛中文 Error', async () => {
    fetchMock.mockResolvedValueOnce(
      mockResponse({
        ok: false,
        status: 400,
        body: { code: 'INVALID_ARGUMENT', message: 'invalid schema: ...' },
      }),
    );
    await expect(previewCodec(req)).rejects.toThrow(/预览失败/);
  });
});
