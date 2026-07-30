/**
 * 左侧三段式侧边栏：
 *   1. 节点类型（5×2 紧凑 grid，固定高度，不滚动）
 *   2. Action 模板库（搜索 + 列表，独立滚动）
 *   3. Listen 模板库（搜索 + 列表，独立滚动）
 *
 * 模板项使用与画布节点一致的视觉风格；右键菜单出「编辑 / 复制到剪贴板 / 删除」。
 * 模板项可拖入画布：拖入时同时插入 ActionDef/ListenDef 到 flow 并建立对应节点。
 */

import { App as AntApp, Button, Empty, Input, Tooltip } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import type { NodeType } from '@/types/flow';
import {
  listActionTemplates,
  listListenTemplates,
  onTemplateChange,
  removeActionTemplate,
  removeListenTemplate,
  type ActionTemplate,
  type ListenTemplate,
} from '../library/templateStore';
import { cloneListenDefaultRef, defaultRefSummary } from '../library/listenTemplateDefaults';
import { useEditorStore } from '../store/editorStore';
import { classifyListen } from '@/types/listen';
import './NodePalette.css';
import { useTemplateLibraryCapability } from '../library/useTemplateLibraryCapability';
import { showApiError } from '@/services/errorHandler';

interface NodeMeta {
  /** 'listen' 是特殊伪类型：拖入画布会创建一个 silent ListenDef + ListenCard，
   *  而非 FlowNode。其它都是真实 NodeType。 */
  type: NodeType | 'listen';
  label: string;
  color: string;
}

const PALETTE: NodeMeta[] = [
  { type: 'sequence', label: 'Sequence', color: 'var(--node-sequence)' },
  { type: 'loop', label: 'Loop', color: 'var(--node-loop)' },
  { type: 'boolean', label: 'Boolean', color: 'var(--node-boolean)' },
  { type: 'switch', label: 'Switch', color: 'var(--node-switch)' },
  { type: 'weighted', label: 'Weighted', color: 'var(--node-weighted)' },
  { type: 'wait', label: 'Wait', color: 'var(--node-wait)' },
  { type: 'break', label: 'Break', color: 'var(--node-break)' },
  { type: 'continue', label: 'Continue', color: 'var(--node-continue)' },
  { type: 'action', label: 'Action', color: 'var(--node-action)' },
  { type: 'listen', label: 'Listen', color: 'var(--node-listen)' },
];

interface ContextMenuState {
  x: number;
  y: number;
  kind: 'action' | 'listen';
  template: ActionTemplate | ListenTemplate;
}

export function NodePalette() {
  const { message } = AntApp.useApp();
  const {
    templateLibrary,
    loading: capabilityLoading,
    error: capabilityError,
  } = useTemplateLibraryCapability();
  const onDragStart = (e: React.DragEvent, type: NodeType | 'listen') => {
    if (type === 'listen') {
      // listen 不是 FlowNode；走独立的 dataTransfer key，由 FlowCanvas onDrop 识别后创建空 silent listen
      e.dataTransfer.setData('application/stressbot-new-listen', '1');
    } else {
      e.dataTransfer.setData('application/stressbot-node-type', type);
    }
    e.dataTransfer.effectAllowed = 'move';
  };

  // === 模板库 ===
  const [actions, setActions] = useState<ActionTemplate[]>([]);
  const [listens, setListens] = useState<ListenTemplate[]>([]);
  const [actionFilter, setActionFilter] = useState('');
  const [listenFilter, setListenFilter] = useState('');
  const [menu, setMenu] = useState<ContextMenuState | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const refreshRequestRef = useRef(0);
  const setActivePanel = useEditorStore((s) => s.setActivePanel);
  const setClipboard = useEditorStore((s) => s.setClipboard);

  const refresh = useCallback(async () => {
    if (templateLibrary !== true) return;
    const request = ++refreshRequestRef.current;
    setRefreshing(true);
    try {
      const [a, c] = await Promise.all([listActionTemplates(), listListenTemplates()]);
      if (request !== refreshRequestRef.current) return;
      setActions(a);
      setListens(c);
    } catch (error) {
      if (request !== refreshRequestRef.current) return;
      message.warning(`模板库刷新失败，继续显示上次数据：${(error as Error).message}`);
    } finally {
      if (request === refreshRequestRef.current) setRefreshing(false);
    }
  }, [message, templateLibrary]);

  useEffect(() => {
    if (templateLibrary !== true) return undefined;
    void refresh();
    // 订阅模板变更，自动刷新
    const unsubscribe = onTemplateChange(() => {
      void refresh();
    });
    const onFocus = () => { void refresh(); };
    window.addEventListener('focus', onFocus);
    return () => {
      refreshRequestRef.current += 1;
      unsubscribe();
      window.removeEventListener('focus', onFocus);
    };
  }, [refresh, templateLibrary]);

  useEffect(() => {
    if (capabilityError && templateLibrary === true) {
      message.warning(`暂时无法确认模板库状态，继续显示上次数据：${capabilityError.message}`);
    }
  }, [capabilityError, message, templateLibrary]);

  const onManualRefresh = async () => {
    await refresh();
  };

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

  const filteredListens = useMemo(() => {
    const q = listenFilter.trim().toLowerCase();
    if (!q) return listens;
    return listens.filter(
      (t) => t.name.toLowerCase().includes(q) || t.kind.toLowerCase().includes(q),
    );
  }, [listens, listenFilter]);

  // 双击/「编辑模板」：打开模板编辑器（直接编辑模板本身，不复用到 flow）
  const onEditAction = (t: ActionTemplate) => {
    setActivePanel({ kind: 'templateEdit', templateKind: 'action', templateId: t.id });
  };

  const onEditListen = (t: ListenTemplate) => {
    setActivePanel({ kind: 'templateEdit', templateKind: 'listen', templateId: t.id });
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
  const onCopyListen = (t: ListenTemplate) => {
    setClipboard({
      kind: 'listen',
      listenName: t.name,
      listen: JSON.parse(JSON.stringify(t.data)),
      defaultRef: cloneListenDefaultRef(t.defaultRef),
    });
    message.success('已复制（在画布空白处右键 → 粘贴）');
  };

  const onTemplateContextMenu = (
    e: React.MouseEvent,
    kind: 'action' | 'listen',
    template: ActionTemplate | ListenTemplate,
  ) => {
    e.preventDefault();
    const menuW = 150;
    const menuH = 104;
    const margin = 8;
    setMenu({
      x: Math.max(margin, Math.min(e.clientX, window.innerWidth - menuW - margin)),
      y: Math.max(margin, Math.min(e.clientY, window.innerHeight - menuH - margin)),
      kind,
      template,
    });
  };

  // 模板拖到画布：通过 dataTransfer 携带 kind + 模板内容
  const onTemplateDragStart = (
    e: React.DragEvent,
    kind: 'action' | 'listen',
    template: ActionTemplate | ListenTemplate,
  ) => {
    e.dataTransfer.setData(
      'application/stressbot-template',
      JSON.stringify({ kind, template }),
    );
    e.dataTransfer.effectAllowed = 'copy';
  };

  return (
    <div className="palette-root">
      {/* 节点类型：固定高度，5×2 grid，不滚动 */}
      <div className="palette-nodes">
        <div className="palette-section-title">节点类型</div>
        <div className="palette-grid">
          {PALETTE.map((m) => (
            <Tooltip key={m.type} title={`拖入画布创建 ${m.label} 节点`} mouseEnterDelay={0.4}>
              <div
                draggable
                onDragStart={(e) => onDragStart(e, m.type)}
                className={`palette-grid-item${m.type === 'listen' ? ' palette-grid-item-listen' : ''}`}
                style={{ borderColor: m.color, color: m.color }}
              >
                {m.label}
              </div>
            </Tooltip>
          ))}
        </div>
      </div>

      {/* 模板库：占据剩余高度，分为 Action / Listen 两段，各自独立滚动 */}
      <div className="palette-templates">
        {templateLibrary !== true ? (
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description={capabilityLoading
              ? '正在确认共享模板库状态'
              : '共享模板库功能未启用，请检查服务器配置'}
          />
        ) : (
          <>
        {/* Action 模板段 */}
        <section className="palette-section">
          <div className="palette-section-header">
            <span className="palette-section-title">Action 模板（{actions.length}）</span>
            <Button
              type="text"
              size="small"
              icon={<ReloadOutlined />}
              loading={refreshing}
              onClick={() => { void onManualRefresh(); }}
              aria-label="刷新模板库"
            />
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
                <Tooltip key={t.id} title={t.description ?? `${t.name} · ${t.pattern}（双击编辑 / 右键菜单 / 拖入画布）`} mouseEnterDelay={0.4}>
                  <div
                    className="tpl-card tpl-card-action"
                    draggable
                    onDragStart={(e) => onTemplateDragStart(e, 'action', t)}
                    onContextMenu={(e) => onTemplateContextMenu(e, 'action', t)}
                    onDoubleClick={() => onEditAction(t)}
                  >
                    <div className="tpl-card-title">{t.name}</div>
                  <div className="tpl-card-meta">
                    <span className="pattern-badge" data-pattern={t.pattern}>
                      {t.pattern}
                    </span>
                  </div>
                </div>
              </Tooltip>
            ))
          )}
        </div>
      </section>

      {/* Listen 模板段 */}
        <section className="palette-section">
          <div className="palette-section-header">
            <span className="palette-section-title">Listen 模板（{listens.length}）</span>
          </div>
          <Input.Search
            size="small"
            placeholder="搜索"
            value={listenFilter}
            onChange={(e) => setListenFilter(e.target.value)}
            allowClear
            className="palette-section-toolbar"
          />
          <div className="palette-template-list">
            {filteredListens.length === 0 ? (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={<span style={{ fontSize: 11 }}>暂无</span>} />
            ) : (
              filteredListens.map((t) => {
                const kind = t.kind || classifyListen(t.data);
                const refSummary = defaultRefSummary(t.defaultRef);
                const title = t.description ?? `${t.name} · ${kind}${refSummary ? ` · ${refSummary}` : ''}（双击编辑 / 右键菜单 / 拖入画布）`;
                return (
                  <Tooltip key={t.id} title={title} mouseEnterDelay={0.4}>
                    <div
                      className="tpl-card tpl-card-listen"
                      draggable
                      onDragStart={(e) => onTemplateDragStart(e, 'listen', t)}
                      onContextMenu={(e) => onTemplateContextMenu(e, 'listen', t)}
                      onDoubleClick={() => onEditListen(t)}
                    >
                      <div className="tpl-card-title">{t.name}</div>
                      <div className="tpl-card-meta">
                        <span className="pattern-badge" data-pattern={kind}>
                          {kind}
                        </span>
                        {/* 服务器名直接展示；外层卡片 Tooltip 已含 refSummary，不再叠加原生 title 避免双提示 */}
                        {refSummary && <span>{t.defaultRef?.server}</span>}
                      </div>
                    </div>
                  </Tooltip>
                );
              })
            )}
          </div>
        </section>
          </>
        )}
      </div>

      {/* 右键菜单 */}
      {menu && createPortal(
        <div
          className="palette-context-menu"
          style={{ left: menu.x, top: menu.y }}
          onClick={(e) => e.stopPropagation()}
        >
          <div
            className="palette-context-item"
            onClick={() => {
              if (menu.kind === 'action') onEditAction(menu.template as ActionTemplate);
              else onEditListen(menu.template as ListenTemplate);
              setMenu(null);
            }}
          >
            编辑模板
          </div>
          <div
            className="palette-context-item"
            onClick={() => {
              if (menu.kind === 'action') onCopyAction(menu.template as ActionTemplate);
              else onCopyListen(menu.template as ListenTemplate);
              setMenu(null);
            }}
          >
            复制到剪贴板
          </div>
          <div
            className="palette-context-item palette-context-danger"
            onClick={async () => {
              try {
                if (menu.kind === 'action') {
                  await removeActionTemplate(menu.template.id);
                } else {
                  await removeListenTemplate(menu.template.id);
                }
                setMenu(null);
              } catch (error) {
                showApiError(error);
              }
            }}
          >
            删除模板
          </div>
        </div>,
        document.body,
      )}
    </div>
  );
}
