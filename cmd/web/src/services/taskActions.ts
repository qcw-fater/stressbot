/**
 * 任务启停编排：连接 flowStore（业务数据）+ resourcesStore（资源）+ 服务器 API + runtimeStore（状态机）。
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
import { getCapabilities } from './capabilitiesApi';
import { listProto, listScript, listCodecFiles, getErrorMap, markResourcesAsBaselineSynced } from './resourcesStore';
import { syncFlowScriptsToIdb, collectFlowScriptNames } from './scriptSync';
import { collectFlowCodecConnections, findMissingCodecConnections } from './taskResourceDiff';
import { useRuntimeStore } from './runtimeStore';
import { ApiError } from './api';
import type { RobotConfig, TaskBrief, TaskDetail } from '@/types/api';
import type { FlowLayout } from '@/types/editor';
import type { FlowJson } from '@/components/FlowEditor/codec/flowToJson';

const DRAFT_STASH_KEY = 'stressbot:flow:stash';

/** 与后端 sharedstate.UsesShare 一致：检测脚本是否引用 share 模块。 */
const SHARE_REQUIRE_RE = /require\s*\(?\s*['"]share['"]/;

/**
 * 剥离 Lua 注释（块注释 --[[ ]] 与行注释 --），避免被注释掉的 require("share") 误判。
 * 仅做前端预检，最终以服务端检测为准，因此实现保持轻量。
 */
function stripLuaComments(content: string): string {
  return content
    .replace(/--\[(=*)\[[\s\S]*?\]\1\]/g, ' ')
    .replace(/--[^\n]*/g, ' ');
}

/** 脚本内容里是否使用了共享状态模块。 */
function scriptUsesShare(content: string): boolean {
  return SHARE_REQUIRE_RE.test(stripLuaComments(content));
}

interface StashedDraft {
  flow: FlowJson;
  layout: FlowLayout;
  savedAt: number;
}

/**
 * 进入运行态前：stash 当前编辑草稿到 LocalStorage，再用实际启动/运行的 flow 替换画布。
 * 主动启动（startTask）与挂载运行中任务（attachToActive）共用，保证草稿保存与画布替换一致。
 */
export function stashAndReplaceCanvas(flow: FlowJson, layout?: FlowLayout) {
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
  flowState.loadFromTaskFlow(flow, layout);
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
  /** 实际启动的流程（当前画布或已保存流程），由调用方显式传入 */
  flow: FlowJson;
  /** flow 对应的布局，用于运行态画布还原（可选） */
  flowLayout?: FlowLayout;
  /** 来源流程模板 ID，仅用于历史溯源（可选） */
  flowTemplateId?: string;
}

/**
 * 启动新任务：组装 multipart → tasksApi.createTask → tasksApi.startTask。
 *
 * 流程（任意一步失败抛出，runtimeStore 不变更）：
 *   1. flowStore.toTaskFlow() + validateFlow() → 有 error 直接拒绝；
 *   2. syncFlowScriptsToIdb：把 flow 引用、本地存储缺失的脚本从基线拉回本地存储（保护已编辑稿）；
 *      仍缺失则抛错。listProto / listScript 拉资源；
 *   3. 容量预检：在线节点 maxBots 之和 >= totalBots；
 *   4. tasksApi.createTask → 拿 taskId；
 *   5. tasksApi.startTask → 拿 assignments；
 *   6. 写入 runtimeStore：mode='running', ownedTaskId=id, activeTask, robotConfig...
 *
 * @returns 创建并启动的任务 ID
 */
export async function startTask(opts: StartTaskOptions): Promise<string> {
  const flowJson = opts.flow;

  // 1. 业务校验（FlowJson 与 TaskFlow 同构，可直接校验）
  const report = validateFlow(flowJson);
  if (report.errors.length > 0) {
    throw new ApiError(
      {
        code: 'INVALID_ARGUMENT',
        message: `流程校验未通过：${report.errors.length} 项错误，请打开「校验」面板查看详情`,
      },
      400,
    );
  }

  // 2. 资源收集
  //   - 再次执行 flow 引用脚本 gap-fill 与缺失检测，作为启动前最终拦截
  //   - 收集本地存储内容作为 multipart payload
  //   - 确保协议配置（*_codec.json）存在；errors.json 错误码表可选，未配置则不下发
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
  const [protos, scripts, codecs, errorMapRes] = await Promise.all([listProto(), listScript(), listCodecFiles(), getErrorMap()]);

  // 只提交 flow 引用到的脚本；proto 文件无法从 message 全名静态映射到文件名，全量提交
  const scriptNames = new Set(collectFlowScriptNames(flowJson));
  const usedScripts = scripts.filter((s) => scriptNames.has(s.name));
  if (codecs.length === 0) {
    throw new ApiError(
      {
        code: 'INVALID_ARGUMENT',
        message: '缺少协议配置，请在「协议配置」面板导入或新建',
      },
      400,
    );
  }

  // flow 引用的每条连接（tcp*/udp* 动作的 `<protocol>:<service>`）都必须在本地存储有对应的
  // `<protocol>_<service>_codec.json`，否则节点在该连接 dial 时 CodecResolver 解析不到 codec
  // 会 fail-loud。这里把该校验前移到任务启动，给清晰中文提示（启动前拦截 vs 启动后失败）。
  // 上传范围不变：下面仍发全部 codec 文件，节点侧 resolver 加载全部。
  {
    const codecFileNames = codecs.map((f) => f.name);
    const referenced = collectFlowCodecConnections(flowJson);
    const missing = findMissingCodecConnections(referenced, codecFileNames);
    if (missing.length > 0) {
      throw new ApiError(
        {
          code: 'INVALID_ARGUMENT',
          message:
            `以下连接缺少协议配置文件：${missing.join('，')}。` +
            `请在「协议配置」面板新建对应连接的协议配置。`,
          details: { missingCodecConnections: missing },
        },
        400,
      );
    }
  }

  // 2.5 共享状态预检：脚本使用 share 但服务器未配置 Redis 时，提前拦截并提示，
  //     避免任务创建后立即被服务端以 SHARED_STATE_UNAVAILABLE 拒绝。
  if (usedScripts.some((s) => scriptUsesShare(s.content))) {
    let sharedAvailable = true;
    try {
      const caps = await getCapabilities();
      sharedAvailable = caps.sharedState;
    } catch {
      // 能力查询失败不阻塞：交由服务端最终裁决
      sharedAvailable = true;
    }
    if (!sharedAvailable) {
      throw new ApiError(
        {
          code: 'SHARED_STATE_UNAVAILABLE',
          message: '该流程使用共享状态，但服务器未配置 Redis。请先在服务器配置中启用共享状态后再启动。',
        },
        400,
      );
    }
  }

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
  if (opts.flowTemplateId) fd.append('flowTemplateId', opts.flowTemplateId);
  fd.append('flow.json', new Blob([JSON.stringify(flowJson, null, 2)], { type: 'application/json' }), 'flow.json');
  for (const f of protos) {
    fd.append(`proto/${f.name}`, new Blob([f.content], { type: 'text/plain' }), f.name);
  }
  for (const f of usedScripts) {
    fd.append(`scripts/${f.name}`, new Blob([f.content], { type: 'text/plain' }), f.name);
  }
  for (const f of codecs) {
    fd.append(`adapter/${f.name}`, new Blob([f.content], { type: 'application/json' }), f.name);
  }
  const errorMapContent = errorMapRes?.content ?? null;
  if (errorMapContent) {
    fd.append('adapter/errors.json', new Blob([errorMapContent], { type: 'application/json' }), 'errors.json');
  }

  // 5. 提交。createTask 成功后服务器已写回 conf/ 基线，立即标记本次上传资源为新基线。
  const created = await tasksApi.createTask(fd);
  await markResourcesAsBaselineSynced({
    protos,
    scripts: usedScripts,
    codecs,
    errorMap: errorMapRes ?? null,
  });
  await tasksApi.startTask(created.id);

  // 6. 远端启动成功后再提交本地运行态。此前任何预检或请求失败都必须保留
  //    上一次最终报告，避免用户因为一次失败启动丢失仍在查看的数据。
  const runtime = useRuntimeStore.getState();
  runtime.clearMonitorData();
  useMetricsStore.getState().setMetrics(undefined);

  // 用实际启动的 flow 替换画布（stash 当前草稿），再切 running。
  stashAndReplaceCanvas(opts.flow, opts.flowLayout);
  runtime.setOwnedTaskId(created.id);
  runtime.setMode('running');
  runtime.setActiveTask({
    id: created.id,
    name: opts.name,
    state: 'starting',
    totalBots: opts.totalBots,
    agentCount: 0,
    activeAgentCount: 0,
    createdAt: new Date().toISOString(),
  });

  return created.id;
}

/**
 * 停止当前任务。RuntimeBar 调用；服务端进入 stopping → stopped 后由轮询触发 onTaskFinished。
 *
 * tasksApi.stopTask 同步返回最新 TaskBrief（含 state=stopping），
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
  const runtime = useRuntimeStore.getState();

  // 拉远端 flow.json
  let remoteFlow: FlowJson | null = null;
  try {
    remoteFlow = await tasksApi.getTaskConfigJson<FlowJson>(taskId, 'flow/flow.json');
  } catch {
    // 不阻塞：detail 已成功，画布保持本地稿
  }

  // 远端 flow 拉取成功才 stash 草稿 + 替换画布；失败时画布保持本地稿（草稿未丢失，无需 stash）。
  if (remoteFlow) {
    stashAndReplaceCanvas(remoteFlow);
    // attach 到远端任务时也尝试同步脚本到本地存储（保护已存在的编辑稿不覆盖）。
    // 这样用户后续切回 edit 模式接续修改时不会因为缺脚本而失败。
    void syncFlowScriptsToIdb(remoteFlow);
  }

  // 同 startTask：在切换 mode 前清掉残留监控数据，避免上一次的 finalReport 数据闪一下；
  // 节点指标 Map 也必须同步清掉，否则节点会保留上次任务 1~2 帧的 p99/apdex/边框。
  runtime.clearMonitorData();
  useMetricsStore.getState().setMetrics(undefined);
  runtime.setActiveTask(detail);
  runtime.setDetachedActiveTask(null);
  if (detail.state === 'stopped' || detail.state === 'failed') {
    runtime.setOwnedTaskId(null);
    runtime.setMode('finalReport');
  } else if (runtime.ownedTaskId !== taskId) {
    // 本端没主导这个任务（任意客户端发起）→ 走 viewActive
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
 * 运行中临时返回编辑：恢复本地草稿画布，保留脱离态任务引用。
 *
 * 与 attachToActive 配对：attach 负责把本地草稿 stash 并加载远端 flow；
 * 本函数把 stash 还原回 flowStore，实现编辑/监测两个独立 flow 互不干扰。
 * 不清除 stash（用户可能重新进入监测）。
 */
export function detachToEditWithRestore(): void {
  restoreStash(false);
  useRuntimeStore.getState().detachToEdit();
}

/**
 * 最终退出任务上下文（finalReport → edit）：恢复本地草稿画布并清除 stash。
 */
export function detachFromActiveWithRestore(): void {
  restoreStash(true);
  useRuntimeStore.getState().detachFromActive();
}

/** 从 LocalStorage 恢复 stash 的编辑稿到 flowStore。 */
function restoreStash(clear: boolean): void {
  try {
    const raw = localStorage.getItem(DRAFT_STASH_KEY);
    if (!raw) return;
    const stash = JSON.parse(raw) as StashedDraft;
    useFlowStore.getState().loadFromTaskFlow(stash.flow, stash.layout);
    if (clear) localStorage.removeItem(DRAFT_STASH_KEY);
  } catch {
    // 恢复失败，保持当前画布
  }
}

/**
 * 还原 stash 编辑稿（外部手动调用）。
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

