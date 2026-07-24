import { App as AntApp } from 'antd';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { getCapabilities } from '@/services/capabilitiesApi';
import { Toolbar } from './Toolbar';

vi.mock('@/services/capabilitiesApi', () => ({
  getCapabilities: vi.fn(async () => ({ sharedState: false, flowLibrary: false })),
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
vi.mock('../library/templateStore', () => ({
  exportAllTemplates: vi.fn(),
  importTemplates: vi.fn(),
}));
vi.mock('@/components/modules/configTransfer/ConfigBackupModal', () => ({
  ConfigBackupModal: ({ open }: { open: boolean }) => open
    ? <div>配置备份弹窗已打开</div>
    : null,
}));

describe('Toolbar configuration backup entry', () => {
  const nativeGetComputedStyle = window.getComputedStyle.bind(window);

  beforeEach(() => {
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
});
