import { describe, expect, it, vi } from 'vitest';

import type { ConfigBackupBundle } from './types';
import {
  executeRestorePlan,
  fingerprintRestoreValue,
  preflightRestore,
  recoverPendingRestore,
  resolveRestorePlanConflicts,
  RestoreExecutionError,
  RestoreTargetChangedError,
  type RestoreAdapter,
  type RestoreEnvironment,
} from './restoreCoordinator';
import type { RecoveryJournal, RecoveryJournalStore } from './recoveryJournal';
import {
  BACKUP_SECTIONS,
  type BackupSection,
  type RestorePlan,
  type SectionStats,
} from './types';

const EMPTY_STATS: SectionStats = {
  added: 0,
  overwritten: 0,
  deleted: 0,
  skipped: 0,
  copied: 0,
};

function memoryJournal(initial: RecoveryJournal | null = null): RecoveryJournalStore & {
  peek: () => RecoveryJournal | null;
} {
  let value = initial ? structuredClone(initial) : null;
  return {
    load: vi.fn(async () => value ? structuredClone(value) : null),
    save: vi.fn(async (next) => {
      value = structuredClone(next);
    }),
    clear: vi.fn(async () => {
      value = null;
    }),
    peek: () => value,
  };
}

interface AdapterOptions {
  before?: unknown;
  after?: unknown;
  failOnApply?: boolean;
}

function fakeAdapter(
  section: BackupSection,
  calls: string[],
  options: AdapterOptions = {},
): RestoreAdapter {
  const before = options.before ?? `before-${section}`;
  const after = options.after ?? `after-${section}`;
  return {
    key: section,
    kind: 'singleton',
    read: vi.fn(async () => structuredClone(before)),
    replace: vi.fn(async (value) => {
      if (JSON.stringify(value) === JSON.stringify(after)) {
        calls.push(`apply:${section}`);
        if (options.failOnApply) throw new Error(`${section} apply failed`);
      } else {
        calls.push(`rollback:${section}`);
      }
    }),
    validate: vi.fn(),
    count: () => 1,
  };
}

interface EnvironmentOptions {
  adapters?: Partial<Record<BackupSection, RestoreAdapter>>;
  flowApplyError?: Error;
  flowRollbackError?: Error;
  journal?: ReturnType<typeof memoryJournal>;
}

function fakeEnvironment(calls: string[], options: EnvironmentOptions = {}): RestoreEnvironment {
  const registry = Object.fromEntries(BACKUP_SECTIONS.map((section) => [
    section,
    options.adapters?.[section] ?? fakeAdapter(section, calls),
  ])) as Record<BackupSection, RestoreAdapter>;
  const journal = options.journal ?? memoryJournal();

  return {
    registry,
    journal,
    getFlowSnapshot: vi.fn(async () => ({ revision: 'flow-before', items: [] })),
    replaceFlowSnapshot: vi.fn(async (request) => {
      if (request.expectedRevision === 'flow-before') {
        calls.push('apply:flows');
        if (options.flowApplyError) throw options.flowApplyError;
        return { revision: 'flow-applied', count: request.items.length };
      }
      calls.push('rollback:flows');
      if (options.flowRollbackError) throw options.flowRollbackError;
      return { revision: 'flow-restored', count: request.items.length };
    }),
    createOperationId: () => 'operation-1',
    now: () => new Date('2026-07-23T10:00:00.000Z'),
  };
}

function planFor(...sections: BackupSection[]): RestorePlan {
  const plans: RestorePlan['sections'] = {};
  const stats: RestorePlan['stats'] = {};
  for (const section of sections) {
    const before = section === 'flows' ? [] : `before-${section}`;
    const after = section === 'flows' ? [] : `after-${section}`;
    plans[section] = {
      before,
      incoming: after,
      after,
      beforeFingerprint: fingerprintRestoreValue(before),
      stats: { ...EMPTY_STATS },
    };
    stats[section] = { ...EMPTY_STATS };
  }
  return {
    operationId: 'operation-1',
    createdAt: '2026-07-23T10:00:00.000Z',
    mode: 'replace',
    selectedSections: sections,
    sections: plans,
    conflicts: [],
    stats,
    flowExpectedRevision: sections.includes('flows') ? 'flow-before' : undefined,
  };
}

describe('executeRestorePlan', () => {
  it('applies flows first, clears the journal, and returns statistics', async () => {
    const calls: string[] = [];
    const env = fakeEnvironment(calls);

    const result = await executeRestorePlan(planFor('protoFiles', 'flows'), env);

    expect(calls).toEqual(['apply:flows', 'apply:protoFiles']);
    expect(result).toMatchObject({ ok: true, pendingSections: [] });
    expect(await env.journal.load()).toBeNull();
  });

  it('stops after a flow failure without applying local sections', async () => {
    const calls: string[] = [];
    const env = fakeEnvironment(calls, { flowApplyError: new Error('flow failed') });

    await expect(executeRestorePlan(planFor('flows', 'protoFiles'), env))
      .rejects.toBeInstanceOf(RestoreExecutionError);

    expect(calls).toEqual(['apply:flows']);
    expect(await env.journal.load()).toBeNull();
  });

  it('rolls completed sections back in reverse order', async () => {
    const calls: string[] = [];
    const env = fakeEnvironment(calls, {
      adapters: {
        codecFiles: fakeAdapter('codecFiles', calls, { failOnApply: true }),
      },
    });

    await expect(executeRestorePlan(
      planFor('protoFiles', 'luaFiles', 'codecFiles'),
      env,
    )).rejects.toThrow('codecFiles');

    expect(calls).toEqual([
      'apply:protoFiles',
      'apply:luaFiles',
      'apply:codecFiles',
      'rollback:luaFiles',
      'rollback:protoFiles',
    ]);
    expect(await env.journal.load()).toBeNull();
  });

  it('compensates an applied flow snapshot after a later local failure', async () => {
    const calls: string[] = [];
    const env = fakeEnvironment(calls, {
      adapters: {
        protoFiles: fakeAdapter('protoFiles', calls, { failOnApply: true }),
      },
    });

    await expect(executeRestorePlan(planFor('flows', 'protoFiles'), env))
      .rejects.toBeInstanceOf(RestoreExecutionError);

    expect(calls).toEqual(['apply:flows', 'apply:protoFiles', 'rollback:flows']);
    expect(env.replaceFlowSnapshot).toHaveBeenLastCalledWith({
      expectedRevision: 'flow-applied',
      items: [],
    });
  });

  it('rejects changed targets before creating a journal or writing', async () => {
    const calls: string[] = [];
    const env = fakeEnvironment(calls, {
      adapters: {
        protoFiles: fakeAdapter('protoFiles', calls, { before: 'changed' }),
      },
    });

    await expect(executeRestorePlan(planFor('protoFiles'), env))
      .rejects.toBeInstanceOf(RestoreTargetChangedError);

    expect(calls).toEqual([]);
    expect(await env.journal.load()).toBeNull();
  });
});

describe('recoverPendingRestore', () => {
  it('keeps the journal when flow compensation revision conflicts', async () => {
    const calls: string[] = [];
    const journal = memoryJournal({
      operationId: 'operation-1',
      startedAt: '2026-07-23T10:00:00.000Z',
      phase: 'rollingBack',
      selectedSections: ['flows'],
      before: { flows: [] },
      completedSections: ['flows'],
      flowBefore: { revision: 'flow-before', items: [] },
      flowAppliedRevision: 'flow-applied',
      pendingRollback: ['flows'],
    });
    const conflict = Object.assign(new Error('revision changed'), {
      code: 'FLOW_SNAPSHOT_CONFLICT',
      status: 409,
    });
    const env = fakeEnvironment(calls, { journal, flowRollbackError: conflict });

    const result = await recoverPendingRestore(env);

    expect(result.pendingSections).toContain('flows');
    expect(await env.journal.load()).not.toBeNull();
  });

  it('resumes reverse local rollback after a reload and clears the journal', async () => {
    const calls: string[] = [];
    const journal = memoryJournal({
      operationId: 'operation-1',
      startedAt: '2026-07-23T10:00:00.000Z',
      phase: 'applying',
      selectedSections: ['protoFiles', 'luaFiles'],
      before: {
        protoFiles: 'before-protoFiles',
        luaFiles: 'before-luaFiles',
      },
      completedSections: ['protoFiles', 'luaFiles'],
      pendingRollback: [],
    });
    const env = fakeEnvironment(calls, { journal });

    const result = await recoverPendingRestore(env);

    expect(calls).toEqual(['rollback:luaFiles', 'rollback:protoFiles']);
    expect(result).toMatchObject({ ok: true, pendingSections: [], rolledBack: true });
    expect(await env.journal.load()).toBeNull();
  });
});

describe('preflightRestore', () => {
  it('reads only selected sections and produces prompt conflicts', async () => {
    const calls: string[] = [];
    const existing = [{ id: 'same', name: 'login.proto' }];
    const incoming = [{ id: 'same', name: 'login.proto' }];
    const protoAdapter: RestoreAdapter = {
      key: 'protoFiles',
      kind: 'collection',
      read: vi.fn(async () => existing),
      replace: vi.fn(async () => undefined),
      validate: vi.fn(),
      count: (value) => (value as unknown[]).length,
      identity: {
        id: (value) => (value as { id: string }).id,
        name: (value) => (value as { name: string }).name,
        clone: (value, id, name) => ({ ...(value as object), id, name }),
        createId: () => 'copy',
      },
    };
    const env = fakeEnvironment(calls, { adapters: { protoFiles: protoAdapter } });
    const bundle: ConfigBackupBundle = {
      kind: 'stressbot-config-backup',
      schemaVersion: 1,
      exportedAt: '2026-07-23T10:00:00.000Z',
      manifest: { includedSections: ['protoFiles'], counts: { protoFiles: 1 } },
      data: { protoFiles: incoming },
    };

    const plan = await preflightRestore(
      bundle,
      ['protoFiles'],
      'merge',
      'prompt',
      env,
    );

    expect(plan.conflicts).toHaveLength(1);
    expect(Object.isFrozen(plan)).toBe(true);
    expect(Object.isFrozen(plan.sections.protoFiles)).toBe(true);
    expect(env.registry.luaFiles.read).not.toHaveBeenCalled();
    expect(env.getFlowSnapshot).not.toHaveBeenCalled();
  });

  it('rejects an invalid current snapshot before creating a plan', async () => {
    const calls: string[] = [];
    const adapter = fakeAdapter('draft', calls, { before: 'invalid', after: 'incoming' });
    adapter.validate = (value) => {
      if (value === 'invalid') throw new Error('current snapshot invalid');
    };
    const env = fakeEnvironment(calls, { adapters: { draft: adapter } });
    const bundle: ConfigBackupBundle = {
      kind: 'stressbot-config-backup',
      schemaVersion: 1,
      exportedAt: '2026-07-23T10:00:00.000Z',
      manifest: { includedSections: ['draft'], counts: { draft: 1 } },
      data: { draft: 'incoming' },
    };

    await expect(preflightRestore(bundle, ['draft'], 'replace', 'overwrite', env))
      .rejects.toThrow('current snapshot invalid');
  });

  it('freezes conflict choices into an executable plan', async () => {
    const calls: string[] = [];
    const existing = [{ id: 'same', name: 'login.proto' }];
    const incoming = [{ id: 'same', name: 'login.proto' }];
    const protoAdapter: RestoreAdapter = {
      key: 'protoFiles',
      kind: 'collection',
      read: vi.fn(async () => existing),
      replace: vi.fn(async () => undefined),
      validate: vi.fn(),
      count: (value) => (value as unknown[]).length,
      identity: {
        id: (value) => (value as { id: string }).id,
        name: (value) => (value as { name: string }).name,
        clone: (value, id, name) => ({ ...(value as object), id, name }),
        createId: () => 'copy-id',
      },
    };
    const env = fakeEnvironment(calls, { adapters: { protoFiles: protoAdapter } });
    const bundle: ConfigBackupBundle = {
      kind: 'stressbot-config-backup',
      schemaVersion: 1,
      exportedAt: '2026-07-23T10:00:00.000Z',
      manifest: { includedSections: ['protoFiles'], counts: { protoFiles: 1 } },
      data: { protoFiles: incoming },
    };
    const pending = await preflightRestore(bundle, ['protoFiles'], 'merge', 'prompt', env);

    const resolved = resolveRestorePlanConflicts(
      pending,
      { [pending.conflicts[0].id]: 'keep-copy' },
      env,
    );

    expect(resolved.conflicts).toEqual([]);
    expect(resolved.stats.protoFiles?.copied).toBe(1);
    expect(resolved.sections.protoFiles?.after).toHaveLength(2);
    expect(Object.isFrozen(resolved)).toBe(true);
  });
});
