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
import { startAutoPersist, loadDraft } from './store/persistDraft';
import { startHistory, undo, redo } from './store/undoRedo';
import { useMetricsStore, type MetricsProvider } from './nodes/shared/MetricsBadge';
import { FlowReadOnlyContext } from './flowReadOnlyContext';
import { App as AntApp, ConfigProvider } from 'antd';
import { useFlowStore } from './store/flowStore';
import { useFloatingWindowStore } from './store/floatingWindowStore';
import { useProtoStore } from './proto/protoStore';
import { collectCodecSchemaErrors, subscribe as subscribeResources } from '@/services/resourcesStore';
import { refreshRouteKeyTemplates } from './listens/routeKeyResolver';
import { fetchBaselineFlow } from '@/services/baselineApi';
import { useEditorStore } from './store/editorStore';
import { StateKeyOptionsProvider } from './editors/ActionEditor/useStateKeyOptions';
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
  // antd 弹出层 zIndex 始终高于所有 FloatingWindow（动态跟踪）
  const nextZ = useFloatingWindowStore((s) => s._nextZ);
  return (
    <ConfigProvider theme={{ token: { zIndexPopup: nextZ + 100 } as never }}>
      <AntApp style={{ height: '100%', width: '100%' }}>
        <FlowEditorInner {...props} />
      </AntApp>
    </ConfigProvider>
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

  // 协议配置校验：mount 时检查本地存储的所有 *_codec.json 是否符合 schema，
  // 把错误汇总挂到徽标。资源拉取（与服务器对比、冲突合并）已改为显式操作
  // （资源管理面板的「拉取」按钮 / 启动任务前），不在加载/刷新时自动进行。
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const errors = await collectCodecSchemaErrors();
        if (!cancelled) {
          useEditorStore.getState().setCodecSchemaErrors(errors);
        }
      } catch {
        // 静默
      }
    })();
    return () => { cancelled = true; };
  }, []);

  // routeKey 模板缓存（§3.7）：mount 时加载所有 codec 的 routeKeyTemplate，
  // 供 refsGraph/refsCheck 的 server 感知 routeKey 计算。codec 文件增删改
  // （resourcesStore.notify）时自动刷新。validateFlow 调用方全 sync，走 cache。
  // 每次 refresh 完成都 bump routeKeyTemplatesVersion，触发 ValidationReport /
  // Toolbar 的 validateFlow useMemo 重算（cache 就绪后 warning 自动刷新）。
  useEffect(() => {
    const bump = useEditorStore.getState().bumpRouteKeyTemplatesVersion;
    void refreshRouteKeyTemplates().then(bump);
    const unsub = subscribeResources(() => { void refreshRouteKeyTemplates().then(bump); });
    return unsub;
  }, []);

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
    //
    // 必须避让编辑目标：在 INPUT/TEXTAREA/contentEditable/Monaco 中按 Ctrl+Z 应让浏览器/编辑器
    // 做原生撤销，而不是回退整张流程的业务快照。否则用户在表单/Monaco 里改字后按 Ctrl+Z，
    // 文本没退一格，反而整张流程图被替换。
    // 只读模式（运行 / 查看运行 / 终态报告）下也不应触发：undo/redo 会替换业务状态。
    const handler = (e: KeyboardEvent) => {
      const mod = e.ctrlKey || e.metaKey;
      if (!mod) return;
      const target = e.target as HTMLElement | null;
      if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)) return;
      // Monaco 编辑器渲染为 .monaco-editor 容器（无 INPUT/TEXTAREA 在 activeElement），单独识别。
      if (target?.closest('.monaco-editor')) return;
      if (readOnly) return;
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
  }, [readOnly]);

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
        const flow = await fetchBaselineFlow<FlowJson>();
        if (!flow || cancelled) return;
        loadFromTaskFlow(flow);
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
      <StateKeyOptionsProvider>
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
          <ValidationReportDrawer open={validationOpen} onClose={() => setValidationOpen(false)} />
        </div>
      </StateKeyOptionsProvider>
    </FlowReadOnlyContext.Provider>
  );
}
