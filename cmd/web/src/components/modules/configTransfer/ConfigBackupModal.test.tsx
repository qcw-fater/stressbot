import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { ConfigSectionRegistry } from '@/services/configTransfer/sectionRegistry';
import type { BackupSection } from '@/services/configTransfer/types';
import * as backupCodec from '@/services/configTransfer/backupCodec';
import { ConfigBackupModal } from './ConfigBackupModal';

const SECTION_LABELS: Record<BackupSection, string> = {
  flows: '已保存流程',
  draft: '当前编辑稿',
  protoFiles: 'Proto 文件',
  luaFiles: 'Lua 脚本',
  codecFiles: '协议配置',
  errorMap: '错误码',
  actionTemplates: 'Action 模板',
  listenTemplates: 'Listen 模板',
  notepadFiles: '笔记文件',
};

function registryWith(values: Partial<Record<BackupSection, unknown>> = {}): ConfigSectionRegistry {
  return Object.fromEntries(
    Object.entries(SECTION_LABELS).map(([key, label]) => {
      const section = key as BackupSection;
      const fallback = section === 'draft' || section === 'errorMap' ? null : [];
      const value = Object.hasOwn(values, section) ? values[section] : fallback;
      return [
        section,
        {
          key: section,
          label,
          kind: section === 'draft' || section === 'errorMap' ? 'singleton' : 'collection',
          read: vi.fn(async () => structuredClone(value)),
          replace: vi.fn(async () => undefined),
          validate: vi.fn(),
          count: (current: unknown) =>
            Array.isArray(current) ? current.length : current === null ? 0 : 1,
        },
      ];
    }),
  ) as unknown as ConfigSectionRegistry;
}

describe('ConfigBackupModal', () => {
  const nativeGetComputedStyle = window.getComputedStyle.bind(window);
  let createObjectURL: ReturnType<typeof vi.fn>;
  let revokeObjectURL: ReturnType<typeof vi.fn>;
  let downloadName = '';

  beforeEach(() => {
    vi.spyOn(window, 'getComputedStyle').mockImplementation((element) =>
      nativeGetComputedStyle(element),
    );
    createObjectURL = vi.fn(() => 'blob:backup');
    revokeObjectURL = vi.fn();
    Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: createObjectURL });
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: revokeObjectURL });
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function click(
      this: HTMLAnchorElement,
    ) {
      downloadName = this.download;
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
    downloadName = '';
  });

  it('selects every available section and disables saved flows when unavailable', async () => {
    render(
      <ConfigBackupModal
        open
        onClose={() => undefined}
        flowLibrary={false}
        templateLibrary={false}
        registry={registryWith()}
      />,
    );

    expect(await screen.findByText('服务器未启用流程库')).toBeTruthy();
    expect(
      (screen.getByRole('checkbox', { name: /已保存流程/ }) as HTMLInputElement).disabled,
    ).toBe(true);
    expect((screen.getByRole('checkbox', { name: /已保存流程/ }) as HTMLInputElement).checked).toBe(
      false,
    );
    expect((screen.getByRole('checkbox', { name: /当前编辑稿/ }) as HTMLInputElement).checked).toBe(
      true,
    );
    expect((screen.getByRole('checkbox', { name: /笔记文件/ }) as HTMLInputElement).checked).toBe(
      true,
    );
  });

  it('does not allow a download when no section is selected', async () => {
    const user = userEvent.setup();
    render(
      <ConfigBackupModal open onClose={() => undefined} flowLibrary templateLibrary registry={registryWith()} />,
    );

    await user.click(await screen.findByRole('button', { name: '清空' }));

    expect((screen.getByRole('button', { name: /下载备份/ }) as HTMLButtonElement).disabled).toBe(
      true,
    );
    expect(createObjectURL).not.toHaveBeenCalled();
  });

  it('明确说明备份不包含服务器环境数据', async () => {
    render(
      <ConfigBackupModal open onClose={() => undefined} flowLibrary templateLibrary registry={registryWith()} />,
    );

    expect(await screen.findByText(/不包含服务器连接、数据库凭据、运行历史和界面偏好/)).toBeTruthy();
  });

  it('shows the final payload size error without creating a download URL', async () => {
    const user = userEvent.setup();
    vi.spyOn(backupCodec, 'downloadBackupBundle').mockImplementation(() => {
      throw new Error('备份文件超过 100 MiB，请减少选择内容');
    });
    render(
      <ConfigBackupModal open onClose={() => undefined} flowLibrary templateLibrary registry={registryWith()} />,
    );

    await user.click(await screen.findByRole('button', { name: /下载备份/ }));

    expect(await screen.findByText('备份文件超过 100 MiB，请减少选择内容')).toBeTruthy();
    expect(createObjectURL).not.toHaveBeenCalled();
  });

  it('downloads one versioned JSON file containing only selected sections', async () => {
    const user = userEvent.setup();
    const registry = registryWith({
      protoFiles: [{ name: 'login.proto', content: 'syntax = "proto3";' }],
    });
    render(
      <ConfigBackupModal open onClose={() => undefined} flowLibrary={false} templateLibrary registry={registry} />,
    );

    await user.click(await screen.findByRole('button', { name: '清空' }));
    await user.click(screen.getByRole('checkbox', { name: /Proto 文件/ }));
    await user.click(screen.getByRole('button', { name: /下载备份/ }));

    await waitFor(() => expect(createObjectURL).toHaveBeenCalledTimes(1));
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:backup');
    expect(downloadName).toMatch(/^stressbot-config-backup-\d{8}-\d{6}\.json$/);
    expect(registry.protoFiles.read).toHaveBeenCalled();
    expect(registry.luaFiles.read).toHaveBeenCalledTimes(1);
  });
});
