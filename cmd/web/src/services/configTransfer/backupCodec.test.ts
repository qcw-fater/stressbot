import { describe, expect, it, vi } from 'vitest';

import {
  assertBackupFileSize,
  createBackupBundle,
  parseBackupText,
} from './backupCodec';
import type { ConfigSectionRegistry } from './sectionRegistry';
import {
  BACKUP_KIND,
  BACKUP_SCHEMA_VERSION,
  type BackupSection,
  MAX_BACKUP_BYTES,
} from './types';

function backup(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    kind: BACKUP_KIND,
    schemaVersion: BACKUP_SCHEMA_VERSION,
    exportedAt: '2026-07-23T10:00:00.000Z',
    manifest: {
      includedSections: ['protoFiles'],
      counts: { protoFiles: 1 },
    },
    data: {
      protoFiles: [{ name: 'login.proto' }],
    },
    ...overrides,
  };
}

describe('parseBackupText', () => {
  it('parses a valid partial backup and invokes nested validation', () => {
    const validateSection = vi.fn();

    const parsed = parseBackupText(JSON.stringify(backup()), validateSection);

    expect(parsed.manifest.includedSections).toEqual(['protoFiles']);
    expect(validateSection).toHaveBeenCalledWith(
      'protoFiles',
      [{ name: 'login.proto' }],
    );
  });

  it('distinguishes omitted sections from selected empty sections', () => {
    const parsed = parseBackupText(JSON.stringify(backup({
      manifest: {
        includedSections: ['protoFiles', 'errorMap'],
        counts: { protoFiles: 0, errorMap: 0 },
      },
      data: { protoFiles: [], errorMap: null },
    })));

    expect(parsed.data.protoFiles).toEqual([]);
    expect(parsed.data.errorMap).toBeNull();
    expect('luaFiles' in parsed.data).toBe(false);
  });

  it('rejects invalid JSON and a wrong backup kind', () => {
    expect(() => parseBackupText('{')).toThrow();
    expect(() => parseBackupText(JSON.stringify(backup({ kind: 'flow' }))))
      .toThrow('不是 stressbot 配置备份文件');
  });

  it('rejects a newer schema version', () => {
    expect(() => parseBackupText(JSON.stringify(backup({ schemaVersion: 2 }))))
      .toThrow('备份格式版本 2 高于当前支持版本 1');
  });

  it('rejects an invalid export time', () => {
    expect(() => parseBackupText(JSON.stringify(backup({ exportedAt: 'yesterday' }))))
      .toThrow('导出时间无效');
  });

  it('rejects unknown and duplicate sections', () => {
    expect(() => parseBackupText(JSON.stringify(backup({
      manifest: { includedSections: ['secrets'], counts: { secrets: 0 } },
      data: { secrets: [] },
    })))).toThrow('未知备份分区 secrets');

    expect(() => parseBackupText(JSON.stringify(backup({
      manifest: {
        includedSections: ['protoFiles', 'protoFiles'],
        counts: { protoFiles: 1 },
      },
    })))).toThrow('备份分区 protoFiles 重复');
  });

  it('requires manifest and data to contain the same section keys', () => {
    expect(() => parseBackupText(JSON.stringify(backup({
      data: { protoFiles: [{ name: 'login.proto' }], luaFiles: [] },
    })))).toThrow('manifest 与 data 分区不一致');
  });

  it('rejects missing, extra, non-integer, and mismatched counts', () => {
    expect(() => parseBackupText(JSON.stringify(backup({
      manifest: { includedSections: ['protoFiles'], counts: {} },
    })))).toThrow('protoFiles 数量缺失');

    expect(() => parseBackupText(JSON.stringify(backup({
      manifest: {
        includedSections: ['protoFiles'],
        counts: { protoFiles: 1, luaFiles: 0 },
      },
    })))).toThrow('counts 包含未选择分区 luaFiles');

    expect(() => parseBackupText(JSON.stringify(backup({
      manifest: {
        includedSections: ['protoFiles'],
        counts: { protoFiles: 1.5 },
      },
    })))).toThrow('protoFiles 数量必须是非负整数');

    expect(() => parseBackupText(JSON.stringify(backup({
      manifest: {
        includedSections: ['protoFiles'],
        counts: { protoFiles: 2 },
      },
    })))).toThrow('protoFiles 数量与实际内容不一致');
  });
});

describe('assertBackupFileSize', () => {
  it('accepts exactly 100 MiB and rejects larger files', () => {
    expect(() => assertBackupFileSize({ size: MAX_BACKUP_BYTES })).not.toThrow();
    expect(() => assertBackupFileSize({ size: MAX_BACKUP_BYTES + 1 }))
      .toThrow('备份文件超过 100 MiB，无法导入');
  });
});

describe('createBackupBundle', () => {
  it('reads only selected sections and keeps selected empty values explicit', async () => {
    const reads = Object.fromEntries([
      'flows',
      'draft',
      'protoFiles',
      'luaFiles',
      'codecFiles',
      'errorMap',
      'actionTemplates',
      'listenTemplates',
      'notepadFiles',
    ].map((section) => [section, vi.fn(async () => section === 'draft' ? null : [])]));
    const registry = Object.fromEntries(Object.entries(reads).map(([key, read]) => [key, {
      key,
      label: key,
      kind: key === 'draft' || key === 'errorMap' ? 'singleton' : 'collection',
      read,
      replace: vi.fn(),
      validate: vi.fn(),
      count: (value: unknown) => Array.isArray(value) ? value.length : value === null ? 0 : 1,
    }])) as unknown as ConfigSectionRegistry;

    const bundle = await createBackupBundle(
      ['draft', 'protoFiles'],
      registry,
      () => new Date('2026-07-23T10:00:00.000Z'),
    );

    expect(bundle.manifest).toEqual({
      includedSections: ['draft', 'protoFiles'],
      counts: { draft: 0, protoFiles: 0 },
    });
    expect(bundle.data).toEqual({ draft: null, protoFiles: [] });
    expect(reads.draft).toHaveBeenCalledTimes(1);
    expect(reads.protoFiles).toHaveBeenCalledTimes(1);
    expect(reads.luaFiles).not.toHaveBeenCalled();
  });

  it('names the failing selected section and does not return a partial bundle', async () => {
    const protoRead = vi.fn(async () => []);
    const luaRead = vi.fn(async () => {
      throw new Error('storage offline');
    });
    const adapter = (key: BackupSection, read: () => Promise<unknown>) => ({
      key,
      label: key === 'protoFiles' ? 'Proto 文件' : 'Lua 脚本',
      kind: 'collection' as const,
      read,
      replace: vi.fn(),
      validate: vi.fn(),
      count: (value: unknown) => (value as unknown[]).length,
    });
    const registry = {
      protoFiles: adapter('protoFiles', protoRead),
      luaFiles: adapter('luaFiles', luaRead),
    } as unknown as ConfigSectionRegistry;

    await expect(createBackupBundle(['protoFiles', 'luaFiles'], registry))
      .rejects.toThrow('Lua 脚本读取失败');
  });
});
