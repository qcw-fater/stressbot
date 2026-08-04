import { Empty, Progress, Statistic, Tooltip } from 'antd';
import { useShallow } from 'zustand/react/shallow';
import { useRuntimeStore } from '@/services';
import {
  fmtBandwidthBytesPerSec,
  fmtByteSize,
  fmtCompactNumber,
  fmtPercentValue,
} from '../shared/formats';
import './SystemTab.css';

function gaugeColor(value: number | null): string {
  if (value == null) return 'var(--border-color)';
  if (value > 80) return 'var(--color-error)';
  if (value > 60) return 'var(--color-warning)';
  return 'var(--color-blue)';
}

export function SystemTab() {
  const { latestSystem, agents } = useRuntimeStore(
    useShallow((s) => ({ latestSystem: s.latestSystem, agents: s.agents })),
  );

  if (!latestSystem) {
    return <Empty description="暂无系统资源数据" />;
  }

  const hostCPU = latestSystem.avgHostCpuPercent;
  const hostMemory = latestSystem.avgHostMemPercent;
  const totalCurrentBots = agents.reduce((sum, agent) => sum + (agent.currentBots ?? 0), 0);
  const totalMaxBots = agents.reduce((sum, agent) => sum + (agent.maxBots ?? 0), 0);

  return (
    <div className="system-tab">
      <div className="system-tab__dashboard">
        <div className="system-tab__gauges">
          <div className="system-tab__gauge">
            <span className="system-tab__gauge-title">主机 CPU</span>
            <Progress
              type="circle"
              percent={hostCPU ?? 0}
              size={140}
              strokeColor={gaugeColor(hostCPU)}
              format={() => (
                <div className="system-tab__gauge-center">
                  <span className="system-tab__gauge-value">{fmtPercentValue(hostCPU)}</span>
                  <span className="system-tab__gauge-sub">有效节点 {latestSystem.hostCpuReportingAgents}</span>
                </div>
              )}
            />
            <div className="system-tab__gauge-extra">
              当前最高 {fmtPercentValue(latestSystem.maxHostCpuPercent)}
              {latestSystem.hotHostCpuAgentName && <span> · {latestSystem.hotHostCpuAgentName}</span>}
            </div>
          </div>

          <div className="system-tab__gauge">
            <span className="system-tab__gauge-title">主机内存</span>
            <Progress
              type="circle"
              percent={hostMemory ?? 0}
              size={140}
              strokeColor={gaugeColor(hostMemory)}
              format={() => (
                <div className="system-tab__gauge-center">
                  <span className="system-tab__gauge-value">{fmtPercentValue(hostMemory)}</span>
                  <span className="system-tab__gauge-sub">
                    {fmtByteSize(latestSystem.usedHostMemBytes)} / {fmtByteSize(latestSystem.totalHostMemBytes)}
                  </span>
                </div>
              )}
            />
            <div className="system-tab__gauge-extra">
              当前最高 {fmtPercentValue(latestSystem.maxHostMemPercent)}
              {latestSystem.hotHostMemAgentName && <span> · {latestSystem.hotHostMemAgentName}</span>}
            </div>
          </div>
        </div>

        <div className="system-tab__summary">
          <div className="system-tab__summary-row">
            <span className="system-tab__summary-label">资源采样</span>
            <span className="system-tab__summary-value">
              {latestSystem.reportingAgents} / {latestSystem.agentCount}
            </span>
          </div>
          <div className="system-tab__summary-row">
            <span className="system-tab__summary-label">节点</span>
            <span className="system-tab__summary-value">
              在线 {latestSystem.onlineCount} · 异常 {latestSystem.unhealthyCount} · 离线 {latestSystem.offlineCount}
            </span>
          </div>
          <div className="system-tab__summary-row">
            <span className="system-tab__summary-label">主机网卡发送</span>
            <span className="system-tab__summary-value">
              {fmtBandwidthBytesPerSec(latestSystem.totalHostNetSendBytesPerSec)}
            </span>
          </div>
          <div className="system-tab__summary-row">
            <span className="system-tab__summary-label">主机网卡接收</span>
            <span className="system-tab__summary-value">
              {fmtBandwidthBytesPerSec(latestSystem.totalHostNetRecvBytesPerSec)}
            </span>
          </div>
          <div className="system-tab__summary-row">
            <span className="system-tab__summary-label">集群容量</span>
            <span className="system-tab__summary-value">{totalCurrentBots} / {totalMaxBots}</span>
          </div>
        </div>
      </div>

      <div className="system-tab__process-row">
        <Tooltip title={`当前最高 ${latestSystem.hotProcessCpuAgentName || '—'} · ${fmtPercentValue(latestSystem.maxProcessCpuPercent)}`}>
          <div className="system-tab__process-stat">
            <Statistic title="节点进程 CPU 均值" value={fmtPercentValue(latestSystem.avgProcessCpuPercent)} valueStyle={{ fontSize: 16, fontWeight: 600 }} />
          </div>
        </Tooltip>
        <div className="system-tab__divider-sm" />
        <Tooltip title={`当前最高 ${latestSystem.hotProcessRssAgentName || '—'} · ${fmtByteSize(latestSystem.maxProcessRssBytes)}`}>
          <div className="system-tab__process-stat">
            <Statistic title="节点进程 RSS 总计" value={fmtByteSize(latestSystem.totalProcessRssBytes)} valueStyle={{ fontSize: 16, fontWeight: 600 }} />
          </div>
        </Tooltip>
        <div className="system-tab__divider-sm" />
        <Tooltip title={`当前最高 ${latestSystem.hotProcessFdsAgentName || '—'} · 总计 ${fmtCompactNumber(latestSystem.totalProcessFds)}`}>
          <div className="system-tab__process-stat">
            <Statistic title="单节点句柄 / FD 最高" value={fmtCompactNumber(latestSystem.maxProcessFds)} valueStyle={{ fontSize: 16, fontWeight: 600 }} />
          </div>
        </Tooltip>
        <div className="system-tab__divider-sm" />
        <Tooltip title="所有有效资源快照中的节点进程协程总数">
          <div className="system-tab__process-stat">
            <Statistic title="协程" value={fmtCompactNumber(latestSystem.totalProcessGoroutines)} valueStyle={{ fontSize: 16, fontWeight: 600 }} />
          </div>
        </Tooltip>
        <div className="system-tab__divider-sm" />
        <Tooltip title="所有成功采集节点的节点进程线程总数">
          <div className="system-tab__process-stat">
            <Statistic title="线程" value={fmtCompactNumber(latestSystem.totalProcessThreads)} valueStyle={{ fontSize: 16, fontWeight: 600 }} />
          </div>
        </Tooltip>
      </div>
    </div>
  );
}
