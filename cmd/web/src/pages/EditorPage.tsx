/**
 * HomeShell：唯一页面入口，编排 FlowEditor + RuntimeBar + 各模块 Drawer + 轮询。
 *
 * 设计要点：
 * - 进入页面 boot()：listTasks 检查 active 任务 → 弹 ActiveTaskGuardModal 或 mode=edit；
 * - 编辑态：仅轮询 /api/agents 与 /api/system（10s）→ 显示集群状态徽章；
 * - 运行 / viewActive：5s 轮询 /api/tasks/{id} + /api/metrics + /api/system + /api/agents；
 *   - 当 task.state ∈ {stopped, failed} → onTaskFinished() → mode='finalReport'；
 * - finalReport：停止轮询，保留最后一份数据用于报告展示。
 *
 * T3 阶段先完成数据通路与 mode 切换；MonitorDock / 节点指标染色由 T4/T5 接入。
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { App as AntApp, Spin } from 'antd';
import { useShallow } from 'zustand/react/shallow';
import {
  ApiError,
  agentsApi,
  buildNodeMetricsMap,
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
import { FlowEditor } from '@/components/FlowEditor';
import { useFlowStore } from '@/components/FlowEditor/store/flowStore';
import { ResourcesDrawer } from '@/components/modules/ResourcesDrawer';
import { AgentsDrawer } from '@/components/modules/AgentsDrawer';
import { BinariesDrawer } from '@/components/modules/BinariesDrawer';
import { HistoryDrawer } from '@/components/modules/history/HistoryDrawer';
import { ActiveTaskGuardModal } from '@/components/runtime/ActiveTaskGuardModal';
import { RuntimeBar } from '@/components/runtime/RuntimeBar';
import { MonitorDock } from '@/components/monitoring/MonitorDock';
import type { TaskBrief } from '@/types/api';

export function EditorPage() {
  return (
    <AntApp style={{ width: '100vw', height: '100vh' }}>
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

  const { mode, activeTask, ownedTaskId, latestStress, onTaskFinished, setActiveTask, setAgents, pushStress, pushSystem, setConnectionLost } =
    useRuntimeStore(
      useShallow((s) => ({
        mode: s.mode,
        activeTask: s.activeTask,
        ownedTaskId: s.ownedTaskId,
        latestStress: s.latestStress,
        onTaskFinished: s.onTaskFinished,
        setActiveTask: s.setActiveTask,
        setAgents: s.setAgents,
        pushStress: s.pushStress,
        pushSystem: s.pushSystem,
        setConnectionLost: s.setConnectionLost,
      })),
    );

  // 业务侧 flow（节点 / actions / callbacks）+ 最新 stress snapshot → nodeId → ActionMetric
  // 这里订阅整个 flowStore 字段会触发频繁 re-render；用 useShallow 压平，仅在数据真变时触发。
  const flowSlice = useFlowStore(
    useShallow((s) => ({ nodes: s.nodes, callbacks: s.callbacks })),
  );
  const metricsProvider = useMemo(() => {
    if (mode === 'edit') return undefined;
    const map = buildNodeMetricsMap(latestStress ?? undefined, flowSlice);
    return map.size > 0 ? makeMetricsProvider(map) : undefined;
  }, [mode, latestStress, flowSlice]);

  const [resourcesOpen, setResourcesOpen] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [agentsOpen, setAgentsOpen] = useState(false);
  const [binariesOpen, setBinariesOpen] = useState(false);
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

  // 启动检测：是否已有 active 任务
  useEffect(() => {
    if (bootRef.current) return;
    bootRef.current = true;
    (async () => {
      try {
        const list = await tasksApi.listTasks();
        const active = list.items.find((t) =>
          t.state === 'starting' || t.state === 'running' || t.state === 'stopping',
        );
        if (active) {
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
  }, [setConnectionLost]);

  // === 轮询：任务详情（用于检测终态切换） ===
  const taskId = activeTask?.id;
  const taskFetcher = useCallback(async () => {
    if (!taskId) throw new Error('no task');
    return tasksApi.getTask(taskId);
  }, [taskId]);

  usePolling({
    fetcher: taskFetcher,
    intervalMs: policy.intervalMs,
    enabled: policy.pollActiveTask && !!taskId,
    onSuccess: (detail) => {
      setActiveTask(detail);
      if (detail.state === 'stopped' || detail.state === 'failed') {
        onTaskFinished();
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
    onSuccess: (snap) => pushStress(snap),
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
        <Spin size="large" tip="正在连接 Admin...">
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
              onOpenBinaries={() => setBinariesOpen(true)}
            />
          }
        />
      </div>
      <MonitorDock />
      <ResourcesDrawer open={resourcesOpen} onClose={() => setResourcesOpen(false)} />
      <HistoryDrawer open={historyOpen} onClose={() => setHistoryOpen(false)} />
      <AgentsDrawer
        open={agentsOpen}
        onClose={() => setAgentsOpen(false)}
        onOpenBinaries={() => {
          setAgentsOpen(false);
          setBinariesOpen(true);
        }}
      />
      <BinariesDrawer open={binariesOpen} onClose={() => setBinariesOpen(false)} />
      <ActiveTaskGuardModal
        open={guardTask !== null}
        task={guardTask}
        onClose={() => setGuardTask(null)}
        onAttached={() => setGuardTask(null)}
      />
    </div>
  );
}
