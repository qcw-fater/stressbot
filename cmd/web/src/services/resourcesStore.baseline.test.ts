import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const storage = vi.hoisted(() => {
  const databases = new Map<string, Map<IDBValidKey, unknown>>();
  const database = (store: string) => {
    let values = databases.get(store);
    if (!values) {
      values = new Map();
      databases.set(store, values);
    }
    return values;
  };
  return { databases, database };
});

const baseline = vi.hoisted(() => ({
  fetchBaselineProtoIndex: vi.fn(),
  fetchBaselineScriptIndex: vi.fn(),
  fetchBaselineCodecIndex: vi.fn(),
  fetchBaselineProtoContent: vi.fn(),
  fetchBaselineScript: vi.fn(),
  fetchBaselineCodec: vi.fn(),
  fetchBaselineResource: vi.fn(),
}));

vi.mock('idb-keyval', () => ({
  createStore: (databaseName: string) => databaseName,
  clear: async (store: string) => storage.database(store).clear(),
  del: async (key: IDBValidKey, store: string) => storage.database(store).delete(key),
  delMany: async (keys: IDBValidKey[], store: string) => {
    for (const key of keys) storage.database(store).delete(key);
  },
  get: async (key: IDBValidKey, store: string) => storage.database(store).get(key),
  getMany: async (keys: IDBValidKey[], store: string) =>
    keys.map((key) => storage.database(store).get(key)),
  keys: async (store: string) => Array.from(storage.database(store).keys()),
  set: async (key: IDBValidKey, value: unknown, store: string) => {
    storage.database(store).set(key, value);
  },
  setMany: async (entries: Array<[IDBValidKey, unknown]>, store: string) => {
    for (const [key, value] of entries) storage.database(store).set(key, value);
  },
}));

vi.mock('./baselineApi', () => baseline);

import {
  getCodecSchema,
  getProto,
  getScript,
  subscribe,
  syncResourcesFromBaseline,
} from './resourcesStore';

function response(body: string[] | string): Response {
  return {
    ok: true,
    status: 200,
    json: vi.fn().mockResolvedValue(body),
    text: vi.fn().mockResolvedValue(body),
  } as unknown as Response;
}

beforeEach(() => {
  vi.clearAllMocks();
  storage.databases.clear();
  localStorage.clear();
  baseline.fetchBaselineProtoIndex.mockResolvedValue(['game.proto']);
  baseline.fetchBaselineScriptIndex.mockResolvedValue(['login.lua']);
  baseline.fetchBaselineCodecIndex.mockResolvedValue(['tcp_game_codec.json']);
  baseline.fetchBaselineProtoContent.mockResolvedValue('syntax = "proto3";');
  baseline.fetchBaselineScript.mockResolvedValue('return true');
  baseline.fetchBaselineCodec.mockResolvedValue('{"version":1}');
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('syncResourcesFromBaseline', () => {
  it('notifies resource subscribers once for one completed batch', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.endsWith('/proto/index.json')) return response(['game.proto']);
        if (url.endsWith('/scripts/index.json')) return response(['login.lua']);
        if (url.endsWith('/proto/game.proto')) return response('syntax = "proto3";');
        if (url.endsWith('/scripts/login.lua')) return response('return true');
        throw new Error(`unexpected fetch: ${url}`);
      }),
    );
    const listener = vi.fn();
    const unsubscribe = subscribe(listener);

    try {
      await syncResourcesFromBaseline();
    } finally {
      unsubscribe();
    }

    expect(listener).toHaveBeenCalledTimes(1);
    await expect(getProto('game.proto')).resolves.toMatchObject({ content: 'syntax = "proto3";' });
    await expect(getScript('login.lua')).resolves.toMatchObject({ content: 'return true' });
    await expect(getCodecSchema('tcp_game_codec.json')).resolves.toMatchObject({
      content: '{"version":1}',
    });
  });

  it('reads every baseline resource through baselineApi', async () => {
    const directFetch = vi.fn(() => {
      throw new Error('resourcesStore must not fetch directly');
    });
    vi.stubGlobal('fetch', directFetch);

    await expect(syncResourcesFromBaseline()).resolves.toMatchObject({
      added: [
        { type: 'proto', name: 'game.proto' },
        { type: 'script', name: 'login.lua' },
        { type: 'adapter', name: 'tcp_game_codec.json' },
      ],
    });

    expect(directFetch).not.toHaveBeenCalled();
    expect(baseline.fetchBaselineProtoIndex).toHaveBeenCalledTimes(1);
    expect(baseline.fetchBaselineScriptIndex).toHaveBeenCalledTimes(1);
    expect(baseline.fetchBaselineCodecIndex).toHaveBeenCalledTimes(1);
    expect(baseline.fetchBaselineProtoContent).toHaveBeenCalledWith('game.proto');
    expect(baseline.fetchBaselineScript).toHaveBeenCalledWith('login.lua');
    expect(baseline.fetchBaselineCodec).toHaveBeenCalledWith('tcp_game_codec.json');
  });
});
