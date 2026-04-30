/**
 * 主画布：基于 @xyflow/react 的 ReactFlow 实例。
 *
 * 接管：
 *   - 节点 / 边渲染、拖动、选中
 *   - 节点双击 → 打开对应 Editor 面板
 *   - Delete / Backspace 键删除选中节点 / 边
 *   - 右键菜单删除（节点 + 边）
 *   - 选中边时联动高亮两端节点（edgeHighlightNodeIds）
 *   - MiniMap / Controls / Background
 */

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Background,
  Controls,
  MiniMap,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  type Edge,
  type Node as RFNode,
  type OnSelectionChangeParams,
} from '@xyflow/react';

import { message } from 'antd';
import { nodeTypes } from './nodes/registry';
import { edgeTypes } from './edges/registry';
import { useFlowStore } from './store/flowStore';
import { useEditorStore, type Clipboard } from './store/editorStore';
import { generateNodeId } from './utils/nodeIdGen';
import { saveActionTemplate, saveCallbackTemplate } from './library/templateStore';
import { useFlowReadOnly } from './flowReadOnlyContext';
import type { FlowNode, NodeType } from '@/types/flow';
import type { ActionDef } from '@/types/action';
import type { CallbackDef } from '@/types/callback';
import { classifyCallback } from '@/types/callback';

interface ContextMenu {
  x: number;
  y: number;
  flowX: number; // 画布坐标：粘贴时用
  flowY: number;
  kind: 'node' | 'edge' | 'pane';
  targetId?: string;
}

function FlowCanvasInner() {
  const readOnly = useFlowReadOnly();
  const rfNodes = useFlowStore((s) => s.rfNodes);
  const rfEdges = useFlowStore((s) => s.rfEdges);
  const onNodesChange = useFlowStore((s) => s.onNodesChange);
  const onEdgesChange = useFlowStore((s) => s.onEdgesChange);
  const addNode = useFlowStore((s) => s.addNode);
  const addAction = useFlowStore((s) => s.addAction);
  const addCallback = useFlowStore((s) => s.addCallback);
  const removeNode = useFlowStore((s) => s.removeNode);
  const removeCallback = useFlowStore((s) => s.removeCallback);
  const updateNode = useFlowStore((s) => s.updateNode);

  const setSelectedNode = useEditorStore((s) => s.setSelectedNode);
  const setActivePanel = useEditorStore((s) => s.setActivePanel);
  const setEdgeHighlightNodes = useEditorStore((s) => s.setEdgeHighlightNodes);
  const setEdgeHighlightColor = useEditorStore((s) => s.setEdgeHighlightColor);
  const showMinimap = useEditorStore((s) => s.showMinimap);
  const showGrid = useEditorStore((s) => s.showGrid);
  const clipboard = useEditorStore((s) => s.clipboard);
  const setClipboard = useEditorStore((s) => s.setClipboard);

  const wrapperRef = useRef<HTMLDivElement>(null);
  const { screenToFlowPosition } = useReactFlow();
  const [menu, setMenu] = useState<ContextMenu | null>(null);

  // dropEffect 必须与拖出端的 effectAllowed 兼容：
  //   - 普通节点类型：effectAllowed='move'  → dropEffect='move'
  //   - 模板（action/callback）：effectAllowed='copy'  → dropEffect='copy'
  // 两者不匹配会导致浏览器静默吞掉 drop 事件，看上去就是"拖入无效"。
  const onDragOver = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    const types = e.dataTransfer.types;
    if (types.includes('application/stressbot-template')) {
      e.dataTransfer.dropEffect = 'copy';
    } else {
      e.dataTransfer.dropEffect = 'move';
    }
  }, []);

  const onDrop = useCallback(
    (e: React.DragEvent) => {
      e.preventDefault();
      if (readOnly) return;
      const flowPos = screenToFlowPosition({ x: e.clientX, y: e.clientY });

      // 1. 普通节点类型拖入
      const type = e.dataTransfer.getData('application/stressbot-node-type') as NodeType | '';
      if (type) {
        const taken = new Set(Object.keys(useFlowStore.getState().nodes));
        const id = generateNodeId(type, taken);
        const node: FlowNode = { type };
        addNode(id, node);
        const layout = useFlowStore.getState().layout;
        layout.nodePositions[id] = { x: flowPos.x, y: flowPos.y };
        useFlowStore.setState((s) => ({
          rfNodes: s.rfNodes.map((n) => (n.id === id ? { ...n, position: flowPos } : n)),
        }));
        setSelectedNode(id);
        return;
      }

      // 1b. callback 拖入：创建空 silent callback + CallbackCard，并打开编辑面板
      if (e.dataTransfer.getData('application/stressbot-new-callback')) {
        const state = useFlowStore.getState();
        let cbName = 'callback';
        let i = 1;
        while (state.callbacks[cbName]) cbName = `callback_${i++}`;
        addCallback(cbName, {});
        const cardId = `__cb__${cbName}`;
        const layout = useFlowStore.getState().layout;
        layout.nodePositions[cardId] = { x: flowPos.x, y: flowPos.y };
        useFlowStore.setState((s) => ({
          rfNodes: s.rfNodes.map((n) => (n.id === cardId ? { ...n, position: flowPos } : n)),
        }));
        setActivePanel({ kind: 'callbackEdit', callbackName: cbName });
        return;
      }

      // 2. 模板拖入（action / callback）
      const tplRaw = e.dataTransfer.getData('application/stressbot-template');
      if (!tplRaw) return;
      try {
        const { kind, template } = JSON.parse(tplRaw) as {
          kind: 'action' | 'callback';
          template: { name: string; data: unknown };
        };
        if (kind === 'action') {
          const state = useFlowStore.getState();
          // 唯一 action 名
          let actionName = template.name;
          let i = 1;
          while (state.actions[actionName]) actionName = `${template.name}_${i++}`;
          addAction(actionName, template.data as ActionDef);
          // 唯一 node id
          const taken = new Set(Object.keys(useFlowStore.getState().nodes));
          const nodeId = generateNodeId('action', taken);
          addNode(nodeId, { type: 'action', action: actionName });
          const layout = useFlowStore.getState().layout;
          layout.nodePositions[nodeId] = { x: flowPos.x, y: flowPos.y };
          useFlowStore.setState((s) => ({
            rfNodes: s.rfNodes.map((n) => (n.id === nodeId ? { ...n, position: flowPos } : n)),
          }));
          setSelectedNode(nodeId);
        } else if (kind === 'callback') {
          const state = useFlowStore.getState();
          let cbName = template.name;
          let i = 1;
          while (state.callbacks[cbName]) cbName = `${template.name}_${i++}`;
          addCallback(cbName, template.data as CallbackDef);
          // CallbackCard 自动由 jsonToFlow 创建；位置覆盖到鼠标落点
          const cardId = `__cb__${cbName}`;
          const layout = useFlowStore.getState().layout;
          layout.nodePositions[cardId] = { x: flowPos.x, y: flowPos.y };
          useFlowStore.setState((s) => ({
            rfNodes: s.rfNodes.map((n) => (n.id === cardId ? { ...n, position: flowPos } : n)),
          }));
        }
      } catch (err) {
        console.warn('[FlowCanvas] 解析模板拖入数据失败：', err);
      }
    },
    [addNode, addAction, addCallback, readOnly, screenToFlowPosition, setActivePanel, setSelectedNode],
  );

  // 选中变化 → 计算 edgeHighlightNodeIds + 高亮颜色（取第一条选中边的源节点类型）
  const onSelectionChange = useCallback(
    (params: OnSelectionChangeParams) => {
      const ids = new Set<string>();
      for (const e of params.edges) {
        ids.add(e.source);
        ids.add(e.target);
      }
      setEdgeHighlightNodes(Array.from(ids));
      const firstEdge = params.edges[0];
      if (firstEdge) {
        const srcType = (firstEdge.data as { sourceNodeType?: string } | undefined)?.sourceNodeType;
        const NODE_COLOR: Record<string, string> = {
          sequence: 'var(--node-sequence)',
          action: 'var(--node-action)',
          loop: 'var(--node-loop)',
          boolean: 'var(--node-boolean)',
          weighted: 'var(--node-weighted)',
          wait: 'var(--node-wait)',
          break: 'var(--node-break)',
          continue: 'var(--node-continue)',
        };
        setEdgeHighlightColor((srcType && NODE_COLOR[srcType]) ?? 'var(--edge-seq)');
      } else {
        setEdgeHighlightColor(null);
      }
    },
    [setEdgeHighlightNodes, setEdgeHighlightColor],
  );

  /** 删除一个 React Flow node：根据类型决定是删 flow node 还是 callback */
  const deleteRfNode = useCallback(
    (n: RFNode) => {
      if (n.type === 'callbackCard') {
        const cbName = (n.data as { callbackName?: string }).callbackName;
        if (cbName) removeCallback(cbName);
      } else {
        removeNode(n.id);
      }
    },
    [removeNode, removeCallback],
  );

  /** 删除一条边：把 source 节点中对 target 的引用清除（保持业务图与视觉同步） */
  const deleteRfEdge = useCallback(
    (e: Edge) => {
      const flowNodes = useFlowStore.getState().nodes;
      const src = flowNodes[e.source];
      if (!src) return;
      const handleId = e.sourceHandle ?? '';
      if (src.type === 'sequence' && handleId.startsWith('seq-')) {
        const idx = Number(handleId.slice(4));
        if (Number.isFinite(idx) && src.next) {
          const next = src.next.slice();
          next.splice(idx, 1);
          updateNode(e.source, { next });
        }
      } else if (src.type === 'loop' && handleId === 'body') {
        updateNode(e.source, { body: '' });
      } else if (src.type === 'boolean' && handleId === 'true') {
        updateNode(e.source, { trueNext: '' });
      } else if (src.type === 'boolean' && handleId === 'false') {
        updateNode(e.source, { falseNext: '' });
      } else if (src.type === 'weighted' && handleId.startsWith('opt-')) {
        const idx = Number(handleId.slice(4));
        if (Number.isFinite(idx) && src.options) {
          const options = src.options.slice();
          options.splice(idx, 1);
          updateNode(e.source, { options });
        }
      } else if (src.type === 'wait' && handleId === 'out') {
        // wait 没有显式 next 字段（顺序由 sequence 控制），无需处理
      } else if (handleId === 'listen') {
        // action → callback：从 listenCallbacks 中移除指向 e.target 的引用
        if (src.type === 'action' && src.listenCallbacks) {
          const cbName = e.target.replace(/^__cb__/, '');
          updateNode(e.source, {
            listenCallbacks: src.listenCallbacks.filter((r) => r.callback !== cbName),
          });
        }
      }
    },
    [updateNode],
  );

  /**
   * 用户从 source handle 拖线到 target handle 时建立业务关系。
   *
   * 规则：
   *   sequence  · seq-N      → next[N] = target            （已存在则替换）
   *   sequence  · seq-add    → next.push(target)           （便于增量续接）
   *   loop      · body       → body = target
   *   boolean   · true/false → trueNext / falseNext = target
   *   weighted  · opt-N      → options[N].node = target
   *   weighted  · opt-add    → options.push({ node: target, weight: 1 })
   *   action    · listen     → listenCallbacks.push({ callback })，target 必须是 callbackCard
   *
   * 注：业务图通过 syncDerived 重算 rfEdges，所以这里不要直接 setEdges。
   */
  const onConnect = useCallback(
    (params: { source: string | null; target: string | null; sourceHandle?: string | null; targetHandle?: string | null }) => {
      if (readOnly) return;
      if (!params.source || !params.target) return;
      const src = useFlowStore.getState().nodes[params.source];
      if (!src) return;
      const handle = params.sourceHandle ?? '';
      const targetIsCallbackCard = params.target.startsWith('__cb__');
      const targetCbName = targetIsCallbackCard ? params.target.slice('__cb__'.length) : null;
      const targetNodeId = !targetIsCallbackCard ? params.target : null;

      if (src.type === 'sequence') {
        const next = (src.next ?? []).slice();
        if (handle === 'seq-add') {
          if (targetNodeId) next.push(targetNodeId);
        } else if (handle.startsWith('seq-')) {
          const idx = Number(handle.slice(4));
          if (Number.isFinite(idx) && targetNodeId) next[idx] = targetNodeId;
        }
        updateNode(params.source, { next });
        return;
      }
      if (src.type === 'loop' && handle === 'body' && targetNodeId) {
        updateNode(params.source, { body: targetNodeId });
        return;
      }
      if (src.type === 'boolean') {
        if (handle === 'true' && targetNodeId) updateNode(params.source, { trueNext: targetNodeId });
        else if (handle === 'false' && targetNodeId) updateNode(params.source, { falseNext: targetNodeId });
        return;
      }
      if (src.type === 'weighted') {
        const options = (src.options ?? []).slice();
        if (handle === 'opt-add') {
          if (targetNodeId) options.push({ node: targetNodeId, weight: 1 });
        } else if (handle.startsWith('opt-')) {
          const idx = Number(handle.slice(4));
          if (Number.isFinite(idx) && targetNodeId) options[idx] = { ...options[idx], node: targetNodeId };
        }
        updateNode(params.source, { options });
        return;
      }
      if (src.type === 'action' && handle === 'listen' && targetCbName) {
        const list = (src.listenCallbacks ?? []).slice();
        if (!list.some((r) => r.callback === targetCbName)) {
          list.push({ route: null, server: '', callback: targetCbName });
        }
        updateNode(params.source, { listenCallbacks: list });
        return;
      }
    },
    [readOnly, updateNode],
  );

  const onNodesDelete = useCallback(
    (deleted: RFNode[]) => {
      if (readOnly) return;
      for (const n of deleted) deleteRfNode(n);
    },
    [deleteRfNode, readOnly],
  );

  const onEdgesDelete = useCallback(
    (deleted: Edge[]) => {
      if (readOnly) return;
      for (const e of deleted) deleteRfEdge(e);
    },
    [deleteRfEdge, readOnly],
  );

  // 关闭右键菜单的全局监听
  useEffect(() => {
    const onClick = () => setMenu(null);
    window.addEventListener('click', onClick);
    return () => window.removeEventListener('click', onClick);
  }, []);

  /** 把一个 React Flow node（含相关 action / callback）打包到剪贴板 */
  const copyRfNode = useCallback(
    (n: RFNode): Clipboard => {
      const flow = useFlowStore.getState();
      if (n.type === 'callbackCard') {
        const cbName = (n.data as { callbackName?: string }).callbackName;
        if (!cbName || !flow.callbacks[cbName]) return null;
        return {
          kind: 'callback',
          callbackName: cbName,
          callback: JSON.parse(JSON.stringify(flow.callbacks[cbName])),
        };
      }
      const node = flow.nodes[n.id];
      if (!node) return null;
      const action =
        node.type === 'action' && node.action && flow.actions[node.action]
          ? { name: node.action, def: JSON.parse(JSON.stringify(flow.actions[node.action])) as ActionDef }
          : undefined;
      // listenCallbacks 涉及的 callback 一并复制（跨流程粘贴时不丢引用）
      const callbacks: Array<{ name: string; def: CallbackDef }> = [];
      if (node.type === 'action' && node.listenCallbacks) {
        for (const r of node.listenCallbacks) {
          if (!r.callback) continue;
          const cb = flow.callbacks[r.callback];
          if (cb) callbacks.push({ name: r.callback, def: JSON.parse(JSON.stringify(cb)) });
        }
      }
      return {
        kind: 'node',
        nodeId: n.id,
        node: JSON.parse(JSON.stringify(node)),
        action,
        callbacks: callbacks.length > 0 ? callbacks : undefined,
      };
    },
    [],
  );

  const uniqueNodeId = (base: string) => {
    const taken = new Set(Object.keys(useFlowStore.getState().nodes));
    return generateNodeId(base as NodeType, taken);
  };
  const uniqueActionName = (base: string) => {
    const map = useFlowStore.getState().actions;
    let name = base;
    let i = 1;
    while (map[name]) name = `${base}_${i++}`;
    return name;
  };
  const uniqueCallbackName = (base: string) => {
    const map = useFlowStore.getState().callbacks;
    let name = base;
    let i = 1;
    while (map[name]) name = `${base}_${i++}`;
    return name;
  };

  /** 把剪贴板内容粘贴到给定 flow 坐标 */
  const pasteAt = useCallback(
    (flowX: number, flowY: number) => {
      if (!clipboard) {
        message.info('剪贴板为空');
        return;
      }
      if (clipboard.kind === 'node') {
        const node: FlowNode = JSON.parse(JSON.stringify(clipboard.node));
        // 重新分配 action / callback 名（避免覆盖已有）
        if (node.type === 'action' && clipboard.action) {
          const newAct = uniqueActionName(clipboard.action.name);
          addAction(newAct, clipboard.action.def);
          node.action = newAct;
        }
        if (node.type === 'action' && node.listenCallbacks && clipboard.callbacks) {
          const renameMap: Record<string, string> = {};
          for (const c of clipboard.callbacks) {
            const newCb = uniqueCallbackName(c.name);
            addCallback(newCb, c.def);
            renameMap[c.name] = newCb;
          }
          node.listenCallbacks = node.listenCallbacks.map((r) => ({
            ...r,
            callback: r.callback ? (renameMap[r.callback] ?? r.callback) : r.callback,
          }));
        }
        const newId = uniqueNodeId(node.type ?? 'sequence');
        addNode(newId, node);
        const layout = useFlowStore.getState().layout;
        layout.nodePositions[newId] = { x: flowX, y: flowY };
        useFlowStore.setState((s) => ({
          rfNodes: s.rfNodes.map((n) => (n.id === newId ? { ...n, position: { x: flowX, y: flowY } } : n)),
        }));
        setSelectedNode(newId);
        message.success(`已粘贴节点 ${newId}`);
      } else if (clipboard.kind === 'callback') {
        const newName = uniqueCallbackName(clipboard.callbackName);
        addCallback(newName, JSON.parse(JSON.stringify(clipboard.callback)));
        const cardId = `__cb__${newName}`;
        const layout = useFlowStore.getState().layout;
        layout.nodePositions[cardId] = { x: flowX, y: flowY };
        useFlowStore.setState((s) => ({
          rfNodes: s.rfNodes.map((n) => (n.id === cardId ? { ...n, position: { x: flowX, y: flowY } } : n)),
        }));
        message.success(`已粘贴 callback ${newName}`);
      }
    },
    [addAction, addCallback, addNode, clipboard, setSelectedNode],
  );

  /** 把节点保存为模板（写入 IndexedDB） */
  const saveNodeAsTemplate = useCallback(async (n: RFNode) => {
    const flow = useFlowStore.getState();
    if (n.type === 'callbackCard') {
      const cbName = (n.data as { callbackName?: string }).callbackName;
      if (!cbName) return;
      const cb = flow.callbacks[cbName];
      if (!cb) return;
      await saveCallbackTemplate({
        name: cbName,
        kind: classifyCallback(cb),
        data: cb,
      });
      message.success(`callback ${cbName} 已保存为模板`);
      return;
    }
    const node = flow.nodes[n.id];
    if (!node || node.type !== 'action' || !node.action) {
      message.warning('仅 action 节点 / callback 卡片可保存为模板');
      return;
    }
    const def = flow.actions[node.action];
    if (!def) return;
    await saveActionTemplate({
      name: node.action,
      // 把节点的描述继承给模板，作为复用时的提示文本
      description: node.description?.trim() || undefined,
      pattern: def.pattern ?? 'declarative',
      data: def,
    });
    message.success(`action ${node.action} 已保存为模板`);
  }, []);

  // 记录最近一次鼠标在画布内的客户端坐标，Ctrl+V 粘贴时作为落点
  const lastMousePosRef = useRef<{ x: number; y: number } | null>(null);

  // 全局快捷键：Ctrl/Cmd+C 复制 / Ctrl/Cmd+X 剪切 / Ctrl/Cmd+V 粘贴
  // 关键约束：
  //   - 在 input/textarea/contentEditable 中触发时让浏览器原生处理（不抢复制粘贴）
  //   - 用户当前选中了文本时让出（getSelection().toString()）
  //   - 仅当前焦点在画布内或 body 时才处理（避免抢其他面板的快捷键）
  //   - 没有可复制的对象时不触发任何提示
  useEffect(() => {
    const inCanvas = () => {
      const ae = document.activeElement;
      if (!ae || ae === document.body) return true;
      return wrapperRef.current?.contains(ae as Node) ?? false;
    };
    const handler = (e: KeyboardEvent) => {
      const mod = e.ctrlKey || e.metaKey;
      if (!mod) return;
      const target = e.target as HTMLElement | null;
      if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)) return;
      if ((window.getSelection()?.toString().length ?? 0) > 0) return;
      if (!inCanvas()) return;

      const key = e.key.toLowerCase();
      if (key === 'c') {
        const selected = rfNodes.find((n) => n.selected);
        if (!selected) return; // 没选中节点 → 让浏览器处理
        const cb = copyRfNode(selected);
        if (!cb) return;
        e.preventDefault();
        setClipboard(cb);
        message.success('已复制（在画布空白处右键 → 粘贴，或 Ctrl+V）');
      } else if (key === 'x') {
        const selected = rfNodes.find((n) => n.selected);
        if (!selected) return;
        const cb = copyRfNode(selected);
        if (!cb) return;
        e.preventDefault();
        setClipboard(cb);
        deleteRfNode(selected);
        message.success('已剪切');
      } else if (key === 'v') {
        if (!useEditorStore.getState().clipboard) return; // 没剪贴板 → 让浏览器处理
        const last = lastMousePosRef.current;
        if (!last) {
          message.info('请把鼠标移到画布上方再 Ctrl+V，或在画布空白处右键 → 粘贴');
          return;
        }
        e.preventDefault();
        const flowPos = screenToFlowPosition({ x: last.x, y: last.y });
        pasteAt(flowPos.x, flowPos.y);
      }
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [copyRfNode, deleteRfNode, pasteAt, rfNodes, screenToFlowPosition, setClipboard]);

  // MiniMap 节点色：直接读 CSS token，确保与节点本体配色 / 主题切换保持完全一致。
  // 注意：每次回调实时读取 getComputedStyle，不缓存（dark/light 切换不需要重 mount）。
  const minimapNodeColor = useCallback((n: RFNode) => {
    const tokenMap: Record<string, string> = {
      sequence: '--node-sequence',
      action: '--node-action',
      loop: '--node-loop',
      boolean: '--node-boolean',
      weighted: '--node-weighted',
      wait: '--node-wait',
      break: '--node-break',
      continue: '--node-continue',
      callbackCard: '--node-callback',
    };
    const cssVar = tokenMap[n.type ?? 'sequence'];
    if (!cssVar) return '#999';
    return getComputedStyle(document.documentElement).getPropertyValue(cssVar).trim() || '#999';
  }, []);

  return (
    <div
      ref={wrapperRef}
      style={{ width: '100%', height: '100%', position: 'relative' }}
      onDragOver={onDragOver}
      onDrop={onDrop}
      onMouseMove={(e) => {
        lastMousePosRef.current = { x: e.clientX, y: e.clientY };
      }}
      onMouseLeave={() => {
        lastMousePosRef.current = null;
      }}
    >
      <ReactFlow
        nodes={rfNodes}
        edges={rfEdges}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        nodesDraggable={!readOnly}
        nodesConnectable={!readOnly}
        edgesFocusable={!readOnly}
        onNodesChange={readOnly ? undefined : onNodesChange}
        onEdgesChange={readOnly ? undefined : onEdgesChange}
        onConnect={onConnect}
        onNodesDelete={onNodesDelete}
        onEdgesDelete={onEdgesDelete}
        onSelectionChange={onSelectionChange}
        onNodeClick={(_, n) => {
          setSelectedNode(n.id);
        }}
        onNodeDoubleClick={(_, n) => {
          if (n.type === 'callbackCard') {
            const cbName = (n.data as { callbackName?: string }).callbackName;
            if (cbName) {
              setActivePanel({ kind: 'callbackEdit', callbackName: cbName });
            }
            return;
          }
          setActivePanel({ kind: 'nodeEdit', nodeId: n.id });
        }}
        onPaneClick={() => {
          setSelectedNode(null);
          setActivePanel({ kind: 'none' });
          setMenu(null);
        }}
        onNodeContextMenu={(e, n) => {
          e.preventDefault();
          const rect = wrapperRef.current?.getBoundingClientRect();
          const flowPos = screenToFlowPosition({ x: e.clientX, y: e.clientY });
          setMenu({
            x: e.clientX - (rect?.left ?? 0),
            y: e.clientY - (rect?.top ?? 0),
            flowX: flowPos.x,
            flowY: flowPos.y,
            kind: 'node',
            targetId: n.id,
          });
        }}
        onEdgeContextMenu={(e, edge) => {
          e.preventDefault();
          const rect = wrapperRef.current?.getBoundingClientRect();
          const flowPos = screenToFlowPosition({ x: e.clientX, y: e.clientY });
          setMenu({
            x: e.clientX - (rect?.left ?? 0),
            y: e.clientY - (rect?.top ?? 0),
            flowX: flowPos.x,
            flowY: flowPos.y,
            kind: 'edge',
            targetId: edge.id,
          });
        }}
        onPaneContextMenu={(e) => {
          e.preventDefault();
          const me = e as unknown as MouseEvent;
          const rect = wrapperRef.current?.getBoundingClientRect();
          const flowPos = screenToFlowPosition({ x: me.clientX, y: me.clientY });
          setMenu({
            x: me.clientX - (rect?.left ?? 0),
            y: me.clientY - (rect?.top ?? 0),
            flowX: flowPos.x,
            flowY: flowPos.y,
            kind: 'pane',
          });
        }}
        deleteKeyCode={readOnly ? null : ['Delete', 'Backspace']}
        fitView
        fitViewOptions={{ padding: 0.15, minZoom: 0.2, maxZoom: 1.5 }}
        proOptions={{ hideAttribution: true }}
        minZoom={0.1}
        maxZoom={2}
      >
        {showGrid && <Background gap={16} size={1} />}
        <Controls position="bottom-left" />
        {showMinimap && <MiniMap pannable zoomable nodeColor={minimapNodeColor} />}
      </ReactFlow>
      {menu && (
        <CanvasContextMenu
          menu={menu}
          rfNodes={rfNodes}
          clipboard={clipboard}
          readOnly={readOnly}
          onClose={() => setMenu(null)}
          onCopyNode={(id) => {
            const n = rfNodes.find((x) => x.id === id);
            if (!n) return;
            const cb = copyRfNode(n);
            if (cb) {
              setClipboard(cb);
              message.success('已复制');
            }
          }}
          onCutNode={(id) => {
            const n = rfNodes.find((x) => x.id === id);
            if (!n) return;
            const cb = copyRfNode(n);
            if (cb) {
              setClipboard(cb);
              deleteRfNode(n);
              message.success('已剪切');
            }
          }}
          onPaste={(fx, fy) => pasteAt(fx, fy)}
          onSaveAsTemplate={(id) => {
            const n = rfNodes.find((x) => x.id === id);
            if (n) void saveNodeAsTemplate(n);
          }}
          onEditNode={(id) => {
            const n = rfNodes.find((x) => x.id === id);
            if (!n) return;
            if (n.type === 'callbackCard') {
              const cbName = (n.data as { callbackName?: string }).callbackName;
              if (cbName) setActivePanel({ kind: 'callbackEdit', callbackName: cbName });
            } else {
              setActivePanel({ kind: 'nodeEdit', nodeId: id });
            }
          }}
          onAddNode={(type, fx, fy) => {
            const taken = new Set(Object.keys(useFlowStore.getState().nodes));
            const id = generateNodeId(type, taken);
            addNode(id, { type });
            const layout = useFlowStore.getState().layout;
            layout.nodePositions[id] = { x: fx, y: fy };
            useFlowStore.setState((s) => ({
              rfNodes: s.rfNodes.map((n) => (n.id === id ? { ...n, position: { x: fx, y: fy } } : n)),
            }));
            setSelectedNode(id);
          }}
          onAddCallback={(fx, fy) => {
            const state = useFlowStore.getState();
            let cbName = 'callback';
            let i = 1;
            while (state.callbacks[cbName]) cbName = `callback_${i++}`;
            addCallback(cbName, {});
            const cardId = `__cb__${cbName}`;
            const layout = useFlowStore.getState().layout;
            layout.nodePositions[cardId] = { x: fx, y: fy };
            useFlowStore.setState((s) => ({
              rfNodes: s.rfNodes.map((n) => (n.id === cardId ? { ...n, position: { x: fx, y: fy } } : n)),
            }));
            setActivePanel({ kind: 'callbackEdit', callbackName: cbName });
          }}
          onDeleteNode={(id) => {
            const n = rfNodes.find((x) => x.id === id);
            if (n) deleteRfNode(n);
          }}
          onDeleteEdge={(id) => {
            const e = rfEdges.find((x) => x.id === id);
            if (e) deleteRfEdge(e);
          }}
        />
      )}
    </div>
  );
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
  onAddCallback: (flowX: number, flowY: number) => void;
  onDeleteNode: (id: string) => void;
  onDeleteEdge: (id: string) => void;
}

const QUICK_NODE_TYPES: Array<{ type: NodeType; label: string }> = [
  { type: 'sequence', label: 'Sequence' },
  { type: 'action', label: 'Action' },
  { type: 'loop', label: 'Loop' },
  { type: 'boolean', label: 'Boolean' },
  { type: 'weighted', label: 'Weighted' },
  { type: 'wait', label: 'Wait' },
  { type: 'break', label: 'Break' },
  { type: 'continue', label: 'Continue' },
];

function CanvasContextMenu({
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
  onAddCallback,
  onDeleteNode,
  onDeleteEdge,
}: CanvasContextMenuProps) {
  const node = menu.kind === 'node' && menu.targetId ? rfNodes.find((n) => n.id === menu.targetId) : null;
  const isCallbackCard = node?.type === 'callbackCard';
  const isAction = node?.type === 'action';
  const canSaveTemplate = isAction || isCallbackCard;

  return (
    <div
      style={{
        position: 'absolute',
        left: menu.x,
        top: menu.y,
        background: 'var(--bg-panel)',
        border: '1px solid var(--border-color, #d9d9d9)',
        borderRadius: 4,
        boxShadow: '0 6px 16px 0 rgba(0,0,0,0.18)',
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
          {QUICK_NODE_TYPES.map((q) => (
            <MenuItem
              key={q.type}
              onClick={() => {
                onAddNode(q.type, menu.flowX, menu.flowY);
                onClose();
              }}
            >
              {q.label}
            </MenuItem>
          ))}
          <MenuItem
            onClick={() => {
              onAddCallback(menu.flowX, menu.flowY);
              onClose();
            }}
          >
            Callback
          </MenuItem>
        </>
      )}
    </div>
  );
}

function MenuItem({
  children,
  onClick,
  danger,
  disabled,
}: {
  children: React.ReactNode;
  onClick: () => void;
  danger?: boolean;
  disabled?: boolean;
}) {
  return (
    <div
      onClick={disabled ? undefined : onClick}
      style={{
        padding: '6px 12px',
        cursor: disabled ? 'not-allowed' : 'pointer',
        borderRadius: 3,
        color: disabled ? 'var(--text-tertiary)' : danger ? '#ff4d4f' : 'inherit',
        fontSize: 13,
        display: 'flex',
        justifyContent: 'space-between',
        alignItems: 'center',
        gap: 12,
      }}
      onMouseEnter={(e) => {
        if (!disabled) e.currentTarget.style.background = 'rgba(0,0,0,0.06)';
      }}
      onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
    >
      {children}
    </div>
  );
}

function Hint({ children }: { children: React.ReactNode }) {
  return (
    <span style={{ fontSize: 11, color: 'var(--text-tertiary)', fontFamily: 'monospace' }}>{children}</span>
  );
}

function SubmenuLabel({ children }: { children: React.ReactNode }) {
  return (
    <div style={{ padding: '4px 12px 2px', fontSize: 11, color: 'var(--text-tertiary)', fontWeight: 600 }}>
      {children}
    </div>
  );
}

function Divider() {
  return <div style={{ height: 1, background: 'rgba(0,0,0,0.06)', margin: '4px 0' }} />;
}

export function FlowCanvas() {
  return (
    <ReactFlowProvider>
      <FlowCanvasInner />
    </ReactFlowProvider>
  );
}
