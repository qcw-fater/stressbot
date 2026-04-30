/**
 * 任务启停编排：连接 flowStore（业务数据）+ resourcesStore（资源）+ Admin API + runtimeStore（状态机）。
 *
 * 设计要点：
 * - 这层是 stress test 的"事务边界"：失败回滚（mode 不切，ownedTaskId 不写）；
 * - 与 React 渲染解耦，纯 async 函数 + 错误抛出，由 RuntimeBar 调用 + showApiError 接住；
 * - 本地稿 stash：进入 viewActive 时把当前 flowStore 内容存到 LocalStorage，detach 时还原；
 * - 容量预检：在 startTask 入口先按 agents 总剩余容量校验 totalBots，避免无谓提交；
 *   服务端仍是单一权威，前端预检只为体验。
 *
 * 单一事实源：plan §4.1 / §4.2 / §4.7。
 */

import { useFlowStore } from '@/components/FlowEditor/store/flowStore';
import { useProtoStore } from '@/components/FlowEditor/proto/protoStore';
import { validateFlow } from '@/components/FlowEditor/validation/refsCheck';
import * as tasksApi from './tasksApi';
import { listProto, listScript } from './resourcesStore';
import { useRuntimeStore } from './runtimeStore';
import { ApiError } from './api';
import type { RobotConfig, TaskBrief, TaskDetail } from '@/types/api';
import type { FlowLayout } from '@/types/editor';
import type { TaskFlow } from '@/types/flow';

const DRAFT_STASH_KEY = 'stressbot:flow:stash';

interface StashedDraft {
  flow: TaskFlow;
  layout: FlowLayout;
  savedAt: number;
}

export interface StartTaskOptions {
  /** 任务名 */
  name: string;
  /** 全集群机器人数 */
  totalBots: number;
  /** 通用 robot 配置 */
  robotConfig: RobotConfig;
  /** 自动停止时间（RFC3339），可选 */
  deadline?: string | null;
  /** 跳过容量预检（强制提交，让服务端兜底） */
  skipCapacityCheck?: boolean;
}

/**
 * 启动新任务：组装 multipart → POST /api/tasks → POST /api/tasks/{id}/start。
 *
 * 流程（任意一步失败抛出，runtimeStore 不变更）：
 *   1. flowStore.toTaskFlow() + validateFlow() → 有 error 直接拒绝；
 *   2. listProto / listScript 拉用户上传的资源（IDB 为空也允许，由 Admin 兜底默认）；
 *   3. 容量预检：sum(agents.maxBots - currentBots) >= totalBots；
 *   4. POST /api/tasks → 拿 taskId；
 *   5. POST /api/tasks/{id}/start → 拿 assignments；
 *   6. 写入 runtimeStore：mode='running', ownedTaskId=id, activeTask, robotConfig...
 *
 * @returns 创建并启动的任务 ID
 */
export async function startTask(opts: StartTaskOptions): Promise<string> {
  const flowState = useFlowStore.getState();
  const flowJson = flowState.toTaskFlow();

  // 1. 业务校验
  const report = validateFlow({
    nodes: flowState.nodes,
    actions: flowState.actions,
    callbacks: flowState.callbacks,
    defaultDelayMs: flowState.defaultDelayMs,
  });
  if (report.errors.length > 0) {
    throw new ApiError(
      {
        code: 'INVALID_ARGUMENT',
        message: `flow.json 校验未通过：${report.errors.length} 项错误，请打开「校验」面板查看详情`,
      },
      400,
    );
  }

  // 2. 资源
  const [protos, scripts] = await Promise.all([listProto(), listScript()]);

  // 3. 容量预检
  if (!opts.skipCapacityCheck) {
    const agents = useRuntimeStore.getState().agents ?? [];
    if (agents.length === 0) {
      throw new ApiError(
        { code: 'INVALID_ARGUMENT', message: '当前没有可用的 Agent，请先确认 Agent 已注册' },
        400,
      );
    }
    const available = agents
      .filter((a) => a.status === 'idle' || a.status === 'busy')
      .reduce((sum, a) => sum + Math.max(0, a.maxBots - a.currentBots), 0);
    if (available < opts.totalBots) {
      throw new ApiError(
        {
          code: 'CAPACITY_EXCEEDED',
          message: `集群剩余容量 ${available}，本次申请 ${opts.totalBots}`,
          details: { availableBots: available, requestedBots: opts.totalBots },
        },
        400,
      );
    }
  }

  // 4. 组装 multipart
  const fd = new FormData();
  fd.append('name', opts.name);
  fd.append('totalBots', String(opts.totalBots));
  fd.append('robotConfig', JSON.stringify(opts.robotConfig));
  if (opts.deadline) fd.append('deadline', opts.deadline);
  fd.append('flow.json', new Blob([JSON.stringify(flowJson, null, 2)], { type: 'application/json' }), 'flow.json');
  for (const f of protos) {
    fd.append(`proto/${f.name}`, new Blob([f.content], { type: 'text/plain' }), f.name);
  }
  for (const f of scripts) {
    fd.append(`scripts/${f.name}`, new Blob([f.content], { type: 'text/plain' }), f.name);
  }

  // 5. 提交
  const created = await tasksApi.createTask(fd);
  await tasksApi.startTask(created.id);

  // 6. 同步运行态
  const runtime = useRuntimeStore.getState();
  runtime.setOwnedTaskId(created.id);
  runtime.setMode('running');
  runtime.setActiveTask({
    id: created.id,
    name: opts.name,
    state: 'starting',
    totalBots: opts.totalBots,
    agentCount: 0,
    createdAt: new Date().toISOString(),
  });

  return created.id;
}

/**
 * 停止当前任务。RuntimeBar 调用；服务端进入 stopping → stopped 后由轮询触发 onTaskFinished。
 *
 * 后端 `POST /api/tasks/{id}/stop` 同步返回最新 TaskBrief（含 state=stopping），
 * 这里立即写入 store，让 UI 不必等到下一轮 5s 轮询才反映"停止中"状态。
 *
 * 兼容老后端返回 `{status:"stopping"}` 的形态：判断返回对象是否带 `state` 字段，
 * 没有则跳过即时更新，靠轮询补齐。
 */
export async function stopTask(taskId?: string): Promise<TaskBrief | null> {
  const id = taskId ?? useRuntimeStore.getState().ownedTaskId ?? useRuntimeStore.getState().activeTask?.id;
  if (!id) {
    throw new Error('当前没有可停止的任务');
  }
  const updated = await tasksApi.stopTask(id);
  if (updated && typeof (updated as TaskBrief).state === 'string') {
    useRuntimeStore.getState().setActiveTask(updated);
    return updated;
  }
  return null;
}

/**
 * 进入"查看运行中"模式：把本地稿 stash → 拉远端 flow.json → 替换 flowStore → 切 mode='viewActive'。
 *
 * - 失败时 runtimeStore 与 flowStore 不变；
 * - 失败时 stash 不写入；
 * - 当远端 flow.json 不存在（极端场景）时降级为只显示 detail，不替换画布。
 */
export async function attachToActive(taskId: string): Promise<void> {
  const detail: TaskDetail = await tasksApi.getTask(taskId);

  // stash 当前编辑稿到 LocalStorage（仅当前面有内容时）
  const flowState = useFlowStore.getState();
  const hasLocal = Object.keys(flowState.nodes).length > 0;
  if (hasLocal) {
    try {
      const stash: StashedDraft = {
        flow: flowState.toTaskFlow(),
        layout: flowState.layout,
        savedAt: Date.now(),
      };
      localStorage.setItem(DRAFT_STASH_KEY, JSON.stringify(stash));
    } catch {
      // localStorage 满 / 隐私模式 → 静默跳过
    }
  }

  // 拉远端 flow.json
  let remoteFlow: TaskFlow | null = null;
  try {
    const res = await fetch(tasksApi.taskConfigUrl(taskId, 'flow.json'));
    if (res.ok) {
      remoteFlow = (await res.json()) as TaskFlow;
    }
  } catch {
    // 不阻塞：detail 已成功，画布保持本地稿
  }

  if (remoteFlow) {
    flowState.loadFromTaskFlow(remoteFlow);
  }

  const runtime = useRuntimeStore.getState();
  runtime.setActiveTask(detail);
  // 本端没主导这个任务（任意客户端发起）→ 走 viewActive
  if (runtime.ownedTaskId !== taskId) {
    runtime.setOwnedTaskId(null);
    runtime.setMode('viewActive');
  } else {
    runtime.setMode('running');
  }

  // 资源同步：把 detail.config.flowFiles 中的 proto/lua 顺带拉回 IDB？
  // 留给后续：本次不动，避免覆盖用户自己上传的资源。
  void useProtoStore.getState();
}

/**
 * 还原 stash 编辑稿（finalReport / detach 后用户主动选择"恢复编辑稿"时调用）。
 * @returns 是否成功还原
 */
export function restoreStashedDraft(): boolean {
  try {
    const raw = localStorage.getItem(DRAFT_STASH_KEY);
    if (!raw) return false;
    const stash = JSON.parse(raw) as StashedDraft;
    useFlowStore.getState().loadFromTaskFlow(stash.flow, stash.layout);
    localStorage.removeItem(DRAFT_STASH_KEY);
    return true;
  } catch {
    return false;
  }
}

/** 是否存在被 stash 起来的编辑稿 */
export function hasStashedDraft(): boolean {
  try {
    return localStorage.getItem(DRAFT_STASH_KEY) !== null;
  } catch {
    return false;
  }
}
