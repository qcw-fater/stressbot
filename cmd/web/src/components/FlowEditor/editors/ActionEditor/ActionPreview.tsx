/**
 * Action 节点消息级预览：递归遍历 proto 消息树，只显示有 binding 的字段。
 */

import { Alert, Collapse, Tabs } from 'antd';
import type { CollapseProps } from 'antd';
import type { ActionDef, FieldBind, StoreMapping } from '@/types/action';
import type { ProtoFieldKind } from '@/types/proto';
import { protoRegistry } from '../../proto/ProtoRegistry';
import { simulateBinding } from './BindingPreview';

// ── 树节点 ──────────────────────────────────────────────────

interface TreeNode {
  fieldName: string;
  protoType: string;
  kind: ProtoFieldKind;
  repeated: boolean;
  display: string;
  displayKind: 'concrete' | 'placeholder' | 'error';
  children?: TreeNode[];
  enumValues?: Record<string, number>;
  mapKey?: string;
  mapValue?: string;
  storeMapping?: string;
}

type SimulateMode =
  | { kind: 'c2s'; bindings: FieldBind[] }
  | { kind: 's2c'; store: StoreMapping[] };

// ── 模拟引擎（只产出有 binding 的字段）──────────────────────

const MAX_DEPTH = 5;

/** 将嵌套路径的 bindings 剥离前缀段，返回子级可用 bindings */
function stripBindings(bindings: FieldBind[], prefix: string): FieldBind[] {
  const pDot = prefix + '.';
  const pBracket = prefix + '[';
  return bindings
    .filter((fb) => fb.field === prefix || fb.field?.startsWith(pDot) || fb.field?.startsWith(pBracket))
    .map((fb) => {
      if (fb.field === prefix) return { ...fb, field: '' };
      const rest = fb.field!.slice(pDot.length);
      return { ...fb, field: rest };
    });
}

/** 匹配 store 映射到字段路径 */
function matchStore(store: StoreMapping[], fieldName: string): string | undefined {
  for (const s of store) {
    if (!s.field || s.field === fieldName) return s.setter;
  }
  return undefined;
}

/** 嵌套 store 剥离前缀 */
function stripStore(store: StoreMapping[], prefix: string): StoreMapping[] {
  const pDot = prefix + '.';
  const pBracket = prefix + '[';
  return store
    .filter((s) => s.field?.startsWith(pDot) || s.field?.startsWith(pBracket))
    .map((s) => ({ ...s, field: s.field!.slice(pDot.length) }));
}

/** 递归构建消息树，只保留有 binding 的字段 */
function simulateMessage(msgName: string, mode: SimulateMode, depth = 0): TreeNode[] | string {
  if (depth > MAX_DEPTH) return [];
  const msg = protoRegistry.lookupMessage(msgName);
  if (!msg) return `Proto 未找到: ${msgName}`;

  const bindings = mode.kind === 'c2s' ? mode.bindings : [];
  const store = mode.kind === 's2c' ? mode.store : [];
  const nodes: TreeNode[] = [];

  for (const field of msg.fields) {
    // 判断此字段（含子级）是否有任何 binding
    const hasDirectBinding = bindings.some((fb) => {
      if (!fb.field) return false;
      const seg = fb.field.split(/[\.\[]/)[0].replace(/\]$/, '');
      return seg === field.name;
    });

    const hasStoreMapping = store.some((s) => {
      if (!s.field || s.field === field.name) return true;
      return s.field.startsWith(field.name + '.') || s.field.startsWith(field.name + '[');
    });

    // 递归检查嵌套 message 是否有子级 binding
    let childNodes: TreeNode[] | string = [];
    let hasChildBinding = false;
    if (field.kind === 'message' && field.messageName) {
      const childBindings = mode.kind === 'c2s' ? stripBindings(bindings, field.name) : [];
      const childStore = mode.kind === 's2c' ? stripStore(store, field.name) : [];
      const childMode: SimulateMode = mode.kind === 'c2s'
        ? { kind: 'c2s', bindings: childBindings }
        : { kind: 's2c', store: childStore };
      const result = simulateMessage(field.messageName, childMode, depth + 1);
      if (typeof result === 'string') {
        childNodes = result;
      } else {
        childNodes = result;
        hasChildBinding = result.length > 0;
      }
    }

    // 没有任何 binding 跳过
    if (!hasDirectBinding && !hasStoreMapping && !hasChildBinding) continue;

    const node: TreeNode = {
      fieldName: field.name,
      protoType: field.type,
      kind: field.kind,
      repeated: field.repeated,
      display: '',
      displayKind: 'concrete',
    };

    // C2S: 匹配 binding
    if (mode.kind === 'c2s' && hasDirectBinding) {
      const matched = bindings.find((fb) => {
        if (!fb.field) return false;
        const seg = fb.field.split(/[\.\[]/)[0].replace(/\]$/, '');
        return seg === field.name;
      });
      if (matched) {
        const result = simulateBinding(matched);
        if (result.kind === 'error') {
          node.display = result.message;
          node.displayKind = 'error';
        } else if (result.kind === 'skipped') {
          node.display = result.reason;
          node.displayKind = 'placeholder';
        } else {
          node.display = (result as { display: string }).display;
          node.displayKind = result.kind as 'concrete' | 'placeholder';
        }
      }
    }

    // S2C: store 映射
    if (mode.kind === 's2c') {
      const storeKey = matchStore(store, field.name);
      if (storeKey) {
        node.storeMapping = storeKey.includes('.') ? `→ state.${storeKey}` : `→ state["${storeKey}"]`;
        if (!node.display) {
          node.display = '（响应字段）';
          node.displayKind = 'placeholder';
        }
      }
    }

    // 嵌套 message 子节点
    if (typeof childNodes === 'string') {
      node.display = childNodes;
      node.displayKind = 'error';
    } else if (childNodes.length > 0) {
      node.children = childNodes;
    }

    // enum 值
    if (field.kind === 'enum' && field.enumName) {
      const e = protoRegistry.lookupEnum(field.enumName);
      if (e) node.enumValues = e.values;
    }

    // map 提示
    if (field.kind === 'map') {
      node.mapKey = field.mapKey;
      node.mapValue = field.mapValue;
    }

    nodes.push(node);
  }

  return nodes;
}

// ── 树渲染 ──────────────────────────────────────────────────

const COLOR_MAP: Record<string, string> = {
  concrete: 'var(--color-success)',
  placeholder: 'var(--text-tertiary)',
  error: 'var(--color-error)',
};

function TreeNodeRow({ node, depth }: { node: TreeNode; depth: number }) {
  const indent = depth * 20;
  const hasChildren = node.children && node.children.length > 0;

  if (hasChildren) {
    const items: CollapseProps['items'] = [
      {
        key: node.fieldName,
        label: (
          <span style={{ fontFamily: 'monospace', fontSize: 12 }}>
            <code>{node.fieldName}</code>
            {node.repeated && <span style={{ color: 'var(--text-tertiary)' }}>[]</span>}
            <span style={{ color: 'var(--text-quaternary)', fontSize: 11, marginLeft: 6 }}>
              {node.protoType}
            </span>
            {node.display && (
              <span style={{ color: COLOR_MAP[node.displayKind], marginLeft: 8 }}>
                {node.display}
              </span>
            )}
            {node.storeMapping && (
              <span style={{ color: 'var(--color-link)', marginLeft: 8, fontSize: 11 }}>
                {node.storeMapping}
              </span>
            )}
          </span>
        ),
        children: <MessageTreeView nodes={node.children!} depth={depth + 1} />,
      },
    ];
    return (
      <div style={{ paddingLeft: indent }}>
        <Collapse
          items={items}
          size="small"
          defaultActiveKey={[node.fieldName]}
          style={{ marginBottom: 2 }}
        />
      </div>
    );
  }

  return (
    <div style={{ paddingLeft: indent, lineHeight: 1.8, fontFamily: 'monospace', fontSize: 12 }}>
      <span>
        <code>{node.fieldName}</code>
        {node.repeated && <span style={{ color: 'var(--text-tertiary)' }}>[]</span>}
      </span>
      <span style={{ color: 'var(--text-quaternary)', fontSize: 11, marginLeft: 6 }}>
        {node.kind === 'map' ? `map<${node.mapKey}, ${node.mapValue}>` : node.protoType}
      </span>
      <span style={{ color: COLOR_MAP[node.displayKind], marginLeft: 8 }}>
        {node.display}
      </span>
      {node.enumValues && (
        <span style={{ color: 'var(--text-quaternary)', fontSize: 11, marginLeft: 4 }}>
          ({Object.keys(node.enumValues).slice(0, 5).join(', ')}
          {Object.keys(node.enumValues).length > 5 ? '...' : ''})
        </span>
      )}
      {node.storeMapping && (
        <span style={{ color: 'var(--color-link)', marginLeft: 8, fontSize: 11 }}>
          {node.storeMapping}
        </span>
      )}
    </div>
  );
}

function MessageTreeView({ nodes, depth }: { nodes: TreeNode[]; depth: number }) {
  if (!nodes.length) return <div style={{ color: 'var(--text-tertiary)', fontSize: 12 }}>（无绑定字段）</div>;
  return (
    <div>
      {nodes.map((n, i) => (
        <TreeNodeRow key={n.fieldName + i} node={n} depth={depth} />
      ))}
    </div>
  );
}

// ── setState / httpRequest 简化预览 ─────────────────────────

function FlatBindingsPreview({ bindings }: { bindings: FieldBind[] }) {
  if (!bindings.length) return <Alert type="info" message="无 bindings" style={{ margin: '8px 0' }} />;
  return (
    <div style={{ fontFamily: 'monospace', fontSize: 12, lineHeight: 1.8 }}>
      {bindings.map((fb, i) => {
        const result = simulateBinding(fb);
        const display = result.kind === 'error' ? result.message : result.kind === 'skipped' ? result.reason : (result as { display: string }).display;
        const color = result.kind === 'error' ? 'var(--color-error)' : result.kind === 'concrete' ? 'var(--color-success)' : 'var(--text-tertiary)';
        return (
          <div key={i}>
            <code>{fb.field || '(未指定)'}</code>
            <span style={{ color: 'var(--text-quaternary)', marginLeft: 6 }}>{fb.type}</span>
            <span style={{ color, marginLeft: 8 }}>{display}</span>
          </div>
        );
      })}
    </div>
  );
}

// ── 主组件 ──────────────────────────────────────────────────

interface ActionPreviewProps {
  action: ActionDef;
}

const NO_PROTO_PATTERNS = ['setState', 'httpRequest'];

export function ActionPreview({ action }: ActionPreviewProps) {
  const { pattern } = action;
  const noProto = NO_PROTO_PATTERNS.includes(pattern);
  const hasC2S = noProto || !!action.c2sProto;
  const hasS2C = !!action.s2cProto;

  if (!hasC2S && !hasS2C) {
    return <Alert type="info" message="当前 pattern 无可预览内容" />;
  }

  const c2sTab = hasC2S
    ? {
        key: 'c2s',
        label: noProto ? '请求字段' : `发送 (${action.c2sProto?.split('.').pop() ?? '?'})`,
        children: noProto ? (
          <FlatBindingsPreview bindings={action.bindings ?? []} />
        ) : action.c2sProto ? (
          <C2SPane msgName={action.c2sProto} bindings={action.bindings ?? []} />
        ) : (
          <Alert type="warning" message="未配置 C2S 消息" />
        ),
      }
    : undefined;

  const s2cTab = hasS2C
    ? {
        key: 's2c',
        label: `响应 (${action.s2cProto?.split('.').pop() ?? '?'})`,
        children: action.s2cProto ? (
          <S2CPane msgName={action.s2cProto} store={action.store ?? []} />
        ) : (
          <Alert type="warning" message="未配置 S2C 消息" />
        ),
      }
    : undefined;

  const items = [c2sTab, s2cTab].filter(Boolean) as NonNullable<typeof c2sTab>[];

  return (
    <Tabs
      defaultActiveKey={hasC2S ? 'c2s' : 's2c'}
      items={items}
      style={{ maxHeight: '60vh', overflowY: 'auto' }}
    />
  );
}

function C2SPane({ msgName, bindings }: { msgName: string; bindings: FieldBind[] }) {
  const result = simulateMessage(msgName, { kind: 'c2s', bindings });
  if (typeof result === 'string') return <Alert type="error" message={result} />;
  return <MessageTreeView nodes={result} depth={0} />;
}

function S2CPane({ msgName, store }: { msgName: string; store: StoreMapping[] }) {
  const result = simulateMessage(msgName, { kind: 's2c', store });
  if (typeof result === 'string') return <Alert type="error" message={result} />;
  return <MessageTreeView nodes={result} depth={0} />;
}
