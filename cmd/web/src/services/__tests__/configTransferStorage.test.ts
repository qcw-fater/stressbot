import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const stores = new Map<unknown, Map<unknown, unknown>>();
const storageOperations: string[] = [];

vi.mock('idb-keyval', () => {
  function storeMap(store: unknown): Map<unknown, unknown> {
    let values = stores.get(store);
    if (!values) {
      values = new Map();
      stores.set(store, values);
    }
    return values;
  }

  return {
    createStore: vi.fn((db: string, store: string) => ({ db, store })),
    get: vi.fn(async (key: unknown, store: unknown) => storeMap(store).get(key)),
    set: vi.fn(async (key: unknown, value: unknown, store: unknown) => {
      storeMap(store).set(key, value);
    }),
    setMany: vi.fn(async (entries: Array<[unknown, unknown]>, store: unknown) => {
      storageOperations.push('setMany');
      const values = storeMap(store);
      for (const [key, value] of entries) values.set(key, value);
    }),
    delMany: vi.fn(async (keys: unknown[], store: unknown) => {
      storageOperations.push('delMany');
      const values = storeMap(store);
      for (const key of keys) values.delete(key);
    }),
    del: vi.fn(async (key: unknown, store: unknown) => {
      storeMap(store).delete(key);
    }),
    keys: vi.fn(async (store: unknown) => [...storeMap(store).keys()]),
    clear: vi.fn(async (store: unknown) => {
      storageOperations.push('clear');
      storeMap(store).clear();
    }),
  };
});

import {
  getErrorMap,
  listCodecFiles,
  listProto,
  listScript,
  replaceCodecFiles,
  replaceErrorMap,
  replaceProtoFiles,
  replaceScriptFiles,
  type ResourceFile,
} from '../resourcesStore';
import {
  listActionTemplates,
  listListenTemplates,
  onTemplateChange,
  replaceActionTemplates,
  replaceListenTemplates,
  type ActionTemplate,
  type ListenTemplate,
} from '@/components/FlowEditor/library/templateStore';
import {
  exportNotepadFiles,
  replaceNotepadFiles,
  useNotepadStore,
  type NotepadFile,
} from '@/components/modules/notepad/notepadStore';
import {
  captureCurrentDraft,
  loadDraft,
  refreshDraftSnapshot,
  saveDraftSnapshot,
  type DraftSnapshot,
} from '@/components/FlowEditor/store/persistDraft';
import { useFlowStore } from '@/components/FlowEditor/store/flowStore';

function resource(name: string, content: string, baseHash: string): ResourceFile {
  return {
    name,
    content,
    size: new Blob([content]).size,
    uploadedAt: '2026-01-02T03:04:05.000Z',
    baseHash,
  };
}

beforeEach(() => {
  stores.clear();
  storageOperations.length = 0;
  localStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('resource snapshots', () => {
  it('replaces Proto and script files without regenerating metadata', async () => {
    const proto = resource('login.proto', 'syntax="proto3";', 'sha256:proto');
    const script = resource('login.lua', 'return true', 'sha256:script');

    await replaceProtoFiles([resource('legacy.proto', 'legacy', 'sha256:legacy')]);
    await replaceScriptFiles([resource('legacy.lua', 'legacy', 'sha256:legacy')]);
    await replaceProtoFiles([proto]);
    await replaceScriptFiles([script]);

    expect(await listProto()).toEqual([proto]);
    expect(await listScript()).toEqual([script]);
  });

  it('replaces codecs and the error map independently', async () => {
    const errorMap = resource('errors.json', '{"100":"failed"}', 'sha256:errors');
    const codec = resource('tcp_logic_codec.json', '{"version":1}', 'sha256:codec');

    await replaceErrorMap(errorMap);
    await replaceCodecFiles([codec]);
    expect(await getErrorMap()).toEqual(errorMap);
    expect(await listCodecFiles()).toEqual([codec]);

    await replaceErrorMap(null);
    expect(await getErrorMap()).toBeUndefined();
    expect(await listCodecFiles()).toEqual([codec]);
  });

  it('writes replacement codecs before deleting obsolete keys', async () => {
    const errorMap = resource('errors.json', '{"100":"failed"}', 'sha256:errors');
    const legacy = resource('tcp_legacy_codec.json', '{"version":1}', 'sha256:legacy');
    const codec = resource('tcp_logic_codec.json', '{"version":2}', 'sha256:codec');
    await replaceErrorMap(errorMap);
    await replaceCodecFiles([legacy]);
    storageOperations.length = 0;

    await replaceCodecFiles([codec]);

    expect(storageOperations).toEqual(['setMany', 'delMany']);
    expect(await getErrorMap()).toEqual(errorMap);
    expect(await listCodecFiles()).toEqual([codec]);
  });
});

describe('template snapshots', () => {
  it('replaces one template section exactly and emits one batch notification', async () => {
    const listen: ListenTemplate = {
      id: 'listen-1',
      name: 'Push',
      kind: 'silent',
      data: {},
      createdAt: 10,
    };
    const action: ActionTemplate = {
      id: 'action-1',
      name: 'Login',
      pattern: 'setState',
      data: { pattern: 'setState' },
      createdAt: 20,
    };
    await replaceListenTemplates([listen]);
    await replaceActionTemplates([{ ...action, id: 'legacy', name: 'Legacy' }]);
    const onChange = vi.fn();
    const unsubscribe = onTemplateChange(onChange);

    await replaceActionTemplates([action]);

    expect(await listActionTemplates()).toEqual([action]);
    expect(await listListenTemplates()).toEqual([listen]);
    expect(onChange).toHaveBeenCalledTimes(1);
    unsubscribe();
  });
});

describe('notepad snapshots', () => {
  const files: NotepadFile[] = [
    {
      id: 'n1',
      name: 'one.md',
      language: 'markdown',
      content: 'old one',
      createdAt: '2026-01-01T00:00:00.000Z',
      updatedAt: '2026-01-02T00:00:00.000Z',
    },
    {
      id: 'n2',
      name: 'two.lua',
      language: 'lua',
      content: 'old two',
      createdAt: '2026-01-03T00:00:00.000Z',
      updatedAt: '2026-01-04T00:00:00.000Z',
    },
  ];

  it('exports and replaces exact ids, metadata, and contents', async () => {
    await replaceNotepadFiles([{ ...files[0], id: 'legacy', name: 'legacy.md' }]);
    await replaceNotepadFiles(files);

    expect(await exportNotepadFiles()).toEqual(files);
    expect(useNotepadStore.getState()).toMatchObject({
      files: files.map(({ content: _content, ...meta }) => meta),
      activeFileId: null,
      activeContent: '',
      contentLoaded: false,
    });
  });

  it('flushes each pending file with its own captured content before export', async () => {
    await replaceNotepadFiles(files);
    await useNotepadStore.getState().selectFile('n1');
    useNotepadStore.getState().updateContent('n1', 'pending one');
    await useNotepadStore.getState().selectFile('n2');
    useNotepadStore.getState().updateContent('n2', 'pending two');

    const exported = await exportNotepadFiles();

    expect(exported.map(({ id, content }) => ({ id, content }))).toEqual([
      { id: 'n1', content: 'pending one' },
      { id: 'n2', content: 'pending two' },
    ]);
  });
});

describe('draft snapshots', () => {
  it('saves, loads, and clears an exact snapshot', () => {
    const snapshot: DraftSnapshot = {
      flow: { defaultDelayMs: 7, nodes: {}, actions: {}, listens: {} },
      layout: { nodePositions: { start: { x: 10, y: 20 } }, showListenEdges: false },
      savedAt: 123456,
    };

    saveDraftSnapshot(snapshot);
    expect(loadDraft()).toEqual(snapshot);

    saveDraftSnapshot(null);
    expect(loadDraft()).toBeNull();
  });

  it('captures current in-memory flow and clones its layout', () => {
    const layout = { nodePositions: { start: { x: 10, y: 20 } }, showListenEdges: true };
    useFlowStore.setState({
      defaultDelayMs: 12,
      nodes: {},
      actions: {},
      listens: {},
      layout,
    });
    vi.spyOn(Date, 'now').mockReturnValue(789);

    const snapshot = captureCurrentDraft();
    layout.nodePositions.start.x = 99;

    expect(snapshot).toEqual({
      flow: { defaultDelayMs: 12, nodes: {}, actions: {}, listens: {} },
      layout: { nodePositions: { start: { x: 10, y: 20 } }, showListenEdges: true },
      savedAt: 789,
    });
  });

  it('refreshes the in-memory editor from a restored snapshot or an empty draft', () => {
    const snapshot: DraftSnapshot = {
      flow: { defaultDelayMs: 7, nodes: {}, actions: {}, listens: {} },
      layout: { nodePositions: {} },
      savedAt: 123456,
    };
    const load = vi.spyOn(useFlowStore.getState(), 'loadFromTaskFlow');
    const reset = vi.spyOn(useFlowStore.getState(), 'reset');

    refreshDraftSnapshot(snapshot);
    refreshDraftSnapshot(null);

    expect(load).toHaveBeenCalledWith(snapshot.flow, snapshot.layout);
    expect(reset).toHaveBeenCalledTimes(1);
  });
});
