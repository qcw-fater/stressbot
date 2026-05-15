/**
 * 运行环境配置：API 前缀与基线路径。
 *
 * 开发时通过 Vite env 文件或 .env.local 覆盖；生产时通过构建时注入或 Nginx 统一代理。
 */

/** Admin API 前缀，所有后端接口走这个前缀 */
export const API_PREFIX: string = import.meta.env.VITE_API_PREFIX || '/sbot';

/** 基线资源读取前缀（proto/scripts/adapter/flow/config） */
export const BASELINE_PREFIX: string = import.meta.env.VITE_BASELINE_PREFIX || '/sbot/baseline';
