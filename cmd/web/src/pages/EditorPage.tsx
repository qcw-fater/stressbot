/**
 * HomeShell：唯一页面入口，编排 FlowEditor + RuntimeBar + 各模块 Drawer + 轮询。
 *
 * 设计要点：
 * - 进入页面 boot()：listTasks 检查 active 任务 → 弹 ActiveTaskGuardModal 或 mode=edit；
 * - 编辑态：仅轮询节点列表与系统资源（10s）→ 显示集群状态徽章；
 * - 运行 / viewActive：5s 轮询任务详情、压测指标、系统资源与节点列表；
 *   - 当 task.state ∈ {stopped, failed} → onTaskFinished() → mode='finalReport'；
 * - finalReport：停止轮询，保留最后一份数据用于报告展示。
 */

import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { App as AntApp, Spin } from 'antd';

import { useShallow } from 'zustand/react/shallow';
import {
  ApiError,
  agentsApi,
  buildNodeMetricsMap,
  historyApi,
  makeMetricsProvider,
  metricsApi,
  pollingPolicy,
  registerTaskConflictHandler,
  setMessageApi,
  showApiError,
  tasksApi,
  attachToActive,
  useRuntimeStore,
  usePolling,
} from '@/services';
import { useMonacoFindTooltip } from '@/services/monacoTooltip';
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';
import { FlowEditor } from '@/components/FlowEditor';
import { useFlowStore } from '@/components/FlowEditor/store/flowStore';
import { RuntimeBar } from '@/components/runtime/RuntimeBar';
import { MonitorDock } from '@/components/monitoring/MonitorDock';
import { FloatingWindow } from '@/components/FlowEditor/panels/FloatingWindow';
import { useFloatingWindowStore } from '@/components/FlowEditor/store/floatingWindowStore';

const LazyResourcesDrawer = lazy(() =>
  import('@/components/modules/ResourcesDrawer').then((m) => ({ default: m.ResourcesDrawer })),
);
const LazyHistoryModal = lazy(() =>
  import('@/components/modules/history/HistoryModal').then((m) => ({ default: m.HistoryModal })),
);
const LazyAgentsPanel = lazy(() =>
  import('@/components/modules/AgentsPanel').then((m) => ({ default: m.AgentsPanel })),
);
const LazySystemTab = lazy(() =>
  import('@/components/monitoring/tabs/SystemTab').then((m) => ({ default: m.SystemTab })),
);
const LazyLogsTab = lazy(() =>
  import('@/components/monitoring/tabs/LogsTab').then((m) => ({ default: m.LogsTab })),
);
const LazyNotepadTab = lazy(() =>
  import('@/components/modules/notepad/NotepadTab').then((m) => ({ default: m.NotepadTab })),
);
const LazyProtocolConfigEditor = lazy(() =>
  import('@/components/modules/ProtocolConfigEditor').then((m) => ({ default: m.ProtocolConfigEditor })),
);
const LazyActiveTaskGuardModal = lazy(() =>
  import('@/components/runtime/ActiveTaskGuardModal').then((m) => ({
    default: m.ActiveTaskGuardModal,
  })),
);
import type { TaskBrief } from '@/types/api';

/** 首次 visible=true 时挂载组件，之后保持挂载（保留组件状态 + 关闭动画） */
function LazyMount({ visible, children }: { visible: boolean; children: React.ReactNode }) {
  const [mounted, setMounted] = useState(false);
  useEffect(() => {
    if (visible) setMounted(true);
  }, [visible]);
  if (!mounted) return null;
  return <>{children}</>;
}

export function EditorPage() {
  return (
    <AntApp style={{ width: '100%', height: '100%', overflow: 'hidden' }}>
      <HomeShellInner />
    </AntApp>
  );
}

function HomeShellInner() {
  // 注入 antd App context 的 message / modal 实例到 errorHandler，
  // 解决 "Static function can not consume context" 警告 + 让 toast/弹窗复用 ConfigProvider 主题。
  const antApp = AntApp.useApp();
  useEffect(() => {
    setMessageApi({ message: antApp.message, modal: antApp.modal });
    return () => setMessageApi(null);
  }, [antApp.message, antApp.modal]);

  // 全局 Monaco find-widget 中文 tooltip（替代被 display:none 隐藏的原生 hover）
  const themeMode = useEditorStore((s) => s.theme);
  useMonacoFindTooltip(themeMode);

  // 视口缩放时重新夹紧所有浮动窗口：否则分屏 / 缩放后窗口可能大于可视区，
  // 标题栏、关闭按钮、缩放手柄落到屏幕外且无法操作。
  useEffect(() => {
    const onResize = () => useFloatingWindowStore.getState().reclampAll();
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }, []);

  const { mode, activeTask, ownedTaskId, latestStress, onTaskFinished, setActiveTask, setDetachedActiveTask, setAgents, pushStress, pushSystem, setConnectionLost, appendAgentEvents, setAgentHealth } =
    useRuntimeStore(
      useShallow((s) => ({
        mode: s.mode,
        activeTask: s.activeTask,
        ownedTaskId: s.ownedTaskId,
        latestStress: s.latestStress,
        onTaskFinished: s.onTaskFinished,
        setActiveTask: s.setActiveTask,
        setDetachedActiveTask: s.setDetachedActiveTask,
        setAgents: s.setAgents,
        pushStress: s.pushStress,
        pushSystem: s.pushSystem,
        setConnectionLost: s.setConnectionLost,
        appendAgentEvents: s.appendAgentEvents,
        setAgentHealth: s.setAgentHealth,
      })),
    );

  // 业务侧 flow（节点 / actions / listens）+ 最新 stress snapshot → nodeId → ActionMetric
  // 这里订阅整个 flowStore 字段会触发频繁 re-render；用 useShallow 压平，仅在数据真变时触发。
  const flowSlice = useFlowStore(
    useShallow((s) => ({ nodes: s.nodes, listens: s.listens })),
  );
  const metricsProvider = useMemo(() => {
    if (mode === 'edit') return undefined;
    const map = buildNodeMetricsMap(latestStress ?? undefined, flowSlice);
    return map.size > 0 ? makeMetricsProvider(map) : undefined;
  }, [mode, latestStress, flowSlice]);

  const [resourcesOpen, setResourcesOpen] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [agentsOpen, setAgentsOpen] = useState(false);
  const [systemOpen, setSystemOpen] = useState(false);
  const [logsOpen, setLogsOpen] = useState(false);
  const [notepadOpen, setNotepadOpen] = useState(false);
  const [protocolConfigOpen, setProtocolConfigOpen] = useState(false);
  const [guardTask, setGuardTask] = useState<TaskBrief | null>(null);
  const [booting, setBooting] = useState(true);
  const policy = useMemo(() => pollingPolicy(mode), [mode]);

  const bootRef = useRef(false);

  // 注册 TASK_CONFLICT 处理器：让 errorHandler 在弹窗确认后调用 attachToActive
  useEffect(() => {
    registerTaskConflictHandler({
      onAttachActive: async (taskId) => {
        try {
          await attachToActive(taskId);
        } catch (e) {
          showApiError(e);
        }
      },
    });
    return () => registerTaskConflictHandler(null);
  }, []);
  useEffect(() => {

    if (bootRef.current) return;
    bootRef.current = true;
    (async () => {
      try {
        // 1. RobotConfig 使用内置默认值，不再从 conf/config.json 同步（服务器模式下无此文件）
        // 2. 探测 history 模块是否启用：admin 未配 MySQL 时 listHistory 会返回 HISTORY_DISABLED。
        //    用 limit=1 最小开销探测一次，结果写到 editorStore.historyEnabled，
        //    RuntimeBar/HistoryDrawer 据此决定按钮是否禁用，避免用户点了才弹错。
        void historyApi
          .listHistory({ limit: 1 })
          .then(() => useEditorStore.getState().setHistoryEnabled(true))
          .catch((err) => {
            if (err instanceof ApiError && err.code === 'HISTORY_DISABLED') {
              useEditorStore.getState().setHistoryEnabled(false);
            } else {
              // 其它错误（404/500/网络）也按"不可用"处理，但不影响主流程
              useEditorStore.getState().setHistoryEnabled(false);
            }
          });
        // 3. 检测远端 active 任务
        const list = await tasksApi.listTasks();
        const active = list.items.find((t) =>
          t.state === 'starting' || t.state === 'running' || t.state === 'stopping',
        );
        if (active) {
          setDetachedActiveTask(active);
          setGuardTask(active);
        }
      } catch (e) {
        if (e instanceof ApiError && e.code === 'NETWORK_ERROR') {
          setConnectionLost(true);
        } else {
          showApiError(e);
        }
      } finally {
        setBooting(false);
      }
    })();
  }, [setConnectionLost, setDetachedActiveTask]);

  // === 轮询：任务详情（用于检测终态切换） ===
  const taskId = activeTask?.id;
  const taskFetcher = useCallback(async () => {
    if (!taskId) throw new Error('no task');
    return tasksApi.getTask(taskId);
  }, [taskId]);

  // 终态清理状态只通知一次：避免轮询每 5s 重复弹窗
  const notifiedCleanupRef = useRef<string | null>(null);

  usePolling({
    fetcher: taskFetcher,
    intervalMs: policy.intervalMs,
    enabled: policy.pollActiveTask && !!taskId,
    onSuccess: (detail) => {
      setActiveTask(detail);
      if (detail.agentEvents?.length) {
        appendAgentEvents(detail.agentEvents);
      }
      if (detail.state === 'stopped' || detail.state === 'failed') {
        onTaskFinished();
        // 首次进入终态时根据 cleanupSummary 弹出运行时清理提示。
        // partial/timeout/unknown 都需要主动告知，避免用户以为是正常停止。
        if (notifiedCleanupRef.current !== detail.id) {
          notifiedCleanupRef.current = detail.id;
          const summary = detail.cleanupSummary;
          if (summary && summary.status !== 'ok') {
            const lines: string[] = [];
            if (summary.message) lines.push(summary.message);
            const parts: string[] = [];
            if (typeof summary.totalRobots === 'number' && summary.totalRobots > 0) {
              parts.push(`机器人 ${summary.cleanedRobots ?? 0}/${summary.totalRobots}`);
            }
            if (typeof summary.timeoutRobots === 'number' && summary.timeoutRobots > 0) {
              parts.push(`超时 ${summary.timeoutRobots}`);
            }
            if (typeof summary.luaSkipped === 'number' && summary.luaSkipped > 0) {
              parts.push(`Lua 未归还 ${summary.luaSkipped}`);
            }
            if (parts.length > 0) lines.push(parts.join(' · '));
            const titleMap: Record<string, string> = {
              partial: '任务已停止，但部分资源清理异常',
              timeout: '任务已停止，但部分机器人清理超时',
              unknown: '任务已停止，但部分节点清理状态未知',
            };
            antApp.notification.warning({
              message: titleMap[summary.status] ?? '任务已停止，资源清理状态异常',
              description: lines.length > 0 ? lines.join('\n') : '建议查看节点结果与日志',
              duration: 10,
            });
          }
        }
      }
    },
    onError: () => {
      // 单错误静默；usePolling 自带连续失败检测
    },
    onConnectionLost: () => setConnectionLost(true),
    onConnectionRestored: () => setConnectionLost(false),
  });

  // === 轮询：压测 metrics ===
  const stressFetcher = useCallback(() => metricsApi.getClusterMetrics(), []);
  usePolling({
    fetcher: stressFetcher,
    intervalMs: policy.intervalMs,
    enabled: policy.pollStress,
    onSuccess: (agg) => {
      pushStress(agg.snapshot);
      setAgentHealth(agg.reportingAgents, agg.totalAgents, agg.offlineAgents, agg.assignedAgents);
    },
    onConnectionLost: () => setConnectionLost(true),
    onConnectionRestored: () => setConnectionLost(false),
  });

  // === 轮询：集群系统资源 ===
  const systemFetcher = useCallback(() => metricsApi.getClusterSystem(), []);
  usePolling({
    fetcher: systemFetcher,
    intervalMs: policy.intervalMs,
    enabled: policy.pollSystem,
    onSuccess: (snap) => pushSystem(snap),
    onConnectionLost: () => setConnectionLost(true),
    onConnectionRestored: () => setConnectionLost(false),
  });

  // === 轮询：Agent 列表 ===
  const agentsFetcher = useCallback(() => agentsApi.listAgents(), []);
  usePolling({
    fetcher: agentsFetcher,
    intervalMs: policy.intervalMs,
    enabled: policy.pollAgents,
    onSuccess: (resp) => setAgents(resp.items),
    onConnectionLost: () => setConnectionLost(true),
    onConnectionRestored: () => setConnectionLost(false),
  });

  const isReadOnly = mode === 'viewActive' || mode === 'running' || mode === 'finalReport';

  // ownedTaskId 仅作内部状态记录，UI 用不到；显式消费一次避免 lint 警告
  void ownedTaskId;

  if (booting) {
    // antd v5 要求 Spin 的 tip 必须配 nest 或 fullscreen 模式才会展示，
    // 这里包一层占位 div 形成 nest 模式，避免 "tip only work in nest or fullscreen" 警告。
    return (
      <div style={{ width: '100%', height: '100%', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        <Spin size="large" tip="正在连接服务器...">
          <div style={{ width: 120, height: 80 }} />
        </Spin>
      </div>
    );
  }

  return (
    <div style={{ width: '100%', height: '100%', display: 'flex', flexDirection: 'column' }}>
      <div style={{ flex: 1, minHeight: 0, position: 'relative' }}>
        <FlowEditor
          autoLoadDefault
          readOnly={isReadOnly}
          metricsProvider={metricsProvider}
          topbarExtra={
            <RuntimeBar
              onOpenResources={() => setResourcesOpen(true)}
              onOpenHistory={() => setHistoryOpen(true)}
              onOpenAgents={() => setAgentsOpen(true)}
              onOpenSystem={() => setSystemOpen(true)}
              onOpenLogs={() => setLogsOpen(true)}
              onOpenNotepad={() => setNotepadOpen(true)}
              onOpenProtocolConfig={() => setProtocolConfigOpen(true)}
            />
          }
        />
      </div>
      <MonitorDock />
      <LazyMount visible={resourcesOpen}>
        <Suspense fallback={null}>
          <LazyResourcesDrawer open={resourcesOpen} onClose={() => setResourcesOpen(false)} />
        </Suspense>
      </LazyMount>
      <LazyMount visible={historyOpen}>
        <Suspense fallback={null}>
          <LazyHistoryModal open={historyOpen} onClose={() => setHistoryOpen(false)} />
        </Suspense>
      </LazyMount>
      <LazyMount visible={agentsOpen}>
        <Suspense fallback={null}>
          <LazyAgentsPanel open={agentsOpen} onClose={() => setAgentsOpen(false)} />
        </Suspense>
      </LazyMount>
      <LazyMount visible={systemOpen}>
        <FloatingWindow
          windowId="systemStatus"
          title="系统状态"
          defaultSize={{ width: 680, height: 400 }}
          minSize={{ width: 520, height: 320 }}
          open={systemOpen}
          onClose={() => setSystemOpen(false)}
        >
          <Suspense fallback={<Spin />}>
            <LazySystemTab />
          </Suspense>
        </FloatingWindow>
      </LazyMount>
      <LazyMount visible={logsOpen}>
        <FloatingWindow
          windowId="logs"
          title="运行日志"
          defaultSize={{ width: 1000, height: 600 }}
          minSize={{ width: 600, height: 400 }}
          open={logsOpen}
          onClose={() => setLogsOpen(false)}
        >
          <Suspense fallback={<Spin />}>
            <LazyLogsTab open={logsOpen} />
          </Suspense>
        </FloatingWindow>
      </LazyMount>
      <LazyMount visible={notepadOpen}>
        <FloatingWindow
          windowId="notepad"
          title="记事本"
          defaultSize={{ width: 960, height: 600 }}
          minSize={{ width: 640, height: 400 }}
          open={notepadOpen}
          onClose={() => setNotepadOpen(false)}
        >
          <Suspense fallback={<Spin />}>
            <LazyNotepadTab />
          </Suspense>
        </FloatingWindow>
      </LazyMount>
      <LazyMount visible={protocolConfigOpen}>
        <FloatingWindow
          windowId="protocolConfig"
          title="协议配置"
          defaultSize={{ width: 900, height: 640 }}
          minSize={{ width: 600, height: 400 }}
          open={protocolConfigOpen}
          onClose={() => setProtocolConfigOpen(false)}
        >
          <Suspense fallback={<Spin />}>
            <LazyProtocolConfigEditor />
          </Suspense>
        </FloatingWindow>
      </LazyMount>
      <LazyMount visible={guardTask !== null}>
        <Suspense fallback={null}>
          <LazyActiveTaskGuardModal
            open={guardTask !== null}
            task={guardTask}
            onClose={() => setGuardTask(null)}
            onAttached={() => setGuardTask(null)}
          />
        </Suspense>
      </LazyMount>
    </div>
  );
}
