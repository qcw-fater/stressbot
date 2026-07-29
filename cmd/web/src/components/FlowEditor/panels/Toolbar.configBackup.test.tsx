import { App as AntApp } from 'antd';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { getCapabilities } from '@/services/capabilitiesApi';
import { Toolbar } from './Toolbar';

const capability = vi.hoisted(() => ({ templateLibrary: false }));

vi.mock('@/services/capabilitiesApi', () => ({
  getCapabilities: vi.fn(async () => ({ sharedState: false, flowLibrary: false, templateLibrary: false })),
}));
vi.mock('../library/useTemplateLibraryCapability', () => ({
  useTemplateLibraryCapability: () => ({
    templateLibrary: capability.templateLibrary,
    loading: false,
    error: undefined,
    refresh: vi.fn(),
  }),
}));
vi.mock('@/services/runtimeStore', () => ({
  useRuntimeStore: (selector: (state: { mode: 'edit' }) => unknown) => selector({ mode: 'edit' }),
}));
vi.mock('@/services/baselineApi', () => ({ fetchBaselineFlow: vi.fn() }));
vi.mock('../store/undoRedo', () => ({ undo: vi.fn(), redo: vi.fn() }));
vi.mock('../store/persistDraft', () => ({
  clearDraft: vi.fn(),
  captureCurrentDraft: vi.fn(),
  loadDraft: vi.fn(() => null),
}));
vi.mock('../store/flowStore', () => ({
  useFlowStore: (selector: (state: {
    loadFromTaskFlow: () => void;
    reset: () => void;
    applyAutoLayout: () => void;
    listens: Record<string, unknown>;
  }) => unknown) => selector({
    loadFromTaskFlow: vi.fn(),
    reset: vi.fn(),
    applyAutoLayout: vi.fn(),
    listens: {},
  }),
}));
vi.mock('../store/editorStore', () => ({
  useEditorStore: (selector: (state: { setActivePanel: () => void }) => unknown) => selector({
    setActivePanel: vi.fn(),
  }),
}));
vi.mock('../proto/protoStore', () => ({
  useProtoStore: (selector: (state: { status: string; fileCount: number }) => unknown) => selector({
    status: 'idle',
    fileCount: 0,
  }),
}));
vi.mock('../validation/validationStore', () => ({
  useValidationStore: (selector: (state: {
    report: { errors: unknown[]; warnings: unknown[] };
  }) => unknown) => selector({ report: { errors: [], warnings: [] } }),
}));
vi.mock('./useFlowFileIO', () => ({
  useFlowFileIO: () => ({
    importFlow: vi.fn(),
    exportFlow: vi.fn(),
    syncScriptsAfterLoad: vi.fn(),
  }),
}));
vi.mock('./FlowManagerModal', () => ({ FlowManagerModal: () => null }));
vi.mock('@/components/modules/configTransfer/ConfigBackupModal', () => ({
  ConfigBackupModal: ({ open }: { open: boolean }) => open
    ? <div>配置备份弹窗已打开</div>
    : null,
}));
vi.mock('@/components/modules/configTransfer/ConfigRestoreModal', () => ({
  ConfigRestoreModal: ({ open, runtimeMode }: { open: boolean; runtimeMode: string }) => open
    ? <div>配置恢复弹窗已打开：{runtimeMode}</div>
    : null,
}));

describe('Toolbar configuration backup entry', () => {
  const nativeGetComputedStyle = window.getComputedStyle.bind(window);

  beforeEach(() => {
    vi.clearAllMocks();
    capability.templateLibrary = false;
    vi.spyOn(window, 'getComputedStyle').mockImplementation((element) => (
      nativeGetComputedStyle(element)
    ));
  });

  afterEach(() => vi.restoreAllMocks());

  it('opens backup from the File menu after loading capabilities', async () => {
    const user = userEvent.setup();
    render(
      <AntApp>
        <Toolbar />
      </AntApp>,
    );

    await user.click(screen.getByRole('button', { name: /文件/ }));
    await user.click(await screen.findByText('备份配置...'));

    expect(await screen.findByText('配置备份弹窗已打开')).toBeTruthy();
    expect(getCapabilities).toHaveBeenCalledTimes(1);
  });

  it('opens restore from the File menu with the current runtime mode', async () => {
    const user = userEvent.setup();
    render(
      <AntApp>
        <Toolbar />
      </AntApp>,
    );

    await user.click(screen.getByRole('button', { name: /文件/ }));
    await user.click(await screen.findByText('恢复配置...'));

    expect(await screen.findByText('配置恢复弹窗已打开：edit')).toBeTruthy();
    expect(getCapabilities).toHaveBeenCalledTimes(1);
  });

  it('文件菜单只保留统一的配置备份和恢复入口', async () => {
    const user = userEvent.setup();
    render(<AntApp><Toolbar /></AntApp>);

    await user.click(screen.getByRole('button', { name: /文件/ }));
    expect(await screen.findByText('备份配置...')).toBeTruthy();
    expect(screen.getByText('恢复配置...')).toBeTruthy();
    expect(screen.queryByText(/导入模板库/)).toBeNull();
    expect(screen.queryByText(/导出模板库/)).toBeNull();
  });
});
