/**
 * Agents 管理 Drawer：列出 Agent，提供删除（仅离线）、查看详情、单点升级。
 *
 * 数据源：runtimeStore.agents（已被 HomeShell 轮询填充），10s 内必新鲜，无需独立刷新。
 * 仅"刷新"按钮可手动 refetch。
 */

import { App, Button, Drawer, Empty, List, Modal, Space, Table, Tag, Tooltip } from 'antd';
import { CloudUploadOutlined, DeleteOutlined, ReloadOutlined } from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';
import { useState } from 'react';
import dayjs from 'dayjs';
import { agentsApi, binariesApi, showApiError, useRuntimeStore } from '@/services';
import { useShallow } from 'zustand/react/shallow';
import type { AgentBrief, BinaryMeta } from '@/types/api';

export interface AgentsDrawerProps {
  open: boolean;
  onClose: () => void;
  /** 主面板想直接打开 BinariesDrawer 时调用（升级二进制管理位于该 Drawer） */
  onOpenBinaries?: () => void;
}

export function AgentsDrawer({ open, onClose, onOpenBinaries }: AgentsDrawerProps) {
  const { agents, setAgents } = useRuntimeStore(
    useShallow((s) => ({ agents: s.agents, setAgents: s.setAgents })),
  );
  const { message, modal } = App.useApp();
  const [loading, setLoading] = useState(false);
  const [binaries, setBinaries] = useState<BinaryMeta[]>([]);
  const [upgradeAgent, setUpgradeAgent] = useState<AgentBrief | null>(null);

  const refresh = async () => {
    setLoading(true);
    try {
      const [resp, binsResp] = await Promise.all([
        agentsApi.listAgents(),
        binariesApi.listBinaries(),
      ]);
      setAgents(resp.items);
      setBinaries(binsResp.items);
    } catch (err) {
      showApiError(err);
    } finally {
      setLoading(false);
    }
  };

  const onUpgrade = (a: AgentBrief) => {
    if (binaries.length === 0) {
      modal.info({
        title: '尚无可用二进制',
        content: '请先在"二进制管理"中上传 stressbot-agent 可执行文件',
      });
      return;
    }
    setUpgradeAgent(a);
  };

  const matchedBinaries = upgradeAgent
    ? binaries.filter(
        (b) => b.os === upgradeAgent.staticInfo.os && b.arch === upgradeAgent.staticInfo.arch,
      )
    : [];

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
          upgrading: 'warning',
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
      width: 100,
      fixed: 'right',
      render: (_, a) => (
        <Space size={2}>
          <Tooltip title="升级">
            <Button
              type="text"
              size="small"
              icon={<CloudUploadOutlined />}
              onClick={(e) => {
                e.stopPropagation();
                onUpgrade(a);
              }}
            />
          </Tooltip>
          <Tooltip title={a.status === 'offline' ? '删除' : '只能删除离线 Agent'}>
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
        </Space>
      ),
    },
  ];

  return (
    <Drawer
      title={
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <span>压测节点 Agents</span>
          <Space>
            {onOpenBinaries && (
              <Button size="small" onClick={onOpenBinaries}>
                二进制管理
              </Button>
            )}
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

      <Modal
        title={upgradeAgent ? `升级 ${upgradeAgent.name}` : '升级'}
        open={upgradeAgent !== null}
        onCancel={() => setUpgradeAgent(null)}
        footer={null}
        destroyOnHidden
      >
        {matchedBinaries.length === 0 ? (
          <Empty
            description={`没有匹配 ${upgradeAgent?.staticInfo.os}/${upgradeAgent?.staticInfo.arch} 的二进制版本`}
          />
        ) : (
          <List
            size="small"
            dataSource={matchedBinaries.slice(0, 10)}
            renderItem={(b) => (
              <List.Item
                actions={[
                  <Button
                    key="upgrade"
                    type="primary"
                    size="small"
                    onClick={async () => {
                      if (!upgradeAgent) return;
                      try {
                        await agentsApi.upgradeAgent(upgradeAgent.agentId, { version: b.version });
                        message.success(`升级请求已下发：${b.version}`);
                        setUpgradeAgent(null);
                        refresh();
                      } catch (err) {
                        showApiError(err);
                      }
                    }}
                  >
                    选用
                  </Button>,
                ]}
              >
                <List.Item.Meta
                  title={<code>{b.version}</code>}
                  description={`${b.filename} · ${(b.sizeBytes / 1024 / 1024).toFixed(1)} MB · ${dayjs(b.uploadedAt).format('MM-DD HH:mm')}`}
                />
              </List.Item>
            )}
          />
        )}
      </Modal>
    </Drawer>
  );
}
