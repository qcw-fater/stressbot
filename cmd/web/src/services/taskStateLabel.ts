/**
 * 任务状态的中文文案与标签配色。
 *
 * 状态值本身是后端状态机的枚举（pending/starting/...），不是给人看的；界面上任何位置
 * 显示状态都必须经过这里翻译。放在 services 而不是某个组件里，是因为顶栏、引导弹窗和
 * 错误提示三处都要用，之前各写各的，结果只有顶栏做了翻译，另外两处把英文枚举直接甩给用户。
 */

import type { TaskBrief } from '@/types/api';

export type TaskState = TaskBrief['state'];

const STATE_LABEL: Record<TaskState, string> = {
  pending: '待启动',
  starting: '启动中',
  running: '运行中',
  stopping: '停止中',
  stopped: '已停止',
  failed: '失败',
};

const STATE_COLOR: Record<TaskState, string> = {
  pending: 'default',
  starting: 'gold',
  running: 'processing',
  stopping: 'volcano',
  stopped: 'success',
  failed: 'error',
};

/** 状态中文名；遇到未知值（后端加了新状态而前端未跟进）原样返回，不吞掉信息。 */
export function taskStateLabel(state: TaskState | string | undefined | null): string {
  if (!state) return '未知';
  return STATE_LABEL[state as TaskState] ?? state;
}

/** antd Tag 的 color，未知状态用默认灰。 */
export function taskStateColor(state: TaskState | string | undefined | null): string {
  if (!state) return 'default';
  return STATE_COLOR[state as TaskState] ?? 'default';
}
