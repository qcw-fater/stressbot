import { Empty } from 'antd';
import { useRuntimeStore } from '@/services';
import type { ActionMetric } from '@/types/api';
import { ActionMetricsTable } from '../shared/ActionMetricsTable';

export function ActionsTab() {
  const latestStress = useRuntimeStore((s) => s.latestStress);

  if (!latestStress) {
    return <Empty description="暂无压测数据" />;
  }

  return (
    <ActionMetricsTable<ActionMetric>
      rows={latestStress.actions ?? []}
      size="small"
      showClientBreakdown
      scrollY="calc(70vh - 220px)"
    />
  );
}
