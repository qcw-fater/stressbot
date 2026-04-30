/**
 * Per-Agent 视图：每个 Agent 一行，列出汇总的压测+系统指标。
 *
 * 数据源：调用 GET /api/metrics/per-agent + /api/system/per-agent。
 * 这里不主动轮询，而是按需 useState + 用户切到该 Tab 时拉一次（避免 idle 时浪费）。
 */

import { Empty, Table, Tag, Spin, Space, Button } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';
import { useEffect, useMemo, useState } from 'react';
import { metricsApi, showApiError, useRuntimeStore } from '@/services';
import type { PerAgentMetricsItem, PerAgentSystemItem } from '@/types/api';
import { ApdexCell } from '../shared/ApdexCell';

interface AgentRow {
  agentId: string;
  agentName: string;
  status: string;
  isStale: boolean;
  totalActions: number;
  apdex: number;
  successRate: number;
  cpuPercent: number;
  memPercent: number;
  numGoroutine: number;
}

export function PerAgentTab() {
  const mode = useRuntimeStore((s) => s.mode);
  const [stressItems, setStressItems] = useState<PerAgentMetricsItem[]>([]);
  const [systemItems, setSystemItems] = useState<PerAgentSystemItem[]>([]);
  const [loading, setLoading] = useState(false);

  async function refresh() {
    setLoading(true);
    try {
      const [m, s] = await Promise.all([metricsApi.getPerAgentMetrics(), metricsApi.getPerAgentSystem()]);
      setStressItems(m.items);
      setSystemItems(s.items);
    } catch (err) {
      showApiError(err);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    if (mode !== 'edit') refresh();
  }, [mode]);

  const rows = useMemo<AgentRow[]>(() => {
    const stressMap = new Map(stressItems.map((it) => [it.agentId, it]));
    const sysMap = new Map(systemItems.map((it) => [it.agentId, it]));
    const ids = new Set([...stressMap.keys(), ...sysMap.keys()]);
    const out: AgentRow[] = [];
    for (const id of ids) {
      const ms = stressMap.get(id);
      const ss = sysMap.get(id);
      const snap = ms?.snapshot;
      const sys = ss?.snapshot;
      // 整个 agent 的 apdex / 成功率 用各动作样本加权
      let totalSamples = 0;
      let weightedApdex = 0;
      let weightedSuccess = 0;
      if (snap) {
        for (const a of snap.actions) {
          totalSamples += a.sampleCount;
          weightedApdex += a.apdex * a.sampleCount;
          weightedSuccess += a.successRate * a.sampleCount;
        }
      }
      out.push({
        agentId: id,
        agentName: ms?.agentName ?? ss?.agentName ?? id,
        status: ss?.status ?? '—',
        isStale: ss?.isStale ?? false,
        totalActions: snap?.totalActions ?? 0,
        apdex: totalSamples > 0 ? weightedApdex / totalSamples : 0,
        successRate: totalSamples > 0 ? weightedSuccess / totalSamples : 0,
        cpuPercent: sys?.cpuPercent ?? 0,
        memPercent: sys?.memPercent ?? 0,
        numGoroutine: sys?.numGoroutine ?? 0,
      });
    }
    return out;
  }, [stressItems, systemItems]);

  const columns: ColumnsType<AgentRow> = [
    {
      title: 'Agent',
      key: 'name',
      render: (_, r) => (
        <Space size={4}>
          <code>{r.agentName}</code>
          {r.isStale && <Tag color="warning">陈旧</Tag>}
        </Space>
      ),
    },
    { title: '状态', dataIndex: 'status', key: 'status', width: 90 },
    { title: '累计动作', dataIndex: 'totalActions', key: 'totalActions', width: 100, sorter: (a, b) => a.totalActions - b.totalActions },
    {
      title: '成功率',
      dataIndex: 'successRate',
      key: 'successRate',
      width: 90,
      render: (v: number) => `${(v * 100).toFixed(1)}%`,
      sorter: (a, b) => a.successRate - b.successRate,
    },
    {
      title: 'Apdex',
      dataIndex: 'apdex',
      key: 'apdex',
      width: 90,
      render: (v: number) => <ApdexCell value={v} />,
      sorter: (a, b) => a.apdex - b.apdex,
    },
    {
      title: 'CPU%',
      dataIndex: 'cpuPercent',
      key: 'cpuPercent',
      width: 90,
      render: (v: number) => (
        <span style={{ color: v > 80 ? '#f5222d' : undefined, fontVariantNumeric: 'tabular-nums' }}>{v.toFixed(1)}%</span>
      ),
      sorter: (a, b) => a.cpuPercent - b.cpuPercent,
      defaultSortOrder: 'descend',
    },
    {
      title: 'MEM%',
      dataIndex: 'memPercent',
      key: 'memPercent',
      width: 90,
      render: (v: number) => (
        <span style={{ color: v > 90 ? '#f5222d' : undefined, fontVariantNumeric: 'tabular-nums' }}>{v.toFixed(1)}%</span>
      ),
    },
    { title: 'Goroutine', dataIndex: 'numGoroutine', key: 'numGoroutine', width: 100 },
  ];

  return (
    <Space direction="vertical" style={{ width: '100%' }} size={8}>
      <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
        <Button size="small" icon={<ReloadOutlined />} loading={loading} onClick={refresh}>
          刷新
        </Button>
      </div>
      {loading && rows.length === 0 ? (
        <Spin />
      ) : rows.length === 0 ? (
        <Empty description="暂无 Agent 数据" />
      ) : (
        <Table<AgentRow>
          rowKey="agentId"
          size="small"
          dataSource={rows}
          columns={columns}
          pagination={false}
          scroll={{ y: 'calc(70vh - 220px)' }}
        />
      )}
    </Space>
  );
}
