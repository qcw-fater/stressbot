/**
 * 将报告 HTML body 包装为完整独立 HTML 文档。
 */

import { REPORT_CSS } from './reportPrintCss';

export function buildStandaloneHtml(body: string): string {
  return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>stressbot 压测报告</title>
<style>${REPORT_CSS}</style>
</head>
<body>${body}</body>
</html>`;
}
