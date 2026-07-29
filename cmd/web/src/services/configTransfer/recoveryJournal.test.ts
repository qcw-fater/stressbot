import { describe, expect, it } from 'vitest';
import { fingerprintRestoreValue } from './restoreCoordinator';
import { normalizeRecoveryJournal } from './recoveryJournal';

describe('recovery journal v2', () => {
  it('把旧流程专用字段迁移为通用版本分区映射', () => {
    const legacy = {
      operationId: 'legacy-operation',
      startedAt: '2026-07-23T10:00:00.000Z',
      phase: 'applying',
      selectedSections: ['flows', 'protoFiles'],
      before: { flows: [], protoFiles: [] },
      completedSections: ['flows'],
      flowBefore: { revision: 'flow-before', items: [] },
      flowAfterFingerprint: fingerprintRestoreValue([{ id: 'after' }]),
      flowAppliedRevision: 'flow-applied',
      pendingRollback: ['flows'],
    };

    expect(normalizeRecoveryJournal(legacy)).toMatchObject({
      version: 2,
      operationId: 'legacy-operation',
      mode: 'replace',
      versionedBefore: {
        flows: { revision: 'flow-before', value: [] },
      },
      afterFingerprints: { flows: fingerprintRestoreValue([{ id: 'after' }]) },
      appliedRevisions: { flows: 'flow-applied' },
    });
  });

  it('v2 日志规范化后保持各分区独立修订信息', () => {
    const journal = {
      version: 2,
      operationId: 'operation-2',
      startedAt: '2026-07-23T10:00:00.000Z',
      phase: 'rollingBack',
      mode: 'merge',
      selectedSections: ['actionTemplates', 'listenTemplates'],
      before: {},
      completedSections: ['actionTemplates'],
      versionedBefore: {
        actionTemplates: { revision: 'action-r1', value: [] },
        listenTemplates: { revision: 'listen-r1', value: [] },
      },
      afterFingerprints: {
        actionTemplates: 'action-after',
        listenTemplates: 'listen-after',
      },
      appliedRevisions: { actionTemplates: 'action-r2' },
      pendingRollback: ['actionTemplates'],
    };

    expect(normalizeRecoveryJournal(journal)).toEqual(journal);
  });
});
