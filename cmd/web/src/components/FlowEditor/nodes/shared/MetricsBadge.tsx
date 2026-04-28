/**
 * 监控数据徽章。
 *
 * 设计文档 §12：通过 metricsProvider 注入实时数据。未注入时仅占位。
 */

import { create } from 'zustand';

export interface NodeMetrics {
  entered?: number;
  avgMs?: number;
  p95Ms?: number;
  failRate?: number;
}

export type MetricsProvider = (nodeId: string) => NodeMetrics | undefined;

interface MetricsState {
  provider?: MetricsProvider;
  setProvider: (p: MetricsProvider | undefined) => void;
}

export const useMetricsStore = create<MetricsState>((set) => ({
  setProvider: (p) => set({ provider: p }),
}));

export function useNodeMetrics(nodeId: string): NodeMetrics | undefined {
  const provider = useMetricsStore((s) => s.provider);
  return provider ? provider(nodeId) : undefined;
}

export interface MetricsBadgeProps {
  nodeId: string;
}

export function MetricsBadge({ nodeId }: MetricsBadgeProps) {
  const metrics = useNodeMetrics(nodeId);
  if (!metrics) {
    // 未注入 provider 时不占空间，避免节点显得空旷
    return null;
  }
  return (
    <div
      className="metrics-slot"
      style={{
        display: 'flex',
        gap: 8,
        marginTop: 6,
        paddingTop: 4,
        borderTop: '1px dashed rgba(0,0,0,0.06)',
        fontSize: 10,
        color: 'var(--text-tertiary)',
      }}
    >
      {typeof metrics.entered === 'number' && <span>进入: {metrics.entered}</span>}
      {typeof metrics.avgMs === 'number' && <span>平均: {metrics.avgMs}ms</span>}
      {typeof metrics.p95Ms === 'number' && <span>p95: {metrics.p95Ms}ms</span>}
      {typeof metrics.failRate === 'number' && (
        <span style={{ color: metrics.failRate > 0.05 ? '#f5222d' : '#52c41a' }}>
          失败: {(metrics.failRate * 100).toFixed(1)}%
        </span>
      )}
    </div>
  );
}
