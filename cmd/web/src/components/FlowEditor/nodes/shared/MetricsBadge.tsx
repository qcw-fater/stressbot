/**
 * 节点上的实时压测指标徽章。
 *
 * 数据形态直接是 `ActionMetric`（与后端 `monitor.ActionMetric` 对齐），
 * 由 HomeShell 把 StressSnapshot.actions[] 通过 `metricsBinding.buildNodeMetricsMap`
 * 转成 `nodeId → ActionMetric` 映射后注入。
 *
 * 显示策略（紧凑、4 个数字最多）：
 *   - 当前并发：`exec N`（最重要，0 时不显示，避免遮蔽未跑节点）
 *   - p99：`p99 12ms`（仅 successCount > 0 才显示）
 *   - apdex：`A 0.92`（彩色文字按阈值染色）
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

/** 节点指标 provider 类型；HomeShell 注入 */
export type MetricsProvider = (nodeId: string) => ActionMetric | undefined;

interface MetricsState {
  provider?: MetricsProvider;
  setProvider: (p: MetricsProvider | undefined) => void;
}

export const useMetricsStore = create<MetricsState>((set) => ({
  setProvider: (p) => set({ provider: p }),
}));

export function useNodeMetrics(nodeId: string): ActionMetric | undefined {
  const provider = useMetricsStore((s) => s.provider);
  return provider ? provider(nodeId) : undefined;
}

/** 将 ActionMetric 的 apdex 转成等级，用于节点边框染色 */
export function useNodeApdexLevel(nodeId: string): ApdexLevel {
  const m = useNodeMetrics(nodeId);
  return classifyApdex(m?.apdex);
}

const APDEX_COLOR: Record<ApdexLevel, string> = {
  excellent: 'var(--color-success)',
  good: '#a0d911',
  fair: 'var(--color-warning)',
  poor: 'var(--color-orange)',
  danger: 'var(--color-error)',
  unknown: 'var(--text-tertiary)',
};

export interface MetricsBadgeProps {
  nodeId: string;
}

export function MetricsBadge({ nodeId }: MetricsBadgeProps) {
  const m = useNodeMetrics(nodeId);
  if (!m) return null;

  const apdexLevel = classifyApdex(m.apdex);
  const apdexColor = APDEX_COLOR[apdexLevel];
  const showP99 = m.latency.count > 0;
  const showErrors = (m.errors?.length ?? 0) > 0;
  const topErr = showErrors ? m.errors![0] : undefined;

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
        <Tooltip title={`avg ${m.latency.avgMs.toFixed(1)} · p95 ${m.latency.p95Ms.toFixed(1)} · p99 ${m.latency.p99Ms.toFixed(1)} · max ${m.latency.maxMs.toFixed(1)} (ms)`}>
          <span className="pattern-badge" style={{ color: 'var(--node-continue)', borderColor: 'var(--node-continue)', background: 'color-mix(in srgb, var(--node-continue) 12%, transparent)' }}>
            p99 {formatMs(m.latency.p99Ms)}
          </span>
        </Tooltip>
      )}
      {apdexLevel !== 'unknown' && (
        <Tooltip
          title={`Apdex ${m.apdex.toFixed(3)} · 成功率 ${(m.successRate * 100).toFixed(1)}% · 平均 QPS ${m.avgQps.toFixed(1)} · 样本数 ${m.sampleCount}`}
        >
          <span className="pattern-badge" style={{ color: apdexColor, borderColor: apdexColor, background: `color-mix(in srgb, ${apdexColor} 12%, transparent)` }}>
            Apdex {m.apdex.toFixed(2)}
          </span>
        </Tooltip>
      )}
      {showErrors && topErr && (
        <Tooltip
          title={
            <div style={{ maxWidth: 300 }}>
              {m.errors!.slice(0, 6).map((e) => (
                <div key={e.msg} style={{ fontSize: 11 }}>
                  <span style={{ color: 'var(--color-error)' }}>×{e.count}</span> {e.msg}
                </div>
              ))}
              {m.errors!.length > 6 && (
                <div style={{ fontSize: 10, opacity: 0.6, marginTop: 4 }}>
                  …还有 {m.errors!.length - 6} 类错误
                </div>
              )}
            </div>
          }
        >
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
