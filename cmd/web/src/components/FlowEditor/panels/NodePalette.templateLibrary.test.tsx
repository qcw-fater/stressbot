import { App as AntApp } from 'antd';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { NodePalette } from './NodePalette';

const mocks = vi.hoisted(() => ({
  capability: { templateLibrary: true as boolean | undefined, loading: false, error: undefined as Error | undefined, refresh: vi.fn(async () => undefined) },
  listActions: vi.fn(),
  listListens: vi.fn(),
  onChange: vi.fn(() => vi.fn()),
}));

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
  mocks.listActions.mockResolvedValue([action]);
  mocks.listListens.mockResolvedValue([listen]);
});

describe('NodePalette 共享模板库', () => {
  it('不可用时不请求模板，并可手动重试能力检测', async () => {
    mocks.capability.templateLibrary = false;
    const user = userEvent.setup();
    render(<AntApp><NodePalette /></AntApp>);

    expect(await screen.findByText('共享模板库未启用，请联系管理员')).toBeTruthy();
    expect(mocks.listActions).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: '刷新模板库' }));
    expect(mocks.capability.refresh).toHaveBeenCalled();
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
});
