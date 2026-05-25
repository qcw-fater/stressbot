/**
 * 报告 HTML 模板：纯函数组件，输出原始 HTML（不用 antd）。
 * 由 renderToStaticMarkup 渲染为字符串，嵌入独立 HTML 文档。
 */

import type {
  HistoryDetail,
  ActionMetric,
  TimeseriesPoint,
  ClusterSystemSnapshot,
} from '@/types/api';
import { computeWeightedMetrics, classifyApdex } from '@/services/metricsBinding';
import { fmtMs, fmtBytesPlain } from '@/components/monitoring/shared/formats';
import type { ChartImages } from './reportCharts';

export interface ReportHtmlProps {
  detail: HistoryDetail;
  timeseries: { stress: TimeseriesPoint[]; system: TimeseriesPoint[] };
  charts: ChartImages;
}

export function ReportHtml({ detail, charts }: ReportHtmlProps) {
  const actions = detail.finalSnapshot?.actions ?? [];
  const sys = detail.finalSystem;
  const cs = detail.configSummary;
  const failed = detail.state === 'failed';
  const conn = detail.finalSnapshot?.connections ?? { established: 0, failed: 0, dropped: 0 };
  const wm = computeWeightedMetrics(actions);
  const totalQps = actions.reduce((s, a) => s + a.avgQps, 0);

  return (
    <div>
      {/* 打印提示 */}
      <div className="print-hint no-print">
        <span>报告已生成，按 <kbd>Ctrl+P</kbd> 保存为 PDF</span>
      </div>

      {/* Section 1: 封面 */}
      <CoverSection detail={detail} failed={failed} cs={cs} />

      {/* Section 2: KPI */}
      <KpiSection actions={actions} sys={sys} conn={conn} wm={wm} totalQps={totalQps} />

      {/* Section 3: 趋势 2×2 */}
      <div className="report-section">
        <div className="section-title">运行趋势</div>
        <div className="trends-grid">
          {charts.qps && (
            <div className="trends-grid__cell">
              <div className="trends-grid__label">QPS</div>
              <img src={charts.qps} alt="QPS" />
            </div>
          )}
          {charts.apdexTrend && (
            <div className="trends-grid__cell">
              <div className="trends-grid__label">Apdex</div>
              <img src={charts.apdexTrend} alt="Apdex" />
            </div>
          )}
          {charts.cpu && (
            <div className="trends-grid__cell">
              <div className="trends-grid__label">CPU%</div>
              <img src={charts.cpu} alt="CPU" />
            </div>
          )}
          {charts.bandwidth && (
            <div className="trends-grid__cell">
              <div className="trends-grid__label">带宽 (KB/s)</div>
              <img src={charts.bandwidth} alt="带宽" />
            </div>
          )}
        </div>
        {!charts.qps && !charts.apdexTrend && !charts.cpu && !charts.bandwidth && (
          <div className="chart-empty">无时序数据</div>
        )}
      </div>

      {/* Section 4: 性能排行 */}
      <ChartSection title="动作性能排行 (p99)" image={charts.ranking} empty="无动作数据" />

      {/* Section 5: 延迟分布 */}
      <ChartSection title="延迟分布" image={charts.latency} empty="无动作数据" />

      {/* Section 6: 成功/失败构成 */}
      <div className="report-section">
        <div className="section-title">成功 / 失败构成</div>
        <div style={{ display: 'flex', gap: 24, alignItems: 'flex-start' }}>
          <div className="chart-wrap" style={{ flex: '0 0 350px' }}>
            {charts.successDonut ? (
              <img src={charts.successDonut} alt="成功/失败构成" />
            ) : (
              <div className="chart-empty">无数据</div>
            )}
          </div>
          <FailureSummaryTable actions={actions} />
        </div>
      </div>

      {/* Section 7: Apdex 分布 */}
      <ChartSection title="Apdex 分布" image={charts.apdexBar} empty="无动作数据" />

      {/* Section 8: 错误分析 */}
      {charts.errors && (
        <ErrorSection actions={actions} errorChart={charts.errors} />
      )}

      {/* Section 9: 集群状态 */}
      <SystemSection sys={sys} />

      {/* Section 10: 节点结果 */}
      {detail.agentReports && detail.agentReports.length > 0 && (
        <AgentSection detail={detail} />
      )}

      {/* Section 11: 动作详表 */}
      <DetailTableSection actions={actions} />

      {/* Footer */}
      <div className="report-footer">
        <span>stressbot 压测报告</span>
        <span>
          {new Date().toLocaleString('zh-CN')} · 任务 {detail.id.slice(0, 8)}
        </span>
      </div>
    </div>
  );
}

/* ── 子组件 ── */

function CoverSection({ detail, failed, cs }: { detail: HistoryDetail; failed: boolean; cs: HistoryDetail['configSummary'] }) {
  return (
    <div className="report-section report-cover">
      <div className="report-cover__logo">stressbot 压测报告</div>
      <div className="report-cover__name">{detail.name}</div>
      <div className="report-cover__meta">
        <code>{detail.id.slice(0, 8)}</code>
        {' · '}
        <span className={`badge ${failed ? 'badge--fail' : 'badge--ok'}`}>
          {failed ? '失败' : '完成'}
        </span>
        {' · '}
        {formatDuration(detail.durationSec)}
        <br />
        {detail.startedAt && (
          <>
            {formatTime(detail.startedAt)} → {detail.stoppedAt ? formatTime(detail.stoppedAt) : '—'}
            <br />
          </>
        )}
        <b>{detail.totalBots}</b> 机器人 · <b>{detail.activeAgentCount}/{detail.agentCount}</b> 节点 · 并发 <b>{cs.concurrency}</b> · 超时 <b>{cs.timeoutSec}s</b>
        {' · '}流程 <b>{cs.flowSizeKB}KB</b> · <b>{cs.protoCount}</b> Proto · <b>{cs.scriptCount}</b> 脚本
      </div>
      {detail.errorMsg && (
        <div className="report-cover__error">{detail.errorMsg}</div>
      )}
    </div>
  );
}

function KpiSection({
  actions, sys, conn, wm, totalQps,
}: {
  actions: ActionMetric[];
  sys: ClusterSystemSnapshot | null;
  conn: { established: number; failed: number; dropped: number };
  wm: { totalSamples: number; apdex: number; successRate: number };
  totalQps: number;
}) {
  const totalActions = actions.reduce((s, a) => s + a.sampleCount, 0);
  const memPct = sys && sys.totalMemMB > 0 ? ((sys.usedMemMB / sys.totalMemMB) * 100).toFixed(1) : '—';

  const kpis = [
    { value: totalActions.toLocaleString(), label: '累计动作' },
    { value: wm.apdex.toFixed(3), label: '整体 Apdex' },
    { value: (wm.successRate * 100).toFixed(1) + '%', label: '成功率' },
    { value: totalQps.toFixed(1), label: '总 QPS' },
    { value: conn.established.toLocaleString(), label: '建连数' },
    { value: conn.failed > 0 ? `${conn.failed} / ${conn.dropped}` : `${conn.failed}`, label: '错误连接' },
    { value: sys ? `${sys.avgCpuPercent.toFixed(1)}%` : '—', label: '平均 CPU' },
    { value: memPct + '%', label: '内存使用' },
  ];

  return (
    <div className="report-section">
      <div className="section-title">执行概要</div>
      <div className="kpi-grid">
        {kpis.map((k) => (
          <div key={k.label} className="kpi-card">
            <div className="kpi-value">{k.value}</div>
            <div className="kpi-label">{k.label}</div>
          </div>
        ))}
      </div>
    </div>
  );
}

function ChartSection({ title, image, empty }: { title: string; image: string | null; empty: string }) {
  return (
    <div className="report-section">
      <div className="section-title">{title}</div>
      <div className="chart-wrap">
        {image ? <img src={image} alt={title} /> : <div className="chart-empty">{empty}</div>}
      </div>
    </div>
  );
}

function FailureSummaryTable({ actions }: { actions: ActionMetric[] }) {
  const problemActions = actions.filter(
    (a) => a.failureCount > 0 || a.timeoutCount > 0,
  );
  if (problemActions.length === 0) {
    return (
      <div style={{ flex: 1 }}>
        <div style={{ fontSize: 12, color: 'var(--color-success)', fontWeight: 600 }}>
          所有动作均无失败或超时
        </div>
      </div>
    );
  }

  return (
    <div style={{ flex: 1, minWidth: 0 }}>
      <table className="report-table">
        <thead>
          <tr>
            <th>动作</th>
            <th className="num">失败</th>
            <th className="num">超时</th>
            <th className="num">失败率</th>
          </tr>
        </thead>
        <tbody>
          {problemActions.slice(0, 10).map((a) => {
            const rate = a.sampleCount > 0 ? ((a.failureCount / a.sampleCount) * 100).toFixed(1) : '0';
            return (
              <tr key={a.name}>
                <td><code>{a.name}</code></td>
                <td className="num c-error">{a.failureCount}</td>
                <td className="num c-warning">{a.timeoutCount}</td>
                <td className="num">{rate}%</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function ErrorSection({ actions, errorChart }: { actions: ActionMetric[]; errorChart: string }) {
  const allErrors: Array<{ action: string; name: string; msg: string; count: number }> = [];
  for (const a of actions) {
    for (const e of a.errors ?? []) {
      const name = e.codeName || `${e.kind}#${e.code}`;
      allErrors.push({ action: a.name, name, msg: e.msgs.join('; '), count: e.count });
    }
  }
  allErrors.sort((a, b) => b.count - a.count);

  return (
    <div className="report-section">
      <div className="section-title">错误分析</div>
      <div className="chart-wrap">
        <img src={errorChart} alt="错误分布" />
      </div>
      <div style={{ marginTop: 12 }}>
        {allErrors.slice(0, 15).map((e, i) => (
          <div key={i} className="error-row">
            <span className="error-count">×{e.count}</span>
            <span className="error-action">{e.action}</span>
            <span className="error-name">{e.name}</span>
            {e.msg && <span className="error-msg">{e.msg}</span>}
          </div>
        ))}
      </div>
    </div>
  );
}

function SystemSection({ sys }: { sys: ClusterSystemSnapshot | null }) {
  if (!sys) return null;

  const items = [
    { label: 'CPU 平均', value: `${sys.avgCpuPercent.toFixed(1)}%` },
    { label: 'CPU 最大', value: `${sys.maxCpuPercent.toFixed(1)}%${sys.hotAgentName ? ` (${sys.hotAgentName})` : ''}` },
    { label: '内存', value: `${(sys.usedMemMB / 1024).toFixed(1)} / ${(sys.totalMemMB / 1024).toFixed(1)} GB` },
    { label: '网络↑', value: fmtBytesPlain(sys.totalNetSendKBps * 1024) + '/s' },
    { label: '网络↓', value: fmtBytesPlain(sys.totalNetRecvKBps * 1024) + '/s' },
    { label: 'Goroutine', value: sys.totalGoroutines.toLocaleString() },
    { label: 'Thread', value: sys.totalThreads.toLocaleString() },
    { label: 'FD', value: sys.totalFds.toLocaleString() },
  ];

  return (
    <div className="report-section">
      <div className="section-title">集群状态</div>
      <div className="sys-grid">
        {items.map((it) => (
          <div key={it.label} className="sys-card">
            <div className="sys-card__label">{it.label}</div>
            <div className="sys-card__value">{it.value}</div>
          </div>
        ))}
      </div>
    </div>
  );
}

function AgentSection({ detail }: { detail: HistoryDetail }) {
  return (
    <div className="report-section">
      <div className="section-title">节点结果</div>
      <table className="report-table">
        <thead>
          <tr>
            <th>节点</th>
            <th>结果</th>
            <th>完成时间</th>
            <th>错误信息</th>
          </tr>
        </thead>
        <tbody>
          {detail.agentReports!.map((r, i) => {
            const resultMap: Record<string, string> = {
              completed: '完成',
              stopped: '停止',
              failed: '失败',
            };
            const colorMap: Record<string, string> = {
              completed: 'c-success',
              stopped: '',
              failed: 'c-error',
            };
            return (
              <tr key={i}>
                <td>{r.agentName || r.agentId}</td>
                <td className={colorMap[r.result] ?? ''}>{resultMap[r.result] ?? r.result}</td>
                <td>{r.finishedAt ? formatTime(r.finishedAt) : '—'}</td>
                <td className={r.errorMsg ? 'c-error' : ''}>{r.errorMsg || '—'}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function DetailTableSection({ actions }: { actions: ActionMetric[] }) {
  if (actions.length === 0) return null;

  const sorted = [...actions].sort((a, b) => b.sampleCount - a.sampleCount);

  return (
    <div className="report-section">
      <div className="section-title">动作详情表</div>
      <table className="report-table report-table--detail">
        <thead>
          <tr>
            <th>动作</th>
            <th className="num">样本</th>
            <th className="num">失败</th>
            <th className="num">超时</th>
            <th className="num">Apdex</th>
            <th className="num">成功率</th>
            <th className="num">net avg</th>
            <th className="num">p50</th>
            <th className="num">p95</th>
            <th className="num">p99</th>
            <th className="num">max</th>
            <th className="num">client</th>
            <th className="num">QPS</th>
          </tr>
        </thead>
        <tbody>
          {sorted.map((a) => {
            const level = classifyApdex(a.apdex);
            const apdexColor = level === 'excellent' || level === 'good' ? 'c-success'
              : level === 'fair' ? 'c-warning'
              : 'c-error';
            const rate = a.sampleCount > 0 ? (a.successRate * 100).toFixed(1) : '0';
            // netSampleCount=0 时延迟列显示 — 避免误把 0ms 当真实数据
            const hasNet = (a.netSampleCount ?? 0) > 0;
            return (
              <tr key={a.name}>
                <td><code>{a.name}</code></td>
                <td className="num">{a.sampleCount.toLocaleString()}</td>
                <td className={`num${a.failureCount > 0 ? ' c-error' : ''}`}>{a.failureCount}</td>
                <td className={`num${a.timeoutCount > 0 ? ' c-warning' : ''}`}>{a.timeoutCount}</td>
                <td className={`num ${apdexColor}`}>{a.apdex.toFixed(3)}</td>
                <td className="num">{rate}%</td>
                <td className="num">{hasNet ? fmtMs(a.latency.avgMs) : '—'}</td>
                <td className="num">{hasNet ? fmtMs(a.latency.p50Ms) : '—'}</td>
                <td className="num">{hasNet ? fmtMs(a.latency.p95Ms) : '—'}</td>
                <td className="num">{hasNet ? fmtMs(a.latency.p99Ms) : '—'}</td>
                <td className="num">{hasNet ? fmtMs(a.latency.maxMs) : '—'}</td>
                <td className="num">{fmtMs(a.clientAvgMs ?? 0)}</td>
                <td className="num">{a.avgQps.toFixed(1)}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

/* ── 工具函数 ── */

function formatDuration(sec: number): string {
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m${sec % 60}s`;
  return `${(sec / 3600).toFixed(1)}h`;
}

function formatTime(iso: string): string {
  try {
    const d = new Date(iso);
    const MM = String(d.getMonth() + 1).padStart(2, '0');
    const dd = String(d.getDate()).padStart(2, '0');
    const HH = String(d.getHours()).padStart(2, '0');
    const mm = String(d.getMinutes()).padStart(2, '0');
    return `${MM}-${dd} ${HH}:${mm}`;
  } catch {
    return iso;
  }
}
