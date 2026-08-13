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

/**
 * 获取基线脚本文件名列表。
 *
 * 返回 null 表示「请求失败 / 无法解析」；返回 string[] 表示「服务器权威返回的清单（含空清单）」。
 * 区分二者是为了让上层同步逻辑在失败时中止删除对账，而不是把空清单当作「服务器删光了」。
 */
export async function fetchBaselineScriptIndex(): Promise<string[] | null> {
  return fetchJson<string[]>(`${BASELINE_PREFIX}/scripts/index.json`);
}

/** 获取基线中指定脚本内容 */
export async function fetchBaselineScript(name: string): Promise<string | null> {
  return fetchText(`${BASELINE_PREFIX}/scripts/${encodeURIComponent(name)}`);
}

/**
 * 获取基线 proto 文件名列表。
 *
 * 返回 null 表示「请求失败 / 无法解析」；返回 string[] 表示「服务器权威返回的清单（含空清单）」。
 * 上层同步逻辑据此在失败时中止删除对账，避免空清单被误判为「服务器删光了」。
 */
export async function fetchBaselineProtoIndex(): Promise<string[] | null> {
  return fetchJson<string[]>(`${BASELINE_PREFIX}/proto/index.json`);
}

/** 获取基线中指定 proto 文件内容 */
export async function fetchBaselineProtoContent(name: string): Promise<string | null> {
  return fetchText(`${BASELINE_PREFIX}/proto/${encodeURIComponent(name)}`);
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
  return fetchJson<string[]>(`${BASELINE_PREFIX}/adapter/index.json`);
}

/** 基线单份 codec/errors 文件内容（name = tcp_logic_codec.json / errors.json）。 */
export async function fetchBaselineCodec(name: string): Promise<string | null> {
  return fetchText(`${BASELINE_PREFIX}/adapter/${encodeURIComponent(name)}`);
}
