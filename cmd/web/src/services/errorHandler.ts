/**
 * 集中处理 ApiError 的展现策略。
 *
 * 设计要点：
 * - `showApiError` 优先使用通过 `setMessageApi` 注入的 antd `App.useApp()` 实例（动态主题、ConfigProvider 上下文）；
 *   未注入时回退静态 `message`/`Modal`（会触发 "Static function can not consume context" 警告）；
 * - `TASK_CONFLICT` 单独走 modal.confirm，需要让用户决定"查看运行中"或"留在编辑态"；
 *   该决策需访问 runtimeStore，故由调用方传 `onAttachActive` 回调进来，避免 services 反向依赖 store；
 * - 网络抖动单错误（NETWORK_ERROR）默认静默；usePolling 会聚合 ≥3 次失败统一提示。
 *
 * 单一事实源：docs/api-monitor.md §14。
 */

import { Modal as StaticModal, message as staticMessage } from 'antd';
import type { MessageInstance } from 'antd/es/message/interface';
import type { ModalStaticFunctions } from 'antd/es/modal/confirm';
import { ApiError } from './api';
import type { TaskConflictDetails } from '@/types/api';

type MessageLike = Pick<MessageInstance, 'error' | 'warning' | 'info' | 'success'>;
type ModalLike = Pick<ModalStaticFunctions, 'confirm' | 'info' | 'warning' | 'error' | 'success'>;

/** 全局共享的 message / modal 实例；由 App 顶层在挂载时调用 `setMessageApi` 注入。 */
let messageRef: MessageLike = staticMessage;
let modalRef: ModalLike = StaticModal as unknown as ModalLike;

/**
 * 注入 antd `App.useApp()` 返回的 message / modal 实例。
 * 调用之后，`showApiError` 与 TASK_CONFLICT 弹窗都会走动态实例，享受 ConfigProvider 主题。
 *
 * 传 null 可恢复静态实例（一般用于卸载/单元测试）。
 */
export function setMessageApi(api: { message?: MessageLike; modal?: ModalLike } | null): void {
  if (!api) {
    messageRef = staticMessage;
    modalRef = StaticModal as unknown as ModalLike;
    return;
  }
  if (api.message) messageRef = api.message;
  if (api.modal) modalRef = api.modal;
}

const TOAST_MAP: Record<string, (err: ApiError) => string> = {
  TASK_NOT_FOUND: () => '任务不存在或已被删除',
  TASK_INVALID_STATE: (err) => `任务当前状态不允许此操作（${err.message}）`,
  AGENT_NOT_FOUND: () => '节点不存在',
  AGENT_BUSY: () => 'Agent 正忙，请选择其他节点',
  AGENT_OFFLINE: () => 'Agent 已离线',
  CAPACITY_EXCEEDED: (err) => {
    const max = (err.details?.maxBots as number | undefined) ?? '?';
    return `集群容量不足，最多支持 ${max} 个机器人`;
  },
  UPGRADE_IN_PROGRESS: () => '已有滚动升级在进行中',
  BINARY_NOT_FOUND: () => '该版本二进制不存在，请先上传',
  HISTORY_NOT_FOUND: () => '历史记录不存在或已被删除',
  HISTORY_STARRED: () => '已收藏的记录不能删除（请先取消收藏，或使用强制删除）',
  HISTORY_DISABLED: () => 'admin 未启用 history 模块（需配置 MySQL），暂无法查看历史记录',
  INVALID_ARGUMENT: (err) => `参数非法：${err.message}`,
  NETWORK_ERROR: () => '网络异常，请检查 Admin 是否可达',
  HTTP_ERROR: (err) => `请求失败（${err.status}）：${err.message}`,
};

export interface TaskConflictHandler {
  /** 用户选择"查看运行中" → 由 runtimeStore.attachToActive 处理 */
  onAttachActive: (activeTaskId: string) => Promise<void> | void;
}

let conflictHandlerRef: TaskConflictHandler | null = null;

/**
 * 注册 TASK_CONFLICT 处理器。HomeShell 启动时注入一次即可；
 * 未注册时回退为只显示提示文案。
 */
export function registerTaskConflictHandler(handler: TaskConflictHandler | null): void {
  conflictHandlerRef = handler;
}

/**
 * 把任意错误以最合适的方式展现给用户。
 *
 * - `ApiError` 走预置文案表；
 * - `TASK_CONFLICT` 走 modal；
 * - 其它走 antd message.error。
 *
 * 返回 boolean：true 表示已经"独占处理"（含 modal），调用方不需要再做什么；
 *               false 表示只是显示了 toast，调用方可决定是否继续业务逻辑。
 */
export function showApiError(err: unknown): boolean {
  if (err instanceof ApiError && err.code === 'TASK_CONFLICT') {
    showTaskConflict(err);
    return true;
  }
  if (err instanceof ApiError) {
    const builder = TOAST_MAP[err.code];
    const text = builder ? builder(err) : err.message;
    messageRef.error(text);
    return false;
  }
  if (err instanceof Error) {
    messageRef.error(err.message);
  } else {
    messageRef.error(String(err));
  }
  return false;
}

function showTaskConflict(err: ApiError): void {
  const details = err.details as TaskConflictDetails | undefined;
  const handler = conflictHandlerRef;
  modalRef.confirm({
    title: '已有任务在执行',
    content: details
      ? `任务"${details.activeName}"当前处于 ${details.activeState} 状态，需先停止才能启动新任务。`
      : '集群中已有任务在执行。',
    okText: details && handler ? '查看运行中' : '我知道了',
    cancelText: '取消',
    onOk: async () => {
      if (details && handler) {
        await handler.onAttachActive(details.activeTaskId);
      }
    },
  });
}
