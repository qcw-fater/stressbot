import { App as AntApp } from 'antd';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { NodePalette } from './NodePalette';

const mocks = vi.hoisted(() => ({
  capability: { templateLibrary: true as boolean | undefined, loading: false, error: undefined as Error | undefined, refresh: vi.fn(async () => undefined) },
  listActions: vi.fn(),
  listListens: vi.fn(),
  changeListener: undefined as (() => void) | undefined,
  onChange: vi.fn((listener: () => void) => {
    void listener;
    return vi.fn();
  }),
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

vi.mock('../library/useTemplateLibraryCapability', () => ({
  useTemplateLibraryCapability: () => mocks.capability,
}));
vi.mock('../library/templateStore', () => ({
  listActionTemplates: mocks.listActions,
  listListenTemplates: mocks.listListens,
  onTemplateChange: mocks.onChange,
  removeActionTemplate: vi.fn(),
  removeListenTemplate: vi.fn(),
}));
vi.mock('../store/editorStore', () => ({
  useEditorStore: (selector: (state: unknown) => unknown) => selector({
    setActivePanel: vi.fn(),
    setClipboard: vi.fn(),
  }),
}));

const action = {
  id: 'a1', name: '登录模板', pattern: 'setState', data: { pattern: 'setState' as const },
  createdAt: 1, updatedAt: 1,
};
const listen = {
  id: 'l1', name: '推送模板', kind: 'silent', data: {}, createdAt: 1, updatedAt: 1,
};

beforeEach(() => {
  vi.clearAllMocks();
  mocks.capability.templateLibrary = true;
  mocks.capability.loading = false;
  mocks.capability.error = undefined;
  mocks.changeListener = undefined;
  mocks.onChange.mockImplementation((listener: () => void) => {
    mocks.changeListener = listener;
    return vi.fn();
  });
  mocks.listActions.mockResolvedValue([action]);
  mocks.listListens.mockResolvedValue([listen]);
});

describe('NodePalette 共享模板库', () => {
  it('不可用时不请求模板，也不提供业务侧重试入口', async () => {
    mocks.capability.templateLibrary = false;
    render(<AntApp><NodePalette /></AntApp>);

    expect(await screen.findByText('共享模板库功能未启用，请检查服务器配置')).toBeTruthy();
    expect(mocks.listActions).not.toHaveBeenCalled();
    expect(screen.queryByRole('button', { name: '刷新模板库' })).toBeNull();
    expect(mocks.capability.refresh).not.toHaveBeenCalled();
  });

  it('进入、手动刷新和窗口聚焦都会重载；失败时保留旧列表', async () => {
    const user = userEvent.setup();
    render(<AntApp><NodePalette /></AntApp>);
    expect(await screen.findByText('登录模板')).toBeTruthy();

    mocks.listActions.mockRejectedValueOnce(new Error('网络抖动'));
    mocks.listListens.mockRejectedValueOnce(new Error('网络抖动'));
    await user.click(screen.getByRole('button', { name: '刷新模板库' }));
    expect(await screen.findByText(/模板库刷新失败/)).toBeTruthy();
    expect(screen.getByText('登录模板')).toBeTruthy();

    window.dispatchEvent(new Event('focus'));
    await waitFor(() => expect(mocks.listActions).toHaveBeenCalledTimes(3));
  });

  it('keeps the newest template list when overlapping refreshes finish out of order', async () => {
    render(<AntApp><NodePalette /></AntApp>);
    expect(await screen.findByText(action.name)).toBeTruthy();

    const staleActions = deferred<typeof action[]>();
    const staleListens = deferred<typeof listen[]>();
    const freshActions = deferred<typeof action[]>();
    const freshListens = deferred<typeof listen[]>();
    mocks.listActions
      .mockImplementationOnce(() => staleActions.promise)
      .mockImplementationOnce(() => freshActions.promise);
    mocks.listListens
      .mockImplementationOnce(() => staleListens.promise)
      .mockImplementationOnce(() => freshListens.promise);

    act(() => mocks.changeListener?.());
    await waitFor(() => expect(mocks.listActions).toHaveBeenCalledTimes(2));
    act(() => mocks.changeListener?.());
    await waitFor(() => expect(mocks.listActions).toHaveBeenCalledTimes(3));

    await act(async () => {
      freshActions.resolve([{ ...action, id: 'fresh', name: 'Fresh action' }]);
      freshListens.resolve([{ ...listen, id: 'fresh', name: 'Fresh listen' }]);
      await Promise.all([freshActions.promise, freshListens.promise]);
    });
    expect(screen.getByText('Fresh action')).toBeTruthy();

    await act(async () => {
      staleActions.resolve([{ ...action, id: 'stale', name: 'Stale action' }]);
      staleListens.resolve([{ ...listen, id: 'stale', name: 'Stale listen' }]);
      await Promise.all([staleActions.promise, staleListens.promise]);
    });

    expect(screen.queryByText('Stale action')).toBeNull();
    expect(screen.getByText('Fresh action')).toBeTruthy();
  });
});
