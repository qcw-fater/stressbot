/**
 * 基线资源读取 API（svn update 方向：磁盘 → 浏览器）。
 *
 * 所有 conf/ 目录下的资源读取统一经过此模块，组件不直接 fetch。
 * 基线前缀通过 env.ts 集中管理，切换 Nginx / Vite 代理只改一处。
 */

import { BASELINE_PREFIX } from './env';

/**
 * 单文件读取结果的三态。
 *
 * 把「服务器说没有」和「请求失败」折叠成 null，对 gap-fill 类调用方（拉不到就进
 * missing 列表）无害，但对**删除对账**是致命的：把一次网络抖动读成「服务器已删除该
 * 文件」，就会连带删掉本地已同步的资源。因此凡是会据此删本地文件的调用方，必须走这个
 * 三态接口，只在 `missing` 上执行删除。
 */
export type BaselineFetchResult =
  | { status: 'ok'; content: string }
  /** 服务器权威答复「无此文件」（404） */
  | { status: 'missing' }
  /** 请求失败（断网 / 5xx / 解析异常），服务器上到底有没有这个文件是未知的 */
  | { status: 'error' };

async function fetchTextResult(url: string): Promise<BaselineFetchResult> {
  try {
    const r = await fetch(url, { cache: 'no-cache' });
    if (r.status === 404) return { status: 'missing' };
    if (!r.ok) return { status: 'error' };
    return { status: 'ok', content: await r.text() };
  } catch {
    return { status: 'error' };
  }
}

/** 基线资源分组，与 resourcesStore 的 ResourceType 取值一致。 */
export type BaselineResourceKind = 'proto' | 'script' | 'adapter';

const BASELINE_RESOURCE_DIR: Record<BaselineResourceKind, string> = {
  proto: 'proto',
  script: 'scripts',
  adapter: 'adapter',
};

/** 按资源分组读取基线单文件，区分「不存在」与「拉取失败」。 */
export function fetchBaselineResource(
  kind: BaselineResourceKind,
  name: string,
): Promise<BaselineFetchResult> {
  const dir = BASELINE_RESOURCE_DIR[kind];
  return fetchTextResult(`${BASELINE_PREFIX}/${dir}/${encodeURIComponent(name)}`);
}

/** 两态便捷读取：拉不到就当没有，仅供「缺了就补」的调用方使用。 */
async function fetchBaselineResourceText(
  kind: BaselineResourceKind,
  name: string,
): Promise<string | null> {
  const r = await fetchBaselineResource(kind, name);
  return r.status === 'ok' ? r.content : null;
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

async function fetchStringArray(url: string): Promise<string[] | null> {
  const data = await fetchJson<unknown>(url);
  return Array.isArray(data) && data.every((item) => typeof item === 'string') ? data : null;
}

/** 获取基线 flow.json */
export async function fetchBaselineFlow<T = unknown>(): Promise<T | null> {
  return fetchJson<T>(`${BASELINE_PREFIX}/flow/flow.json`);
}

/**
 * 获取基线脚本文件名列表。
 *
 * 返回 null 表示「请求失败 / 无法解析」；返回 string[] 表示「服务器权威返回的清单（含空清单）」。
 * 区分二者是为了让上层同步逻辑在失败时中止删除对账，而不是把空清单当作「服务器删光了」。
 */
export async function fetchBaselineScriptIndex(): Promise<string[] | null> {
  return fetchStringArray(`${BASELINE_PREFIX}/scripts/index.json`);
}

/** 获取基线中指定脚本内容；拉取失败与不存在都返回 null（不可用于删除对账）。 */
export async function fetchBaselineScript(name: string): Promise<string | null> {
  return fetchBaselineResourceText('script', name);
}

/**
 * 获取基线 proto 文件名列表。
 *
 * 返回 null 表示「请求失败 / 无法解析」；返回 string[] 表示「服务器权威返回的清单（含空清单）」。
 * 上层同步逻辑据此在失败时中止删除对账，避免空清单被误判为「服务器删光了」。
 */
export async function fetchBaselineProtoIndex(): Promise<string[] | null> {
  return fetchStringArray(`${BASELINE_PREFIX}/proto/index.json`);
}

/** 获取基线中指定 proto 文件内容；拉取失败与不存在都返回 null（不可用于删除对账）。 */
export async function fetchBaselineProtoContent(name: string): Promise<string | null> {
  return fetchBaselineResourceText('proto', name);
}

// === T3 声明式 codec：基线 adapter 多文件读取（codec.json + errors.json）===
//
// 后端（T4.3 + B1 adapter-index 前置）已提供：
//   GET /sbot/baseline/adapter/index.json — 列 *_codec.json + errors.json 文件名
//   GET /sbot/baseline/adapter/{name}      — 按文件名取单文件（路径透传）

/**
 * 基线 adapter 文件名清单（*_codec.json + errors.json）。
 *
 * 返回 null 表示「请求失败 / 无法解析」；返回 string[] 表示「服务器权威返回的清单（含空清单）」。
 * 上层同步逻辑据此在失败时中止删除对账，避免空清单被误判为「服务器删光了」。
 */
export async function fetchBaselineCodecIndex(): Promise<string[] | null> {
  return fetchStringArray(`${BASELINE_PREFIX}/adapter/index.json`);
}

/**
 * 基线单份 codec/errors 文件内容（name = tcp_logic_codec.json / errors.json）。
 *
 * 拉取失败与不存在都返回 null（不可用于删除对账）。
 */
export async function fetchBaselineCodec(name: string): Promise<string | null> {
  return fetchBaselineResourceText('adapter', name);
}
