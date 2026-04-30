/**
 * 启动任务确认弹窗：参数复核 + 资源清单 + 容量预检 + 提交。
 *
 * 数据来源：
 *   - 任务名 / totalBots / robotConfig / deadline 来自 useRuntimeStore（与 RuntimeBar 双向绑定）；
 *   - 资源清单（proto / lua）来自 resourcesStore.listProto / listScript；
 *   - 容量提示来自 useRuntimeStore.agents。
 *
 * 提交：调用 services.startTask；成功后由调用方关闭 modal，失败由 showApiError 接住并展示。
 */

import { Alert, DatePicker, Descriptions, Form, Input, InputNumber, Modal, Tag, Typography } from 'antd';
import { useEffect, useMemo, useState } from 'react';
import dayjs, { type Dayjs } from 'dayjs';
import { useShallow } from 'zustand/react/shallow';
import { showApiError, startTask, useRuntimeStore } from '@/services';
import { listProto, listScript, type ResourceFile } from '@/services/resourcesStore';

export interface TaskStartModalProps {
  open: boolean;
  onClose: () => void;
  /** 启动成功后的回调（拿到 taskId） */
  onStarted?: (taskId: string) => void;
}

export function TaskStartModal({ open, onClose, onStarted }: TaskStartModalProps) {
  const {
    taskName,
    totalBots,
    robotConfig,
    deadline,
    agents,
    setTaskName,
    setTotalBots,
    setRobotConfig,
    setDeadline,
  } = useRuntimeStore(
    useShallow((s) => ({
      taskName: s.taskName,
      totalBots: s.totalBots,
      robotConfig: s.robotConfig,
      deadline: s.deadline,
      agents: s.agents,
      setTaskName: s.setTaskName,
      setTotalBots: s.setTotalBots,
      setRobotConfig: s.setRobotConfig,
      setDeadline: s.setDeadline,
    })),
  );

  const [protos, setProtos] = useState<ResourceFile[]>([]);
  const [scripts, setScripts] = useState<ResourceFile[]>([]);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!open) return;
    void Promise.all([listProto(), listScript()]).then(([p, s]) => {
      setProtos(p);
      setScripts(s);
    });
  }, [open]);

  const availableBots = useMemo(() => {
    return agents
      .filter((a) => a.status === 'idle' || a.status === 'busy')
      .reduce((sum, a) => sum + Math.max(0, a.maxBots - a.currentBots), 0);
  }, [agents]);

  const capacityWarn = totalBots > availableBots;

  const handleSubmit = async () => {
    setSubmitting(true);
    try {
      const id = await startTask({
        name: taskName,
        totalBots,
        robotConfig,
        deadline: deadline ?? undefined,
      });
      onStarted?.(id);
      onClose();
    } catch (e) {
      showApiError(e);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      title="启动压测任务"
      open={open}
      onCancel={onClose}
      onOk={handleSubmit}
      okText="启动"
      cancelText="取消"
      confirmLoading={submitting}
      okButtonProps={{ disabled: capacityWarn || !taskName.trim() || totalBots <= 0 }}
      width={620}
      destroyOnHidden
    >
      <Form layout="vertical">
        <Form.Item label="任务名" required>
          <Input value={taskName} onChange={(e) => setTaskName(e.target.value)} placeholder="例：200v200 v1.2" />
        </Form.Item>
        <Form.Item label="集群总机器人数" required extra={`集群剩余容量约 ${availableBots}`}>
          <InputNumber
            min={1}
            max={100000}
            value={totalBots}
            onChange={(v) => setTotalBots(typeof v === 'number' ? v : 0)}
            style={{ width: '100%' }}
          />
        </Form.Item>
        <Form.Item label="Auth 地址" required>
          <Input
            value={robotConfig.authAddr}
            onChange={(e) => setRobotConfig({ authAddr: e.target.value })}
            placeholder="例：auth.example.com:8001"
          />
        </Form.Item>
        <Form.Item label="并发（每秒新建机器人数）">
          <InputNumber
            min={1}
            max={1000}
            value={robotConfig.concurrency}
            onChange={(v) => setRobotConfig({ concurrency: typeof v === 'number' ? v : 1 })}
            style={{ width: '100%' }}
          />
        </Form.Item>
        <Form.Item label="超时秒数">
          <InputNumber
            min={1}
            max={600}
            value={robotConfig.timeoutSec}
            onChange={(v) => setRobotConfig({ timeoutSec: typeof v === 'number' ? v : 30 })}
            style={{ width: '100%' }}
          />
        </Form.Item>
        <Form.Item label="自动停止时间（可选）">
          <DatePicker
            showTime
            value={deadline ? dayjs(deadline) : null}
            onChange={(d: Dayjs | null) => setDeadline(d ? d.toISOString() : null)}
            style={{ width: '100%' }}
          />
        </Form.Item>
      </Form>

      <Descriptions size="small" column={2} bordered style={{ marginTop: 8 }}>
        <Descriptions.Item label="Proto 文件">
          {protos.length === 0 ? <Tag color="default">无</Tag> : <Tag color="blue">{protos.length} 个</Tag>}
        </Descriptions.Item>
        <Descriptions.Item label="Lua 脚本">
          {scripts.length === 0 ? <Tag color="default">无</Tag> : <Tag color="purple">{scripts.length} 个</Tag>}
        </Descriptions.Item>
      </Descriptions>

      {(protos.length === 0 || scripts.length === 0) && (
        <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginTop: 8 }}>
          未上传的资源由 Admin 兜底默认值；如需自定义请到「资源管理」上传。
        </Typography.Text>
      )}

      {capacityWarn && (
        <Alert
          type="warning"
          showIcon
          style={{ marginTop: 12 }}
          message={`集群剩余容量 ${availableBots}，本次申请 ${totalBots}`}
          description="请减少机器人数，或先停止其他任务释放节点。"
        />
      )}
    </Modal>
  );
}
