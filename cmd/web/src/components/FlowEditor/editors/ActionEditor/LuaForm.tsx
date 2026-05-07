/**
 * Lua 脚本编辑器（mode='action' / 'callback' 双用）。
 *
 * - 选择脚本文件（从 /conf/scripts/index.json 列出）
 * - Monaco 全功能 Lua 编辑
 * - 入口签名：
 *   action 模式   : function execute(r) ... return 0/non-zero end
 *   callback 模式 : function onMessage(r, msg) ... end
 *
 * 持久化：
 *   - 加载脚本时优先读 IDB（用户编辑过的版本），IDB 没有再 fetch /conf/scripts/<name> 兜底；
 *   - onChange 时 600ms debounce 写回 IDB（resourcesStore.scripts），
 *     启动任务时 taskActions.collectScripts 会自动一并提交；
 *   - "导入本地"按钮也会立即写入 IDB；
 *   - 即使没主动到「资源管理」上传过，编辑器里的修改也不会丢。
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
import { addScript, getScript } from '@/services/resourcesStore';

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
  /** 本地是否已有该脚本的修改稿（IDB 命中），仅用于状态条提示 */
  const [hasLocalDraft, setHasLocalDraft] = useState(false);
  const themeMode = useEditorStore((s) => s.theme);
  const monacoTheme = themeMode === 'dark' ? 'vs-dark' : 'light';
  const editorRef = useRef<editor.IStandaloneCodeEditor | null>(null);
  const monacoRef = useRef<Monaco | null>(null);
  const lintDebounceRef = useRef<number | null>(null);
  const persistDebounceRef = useRef<number | null>(null);
  /** 当前脚本是否完成首次加载，避免初始 setContent 触发回写 */
  const loadedScriptRef = useRef<string | null>(null);

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

  // 拉取选中脚本内容：IDB 优先 → fetch /conf/scripts/ 兜底
  useEffect(() => {
    if (!script) {
      setContent(TEMPLATE[mode]);
      setHasLocalDraft(false);
      loadedScriptRef.current = null;
      return;
    }
    let cancel = false;
    setLoading(true);
    loadedScriptRef.current = null;
    (async () => {
      try {
        const idb = await getScript(script);
        if (cancel) return;
        if (idb) {
          setContent(idb.content);
          setHasLocalDraft(true);
          loadedScriptRef.current = script;
          setLoading(false);
          return;
        }
        const r = await fetch('/conf/scripts/' + script);
        if (cancel) return;
        const text = r.ok ? await r.text() : null;
        if (cancel) return;
        setContent(text ?? TEMPLATE[mode]);
        setHasLocalDraft(false);
        loadedScriptRef.current = script;
        setLoading(false);
      } catch {
        if (!cancel) {
          setContent(TEMPLATE[mode]);
          setHasLocalDraft(false);
          loadedScriptRef.current = script;
          setLoading(false);
        }
      }
    })();
    return () => {
      cancel = true;
    };
  }, [script, mode]);

  const onImport: UploadProps['beforeUpload'] = async (file) => {
    const text = await file.text();
    setContent(text);
    onChangeScript(file.name);
    loadedScriptRef.current = file.name;
    try {
      await addScript(file.name, text);
      setHasLocalDraft(true);
      message.success(`已导入 ${file.name}（已自动保存到本地，启动任务时一并提交）`);
    } catch (e) {
      message.warning(`已导入 ${file.name}，但本地保存失败：${(e as Error).message}`);
    }
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
    if (lintDebounceRef.current) window.clearTimeout(lintDebounceRef.current);
    lintDebounceRef.current = window.setTimeout(() => {
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
      if (lintDebounceRef.current) window.clearTimeout(lintDebounceRef.current);
    };
  }, [content, mode]);

  // 内容变化 → 防抖写回 IDB（仅在已选择脚本文件且首次加载完成后）
  useEffect(() => {
    if (!script) return;
    // 首次加载（loadedScriptRef 与 script 还未对齐）不要回写
    if (loadedScriptRef.current !== script) return;
    if (persistDebounceRef.current) window.clearTimeout(persistDebounceRef.current);
    persistDebounceRef.current = window.setTimeout(() => {
      void addScript(script, content)
        .then(() => setHasLocalDraft(true))
        .catch(() => {
          // 静默：localStorage / IDB 异常不应阻塞编辑
        });
    }, 600);
    return () => {
      if (persistDebounceRef.current) window.clearTimeout(persistDebounceRef.current);
    };
  }, [script, content]);

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
        {script && hasLocalDraft && (
          <Tag color="green" style={{ marginInlineEnd: 0 }}>
            已保存到本地
          </Tag>
        )}
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
        ⓘ 修改会自动保存到浏览器本地（IndexedDB），启动任务时随 multipart 一并提交给 Admin；
        如果想同步到仓库 conf/scripts/，请用"下载当前内容"导出后再 commit。
      </div>
    </div>
  );
}
