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
import {
  normalizeRecoveryJournal,
  type RecoveryJournal,
  type RecoveryJournalStore,
} from './recoveryJournal';
import { BACKUP_SECTIONS, type BackupSection, type RestorePlan, type SectionStats } from './types';

const EMPTY_STATS: SectionStats = {
  added: 0,
  overwritten: 0,
  deleted: 0,
  skipped: 0,
  copied: 0,
};

function memoryJournal(initial: unknown = null): RecoveryJournalStore & {
  peek: () => RecoveryJournal | null;
} {
  let value = initial ? normalizeRecoveryJournal(structuredClone(initial)) : null;
  return {
    load: vi.fn(async () => (value ? structuredClone(value) : null)),
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

type TestEnvironment = RestoreEnvironment & {
  getFlowSnapshot: ReturnType<typeof vi.fn>;
  replaceFlowSnapshot: ReturnType<typeof vi.fn>;
  setFlowSnapshot: (snapshot: { revision: string; items: unknown[] }) => void;
};

function fakeEnvironment(calls: string[], options: EnvironmentOptions = {}): TestEnvironment {
  const journal = options.journal ?? memoryJournal();
  let flowRevision = 'flow-before';
  let flowItems: unknown[] = [];
  const getFlowSnapshot = vi.fn(async () => ({ revision: flowRevision, items: structuredClone(flowItems) }));
  const replaceFlowSnapshot = vi.fn(async (request: { expectedRevision: string; items: unknown[] }) => {
    if (request.expectedRevision === 'flow-before') {
      calls.push('apply:flows');
      if (options.flowApplyError) throw options.flowApplyError;
      flowRevision = 'flow-applied';
      flowItems = structuredClone(request.items);
      return { revision: 'flow-applied', count: request.items.length };
    }
    calls.push('rollback:flows');
    if (options.flowRollbackError) throw options.flowRollbackError;
    flowRevision = 'flow-before';
    flowItems = structuredClone(request.items);
    return { revision: 'flow-before', count: request.items.length };
  });
  const flowAdapter: RestoreAdapter = {
    key: 'flows',
    kind: 'collection',
    read: vi.fn(async () => structuredClone(flowItems)),
    replace: vi.fn(async () => undefined),
    validate: vi.fn(),
    count: (value) => (value as unknown[]).length,
    identity: {
      id: (value) => String((value as { id?: unknown }).id ?? ''),
      name: (value) => String((value as { name?: unknown }).name ?? ''),
      clone: (value, id, name) => ({ ...(value as object), id, name }),
      createId: () => 'flow-copy',
    },
    versioned: {
      read: async () => {
        const snapshot = await getFlowSnapshot();
        return { revision: snapshot.revision, value: snapshot.items };
      },
      replace: async ({ expectedRevision, value }) => {
        const result = await replaceFlowSnapshot({
          expectedRevision,
          items: value as unknown[],
        });
        return { revision: result.revision, value };
      },
    },
  };
  const registry = Object.fromEntries(
    BACKUP_SECTIONS.map((section) => [
      section,
      options.adapters?.[section] ?? (section === 'flows' ? flowAdapter : fakeAdapter(section, calls)),
    ]),
  ) as Record<BackupSection, RestoreAdapter>;

  return {
    registry,
    journal,
    getFlowSnapshot,
    replaceFlowSnapshot,
    setFlowSnapshot: (snapshot) => {
      flowRevision = snapshot.revision;
      flowItems = structuredClone(snapshot.items);
    },
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
    expectedRevisions: sections.includes('flows') ? { flows: 'flow-before' } : {},
  };
}

describe('executeRestorePlan', () => {
  it('arms each section for rollback before applying it', async () => {
    const calls: string[] = [];
    const journal = memoryJournal();
    let pendingAtApply: BackupSection[] = [];
    const adapter = fakeAdapter('protoFiles', calls);
    adapter.replace = vi.fn(async (value) => {
      if (value === 'after-protoFiles') {
        pendingAtApply = journal.peek()?.pendingRollback ?? [];
        calls.push('apply:protoFiles');
      }
    });
    const env = fakeEnvironment(calls, {
      adapters: { protoFiles: adapter },
      journal,
    });

    await executeRestorePlan(planFor('protoFiles'), env);

    expect(pendingAtApply).toEqual(['protoFiles']);
  });

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

    await expect(executeRestorePlan(planFor('flows', 'protoFiles'), env)).rejects.toBeInstanceOf(
      RestoreExecutionError,
    );

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

    await expect(
      executeRestorePlan(planFor('protoFiles', 'luaFiles', 'codecFiles'), env),
    ).rejects.toThrow('codecFiles');

    expect(calls).toEqual([
      'apply:protoFiles',
      'apply:luaFiles',
      'apply:codecFiles',
      'rollback:codecFiles',
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

    await expect(executeRestorePlan(planFor('flows', 'protoFiles'), env)).rejects.toBeInstanceOf(
      RestoreExecutionError,
    );

    expect(calls).toEqual([
      'apply:flows',
      'apply:protoFiles',
      'rollback:protoFiles',
      'rollback:flows',
    ]);
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

    await expect(executeRestorePlan(planFor('protoFiles'), env)).rejects.toBeInstanceOf(
      RestoreTargetChangedError,
    );

    expect(calls).toEqual([]);
    expect(await env.journal.load()).toBeNull();
  });
});

describe('recoverPendingRestore', () => {
  it('treats an already restored flow snapshot as a completed rollback', async () => {
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
    const env = fakeEnvironment(calls, { journal });

    const result = await recoverPendingRestore(env);

    expect(result).toMatchObject({ ok: true, pendingSections: [], rolledBack: true });
    expect(env.replaceFlowSnapshot).not.toHaveBeenCalled();
    expect(await env.journal.load()).toBeNull();
  });

  it('restores a flow applied before its response revision was journaled', async () => {
    const calls: string[] = [];
    const afterItems = [{ id: 'flow-after' }] as never[];
    const journal = memoryJournal({
      operationId: 'operation-1',
      startedAt: '2026-07-23T10:00:00.000Z',
      phase: 'applying',
      selectedSections: ['flows'],
      before: { flows: [] },
      completedSections: [],
      flowBefore: { revision: 'flow-before', items: [] },
      pendingRollback: ['flows'],
      flowAfterFingerprint: fingerprintRestoreValue(afterItems),
    });
    const env = fakeEnvironment(calls, { journal });
    env.setFlowSnapshot({
      revision: 'flow-after-without-response',
      items: afterItems,
    });

    const result = await recoverPendingRestore(env);

    expect(result).toMatchObject({ ok: true, pendingSections: [], rolledBack: true });
    expect(env.replaceFlowSnapshot).toHaveBeenCalledWith({
      expectedRevision: 'flow-after-without-response',
      items: [],
    });
  });

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
    env.setFlowSnapshot({ revision: 'flow-applied', items: [] });

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

    const plan = await preflightRestore(bundle, ['protoFiles'], 'merge', 'prompt', env);

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

    await expect(preflightRestore(bundle, ['draft'], 'replace', 'overwrite', env)).rejects.toThrow(
      'current snapshot invalid',
    );
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

describe('generic versioned sections', () => {
  function versionedAdapter(
    section: BackupSection,
    calls: string[],
    initialRevision: string,
    initialValue: unknown,
  ): RestoreAdapter & {
    setRevision: (revision: string) => void;
  } {
    let revision = initialRevision;
    let value = structuredClone(initialValue);
    return {
      key: section,
      kind: 'singleton',
      read: vi.fn(async () => structuredClone(value)),
      replace: vi.fn(async (next) => {
        calls.push(`local:${section}`);
        value = structuredClone(next);
      }),
      validate: vi.fn(),
      count: () => 1,
      versioned: {
        read: vi.fn(async () => ({ revision, value: structuredClone(value) })),
        replace: vi.fn(async (input: { expectedRevision: string; value: unknown }) => {
          calls.push(`versioned:${section}:${input.expectedRevision}`);
          if (input.expectedRevision !== revision) throw new Error(`${section} stale`);
          value = structuredClone(input.value);
          revision = `${section}-r${Number(revision.at(-1) ?? '1') + 1}`;
          return { revision, value: structuredClone(value) };
        }),
      },
      setRevision: (next) => { revision = next; },
    } as RestoreAdapter & { setRevision: (revision: string) => void };
  }

  function bundleFor(data: Partial<Record<BackupSection, unknown>>): ConfigBackupBundle {
    const includedSections = Object.keys(data) as BackupSection[];
    return {
      kind: 'stressbot-config-backup',
      schemaVersion: 1,
      exportedAt: '2026-07-23T10:00:00.000Z',
      manifest: { includedSections, counts: {} },
      data,
    };
  }

  it('预检为每个服务器分区保存独立 revision', async () => {
    const calls: string[] = [];
    const action = versionedAdapter('actionTemplates', calls, 'action-r1', 'action-before');
    const listen = versionedAdapter('listenTemplates', calls, 'listen-r7', 'listen-before');
    const env = fakeEnvironment(calls, {
      adapters: { actionTemplates: action, listenTemplates: listen },
    });

    const plan = await preflightRestore(
      bundleFor({ actionTemplates: 'action-after', listenTemplates: 'listen-after' }),
      ['actionTemplates', 'listenTemplates'],
      'replace',
      'overwrite',
      env,
    );

    expect(plan.expectedRevisions).toEqual({
      actionTemplates: 'action-r1',
      listenTemplates: 'listen-r7',
    });
  });

  it('任一服务器分区 revision 变化时在所有写入前终止', async () => {
    const calls: string[] = [];
    const action = versionedAdapter('actionTemplates', calls, 'action-r1', 'action-before');
    const listen = versionedAdapter('listenTemplates', calls, 'listen-r1', 'listen-before');
    const env = fakeEnvironment(calls, {
      adapters: { actionTemplates: action, listenTemplates: listen },
    });
    const plan = await preflightRestore(
      bundleFor({ actionTemplates: 'action-after', listenTemplates: 'listen-after' }),
      ['actionTemplates', 'listenTemplates'],
      'replace',
      'overwrite',
      env,
    );
    listen.setRevision('listen-r2');

    await expect(executeRestorePlan(plan, env)).rejects.toBeInstanceOf(RestoreTargetChangedError);
    expect(calls).toEqual([]);
  });

  it('先按固定顺序写服务器分区，再写本地分区；失败后使用各自最新 revision 逆序回滚', async () => {
    const calls: string[] = [];
    const action = versionedAdapter('actionTemplates', calls, 'action-r1', 'action-before');
    const listen = versionedAdapter('listenTemplates', calls, 'listen-r1', 'listen-before');
    const draft = fakeAdapter('draft', calls, { before: 'draft-before', after: 'draft-after' });
    draft.replace = vi.fn(async (value) => {
      calls.push(value === 'draft-after' ? 'apply:draft' : 'rollback:draft');
      if (value === 'draft-after') throw new Error('draft failed');
    });
    const env = fakeEnvironment(calls, {
      adapters: { actionTemplates: action, listenTemplates: listen, draft },
    });
    const plan = await preflightRestore(
      bundleFor({ draft: 'draft-after', actionTemplates: 'action-after', listenTemplates: 'listen-after' }),
      ['draft', 'actionTemplates', 'listenTemplates'],
      'replace',
      'overwrite',
      env,
    );

    await expect(executeRestorePlan(plan, env)).rejects.toBeInstanceOf(RestoreExecutionError);
    expect(calls).toEqual([
      'versioned:actionTemplates:action-r1',
      'versioned:listenTemplates:listen-r1',
      'apply:draft',
      'rollback:draft',
      'versioned:listenTemplates:listenTemplates-r2',
      'versioned:actionTemplates:actionTemplates-r2',
    ]);
    expect(await env.journal.load()).toBeNull();
  });

  it('响应前中断时以语义指纹识别服务器生成 ID，并安全回滚', async () => {
    const calls: string[] = [];
    const before = [{ id: 'old', name: '旧模板', createdAt: 1, updatedAt: 1 }];
    const intended = [{ id: '', name: '新模板', createdAt: 0, updatedAt: 0 }];
    let revision = 'action-r1';
    let value = structuredClone(before);
    let firstApply = true;
    const semantic = (items: unknown) => JSON.stringify(
      (items as Array<{ name: string }>).map((item) => ({ name: item.name })),
    );
    const adapter: RestoreAdapter = {
      key: 'actionTemplates',
      kind: 'collection',
      read: vi.fn(async () => structuredClone(value)),
      replace: vi.fn(async () => undefined),
      validate: vi.fn(),
      count: (items) => (items as unknown[]).length,
      identity: {
        id: (item) => (item as { id: string }).id,
        name: (item) => (item as { name: string }).name,
        clone: (item, id, name) => ({ ...(item as object), id, name }),
        createId: () => '',
      },
      versioned: {
        read: vi.fn(async () => ({ revision, value: structuredClone(value) })),
        fingerprint: semantic,
        replace: vi.fn(async ({ expectedRevision, value: next }) => {
          calls.push(`replace:${expectedRevision}`);
          if (expectedRevision !== revision) throw new Error('stale');
          if (firstApply) {
            firstApply = false;
            value = [{ ...(next as Array<Record<string, unknown>>)[0], id: 'server-id', createdAt: 100, updatedAt: 101 }] as typeof before;
            revision = 'action-r2';
            throw new Error('响应前连接中断');
          }
          value = structuredClone(next as typeof before);
          revision = 'action-r3';
          return { revision, value: structuredClone(value) };
        }),
      },
    };
    const env = fakeEnvironment(calls, { adapters: { actionTemplates: adapter } });
    const plan = planFor('actionTemplates');
    plan.mode = 'merge';
    plan.sections.actionTemplates = {
      before,
      incoming: intended,
      after: intended,
      beforeFingerprint: fingerprintRestoreValue(before),
      stats: { ...EMPTY_STATS },
    };
    plan.expectedRevisions = { actionTemplates: 'action-r1' };

    await expect(executeRestorePlan(plan, env)).rejects.toBeInstanceOf(RestoreExecutionError);
    expect(calls).toEqual(['replace:action-r1', 'replace:action-r2']);
    expect(value).toEqual(before);
    expect(await env.journal.load()).toBeNull();
  });
});
