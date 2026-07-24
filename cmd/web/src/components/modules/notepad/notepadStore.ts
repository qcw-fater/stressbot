/**
 * 记事本文件管理（本地存储 + Zustand）。
 *
 * 设计要点：
 * - 使用 idb-keyval 的 createStore 模式（与 resourcesStore.ts 一致）；
 * - `__index__` key 存储文件元数据列表（无 content），用于快速列表渲染；
 * - 内容按 `file:${id}` 单独存储，编辑时只更新单文件；
 * - 语言由文件扩展名自动检测；
 * - updateContent 内置 300ms debounce 自动保存。
 */

import { createStore, get, set, del } from 'idb-keyval';
import { create } from 'zustand';
import { nanoid } from 'nanoid';

const DB_NAME = 'stressbot-notepad';
const notepadStore = createStore(DB_NAME, 'data');
const INDEX_KEY = '__index__';

export interface NotepadFileMeta {
  id: string;
  name: string;
  language: string;
  createdAt: string;
  updatedAt: string;
}

export interface NotepadFile extends NotepadFileMeta {
  content: string;
}

// ── 语言检测 ──

const EXT_LANG_MAP: Record<string, string> = {
  lua: 'lua',
  proto: 'proto',
  json: 'json',
  js: 'javascript',
  ts: 'typescript',
  go: 'go',
  cs: 'csharp',
  py: 'python',
  xml: 'xml',
  html: 'html',
  css: 'css',
  yaml: 'yaml',
  yml: 'yaml',
  md: 'markdown',
  sql: 'sql',
  sh: 'shell',
  txt: 'plaintext',
};

function detectLanguage(filename: string): string {
  const ext = filename.split('.').pop()?.toLowerCase() ?? '';
  return EXT_LANG_MAP[ext] ?? 'plaintext';
}

// ── 本地存储 helpers ──

async function loadIndex(): Promise<NotepadFileMeta[]> {
  const raw = await get<NotepadFileMeta[]>(INDEX_KEY, notepadStore);
  return raw ?? [];
}

async function saveIndex(list: NotepadFileMeta[]): Promise<void> {
  await set(INDEX_KEY, list, notepadStore);
}

async function loadFileContent(id: string): Promise<string> {
  const raw = await get<string>(`file:${id}`, notepadStore);
  return raw ?? '';
}

async function saveFileContent(id: string, content: string): Promise<void> {
  await set(`file:${id}`, content, notepadStore);
}

// ── Debounce map ──

interface PendingSave {
  timer: ReturnType<typeof setTimeout>;
  content: string;
}

const saveTimers = new Map<string, PendingSave>();

function debounceSave(id: string, content: string, cb: () => void, ms = 300): void {
  const existing = saveTimers.get(id);
  if (existing) clearTimeout(existing.timer);
  const timer = setTimeout(async () => {
    saveTimers.delete(id);
    await saveFileContent(id, content);
    cb();
  }, ms);
  saveTimers.set(id, { timer, content });
}

export async function flushAllPendingSaves(): Promise<void> {
  const pending = [...saveTimers.entries()];
  if (pending.length === 0) return;
  saveTimers.clear();
  for (const [, value] of pending) clearTimeout(value.timer);
  await Promise.all(pending.map(([id, value]) => saveFileContent(id, value.content)));
  await saveIndex(useNotepadStore.getState().files);
}

export async function exportNotepadFiles(): Promise<NotepadFile[]> {
  await flushAllPendingSaves();
  const files = await loadIndex();
  return Promise.all(files.map(async (meta) => ({
    ...meta,
    content: await loadFileContent(meta.id),
  })));
}

export async function replaceNotepadFiles(files: readonly NotepadFile[]): Promise<void> {
  await flushAllPendingSaves();
  const existing = await loadIndex();
  await Promise.all(existing.map((meta) => del(`file:${meta.id}`, notepadStore)));
  await Promise.all(files.map((file) => saveFileContent(file.id, file.content)));
  const metadata = files.map(({ content: _content, ...meta }) => ({ ...meta }));
  await saveIndex(metadata);
  useNotepadStore.setState({
    files: metadata,
    activeFileId: null,
    activeContent: '',
    contentLoaded: false,
  });
}

// ── Zustand Store ──

interface NotepadState {
  files: NotepadFileMeta[];
  activeFileId: string | null;
  searchQuery: string;
  // cached content for active file
  activeContent: string;
  contentLoaded: boolean;

  loadFileList: () => Promise<void>;
  selectFile: (id: string | null) => Promise<void>;
  createFile: (name: string) => Promise<NotepadFileMeta>;
  importFiles: (fileList: { name: string; content: string }[]) => Promise<void>;
  updateContent: (id: string, content: string) => void;
  renameFile: (id: string, newName: string) => Promise<void>;
  deleteFile: (id: string) => Promise<void>;
  setSearchQuery: (q: string) => void;
  flushPendingSave: (id: string) => Promise<void>;
}

export const useNotepadStore = create<NotepadState>()((set, get) => ({
  files: [],
  activeFileId: null,
  searchQuery: '',
  activeContent: '',
  contentLoaded: false,

  loadFileList: async () => {
    const list = await loadIndex();
    set({ files: list });
  },

  selectFile: async (id) => {
    if (!id) {
      set({ activeFileId: null, activeContent: '', contentLoaded: false });
      return;
    }
    const content = await loadFileContent(id);
    set({ activeFileId: id, activeContent: content, contentLoaded: true });
  },

  createFile: async (name) => {
    const id = nanoid(10);
    const now = new Date().toISOString();
    const meta: NotepadFileMeta = {
      id,
      name,
      language: detectLanguage(name),
      createdAt: now,
      updatedAt: now,
    };
    await saveFileContent(id, '');
    const list = [...(await loadIndex()), meta];
    await saveIndex(list);
    set({ files: list });
    return meta;
  },

  importFiles: async (fileList) => {
    const now = new Date().toISOString();
    const newMetas: NotepadFileMeta[] = fileList.map((f) => ({
      id: nanoid(10),
      name: f.name,
      language: detectLanguage(f.name),
      createdAt: now,
      updatedAt: now,
    }));
    // save contents in parallel
    await Promise.all(newMetas.map((m, i) => saveFileContent(m.id, fileList[i].content)));
    const list = [...(await loadIndex()), ...newMetas];
    await saveIndex(list);
    set({ files: list });
  },

  updateContent: (id, content) => {
    // immediately update local state
    if (get().activeFileId === id) {
      set({ activeContent: content });
    }
    // update updatedAt in index
    const files = get().files.map((f) =>
      f.id === id ? { ...f, updatedAt: new Date().toISOString() } : f,
    );
    set({ files });
    debounceSave(id, content, async () => {
      await saveIndex(get().files);
    });
  },

  renameFile: async (id, newName) => {
    const files = get().files.map((f) =>
      f.id === id ? { ...f, name: newName, language: detectLanguage(newName), updatedAt: new Date().toISOString() } : f,
    );
    await saveIndex(files);
    set({ files });
  },

  deleteFile: async (id) => {
    await del(`file:${id}`, notepadStore);
    const files = get().files.filter((f) => f.id !== id);
    await saveIndex(files);
    const { activeFileId } = get();
    if (activeFileId === id) {
      set({ files, activeFileId: null, activeContent: '', contentLoaded: false });
    } else {
      set({ files });
    }
  },

  setSearchQuery: (q) => set({ searchQuery: q }),

  flushPendingSave: async (id) => {
    const pending = saveTimers.get(id);
    if (pending) {
      clearTimeout(pending.timer);
      saveTimers.delete(id);
      await saveFileContent(id, pending.content);
      await saveIndex(get().files);
    }
  },
}));
