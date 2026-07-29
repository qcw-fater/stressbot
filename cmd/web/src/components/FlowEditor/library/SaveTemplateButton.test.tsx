import { App as AntApp } from 'antd';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { SaveTemplateButton } from './SaveTemplateButton';

const mocks = vi.hoisted(() => ({
  capability: { templateLibrary: false as boolean | undefined, loading: false, error: undefined as Error | undefined, refresh: vi.fn() },
  findAction: vi.fn(),
  saveAction: vi.fn(),
  showApiError: vi.fn(),
}));

vi.mock('./useTemplateLibraryCapability', () => ({
  useTemplateLibraryCapability: () => mocks.capability,
}));
vi.mock('./templateStore', () => ({
  findActionTemplateByName: mocks.findAction,
  findListenTemplateByName: vi.fn(),
  saveActionTemplate: mocks.saveAction,
  saveListenTemplate: vi.fn(),
  updateActionTemplate: vi.fn(),
  updateListenTemplate: vi.fn(),
}));
vi.mock('@/services/errorHandler', () => ({ showApiError: mocks.showApiError }));
vi.mock('../store/flowStore', () => ({
  useFlowStore: { getState: () => ({ nodes: [] }) },
}));

beforeEach(() => {
  vi.clearAllMocks();
  mocks.capability.templateLibrary = false;
  mocks.capability.loading = false;
  mocks.capability.error = undefined;
});

describe('SaveTemplateButton', () => {
  it('共享模板库不可用时禁用并解释原因', async () => {
    const user = userEvent.setup();
    render(
      <AntApp>
        <SaveTemplateButton kind="action" name="登录" data={{ pattern: 'setState' }} />
      </AntApp>,
    );

    const button = screen.getByRole('button', { name: /加入模板库/ });
    expect(button.hasAttribute('disabled')).toBe(true);
    await user.hover(button);
    expect(await screen.findByText('共享模板库未启用，请联系管理员')).toBeTruthy();
  });

  it('保存失败交给统一错误处理且不产生未处理拒绝', async () => {
    mocks.capability.templateLibrary = true;
    mocks.findAction.mockResolvedValue(undefined);
    const failure = new Error('数据库暂时不可用');
    mocks.saveAction.mockRejectedValue(failure);
    const user = userEvent.setup();
    render(
      <AntApp>
        <SaveTemplateButton kind="action" name="登录" data={{ pattern: 'setState' }} />
      </AntApp>,
    );

    await user.click(screen.getByRole('button', { name: /加入模板库/ }));
    await waitFor(() => expect(mocks.showApiError).toHaveBeenCalledWith(failure));
  });
});
