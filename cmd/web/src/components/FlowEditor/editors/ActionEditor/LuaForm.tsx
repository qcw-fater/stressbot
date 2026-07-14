/**
 * Lua 脚本编辑器（mode='action' / 'listen' / 'boolean' 三用）。
 *
 * - 选择脚本文件（从基线 scripts 索引列出）
 * - Monaco 全功能 Lua 编辑
 * - 入口签名：
 *   action  模式 : function execute(r) ... return nil / err table end
 *                  return nil 表示成功，失败时 return err table。
 *   listen 模式 : function on_message(r, msg) ... end
 *   boolean 模式 : function execute(r) ... return true/false end（条件节点 / loop breakCondition 用）
 *
 * 持久化：
 *   - 加载脚本时优先读本地存储（用户保存过的版本），本地没有再从基线 fetch 兜底；
 *   - 需手动保存（Ctrl+S 或「保存到本地」按钮），脚本名不能为空；
 *   - "导入本地"按钮也会立即写入本地存储；
 *   - 启动任务时 taskActions.collectScripts 会自动一并提交。
 */

import { Alert, AutoComplete, App as AntApp, Button, Space, Tag, Upload } from 'antd';
import { CloudDownloadOutlined, ImportOutlined, SaveOutlined, UndoOutlined } from '@ant-design/icons';
import Editor, { type Monaco } from '@monaco-editor/react';
import type { editor } from 'monaco-editor';
import { useCallback, useEffect, useRef, useState } from 'react';
import type { UploadProps } from 'antd';
import { useEditorStore } from '../../store/editorStore';
import { registerLuaProviders } from '../../lua/luaProviders';
import { checkLuaSyntax, type SyntaxIssue } from '../../lua/luaSyntaxClient';
import { LuaApiPopover } from '../../lua/LuaApiPopover';
import { addScript, getScript } from '@/services/resourcesStore';
import { fetchBaselineScriptIndex, fetchBaselineScript } from '@/services/baselineApi';

export type LuaMode = 'action' | 'listen' | 'boolean';

export interface LuaFormProps {
  mode: LuaMode;
  /** 当前脚本路径（如 "post_login.lua"） */
  script?: string;
  onChangeScript: (path: string) => void;
  /** 外部监听 dirty 状态变化（用于关闭前确认） */
  onDirtyChange?: (dirty: boolean) => void;
}

const TEMPLATE: Record<LuaMode, string> = {
  action: `-- script_name.lua
local network = require('network')
local robot = require('robot')

function execute(r)
  -- TODO: 业务逻辑
  -- 示例：
  -- local err, resp = network.tcp_request('logic', {cmd=1, act=2}, msg, 'Game.SomeS2C')
  -- if err then return err end
  return nil  -- 成功；失败时 return robot.error(code, 'detail') 或透传 err
end
`,
  listen: `-- listen_xxx.lua
local robot = require('robot')

function on_message(r, msg)
  -- 有 s2cProto 时 msg 为字段表；未配置 s2cProto 时 msg 为 nil
  if msg == nil then return end
  -- TODO: 写 state
end
`,
  boolean: `-- condition_xxx.lua
local robot = require('robot')

function execute(r)
  -- TODO: 判断条件
  -- 必须 return true 或 false（boolean 节点 / loop breakCondition 用）
  return false
end
`,
};

function ensureLuaSuffix(name: string): string {
  const trimmed = name.trim();
  if (!trimmed) return '';
  return trimmed.endsWith('.lua') ? trimmed : `${trimmed}.lua`;
}

export function LuaForm({ mode, script, onChangeScript, onDirtyChange }: LuaFormProps) {
  const { message } = AntApp.useApp();
  const [files, setFiles] = useState<string[]>([]);
  const [content, setContent] = useState<string>('');
  const [loading, setLoading] = useState(false);
  const [issues, setIssues] = useState<SyntaxIssue[]>([]);
  const [hasLocalDraft, setHasLocalDraft] = useState(false);
  const [dirty, setDirty] = useState(false);
  const themeMode = useEditorStore((s) => s.theme);
  const monacoTheme = themeMode === 'dark' ? 'vs-dark' : 'light';
  const editorRef = useRef<editor.IStandaloneCodeEditor | null>(null);
  const monacoRef = useRef<Monaco | null>(null);
  const lintDebounceRef = useRef<number | null>(null);
  /** 加载完成后的初始内容，用于 dirty 判断 */
  const initialContentRef = useRef<string>('');
  /** 当前脚本是否完成首次加载，避免初始 setContent 触发 dirty */
  const loadedScriptRef = useRef<string | null>(null);

  // 通知外层 dirty 状态变化
  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);

  // 拉取脚本列表
  useEffect(() => {
    let cancel = false;
    fetchBaselineScriptIndex()
      .then((list) => { if (!cancel) setFiles(list ?? []); })
      .catch(() => undefined);
    return () => {
      cancel = true;
    };
  }, []);

  // 拉取选中脚本内容：本地存储优先 → baselineApi 兜底
  useEffect(() => {
    if (!script) {
      const tpl = TEMPLATE[mode];
      setContent(tpl);
      setHasLocalDraft(false);
      setDirty(false);
      initialContentRef.current = tpl;
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
          setDirty(false);
          initialContentRef.current = idb.content;
          loadedScriptRef.current = script;
          setLoading(false);
          return;
        }
        const body = await fetchBaselineScript(script);
        if (cancel) return;
        let text: string | null = null;
        if (body) {
          const lower = body.slice(0, 200).toLowerCase();
          if (!lower.includes('<!doctype') && !lower.includes('<html')) {
            text = body;
          }
        }
        if (cancel) return;
        const final = text ?? TEMPLATE[mode];
        setContent(final);
        setHasLocalDraft(false);
        setDirty(false);
        initialContentRef.current = final;
        loadedScriptRef.current = script;
        setLoading(false);
      } catch {
        if (!cancel) {
          const tpl = TEMPLATE[mode];
          setContent(tpl);
          setHasLocalDraft(false);
          setDirty(false);
          initialContentRef.current = tpl;
          loadedScriptRef.current = script;
          setLoading(false);
        }
      }
    })();
    return () => {
      cancel = true;
    };
  }, [script, mode]);

  const handleSave = useCallback(async () => {
    const raw = script?.trim();
    if (!raw) {
      message.warning('请先填写脚本文件名');
      return;
    }
    const name = ensureLuaSuffix(raw);
    if (name !== script) onChangeScript(name);
    try {
      await addScript(name, content);
      setHasLocalDraft(true);
      setDirty(false);
      initialContentRef.current = content;
      message.success('已保存到本地');
    } catch (e) {
      message.error(`保存失败：${(e as Error).message}`);
    }
  }, [script, content]);

  const handleSaveRef = useRef(handleSave);
  handleSaveRef.current = handleSave;

  const onImport: UploadProps['beforeUpload'] = async (file) => {
    const text = await file.text();
    setContent(text);
    onChangeScript(file.name);
    loadedScriptRef.current = file.name;
    try {
      await addScript(file.name, text);
      setHasLocalDraft(true);
      setDirty(false);
      initialContentRef.current = text;
      message.success(`已导入 ${file.name}（已自动保存到本地，启动任务时一并提交）`);
    } catch (e) {
      message.warning(`已导入 ${file.name}，但本地保存失败：${(e as Error).message}`);
    }
    return false;
  };

  const onDownload = () => {
    const name = ensureLuaSuffix(script || `${mode}_template`);
    const blob = new Blob([content], { type: 'text/plain' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = name;
    a.click();
    URL.revokeObjectURL(url);
  };

  const expectedSig =
    mode === 'listen' ? 'function on_message(r, msg)' : 'function execute(r)';
  const errorCount = issues.filter((i) => i.severity === 'error').length;
  const warnCount = issues.filter((i) => i.severity === 'warning').length;
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

  const handleEditorMount = (ed: editor.IStandaloneCodeEditor, mon: Monaco) => {
    editorRef.current = ed;
    monacoRef.current = mon;
    registerLuaProviders(mon);
    // Ctrl+S 保存（通过 ref 调用最新 handleSave，避免闭包捕获过期的 script/content）
    ed.addCommand(mon.KeyMod.CtrlCmd | mon.KeyCode.KeyS, () => {
      handleSaveRef.current();
    });
  };

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', marginBottom: 8, gap: 8, flexWrap: 'wrap' }}>
        <Space size={8} wrap>
          <span>脚本文件：</span>
          <AutoComplete
            style={{ width: 280 }}
            value={script}
            onChange={(v) => onChangeScript(v)}
            options={files.map((f) => ({ value: f, label: f }))}
            placeholder="输入新文件名或选择已有脚本"
            allowClear
            filterOption={(input, option) =>
              (option?.value as string)?.toLowerCase().includes(input.toLowerCase()) ?? false
            }
          />
          {script?.trim() && !script.trim().endsWith('.lua') && (
            <Tag color="purple">.lua</Tag>
          )}
          <Upload accept=".lua" beforeUpload={onImport} showUploadList={false}>
            <Button icon={<ImportOutlined />} size="small">
              导入
            </Button>
          </Upload>
          <Button icon={<CloudDownloadOutlined />} size="small" onClick={onDownload}>
            下载
          </Button>
          <Button
            icon={<SaveOutlined />}
            size="small"
            type="primary"
            disabled={!script?.trim()}
            onClick={handleSave}
          >
            保存
          </Button>
          <Tag color={mode === 'action' ? 'geekblue' : mode === 'boolean' ? 'gold' : 'orange'}>
            {mode}
          </Tag>
          {dirty && (
            <Tag color="warning" style={{ marginInlineEnd: 0 }}>
              未保存
            </Tag>
          )}
          {!dirty && hasLocalDraft && script && (
            <Tag color="green" style={{ marginInlineEnd: 0 }}>
              已保存
            </Tag>
          )}
        </Space>
        <span style={{ flex: 1 }} />
        <Space size={8}>
          {dirty && (
            <Button
              icon={<UndoOutlined />}
              size="small"
              onClick={() => {
                setContent(initialContentRef.current);
                setDirty(false);
              }}
            >
              还原
            </Button>
          )}
          <LuaApiPopover />
        </Space>
      </div>
      <Alert
        type={errorCount > 0 ? 'error' : warnCount > 0 || lintFallbackWarn ? 'warning' : 'info'}
        message={
          <span>
            入口签名：<code>{expectedSig}</code>
            {mode === 'action' && (
              <>
                ；<code>return nil</code>（成功）/ <code>return err table</code>（失败）
              </>
            )}
            {mode === 'boolean' && (
              <>
                ；<code>return true / false</code>
              </>
            )}
            {mode === 'listen' && (
              <>
                ；配置响应消息类型时 <code>msg</code> 为字段表，未配置时为 <code>nil</code>
              </>
            )}
            {errorCount > 0 && (
              <span style={{ marginLeft: 12, color: 'var(--color-error)' }}>
                · {errorCount} 个语法错误
              </span>
            )}
            {warnCount > 0 && (
              <span style={{ marginLeft: 8, color: 'var(--color-warning)' }}>· {warnCount} 个警告</span>
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
          fontFamily: "'JetBrains Mono', Consolas, Menlo, 'Courier New', monospace",
          scrollBeyondLastLine: false,
          readOnly: false,
          fixedOverflowWidgets: true,
          quickSuggestions: { other: true, comments: false, strings: true },
          parameterHints: { enabled: true, cycle: true },
          suggestOnTriggerCharacters: true,
        }}
        onChange={(v) => {
          const next = v ?? '';
          setContent(next);
          if (loadedScriptRef.current === script) {
            setDirty(next !== initialContentRef.current);
          }
        }}
        onMount={handleEditorMount}
      />
      {/* 抹除 lintWarn 未使用警告（仅作为兜底状态） */}
      <span style={{ display: 'none' }}>{lintWarn ? '1' : '0'}</span>
      <div style={{ fontSize: 11, color: 'var(--text-tertiary)', marginTop: 6 }}>
        编辑后点击「保存到本地」或按 Ctrl+S 保存。启动任务时会随流程一起提交。如需同步到代码仓库，请用「下载当前内容」导出。
      </div>
    </div>
  );
}
