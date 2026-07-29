import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { ActionTemplateDto, ListenTemplateDto } from '@/services/templatesApi';

const api = vi.hoisted(() => ({
  action: {
    list: vi.fn(), get: vi.fn(), create: vi.fn(), update: vi.fn(), delete: vi.fn(),
    getSnapshot: vi.fn(), replaceSnapshot: vi.fn(),
  },
  listen: {
    list: vi.fn(), get: vi.fn(), create: vi.fn(), update: vi.fn(), delete: vi.fn(),
    getSnapshot: vi.fn(), replaceSnapshot: vi.fn(),
  },
}));

vi.mock('@/services/templatesApi', () => ({
  actionTemplatesApi: api.action,
  listenTemplatesApi: api.listen,
}));

import {
  findActionTemplateByName,
  getActionTemplateSnapshot,
  getListenTemplateSnapshot,
  listActionTemplates,
  onTemplateChange,
  replaceActionTemplateSnapshot,
  replaceListenTemplateSnapshot,
  saveActionTemplate,
  updateActionTemplate,
} from './templateStore';

const actionDto: ActionTemplateDto = {
  id: 'server-action',
  name: '登录',
  description: '请求登录',
  pattern: 'tcpRequest',
  data: { pattern: 'tcpRequest' } as ActionTemplateDto['data'],
  createdAt: '2026-07-29T08:00:00.000Z',
  updatedAt: '2026-07-29T09:00:00.000Z',
};

const listenDto: ListenTemplateDto = {
  id: 'server-listen',
  name: '推送',
  kind: 'silent',
  data: {} as ListenTemplateDto['data'],
  createdAt: '2026-07-29T08:00:00.000Z',
  updatedAt: '2026-07-29T09:00:00.000Z',
};

beforeEach(() => {
  vi.clearAllMocks();
  api.action.list.mockResolvedValue([actionDto]);
  api.listen.list.mockResolvedValue([listenDto]);
});

describe('templateStore 服务器门面', () => {
  it('把 RFC 3339 时间转换为组件使用的毫秒数，并保留 updatedAt', async () => {
    await expect(listActionTemplates()).resolves.toEqual([{
      ...actionDto,
      createdAt: Date.parse(actionDto.createdAt),
      updatedAt: Date.parse(actionDto.updatedAt),
    }]);
  });

  it('按名称精确匹配，大小写不同不视为同名', async () => {
    await expect(findActionTemplateByName('登录')).resolves.toMatchObject({ id: 'server-action' });
    await expect(findActionTemplateByName('登录 ')).resolves.toBeUndefined();
  });

  it('保存成功后才广播变更，并使用服务器生成的身份信息', async () => {
    const changed = vi.fn();
    const off = onTemplateChange(changed);
    api.action.create.mockResolvedValue(actionDto);

    await expect(saveActionTemplate({
      name: actionDto.name,
      description: actionDto.description,
      pattern: actionDto.pattern,
      data: actionDto.data,
    })).resolves.toMatchObject({
      id: actionDto.id,
      createdAt: Date.parse(actionDto.createdAt),
      updatedAt: Date.parse(actionDto.updatedAt),
    });
    expect(api.action.create).toHaveBeenCalledWith({
      name: actionDto.name,
      description: actionDto.description,
      pattern: actionDto.pattern,
      data: actionDto.data,
    });
    expect(changed).toHaveBeenCalledTimes(1);

    api.action.update.mockRejectedValueOnce(new Error('保存失败'));
    await expect(updateActionTemplate({
      ...actionDto,
      createdAt: Date.parse(actionDto.createdAt),
      updatedAt: Date.parse(actionDto.updatedAt),
    })).rejects.toThrow('保存失败');
    expect(changed).toHaveBeenCalledTimes(1);
    off();
  });

  it('快照替换省略合并新增项的空 ID 与零时间，并返回服务器最终记录', async () => {
    api.action.getSnapshot.mockResolvedValue({ revision: 'old', items: [actionDto] });
    api.action.replaceSnapshot.mockResolvedValue({
      revision: 'new',
      count: 1,
      items: [actionDto],
    });

    await expect(getActionTemplateSnapshot()).resolves.toMatchObject({
      revision: 'old',
      items: [{ id: 'server-action', updatedAt: Date.parse(actionDto.updatedAt) }],
    });

    const result = await replaceActionTemplateSnapshot({
      expectedRevision: 'old',
      idPolicy: 'generate-missing',
      items: [{
        id: '',
        name: actionDto.name,
        pattern: actionDto.pattern,
        data: actionDto.data,
        createdAt: 0,
        updatedAt: 0,
      }],
    });

    expect(api.action.replaceSnapshot).toHaveBeenCalledWith({
      expectedRevision: 'old',
      idPolicy: 'generate-missing',
      items: [{ name: actionDto.name, pattern: actionDto.pattern, data: actionDto.data }],
    });
    expect(result).toEqual({
      revision: 'new',
      count: 1,
      items: [{
        ...actionDto,
        createdAt: Date.parse(actionDto.createdAt),
        updatedAt: Date.parse(actionDto.updatedAt),
      }],
    });
  });

  it('监听模板快照使用独立 revision，并返回服务器生成的最终身份', async () => {
    api.listen.getSnapshot.mockResolvedValue({ revision: 'listen-old', items: [listenDto] });
    api.listen.replaceSnapshot.mockResolvedValue({
      revision: 'listen-new',
      count: 1,
      items: [listenDto],
    });

    await expect(getListenTemplateSnapshot()).resolves.toMatchObject({
      revision: 'listen-old',
      items: [{ id: 'server-listen', updatedAt: Date.parse(listenDto.updatedAt) }],
    });
    const result = await replaceListenTemplateSnapshot({
      expectedRevision: 'listen-old',
      idPolicy: 'generate-missing',
      items: [{
        id: '',
        name: listenDto.name,
        kind: listenDto.kind,
        data: listenDto.data,
        createdAt: 0,
        updatedAt: 0,
      }],
    });

    expect(api.listen.replaceSnapshot).toHaveBeenCalledWith({
      expectedRevision: 'listen-old',
      idPolicy: 'generate-missing',
      items: [{ name: listenDto.name, kind: listenDto.kind, data: listenDto.data }],
    });
    expect(result.items[0].id).toBe('server-listen');
  });
});
