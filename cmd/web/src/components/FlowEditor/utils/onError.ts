import type { OnErrorDef, RetryDef } from '@/types/flow';

export function normalizeOnError(onError: OnErrorDef | undefined): OnErrorDef | undefined {
  if (!onError) return undefined;
  const out: OnErrorDef = {};

  const ignoreCodes = (onError.ignoreCodes ?? []).filter((code) => Number.isInteger(code) && code > 0);
  if (ignoreCodes.length > 0) out.ignoreCodes = ignoreCodes;

  const handler = onError.handler?.trim();
  if (handler) out.handler = handler;

  const retry = normalizeRetry(onError.retry);
  if (retry) out.retry = retry;

  if (onError.strategy && onError.strategy !== 'resume') out.strategy = onError.strategy;

  return Object.keys(out).length > 0 ? out : undefined;
}

function normalizeRetry(retry: RetryDef | undefined): RetryDef | undefined {
  if (!retry) return undefined;
  const out: RetryDef = {};
  if (typeof retry.maxRetries === 'number' && retry.maxRetries > 0) out.maxRetries = retry.maxRetries;
  if (typeof retry.retryDelayMs === 'number' && retry.retryDelayMs > 0) out.retryDelayMs = retry.retryDelayMs;
  return Object.keys(out).length > 0 ? out : undefined;
}
