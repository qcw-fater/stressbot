/**
 * 左侧三段式侧边栏：
 *   1. 节点类型（4×2 紧凑 grid，固定高度，不滚动）
 *   2. Action 模板库（搜索 + 列表，独立滚动）
 *   3. Callback 模板库（搜索 + 列表，独立滚动）
 *
 * 模板项使用与画布节点一致的视觉风格；右键菜单出「复用 / 编辑 / 删除」。
 * 模板项可拖入画布：拖入时同时插入 ActionDef/CallbackDef 到 flow 并建立对应节点。
 */

import { Button, Empty, Input, Upload, message } from 'antd';
import { DownloadOutlined, ImportOutlined } from '@ant-design/icons';
import { useEffect, useMemo, useRef, useState } from 'react';
import type { UploadProps } from 'antd';
import type { NodeType } from '@/types/flow';
import {
  exportAllTemplates,
  importTemplates,
  listActionTemplates,
  listCallbackTemplates,
  onTemplateChange,
  removeActionTemplate,
  removeCallbackTemplate,
  type ActionTemplate,
  type CallbackTemplate,
  type TemplateBundle,
} from '../library/templateStore';
import { useEditorStore } from '../store/editorStore';
import { classifyCallback } from '@/types/callback';
import './NodePalette.css';

interface NodeMeta {
  /** 'callback' 是特殊伪类型：拖入画布会创建一个 silent CallbackDef + CallbackCard，
   *  而非 FlowNode。其它都是真实 NodeType。 */
  type: NodeType | 'callback';
  label: string;
  color: string;
}

const PALETTE: NodeMeta[] = [
  { type: 'sequence', label: 'Sequence', color: 'var(--node-sequence)' },
  { type: 'action', label: 'Action', color: 'var(--node-action)' },
  { type: 'loop', label: 'Loop', color: 'var(--node-loop)' },
  { type: 'boolean', label: 'Boolean', color: 'var(--node-boolean)' },
  { type: 'weighted', label: 'Weighted', color: 'var(--node-weighted)' },
  { type: 'wait', label: 'Wait', color: 'var(--node-wait)' },
  { type: 'break', label: 'Break', color: 'var(--node-break)' },
  { type: 'continue', label: 'Continue', color: 'var(--node-continue)' },
  { type: 'callback', label: 'Callback', color: 'var(--node-callback)' },
];

interface ContextMenuState {
  x: number;
  y: number;
  kind: 'action' | 'callback';
  template: ActionTemplate | CallbackTemplate;
}

export function NodePalette() {
  const onDragStart = (e: React.DragEvent, type: NodeType | 'callback') => {
    if (type === 'callback') {
      // callback 不是 FlowNode；走独立的 dataTransfer key，由 FlowCanvas onDrop 识别后创建空 silent callback
      e.dataTransfer.setData('application/stressbot-new-callback', '1');
    } else {
      e.dataTransfer.setData('application/stressbot-node-type', type);
    }
    e.dataTransfer.effectAllowed = 'move';
  };

  // === 模板库 ===
  const [actions, setActions] = useState<ActionTemplate[]>([]);
  const [callbacks, setCallbacks] = useState<CallbackTemplate[]>([]);
  const [actionFilter, setActionFilter] = useState('');
  const [callbackFilter, setCallbackFilter] = useState('');
  const [menu, setMenu] = useState<ContextMenuState | null>(null);
  const wrapperRef = useRef<HTMLDivElement>(null);

  const setActivePanel = useEditorStore((s) => s.setActivePanel);
  const setClipboard = useEditorStore((s) => s.setClipboard);

  const refresh = async () => {
    const [a, c] = await Promise.all([listActionTemplates(), listCallbackTemplates()]);
    setActions(a);
    setCallbacks(c);
  };

  useEffect(() => {
    void refresh();
    // 订阅模板变更，自动刷新
    return onTemplateChange(() => {
      void refresh();
    });
  }, []);

  // 关闭右键菜单
  useEffect(() => {
    const onClick = () => setMenu(null);
    window.addEventListener('click', onClick);
    return () => window.removeEventListener('click', onClick);
  }, []);

  const filteredActions = useMemo(() => {
    const q = actionFilter.trim().toLowerCase();
    if (!q) return actions;
    return actions.filter(
      (t) => t.name.toLowerCase().includes(q) || t.pattern.toLowerCase().includes(q),
    );
  }, [actions, actionFilter]);

  const filteredCallbacks = useMemo(() => {
    const q = callbackFilter.trim().toLowerCase();
    if (!q) return callbacks;
    return callbacks.filter(
      (t) => t.name.toLowerCase().includes(q) || t.kind.toLowerCase().includes(q),
    );
  }, [callbacks, callbackFilter]);

  // 双击/「编辑模板」：打开模板编辑器（直接编辑模板本身，不复用到 flow）
  const onEditAction = (t: ActionTemplate) => {
    setActivePanel({ kind: 'templateEdit', templateKind: 'action', templateId: t.id });
  };

  const onEditCallback = (t: CallbackTemplate) => {
    setActivePanel({ kind: 'templateEdit', templateKind: 'callback', templateId: t.id });
  };

  // 复制到剪贴板：与画布节点复制一致，由用户在画布右键 → 粘贴选择落点
  const onCopyAction = (t: ActionTemplate) => {
    setClipboard({
      kind: 'node',
      nodeId: t.name,
      node: { type: 'action', action: t.name },
      action: { name: t.name, def: JSON.parse(JSON.stringify(t.data)) },
    });
    message.success('已复制（在画布空白处右键 → 粘贴）');
  };
  const onCopyCallback = (t: CallbackTemplate) => {
    setClipboard({
      kind: 'callback',
      callbackName: t.name,
      callback: JSON.parse(JSON.stringify(t.data)),
    });
    message.success('已复制（在画布空白处右键 → 粘贴）');
  };

  const onExportAll = async () => {
    const bundle = await exportAllTemplates();
    const blob = new Blob([JSON.stringify(bundle, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'stressbot-templates.json';
    a.click();
    URL.revokeObjectURL(url);
  };

  const onImportFile: UploadProps['beforeUpload'] = async (file) => {
    try {
      const bundle = JSON.parse(await file.text()) as TemplateBundle;
      const r = await importTemplates(bundle);
      message.success(`导入：${r.actions} action + ${r.callbacks} callback`);
    } catch (e) {
      message.error(`导入失败：${(e as Error).message}`);
    }
    return false;
  };

  const onTemplateContextMenu = (
    e: React.MouseEvent,
    kind: 'action' | 'callback',
    template: ActionTemplate | CallbackTemplate,
  ) => {
    e.preventDefault();
    const rect = wrapperRef.current?.getBoundingClientRect();
    setMenu({
      x: e.clientX - (rect?.left ?? 0),
      y: e.clientY - (rect?.top ?? 0),
      kind,
      template,
    });
  };

  // 模板拖到画布：通过 dataTransfer 携带 kind + 模板内容
  const onTemplateDragStart = (
    e: React.DragEvent,
    kind: 'action' | 'callback',
    template: ActionTemplate | CallbackTemplate,
  ) => {
    e.dataTransfer.setData(
      'application/stressbot-template',
      JSON.stringify({ kind, template }),
    );
    e.dataTransfer.effectAllowed = 'copy';
  };

  return (
    <div ref={wrapperRef} className="palette-root">
      {/* 顶部工具栏 */}
      <div className="palette-toolbar">
        <Upload accept="application/json" beforeUpload={onImportFile} showUploadList={false}>
          <Button icon={<ImportOutlined />} size="small">
            导入
          </Button>
        </Upload>
        <Button icon={<DownloadOutlined />} size="small" onClick={onExportAll}>
          导出
        </Button>
      </div>

      {/* 节点类型：固定高度，4×2 grid，不滚动 */}
      <div className="palette-nodes">
        <div className="palette-section-title">节点类型</div>
        <div className="palette-grid">
          {PALETTE.map((m) => (
            <div
              key={m.type}
              draggable
              onDragStart={(e) => onDragStart(e, m.type)}
              className="palette-grid-item"
              style={{ borderColor: m.color, color: m.color }}
              title={`拖入画布创建 ${m.label} 节点`}
            >
              {m.label}
            </div>
          ))}
        </div>
      </div>

      {/* 模板库：占据剩余高度，分为 Action / Callback 两段，各自独立滚动 */}
      <div className="palette-templates">
        {/* Action 模板段 */}
        <section className="palette-section">
          <div className="palette-section-header">
            <span className="palette-section-title">Action 模板（{actions.length}）</span>
          </div>
          <Input.Search
            size="small"
            placeholder="搜索"
            value={actionFilter}
            onChange={(e) => setActionFilter(e.target.value)}
            allowClear
            className="palette-section-toolbar"
          />
          <div className="palette-template-list">
            {filteredActions.length === 0 ? (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={<span style={{ fontSize: 11 }}>暂无</span>} />
            ) : (
              filteredActions.map((t) => (
                <div
                  key={t.id}
                  className="tpl-card tpl-card-action"
                  draggable
                  onDragStart={(e) => onTemplateDragStart(e, 'action', t)}
                  onContextMenu={(e) => onTemplateContextMenu(e, 'action', t)}
                  onDoubleClick={() => onEditAction(t)}
                  title={t.description ?? `${t.name} · ${t.pattern}（双击编辑 / 右键菜单 / 拖入画布）`}
                >
                  <div className="tpl-card-title">{t.name}</div>
                <div className="tpl-card-meta">
                  <span className="pattern-badge" data-pattern={t.pattern}>
                    {t.pattern}
                  </span>
                </div>
              </div>
            ))
          )}
        </div>
      </section>

      {/* Callback 模板段 */}
        <section className="palette-section">
          <div className="palette-section-header">
            <span className="palette-section-title">Callback 模板（{callbacks.length}）</span>
          </div>
          <Input.Search
            size="small"
            placeholder="搜索"
            value={callbackFilter}
            onChange={(e) => setCallbackFilter(e.target.value)}
            allowClear
            className="palette-section-toolbar"
          />
          <div className="palette-template-list">
            {filteredCallbacks.length === 0 ? (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={<span style={{ fontSize: 11 }}>暂无</span>} />
            ) : (
              filteredCallbacks.map((t) => {
                const kind = t.kind || classifyCallback(t.data);
                return (
                  <div
                    key={t.id}
                    className="tpl-card tpl-card-callback"
                    draggable
                    onDragStart={(e) => onTemplateDragStart(e, 'callback', t)}
                    onContextMenu={(e) => onTemplateContextMenu(e, 'callback', t)}
                    onDoubleClick={() => onEditCallback(t)}
                    title={t.description ?? `${t.name} · ${kind}（双击编辑 / 右键菜单 / 拖入画布）`}
                  >
                    <div className="tpl-card-title">{t.name}</div>
                    <div className="tpl-card-meta">
                      <span className="pattern-badge" data-pattern={kind}>
                        {kind}
                      </span>
                    </div>
                  </div>
                );
              })
            )}
          </div>
        </section>
      </div>

      {/* 右键菜单 */}
      {menu && (
        <div
          className="palette-context-menu"
          style={{ left: menu.x, top: menu.y }}
          onClick={(e) => e.stopPropagation()}
        >
          <div
            className="palette-context-item"
            onClick={() => {
              if (menu.kind === 'action') onEditAction(menu.template as ActionTemplate);
              else onEditCallback(menu.template as CallbackTemplate);
              setMenu(null);
            }}
          >
            编辑模板
          </div>
          <div
            className="palette-context-item"
            onClick={() => {
              if (menu.kind === 'action') onCopyAction(menu.template as ActionTemplate);
              else onCopyCallback(menu.template as CallbackTemplate);
              setMenu(null);
            }}
          >
            复制到剪贴板
          </div>
          <div
            className="palette-context-item palette-context-danger"
            onClick={async () => {
              if (menu.kind === 'action') {
                await removeActionTemplate(menu.template.id);
              } else {
                await removeCallbackTemplate(menu.template.id);
              }
              setMenu(null);
            }}
          >
            删除模板
          </div>
        </div>
      )}
    </div>
  );
}
