import { useCallback, useEffect } from 'react';
import { useQuery } from '@tanstack/react-query';
import { capabilitiesQueryOptions } from '@/services/queryOptions';

export interface TemplateLibraryCapabilityState {
  /** undefined 表示尚未取得过服务器状态。 */
  templateLibrary: boolean | undefined;
  loading: boolean;
  error: Error | undefined;
  refresh: () => Promise<void>;
}

/**
 * 查询共享模板库能力，并在窗口重新获得焦点时复查。
 * 刷新失败会保留最近一次成功值，避免短暂网络抖动清空已显示的模板。
 */
export function useTemplateLibraryCapability(): TemplateLibraryCapabilityState {
  const query = useQuery(capabilitiesQueryOptions());
  const { refetch } = query;

  const refresh = useCallback(async (): Promise<void> => {
    await refetch();
  }, [refetch]);

  useEffect(() => {
    const onFocus = () => {
      void refresh();
    };
    window.addEventListener('focus', onFocus);
    return () => window.removeEventListener('focus', onFocus);
  }, [refresh]);

  return {
    templateLibrary: query.data?.templateLibrary,
    loading: query.isPending,
    error: query.error ?? undefined,
    refresh,
  };
}
