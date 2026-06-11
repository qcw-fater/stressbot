/**
 * 服务器能力查询 API（对应后端 GET /sbot/capabilities）。
 *
 * 目前仅暴露共享状态（Redis）可用性，供前端在脚本使用 share 时提示是否可运行。
 */

import { getJson } from './api';

export interface CapabilitiesResponse {
  /** 服务器是否已配置共享状态（Redis）。 */
  sharedState: boolean;
  /** Redis 地址（脱敏，主机已隐藏仅保留端口，如 ***:6379）；仅 sharedState=true 时有值。 */
  sharedAddr?: string;
}

/** 查询服务器能力。 */
export function getCapabilities(): Promise<CapabilitiesResponse> {
  return getJson<CapabilitiesResponse>('/capabilities');
}
