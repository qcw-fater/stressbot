import { describe, expect, it, vi } from 'vitest';

import type { FlowJson } from '@/components/FlowEditor/codec/flowToJson';
import type { ActionTemplate, ListenTemplate } from '@/components/FlowEditor/library/templateStore';
import type { DraftSnapshot } from '@/components/FlowEditor/store/persistDraft';
import type { NotepadFile } from '@/components/modules/notepad/notepadStore';
import type { FlowTemplateDetail } from '../flowsApi';
import type { ResourceFile } from '../resourcesStore';
import { parseBackupWithRegistry } from './backupCodec';
import { createSectionRegistry, type SectionRegistryDependencies } from './sectionRegistry';

const validFlow: FlowJson = {
  defaultDelayMs: 0,
  nodes: { main: { type: 'sequence', next: [] } },
  actions: {},
  listens: {},
};

function flow(id = 'id-1', name = '登录流程'): FlowTemplateDetail {
  return {
    id,
    name,
    nodeCount: 1,
    actionCount: 0,
    createdAt: '2026-01-01T00:00:00.000Z',
    updatedAt: '2026-01-02T00:00:00.000Z',
    flow: validFlow,
    layout: { nodePositions: {} },
  };
}

function resource(name = 'Login.proto'): ResourceFile {
  return {
    name,
    content: 'syntax="proto3";',
    size: 16,
    uploadedAt: '2026-01-02T03:04:05Z',
    baseHash: 'sha256:original',
  };
}

function dependencies(): SectionRegistryDependencies {
  return {
    getFlowSnapshot: vi.fn(async () => ({ revision: 'sha256:one', items: [flow()] })),
    replaceFlowSnapshot: vi.fn(async (request) => ({
      revision: 'sha256:two',
      count: request.items.length,
    })),
    loadDraft: vi.fn(() => null),
    saveDraftSnapshot: vi.fn(),
    refreshDraftSnapshot: vi.fn(),
    listProto: vi.fn(async () => []),
    replaceProtoFiles: vi.fn(async () => undefined),
    listScript: vi.fn(async () => []),
    replaceScriptFiles: vi.fn(async () => undefined),
    listCodecFiles: vi.fn(async () => []),
    replaceCodecFiles: vi.fn(async () => undefined),
    getErrorMap: vi.fn(async () => undefined),
    replaceErrorMap: vi.fn(async () => undefined),
    listActionTemplates: vi.fn(async () => []),
    replaceActionTemplates: vi.fn(async () => undefined),
    getActionTemplateSnapshot: vi.fn(async () => ({ revision: 'action-r1', items: [] })),
    replaceActionTemplateSnapshot: vi.fn(async (request) => ({
      revision: 'action-r2',
      count: request.items.length,
      items: request.items.map((item: ActionTemplate, index: number) => ({
        ...item,
        id: item.id || `server-action-${index}`,
        createdAt: item.createdAt || 100,
        updatedAt: item.updatedAt || 101,
      })),
    })),
    listListenTemplates: vi.fn(async () => []),
    replaceListenTemplates: vi.fn(async () => undefined),
    getListenTemplateSnapshot: vi.fn(async () => ({ revision: 'listen-r7', items: [] })),
    replaceListenTemplateSnapshot: vi.fn(async (request) => ({
      revision: 'listen-r8',
      count: request.items.length,
      items: request.items.map((item: ListenTemplate, index: number) => ({
        ...item,
        id: item.id || `server-listen-${index}`,
        createdAt: item.createdAt || 200,
        updatedAt: item.updatedAt || 201,
      })),
    })),
    exportNotepadFiles: vi.fn(async () => []),
    replaceNotepadFiles: vi.fn(async () => undefined),
    createId: vi.fn(() => 'copy-id'),
  };
}

describe('section identities', () => {
  it('uses id or exact name for saved-flow duplicates', () => {
    const adapter = createSectionRegistry(dependencies()).flows;
    const existing = flow('id-1', '登录流程');

    expect(adapter.identity?.id(existing)).toBe('id-1');
    expect(adapter.identity?.name(existing)).toBe('登录流程');
  });

  it('uses case-sensitive exact filenames for resource duplicates', () => {
    const adapter = createSectionRegistry(dependencies()).protoFiles;

    expect(adapter.identity?.name(resource('Login.proto'))).toBe('Login.proto');
    expect(adapter.identity?.name(resource('login.proto'))).not.toBe('Login.proto');
  });

  it('动作和监听模板按精确名称合并并交由服务器生成新增身份', () => {
    const registry = createSectionRegistry(dependencies());
    const source: ActionTemplate = {
      id: 'imported',
      name: 'Login',
      pattern: 'setState',
      data: { pattern: 'setState' },
      createdAt: 20,
      updatedAt: 21,
    };
    const target: ActionTemplate = { ...source, id: 'target', createdAt: 10, updatedAt: 11 };
    const identity = registry.actionTemplates.identity!;

    expect(identity.matchBy).toBe('name');
    expect(identity.prepareAdd?.(source)).toMatchObject({ id: '', createdAt: 0, updatedAt: 0 });
    expect(identity.prepareOverwrite?.(target, source)).toEqual({
      ...source,
      id: 'target',
      createdAt: 10,
      updatedAt: 0,
    });
    expect(identity.prepareCopy?.(source, 'Login（副本）')).toMatchObject({
      id: '',
      name: 'Login（副本）',
      createdAt: 0,
      updatedAt: 0,
    });
    expect(registry.listenTemplates.identity?.matchBy).toBe('name');
  });
});

describe('section validation', () => {
  it('validates saved flows and nullable drafts with the flow validator', () => {
    const registry = createSectionRegistry(dependencies());
    const draft: DraftSnapshot = {
      flow: validFlow,
      layout: { nodePositions: {} },
      savedAt: 123,
    };

    expect(() => registry.flows.validate([flow()])).not.toThrow();
    expect(() => registry.draft.validate(draft)).not.toThrow();
    expect(() => registry.draft.validate(null)).not.toThrow();
    expect(() =>
      registry.flows.validate([{ ...flow(), flow: { ...validFlow, nodes: {} } }]),
    ).toThrow('流程校验失败');
  });

  it('allows duplicate display names but rejects duplicate primary ids', () => {
    const registry = createSectionRegistry(dependencies());

    expect(() =>
      registry.flows.validate([flow('id-1', 'Same name'), flow('id-2', 'Same name')]),
    ).not.toThrow();
    expect(() =>
      registry.flows.validate([flow('same-id', 'First'), flow('same-id', 'Second')]),
    ).toThrow('ID');
  });

  it('validates resource metadata and section-specific filenames', () => {
    const registry = createSectionRegistry(dependencies());

    expect(() => registry.protoFiles.validate([resource()])).not.toThrow();
    expect(() => registry.protoFiles.validate([resource('login.lua')])).toThrow('Proto 文件名');
    expect(() => registry.luaFiles.validate([{ ...resource('login.lua'), size: -1 }])).toThrow(
      'size',
    );
  });

  it('validates action and listen template ids, names, timestamps, and data', () => {
    const registry = createSectionRegistry(dependencies());
    const action: ActionTemplate = {
      id: 'action-1',
      name: '登录',
      pattern: 'setState',
      data: { pattern: 'setState' },
      createdAt: 10,
      updatedAt: 10,
    };
    const listen: ListenTemplate = {
      id: 'listen-1',
      name: '推送',
      kind: 'silent',
      data: {},
      createdAt: 20,
      updatedAt: 20,
    };

    expect(() => registry.actionTemplates.validate([action])).not.toThrow();
    expect(() => registry.listenTemplates.validate([listen])).not.toThrow();
    expect(() => registry.actionTemplates.validate([{ ...action, id: '' }])).toThrow('ID');
    expect(() =>
      registry.actionTemplates.validate([
        {
          ...action,
          pattern: 'lua',
          data: { pattern: 'lua' },
        },
      ]),
    ).toThrow('动作模板校验失败');
    expect(() =>
      registry.listenTemplates.validate([
        {
          ...listen,
          kind: 'lua',
          data: {},
        },
      ]),
    ).toThrow('监听模板校验失败');
  });

  it('validates notepad contents and timestamps', () => {
    const registry = createSectionRegistry(dependencies());
    const note: NotepadFile = {
      id: 'note-1',
      name: 'notes.md',
      language: 'markdown',
      content: '# Notes',
      createdAt: '2026-01-01T00:00:00Z',
      updatedAt: '2026-01-02T00:00:00Z',
    };

    expect(() => registry.notepadFiles.validate([note])).not.toThrow();
    expect(() => registry.notepadFiles.validate([{ ...note, updatedAt: 'today' }])).toThrow(
      '更新时间',
    );
  });

  it('accepts an absent error map and rejects reserved or empty descriptions', () => {
    const registry = createSectionRegistry(dependencies());
    const invalidContent = '{"1":"reserved","100":""}';

    expect(() => registry.errorMap.validate(null)).not.toThrow();
    expect(() =>
      registry.errorMap.validate({
        ...resource('errors.json'),
        content: invalidContent,
        size: new Blob([invalidContent]).size,
      }),
    ).toThrow('错误码配置无效');
  });
});

describe('section IO and backup parsing', () => {
  it('reads flow items and replaces them against the latest revision', async () => {
    const deps = dependencies();
    const adapter = createSectionRegistry(deps).flows;

    expect(await adapter.read()).toEqual([flow()]);
    await adapter.replace([flow('id-2', '战斗流程')]);

    expect(deps.replaceFlowSnapshot).toHaveBeenCalledWith({
      expectedRevision: 'sha256:one',
      items: [flow('id-2', '战斗流程')],
    });
  });

  it('refreshes the editor after restoring the draft section', async () => {
    const deps = dependencies();
    const adapter = createSectionRegistry(deps).draft;
    const draft: DraftSnapshot = {
      flow: validFlow,
      layout: { nodePositions: {} },
      savedAt: 123,
    };

    await adapter.refresh?.(draft);

    expect(deps.refreshDraftSnapshot).toHaveBeenCalledWith(draft);
  });

  it('runs section validation while parsing a backup', () => {
    const registry = createSectionRegistry(dependencies());
    const text = JSON.stringify({
      kind: 'stressbot-config-backup',
      schemaVersion: 1,
      exportedAt: '2026-07-23T10:00:00.000Z',
      manifest: { includedSections: ['protoFiles'], counts: { protoFiles: 1 } },
      data: { protoFiles: [resource('wrong.lua')] },
    });

    expect(() => parseBackupWithRegistry(text, registry)).toThrow('Proto 文件名');
  });

  it('动作和监听模板通过独立服务器快照备份与恢复', async () => {
    const deps = dependencies();
    const action: ActionTemplate = {
      id: 'action-imported',
      name: '登录',
      pattern: 'setState',
      data: { pattern: 'setState' },
      createdAt: 10,
      updatedAt: 11,
    };
    const listen: ListenTemplate = {
      id: 'listen-imported',
      name: '推送',
      kind: 'silent',
      data: {},
      createdAt: 20,
      updatedAt: 21,
    };
    vi.mocked(deps.getActionTemplateSnapshot).mockResolvedValue({
      revision: 'action-r1',
      items: [action],
    });
    vi.mocked(deps.getListenTemplateSnapshot).mockResolvedValue({
      revision: 'listen-r7',
      items: [listen],
    });
    const registry = createSectionRegistry(deps);

    await expect(registry.actionTemplates.versioned?.read()).resolves.toEqual({
      revision: 'action-r1',
      value: [action],
    });
    await expect(registry.listenTemplates.versioned?.read()).resolves.toEqual({
      revision: 'listen-r7',
      value: [listen],
    });

    const replaced = await registry.actionTemplates.versioned?.replace({
      expectedRevision: 'action-r1',
      value: [action],
      mode: 'replace',
    });
    expect(deps.replaceActionTemplateSnapshot).toHaveBeenCalledWith({
      expectedRevision: 'action-r1',
      idPolicy: 'preserve',
      items: [action],
    });
    expect(replaced?.value).toEqual([action]);

    const generated = { ...listen, id: '', createdAt: 0, updatedAt: 0 };
    const merged = await registry.listenTemplates.versioned?.replace({
      expectedRevision: 'listen-r7',
      value: [generated],
      mode: 'merge',
    });
    expect(deps.replaceListenTemplateSnapshot).toHaveBeenCalledWith({
      expectedRevision: 'listen-r7',
      idPolicy: 'generate-missing',
      items: [generated],
    });
    expect(merged?.value[0]).toMatchObject({ id: 'server-listen-0', createdAt: 200, updatedAt: 201 });
  });

  it('合并恢复指纹忽略服务器生成的身份，完整恢复指纹保留全部元数据', () => {
    const registry = createSectionRegistry(dependencies());
    const planned: ActionTemplate[] = [{
      id: '',
      name: '登录',
      pattern: 'setState',
      data: { pattern: 'setState' },
      createdAt: 0,
      updatedAt: 0,
    }];
    const persisted: ActionTemplate[] = [{
      ...planned[0],
      id: 'server-generated',
      createdAt: 100,
      updatedAt: 101,
    }];
    const fingerprint = registry.actionTemplates.versioned?.fingerprint;

    expect(fingerprint?.(planned, 'merge')).toBe(fingerprint?.(persisted, 'merge'));
    expect(fingerprint?.(planned, 'replace')).not.toBe(fingerprint?.(persisted, 'replace'));
  });
});
