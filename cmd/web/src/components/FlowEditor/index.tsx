/**
 * FlowEditor 组件封装出口。
 *
 * 设计文档 §13：作为独立的 React 组件，外部容器只需挂载 <FlowEditor />。
 * 后续接 onSave / metricsProvider 等 props（Phase 11 收口）。
 */

import { useEffect, useRef, useState } from 'react';
import { FlowCanvas } from './FlowCanvas';
import { Toolbar } from './panels/Toolbar';
import { NodePalette } from './panels/NodePalette';
import { JsonPreviewModal } from './preview/JsonPreviewModal';
import { NodeEditorDrawer } from './editors/NodeEditorDrawer';
import { ProtoBrowser } from './proto/ProtoBrowser';
import { CallbackPanel } from './callbacks/CallbackPanel';
import { ValidationReportDrawer } from './validation/ValidationReport';
import { TemplateEditorDrawer } from './library/TemplateEditorDrawer';
import { CodecAdapterDrawer } from './adapter/CodecAdapterDrawer';
import { startAutoPersist, loadDraft } from './store/persistDraft';
import { startHistory, undo, redo } from './store/undoRedo';
import { useMetricsStore, type MetricsProvider } from './nodes/shared/MetricsBadge';
import { App as AntApp } from 'antd';
import { useFlowStore } from './store/flowStore';
import { useProtoStore } from './proto/protoStore';
import type { TaskFlow } from '@/types/flow';
import type { FlowLayout } from '@/types/editor';

export interface FlowEditorProps {
  /** 初始 flow.json，未传时按 autoLoadDefault 决定是否从 /conf/flow.json fetch */
  initialFlow?: TaskFlow;
  /** 初始 layout.json */
  initialLayout?: FlowLayout;
  /** 自动加载 conf/flow.json（开发模式默认 true） */
  autoLoadDefault?: boolean;
  /** 监控数据提供方：实时返回某节点的运行指标，未提供时不显示监控徽章 */
  metricsProvider?: MetricsProvider;
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
}: FlowEditorProps) {
  const loadFromTaskFlow = useFlowStore((s) => s.loadFromTaskFlow);
  const loadProtos = useProtoStore((s) => s.load);
  const setMetricsProvider = useMetricsStore((s) => s.setProvider);
  const [validationOpen, setValidationOpen] = useState(false);
  const { notification } = AntApp.useApp();

  useEffect(() => {
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
        const res = await fetch('/conf/flow.json');
        if (!res.ok) return;
        const flow = (await res.json()) as TaskFlow;
        if (!cancelled) loadFromTaskFlow(flow);
      } catch {
        // 静默：未挂载 conf/ 时不报错
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [initialFlow, initialLayout, autoLoadDefault, loadFromTaskFlow, notification]);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', width: '100%' }}>
      <Toolbar onOpenValidation={() => setValidationOpen(true)} />
      <div style={{ display: 'flex', flex: 1, minHeight: 0 }}>
        <div
          style={{
            width: 240,
            borderRight: '1px solid var(--border-color, rgba(0,0,0,0.06))',
            background: 'var(--bg-panel)',
          }}
        >
          <NodePalette />
        </div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <FlowCanvas />
        </div>
      </div>
      <JsonPreviewModal />
      <NodeEditorDrawer />
      <ProtoBrowser />
      <CallbackPanel />
      <TemplateEditorDrawer />
      <CodecAdapterDrawer />
      <ValidationReportDrawer open={validationOpen} onClose={() => setValidationOpen(false)} />
    </div>
  );
}
