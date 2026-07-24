import { describe, expect, it, vi } from 'vitest';

import type { FlowJson } from '@/components/FlowEditor/codec/flowToJson';
import type { ActionTemplate, ListenTemplate } from '@/components/FlowEditor/library/templateStore';
import type { DraftSnapshot } from '@/components/FlowEditor/store/persistDraft';
import type { NotepadFile } from '@/components/modules/notepad/notepadStore';
import type { FlowTemplateDetail } from '../flowsApi';
import type { ResourceFile } from '../resourcesStore';
import { parseBackupWithRegistry } from './backupCodec';
import {
  createSectionRegistry,
  type SectionRegistryDependencies,
} from './sectionRegistry';

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
    listListenTemplates: vi.fn(async () => []),
    replaceListenTemplates: vi.fn(async () => undefined),
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
    expect(() => registry.flows.validate([{ ...flow(), flow: { ...validFlow, nodes: {} } }]))
      .toThrow('流程校验失败');
  });

  it('validates resource metadata and section-specific filenames', () => {
    const registry = createSectionRegistry(dependencies());

    expect(() => registry.protoFiles.validate([resource()])).not.toThrow();
    expect(() => registry.protoFiles.validate([resource('login.lua')]))
      .toThrow('Proto 文件名');
    expect(() => registry.luaFiles.validate([{ ...resource('login.lua'), size: -1 }]))
      .toThrow('size');
  });

  it('validates action and listen template ids, names, timestamps, and data', () => {
    const registry = createSectionRegistry(dependencies());
    const action: ActionTemplate = {
      id: 'action-1',
      name: '登录',
      pattern: 'setState',
      data: { pattern: 'setState' },
      createdAt: 10,
    };
    const listen: ListenTemplate = {
      id: 'listen-1',
      name: '推送',
      kind: 'silent',
      data: {},
      createdAt: 20,
    };

    expect(() => registry.actionTemplates.validate([action])).not.toThrow();
    expect(() => registry.listenTemplates.validate([listen])).not.toThrow();
    expect(() => registry.actionTemplates.validate([{ ...action, id: '' }]))
      .toThrow('ID');
    expect(() => registry.actionTemplates.validate([{
      ...action,
      pattern: 'lua',
      data: { pattern: 'lua' },
    }])).toThrow('动作模板校验失败');
    expect(() => registry.listenTemplates.validate([{
      ...listen,
      kind: 'lua',
      data: {},
    }])).toThrow('监听模板校验失败');
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
    expect(() => registry.notepadFiles.validate([{ ...note, updatedAt: 'today' }]))
      .toThrow('更新时间');
  });

  it('accepts an absent error map and rejects reserved or empty descriptions', () => {
    const registry = createSectionRegistry(dependencies());
    const invalidContent = '{"1":"reserved","100":""}';

    expect(() => registry.errorMap.validate(null)).not.toThrow();
    expect(() => registry.errorMap.validate({
      ...resource('errors.json'),
      content: invalidContent,
      size: new Blob([invalidContent]).size,
    })).toThrow('错误码配置无效');
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
});
