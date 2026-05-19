/**
 * 记事本 — 右侧 Monaco 编辑器。
 *
 * 选中文件时渲染可编辑 Monaco 实例，未选中时显示空状态。
 * 编辑内容 300ms debounce 自动保存到 IDB。
 */

import { FileTextOutlined } from '@ant-design/icons';
import Editor from '@monaco-editor/react';
import { useCallback } from 'react';
import { useNotepadStore } from './notepadStore';
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';

export function FileEditor() {
  const theme = useEditorStore((s) => s.theme);
  const monacoTheme = theme === 'dark' ? 'vs-dark' : 'light';

  const activeFileId = useNotepadStore((s) => s.activeFileId);
  const activeContent = useNotepadStore((s) => s.activeContent);
  const contentLoaded = useNotepadStore((s) => s.contentLoaded);
  const files = useNotepadStore((s) => s.files);
  const updateContent = useNotepadStore((s) => s.updateContent);

  const activeFile = files.find((f) => f.id === activeFileId);

  const handleChange = useCallback(
    (value: string | undefined) => {
      if (activeFileId) {
        updateContent(activeFileId, value ?? '');
      }
    },
    [activeFileId, updateContent],
  );

  const handleMount = useCallback((_ed: any, _monaco: any) => {
    // Monaco 编辑器已加载，全局 Ctrl+F / Ctrl+H 均由 Monaco 内置处理
  }, []);

  // 拖拽导入支持
  const handleDragOver = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
  }, []);

  const handleDrop = useCallback(
    async (e: React.DragEvent) => {
      e.preventDefault();
      e.stopPropagation();
      const droppedFiles = e.dataTransfer.files;
      if (!droppedFiles.length) return;
      const importFiles = useNotepadStore.getState().importFiles;
      const items: { name: string; content: string }[] = [];
      for (let i = 0; i < droppedFiles.length; i++) {
        const f = droppedFiles[i];
        if (f.size > 5 * 1024 * 1024) continue; // skip >5MB
        const text = await f.text();
        items.push({ name: f.name, content: text });
      }
      if (items.length) {
        await importFiles(items);
      }
    },
    [],
  );

  if (!activeFileId || !activeFile) {
    return (
      <div className="notepad-empty" onDragOver={handleDragOver} onDrop={handleDrop}>
        <FileTextOutlined className="notepad-empty__icon" />
        <div className="notepad-empty__text">选择或新建文件开始编辑</div>
        <div className="notepad-empty__text" style={{ fontSize: 11, marginTop: -4 }}>
          也可拖拽文件到此区域导入
        </div>
      </div>
    );
  }

  if (!contentLoaded) {
    return (
      <div className="notepad-empty">
        <div className="notepad-empty__text">加载中...</div>
      </div>
    );
  }

  return (
    <div className="notepad-editor" onDragOver={handleDragOver} onDrop={handleDrop}>
      <Editor
        height="100%"
        language={activeFile.language}
        theme={monacoTheme}
        value={activeContent}
        onChange={handleChange}
        onMount={handleMount}
        options={{
          fixedOverflowWidgets: true,
          minimap: { enabled: false },
          fontSize: 13,
          fontFamily: "'JetBrains Mono', Consolas, Menlo, 'Courier New', monospace",
          scrollBeyondLastLine: false,
          readOnly: false,
          quickSuggestions: { other: true, comments: false, strings: true },
          suggestOnTriggerCharacters: true,
          wordWrap: 'on',
          lineNumbers: 'on',
          renderLineHighlight: 'line',
          smoothScrolling: true,
          cursorSmoothCaretAnimation: 'on',
          padding: { top: 8, bottom: 8 },
        }}
        loading={null}
      />
    </div>
  );
}
