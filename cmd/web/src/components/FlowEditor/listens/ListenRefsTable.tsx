/**
 * action.listenRefs 表格编辑器。
 *
 * 设计文档 §8.6：route + server + listen 三列 + 形态徽章 + 排序删除 + 批量入口。
 */

import { App as AntApp, Button, Input, InputNumber, Modal, Select, Space, Table, Tag, Tooltip } from 'antd';
import { ArrowDownOutlined, ArrowUpOutlined, DeleteOutlined, EditOutlined, PlusOutlined } from '@ant-design/icons';
import { useState } from 'react';
import type { ListenRef } from '@/types/flow';
import { classifyListen } from '@/types/listen';
import { useFlowStore } from '../store/flowStore';
import { useEditorStore } from '../store/editorStore';
import { listenKindTagColor } from './listenKindStyle';
import { useFloatingWindowStore } from '../store/floatingWindowStore';
import { TargetConnectionSelect } from '../codec/TargetConnectionRouteEditor';
import { useCodecConnections, useCodecRouteSpecs } from '../codec/useCodecConnections';
import { RouteFieldTrack } from './RouteFieldTrack';
import { RouteFloatingEditor } from './RouteFloatingEditor';

export interface ListenRefsTableProps {
  nodeId: string;
}

export function ListenRefsTable({ nodeId }: ListenRefsTableProps) {
  const { message } = AntApp.useApp();
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;
  const node = useFlowStore((s) => s.nodes[nodeId]);
  const listens = useFlowStore((s) => s.listens);
  const updateNode = useFlowStore((s) => s.updateNode);
  const addListen = useFlowStore((s) => s.addListen);
  const rfNodePos = useFlowStore((s) => s.rfNodes.find((n) => n.id === nodeId)?.position);
  const setActivePanel = useEditorStore((s) => s.setActivePanel);
  const { connections, loading: connectionsLoading, error: connectionsError } = useCodecConnections();
  const { specs, loading: routeSpecsLoading, error: routeSpecsError } = useCodecRouteSpecs();

  const [pasteOpen, setPasteOpen] = useState(false);
  const [pasteText, setPasteText] = useState('');
  const [routeEditorIndex, setRouteEditorIndex] = useState<number | null>(null);

  if (!node) return null;
  const refs = node.listenRefs ?? [];
  const set = (next: ListenRef[]) => updateNode(nodeId, { listenRefs: next });
  const routeEditorRef = routeEditorIndex === null ? undefined : refs[routeEditorIndex];

  const updateRef = (index: number, patch: Partial<ListenRef>) => {
    const arr = [...refs];
    if (!arr[index]) return;
    arr[index] = { ...arr[index], ...patch };
    set(arr);
  };

  const moveRef = (from: number, to: number) => {
    if (!refs[to]) return;
    const arr = [...refs];
    [arr[from], arr[to]] = [arr[to], arr[from]];
    setRouteEditorIndex((current) => {
      if (current === from) return to;
      if (current === to) return from;
      return current;
    });
    set(arr);
  };

  const removeRef = (index: number) => {
    setRouteEditorIndex((current) => {
      if (current === null) return null;
      if (current === index) return null;
      return current > index ? current - 1 : current;
    });
    set(refs.filter((_, refIndex) => refIndex !== index));
  };

  // listen 候选下拉：现有 listen + "新建..."
  const listenOptions = [
    ...Object.keys(listens)
      .sort()
      .map((n) => ({
        value: n,
        label: (
          <span>
            <code>{n}</code> <Tag color={listenKindTagColor[classifyListen(listens[n])]}>{classifyListen(listens[n])}</Tag>
          </span>
        ) as React.ReactNode,
      })),
  ];

  const onListenChange = (i: number, v: string | undefined) => {
    const arr = [...refs];
    arr[i] = { ...arr[i], listen: v ?? '' };
    set(arr);
  };

  const onCreateListen = (i: number) => {
    let n = 1;
    while (listens[`listen_${n}`]) n++;
    const name = `listen_${n}`;
    const pos = rfNodePos ? { x: rfNodePos.x + 260, y: rfNodePos.y + i * 90 } : undefined;
    addListen(name, {}, pos);
    onListenChange(i, name);
    setActivePanel({ kind: 'listenEdit', listenName: name });
  };

  const onApplyPaste = () => {
    try {
      const arr = JSON.parse(pasteText);
      if (!Array.isArray(arr)) throw new Error('JSON 必须是数组');
      // 简单校验
      for (const x of arr) {
        if (typeof x !== 'object' || x == null) throw new Error('每个 item 必须是对象');
        if (!('server' in x) || !('listen' in x)) throw new Error('每个 item 必须含 server 和 listen');
      }
      set([...refs, ...(arr as ListenRef[])]);
      message.success(`已追加 ${arr.length} 条监听`);
      setPasteOpen(false);
      setPasteText('');
    } catch (e) {
      message.error(`解析失败：${(e as Error).message}`);
    }
  };

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'flex-end', alignItems: 'center', marginBottom: 8 }}>
        <Space>
          <Button
            size="small"
            icon={<PlusOutlined />}
            onClick={() => set([...refs, { route: undefined, server: '', listen: '' }])}
          >
            添加
          </Button>
          <Button size="small" onClick={() => setPasteOpen(true)}>
            批量粘贴 JSON
          </Button>
        </Space>
      </div>

      <Table
        size="small"
        dataSource={refs.map((r, i) => ({ ...r, _i: i }))}
        rowKey="_i"
        pagination={false}
        tableLayout="fixed"
        locale={{ emptyText: '无监听注册' }}
        scroll={{ x: 600 }}
        columns={[
          {
            title: '监听',
            dataIndex: 'listen',
            width: 150,
            render: (_, r) => {
              const cur = r.listen || undefined;
              const listen = r.listen ? listens[r.listen] : undefined;
              return (
                <Space.Compact style={{ width: '100%' }}>
                  <Select
                    aria-label="监听"
                    size="small"
                    value={cur}
                    onChange={(v) => onListenChange(r._i, v)}
                    options={listenOptions}
                    style={{ flex: '1 1 0', minWidth: 0 }}
                    showSearch
                    optionFilterProp="value"
                  />
                  {r.listen && listen && (
                    <Tooltip title="打开监听节点">
                      <Button
                        size="small"
                        icon={<EditOutlined />}
                        style={{ flex: '0 0 auto' }}
                        onClick={() =>
                          setActivePanel({ kind: 'listenEdit', listenName: r.listen! })
                        }
                      />
                    </Tooltip>
                  )}
                  {!r.listen && (
                    <Tooltip title="新建并绑定">
                      <Button
                        size="small"
                        style={{ flex: '0 0 auto' }}
                        onClick={() => onCreateListen(r._i)}
                      >
                        +
                      </Button>
                    </Tooltip>
                  )}
                </Space.Compact>
              );
            },
          },
          {
            title: '目标连接',
            dataIndex: 'server',
            width: 120,
            render: (_, r) => (
              <TargetConnectionSelect
                inline
                size="small"
                server={r.server}
                onChangeServer={(v) => {
                  updateRef(r._i, { server: v ?? '' });
                }}
                connections={connections}
                loading={connectionsLoading}
                error={connectionsError}
              />
            ),
          },
          {
            title: 'route',
            dataIndex: 'route',
            width: 150,
            render: (_, r) => (
              <RouteFieldTrack
                server={r.server}
                value={r.route}
                routeKeyTemplate={r.server ? specs.get(r.server)?.routeKeyTemplate : undefined}
                loading={routeSpecsLoading}
                error={routeSpecsError}
                onChange={(route) => updateRef(r._i, { route })}
                onOpenFloating={() => setRouteEditorIndex(r._i)}
              />
            ),
          },
          {
            title: '队列容量',
            dataIndex: 'queueSize',
            width: 90,
            render: (_, r) => (
              <Tooltip title="监听缓存队列容量，缺省 1；<=0 会在校验报错">
                <InputNumber
                  aria-label="队列容量"
                  size="small"
                  min={1}
                  placeholder="缺省 1"
                  value={r.queueSize}
                  onChange={(v) => {
                    updateRef(r._i, { queueSize: (v as number) ?? undefined });
                  }}
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
                <Tooltip title="上移">
                  <Button
                    size="small"
                    icon={<ArrowUpOutlined />}
                    disabled={r._i === 0}
                    onClick={() => {
                      moveRef(r._i, r._i - 1);
                    }}
                  />
                </Tooltip>
                <Tooltip title="下移">
                  <Button
                    size="small"
                    icon={<ArrowDownOutlined />}
                    disabled={r._i === refs.length - 1}
                    onClick={() => {
                      moveRef(r._i, r._i + 1);
                    }}
                  />
                </Tooltip>
                <Tooltip title="删除">
                  <Button
                    size="small"
                    danger
                    icon={<DeleteOutlined />}
                    onClick={() => removeRef(r._i)}
                  />
                </Tooltip>
              </Space>
            ),
          },
        ]}
      />

      <RouteFloatingEditor
        windowId={`listen-ref-route:${nodeId}`}
        open={Boolean(routeEditorRef)}
        server={routeEditorRef?.server}
        value={routeEditorRef?.route}
        routeKeyTemplate={routeEditorRef?.server ? specs.get(routeEditorRef.server)?.routeKeyTemplate : undefined}
        loading={routeSpecsLoading}
        error={routeSpecsError}
        onChange={(route) => {
          if (routeEditorIndex !== null) updateRef(routeEditorIndex, { route });
        }}
        onClose={() => setRouteEditorIndex(null)}
      />

      <Modal
        open={pasteOpen}
        title="批量粘贴 listenRefs JSON"
        onCancel={() => setPasteOpen(false)}
        onOk={onApplyPaste}
        okText="追加"
        styles={{ mask: { zIndex: popupZ }, wrapper: { zIndex: popupZ + 1 } }}
      >
        <p style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>
          粘贴 <code>ListenRef[]</code> JSON 数组。每个对象需包含 <code>route</code>、<code>server</code>、
          <code>listen</code>。
        </p>
        <Input.TextArea
          rows={12}
          value={pasteText}
          onChange={(e) => setPasteText(e.target.value)}
          placeholder="[]"
          style={{ fontFamily: 'monospace' }}
        />
      </Modal>
    </div>
  );
}
