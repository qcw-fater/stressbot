/**
 * 压测节点面板：FloatingWindow + 指标卡片。
 *
 * 每个节点一张全宽卡片（header + Circle 仪表盘 + 信息列含操作按钮），
 * 纵向堆叠。"全部关闭"发送 shutdown 信号关闭节点进程。
 */

import { App as AntApp, Button, Empty, Progress, Space, Tooltip } from 'antd';
import { DeleteOutlined, ReloadOutlined, StopOutlined } from '@ant-design/icons';
import { useEffect, useRef, useState } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { agentsApi, computeWeightedMetrics, metricsApi, showApiError, useRuntimeStore } from '@/services';
import { FloatingWindow } from '@/components/FlowEditor/panels/FloatingWindow';
import { ApdexCell } from '@/components/monitoring/shared/ApdexCell';
import type { AgentBrief, AgentStatus, PerAgentMetricsItem } from '@/types/api';
import './AgentsPanel.css';

const STATUS_ORDER: Record<AgentStatus, number> = { unhealthy: 0, busy: 1, idle: 2, offline: 3 };

const STATUS_DOT: Record<AgentStatus, string> = {
  idle: 'var(--color-success)',
  busy: 'var(--color-blue)',
  unhealthy: 'var(--color-error)',
  offline: 'var(--text-tertiary)',
};

const STATUS_LABEL: Record<AgentStatus, string> = {
  idle: '空闲',
  busy: '执行中',
  unhealthy: '异常',
  offline: '离线',
};

const STATUS_BADGE_BG: Record<AgentStatus, string> = {
  idle: 'var(--status-idle-bg)',
  busy: 'var(--status-busy-bg)',
  unhealthy: 'var(--status-unhealthy-bg)',
  offline: 'var(--badge-bg)',
};

const STATUS_BADGE_COLOR: Record<AgentStatus, string> = {
  idle: 'var(--color-success)',
  busy: 'var(--color-blue)',
  unhealthy: 'var(--color-error)',
  offline: 'var(--text-tertiary)',
};

function gaugeColor(v?: number): string {
  if (v == null) return 'var(--color-blue)';
  if (v > 80) return 'var(--color-error)';
  if (v > 60) return 'var(--color-warning)';
  return 'var(--color-blue)';
}

export interface AgentsPanelProps {
  open: boolean;
  onClose: () => void;
}

export function AgentsPanel({ open, onClose }: AgentsPanelProps) {
  const { agents, setAgents } = useRuntimeStore(
    useShallow((s) => ({ agents: s.agents, setAgents: s.setAgents })),
  );
  const { message, modal } = AntApp.useApp();
  const [loading, setLoading] = useState(false);
  const [shuttingDown, setShuttingDown] = useState(false);
  const [perAgentMetrics, setPerAgentMetrics] = useState<PerAgentMetricsItem[]>([]);

  const refresh = async () => {
    setLoading(true);
    try {
      const [agentResp, perAgentResp] = await Promise.all([
        agentsApi.listAgents(),
        metricsApi.getPerAgentMetrics().catch(() => ({ items: [] as PerAgentMetricsItem[] })),
      ]);
      setAgents(agentResp.items);
      setPerAgentMetrics(perAgentResp.items);
    } catch (err) {
      showApiError(err);
    } finally {
      setLoading(false);
    }
  };

  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;

  useEffect(() => {
    if (!open) return;
    refreshRef.current();
    const timer = setInterval(() => refreshRef.current(), 5000);
    return () => clearInterval(timer);
  }, [open]);

  const onShutdownAll = () => {
    modal.confirm({
      title: '关闭所有在线节点？',
      content: '将向所有在线节点发送关闭信号，节点程序会退出。请确保已停止运行中的任务。',
      okType: 'danger',
      okText: '全部关闭',
      onOk: async () => {
        setShuttingDown(true);
        try {
          const result = await agentsApi.shutdownAllAgents();
          const succeeded = result.succeeded ?? [];
          const failed = result.failed ?? [];
          if (failed.length > 0) {
            message.warning(`${succeeded.length} 个节点已关闭，${failed.length} 个失败`);
          } else {
            message.success(`${succeeded.length} 个节点已发送关闭信号`);
          }
          refresh();
        } catch (err) {
          showApiError(err);
        } finally {
          setShuttingDown(false);
        }
      },
    });
  };

  const onShutdownOne = (a: AgentBrief) => {
    modal.confirm({
      title: `关闭节点 ${a.name}？`,
      content: '将向该节点发送关闭信号，节点程序会退出。',
      okType: 'danger',
      okText: '关闭',
      onOk: async () => {
        try {
          await agentsApi.shutdownAgent(a.agentId);
          message.success('已发送关闭信号');
          refresh();
        } catch (err) {
          showApiError(err);
        }
      },
    });
  };

  const onDelete = (a: AgentBrief) => {
    modal.confirm({
      title: `从注册表删除 ${a.name}？`,
      content: `标识: ${a.agentId.slice(0, 12)}`,
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

  const safeAgents = agents ?? [];
  const onlineCount = safeAgents.filter((a) => a.status !== 'offline').length;
  const hasOnline = onlineCount > 0;
  const perAgentMap = new Map(perAgentMetrics.map((it) => [it.agentId, it]));

  const sorted = [...safeAgents].sort((a, b) => {
    const oa = STATUS_ORDER[a.status] ?? 9;
    const ob = STATUS_ORDER[b.status] ?? 9;
    if (oa !== ob) return oa - ob;
    return (b.cpuPercent ?? 0) - (a.cpuPercent ?? 0);
  });

  return (
    <FloatingWindow
      windowId="agents"
      title="压测节点"
      defaultSize={{ width: 860, height: 560 }}
      minSize={{ width: 640, height: 400 }}
      open={open}
      onClose={onClose}
      extra={
        <Space size={8}>
          <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
            在线 {onlineCount} / 总 {safeAgents.length}
          </span>
          <Button size="small" icon={<ReloadOutlined />} loading={loading} onClick={refresh}>
            刷新
          </Button>
          <Tooltip title={hasOnline ? '关闭所有在线节点' : '没有在线节点'}>
            <Button
              danger
              size="small"
              icon={<StopOutlined />}
              disabled={!hasOnline}
              loading={shuttingDown}
              onClick={onShutdownAll}
            >
              全部关闭
            </Button>
          </Tooltip>
        </Space>
      }
    >
      {sorted.length === 0 ? (
        <Empty description="暂无节点，请先在目标机器上启动节点程序" style={{ marginTop: 80 }} />
      ) : (
        <div className="agents-card-stack">
          {sorted.map((a) => (
            <AgentMetricCard
              key={a.agentId}
              agent={a}
              stressMetrics={perAgentMap.get(a.agentId)}
              onShutdown={onShutdownOne}
              onDelete={onDelete}
            />
          ))}
        </div>
      )}
    </FloatingWindow>
  );
}

function AgentMetricCard({
  agent,
  stressMetrics,
  onShutdown,
  onDelete,
}: {
  agent: AgentBrief;
  stressMetrics?: PerAgentMetricsItem;
  onShutdown: (a: AgentBrief) => void;
  onDelete: (a: AgentBrief) => void;
}) {
  const { message } = AntApp.useApp();
  const isOffline = agent.status === 'offline';
  const cpu = agent.cpuPercent;
  const mem = agent.memPercent;
  const uptime = formatUptime(agent.staticInfo.startedAt);

  const stressActions = stressMetrics?.snapshot?.actions ?? [];
  const { totalSamples: stressSamples, apdex: stressApdex, successRate: stressSuccess } = computeWeightedMetrics(stressActions);
  const stressTotalActions = stressMetrics?.snapshot?.totalActions ?? 0;
  const hasStressData = stressSamples > 0;

  const successColor = !hasStressData ? 'var(--text-tertiary)'
    : stressSuccess >= 0.95 ? 'var(--color-success)'
    : stressSuccess >= 0.8 ? 'var(--color-warning)' : 'var(--color-error)';

  const handleDelete = () => {
    if (!isOffline) {
      message.warning('只能删除离线节点');
      return;
    }
    onDelete(agent);
  };

  const staticTooltip = (
    <div style={{ fontSize: 11, lineHeight: 1.8 }}>
      <div>系统: {agent.staticInfo.os}/{agent.staticInfo.arch}</div>
      <div>核心: {agent.staticInfo.numCpu} 核 · 内存: {(agent.staticInfo.memTotalMB / 1024).toFixed(1)} GB</div>
      <div>内核: {agent.staticInfo.kernelVer}</div>
      <div>运行时: {agent.staticInfo.goVersion}</div>
    </div>
  );

  return (
    <div
      className={`agent-card${isOffline ? ' agent-card--offline' : ''}${agent.status === 'unhealthy' ? ' agent-card--unhealthy' : ''}`}
    >
      {/* Header */}
      <div className="agent-card__header">
        <div className="agent-card__header-left">
          <span
            className="agent-card__dot"
            style={{
              background: STATUS_DOT[agent.status],
              filter: isOffline ? 'none' : `drop-shadow(${STATUS_DOT[agent.status]} 0 0 4px)`,
            }}
          />
          <span className="agent-card__name">{agent.name}</span>
          <span className="agent-card__version">v{agent.appVersion}</span>
          <span
            className="agent-card__status-badge"
            style={{
              background: STATUS_BADGE_BG[agent.status],
              color: STATUS_BADGE_COLOR[agent.status],
            }}
          >
            {STATUS_LABEL[agent.status]}
          </span>
        </div>
        <div className="agent-card__header-right">
          <Tooltip title={staticTooltip}>
            <span className="agent-card__address">
              {agent.staticInfo.hostname}:{agent.address.split(':').pop()}
            </span>
          </Tooltip>
        </div>
      </div>

      {/* Body: Circle gauges + Info (no separate footer) */}
      <div className="agent-card__body">
        <div className="agent-card__gauges">
          <div className="agent-card__gauge">
            <span className="agent-card__gauge-label">CPU</span>
            <Progress
              type="circle"
              percent={cpu ?? 0}
              size={100}
              strokeColor={gaugeColor(cpu)}
              format={(p) => (
                <span style={{ fontSize: 16, fontWeight: 700, color: 'var(--text-primary)' }}>
                  {p !== undefined && cpu != null ? `${p.toFixed(1)}` : '—'}
                </span>
              )}
            />
          </div>
          <div className="agent-card__gauge">
            <span className="agent-card__gauge-label">MEM</span>
            <Progress
              type="circle"
              percent={mem ?? 0}
              size={100}
              strokeColor={gaugeColor(mem)}
              format={(p) => (
                <span style={{ fontSize: 16, fontWeight: 700, color: 'var(--text-primary)' }}>
                  {p !== undefined && mem != null ? `${p.toFixed(1)}` : '—'}
                </span>
              )}
            />
          </div>
        </div>

        <div className="agent-card__info-cols">
          <div className="agent-card__info-col">
            <div className="agent-card__info-row">
              <span className="agent-card__info-label">机器人</span>
              <span className="agent-card__info-value">
                {agent.currentBots} / {agent.maxBots}
              </span>
            </div>
            <div className="agent-card__info-row">
              <span className="agent-card__info-label">任务</span>
              <span className="agent-card__info-value">
                {agent.currentTaskId ? (
                  <Tooltip title={agent.currentTaskId}>
                    <code>{agent.currentTaskId.slice(0, 8)}</code>
                  </Tooltip>
                ) : (
                  '—'
                )}
              </span>
            </div>
            <div className="agent-card__info-row">
              <span className="agent-card__info-label">协程</span>
              <span className="agent-card__info-value">
                {agent.numGoroutine ?? '—'}
              </span>
            </div>
            <div className="agent-card__info-row">
              <span className="agent-card__info-label">累计动作</span>
              <span className="agent-card__info-value">
                {hasStressData ? stressTotalActions.toLocaleString() : '—'}
              </span>
            </div>
            <div className="agent-card__info-row">
              <span className="agent-card__info-label">成功率</span>
              <span className="agent-card__info-value" style={{ color: successColor }}>
                {hasStressData ? `${(stressSuccess * 100).toFixed(1)}%` : '—'}
              </span>
            </div>
            <div className="agent-card__info-row">
              <span className="agent-card__info-label">Apdex</span>
              <span className="agent-card__info-value">
                {hasStressData ? <ApdexCell value={stressApdex} /> : '—'}
              </span>
            </div>
          </div>
          <div className="agent-card__info-col">
            <div className="agent-card__info-row">
              <span className="agent-card__info-label">运行时长</span>
              <span className="agent-card__info-value">{uptime}</span>
            </div>
            <div className="agent-card__info-row">
              <span className="agent-card__info-label">心跳</span>
              <span className="agent-card__info-value">{formatHeartbeat(agent.lastHeartbeatAt)}</span>
            </div>
            <div className="agent-card__info-row">
              <span className="agent-card__info-label">系统</span>
              <span className="agent-card__info-value">
                {agent.staticInfo.os}/{agent.staticInfo.arch} · {agent.staticInfo.numCpu}核
              </span>
            </div>
            <div className="agent-card__info-row agent-card__info-row--actions">
              {!isOffline && (
                <Tooltip title="关闭此节点进程">
                  <Button type="text" size="small" danger icon={<StopOutlined />} onClick={() => onShutdown(agent)}>
                    关闭
                  </Button>
                </Tooltip>
              )}
              <Tooltip title={isOffline ? '从注册表删除' : '只能删除离线节点'}>
                <Button
                  type="text"
                  size="small"
                  danger={isOffline}
                  disabled={!isOffline}
                  icon={<DeleteOutlined />}
                  onClick={handleDelete}
                />
              </Tooltip>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

function formatHeartbeat(iso: string): string {
  const ago = (Date.now() - new Date(iso).getTime()) / 1000;
  if (ago < 60) return `${Math.floor(ago)} 秒前`;
  if (ago < 3600) return `${Math.floor(ago / 60)} 分钟前`;
  return `${Math.floor(ago / 3600)} 小时前`;
}

function formatUptime(iso: string): string {
  const secs = (Date.now() - new Date(iso).getTime()) / 1000;
  if (secs < 60) return `${Math.floor(secs)} 秒`;
  if (secs < 3600) return `${Math.floor(secs / 60)} 分钟`;
  if (secs < 86400) return `${Math.floor(secs / 3600)} 小时 ${Math.floor((secs % 3600) / 60)} 分`;
  return `${Math.floor(secs / 86400)} 天 ${Math.floor((secs % 86400) / 3600)} 小时`;
}
