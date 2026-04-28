/**
 * 编辑器 UI 状态：选中节点、面板、悬停。
 *
 * 与 flowStore 分离，避免业务数据频繁触发 UI 重渲染。
 */

import { create } from 'zustand';
import type { FlowNode } from '@/types/flow';
import type { ActionDef } from '@/types/action';
import type { CallbackDef } from '@/types/callback';

export type ActivePanel =
  | { kind: 'none' }
  | { kind: 'nodeEdit'; nodeId: string }
  | { kind: 'actionEdit'; actionName: string }
  | { kind: 'callbackEdit'; callbackName: string }
  | { kind: 'protoBrowser' }
  | { kind: 'callbackPanel' }
  | { kind: 'jsonPreview' }
  | { kind: 'templateEdit'; templateKind: 'action' | 'callback'; templateId: string }
  | { kind: 'codecAdapter' };

export type ThemeMode = 'light' | 'dark';

const THEME_STORAGE_KEY = 'stressbot:theme';

function readInitialTheme(): ThemeMode {
  try {
    const v = localStorage.getItem(THEME_STORAGE_KEY);
    if (v === 'light' || v === 'dark') return v;
  } catch {
    // SSR / 无 localStorage
  }
  if (typeof window !== 'undefined' && window.matchMedia?.('(prefers-color-scheme: dark)').matches) {
    return 'dark';
  }
  return 'light';
}

function applyThemeAttr(t: ThemeMode) {
  if (typeof document !== 'undefined') {
    document.documentElement.setAttribute('data-theme', t);
  }
}

/**
 * 剪贴板内容：
 *   - 节点（含其引用的 action / 监听的 callback 完整数据，方便跨流程粘贴）
 *   - 单个 callback 卡片
 */
export type Clipboard =
  | null
  | {
      kind: 'node';
      nodeId: string;
      node: FlowNode;
      action?: { name: string; def: ActionDef };
      callbacks?: Array<{ name: string; def: CallbackDef }>;
    }
  | {
      kind: 'callback';
      callbackName: string;
      callback: CallbackDef;
    };

interface EditorState {
  selectedNodeId: string | null;
  selectedCallbackName: string | null;
  hoveredCallback: string | null;
  /** 当前选中的边相关的节点 ID 集合（用于高亮显示） */
  edgeHighlightNodeIds: string[];
  /** 选中边发光颜色（CSS 颜色 / var()），通过 .edge-highlight 子节点的 inline style 注入 */
  edgeHighlightColor: string | null;
  activePanel: ActivePanel;

  showListenEdges: boolean;
  showMinimap: boolean;
  showGrid: boolean;
  theme: ThemeMode;
  clipboard: Clipboard;

  setSelectedNode: (id: string | null) => void;
  setSelectedCallback: (name: string | null) => void;
  setHoveredCallback: (name: string | null) => void;
  setEdgeHighlightNodes: (ids: string[]) => void;
  setEdgeHighlightColor: (color: string | null) => void;
  setActivePanel: (p: ActivePanel) => void;
  toggleListenEdges: () => void;
  setShowMinimap: (v: boolean) => void;
  setShowGrid: (v: boolean) => void;
  setTheme: (t: ThemeMode) => void;
  toggleTheme: () => void;
  setClipboard: (c: Clipboard) => void;
}

const initialTheme = readInitialTheme();
applyThemeAttr(initialTheme);

export const useEditorStore = create<EditorState>((set, get) => ({
  selectedNodeId: null,
  selectedCallbackName: null,
  hoveredCallback: null,
  edgeHighlightNodeIds: [],
  edgeHighlightColor: null,
  activePanel: { kind: 'none' },
  showListenEdges: true,
  showMinimap: true,
  showGrid: true,
  theme: initialTheme,
  clipboard: null,

  setSelectedNode: (id) => set({ selectedNodeId: id }),
  setSelectedCallback: (name) => set({ selectedCallbackName: name }),
  setHoveredCallback: (name) => set({ hoveredCallback: name }),
  setEdgeHighlightNodes: (ids) => set({ edgeHighlightNodeIds: ids }),
  setEdgeHighlightColor: (color) => set({ edgeHighlightColor: color }),
  setActivePanel: (p) => set({ activePanel: p }),
  toggleListenEdges: () => set((s) => ({ showListenEdges: !s.showListenEdges })),
  setShowMinimap: (v) => set({ showMinimap: v }),
  setShowGrid: (v) => set({ showGrid: v }),
  setTheme: (t) => {
    applyThemeAttr(t);
    try {
      localStorage.setItem(THEME_STORAGE_KEY, t);
    } catch {
      // localStorage 不可用时只切换运行时
    }
    set({ theme: t });
  },
  toggleTheme: () => {
    const next: ThemeMode = get().theme === 'light' ? 'dark' : 'light';
    get().setTheme(next);
  },
  setClipboard: (c) => set({ clipboard: c }),
}));
