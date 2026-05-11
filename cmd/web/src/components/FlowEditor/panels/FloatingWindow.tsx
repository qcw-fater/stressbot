/**
 * 通用浮动窗口：可拖拽 + 可缩放 + zIndex 焦点管理。
 *
 * 使用 react-rnd 提供拖拽（标题栏）和 8 方向缩放。
 * 通过 floatingWindowStore 管理位置/大小/zIndex 状态。
 */

import { CloseOutlined } from '@ant-design/icons';
import { Rnd } from 'react-rnd';
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

  // 首次渲染时注册窗口
  if (open && !windowState) {
    openWindow(windowId, defaultSize, defaultPosition);
  }

  // 关闭时不卸载子组件，用 CSS 隐藏 + 禁用交互
  // 保证 Monaco 等有状态组件不会丢失滚动/数据
  if (!windowState) {
    // 窗口从未打开过，不渲染
    return open ? null : null;
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
      onDragStart={() => focusWindow(windowId)}
      onDragStop={(_e, d) => updatePosition(windowId, { x: d.x, y: d.y })}
      onResizeStop={(_e, _dir, _ref, _delta, pos) => {
        updateSize(windowId, {
          width: parseFloat(_ref.style.width),
          height: parseFloat(_ref.style.height),
        });
        updatePosition(windowId, pos);
      }}
      onMouseDown={() => focusWindow(windowId)}
    >
      <div className="floating-window">
        <div className={`floating-window-titlebar ${DRAG_HANDLE_CLASS}`}>
          <span className="floating-window-title">{title}</span>
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
