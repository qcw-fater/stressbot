/**
 * BackrefList：列出引用某 listen 的所有 (action 节点, server, route)。
 *
 * 与 ListenRefsTable 不同：BackrefList 站在 listen 视角，
 * 列出"哪些 action 节点的哪条 ListenRef 引用了我"，并允许就地修改它的 server / route。
 * 这样用户在 listen 节点（ListenCard）上直接编辑监听路由，无需回到 action 节点。
 */

import { Button, Input, List, Space, Tooltip, Typography } from 'antd';
import { DeleteOutlined } from '@ant-design/icons';
import { useMemo } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useFlowStore } from '../store/flowStore';
import { useEditorStore } from '../store/editorStore';
import { buildRefsGraph } from './refsGraph';
import { RouteEditor } from './RouteEditor';
import { monoCellStyle } from '../styles/inlineStyles';

export interface BackrefListProps {
  listenName: string;
}

export function BackrefList({ listenName }: BackrefListProps) {
  const flow = useFlowStore(
    useShallow((s) => ({
      nodes: s.nodes,
      actions: s.actions,
      listens: s.listens,
      defaultDelayMs: s.defaultDelayMs,
    })),
  );
  const updateNode = useFlowStore((s) => s.updateNode);
  const setSelectedNode = useEditorStore((s) => s.setSelectedNode);
  const setActivePanel = useEditorStore((s) => s.setActivePanel);

  const graph = useMemo(() => buildRefsGraph(flow), [flow]);
  const refs = graph.listenToRefs.get(listenName) ?? [];

  if (refs.length === 0) {
    return (
      <div style={{ color: 'var(--text-tertiary)', fontSize: 12 }}>
        当前 listen 未被任何 action 注册（孤儿）。
      </div>
    );
  }

  const updateRefField = (
    nodeId: string,
    refIndex: number,
    patch: Partial<{ server: string; route: unknown }>,
  ) => {
    const node = flow.nodes[nodeId];
    if (!node) return;
    const list = [...(node.listenRefs ?? [])];
    if (refIndex < 0 || refIndex >= list.length) return;
    list[refIndex] = { ...list[refIndex], ...patch };
    updateNode(nodeId, { listenRefs: list });
  };

  const removeRef = (nodeId: string, refIndex: number) => {
    const node = flow.nodes[nodeId];
    if (!node) return;
    const list = (node.listenRefs ?? []).filter((_, i) => i !== refIndex);
    updateNode(nodeId, { listenRefs: list });
  };

  return (
    <List
      size="small"
      header={
        <Typography.Text strong>
          监听路由（{refs.length} 处引用）
        </Typography.Text>
      }
      dataSource={refs}
      renderItem={(r) => (
        <List.Item style={{ display: 'block', padding: '8px 0' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
            <a
              onClick={() => {
                setSelectedNode(r.nodeId);
                setActivePanel({ kind: 'nodeEdit', nodeId: r.nodeId });
              }}
              style={monoCellStyle}
            >
              {r.nodeId}
            </a>
            <span style={{ color: 'var(--text-tertiary)', fontSize: 11 }}>
              第 {r.refIndex + 1} 条
            </span>
            <div style={{ flex: 1 }} />
            <Tooltip title="从该 action 的 listenRefs 中删除这条监听">
              <Button
                size="small"
                danger
                type="text"
                icon={<DeleteOutlined />}
                onClick={() => removeRef(r.nodeId, r.refIndex)}
              />
            </Tooltip>
          </div>
          <Space.Compact style={{ width: '100%', marginBottom: 4 }}>
            <span style={{ display: 'flex', alignItems: 'center', padding: '0 11px', background: 'var(--container-bg)', border: '1px solid var(--border-color)', borderRadius: '6px 0 0 6px', fontSize: 12, whiteSpace: 'nowrap' }}>server</span>
            <Input
              size="small"
              value={r.ref.server}
              onChange={(e) => updateRefField(r.nodeId, r.refIndex, { server: e.target.value })}
              placeholder="如 tcp:logic"
              style={{ ...monoCellStyle, flex: 1 }}
            />
          </Space.Compact>
          <Space.Compact style={{ width: '100%' }}>
            <span
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                padding: '0 11px',
                background: 'var(--bg-panel, rgba(0,0,0,0.02))',
                border: '1px solid var(--border-color, rgba(0,0,0,0.15))',
                borderRight: 'none',
                borderRadius: '6px 0 0 6px',
                color: 'var(--text-tertiary)',
                fontSize: 12,
              }}
            >
              route
            </span>
            <RouteEditor
              size="small"
              value={r.ref.route}
              onChange={(v) => updateRefField(r.nodeId, r.refIndex, { route: v })}
            />
          </Space.Compact>
        </List.Item>
      )}
    />
  );
}
