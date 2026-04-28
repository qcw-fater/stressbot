/**
 * 在多个紧凑表格 / 单元格中共享的 inline 样式片段。
 *
 * 仅放"重复 ≥3 处"的小片段；UI 主题、节点配色等仍在 tokens.css 维护。
 */

import type { CSSProperties } from 'react';

/** 表格内紧凑等宽输入（route / server / 路径等技术字符串） */
export const monoCellStyle: CSSProperties = {
  fontFamily: 'monospace',
  fontSize: 12,
};
