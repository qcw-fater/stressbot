/**
 * 基线资源同步冲突解决面板。
 *
 * 当服务端基线资源（proto / lua / adapter）与本地 IDB 内容不同时弹出，
 * 用户通过 Monaco DiffEditor 逐个查看差异并选择保留本地版本或采用服务器版本。
 *
 * 性能优化：一次只渲染一个 DiffEditor 实例，通过导航切换，
 * 避免差异项过多时同时创建大量 Monaco 实例导致内存溢出或卡顿。
 */

import { Button, Modal, Radio, Space, Tag, Typography } from 'antd';
import { DiffEditor } from '@monaco-editor/react';
import type { editor } from 'monaco-editor';
import type { BaselineSyncResult, ConflictDecision, ResourceType, SyncDiff } from '@/services/resourcesStore';
import { applyConflictResolution } from '@/services/resourcesStore';
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';
import { useFloatingWindowStore } from '@/components/FlowEditor/store/floatingWindowStore';
import { saveSkippedConflict, hashContent } from '@/components/FlowEditor/skippedConflicts';

export interface BaselineSyncModalProps {
  open: boolean;
  result: BaselineSyncResult;
  onClose: () => void;
  /** 冲突已成功处理 */
  onResolved?: (decisions: ConflictDecision[]) => void | Promise<void>;
  title?: string;
  description?: ReactNode;
  localLabel?: string;
  serverLabel?: string;
  applyResolution?: (decisions: ConflictDecision[]) => Promise<void>;
  saveSkipped?: boolean;
}

const TYPE_LABEL: Record<ResourceType, { text: string; color: string }> = {
  proto: { text: 'Proto', color: 'blue' },
  script: { text: 'Lua', color: 'purple' },
  adapter: { text: 'Adapter', color: 'orange' },
};

export function BaselineSyncModal({
  open,
  result,
  onClose,
  onResolved,
  title = '服务器资源变更',
  description = '服务器资源有变更，请逐个确认保留本地版本还是采用服务器版本。',
  localLabel = '保留本地版本',
  serverLabel = '采用服务器版本',
  applyResolution = applyConflictResolution,
  saveSkipped = true,
}: BaselineSyncModalProps) {
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;
  const themeMode = useEditorStore((s) => s.theme);
  const [decisions, setDecisions] = useState<Record<string, boolean>>({});
  const [applying, setApplying] = useState(false);
  const [currentIdx, setCurrentIdx] = useState(0);
  const editorRef = useRef<editor.IDiffEditor | null>(null);

  useEffect(() => {
    if (!open) return;
    setDecisions({});
    setCurrentIdx(0);
  }, [open, result]);

  const conflicts = result.conflicts;
  const removed = result.removed;
  const hasConflicts = conflicts.length > 0 || removed.length > 0;

  if (!hasConflicts) return null;

  const allItems: SyncDiff[] = [...conflicts, ...removed];
  const total = allItems.length;
  // 确保 currentIdx 在有效范围内
  const idx = Math.min(currentIdx, total - 1);
  const item = allItems[idx];
  const decisionKey = (it: SyncDiff) => `${it.type}:${it.name}`;

  function getDecision(it: SyncDiff): boolean {
    return decisions[decisionKey(it)] ?? true; // 默认保留本地
  }

  function setDecision(it: SyncDiff, keepLocal: boolean) {
    setDecisions((prev) => ({ ...prev, [decisionKey(it)]: keepLocal }));
  }

  function setAll(keepLocal: boolean) {
    const next: Record<string, boolean> = {};
    for (const it of allItems) {
      next[decisionKey(it)] = keepLocal;
    }
    setDecisions(next);
  }

  // 统计已选择数量
  const decidedCount = allItems.filter((it) => decisions[decisionKey(it)] !== undefined).length;

  const handleEditorMount = useCallback((ed: editor.IDiffEditor) => {
    editorRef.current = ed;
  }, []);

  // 切换项时清理上一个编辑器模型
  function navigateTo(newIdx: number) {
    if (editorRef.current) {
      try {
        editorRef.current.getModifiedEditor()?.setModel(null);
        editorRef.current.getOriginalEditor()?.setModel(null);
      } catch { /* ignore */ }
      editorRef.current = null;
    }
    setCurrentIdx(newIdx);
  }

  async function handleApply() {
    setApplying(true);
    try {
      const decArray: ConflictDecision[] = allItems.map((it) => ({
        type: it.type,
        name: it.name,
        keepLocal: getDecision(it),
      }));
      if (saveSkipped) {
        // 保留本地的项目记录跳过，下次同步不再重复提示
        for (const it of allItems) {
          if (getDecision(it)) {
            const isRemoved = removed.includes(it);
            const key = isRemoved
              ? `${it.type}:${it.name}:__removed__`
              : `${it.type}:${it.name}:${hashContent(it.baselineContent)}`;
            saveSkippedConflict(key);
          }
        }
      }
      await applyResolution(decArray);
      onClose();
      await onResolved?.(decArray);
    } finally {
      setApplying(false);
    }
  }

  function handleCancel() {
    onClose();
  }

  const isRemoved = removed.includes(item);
  const keepLocal = getDecision(item);
  const label = TYPE_LABEL[item.type];

  return (
    <Modal
      title={title}
      open={open}
      onCancel={handleCancel}
      width={860}
      styles={{ mask: { zIndex: popupZ }, wrapper: { zIndex: popupZ + 1 } }}
      footer={
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <Space>
            <Button size="small" onClick={() => setAll(true)}>
              全部保留本地
            </Button>
            <Button size="small" onClick={() => setAll(false)}>
              全部采用服务器
            </Button>
          </Space>
          <Space>
            <Button onClick={handleCancel}>取消</Button>
            <Button type="primary" loading={applying} onClick={handleApply}>
              应用选择
            </Button>
          </Space>
        </div>
      }
      destroyOnHidden={false}
    >
      <Typography.Paragraph type="secondary" style={{ marginBottom: 12 }}>
        {description}
      </Typography.Paragraph>

      {/* 导航栏：当前项信息 + 上一个/下一个 */}
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 12 }}>
        <Space size={8}>
          <Tag color={label.color}>{label.text}</Tag>
          <Typography.Text strong>{item.name}</Typography.Text>
          {isRemoved && <Tag color="red">服务器未找到</Tag>}
        </Space>
        <Space size={8}>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {idx + 1} / {total}
            {decidedCount > 0 && `（已选 ${decidedCount} 项）`}
          </Typography.Text>
          <Button
            size="small"
            disabled={idx === 0}
            onClick={() => navigateTo(idx - 1)}
          >
            上一个
          </Button>
          <Button
            size="small"
            disabled={idx === total - 1}
            onClick={() => navigateTo(idx + 1)}
          >
            下一个
          </Button>
        </Space>
      </div>

      {/* 当前项的选择 */}
      <div style={{ marginBottom: 12 }}>
        <Radio.Group
          value={keepLocal ? 'local' : 'remote'}
          onChange={(e) => setDecision(item, e.target.value === 'local')}
          size="small"
        >
          <Radio.Button value="local">{localLabel}</Radio.Button>
          <Radio.Button value="remote">
            {isRemoved ? '删除本地版本' : serverLabel}
          </Radio.Button>
        </Radio.Group>
      </div>

      {/* DiffEditor：只渲染当前项 */}
      {!isRemoved ? (
        <div style={{ border: '1px solid var(--border-color)', borderRadius: 6, overflow: 'hidden' }}>
          <DiffEditor
            key={decisionKey(item)}
            height={360}
            original={item.localContent}
            modified={item.baselineContent}
            language={item.type === 'proto' ? 'protobuf' : 'lua'}
            theme={themeMode === 'dark' ? 'vs-dark' : 'light'}
            onMount={handleEditorMount}
            options={{
              readOnly: true,
              renderSideBySide: true,
              minimap: { enabled: false },
              scrollBeyondLastLine: false,
              folding: false,
              lineNumbers: 'on',
              renderOverviewRuler: true,
              fixedOverflowWidgets: true,
            }}
          />
        </div>
      ) : (
        <div style={{
          border: '1px solid var(--border-color)',
          borderRadius: 6,
          padding: 24,
          textAlign: 'center',
          background: 'var(--bg-elevated)',
        }}>
          <Typography.Text type="secondary">服务器中未找到该文件</Typography.Text>
        </div>
      )}

      {/* 底部快捷导航：已跳转的项用小圆点标记选择状态 */}
      {total > 1 && (
        <div style={{ display: 'flex', justifyContent: 'center', gap: 6, marginTop: 12, flexWrap: 'wrap' }}>
          {allItems.map((it, i) => {
            const d = decisions[decisionKey(it)];
            return (
              <button
                key={decisionKey(it)}
                onClick={() => navigateTo(i)}
                style={{
                  width: 10,
                  height: 10,
                  borderRadius: '50%',
                  border: i === idx ? '2px solid var(--color-blue)' : '1px solid var(--border-color)',
                  background: d === undefined
                    ? 'transparent'
                    : d
                      ? 'var(--color-blue)'
                      : 'var(--color-orange)',
                  cursor: 'pointer',
                  padding: 0,
                  transition: 'all 0.2s',
                }}
                aria-label={it.name}
              />
            );
          })}
        </div>
      )}
    </Modal>
  );
}
