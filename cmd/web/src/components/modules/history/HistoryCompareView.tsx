/**
 * 历史对比视图：2~5 个任务并排比较关键指标。
 *
 * 展现策略：
 *   - 顶部 N 列玻璃摘要卡片：任务名 / hero 指标行（时长/机器人/动作）
 *   - 下方对比表格（glass 包裹）：每行一个动作，N 列 sampleCount/p99/apdex；
 *     best/worst 单元格着色高亮
 */

import { Empty, Spin, Table, Tag } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import { useEffect, useMemo, useState } from 'react';
import { historyApi, showApiError } from '@/services';
import type { HistoryDetail } from '@/types/api';
import { ApdexCell } from '@/components/monitoring/shared/ApdexCell';
import './HistoryPanel.css';

function useViewportHeight(): number {
  const [h, setH] = useState(() => (typeof window !== 'undefined' ? window.innerHeight : 720));
  useEffect(() => {
    const onResize = () => setH(window.innerHeight);
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }, []);
  return h;
}

export interface HistoryCompareViewProps {
  ids: string[];
}

interface ActionRow {
  name: string;
  samples: (number | undefined)[];
  apdexes: (number | undefined)[];
  p99s: (number | undefined)[];
}

function shortName(name: string): string {
  return name.length > 20 ? name.slice(0, 18) + '…' : name;
}

function formatDuration(sec: number): string {
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m${sec % 60}s`;
  return `${(sec / 3600).toFixed(1)}h`;
}

function bestWorst(
  values: (number | undefined)[],
  higherIsBetter: boolean,
): { best: Set<number>; worst: Set<number> } {
  const defined = values
    .map((v, i) => ({ v, i }))
    .filter((x): x is { v: number; i: number } => x.v !== undefined);
  if (defined.length < 2) return { best: new Set(), worst: new Set() };
  const sorted = defined.sort((a, b) => a.v - b.v);
  const bestVal = higherIsBetter ? sorted[sorted.length - 1].v : sorted[0].v;
  const worstVal = higherIsBetter ? sorted[0].v : sorted[sorted.length - 1].v;
  if (bestVal === worstVal) return { best: new Set(), worst: new Set() };
  return {
    best: new Set(defined.filter((x) => x.v === bestVal).map((x) => x.i)),
    worst: new Set(defined.filter((x) => x.v === worstVal).map((x) => x.i)),
  };
}

export function HistoryCompareView({ ids }: HistoryCompareViewProps) {
  const [data, setData] = useState<HistoryDetail[] | null>(null);
  const [loading, setLoading] = useState(true);
  const viewportH = useViewportHeight();
  const tableScrollY = useMemo(
    () => Math.min(440, Math.max(200, viewportH - 320)),
    [viewportH],
  );

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
    columns.push({
      title: <code>{shortName(data[i].name)}</code>,
      key: `t${i}`,
      width: 180,
      render: (_, r) => {
        const p99Bw = bestWorst(r.p99s, false);
        const apdexBw = bestWorst(r.apdexes, true);
        return (
          <div style={{ fontSize: 11, lineHeight: 1.6 }}>
            <div className="hp-compare-cell">样本: {r.samples[i] ?? '—'}</div>
            <div
              className={`hp-compare-cell${p99Bw.best.has(i) ? ' hp-compare-cell--best' : ''}${p99Bw.worst.has(i) ? ' hp-compare-cell--worst' : ''}`}
            >
              p99: {r.p99s[i] !== undefined ? `${r.p99s[i]!.toFixed(0)}ms` : '—'}
            </div>
            <div
              className={`hp-compare-cell${apdexBw.best.has(i) ? ' hp-compare-cell--best' : ''}${apdexBw.worst.has(i) ? ' hp-compare-cell--worst' : ''}`}
            >
              Apdex: <ApdexCell value={r.apdexes[i]} />
            </div>
          </div>
        );
      },
    });
  }

  return (
    <div className="hp-compare-root">
      <div className="hp-compare-cards" style={{ gridTemplateColumns: `repeat(${data.length}, 1fr)` }}>
        {data.map((d, i) => (
          <div key={d.id} className="hp-glass hp-compare-card" data-index={i}>
            <div className="hp-compare-card__title">
              <span style={{ marginRight: 6 }}>#{i + 1}</span>
              {d.name}
            </div>
            <div className="hp-compare-card__meta">
              <code>{d.id.slice(0, 8)}</code> · {d.startedAt ? dayjs(d.startedAt).format('MM-DD HH:mm') : '—'}
            </div>
            <div className="hp-compare-card__hero">
              <div className="hp-hero-row">
                <div className="hp-hero-box">
                  <div className="hp-hero-value hp-hero-value-sm" style={{ color: 'var(--color-blue)' }}>
                    {formatDuration(d.durationSec)}
                  </div>
                  <div className="hp-hero-title">时长</div>
                </div>
                <div className="hp-hero-divider" />
                <div className="hp-hero-box">
                  <div className="hp-hero-value hp-hero-value-sm" style={{ color: 'var(--color-success)' }}>
                    {d.totalBots}
                  </div>
                  <div className="hp-hero-title">机器人</div>
                </div>
                <div className="hp-hero-divider" />
                <div className="hp-hero-box">
                  <div className="hp-hero-value hp-hero-value-sm" style={{ color: 'var(--color-purple)' }}>
                    {d.finalSnapshot.totalActions}
                  </div>
                  <div className="hp-hero-title">动作</div>
                </div>
              </div>
            </div>
          </div>
        ))}
      </div>

      <div className="hp-glass hp-compare-table-wrap">
        <div className="hp-section-title">动作对比</div>
        <Table<ActionRow>
          rowKey="name"
          size="small"
          dataSource={rows}
          columns={columns}
          pagination={{ pageSize: 50, showSizeChanger: false }}
          scroll={{ x: 220 + data.length * 180, y: tableScrollY }}
        />
      </div>
    </div>
  );
}
