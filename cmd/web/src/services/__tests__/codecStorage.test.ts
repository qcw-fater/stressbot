/**
 * 多 codec 存储 + baseline 封装单测。
 *
 * Node 环境无浏览器本地数据库，mock idb-keyval 为内存 Map（保留 createStore 句柄，但不实际打开数据库）。
 * 新 codec 文件 key 形如 `<protocol>_<service>_codec.json`，errors.json 单独存储。
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// ---- mock idb-keyval（内存 Map，多 store 共享一张表，按 store 实例隔离）----
const stores = new Map<unknown, Map<unknown, unknown>>();

vi.mock('idb-keyval', () => {
  function storeMap(store: unknown): Map<unknown, unknown> {
    let m = stores.get(store);
    if (!m) {
      m = new Map();
      stores.set(store, m);
    }
    return m;
  }
  return {
    createStore: vi.fn((db: string, _store: string) => ({ db, store: _store })),
    get: vi.fn(async (key: unknown, store: unknown) => storeMap(store).get(key)),
    set: vi.fn(async (key: unknown, value: unknown, store: unknown) => {
      storeMap(store).set(key, value);
    }),
    setMany: vi.fn(async (entries: Array<[unknown, unknown]>, store: unknown) => {
      const m = storeMap(store);
      for (const [k, v] of entries) m.set(k, v);
    }),
    del: vi.fn(async (key: unknown, store: unknown) => {
      storeMap(store).delete(key);
    }),
    keys: vi.fn(async (store: unknown) => Array.from(storeMap(store).keys())),
    clear: vi.fn(async (store: unknown) => {
      storeMap(store).clear();
    }),
  };
});

import {
  getCodecSchema,
  setCodecSchema,
  setCodecSchemaFromBaseline,
  clearCodecSchema,
  listCodecFiles,
  getErrorMap,
  setErrorMap,
  setErrorMapFromBaseline,
  clearErrorMap,
} from '../resourcesStore';
import { fetchBaselineCodecIndex, fetchBaselineCodec } from '../baselineApi';

beforeEach(() => {
  stores.clear();
});

afterEach(() => {
  stores.clear();
  vi.unstubAllGlobals();
});

describe('codec 多文件存储 round-trip', () => {
  it('setCodecSchema + getCodecSchema round-trip，baseHash 继承语义（local 编辑保留旧 baseHash）', async () => {
    // 先以 baseline 写入，baseHash = 内容 hash
    const baseline = await setCodecSchemaFromBaseline('tcp_logic_codec.json', '{"version":1}');
    expect(baseline.baseHash).toBeTruthy();
    const prevHash = baseline.baseHash;

    // local 编辑：baseHash 应保留
    const edited = await setCodecSchema('tcp_logic_codec.json', '{"version":1,"edited":true}');
    expect(edited.baseHash).toBe(prevHash);

    const got = await getCodecSchema('tcp_logic_codec.json');
    expect(got).toBeDefined();
    expect(got!.content).toBe('{"version":1,"edited":true}');
    expect(got!.baseHash).toBe(prevHash);
  });

  it('文件名校验：非 *_codec.json 名字抛中文错误', async () => {
    await expect(setCodecSchema('errors.json', '{}')).rejects.toThrow(/codec\.json/);
    await expect(getCodecSchema('errors.json')).rejects.toThrow(/codec\.json/);
    await expect(clearCodecSchema('errors.json')).rejects.toThrow(/codec\.json/);
    await expect(setCodecSchema('foo.txt', '{}')).rejects.toThrow(/codec\.json/);
  });

  it('listCodecFiles 只列 *_codec.json，不含 errors.json，按 name 排序', async () => {
    await setCodecSchemaFromBaseline('udp_battle_codec.json', '{}');
    await setCodecSchemaFromBaseline('tcp_logic_codec.json', '{}');
    await setErrorMapFromBaseline('{}'); // 共享错误表，不应出现在 listCodecFiles
    const list = await listCodecFiles();
    expect(list.map((f) => f.name)).toEqual(['tcp_logic_codec.json', 'udp_battle_codec.json']);
  });

  it('clearCodecSchema 删除单份，listCodecFiles 不再包含', async () => {
    await setCodecSchemaFromBaseline('tcp_logic_codec.json', '{}');
    await clearCodecSchema('tcp_logic_codec.json');
    expect(await getCodecSchema('tcp_logic_codec.json')).toBeUndefined();
    expect(await listCodecFiles()).toEqual([]);
  });
});

describe('共享 errors.json 错误表 round-trip', () => {
  it('setErrorMap + getErrorMap round-trip', async () => {
    const f = await setErrorMap('{"0":"成功"}');
    expect(f.name).toBe('errors.json');
    expect(f.size).toBeGreaterThan(0);
    const got = await getErrorMap();
    expect(got).toBeDefined();
    expect(got!.content).toBe('{"0":"成功"}');
  });

  it('setErrorMapFromBaseline 写 baseHash = 内容 hash', async () => {
    const f = await setErrorMapFromBaseline('{"1":"错误"}');
    expect(f.baseHash).toBeTruthy();
  });

  it('clearErrorMap 删除', async () => {
    await setErrorMap('{}');
    await clearErrorMap();
    expect(await getErrorMap()).toBeUndefined();
  });
});

describe('baseline codec 封装', () => {
  beforeEach(() => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
  });

  it('fetchBaselineCodecIndex 命中 /sbot/baseline/adapter/index.json，空时返回 []', async () => {
    const fetchMock = vi.mocked(globalThis.fetch);
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify(['tcp_logic_codec.json', 'errors.json']), { status: 200 }),
    );
    const idx = await fetchBaselineCodecIndex();
    expect(idx).toEqual(['tcp_logic_codec.json', 'errors.json']);
    expect(fetchMock.mock.calls[0][0]).toBe('/sbot/baseline/adapter/index.json');
  });

  it('fetchBaselineCodecIndex fetch 失败返回 []', async () => {
    const fetchMock = vi.mocked(globalThis.fetch);
    fetchMock.mockResolvedValueOnce(new Response('Not Found', { status: 404 }));
    expect(await fetchBaselineCodecIndex()).toEqual([]);
  });

  it('fetchBaselineCodec 命中 /sbot/baseline/adapter/{name}，encodeURIComponent 生效', async () => {
    const fetchMock = vi.mocked(globalThis.fetch);
    fetchMock.mockResolvedValueOnce(new Response('{"version":1}', { status: 200 }));
    const content = await fetchBaselineCodec('tcp_logic_codec.json');
    expect(content).toBe('{"version":1}');
    expect(fetchMock.mock.calls[0][0]).toBe('/sbot/baseline/adapter/tcp_logic_codec.json');
  });

  it('fetchBaselineCodec 404/失败返回 null', async () => {
    const fetchMock = vi.mocked(globalThis.fetch);
    fetchMock.mockResolvedValueOnce(new Response('Not Found', { status: 404 }));
    expect(await fetchBaselineCodec('missing.json')).toBeNull();
  });
});
