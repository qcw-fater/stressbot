export const BACKUP_KIND = 'stressbot-config-backup' as const;
export const BACKUP_SCHEMA_VERSION = 1 as const;
export const MAX_BACKUP_BYTES = 100 * 1024 * 1024;

export const BACKUP_SECTIONS = [
  'flows',
  'draft',
  'protoFiles',
  'luaFiles',
  'codecFiles',
  'errorMap',
  'actionTemplates',
  'listenTemplates',
  'notepadFiles',
] as const;

export type BackupSection = (typeof BACKUP_SECTIONS)[number];

export interface BackupManifest {
  includedSections: BackupSection[];
  counts: Partial<Record<BackupSection, number>>;
}

export interface ConfigBackupBundle {
  kind: typeof BACKUP_KIND;
  schemaVersion: typeof BACKUP_SCHEMA_VERSION;
  exportedAt: string;
  manifest: BackupManifest;
  data: Partial<Record<BackupSection, unknown>>;
}

export type RestoreMode = 'merge' | 'replace';
export type ConflictChoice = 'overwrite' | 'keep-copy' | 'skip';

export interface SectionStats {
  added: number;
  overwritten: number;
  deleted: number;
  skipped: number;
  copied: number;
}

export interface RestoreConflict {
  id: string;
  section: BackupSection;
  kind: 'duplicate' | 'ambiguous';
  sourceId?: string;
  sourceName: string;
  targetIds: string[];
  targetNames: string[];
  allowedChoices: ConflictChoice[];
  choice?: ConflictChoice;
}

export interface RestoreSectionPlan {
  before: unknown;
  after: unknown;
  stats: SectionStats;
}

export interface RestorePlan {
  operationId: string;
  createdAt: string;
  mode: RestoreMode;
  selectedSections: BackupSection[];
  sections: Partial<Record<BackupSection, RestoreSectionPlan>>;
  conflicts: RestoreConflict[];
  stats: Partial<Record<BackupSection, SectionStats>>;
  flowExpectedRevision?: string;
}

export interface RestoreResult {
  ok: boolean;
  stats: Partial<Record<BackupSection, SectionStats>>;
  pendingSections: BackupSection[];
  rolledBack?: boolean;
}
