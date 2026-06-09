export function formatStageLabel(label?: string, stageIndex?: number): string {
  if (label) {
    return label.replace(/^段\s*(\d+)/, '第 $1 轮');
  }
  return (stageIndex ?? -1) > 0 ? `第 ${stageIndex} 轮` : '';
}
