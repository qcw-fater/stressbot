import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import type { AgentBrief, ClusterSystemSnapshot, StressSnapshot } from '@/types/api';
import { useRuntimeStore } from '@/services';
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';
import { MonitorDock } from './MonitorDock';
import { SystemTab } from './tabs/SystemTab';

vi.mock('./shared/ActionMetricsTable', () => ({
  ActionMetricsTable: () => <div data-testid="action-metrics-table" />,
}));

const stressSnapshot = {
  timestamp: '2026-08-04T06:00:00Z',
  collectionEpoch: 1,
  uptimeSeconds: 30,
  totalActions: 8706,
  apdexT: 100,
  timingDetail: 'rtt',
  summary: { avgQps: 100 },
  robots: { started: 100, running: 100, stopped: 0, errored: 0 },
  rampUp: { currentStage: 0, totalStages: 0 },
  connections: { established: 100, active: 100, closed: 0, failed: 0, dropped: 0 },
  bandwidth: { totalSendBytes: 0, totalRecvBytes: 0, sendMBps: 0, recvMBps: 0 },
  actions: [],
  invalidMetricSamples: 0,
  window: null,
} as unknown as StressSnapshot;

const systemSnapshot = {
  timestamp: '2026-08-04T06:00:00Z',
  agentCount: 1,
  onlineCount: 1,
  unhealthyCount: 0,
  offlineCount: 0,
  reportingAgents: 1,
  staleAgents: 0,
  missingAgents: 0,
  coverageRatio: 1,
  hostCpuReportingAgents: 1,
  avgHostCpuPercent: 83.4,
  maxHostCpuPercent: 83.4,
  hotHostCpuAgentName: '节点一',
  hostMemoryReportingAgents: 1,
  totalHostMemBytes: 16 * 1024 * 1024 * 1024,
  usedHostMemBytes: 12 * 1024 * 1024 * 1024,
  avgHostMemPercent: 75,
  maxHostMemPercent: 75,
  hotHostMemAgentName: '节点一',
  hostNetSendReportingAgents: 1,
  hostNetRecvReportingAgents: 1,
  totalHostNetSendBytesPerSec: 1024,
  totalHostNetRecvBytesPerSec: 2048,
  processCpuReportingAgents: 1,
  avgProcessCpuPercent: 12.5,
  maxProcessCpuPercent: 12.5,
  hotProcessCpuAgentName: '节点一',
  processRssReportingAgents: 1,
  totalProcessRssBytes: 256 * 1024 * 1024,
  maxProcessRssBytes: 256 * 1024 * 1024,
  hotProcessRssAgentName: '节点一',
  totalProcessHeapBytes: 128 * 1024 * 1024,
  totalProcessGoroutines: 100,
  processThreadsReportingAgents: 1,
  totalProcessThreads: 20,
  processFdsReportingAgents: 1,
  totalProcessFds: 1025,
  maxProcessFds: 1025,
  hotProcessFdsAgentName: '节点一',
  agents: [],
} satisfies ClusterSystemSnapshot;

const agent = {
  agentId: 'node-1',
  name: '节点一',
  status: 'busy',
  currentBots: 100,
  maxBots: 5000,
} as unknown as AgentBrief;

afterEach(() => {
  useEditorStore.setState({ monitorDockOpen: false });
  useRuntimeStore.setState({
    mode: 'edit',
    latestStress: null,
    latestSystem: null,
    agents: [],
    reportingAgents: 0,
    totalAgents: 0,
    offlineAgents: 0,
    assignedAgents: 0,
  });
});

describe('MonitorDock resource summary', () => {
  it('uses the same three-row KPI rhythm as the other summary cards', () => {
    useEditorStore.setState({ monitorDockOpen: true });
    useRuntimeStore.setState({
      mode: 'running',
      latestStress: stressSnapshot,
      latestSystem: systemSnapshot,
      agents: [agent],
      reportingAgents: 1,
      totalAgents: 1,
      offlineAgents: 0,
      assignedAgents: 1,
    });

    const { container } = render(<MonitorDock />);
    const resourceCard = screen.getByText('资源 / 节点').closest('section');

    expect(resourceCard).not.toBeNull();
    expect(resourceCard?.querySelectorAll('.md-kpi-cell')).toHaveLength(9);
    expect(resourceCard?.querySelector('.md-resource-hotline')).toBeNull();
    expect(resourceCard?.textContent).toContain('容量 100 / 5,000');
    expect(resourceCard?.textContent).toContain('CPU 最高');
    expect(resourceCard?.textContent).toContain('内存最高');
    expect(resourceCard?.textContent).toContain('RSS 总计');
    expect(resourceCard?.textContent).toContain('FD 最高');
    expect(resourceCard?.textContent).not.toContain('主机');
    expect(resourceCard?.textContent).not.toContain('进程');
    expect(resourceCard?.textContent).not.toContain('句柄');
    expect(resourceCard?.textContent).not.toContain('节点一');
    expect(container.querySelectorAll('.md-metric-group')).toHaveLength(4);
  });

  it('uses the implicit node scope in the system resource view', () => {
    useRuntimeStore.setState({ latestSystem: systemSnapshot, agents: [agent] });

    const { container } = render(<SystemTab />);

    expect(container.textContent).toContain('CPU');
    expect(container.textContent).toContain('内存');
    expect(container.textContent).toContain('网卡发送');
    expect(container.textContent).not.toContain('主机');
    expect(container.textContent).not.toContain('进程');
    expect(container.textContent).not.toContain('句柄');
  });
});
