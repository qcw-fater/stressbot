/**
 * 延迟分位数可视化：把 P50/P90/P95/P99 渲染成横向 bar。
 *
 * 不是真正的分桶柱状（后端目前没有暴露原始桶数据），仅用 4 个分位点做可视提示。
 *
 * 注：自 v2 起，hist 中的 ms 值反映"纯网络往返"耗时（不含客户端构建/解析）。
 * 当 hist.count=0 时，说明该 action 没有真正进入 send→recv 窗口
 * （例如纯本地 setState、或 lua 内仅做 connect / set_secret_key），显示 —。
 */

import { Tooltip } from 'antd';
import type { HistogramView } from '@/types/api';

export interface LatencyHistogramProps {
  hist: HistogramView;
  /** 横轴最大刻度（用于把 P99 缩放到容器内）。默认按 hist.maxMs 自适应 */
  maxMs?: number;
  width?: number;
}

const P_COLOR: Record<string, string> = {
  p50: 'var(--color-success)',
  p90: 'var(--chart-lime)',
  p95: 'var(--color-warning)',
  p99: 'var(--color-error)',
};

export function LatencyHistogram({ hist, maxMs, width = 160 }: LatencyHistogramProps) {
  const { minMs, maxMs: histogramMaxMs, avgMs, p50Ms, p90Ms, p95Ms, p99Ms } = hist;
  if (
    hist.count === 0 ||
    minMs == null || histogramMaxMs == null || avgMs == null ||
    p50Ms == null || p90Ms == null || p95Ms == null || p99Ms == null
  ) {
    return <span style={{ color: 'var(--text-tertiary)', fontSize: 11 }}>—</span>;
  }
  const max = maxMs ?? Math.max(p99Ms, 1);
  const points: Array<{ key: keyof HistogramView; label: string; ms: number }> = [
    { key: 'p50Ms', label: 'p50', ms: p50Ms },
    { key: 'p90Ms', label: 'p90', ms: p90Ms },
    { key: 'p95Ms', label: 'p95', ms: p95Ms },
    { key: 'p99Ms', label: 'p99', ms: p99Ms },
  ];

  return (
    <Tooltip
      title={
        <div style={{ fontSize: 11 }}>
          <div>min: {minMs.toFixed(1)}ms · max: {histogramMaxMs.toFixed(1)}ms · avg: {avgMs.toFixed(1)}ms</div>
          {points.map((p) => (
            <div key={p.label}>
              {p.label}: {p.ms.toFixed(1)}ms
            </div>
          ))}
        </div>
      }
    >
      <div
        style={{
          position: 'relative',
          width,
          height: 14,
          background: 'var(--track-bg)',
          borderRadius: 4,
          overflow: 'hidden',
        }}
      >
        {points.map((p) => {
          const left = Math.min(100, (p.ms / max) * 100);
          return (
            <div
              key={p.label}
              style={{
                position: 'absolute',
                left: `calc(${left}% - 1px)`,
                top: 0,
                bottom: 0,
                width: 2,
                background: P_COLOR[p.label],
              }}
            />
          );
        })}
      </div>
    </Tooltip>
  );
}
