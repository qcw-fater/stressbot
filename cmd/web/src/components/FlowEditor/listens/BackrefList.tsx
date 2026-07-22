/**
 * BackrefList：列出引用某 listen 的所有 (action 节点, server, route)。
 *
 * 与 ListenRefsTable 不同：BackrefList 站在 listen 视角，
 * 列出“哪些 action 节点的哪条 ListenRef 引用了我”，并允许就地修改连接、route 与队列容量。
 * 这样用户在 listen 节点（ListenCard）上直接编辑监听注册，无需回到 action 节点。
 */

import { Button, InputNumber, Space, Table, Tooltip, Typography } from 'antd';
import { DeleteOutlined, EditOutlined } from '@ant-design/icons';
import { useMemo, useState } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useFlowStore } from '../store/flowStore';
import { useEditorStore } from '../store/editorStore';
import { buildRefsGraph } from './refsGraph';
import { monoCellStyle } from '../styles/inlineStyles';
import { TargetConnectionSelect } from '../codec/TargetConnectionRouteEditor';
import { useCodecConnections, useCodecRouteSpecs } from '../codec/useCodecConnections';
import { RouteFieldTrack } from './RouteFieldTrack';
import { RouteFloatingEditor } from './RouteFloatingEditor';
import type { ListenRef } from '@/types/flow';

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
  const { connections, loading: connectionsLoading, error: connectionsError } = useCodecConnections();
  const { specs, loading: routeSpecsLoading, error: routeSpecsError } = useCodecRouteSpecs();
  const [routeEditorTarget, setRouteEditorTarget] = useState<{
    nodeId: string;
    refIndex: number;
  } | null>(null);

  const graph = useMemo(() => buildRefsGraph(flow), [flow]);
  const refs = graph.listenToRefs.get(listenName) ?? [];
  const routeEditorRef = routeEditorTarget
    ? flow.nodes[routeEditorTarget.nodeId]?.listenRefs?.[routeEditorTarget.refIndex]
    : undefined;

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
    patch: Partial<Pick<ListenRef, 'server' | 'route' | 'queueSize'>>,
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
    setRouteEditorTarget((target) => {
      if (!target || target.nodeId !== nodeId) return target;
      if (target.refIndex === refIndex) return null;
      return target.refIndex > refIndex
        ? { ...target, refIndex: target.refIndex - 1 }
        : target;
    });
    const list = (node.listenRefs ?? []).filter((_, i) => i !== refIndex);
    updateNode(nodeId, { listenRefs: list });
  };

  return (
    <div>
      <Typography.Text strong>
        监听注册（{refs.length} 处引用）
      </Typography.Text>
      <Table
        size="small"
        style={{ marginTop: 8 }}
        dataSource={refs.map((ref) => ({ ...ref, key: `${ref.nodeId}:${ref.refIndex}` }))}
        rowKey="key"
        pagination={false}
        tableLayout="fixed"
        scroll={{ x: 600 }}
        columns={[
          {
            title: '动作节点',
            dataIndex: 'nodeId',
            width: 150,
            render: (_, r) => (
              <Space size={6}>
                <a
                  onClick={() => {
                    setSelectedNode(r.nodeId);
                    setActivePanel({ kind: 'nodeEdit', nodeId: r.nodeId });
                  }}
                  style={monoCellStyle}
                >
                  {r.nodeId}
                </a>
                <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                  #{r.refIndex + 1}
                </Typography.Text>
              </Space>
            ),
          },
        {
          title: '目标连接',
          dataIndex: ['ref', 'server'],
          width: 120,
          render: (_, r) => (
            <TargetConnectionSelect
              inline
              size="small"
              server={r.ref.server}
              onChangeServer={(server) => updateRefField(r.nodeId, r.refIndex, { server: server ?? '' })}
              connections={connections}
              loading={connectionsLoading}
              error={connectionsError}
            />
          ),
        },
        {
          title: 'route',
          dataIndex: ['ref', 'route'],
          width: 150,
          render: (_, r) => (
            <RouteFieldTrack
              server={r.ref.server}
              value={r.ref.route}
              routeKeyTemplate={r.ref.server ? specs.get(r.ref.server)?.routeKeyTemplate : undefined}
              loading={routeSpecsLoading}
              error={routeSpecsError}
              onChange={(route) => updateRefField(r.nodeId, r.refIndex, { route })}
              onOpenFloating={() => setRouteEditorTarget({
                nodeId: r.nodeId,
                refIndex: r.refIndex,
              })}
            />
          ),
        },
        {
          title: '队列容量',
          dataIndex: ['ref', 'queueSize'],
          width: 90,
          render: (_, r) => (
            <Tooltip title="监听缓存队列容量，缺省 1；<=0 会在校验报错">
              <InputNumber
                aria-label="队列容量"
                size="small"
                min={1}
                placeholder="缺省 1"
                value={r.ref.queueSize}
                onChange={(value) => updateRefField(r.nodeId, r.refIndex, {
                  queueSize: (value as number) ?? undefined,
                })}
                style={{ width: 78 }}
              />
            </Tooltip>
          ),
        },
        {
          title: '操作',
          width: 90,
          render: (_, r) => (
            <Space size={4}>
              <Tooltip title="打开动作节点">
                <Button
                  size="small"
                  type="text"
                  icon={<EditOutlined />}
                  onClick={() => {
                    setSelectedNode(r.nodeId);
                    setActivePanel({ kind: 'nodeEdit', nodeId: r.nodeId });
                  }}
                />
              </Tooltip>
              <Tooltip title="删除监听注册">
                <Button
                  size="small"
                  danger
                  type="text"
                  icon={<DeleteOutlined />}
                  onClick={() => removeRef(r.nodeId, r.refIndex)}
                />
              </Tooltip>
            </Space>
          ),
        },
        ]}
      />
      <RouteFloatingEditor
        windowId={`listen-backref-route:${listenName}`}
        open={Boolean(routeEditorRef)}
        server={routeEditorRef?.server}
        value={routeEditorRef?.route}
        routeKeyTemplate={routeEditorRef?.server ? specs.get(routeEditorRef.server)?.routeKeyTemplate : undefined}
        loading={routeSpecsLoading}
        error={routeSpecsError}
        onChange={(route) => {
          if (routeEditorTarget) {
            updateRefField(routeEditorTarget.nodeId, routeEditorTarget.refIndex, { route });
          }
        }}
        onClose={() => setRouteEditorTarget(null)}
      />
    </div>
  );
}
