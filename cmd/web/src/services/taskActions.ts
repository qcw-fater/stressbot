/**
 * 任务启停编排：连接 flowStore（业务数据）+ resourcesStore（资源）+ Admin API + runtimeStore（状态机）。
 *
 * 设计要点：
 * - 这层是 stress test 的"事务边界"：失败回滚（mode 不切，ownedTaskId 不写）；
 * - 与 React 渲染解耦，纯 async 函数 + 错误抛出，由 RuntimeBar 调用 + showApiError 接住；
 * - 本地稿 stash：进入 viewActive 时把当前 flowStore 内容存到 LocalStorage，detach 时还原；
 * - 容量预检：在 startTask 入口先按在线 agents 的 maxBots 总量校验 totalBots，避免无谓提交；
 *   服务端仍是单一权威，前端预检只为体验。
 *
 * 单一事实源：plan §4.1 / §4.2 / §4.7。
 */

import { useFlowStore } from '@/components/FlowEditor/store/flowStore';
import { useProtoStore } from '@/components/FlowEditor/proto/protoStore';
import { validateFlow } from '@/components/FlowEditor/validation/refsCheck';
import { useMetricsStore } from '@/components/FlowEditor/nodes/shared/MetricsBadge';
import * as tasksApi from './tasksApi';
import { listProto, listScript, syncProtoFromBaseline, syncScriptFromBaseline, syncAdapterFromBaseline } from './resourcesStore';
import { syncFlowScriptsToIdb } from './scriptSync';
import { useRuntimeStore } from './runtimeStore';
import { ApiError } from './api';
import type { RobotConfig, TaskBrief, TaskDetail } from '@/types/api';
import type { FlowLayout } from '@/types/editor';
import type { FlowJson } from '@/components/FlowEditor/codec/flowToJson';

const DRAFT_STASH_KEY = 'stressbot:flow:stash';

interface StashedDraft {
  flow: FlowJson;
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
}

/**
 * 启动新任务：组装 multipart → POST /api/tasks → POST /api/tasks/{id}/start。
 *
 * 流程（任意一步失败抛出，runtimeStore 不变更）：
 *   1. flowStore.toTaskFlow() + validateFlow() → 有 error 直接拒绝；
 *   2. syncFlowScriptsToIdb：把 flow 引用、IDB 缺失的脚本从基线拉回 IDB（保护已编辑稿）；
 *      仍缺失则抛错。listProto / listScript 拉资源；
 *   3. 容量预检：sum(online agents.maxBots) >= totalBots；
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
    listens: flowState.listens,
    defaultDelayMs: flowState.defaultDelayMs,
  });
  if (report.errors.length > 0) {
    throw new ApiError(
      {
        code: 'INVALID_ARGUMENT',
        message: `流程校验未通过：${report.errors.length} 项错误，请打开「校验」面板查看详情`,
      },
      400,
    );
  }

  // 校验通过 → 立即清空上次 finalReport 的监控数据。
  //
  // 这一步必须在 await tasks API 之前完成，原因：
  //   - createTask 上传 multipart（含 lua/proto），可能耗时几百 ms 到数秒；
  //   - 在此期间 UI 仍处于 finalReport，节点上保留着上一次任务的 p99/apdex/边框颜色；
  //   - 用户点完"开始压测"后会看到"上次的指标"持续展示，直到第一份新 polling 回来才更新；
  //   - 早清不晚清：极端情况下 createTask 失败回滚，用户也只丢一份"已经看完的最终报告"，
  //     与"开始压测"语义不冲突，可接受。
  //
  // 同时同步清掉 useMetricsStore.provider —— 这一步必须显式做：
  //   - latestStress=null 后 EditorPage 的 useMemo 重算 metricsProvider=undefined；
  //   - 但把 undefined 推到 useMetricsStore 是在 useLayoutEffect 里，仍要等下一次 commit；
  //   - 而 React 组件 re-render 在 zustand set() 之后还要排队，至少 1 个 microtask；
  //   - 直接同步把 provider 设成 undefined 后，所有 NodeShell/MetricsBadge 立刻失去依据，
  //     下一次重渲染就不会再看到上次任务的 p99/apdex/边框残留。
  useRuntimeStore.getState().clearMonitorData();
  useMetricsStore.getState().setProvider(undefined);

  // 2. 资源同步与收集
  //   - 先全量基线同步（三种资源并行），按 fromBaseline 优先级：
  //       IDB 无 → 写入 fromBaseline:true；IDB 有 + fromBaseline:true → 用最新基线覆盖；
  //       IDB 有 + fromBaseline:false → 跳过（用户编辑过）
  //   - 再做 flow 引用的脚本缺失检测（IDB 已有最新基线）
  //   - 最后收集 IDB 内容作为 multipart payload
  const [, , adapterContent] = await Promise.all([
    syncProtoFromBaseline(),
    syncScriptFromBaseline(),
    syncAdapterFromBaseline(),
  ]);
  if (!adapterContent) {
    throw new ApiError(
      {
        code: 'INVALID_ARGUMENT',
        message: '缺少协议适配器。请在「协议适配器」面板导入或载入模板。',
      },
      400,
    );
  }
  const sync = await syncFlowScriptsToIdb(flowJson);
  if (sync.missing.length > 0) {
    throw new ApiError(
      {
        code: 'INVALID_ARGUMENT',
        message:
          `缺少脚本：${sync.missing.join(', ')}。` +
          `请在「资源管理」上传，或在动作编辑器中直接编写。`,
        details: { missingScripts: sync.missing },
      },
      400,
    );
  }
  const [protos, scripts] = await Promise.all([listProto(), listScript()]);

  // 3. 容量预检
  {
    const agents = useRuntimeStore.getState().agents ?? [];
    if (agents.length === 0) {
      throw new ApiError(
        { code: 'INVALID_ARGUMENT', message: '当前没有可用的节点，请先确认节点已注册' },
        400,
      );
    }
    const totalCapacity = agents
      .filter((a) => a.status !== 'offline')
      .reduce((sum, a) => sum + a.maxBots, 0);
    if (totalCapacity < opts.totalBots) {
      throw new ApiError(
        {
          code: 'CAPACITY_EXCEEDED',
          message: `集群总容量 ${totalCapacity}，本次申请 ${opts.totalBots}`,
          details: { totalCapacity, requestedBots: opts.totalBots },
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
  if (adapterContent) {
    fd.append('adapter/codec.lua', new Blob([adapterContent], { type: 'text/plain' }), 'codec.lua');
  }

  // 5. 提交
  const created = await tasksApi.createTask(fd);
  await tasksApi.startTask(created.id);

  // 6. 同步运行态。clearMonitorData 已经在校验后立刻调过了（开头），这里不必再清。
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
  let remoteFlow: FlowJson | null = null;
  try {
    const res = await fetch(tasksApi.taskConfigUrl(taskId, 'flow.json'));
    if (res.ok) {
      remoteFlow = (await res.json()) as FlowJson;
    }
  } catch {
    // 不阻塞：detail 已成功，画布保持本地稿
  }

  if (remoteFlow) {
    flowState.loadFromTaskFlow(remoteFlow);
    // attach 到远端任务时也尝试同步脚本到 IDB（保护已存在的编辑稿不覆盖）。
    // 这样用户后续切回 edit 模式接续修改时不会因为缺脚本而失败。
    void syncFlowScriptsToIdb(remoteFlow);
  }

  const runtime = useRuntimeStore.getState();
  // 同 startTask：在切换 mode 前清掉残留监控数据，避免上一次的 finalReport 数据闪一下；
  // useMetricsStore.provider 也必须同步清掉，否则节点会保留上次任务 1~2 帧的 p99/apdex/边框。
  runtime.clearMonitorData();
  useMetricsStore.getState().setProvider(undefined);
  runtime.setActiveTask(detail);
  // 本端没主导这个任务（任意客户端发起）→ 走 viewActive
  if (runtime.ownedTaskId !== taskId) {
    runtime.setOwnedTaskId(null);
    runtime.setMode('viewActive');
  } else {
    runtime.setMode('running');
  }

  // proto 资源同步：留给后续，proto 是按 messageType 名引用的，无法从 flow 静态推断
  // 应该带哪些 .proto 文件；用户需主动到「资源管理」上传。
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

