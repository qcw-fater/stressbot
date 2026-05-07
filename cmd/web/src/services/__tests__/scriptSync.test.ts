/**
 * scriptSync 单测：扫描 + 同步语义（IDB 已有不覆盖、缺失收集、混合场景）。
 *
 * 不依赖真实 IndexedDB —— 用 vi.mock 把 resourcesStore 的 getScript / addScript 替成内存 Map。
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { TaskFlow } from '@/types/flow';

// ---- mock ----
const idb = new Map<string, { name: string; content: string }>();

vi.mock('../resourcesStore', () => ({
  getScript: vi.fn(async (name: string) => idb.get(name)),
  addScript: vi.fn(async (name: string, content: string) => {
    const file = { name, content, size: content.length, uploadedAt: '2026-01-01T00:00:00Z' };
    idb.set(name, file);
    return file;
  }),
}));

// ---- import after mock ----
import { collectFlowScriptNames, syncFlowScriptsToIdb } from '../scriptSync';

const fetchMock = vi.fn();

beforeEach(() => {
  idb.clear();
  fetchMock.mockReset();
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function buildFlow(opts: {
  actions?: Record<string, { script?: string }>;
  callbacks?: Record<string, { script?: string }>;
}): TaskFlow {
  return {
    defaultDelayMs: 1000,
    nodes: {},
    actions: (opts.actions ?? {}) as TaskFlow['actions'],
    callbacks: (opts.callbacks ?? {}) as TaskFlow['callbacks'],
  };
}

describe('collectFlowScriptNames', () => {
  it('扫描 actions + callbacks 中所有 script 字段并去重排序', () => {
    const flow = buildFlow({
      actions: {
        a1: { script: 'foo.lua' },
        a2: { script: 'bar.lua' },
        a3: {},
        a4: { script: 'foo.lua' }, // 重复
      },
      callbacks: {
        c1: { script: 'cb.lua' },
        c2: {},
        c3: { script: 'bar.lua' }, // 跨 actions/callbacks 重复
      },
    });
    expect(collectFlowScriptNames(flow)).toEqual(['bar.lua', 'cb.lua', 'foo.lua']);
  });

  it('flow 没有任何脚本引用时返回空数组', () => {
    expect(collectFlowScriptNames(buildFlow({}))).toEqual([]);
  });

  it('容忍 actions / callbacks 为 undefined（旧数据兼容）', () => {
    const flow = { defaultDelayMs: 1000, nodes: {} } as unknown as TaskFlow;
    expect(collectFlowScriptNames(flow)).toEqual([]);
  });
});

describe('syncFlowScriptsToIdb', () => {
  it('IDB 已有的脚本不会再 fetch、不会被覆盖', async () => {
    idb.set('keep.lua', { name: 'keep.lua', content: '-- user edit' });
    const flow = buildFlow({ actions: { a: { script: 'keep.lua' } } });

    const r = await syncFlowScriptsToIdb(flow);

    expect(r).toEqual({ added: [], skipped: ['keep.lua'], missing: [] });
    expect(fetchMock).not.toHaveBeenCalled();
    expect(idb.get('keep.lua')!.content).toBe('-- user edit'); // 内容未变
  });

  it('IDB 缺失的脚本从默认基线拉回并写 IDB', async () => {
    fetchMock.mockResolvedValueOnce({
      ok: true,
      text: async () => '-- baseline content',
    });
    const flow = buildFlow({ callbacks: { c: { script: 'baseline.lua' } } });

    const r = await syncFlowScriptsToIdb(flow);

    expect(r).toEqual({ added: ['baseline.lua'], skipped: [], missing: [] });
    expect(fetchMock).toHaveBeenCalledWith('/conf/scripts/baseline.lua');
    expect(idb.get('baseline.lua')?.content).toBe('-- baseline content');
  });

  it('basic baseUrl 可配置，名字会做 URL 编码', async () => {
    fetchMock.mockResolvedValueOnce({ ok: true, text: async () => 'x' });
    const flow = buildFlow({ actions: { a: { script: 'has space.lua' } } });

    await syncFlowScriptsToIdb(flow, '/custom/');

    expect(fetchMock).toHaveBeenCalledWith('/custom/has%20space.lua');
  });

  it('fetch 404 / 网络异常的脚本进入 missing，不写 IDB', async () => {
    fetchMock
      .mockResolvedValueOnce({ ok: false, status: 404 })
      .mockRejectedValueOnce(new Error('network'));
    const flow = buildFlow({
      actions: { a1: { script: 'gone.lua' }, a2: { script: 'crash.lua' } },
    });

    const r = await syncFlowScriptsToIdb(flow);

    expect(r.added).toEqual([]);
    expect(r.skipped).toEqual([]);
    expect(r.missing).toEqual(['crash.lua', 'gone.lua']);
    expect(idb.size).toBe(0);
  });

  it('混合场景：部分已在 IDB、部分需拉、部分缺失', async () => {
    idb.set('a.lua', { name: 'a.lua', content: 'A' });
    fetchMock.mockImplementation(async (url: string) => {
      if (url.endsWith('b.lua')) return { ok: true, text: async () => 'B-baseline' };
      return { ok: false };
    });
    const flow = buildFlow({
      actions: {
        a1: { script: 'a.lua' },
        a2: { script: 'b.lua' },
        a3: { script: 'c.lua' },
      },
    });

    const r = await syncFlowScriptsToIdb(flow);

    expect(r.skipped).toEqual(['a.lua']);
    expect(r.added).toEqual(['b.lua']);
    expect(r.missing).toEqual(['c.lua']);
    expect(idb.get('a.lua')!.content).toBe('A');
    expect(idb.get('b.lua')!.content).toBe('B-baseline');
    expect(idb.has('c.lua')).toBe(false);
  });
});
