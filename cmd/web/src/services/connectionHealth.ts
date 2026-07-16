import { useCallback, useRef } from 'react';

/** 按来源更新失败集合，避免一个轮询恢复时覆盖其他仍失败的轮询。 */
export function updateFailedSources(
  previous: ReadonlySet<string>,
  source: string,
  failed: boolean,
): Set<string> {
  const next = new Set(previous);
  if (failed) next.add(source);
  else next.delete(source);
  return next;
}

export function useConnectionHealth(setConnectionLost: (lost: boolean) => void) {
  const failedSourcesRef = useRef<Set<string>>(new Set());

  return useCallback((source: string, failed: boolean) => {
    const next = updateFailedSources(failedSourcesRef.current, source, failed);
    failedSourcesRef.current = next;
    setConnectionLost(next.size > 0);
  }, [setConnectionLost]);
}
