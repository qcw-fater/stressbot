import { nanoid } from 'nanoid';

import {
  defaultSectionRegistry,
  type ConfigSectionRegistry,
  type VersionedSectionIO,
} from './sectionRegistry';
import {
  applyConflictChoices,
  planCollectionMerge,
  planCollectionReplace,
  planSingleton,
  type CollectionPlan,
  type CollectionIdentity,
  type PlannedConflict,
  type SingletonPlan,
} from './restorePlanner';
import {
  recoveryJournal,
  type RecoveryJournal,
  type RecoveryJournalStore,
} from './recoveryJournal';
import {
  BACKUP_SECTIONS,
  type BackupSection,
  type ConflictChoice,
  type ConfigBackupBundle,
  type MergeConflictPolicy,
  type RestoreConflict,
  type RestoreMode,
  type RestorePlan,
  type RestoreResult,
  type RestoreSectionPlan,
} from './types';

export interface RestoreAdapter {
  key: BackupSection;
  kind: 'collection' | 'singleton';
  read: () => Promise<unknown>;
  replace: (value: unknown) => Promise<void>;
  validate: (value: unknown) => void;
  count: (value: unknown) => number;
  identity?: CollectionIdentity<unknown>;
  refresh?: (value: unknown) => Promise<void> | void;
  versioned?: VersionedSectionIO<unknown>;
}

export interface RestoreEnvironment {
  registry: Record<BackupSection, RestoreAdapter>;
  journal: RecoveryJournalStore;
  createOperationId: () => string;
  now: () => Date;
}

function runtimeRegistry(registry: ConfigSectionRegistry): Record<BackupSection, RestoreAdapter> {
  return registry as unknown as Record<BackupSection, RestoreAdapter>;
}

export const defaultRestoreEnvironment: RestoreEnvironment = {
  registry: runtimeRegistry(defaultSectionRegistry),
  journal: recoveryJournal,
  createOperationId: () => nanoid(16),
  now: () => new Date(),
};

export class RestoreTargetChangedError extends Error {
  readonly section: BackupSection;

  constructor(section: BackupSection) {
    super(`配置分区 ${section} 在预检后发生变化，请重新预检`);
    this.name = 'RestoreTargetChangedError';
    this.section = section;
  }
}

export class RestoreExecutionError extends Error {
  readonly originalError: unknown;
  readonly pendingSections: BackupSection[];

  constructor(originalError: unknown, pendingSections: BackupSection[]) {
    const message = originalError instanceof Error ? originalError.message : String(originalError);
    super(`配置恢复失败：${message}`);
    this.name = 'RestoreExecutionError';
    this.originalError = originalError;
    this.pendingSections = pendingSections;
  }
}

function canonicalize(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonicalize);
  if (value !== null && typeof value === 'object') {
    const record = value as Record<string, unknown>;
    return Object.fromEntries(
      Object.keys(record)
        .sort()
        .map((key) => [key, canonicalize(record[key])]),
    );
  }
  return value;
}

export function fingerprintRestoreValue(value: unknown): string {
  return JSON.stringify(canonicalize(value)) ?? 'undefined';
}

function deepFreeze<T>(value: T, seen = new WeakSet<object>()): T {
  if (value === null || typeof value !== 'object') return value;
  const object = value as object;
  if (seen.has(object)) return value;
  seen.add(object);
  for (const child of Object.values(value as Record<string, unknown>)) {
    deepFreeze(child, seen);
  }
  return Object.freeze(value);
}

function assertSelectedSections(
  bundle: ConfigBackupBundle,
  selected: readonly BackupSection[],
): void {
  const included = new Set(bundle.manifest.includedSections);
  const seen = new Set<BackupSection>();
  for (const section of selected) {
    if (seen.has(section)) throw new Error(`恢复分区 ${section} 重复`);
    if (!included.has(section) || !Object.hasOwn(bundle.data, section)) {
      throw new Error(`备份文件不包含分区 ${section}`);
    }
    seen.add(section);
  }
}

function planSection(
  section: BackupSection,
  current: unknown,
  incoming: unknown,
  mode: RestoreMode,
  policy: MergeConflictPolicy,
  adapter: RestoreAdapter,
): { sectionPlan: RestoreSectionPlan; conflicts: RestoreConflict[] } {
  adapter.validate(current);
  adapter.validate(incoming);
  if (adapter.kind === 'collection') {
    if (!Array.isArray(current) || !Array.isArray(incoming) || !adapter.identity) {
      throw new Error(`集合分区 ${section} 的数据或身份规则无效`);
    }
    const planned =
      mode === 'replace'
        ? planCollectionReplace(current, incoming, adapter.identity, section)
        : planCollectionMerge(current, incoming, adapter.identity, policy, section);
    return {
      sectionPlan: {
        before: current,
        incoming,
        after: planned.finalItems,
        beforeFingerprint: fingerprintRestoreValue(current),
        stats: planned.stats,
      },
      conflicts: planned.conflicts,
    };
  }

  const currentValue = current === undefined ? null : current;
  const incomingValue = incoming === undefined ? null : incoming;
  const planned = planSingleton(currentValue, incomingValue, mode, policy, section);
  return {
    sectionPlan: {
      before: currentValue,
      incoming: incomingValue,
      after: planned.finalValue,
      beforeFingerprint: fingerprintRestoreValue(currentValue),
      stats: planned.stats,
    },
    conflicts: planned.conflicts,
  };
}

export async function preflightRestore(
  bundle: ConfigBackupBundle,
  selectedSections: readonly BackupSection[],
  mode: RestoreMode,
  policy: MergeConflictPolicy,
  environment: RestoreEnvironment = defaultRestoreEnvironment,
): Promise<RestorePlan> {
  assertSelectedSections(bundle, selectedSections);
  const sections: RestorePlan['sections'] = {};
  const stats: RestorePlan['stats'] = {};
  const conflicts: RestoreConflict[] = [];
  const expectedRevisions: RestorePlan['expectedRevisions'] = {};

  for (const section of selectedSections) {
    const adapter = environment.registry[section];
    let current: unknown;
    if (adapter.versioned) {
      const snapshot = await adapter.versioned.read();
      current = snapshot.value;
      expectedRevisions[section] = snapshot.revision;
    } else {
      current = await adapter.read();
    }
    const planned = planSection(section, current, bundle.data[section], mode, policy, adapter);
    sections[section] = planned.sectionPlan;
    stats[section] = planned.sectionPlan.stats;
    conflicts.push(...planned.conflicts);
  }

  conflicts.sort((left, right) => {
    if (left.section !== right.section) return left.section < right.section ? -1 : 1;
    return left.sourceName < right.sourceName ? -1 : left.sourceName > right.sourceName ? 1 : 0;
  });

  return deepFreeze({
    operationId: environment.createOperationId(),
    createdAt: environment.now().toISOString(),
    mode,
    selectedSections: [...selectedSections],
    sections,
    conflicts,
    stats,
    expectedRevisions,
  });
}

export function resolveRestorePlanConflicts(
  plan: RestorePlan,
  choices: Readonly<Record<string, ConflictChoice>>,
  environment: RestoreEnvironment = defaultRestoreEnvironment,
): RestorePlan {
  if (plan.conflicts.length === 0) return plan;

  const sections: RestorePlan['sections'] = { ...plan.sections };
  const stats: RestorePlan['stats'] = { ...plan.stats };
  const conflicts: RestoreConflict[] = [];

  for (const section of plan.selectedSections) {
    const sectionPlan = plan.sections[section];
    if (!sectionPlan) throw new Error(`恢复计划缺少内容 ${section}`);
    const matchingConflicts = plan.conflicts.filter((conflict) => conflict.section === section);
    const sectionConflicts = matchingConflicts as unknown as PlannedConflict<unknown>[];
    if (sectionConflicts.length === 0) continue;

    const adapter = environment.registry[section];
    if (adapter.kind === 'collection') {
      if (
        !Array.isArray(sectionPlan.before) ||
        !Array.isArray(sectionPlan.after) ||
        !adapter.identity
      ) {
        throw new Error(`集合内容 ${section} 的冲突计划无效`);
      }
      const pending: CollectionPlan<unknown> = {
        kind: 'collection',
        section,
        currentItems: sectionPlan.before,
        finalItems: sectionPlan.after,
        conflicts: sectionConflicts,
        stats: sectionPlan.stats,
      };
      const resolved = applyConflictChoices(pending, choices, adapter.identity);
      sections[section] = {
        ...sectionPlan,
        after: resolved.finalItems,
        stats: resolved.stats,
      };
      stats[section] = resolved.stats;
      conflicts.push(...resolved.conflicts);
      continue;
    }

    const pending: SingletonPlan<unknown> = {
      kind: 'singleton',
      section,
      currentValue: sectionPlan.before ?? null,
      incomingValue: sectionPlan.incoming ?? null,
      finalValue: sectionPlan.after ?? null,
      conflicts: sectionConflicts,
      stats: sectionPlan.stats,
    };
    const resolved = applyConflictChoices(pending, choices);
    sections[section] = {
      ...sectionPlan,
      after: resolved.finalValue,
      stats: resolved.stats,
    };
    stats[section] = resolved.stats;
    conflicts.push(...resolved.conflicts);
  }

  return deepFreeze({ ...plan, sections, stats, conflicts });
}

function orderedSections(
  selected: readonly BackupSection[],
  environment: RestoreEnvironment,
): BackupSection[] {
  const set = new Set(selected);
  const ordered = BACKUP_SECTIONS.filter((section) => set.has(section));
  return [
    ...ordered.filter((section) => environment.registry[section].versioned !== undefined),
    ...ordered.filter((section) => environment.registry[section].versioned === undefined),
  ];
}

function sectionFingerprint(
  adapter: RestoreAdapter,
  value: unknown,
  mode: RestoreMode,
): string {
  return adapter.versioned?.fingerprint?.(value, mode) ?? fingerprintRestoreValue(value);
}

async function assertTargetsUnchanged(
  plan: RestorePlan,
  environment: RestoreEnvironment,
): Promise<void> {
  for (const section of orderedSections(plan.selectedSections, environment)) {
    const adapter = environment.registry[section];
    if (adapter.versioned) {
      const snapshot = await adapter.versioned.read();
      if (
        !plan.expectedRevisions[section]
        || snapshot.revision !== plan.expectedRevisions[section]
      ) {
        throw new RestoreTargetChangedError(section);
      }
      continue;
    }
    const current = await adapter.read();
    const expected = plan.sections[section]?.beforeFingerprint;
    if (expected === undefined || fingerprintRestoreValue(current) !== expected) {
      throw new RestoreTargetChangedError(section);
    }
  }
}

function makeJournal(plan: RestorePlan, environment: RestoreEnvironment): RecoveryJournal {
  const before: RecoveryJournal['before'] = {};
  const versionedBefore: RecoveryJournal['versionedBefore'] = {};
  const afterFingerprints: RecoveryJournal['afterFingerprints'] = {};
  for (const section of plan.selectedSections) {
    const sectionPlan = plan.sections[section];
    before[section] = sectionPlan?.before;
    const revision = plan.expectedRevisions[section];
    const adapter = environment.registry[section];
    if (adapter.versioned && revision && sectionPlan) {
      versionedBefore[section] = { revision, value: sectionPlan.before };
      afterFingerprints[section] = sectionFingerprint(adapter, sectionPlan.after, plan.mode);
    }
  }
  return {
    version: 2,
    operationId: plan.operationId,
    startedAt: plan.createdAt,
    phase: 'prepared',
    mode: plan.mode,
    selectedSections: [...plan.selectedSections],
    before,
    completedSections: [],
    versionedBefore,
    afterFingerprints,
    appliedRevisions: {},
    pendingRollback: [],
  };
}

async function applyPlanSections(
  plan: RestorePlan,
  journal: RecoveryJournal,
  environment: RestoreEnvironment,
): Promise<Partial<Record<BackupSection, unknown>>> {
  const appliedValues: Partial<Record<BackupSection, unknown>> = {};
  journal.phase = 'applying';
  await environment.journal.save(journal);
  for (const section of orderedSections(plan.selectedSections, environment)) {
    const sectionPlan = plan.sections[section];
    if (!sectionPlan) throw new Error(`恢复计划缺少分区 ${section}`);
    if (!journal.pendingRollback.includes(section)) {
      journal.pendingRollback.push(section);
      await environment.journal.save(journal);
    }
    const adapter = environment.registry[section];
    if (adapter.versioned) {
      const expectedRevision = plan.expectedRevisions[section];
      if (!expectedRevision) {
        throw new Error(`服务器分区 ${section} 的恢复计划缺少 revision`);
      }
      const result = await adapter.versioned.replace({
        expectedRevision,
        value: sectionPlan.after,
        mode: plan.mode,
      });
      appliedValues[section] = result.value;
      journal.appliedRevisions[section] = result.revision;
      journal.afterFingerprints[section] = sectionFingerprint(adapter, result.value, plan.mode);
    } else {
      await adapter.replace(sectionPlan.after);
      appliedValues[section] = sectionPlan.after;
    }
    journal.completedSections.push(section);
    await environment.journal.save(journal);
  }
  return appliedValues;
}

async function refreshPlanSections(
  plan: RestorePlan,
  appliedValues: Partial<Record<BackupSection, unknown>>,
  environment: RestoreEnvironment,
): Promise<void> {
  for (const section of orderedSections(plan.selectedSections, environment)) {
    const sectionPlan = plan.sections[section];
    if (sectionPlan) {
      await environment.registry[section].refresh?.(
        Object.hasOwn(appliedValues, section) ? appliedValues[section] : sectionPlan.after,
      );
    }
  }
}

async function rollbackOne(
  section: BackupSection,
  journal: RecoveryJournal,
  environment: RestoreEnvironment,
): Promise<void> {
  const adapter = environment.registry[section];
  if (adapter.versioned) {
    const before = journal.versionedBefore[section];
    if (!before) throw new Error(`服务器分区 ${section} 回滚缺少恢复前快照`);
    const current = await adapter.versioned.read();
    if (current.revision === before.revision) return;

    let expectedRevision = journal.appliedRevisions[section];
    if (expectedRevision) {
      if (current.revision !== expectedRevision) {
        throw new RestoreTargetChangedError(section);
      }
    } else {
      const intended = journal.afterFingerprints[section];
      if (!intended || sectionFingerprint(adapter, current.value, journal.mode) !== intended) {
        throw new RestoreTargetChangedError(section);
      }
      expectedRevision = current.revision;
    }
    const result = await adapter.versioned.replace({
      expectedRevision,
      value: before.value,
      mode: 'replace',
    });
    await adapter.refresh?.(result.value);
    return;
  }
  if (!Object.hasOwn(journal.before, section)) {
    throw new Error(`恢复日志缺少分区 ${section} 的原始快照`);
  }
  const before = journal.before[section];
  await adapter.replace(before);
  await adapter.refresh?.(before);
}

async function rollbackCompleted(
  journal: RecoveryJournal,
  environment: RestoreEnvironment,
): Promise<BackupSection[]> {
  journal.phase = 'rollingBack';
  if (journal.pendingRollback.length === 0) {
    journal.pendingRollback = [...journal.completedSections];
  }
  await environment.journal.save(journal);

  for (const section of [...journal.pendingRollback].reverse()) {
    try {
      await rollbackOne(section, journal, environment);
      journal.pendingRollback = journal.pendingRollback.filter((value) => value !== section);
    } catch {
      // Keep failed sections in the journal and continue rolling back independent sections.
    }
    await environment.journal.save(journal);
  }
  return [...journal.pendingRollback];
}

export async function executeRestorePlan(
  plan: RestorePlan,
  environment: RestoreEnvironment = defaultRestoreEnvironment,
): Promise<RestoreResult> {
  if (await environment.journal.load()) {
    throw new Error('存在未完成的配置恢复，请先完成回滚');
  }
  if (plan.conflicts.length > 0) {
    throw new Error('恢复计划仍有未处理冲突');
  }
  await assertTargetsUnchanged(plan, environment);

  const journal = makeJournal(plan, environment);
  await environment.journal.save(journal);
  try {
    const appliedValues = await applyPlanSections(plan, journal, environment);
    await refreshPlanSections(plan, appliedValues, environment);
    await environment.journal.clear();
    return { ok: true, stats: plan.stats, pendingSections: [] };
  } catch (error) {
    const pendingSections = await rollbackCompleted(journal, environment);
    if (pendingSections.length === 0) await environment.journal.clear();
    throw new RestoreExecutionError(error, pendingSections);
  }
}

export async function recoverPendingRestore(
  environment: RestoreEnvironment = defaultRestoreEnvironment,
): Promise<RestoreResult> {
  const journal = await environment.journal.load();
  if (!journal) {
    return { ok: true, stats: {}, pendingSections: [], rolledBack: false };
  }
  const pendingSections = await rollbackCompleted(journal, environment);
  if (pendingSections.length === 0) {
    await environment.journal.clear();
    return { ok: true, stats: {}, pendingSections: [], rolledBack: true };
  }
  return { ok: false, stats: {}, pendingSections, rolledBack: false };
}
