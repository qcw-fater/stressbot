import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';

import type { ConflictChoice, RestoreConflict } from '@/services/configTransfer/types';
import { ConflictResolutionView } from './ConflictResolutionView';

const collectionConflicts: RestoreConflict[] = [
  {
    id: 'proto:one',
    section: 'protoFiles',
    kind: 'duplicate',
    sourceName: 'login.proto',
    targetIds: ['login.proto'],
    targetNames: ['login.proto'],
    allowedChoices: ['overwrite', 'keep-copy', 'skip'],
  },
  {
    id: 'proto:two',
    section: 'protoFiles',
    kind: 'duplicate',
    sourceName: 'battle.proto',
    targetIds: ['battle.proto'],
    targetNames: ['battle.proto'],
    allowedChoices: ['overwrite', 'keep-copy', 'skip'],
  },
];

function ControlledConflicts({
  conflicts = collectionConflicts,
  onChange = vi.fn(),
}: {
  conflicts?: RestoreConflict[];
  onChange?: (choices: Record<string, ConflictChoice>) => void;
}) {
  const [choices, setChoices] = useState<Record<string, ConflictChoice>>({});
  return (
    <ConflictResolutionView
      conflicts={conflicts}
      choices={choices}
      onChange={(next) => {
        setChoices(next);
        onChange(next);
      }}
    />
  );
}

describe('ConflictResolutionView', () => {
  it('applies a choice to remaining conflicts of the same type', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<ControlledConflicts onChange={onChange} />);
    const first = screen.getByTestId('conflict-proto:one');

    await user.click(within(first).getByRole('radio', { name: '覆盖' }));
    await user.click(within(first).getByRole('checkbox', { name: '应用到剩余同类冲突' }));

    expect(onChange).toHaveBeenLastCalledWith({
      'proto:one': 'overwrite',
      'proto:two': 'overwrite',
    });
  });

  it('omits keep-copy for singleton conflicts', () => {
    render(<ControlledConflicts conflicts={[{
      id: 'draft:one',
      section: 'draft',
      kind: 'duplicate',
      sourceName: '当前编辑稿',
      targetIds: [],
      targetNames: ['当前编辑稿'],
      allowedChoices: ['overwrite', 'skip'],
    }]} />);

    const row = screen.getByTestId('conflict-draft:one');
    expect(within(row).queryByRole('radio', { name: '保留两份' })).toBeNull();
    expect(within(row).getByRole('radio', { name: '覆盖' })).toBeTruthy();
    expect(within(row).getByRole('radio', { name: '忽略' })).toBeTruthy();
  });
});
