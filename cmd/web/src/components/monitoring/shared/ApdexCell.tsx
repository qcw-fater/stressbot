/**
 * Apdex 单元格：按 §7.5 阈值表染色 + 数值显示。
 *
 * 用于 ActionsTab / PerAgentTab 表格列。
 */

import { Tag } from 'antd';
import { classifyApdex, type ApdexLevel } from '@/services/metricsBinding';

const COLOR: Record<ApdexLevel, string> = {
  excellent: 'success',
  good: 'lime',
  fair: 'warning',
  poor: 'orange',
  danger: 'error',
  unknown: 'default',
};

const LABEL: Record<ApdexLevel, string> = {
  excellent: '优秀',
  good: '良好',
  fair: '一般',
  poor: '较差',
  danger: '危险',
  unknown: '—',
};

export interface ApdexCellProps {
  value: number | undefined;
  showLabel?: boolean;
}

export function ApdexCell({ value, showLabel = false }: ApdexCellProps) {
  const level = classifyApdex(value);
  const text = value === undefined || Number.isNaN(value) ? '—' : value.toFixed(3);
  return (
    <Tag color={COLOR[level]} style={{ fontVariantNumeric: 'tabular-nums', minWidth: 56, textAlign: 'center' }}>
      {showLabel ? `${LABEL[level]} ${text}` : text}
    </Tag>
  );
}
