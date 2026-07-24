import { createStore, del, get, set } from 'idb-keyval';

import type { FlowSnapshot } from '../flowsApi';
import type { BackupSection } from './types';

const recoveryStore = createStore('stressbot-config-recovery', 'data');
const ACTIVE_KEY = 'active';

export interface RecoveryJournal {
  operationId: string;
  startedAt: string;
  phase: 'prepared' | 'applying' | 'rollingBack';
  selectedSections: BackupSection[];
  before: Partial<Record<BackupSection, unknown>>;
  completedSections: BackupSection[];
  flowBefore?: FlowSnapshot;
  flowAfterFingerprint?: string;
  flowAppliedRevision?: string;
  pendingRollback: BackupSection[];
}

export interface RecoveryJournalStore {
  load: () => Promise<RecoveryJournal | null>;
  save: (value: RecoveryJournal) => Promise<void>;
  clear: () => Promise<void>;
}

export const recoveryJournal: RecoveryJournalStore = {
  load: () => get<RecoveryJournal>(ACTIVE_KEY, recoveryStore).then((value) => value ?? null),
  save: (value) => set(ACTIVE_KEY, value, recoveryStore),
  clear: () => del(ACTIVE_KEY, recoveryStore),
};
