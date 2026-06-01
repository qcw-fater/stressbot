/**
 * 日志查看标签页（v12）— Monaco 只读编辑器模式。
 *
 * 核心设计：
 *   - 组件常驻（Modal 不再 destroyOnClose），关闭时仅暂停轮询
 *   - 滚动/光标/entries 全部在内存中保留，关闭再打开无需重新加载
 *   - 轮询由 open prop 控制：关闭=暂停，打开=恢复
 *   - 增量追加用 model.applyEdits，不触碰滚动
 *   - 全量替换时 saveViewState / restoreViewState 保护滚动和光标
 */

import Editor, { type Monaco } from '@monaco-editor/react';
import type { editor } from 'monaco-editor';
import { Button, Input, Modal, Select, Space, Switch, Table } from 'antd';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { agentsApi, logsApi, usePolling, useRuntimeStore } from '@/services';
import { API_PREFIX } from '@/services/env';
import type { LogEntry, LogFileInfo } from '@/types/api';
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';
import { useFloatingWindowStore } from '@/components/FlowEditor/store/floatingWindowStore';
import dayjs from 'dayjs';
import { registerLogLanguage, getLogTheme } from './logLanguage';
import './LogsTab.css';

const MAX_ENTRIES_ADMIN = 5000;
const MAX_ENTRIES_AGENT = 50000;

const STATE_KEY = 'stressbot.logsTab';

interface SavedState {
  target: string;
  level: string;
  filterText: string;
  polling: boolean;
}

function loadState(): SavedState | null {
  try {
    const raw = localStorage.getItem(STATE_KEY);
    return raw ? JSON.parse(raw) : null;
  } catch { return null; }
}

function saveState(s: SavedState) {
  try { localStorage.setItem(STATE_KEY, JSON.stringify(s)); } catch {}
}

interface FormattedEntry {
  level: string;
  message: string;
  text: string;
}

function formatTimestamp(raw: string): string {
  const d = dayjs(raw);
  const base = d.format('YYYY/MM/DD HH:mm:ss');
  const fracMatch = raw.match(/\.(\d{1,6})/);
  const frac = fracMatch ? fracMatch[1].padEnd(6, '0') : '000000';
  const tzMatch = raw.match(/([+-])(\d{2}):(\d{2})$/);
  const tz = tzMatch ? `${tzMatch[1]}${tzMatch[2]}${tzMatch[3]}` : '+0000';
  return `${base}.${frac}${tz}`;
}

function formatEntry(e: LogEntry): string {
  const ts = formatTimestamp(e.time);
  const level = (e.level || 'info').padEnd(7);
  const fields = e.fields?.length
    ? `  ${JSON.stringify(Object.fromEntries(e.fields.map((f) => [f.key, f.value])))}`
    : '';
  return `${ts}  ${level}${e.caller || ''}  ${e.service || ''}  ${e.message}${fields}`;
}

function isEditorAtBottom(ed: editor.IStandaloneCodeEditor): boolean {
  const scrollTop = ed.getScrollTop();
  const scrollHeight = ed.getScrollHeight();
  const layoutHeight = ed.getLayoutInfo().height;
  return scrollHeight - scrollTop - layoutHeight < 30;
}

export function LogsTab({ open }: { open: boolean }) {
  const agents = useRuntimeStore((s) => s.agents);
  const setAgents = useRuntimeStore((s) => s.setAgents);
  const themeMode = useEditorStore((s) => s.theme);
  const monacoTheme = getLogTheme(themeMode === 'dark');
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;

  const saved = useMemo(() => loadState(), []);

  const [target, setTarget] = useState('admin');
  const [level, setLevel] = useState(saved?.level ?? '');
  const [filterText, setFilterText] = useState(saved?.filterText ?? '');
  const [filterVal, setFilterVal] = useState(saved?.filterText ?? '');
  const [pollingInterval, setPollingInterval] = useState(3000);

  // agents 加载后尝试恢复保存的 target（agentId 须在当前列表中才有效）
  const restoredRef = useRef(false);
  useEffect(() => {
    if (restoredRef.current || !saved?.target || saved.target === 'admin') return;
    if ((agents ?? []).some((a) => a.agentId === saved.target)) {
      restoredRef.current = true;
      setTarget(saved.target);
    }
  }, [agents, saved?.target]);

  const maxEntries = target === 'admin' ? MAX_ENTRIES_ADMIN : MAX_ENTRIES_AGENT;

  const [entries, setEntries] = useState<FormattedEntry[]>([]);
  const [polling, setPolling] = useState(saved?.polling ?? true);
  const lineCountElRef = useRef<HTMLSpanElement>(null);

  const nextSeqRef = useRef<number>(0);
  const editorRef = useRef<editor.IStandaloneCodeEditor | null>(null);
  const monacoRef = useRef<Monaco | null>(null);
  const languageRegistered = useRef(false);

  const prevTextRef = useRef('');
  // 用户主动滚动到底部标记，避免 layout/isEditorAtBottom 误判
  const userAtBottomRef = useRef(true);
  // seekToLatest 期间标记，跳过正常轮询
  const seekingRef = useRef(false);
  const [seeking, setSeeking] = useState(false);
  // 等级/过滤变更后需要全量替换 Monaco 文本，但不清 prevTextRef 以保留增量追加能力
  const needsFullReplaceRef = useRef(false);

  // === seekToLatest：切换 target 时跳到最新，不逐批渲染 ===
  const seekToLatest = useCallback(async (tgt: string) => {
    seekingRef.current = true;
    setSeeking(true);
    let afterSeq = 0;
    let lastEntries: FormattedEntry[] = [];
    let lastNextSeq = 0;

    try {
      for (;;) {
        const res = tgt === 'admin'
          ? await logsApi.getAdminLogs({ afterSeq, limit: 500 })
          : await logsApi.getAgentLogs(tgt, { afterSeq, limit: 500 });

        if (res.entries && res.entries.length > 0) {
          lastEntries = res.entries.map((e: LogEntry) => ({
            level: (e.level || 'info').toLowerCase(),
            message: e.message,
            text: formatEntry(e),
          }));
          lastNextSeq = res.nextSeq;
        }

        if (!res.hasMore) break;
        // 中间批次丢弃，只推进 cursor
        afterSeq = res.nextSeq;
      }
    } catch {
      // 网络错误：显示已拿到的最后一批
    }

    seekingRef.current = false;
    setSeeking(false);
    setEntries(lastEntries);
    nextSeqRef.current = lastNextSeq;
    prevTextRef.current = '';
    userAtBottomRef.current = true;
    setPollingInterval(3000);
  }, []);

  // === 保存设置 ===
  const stateRef = useRef({ target, level, filterText, polling });
  stateRef.current = { target, level, filterText, polling };
  useEffect(() => () => {
    saveState(stateRef.current);
  }, []);

  // === 窗口重新可见时刷新 Monaco 布局，并同步最新节点列表 ===
  useEffect(() => {
    if (!open) return;

    const ed = editorRef.current;
    if (ed) requestAnimationFrame(() => ed.layout());

    let cancelled = false;
    agentsApi.listAgents()
      .then((resp) => {
        if (!cancelled) setAgents(resp.items);
      })
      .catch(() => {});

    return () => {
      cancelled = true;
    };
  }, [open, setAgents]);

  // === 日志文件下载 ===
  const [fileModalOpen, setFileModalOpen] = useState(false);
  const [fileList, setFileList] = useState<LogFileInfo[]>([]);
  const [fileLoading, setFileLoading] = useState(false);

  const openFileList = async () => {
    setFileLoading(true);
    setFileModalOpen(true);
    try {
      const files = target === 'admin'
        ? await logsApi.getAdminLogFiles()
        : await logsApi.getAgentLogFiles(target);
      setFileList(files ?? []);
    } catch {
      setFileList([]);
    } finally {
      setFileLoading(false);
    }
  };

  const downloadFile = (name: string) => {
    const url = target === 'admin'
      ? `${API_PREFIX}/logs/admin/files/${encodeURIComponent(name)}`
      : `${API_PREFIX}/logs/agents/${encodeURIComponent(target)}/files/${encodeURIComponent(name)}`;
    window.open(url, '_blank');
  };

  // === Handlers ===
  const handleTargetChange = (val: string) => {
    setTarget(val);
    setEntries([]);
    prevTextRef.current = '';
    nextSeqRef.current = 0;
    seekToLatest(val);
  };
  const handleLevelChange = (val: string) => {
    setLevel(val);
    needsFullReplaceRef.current = true;
  };
  const handleFilter = (val: string) => {
    setFilterText(val);
    needsFullReplaceRef.current = true;
  };
  const clearLogs = () => {
    setEntries([]);
    prevTextRef.current = '';
    nextSeqRef.current = 0;
    const ed = editorRef.current;
    if (ed) {
      const model = ed.getModel();
      if (model) model.setValue('');
    }
    if (lineCountElRef.current) lineCountElRef.current.textContent = '0 行';
  };

  // === Polling ===
  // enabled = open && polling：弹窗关闭时暂停，打开时恢复
  // 首次挂载时 open=true 且 polling=true → 立即拉取第一批（500 条）
  // hasMore 时 100ms 追赶，直到追平后切回 3000ms 常规轮询
  const fetchLogs = useCallback(async () => {
    const currentTarget = target;
    const afterSeq = nextSeqRef.current;
    const params = { afterSeq, limit: 500 };
    let res;
    if (currentTarget === 'admin') {
      res = await logsApi.getAdminLogs(params);
    } else {
      res = await logsApi.getAgentLogs(currentTarget, params);
    }
    return { res, capturedTarget: currentTarget, isReset: afterSeq === 0 };
  }, [target]);

  usePolling({
    fetcher: fetchLogs,
    intervalMs: pollingInterval,
    enabled: open && polling && !seekingRef.current,
    onSuccess: ({ res, capturedTarget, isReset }) => {
      if (capturedTarget !== target) return;
      if (res.entries && res.entries.length > 0) {
        setEntries((prev) => {
          let newList = isReset ? [] : prev;
          const formatted = res.entries.map((e: LogEntry) => ({
            level: (e.level || 'info').toLowerCase(),
            message: e.message,
            text: formatEntry(e),
          }));
          newList = [...newList, ...formatted];
          if (newList.length > maxEntries) {
            newList = newList.slice(-maxEntries);
          }
          return newList;
        });
        nextSeqRef.current = res.nextSeq;
      } else if (isReset) {
        setEntries([]);
        nextSeqRef.current = 0;
      }
      setPollingInterval(res.hasMore ? 100 : 3000);
    },
    onError: () => {},
  });

  // === 过滤统计 ===
  const filteredCount = useRef(0);
  const totalCount = useRef(0);

  useMemo(() => {
    let list = entries;
    if (level) list = list.filter((e) => e.level === level);
    if (filterText) {
      const lower = filterText.toLowerCase();
      list = list.filter((e) => e.message.toLowerCase().includes(lower));
    }
    filteredCount.current = list.length;
    totalCount.current = entries.length;
    return null;
  }, [entries, level, filterText]);

  const [editorReady, setEditorReady] = useState(false);

  // === 同步文本到 Monaco ===
  useEffect(() => {
    const ed = editorRef.current;
    if (!ed) return;
    const model = ed.getModel();
    if (!model) return;

    let list = entries;
    if (level) list = list.filter((e) => e.level === level);
    if (filterText) {
      const lower = filterText.toLowerCase();
      list = list.filter((e) => e.message.toLowerCase().includes(lower));
    }
    const text = list.map((e) => e.text).join('\n');

    if (text === prevTextRef.current) {
      needsFullReplaceRef.current = false;
      return;
    }

    // ── 等级/过滤变更：全量替换但保持滚动位置 ──
    if (needsFullReplaceRef.current) {
      needsFullReplaceRef.current = false;
      const savedVS = ed.saveViewState();
      model.setValue(text);
      prevTextRef.current = text;
      const lc = model.getLineCount();
      if (lineCountElRef.current) lineCountElRef.current.textContent = `${lc} 行`;
      if (savedVS) ed.restoreViewState(savedVS);
      return;
    }

    // ── 首次加载 ──
    if (!prevTextRef.current) {
      model.setValue(text);
      prevTextRef.current = text;

      const lc = model.getLineCount();
      if (lineCountElRef.current) lineCountElRef.current.textContent = `${lc} 行`;
      // 首次加载直接滚到底部展示最新日志
      requestAnimationFrame(() => ed.revealLine(lc));
      return;
    }

    // 在修改前捕获滚动位置
    const savedVS = ed.saveViewState();
    const shouldFollow = userAtBottomRef.current;
    // 文本更新后恢复滚动：如果用户之前在底部则跟随新内容，否则保持原位
    const restoreScroll = () => {
      if (shouldFollow) {
        const lc = model.getLineCount();
        ed.revealLine(lc);
      } else if (savedVS) {
        ed.restoreViewState(savedVS);
      }
    };

    // ── 尝试增量追加 ──
    const prev = prevTextRef.current;
    if (text.length > prev.length && text.startsWith(prev + '\n') && monacoRef.current) {
      const newPart = text.slice(prev.length + 1);
      const lastLine = model.getLineCount();
      const lastCol = model.getLineMaxColumn(lastLine);
      model.applyEdits([{
        range: new monacoRef.current.Range(lastLine, lastCol, lastLine, lastCol),
        text: '\n' + newPart,
      }]);
      prevTextRef.current = text;

      const lc = model.getLineCount();
      if (lineCountElRef.current) lineCountElRef.current.textContent = `${lc} 行`;

      restoreScroll();
      return;
    }

    // ── 全量替换 ──
    model.setValue(text);
    prevTextRef.current = text;

    const lc = model.getLineCount();
    if (lineCountElRef.current) lineCountElRef.current.textContent = `${lc} 行`;

    restoreScroll();
  }, [entries, level, filterText, editorReady]);

  // === Monaco beforeMount ===
  const handleBeforeMount = (mon: Monaco) => {
    monacoRef.current = mon;
    if (!languageRegistered.current) {
      registerLogLanguage(mon);
      languageRegistered.current = true;
    }
  };

  // === Monaco onMount ===
  const handleEditorMount = (ed: editor.IStandaloneCodeEditor) => {
    editorRef.current = ed;
    setEditorReady(true);
    // 监听用户滚动，仅在内容足够多时更新 userAtBottomRef
    ed.onDidScrollChange(() => {
      const sh = ed.getScrollHeight();
      const lh = ed.getLayoutInfo().height;
      // 内容不足以滚动时不更新（避免空编辑器污染）
      if (sh <= lh + 30) return;
      userAtBottomRef.current = isEditorAtBottom(ed);
    });
  };

  // === Editor options ===
  const editorOptions = useMemo<editor.IStandaloneEditorConstructionOptions>(() => ({
    readOnly: true,
    minimap: { enabled: false },
    lineNumbers: 'on',
    glyphMargin: false,
    folding: false,
    wordWrap: 'on',
    scrollBeyondLastLine: false,
    fontSize: 12,
    fontFamily: "'JetBrains Mono', Consolas, Menlo, 'Courier New', monospace",
    renderLineHighlight: 'none',
    overviewRulerBorder: false,
    hideCursorInOverviewRuler: true,
    overviewRulerLanes: 0,
    scrollbar: { verticalScrollbarSize: 8, horizontalScrollbarSize: 8 },
    padding: { top: 4 },
    fixedOverflowWidgets: true,
    find: {
      addExtraSpaceOnTop: true,
      autoFindInSelection: 'never',
      seedSearchStringFromSelection: 'selection',
    },
  }), []);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
      {/* 工具栏 */}
      <Space size={8} style={{ flexShrink: 0, marginBottom: 4 }}>
        <Select
          value={target}
          onChange={handleTargetChange}
          style={{ width: 180 }}
          dropdownStyle={{ zIndex: popupZ }}
          options={[
            { value: 'admin', label: '服务器' },
            ...agents.map((a) => ({ value: a.agentId, label: `节点: ${a.name}` })),
          ]}
        />
        <Select
          value={level}
          onChange={handleLevelChange}
          style={{ width: 100 }}
          dropdownStyle={{ zIndex: popupZ }}
          options={[
            { value: '', label: '所有级别' },
            { value: 'debug', label: 'Debug' },
            { value: 'info', label: 'Info' },
            { value: 'warn', label: 'Warn' },
            { value: 'error', label: 'Error' },
          ]}
        />
        <Input.Search
          placeholder="过滤关键词..."
          value={filterVal}
          onChange={(e) => setFilterVal(e.target.value)}
          onSearch={(v) => { setFilterVal(v); handleFilter(v); }}
          style={{ width: 200 }}
          allowClear
        />
        <Button onClick={clearLogs} size="small">清空</Button>
        <Button onClick={openFileList} size="small">下载日志</Button>
        <span style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>
          {filteredCount.current} 条{filteredCount.current !== totalCount.current ? ` / 共 ${totalCount.current}` : ''}
        </span>
      </Space>

      {/* Monaco Editor */}
      <div className="logs-tab-editor" style={{ flex: 1, minHeight: 0, position: 'relative', fontSize: '13px', lineHeight: 'normal' }}>
        {seeking && (
          <div style={{
            position: 'absolute', inset: 0, zIndex: 10,
            display: 'flex', alignItems: 'center', justifyContent: 'center',
            background: 'var(--bg-panel)', opacity: 0.7,
            fontSize: 12, color: 'var(--text-secondary)',
          }}>
            加载中...
          </div>
        )}
        <Editor
          language="stressbot-log"
          theme={monacoTheme}
          beforeMount={handleBeforeMount}
          onMount={handleEditorMount}
          options={editorOptions}
          loading="加载日志编辑器..."
        />
      </div>

      {/* 状态栏 */}
      <div style={{
        flexShrink: 0,
        display: 'flex',
        justifyContent: 'space-between',
        alignItems: 'center',
        padding: '2px 8px',
        fontSize: 11,
        color: 'var(--text-tertiary)',
        borderTop: '1px solid var(--border-color, rgba(0,0,0,0.06))',
      }}>
        <span ref={lineCountElRef}>0 行 · Ctrl+F 搜索</span>
        <Space size={4} align="center">
          <span>自动更新</span>
          <Switch
            size="small"
            checked={polling}
            onChange={setPolling}
          />
        </Space>
      </div>

      {/* 日志文件列表弹窗 */}
      <Modal
        title="日志文件"
        open={fileModalOpen}
        onCancel={() => setFileModalOpen(false)}
        footer={null}
        width={520}
        styles={{ mask: { zIndex: popupZ }, wrapper: { zIndex: popupZ + 1 } }}
      >
        <Table
          dataSource={fileList}
          loading={fileLoading}
          rowKey="name"
          size="small"
          pagination={false}
          columns={[
            { title: '文件名', dataIndex: 'name', key: 'name', ellipsis: true },
            {
              title: '大小', dataIndex: 'size', key: 'size', width: 90,
              render: (v: number) => v >= 1048576 ? `${(v / 1048576).toFixed(1)} MB` : `${(v / 1024).toFixed(0)} KB`,
            },
            { title: '修改时间', dataIndex: 'modTime', key: 'modTime', width: 150 },
            {
              title: '', key: 'dl', width: 60,
              render: (_: unknown, record: LogFileInfo) => (
                <a onClick={() => downloadFile(record.name)}>下载</a>
              ),
            },
          ]}
        />
      </Modal>
    </div>
  );
}
