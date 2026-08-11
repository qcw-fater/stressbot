/**
 * Zustand 核心 store：维护 TaskFlow 业务数据 + React Flow 视觉状态。
 *
 * 设计文档 §10.1：业务数据（nodes/actions/listens）与视觉状态（rfNodes/rfEdges/positions）
 * 同 store 中维护，但导出 flow.json 时只取业务部分。
 */

import { create } from 'zustand';
import type { StateCreator } from 'zustand';
import { temporal } from 'zundo';
import { applyEdgeChanges, applyNodeChanges } from '@xyflow/react';
import type { Edge, EdgeChange, Node as RFNode, NodeChange } from '@xyflow/react';

import type { ActionDef } from '@/types/action';
import type { ListenDef } from '@/types/listen';
import type { FlowNode } from '@/types/flow';
import type { FlowLayout } from '@/types/editor';
import { emptyFlowLayout } from '@/types/editor';
import type { ListenTemplateDefaultRef } from '../library/templateStore';
import { cloneListenDefaultRef } from '../library/listenTemplateDefaults';
import { dagreLayout } from '../codec/dagreLayout';
import { jsonToFlow } from '../codec/jsonToFlow';
import { flowToJson, type FlowJson } from '../codec/flowToJson';
import { normalizeOnError } from '../utils/onError';

export interface FlowState {
  // ── 业务数据（与 flow.json 1:1） ────────
  defaultDelayMs: number;
  nodes: Record<string, FlowNode>;
  actions: Record<string, ActionDef>;
  listens: Record<string, ListenDef>;

  // ── React Flow 视觉状态 ────────
  rfNodes: RFNode[];
  rfEdges: Edge[];
  layout: FlowLayout;

  // ── UI-only：模板带入的默认监听注册信息，不导出到 flow.json ────────
  listenDefaultRefs: Record<string, ListenTemplateDefaultRef | undefined>;

  // ── 派生：listen 引用计数 ────────
  listenRefCount: Record<string, number>;

  // ── 派生：每个 listen 被哪些 action 节点注册（用于反向悬停高亮） ────────
  nodesByListen: Record<string, string[]>;

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
  /** 提交一个或多个节点的最终位置，并同步画布与持久化布局。 */
  setNodePositions: (positions: Record<string, { x: number; y: number }>) => void;

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

  // ── 监听 CRUD ────────
  addListen: (name: string, def: ListenDef, position?: { x: number; y: number }) => void;
  updateListen: (name: string, partial: Partial<ListenDef>) => void;
  /** 完全替换 listen（用于 silent/decl/lua 形态切换：partial merge 会保留旧字段导致 kind 判错） */
  replaceListen: (name: string, def: ListenDef) => void;
  removeListen: (name: string) => void;
  renameListen: (oldName: string, newName: string) => void;
  setListenDefaultRef: (name: string, ref?: ListenTemplateDefaultRef) => void;

  /** 触发画布派生数据重算（rfNodes/rfEdges/listenRefCount/nodesByListen） */
  syncDerived: () => void;
}

export type FlowHistoryState = Pick<FlowState, 'defaultDelayMs' | 'nodes' | 'actions' | 'listens'>;

const flowStateCreator: StateCreator<FlowState, [['temporal', unknown]], []> = (set, get) => ({
  defaultDelayMs: 1000,
  nodes: {},
  actions: {},
  listens: {},
  rfNodes: [],
  rfEdges: [],
  layout: emptyFlowLayout(),
  listenDefaultRefs: {},
  listenRefCount: {},
  nodesByListen: {},
  needsFitView: false,

  loadFromTaskFlow: (flow, layout) => {
    resetFlowHistory(() => {
      const { rfNodes, rfEdges, listenRefCount, nodesByListen } = jsonToFlow({
        defaultDelayMs: flow.defaultDelayMs,
        nodes: flow.nodes,
        actions: flow.actions,
        listens: flow.listens,
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
      const positioned = rfNodes.map((n) => ({
        ...n,
        position: positions[n.id] ?? { x: 0, y: 0 },
      }));
      set({
        defaultDelayMs: flow.defaultDelayMs,
        nodes: flow.nodes,
        actions: flow.actions,
        listens: flow.listens,
        listenDefaultRefs: {},
        rfNodes: positioned,
        rfEdges,
        listenRefCount,
        nodesByListen,
        needsFitView: true,
        layout: layout ?? {
          ...emptyFlowLayout(),
          nodePositions: Object.fromEntries(
            Object.entries(positions).map(([id, p]) => [id, { x: p.x, y: p.y }]),
          ),
        },
      });
    });
  },

  reset: (center?: { x: number; y: number }) => {
    resetFlowHistory(() => {
      const pos = center ?? { x: 300, y: 300 };
      set({
        defaultDelayMs: 1000,
        nodes: { main: { type: 'sequence', next: [], description: '入口节点' } },
        actions: {},
        listens: {},
        listenDefaultRefs: {},
        rfNodes: [],
        rfEdges: [],
        layout: {
          ...emptyFlowLayout(),
          nodePositions: { main: pos },
        },
        listenRefCount: {},
        nodesByListen: {},
        needsFitView: true,
      });
      get().syncDerived();
    });
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
    set((s) => ({ rfNodes: applyNodeChanges(changes, s.rfNodes) }));
  },

  onEdgesChange: (changes) => {
    set((s) => ({ rfEdges: applyEdgeChanges(changes, s.rfEdges) }));
  },

  setNodePositions: (positions) => {
    const ids = new Set(Object.keys(positions));
    if (ids.size === 0) return;
    set((s) => ({
      layout: {
        ...s.layout,
        nodePositions: {
          ...s.layout.nodePositions,
          ...Object.fromEntries(
            Object.entries(positions).map(([id, position]) => [id, { ...position }]),
          ),
        },
      },
      rfNodes: s.rfNodes.map((node) => {
        const position = positions[node.id];
        return position ? { ...node, position: { ...position } } : node;
      }),
    }));
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
      const positioned = s.rfNodes.map((n) => ({
        ...n,
        position: positions[n.id] ?? n.position,
      }));
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
    const affectsTopology = nodePatchAffectsTopology(partial);
    set((s) => {
      const cur = s.nodes[id];
      if (!cur) return {};
      const node = { ...cur, ...partial };
      const nodes = { ...s.nodes, [id]: node };
      if (affectsTopology) return { nodes };
      return {
        nodes,
        rfNodes: patchFlowNode(s.rfNodes, id, node, s.actions),
      };
    });
    if (affectsTopology) get().syncDerived();
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
      // 清理其他节点中对被删节点的引用（next/body/trueNext/falseNext/options/onError.handler/cases/defaultNext/then）
      for (const [nid, n] of Object.entries(nodes)) {
        const partial: Partial<FlowNode> = {};
        if (n.next?.includes(id)) partial.next = n.next.filter((x) => x !== id);
        if (n.body === id) partial.body = '';
        if (n.trueNext === id) partial.trueNext = '';
        if (n.falseNext === id) partial.falseNext = '';
        if (n.options?.some((o) => o.node === id)) {
          partial.options = n.options.filter((o) => o.node !== id);
        }
        if (n.type === 'action' && n.onError?.handler === id) {
          partial.onError = normalizeOnError({ ...n.onError, handler: undefined });
        }
        if (n.cases?.some((c) => c.next === id)) {
          partial.cases = n.cases.filter((c) => c.next !== id);
        }
        if (n.defaultNext === id) partial.defaultNext = '';
        if (n.then === id) partial.then = '';
        if (Object.keys(partial).length > 0) {
          nodes[nid] = { ...n, ...partial };
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
      const oldNode = s.nodes[oldId];
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
      // action 节点：重命名时同步 action key
      let actions = s.actions;
      if (
        oldNode.type === 'action' &&
        oldNode.action &&
        s.actions[oldNode.action] &&
        !(newId in s.actions)
      ) {
        const oldAct = oldNode.action;
        actions = {};
        for (const [k, v] of Object.entries(s.actions)) {
          actions[k === oldAct ? newId : k] = v;
        }
        nodes[newId] = { ...nodes[newId], action: newId };
      }
      return { nodes, actions, layout };
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
      const action = { ...cur, ...partial };
      return {
        actions: { ...s.actions, [name]: action },
        rfNodes: patchActionNodes(s.rfNodes, s.nodes, name, action),
      };
    });
  },
  replaceAction: (name, def) => {
    set((s) => {
      if (!s.actions[name]) return {};
      return {
        actions: { ...s.actions, [name]: def },
        rfNodes: patchActionNodes(s.rfNodes, s.nodes, name, def),
      };
    });
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

  addListen: (name, def, position) => {
    set((s) => {
      const listens = { ...s.listens, [name]: def };
      if (position) {
        const nodePositions = {
          ...s.layout.nodePositions,
          [`__cb__${name}`]: { x: position.x, y: position.y },
        };
        return { listens, layout: { ...s.layout, nodePositions } };
      }
      return { listens };
    });
    get().syncDerived();
  },
  updateListen: (name, partial) => {
    set((s) => {
      const cur = s.listens[name];
      if (!cur) return {};
      const listen = { ...cur, ...partial };
      return {
        listens: { ...s.listens, [name]: listen },
        rfNodes: patchListenCard(s.rfNodes, name, listen),
      };
    });
  },
  replaceListen: (name, def) => {
    set((s) => {
      if (!s.listens[name]) return {};
      return {
        listens: { ...s.listens, [name]: def },
        rfNodes: patchListenCard(s.rfNodes, name, def),
      };
    });
  },
  removeListen: (name) => {
    set((s) => {
      if (!(name in s.listens)) return {};
      const listens = { ...s.listens };
      const listenDefaultRefs = { ...s.listenDefaultRefs };
      delete listens[name];
      delete listenDefaultRefs[name];
      // 级联清理：所有 action 节点的 listenRefs 中指向该 listen 的注册必须一并移除，
      // 否则派生边会隐藏不存在的 listen（jsonToFlow 跳过），画布看似干净却留下悬空引用，
      // 导出/校验/启动会报引用不存在。
      const nodes: Record<string, FlowNode> = {};
      let touched = false;
      for (const [k, v] of Object.entries(s.nodes)) {
        if (v.type === 'action' && v.listenRefs?.length) {
          const filtered = v.listenRefs.filter((r) => r.listen !== name);
          if (filtered.length !== v.listenRefs.length) {
            touched = true;
            nodes[k] = { ...v, listenRefs: filtered };
            continue;
          }
        }
        nodes[k] = v;
      }
      // 清理 listenCard 位置
      const cbId = `__cb__${name}`;
      const nodePositions = { ...s.layout.nodePositions };
      delete nodePositions[cbId];
      const layout = { ...s.layout, nodePositions };
      return touched
        ? { listens, nodes, listenDefaultRefs, layout }
        : { listens, listenDefaultRefs, layout };
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
      // 同步所有 listenRefs 中的引用
      const nodes: Record<string, FlowNode> = {};
      for (const [k, v] of Object.entries(s.nodes)) {
        if (v.type === 'action' && v.listenRefs?.length) {
          nodes[k] = {
            ...v,
            listenRefs: v.listenRefs.map((r) =>
              r.listen === oldName ? { ...r, listen: newName } : r,
            ),
          };
        } else {
          nodes[k] = v;
        }
      }
      let listenDefaultRefs = s.listenDefaultRefs;
      if (oldName in s.listenDefaultRefs) {
        listenDefaultRefs = { ...s.listenDefaultRefs };
        listenDefaultRefs[newName] = cloneListenDefaultRef(listenDefaultRefs[oldName]);
        delete listenDefaultRefs[oldName];
      }
      // 迁移 listenCard 位置：旧 ID → 新 ID
      const oldCbId = `__cb__${oldName}`;
      const newCbId = `__cb__${newName}`;
      const nodePositions = { ...s.layout.nodePositions };
      if (oldCbId in nodePositions) {
        nodePositions[newCbId] = nodePositions[oldCbId];
        delete nodePositions[oldCbId];
      }
      const layout = { ...s.layout, nodePositions };
      return { listens, nodes, listenDefaultRefs, layout };
    });
    get().syncDerived();
  },

  setListenDefaultRef: (name, ref) => {
    set((s) => {
      const listenDefaultRefs = { ...s.listenDefaultRefs };
      const next = cloneListenDefaultRef(ref);
      if (next) listenDefaultRefs[name] = next;
      else delete listenDefaultRefs[name];
      return { listenDefaultRefs };
    });
  },

  syncDerived: () => {
    const s = get();
    const { rfNodes, rfEdges, listenRefCount, nodesByListen } = jsonToFlow({
      defaultDelayMs: s.defaultDelayMs,
      nodes: s.nodes,
      actions: s.actions,
      listens: s.listens,
    });
    // 保留已有位置（避免拖动后被覆盖）
    const positions = s.layout.nodePositions;
    // 计算事件区基准位置：现有 listenCard 的最大 Y + 间距，X 取 listenCard 的 X
    let cardBaseX = 0;
    let cardBaseY = 0;
    for (const n of s.rfNodes) {
      if (n.type === 'listenCard') {
        cardBaseX = Math.max(cardBaseX, n.position.x);
        cardBaseY = Math.max(cardBaseY, n.position.y);
      }
    }
    if (cardBaseX > 0) cardBaseY += 90; // 新节点排在现有最后一张下方
    const existingById = new Map(s.rfNodes.map((node) => [node.id, node]));
    const positioned = rfNodes.map((n) => {
      const pos = positions[n.id];
      const existing = existingById.get(n.id);
      let position: { x: number; y: number };
      if (pos) {
        position = { x: pos.x, y: pos.y };
      } else if (existing) {
        position = existing.position;
      } else if (n.type === 'listenCard') {
        // 新 listenCard：放到事件区末尾
        position = { x: cardBaseX, y: cardBaseY };
        cardBaseY += 90;
      } else {
        position = { x: 0, y: 0 };
      }
      return { ...n, position };
    });
    set({ rfNodes: positioned, rfEdges, listenRefCount, nodesByListen });
  },
});

export const useFlowStore = create<FlowState>()(
  temporal(flowStateCreator, {
    limit: 50,
    partialize: (state): FlowHistoryState => ({
      defaultDelayMs: state.defaultDelayMs,
      nodes: state.nodes,
      actions: state.actions,
      listens: state.listens,
    }),
    equality: (past, current) =>
      past.defaultDelayMs === current.defaultDelayMs &&
      past.nodes === current.nodes &&
      past.actions === current.actions &&
      past.listens === current.listens,
  }),
);

function resetFlowHistory(apply: () => void): void {
  const history = useFlowStore.temporal.getState();
  history.pause();
  try {
    apply();
  } finally {
    history.clear();
    history.resume();
  }
}

const TOPOLOGY_FIELDS = new Set<keyof FlowNode>([
  'type',
  'next',
  'body',
  'trueNext',
  'falseNext',
  'cases',
  'defaultNext',
  'options',
  'listenRefs',
  'onError',
  'then',
]);

function nodePatchAffectsTopology(partial: Partial<FlowNode>): boolean {
  return Object.keys(partial).some((key) => TOPOLOGY_FIELDS.has(key as keyof FlowNode));
}

function patchFlowNode(
  rfNodes: RFNode[],
  id: string,
  node: FlowNode,
  actions: Record<string, ActionDef>,
): RFNode[] {
  return rfNodes.map((rfNode) => {
    if (rfNode.id !== id) return rfNode;
    return {
      ...rfNode,
      type: node.type,
      data: {
        ...rfNode.data,
        nodeId: id,
        node,
        action: node.action ? actions[node.action] : undefined,
      },
    };
  });
}

function patchActionNodes(
  rfNodes: RFNode[],
  nodes: Record<string, FlowNode>,
  actionName: string,
  action: ActionDef,
): RFNode[] {
  const affected = new Set(
    Object.entries(nodes)
      .filter(([, node]) => node.type === 'action' && node.action === actionName)
      .map(([id]) => id),
  );
  if (affected.size === 0) return rfNodes;
  return rfNodes.map((rfNode) =>
    affected.has(rfNode.id) ? { ...rfNode, data: { ...rfNode.data, action } } : rfNode,
  );
}

function patchListenCard(rfNodes: RFNode[], name: string, listen: ListenDef): RFNode[] {
  const id = `__cb__${name}`;
  return rfNodes.map((rfNode) =>
    rfNode.id === id ? { ...rfNode, data: { ...rfNode.data, listen } } : rfNode,
  );
}

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

/** 节点重命名时，同步更新所有指向它的引用。 */
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
  if (node.type === 'action' && node.onError?.handler === oldId) {
    out.onError = { ...node.onError, handler: newId };
  }
  if (node.cases?.length) {
    out.cases = node.cases.map((c) => (c.next === oldId ? { ...c, next: newId } : c));
  }
  if (node.defaultNext === oldId) out.defaultNext = newId;
  if (node.then === oldId) out.then = newId;
  return out;
}
