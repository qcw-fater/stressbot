/**
 * 集成层状态机（编辑 / 查看运行 / 运行 / 终态报告）。
 *
 * 设计要点：
 * - mode 是单向状态机，由方法（startTask/attachToActive/onTaskFinished/detachFromActive/reset）驱动；
 * - latestStress / latestSystem 由 HomeShell 的 usePolling 写入，store 仅维护数据结构与滑窗；
 * - stressHistory / systemHistory 默认保留 60 个点（5s × 60 = 5min），上限可调；
 * - T1 阶段不实现 startTask / attachToActive 内部细节（依赖 flowStore + IDB），留给 T3。
 *   先把状态机骨架与 setter 都做好，让 T2 资源管理能复用 mode 判定（idle 才允许编辑资源）。
 *
 * 持久化（partialize）：仅缓存"启动表单字段"到 localStorage：
 *   taskName / totalBots / robotConfig / deadline。
 * 运行态字段（mode / activeTask / latestStress / agents / history / connectionLost）
 * **不持久化**：刷新页面应当重新查询 admin 拿到最新真实状态，而不是恢复旧 mode。
 *
 * 单一事实源：plan §2 / §4。
 */

import { create } from 'zustand';
import { persist, createJSONStorage } from 'zustand/middleware';
import type {
  AgentBrief,
  AgentEvent,
  ClusterSystemSnapshot,
  RobotConfig,
  StressSnapshot,
  TaskBrief,
} from '@/types/api';

export type RuntimeMode = 'edit' | 'viewActive' | 'running' | 'finalReport';

/** 历史滑窗最大长度（60 点 × 5s ≈ 5 分钟） */
const HISTORY_WINDOW = 60;

export interface RuntimeState {
  mode: RuntimeMode;

  /** 当前 active 任务（任意客户端发起的都算） */
  activeTask: TaskBrief | null;
  /** 本端发起的任务 ID（停止时校验用） */
  ownedTaskId: string | null;

  /** 启动表单状态（与 Toolbar/RuntimeBar 双向绑定） */
  taskName: string;
  totalBots: number;
  robotConfig: RobotConfig;
  deadline: string | null;

  /** 实时数据 */
  latestStress: StressSnapshot | null;
  latestSystem: ClusterSystemSnapshot | null;
  agents: AgentBrief[];

  /** 历史滑窗，时间正序（最新在末尾） */
  stressHistory: StressSnapshot[];
  systemHistory: ClusterSystemSnapshot[];

  /** 连接 Admin 失败 banner（usePolling 触发） */
  connectionLost: boolean;

  /** 任务期间 Agent 事件（离线/重连/注销） */
  agentEvents: AgentEvent[];

  /** 节点健康指标（来自 StressAggregate） */
  reportingAgents: number;
  totalAgents: number;
  offlineAgents: number;
  assignedAgents: number;

  // === 状态机 / 数据 setter ===
  setMode: (m: RuntimeMode) => void;
  setActiveTask: (task: TaskBrief | null) => void;
  setOwnedTaskId: (id: string | null) => void;

  setTaskName: (v: string) => void;
  setTotalBots: (v: number) => void;
  setRobotConfig: (v: Partial<RobotConfig>) => void;
  setDeadline: (v: string | null) => void;

  pushStress: (snap: StressSnapshot) => void;
  pushSystem: (snap: ClusterSystemSnapshot) => void;
  setAgents: (items: AgentBrief[]) => void;
  setConnectionLost: (lost: boolean) => void;
  setAgentEvents: (events: AgentEvent[]) => void;
  appendAgentEvents: (events: AgentEvent[]) => void;
  setAgentHealth: (reporting: number, total: number, offline: number, assigned: number) => void;

  /** 任务结束钩子：mode 切到 finalReport，停止后续历史写入 */
  onTaskFinished: () => void;
  /**
   * 清空所有监控数据（latestStress/latestSystem/stressHistory/systemHistory）。
   * 用于"启动新任务"前清掉上一次的残留 —— 否则 mode=running 切换瞬间，UI 已经
   * 在渲染上一次 finalReport 留下的 latestStress（如动作面板/连接数），
   * 直到下一拍 polling 回包才覆盖，会出现"短暂展示上次数据"的视觉残留。
   */
  clearMonitorData: () => void;
  /**
   * 返回编辑（finalReport / viewActive → edit）。
   *
   * 仅切状态机 + 清掉与"具体任务"绑定的 activeTask / ownedTaskId，
   * **不动**画布（flowStore）、表单缓存与最后一份监控快照：
   *   - 自己启动的任务结束后想接着改流程：画布即原始内容；
   *   - 只读监控期间想退出查看模式：画布会保留远端 attach 时拉到的 flow（用户可继续基于它编辑）；
   *   - 监控数据保留是为了让 MonitorDock 仍能查看末次值（edit 模式下默认折叠）。
   * 如需彻底清空请用 `reset()`（"新建任务"按钮）。
   */
  detachFromActive: () => void;
  /** 完整重置（新建任务） */
  reset: () => void;
}

// 仅作占位，EditorPage 启动时会尝试从 /conf/config.json 同步真实值覆盖。
const DEFAULT_ROBOT_CONFIG: RobotConfig = {
  concurrency: 50,
  timeoutSec: 60,
  accountPrefix: 'bot_',
  startNumber: 0,
  mainService: 'logic',
  stateExtra: {},
  heartbeatSec: 5,
  httpTimeoutSec: 10,
  apdexT: 100,
  logLevel: 'info',
};

const initialState = {
  mode: 'edit' as RuntimeMode,
  activeTask: null,
  ownedTaskId: null,
  taskName: '未命名任务',
  totalBots: 100,
  robotConfig: { ...DEFAULT_ROBOT_CONFIG },
  deadline: null as string | null,
  latestStress: null as StressSnapshot | null,
  latestSystem: null as ClusterSystemSnapshot | null,
  agents: [] as AgentBrief[],
  stressHistory: [] as StressSnapshot[],
  systemHistory: [] as ClusterSystemSnapshot[],
  connectionLost: false,
  agentEvents: [] as AgentEvent[],
  reportingAgents: 0,
  totalAgents: 0,
  offlineAgents: 0,
  assignedAgents: 0,
};

function pushWithLimit<T>(arr: T[], item: T, limit = HISTORY_WINDOW): T[] {
  const next = arr.length >= limit ? arr.slice(arr.length - limit + 1) : arr.slice();
  next.push(item);
  return next;
}

export const useRuntimeStore = create<RuntimeState>()(
  persist(
    (set) => ({
      ...initialState,

      setMode: (m) => set({ mode: m }),
      setActiveTask: (task) => set({ activeTask: task }),
      setOwnedTaskId: (id) => set({ ownedTaskId: id }),

      setTaskName: (v) => set({ taskName: v }),
      setTotalBots: (v) => set({ totalBots: v }),
      setRobotConfig: (v) =>
        set((s) => ({ robotConfig: { ...s.robotConfig, ...v } })),
      setDeadline: (v) => set({ deadline: v }),

      // pushStress 双重守门：
      //
      // 1. 模式守门：只在 running / viewActive 写入（finalReport / edit 下静默丢弃）。
      //    polling 本身在这两种模式下也是 disabled，这里只是兜底竞态。
      //
      // 2. **空快照守门**：任务结束的瞬间 admin 会立刻清空 activeID（admin/task.go:166），
      //    handleGetMetrics 看到 active==nil 直接返回 `&CollectorSnapshot{}`（零值，actions=null）。
      //    而前端 mode 还要等下一次 task polling 才切到 finalReport，期间 pushStress 仍允许写入。
      //    如果直接接受这份空快照，会把"最后一份真实数据"覆盖成空 → 用户看到动作面板在
      //    任务一结束就瞬间变空。识别空快照后 return {}，可以保留最后的有效快照供 finalReport 展示。
      //
      //    判定规则：uptimeSeconds=0 且 actions 为空 ⇒ 后端零值响应。运行中第一拍若刚好都是 0
      //    会被一并丢弃，但下一拍很快就会有数，对最终展示无影响。
      pushStress: (snap) =>
        set((s) => {
          if (s.mode !== 'running' && s.mode !== 'viewActive') return {};
          const isEmpty = (!snap.actions || snap.actions.length === 0)
            && (!snap.uptimeSeconds || snap.uptimeSeconds <= 0);
          if (isEmpty) return {};
          return {
            latestStress: snap,
            stressHistory: pushWithLimit(s.stressHistory, snap),
          };
        }),
      pushSystem: (snap) =>
        set((s) => {
          // edit 模式 polling 是开的（10s 拉一次集群系统），不能拒绝；
          // 仅 finalReport 期间拒，避免覆盖最后一份。
          if (s.mode === 'finalReport') return {};
          return {
            latestSystem: snap,
            systemHistory: pushWithLimit(s.systemHistory, snap),
          };
        }),
      setAgents: (items) => set({ agents: items }),
      setConnectionLost: (lost) => set({ connectionLost: lost }),
      setAgentHealth: (reporting, total, offline, assigned) =>
        set({ reportingAgents: reporting, totalAgents: total, offlineAgents: offline, assignedAgents: assigned }),
      setAgentEvents: (events) => set({ agentEvents: events }),
      appendAgentEvents: (events) =>
        set((s) => {
          if (!events.length) return {};
          // 按 timestamp 去重，避免 polling 重复推入
          const existing = new Set(s.agentEvents.map((e) => `${e.agentId}:${e.type}:${e.timestamp}`));
          const newEvents = events.filter(
            (e) => !existing.has(`${e.agentId}:${e.type}:${e.timestamp}`),
          );
          if (!newEvents.length) return {};
          return { agentEvents: [...s.agentEvents, ...newEvents] };
        }),

      onTaskFinished: () =>
        set({
          mode: 'finalReport',
          // 保留 latestStress / latestSystem / history 给最终报告使用
        }),

      // clearMonitorData 只清"任务级监控数据"，**不动 agents 列表**：
      //   - agents 是集群在线/容量状态，独立于某次任务的 stress 数据，下一拍 polling
      //     才能拉回来。如果在这里清掉，TaskStartModal 在 await createTask 的几百毫秒里
      //     会误判 onlineAgents===0 → 闪现"没有在线的 Agent"红色 Alert，体验非常差。
      //   - latestSystem 同理：edit 模式下 polling 在跑，清掉只会让大盘瞬间归零。
      //     不过 latestSystem 与"上次任务"语义上有些粘连（系统快照确实在上次任务期间采的），
      //     当前选择保守清掉，等下一拍 system polling 补；如果发现也有闪烁问题再调整。
      clearMonitorData: () =>
        set({
          latestStress: null,
          latestSystem: null,
          stressHistory: [],
          systemHistory: [],
          agentEvents: [],
          reportingAgents: 0,
          totalAgents: 0,
          offlineAgents: 0,
          assignedAgents: 0,
        }),

      detachFromActive: () =>
        set({
          mode: 'edit',
          activeTask: null,
          ownedTaskId: null,
          // 保留 latestStress / latestSystem / history：edit 模式 MonitorDock 默认折叠，
          // 用户主动展开仍可看到末次快照，需要彻底清掉请走 reset()。
        }),

      reset: () =>
        set({ ...initialState, robotConfig: { ...DEFAULT_ROBOT_CONFIG } }),
    }),
    {
      name: 'stressbot:runtime-form',
      storage: createJSONStorage(() => localStorage),
      // v3：authExtra → stateExtra，authAddr 已从 RobotConfig 中移除。
      version: 3,
      // 只缓存"启动表单"四个字段；运行态（mode/activeTask/agents 等）每次刷新都从 admin 重拉。
      partialize: (s) => ({
        taskName: s.taskName,
        totalBots: s.totalBots,
        robotConfig: s.robotConfig,
        deadline: s.deadline,
      }),
      migrate: (persisted, version) => {
        const p = (persisted ?? {}) as Partial<RuntimeState>;
        // v2 → v3：authExtra → stateExtra，移除 authAddr。
        if (version < 3 && p.robotConfig) {
          const { authAddr: _, authExtra, ...rest } = p.robotConfig as any;
          p.robotConfig = { ...rest, stateExtra: authExtra ?? {} };
        }
        return p;
      },
      // 反序列化后与新版 DEFAULT_ROBOT_CONFIG 合并：
      //   - 老版本 localStorage 里没有的新字段会自动补全；
      //   - 用户已设置的字段不会被覆盖。
      merge: (persisted, current) => {
        const p = (persisted ?? {}) as Partial<RuntimeState>;
        return {
          ...current,
          ...p,
          robotConfig: {
            ...DEFAULT_ROBOT_CONFIG,
            ...(p.robotConfig ?? {}),
          },
        };
      },
    },
  ),
);

/** 便捷：按当前 mode 派生轮询参数（HomeShell 使用） */
export function pollingPolicy(
  mode: RuntimeMode,
): {
  pollStress: boolean;
  pollSystem: boolean;
  pollAgents: boolean;
  pollActiveTask: boolean;
  intervalMs: number;
} {
  switch (mode) {
    case 'running':
    case 'viewActive':
      return {
        pollStress: true,
        pollSystem: true,
        pollAgents: true,
        pollActiveTask: true,
        intervalMs: 5000,
      };
    case 'edit':
      return {
        pollStress: false,
        pollSystem: true,
        pollAgents: true,
        pollActiveTask: false,
        intervalMs: 10000,
      };
    case 'finalReport':
    default:
      return {
        pollStress: false,
        pollSystem: false,
        pollAgents: false,
        pollActiveTask: false,
        intervalMs: 30000,
      };
  }
}
