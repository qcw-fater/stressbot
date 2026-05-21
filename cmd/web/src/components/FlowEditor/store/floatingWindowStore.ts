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
      const pos = defaultPosition ?? centerPosition(idx, defaultSize);
      const nextZ = s._nextZ + 1;
      return {
        _nextZ: nextZ,
        windows: {
          ...s.windows,
          [id]: { id, position: pos, size: defaultSize, zIndex: nextZ },
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

  isOpen: (id) => id in get().windows,
}));
