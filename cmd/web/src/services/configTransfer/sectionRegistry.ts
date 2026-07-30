import { nanoid } from 'nanoid';

import type { FlowJson } from '@/components/FlowEditor/codec/flowToJson';
import {
  listActionTemplates,
  listListenTemplates,
  getActionTemplateSnapshot,
  getListenTemplateSnapshot,
  replaceActionTemplateSnapshot,
  replaceListenTemplateSnapshot,
  replaceActionTemplates,
  replaceListenTemplates,
  type ActionTemplate,
  type ComponentTemplateSnapshot,
  type ListenTemplate,
  type ReplaceComponentTemplateSnapshotRequest,
  type ReplaceComponentTemplateSnapshotResponse,
} from '@/components/FlowEditor/library/templateStore';
import {
  loadDraft,
  refreshDraftSnapshot,
  saveDraftSnapshot,
  type DraftSnapshot,
} from '@/components/FlowEditor/store/persistDraft';
import {
  exportNotepadFiles,
  replaceNotepadFiles,
  type NotepadFile,
} from '@/components/modules/notepad/notepadStore';
import { validateFlow } from '@/components/FlowEditor/validation/refsCheck';
import type { FlowLayout } from '@/types/editor';
import { ALL_ACTION_PATTERNS, type ActionDef } from '@/types/action';
import { classifyListen, type ListenDef } from '@/types/listen';
import {
  getFlowSnapshot,
  replaceFlowSnapshot,
  type FlowSnapshot,
  type FlowTemplateDetail,
  type ReplaceFlowSnapshotRequest,
  type ReplaceFlowSnapshotResponse,
} from '../flowsApi';
import {
  getErrorMap,
  listCodecFiles,
  listProto,
  listScript,
  replaceCodecFiles,
  replaceErrorMap,
  replaceProtoFiles,
  replaceScriptFiles,
  validateCodecSchema,
  type ResourceFile,
} from '../resourcesStore';
import { parseErrorMap, validateErrorMap } from '../errorMapValidation';
import type { BackupSection, RestoreMode } from './types';
import type { CollectionIdentity } from './restorePlanner';

export interface VersionedSectionIO<T> {
  read: () => Promise<{ revision: string; value: T }>;
  replace: (input: {
    expectedRevision: string;
    value: T;
    mode: RestoreMode;
  }) => Promise<{ revision: string; value: T }>;
  fingerprint?: (value: T, mode: RestoreMode) => string;
}

export interface ConfigSectionAdapter<T> {
  key: BackupSection;
  label: string;
  kind: 'collection' | 'singleton';
  read: () => Promise<T>;
  replace: (value: T) => Promise<void>;
  validate: (value: unknown) => asserts value is T;
  count: (value: T) => number;
  identity?: T extends readonly (infer Item)[] ? CollectionIdentity<Item> : never;
  refresh?: (value: T) => Promise<void> | void;
  versioned?: VersionedSectionIO<T>;
}

export interface ConfigSectionRegistry {
  flows: ConfigSectionAdapter<FlowTemplateDetail[]>;
  draft: ConfigSectionAdapter<DraftSnapshot | null>;
  protoFiles: ConfigSectionAdapter<ResourceFile[]>;
  luaFiles: ConfigSectionAdapter<ResourceFile[]>;
  codecFiles: ConfigSectionAdapter<ResourceFile[]>;
  errorMap: ConfigSectionAdapter<ResourceFile | null>;
  actionTemplates: ConfigSectionAdapter<ActionTemplate[]>;
  listenTemplates: ConfigSectionAdapter<ListenTemplate[]>;
  notepadFiles: ConfigSectionAdapter<NotepadFile[]>;
}

export interface SectionRegistryDependencies {
  getFlowSnapshot: () => Promise<FlowSnapshot>;
  replaceFlowSnapshot: (
    request: ReplaceFlowSnapshotRequest,
  ) => Promise<ReplaceFlowSnapshotResponse>;
  loadDraft: () => DraftSnapshot | null;
  saveDraftSnapshot: (snapshot: DraftSnapshot | null) => void;
  refreshDraftSnapshot: (snapshot: DraftSnapshot | null) => void;
  listProto: () => Promise<ResourceFile[]>;
  replaceProtoFiles: (files: readonly ResourceFile[]) => Promise<void>;
  listScript: () => Promise<ResourceFile[]>;
  replaceScriptFiles: (files: readonly ResourceFile[]) => Promise<void>;
  listCodecFiles: () => Promise<ResourceFile[]>;
  replaceCodecFiles: (files: readonly ResourceFile[]) => Promise<void>;
  getErrorMap: () => Promise<ResourceFile | undefined>;
  replaceErrorMap: (file: ResourceFile | null) => Promise<void>;
  listActionTemplates: () => Promise<ActionTemplate[]>;
  replaceActionTemplates: (templates: readonly ActionTemplate[]) => Promise<void>;
  getActionTemplateSnapshot: () => Promise<ComponentTemplateSnapshot<ActionTemplate>>;
  replaceActionTemplateSnapshot: (
    request: ReplaceComponentTemplateSnapshotRequest<ActionTemplate>,
  ) => Promise<ReplaceComponentTemplateSnapshotResponse<ActionTemplate>>;
  listListenTemplates: () => Promise<ListenTemplate[]>;
  replaceListenTemplates: (templates: readonly ListenTemplate[]) => Promise<void>;
  getListenTemplateSnapshot: () => Promise<ComponentTemplateSnapshot<ListenTemplate>>;
  replaceListenTemplateSnapshot: (
    request: ReplaceComponentTemplateSnapshotRequest<ListenTemplate>,
  ) => Promise<ReplaceComponentTemplateSnapshotResponse<ListenTemplate>>;
  exportNotepadFiles: () => Promise<NotepadFile[]>;
  replaceNotepadFiles: (files: readonly NotepadFile[]) => Promise<void>;
  createId: () => string;
}

const defaultDependencies: SectionRegistryDependencies = {
  getFlowSnapshot,
  replaceFlowSnapshot,
  loadDraft,
  saveDraftSnapshot,
  refreshDraftSnapshot,
  listProto,
  replaceProtoFiles,
  listScript,
  replaceScriptFiles,
  listCodecFiles,
  replaceCodecFiles,
  getErrorMap,
  replaceErrorMap,
  listActionTemplates,
  replaceActionTemplates,
  getActionTemplateSnapshot,
  replaceActionTemplateSnapshot,
  listListenTemplates,
  replaceListenTemplates,
  getListenTemplateSnapshot,
  replaceListenTemplateSnapshot,
  exportNotepadFiles,
  replaceNotepadFiles,
  createId: () => nanoid(12),
};

function assertRecord(value: unknown, label: string): asserts value is Record<string, unknown> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(`${label} 必须是对象`);
  }
}

function assertNonEmptyString(value: unknown, label: string): asserts value is string {
  if (typeof value !== 'string' || value.trim() === '') {
    throw new Error(`${label} 不能为空`);
  }
}

function assertTimestamp(value: unknown, label: string): asserts value is string {
  if (typeof value !== 'string' || Number.isNaN(Date.parse(value))) {
    throw new Error(`${label}无效`);
  }
}

function assertFiniteNumber(value: unknown, label: string): asserts value is number {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    throw new Error(`${label} 必须是有效数字`);
  }
}

function assertTemplateTimestamp(value: unknown, label: string): asserts value is number {
  assertFiniteNumber(value, label);
  if (value <= 0 || Number.isNaN(new Date(value).getTime())) {
    throw new Error(`${label}无效`);
  }
}

function assertTemplateMetadata(value: Record<string, unknown>, label: string): void {
  assertNonEmptyString(value.id, `${label} ID`);
  if (new TextEncoder().encode(value.id).length > 32) {
    throw new Error(`${label} ID 不能超过 32 个字节`);
  }
  assertNonEmptyString(value.name, `${label}名称`);
  if ([...value.name.trim()].length > 80) {
    throw new Error(`${label}名称不能超过 80 个字符`);
  }
  if (value.description !== undefined) {
    if (typeof value.description !== 'string') {
      throw new Error(`${label} description 必须是字符串`);
    }
    if ([...value.description].length > 500) {
      throw new Error(`${label}描述不能超过 500 个字符`);
    }
  }
  assertTemplateTimestamp(value.createdAt, `${label}创建时间`);
  assertTemplateTimestamp(value.updatedAt, `${label}更新时间`);
}

function assertArray(value: unknown, label: string): asserts value is unknown[] {
  if (!Array.isArray(value)) throw new Error(`${label} 必须是数组`);
}

function assertUnique<T>(items: readonly T[], key: (item: T) => string, label: string): void {
  const seen = new Set<string>();
  for (const item of items) {
    const value = key(item);
    if (seen.has(value)) throw new Error(`${label} ${value} 重复`);
    seen.add(value);
  }
}

function assertFlowJson(value: unknown, label: string): asserts value is FlowJson {
  assertRecord(value, label);
  assertFiniteNumber(value.defaultDelayMs, `${label}.defaultDelayMs`);
  assertRecord(value.nodes, `${label}.nodes`);
  assertRecord(value.actions, `${label}.actions`);
  assertRecord(value.listens, `${label}.listens`);
  try {
    const report = validateFlow(value as unknown as FlowJson);
    if (report.errors.length > 0) {
      throw new Error(report.errors[0].message);
    }
  } catch (error) {
    throw new Error(`${label}流程校验失败：${(error as Error).message}`);
  }
}

function assertLayout(value: unknown, label: string): asserts value is FlowLayout {
  assertRecord(value, label);
  assertRecord(value.nodePositions, `${label}.nodePositions`);
  if (value.showListenEdges !== undefined && typeof value.showListenEdges !== 'boolean') {
    throw new Error(`${label}.showListenEdges 必须是布尔值`);
  }
}

function assertFlowTemplate(value: unknown, index: number): asserts value is FlowTemplateDetail {
  const label = `第 ${index + 1} 个流程`;
  assertRecord(value, label);
  assertNonEmptyString(value.id, `${label} ID`);
  assertNonEmptyString(value.name, `${label}名称`);
  if (!Number.isInteger(value.nodeCount) || (value.nodeCount as number) < 0) {
    throw new Error(`${label} nodeCount 必须是非负整数`);
  }
  if (!Number.isInteger(value.actionCount) || (value.actionCount as number) < 0) {
    throw new Error(`${label} actionCount 必须是非负整数`);
  }
  assertTimestamp(value.createdAt, `${label}创建时间`);
  assertTimestamp(value.updatedAt, `${label}更新时间`);
  assertFlowJson(value.flow, `${label}的`);
  if (value.layout !== undefined) assertLayout(value.layout, `${label}布局`);
}

function assertFlowTemplates(value: unknown): asserts value is FlowTemplateDetail[] {
  assertArray(value, '流程分区');
  value.forEach(assertFlowTemplate);
  const items = value as FlowTemplateDetail[];
  assertUnique(items, (item) => item.id, '流程 ID');
}

function assertDraft(value: unknown): asserts value is DraftSnapshot | null {
  if (value === null) return;
  assertRecord(value, '当前编辑稿');
  assertFlowJson(value.flow, '当前编辑稿');
  assertLayout(value.layout, '当前编辑稿布局');
  assertFiniteNumber(value.savedAt, '当前编辑稿保存时间');
}

function assertResourceFile(value: unknown, label: string): asserts value is ResourceFile {
  assertRecord(value, label);
  assertNonEmptyString(value.name, `${label}文件名`);
  if (typeof value.content !== 'string') throw new Error(`${label} content 必须是字符串`);
  if (!Number.isInteger(value.size) || (value.size as number) < 0) {
    throw new Error(`${label} size 必须是非负整数`);
  }
  if (new Blob([value.content]).size !== value.size) {
    throw new Error(`${label} size 与内容不一致`);
  }
  assertTimestamp(value.uploadedAt, `${label}上传时间`);
  if (
    value.baseHash !== undefined &&
    value.baseHash !== null &&
    typeof value.baseHash !== 'string'
  ) {
    throw new Error(`${label} baseHash 必须是字符串或 null`);
  }
}

function assertResourceFiles(
  value: unknown,
  label: string,
  acceptsName: (name: string) => boolean,
  nameError: string,
): asserts value is ResourceFile[] {
  assertArray(value, label);
  value.forEach((item, index) => {
    assertResourceFile(item, `${label}第 ${index + 1} 项`);
    if (!acceptsName(item.name)) throw new Error(`${nameError}：${item.name}`);
  });
  const items = value as ResourceFile[];
  assertUnique(items, (item) => item.name, `${label}文件名`);
}

function assertProtoFiles(value: unknown): asserts value is ResourceFile[] {
  assertResourceFiles(
    value,
    'Proto 分区',
    (name) => name.endsWith('.proto'),
    'Proto 文件名必须以 .proto 结尾',
  );
}

function assertLuaFiles(value: unknown): asserts value is ResourceFile[] {
  assertResourceFiles(
    value,
    'Lua 分区',
    (name) => name.endsWith('.lua'),
    'Lua 文件名必须以 .lua 结尾',
  );
}

function assertCodecFiles(value: unknown): asserts value is ResourceFile[] {
  assertResourceFiles(
    value,
    '协议配置分区',
    (name) => name.endsWith('_codec.json'),
    '协议配置文件名必须以 _codec.json 结尾',
  );
  for (const file of value) {
    const errors = validateCodecSchema(file.content);
    if (errors.length > 0) throw new Error(`${file.name} 配置无效：${errors[0]}`);
  }
}

function assertErrorMap(value: unknown): asserts value is ResourceFile | null {
  if (value === null) return;
  assertResourceFile(value, '错误码配置');
  if (value.name !== 'errors.json') throw new Error('错误码文件名必须是 errors.json');
  let parsed: unknown;
  try {
    parsed = JSON.parse(value.content);
  } catch (error) {
    throw new Error(`错误码配置不是合法 JSON：${(error as Error).message}`);
  }
  assertRecord(parsed, '错误码配置内容');
  const errors = validateErrorMap(parseErrorMap(value.content));
  if (errors.length > 0) throw new Error(`错误码配置无效：${errors[0].message}`);
}

function assertActionTemplate(value: unknown, index: number): asserts value is ActionTemplate {
  const label = `第 ${index + 1} 个动作模板`;
  assertRecord(value, label);
  assertTemplateMetadata(value, label);
  assertNonEmptyString(value.pattern, `${label} pattern`);
  assertRecord(value.data, `${label} data`);
  if (!ALL_ACTION_PATTERNS.includes((value.data as unknown as ActionDef).pattern)) {
    throw new Error(`${label} data.pattern 无效`);
  }
  if ((value.data as unknown as ActionDef).pattern !== value.pattern) {
    throw new Error(`${label} pattern 与 data.pattern 不一致`);
  }
  const actionFlow: FlowJson = {
    defaultDelayMs: 0,
    nodes: {
      main: { type: 'sequence', next: ['template'] },
      template: { type: 'action', action: 'template' },
    },
    actions: { template: value.data as unknown as ActionDef },
    listens: {},
  };
  const actionErrors = validateFlow(actionFlow).errors;
  if (actionErrors.length > 0) {
    throw new Error(`${label}动作模板校验失败：${actionErrors[0].message}`);
  }
}

function assertActionTemplates(value: unknown): asserts value is ActionTemplate[] {
  assertArray(value, '动作模板分区');
  value.forEach(assertActionTemplate);
  const items = value as ActionTemplate[];
  assertUnique(items, (item) => item.id, '动作模板 ID');
  assertUnique(items, (item) => item.name, '动作模板名称');
}

const LISTEN_KINDS = new Set(['silent', 'declarative', 'lua']);

function assertListenTemplate(value: unknown, index: number): asserts value is ListenTemplate {
  const label = `第 ${index + 1} 个监听模板`;
  assertRecord(value, label);
  assertTemplateMetadata(value, label);
  if (typeof value.kind !== 'string' || !LISTEN_KINDS.has(value.kind)) {
    throw new Error(`${label} kind 无效`);
  }
  assertRecord(value.data, `${label} data`);
  const listen = value.data as unknown as ListenDef;
  if (classifyListen(listen) !== value.kind) {
    throw new Error(`${label}监听模板校验失败：kind 与内容形态不一致`);
  }
  const listenErrors = validateFlow({
    defaultDelayMs: 0,
    nodes: { main: { type: 'sequence', next: [] } },
    actions: {},
    listens: { template: listen },
  }).errors;
  if (listenErrors.length > 0) {
    throw new Error(`${label}监听模板校验失败：${listenErrors[0].message}`);
  }
  if (value.defaultRef !== undefined) {
    const defaultRef = value.defaultRef;
    assertRecord(defaultRef, `${label} defaultRef`);
    assertNonEmptyString(defaultRef.server, `${label} defaultRef.server`);
    if (!Object.prototype.hasOwnProperty.call(defaultRef, 'route') || defaultRef.route === null) {
      throw new Error(`${label} defaultRef.route 不能为空`);
    }
    if (
      defaultRef.queueSize !== undefined &&
      (!Number.isInteger(defaultRef.queueSize) || (defaultRef.queueSize as number) <= 0)
    ) {
      throw new Error(`${label} defaultRef.queueSize 必须是正整数`);
    }
  }
}

function assertListenTemplates(value: unknown): asserts value is ListenTemplate[] {
  assertArray(value, '监听模板分区');
  value.forEach(assertListenTemplate);
  const items = value as ListenTemplate[];
  assertUnique(items, (item) => item.id, '监听模板 ID');
  assertUnique(items, (item) => item.name, '监听模板名称');
}

function assertNotepadFile(value: unknown, index: number): asserts value is NotepadFile {
  const label = `第 ${index + 1} 个笔记文件`;
  assertRecord(value, label);
  assertNonEmptyString(value.id, `${label} ID`);
  assertNonEmptyString(value.name, `${label}名称`);
  assertNonEmptyString(value.language, `${label}语言`);
  if (typeof value.content !== 'string') throw new Error(`${label}内容必须是字符串`);
  assertTimestamp(value.createdAt, `${label}创建时间`);
  assertTimestamp(value.updatedAt, `${label}更新时间`);
}

function assertNotepadFiles(value: unknown): asserts value is NotepadFile[] {
  assertArray(value, '笔记分区');
  value.forEach(assertNotepadFile);
  const items = value as NotepadFile[];
  assertUnique(items, (item) => item.id, '笔记 ID');
}

function itemIdentity<T extends { id: string; name: string }>(
  createId: () => string,
): CollectionIdentity<T> {
  return {
    id: (item) => item.id,
    name: (item) => item.name,
    clone: (item, id, name) => ({ ...item, id, name }),
    createId,
  };
}

function resourceIdentity(createId: () => string): CollectionIdentity<ResourceFile> {
  return {
    id: (file) => file.name,
    name: (file) => file.name,
    clone: (file, _id, name) => ({ ...file, name }),
    createId,
  };
}

function templateIdentity<T extends { id: string; name: string; createdAt: number; updatedAt: number }>(
  createId: () => string,
): CollectionIdentity<T> {
  return {
    ...itemIdentity<T>(createId),
    matchBy: 'name',
    prepareAdd: (source) => ({ ...source, id: '', createdAt: 0, updatedAt: 0 }),
    prepareOverwrite: (target, source) => ({
      ...source,
      id: target.id,
      createdAt: target.createdAt,
      updatedAt: 0,
    }),
    prepareCopy: (source, name) => ({
      ...source,
      id: '',
      name,
      createdAt: 0,
      updatedAt: 0,
    }),
  };
}

function canonicalize(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonicalize);
  if (value !== null && typeof value === 'object') {
    const record = value as Record<string, unknown>;
    return Object.fromEntries(
      Object.keys(record).sort().map((key) => [key, canonicalize(record[key])]),
    );
  }
  return value;
}

function templateFingerprint<
  T extends { id: string; name: string; createdAt: number; updatedAt: number },
>(items: T[], mode: RestoreMode): string {
  const stable = [...items]
    .sort((left, right) => left.name < right.name ? -1 : left.name > right.name ? 1 : 0)
    .map((item) => {
      if (mode === 'replace') return item;
      const { id: _id, createdAt: _createdAt, updatedAt: _updatedAt, ...semantic } = item;
      return semantic;
    });
  return JSON.stringify(canonicalize(stable));
}

const collectionCount = <T>(value: readonly T[]): number => value.length;
const singletonCount = <T>(value: T | null): number => (value === null ? 0 : 1);

export function createSectionRegistry(
  dependencies: SectionRegistryDependencies = defaultDependencies,
): ConfigSectionRegistry {
  const flowIdentity = itemIdentity<FlowTemplateDetail>(dependencies.createId);
  const fileIdentity = resourceIdentity(dependencies.createId);
  const actionIdentity = templateIdentity<ActionTemplate>(dependencies.createId);
  const listenIdentity = templateIdentity<ListenTemplate>(dependencies.createId);
  const notepadIdentity = itemIdentity<NotepadFile>(dependencies.createId);

  return {
    flows: {
      key: 'flows',
      label: '已保存流程',
      kind: 'collection',
      read: async () => (await dependencies.getFlowSnapshot()).items,
      replace: async (items) => {
        const current = await dependencies.getFlowSnapshot();
        await dependencies.replaceFlowSnapshot({ expectedRevision: current.revision, items });
      },
      validate: assertFlowTemplates,
      count: collectionCount,
      identity: flowIdentity,
      versioned: {
        read: async () => {
          const snapshot = await dependencies.getFlowSnapshot();
          return { revision: snapshot.revision, value: snapshot.items };
        },
        replace: async ({ expectedRevision, value }) => {
          const result = await dependencies.replaceFlowSnapshot({
            expectedRevision,
            items: value,
          });
          return { revision: result.revision, value };
        },
      },
    },
    draft: {
      key: 'draft',
      label: '当前编辑稿',
      kind: 'singleton',
      read: async () => dependencies.loadDraft(),
      replace: async (value) => dependencies.saveDraftSnapshot(value),
      validate: assertDraft,
      count: singletonCount,
      refresh: dependencies.refreshDraftSnapshot,
    },
    protoFiles: {
      key: 'protoFiles',
      label: 'Proto 文件',
      kind: 'collection',
      read: dependencies.listProto,
      replace: dependencies.replaceProtoFiles,
      validate: assertProtoFiles,
      count: collectionCount,
      identity: fileIdentity,
    },
    luaFiles: {
      key: 'luaFiles',
      label: 'Lua 脚本',
      kind: 'collection',
      read: dependencies.listScript,
      replace: dependencies.replaceScriptFiles,
      validate: assertLuaFiles,
      count: collectionCount,
      identity: fileIdentity,
    },
    codecFiles: {
      key: 'codecFiles',
      label: '协议配置',
      kind: 'collection',
      read: dependencies.listCodecFiles,
      replace: dependencies.replaceCodecFiles,
      validate: assertCodecFiles,
      count: collectionCount,
      identity: fileIdentity,
    },
    errorMap: {
      key: 'errorMap',
      label: '错误码',
      kind: 'singleton',
      read: async () => (await dependencies.getErrorMap()) ?? null,
      replace: dependencies.replaceErrorMap,
      validate: assertErrorMap,
      count: singletonCount,
    },
    actionTemplates: {
      key: 'actionTemplates',
      label: '动作模板',
      kind: 'collection',
      read: async () => (await dependencies.getActionTemplateSnapshot()).items,
      replace: async (items) => {
        const current = await dependencies.getActionTemplateSnapshot();
        await dependencies.replaceActionTemplateSnapshot({
          expectedRevision: current.revision,
          idPolicy: 'preserve',
          items,
        });
      },
      validate: assertActionTemplates,
      count: collectionCount,
      identity: actionIdentity,
      versioned: {
        read: async () => {
          const snapshot = await dependencies.getActionTemplateSnapshot();
          return { revision: snapshot.revision, value: snapshot.items };
        },
        replace: async ({ expectedRevision, value, mode }) => {
          const result = await dependencies.replaceActionTemplateSnapshot({
            expectedRevision,
            idPolicy: mode === 'replace' ? 'preserve' : 'generate-missing',
            items: value,
          });
          return { revision: result.revision, value: result.items };
        },
        fingerprint: templateFingerprint,
      },
    },
    listenTemplates: {
      key: 'listenTemplates',
      label: '监听模板',
      kind: 'collection',
      read: async () => (await dependencies.getListenTemplateSnapshot()).items,
      replace: async (items) => {
        const current = await dependencies.getListenTemplateSnapshot();
        await dependencies.replaceListenTemplateSnapshot({
          expectedRevision: current.revision,
          idPolicy: 'preserve',
          items,
        });
      },
      validate: assertListenTemplates,
      count: collectionCount,
      identity: listenIdentity,
      versioned: {
        read: async () => {
          const snapshot = await dependencies.getListenTemplateSnapshot();
          return { revision: snapshot.revision, value: snapshot.items };
        },
        replace: async ({ expectedRevision, value, mode }) => {
          const result = await dependencies.replaceListenTemplateSnapshot({
            expectedRevision,
            idPolicy: mode === 'replace' ? 'preserve' : 'generate-missing',
            items: value,
          });
          return { revision: result.revision, value: result.items };
        },
        fingerprint: templateFingerprint,
      },
    },
    notepadFiles: {
      key: 'notepadFiles',
      label: '笔记文件',
      kind: 'collection',
      read: dependencies.exportNotepadFiles,
      replace: dependencies.replaceNotepadFiles,
      validate: assertNotepadFiles,
      count: collectionCount,
      identity: notepadIdentity,
    },
  };
}

export const defaultSectionRegistry = createSectionRegistry();
