/**
 * action.listenRefs 表格编辑器。
 *
 * 设计文档 §8.6：route + server + listen 三列 + 形态徽章 + 排序删除 + 批量入口。
 */

import { App as AntApp, Button, Input, Modal, Select, Space, Table, Tag, Tooltip } from 'antd';
import { ArrowDownOutlined, ArrowUpOutlined, DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import { useState } from 'react';
import type { ListenRef } from '@/types/flow';
import { classifyListen } from '@/types/listen';
import { useFlowStore } from '../store/flowStore';
import { useEditorStore } from '../store/editorStore';
import { RouteEditor } from './RouteEditor';
import { listenKindTagColor } from './listenKindStyle';
import { monoCellStyle } from '../styles/inlineStyles';
import { useFloatingWindowStore } from '../store/floatingWindowStore';

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

  const [pasteOpen, setPasteOpen] = useState(false);
  const [pasteText, setPasteText] = useState('');

  if (!node) return null;
  const refs = node.listenRefs ?? [];
  const set = (next: ListenRef[]) => updateNode(nodeId, { listenRefs: next });

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
            onClick={() => set([...refs, { route: { cmd: 0, act: 0 }, server: '', listen: '' }])}
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
        locale={{ emptyText: '无监听注册' }}
        scroll={{ x: 700 }}
        columns={[
          {
            title: 'route',
            dataIndex: 'route',
            width: 220,
            render: (_, r) => (
              <RouteEditor
                size="small"
                value={r.route}
                onChange={(v) => {
                  const arr = [...refs];
                  arr[r._i] = { ...arr[r._i], route: v };
                  set(arr);
                }}
              />
            ),
          },
          {
            title: 'server',
            dataIndex: 'server',
            width: 160,
            render: (_, r) => (
              <Input
                size="small"
                value={r.server}
                onChange={(e) => {
                  const arr = [...refs];
                  arr[r._i] = { ...arr[r._i], server: e.target.value };
                  set(arr);
                }}
                placeholder="如 tcp:logic"
                style={monoCellStyle}
              />
            ),
          },
          {
            title: 'listen',
            dataIndex: 'listen',
            render: (_, r) => {
              const cur = r.listen || undefined;
              const listen = r.listen ? listens[r.listen] : undefined;
              return (
                <Space.Compact style={{ width: '100%' }}>
                  <Select
                    size="small"
                    value={cur}
                    onChange={(v) => onListenChange(r._i, v)}
                    options={listenOptions}
                    style={{ flex: 1 }}
                    showSearch
                    optionFilterProp="value"
                  />
                  {r.listen && listen && (
                    <Tooltip title="跳转到 ListenEditor">
                      <Button
                        size="small"
                        onClick={() =>
                          setActivePanel({ kind: 'listenEdit', listenName: r.listen! })
                        }
                      >
                        →
                      </Button>
                    </Tooltip>
                  )}
                  {!r.listen && (
                    <Tooltip title="新建并绑定">
                      <Button size="small" onClick={() => onCreateListen(r._i)}>
                        +
                      </Button>
                    </Tooltip>
                  )}
                </Space.Compact>
              );
            },
          },
          {
            title: '操作',
            width: 130,
            render: (_, r) => (
              <Space size={4}>
                <Button
                  size="small"
                  icon={<ArrowUpOutlined />}
                  disabled={r._i === 0}
                  onClick={() => {
                    const arr = [...refs];
                    [arr[r._i - 1], arr[r._i]] = [arr[r._i], arr[r._i - 1]];
                    set(arr);
                  }}
                />
                <Button
                  size="small"
                  icon={<ArrowDownOutlined />}
                  disabled={r._i === refs.length - 1}
                  onClick={() => {
                    const arr = [...refs];
                    [arr[r._i], arr[r._i + 1]] = [arr[r._i + 1], arr[r._i]];
                    set(arr);
                  }}
                />
                <Button
                  size="small"
                  danger
                  icon={<DeleteOutlined />}
                  onClick={() => set(refs.filter((_, j) => j !== r._i))}
                />
              </Space>
            ),
          },
        ]}
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
          placeholder={`[\n  {"route":{"cmd":3,"act":1},"server":"tcp:logic","listen":"matchPoll"},\n  {"route":{"cmd":2,"act":18},"server":"tcp:logic","listen":"stateUpdate"}\n]`}
          style={{ fontFamily: 'monospace' }}
        />
      </Modal>
    </div>
  );
}
