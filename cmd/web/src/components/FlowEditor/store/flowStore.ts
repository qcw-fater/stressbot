/**
 * Zustand 核心 store：维护 TaskFlow 业务数据 + React Flow 视觉状态。
 *
 * 设计文档 §10.1：业务数据（nodes/actions/callbacks）与视觉状态（rfNodes/rfEdges/positions）
 * 同 store 中维护，但导出 flow.json 时只取业务部分。
 */

import { create } from 'zustand';
import { applyEdgeChanges, applyNodeChanges } from '@xyflow/react';
import type { Edge, EdgeChange, Node as RFNode, NodeChange } from '@xyflow/react';

import type { ActionDef } from '@/types/action';
import type { CallbackDef } from '@/types/callback';
import type { FlowNode, TaskFlow } from '@/types/flow';
import type { FlowLayout } from '@/types/editor';
import { emptyFlowLayout } from '@/types/editor';
import { dagreLayout } from '../codec/dagreLayout';
import { jsonToFlow } from '../codec/jsonToFlow';
import { flowToJson } from '../codec/flowToJson';
import { validateFlow, type ValidationIssue } from '../validation/refsCheck';

interface FlowState {
  // ── 业务数据（与 flow.json 1:1） ────────
  defaultDelayMs: number;
  nodes: Record<string, FlowNode>;
  actions: Record<string, ActionDef>;
  callbacks: Record<string, CallbackDef>;

  // ── React Flow 视觉状态 ────────
  rfNodes: RFNode[];
  rfEdges: Edge[];
  layout: FlowLayout;

  // ── 派生：callback 引用计数 ────────
  callbackRefCount: Record<string, number>;

  // ── 派生：每个 callback 被哪些 action 节点注册（用于反向悬停高亮） ────────
  nodesByCallback: Record<string, string[]>;

  // ── 派生：节点级校验问题（按节点 ID 分组，仅含 location.kind === 'node' 的 issue） ────────
  issuesByNodeId: Record<string, ValidationIssue[]>;

  // ── 加载 / 替换 ────────
  loadFromTaskFlow: (flow: TaskFlow, layout?: FlowLayout) => void;
  /** 清空，回到空白编辑稿 */
  reset: () => void;

  // ── 导出 ────────
  toTaskFlow: () => TaskFlow;

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
  addCallback: (name: string, def: CallbackDef) => void;
  updateCallback: (name: string, partial: Partial<CallbackDef>) => void;
  /** 完全替换 callback（用于 silent/decl/lua 形态切换：partial merge 会保留旧字段导致 kind 判错） */
  replaceCallback: (name: string, def: CallbackDef) => void;
  removeCallback: (name: string) => void;
  renameCallback: (oldName: string, newName: string) => void;

  /** 触发派生数据重算（rfNodes/rfEdges/callbackRefCount） */
  syncDerived: () => void;
}

export const useFlowStore = create<FlowState>((set, get) => ({
  defaultDelayMs: 1000,
  nodes: {},
  actions: {},
  callbacks: {},
  rfNodes: [],
  rfEdges: [],
  layout: emptyFlowLayout(),
  callbackRefCount: {},
  nodesByCallback: {},
  issuesByNodeId: {},

  loadFromTaskFlow: (flow, layout) => {
    const { rfNodes, rfEdges, callbackRefCount, nodesByCallback } = jsonToFlow(flow);
    let positions: Record<string, { x: number; y: number }>;
    if (layout?.nodePositions && Object.keys(layout.nodePositions).length > 0) {
      positions = Object.fromEntries(
        Object.entries(layout.nodePositions).map(([id, m]) => [id, { x: m.x, y: m.y }]),
      );
    } else {
      positions = dagreLayout(rfNodes, rfEdges, { direction: 'LR' });
      // 给 CallbackCard 一个事件区独立排布（画布右侧竖排）
      const cardX = computeCardX(positions);
      let cardY = 0;
      for (const n of rfNodes) {
        if (n.type === 'callbackCard') {
          positions[n.id] = { x: cardX, y: cardY };
          cardY += 90;
        }
      }
    }
    const positioned = rfNodes.map((n) => ({ ...n, position: positions[n.id] ?? { x: 0, y: 0 } }));
    set({
      defaultDelayMs: flow.defaultDelayMs,
      nodes: flow.nodes,
      actions: flow.actions,
      callbacks: flow.callbacks,
      rfNodes: positioned,
      rfEdges,
      callbackRefCount,
      nodesByCallback,
      layout: layout ?? {
        ...emptyFlowLayout(),
        nodePositions: Object.fromEntries(
          Object.entries(positions).map(([id, p]) => [id, { x: p.x, y: p.y }]),
        ),
      },
    });
  },

  reset: () => {
    set({
      defaultDelayMs: 1000,
      nodes: {},
      actions: {},
      callbacks: {},
      rfNodes: [],
      rfEdges: [],
      layout: emptyFlowLayout(),
      callbackRefCount: {},
      nodesByCallback: {},
      issuesByNodeId: {},
    });
  },

  toTaskFlow: () => {
    const s = get();
    return flowToJson({
      defaultDelayMs: s.defaultDelayMs,
      nodes: s.nodes,
      actions: s.actions,
      callbacks: s.callbacks,
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
        if (n.type === 'callbackCard') {
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
      const nodes = { ...s.nodes };
      delete nodes[id];
      return { nodes };
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
      return { nodes };
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

  addCallback: (name, def) => {
    set((s) => ({ callbacks: { ...s.callbacks, [name]: def } }));
    get().syncDerived();
  },
  updateCallback: (name, partial) => {
    set((s) => {
      const cur = s.callbacks[name];
      if (!cur) return {};
      return { callbacks: { ...s.callbacks, [name]: { ...cur, ...partial } } };
    });
    get().syncDerived();
  },
  replaceCallback: (name, def) => {
    set((s) => {
      if (!s.callbacks[name]) return {};
      return { callbacks: { ...s.callbacks, [name]: def } };
    });
    get().syncDerived();
  },
  removeCallback: (name) => {
    set((s) => {
      const callbacks = { ...s.callbacks };
      delete callbacks[name];
      return { callbacks };
    });
    get().syncDerived();
  },
  renameCallback: (oldName, newName) => {
    if (oldName === newName) return;
    set((s) => {
      if (!(oldName in s.callbacks) || newName in s.callbacks) return {};
      const callbacks: Record<string, CallbackDef> = {};
      for (const [k, v] of Object.entries(s.callbacks)) {
        callbacks[k === oldName ? newName : k] = v;
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
      return { callbacks, nodes };
    });
    get().syncDerived();
  },

  syncDerived: () => {
    const s = get();
    const flow: TaskFlow = {
      defaultDelayMs: s.defaultDelayMs,
      nodes: s.nodes,
      actions: s.actions,
      callbacks: s.callbacks,
    };
    const { rfNodes, rfEdges, callbackRefCount, nodesByCallback } = jsonToFlow(flow);
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
    const report = validateFlow(flow);
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
    set({ rfNodes: positioned, rfEdges, callbackRefCount, nodesByCallback, issuesByNodeId });
  },
}));

/**
 * 计算 CallbackCard 的 X 坐标：取主区最右节点 +距离。
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
