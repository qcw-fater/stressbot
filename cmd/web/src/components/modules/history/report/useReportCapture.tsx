/**
 * useReportCapture: 捕获图表 → 渲染 HTML → 在新标签页打开报告。
 *
 * 为什么先把标签页开出来再生成：九张图逐张离屏渲染 + getDataURL(pixelRatio 2) 加上整份
 * 报告的 renderToStaticMarkup 都是同步重活，动作多的归档能卡住主线程一两秒。等生成完才
 * window.open 有两个问题——这段时间里用户点了按钮却什么都没发生，以及浏览器的「用户手势」
 * 授权可能已经过期（Chrome 的瞬时激活只有几秒），弹窗会被直接拦掉。
 *
 * 所以在点击的同一个任务里先开标签页、写一张占位页，等它和按钮的 loading 都上屏之后再做
 * 重活，最后把标签页地址换成报告本体。弹窗被拦时退回「生成完再开」的老路径。
 */

import { App } from 'antd';
import { useCallback, useState } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { captureAllCharts } from './reportCharts';
import { ReportHtml } from './ReportHtml';
import { buildStandaloneHtml } from './reportHtmlBuilder';
import type { HistoryDetail, HistoryTrendPoint } from '@/types/api';

const PLACEHOLDER_HTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>正在生成压测报告…</title>
<style>
  body { margin: 0; height: 100vh; display: flex; align-items: center; justify-content: center;
         font: 14px -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', sans-serif;
         color: #555; background: #fafafa; }
</style></head>
<body>正在生成压测报告，请稍候…</body></html>`;

/** 等到下一次绘制之后：单个 rAF 只排到绘制前，占位页与 loading 态还没真正上屏。 */
function nextPaint(): Promise<void> {
  return new Promise((resolve) => {
    let settled = false;
    const finish = () => {
      if (settled) return;
      settled = true;
      resolve();
    };
    requestAnimationFrame(() => requestAnimationFrame(finish));
    // 点完按钮立刻切走标签页时 rAF 会被冻结，没有这个兜底报告就永远停在「生成中」
    setTimeout(finish, 250);
  });
}

export function useReportCapture(
  detail: HistoryDetail | null,
  timeseries: { points: HistoryTrendPoint[] } | null,
) {
  const { message } = App.useApp();
  const [generating, setGenerating] = useState(false);

  const generateReport = useCallback(async () => {
    if (!detail || !timeseries || generating) return;

    const target = window.open('', '_blank');
    if (target) {
      target.document.write(PLACEHOLDER_HTML);
      // 不 close 的话文档一直处于加载中，占位文案可能不显示
      target.document.close();
    }
    setGenerating(true);
    try {
      await nextPaint();

      const actions = detail.finalSnapshot?.actions ?? [];
      const charts = captureAllCharts(actions, timeseries.points);
      const htmlBody = renderToStaticMarkup(
        <ReportHtml detail={detail} timeseries={timeseries} charts={charts} />,
      );
      const fullHtml = buildStandaloneHtml(htmlBody);
      const blob = new Blob([fullHtml], { type: 'text/html;charset=utf-8' });
      const url = URL.createObjectURL(blob);
      // replace 而不是 assign：占位页不该留在标签页的返回历史里
      if (target && !target.closed) target.location.replace(url);
      else window.open(url, '_blank');
      setTimeout(() => URL.revokeObjectURL(url), 30000);
    } catch (err) {
      // 生成失败不能把占位页晾在「正在生成」上，那会一直等一个不会来的结果
      if (target && !target.closed) target.document.body.textContent = '报告生成失败';
      message.error(`报告生成失败：${(err as Error).message}`);
    } finally {
      setGenerating(false);
    }
  }, [detail, generating, message, timeseries]);

  return { generateReport, generating };
}
