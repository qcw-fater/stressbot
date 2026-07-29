import { createStore, del, get, set } from 'idb-keyval';

import type { BackupSection, RestoreMode } from './types';

const recoveryStore = createStore('stressbot-config-recovery', 'data');
const ACTIVE_KEY = 'active';

export interface VersionedBeforeSnapshot {
  revision: string;
  value: unknown;
}

export interface RecoveryJournal {
  version: 2;
  operationId: string;
  startedAt: string;
  phase: 'prepared' | 'applying' | 'rollingBack';
  mode: RestoreMode;
  selectedSections: BackupSection[];
  before: Partial<Record<BackupSection, unknown>>;
  completedSections: BackupSection[];
  versionedBefore: Partial<Record<BackupSection, VersionedBeforeSnapshot>>;
  afterFingerprints: Partial<Record<BackupSection, string>>;
  appliedRevisions: Partial<Record<BackupSection, string>>;
  pendingRollback: BackupSection[];
}

interface LegacyRecoveryJournal extends Omit<RecoveryJournal, 'version' | 'mode' | 'versionedBefore' | 'afterFingerprints' | 'appliedRevisions'> {
  flowBefore?: { revision: string; items: unknown };
  flowAfterFingerprint?: string;
  flowAppliedRevision?: string;
}

export function normalizeRecoveryJournal(value: unknown): RecoveryJournal {
  if (value === null || typeof value !== 'object') {
    throw new Error('配置恢复日志格式无效');
  }
  const raw = value as Partial<RecoveryJournal> & LegacyRecoveryJournal;
  if (raw.version === 2) {
    return raw as RecoveryJournal;
  }

  const versionedBefore: RecoveryJournal['versionedBefore'] = {};
  const afterFingerprints: RecoveryJournal['afterFingerprints'] = {};
  const appliedRevisions: RecoveryJournal['appliedRevisions'] = {};
  if (raw.flowBefore) {
    versionedBefore.flows = {
      revision: raw.flowBefore.revision,
      value: raw.flowBefore.items,
    };
  }
  if (raw.flowAfterFingerprint) afterFingerprints.flows = raw.flowAfterFingerprint;
  if (raw.flowAppliedRevision) appliedRevisions.flows = raw.flowAppliedRevision;

  return {
    version: 2,
    operationId: raw.operationId,
    startedAt: raw.startedAt,
    phase: raw.phase,
    mode: 'replace',
    selectedSections: raw.selectedSections,
    before: raw.before,
    completedSections: raw.completedSections,
    versionedBefore,
    afterFingerprints,
    appliedRevisions,
    pendingRollback: raw.pendingRollback,
  };
}

export interface RecoveryJournalStore {
  load: () => Promise<RecoveryJournal | null>;
  save: (value: RecoveryJournal) => Promise<void>;
  clear: () => Promise<void>;
}

export const recoveryJournal: RecoveryJournalStore = {
  load: () => get<unknown>(ACTIVE_KEY, recoveryStore).then((value) => (
    value === undefined ? null : normalizeRecoveryJournal(value)
  )),
  save: (value) => set(ACTIVE_KEY, value, recoveryStore),
  clear: () => del(ACTIVE_KEY, recoveryStore),
};
