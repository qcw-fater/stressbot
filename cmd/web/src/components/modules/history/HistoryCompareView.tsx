/**
 * 历史对比视图：2~5 个任务并排比较关键指标。
 *
 * 展现策略：
 *   - 顶部 N 列玻璃摘要卡片：任务名 / hero 指标行（时长/机器人/动作）
 *   - 下方对比表格（glass 包裹）：每行一个动作，N 列 sampleCount/主指标 p99/apdex；
 *     best/worst 单元格着色高亮
 *   - 主指标口径（RTT / 等待 / 执行）按动作类别定，与实时动作表、节点徽章共用
 *     `monitoring/shared/actionKind` 的选取逻辑
 */

import { Empty, Spin, Table, Tag } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import { useEffect, useMemo, useState } from 'react';
import { historyApi, isAbortError, showApiError } from '@/services';
import type { CompareTarget } from '@/services/historyApi';
import type { HistoryCompareTask } from '@/types/api';
import { ApdexCell } from '@/components/monitoring/shared/ApdexCell';
import {
  PRIMARY_METRIC_LABEL,
  primaryMetricOfKind,
  resolveKind,
  strongestKind,
} from '@/components/monitoring/shared/actionKind';
import { formatStageLabel } from './stageLabel';
import './HistoryPanel.css';

/** 表格可视高度：跟随窗口但夹在 [200, 440]。 */
function tableScrollYOf(viewportHeight: number): number {
  return Math.min(440, Math.max(200, viewportHeight - 320));
}

/**
 * 直接存夹取后的高度，而不是存 window.innerHeight 再派生。
 *
 * resize 在拖窗口边框时每帧都触发，但夹取结果绝大多数时候不变；存派生值时 setState 传入
 * 相同数字 React 会自行跳过重渲染，于是不需要再加一层防抖计时器。
 */
function useTableScrollY(): number {
  const [scrollY, setScrollY] = useState(() =>
    tableScrollYOf(typeof window !== 'undefined' ? window.innerHeight : 720),
  );
  useEffect(() => {
    const onResize = () => setScrollY(tableScrollYOf(window.innerHeight));
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }, []);
  return scrollY;
}

export interface HistoryCompareViewProps {
  targets: CompareTarget[];
}

interface ActionRow {
  name: string;
  samples: (number | undefined)[];
  apdexes: (number | undefined)[];
  p99s: (number | undefined)[];
  /** 主指标口径标签（RTT / 等待 / 执行），整行一致，见 rows 的注释 */
  p99Label: string;
  /** 行内 best/worst 高亮集合，按行算一次而不是每个单元格重算 */
  p99Highlight: BestWorst;
  apdexHighlight: BestWorst;
}

interface BestWorst {
  best: Set<number>;
  worst: Set<number>;
}

function shortName(name: string): string {
  return name.length > 20 ? name.slice(0, 18) + '…' : name;
}

function formatDuration(sec: number): string {
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m${sec % 60}s`;
  return `${(sec / 3600).toFixed(1)}h`;
}

function bestWorst(values: (number | undefined)[], higherIsBetter: boolean): BestWorst {
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

export function HistoryCompareView({ targets }: HistoryCompareViewProps) {
  const [data, setData] = useState<HistoryCompareTask[] | null>(null);
  const [loading, setLoading] = useState(true);
  const tableScrollY = useTableScrollY();
  const targetsKey = useMemo(
    () =>
      targets.map((t) => (typeof t === 'string' ? t : `${t.id}:${t.stageIndex ?? -1}`)).join(','),
    [targets],
  );

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setData(null);
    historyApi
      .compareHistory(targets, { signal: controller.signal })
      .then((r) => {
        if (!controller.signal.aborted) setData(r.tasks);
      })
      .catch((err: unknown) => {
        if (controller.signal.aborted || isAbortError(err)) return;
        showApiError(err);
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [targetsKey]);

  const rows = useMemo<ActionRow[]>(() => {
    if (!data) return [];
    const allActions = new Set<string>();
    for (const d of data) {
      for (const a of d.finalSnapshot.actions) allActions.add(a.name);
    }
    const out: ActionRow[] = [];
    for (const name of allActions) {
      const found = data.map((d) => d.finalSnapshot.actions.find((x) => x.name === name));
      const present = found.filter((a): a is NonNullable<typeof a> => !!a);
      // 主指标口径按整行定一次（取各次运行中语义最强的类别），而不是逐列各自推断：
      // 监听/发送/本地类原来一律读 rtt，整行显示 —，和实时动作表、节点徽章对不上；
      // 逐列推断又会让「这一次没留下 RTT 样本」的列悄悄换成另一个直方图去比。
      const kind = strongestKind(present) ?? 'local';
      const samples: (number | undefined)[] = [];
      const apdexes: (number | undefined)[] = [];
      const p99s: (number | undefined)[] = [];
      for (const a of found) {
        samples.push(a?.sampleCount);
        // 只有往返类有 Apdex；其余类别留空，避免 0 分被读成「很差」。
        apdexes.push(a && resolveKind(a) === 'networked' ? a.rttApdex : undefined);
        if (!a) {
          p99s.push(undefined);
          continue;
        }
        const primary = primaryMetricOfKind(a, kind);
        p99s.push(primary.sampleCount > 0 ? (primary.histogram?.p99Ms ?? undefined) : undefined);
      }
      out.push({
        name,
        samples,
        apdexes,
        p99s,
        p99Label: PRIMARY_METRIC_LABEL[kind],
        p99Highlight: bestWorst(p99s, false),
        apdexHighlight: bestWorst(apdexes, true),
      });
    }
    out.sort(
      (a, b) =>
        (b.samples.find((s) => s !== undefined) ?? 0) -
        (a.samples.find((s) => s !== undefined) ?? 0),
    );
    return out;
  }, [data]);

  // 列定义只随对比目标变化。不 memo 的话每次渲染都换一批新的 render 闭包，
  // antd Table 会把所有单元格全量重渲染一遍——而这个视图光是窗口 resize 就会重渲染。
  const columns = useMemo<ColumnsType<ActionRow>>(() => {
    const cols: ColumnsType<ActionRow> = [
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
    for (let i = 0; i < (data?.length ?? 0); i++) {
      cols.push({
        title: <code>{shortName(data![i].name)}</code>,
        key: `t${i}`,
        width: 180,
        render: (_, r) => (
          <div style={{ fontSize: 11, lineHeight: 1.6 }}>
            <div className="hp-compare-cell">样本: {r.samples[i] ?? '—'}</div>
            <div
              className={`hp-compare-cell${r.p99Highlight.best.has(i) ? ' hp-compare-cell--best' : ''}${r.p99Highlight.worst.has(i) ? ' hp-compare-cell--worst' : ''}`}
            >
              {r.p99Label} p99: {r.p99s[i] !== undefined ? `${r.p99s[i]!.toFixed(0)}ms` : '—'}
            </div>
            <div
              className={`hp-compare-cell${r.apdexHighlight.best.has(i) ? ' hp-compare-cell--best' : ''}${r.apdexHighlight.worst.has(i) ? ' hp-compare-cell--worst' : ''}`}
            >
              Apdex: <ApdexCell value={r.apdexes[i]} />
            </div>
          </div>
        ),
      });
    }
    return cols;
  }, [data]);

  const tableScroll = useMemo(
    () => ({ x: 220 + (data?.length ?? 0) * 180, y: tableScrollY }),
    [data, tableScrollY],
  );

  if (loading) return <Spin />;
  if (!data || data.length === 0) return <Empty description="加载失败" />;

  return (
    <div className="hp-compare-root">
      <div
        className="hp-compare-cards"
        style={{ gridTemplateColumns: `repeat(${data.length}, 1fr)` }}
      >
        {data.map((d, i) => (
          <div
            key={`${d.id}:${d.stageIndex ?? -1}`}
            className="hp-glass hp-compare-card"
            data-index={i}
          >
            <div className="hp-compare-card__title">
              <span style={{ marginRight: 6 }}>#{i + 1}</span>
              {d.name}
              {(d.stageIndex ?? -1) > 0 && (
                <Tag color="warning" style={{ marginLeft: 6 }}>
                  {formatStageLabel(d.stageLabel, d.stageIndex)}
                </Tag>
              )}
            </div>
            <div className="hp-compare-card__meta">
              <code>{d.id.slice(0, 8)}</code> ·{' '}
              {d.startedAt ? dayjs(d.startedAt).format('MM-DD HH:mm') : '—'}
            </div>
            <div className="hp-compare-card__hero">
              <div className="hp-hero-row">
                <div className="hp-hero-box">
                  <div
                    className="hp-hero-value hp-hero-value-sm"
                    style={{ color: 'var(--color-blue)' }}
                  >
                    {formatDuration(d.durationSec)}
                  </div>
                  <div className="hp-hero-title">时长</div>
                </div>
                <div className="hp-hero-divider" />
                <div className="hp-hero-box">
                  <div
                    className="hp-hero-value hp-hero-value-sm"
                    style={{ color: 'var(--color-success)' }}
                  >
                    {d.totalBots}
                  </div>
                  <div className="hp-hero-title">机器人</div>
                </div>
                <div className="hp-hero-divider" />
                <div className="hp-hero-box">
                  <div
                    className="hp-hero-value hp-hero-value-sm"
                    style={{ color: 'var(--color-purple)' }}
                  >
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
          scroll={tableScroll}
        />
      </div>
    </div>
  );
}
