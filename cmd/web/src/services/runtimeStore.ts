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
 * 单一事实源：plan §2 / §4。
 */

import { create } from 'zustand';
import type {
  AgentBrief,
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

  /** 任务结束钩子：mode 切到 finalReport，停止后续历史写入 */
  onTaskFinished: () => void;
  /** 离开终态 / 取消 viewActive，回到 edit；保留最后一份快照 */
  detachFromActive: () => void;
  /** 完整重置（新建任务） */
  reset: () => void;
}

const DEFAULT_ROBOT_CONFIG: RobotConfig = {
  authAddr: '127.0.0.1:6000',
  concurrency: 50,
  timeoutSec: 30,
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
};

function pushWithLimit<T>(arr: T[], item: T, limit = HISTORY_WINDOW): T[] {
  const next = arr.length >= limit ? arr.slice(arr.length - limit + 1) : arr.slice();
  next.push(item);
  return next;
}

export const useRuntimeStore = create<RuntimeState>((set) => ({
  ...initialState,

  setMode: (m) => set({ mode: m }),
  setActiveTask: (task) => set({ activeTask: task }),
  setOwnedTaskId: (id) => set({ ownedTaskId: id }),

  setTaskName: (v) => set({ taskName: v }),
  setTotalBots: (v) => set({ totalBots: v }),
  setRobotConfig: (v) => set((s) => ({ robotConfig: { ...s.robotConfig, ...v } })),
  setDeadline: (v) => set({ deadline: v }),

  pushStress: (snap) =>
    set((s) => ({
      latestStress: snap,
      stressHistory: pushWithLimit(s.stressHistory, snap),
    })),
  pushSystem: (snap) =>
    set((s) => ({
      latestSystem: snap,
      systemHistory: pushWithLimit(s.systemHistory, snap),
    })),
  setAgents: (items) => set({ agents: items }),
  setConnectionLost: (lost) => set({ connectionLost: lost }),

  onTaskFinished: () =>
    set({
      mode: 'finalReport',
      // 保留 latestStress / latestSystem / history 给最终报告使用
    }),

  detachFromActive: () =>
    set({
      mode: 'edit',
      activeTask: null,
      ownedTaskId: null,
      // 保留 latestStress / latestSystem 让 finalReport 还能展示最后值
    }),

  reset: () => set({ ...initialState, robotConfig: { ...DEFAULT_ROBOT_CONFIG } }),
}));

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
