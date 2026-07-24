import type {
  BackupSection,
  ConflictChoice,
  MergeConflictPolicy,
  RestoreConflict,
  RestoreMode,
  SectionStats,
} from './types';

export interface CollectionIdentity<T> {
  id: (item: T) => string;
  name: (item: T) => string;
  clone: (item: T, nextId: string, nextName: string) => T;
  createId: () => string;
}

interface IdentityReader<T> {
  id: (item: T) => string;
  name: (item: T) => string;
}

export interface DuplicateMatch<T> {
  kind: 'none' | 'one' | 'ambiguous';
  matches: T[];
}

export interface PlannedConflict<T> extends RestoreConflict {
  source: T;
  targets: T[];
}

export interface CollectionPlan<T> {
  kind: 'collection';
  section: BackupSection;
  currentItems: T[];
  finalItems: T[];
  conflicts: PlannedConflict<T>[];
  stats: SectionStats;
}

export interface SingletonPlan<T> {
  kind: 'singleton';
  section: BackupSection;
  currentValue: T | null;
  incomingValue: T | null;
  finalValue: T | null;
  conflicts: PlannedConflict<T>[];
  stats: SectionStats;
}

function emptyStats(): SectionStats {
  return { added: 0, overwritten: 0, deleted: 0, skipped: 0, copied: 0 };
}

function structuralIdentity<T>(item: T, key: 'id' | 'name'): string {
  if (item === null || typeof item !== 'object') return '';
  const value = (item as Record<string, unknown>)[key];
  return typeof value === 'string' ? value : '';
}

function defaultIdentity<T>(): IdentityReader<T> {
  return {
    id: (item) => structuralIdentity(item, 'id'),
    name: (item) => structuralIdentity(item, 'name'),
  };
}

export function findDuplicate<T>(
  current: readonly T[],
  incoming: T,
  identity: IdentityReader<T>,
): DuplicateMatch<T> {
  const incomingID = identity.id(incoming);
  const incomingName = identity.name(incoming);
  const byID = incomingID === ''
    ? undefined
    : current.find((item) => identity.id(item) === incomingID);
  const byName = incomingName === ''
    ? undefined
    : current.find((item) => identity.name(item) === incomingName);
  const matches = [...new Set([byID, byName].filter((value): value is T => value !== undefined))];

  if (matches.length === 0) return { kind: 'none', matches };
  if (matches.length === 1) return { kind: 'one', matches };
  return { kind: 'ambiguous', matches };
}

function conflictID(section: BackupSection, sourceID: string, sourceName: string, index: number): string {
  return `${section}:${sourceID}:${sourceName}:${index}`;
}

function makeConflict<T>(
  section: BackupSection,
  source: T,
  targets: T[],
  identity: IdentityReader<T>,
  index: number,
  kind: RestoreConflict['kind'],
): PlannedConflict<T> {
  const sourceID = identity.id(source);
  const sourceName = identity.name(source);
  return {
    id: conflictID(section, sourceID, sourceName, index),
    section,
    kind,
    source,
    targets,
    sourceId: sourceID || undefined,
    sourceName,
    targetIds: targets.map(identity.id),
    targetNames: targets.map(identity.name),
    allowedChoices: kind === 'ambiguous'
      ? ['keep-copy', 'skip']
      : ['overwrite', 'keep-copy', 'skip'],
  };
}

function compareConflicts<T>(left: PlannedConflict<T>, right: PlannedConflict<T>): number {
  if (left.section !== right.section) return left.section < right.section ? -1 : 1;
  if (left.sourceName === right.sourceName) return left.id < right.id ? -1 : 1;
  return left.sourceName < right.sourceName ? -1 : 1;
}

function replaceTarget<T>(items: T[], target: T, incoming: T): void {
  const index = items.indexOf(target);
  if (index < 0) throw new Error('恢复计划中的冲突目标已不存在');
  items[index] = incoming;
}

export function planCollectionMerge<T>(
  current: readonly T[],
  incoming: readonly T[],
  identity: CollectionIdentity<T>,
  policy: MergeConflictPolicy,
  section: BackupSection = 'flows',
): CollectionPlan<T> {
  const finalItems = [...current];
  const conflicts: PlannedConflict<T>[] = [];
  const stats = emptyStats();

  incoming.forEach((source, index) => {
    const duplicate = findDuplicate(finalItems, source, identity);
    if (duplicate.kind === 'none') {
      finalItems.push(source);
      stats.added++;
      return;
    }
    if (policy === 'skip') {
      stats.skipped++;
      return;
    }
    if (policy === 'overwrite' && duplicate.kind === 'one') {
      replaceTarget(finalItems, duplicate.matches[0], source);
      stats.overwritten++;
      return;
    }
    conflicts.push(makeConflict(
      section,
      source,
      duplicate.matches,
      identity,
      index,
      duplicate.kind === 'ambiguous' ? 'ambiguous' : 'duplicate',
    ));
  });

  conflicts.sort(compareConflicts);
  return {
    kind: 'collection',
    section,
    currentItems: [...current],
    finalItems,
    conflicts,
    stats,
  };
}

export function planCollectionReplace<T>(
  current: readonly T[],
  incoming: readonly T[],
  identity: IdentityReader<T> = defaultIdentity<T>(),
  section: BackupSection = 'flows',
): CollectionPlan<T> {
  const unmatched = new Set(current.map((_, index) => index));
  const stats = emptyStats();

  for (const source of incoming) {
    const sourceID = identity.id(source);
    const sourceName = identity.name(source);
    let match = [...unmatched].find((index) => (
      sourceID !== '' && identity.id(current[index]) === sourceID
    ));
    match ??= [...unmatched].find((index) => (
      sourceName !== '' && identity.name(current[index]) === sourceName
    ));
    if (match === undefined) {
      stats.added++;
    } else {
      unmatched.delete(match);
      stats.overwritten++;
    }
  }
  stats.deleted = unmatched.size;

  return {
    kind: 'collection',
    section,
    currentItems: [...current],
    finalItems: [...incoming],
    conflicts: [],
    stats,
  };
}

function splitExtension(name: string): { stem: string; extension: string } {
  const dot = name.lastIndexOf('.');
  if (dot <= 0) return { stem: name, extension: '' };
  return { stem: name.slice(0, dot), extension: name.slice(dot) };
}

export function nextCopyName(name: string, usedNames: ReadonlySet<string>): string {
  const { stem, extension } = splitExtension(name);
  const first = `${stem}（副本）${extension}`;
  if (!usedNames.has(first)) return first;

  for (let copy = 2; ; copy++) {
    const candidate = `${stem}（副本 ${copy}）${extension}`;
    if (!usedNames.has(candidate)) return candidate;
  }
}

function nextCopyID<T>(items: readonly T[], identity: CollectionIdentity<T>): string {
  const used = new Set(items.map(identity.id));
  for (let attempt = 0; attempt < 1000; attempt++) {
    const candidate = identity.createId();
    if (candidate !== '' && !used.has(candidate)) return candidate;
  }
  throw new Error('无法为保留副本生成唯一标识');
}

function assertChoiceAllowed(conflict: RestoreConflict, choice: ConflictChoice): void {
  if (!conflict.allowedChoices.includes(choice)) {
    if (choice === 'keep-copy' && conflict.allowedChoices.length === 2) {
      throw new Error('单例分区不支持保留副本');
    }
    throw new Error(`冲突 ${conflict.sourceName} 不支持选择 ${choice}`);
  }
}

export function applyConflictChoices<T>(
  plan: CollectionPlan<T>,
  choices: Readonly<Record<string, ConflictChoice>>,
  identity: CollectionIdentity<T>,
): CollectionPlan<T>;
export function applyConflictChoices<T>(
  plan: SingletonPlan<T>,
  choices: Readonly<Record<string, ConflictChoice>>,
): SingletonPlan<T>;
export function applyConflictChoices<T>(
  plan: CollectionPlan<T> | SingletonPlan<T>,
  choices: Readonly<Record<string, ConflictChoice>>,
  identity?: CollectionIdentity<T>,
): CollectionPlan<T> | SingletonPlan<T> {
  if (plan.kind === 'singleton') {
    const stats = { ...plan.stats };
    let finalValue = plan.finalValue;
    const conflicts: PlannedConflict<T>[] = [];
    for (const conflict of plan.conflicts) {
      const choice = choices[conflict.id];
      if (choice === undefined) {
        conflicts.push(conflict);
        continue;
      }
      if (choice === 'keep-copy') {
        throw new Error('单例分区不支持保留副本');
      }
      assertChoiceAllowed(conflict, choice);
      if (choice === 'overwrite') {
        finalValue = conflict.source;
        stats.overwritten++;
      } else {
        finalValue = plan.currentValue;
        stats.skipped++;
      }
    }
    return { ...plan, finalValue, conflicts, stats };
  }

  if (identity === undefined) {
    throw new Error('集合冲突处理缺少身份规则');
  }
  const stats = { ...plan.stats };
  const finalItems = [...plan.finalItems];
  const conflicts: PlannedConflict<T>[] = [];
  for (const conflict of plan.conflicts) {
    const choice = choices[conflict.id];
    if (choice === undefined) {
      conflicts.push(conflict);
      continue;
    }
    assertChoiceAllowed(conflict, choice);
    if (choice === 'overwrite') {
      replaceTarget(finalItems, conflict.targets[0], conflict.source);
      stats.overwritten++;
    } else if (choice === 'keep-copy') {
      const usedNames = new Set(finalItems.map(identity.name));
      const sourceName = identity.name(conflict.source);
      const copy = identity.clone(
        conflict.source,
        nextCopyID(finalItems, identity),
        usedNames.has(sourceName) ? nextCopyName(sourceName, usedNames) : sourceName,
      );
      finalItems.push(copy);
      stats.copied++;
    } else {
      stats.skipped++;
    }
  }
  return { ...plan, finalItems, conflicts, stats };
}

export function planSingleton<T>(
  current: T | null,
  incoming: T | null,
  mode: RestoreMode,
  policy: MergeConflictPolicy,
  section: BackupSection,
): SingletonPlan<T> {
  const stats = emptyStats();
  const plan: SingletonPlan<T> = {
    kind: 'singleton',
    section,
    currentValue: current,
    incomingValue: incoming,
    finalValue: current,
    conflicts: [],
    stats,
  };

  if (mode === 'replace') {
    plan.finalValue = incoming;
    if (current === null && incoming !== null) stats.added++;
    else if (current !== null && incoming === null) stats.deleted++;
    else if (current !== null && incoming !== null) stats.overwritten++;
    return plan;
  }
  if (incoming === null) return plan;
  if (current === null) {
    plan.finalValue = incoming;
    stats.added++;
    return plan;
  }
  if (policy === 'overwrite') {
    plan.finalValue = incoming;
    stats.overwritten++;
    return plan;
  }
  if (policy === 'skip') {
    stats.skipped++;
    return plan;
  }

  plan.conflicts = [{
    id: conflictID(section, '', section, 0),
    section,
    kind: 'duplicate',
    source: incoming,
    targets: [current],
    sourceName: section,
    targetIds: [],
    targetNames: [section],
    allowedChoices: ['overwrite', 'skip'],
  }];
  return plan;
}
