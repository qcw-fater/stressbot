/**
 * 日志查看标签页（v6）— Monaco 只读编辑器模式。
 *
 * 体验类似 VSCode 只读文本编辑器：
 *   - 光标、键盘导航（Home/End/Ctrl+Home/End、上下左右）
 *   - Ctrl+F 内置搜索（高亮、上/下一个、大小写/正则/全词匹配）
 *   - 级别着色（自定义 stressbot-log language）
 *   - 最新日志在底部，自动滚底
 *   - 前端过滤（level + 文本搜索）
 */

import Editor, { type Monaco } from '@monaco-editor/react';
import type { editor } from 'monaco-editor';
import { Button, Input, Modal, Select, Space, Switch, Table } from 'antd';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { logsApi, usePolling, useRuntimeStore } from '@/services';
import type { LogEntry, LogFileInfo } from '@/types/api';
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';
import dayjs from 'dayjs';
import { registerLogLanguage, getLogTheme } from './logLanguage';
import './LogsTab.css';

const MAX_ENTRIES_ADMIN = 5000;
const MAX_ENTRIES_AGENT = 50000;

/** 预格式化的日志条目：到达时一次性格式化，后续过滤/拼接只操作 text。 */
interface FormattedEntry {
  level: string;
  message: string;
  text: string;
}

/**
 * 将后端 RFC3339Nano 时间字符串（如 2026-05-08T16:46:50.356608+08:00）
 * 格式化为与后端控制台一致的风格：2026/05/08 16:46:50.356608+0800
 *
 * dayjs 只支持毫秒精度，微秒部分需要从原始字符串中提取。
 */
function formatTimestamp(raw: string): string {
  // raw: "2026-05-08T16:46:50.356608+08:00" 或 "2026-05-08T16:46:50.356608Z"
  const d = dayjs(raw);
  const base = d.format('YYYY/MM/DD HH:mm:ss');

  // 提取小数秒部分（最多 6 位 = 微秒）
  const fracMatch = raw.match(/\.(\d{1,6})/);
  const frac = fracMatch ? fracMatch[1].padEnd(6, '0') : '000000';

  // 提取时区偏移：+08:00 → +0800, -05:00 → -0500, Z → +0000
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
  // level 固定 7 字符宽（含尾部空格），caller 对齐同一列
  return `${ts}  ${level}${e.caller || ''}  ${e.service || ''}  ${e.message}${fields}`;
}

export function LogsTab() {
  const agents = useRuntimeStore((s) => s.agents);
  const themeMode = useEditorStore((s) => s.theme);
  const monacoTheme = getLogTheme(themeMode === 'dark');

  const [target, setTarget] = useState<string>('admin');
  const [level, setLevel] = useState<string>('');
  const [filterText, setFilterText] = useState<string>('');
  const [filterVal, setFilterVal] = useState<string>('');
  const [pollingInterval, setPollingInterval] = useState(3000);

  const maxEntries = target === 'admin' ? MAX_ENTRIES_ADMIN : MAX_ENTRIES_AGENT;

  const [entries, setEntries] = useState<FormattedEntry[]>([]);

  const autoScrollRef = useRef(true);
  const [polling, setPolling] = useState(true);
  const lineCountRef = useRef(0);
  const lineCountElRef = useRef<HTMLSpanElement>(null);

  const nextSeqRef = useRef<number>(0);
  const isFilterChangedRef = useRef<boolean>(false);
  const editorRef = useRef<editor.IStandaloneCodeEditor | null>(null);
  const monacoRef = useRef<Monaco | null>(null);
  const languageRegistered = useRef(false);
  const fullTextRef = useRef<string>('');

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
      ? `/api/logs/admin/files/${encodeURIComponent(name)}`
      : `/api/logs/agents/${encodeURIComponent(target)}/files/${encodeURIComponent(name)}`;
    window.open(url, '_blank');
  };

  // === Handlers ===
  const handleTargetChange = (val: string) => {
    setTarget(val);
    setEntries([]);
    nextSeqRef.current = 0;
    isFilterChangedRef.current = true;
    // 切换 target 后立即触发一次拉取，不用等 3s 轮询周期
    setPolling(false);
    requestAnimationFrame(() => setPolling(true));
  };
  const handleLevelChange = (val: string) => {
    setLevel(val);
    isFilterChangedRef.current = true;
  };
  const handleFilter = (val: string) => {
    setFilterText(val);
    isFilterChangedRef.current = true;
  };
  const clearLogs = () => {
    setEntries([]);
    nextSeqRef.current = 0;
    editorRef.current?.setValue('');
    lineCountRef.current = 0;
    if (lineCountElRef.current) lineCountElRef.current.textContent = '0 行';
  };

  // === Polling ===
  const fetchLogs = useCallback(async () => {
    const currentFilter = { target, level, filterText };
    let afterSeq = nextSeqRef.current;
    if (isFilterChangedRef.current) {
      afterSeq = 0;
      isFilterChangedRef.current = false;
    }

    const params = { afterSeq, limit: 500 };

    let res;
    if (target === 'admin') {
      res = await logsApi.getAdminLogs(params);
    } else {
      res = await logsApi.getAgentLogs(target, params);
    }
    return { res, filter: currentFilter, isReset: afterSeq === 0 };
  }, [target, level, filterText]);

  usePolling({
    fetcher: fetchLogs,
    intervalMs: pollingInterval,
    enabled: polling,
    onSuccess: ({ res, filter, isReset }) => {
      if (filter.target !== target || filter.level !== level || filter.filterText !== filterText) {
        return;
      }

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

      // 追赶轮询：有积压时缩短间隔快速追平
      setPollingInterval(res.hasMore ? 100 : 3000);
    },
    onError: () => {},
  });

  // === 过滤 & 拼接（只 join，不 formatEntry） ===
  const filteredCount = useRef(0);
  const totalCount = useRef(0);
  const fullText = useMemo(() => {
    let list = entries;
    if (level) {
      list = list.filter((e) => e.level === level);
    }
    if (filterText) {
      const lower = filterText.toLowerCase();
      list = list.filter((e) => e.message.toLowerCase().includes(lower));
    }
    filteredCount.current = list.length;
    totalCount.current = entries.length;
    return list.map((e) => e.text).join('\n');
  }, [entries, level, filterText]);
  fullTextRef.current = fullText;

  // === 同步文本到 Monaco ===
  const isInitialRef = useRef(true);

  useEffect(() => {
    const ed = editorRef.current;
    if (!ed) return;

    const model = ed.getModel();
    if (!model) return;

    if (isInitialRef.current) {
      model.setValue(fullText);
      isInitialRef.current = false;
      // 初始加载后滚到底部
      requestAnimationFrame(() => {
        const lineCount = ed.getModel()?.getLineCount() ?? 0;
        ed.revealLine(lineCount);
      });
    } else {
      // 增量追加：只在文本变长时追加
      const oldText = model.getValue();
      const newPart = fullText.startsWith(oldText + '\n')
        ? fullText.slice(oldText.length + 1)
        : null;

      if (newPart !== null && oldText.length > 0) {
        // 增量追加
        const lineCount = model.getLineCount();
        const lastLineLength = model.getLineMaxColumn(lineCount);
        ed.executeEdits('log-append', [{
          range: new monacoRef.current!.Range(lineCount, lastLineLength, lineCount, lastLineLength),
          text: '\n' + newPart,
        }]);
      } else {
        // 全量替换（过滤条件变化等）
        model.setValue(fullText);
      }
    }

    const lc = ed.getModel()?.getLineCount() ?? 0;
    lineCountRef.current = lc;
    if (lineCountElRef.current) lineCountElRef.current.textContent = `${lc} 行`;

    if (autoScrollRef.current) {
      requestAnimationFrame(() => {
        const lc = ed.getModel()?.getLineCount() ?? 0;
        ed.revealLine(lc);
      });
    }
  }, [fullText]);

  // === Monaco beforeMount：注册自定义 language 和主题（必须在 Editor 渲染前） ===
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

    // Monaco 异步加载：可能在 fullText 已有数据后才 mount，
    // 此时 isInitialRef 仍为 true，手动触发一次同步。
    if (isInitialRef.current && fullTextRef.current) {
      const model = ed.getModel();
      if (model) {
        model.setValue(fullTextRef.current);
        isInitialRef.current = false;
        const lc = model.getLineCount();
        lineCountRef.current = lc;
        if (lineCountElRef.current) lineCountElRef.current.textContent = `${lc} 行`;
        requestAnimationFrame(() => ed.revealLine(lc));
      }
    }

    // 监听滚动，判断用户是否手动上滚（用 ref 避免 setState 触发 re-render 干扰 find widget）
    ed.onDidScrollChange(() => {
      const scrollTop = ed.getScrollTop();
      const scrollHeight = ed.getScrollHeight();
      const layoutHeight = ed.getLayoutInfo().height;
      autoScrollRef.current = scrollHeight - scrollTop - layoutHeight < 30;
    });
  };

  // === Find Widget 中文提示（自绘浮层，避免 Monaco 自带 tooltip 闪烁 + 浏览器原生 title 黑/白主题不可控） ===
  // Monaco 自带 hover 弹层在 Ant Design 容器内会出现死循环闪烁（已在 LogsTab.css 中用
  // display:none 屏蔽掉），这里在 body 上自绘一个跟随主题的小浮层做中文提示。
  // 通过 aria-label（Monaco 自动写在 .button 上的英文标签）匹配到对应按钮。
  useEffect(() => {
    const ARIA_TO_ZH: Record<string, string> = {
      'Previous Match': '上一个匹配 (Shift+Enter)',
      'Next Match': '下一个匹配 (Enter)',
      'Find in Selection': '在选区中查找 (Alt+L)',
      'Close (Escape)': '关闭 (Escape)',
      Close: '关闭 (Escape)',
      'Toggle Replace': '切换替换模式',
      'Toggle Replace mode': '切换替换模式',
      Replace: '替换',
      'Replace (Enter)': '替换 (Enter)',
      'Replace All': '全部替换',
      'Replace All (Ctrl+Alt+Enter)': '全部替换 (Ctrl+Alt+Enter)',
      'Match Case': '区分大小写 (Alt+C)',
      'Match Case (Alt+C)': '区分大小写 (Alt+C)',
      'Match Whole Word': '全字匹配 (Alt+W)',
      'Match Whole Word (Alt+W)': '全字匹配 (Alt+W)',
      'Use Regular Expression': '使用正则表达式 (Alt+R)',
      'Use Regular Expression (Alt+R)': '使用正则表达式 (Alt+R)',
      'Preserve Case': '保留大小写 (Alt+P)',
      'Preserve Case (Alt+P)': '保留大小写 (Alt+P)',
    };

    const dark = themeMode === 'dark';
    const tip = document.createElement('div');
    tip.style.cssText = [
      'position:fixed',
      'z-index:10000',
      'padding:4px 8px',
      'border-radius:4px',
      'font-size:12px',
      'line-height:1.4',
      'pointer-events:none',
      'opacity:0',
      'transition:opacity 0.12s ease',
      'white-space:nowrap',
      'box-shadow:0 2px 8px rgba(0,0,0,0.18)',
      `background:${dark ? '#3c3c3c' : '#ffffff'}`,
      `color:${dark ? '#e6e6e6' : '#333'}`,
      `border:1px solid ${dark ? '#5a5a5a' : '#d9d9d9'}`,
    ].join(';');
    document.body.appendChild(tip);

    let showTimer: number | null = null;
    let hideTimer: number | null = null;

    const lookup = (el: HTMLElement): string | null => {
      const aria = el.getAttribute('aria-label') ?? '';
      if (ARIA_TO_ZH[aria]) return ARIA_TO_ZH[aria];
      const stripped = aria.replace(/\s*\([^)]*\)\s*$/, '').trim();
      return ARIA_TO_ZH[stripped] ?? null;
    };

    const onOver = (e: MouseEvent) => {
      const t = (e.target as HTMLElement | null)?.closest?.('.find-widget [aria-label]') as HTMLElement | null;
      if (!t) return;
      const label = lookup(t);
      if (!label) return;
      if (hideTimer) { window.clearTimeout(hideTimer); hideTimer = null; }
      if (showTimer) window.clearTimeout(showTimer);
      showTimer = window.setTimeout(() => {
        tip.textContent = label;
        tip.style.opacity = '1';
        const r = t.getBoundingClientRect();
        // 先显示再测量，确保 tip 尺寸正确
        const tr = tip.getBoundingClientRect();
        let left = r.left + r.width / 2 - tr.width / 2;
        let top = r.top - tr.height - 6;
        // 视口边界保护
        left = Math.max(4, Math.min(left, window.innerWidth - tr.width - 4));
        if (top < 4) top = r.bottom + 6;
        tip.style.left = `${left}px`;
        tip.style.top = `${top}px`;
      }, 350);
    };

    const onOut = (e: MouseEvent) => {
      const t = (e.target as HTMLElement | null)?.closest?.('.find-widget [aria-label]');
      if (!t) return;
      if (showTimer) { window.clearTimeout(showTimer); showTimer = null; }
      hideTimer = window.setTimeout(() => { tip.style.opacity = '0'; }, 80);
    };

    document.addEventListener('mouseover', onOver);
    document.addEventListener('mouseout', onOut);

    return () => {
      document.removeEventListener('mouseover', onOver);
      document.removeEventListener('mouseout', onOut);
      if (showTimer) window.clearTimeout(showTimer);
      if (hideTimer) window.clearTimeout(hideTimer);
      tip.remove();
    };
  }, [themeMode]);

  // === Memoize editor options：稳定引用，避免每次 re-render 触发 Monaco updateOptions 干扰 find widget ===
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
          options={[
            { value: 'admin', label: 'Admin (Master)' },
            ...agents.map((a) => ({ value: a.agentId, label: `Agent: ${a.name}` })),
          ]}
        />
        <Select
          value={level}
          onChange={handleLevelChange}
          style={{ width: 100 }}
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

      {/* Monaco Editor — 重置 font/line-height 避免继承 Ant Design Modal 的样式干扰 find widget */}
      <div className="logs-tab-editor" style={{ flex: 1, minHeight: 0, position: 'relative', fontSize: '13px', lineHeight: 'normal' }}>
        <Editor
          language="stressbot-log"
          theme={monacoTheme}
          value=""
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
