/**
 * 报告专用 CSS — 内联到独立 HTML，白底黑字，A4 print 规则。
 * 导出为字符串常量，由 reportHtmlBuilder.ts 注入 <style> 标签。
 */

export const REPORT_CSS = `
* { margin: 0; padding: 0; box-sizing: border-box; }

body {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', sans-serif;
  color: #1a1a1a;
  background: #fff;
  line-height: 1.5;
  padding: 32px 40px;
  max-width: 210mm;
  margin: 0 auto;
}

/* ── 打印提示横幅 ── */
.print-hint {
  position: sticky;
  top: 0;
  z-index: 100;
  background: #e6f4ff;
  border: 1px solid #91caff;
  border-radius: 6px;
  padding: 8px 16px;
  margin-bottom: 20px;
  font-size: 13px;
  color: #1677ff;
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.print-hint kbd {
  background: #fff;
  border: 1px solid #d9d9d9;
  border-radius: 3px;
  padding: 1px 6px;
  font-size: 12px;
  font-family: inherit;
}

/* ── Section 通用 ── */
.report-section {
  margin-bottom: 24px;
  page-break-inside: avoid;
}

.section-title {
  font-size: 14px;
  font-weight: 700;
  color: #333;
  border-bottom: 2px solid #1677ff;
  padding-bottom: 4px;
  margin-bottom: 12px;
  letter-spacing: 0.02em;
}

/* ── 封面 ── */
.report-cover {
  text-align: center;
  padding: 32px 0 24px;
  border-bottom: 3px solid #1677ff;
  margin-bottom: 28px;
}
.report-cover__logo {
  font-size: 12px;
  font-weight: 600;
  color: #8c8c8c;
  text-transform: uppercase;
  letter-spacing: 2px;
  margin-bottom: 8px;
}
.report-cover__name {
  font-size: 22px;
  font-weight: 700;
  color: #1a1a1a;
  margin-bottom: 8px;
}
.report-cover__meta {
  font-size: 13px;
  color: #595959;
  line-height: 1.8;
}
.report-cover__meta b { color: #1a1a1a; }
.report-cover__error {
  margin-top: 12px;
  background: #fff2f0;
  border: 1px solid #ffccc7;
  border-radius: 6px;
  padding: 10px 16px;
  text-align: left;
  font-size: 12px;
  color: #cf1322;
  font-family: 'SFMono-Regular', Consolas, monospace;
  white-space: pre-wrap;
  word-break: break-all;
}

/* ── 状态徽章 ── */
.badge {
  display: inline-block;
  font-size: 12px;
  font-weight: 600;
  padding: 2px 10px;
  border-radius: 999px;
  vertical-align: middle;
}
.badge--ok { color: #389e0d; background: #f6ffed; border: 1px solid #b7eb8f; }
.badge--fail { color: #cf1322; background: #fff2f0; border: 1px solid #ffa39e; }

/* ── KPI 网格 ── */
.kpi-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 10px;
}
.kpi-card {
  background: #f8f9fa;
  border: 1px solid #e8e8e8;
  border-radius: 6px;
  padding: 10px 8px;
  text-align: center;
}
.kpi-value {
  font-size: 22px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
  line-height: 1.2;
}
.kpi-label {
  font-size: 10px;
  color: #8c8c8c;
  margin-top: 4px;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  font-weight: 500;
}

/* ── 图表容器 ── */
.chart-wrap {
  text-align: center;
  margin: 8px 0;
}
.chart-wrap img {
  max-width: 100%;
  height: auto;
}
.chart-empty {
  text-align: center;
  padding: 24px;
  color: #8c8c8c;
  font-size: 13px;
  background: #fafafa;
  border-radius: 6px;
  border: 1px dashed #e8e8e8;
}

/* ── 趋势 2×2 网格 ── */
.trends-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 12px;
}
.trends-grid__cell {
  min-width: 0;
}
.trends-grid__cell img {
  max-width: 100%;
  height: auto;
  border-radius: 4px;
}
.trends-grid__label {
  font-size: 11px;
  font-weight: 600;
  color: #595959;
  margin-bottom: 4px;
}

/* ── 动作详情宽表 ── */
.report-table--detail {
  font-size: 10px;
}
.report-table--detail th {
  font-size: 9px;
  padding: 5px 4px;
}
.report-table--detail td {
  padding: 4px;
}

/* ── 表格 ── */
.report-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}
.report-table th {
  background: #fafafa;
  font-weight: 600;
  color: #595959;
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.3px;
  padding: 6px 8px;
  text-align: left;
  border-bottom: 2px solid #e8e8e8;
}
.report-table td {
  padding: 5px 8px;
  border-bottom: 1px solid #f0f0f0;
  vertical-align: middle;
}
.report-table tr:last-child td { border-bottom: none; }
.report-table code {
  font-size: 11px;
  background: #f5f5f5;
  padding: 1px 4px;
  border-radius: 3px;
}
.report-table .num { text-align: right; }

/* ── 颜色工具 ── */
.c-success { color: #389e0d; }
.c-error { color: #cf1322; }
.c-warning { color: #d48806; }
.c-blue { color: #1677ff; }
.c-purple { color: #722ed1; }

/* ── 动作类别配色 ──
 * 报告是脱离应用样式的独立 HTML，取不到 --node-* 变量，这里写死 tokens.css 的
 * 亮色值（报告永远白底）：往返 #713f12 / 监听 #9a3412 / 发送 #1e40af / 本地 #115e59。
 */
.k-networked { color: #713f12; }
.k-listen { color: #9a3412; }
.k-send { color: #1e40af; }
.k-local { color: #115e59; }

/* ── 系统资源卡片 ── */
.sys-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 10px;
}
.sys-card {
  background: #f8f9fa;
  border: 1px solid #e8e8e8;
  border-radius: 6px;
  padding: 10px 12px;
}
.sys-card__label {
  font-size: 10px;
  color: #8c8c8c;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.sys-card__value {
  font-size: 16px;
  font-weight: 700;
  margin-top: 2px;
  font-variant-numeric: tabular-nums;
}

/* ── 错误小表 ── */
.error-row {
  display: flex;
  gap: 8px;
  align-items: baseline;
  padding: 4px 0;
  font-size: 11px;
  border-bottom: 1px solid #f5f5f5;
}
.error-row:last-child { border-bottom: none; }
.error-count {
  display: inline-block;
  background: #fff2f0;
  color: #cf1322;
  font-weight: 600;
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 3px;
  flex-shrink: 0;
}
.error-action {
  color: #8c8c8c;
  flex-shrink: 0;
  font-size: 10px;
}
.error-name {
  font-weight: 600;
  color: #333;
  font-size: 10px;
  flex-shrink: 0;
}
.error-msg {
  color: #595959;
  font-family: 'SFMono-Regular', Consolas, monospace;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* ── 标签 ── */
.tag {
  display: inline-block;
  font-size: 10px;
  font-weight: 500;
  padding: 1px 6px;
  border-radius: 3px;
  margin-right: 4px;
}
.tag--orange { background: #fff7e6; color: #d48806; border: 1px solid #ffe58f; }
.tag--green { background: #f6ffed; color: #389e0d; border: 1px solid #b7eb8f; }
.tag--red { background: #fff2f0; color: #cf1322; border: 1px solid #ffa39e; }

/* ── Footer ── */
.report-footer {
  margin-top: 32px;
  padding-top: 12px;
  border-top: 1px solid #e8e8e8;
  font-size: 10px;
  color: #bfbfbf;
  display: flex;
  justify-content: space-between;
}

/* ── Print ── */
@media print {
  @page { size: A4; margin: 15mm 12mm; }
  body { padding: 0; max-width: none; -webkit-print-color-adjust: exact; print-color-adjust: exact; }
  .report-section { page-break-inside: avoid; }
  .no-print { display: none !important; }
  .print-hint { display: none !important; }
}
`;
