/**
 * 基线资源同步冲突解决面板。
 *
 * 当服务端基线资源（proto / lua / adapter）与本地 IDB 内容不同时弹出，
 * 用户通过 Monaco DiffEditor 查看差异并逐个选择保留本地版本或采用远端版本。
 */

import { Button, Modal, Radio, Space, Tag, Typography } from 'antd';
import { DiffEditor } from '@monaco-editor/react';
import type { BaselineSyncResult, ConflictDecision, ResourceType, SyncDiff } from '@/services/resourcesStore';
import { applyConflictResolution } from '@/services/resourcesStore';
import { useState } from 'react';
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';

export interface BaselineSyncModalProps {
  open: boolean;
  result: BaselineSyncResult;
  onClose: () => void;
  /** 冲突已成功处理 */
  onResolved?: () => void;
}

const TYPE_LABEL: Record<ResourceType, { text: string; color: string }> = {
  proto: { text: 'Proto', color: 'blue' },
  script: { text: 'Lua', color: 'purple' },
  adapter: { text: 'Adapter', color: 'orange' },
};

export function BaselineSyncModal({ open, result, onClose, onResolved }: BaselineSyncModalProps) {
  const themeMode = useEditorStore((s) => s.theme);
  const [decisions, setDecisions] = useState<Record<string, boolean>>({});
  const [applying, setApplying] = useState(false);

  const conflicts = result.conflicts;
  const removed = result.removed;
  const hasConflicts = conflicts.length > 0 || removed.length > 0;

  if (!hasConflicts) return null;

  const allItems: SyncDiff[] = [...conflicts, ...removed];
  const decisionKey = (item: SyncDiff) => `${item.type}:${item.name}`;

  function getDecision(item: SyncDiff): boolean {
    return decisions[decisionKey(item)] ?? true; // 默认保留本地
  }

  function setDecision(item: SyncDiff, keepLocal: boolean) {
    setDecisions((prev) => ({ ...prev, [decisionKey(item)]: keepLocal }));
  }

  function setAll(keepLocal: boolean) {
    const next: Record<string, boolean> = {};
    for (const item of allItems) {
      next[decisionKey(item)] = keepLocal;
    }
    setDecisions(next);
  }

  async function handleApply() {
    setApplying(true);
    try {
      const decArray: ConflictDecision[] = allItems.map((item) => ({
        type: item.type,
        name: item.name,
        keepLocal: getDecision(item),
      }));
      await applyConflictResolution(decArray);
      onResolved?.();
      onClose();
    } finally {
      setApplying(false);
    }
  }

  return (
    <Modal
      title="远端资源变更"
      open={open}
      onCancel={onClose}
      width={860}
      footer={
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <Space>
            <Button size="small" onClick={() => setAll(true)}>
              全部保留本地
            </Button>
            <Button size="small" onClick={() => setAll(false)}>
              全部采用远端
            </Button>
          </Space>
          <Space>
            <Button onClick={onClose}>取消</Button>
            <Button type="primary" loading={applying} onClick={handleApply}>
              应用选择
            </Button>
          </Space>
        </div>
      }
      destroyOnHidden
    >
      <Typography.Paragraph type="secondary" style={{ marginBottom: 16 }}>
        远端资源有变更，请逐个确认保留本地版本还是采用远端版本。
      </Typography.Paragraph>

      {allItems.map((item) => {
        const key = decisionKey(item);
        const isRemoved = removed.includes(item);
        const keepLocal = getDecision(item);
        const label = TYPE_LABEL[item.type];

        return (
          <div key={key} style={{ marginBottom: 20, border: '1px solid var(--color-border, #e8e8e8)', borderRadius: 6, padding: 12 }}>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 8 }}>
              <Space size={8}>
                <Tag color={label.color}>{label.text}</Tag>
                <Typography.Text strong>{item.name}</Typography.Text>
                {isRemoved && <Tag color="red">远端已删除</Tag>}
              </Space>
              <Radio.Group
                value={keepLocal ? 'local' : 'remote'}
                onChange={(e) => setDecision(item, e.target.value === 'local')}
                size="small"
              >
                <Radio.Button value="local">保留本地</Radio.Button>
                <Radio.Button value="remote">
                  {isRemoved ? '删除本地' : '采用远端'}
                </Radio.Button>
              </Radio.Group>
            </div>

            {!isRemoved && (
              <DiffEditor
                height={240}
                original={item.localContent}
                modified={item.baselineContent}
                language={item.type === 'proto' ? 'protobuf' : 'lua'}
                theme={themeMode === 'dark' ? 'vs-dark' : 'light'}
                options={{
                  readOnly: true,
                  renderSideBySide: true,
                  minimap: { enabled: false },
                  scrollBeyondLastLine: false,
                  folding: false,
                  lineNumbers: 'on',
                  renderOverviewRuler: true,
                }}
              />
            )}
          </div>
        );
      })}
    </Modal>
  );
}
