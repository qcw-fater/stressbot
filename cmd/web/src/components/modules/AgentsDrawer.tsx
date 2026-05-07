/**
 * Agents 管理 Drawer：列出 Agent，提供查看详情、删除（仅离线）、批量停止。
 *
 * 数据源：runtimeStore.agents（已被 HomeShell 轮询填充），10s 内必新鲜，无需独立刷新。
 * 仅"刷新"按钮可手动 refetch。
 *
 * 升级流程已废弃：版本更新通过手动重启 Agent 完成。本组件保留的是日常运维操作：
 *   - 删除 offline Agent（清理注册表）
 *   - 一键停止当前 active 任务（等价于"批量停止所有 Agent"）
 *
 * 由于系统是单 active 任务模型，"停止所有 Agent" ≡ "停止当前任务"；
 * 没有 active 任务时按钮 disabled。
 */

import { App, Button, Drawer, Empty, Space, Table, Tag, Tooltip } from 'antd';
import { DeleteOutlined, ReloadOutlined, StopOutlined } from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';
import { useState } from 'react';
import dayjs from 'dayjs';
import { useShallow } from 'zustand/react/shallow';
import { agentsApi, showApiError, stopTask, useRuntimeStore } from '@/services';
import type { AgentBrief } from '@/types/api';

export interface AgentsDrawerProps {
  open: boolean;
  onClose: () => void;
}

export function AgentsDrawer({ open, onClose }: AgentsDrawerProps) {
  const { agents, activeTask, setAgents } = useRuntimeStore(
    useShallow((s) => ({ agents: s.agents, activeTask: s.activeTask, setAgents: s.setAgents })),
  );
  const { message, modal } = App.useApp();
  const [loading, setLoading] = useState(false);
  const [stopping, setStopping] = useState(false);

  const refresh = async () => {
    setLoading(true);
    try {
      const resp = await agentsApi.listAgents();
      setAgents(resp.items);
    } catch (err) {
      showApiError(err);
    } finally {
      setLoading(false);
    }
  };

  // 没有 active 任务时禁用：单任务模型下没有任务可停。
  // active 任务存在时（无论是不是本端发起）都允许停止——服务端是唯一权威，会校验。
  const canStopAll = !!activeTask &&
    (activeTask.state === 'starting' || activeTask.state === 'running' || activeTask.state === 'stopping');

  const onStopAll = () => {
    if (!activeTask) return;
    modal.confirm({
      title: '停止所有 Agent？',
      content: (
        <div>
          <p>
            将向所有正在执行 <code>{activeTask.name}</code> 的 Agent 下发停止指令；
            等待全部进入 <code>stopped</code> 后任务结束。
          </p>
          <p style={{ marginBottom: 0, color: 'var(--text-secondary)', fontSize: 12 }}>
            此操作等价于在顶部点击"停止任务"。
          </p>
        </div>
      ),
      okType: 'danger',
      okText: '全部停止',
      onOk: async () => {
        setStopping(true);
        try {
          await stopTask(activeTask.id);
          message.success('已下发停止指令，等待 Agent 收尾');
          refresh();
        } catch (err) {
          showApiError(err);
        } finally {
          setStopping(false);
        }
      },
    });
  };

  const onDelete = (a: AgentBrief) => {
    if (a.status !== 'offline') {
      message.warning('只能删除离线 Agent');
      return;
    }
    modal.confirm({
      title: `从注册表删除 ${a.name}？`,
      content: `agentId: ${a.agentId}`,
      okButtonProps: { danger: true },
      onOk: async () => {
        try {
          await agentsApi.deleteAgent(a.agentId);
          message.success('已删除');
          refresh();
        } catch (err) {
          showApiError(err);
        }
      },
    });
  };

  const columns: ColumnsType<AgentBrief> = [
    {
      title: '名称',
      key: 'name',
      render: (_, a) => (
        <div>
          <div style={{ fontWeight: 500 }}>{a.name}</div>
          <div style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>
            <code>{a.agentId.slice(0, 12)}</code> · v{a.appVersion}
          </div>
        </div>
      ),
    },
    {
      title: '状态',
      dataIndex: 'status',
      key: 'status',
      width: 100,
      render: (v: string) => {
        const colorMap: Record<string, string> = {
          idle: 'default',
          busy: 'processing',
          unhealthy: 'error',
          offline: 'default',
        };
        return <Tag color={colorMap[v] ?? 'default'}>{v}</Tag>;
      },
    },
    {
      title: '当前任务',
      key: 'task',
      width: 110,
      render: (_, a) =>
        a.currentTaskId ? (
          <Tooltip title={a.currentTaskId}>
            <code>{a.currentTaskId.slice(0, 8)}</code>
          </Tooltip>
        ) : (
          <span style={{ color: 'var(--text-tertiary)' }}>—</span>
        ),
    },
    {
      title: '机器人',
      key: 'bots',
      width: 100,
      render: (_, a) => `${a.currentBots} / ${a.maxBots}`,
    },
    {
      title: 'CPU%',
      dataIndex: 'cpuPercent',
      key: 'cpuPercent',
      width: 80,
      render: (v: number | undefined) =>
        v === undefined ? '—' : (
          <span style={{ color: v > 80 ? '#f5222d' : v > 60 ? '#faad14' : undefined }}>
            {v.toFixed(1)}%
          </span>
        ),
    },
    {
      title: 'MEM%',
      dataIndex: 'memPercent',
      key: 'memPercent',
      width: 80,
      render: (v: number | undefined) => (v === undefined ? '—' : `${v.toFixed(1)}%`),
    },
    {
      title: '主机',
      key: 'host',
      width: 200,
      render: (_, a) => (
        <Tooltip
          title={
            <div style={{ fontSize: 11 }}>
              <div>OS: {a.staticInfo.os}/{a.staticInfo.arch}</div>
              <div>CPU: {a.staticInfo.numCpu} cores · MEM: {(a.staticInfo.memTotalMB / 1024).toFixed(1)} GB</div>
              <div>Go: {a.staticInfo.goVersion}</div>
              <div>Kernel: {a.staticInfo.kernelVer}</div>
              <div>启动: {dayjs(a.staticInfo.startedAt).format('MM-DD HH:mm')}</div>
            </div>
          }
        >
          <span style={{ fontFamily: 'monospace', fontSize: 11 }}>
            {a.staticInfo.hostname} · {a.address}
          </span>
        </Tooltip>
      ),
    },
    {
      title: '心跳',
      dataIndex: 'lastHeartbeatAt',
      key: 'lastHeartbeatAt',
      width: 80,
      render: (v: string) => {
        const ago = (Date.now() - new Date(v).getTime()) / 1000;
        return ago > 60 ? <Tag color="warning">{Math.floor(ago)}s 前</Tag> : <span>{Math.floor(ago)}s</span>;
      },
    },
    {
      title: '操作',
      key: 'actions',
      width: 80,
      fixed: 'right',
      render: (_, a) => (
        <Tooltip title={a.status === 'offline' ? '从注册表删除' : '只能删除离线 Agent'}>
          <Button
            type="text"
            size="small"
            danger
            disabled={a.status !== 'offline'}
            icon={<DeleteOutlined />}
            onClick={(e) => {
              e.stopPropagation();
              onDelete(a);
            }}
          />
        </Tooltip>
      ),
    },
  ];

  return (
    <Drawer
      title={
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <span>压测节点 Agents</span>
          <Space>
            <Tooltip title={canStopAll ? '一键停止当前正在执行的任务' : '当前没有运行中的任务'}>
              <Button
                danger
                size="small"
                icon={<StopOutlined />}
                disabled={!canStopAll}
                loading={stopping}
                onClick={onStopAll}
              >
                全部停止
              </Button>
            </Tooltip>
            <Button size="small" icon={<ReloadOutlined />} loading={loading} onClick={refresh}>
              刷新
            </Button>
          </Space>
        </div>
      }
      open={open}
      onClose={onClose}
      width={1100}
      destroyOnHidden
    >
      {(agents ?? []).length === 0 ? (
        <Empty description="暂无 Agent，请先在节点上启动 stressbot-agent" />
      ) : (
        <Table<AgentBrief>
          rowKey="agentId"
          size="small"
          dataSource={agents ?? []}
          columns={columns}
          pagination={false}
          scroll={{ x: 1100 }}
        />
      )}
    </Drawer>
  );
}
