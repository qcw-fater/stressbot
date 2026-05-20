/**
 * 记事本 — 左侧文件列表：搜索 / 新建 / 导入 / 重命名 / 删除。
 */

import {
  DeleteOutlined,
  EditOutlined,
  FileAddOutlined,
  ImportOutlined,
  SearchOutlined,
} from '@ant-design/icons';
import { App as AntApp, Button, Input, Modal, Popconfirm, Tooltip } from 'antd';
import { useCallback, useRef, useState } from 'react';
import { useNotepadStore, type NotepadFileMeta } from './notepadStore';
import { useFloatingWindowStore } from '@/components/FlowEditor/store/floatingWindowStore';

const LANG_ICON: Record<string, { char: string; color: string }> = {
  lua: { char: '🌙', color: '#f5a623' },
  proto: { char: '◆', color: '#52c41a' },
  json: { char: '{}', color: '#fa8c16' },
  javascript: { char: 'JS', color: '#f7df1e' },
  typescript: { char: 'TS', color: '#3178c6' },
  go: { char: 'Go', color: '#00add8' },
  csharp: { char: 'C#', color: '#9b4f96' },
  python: { char: '🐍', color: '#3776ab' },
  xml: { char: '<>', color: '#e44d26' },
  html: { char: '</', color: '#e44d26' },
  css: { char: '#', color: '#264de4' },
  yaml: { char: '—', color: '#cb171e' },
  sql: { char: '⊞', color: '#00758f' },
  markdown: { char: 'M↓', color: '#083fa1' },
  shell: { char: '$_', color: '#4eaa25' },
};

function langIcon(lang: string) {
  const info = LANG_ICON[lang];
  if (info) return info;
  return { char: '📄', color: '#999' };
}

export function FileSidebar() {
  const { message } = AntApp.useApp();
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;
  const files = useNotepadStore((s) => s.files);
  const activeFileId = useNotepadStore((s) => s.activeFileId);
  const searchQuery = useNotepadStore((s) => s.searchQuery);
  const selectFile = useNotepadStore((s) => s.selectFile);
  const createFile = useNotepadStore((s) => s.createFile);
  const importFiles = useNotepadStore((s) => s.importFiles);
  const renameFile = useNotepadStore((s) => s.renameFile);
  const deleteFile = useNotepadStore((s) => s.deleteFile);
  const setSearchQuery = useNotepadStore((s) => s.setSearchQuery);
  const flushPendingSave = useNotepadStore((s) => s.flushPendingSave);

  const [createOpen, setCreateOpen] = useState(false);
  const [createName, setCreateName] = useState('');
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const [renameValue, setRenameValue] = useState('');

  const importRef = useRef<HTMLInputElement>(null);

  const filtered = searchQuery
    ? files.filter((f) => f.name.toLowerCase().includes(searchQuery.toLowerCase()))
    : files;

  const handleCreate = useCallback(async () => {
    const name = createName.trim();
    if (!name) return;
    const meta = await createFile(name);
    setCreateOpen(false);
    setCreateName('');
    await selectFile(meta.id);
  }, [createName, createFile, selectFile]);

  const handleImport = useCallback(
    async (e: React.ChangeEvent<HTMLInputElement>) => {
      const fileList = e.target.files;
      if (!fileList?.length) return;
      const items: { name: string; content: string }[] = [];
      for (let i = 0; i < fileList.length; i++) {
        const f = fileList[i];
        const text = await f.text();
        items.push({ name: f.name, content: text });
      }
      await importFiles(items);
      message.success(`已导入 ${items.length} 个文件`);
      // reset input
      e.target.value = '';
    },
    [importFiles, message],
  );

  const startRename = useCallback((f: NotepadFileMeta) => {
    setRenamingId(f.id);
    setRenameValue(f.name);
  }, []);

  const confirmRename = useCallback(async () => {
    if (!renamingId || !renameValue.trim()) return;
    await renameFile(renamingId, renameValue.trim());
    setRenamingId(null);
  }, [renamingId, renameValue, renameFile]);

  const handleSelect = useCallback(
    async (id: string) => {
      if (activeFileId) await flushPendingSave(activeFileId);
      await selectFile(id);
    },
    [activeFileId, selectFile, flushPendingSave],
  );

  const handleDelete = useCallback(
    async (id: string) => {
      if (activeFileId === id) {
        await selectFile(null);
      }
      await deleteFile(id);
    },
    [activeFileId, deleteFile, selectFile],
  );

  return (
    <div className="notepad-sidebar">
      <div className="notepad-search">
        <Input
          size="small"
          placeholder="搜索文件..."
          prefix={<SearchOutlined />}
          value={searchQuery}
          onChange={(e) => setSearchQuery(e.target.value)}
          allowClear
        />
      </div>

      <div className="notepad-file-list">
        {filtered.length === 0 && (
          <div style={{ padding: '16px 8px', textAlign: 'center', fontSize: 12, color: 'var(--text-tertiary)' }}>
            {files.length === 0 ? '暂无文件' : '无匹配文件'}
          </div>
        )}
        {filtered.map((f) => {
          const icon = langIcon(f.language);
          const isActive = f.id === activeFileId;
          const isRenaming = f.id === renamingId;

          return (
            <div
              key={f.id}
              className={`notepad-file-item${isActive ? ' notepad-file-item--active' : ''}`}
              onClick={() => !isRenaming && handleSelect(f.id)}
            >
              <span
                className="notepad-file-icon"
                style={{ color: icon.color }}
                title={f.language}
              >
                {icon.char}
              </span>
              {isRenaming ? (
                <input
                  className="notepad-file-name"
                  value={renameValue}
                  onChange={(e) => setRenameValue(e.target.value)}
                  onBlur={confirmRename}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') confirmRename();
                    if (e.key === 'Escape') setRenamingId(null);
                  }}
                  autoFocus
                  onClick={(e) => e.stopPropagation()}
                  style={{
                    border: 'none',
                    outline: 'none',
                    background: 'transparent',
                    fontSize: 12,
                    color: 'var(--text-primary)',
                    padding: 0,
                    width: '100%',
                  }}
                />
              ) : (
                <Tooltip title={f.name} mouseEnterDelay={0.4}>
                  <span className="notepad-file-name">{f.name}</span>
                </Tooltip>
              )}
              {!isRenaming && (
                <div className="notepad-file-actions">
                  <button
                    className="notepad-file-action-btn"
                    title="重命名"
                    onClick={(e) => {
                      e.stopPropagation();
                      startRename(f);
                    }}
                  >
                    <EditOutlined style={{ fontSize: 10 }} />
                  </button>
                  <Popconfirm
                    title="确认删除？"
                    description={`将删除「${f.name}」`}
                    onConfirm={(e) => {
                      e?.stopPropagation();
                      handleDelete(f.id);
                    }}
                    onCancel={(e) => e?.stopPropagation()}
                    okText="删除"
                    cancelText="取消"
                    okButtonProps={{ danger: true }}
                  >
                    <button
                      className="notepad-file-action-btn"
                      title="删除"
                      onClick={(e) => e.stopPropagation()}
                    >
                      <DeleteOutlined style={{ fontSize: 10 }} />
                    </button>
                  </Popconfirm>
                </div>
              )}
            </div>
          );
        })}
      </div>

      <div className="notepad-sidebar-actions">
        <Button
          size="small"
          icon={<FileAddOutlined />}
          onClick={() => {
            setCreateName('');
            setCreateOpen(true);
          }}
          style={{ flex: 1 }}
        >
          新建
        </Button>
        <Button
          size="small"
          icon={<ImportOutlined />}
          onClick={() => importRef.current?.click()}
          style={{ flex: 1 }}
        >
          导入
        </Button>
        <input
          ref={importRef}
          type="file"
          multiple
          style={{ display: 'none' }}
          onChange={handleImport}
        />
      </div>

      <Modal
        title="新建文件"
        open={createOpen}
        onOk={handleCreate}
        onCancel={() => setCreateOpen(false)}
        okText="创建"
        cancelText="取消"
        okButtonProps={{ disabled: !createName.trim() }}
        width={360}
        styles={{ mask: { zIndex: popupZ }, wrapper: { zIndex: popupZ + 1 } }}
      >
        <Input
          placeholder="文件名（含扩展名，如 notes.txt）"
          value={createName}
          onChange={(e) => setCreateName(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && handleCreate()}
          autoFocus
          style={{ marginTop: 12 }}
        />
      </Modal>
    </div>
  );
}
