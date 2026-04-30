/**
 * 通用轮询 hook。
 *
 * 设计要点：
 * - 失败计数 ≥ failThreshold 时触发 `onConnectionLost`；恢复成功时触发 `onConnectionRestored`；
 * - 不在 hook 内 toast，把"是否提示用户"交给上层决策（避免 5 个轮询都各自弹窗）；
 * - `enabled=false` 时不发请求；运行中切换 enabled 立即生效；
 * - `intervalMs` 变化时不重置 fail 计数，只重排下一轮 schedule；
 * - 卸载时取消正在 in-flight 的回调（用 ref 标记 stale），避免 setState on unmounted。
 *
 * 用法见 services/runtimeStore.ts 的多接口并发轮询。
 */

import { useEffect, useRef } from 'react';

export interface UsePollingOptions<T> {
  /** 数据获取函数，返回 Promise<T>。同名调用会被前一次完成后才发新一次（不并发）。 */
  fetcher: () => Promise<T>;
  /** 轮询间隔，毫秒；改变会立即生效。 */
  intervalMs: number;
  /** 是否启用；false 时不发请求。 */
  enabled: boolean;
  /** 成功回调（每次成功都触发）。 */
  onSuccess?: (data: T) => void;
  /** 失败回调（含 NETWORK_ERROR）。 */
  onError?: (err: unknown) => void;
  /** 连续失败 N 次后触发；默认 3。 */
  failThreshold?: number;
  /** 连续失败达到阈值时触发一次。 */
  onConnectionLost?: () => void;
  /** 从失败恢复时触发一次。 */
  onConnectionRestored?: () => void;
  /** 首次是否立即拉一次（默认 true）。 */
  immediate?: boolean;
}

export function usePolling<T>(opts: UsePollingOptions<T>): void {
  const optsRef = useRef(opts);
  optsRef.current = opts;

  const failCountRef = useRef(0);
  const wasLostRef = useRef(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const inFlightRef = useRef(false);
  const aliveRef = useRef(true);

  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
      if (timerRef.current) clearTimeout(timerRef.current);
    };
  }, []);

  useEffect(() => {
    if (!opts.enabled) {
      if (timerRef.current) {
        clearTimeout(timerRef.current);
        timerRef.current = null;
      }
      return;
    }

    const tick = async (): Promise<void> => {
      if (!aliveRef.current || !optsRef.current.enabled) return;
      if (inFlightRef.current) {
        scheduleNext();
        return;
      }
      inFlightRef.current = true;
      try {
        const data = await optsRef.current.fetcher();
        if (!aliveRef.current) return;
        if (wasLostRef.current) {
          wasLostRef.current = false;
          optsRef.current.onConnectionRestored?.();
        }
        failCountRef.current = 0;
        optsRef.current.onSuccess?.(data);
      } catch (e) {
        if (!aliveRef.current) return;
        failCountRef.current += 1;
        optsRef.current.onError?.(e);
        const threshold = optsRef.current.failThreshold ?? 3;
        if (!wasLostRef.current && failCountRef.current >= threshold) {
          wasLostRef.current = true;
          optsRef.current.onConnectionLost?.();
        }
      } finally {
        inFlightRef.current = false;
        scheduleNext();
      }
    };

    const scheduleNext = (): void => {
      if (!aliveRef.current || !optsRef.current.enabled) return;
      if (timerRef.current) clearTimeout(timerRef.current);
      timerRef.current = setTimeout(tick, optsRef.current.intervalMs);
    };

    if (opts.immediate !== false) {
      tick();
    } else {
      scheduleNext();
    }

    return () => {
      if (timerRef.current) {
        clearTimeout(timerRef.current);
        timerRef.current = null;
      }
    };
    // 依赖只读 enabled 和 intervalMs；其他 opt 变更已通过 ref 同步
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [opts.enabled, opts.intervalMs]);
}
