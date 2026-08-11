/**
 * 服务器能力查询 API（对应后端 GET /sbot/capabilities）。
 *
 * 目前仅暴露共享状态（Redis）可用性，供前端在脚本使用 share 时提示是否可运行。
 */

import type { components } from '@/generated/admin-api';
import { managementClient, unwrapGenerated } from './generatedClient';

export type CapabilitiesResponse = components['schemas']['CapabilitiesResponse'];

/** 查询服务器能力。 */
export function getCapabilities(init?: Pick<RequestInit, 'signal'>): Promise<CapabilitiesResponse> {
  return unwrapGenerated<CapabilitiesResponse>(() =>
    managementClient.GET('/sbot/capabilities', init),
  );
}
