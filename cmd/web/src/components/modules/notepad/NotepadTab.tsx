/**
 * 笔记 — FloatingWindow 内容：左右分栏（文件列表 + Monaco 编辑器）。
 */

import { useEffect } from 'react';
import { FileSidebar } from './FileSidebar';
import { FileEditor } from './FileEditor';
import { useNotepadStore } from './notepadStore';
import './notepad.css';

export function NotepadTab() {
  const loadFileList = useNotepadStore((s) => s.loadFileList);

  useEffect(() => {
    loadFileList();
  }, [loadFileList]);

  return (
    <div className="notepad-container">
      <FileSidebar />
      <FileEditor />
    </div>
  );
}
