import { describe, expect, it, vi } from 'vitest';

import {
  assertBackupFileSize,
  parseBackupText,
} from './backupCodec';
import {
  BACKUP_KIND,
  BACKUP_SCHEMA_VERSION,
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
