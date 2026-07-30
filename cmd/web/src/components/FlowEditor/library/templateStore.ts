/**
 * Action / Listen 模板库的组件门面。
 *
 * 组件继续使用毫秒时间戳和既有函数名；实际数据统一由服务器 MySQL 模板库保存。
 * 旧 IndexedDB 数据不会被删除，但正常读写路径不再访问它。
 */
import type { ActionDef } from '@/types/action';
import type { ListenDef } from '@/types/listen';
import {
  actionTemplatesApi,
  listenTemplatesApi,
  type ActionTemplateDto,
  type ActionTemplateSaveDto,
  type ActionTemplateSnapshotInputDto,
  type ListenTemplateDefaultRefDto,
  type ListenTemplateDto,
  type ListenTemplateSaveDto,
  type ListenTemplateSnapshotInputDto,
  type ReplaceTemplateSnapshotRequest,
  type ReplaceTemplateSnapshotResponse,
  type TemplateIdPolicy,
  type TemplateSnapshot,
} from '@/services/templatesApi';

export interface ListenTemplateDefaultRef extends ListenTemplateDefaultRefDto {}

const templateBus = new EventTarget();
const TEMPLATE_CHANGE_EVENT = 'template-change';

export function onTemplateChange(handler: () => void): () => void {
  const wrapped = () => handler();
  templateBus.addEventListener(TEMPLATE_CHANGE_EVENT, wrapped);
  return () => templateBus.removeEventListener(TEMPLATE_CHANGE_EVENT, wrapped);
}

function emitTemplateChange(): void {
  templateBus.dispatchEvent(new Event(TEMPLATE_CHANGE_EVENT));
}

export interface ActionTemplate {
  id: string;
  name: string;
  description?: string;
  pattern: string;
  data: ActionDef;
  createdAt: number;
  updatedAt: number;
}

export interface ListenTemplate {
  id: string;
  name: string;
  description?: string;
  kind: string;
  data: ListenDef;
  defaultRef?: ListenTemplateDefaultRef;
  createdAt: number;
  updatedAt: number;
}

export interface ComponentTemplateSnapshot<T> {
  revision: string;
  items: T[];
}

export interface ReplaceComponentTemplateSnapshotRequest<T> {
  expectedRevision: string;
  idPolicy: TemplateIdPolicy;
  items: readonly T[];
}

export interface ReplaceComponentTemplateSnapshotResponse<T> {
  revision: string;
  count: number;
  items: T[];
}

function fromActionDto(template: ActionTemplateDto): ActionTemplate {
  return {
    ...template,
    createdAt: Date.parse(template.createdAt),
    updatedAt: Date.parse(template.updatedAt),
  };
}

function fromListenDto(template: ListenTemplateDto): ListenTemplate {
  return {
    ...template,
    createdAt: Date.parse(template.createdAt),
    updatedAt: Date.parse(template.updatedAt),
  };
}

function actionSave(template: Omit<ActionTemplate, 'id' | 'createdAt' | 'updatedAt'>): ActionTemplateSaveDto {
  return {
    name: template.name,
    ...(template.description ? { description: template.description } : {}),
    pattern: template.pattern,
    data: template.data,
  };
}

function listenSave(template: Omit<ListenTemplate, 'id' | 'createdAt' | 'updatedAt'>): ListenTemplateSaveDto {
  return {
    name: template.name,
    ...(template.description ? { description: template.description } : {}),
    kind: template.kind,
    data: template.data,
    ...(template.defaultRef ? { defaultRef: template.defaultRef } : {}),
  };
}

function wireTime(value: number): string | undefined {
  return value > 0 ? new Date(value).toISOString() : undefined;
}

function actionSnapshotInput(template: ActionTemplate): ActionTemplateSnapshotInputDto {
  return {
    ...actionSave(template),
    ...(template.id ? { id: template.id } : {}),
    ...(wireTime(template.createdAt) ? { createdAt: wireTime(template.createdAt) } : {}),
    ...(wireTime(template.updatedAt) ? { updatedAt: wireTime(template.updatedAt) } : {}),
  };
}

function listenSnapshotInput(template: ListenTemplate): ListenTemplateSnapshotInputDto {
  return {
    ...listenSave(template),
    ...(template.id ? { id: template.id } : {}),
    ...(wireTime(template.createdAt) ? { createdAt: wireTime(template.createdAt) } : {}),
    ...(wireTime(template.updatedAt) ? { updatedAt: wireTime(template.updatedAt) } : {}),
  };
}

export async function saveActionTemplate(
  template: Omit<ActionTemplate, 'id' | 'createdAt' | 'updatedAt'>,
): Promise<ActionTemplate> {
  const saved = fromActionDto(await actionTemplatesApi.create(actionSave(template)));
  emitTemplateChange();
  return saved;
}

export async function updateActionTemplate(template: ActionTemplate): Promise<void> {
  await actionTemplatesApi.update(template.id, actionSave(template));
  emitTemplateChange();
}

export async function listActionTemplates(): Promise<ActionTemplate[]> {
  return (await actionTemplatesApi.list()).map(fromActionDto);
}

export async function removeActionTemplate(id: string): Promise<void> {
  await actionTemplatesApi.delete(id);
  emitTemplateChange();
}

export async function getActionTemplate(id: string): Promise<ActionTemplate | undefined> {
  return fromActionDto(await actionTemplatesApi.get(id));
}

export async function findActionTemplateByName(name: string): Promise<ActionTemplate | undefined> {
  return (await listActionTemplates()).find((template) => template.name === name);
}

export async function getActionTemplateSnapshot(): Promise<ComponentTemplateSnapshot<ActionTemplate>> {
  const snapshot: TemplateSnapshot<ActionTemplateDto> = await actionTemplatesApi.getSnapshot();
  return { revision: snapshot.revision, items: snapshot.items.map(fromActionDto) };
}

export async function replaceActionTemplateSnapshot(
  request: ReplaceComponentTemplateSnapshotRequest<ActionTemplate>,
): Promise<ReplaceComponentTemplateSnapshotResponse<ActionTemplate>> {
  const wireRequest: ReplaceTemplateSnapshotRequest<ActionTemplateSnapshotInputDto> = {
    expectedRevision: request.expectedRevision,
    idPolicy: request.idPolicy,
    items: request.items.map(actionSnapshotInput),
  };
  const response: ReplaceTemplateSnapshotResponse<ActionTemplateDto> = (
    await actionTemplatesApi.replaceSnapshot(wireRequest)
  );
  emitTemplateChange();
  return { ...response, items: response.items.map(fromActionDto) };
}

export async function replaceActionTemplates(templates: readonly ActionTemplate[]): Promise<void> {
  const current = await getActionTemplateSnapshot();
  await replaceActionTemplateSnapshot({
    expectedRevision: current.revision,
    idPolicy: 'preserve',
    items: templates,
  });
}

export async function saveListenTemplate(
  template: Omit<ListenTemplate, 'id' | 'createdAt' | 'updatedAt'>,
): Promise<ListenTemplate> {
  const saved = fromListenDto(await listenTemplatesApi.create(listenSave(template)));
  emitTemplateChange();
  return saved;
}

export async function updateListenTemplate(template: ListenTemplate): Promise<void> {
  await listenTemplatesApi.update(template.id, listenSave(template));
  emitTemplateChange();
}

export async function listListenTemplates(): Promise<ListenTemplate[]> {
  return (await listenTemplatesApi.list()).map(fromListenDto);
}

export async function removeListenTemplate(id: string): Promise<void> {
  await listenTemplatesApi.delete(id);
  emitTemplateChange();
}

export async function getListenTemplate(id: string): Promise<ListenTemplate | undefined> {
  return fromListenDto(await listenTemplatesApi.get(id));
}

export async function findListenTemplateByName(name: string): Promise<ListenTemplate | undefined> {
  return (await listListenTemplates()).find((template) => template.name === name);
}

export async function getListenTemplateSnapshot(): Promise<ComponentTemplateSnapshot<ListenTemplate>> {
  const snapshot: TemplateSnapshot<ListenTemplateDto> = await listenTemplatesApi.getSnapshot();
  return { revision: snapshot.revision, items: snapshot.items.map(fromListenDto) };
}

export async function replaceListenTemplateSnapshot(
  request: ReplaceComponentTemplateSnapshotRequest<ListenTemplate>,
): Promise<ReplaceComponentTemplateSnapshotResponse<ListenTemplate>> {
  const wireRequest: ReplaceTemplateSnapshotRequest<ListenTemplateSnapshotInputDto> = {
    expectedRevision: request.expectedRevision,
    idPolicy: request.idPolicy,
    items: request.items.map(listenSnapshotInput),
  };
  const response: ReplaceTemplateSnapshotResponse<ListenTemplateDto> = (
    await listenTemplatesApi.replaceSnapshot(wireRequest)
  );
  emitTemplateChange();
  return { ...response, items: response.items.map(fromListenDto) };
}

export async function replaceListenTemplates(templates: readonly ListenTemplate[]): Promise<void> {
  const current = await getListenTemplateSnapshot();
  await replaceListenTemplateSnapshot({
    expectedRevision: current.revision,
    idPolicy: 'preserve',
    items: templates,
  });
}

/** 模板导入/导出包（动作 + 监听）。供工具栏「导出/导入模板」与配置备份使用。 */
export interface TemplateBundle {
  actions: ActionTemplate[];
  listens: ListenTemplate[];
}

/** 导出全部动作 + 监听模板为一个 bundle。 */
export async function exportAllTemplates(): Promise<TemplateBundle> {
  const [actions, listens] = await Promise.all([listActionTemplates(), listListenTemplates()]);
  return { actions, listens };
}

/** 从 bundle 整体替换导入动作 + 监听模板，返回导入数量。 */
export async function importTemplates(bundle: TemplateBundle): Promise<{ actions: number; listens: number }> {
  const actions = Array.isArray(bundle?.actions) ? bundle.actions : [];
  const listens = Array.isArray(bundle?.listens) ? bundle.listens : [];
  await Promise.all([replaceActionTemplates(actions), replaceListenTemplates(listens)]);
  return { actions: actions.length, listens: listens.length };
}
