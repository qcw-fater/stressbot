import { App as AntApp } from 'antd';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { ConfigSectionRegistry } from '@/services/configTransfer/sectionRegistry';
import type {
  BackupSection,
  ConfigBackupBundle,
  RestorePlan,
  RestoreResult,
  SectionStats,
} from '@/services/configTransfer/types';
import { ConfigRestoreModal, type ConfigRestoreServices } from './ConfigRestoreModal';

const EMPTY_STATS: SectionStats = {
  added: 0,
  overwritten: 0,
  deleted: 0,
  skipped: 0,
  copied: 0,
};

const bundle: ConfigBackupBundle = {
  kind: 'stressbot-config-backup',
  schemaVersion: 1,
  exportedAt: '2026-07-23T10:00:00.000Z',
  manifest: {
    includedSections: ['flows', 'protoFiles'],
    counts: { flows: 1, protoFiles: 1 },
  },
  data: { flows: [], protoFiles: [] },
};

function planFor(selectedSections: BackupSection[] = ['protoFiles']): RestorePlan {
  return {
    operationId: 'restore-1',
    createdAt: '2026-07-23T10:00:00.000Z',
    mode: 'merge',
    selectedSections,
    sections: {},
    conflicts: [],
    stats: Object.fromEntries(
      selectedSections.map((section) => [
        section,
        {
          ...EMPTY_STATS,
          added: 1,
        },
      ]),
    ),
  };
}

function registry(): ConfigSectionRegistry {
  const labels: Record<BackupSection, string> = {
    flows: '已保存流程',
    draft: '当前编辑稿',
    protoFiles: 'Proto 文件',
    luaFiles: '脚本文件',
    codecFiles: '协议配置',
    errorMap: '错误码',
    actionTemplates: '动作模板',
    listenTemplates: '监听模板',
    notepadFiles: '记事本文件',
  };
  return Object.fromEntries(
    Object.entries(labels).map(([key, label]) => [
      key,
      {
        key,
        label,
        kind: key === 'draft' || key === 'errorMap' ? 'singleton' : 'collection',
        read: vi.fn(),
        replace: vi.fn(),
        validate: vi.fn(),
        count: vi.fn(),
      },
    ]),
  ) as unknown as ConfigSectionRegistry;
}

function restoreServices(overrides: Partial<ConfigRestoreServices> = {}): ConfigRestoreServices {
  return {
    assertFileSize: vi.fn(),
    parse: vi.fn(() => bundle),
    preflight: vi.fn(async (_bundle, selected) => planFor([...selected])),
    resolve: vi.fn((plan) => plan),
    execute: vi.fn(
      async (plan): Promise<RestoreResult> => ({
        ok: true,
        stats: plan.stats,
        pendingSections: [],
      }),
    ),
    ...overrides,
  };
}

function backupFile(): File {
  const file = new File(['{}'], 'backup.json', { type: 'application/json' });
  Object.defineProperty(file, 'text', { configurable: true, value: vi.fn(async () => '{}') });
  return file;
}

function renderModal(
  services: ConfigRestoreServices,
  props: Partial<React.ComponentProps<typeof ConfigRestoreModal>> = {},
) {
  return render(
    <AntApp>
      <ConfigRestoreModal
        open
        runtimeMode="edit"
        onClose={() => undefined}
        flowLibrary
        registry={registry()}
        services={services}
        confirmRestore={async () => true}
        {...props}
      />
    </AntApp>,
  );
}

async function selectValidBackup(user: ReturnType<typeof userEvent.setup>) {
  await user.upload(screen.getByLabelText('选择配置备份文件'), backupFile());
  await screen.findByText('格式版本 1');
}

describe('ConfigRestoreModal', () => {
  const nativeGetComputedStyle = window.getComputedStyle.bind(window);

  beforeEach(() => {
    vi.spyOn(window, 'getComputedStyle').mockImplementation((element) =>
      nativeGetComputedStyle(element),
    );
  });

  afterEach(() => vi.restoreAllMocks());

  it('rejects an oversized file before reading or parsing it', async () => {
    const user = userEvent.setup();
    const services = restoreServices({
      assertFileSize: vi.fn(() => {
        throw new Error('备份文件超过 100 MiB');
      }),
    });
    renderModal(services);

    await user.upload(screen.getByLabelText('选择配置备份文件'), backupFile());

    expect(await screen.findByText('备份文件超过 100 MiB')).toBeTruthy();
    expect(services.parse).not.toHaveBeenCalled();
  });

  it('shows parse errors without entering preview', async () => {
    const user = userEvent.setup();
    const services = restoreServices({
      parse: vi.fn(() => {
        throw new Error('不是 stressbot 配置备份文件');
      }),
    });
    renderModal(services);

    await user.upload(screen.getByLabelText('选择配置备份文件'), backupFile());

    expect(await screen.findByText('不是 stressbot 配置备份文件')).toBeTruthy();
    expect(screen.queryByText('格式版本 1')).toBeNull();
  });

  it('selects available contents and disables saved flows when unavailable', async () => {
    const user = userEvent.setup();
    const services = restoreServices();
    renderModal(services, { flowLibrary: false });

    await selectValidBackup(user);

    const flows = screen.getByRole('checkbox', { name: /已保存流程/ }) as HTMLInputElement;
    const proto = screen.getByRole('checkbox', { name: /Proto 文件/ }) as HTMLInputElement;
    expect(flows.disabled).toBe(true);
    expect(flows.checked).toBe(false);
    expect(proto.checked).toBe(true);
    await waitFor(() =>
      expect(services.preflight).toHaveBeenCalledWith(bundle, ['protoFiles'], 'merge', 'prompt'),
    );
  });

  it('replans when the merge duplicate strategy changes', async () => {
    const user = userEvent.setup();
    const services = restoreServices();
    renderModal(services, { flowLibrary: false });
    await selectValidBackup(user);

    await user.click(screen.getByRole('radio', { name: '全部忽略' }));

    await waitFor(() =>
      expect(services.preflight).toHaveBeenLastCalledWith(bundle, ['protoFiles'], 'merge', 'skip'),
    );
  });

  it('shows the deletion warning only for full restore', async () => {
    const user = userEvent.setup();
    renderModal(restoreServices(), { flowLibrary: false });
    await selectValidBackup(user);

    expect(screen.queryByText(/会删除选中内容中备份不存在的配置/)).toBeNull();
    await user.click(screen.getByText('完整恢复'));

    expect(await screen.findByText(/会删除选中内容中备份不存在的配置/)).toBeTruthy();
  });

  it('updates preview counts from the resolved conflict choices', async () => {
    const user = userEvent.setup();
    const conflicted = planFor(['protoFiles']);
    conflicted.conflicts = [
      {
        id: 'proto:one',
        section: 'protoFiles',
        kind: 'duplicate',
        sourceName: 'login.proto',
        targetIds: ['login.proto'],
        targetNames: ['login.proto'],
        allowedChoices: ['overwrite', 'keep-copy', 'skip'],
      },
    ];
    const services = restoreServices({
      preflight: vi.fn(async () => conflicted),
      resolve: vi.fn((plan, choices) =>
        choices['proto:one']
          ? {
              ...plan,
              conflicts: [],
              stats: { protoFiles: { ...EMPTY_STATS, copied: 1 } },
            }
          : plan,
      ),
    });
    renderModal(services, { flowLibrary: false });
    await selectValidBackup(user);
    const row = await screen.findByTestId('conflict-proto:one');

    await user.click(within(row).getByRole('radio', { name: '保留两份' }));

    expect(await screen.findByText('保留两份 1')).toBeTruthy();
  });

  it('allows preview but blocks execution outside edit mode', async () => {
    const user = userEvent.setup();
    renderModal(restoreServices(), { runtimeMode: 'running', flowLibrary: false });
    await selectValidBackup(user);

    expect(screen.getByText('请先返回编辑模式')).toBeTruthy();
    expect((screen.getByRole('button', { name: /开始恢复/ }) as HTMLButtonElement).disabled).toBe(
      true,
    );
  });

  it('executes after confirmation and renders per-content result counts', async () => {
    const user = userEvent.setup();
    const services = restoreServices();
    renderModal(services, { flowLibrary: false });
    await selectValidBackup(user);
    await waitFor(() => expect(services.preflight).toHaveBeenCalled());

    await user.click(screen.getByRole('button', { name: /开始恢复/ }));

    expect(await screen.findByText('恢复完成')).toBeTruthy();
    expect(screen.getByText('Proto 文件')).toBeTruthy();
    expect(screen.getByText('新增 1')).toBeTruthy();
    expect(services.execute).toHaveBeenCalledTimes(1);
  });
});
