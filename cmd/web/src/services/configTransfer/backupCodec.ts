import {
  BACKUP_KIND,
  BACKUP_SCHEMA_VERSION,
  BACKUP_SECTIONS,
  MAX_BACKUP_BYTES,
  type BackupSection,
  type ConfigBackupBundle,
} from './types';
import { defaultSectionRegistry, type ConfigSectionRegistry } from './sectionRegistry';

export type SectionValidator = (section: BackupSection, value: unknown) => void;

const SECTION_SET = new Set<string>(BACKUP_SECTIONS);
const SINGLETON_SECTIONS = new Set<BackupSection>(['draft', 'errorMap']);

interface RuntimeSectionAdapter {
  label: string;
  read: () => Promise<unknown>;
  validate: (value: unknown) => void;
  count: (value: unknown) => number;
}

function sectionAdapter(
  registry: ConfigSectionRegistry,
  section: BackupSection,
): RuntimeSectionAdapter {
  return registry[section] as unknown as RuntimeSectionAdapter;
}

function assertRecord(value: unknown, label: string): asserts value is Record<string, unknown> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(`${label} 必须是对象`);
  }
}

function assertCanonicalISOTime(value: unknown): asserts value is string {
  if (typeof value !== 'string') {
    throw new Error('备份导出时间无效');
  }
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime()) || parsed.toISOString() !== value) {
    throw new Error('备份导出时间无效');
  }
}

function assertSectionList(value: unknown): BackupSection[] {
  if (!Array.isArray(value)) {
    throw new Error('manifest.includedSections 必须是数组');
  }

  const seen = new Set<BackupSection>();
  return value.map((entry) => {
    if (typeof entry !== 'string' || !SECTION_SET.has(entry)) {
      throw new Error(`未知备份分区 ${String(entry)}`);
    }
    const section = entry as BackupSection;
    if (seen.has(section)) {
      throw new Error(`备份分区 ${section} 重复`);
    }
    seen.add(section);
    return section;
  });
}

function sameStringSet(left: readonly string[], right: readonly string[]): boolean {
  if (left.length !== right.length) return false;
  const rightSet = new Set(right);
  return left.every((value) => rightSet.has(value));
}

function defaultSectionValidator(section: BackupSection, value: unknown): void {
  if (SINGLETON_SECTIONS.has(section)) {
    if (value !== null && (typeof value !== 'object' || Array.isArray(value))) {
      throw new Error(`${section} 分区必须是对象或 null`);
    }
    return;
  }
  if (!Array.isArray(value)) {
    throw new Error(`${section} 分区必须是数组`);
  }
}

function sectionCount(section: BackupSection, value: unknown): number {
  if (SINGLETON_SECTIONS.has(section)) return value === null ? 0 : 1;
  return Array.isArray(value) ? value.length : 0;
}

function assertManifestCounts(
  value: unknown,
  included: readonly BackupSection[],
  data: Record<string, unknown>,
): void {
  assertRecord(value, 'manifest.counts');
  const countKeys = Object.keys(value);
  for (const key of countKeys) {
    if (!included.includes(key as BackupSection)) {
      throw new Error(`counts 包含未选择分区 ${key}`);
    }
  }
  for (const section of included) {
    if (!Object.hasOwn(value, section)) {
      throw new Error(`${section} 数量缺失`);
    }
    const count = value[section];
    if (!Number.isInteger(count) || (count as number) < 0) {
      throw new Error(`${section} 数量必须是非负整数`);
    }
    if (count !== sectionCount(section, data[section])) {
      throw new Error(`${section} 数量与实际内容不一致`);
    }
  }
}

export function parseBackupText(
  text: string,
  validateSection: SectionValidator = defaultSectionValidator,
): ConfigBackupBundle {
  const raw: unknown = JSON.parse(text);
  assertRecord(raw, '备份文件');
  if (raw.kind !== BACKUP_KIND) {
    throw new Error('不是 stressbot 配置备份文件');
  }
  if (raw.schemaVersion !== BACKUP_SCHEMA_VERSION) {
    if (typeof raw.schemaVersion === 'number' && raw.schemaVersion > BACKUP_SCHEMA_VERSION) {
      throw new Error(
        `备份格式版本 ${String(raw.schemaVersion)} 高于当前支持版本 ${BACKUP_SCHEMA_VERSION}`,
      );
    }
    throw new Error(`不支持的备份格式版本 ${String(raw.schemaVersion)}`);
  }
  assertCanonicalISOTime(raw.exportedAt);
  assertRecord(raw.manifest, 'manifest');
  assertRecord(raw.data, 'data');

  const included = assertSectionList(raw.manifest.includedSections);
  const dataKeys = Object.keys(raw.data);
  if (!sameStringSet(included, dataKeys)) {
    throw new Error('manifest 与 data 分区不一致');
  }

  for (const section of included) {
    defaultSectionValidator(section, raw.data[section]);
    if (validateSection !== defaultSectionValidator) {
      validateSection(section, raw.data[section]);
    }
  }
  assertManifestCounts(raw.manifest.counts, included, raw.data);

  return raw as unknown as ConfigBackupBundle;
}

export function assertBackupFileSize(file: Pick<File, 'size'>): void {
  if (file.size > MAX_BACKUP_BYTES) {
    throw new Error('备份文件超过 100 MiB，无法导入');
  }
}

export function assertBackupPayloadSize(size: number): void {
  if (size > MAX_BACKUP_BYTES) {
    throw new Error('备份文件超过 100 MiB，请减少选择内容');
  }
}

export function parseBackupWithRegistry(
  text: string,
  registry: ConfigSectionRegistry = defaultSectionRegistry,
): ConfigBackupBundle {
  return parseBackupText(text, (section, value) => {
    registry[section].validate(value);
  });
}

export function buildBackupBundle(
  data: ConfigBackupBundle['data'],
  registry: ConfigSectionRegistry = defaultSectionRegistry,
  now: () => Date = () => new Date(),
): ConfigBackupBundle {
  const includedSections = BACKUP_SECTIONS.filter((section) => Object.hasOwn(data, section));
  const unknownSections = Object.keys(data).filter((section) => !SECTION_SET.has(section));
  if (unknownSections.length > 0) {
    throw new Error(`未知备份分区 ${unknownSections[0]}`);
  }

  const counts: ConfigBackupBundle['manifest']['counts'] = {};
  for (const section of includedSections) {
    const adapter = sectionAdapter(registry, section);
    const value = data[section];
    adapter.validate(value);
    counts[section] = adapter.count(value);
  }

  return {
    kind: BACKUP_KIND,
    schemaVersion: BACKUP_SCHEMA_VERSION,
    exportedAt: now().toISOString(),
    manifest: { includedSections, counts },
    data,
  };
}

export async function createBackupBundle(
  selectedSections: readonly BackupSection[],
  registry: ConfigSectionRegistry = defaultSectionRegistry,
  now: () => Date = () => new Date(),
): Promise<ConfigBackupBundle> {
  const selected = new Set(selectedSections);
  if (selected.size !== selectedSections.length) {
    throw new Error('备份分区不能重复');
  }
  const orderedSections = BACKUP_SECTIONS.filter((section) => selected.has(section));
  if (orderedSections.length !== selectedSections.length) {
    throw new Error('备份包含未知分区');
  }

  const entries = await Promise.all(
    orderedSections.map(async (section) => {
      const adapter = sectionAdapter(registry, section);
      try {
        return [section, await adapter.read()] as const;
      } catch (error) {
        const detail = error instanceof Error ? error.message : String(error);
        throw new Error(`${adapter.label}读取失败：${detail}`);
      }
    }),
  );
  const data = Object.fromEntries(entries) as ConfigBackupBundle['data'];
  return buildBackupBundle(data, registry, now);
}

function padDatePart(value: number): string {
  return String(value).padStart(2, '0');
}

export function backupFileName(exportedAt: string): string {
  const date = new Date(exportedAt);
  const day = [
    date.getFullYear(),
    padDatePart(date.getMonth() + 1),
    padDatePart(date.getDate()),
  ].join('');
  const time = [date.getHours(), date.getMinutes(), date.getSeconds()].map(padDatePart).join('');
  return `stressbot-config-backup-${day}-${time}.json`;
}

export function downloadBackupBundle(bundle: ConfigBackupBundle): void {
  const blob = new Blob([JSON.stringify(bundle, null, 2)], {
    type: 'application/json;charset=utf-8',
  });
  assertBackupPayloadSize(blob.size);
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  try {
    anchor.href = url;
    anchor.download = backupFileName(bundle.exportedAt);
    document.body.appendChild(anchor);
    anchor.click();
  } finally {
    anchor.remove();
    URL.revokeObjectURL(url);
  }
}
