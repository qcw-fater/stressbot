/**
 * 集群系统资源详情。
 */

import { Card, Col, Empty, Row, Statistic, Tag } from 'antd';
import { useShallow } from 'zustand/react/shallow';
import { useRuntimeStore } from '@/services';

export function SystemTab() {
  const { latestSystem, agents } = useRuntimeStore(
    useShallow((s) => ({ latestSystem: s.latestSystem, agents: s.agents })),
  );

  if (!latestSystem) {
    return <Empty description="暂无系统资源数据" />;
  }

  const memUsedGB = latestSystem.usedMemMB / 1024;
  const memTotalGB = latestSystem.totalMemMB / 1024;
  const memPercent = (latestSystem.usedMemMB / Math.max(latestSystem.totalMemMB, 1)) * 100;

  return (
    <Row gutter={[12, 12]}>
      <Col span={6}>
        <Card size="small" title="集群拓扑">
          <Statistic title="Agent 数" value={latestSystem.agentCount} />
          <div style={{ marginTop: 8 }}>
            <Tag color="success">在线 {latestSystem.onlineCount}</Tag>
            <Tag>离线 {latestSystem.offlineCount}</Tag>
            {latestSystem.upgradingCount > 0 && <Tag color="processing">升级中 {latestSystem.upgradingCount}</Tag>}
          </div>
        </Card>
      </Col>
      <Col span={6}>
        <Card size="small" title="CPU">
          <Statistic
            title="平均 CPU%"
            value={latestSystem.avgCpuPercent}
            precision={1}
            suffix="%"
            valueStyle={{ color: latestSystem.avgCpuPercent > 80 ? '#f5222d' : undefined }}
          />
          <Statistic
            title="最高 CPU%"
            value={latestSystem.maxCpuPercent}
            precision={1}
            suffix="%"
            valueStyle={{ color: latestSystem.maxCpuPercent > 90 ? '#f5222d' : latestSystem.maxCpuPercent > 70 ? '#faad14' : undefined }}
          />
          {latestSystem.hotAgentName && (
            <div style={{ fontSize: 11, color: 'var(--text-tertiary)', marginTop: 4 }}>
              热点：<code>{latestSystem.hotAgentName}</code>
            </div>
          )}
        </Card>
      </Col>
      <Col span={6}>
        <Card size="small" title="内存">
          <Statistic title="使用" value={memUsedGB} precision={1} suffix={`GB / ${memTotalGB.toFixed(1)}GB`} />
          <Statistic title="占用率" value={memPercent} precision={1} suffix="%" />
        </Card>
      </Col>
      <Col span={6}>
        <Card size="small" title="网络">
          <Statistic title="↑" value={latestSystem.totalNetSendKBps / 1024} precision={2} suffix="MB/s" />
          <Statistic title="↓" value={latestSystem.totalNetRecvKBps / 1024} precision={2} suffix="MB/s" />
        </Card>
      </Col>
      <Col span={12}>
        <Card size="small" title="协程 / 线程 / 文件描述符">
          <Row gutter={12}>
            <Col span={8}>
              <Statistic title="goroutine 总" value={latestSystem.totalGoroutines} />
            </Col>
            <Col span={8}>
              <Statistic title="thread 总" value={latestSystem.totalThreads} />
            </Col>
            <Col span={8}>
              <Statistic title="fd 总" value={latestSystem.totalFds} />
            </Col>
          </Row>
        </Card>
      </Col>
      <Col span={12}>
        <Card size="small" title="Agent 在线状态">
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
            {(agents ?? []).map((a) => (
              <Tag
                key={a.agentId}
                color={a.status === 'offline' ? 'default' : a.status === 'unhealthy' ? 'error' : a.status === 'busy' ? 'processing' : 'success'}
              >
                {a.name}
                {a.cpuPercent !== undefined && <span style={{ marginLeft: 4, opacity: 0.7 }}>· {a.cpuPercent.toFixed(0)}%</span>}
              </Tag>
            ))}
          </div>
        </Card>
      </Col>
    </Row>
  );
}
