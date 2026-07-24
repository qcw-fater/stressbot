import { beforeEach, describe, expect, it } from 'vitest';

import {
  applyConflictChoices,
  findDuplicate,
  nextCopyName,
  planCollectionMerge,
  planCollectionReplace,
  planSingleton,
  type CollectionIdentity,
} from './restorePlanner';

interface Item {
  id: string;
  name: string;
  value: string;
}

function item(id: string, name: string, value = name): Item {
  return { id, name, value };
}

let nextID = 0;
const identity: CollectionIdentity<Item> = {
  id: (value) => value.id,
  name: (value) => value.name,
  clone: (value, id, name) => ({ ...value, id, name }),
  createId: () => `copy-${++nextID}`,
};

beforeEach(() => {
  nextID = 0;
});

describe('findDuplicate', () => {
  it('matches exact file names within one section', () => {
    const fileIdentity: CollectionIdentity<Item> = {
      ...identity,
      id: (value) => value.name,
    };

    expect(
      findDuplicate([item('old', 'login.lua')], item('new', 'login.lua'), fileIdentity),
    ).toMatchObject({ kind: 'one' });
  });

  it('matches either an id or an exact name', () => {
    const current = [item('id-1', 'A'), item('id-2', 'B')];

    expect(findDuplicate(current, item('id-1', 'Renamed'), identity)).toMatchObject({
      kind: 'one',
      matches: [current[0]],
    });
    expect(findDuplicate(current, item('id-3', 'B'), identity)).toMatchObject({
      kind: 'one',
      matches: [current[1]],
    });
  });

  it('reports ambiguity when id and name hit different targets', () => {
    const current = [item('id-1', 'A'), item('id-2', 'B')];

    expect(findDuplicate(current, item('id-1', 'B'), identity)).toMatchObject({
      kind: 'ambiguous',
      matches: current,
    });
  });
});

describe('planCollectionMerge', () => {
  it('overwrites duplicate items and adds new items with overwrite-all', () => {
    const current = [item('id-1', 'A', 'old')];
    const incoming = [item('id-1', 'A', 'new'), item('id-2', 'B')];

    const plan = planCollectionMerge(current, incoming, identity, 'overwrite', 'flows');

    expect(plan.finalItems).toEqual(incoming);
    expect(plan.conflicts).toEqual([]);
    expect(plan.stats).toEqual({
      added: 1,
      overwritten: 1,
      deleted: 0,
      skipped: 0,
      copied: 0,
    });
  });

  it('keeps duplicate targets with skip-all', () => {
    const current = [item('id-1', 'A', 'old')];
    const plan = planCollectionMerge(
      current,
      [item('id-1', 'A', 'new')],
      identity,
      'skip',
      'flows',
    );

    expect(plan.finalItems).toEqual(current);
    expect(plan.conflicts).toEqual([]);
    expect(plan.stats.skipped).toBe(1);
  });

  it('leaves ambiguous matches unresolved under overwrite-all', () => {
    const current = [item('id-1', 'A'), item('id-2', 'B')];
    const incoming = [item('id-1', 'B')];

    const plan = planCollectionMerge(current, incoming, identity, 'overwrite', 'flows');

    expect(plan.conflicts[0]).toMatchObject({
      kind: 'ambiguous',
      sourceId: 'id-1',
      allowedChoices: ['keep-copy', 'skip'],
    });
    expect(plan.finalItems).toEqual(current);
  });

  it('sorts prompt conflicts by source name', () => {
    const current = [item('id-a', 'A'), item('id-b', 'B')];
    const incoming = [item('id-b', 'B', 'new-b'), item('id-a', 'A', 'new-a')];

    const plan = planCollectionMerge(current, incoming, identity, 'prompt', 'flows');

    expect(plan.conflicts.map((conflict) => conflict.sourceName)).toEqual(['A', 'B']);
  });

  it('applies per-item overwrite, keep-copy, and skip choices immutably', () => {
    const current = [item('id-a', 'A', 'old-a'), item('id-b', 'B', 'old-b')];
    const incoming = [item('id-a', 'A', 'new-a'), item('id-b', 'B', 'new-b')];
    const pending = planCollectionMerge(current, incoming, identity, 'prompt', 'flows');
    const [conflictA, conflictB] = pending.conflicts;

    const copied = applyConflictChoices(
      pending,
      {
        [conflictA.id]: 'keep-copy',
        [conflictB.id]: 'skip',
      },
      identity,
    );

    expect(pending.finalItems).toEqual(current);
    expect(copied.conflicts).toEqual([]);
    expect(copied.finalItems).toEqual([...current, item('copy-1', 'A（副本）', 'new-a')]);
    expect(copied.stats).toMatchObject({ copied: 1, skipped: 1 });

    const overwritten = applyConflictChoices(
      pending,
      {
        [conflictA.id]: 'overwrite',
      },
      identity,
    );
    expect(overwritten.finalItems[0]).toEqual(item('id-a', 'A', 'new-a'));
    expect(overwritten.conflicts).toHaveLength(1);
    expect(overwritten.stats.overwritten).toBe(1);
  });

  it('keeps an already unique source name when only the id conflicts', () => {
    const pending = planCollectionMerge(
      [item('id-a', 'Old name')],
      [item('id-a', 'Imported name')],
      identity,
      'prompt',
      'flows',
    );
    const conflict = pending.conflicts[0];

    const resolved = applyConflictChoices(
      pending,
      {
        [conflict.id]: 'keep-copy',
      },
      identity,
    );

    expect(resolved.finalItems[1]).toEqual(item('copy-1', 'Imported name'));
  });

  it('rechecks a shared target after an earlier overwrite changes its identity', () => {
    const incoming = [item('id-a', 'Renamed', 'first'), item('id-b', 'Original', 'second')];
    const pending = planCollectionMerge(
      [item('id-a', 'Original', 'old')],
      incoming,
      identity,
      'prompt',
      'flows',
    );

    const resolved = applyConflictChoices(
      pending,
      Object.fromEntries(pending.conflicts.map((conflict) => [conflict.id, 'overwrite' as const])),
      identity,
    );

    expect(resolved.finalItems).toHaveLength(2);
    expect(resolved.finalItems).toEqual(expect.arrayContaining(incoming));
    expect(resolved.stats).toMatchObject({ added: 1, overwritten: 1 });
  });
});

describe('planCollectionReplace', () => {
  it('removes target-only items during complete restore', () => {
    const plan = planCollectionReplace([item('old', 'Old')], [item('new', 'New')], identity);

    expect(plan.finalItems.map((value) => value.id)).toEqual(['new']);
    expect(plan.stats).toMatchObject({ added: 1, deleted: 1 });
  });

  it('counts matching items as overwritten', () => {
    const plan = planCollectionReplace(
      [item('same', 'Old', 'old')],
      [item('same', 'New', 'new')],
      identity,
    );

    expect(plan.stats).toMatchObject({ overwritten: 1, deleted: 0, added: 0 });
  });
});

describe('singleton planning', () => {
  it('does not allow keeping a second singleton copy', () => {
    const pending = planSingleton(
      { value: 'old' },
      { value: 'new' },
      'merge',
      'prompt',
      'errorMap',
    );
    const conflict = pending.conflicts[0];

    expect(conflict.allowedChoices).toEqual(['overwrite', 'skip']);
    expect(() =>
      applyConflictChoices(pending, {
        [conflict.id]: 'keep-copy',
      }),
    ).toThrow('单例分区不支持保留副本');
  });

  it('clears an existing singleton only during complete restore', () => {
    const replaced = planSingleton({ value: 'old' }, null, 'replace', 'overwrite', 'draft');
    const merged = planSingleton({ value: 'old' }, null, 'merge', 'overwrite', 'draft');

    expect(replaced.finalValue).toBeNull();
    expect(replaced.stats.deleted).toBe(1);
    expect(merged.finalValue).toEqual({ value: 'old' });
    expect(merged.stats.deleted).toBe(0);
  });
});

describe('nextCopyName', () => {
  it('allocates a unique name before the extension', () => {
    expect(nextCopyName('login.lua', new Set(['login.lua', 'login（副本）.lua']))).toBe(
      'login（副本 2）.lua',
    );
  });
});
