/**
 * 集群系统资源详情。
 */

import { Card, Col, Empty, Row, Statistic, Tag, Tooltip } from 'antd';
import { useShallow } from 'zustand/react/shallow';
import { useRuntimeStore } from '@/services';

/** 与 DashboardTab 同源的智能单位选择 */
function fmtBandwidth(mbps: number): { value: number; suffix: string; precision: number } {
  const v = Number.isFinite(mbps) ? mbps : 0;
  if (v < 1) return { value: v * 1024, suffix: 'KB/s', precision: 1 };
  return { value: v, suffix: 'MB/s', precision: 2 };
}

export function SystemTab() {
  const { latestSystem, agents } = useRuntimeStore(
    useShallow((s) => ({ latestSystem: s.latestSystem, agents: s.agents })),
  );

  if (!latestSystem) {
    return <Empty description="暂无系统资源数据" />;
  }

  // 防御性兜底：如果后端返回旧字段名（升级未重启 admin），所有 totalXxx 字段都是 undefined，
  // undefined / 1024 = NaN → Statistic 渲染成 "NaN"。统一 ?? 0 兜底。
  const usedMemMB = latestSystem.usedMemMB ?? 0;
  const totalMemMB = latestSystem.totalMemMB ?? 0;
  const memUsedGB = usedMemMB / 1024;
  const memTotalGB = totalMemMB / 1024;
  const memPercent = (usedMemMB / Math.max(totalMemMB, 1)) * 100;
  // sendKBps/recvKBps 是后端 net.IOCounters 差分，单位 KB/s。
  // 转 MB/s 后小流量场景会一直是 0.00；用智能单位 + Tooltip 说明数据语义。
  const sendKBps = latestSystem.totalNetSendKBps ?? 0;
  const recvKBps = latestSystem.totalNetRecvKBps ?? 0;
  const sendBw = fmtBandwidth(sendKBps / 1024);
  const recvBw = fmtBandwidth(recvKBps / 1024);

  return (
    <Row gutter={[12, 12]}>
      <Col span={6}>
        <Card size="small" title="集群拓扑">
          <Statistic title="Agent 数" value={latestSystem.agentCount} />
          <div style={{ marginTop: 8 }}>
            <Tag color="success">在线 {latestSystem.onlineCount}</Tag>
            <Tag>离线 {latestSystem.offlineCount}</Tag>
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
            valueStyle={{ color: latestSystem.avgCpuPercent > 80 ? 'var(--color-error)' : undefined }}
          />
          <Statistic
            title="最高 CPU%"
            value={latestSystem.maxCpuPercent}
            precision={1}
            suffix="%"
            valueStyle={{ color: latestSystem.maxCpuPercent > 90 ? 'var(--color-error)' : latestSystem.maxCpuPercent > 70 ? 'var(--color-warning)' : undefined }}
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
        <Tooltip title="基于 gopsutil net.IOCounters 差分计算（系统总网络流量，含其他进程），小流量自动切换 KB/s。">
          <Card size="small" title="网络">
            <Statistic title="↑" value={sendBw.value} precision={sendBw.precision} suffix={sendBw.suffix} />
            <Statistic title="↓" value={recvBw.value} precision={recvBw.precision} suffix={recvBw.suffix} />
          </Card>
        </Tooltip>
      </Col>
      <Col span={12}>
        <Card size="small" title="进程资源（agent 进程内）">
          <Row gutter={12}>
            <Col span={8}>
              <Tooltip title="所有 Agent 的 runtime.NumGoroutine() 之和（Go 协程数）">
                <Statistic title="goroutine 总" value={latestSystem.totalGoroutines ?? 0} />
              </Tooltip>
            </Col>
            <Col span={8}>
              <Tooltip title="所有 Agent 的 OS 线程数之和（Windows 上为内核线程对象）">
                <Statistic title="thread 总" value={latestSystem.totalThreads ?? 0} />
              </Tooltip>
            </Col>
            <Col span={8}>
              <Tooltip
                title="Linux/macOS 上是文件描述符数；Windows 上 gopsutil.NumFDs 实际返回 GetProcessHandleCount —— 即进程持有的 handle 总数（含文件、socket、线程、事件、注册表等内核对象），通常达数百，属正常。"
              >
                <Statistic
                  title={
                    <span>
                      fd 总
                      <span style={{ marginLeft: 4, fontSize: 10, color: 'var(--text-tertiary)' }}>
                        (Win=handle)
                      </span>
                    </span>
                  }
                  value={latestSystem.totalFds ?? 0}
                />
              </Tooltip>
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
