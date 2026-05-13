/**
 * JSON 预览浮动窗口：Monaco 只读展示，可复制。
 */

import { App as AntApp, Button, Tooltip } from 'antd';
import { CopyOutlined } from '@ant-design/icons';
import Editor from '@monaco-editor/react';
import { useShallow } from 'zustand/react/shallow';
import { useFlowStore } from '../store/flowStore';
import { useEditorStore } from '../store/editorStore';
import { FloatingWindow } from '../panels/FloatingWindow';

export function JsonPreviewModal() {
  const { message } = AntApp.useApp();
  const activePanel = useEditorStore((s) => s.activePanel.jsonPreview);
  const closePanel = useEditorStore((s) => s.closePanel);
  const flow = useFlowStore(
    useShallow((s) => ({
      defaultDelayMs: s.defaultDelayMs,
      nodes: s.nodes,
      actions: s.actions,
      listens: s.listens,
    })),
  );

  const open = activePanel?.kind === 'jsonPreview';
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
    <FloatingWindow
      windowId="jsonPreview"
      title="flow.json 预览"
      open={open}
      onClose={() => closePanel('jsonPreview')}
      defaultSize={{ width: 800, height: 560 }}
      minSize={{ width: 500, height: 350 }}
      footer={
        <>
          <Button type="primary" onClick={onDownload}>
            下载 flow.json
          </Button>
          <Button onClick={() => closePanel('jsonPreview')}>
            关闭
          </Button>
        </>
      }
    >
      <div
        style={{
          display: 'flex',
          flexDirection: 'column',
          height: '100%',
          minHeight: 0,
        }}
      >
        <div
          style={{
            fontSize: 11,
            color: 'var(--text-tertiary)',
            marginBottom: 8,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            flexShrink: 0,
          }}
        >
          <span>
            节点 {Object.keys(flow.nodes).length} · 动作 {Object.keys(flow.actions).length} · 回调{' '}
            {Object.keys(flow.listens).length}
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
        <div style={{ flex: 1, minHeight: 0 }}>
          <Editor
            height="100%"
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
        </div>
      </div>
    </FloatingWindow>
  );
}
