/**
 * JSON 预览模态框：Monaco 只读展示，可复制。
 */

import { Modal, Button, Tooltip, message } from 'antd';
import { CopyOutlined } from '@ant-design/icons';
import Editor from '@monaco-editor/react';
import { useShallow } from 'zustand/react/shallow';
import { useFlowStore } from '../store/flowStore';
import { useEditorStore } from '../store/editorStore';

export function JsonPreviewModal() {
  const activePanel = useEditorStore((s) => s.activePanel);
  const setActivePanel = useEditorStore((s) => s.setActivePanel);
  const flow = useFlowStore(
    useShallow((s) => ({
      defaultDelayMs: s.defaultDelayMs,
      nodes: s.nodes,
      actions: s.actions,
      callbacks: s.callbacks,
    })),
  );

  const open = activePanel.kind === 'jsonPreview';
  const json = JSON.stringify(useFlowStore.getState().toTaskFlow(), null, 2);
  const themeMode = useEditorStore((s) => s.theme);
  const monacoTheme = themeMode === 'dark' ? 'vs-dark' : 'light';

  const onCopy = async () => {
    await navigator.clipboard.writeText(json);
    message.success('已复制到剪贴板');
  };

  const onDownload = () => {
    const blob = new Blob([json], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'flow.json';
    a.click();
    URL.revokeObjectURL(url);
  };

  return (
    <Modal
      title="flow.json 预览"
      open={open}
      onCancel={() => setActivePanel({ kind: 'none' })}
      width={900}
      footer={[
        <Button key="download" type="primary" onClick={onDownload}>
          下载 flow.json
        </Button>,
        <Button key="close" onClick={() => setActivePanel({ kind: 'none' })}>
          关闭
        </Button>,
      ]}
    >
      <div
        style={{
          fontSize: 11,
          color: 'var(--text-tertiary)',
          marginBottom: 8,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
        }}
      >
        <span>
          节点 {Object.keys(flow.nodes).length} · 动作 {Object.keys(flow.actions).length} · 回调{' '}
          {Object.keys(flow.callbacks).length}
        </span>
        <Tooltip title="复制到剪贴板">
          <Button
            type="text"
            size="small"
            icon={<CopyOutlined />}
            onClick={onCopy}
            aria-label="复制 JSON"
          />
        </Tooltip>
      </div>
      <Editor
        height="60vh"
        language="json"
        theme={monacoTheme}
        value={json}
        options={{
          readOnly: true,
          minimap: { enabled: false },
          fontSize: 12,
          scrollBeyondLastLine: false,
        }}
      />
    </Modal>
  );
}
