/**
 * 基线单文件「不存在」与「拉取失败」的区分，以及删除对账对失败的处理。
 *
 * 这里守的是一条数据安全线：任务启动前的资源对账会对「服务器没有 + 本地未改动」的文件
 * 删除本地副本。如果把一次断网 / 5xx 读成「服务器已删除」，用户的脚本、proto、连接配置
 * 会凭空消失。因此只有服务器明确答复 404 才允许走删除，其余失败一律跳过。
 */

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

import type { FlowJson } from '@/components/FlowEditor/codec/flowToJson';
import { fetchBaselineResource } from '../baselineApi';
import {
  BaselineFileFetchError,
  addScript,
  addScriptFromBaseline,
  applyConflictResolution,
  getScript,
} from '../resourcesStore';
import { checkTaskResourcesAgainstBaseline } from '../taskResourceDiff';

/** 只引用一个 lua 动作的最小 flow，保证对账范围里只有 login.lua。 */
function flowWithScript(script: string): FlowJson {
  return {
    nodes: {},
    actions: { doLogin: { pattern: 'lua', script } },
    listens: {},
  } as unknown as FlowJson;
}

beforeEach(() => {
  storage.databases.clear();
  vi.stubGlobal('fetch', vi.fn());
});

afterEach(() => {
  storage.databases.clear();
  vi.unstubAllGlobals();
});

describe('fetchBaselineResource 三态', () => {
  it('200 → ok，按资源分组拼出目录前缀并对文件名转义', async () => {
    const fetchMock = vi.mocked(globalThis.fetch);
    fetchMock.mockResolvedValueOnce(new Response('return true', { status: 200 }));
    await expect(fetchBaselineResource('script', 'a b.lua')).resolves.toEqual({
      status: 'ok',
      content: 'return true',
    });
    expect(fetchMock.mock.calls[0][0]).toBe('/sbot/baseline/scripts/a%20b.lua');
  });

  it('404 → missing（服务器权威答复「没有」）', async () => {
    vi.mocked(globalThis.fetch).mockResolvedValueOnce(new Response('', { status: 404 }));
    await expect(fetchBaselineResource('proto', 'gone.proto')).resolves.toEqual({
      status: 'missing',
    });
  });

  it('5xx 与网络异常 → error（服务器上有没有这个文件是未知的）', async () => {
    const fetchMock = vi.mocked(globalThis.fetch);
    fetchMock.mockResolvedValueOnce(new Response('boom', { status: 500 }));
    await expect(fetchBaselineResource('adapter', 'tcp_game_codec.json')).resolves.toEqual({
      status: 'error',
    });

    fetchMock.mockRejectedValueOnce(new TypeError('Failed to fetch'));
    await expect(fetchBaselineResource('adapter', 'tcp_game_codec.json')).resolves.toEqual({
      status: 'error',
    });
  });
});

describe('checkTaskResourcesAgainstBaseline 对拉取失败的处理', () => {
  it('拉取失败时跳过该文件，不删本地未改动副本', async () => {
    await addScriptFromBaseline('login.lua', 'return true');
    vi.mocked(globalThis.fetch).mockResolvedValue(new Response('boom', { status: 503 }));

    const result = await checkTaskResourcesAgainstBaseline(flowWithScript('login.lua'));

    expect(result.removed).toEqual([]);
    expect(result.unchanged).toEqual([]);
    await expect(getScript('login.lua')).resolves.toMatchObject({ content: 'return true' });
  });

  it('服务器答复 404 且本地未改动时，仍按「服务器已删除」清理本地副本', async () => {
    await addScriptFromBaseline('login.lua', 'return true');
    vi.mocked(globalThis.fetch).mockResolvedValue(new Response('', { status: 404 }));

    await checkTaskResourcesAgainstBaseline(flowWithScript('login.lua'));

    await expect(getScript('login.lua')).resolves.toBeUndefined();
  });

  it('服务器答复 404 但本地有改动时，进 removed 待用户确认而非直接删除', async () => {
    await addScriptFromBaseline('login.lua', 'return true');
    await addScript('login.lua', 'return false');
    vi.mocked(globalThis.fetch).mockResolvedValue(new Response('', { status: 404 }));

    const result = await checkTaskResourcesAgainstBaseline(flowWithScript('login.lua'));

    expect(result.removed).toMatchObject([{ type: 'script', name: 'login.lua' }]);
    await expect(getScript('login.lua')).resolves.toMatchObject({ content: 'return false' });
  });
});

describe('applyConflictResolution 对拉取失败的处理', () => {
  it('「采用服务器」遇到拉取失败时抛错中止，本地内容保持不变', async () => {
    await addScriptFromBaseline('login.lua', 'return true');
    await addScript('login.lua', 'return false');
    vi.mocked(globalThis.fetch).mockResolvedValue(new Response('boom', { status: 500 }));

    await expect(
      applyConflictResolution([{ type: 'script', name: 'login.lua', keepLocal: false }]),
    ).rejects.toThrow(BaselineFileFetchError);
    await expect(getScript('login.lua')).resolves.toMatchObject({ content: 'return false' });
  });

  it('「保留本地」遇到拉取失败时抛错中止，不把 baseHash 清成已对齐', async () => {
    const before = await addScriptFromBaseline('login.lua', 'return true');
    vi.mocked(globalThis.fetch).mockResolvedValue(new Response('boom', { status: 500 }));

    await expect(
      applyConflictResolution([{ type: 'script', name: 'login.lua', keepLocal: true }]),
    ).rejects.toThrow(BaselineFileFetchError);
    await expect(getScript('login.lua')).resolves.toMatchObject({ baseHash: before.baseHash });
  });
});
