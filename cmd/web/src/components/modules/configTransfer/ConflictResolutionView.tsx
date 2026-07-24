import { Checkbox, Radio, Typography } from 'antd';
import { useEffect, useState } from 'react';

import type {
  BackupSection,
  ConflictChoice,
  RestoreConflict,
} from '@/services/configTransfer/types';
import './ConfigRestoreModal.css';

const CHOICE_LABELS: Record<ConflictChoice, string> = {
  overwrite: '覆盖',
  'keep-copy': '保留两份',
  skip: '忽略',
};

interface DetailedConflict extends RestoreConflict {
  source?: unknown;
  targets?: unknown[];
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

function conflictMetadata(conflict: DetailedConflict): string | undefined {
  const source = recordValue(conflict.source);
  if (!source) return undefined;
  const parts: string[] = [];
  const timestamp = source.updatedAt ?? source.uploadedAt ?? source.createdAt;
  if (typeof timestamp === 'string') {
    const parsed = new Date(timestamp);
    if (!Number.isNaN(parsed.getTime())) parts.push(`来源更新于 ${parsed.toLocaleString()}`);
  }
  if (typeof source.content === 'string') parts.push(`内容 ${source.content.length} 个字符`);
  const flow = recordValue(source.flow);
  const nodes = flow ? recordValue(flow.nodes) : undefined;
  if (nodes) parts.push(`流程 ${Object.keys(nodes).length} 个节点`);
  return parts.length > 0 ? parts.join('；') : undefined;
}

function sameConflictType(left: RestoreConflict, right: RestoreConflict): boolean {
  return (
    left.section === right.section &&
    left.kind === right.kind &&
    left.allowedChoices.join('|') === right.allowedChoices.join('|')
  );
}

export interface ConflictResolutionViewProps {
  conflicts: RestoreConflict[];
  choices: Readonly<Record<string, ConflictChoice>>;
  onChange: (choices: Record<string, ConflictChoice>) => void;
  sectionLabel?: (section: BackupSection) => string;
}

export function ConflictResolutionView({
  conflicts,
  choices,
  onChange,
  sectionLabel = (section) => section,
}: ConflictResolutionViewProps) {
  const [applyToRemaining, setApplyToRemaining] = useState<Record<string, boolean>>({});
  const grouped = conflicts.reduce<Map<BackupSection, RestoreConflict[]>>((groups, conflict) => {
    const items = groups.get(conflict.section);
    if (items) items.push(conflict);
    else groups.set(conflict.section, [conflict]);
    return groups;
  }, new Map());

  useEffect(() => {
    setApplyToRemaining({});
  }, [conflicts]);

  const applyChoice = (conflict: RestoreConflict, choice: ConflictChoice) => {
    const next = { ...choices, [conflict.id]: choice };
    if (applyToRemaining[conflict.id]) {
      for (const candidate of conflicts) {
        if (sameConflictType(conflict, candidate) && candidate.allowedChoices.includes(choice)) {
          next[candidate.id] = choice;
        }
      }
    }
    onChange(next);
  };

  const toggleApply = (conflict: RestoreConflict, checked: boolean) => {
    setApplyToRemaining((current) => ({ ...current, [conflict.id]: checked }));
    const choice = choices[conflict.id];
    if (!checked || !choice) return;
    const next = { ...choices };
    for (const candidate of conflicts) {
      if (sameConflictType(conflict, candidate) && candidate.allowedChoices.includes(choice)) {
        next[candidate.id] = choice;
      }
    }
    onChange(next);
  };

  return (
    <div className="config-restore__conflicts">
      <Typography.Title level={5}>处理重复内容</Typography.Title>
      {[...grouped.entries()].map(([section, items]) => (
        <section key={section} aria-label={sectionLabel(section)}>
          <Typography.Text strong>{sectionLabel(section)}</Typography.Text>
          <div className="config-restore__conflict-list">
            {items.map((conflict) => {
              const metadata = conflictMetadata(conflict as DetailedConflict);
              return (
                <div
                  key={conflict.id}
                  data-testid={`conflict-${conflict.id}`}
                  className="config-restore__conflict-row"
                >
                  <div className="config-restore__conflict-copy">
                    <Typography.Text>{conflict.sourceName}</Typography.Text>
                    <Typography.Text type="secondary">
                      目标：{conflict.targetNames.join('、') || sectionLabel(section)}
                    </Typography.Text>
                    {metadata && <Typography.Text type="secondary">{metadata}</Typography.Text>}
                  </div>
                  <Radio.Group
                    value={choices[conflict.id]}
                    onChange={(event) =>
                      applyChoice(conflict, event.target.value as ConflictChoice)
                    }
                  >
                    {conflict.allowedChoices.map((choice) => (
                      <Radio key={choice} value={choice}>
                        {CHOICE_LABELS[choice]}
                      </Radio>
                    ))}
                  </Radio.Group>
                  <Checkbox
                    checked={applyToRemaining[conflict.id] ?? false}
                    onChange={(event) => toggleApply(conflict, event.target.checked)}
                  >
                    应用到剩余同类冲突
                  </Checkbox>
                </div>
              );
            })}
          </div>
        </section>
      ))}
    </div>
  );
}
