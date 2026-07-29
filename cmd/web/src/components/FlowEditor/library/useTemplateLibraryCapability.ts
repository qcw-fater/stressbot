import { useCallback, useEffect, useRef, useState } from 'react';
import { getCapabilities } from '@/services/capabilitiesApi';

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
  const [templateLibrary, setTemplateLibrary] = useState<boolean>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error>();
  const mountedRef = useRef(false);
  const requestRef = useRef(0);

  const refresh = useCallback(async (): Promise<void> => {
    const request = ++requestRef.current;
    if (mountedRef.current) setLoading(true);
    try {
      const capabilities = await getCapabilities();
      if (!mountedRef.current || request !== requestRef.current) return;
      setTemplateLibrary(capabilities.templateLibrary);
      setError(undefined);
    } catch (value) {
      if (!mountedRef.current || request !== requestRef.current) return;
      setError(value instanceof Error ? value : new Error(String(value)));
    } finally {
      if (mountedRef.current && request === requestRef.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    void refresh();
    const onFocus = () => { void refresh(); };
    window.addEventListener('focus', onFocus);
    return () => {
      mountedRef.current = false;
      requestRef.current += 1;
      window.removeEventListener('focus', onFocus);
    };
  }, [refresh]);

  return { templateLibrary, loading, error, refresh };
}
