/**
 * Zustand 核心 store：维护 TaskFlow 业务数据 + React Flow 视觉状态。
 *
 * 设计文档 §10.1：业务数据（nodes/actions/listens）与视觉状态（rfNodes/rfEdges/positions）
 * 同 store 中维护，但导出 flow.json 时只取业务部分。
 */

import { create } from 'zustand';
import { applyEdgeChanges, applyNodeChanges } from '@xyflow/react';
import type { Edge, EdgeChange, Node as RFNode, NodeChange } from '@xyflow/react';

import type { ActionDef } from '@/types/action';
import type { ListenDef } from '@/types/listen';
import type { FlowNode, TaskFlow } from '@/types/flow';
import type { FlowLayout } from '@/types/editor';
import { emptyFlowLayout } from '@/types/editor';
import { dagreLayout } from '../codec/dagreLayout';
import { jsonToFlow } from '../codec/jsonToFlow';
import { flowToJson, type FlowJson } from '../codec/flowToJson';
import { validateFlow, type ValidationIssue } from '../validation/refsCheck';

interface FlowState {
  // ── 业务数据（与 flow.json 1:1） ────────
  defaultDelayMs: number;
  nodes: Record<string, FlowNode>;
  actions: Record<string, ActionDef>;
  listens: Record<string, ListenDef>;

  // ── React Flow 视觉状态 ────────
  rfNodes: RFNode[];
  rfEdges: Edge[];
  layout: FlowLayout;

  // ── 派生：listen 引用计数 ────────
  listenRefCount: Record<string, number>;

  // ── 派生：每个 listen 被哪些 action 节点注册（用于反向悬停高亮） ────────
  nodesByListen: Record<string, string[]>;

  // ── 派生：节点级校验问题（按节点 ID 分组，仅含 location.kind === 'node' 的 issue） ────────
  issuesByNodeId: Record<string, ValidationIssue[]>;

  // ── UI 信号：加载/重置后需要 fitView ────────
  needsFitView: boolean;

  // ── 加载 / 替换 ────────
  loadFromTaskFlow: (flow: FlowJson, layout?: FlowLayout) => void;
  /** 清空，回到空白编辑稿 */
  reset: (center?: { x: number; y: number }) => void;

  // ── 导出 ────────
  toTaskFlow: () => FlowJson;

  // ── React Flow 事件代理 ────────
  onNodesChange: (changes: NodeChange[]) => void;
  onEdgesChange: (changes: EdgeChange[]) => void;

  // ── 自动布局 ────────
  applyAutoLayout: (direction?: 'LR' | 'TB') => void;

  // ── 节点 CRUD ────────
  addNode: (id: string, node: FlowNode) => void;
  updateNode: (id: string, partial: Partial<FlowNode>) => void;
  /** 完全替换节点（用于"还原本次修改"等需要把当前 node 整体回退到某个快照的场景） */
  replaceNode: (id: string, node: FlowNode) => void;
  removeNode: (id: string) => void;
  renameNode: (oldId: string, newId: string) => void;

  // ── 动作 CRUD ────────
  addAction: (name: string, def: ActionDef) => void;
  updateAction: (name: string, partial: Partial<ActionDef>) => void;
  /** 完全替换 action（用于回退快照，partial merge 保留旧字段会留下脏数据） */
  replaceAction: (name: string, def: ActionDef) => void;
  removeAction: (name: string) => void;
  renameAction: (oldName: string, newName: string) => void;

  // ── 回调 CRUD ────────
  addListen: (name: string, def: ListenDef) => void;
  updateListen: (name: string, partial: Partial<ListenDef>) => void;
  /** 完全替换 listen（用于 silent/decl/lua 形态切换：partial merge 会保留旧字段导致 kind 判错） */
  replaceListen: (name: string, def: ListenDef) => void;
  removeListen: (name: string) => void;
  renameListen: (oldName: string, newName: string) => void;

  /** 触发派生数据重算（rfNodes/rfEdges/listenRefCount） */
  syncDerived: () => void;
}

export const useFlowStore = create<FlowState>((set, get) => ({
  defaultDelayMs: 1000,
  nodes: {},
  actions: {},
  listens: {},
  rfNodes: [],
  rfEdges: [],
  layout: emptyFlowLayout(),
  listenRefCount: {},
  nodesByListen: {},
  issuesByNodeId: {},
  needsFitView: false,

  loadFromTaskFlow: (flow, layout) => {
    const { rfNodes, rfEdges, listenRefCount, nodesByListen } = jsonToFlow({
      defaultDelayMs: flow.defaultDelayMs,
      nodes: flow.nodes,
      actions: flow.actions,
      callbacks: flow.callbacks,
    });
    let positions: Record<string, { x: number; y: number }>;
    if (layout?.nodePositions && Object.keys(layout.nodePositions).length > 0) {
      positions = Object.fromEntries(
        Object.entries(layout.nodePositions).map(([id, m]) => [id, { x: m.x, y: m.y }]),
      );
    } else {
      positions = dagreLayout(rfNodes, rfEdges, { direction: 'LR' });
      // 给 ListenCard 一个事件区独立排布（画布右侧竖排）
      const cardX = computeCardX(positions);
      let cardY = 0;
      for (const n of rfNodes) {
        if (n.type === 'listenCard') {
          positions[n.id] = { x: cardX, y: cardY };
          cardY += 90;
        }
      }
    }
    const positioned = rfNodes.map((n) => ({ ...n, position: positions[n.id] ?? { x: 0, y: 0 } }));
    // 计算节点级校验
    const flowForValidation: TaskFlow = {
      defaultDelayMs: flow.defaultDelayMs,
      nodes: flow.nodes,
      actions: flow.actions,
      listens: flow.callbacks,
    };
    const report = validateFlow(flowForValidation);
    const issuesByNodeId: Record<string, ValidationIssue[]> = {};
    for (const it of [...report.errors, ...report.warnings]) {
      if (it.location?.kind === 'node') {
        (issuesByNodeId[it.location.id] ??= []).push(it);
      }
      if (it.location?.kind === 'action') {
        for (const [id, n] of Object.entries(flow.nodes)) {
          if (n.type === 'action' && n.action === it.location.id) {
            (issuesByNodeId[id] ??= []).push(it);
          }
        }
      }
    }
    set({
      defaultDelayMs: flow.defaultDelayMs,
      nodes: flow.nodes,
      actions: flow.actions,
      listens: flow.callbacks,
      rfNodes: positioned,
      rfEdges,
      listenRefCount,
      nodesByListen,
      issuesByNodeId,
      needsFitView: true,
      layout: layout ?? {
        ...emptyFlowLayout(),
        nodePositions: Object.fromEntries(
          Object.entries(positions).map(([id, p]) => [id, { x: p.x, y: p.y }]),
        ),
      },
    });
  },

  reset: (center?: { x: number; y: number }) => {
    const pos = center ?? { x: 300, y: 300 };
    set({
      defaultDelayMs: 1000,
      nodes: { main: { type: 'sequence', next: [], description: '入口节点' } },
      actions: {},
      listens: {},
      rfNodes: [],
      rfEdges: [],
      layout: {
        ...emptyFlowLayout(),
        nodePositions: { main: pos },
      },
      listenRefCount: {},
      nodesByListen: {},
      issuesByNodeId: {},
      needsFitView: true,
    });
    get().syncDerived();
  },

  toTaskFlow: () => {
    const s = get();
    return flowToJson({
      defaultDelayMs: s.defaultDelayMs,
      nodes: s.nodes,
      actions: s.actions,
      listens: s.listens,
    });
  },

  onNodesChange: (changes) => {
    set((s) => {
      const next = applyNodeChanges(changes, s.rfNodes);
      // 把位置变更同步到 layout（用户拖动时自动持久化）
      const layout = { ...s.layout, nodePositions: { ...s.layout.nodePositions } };
      for (const c of changes) {
        if (c.type === 'position' && c.position) {
          layout.nodePositions[c.id] = { ...layout.nodePositions[c.id], x: c.position.x, y: c.position.y };
        }
      }
      return { rfNodes: next, layout };
    });
  },

  onEdgesChange: (changes) => {
    set((s) => ({ rfEdges: applyEdgeChanges(changes, s.rfEdges) }));
  },

  applyAutoLayout: (direction = 'LR') => {
    set((s) => {
      const positions = dagreLayout(s.rfNodes, s.rfEdges, { direction });
      const cardX = computeCardX(positions);
      let cardY = 0;
      for (const n of s.rfNodes) {
        if (n.type === 'listenCard') {
          positions[n.id] = { x: cardX, y: cardY };
          cardY += 90;
        }
      }
      const positioned = s.rfNodes.map((n) => ({ ...n, position: positions[n.id] ?? n.position }));
      const layout = {
        ...s.layout,
        nodePositions: Object.fromEntries(
          Object.entries(positions).map(([id, p]) => [id, { x: p.x, y: p.y }]),
        ),
      };
      return { rfNodes: positioned, layout };
    });
  },

  addNode: (id, node) => {
    set((s) => ({ nodes: { ...s.nodes, [id]: node } }));
    get().syncDerived();
  },

  updateNode: (id, partial) => {
    set((s) => {
      const cur = s.nodes[id];
      if (!cur) return {};
      return { nodes: { ...s.nodes, [id]: { ...cur, ...partial } } };
    });
    get().syncDerived();
  },

  replaceNode: (id, node) => {
    set((s) => {
      if (!s.nodes[id]) return {};
      return { nodes: { ...s.nodes, [id]: node } };
    });
    get().syncDerived();
  },

  removeNode: (id) => {
    set((s) => {
      const removed = s.nodes[id];
      const nodes = { ...s.nodes };
      delete nodes[id];
      // 清理孤儿 action：如果删除的节点是 action 节点，且没有其他节点引用同一个 action，则一并删除
      let actions = s.actions;
      if (removed?.type === 'action' && removed.action) {
        const stillReferenced = Object.values(nodes).some(
          (n) => n.type === 'action' && n.action === removed.action,
        );
        if (!stillReferenced) {
          actions = { ...s.actions };
          delete actions[removed.action];
        }
      }
      return { nodes, actions };
    });
    get().syncDerived();
  },

  renameNode: (oldId, newId) => {
    if (oldId === newId) return;
    set((s) => {
      if (!(oldId in s.nodes) || newId in s.nodes) return {};
      const nodes: Record<string, FlowNode> = {};
      for (const [k, v] of Object.entries(s.nodes)) {
        if (k === oldId) {
          nodes[newId] = renameRefsInNode(v, oldId, newId);
        } else {
          nodes[k] = renameRefsInNode(v, oldId, newId);
        }
      }
      // 同步更新布局位置表的 key，避免新 ID 节点掉到 (0,0)
      const layout = { ...s.layout };
      if (layout.nodePositions[oldId]) {
        layout.nodePositions = { ...layout.nodePositions };
        layout.nodePositions[newId] = layout.nodePositions[oldId];
        delete layout.nodePositions[oldId];
      }
      return { nodes, layout };
    });
    get().syncDerived();
  },

  addAction: (name, def) => {
    set((s) => ({ actions: { ...s.actions, [name]: def } }));
    get().syncDerived();
  },
  updateAction: (name, partial) => {
    set((s) => {
      const cur = s.actions[name];
      if (!cur) return {};
      return { actions: { ...s.actions, [name]: { ...cur, ...partial } } };
    });
    get().syncDerived();
  },
  replaceAction: (name, def) => {
    set((s) => {
      if (!s.actions[name]) return {};
      return { actions: { ...s.actions, [name]: def } };
    });
    get().syncDerived();
  },
  removeAction: (name) => {
    set((s) => {
      const actions = { ...s.actions };
      delete actions[name];
      return { actions };
    });
    get().syncDerived();
  },
  renameAction: (oldName, newName) => {
    if (oldName === newName) return;
    set((s) => {
      if (!(oldName in s.actions) || newName in s.actions) return {};
      const actions: Record<string, ActionDef> = {};
      for (const [k, v] of Object.entries(s.actions)) {
        actions[k === oldName ? newName : k] = v;
      }
      // 同步 nodes 中的 action 引用
      const nodes: Record<string, FlowNode> = {};
      for (const [k, v] of Object.entries(s.nodes)) {
        if (v.type === 'action' && v.action === oldName) {
          nodes[k] = { ...v, action: newName };
        } else {
          nodes[k] = v;
        }
      }
      return { actions, nodes };
    });
    get().syncDerived();
  },

  addListen: (name, def) => {
    set((s) => ({ listens: { ...s.listens, [name]: def } }));
    get().syncDerived();
  },
  updateListen: (name, partial) => {
    set((s) => {
      const cur = s.listens[name];
      if (!cur) return {};
      return { listens: { ...s.listens, [name]: { ...cur, ...partial } } };
    });
    get().syncDerived();
  },
  replaceListen: (name, def) => {
    set((s) => {
      if (!s.listens[name]) return {};
      return { listens: { ...s.listens, [name]: def } };
    });
    get().syncDerived();
  },
  removeListen: (name) => {
    set((s) => {
      const listens = { ...s.listens };
      delete listens[name];
      return { listens };
    });
    get().syncDerived();
  },
  renameListen: (oldName, newName) => {
    if (oldName === newName) return;
    set((s) => {
      if (!(oldName in s.listens) || newName in s.listens) return {};
      const listens: Record<string, ListenDef> = {};
      for (const [k, v] of Object.entries(s.listens)) {
        listens[k === oldName ? newName : k] = v;
      }
      // 同步所有 listenCallbacks 中的引用
      const nodes: Record<string, FlowNode> = {};
      for (const [k, v] of Object.entries(s.nodes)) {
        if (v.type === 'action' && v.listenCallbacks?.length) {
          nodes[k] = {
            ...v,
            listenCallbacks: v.listenCallbacks.map((r) =>
              r.callback === oldName ? { ...r, callback: newName } : r,
            ),
          };
        } else {
          nodes[k] = v;
        }
      }
      return { listens, nodes };
    });
    get().syncDerived();
  },

  syncDerived: () => {
    const s = get();
    const { rfNodes, rfEdges, listenRefCount, nodesByListen } = jsonToFlow({
      defaultDelayMs: s.defaultDelayMs,
      nodes: s.nodes,
      actions: s.actions,
      callbacks: s.listens,
    });
    // 保留已有位置（避免拖动后被覆盖）
    const positions = s.layout.nodePositions;
    const positioned = rfNodes.map((n) => {
      const pos = positions[n.id];
      const existing = s.rfNodes.find((x) => x.id === n.id);
      return {
        ...n,
        position: pos
          ? { x: pos.x, y: pos.y }
          : (existing?.position ?? { x: 0, y: 0 }),
      };
    });
    // 实时校验：把节点级 issue 按节点 ID 分组，供 NodeShell 显示徽章
    const flowForValidation: TaskFlow = {
      defaultDelayMs: s.defaultDelayMs,
      nodes: s.nodes,
      actions: s.actions,
      listens: s.listens,
    };
    const report = validateFlow(flowForValidation);
    const issuesByNodeId: Record<string, ValidationIssue[]> = {};
    for (const it of [...report.errors, ...report.warnings]) {
      if (it.location?.kind === 'node') {
        (issuesByNodeId[it.location.id] ??= []).push(it);
      }
      // 把 action 上的 issue 关联到引用此 action 的所有节点上
      if (it.location?.kind === 'action') {
        for (const [id, n] of Object.entries(s.nodes)) {
          if (n.type === 'action' && n.action === it.location.id) {
            (issuesByNodeId[id] ??= []).push(it);
          }
        }
      }
    }
    set({ rfNodes: positioned, rfEdges, listenRefCount, nodesByListen, issuesByNodeId });
  },
}));

/**
 * 计算 ListenCard 的 X 坐标：取主区最右节点 +距离。
 */
function computeCardX(positions: Record<string, { x: number; y: number }>): number {
  let maxX = 0;
  for (const [id, p] of Object.entries(positions)) {
    if (id.startsWith('__cb__')) continue;
    if (p.x > maxX) maxX = p.x;
  }
  return maxX + 360;
}

/** 节点重命名时，同步更新所有指向它的引用（next/body/trueNext/falseNext/options） */
function renameRefsInNode(node: FlowNode, oldId: string, newId: string): FlowNode {
  const out: FlowNode = { ...node };
  if (node.next?.length) {
    out.next = node.next.map((n) => (n === oldId ? newId : n));
  }
  if (node.body === oldId) out.body = newId;
  if (node.trueNext === oldId) out.trueNext = newId;
  if (node.falseNext === oldId) out.falseNext = newId;
  if (node.options?.length) {
    out.options = node.options.map((o) => (o.node === oldId ? { ...o, node: newId } : o));
  }
  return out;
}
