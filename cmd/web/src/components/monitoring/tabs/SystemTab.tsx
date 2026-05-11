/**
 * 集群系统资源详情：Circle 仪表盘 + 统计条。
 *
 * 1. 仪表盘区域：CPU / MEM 两个大 Circle + 集群概要信息
 * 2. 统计条：协程 / 线程 / FDs / 机器人
 */

import { Empty, Progress, Statistic, Tooltip } from 'antd';
import { useShallow } from 'zustand/react/shallow';
import { useRuntimeStore } from '@/services';
import './SystemTab.css';

function fmtBandwidth(kbps: number): string {
  const mbps = kbps / 1024;
  if (mbps < 1) return `${kbps.toFixed(0)} KB/s`;
  return `${mbps.toFixed(2)} MB/s`;
}

function gaugeColor(v: number): string {
  if (v > 80) return 'var(--color-error)';
  if (v > 60) return 'var(--color-warning)';
  return 'var(--color-blue)';
}

export function SystemTab() {
  const { latestSystem, agents } = useRuntimeStore(
    useShallow((s) => ({ latestSystem: s.latestSystem, agents: s.agents })),
  );

  if (!latestSystem) {
    return <Empty description="暂无系统资源数据" />;
  }

  const usedMemMB = latestSystem.usedMemMB ?? 0;
  const totalMemMB = latestSystem.totalMemMB ?? 0;
  const memPercent = totalMemMB > 0 ? (usedMemMB / totalMemMB) * 100 : 0;
  const memUsedGB = usedMemMB / 1024;
  const memTotalGB = totalMemMB / 1024;
  const sendKBps = latestSystem.totalNetSendKBps ?? 0;
  const recvKBps = latestSystem.totalNetRecvKBps ?? 0;
  const safeAgents = agents ?? [];
  const totalCurrentBots = safeAgents.reduce((s, a) => s + (a.currentBots ?? 0), 0);
  const totalMaxBots = safeAgents.reduce((s, a) => s + (a.maxBots ?? 0), 0);

  return (
    <div className="system-tab">
      {/* ── 仪表盘区域 ── */}
      <div className="system-tab__dashboard">
        <div className="system-tab__gauges">
          <div className="system-tab__gauge">
            <span className="system-tab__gauge-title">CPU</span>
            <Progress
              type="circle"
              percent={latestSystem.avgCpuPercent}
              size={140}
              strokeWidth={10}
              strokeColor={gaugeColor(latestSystem.avgCpuPercent)}
              format={(p) => (
                <div className="system-tab__gauge-center">
                  <span className="system-tab__gauge-value">{(p ?? 0).toFixed(1)}%</span>
                  <span className="system-tab__gauge-sub">平均</span>
                </div>
              )}
            />
            <div className="system-tab__gauge-extra">
              最高 {latestSystem.maxCpuPercent.toFixed(1)}%
              {latestSystem.hotAgentName && (
                <span> · {latestSystem.hotAgentName}</span>
              )}
            </div>
          </div>
          <div className="system-tab__gauge">
            <span className="system-tab__gauge-title">内存</span>
            <Progress
              type="circle"
              percent={memPercent}
              size={140}
              strokeWidth={10}
              strokeColor={gaugeColor(memPercent)}
              format={() => (
                <div className="system-tab__gauge-center">
                  <span className="system-tab__gauge-value">{memPercent.toFixed(1)}%</span>
                  <span className="system-tab__gauge-sub">{memUsedGB.toFixed(1)} / {memTotalGB.toFixed(1)} GB</span>
                </div>
              )}
            />
            <div className="system-tab__gauge-extra">
              可用 {(memTotalGB - memUsedGB).toFixed(1)} GB
            </div>
          </div>
        </div>

        <div className="system-tab__summary">
          <div className="system-tab__summary-row">
            <span className="system-tab__summary-label">节点</span>
            <span className="system-tab__summary-value">
              在线 {latestSystem.onlineCount} / 共 {latestSystem.agentCount}
            </span>
          </div>
          <div className="system-tab__summary-row">
            <span className="system-tab__summary-label">↑ 发送</span>
            <span className="system-tab__summary-value">{fmtBandwidth(sendKBps)}</span>
          </div>
          <div className="system-tab__summary-row">
            <span className="system-tab__summary-label">↓ 接收</span>
            <span className="system-tab__summary-value">{fmtBandwidth(recvKBps)}</span>
          </div>
          <div className="system-tab__summary-row">
            <span className="system-tab__summary-label">机器人</span>
            <span className="system-tab__summary-value">{totalCurrentBots} / {totalMaxBots}</span>
          </div>
        </div>
      </div>

      {/* ── 进程统计条 ── */}
      <div className="system-tab__process-row">
        <Tooltip title="所有节点的协程总数">
          <div className="system-tab__process-stat">
            <Statistic title="协程" value={latestSystem.totalGoroutines ?? 0} valueStyle={{ fontSize: 16, fontWeight: 600 }} />
          </div>
        </Tooltip>
        <div className="system-tab__divider-sm" />
        <Tooltip title="所有节点的系统线程总数">
          <div className="system-tab__process-stat">
            <Statistic title="线程" value={latestSystem.totalThreads ?? 0} valueStyle={{ fontSize: 16, fontWeight: 600 }} />
          </div>
        </Tooltip>
        <div className="system-tab__divider-sm" />
        <Tooltip title="文件描述符数量">
          <div className="system-tab__process-stat">
            <Statistic title="FDs" value={latestSystem.totalFds ?? 0} valueStyle={{ fontSize: 16, fontWeight: 600 }} />
          </div>
        </Tooltip>
      </div>
    </div>
  );
}
