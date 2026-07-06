/**
 * 共享边样式 helper：默认虚线 + 选中实线加粗高亮。
 *
 * 颜色策略（按用户反馈调整）：
 *   - 默认色：取「起点节点类型对应的主色」（sequence 蓝 / weighted 紫 / loop 绿 / boolean 黄 / action 灰黑 / ...）
 *   - 选中色：使用同一颜色但加深 + 加粗 + 实线 + drop-shadow
 */

/** 节点类型 → 主色 / 深色 token 名 */
const NODE_COLOR_MAP: Record<string, { main: string; deep: string }> = {
  sequence: { main: 'var(--node-sequence)', deep: 'var(--node-sequence-border-active)' },
  action: { main: 'var(--node-action)', deep: 'var(--node-action-border-active)' },
  loop: { main: 'var(--node-loop)', deep: 'var(--node-loop-border-active)' },
  boolean: { main: 'var(--node-boolean)', deep: 'var(--node-boolean-border-active)' },
  switch: { main: 'var(--node-switch)', deep: 'var(--node-switch-border-active)' },
  weighted: { main: 'var(--node-weighted)', deep: 'var(--node-weighted-border-active)' },
  wait: { main: 'var(--node-wait)', deep: 'var(--node-wait-border-active)' },
  break: { main: 'var(--node-break)', deep: 'var(--node-break-border-active)' },
  continue: { main: 'var(--node-continue)', deep: 'var(--node-continue-border-active)' },
  listenCard: { main: 'var(--node-listen)', deep: 'var(--node-listen-border-active)' },
};

/** 取节点类型对应的边主色（默认态） */
export function colorOfNodeType(t: string | undefined): string {
  return (t && NODE_COLOR_MAP[t]?.main) ?? 'var(--edge-seq)';
}

/** 取节点类型对应的边深色（选中态） */
export function deepColorOfNodeType(t: string | undefined): string {
  return (t && NODE_COLOR_MAP[t]?.deep) ?? 'var(--edge-seq)';
}

export interface EdgeStyleOpts {
  /** 起点节点类型（决定颜色） */
  sourceNodeType?: string;
  /** 默认描边宽度（未选中） */
  width?: number;
  /** 默认 stroke-dasharray（未选中） */
  dash?: string;
  /** 是否选中 */
  selected?: boolean;
  /** 颜色覆盖（branch true/false 走自己的绿/红） */
  colorOverride?: string;
  /** 选中颜色覆盖 */
  selectedColorOverride?: string;
}

export function buildEdgeStyle({
  sourceNodeType,
  width = 1.5,
  dash = '5 4',
  selected,
  colorOverride,
  selectedColorOverride,
}: EdgeStyleOpts): React.CSSProperties {
  const color = colorOverride ?? colorOfNodeType(sourceNodeType);
  const deep = selectedColorOverride ?? deepColorOfNodeType(sourceNodeType);
  if (selected) {
    return {
      stroke: deep,
      strokeWidth: width + 0.8,
      strokeDasharray: undefined,
      filter: `drop-shadow(0 0 2px ${deep})`,
    };
  }
  return {
    stroke: color,
    strokeWidth: width,
    strokeDasharray: dash,
    opacity: 0.85,
  };
}
