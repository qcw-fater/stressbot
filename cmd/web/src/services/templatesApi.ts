/**
 * Action / Listen 共享模板库 API。
 *
 * 服务器负责模板身份、时间戳、名称唯一性和快照并发控制；本模块只定义稳定的
 * TypeScript 契约，并统一复用 services/api.ts 的请求入口。
 */
import type { ActionDef } from '@/types/action';
import type { ListenDef } from '@/types/listen';
import { del, getJson, postJson, putJson } from './api';

export interface ListenTemplateDefaultRefDto {
  server: string;
  route: unknown;
  queueSize?: number;
}

export interface ActionTemplateSaveDto {
  name: string;
  description?: string;
  pattern: string;
  data: ActionDef;
}

export interface ActionTemplateDto extends ActionTemplateSaveDto {
  id: string;
  createdAt: string;
  updatedAt: string;
}

export interface ActionTemplateSnapshotInputDto extends ActionTemplateSaveDto {
  id?: string;
  createdAt?: string;
  updatedAt?: string;
}

export interface ListenTemplateSaveDto {
  name: string;
  description?: string;
  kind: string;
  data: ListenDef;
  defaultRef?: ListenTemplateDefaultRefDto;
}

export interface ListenTemplateDto extends ListenTemplateSaveDto {
  id: string;
  createdAt: string;
  updatedAt: string;
}

export interface ListenTemplateSnapshotInputDto extends ListenTemplateSaveDto {
  id?: string;
  createdAt?: string;
  updatedAt?: string;
}

export type TemplateIdPolicy = 'preserve' | 'generate-missing';

export interface TemplateSnapshot<T> {
  revision: string;
  items: T[];
}

export interface ReplaceTemplateSnapshotRequest<T> {
  expectedRevision: string;
  idPolicy: TemplateIdPolicy;
  items: T[];
}

export interface ReplaceTemplateSnapshotResponse<T> {
  revision: string;
  count: number;
  items: T[];
}

function templateApi<
  TDto,
  TSave,
  TSnapshotInput,
>(path: string) {
  return {
    list: (): Promise<TDto[]> => getJson<TDto[]>(path),
    get: (id: string): Promise<TDto> => getJson<TDto>(`${path}/${encodeURIComponent(id)}`),
    create: (request: TSave): Promise<TDto> => postJson<TDto>(path, request),
    update: (id: string, request: TSave): Promise<TDto> => (
      putJson<TDto>(`${path}/${encodeURIComponent(id)}`, request)
    ),
    delete: (id: string): Promise<void> => del<void>(`${path}/${encodeURIComponent(id)}`),
    getSnapshot: (): Promise<TemplateSnapshot<TDto>> => (
      getJson<TemplateSnapshot<TDto>>(`${path}/snapshot`)
    ),
    replaceSnapshot: (
      request: ReplaceTemplateSnapshotRequest<TSnapshotInput>,
    ): Promise<ReplaceTemplateSnapshotResponse<TDto>> => (
      putJson<ReplaceTemplateSnapshotResponse<TDto>>(`${path}/snapshot`, request)
    ),
  };
}

export const actionTemplatesApi = templateApi<
  ActionTemplateDto,
  ActionTemplateSaveDto,
  ActionTemplateSnapshotInputDto
>('/action-templates');

export const listenTemplatesApi = templateApi<
  ListenTemplateDto,
  ListenTemplateSaveDto,
  ListenTemplateSnapshotInputDto
>('/listen-templates');
