/**
 * Lua 脚本编辑器（mode='action' / 'callback' 双用）。
 *
 * - 选择脚本文件（从 /conf/scripts/index.json 列出）
 * - Monaco 全功能 Lua 编辑
 * - 入口签名：
 *   action 模式   : function execute(r) ... return 0/non-zero end
 *   callback 模式 : function onMessage(r, msg) ... end
 */

import { Alert, Button, Select, Space, Tag, Upload, message } from 'antd';
import { CloudDownloadOutlined, ImportOutlined } from '@ant-design/icons';
import Editor from '@monaco-editor/react';
import { useEffect, useState } from 'react';
import type { UploadProps } from 'antd';
import { useEditorStore } from '../../store/editorStore';

export type LuaMode = 'action' | 'callback';

export interface LuaFormProps {
  mode: LuaMode;
  /** 当前脚本路径（如 "post_login.lua"） */
  script?: string;
  onChangeScript: (path: string) => void;
}

const TEMPLATE: Record<LuaMode, string> = {
  action: `-- script_name.lua
local network = require('network')
local robot = require('robot')

function execute(r)
  -- TODO: 业务逻辑
  return 0   -- 0 = 成功，非 0 = 失败
end
`,
  callback: `-- listen_xxx.lua
local robot = require('robot')

function onMessage(r, msg)
  -- 有 s2cProto 时 msg 为 proto userdata，否则为原始二进制 string
  if msg == nil then return end
  -- TODO: 写 state
end
`,
};

export function LuaForm({ mode, script, onChangeScript }: LuaFormProps) {
  const [files, setFiles] = useState<string[]>([]);
  const [content, setContent] = useState<string>('');
  const [loading, setLoading] = useState(false);
  const themeMode = useEditorStore((s) => s.theme);
  const monacoTheme = themeMode === 'dark' ? 'vs-dark' : 'light';

  // 拉取脚本列表
  useEffect(() => {
    let cancel = false;
    fetch('/conf/scripts/index.json')
      .then((r) => (r.ok ? r.json() : []))
      .then((list: string[]) => {
        if (!cancel) setFiles(list);
      })
      .catch(() => undefined);
    return () => {
      cancel = true;
    };
  }, []);

  // 拉取选中脚本内容
  useEffect(() => {
    if (!script) {
      setContent(TEMPLATE[mode]);
      return;
    }
    let cancel = false;
    setLoading(true);
    fetch('/conf/scripts/' + script)
      .then((r) => (r.ok ? r.text() : null))
      .then((text) => {
        if (cancel) return;
        setContent(text ?? TEMPLATE[mode]);
        setLoading(false);
      })
      .catch(() => {
        if (!cancel) {
          setContent(TEMPLATE[mode]);
          setLoading(false);
        }
      });
    return () => {
      cancel = true;
    };
  }, [script, mode]);

  const onImport: UploadProps['beforeUpload'] = async (file) => {
    const text = await file.text();
    setContent(text);
    onChangeScript(file.name);
    message.success(`已导入 ${file.name}（仅前端预览，导出 flow 时请确保 conf/scripts/ 中存在该文件）`);
    return false;
  };

  const onDownload = () => {
    const name = script || `${mode}_template.lua`;
    const blob = new Blob([content], { type: 'text/plain' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = name;
    a.click();
    URL.revokeObjectURL(url);
  };

  const expectedSig = mode === 'action' ? 'function execute(r)' : 'function onMessage(r, msg)';
  const lintWarn = !content.includes(expectedSig);

  return (
    <div>
      <Space style={{ marginBottom: 8 }} wrap>
        <span>脚本文件：</span>
        <Select
          showSearch
          allowClear
          style={{ width: 280 }}
          value={script}
          onChange={(v) => onChangeScript(v ?? '')}
          options={files.map((f) => ({ value: f, label: f }))}
          placeholder="选择 conf/scripts/ 下的 .lua 文件"
        />
        <Upload accept=".lua" beforeUpload={onImport} showUploadList={false}>
          <Button icon={<ImportOutlined />} size="small">
            导入本地
          </Button>
        </Upload>
        <Button icon={<CloudDownloadOutlined />} size="small" onClick={onDownload}>
          下载当前内容
        </Button>
        <Tag color={mode === 'action' ? 'blue' : 'orange'}>{mode}</Tag>
      </Space>
      <Alert
        type={lintWarn ? 'warning' : 'info'}
        message={
          <span>
            入口签名：<code>{expectedSig}</code>
            {mode === 'callback' && (
              <>
                ；<code>msg</code> 在无 s2cProto 时为原始二进制 string
              </>
            )}
          </span>
        }
        showIcon
        style={{ marginBottom: 8 }}
      />
      <Editor
        height="50vh"
        language="lua"
        theme={monacoTheme}
        value={content}
        loading={loading ? '加载中…' : undefined}
        options={{
          minimap: { enabled: false },
          fontSize: 12,
          scrollBeyondLastLine: false,
          readOnly: false,
        }}
        onChange={(v) => setContent(v ?? '')}
      />
      <div style={{ fontSize: 11, color: 'var(--text-tertiary)', marginTop: 6 }}>
        ⓘ 编辑器对脚本内容的修改不会写回 conf/scripts/。如需保存，请使用"下载当前内容"或在仓库中直接编辑文件。
      </div>
    </div>
  );
}
