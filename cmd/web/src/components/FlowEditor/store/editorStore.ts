/**
 * 编辑器 UI 状态：选中节点、面板、悬停。
 *
 * 与 flowStore 分离，避免业务数据频繁触发 UI 重渲染。
 */

import { create } from 'zustand';
import type { FlowNode } from '@/types/flow';
import type { ActionDef } from '@/types/action';
import type { ListenDef } from '@/types/listen';
import type { ListenTemplateDefaultRef } from '../library/templateStore';
import type { BaselineSyncResult } from '@/services/resourcesStore';
import { useFloatingWindowStore } from './floatingWindowStore';

export type ActivePanel =
  | { kind: 'nodeEdit'; nodeId: string }
  | { kind: 'actionEdit'; actionName: string }
  | { kind: 'listenEdit'; listenName: string }
  | { kind: 'protoBrowser' }
  | { kind: 'listenPanel' }
  | { kind: 'jsonPreview' }
  | { kind: 'templateEdit'; templateKind: 'action' | 'listen'; templateId: string };

/** 每个面板 kind 独立存储，互不干扰 */
export type ActivePanels = Partial<Record<ActivePanel['kind'], ActivePanel>>;

export type ThemeMode = 'light' | 'dark';

const THEME_STORAGE_KEY = 'stressbot:theme';
const DEBUG_MODE_STORAGE_KEY = 'stressbot:debugMode';
const TASK_FORM_ADV_STORAGE_KEY = 'stressbot:taskForm:advancedExpanded';

export const EDITOR_PANEL_WIDTH = 720;

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

function readInitialDebugMode(): boolean {
  try {
    return localStorage.getItem(DEBUG_MODE_STORAGE_KEY) === '1';
  } catch {
    return false;
  }
}

function readInitialTaskFormAdvancedExpanded(): boolean {
  try {
    return localStorage.getItem(TASK_FORM_ADV_STORAGE_KEY) === '1';
  } catch {
    return false;
  }
}

function applyThemeAttr(t: ThemeMode) {
  if (typeof document !== 'undefined') {
    document.documentElement.setAttribute('data-theme', t);
  }
}

/**
 * 剪贴板内容：
 *   - 节点（含其引用的 action / 监听的 listen 完整数据，方便跨流程粘贴）
 *   - 单个 listen 卡片
 */
export type Clipboard =
  | null
  | {
      kind: 'node';
      nodeId: string;
      node: FlowNode;
      action?: { name: string; def: ActionDef };
      listens?: Array<{ name: string; def: ListenDef; defaultRef?: ListenTemplateDefaultRef }>;
    }
  | {
      kind: 'listen';
      listenName: string;
      listen: ListenDef;
      defaultRef?: ListenTemplateDefaultRef;
    };

interface EditorState {
  selectedNodeId: string | null;
  /** 校验报告"跳转"请求：画布把目标节点 pan 到中央并选中高亮。nonce 每次自增，连点同节点也触发。 */
  focusRequestNodeId: string | null;
  focusRequestNonce: number;
  selectedListenName: string | null;
  hoveredListen: string | null;
  /** 当前选中的边相关的节点 ID 集合（用于高亮显示） */
  edgeHighlightNodeIds: string[];
  /** 选中边发光颜色（CSS 颜色 / var()），通过 .edge-highlight 子节点的 inline style 注入 */
  edgeHighlightColor: string | null;
  activePanel: ActivePanels;

  showListenEdges: boolean;
  showMinimap: boolean;
  showGrid: boolean;
  theme: ThemeMode;
  clipboard: Clipboard;
  /** MonitorDock 是否展开；编辑态默认关，运行态默认开（首次切换由 MonitorDock 内部按 mode 切换） */
  monitorDockOpen: boolean;
  /**
   * 调试模式：开启后启动表单一键装填为最小压测配置（1 个机器人 / 并发 1），
   * 同时 startTask 走 skipCapacityCheck=true，便于本地快速验证流程。
   * 持久化到 localStorage，刷新页面保留。
   */
  debugMode: boolean;
  /**
   * 启动任务弹窗中"高级设置"折叠面板的展开状态，持久化到 localStorage。
   * 这样用户上次手动展开了，下次刷新打开弹窗仍是展开的，避免"看上去字段没缓存"的错觉
   * （实际上字段值缓存在 runtimeStore.persist 里，但折叠时看不到）。
   */
  taskFormAdvancedExpanded: boolean;

  /**
   * 后端 history 模块是否启用：
   *   - null：尚未探测（启动时探测）；
   *   - true：listHistory() 调用成功；
   *   - false：返回 HISTORY_DISABLED（admin 未配置 MySQL）。
   * UI 据此 disable "历史" 按钮，避免点了之后才弹错。
   */
  historyEnabled: boolean | null;

  /** 基线同步冲突结果（未处理时非 null），用于资源按钮 badge 和 ResourcesDrawer 处理冲突入口 */
  pendingSyncResult: BaselineSyncResult | null;
  setPendingSyncResult: (r: BaselineSyncResult | null) => void;

  /** 协议配置的 schema 校验错误数组；null=未校验/空，空数组=全部通过 */
  codecSchemaErrors: string[] | null;
  setCodecSchemaErrors: (v: string[] | null) => void;

  /**
   * routeKey 模板缓存版本号：每次 refreshRouteKeyTemplates 完成（mount 加载 /
   * codec CRUD 后刷新）时 bump。validateFlow 的 useMemo 把它列入依赖，使缓存
   * 就绪后校验报告自动重算（ROUTEKEY_CODEC_MISSING warning 随真实 codec 状态刷新，
   * 不再卡到下次 flow 编辑）。对齐 proto 模式的 protoStore.set({status:'ready'})。
   */
  routeKeyTemplatesVersion: number;
  bumpRouteKeyTemplatesVersion: () => void;

  setSelectedNode: (id: string | null) => void;
  requestFocusNode: (nodeId: string) => void;
  setSelectedListen: (name: string | null) => void;
  setHoveredListen: (name: string | null) => void;
  setEdgeHighlightNodes: (ids: string[]) => void;
  setEdgeHighlightColor: (color: string | null) => void;
  setActivePanel: (p: ActivePanel | null) => void;
  closePanel: (kind: ActivePanel['kind']) => void;
  toggleListenEdges: () => void;
  setShowMinimap: (v: boolean) => void;
  setShowGrid: (v: boolean) => void;
  setTheme: (t: ThemeMode) => void;
  toggleTheme: () => void;
  setClipboard: (c: Clipboard) => void;
  setMonitorDockOpen: (v: boolean) => void;
  setDebugMode: (v: boolean) => void;
  setTaskFormAdvancedExpanded: (v: boolean) => void;
  setHistoryEnabled: (v: boolean | null) => void;
}

const initialTheme = readInitialTheme();
applyThemeAttr(initialTheme);

export const useEditorStore = create<EditorState>()((set, get) => ({
  selectedNodeId: null,
  focusRequestNodeId: null,
  focusRequestNonce: 0,
  selectedListenName: null,
  hoveredListen: null,
  edgeHighlightNodeIds: [],
  edgeHighlightColor: null,
  activePanel: {},
  showListenEdges: true,
  showMinimap: true,
  showGrid: true,
  theme: initialTheme,
  clipboard: null,
  monitorDockOpen: false,
  debugMode: readInitialDebugMode(),
  taskFormAdvancedExpanded: readInitialTaskFormAdvancedExpanded(),
  historyEnabled: null,
  pendingSyncResult: null,
  setPendingSyncResult: (r) => set({ pendingSyncResult: r }),
  codecSchemaErrors: null,
  setCodecSchemaErrors: (v) => set({ codecSchemaErrors: v }),
  routeKeyTemplatesVersion: 0,
  bumpRouteKeyTemplatesVersion: () => set((s) => ({ routeKeyTemplatesVersion: s.routeKeyTemplatesVersion + 1 })),

  setSelectedNode: (id) => set({ selectedNodeId: id }),
  requestFocusNode: (nodeId) => set((s) => ({ focusRequestNodeId: nodeId, focusRequestNonce: s.focusRequestNonce + 1 })),
  setSelectedListen: (name) => set({ selectedListenName: name }),
  setHoveredListen: (name) => set({ hoveredListen: name }),
  setEdgeHighlightNodes: (ids) => set({ edgeHighlightNodeIds: ids }),
  setEdgeHighlightColor: (color) => set({ edgeHighlightColor: color }),
  setActivePanel: (p) => {
    const fws = useFloatingWindowStore.getState();
    if (p === null) {
      // 关闭所有面板
      fws.closeAllWindows();
      set({ activePanel: {} });
    } else {
      fws.openWindow(p.kind, DEFAULT_SIZES[p.kind] ?? { width: 640, height: 500 });
      set((s) => ({ activePanel: { ...s.activePanel, [p.kind]: p } }));
    }
  },
  closePanel: (kind) => {
    const fws = useFloatingWindowStore.getState();
    fws.closeWindow(kind);
    set((s) => {
      const rest = { ...s.activePanel };
      delete rest[kind];
      return { activePanel: rest };
    });
  },
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
  setMonitorDockOpen: (v) => set({ monitorDockOpen: v }),
  setDebugMode: (v) => {
    try {
      localStorage.setItem(DEBUG_MODE_STORAGE_KEY, v ? '1' : '0');
    } catch {
      // localStorage 不可用时只切换运行时
    }
    set({ debugMode: v });
  },
  setTaskFormAdvancedExpanded: (v) => {
    try {
      localStorage.setItem(TASK_FORM_ADV_STORAGE_KEY, v ? '1' : '0');
    } catch {
      // localStorage 不可用时只切换运行时
    }
    set({ taskFormAdvancedExpanded: v });
  },
  setHistoryEnabled: (v) => set({ historyEnabled: v }),
}));

/** 各面板类型的默认窗口尺寸 */
const DEFAULT_SIZES: Record<string, { width: number; height: number }> = {
  nodeEdit: { width: EDITOR_PANEL_WIDTH, height: 500 },
  listenEdit: { width: EDITOR_PANEL_WIDTH, height: 520 },
  protoBrowser: { width: 780, height: 540 },
  listenPanel: { width: 440, height: 560 },
  jsonPreview: { width: 800, height: 560 },
  templateEdit: { width: 680, height: 520 },
};
