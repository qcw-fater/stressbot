/**
 * 浮动窗口状态管理：支持多窗口同时打开、拖拽、缩放、zIndex 焦点管理。
 */

import { create } from 'zustand';

export interface WindowPosition { x: number; y: number }
export interface WindowSize { width: number; height: number }

export interface WindowState {
  id: string;
  position: WindowPosition;
  size: WindowSize;
  zIndex: number;
}

interface FloatingWindowStore {
  windows: Record<string, WindowState>;
  /** 单调递增的 zIndex 计数器 */
  _nextZ: number;

  openWindow: (
    id: string,
    defaultSize: WindowSize,
    defaultPosition?: WindowPosition,
  ) => void;
  closeWindow: (id: string) => void;
  closeAllWindows: () => void;
  focusWindow: (id: string) => void;
  updatePosition: (id: string, pos: WindowPosition) => void;
  updateSize: (id: string, size: WindowSize) => void;
  /** 查看某个窗口是否打开 */
  isOpen: (id: string) => boolean;
  /** 视口缩放时重新夹紧所有打开窗口的尺寸/位置，避免控件落到屏幕外。 */
  reclampAll: () => void;
}

/** 计算居中位置，多个窗口时小偏移避免完全重叠 */
function centerPosition(idx: number, size: WindowSize): WindowPosition {
  const cx = (window.innerWidth - size.width) / 2;
  const cy = (window.innerHeight - size.height) / 2;
  const offset = idx * 28;
  return {
    x: Math.max(0, cx + offset),
    y: Math.max(0, cy + offset),
  };
}

/** 按当前视口夹紧窗口尺寸，避免打开/缩放后窗口大于可视区、标题栏与关闭按钮落到屏幕外。 */
function clampSize(size: WindowSize): WindowSize {
  if (typeof window === 'undefined') return size;
  const maxW = Math.max(320, window.innerWidth - 24);
  const maxH = Math.max(200, window.innerHeight - 24);
  return {
    width: Math.min(size.width, maxW),
    height: Math.min(size.height, maxH),
  };
}

/** 按尺寸夹紧位置，保证窗口右下边不超出视口（左上已由 centerPosition 保 >=0）。 */
function clampPosition(pos: WindowPosition, size: WindowSize): WindowPosition {
  if (typeof window === 'undefined') return pos;
  const maxX = Math.max(0, window.innerWidth - size.width - 8);
  const maxY = Math.max(0, window.innerHeight - size.height - 8);
  return {
    x: Math.min(Math.max(0, pos.x), maxX),
    y: Math.min(Math.max(0, pos.y), maxY),
  };
}

export const useFloatingWindowStore = create<FloatingWindowStore>()((set, get) => ({
  windows: {},
  _nextZ: 1000,

  openWindow: (id, defaultSize, defaultPosition) => {
    set((s) => {
      // 已存在则只 focus
      if (s.windows[id]) {
        const nextZ = s._nextZ + 1;
        return {
          _nextZ: nextZ,
          windows: { ...s.windows, [id]: { ...s.windows[id], zIndex: nextZ } },
        };
      }
      const idx = Object.keys(s.windows).length;
      // 先按视口夹紧尺寸，再据此算位置并夹紧，避免窗口大于可视区时控件落到屏幕外。
      const size = clampSize(defaultSize);
      const pos = clampPosition(defaultPosition ?? centerPosition(idx, size), size);
      const nextZ = s._nextZ + 1;
      return {
        _nextZ: nextZ,
        windows: {
          ...s.windows,
          [id]: { id, position: pos, size, zIndex: nextZ },
        },
      };
    });
  },

  closeWindow: (id) => {
    set((s) => {
      const { [id]: _, ...rest } = s.windows;
      return { windows: rest };
    });
  },

  closeAllWindows: () => {
    set({ windows: {} });
  },

  focusWindow: (id) => {
    set((s) => {
      const w = s.windows[id];
      if (!w || w.zIndex === s._nextZ) return s;
      const nextZ = s._nextZ + 1;
      return {
        _nextZ: nextZ,
        windows: { ...s.windows, [id]: { ...w, zIndex: nextZ } },
      };
    });
  },

  updatePosition: (id, pos) => {
    set((s) => {
      const w = s.windows[id];
      if (!w) return s;
      return { windows: { ...s.windows, [id]: { ...w, position: pos } } };
    });
  },

  updateSize: (id, size) => {
    set((s) => {
      const w = s.windows[id];
      if (!w) return s;
      return { windows: { ...s.windows, [id]: { ...w, size } } };
    });
  },

  reclampAll: () => {
    set((s) => {
      const entries = Object.entries(s.windows);
      if (entries.length === 0) return s;
      let changed = false;
      const windows: Record<string, WindowState> = {};
      for (const [id, w] of entries) {
        const size = clampSize(w.size);
        const position = clampPosition(w.position, size);
        if (size.width !== w.size.width || size.height !== w.size.height || position.x !== w.position.x || position.y !== w.position.y) {
          changed = true;
        }
        windows[id] = { ...w, size, position };
      }
      return changed ? { windows } : s;
    });
  },

  isOpen: (id) => id in get().windows,
}));
