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
import Editor, { type Monaco } from '@monaco-editor/react';
import type { editor } from 'monaco-editor';
import { useEffect, useRef, useState } from 'react';
import type { UploadProps } from 'antd';
import { useEditorStore } from '../../store/editorStore';
import { registerLuaProviders } from '../../lua/luaProviders';
import { checkLuaSyntax, type SyntaxIssue } from '../../lua/luaSyntaxClient';

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
  const [issues, setIssues] = useState<SyntaxIssue[]>([]);
  const themeMode = useEditorStore((s) => s.theme);
  const monacoTheme = themeMode === 'dark' ? 'vs-dark' : 'light';
  const editorRef = useRef<editor.IStandaloneCodeEditor | null>(null);
  const monacoRef = useRef<Monaco | null>(null);
  const debounceRef = useRef<number | null>(null);

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
  const errorCount = issues.filter((i) => i.severity === 'error').length;
  const warnCount = issues.filter((i) => i.severity === 'warning').length;
  // 用 worker 校验失败时（worker 启动异常）退化为旧的字符串包含检查，避免完全没提示
  const lintFallbackWarn = issues.length === 0 && !content.includes(expectedSig);
  const lintWarn = errorCount > 0 || warnCount > 0 || lintFallbackWarn;

  // 内容变化 → 防抖触发 worker 校验 → 写回 monaco markers
  useEffect(() => {
    if (debounceRef.current) window.clearTimeout(debounceRef.current);
    debounceRef.current = window.setTimeout(() => {
      checkLuaSyntax(content, mode).then((result) => {
        setIssues(result);
        const ed = editorRef.current;
        const mon = monacoRef.current;
        if (!ed || !mon) return;
        const model = ed.getModel();
        if (!model) return;
        mon.editor.setModelMarkers(
          model,
          'stressbot-lua',
          result.map((it) => ({
            startLineNumber: it.line,
            startColumn: it.column,
            endLineNumber: it.endLine,
            endColumn: it.endColumn,
            message: it.message,
            severity:
              it.severity === 'error'
                ? mon.MarkerSeverity.Error
                : it.severity === 'warning'
                  ? mon.MarkerSeverity.Warning
                  : mon.MarkerSeverity.Info,
            source: it.source,
          })),
        );
      });
    }, 400);
    return () => {
      if (debounceRef.current) window.clearTimeout(debounceRef.current);
    };
  }, [content, mode]);

  const handleEditorMount = (ed: editor.IStandaloneCodeEditor, mon: Monaco) => {
    editorRef.current = ed;
    monacoRef.current = mon;
    registerLuaProviders(mon);
  };

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
        type={errorCount > 0 ? 'error' : warnCount > 0 || lintFallbackWarn ? 'warning' : 'info'}
        message={
          <span>
            入口签名：<code>{expectedSig}</code>
            {mode === 'callback' && (
              <>
                ；<code>msg</code> 在无 s2cProto 时为原始二进制 string
              </>
            )}
            {errorCount > 0 && (
              <span style={{ marginLeft: 12, color: '#f5222d' }}>
                · {errorCount} 个语法错误
              </span>
            )}
            {warnCount > 0 && (
              <span style={{ marginLeft: 8, color: '#faad14' }}>· {warnCount} 个警告</span>
            )}
          </span>
        }
        description={
          issues.length > 0 ? (
            <ul style={{ margin: 0, paddingLeft: 18, fontSize: 11 }}>
              {issues.slice(0, 5).map((it, i) => (
                <li key={i}>
                  <code>L{it.line}</code> [{it.source}] {it.message}
                </li>
              ))}
              {issues.length > 5 && <li>…还有 {issues.length - 5} 条</li>}
            </ul>
          ) : undefined
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
          quickSuggestions: { other: true, comments: false, strings: false },
          parameterHints: { enabled: true, cycle: true },
          suggestOnTriggerCharacters: true,
        }}
        onChange={(v) => setContent(v ?? '')}
        onMount={handleEditorMount}
      />
      {/* 抹除 lintWarn 未使用警告（仅作为兜底状态） */}
      <span style={{ display: 'none' }}>{lintWarn ? '1' : '0'}</span>
      <div style={{ fontSize: 11, color: 'var(--text-tertiary)', marginTop: 6 }}>
        ⓘ 编辑器对脚本内容的修改不会写回 conf/scripts/。如需保存，请使用"下载当前内容"或在仓库中直接编辑文件。
      </div>
    </div>
  );
}
