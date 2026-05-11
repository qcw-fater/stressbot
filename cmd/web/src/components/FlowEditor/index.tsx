/**
 * FlowEditor 组件封装出口。
 *
 * 设计文档 §13：作为独立的 React 组件，外部容器只需挂载 <FlowEditor />。
 * 后续接 onSave / metricsProvider 等 props（Phase 11 收口）。
 */

import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { FlowCanvas } from './FlowCanvas';
import { Toolbar } from './panels/Toolbar';
import { NodePalette } from './panels/NodePalette';
import { JsonPreviewModal } from './preview/JsonPreviewModal';
import { NodeEditorDrawer } from './editors/NodeEditorDrawer';
import { ProtoBrowser } from './proto/ProtoBrowser';
import { ListenPanel } from './listens/ListenPanel';
import { ValidationReportDrawer } from './validation/ValidationReport';
import { TemplateEditorDrawer } from './library/TemplateEditorDrawer';
import { CodecAdapterDrawer } from './adapter/CodecAdapterDrawer';
import { startAutoPersist, loadDraft } from './store/persistDraft';
import { startHistory, undo, redo } from './store/undoRedo';
import { useMetricsStore, type MetricsProvider } from './nodes/shared/MetricsBadge';
import { FlowReadOnlyContext } from './flowReadOnlyContext';
import { App as AntApp } from 'antd';
import { useFlowStore } from './store/flowStore';
import { useProtoStore } from './proto/protoStore';
import { syncFlowScriptsToIdb } from '@/services/scriptSync';
import type { FlowJson } from './codec/flowToJson';
import type { FlowLayout } from '@/types/editor';

export interface FlowEditorProps {
  /** 初始 flow.json，未传时按 autoLoadDefault 决定是否从 /conf/flow/flow.json fetch */
  initialFlow?: FlowJson;
  /** 初始 layout.json */
  initialLayout?: FlowLayout;
  /** 自动加载 conf/flow/flow.json（开发模式默认 true） */
  autoLoadDefault?: boolean;
  /** 监控数据提供方：实时返回某节点的运行指标，未提供时不显示监控徽章 */
  metricsProvider?: MetricsProvider;
  /** 只读模式：viewActive / running / finalReport 时为 true，画布与编辑器均锁定 */
  readOnly?: boolean;
  /** 渲染在 Toolbar 最右侧（运行控制条等） */
  topbarExtra?: ReactNode;
}

export function FlowEditor(props: FlowEditorProps) {
  // 用 antd <App> 包一层，让内部能用 App.useApp() 拿到主题感知的 message/notification/Modal
  return (
    <AntApp style={{ height: '100%', width: '100%' }}>
      <FlowEditorInner {...props} />
    </AntApp>
  );
}

function FlowEditorInner({
  initialFlow,
  initialLayout,
  autoLoadDefault = true,
  metricsProvider,
  readOnly = false,
  topbarExtra,
}: FlowEditorProps) {
  const loadFromTaskFlow = useFlowStore((s) => s.loadFromTaskFlow);
  const loadProtos = useProtoStore((s) => s.load);
  const setMetricsProvider = useMetricsStore((s) => s.setProvider);
  const [validationOpen, setValidationOpen] = useState(false);
  const { notification } = AntApp.useApp();

  // 同步推送 metricsProvider 到全局 useMetricsStore：必须用 useLayoutEffect 而非 useEffect。
  //   - useEffect 在 paint 之后异步执行；启动新任务时 EditorPage 会先把 latestStress 清成 null，
  //     useMemo 重算 metricsProvider=undefined，但浏览器还是会先按"旧 provider"画一帧
  //     节点上的 p99/apdex/边框，下一拍才被清掉 → 用户看到的"残留" 1~2 帧。
  //   - useLayoutEffect 在 commit 后、paint 前同步执行，setProvider(undefined) 立即生效，
  //     paint 时所有 NodeShell/MetricsBadge 拿到的都是新值，不会出现残留闪烁。
  useLayoutEffect(() => {
    setMetricsProvider(metricsProvider);
    return () => setMetricsProvider(undefined);
  }, [metricsProvider, setMetricsProvider]);

  useEffect(() => {
    // 启动时全量加载 proto：先尝试 vite 中间件 /conf/proto/*，失败再用编译期 glob 兜底
    console.log('[FlowEditor] 触发 proto 加载…');
    void loadProtos({ kind: 'static' });
  }, [loadProtos]);

  useEffect(() => {
    // 启动持久化 + Undo/Redo 历史栈
    const stopPersist = startAutoPersist();
    const stopHistory = startHistory();
    return () => {
      stopPersist();
      stopHistory();
    };
  }, []);

  useEffect(() => {
    // 全局快捷键：Ctrl/Cmd+Z 撤销 / Ctrl/Cmd+Shift+Z 重做
    const handler = (e: KeyboardEvent) => {
      const mod = e.ctrlKey || e.metaKey;
      if (!mod) return;
      const key = e.key.toLowerCase();
      if (key === 'z' && !e.shiftKey) {
        e.preventDefault();
        undo();
      } else if ((key === 'z' && e.shiftKey) || key === 'y') {
        e.preventDefault();
        redo();
      }
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, []);

  // StrictMode 下 effect 会跑两次；用 ref 标记保证 loadDraft / fetch 只发生一次
  const initOnceRef = useRef(false);
  useEffect(() => {
    if (initOnceRef.current) return;
    initOnceRef.current = true;

    if (initialFlow) {
      loadFromTaskFlow(initialFlow, initialLayout);
      return;
    }
    const draft = loadDraft();
    if (draft) {
      loadFromTaskFlow(draft.flow, draft.layout);
      notification.info({
        message: '已恢复编辑稿',
        description: `上次保存于 ${new Date(draft.savedAt).toLocaleString()}`,
        placement: 'topRight',
        duration: 4,
      });
      return;
    }
    if (!autoLoadDefault) return;
    let cancelled = false;
    (async () => {
      try {
        const res = await fetch('/conf/flow/flow.json');
        if (!res.ok) return;
        const flow = (await res.json()) as FlowJson;
        if (cancelled) return;
        loadFromTaskFlow(flow);
        // 把默认 flow 引用的 lua 自动复制到 IDB，方便用户后续编辑保留
        // （IDB 已有则不覆盖；拉不到的不报错，留给用户手动处理）
        void syncFlowScriptsToIdb(flow);
      } catch {
        // 静默：未挂载 conf/ 时不报错
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [initialFlow, initialLayout, autoLoadDefault, loadFromTaskFlow, notification]);

  return (
    <FlowReadOnlyContext.Provider value={readOnly}>
      <div style={{ display: 'flex', flexDirection: 'column', height: '100%', width: '100%', position: 'relative' }}>
        <Toolbar onOpenValidation={() => setValidationOpen(true)} extra={topbarExtra} />
        <div style={{ display: 'flex', flex: 1, minHeight: 0 }}>
          {/* 只读模式（运行 / 查看 / finalReport）下完全隐藏 NodePalette：
              这些模式下不能拖入新节点，画布也是只读，留着只是"灰底占位"
              意义不大；隐藏后画布占满宽度，监控信息更易读。
              切回 edit 模式时自动重新挂载，无状态丢失。 */}
          {!readOnly && (
            <div
              style={{
                width: 240,
                borderRight: '1px solid var(--border-color, rgba(0,0,0,0.06))',
                background: 'var(--bg-panel)',
              }}
            >
              <NodePalette />
            </div>
          )}
          <div style={{ flex: 1, minWidth: 0 }}>
            <FlowCanvas />
          </div>
        </div>
        <JsonPreviewModal />
        <NodeEditorDrawer />
        <ProtoBrowser />
        <ListenPanel />
        <TemplateEditorDrawer />
        <CodecAdapterDrawer />
        <ValidationReportDrawer open={validationOpen} onClose={() => setValidationOpen(false)} />
      </div>
    </FlowReadOnlyContext.Provider>
  );
}
