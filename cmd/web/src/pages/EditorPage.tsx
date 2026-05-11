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
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';
import { FlowEditor } from '@/components/FlowEditor';
import { useFlowStore } from '@/components/FlowEditor/store/flowStore';
import { ResourcesDrawer } from '@/components/modules/ResourcesDrawer';
import { AgentsPanel } from '@/components/modules/AgentsPanel';
import { HistoryModal } from '@/components/modules/history/HistoryModal';
import { ActiveTaskGuardModal } from '@/components/runtime/ActiveTaskGuardModal';
import { RuntimeBar } from '@/components/runtime/RuntimeBar';
import { MonitorDock } from '@/components/monitoring/MonitorDock';
import { SystemTab } from '@/components/monitoring/tabs/SystemTab';
import { LogsTab } from '@/components/monitoring/tabs/LogsTab';
import { FloatingWindow } from '@/components/FlowEditor/panels/FloatingWindow';
import type { RobotConfig, TaskBrief } from '@/types/api';

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

  // 启动检测：是否已有 active 任务 + 同步默认 RobotConfig。
  //
  // RobotConfig 引导：从 /conf/config.json 读取单机配置作为默认值。
  //   - 仅在用户当前字段仍是开箱占位时才覆盖（避免覆盖用户手改）；
  //   - 失败静默：未挂载 conf/ 时维持开箱默认；
  //   - 这一步必须放在 startTask 之前，否则前端默认值（authAddr 是 127.0.0.1、
  //     authExtra 是 {} 等）会被下发到 agent，导致 lua 脚本鉴权失败。
  useEffect(() => {
    if (bootRef.current) return;
    bootRef.current = true;
    (async () => {
      try {
        // 1. 同步 RobotConfig（不阻塞 listTasks）
        void syncDefaultRobotConfigFromConf();
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
            />
          }
        />
      </div>
      <MonitorDock />
      <ResourcesDrawer open={resourcesOpen} onClose={() => setResourcesOpen(false)} />
      <HistoryModal open={historyOpen} onClose={() => setHistoryOpen(false)} />
      <AgentsPanel open={agentsOpen} onClose={() => setAgentsOpen(false)} />
      <FloatingWindow
        windowId="systemStatus"
        title="系统状态"
        defaultSize={{ width: 680, height: 400 }}
        minSize={{ width: 520, height: 320 }}
        open={systemOpen}
        onClose={() => setSystemOpen(false)}
      >
        <SystemTab />
      </FloatingWindow>
      <FloatingWindow
        windowId="logs"
        title="运行日志"
        defaultSize={{ width: 1000, height: 600 }}
        minSize={{ width: 600, height: 400 }}
        open={logsOpen}
        onClose={() => setLogsOpen(false)}
      >
        <LogsTab open={logsOpen} />
      </FloatingWindow>
      <ActiveTaskGuardModal
        open={guardTask !== null}
        task={guardTask}
        onClose={() => setGuardTask(null)}
        onAttached={() => setGuardTask(null)}
      />
    </div>
  );
}

/**
 * 从 /conf/config.json 读取单机配置作为前端 RobotConfig 默认值的引导。
 *
 * 字段映射（仅在用户当前值还是占位默认时才覆盖）：
 *   conf/config.json                     → RobotConfig
 *   ─────────────────────────────────────────────────────
 *   auth.address                         → authAddr
 *   bot.accountPrefix                    → accountPrefix
 *   bot.startNumber                      → startNumber
 *   bot.mainService                      → mainService
 *   bot.concurrentNum                    → concurrency
 *   network.heartbeatInterval ("5s")     → heartbeatSec
 *   network.tcpTimeout ("60s")           → timeoutSec
 *   network.httpTimeout ("10s")          → httpTimeoutSec
 *   monitor.apdexT                       → apdexT
 *
 * **不同步** auth.extra：
 *   该字段是 Auth 请求的扩展键值集合（version/channel/platform 等），
 *   单机模式下确实需要，但 Web 模式下用户的诉求是"完全手动控制"——
 *   单机配置里写什么是单机部署的事，Web 端不应该把它当默认值悄悄填上去。
 *   如有需要用户自行在"高级设置 → 添加字段"里加。
 *
 * 失败静默：未挂载 conf/ 时维持开箱默认。
 */
async function syncDefaultRobotConfigFromConf(): Promise<void> {
  // 这些是 runtimeStore 中开箱默认值；用户改过就不会等于这些值。
  const PLACEHOLDERS = {
    authAddr: 'http://127.0.0.1:20000',
    accountPrefix: 'bot_',
    startNumber: 0,
    mainService: 'logic',
    concurrency: 50,
    timeoutSec: 60,
    heartbeatSec: 5,
    httpTimeoutSec: 10,
    apdexT: 100,
  } as const;

  try {
    const r = await fetch('/conf/config.json');
    if (!r.ok) return;
    const cfg = (await r.json()) as {
      auth?: { address?: string };
      bot?: { accountPrefix?: string; startNumber?: number; mainService?: string; concurrentNum?: number };
      network?: { heartbeatInterval?: string; tcpTimeout?: string; httpTimeout?: string };
      monitor?: { apdexT?: number };
    };

    const cur = useRuntimeStore.getState().robotConfig;
    const patch: Partial<RobotConfig> = {};

    // 字符串：cur 等于占位时才填入
    const addr = cfg.auth?.address?.trim();
    if (addr && cur.authAddr.trim() === PLACEHOLDERS.authAddr) patch.authAddr = addr;

    const ap = cfg.bot?.accountPrefix?.trim();
    if (ap && (cur.accountPrefix ?? '') === PLACEHOLDERS.accountPrefix) patch.accountPrefix = ap;

    const ms = cfg.bot?.mainService?.trim();
    if (ms && (cur.mainService ?? '') === PLACEHOLDERS.mainService) patch.mainService = ms;

    // 注意：auth.extra 不同步（用户诉求"完全手动控制"，详见上方文档注释）。

    // int：cur 等于占位时才填入
    if (typeof cfg.bot?.startNumber === 'number' && (cur.startNumber ?? 0) === PLACEHOLDERS.startNumber) {
      patch.startNumber = cfg.bot.startNumber;
    }
    if (typeof cfg.bot?.concurrentNum === 'number' && cur.concurrency === PLACEHOLDERS.concurrency) {
      patch.concurrency = cfg.bot.concurrentNum;
    }
    const tcpSec = parseDurationSeconds(cfg.network?.tcpTimeout);
    if (tcpSec && cur.timeoutSec === PLACEHOLDERS.timeoutSec) patch.timeoutSec = tcpSec;

    const hbSec = parseDurationSeconds(cfg.network?.heartbeatInterval);
    if (hbSec && (cur.heartbeatSec ?? 0) === PLACEHOLDERS.heartbeatSec) patch.heartbeatSec = hbSec;

    const httpSec = parseDurationSeconds(cfg.network?.httpTimeout);
    if (httpSec && (cur.httpTimeoutSec ?? 0) === PLACEHOLDERS.httpTimeoutSec) patch.httpTimeoutSec = httpSec;

    if (typeof cfg.monitor?.apdexT === 'number' && (cur.apdexT ?? 0) === PLACEHOLDERS.apdexT) {
      patch.apdexT = cfg.monitor.apdexT;
    }

    if (Object.keys(patch).length > 0) {
      useRuntimeStore.getState().setRobotConfig(patch);
    }
  } catch {
    // 静默：开发期可能没挂 conf，生产期 admin 会自己提供默认值
  }
}

/** 把 Go duration 字符串（"5s" / "100ms" / "1m"）粗略转秒，失败/0/<1s 返回 0 */
function parseDurationSeconds(s: string | undefined): number {
  if (!s) return 0;
  const m = /^(\d+(?:\.\d+)?)(ms|s|m|h)$/.exec(s.trim());
  if (!m) return 0;
  const n = parseFloat(m[1]);
  switch (m[2]) {
    case 'ms':
      return Math.round(n / 1000);
    case 's':
      return Math.round(n);
    case 'm':
      return Math.round(n * 60);
    case 'h':
      return Math.round(n * 3600);
    default:
      return 0;
  }
}
