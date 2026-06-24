/**
 * Lua 脚本编辑组件：文件名输入 + 编辑按钮 → Modal 内嵌 LuaForm。
 *
 * 统一 action / listen / boolean 三处 lua 编辑入口。
 * action 模式：入口 execute(r)，返回 nil / err table
 * listen 模式：入口 onMessage(r, msg)
 * boolean 模式：入口 execute(r)，返回 true / false
 */

import { App as AntApp, AutoComplete, Button, Modal, Tag, Tooltip } from 'antd';
import { EditOutlined } from '@ant-design/icons';
import { useEffect, useState } from 'react';
import type { LuaMode } from '../ActionEditor/LuaForm';
import { LuaForm } from '../ActionEditor/LuaForm';
import { useFloatingWindowStore } from '../../store/floatingWindowStore';
import { fetchBaselineScriptIndex } from '@/services/baselineApi';

export interface LuaScriptFieldProps {
  mode: LuaMode;
  /** 脚本文件名（不含 lua: 前缀） */
  value?: string;
  onChange?: (v: string) => void;
  /** 自定义帮助文字 */
  helpText?: React.ReactNode;
}

const DEFAULT_HELP: Record<LuaMode, React.ReactNode> = {
  action: (
    <>
      入口 <code>function execute(r)</code>，返回 <code>nil</code>（成功）/ <code>err table</code>（失败）。
      点「编辑」按钮可在编辑器里直接写脚本，按 Ctrl+S 保存到本地。
    </>
  ),
  listen: (
    <>
      入口 <code>function onMessage(r, msg)</code>，
      未指定响应消息类型时 <code>msg</code> 为原始二进制数据。
      点「编辑」按钮可在编辑器里直接写脚本，按 Ctrl+S 保存到本地。
    </>
  ),
  boolean: (
    <>
      入口 <code>function execute(r)</code>，必须 <code>return true / false</code>。
      点「编辑」按钮可在编辑器里直接写脚本，按 Ctrl+S 保存到本地。
    </>
  ),
};

const MODAL_TITLE_PREFIX: Record<LuaMode, string> = {
  action: '编辑动作脚本',
  listen: '编辑监听脚本',
  boolean: '编辑条件脚本',
};

export function LuaScriptField({ mode, value, onChange, helpText }: LuaScriptFieldProps) {
  const { modal } = AntApp.useApp();
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;
  const [editorOpen, setEditorOpen] = useState(false);
  const [luaDirty, setLuaDirty] = useState(false);
  const [files, setFiles] = useState<string[]>([]);

  useEffect(() => {
    let cancel = false;
    fetchBaselineScriptIndex()
      .then((list) => { if (!cancel) setFiles(list); })
      .catch(() => undefined);
    return () => { cancel = true; };
  }, []);

  const closeEditor = () => {
    if (luaDirty) {
      modal.confirm({
        title: '脚本有未保存的改动',
        content: '关闭后未保存的内容将丢失，是否继续？',
        okText: '不保存',
        cancelText: '取消',
        onOk: () => {
          setEditorOpen(false);
          setLuaDirty(false);
        },
      });
    } else {
      setEditorOpen(false);
    }
  };

  const scriptName = value ?? '';

  return (
    <>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 8 }}>
        <AutoComplete
          style={{ flex: 1 }}
          value={scriptName}
          onChange={(v) => onChange?.(v)}
          onBlur={() => {
            const cur = scriptName.trim();
            if (cur && !cur.endsWith('.lua')) {
              onChange?.(cur + '.lua');
            }
          }}
          options={files.map((f) => ({ value: f, label: f }))}
          placeholder="输入新文件名或选择已有脚本"
          allowClear
          filterOption={(input, option) =>
            (option?.value as string)?.toLowerCase().includes(input.toLowerCase()) ?? false
          }
        />
        {scriptName.trim() && !scriptName.trim().endsWith('.lua') && (
          <Tag color="purple">.lua</Tag>
        )}
        <Tooltip title={!scriptName.trim() ? '先填写脚本文件名再编辑' : '在编辑器里编辑该脚本内容'} mouseEnterDelay={0.4}>
          <Button
            icon={<EditOutlined />}
            onClick={() => setEditorOpen(true)}
            disabled={!scriptName.trim()}
          >
            编辑
          </Button>
        </Tooltip>
      </div>
      <div style={{ fontSize: 11, color: 'var(--text-tertiary)', lineHeight: 1.5 }}>
        {helpText ?? DEFAULT_HELP[mode]}
      </div>

      <Modal
        open={editorOpen}
        title={
          <span>
            {MODAL_TITLE_PREFIX[mode]} <code style={{ color: 'var(--text-secondary)' }}>{scriptName || '(未命名)'}</code>
          </span>
        }
        onCancel={closeEditor}
        footer={[
          <Button key="close" onClick={closeEditor}>
            完成
          </Button>,
        ]}
        width={900}
        destroyOnHidden
        focusTriggerAfterClose={false}
        styles={{ mask: { zIndex: popupZ }, wrapper: { zIndex: popupZ + 1 } }}
      >
        <div onKeyDown={(e) => e.stopPropagation()}>
          <LuaForm
            mode={mode}
            script={scriptName}
            onChangeScript={(s) => onChange?.(s)}
            onDirtyChange={setLuaDirty}
          />
        </div>
      </Modal>
    </>
  );
}
