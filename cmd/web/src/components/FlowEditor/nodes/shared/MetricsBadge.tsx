/**
 * 节点上的实时压测指标徽章。
 *
 * 数据形态直接是 `ActionMetric`（与后端 `monitor.ActionMetric` 对齐），
 * 由 HomeShell 把 StressSnapshot.actions[] 通过 `metricsBinding.buildNodeMetricsMap`
 * 转成 `nodeId → ActionMetric` 映射后注入。
 *
 * 显示策略（紧凑、4 个数字最多）：
 *   - 当前并发：`exec N`（最重要，0 时不显示，避免遮蔽未跑节点）
 *   - p99：`p99 12ms`（仅网络动作、successCount > 0 才显示）
 *   - apdex：`A 0.92`（仅网络动作，彩色文字按阈值染色）
 *   - 本地成功：`✓ 150`（非网络动作如 setState/send，绿色徽章表示已执行成功）
 *   - 错误条：`err N×Top`（hover 看完整 error 列表）
 *
 * 节点边框的 Apdex 染色由 NodeShell 读取 `apdexLevel(...)` 后挂 `apdex-*` className 完成，
 * MetricsBadge 自己只负责数字显示。
 */

import { create } from 'zustand';
import { Tooltip } from 'antd';
import { WarningOutlined } from '@ant-design/icons';
import type { ActionMetric } from '@/types/api';
import { classifyApdex, type ApdexLevel } from '@/services/metricsBinding';

interface MetricsState {
  metrics: ReadonlyMap<string, ActionMetric>;
  setMetrics: (metrics: ReadonlyMap<string, ActionMetric> | undefined) => void;
}

const EMPTY_METRICS: ReadonlyMap<string, ActionMetric> = new Map();

export const useMetricsStore = create<MetricsState>()((set) => ({
  metrics: EMPTY_METRICS,
  setMetrics: (metrics) => set({ metrics: metrics ?? EMPTY_METRICS }),
}));

export function useNodeMetrics(nodeId: string): ActionMetric | undefined {
  return useMetricsStore((state) => state.metrics.get(nodeId));
}

const LEVEL_ORDER: Record<ApdexLevel, number> = {
  unknown: 0, danger: 1, poor: 2, fair: 3, good: 4, excellent: 5,
};

/** 取两个等级中较差的那个 */
function worseLevel(a: ApdexLevel, b: ApdexLevel): ApdexLevel {
  return LEVEL_ORDER[a] <= LEVEL_ORDER[b] ? a : b;
}

/** 成功率 → 健康等级（阈值与 Apdex 对齐：0.94/0.85/0.70/0.50） */
function classifySuccessRate(rate: number): ApdexLevel {
  if (rate >= 0.94) return 'excellent';
  if (rate >= 0.85) return 'good';
  if (rate >= 0.70) return 'fair';
  if (rate >= 0.50) return 'poor';
  return 'danger';
}

/** 节点边框染色：综合 Apdex 和成功率，取较差值。
 *  - 未执行（sampleCount=0）→ unknown（无染色）
 *  - 网络动作：Apdex 等级 vs 成功率等级，取较差
 *  - 非网络动作：仅看成功率 */
export function getNodeApdexLevel(m: ActionMetric | undefined): ApdexLevel {
  if (!m || m.sampleCount === 0) return 'unknown';

  const srLevel = classifySuccessRate(m.successRate);

  if (m.totalDurationSampleCount === 0) {
    return srLevel;
  }
  return worseLevel(classifyApdex(m.totalDurationApdex), srLevel);
}

const APDEX_COLOR: Record<ApdexLevel, string> = {
  excellent: 'var(--color-success)',
  good: 'var(--chart-lime)',
  fair: 'var(--color-warning)',
  poor: 'var(--chart-orange)',
  danger: 'var(--color-error)',
  unknown: 'var(--text-tertiary)',
};

export interface MetricsBadgeProps {
  metric?: ActionMetric;
}

export function MetricsBadge({ metric: m }: MetricsBadgeProps) {
  if (!m) return null;

  const hasTotalDuration = m.totalDurationSampleCount > 0;
  const hasNet = m.rttSampleCount > 0;
  const showP99 = hasTotalDuration && m.totalDuration.count > 0;
  const apdexLevel = hasTotalDuration ? classifyApdex(m.totalDurationApdex) : 'unknown';
  const apdexColor = APDEX_COLOR[apdexLevel];
  // 健康等级用于无总耗时样本动作徽章颜色（综合成功率）
  const healthLevel = (m.sampleCount > 0 && !hasTotalDuration) ? classifySuccessRate(m.successRate) : 'unknown';
  const healthColor = APDEX_COLOR[healthLevel];
  // 有总耗时样本的动作显示总耗时 Apdex；否则显示成功计数（样式统一，文本诚实）
  const showApdex = hasTotalDuration && apdexLevel !== 'unknown';
  const showLocalBadge = !hasTotalDuration && m.sampleCount > 0;
  const showErrors = (m.errors?.length ?? 0) > 0;
  const topErr = showErrors ? m.errors![0] : undefined;

  // 预构建错误列表，避免 JSX 内联 .map() + 条件渲染触发 React key 警告
  const errorTooltipContent = showErrors ? (
    <div style={{ maxWidth: 360 }}>
      {m.errors!.slice(0, 6).map((e, i) => (
        <div key={`e${i}`} style={{ marginTop: i > 0 ? 3 : 0, fontSize: 11, lineHeight: '16px' }}>
          <span style={{ color: 'var(--color-error)', fontWeight: 700, fontSize: 10, fontVariantNumeric: 'tabular-nums', marginRight: 6 }}>×{e.count}</span>
          <span style={{ fontWeight: 500 }}>{e.codeName || `#${e.code}`}</span>
          {e.msgs.length > 0 && (
            <div style={{ color: 'var(--text-tertiary)', marginLeft: 14, marginTop: 1 }}>{e.msgs.join('; ')}</div>
          )}
        </div>
      ))}
      {m.errors!.length > 6 && (
        <div style={{ fontSize: 10, opacity: 0.6, marginTop: 4 }}>
          …还有 {m.errors!.length - 6} 类错误
        </div>
      )}
    </div>
  ) : null;

  return (
    <div
      className="metrics-slot"
      style={{
        display: 'flex',
        flexWrap: 'wrap',
        gap: 4,
        marginTop: 8,
        paddingTop: 6,
        borderTop: '1px dashed var(--border-color)',
      }}
    >
      {showP99 && (
        <Tooltip title={`总耗时 avg ${m.totalDuration.avgMs.toFixed(1)} · p95 ${m.totalDuration.p95Ms.toFixed(1)} · p99 ${m.totalDuration.p99Ms.toFixed(1)} · max ${m.totalDuration.maxMs.toFixed(1)} (ms)${hasNet ? ` · RTT p99 ${m.rtt.p99Ms.toFixed(1)}ms` : ''}`}>
          <span className="pattern-badge" style={{ color: 'var(--node-continue)', borderColor: 'var(--node-continue)', background: 'color-mix(in srgb, var(--node-continue) 12%, transparent)' }}>
            p99 {formatMs(m.totalDuration.p99Ms)}
          </span>
        </Tooltip>
      )}
      {showApdex && (
        <Tooltip
          title={`总耗时 Apdex ${m.totalDurationApdex.toFixed(3)}${hasNet ? ` · RTT Apdex ${m.rttApdex.toFixed(3)}` : ''} · 成功率 ${(m.successRate * 100).toFixed(1)}% · 平均 QPS ${m.avgQps.toFixed(1)} · 样本数 ${m.sampleCount}`}
        >
          <span className="pattern-badge" style={{ color: apdexColor, borderColor: apdexColor, background: `color-mix(in srgb, ${apdexColor} 12%, transparent)` }}>
            Apdex {m.totalDurationApdex.toFixed(2)}
          </span>
        </Tooltip>
      )}
      {showLocalBadge && (
        <Tooltip title={`成功 ${m.successCount} 次 · 成功率 ${(m.successRate * 100).toFixed(1)}% · 平均 QPS ${m.avgQps.toFixed(1)}`}>
          <span className="pattern-badge" style={{ color: healthColor, borderColor: healthColor, background: `color-mix(in srgb, ${healthColor} 12%, transparent)` }}>
            ✓ {m.successCount}
          </span>
        </Tooltip>
      )}
      {showErrors && topErr && (
        <Tooltip title={errorTooltipContent}>
          <span className="pattern-badge" style={{ color: 'var(--color-error)', borderColor: 'var(--color-error)', background: 'color-mix(in srgb, var(--color-error) 12%, transparent)' }}>
            <WarningOutlined style={{ marginRight: 4 }} /> {topErr.count}
          </span>
        </Tooltip>
      )}
    </div>
  );
}

function formatMs(ms: number): string {
  if (ms < 10) return ms.toFixed(1) + 'ms';
  if (ms < 1000) return Math.round(ms) + 'ms';
  return (ms / 1000).toFixed(2) + 's';
}
