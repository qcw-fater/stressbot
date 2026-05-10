/**
 * action.listenCallbacks 表格编辑器。
 *
 * 设计文档 §8.6：route + server + callback 三列 + 形态徽章 + 排序删除 + 批量入口。
 */

import { App as AntApp, Button, Input, Modal, Select, Space, Table, Tag, Tooltip } from 'antd';
import { ArrowDownOutlined, ArrowUpOutlined, DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import { useState } from 'react';
import type { ListenRef } from '@/types/flow';
import { classifyCallback } from '@/types/callback';
import { useFlowStore } from '../store/flowStore';
import { useEditorStore } from '../store/editorStore';
import { RouteEditor } from './RouteEditor';
import { callbackKindTagColor } from './callbackKindStyle';
import { monoCellStyle } from '../styles/inlineStyles';

export interface ListenRefsTableProps {
  nodeId: string;
}

export function ListenRefsTable({ nodeId }: ListenRefsTableProps) {
  const { message } = AntApp.useApp();
  const node = useFlowStore((s) => s.nodes[nodeId]);
  const callbacks = useFlowStore((s) => s.callbacks);
  const updateNode = useFlowStore((s) => s.updateNode);
  const addCallback = useFlowStore((s) => s.addCallback);
  const setActivePanel = useEditorStore((s) => s.setActivePanel);

  const [pasteOpen, setPasteOpen] = useState(false);
  const [pasteText, setPasteText] = useState('');

  if (!node) return null;
  const refs = node.listenCallbacks ?? [];
  const set = (next: ListenRef[]) => updateNode(nodeId, { listenCallbacks: next });

  // callback 候选下拉：null + 现有 callback + "新建..."
  const callbackOptions = [
    { value: '__null__', label: '(null) — 静默丢弃' },
    ...Object.keys(callbacks)
      .sort()
      .map((n) => ({
        value: n,
        label: (
          <span>
            <code>{n}</code> <Tag color={callbackKindTagColor[classifyCallback(callbacks[n])]}>{classifyCallback(callbacks[n])}</Tag>
          </span>
        ) as React.ReactNode,
      })),
  ];

  const onCallbackChange = (i: number, v: string | undefined) => {
    const arr = [...refs];
    if (v === '__null__' || !v) {
      arr[i] = { ...arr[i], callback: null };
    } else {
      arr[i] = { ...arr[i], callback: v };
    }
    set(arr);
  };

  const onCreateCallback = (i: number) => {
    let n = 1;
    while (callbacks[`callback_${n}`]) n++;
    const name = `callback_${n}`;
    addCallback(name, {});
    onCallbackChange(i, name);
    setActivePanel({ kind: 'callbackEdit', callbackName: name });
  };

  const onApplyPaste = () => {
    try {
      const arr = JSON.parse(pasteText);
      if (!Array.isArray(arr)) throw new Error('JSON 必须是数组');
      // 简单校验
      for (const x of arr) {
        if (typeof x !== 'object' || x == null) throw new Error('每个 item 必须是对象');
        if (!('server' in x) || !('callback' in x)) throw new Error('每个 item 必须含 server 和 callback');
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
            onClick={() => set([...refs, { route: { cmd: 0, act: 0 }, server: '', callback: null }])}
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
            title: 'callback',
            dataIndex: 'callback',
            render: (_, r) => {
              const cur = r.callback ?? '__null__';
              const cb = r.callback ? callbacks[r.callback] : undefined;
              return (
                <Space.Compact style={{ width: '100%' }}>
                  <Select
                    value={cur}
                    onChange={(v) => onCallbackChange(r._i, v)}
                    options={callbackOptions}
                    style={{ flex: 1 }}
                    showSearch
                    optionFilterProp="value"
                  />
                  {r.callback && cb && (
                    <Tooltip title="跳转到 CallbackEditor">
                      <Button
                        size="small"
                        onClick={() =>
                          setActivePanel({ kind: 'callbackEdit', callbackName: r.callback! })
                        }
                      >
                        →
                      </Button>
                    </Tooltip>
                  )}
                  <Tooltip title="新建并绑定">
                    <Button size="small" onClick={() => onCreateCallback(r._i)}>
                      +
                    </Button>
                  </Tooltip>
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
        title="批量粘贴 listenCallbacks JSON"
        onCancel={() => setPasteOpen(false)}
        onOk={onApplyPaste}
        okText="追加"
      >
        <p style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>
          粘贴 <code>ListenRef[]</code> JSON 数组。每个对象需包含 <code>route</code>、<code>server</code>、
          <code>callback</code>。
        </p>
        <Input.TextArea
          rows={12}
          value={pasteText}
          onChange={(e) => setPasteText(e.target.value)}
          placeholder={`[\n  {"route":{"cmd":3,"act":1},"server":"tcp:logic","callback":"matchPoll"},\n  {"route":{"cmd":2,"act":18},"server":"tcp:logic","callback":"stateUpdate"}\n]`}
          style={{ fontFamily: 'monospace' }}
        />
      </Modal>
    </div>
  );
}
