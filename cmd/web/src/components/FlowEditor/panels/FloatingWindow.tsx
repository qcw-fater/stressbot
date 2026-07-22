/**
 * 通用浮动窗口：可拖拽 + 可缩放 + zIndex 焦点管理。
 *
 * 使用 react-rnd 提供拖拽（标题栏）和 8 方向缩放。
 * 通过 floatingWindowStore 管理位置/大小/zIndex 状态。
 */

import { CloseOutlined } from '@ant-design/icons';
import { Rnd } from 'react-rnd';
import { useCallback, useEffect } from 'react';
import type { ReactNode } from 'react';
import { useFloatingWindowStore } from '../store/floatingWindowStore';
import './FloatingWindow.css';

export interface FloatingWindowProps {
  /** 窗口唯一 ID（对应 floatingWindowStore 中的 key） */
  windowId: string;
  /** 标题栏显示内容 */
  title: ReactNode;
  /** 首次打开时的默认尺寸 */
  defaultSize: { width: number; height: number };
  /** 首次打开时的默认位置（省略则自动级联） */
  defaultPosition?: { x: number; y: number };
  /** 最小尺寸 */
  minSize?: { width: number; height: number };
  /** 是否渲染 */
  open: boolean;
  /** 关闭后是否保留子组件，仅用于存在未提交草稿的编辑窗口。 */
  keepMounted?: boolean;
  /** 关闭回调 */
  onClose: () => void;
  /** 标题栏右侧额外内容（如操作按钮） */
  extra?: ReactNode;
  /** 底部内容（如 footer 按钮） */
  footer?: ReactNode;
  /** 子内容 */
  children: ReactNode;
}

const DRAG_HANDLE_CLASS = 'floating-window-drag-handle';

export function FloatingWindow({
  windowId,
  title,
  defaultSize,
  defaultPosition,
  minSize = { width: 320, height: 200 },
  open,
  keepMounted = false,
  onClose,
  extra,
  footer,
  children,
}: FloatingWindowProps) {
  const windowState = useFloatingWindowStore((s) => s.windows[windowId]);
  const openWindow = useFloatingWindowStore((s) => s.openWindow);
  const focusWindow = useFloatingWindowStore((s) => s.focusWindow);
  const updatePosition = useFloatingWindowStore((s) => s.updatePosition);
  const updateSize = useFloatingWindowStore((s) => s.updateSize);

  // 首次渲染时注册窗口（必须在 effect 中，不能在渲染期调用 set）
  useEffect(() => {
    if (open && !windowState) {
      openWindow(windowId, defaultSize, defaultPosition);
    }
  }, [open, windowState, windowId, openWindow, defaultSize, defaultPosition]);

  // 当 open 变为 true 时，确保窗口获得焦点（置于顶层）
  useEffect(() => {
    if (open && windowState) {
      focusWindow(windowId);
    }
  }, [open, windowId, focusWindow, windowState]);

  // ESC 关闭最顶层窗口
  const handleEsc = useCallback((e: KeyboardEvent) => {
    if (e.key !== 'Escape') return;
    if (e.target instanceof Element && e.target.closest('[data-floating-window-escape-local]')) return;
    // 只响应自己是最顶层窗口的情况
    const windows = useFloatingWindowStore.getState().windows;
    const topEntry = Object.entries(windows).reduce<[string, number] | null>((acc, [id, w]) => {
      if (!acc || w.zIndex > acc[1]) return [id, w.zIndex];
      return acc;
    }, null);
    if (topEntry && topEntry[0] === windowId && open) {
      e.preventDefault();
      e.stopPropagation();
      onClose();
    }
  }, [windowId, open, onClose]);

  useEffect(() => {
    if (!open) return;
    document.addEventListener('keydown', handleEsc, true);
    return () => document.removeEventListener('keydown', handleEsc, true);
  }, [handleEsc, open]);

  if ((!open && !keepMounted) || !windowState) {
    // 窗口从未打开过，不渲染
    return null;
  }

  const hidden = !open;

  return (
    <Rnd
      position={windowState.position}
      size={windowState.size}
      minWidth={minSize.width}
      minHeight={minSize.height}
      bounds="body"
      dragHandleClassName={DRAG_HANDLE_CLASS}
      style={{ zIndex: windowState.zIndex, display: hidden ? 'none' : undefined }}
      onDragStart={(e) => {
        focusWindow(windowId);
        // 如果有嵌套的浮动窗口，阻止事件冒泡，防止父窗口抢夺焦点
        if (e && e.stopPropagation) e.stopPropagation();
      }}
      onDragStop={(_e, d) => updatePosition(windowId, { x: d.x, y: d.y })}
      onResizeStop={(_e, _dir, _ref, _delta, pos) => {
        updateSize(windowId, {
          width: parseFloat(_ref.style.width),
          height: parseFloat(_ref.style.height),
        });
        updatePosition(windowId, pos);
      }}
      onMouseDown={(e) => {
        focusWindow(windowId);
        if (e && e.stopPropagation) e.stopPropagation();
      }}
    >
      <div className="floating-window">
        <div className={`floating-window-titlebar ${DRAG_HANDLE_CLASS}`}>
          <div className="floating-window-title">{title}</div>
          {extra && <div className="floating-window-extra">{extra}</div>}
          <button className="floating-window-close" onClick={onClose}>
            <CloseOutlined style={{ fontSize: 12 }} />
          </button>
        </div>
        <div className="floating-window-body">{children}</div>
        {footer && <div className="floating-window-footer">{footer}</div>}
      </div>
    </Rnd>
  );
}
