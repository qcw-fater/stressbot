/**
 * CallbackPanel：右侧抽屉，列出所有 callbacks，提供 CRUD 入口。
 *
 * 设计文档 §8.4：
 *   - 列表项含形态徽章 + 引用计数
 *   - 引用计数 0 → 橙色"孤儿"警告
 *   - 双击列表项 → 打开 CallbackEditor
 */

import { App as AntApp, Badge, Button, Drawer, Input, List, Popconfirm, Space, Tag } from 'antd';
import { DeleteOutlined, EditOutlined, PlusOutlined } from '@ant-design/icons';
import { useMemo, useState } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useEditorStore } from '../store/editorStore';
import { useFlowStore } from '../store/flowStore';
import { classifyCallback } from '@/types/callback';
import { CallbackEditor } from './CallbackEditor';
import { buildRefsGraph } from './refsGraph';
import { callbackKindTagColor } from './callbackKindStyle';

export function CallbackPanel() {
  const { message } = AntApp.useApp();
  const activePanel = useEditorStore((s) => s.activePanel);
  const setActivePanel = useEditorStore((s) => s.setActivePanel);
  const setHoveredCallback = useEditorStore((s) => s.setHoveredCallback);

  const flow = useFlowStore(
    useShallow((s) => ({
      nodes: s.nodes,
      actions: s.actions,
      callbacks: s.callbacks,
      defaultDelayMs: s.defaultDelayMs,
    })),
  );
  const callbacks = flow.callbacks;
  const addCallback = useFlowStore((s) => s.addCallback);
  const removeCallback = useFlowStore((s) => s.removeCallback);

  const [search, setSearch] = useState('');

  const open = activePanel.kind === 'callbackPanel';
  const editorOpen = activePanel.kind === 'callbackEdit';

  // 抽屉内每次 render 都重算图开销不小（O(N*M)），只有 nodes/callbacks 变化时才重算。
  const graph = useMemo(() => buildRefsGraph(flow), [flow]);

  const list = Object.entries(callbacks)
    .filter(([n]) => !search || n.toLowerCase().includes(search.toLowerCase()))
    .sort(([a], [b]) => a.localeCompare(b));

  const onAdd = () => {
    let i = 1;
    while (callbacks[`callback_${i}`]) i++;
    const name = `callback_${i}`;
    addCallback(name, {});
    setActivePanel({ kind: 'callbackEdit', callbackName: name });
    message.success(`已新建 ${name}`);
  };

  return (
    <>
      <Drawer
        title={
          <Space>
            <span>Callback ({Object.keys(callbacks).length})</span>
          </Space>
        }
        open={open}
        onClose={() => setActivePanel({ kind: 'none' })}
        width={520}
        mask={false}
        extra={
          <Space>
            <Button icon={<PlusOutlined />} type="primary" size="small" onClick={onAdd}>
              新建
            </Button>
          </Space>
        }
      >
        <Input.Search
          allowClear
          placeholder="搜索 callback 名"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          style={{ marginBottom: 8 }}
        />
        <List
          size="small"
          dataSource={list}
          rowKey={([name]) => name}
          locale={{ emptyText: '尚无 callback' }}
          renderItem={([name, def]) => {
            const kind = classifyCallback(def);
            const refCount = graph.refCount.get(name) ?? 0;
            return (
              <List.Item
                onMouseEnter={() => setHoveredCallback(name)}
                onMouseLeave={() => setHoveredCallback(null)}
                actions={[
                  <a
                    key="edit"
                    onClick={() => setActivePanel({ kind: 'callbackEdit', callbackName: name })}
                  >
                    <EditOutlined />
                  </a>,
                  <Popconfirm
                    key="del"
                    title="删除此 callback？"
                    description={
                      refCount > 0
                        ? `仍被 ${refCount} 个 action 引用，删除后这些 listenCallbacks 会变成悬空引用（导出校验报错）。`
                        : '此 callback 是孤儿，可安全删除。'
                    }
                    onConfirm={() => {
                      removeCallback(name);
                      message.success(`已删除 ${name}`);
                    }}
                  >
                    <a style={{ color: 'var(--color-error)' }}>
                      <DeleteOutlined />
                    </a>
                  </Popconfirm>,
                ]}
              >
                <List.Item.Meta
                  title={
                    <Space>
                      <code>{name}</code>
                      <Tag color={callbackKindTagColor[kind]}>{kind}</Tag>
                      {refCount > 0 ? (
                        <Badge count={refCount} color="blue" overflowCount={99} />
                      ) : (
                        <Tag color="orange">⚠ 孤儿</Tag>
                      )}
                    </Space>
                  }
                  description={
                    <span style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>
                      {def.s2cProto && <span>s2c: {def.s2cProto}</span>}
                      {def.script && <span>script: {def.script}</span>}
                      {!def.s2cProto && !def.script && <em>silent</em>}
                    </span>
                  }
                />
              </List.Item>
            );
          }}
        />
      </Drawer>
      {editorOpen && <CallbackEditor />}
    </>
  );
}
