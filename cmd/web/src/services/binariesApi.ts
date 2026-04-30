/**
 * 二进制管理 API 封装（对应 docs/api-monitor.md §9.1~§9.4）。
 *
 * 升级相关接口（§9.5~§9.8）放在 agentsApi.ts，与 Agent 紧耦合，避免循环引用。
 */

import { adaptList, del, getJson, postMultipart } from './api';
import type { BinariesListResponse, BinaryMeta, OS, Arch } from '@/types/api';

/**
 * 后端目前直接返回 `[]BinaryMeta`，前端包装为 `{items}`。
 */
export async function listBinaries(): Promise<BinariesListResponse> {
  const resp = await getJson<unknown>('/binaries');
  return { items: adaptList<BinaryMeta>(resp).items };
}

export interface UploadBinaryParams {
  file: File;
  version: string;
  os?: OS;
  arch?: Arch;
  force?: boolean;
}

export function uploadBinary(params: UploadBinaryParams): Promise<BinaryMeta> {
  const fd = new FormData();
  fd.append('file', params.file);
  fd.append('version', params.version);
  if (params.os) fd.append('os', params.os);
  if (params.arch) fd.append('arch', params.arch);
  if (params.force) fd.append('force', 'true');
  return postMultipart<BinaryMeta>('/binaries', fd);
}

export function deleteBinary(filename: string): Promise<void> {
  return del<void>(`/binaries/${encodeURIComponent(filename)}`);
}

/** 直接下载链接，前端用 a 标签即可 */
export function binaryDownloadUrl(filename: string): string {
  return `/api/binaries/${encodeURIComponent(filename)}`;
}
