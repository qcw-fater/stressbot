/**
 * 进入页面时检测到 active 任务的引导弹窗。
 *
 * 用户选择：
 *   - "查看运行中"：调用 attachToActive(taskId)；mode → viewActive；本地稿 stash 到 LocalStorage；
 *   - "继续编辑"：留在 edit；顶部栏保留“查看监控”入口且启动按钮禁用。
 *
 * 关闭对话框（点 X / mask）等价于"继续编辑"。
 */

import { Descriptions, Modal, Tag } from 'antd';
import { attachToActive, showApiError } from '@/services';
import { useFloatingWindowStore } from '@/components/FlowEditor/store/floatingWindowStore';
import type { TaskBrief } from '@/types/api';

export interface ActiveTaskGuardModalProps {
  open: boolean;
  task: TaskBrief | null;
  onClose: () => void;
  /** 用户选择查看运行中（attach 完成后由调用方决定后续 UI） */
  onAttached?: (task: TaskBrief) => void;
}

export function ActiveTaskGuardModal({ open, task, onClose, onAttached }: ActiveTaskGuardModalProps) {
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;
  const handleAttach = async () => {
    if (!task) return;
    try {
      await attachToActive(task.id);
      onAttached?.(task);
      onClose();
    } catch (e) {
      showApiError(e);
    }
  };

  return (
    <Modal
      title="集群已有任务在执行"
      open={open && task !== null}
      onCancel={onClose}
      onOk={handleAttach}
      okText="查看运行中"
      cancelText="继续编辑"
      width={520}
      styles={{ mask: { zIndex: popupZ }, wrapper: { zIndex: popupZ + 1 } }}
    >
      {task && (
        <>
          <Descriptions size="small" column={1}>
            <Descriptions.Item label="任务名">{task.name}</Descriptions.Item>
            <Descriptions.Item label="任务 ID">
              <code>{task.id}</code>
            </Descriptions.Item>
            <Descriptions.Item label="状态">
              <Tag color={STATE_COLOR[task.state]}>{task.state}</Tag>
            </Descriptions.Item>
            <Descriptions.Item label="机器人数">
              {task.totalBots} 个，分布在 {task.activeAgentCount}/{task.agentCount} 个节点
            </Descriptions.Item>
            <Descriptions.Item label="启动时间">
              {task.startedAt ? new Date(task.startedAt).toLocaleString() : '—'}
            </Descriptions.Item>
          </Descriptions>
          <p style={{ marginTop: 12, fontSize: 13, color: 'var(--text-secondary)' }}>
            选择「查看运行中」会用该任务的流程替换当前画布并锁定为只读，本地编辑稿会自动暂存；
            选择「继续编辑」可继续修改本地稿，并可通过顶部「查看监控」重新进入监控。
          </p>
        </>
      )}
    </Modal>
  );
}

const STATE_COLOR: Record<TaskBrief['state'], string> = {
  pending: 'default',
  starting: 'gold',
  running: 'processing',
  stopping: 'volcano',
  stopped: 'default',
  failed: 'error',
};
