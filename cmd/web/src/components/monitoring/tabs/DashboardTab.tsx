/**
 * 集群总览：robots / connections / bandwidth / system 关键数。
 */

import { Card, Col, Row, Statistic, Tag } from 'antd';
import { useShallow } from 'zustand/react/shallow';
import { useRuntimeStore } from '@/services';

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
        <Card size="small" title="连接">
          <Statistic title="established" value={c.established} />
          <div style={{ marginTop: 8, fontSize: 12 }}>
            <Tag color="warning">failed {c.failed}</Tag>
            <Tag color="error">dropped {c.dropped}</Tag>
          </div>
        </Card>
      </Col>
      <Col span={6}>
        <Card size="small" title="带宽（上 / 下）">
          <Statistic title="↑" value={b.sendMBps} precision={2} suffix="MB/s" />
          <Statistic title="↓" value={b.recvMBps} precision={2} suffix="MB/s" />
        </Card>
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
