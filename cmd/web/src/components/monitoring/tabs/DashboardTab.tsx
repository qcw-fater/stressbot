/**
 * 集群总览：robots / connections / bandwidth / system 关键数。
 */

import { Card, Col, Row, Statistic, Tag, Tooltip } from 'antd';
import { useShallow } from 'zustand/react/shallow';
import { useRuntimeStore } from '@/services';

/**
 * 智能选择带宽显示单位：< 1MB/s 用 KB/s 显示更直观（避免一直显示 0.00 MB/s），
 * 否则用 MB/s。
 */
function fmtBandwidth(mbps: number): { value: number; suffix: string; precision: number } {
  const v = Number.isFinite(mbps) ? mbps : 0;
  if (v < 1) return { value: v * 1024, suffix: 'KB/s', precision: 1 };
  return { value: v, suffix: 'MB/s', precision: 2 };
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
    return <Card size="small">暂无压测数据；启动任务后实时显示</Card>;
  }

  const r = latestStress.robots;
  const c = latestStress.connections;
  const b = latestStress.bandwidth;
  const sys = latestSystem;
  const safeAgents = agents ?? [];

  // 后端 connections.established 是"累计建连次数"（含失败 / 主动重连），
  // dropped 是"累计关闭次数"（主动 + 被动）。当前活跃 = established − dropped。
  // failed 单独累计是建连失败次数，业务上诊断价值有限，UI 不再单独显示，
  // 用户如需细看可从 ActionsTab 错误面板或 agent 日志定位。
  const activeConns = Math.max(0, c.established - c.dropped);

  const send = fmtBandwidth(b.sendMBps ?? 0);
  const recv = fmtBandwidth(b.recvMBps ?? 0);

  return (
    <Row gutter={[12, 12]}>
      <Col span={6}>
        <Card size="small" title="机器人">
          <Statistic title="运行中" value={r.running} suffix={`/ ${r.started}`} />
          <div style={{ marginTop: 8, fontSize: 12 }}>
            <Tag color="default">stopped {r.stopped}</Tag>
            <Tag color="error">errored {r.errored}</Tag>
          </div>
        </Card>
      </Col>
      <Col span={6}>
        <Tooltip title={`累计建连 ${c.established} · 累计关闭 ${c.dropped} · 建连失败 ${c.failed}（仅作参考，不在面板上展示）`}>
          <Card size="small" title="连接（当前 / 累计）">
            <Statistic title="当前活跃" value={activeConns} />
            <div style={{ marginTop: 8, fontSize: 12 }}>
              <Tag>累计 {c.established}</Tag>
            </div>
          </Card>
        </Tooltip>
      </Col>
      <Col span={6}>
        <Tooltip
          title={`累计 ↑ ${(b.totalSendBytes / 1024).toFixed(0)}KB · ↓ ${(b.totalRecvBytes / 1024).toFixed(0)}KB；` +
            '速率 = 累计字节 / uptime。游戏协议消息通常很小，小流量场景已自动切换 KB/s 单位。'}
        >
          <Card size="small" title="带宽（上 / 下）">
            <Statistic title="↑" value={send.value} precision={send.precision} suffix={send.suffix} />
            <Statistic title="↓" value={recv.value} precision={recv.precision} suffix={recv.suffix} />
          </Card>
        </Tooltip>
      </Col>
      <Col span={6}>
        <Card size="small" title="集群资源">
          <Statistic
            title="平均 CPU"
            value={sys?.avgCpuPercent ?? 0}
            precision={1}
            suffix="%"
            valueStyle={{ color: (sys?.avgCpuPercent ?? 0) > 80 ? '#f5222d' : undefined }}
          />
          <div style={{ marginTop: 8, fontSize: 12, color: 'var(--text-secondary)' }}>
            在线 Agent {safeAgents.filter((a) => a.status !== 'offline').length}/{safeAgents.length}
            {sys?.hotAgentName && (
              <span style={{ marginLeft: 6 }}>
                · 热点 <code>{sys.hotAgentName}</code>
              </span>
            )}
          </div>
        </Card>
      </Col>
      <Col span={24}>
        <Card size="small" title="动作汇总">
          <div style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
            uptime {(latestStress.uptimeSeconds / 60).toFixed(1)}min · 累计动作{' '}
            {latestStress.totalActions} · 监测动作 {latestStress.actions.length} 类
          </div>
        </Card>
      </Col>
    </Row>
  );
}
