/**
 * useReportCapture: 捕获图表 → 渲染 HTML → 在新标签页打开报告。
 */

import { renderToStaticMarkup } from 'react-dom/server';
import { captureAllCharts } from './reportCharts';
import { ReportHtml } from './ReportHtml';
import { buildStandaloneHtml } from './reportHtmlBuilder';
import type { HistoryDetail, TimeseriesPoint } from '@/types/api';

export function useReportCapture(
  detail: HistoryDetail | null,
  timeseries: { stress: TimeseriesPoint[]; system: TimeseriesPoint[] } | null,
) {
  return function generateReport() {
    if (!detail || !timeseries) return;

    const actions = detail.finalSnapshot?.actions ?? [];
    const charts = captureAllCharts(actions, timeseries.stress);

    const htmlBody = renderToStaticMarkup(
      <ReportHtml detail={detail} timeseries={timeseries} charts={charts} />,
    );

    const fullHtml = buildStandaloneHtml(htmlBody);
    const blob = new Blob([fullHtml], { type: 'text/html;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    window.open(url, '_blank');
    setTimeout(() => URL.revokeObjectURL(url), 30000);
  };
}
