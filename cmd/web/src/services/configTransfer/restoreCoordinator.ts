import { nanoid } from 'nanoid';

import {
  getFlowSnapshot,
  replaceFlowSnapshot,
  type FlowSnapshot,
  type FlowTemplateDetail,
  type ReplaceFlowSnapshotRequest,
  type ReplaceFlowSnapshotResponse,
} from '../flowsApi';
import {
  defaultSectionRegistry,
  type ConfigSectionRegistry,
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
}

export interface RestoreEnvironment {
  registry: Record<BackupSection, RestoreAdapter>;
  journal: RecoveryJournalStore;
  getFlowSnapshot: () => Promise<FlowSnapshot>;
  replaceFlowSnapshot: (
    request: ReplaceFlowSnapshotRequest,
  ) => Promise<ReplaceFlowSnapshotResponse>;
  createOperationId: () => string;
  now: () => Date;
}

function runtimeRegistry(registry: ConfigSectionRegistry): Record<BackupSection, RestoreAdapter> {
  return registry as unknown as Record<BackupSection, RestoreAdapter>;
}

export const defaultRestoreEnvironment: RestoreEnvironment = {
  registry: runtimeRegistry(defaultSectionRegistry),
  journal: recoveryJournal,
  getFlowSnapshot,
  replaceFlowSnapshot,
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
      Object.keys(record).sort().map((key) => [key, canonicalize(record[key])]),
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

function assertSelectedSections(bundle: ConfigBackupBundle, selected: readonly BackupSection[]): void {
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
    const planned = mode === 'replace'
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
  let flowExpectedRevision: string | undefined;

  for (const section of selectedSections) {
    const adapter = environment.registry[section];
    let current: unknown;
    if (section === 'flows') {
      const snapshot = await environment.getFlowSnapshot();
      current = snapshot.items;
      flowExpectedRevision = snapshot.revision;
    } else {
      current = await adapter.read();
    }
    const planned = planSection(
      section,
      current,
      bundle.data[section],
      mode,
      policy,
      adapter,
    );
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
    flowExpectedRevision,
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
      if (!Array.isArray(sectionPlan.before) || !Array.isArray(sectionPlan.after) || !adapter.identity) {
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

function orderedSections(selected: readonly BackupSection[]): BackupSection[] {
  const set = new Set(selected);
  return BACKUP_SECTIONS.filter((section) => set.has(section)).sort((left, right) => {
    if (left === 'flows') return -1;
    if (right === 'flows') return 1;
    return 0;
  });
}

async function assertTargetsUnchanged(
  plan: RestorePlan,
  environment: RestoreEnvironment,
): Promise<void> {
  for (const section of orderedSections(plan.selectedSections)) {
    if (section === 'flows') {
      const snapshot = await environment.getFlowSnapshot();
      if (!plan.flowExpectedRevision || snapshot.revision !== plan.flowExpectedRevision) {
        throw new RestoreTargetChangedError(section);
      }
      continue;
    }
    const current = await environment.registry[section].read();
    const expected = plan.sections[section]?.beforeFingerprint;
    if (expected === undefined || fingerprintRestoreValue(current) !== expected) {
      throw new RestoreTargetChangedError(section);
    }
  }
}

function makeJournal(plan: RestorePlan): RecoveryJournal {
  const before: RecoveryJournal['before'] = {};
  for (const section of plan.selectedSections) {
    before[section] = plan.sections[section]?.before;
  }
  const flowItems = plan.sections.flows?.before;
  return {
    operationId: plan.operationId,
    startedAt: plan.createdAt,
    phase: 'prepared',
    selectedSections: [...plan.selectedSections],
    before,
    completedSections: [],
    flowBefore: plan.flowExpectedRevision && Array.isArray(flowItems)
      ? { revision: plan.flowExpectedRevision, items: flowItems as FlowTemplateDetail[] }
      : undefined,
    pendingRollback: [],
  };
}

async function applyPlanSections(
  plan: RestorePlan,
  journal: RecoveryJournal,
  environment: RestoreEnvironment,
): Promise<void> {
  journal.phase = 'applying';
  await environment.journal.save(journal);
  for (const section of orderedSections(plan.selectedSections)) {
    const sectionPlan = plan.sections[section];
    if (!sectionPlan) throw new Error(`恢复计划缺少分区 ${section}`);
    if (section === 'flows') {
      if (!plan.flowExpectedRevision || !Array.isArray(sectionPlan.after)) {
        throw new Error('流程恢复计划缺少 revision 或流程列表');
      }
      const result = await environment.replaceFlowSnapshot({
        expectedRevision: plan.flowExpectedRevision,
        items: sectionPlan.after as FlowTemplateDetail[],
      });
      journal.flowAppliedRevision = result.revision;
    } else {
      await environment.registry[section].replace(sectionPlan.after);
    }
    journal.completedSections.push(section);
    await environment.journal.save(journal);
  }
}

async function refreshPlanSections(
  plan: RestorePlan,
  environment: RestoreEnvironment,
): Promise<void> {
  for (const section of orderedSections(plan.selectedSections)) {
    const sectionPlan = plan.sections[section];
    if (sectionPlan) await environment.registry[section].refresh?.(sectionPlan.after);
  }
}

async function rollbackOne(
  section: BackupSection,
  journal: RecoveryJournal,
  environment: RestoreEnvironment,
): Promise<void> {
  if (section === 'flows') {
    if (!journal.flowBefore || !journal.flowAppliedRevision) {
      throw new Error('流程回滚缺少恢复前快照或已应用 revision');
    }
    await environment.replaceFlowSnapshot({
      expectedRevision: journal.flowAppliedRevision,
      items: journal.flowBefore.items,
    });
    return;
  }
  if (!Object.hasOwn(journal.before, section)) {
    throw new Error(`恢复日志缺少分区 ${section} 的原始快照`);
  }
  const before = journal.before[section];
  await environment.registry[section].replace(before);
  await environment.registry[section].refresh?.(before);
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

  const journal = makeJournal(plan);
  await environment.journal.save(journal);
  try {
    await applyPlanSections(plan, journal, environment);
    await refreshPlanSections(plan, environment);
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
