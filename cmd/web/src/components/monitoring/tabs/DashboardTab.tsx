/**
 * 集群总览大盘 — 单面板密集布局。
 *
 * 一张 glass 面板内：4 列核心指标 → 分割线 → 汇总条 → Top Actions 迷你列表
 */

import { Progress, Tooltip } from 'antd';
import { ArrowUpOutlined, ArrowDownOutlined } from '@ant-design/icons';
import { useShallow } from 'zustand/react/shallow';
import { useRuntimeStore, classifyApdex } from '@/services';
import type { ActionMetric } from '@/types/api';
import './DashboardTab.css';

function fmtBandwidth(mbps: number) {
  const v = Number.isFinite(mbps) ? mbps : 0;
  if (v < 1) return { value: v * 1024, suffix: 'KB/s', precision: 1 };
  return { value: v, suffix: 'MB/s', precision: 2 };
}

const APDEX_COLOR: Record<string, string> = {
  excellent: 'var(--color-success)',
  good: 'var(--chart-lime)',
  fair: 'var(--color-warning)',
  poor: 'var(--chart-orange)',
  danger: 'var(--color-error)',
  unknown: 'var(--text-tertiary)',
};

function gaugeColor(v: number): string {
  if (v > 80) return 'var(--color-error)';
  if (v > 60) return 'var(--color-warning)';
  return 'var(--color-blue)';
}

function successColor(rate: number): string {
  if (rate >= 0.95) return 'var(--color-success)';
  if (rate >= 0.8) return 'var(--color-warning)';
  return 'var(--color-error)';
}

function fmtMs(ms: number): string {
  if (ms < 1000) return `${ms.toFixed(0)}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

export function DashboardTab() {
  const { latestStress, latestSystem, agents } = useRuntimeStore(
    useShallow((s) => ({
      latestStress: s.latestStress,
      latestSystem: s.latestSystem,
      agents: s.agents,
    })),
  );

  if (!latestStress) {
    return (
      <div className="dashboard-tab__panel" style={{ justifyContent: 'center', alignItems: 'center' }}>
        <span style={{ color: 'var(--text-tertiary)', fontSize: 12 }}>暂无压测数据；启动任务后实时显示</span>
      </div>
    );
  }

  const r = latestStress.robots;
  const c = latestStress.connections;
  const b = latestStress.bandwidth;
  const sys = latestSystem;
  const safeAgents = agents ?? [];
  const actions = latestStress.actions;

  const activeConns = Math.max(0, c.established - c.dropped);
  const send = fmtBandwidth(b.sendMBps ?? 0);
  const recv = fmtBandwidth(b.recvMBps ?? 0);

  // 加权 Apdex（用 rttSampleCount 作权重，排除纯客户端动作）/ 成功率
  let totalSamples = 0, apdexWeight = 0, wApdex = 0, wSuccess = 0;
  for (const a of actions) {
    totalSamples += a.sampleCount;
    wSuccess += a.successRate * a.sampleCount;
    if (a.rttSampleCount > 0) {
      apdexWeight += a.rttSampleCount;
      wApdex += a.apdex * a.rttSampleCount;
    }
  }
  const clusterApdex = apdexWeight > 0 ? wApdex / apdexWeight : 0;
  const clusterSuccess = totalSamples > 0 ? wSuccess / totalSamples : 0;
  const apdexLevel = classifyApdex(clusterApdex);

  const onlineAgents = safeAgents.filter((a) => a.status !== 'offline').length;
  const robotPercent = r.started > 0 ? Math.round((r.running / r.started) * 100) : 0;

  // Top 8 actions by sample count
  const topActions = [...actions]
    .sort((a, b) => b.sampleCount - a.sampleCount)
    .slice(0, 8);
  const maxSamples = topActions.length > 0 ? topActions[0].sampleCount : 1;

  return (
    <div className="dashboard-tab">
      <div className="dashboard-tab__panel">
        {/* ── 4 列核心指标 ── */}
        <div className="dashboard-tab__metrics">
          {/* 机器人 */}
          <div className="dashboard-tab__metric">
            <div className="dashboard-tab__metric-title">机器人</div>
            <div className="dashboard-tab__metric-value">
              {r.running}
              <span style={{ fontSize: 14, fontWeight: 400, color: 'var(--text-secondary)' }}> / {r.started}</span>
            </div>
            <div className="dashboard-tab__progress-wrap">
              <Progress percent={robotPercent} strokeColor="var(--color-success)" showInfo={false} size={4} />
            </div>
            <div className="dashboard-tab__chips">
              {r.stopped > 0 && <span className="dashboard-tab__chip dashboard-tab__chip--stopped">stop {r.stopped}</span>}
              {r.errored > 0 && <span className="dashboard-tab__chip dashboard-tab__chip--errored">err {r.errored}</span>}
            </div>
          </div>

          {/* 连接 */}
          <Tooltip title={`累计建连 ${c.established} · 关闭 ${c.dropped} · 失败 ${c.failed}`}>
            <div className="dashboard-tab__metric">
              <div className="dashboard-tab__metric-title">连接</div>
              <div className="dashboard-tab__metric-value">{activeConns}</div>
              <div className="dashboard-tab__metric-sub">活跃 · 累计 {c.established}</div>
              <div className="dashboard-tab__metric-detail">失败 {c.failed}</div>
            </div>
          </Tooltip>

          {/* 带宽 */}
          <Tooltip title={`速率 = 累计字节 / uptime`}>
            <div className="dashboard-tab__metric">
              <div className="dashboard-tab__metric-title">带宽</div>
              <div className="dashboard-tab__bw-row">
                <ArrowUpOutlined className="dashboard-tab__bw-arrow dashboard-tab__bw-arrow--up" />
                <span className="dashboard-tab__metric-sub" style={{ fontWeight: 600, color: 'var(--text-primary)' }}>
                  {send.value.toFixed(send.precision)} {send.suffix}
                </span>
              </div>
              <div className="dashboard-tab__bw-row">
                <ArrowDownOutlined className="dashboard-tab__bw-arrow dashboard-tab__bw-arrow--down" />
                <span className="dashboard-tab__metric-sub" style={{ fontWeight: 600, color: 'var(--text-primary)' }}>
                  {recv.value.toFixed(recv.precision)} {recv.suffix}
                </span>
              </div>
              <div className="dashboard-tab__metric-detail">
                累计 ↑{(b.totalSendBytes / 1024).toFixed(0)}K ↓{(b.totalRecvBytes / 1024).toFixed(0)}K
              </div>
            </div>
          </Tooltip>

          {/* 集群资源 */}
          <div className="dashboard-tab__metric">
            <div className="dashboard-tab__metric-title">集群资源</div>
            <div className="dashboard-tab__res-row">
              <Progress
                type="circle"
                percent={sys?.avgCpuPercent ?? 0}
                size={52}
                strokeColor={gaugeColor(sys?.avgCpuPercent ?? 0)}
                format={(p) => (
                  <span style={{ fontSize: 13, fontWeight: 700, color: 'var(--text-primary)', fontVariantNumeric: 'tabular-nums' }}>
                    {p !== undefined ? `${p.toFixed(0)}` : '—'}
                  </span>
                )}
              />
              <div className="dashboard-tab__res-info">
                <div className="dashboard-tab__res-text">
                  {onlineAgents}/{safeAgents.length} 节点
                </div>
                {sys?.hotAgentName && (
                  <Tooltip title="CPU 最高节点">
                    <div className="dashboard-tab__res-hot">热点 {sys.hotAgentName}</div>
                  </Tooltip>
                )}
                {sys && <div className="dashboard-tab__res-hot">max {sys.maxCpuPercent.toFixed(0)}%</div>}
              </div>
            </div>
          </div>
        </div>

        <div className="dashboard-tab__hr" />

        {/* ── 汇总条 ── */}
        <div className="dashboard-tab__summary">
          <div className="dashboard-tab__sg">
            <span className="dashboard-tab__sg-label">uptime</span>
            <span className="dashboard-tab__sg-value">{(latestStress.uptimeSeconds / 60).toFixed(1)}min</span>
          </div>
          <div className="dashboard-tab__sg-divider" />
          <div className="dashboard-tab__sg">
            <span className="dashboard-tab__sg-label">动作</span>
            <span className="dashboard-tab__sg-value">{latestStress.totalActions.toLocaleString()}</span>
          </div>
          <div className="dashboard-tab__sg-divider" />
          <div className="dashboard-tab__sg">
            <span className="dashboard-tab__sg-label">类型</span>
            <span className="dashboard-tab__sg-value">{actions.length}</span>
          </div>
          <div className="dashboard-tab__sg-divider" />
          <div className="dashboard-tab__sg">
            <span className="dashboard-tab__sg-label">Apdex</span>
            <span className="dashboard-tab__sg-value" style={{ color: APDEX_COLOR[apdexLevel] }}>
              {clusterApdex.toFixed(3)}
            </span>
          </div>
          <div className="dashboard-tab__sg-divider" />
          <div className="dashboard-tab__sg">
            <span className="dashboard-tab__sg-label">成功</span>
            <span className="dashboard-tab__sg-value" style={{ color: successColor(clusterSuccess) }}>
              {(clusterSuccess * 100).toFixed(1)}%
            </span>
          </div>
        </div>

        <div className="dashboard-tab__hr" />

        {/* ── Top Actions 迷你列表 ── */}
        <div className="dashboard-tab__top-actions">
          <div className="dashboard-tab__top-title">Top 动作</div>
          {topActions.map((a) => (
            <ActionMiniRow key={a.name} action={a} maxSamples={maxSamples} />
          ))}
        </div>
      </div>
    </div>
  );
}

function ActionMiniRow({ action, maxSamples }: { action: ActionMetric; maxSamples: number }) {
  const isCallback = action.name.startsWith('callback:');
  const displayName = isCallback ? action.name.replace('callback:', '') + ' (推送)' : action.name;
  const barPct = maxSamples > 0 ? (action.sampleCount / maxSamples) * 100 : 0;
  const rateColor = action.successRate >= 0.95 ? 'var(--color-success)'
    : action.successRate >= 0.8 ? 'var(--color-warning)' : 'var(--color-error)';

  return (
    <div className="dashboard-tab__action-row">
      <Tooltip title={`avg ${fmtMs(action.rtt.avgMs)} · p99 ${fmtMs(action.rtt.p99Ms)} · err ${action.failureCount + action.timeoutCount}`}>
        <span className={`dashboard-tab__action-name${isCallback ? ' dashboard-tab__action-name--callback' : ''}`}>
          {displayName}
        </span>
      </Tooltip>
      <div className="dashboard-tab__action-bar">
        <div className="dashboard-tab__action-bar-fill" style={{ width: `${barPct}%` }} />
      </div>
      <span className="dashboard-tab__action-stat">{action.sampleCount.toLocaleString()}</span>
      <span className="dashboard-tab__action-rate" style={{ color: rateColor }}>
        {(action.successRate * 100).toFixed(0)}%
      </span>
    </div>
  );
}
