import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from './api';
import {
  actionTemplatesApi,
  listenTemplatesApi,
  type ActionTemplateDto,
  type ListenTemplateDto,
} from './templatesApi';

const fetchMock = vi.fn();

function response(status: number, body?: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: status >= 200 && status < 300 ? 'OK' : `HTTP ${status}`,
    json: async () => body,
  } as Response;
}

const action: ActionTemplateDto = {
  id: 'action/1',
  name: '登录',
  pattern: 'tcpRequest',
  data: { pattern: 'tcpRequest' } as ActionTemplateDto['data'],
  createdAt: '2026-07-29T08:00:00Z',
  updatedAt: '2026-07-29T09:00:00Z',
};

const listen: ListenTemplateDto = {
  id: 'listen/1',
  name: '消息监听',
  kind: 'silent',
  data: {} as ListenTemplateDto['data'],
  createdAt: '2026-07-29T08:00:00Z',
  updatedAt: '2026-07-29T09:00:00Z',
};

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => vi.unstubAllGlobals());

describe('actionTemplatesApi', () => {
  it('通过集中请求封装访问 CRUD，并编码路径中的 ID', async () => {
    fetchMock
      .mockResolvedValueOnce(response(200, [action]))
      .mockResolvedValueOnce(response(200, action))
      .mockResolvedValueOnce(response(201, action))
      .mockResolvedValueOnce(response(200, action))
      .mockResolvedValueOnce(response(204));

    await actionTemplatesApi.list();
    await actionTemplatesApi.get(action.id);
    await actionTemplatesApi.create({
      name: action.name,
      pattern: action.pattern,
      data: action.data,
    });
    await actionTemplatesApi.update(action.id, {
      name: action.name,
      pattern: action.pattern,
      data: action.data,
    });
    await actionTemplatesApi.delete(action.id);

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      '/sbot/action-templates',
      '/sbot/action-templates/action%2F1',
      '/sbot/action-templates',
      '/sbot/action-templates/action%2F1',
      '/sbot/action-templates/action%2F1',
    ]);
    expect(fetchMock.mock.calls.map(([, init]) => init.method)).toEqual([
      'GET',
      'GET',
      'POST',
      'PUT',
      'DELETE',
    ]);
  });

  it('读写独立快照并原样返回服务器持久化结果', async () => {
    const snapshot = { revision: 'sha256:old', items: [action] };
    const replaced = { revision: 'sha256:new', count: 1, items: [action] };
    fetchMock
      .mockResolvedValueOnce(response(200, snapshot))
      .mockResolvedValueOnce(response(200, replaced));

    await expect(actionTemplatesApi.getSnapshot()).resolves.toEqual(snapshot);
    await expect(actionTemplatesApi.replaceSnapshot({
      expectedRevision: 'sha256:old',
      idPolicy: 'preserve',
      items: [action],
    })).resolves.toEqual(replaced);

    expect(fetchMock.mock.calls[0][0]).toBe('/sbot/action-templates/snapshot');
    expect(fetchMock.mock.calls[1][0]).toBe('/sbot/action-templates/snapshot');
    expect(fetchMock.mock.calls[1][1].method).toBe('PUT');
    expect(JSON.parse(fetchMock.mock.calls[1][1].body as string)).toEqual({
      expectedRevision: 'sha256:old',
      idPolicy: 'preserve',
      items: [action],
    });
  });

  it('保留集中请求层抛出的结构化错误', async () => {
    fetchMock.mockResolvedValueOnce(response(503, {
      code: 'TEMPLATE_LIBRARY_DISABLED',
      message: '服务器未启用模板库',
    }));

    const error = await actionTemplatesApi.list().catch((value) => value);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ code: 'TEMPLATE_LIBRARY_DISABLED', status: 503 });
  });
});

describe('listenTemplatesApi', () => {
  it('覆盖 CRUD 与独立快照端点', async () => {
    fetchMock
      .mockResolvedValueOnce(response(200, [listen]))
      .mockResolvedValueOnce(response(200, listen))
      .mockResolvedValueOnce(response(201, listen))
      .mockResolvedValueOnce(response(200, listen))
      .mockResolvedValueOnce(response(204))
      .mockResolvedValueOnce(response(200, { revision: 'r1', items: [listen] }))
      .mockResolvedValueOnce(response(200, { revision: 'r2', count: 1, items: [listen] }));

    await listenTemplatesApi.list();
    await listenTemplatesApi.get(listen.id);
    await listenTemplatesApi.create({ name: listen.name, kind: listen.kind, data: listen.data });
    await listenTemplatesApi.update(listen.id, {
      name: listen.name,
      kind: listen.kind,
      data: listen.data,
    });
    await listenTemplatesApi.delete(listen.id);
    await listenTemplatesApi.getSnapshot();
    await listenTemplatesApi.replaceSnapshot({
      expectedRevision: 'r1',
      idPolicy: 'generate-missing',
      items: [{ name: listen.name, kind: listen.kind, data: listen.data }],
    });

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      '/sbot/listen-templates',
      '/sbot/listen-templates/listen%2F1',
      '/sbot/listen-templates',
      '/sbot/listen-templates/listen%2F1',
      '/sbot/listen-templates/listen%2F1',
      '/sbot/listen-templates/snapshot',
      '/sbot/listen-templates/snapshot',
    ]);
  });
});
