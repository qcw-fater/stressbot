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
import { useShallow } from 'zustand/react/shallow';

import { App as AntApp } from 'antd';
import { nodeTypes } from './nodes/registry';
import { edgeTypes } from './edges/registry';
import { useFlowStore } from './store/flowStore';
import { useEditorStore, type Clipboard } from './store/editorStore';
import { generateNodeId } from './utils/nodeIdGen';
import { saveActionTemplate, saveListenTemplate, type ActionTemplate, type ListenTemplate, type ListenTemplateDefaultRef } from './library/templateStore';
import { cloneListenDefaultRef, inferListenDefaultRef } from './library/listenTemplateDefaults';
import { useFlowReadOnly } from './flowReadOnlyContext';
import type { FlowNode, NodeType } from '@/types/flow';
import type { ActionDef } from '@/types/action';
import type { ListenDef } from '@/types/listen';
import { classifyListen } from '@/types/listen';
import { normalizeOnError } from './utils/onError';

interface ContextMenu {
  x: number;
  y: number;
  flowX: number; // 画布坐标：粘贴时用
  flowY: number;
  kind: 'node' | 'edge' | 'pane';
  targetId?: string;
}

function FlowCanvasInner() {
  const { message } = AntApp.useApp();
  const readOnly = useFlowReadOnly();

  // flowStore: frequently-changing visual data (grouped for fewer re-subscriptions)
  const { rfNodes, rfEdges, needsFitView } = useFlowStore(useShallow((s) => ({
    rfNodes: s.rfNodes,
    rfEdges: s.rfEdges,
    needsFitView: s.needsFitView,
  })));

  // flowStore: stable action refs (never change, individual calls are fine)
  const onNodesChange = useFlowStore((s) => s.onNodesChange);
  const onEdgesChange = useFlowStore((s) => s.onEdgesChange);
  const addNode = useFlowStore((s) => s.addNode);
  const addAction = useFlowStore((s) => s.addAction);
  const addListen = useFlowStore((s) => s.addListen);
  const setListenDefaultRef = useFlowStore((s) => s.setListenDefaultRef);
  const removeNode = useFlowStore((s) => s.removeNode);
  const removeListen = useFlowStore((s) => s.removeListen);
  const updateNode = useFlowStore((s) => s.updateNode);

  // editorStore: UI flags + clipboard data (grouped)
  const { showMinimap, showGrid, clipboard } = useEditorStore(useShallow((s) => ({
    showMinimap: s.showMinimap,
    showGrid: s.showGrid,
    clipboard: s.clipboard,
  })));

  // editorStore: stable action refs
  const setSelectedNode = useEditorStore((s) => s.setSelectedNode);
  const setActivePanel = useEditorStore((s) => s.setActivePanel);
  const setEdgeHighlightNodes = useEditorStore((s) => s.setEdgeHighlightNodes);
  const setEdgeHighlightColor = useEditorStore((s) => s.setEdgeHighlightColor);
  const setClipboard = useEditorStore((s) => s.setClipboard);

  const wrapperRef = useRef<HTMLDivElement>(null);
  const { screenToFlowPosition, setCenter } = useReactFlow();
  const [menu, setMenu] = useState<ContextMenu | null>(null);

  // 加载/新建流程后定位到 main 入口节点
  const rfNodeIds = useFlowStore((s) => s.rfNodes.map((n) => n.id).join(','));
  useEffect(() => {
    if (needsFitView && rfNodeIds.length > 0) {
      const mainPos = useFlowStore.getState().layout.nodePositions['main'];
      if (mainPos) {
        const timer = setTimeout(() => {
          if (wrapperRef.current && wrapperRef.current.clientWidth > 0 && wrapperRef.current.clientHeight > 0) {
            setCenter(mainPos.x, mainPos.y, { zoom: 1, duration: 300 });
          }
          useFlowStore.setState({ needsFitView: false });
        }, 300);
        return () => clearTimeout(timer);
      }
    }
  }, [needsFitView, rfNodeIds, setCenter]);

  // dropEffect 必须与拖出端的 effectAllowed 兼容：
  //   - 普通节点类型：effectAllowed='move'  → dropEffect='move'
  //   - 模板（action/listen）：effectAllowed='copy'  → dropEffect='copy'
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

      // 1b. listen 拖入：创建空 silent listen + ListenCard，并打开编辑面板
      if (e.dataTransfer.getData('application/stressbot-new-listen')) {
        const state = useFlowStore.getState();
        let listenName = 'listen';
        let i = 1;
        while (state.listens[listenName]) listenName = `listen_${i++}`;
        addListen(listenName, {});
        const cardId = `__cb__${listenName}`;
        const layout = useFlowStore.getState().layout;
        layout.nodePositions[cardId] = { x: flowPos.x, y: flowPos.y };
        useFlowStore.setState((s) => ({
          rfNodes: s.rfNodes.map((n) => (n.id === cardId ? { ...n, position: flowPos } : n)),
        }));
        setActivePanel({ kind: 'listenEdit', listenName });
        return;
      }

      // 2. 模板拖入（action / listen）
      const tplRaw = e.dataTransfer.getData('application/stressbot-template');
      if (!tplRaw) return;
      try {
        const { kind, template } = JSON.parse(tplRaw) as
          | { kind: 'action'; template: ActionTemplate }
          | { kind: 'listen'; template: ListenTemplate };
        if (kind === 'action') {
          const state = useFlowStore.getState();
          // 唯一 action 名
          let actionName = template.name;
          let i = 1;
          while (state.actions[actionName]) actionName = `${template.name}_${i++}`;
          addAction(actionName, template.data as ActionDef);
          // 模板复用节点用动作名作为节点 ID；只有冲突时递增后缀。
          const taken = new Set(Object.keys(useFlowStore.getState().nodes));
          let nodeId = actionName;
          let nodeIndex = 1;
          while (taken.has(nodeId)) nodeId = `${actionName}_${nodeIndex++}`;
          addNode(nodeId, { type: 'action', action: actionName, description: template.description });
          const layout = useFlowStore.getState().layout;
          layout.nodePositions[nodeId] = { x: flowPos.x, y: flowPos.y };
          useFlowStore.setState((s) => ({
            rfNodes: s.rfNodes.map((n) => (n.id === nodeId ? { ...n, position: flowPos } : n)),
          }));
          setSelectedNode(nodeId);
        } else if (kind === 'listen') {
          const state = useFlowStore.getState();
          let listenName = template.name;
          let i = 1;
          while (state.listens[listenName]) listenName = `${template.name}_${i++}`;
          addListen(listenName, template.data as ListenDef);
          setListenDefaultRef(listenName, template.defaultRef);
          // ListenCard 自动由 jsonToFlow 创建；位置覆盖到鼠标落点
          const cardId = `__cb__${listenName}`;
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
    [addNode, addAction, addListen, readOnly, screenToFlowPosition, setActivePanel, setSelectedNode],
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

  /** 删除一个 React Flow node：根据类型决定是删 flow node 还是 listen */
  const deleteRfNode = useCallback(
    (n: RFNode) => {
      if (n.type === 'listenCard') {
        const listenName = (n.data as { listenName?: string }).listenName;
        if (listenName) removeListen(listenName);
      } else {
        removeNode(n.id);
      }
    },
    [removeNode, removeListen],
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
        // action → listen：从 listenRefs 中移除指向 e.target 的引用
        if (src.type === 'action' && src.listenRefs) {
          const listenName = e.target.replace(/^__cb__/, '');
          updateNode(e.source, {
            listenRefs: src.listenRefs.filter((r) => r.listen !== listenName),
          });
        }
      } else if (src.type === 'action' && handleId === 'error') {
        updateNode(e.source, { onError: normalizeOnError({ ...(src.onError ?? {}), handler: undefined }) });
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
   *   action    · listen     → listenRefs.push({ listen })，target 必须是 listenCard
   *   action    · error      → onError.handler = target，target 必须是普通节点
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
      const targetIsListenCard = params.target.startsWith('__cb__');
      const targetListenName = targetIsListenCard ? params.target.slice('__cb__'.length) : null;
      const targetNodeId = !targetIsListenCard ? params.target : null;

      if (src.type === 'sequence') {
        const next = (src.next ?? []).slice();
        if (handle === 'seq-add') {
          if (targetNodeId) next.push(targetNodeId);
        } else if (handle.startsWith('seq-')) {
          const idx = Number(handle.slice(4));
          if (Number.isFinite(idx) && targetNodeId) next.splice(idx, 0, targetNodeId);
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
          if (Number.isFinite(idx) && targetNodeId) options.splice(idx, 0, { node: targetNodeId, weight: 1 });
        }
        updateNode(params.source, { options });
        return;
      }
      if (src.type === 'action' && handle === 'listen' && targetListenName) {
        const list = (src.listenRefs ?? []).slice();
        if (!list.some((r) => r.listen === targetListenName)) {
          const defaultRef = useFlowStore.getState().listenDefaultRefs[targetListenName];
          list.push(defaultRef
            ? { route: cloneListenDefaultRef(defaultRef)?.route, server: defaultRef.server, listen: targetListenName }
            : { route: null, server: '', listen: targetListenName });
        }
        updateNode(params.source, { listenRefs: list });
        return;
      }
      if (src.type === 'action' && handle === 'error') {
        if (!targetNodeId) {
          message.warning('错误处理节点必须连接到普通节点');
          return;
        }
        if (targetNodeId === params.source) {
          message.warning('错误处理节点不能指向自身');
          return;
        }
        updateNode(params.source, { onError: normalizeOnError({ ...(src.onError ?? {}), handler: targetNodeId }) });
        return;
      }
    },
    [message, readOnly, updateNode],
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

  /** 把一个 React Flow node（含相关 action / listen）打包到剪贴板 */
  const copyRfNode = useCallback(
    (n: RFNode): Clipboard => {
      const flow = useFlowStore.getState();
      if (n.type === 'listenCard') {
        const listenName = (n.data as { listenName?: string }).listenName;
        if (!listenName || !flow.listens[listenName]) return null;
        const inferred = inferListenDefaultRef(flow.nodes, listenName);
        return {
          kind: 'listen',
          listenName,
          listen: JSON.parse(JSON.stringify(flow.listens[listenName])),
          defaultRef: inferred.defaultRef ?? flow.listenDefaultRefs[listenName],
        };
      }
      const node = flow.nodes[n.id];
      if (!node) return null;
      const action =
        node.type === 'action' && node.action && flow.actions[node.action]
          ? { name: node.action, def: JSON.parse(JSON.stringify(flow.actions[node.action])) as ActionDef }
          : undefined;
      // listenRefs 涉及的 listen 一并复制（跨流程粘贴时不丢引用）
      const listens: Array<{ name: string; def: ListenDef; defaultRef?: ListenTemplateDefaultRef }> = [];
      if (node.type === 'action' && node.listenRefs) {
        for (const r of node.listenRefs) {
          if (!r.listen) continue;
          const listenDef = flow.listens[r.listen];
          if (listenDef) {
            const inferred = inferListenDefaultRef(flow.nodes, r.listen);
            listens.push({
              name: r.listen,
              def: JSON.parse(JSON.stringify(listenDef)),
              defaultRef: inferred.defaultRef ?? flow.listenDefaultRefs[r.listen],
            });
          }
        }
      }
      return {
        kind: 'node',
        nodeId: n.id,
        node: JSON.parse(JSON.stringify(node)),
        action,
        listens: listens.length > 0 ? listens : undefined,
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
  const uniqueListenName = (base: string) => {
    const map = useFlowStore.getState().listens;
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
        // 重新分配 action / listen 名（避免覆盖已有）
        if (node.type === 'action' && clipboard.action) {
          const newAct = uniqueActionName(clipboard.action.name);
          addAction(newAct, clipboard.action.def);
          node.action = newAct;
        }
        if (node.type === 'action' && node.listenRefs && clipboard.listens) {
          const renameMap: Record<string, string> = {};
          for (const c of clipboard.listens) {
            const newListen = uniqueListenName(c.name);
            addListen(newListen, c.def);
            setListenDefaultRef(newListen, c.defaultRef);
            renameMap[c.name] = newListen;
          }
          node.listenRefs = node.listenRefs.map((r) => ({
            ...r,
            listen: r.listen ? (renameMap[r.listen] ?? r.listen) : r.listen,
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
      } else if (clipboard.kind === 'listen') {
        const newName = uniqueListenName(clipboard.listenName);
        addListen(newName, JSON.parse(JSON.stringify(clipboard.listen)));
        setListenDefaultRef(newName, clipboard.defaultRef);
        const cardId = `__cb__${newName}`;
        const layout = useFlowStore.getState().layout;
        layout.nodePositions[cardId] = { x: flowX, y: flowY };
        useFlowStore.setState((s) => ({
          rfNodes: s.rfNodes.map((n) => (n.id === cardId ? { ...n, position: { x: flowX, y: flowY } } : n)),
        }));
        message.success(`已粘贴 listen ${newName}`);
      }
    },
    [addAction, addListen, addNode, clipboard, setListenDefaultRef, setSelectedNode],
  );

  /** 把节点保存为模板（写入本地模板库） */
  const saveNodeAsTemplate = useCallback(async (n: RFNode) => {
    const flow = useFlowStore.getState();
    if (n.type === 'listenCard') {
      const listenName = (n.data as { listenName?: string }).listenName;
      if (!listenName) return;
      const listenDef = flow.listens[listenName];
      if (!listenDef) return;
      const inferred = inferListenDefaultRef(flow.nodes, listenName);
      await saveListenTemplate({
        name: listenName,
        kind: classifyListen(listenDef),
        data: listenDef,
        defaultRef: inferred.defaultRef ?? flow.listenDefaultRefs[listenName],
      });
      if (inferred.ambiguous) {
        message.warning('存在多条不同监听注册，已使用第一条作为模板默认注册');
      }
      message.success(`listen ${listenName} 已保存为模板`);
      return;
    }
    const node = flow.nodes[n.id];
    if (!node || node.type !== 'action' || !node.action) {
      message.warning('仅 action 节点 / listen 卡片可保存为模板');
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
  }, [message]);

  // 记录最近一次鼠标在画布内的客户端坐标，Ctrl+V 粘贴时作为落点
  const lastMousePosRef = useRef<{ x: number; y: number } | null>(null);

  // 全局快捷键：
  //   F1~F9 在鼠标位置新建节点 / listen 卡片
  //   Ctrl/Cmd+C 复制 / Ctrl/Cmd+X 剪切 / Ctrl/Cmd+V 粘贴
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
      const target = e.target as HTMLElement | null;
      if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)) return;
      if (!inCanvas()) return;

      // F1~F9 快捷新建节点（无需修饰键）
      const fKey = /^F(\d)$/i.exec(e.key);
      if (fKey && !e.ctrlKey && !e.metaKey && !e.altKey) {
        const idx = parseInt(fKey[1], 10);
        if (idx >= 1 && idx <= 9) {
          const last = lastMousePosRef.current;
          if (!last) return;
          e.preventDefault();
          const flowPos = screenToFlowPosition({ x: last.x, y: last.y });
          if (idx <= 8) {
            const q = QUICK_NODE_TYPES[idx - 1];
            const taken = new Set(Object.keys(useFlowStore.getState().nodes));
            const id = generateNodeId(q.type, taken);
            addNode(id, { type: q.type });
            const layout = useFlowStore.getState().layout;
            layout.nodePositions[id] = { x: flowPos.x, y: flowPos.y };
            useFlowStore.setState((s) => ({
              rfNodes: s.rfNodes.map((n) => (n.id === id ? { ...n, position: flowPos } : n)),
            }));
            setSelectedNode(id);
          } else {
            // F9 = Listen
            const listenName = uniqueListenName('listen');
            addListen(listenName, {});
            const cardId = `__cb__${listenName}`;
            const layout = useFlowStore.getState().layout;
            layout.nodePositions[cardId] = { x: flowPos.x, y: flowPos.y };
            useFlowStore.setState((s) => ({
              rfNodes: s.rfNodes.map((n) => (n.id === cardId ? { ...n, position: flowPos } : n)),
            }));
          }
          return;
        }
      }

      const mod = e.ctrlKey || e.metaKey;
      if (!mod) return;
      if ((window.getSelection()?.toString().length ?? 0) > 0) return;

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
      listenCard: '--node-listen',
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
          if (n.type === 'listenCard') {
            const listenName = (n.data as { listenName?: string }).listenName;
            if (listenName) {
              setActivePanel({ kind: 'listenEdit', listenName });
            }
            return;
          }
          // break/continue 节点无可编辑字段，双击不打开编辑面板
          if (n.type === 'break' || n.type === 'continue') return;
          setActivePanel({ kind: 'nodeEdit', nodeId: n.id });
        }}
        onPaneClick={() => {
          setSelectedNode(null);
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
            if (n.type === 'listenCard') {
              const listenName = (n.data as { listenName?: string }).listenName;
              if (listenName) setActivePanel({ kind: 'listenEdit', listenName });
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
          onAddListen={(fx, fy) => {
            const listenName = uniqueListenName('listen');
            addListen(listenName, {});
            const cardId = `__cb__${listenName}`;
            const layout = useFlowStore.getState().layout;
            layout.nodePositions[cardId] = { x: fx, y: fy };
            useFlowStore.setState((s) => ({
              rfNodes: s.rfNodes.map((n) => (n.id === cardId ? { ...n, position: { x: fx, y: fy } } : n)),
            }));
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
  onAddListen: (flowX: number, flowY: number) => void;
  onDeleteNode: (id: string) => void;
  onDeleteEdge: (id: string) => void;
}

const QUICK_NODE_TYPES: Array<{ type: NodeType; label: string }> = [
  { type: 'sequence', label: 'Sequence' },
  { type: 'loop', label: 'Loop' },
  { type: 'boolean', label: 'Boolean' },
  { type: 'weighted', label: 'Weighted' },
  { type: 'wait', label: 'Wait' },
  { type: 'break', label: 'Break' },
  { type: 'continue', label: 'Continue' },
  { type: 'action', label: 'Action' },
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
  onAddListen,
  onDeleteNode,
  onDeleteEdge,
}: CanvasContextMenuProps) {
  const node = menu.kind === 'node' && menu.targetId ? rfNodes.find((n) => n.id === menu.targetId) : null;
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
            Listen <Hint>F9</Hint>
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
  return <div style={{ height: 1, background: 'var(--divider-bg)', margin: '4px 0' }} />;
}

export function FlowCanvas() {
  return (
    <ReactFlowProvider>
      <FlowCanvasInner />
    </ReactFlowProvider>
  );
}
