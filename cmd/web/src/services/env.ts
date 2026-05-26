/**
 * 运行环境配置：API 前缀、基线路径、RobotConfig 默认值。
 *
 * 开发时通过 Vite env 文件或 .env.local 覆盖；生产时通过构建时注入或 Nginx 统一代理。
 */

import type { LogLevel, RobotConfig } from '@/types/api';

/** Admin API 前缀，所有后端接口走这个前缀 */
export const API_PREFIX: string = import.meta.env.VITE_API_PREFIX || '/sbot';

/** 基线资源读取前缀（proto/scripts/adapter/flow/config） */
export const BASELINE_PREFIX: string = import.meta.env.VITE_BASELINE_PREFIX || '/sbot/baseline';

/**
 * 从 .env 注入的 RobotConfig 默认值。
 * 所有字段可选，仅覆盖显式配置的项，其余保持内置默认值。
 *
 * .env 示例：
 *   VITE_ROBOT_ACCOUNT_PREFIX=bot_
 *   VITE_ROBOT_MAIN_SERVICE=logic
 *   VITE_ROBOT_CONCURRENCY=50
 *   VITE_ROBOT_TIMEOUT_SEC=60
 *   VITE_ROBOT_HEARTBEAT_SEC=5
 *   VITE_ROBOT_HTTP_TIMEOUT_SEC=10
 *   VITE_ROBOT_APDEX_T=100
 *   VITE_ROBOT_START_NUMBER=0
 *   VITE_ROBOT_LOG_LEVEL=info
 */
export const ENV_ROBOT_DEFAULTS: Partial<RobotConfig> = {
  ...(import.meta.env.VITE_ROBOT_ACCOUNT_PREFIX && { accountPrefix: import.meta.env.VITE_ROBOT_ACCOUNT_PREFIX }),
  ...(import.meta.env.VITE_ROBOT_MAIN_SERVICE && { mainService: import.meta.env.VITE_ROBOT_MAIN_SERVICE }),
  ...(import.meta.env.VITE_ROBOT_CONCURRENCY && { concurrency: Number(import.meta.env.VITE_ROBOT_CONCURRENCY) || undefined }),
  ...(import.meta.env.VITE_ROBOT_TIMEOUT_SEC && { timeoutSec: Number(import.meta.env.VITE_ROBOT_TIMEOUT_SEC) || undefined }),
  ...(import.meta.env.VITE_ROBOT_HEARTBEAT_SEC && { heartbeatSec: Number(import.meta.env.VITE_ROBOT_HEARTBEAT_SEC) || undefined }),
  ...(import.meta.env.VITE_ROBOT_HTTP_TIMEOUT_SEC && { httpTimeoutSec: Number(import.meta.env.VITE_ROBOT_HTTP_TIMEOUT_SEC) || undefined }),
  ...(import.meta.env.VITE_ROBOT_APDEX_T && { apdexT: Number(import.meta.env.VITE_ROBOT_APDEX_T) || undefined }),
  ...(import.meta.env.VITE_ROBOT_START_NUMBER && { startNumber: Number(import.meta.env.VITE_ROBOT_START_NUMBER) || undefined }),
  ...(import.meta.env.VITE_ROBOT_LOG_LEVEL && { logLevel: import.meta.env.VITE_ROBOT_LOG_LEVEL as LogLevel }),
};
