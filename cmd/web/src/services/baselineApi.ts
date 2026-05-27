/**
 * 基线资源读取 API（svn update 方向：磁盘 → 浏览器）。
 *
 * 所有 conf/ 目录下的资源读取统一经过此模块，组件不直接 fetch。
 * 基线前缀通过 env.ts 集中管理，切换 Nginx / Vite 代理只改一处。
 */

import { BASELINE_PREFIX } from './env';

async function fetchText(url: string): Promise<string | null> {
  try {
    const r = await fetch(url, { cache: 'no-cache' });
    if (!r.ok) return null;
    return await r.text();
  } catch {
    return null;
  }
}

async function fetchJson<T>(url: string): Promise<T | null> {
  try {
    const r = await fetch(url, { cache: 'no-cache' });
    if (!r.ok) return null;
    return (await r.json()) as T;
  } catch {
    return null;
  }
}

/** 获取基线 flow.json */
export async function fetchBaselineFlow<T = unknown>(): Promise<T | null> {
  return fetchJson<T>(`${BASELINE_PREFIX}/flow/flow.json`);
}

/** 获取基线 config.json */
export async function fetchBaselineConfig<T = unknown>(): Promise<T | null> {
  return fetchJson<T>(`${BASELINE_PREFIX}/config.json`);
}

/** 获取基线脚本文件名列表 */
export async function fetchBaselineScriptIndex(): Promise<string[]> {
  const list = await fetchJson<string[]>(`${BASELINE_PREFIX}/scripts/index.json`);
  return list ?? [];
}

/** 获取基线中指定脚本内容 */
export async function fetchBaselineScript(name: string): Promise<string | null> {
  return fetchText(`${BASELINE_PREFIX}/scripts/${encodeURIComponent(name)}`);
}

/** 获取基线 proto 文件名列表 */
export async function fetchBaselineProtoIndex(): Promise<string[]> {
  const list = await fetchJson<string[]>(`${BASELINE_PREFIX}/proto/index.json`);
  return list ?? [];
}

/** 获取基线中指定 proto 文件内容 */
export async function fetchBaselineProtoContent(name: string): Promise<string | null> {
  return fetchText(`${BASELINE_PREFIX}/proto/${encodeURIComponent(name)}`);
}

/** 获取基线 adapter/codec.lua 内容 */
export async function fetchBaselineAdapter(): Promise<string | null> {
  return fetchText(`${BASELINE_PREFIX}/adapter/codec.lua`);
}

/** 获取基线 adapter/error.lua 内容 */
export async function fetchBaselineErrorMap(): Promise<string | null> {
  return fetchText(`${BASELINE_PREFIX}/adapter/error.lua`);
}
