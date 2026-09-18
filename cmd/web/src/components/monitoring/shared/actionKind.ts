/**
 * 动作分类与「主指标」选取（纯函数，无 React / antd 依赖）。
 *
 * 为什么必须共用一份：同一个动作会同时出现在流程节点徽章、实时动作表、历史详情、
 * 历史对比和离线报告里。一旦各处自己决定「这个动作的 p99 看哪个直方图」，就会出现
 * 同一个动作在两个面板上一个有数、一个显示 —，或者两个都有数但口径不同——读的人无从
 * 判断哪个是真的。
 *
 * 口径与后端 `monitor` 的 `classifyActionKind` 对齐：往返类看 RTT、监听类看等待时长、
 * 发送/本地类看执行耗时（wallClock）。总耗时只作诊断，不参与主指标。
 */

/** 动作的网络语义分类，由后端按运行期实际行为判定。 */
export type ActionKindLike = 'networked' | 'listen' | 'send' | 'local';

export interface ActionHistogramLike {
  maxMs: number | null;
  avgMs: number | null;
  p50Ms: number | null;
  p95Ms: number | null;
  p99Ms: number | null;
}

/** 分类与主指标选取所需的最小字段集，实时快照与历史归档都满足。 */
export interface ActionKindSource {
  kind?: ActionKindLike;
  rtt?: ActionHistogramLike;
  rttSampleCount?: number;
  rttApdexSampleCount?: number;
  listenWait?: ActionHistogramLike;
  listenWaitSampleCount?: number;
  totalDuration?: ActionHistogramLike;
  totalDurationSampleCount?: number;
  avgSendBytes: number;
}

export interface SelectedLatencyMetric {
  histogram?: ActionHistogramLike;
  sampleCount: number;
  /** 该指标的口径标签，用于列头、徽章与 CSV 表头 */
  label: string;
}

/** 强弱顺序，与后端「取最强语义」一致：有往返就是往返，其次监听、发送，最后本地。 */
const KIND_STRENGTH: Record<ActionKindLike, number> = {
  networked: 3,
  listen: 2,
  send: 1,
  local: 0,
};

/**
 * 取行的类别。kind 是后端新增字段，改造之前归档的历史快照里没有，
 * 缺失时按与后端 classifyActionKind 相同的判据就地推断——否则老报告会整表退化成
 * 无色 + 主指标一律显示 wallClock，连本来有 RTT 的往返动作也看不到 RTT。
 */
export function resolveKind(row: ActionKindSource): ActionKindLike {
  if (row.kind) return row.kind;
  if ((row.rttApdexSampleCount ?? 0) > 0) return 'networked';
  if ((row.rttSampleCount ?? 0) > 0) return 'networked';
  if ((row.listenWaitSampleCount ?? 0) > 0) return 'listen';
  if (row.avgSendBytes > 0) return 'send';
  return 'local';
}

/**
 * 多次运行同一个动作时取语义最强的类别。
 *
 * 用于历史对比：某一次跑挂在连接阶段可能一条 RTT 样本都没留下，按它自己的样本推断会得到
 * 'send'。逐列各按自己的类别取直方图，横向比较的就是两个不同口径的数了。
 */
export function strongestKind(rows: readonly ActionKindSource[]): ActionKindLike | undefined {
  let best: ActionKindLike | undefined;
  for (const row of rows) {
    const kind = resolveKind(row);
    if (!best || KIND_STRENGTH[kind] > KIND_STRENGTH[best]) best = kind;
  }
  return best;
}

/** 各类别主指标的口径标签。展示处必须带上它，否则读的人无从判断这个 p99 是什么。 */
export const PRIMARY_METRIC_LABEL: Record<ActionKindLike, string> = {
  networked: 'RTT',
  listen: '等待',
  send: '执行',
  local: '执行',
};

/** 按给定类别取主指标口径（类别已知时用，避免逐列重复推断）。 */
export function primaryMetricOfKind(
  row: ActionKindSource,
  kind: ActionKindLike,
): SelectedLatencyMetric {
  const label = PRIMARY_METRIC_LABEL[kind];
  switch (kind) {
    case 'networked':
      return { histogram: row.rtt, sampleCount: row.rttSampleCount ?? 0, label };
    case 'listen':
      return { histogram: row.listenWait, sampleCount: row.listenWaitSampleCount ?? 0, label };
    default:
      return {
        histogram: row.totalDuration,
        sampleCount: row.totalDurationSampleCount ?? 0,
        label,
      };
  }
}

/** 按动作自身类别取它的主指标口径。 */
export function selectPrimaryMetric(row: ActionKindSource): SelectedLatencyMetric {
  return primaryMetricOfKind(row, resolveKind(row));
}
