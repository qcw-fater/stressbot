/**
 * 历史对比视图：2~5 个任务并排比较关键指标。
 *
 * 展现策略：
 *   - 顶部 N 列卡片：任务名 / 时长 / 总动作 / 成功率 / 加权 Apdex
 *   - 下面动作表：每行一个动作，N 列 sampleCount/p99/apdex；diff 字段亮显
 */

import { Card, Empty, Spin, Table, Tag } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import { useEffect, useMemo, useState } from 'react';
import { historyApi, showApiError } from '@/services';
import type { HistoryDetail } from '@/types/api';
import { ApdexCell } from '@/components/monitoring/shared/ApdexCell';

export interface HistoryCompareViewProps {
  ids: string[];
}

interface ActionRow {
  name: string;
  /** [taskIdx] -> sampleCount，未出现的为 undefined */
  samples: (number | undefined)[];
  apdexes: (number | undefined)[];
  p99s: (number | undefined)[];
}

export function HistoryCompareView({ ids }: HistoryCompareViewProps) {
  const [data, setData] = useState<HistoryDetail[] | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    setLoading(true);
    historyApi
      .compareHistory(ids)
      .then((r) => setData(r.tasks))
      .catch(showApiError)
      .finally(() => setLoading(false));
  }, [ids]);

  const rows = useMemo<ActionRow[]>(() => {
    if (!data) return [];
    const allActions = new Set<string>();
    for (const d of data) {
      for (const a of d.finalSnapshot.actions) allActions.add(a.name);
    }
    const out: ActionRow[] = [];
    for (const name of allActions) {
      const samples: (number | undefined)[] = [];
      const apdexes: (number | undefined)[] = [];
      const p99s: (number | undefined)[] = [];
      for (const d of data) {
        const a = d.finalSnapshot.actions.find((x) => x.name === name);
        samples.push(a?.sampleCount);
        apdexes.push(a?.apdex);
        p99s.push(a?.latency.p99Ms);
      }
      out.push({ name, samples, apdexes, p99s });
    }
    out.sort(
      (a, b) =>
        (b.samples.find((s) => s !== undefined) ?? 0) -
        (a.samples.find((s) => s !== undefined) ?? 0),
    );
    return out;
  }, [data]);

  if (loading) return <Spin />;
  if (!data || data.length === 0) return <Empty description="加载失败" />;

  // 构造表格列：第 1 列 name；之后为每个任务 3 列（样本/p99/Apdex）
  const columns: ColumnsType<ActionRow> = [
    {
      title: '动作',
      dataIndex: 'name',
      key: 'name',
      fixed: 'left',
      width: 220,
      render: (v: string) =>
        v.startsWith('callback:') ? (
          <span>
            <Tag color="orange">推送</Tag>
            <code>{v.slice('callback:'.length)}</code>
          </span>
        ) : (
          <code>{v}</code>
        ),
    },
  ];
  for (let i = 0; i < data.length; i++) {
    const taskName = data[i].name;
    columns.push({
      title: <code>{shortName(taskName)}</code>,
      key: `t${i}`,
      width: 180,
      render: (_, r) => (
        <div style={{ fontSize: 11, lineHeight: 1.5 }}>
          <div>样本: {r.samples[i] ?? '—'}</div>
          <div>p99: {r.p99s[i] !== undefined ? `${r.p99s[i]!.toFixed(0)}ms` : '—'}</div>
          <div>
            Apdex: <ApdexCell value={r.apdexes[i]} />
          </div>
        </div>
      ),
    });
  }

  return (
    <div>
      <div style={{ display: 'grid', gridTemplateColumns: `repeat(${data.length}, 1fr)`, gap: 8, marginBottom: 12 }}>
        {data.map((d, i) => (
          <Card size="small" key={d.id} title={`#${i + 1} ${d.name}`}>
            <div style={{ fontSize: 11, lineHeight: 1.6 }}>
              <div>
                <code>{d.id.slice(0, 8)}</code> · {d.totalBots} bots
              </div>
              <div>
                {d.startedAt ? dayjs(d.startedAt).format('MM-DD HH:mm') : '—'} · {d.durationSec}s
              </div>
              <div>累计 {d.finalSnapshot.totalActions} 动作</div>
            </div>
          </Card>
        ))}
      </div>
      <Table<ActionRow>
        rowKey="name"
        size="small"
        dataSource={rows}
        columns={columns}
        pagination={{ pageSize: 50, showSizeChanger: false }}
        scroll={{ x: 220 + data.length * 180, y: 480 }}
      />
    </div>
  );
}

function shortName(name: string): string {
  return name.length > 20 ? name.slice(0, 18) + '…' : name;
}
