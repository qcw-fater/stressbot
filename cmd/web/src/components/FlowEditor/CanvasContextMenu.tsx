/**
 * 画布右键菜单组件簇：按目标（节点/边/空白）分发操作，以及「新建节点」快捷类型表。
 * 从 FlowCanvas 拆出的纯展示组件：仅依赖 props，不触碰 store 与画布实例。
 */

import type { Node as RFNode } from '@xyflow/react';

import type { Clipboard } from './store/editorStore';
import type { NodeType } from '@/types/flow';

export interface ContextMenu {
  x: number;
  y: number;
  flowX: number; // 画布坐标：粘贴时用
  flowY: number;
  kind: 'node' | 'edge' | 'pane';
  targetId?: string;
}

interface CanvasContextMenuProps {
  menu: ContextMenu;
  rfNodes: RFNode[];
  clipboard: Clipboard;
  readOnly: boolean;
  onClose: () => void;
  onCopyNode: (id: string) => void;
  onCutNode: (id: string) => void;
  onPaste: (flowX: number, flowY: number) => void;
  onSaveAsTemplate: (id: string) => void;
  onEditNode: (id: string) => void;
  onAddNode: (type: NodeType, flowX: number, flowY: number) => void;
  onAddListen: (flowX: number, flowY: number) => void;
  onDeleteNode: (id: string) => void;
  onDeleteEdge: (id: string) => void;
}

// 右键菜单「新建节点」+ F1..F9 快捷键对应的节点类型；listen 占 F{length+1}（当前 F10）。
// 键盘 handler 与右键菜单均按数组长度推导（节点 F1..F{length}，listen F{length+1}），新增类型加到末尾即可顺延。
export const QUICK_NODE_TYPES: Array<{ type: NodeType; label: string }> = [
  { type: 'sequence', label: 'Sequence' },
  { type: 'loop', label: 'Loop' },
  { type: 'boolean', label: 'Boolean' },
  { type: 'switch', label: 'Switch' },
  { type: 'weighted', label: 'Weighted' },
  { type: 'wait', label: 'Wait' },
  { type: 'break', label: 'Break' },
  { type: 'continue', label: 'Continue' },
  { type: 'action', label: 'Action' },
];

export function CanvasContextMenu({
  menu,
  rfNodes,
  clipboard,
  readOnly,
  onClose,
  onCopyNode,
  onCutNode,
  onPaste,
  onSaveAsTemplate,
  onEditNode,
  onAddNode,
  onAddListen,
  onDeleteNode,
  onDeleteEdge,
}: CanvasContextMenuProps) {
  const node =
    menu.kind === 'node' && menu.targetId ? rfNodes.find((n) => n.id === menu.targetId) : null;
  const isListenCard = node?.type === 'listenCard';
  const isAction = node?.type === 'action';
  const canSaveTemplate = isAction || isListenCard;

  return (
    <div
      style={{
        position: 'absolute',
        left: menu.x,
        top: menu.y,
        background: 'var(--bg-panel)',
        border: '1px solid var(--border-color)',
        borderRadius: 4,
        boxShadow: 'var(--shadow-popover)',
        padding: 4,
        zIndex: 1000,
        minWidth: 160,
        color: 'var(--text-primary)',
      }}
      onClick={(e) => e.stopPropagation()}
    >
      {menu.kind === 'node' && menu.targetId && (
        <>
          <MenuItem
            onClick={() => {
              onEditNode(menu.targetId!);
              onClose();
            }}
          >
            {readOnly ? '查看…' : '编辑…'}
          </MenuItem>
          {!readOnly && (
            <>
              <MenuItem
                onClick={() => {
                  onCopyNode(menu.targetId!);
                  onClose();
                }}
              >
                复制 <Hint>Ctrl+C</Hint>
              </MenuItem>
              <MenuItem
                onClick={() => {
                  onCutNode(menu.targetId!);
                  onClose();
                }}
              >
                剪切 <Hint>Ctrl+X</Hint>
              </MenuItem>
              {canSaveTemplate && (
                <MenuItem
                  onClick={() => {
                    onSaveAsTemplate(menu.targetId!);
                    onClose();
                  }}
                >
                  保存为模板…
                </MenuItem>
              )}
              <Divider />
              <MenuItem
                danger
                onClick={() => {
                  onDeleteNode(menu.targetId!);
                  onClose();
                }}
              >
                删除 <Hint>Del</Hint>
              </MenuItem>
            </>
          )}
        </>
      )}
      {menu.kind === 'edge' && menu.targetId && !readOnly && (
        <MenuItem
          danger
          onClick={() => {
            onDeleteEdge(menu.targetId!);
            onClose();
          }}
        >
          删除连线 <Hint>Del</Hint>
        </MenuItem>
      )}
      {menu.kind === 'pane' && !readOnly && (
        <>
          <MenuItem
            disabled={!clipboard}
            onClick={() => {
              if (!clipboard) return;
              onPaste(menu.flowX, menu.flowY);
              onClose();
            }}
          >
            粘贴 {clipboard ? `(${clipboard.kind})` : ''}
          </MenuItem>
          <Divider />
          <SubmenuLabel>新建节点</SubmenuLabel>
          {QUICK_NODE_TYPES.map((q, i) => (
            <MenuItem
              key={q.type}
              onClick={() => {
                onAddNode(q.type, menu.flowX, menu.flowY);
                onClose();
              }}
            >
              {q.label} <Hint>F{i + 1}</Hint>
            </MenuItem>
          ))}
          <MenuItem
            onClick={() => {
              onAddListen(menu.flowX, menu.flowY);
              onClose();
            }}
          >
            Listen <Hint>F{QUICK_NODE_TYPES.length + 1}</Hint>
          </MenuItem>
        </>
      )}
    </div>
  );
}

interface MenuItemProps {
  children: React.ReactNode;
  onClick: () => void;
  danger?: boolean;
  disabled?: boolean;
}

function MenuItem({ children, onClick, danger, disabled }: MenuItemProps) {
  return (
    <div
      onClick={disabled ? undefined : onClick}
      style={{
        padding: '6px 12px',
        cursor: disabled ? 'not-allowed' : 'pointer',
        borderRadius: 3,
        color: disabled ? 'var(--text-tertiary)' : danger ? 'var(--color-error)' : 'inherit',
        fontSize: 13,
        display: 'flex',
        justifyContent: 'space-between',
        alignItems: 'center',
        gap: 12,
      }}
      onMouseEnter={(e) => {
        if (!disabled) e.currentTarget.style.background = 'var(--hover-bg)';
      }}
      onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
    >
      {children}
    </div>
  );
}

interface HintProps {
  children: React.ReactNode;
}

function Hint({ children }: HintProps) {
  return (
    <span style={{ fontSize: 11, color: 'var(--text-tertiary)', fontFamily: 'monospace' }}>
      {children}
    </span>
  );
}

interface SubmenuLabelProps {
  children: React.ReactNode;
}

function SubmenuLabel({ children }: SubmenuLabelProps) {
  return (
    <div
      style={{
        padding: '4px 12px 2px',
        fontSize: 11,
        color: 'var(--text-tertiary)',
        fontWeight: 600,
      }}
    >
      {children}
    </div>
  );
}

function Divider() {
  return <div style={{ height: 1, background: 'var(--divider-bg)', margin: '4px 0' }} />;
}
