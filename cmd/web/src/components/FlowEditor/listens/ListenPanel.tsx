/**
 * ListenPanel：右侧抽屉，列出所有 listens，提供 CRUD 入口。
 *
 * 设计文档 §8.4：
 *   - 列表项含形态徽章 + 引用计数
 *   - 引用计数 0 → 橙色"孤儿"警告
 *   - 双击列表项 → 打开 ListenEditor
 */

import { App as AntApp, Badge, Button, Drawer, Input, List, Popconfirm, Space, Tag } from 'antd';
import { DeleteOutlined, EditOutlined, PlusOutlined } from '@ant-design/icons';
import { useMemo, useState } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useEditorStore } from '../store/editorStore';
import { useFlowStore } from '../store/flowStore';
import { classifyListen } from '@/types/listen';
import { ListenEditor } from './ListenEditor';
import { buildRefsGraph } from './refsGraph';
import { listenKindTagColor } from './listenKindStyle';

export function ListenPanel() {
  const { message } = AntApp.useApp();
  const { activePanel, setActivePanel, closePanel, setHoveredListen } = useEditorStore(
    useShallow((s) => ({
      activePanel: s.activePanel,
      setActivePanel: s.setActivePanel,
      closePanel: s.closePanel,
      setHoveredListen: s.setHoveredListen,
    })),
  );

  const flow = useFlowStore(
    useShallow((s) => ({
      nodes: s.nodes,
      actions: s.actions,
      listens: s.listens,
      defaultDelayMs: s.defaultDelayMs,
    })),
  );
  const listens = flow.listens;
  const addListen = useFlowStore((s) => s.addListen);
  const removeListen = useFlowStore((s) => s.removeListen);

  const [search, setSearch] = useState('');

  const open = activePanel.listenPanel?.kind === 'listenPanel';
  const editorOpen = activePanel.listenEdit?.kind === 'listenEdit';

  // 抽屉内每次 render 都重算图开销不小（O(N*M)），只有 nodes/listens 变化时才重算。
  const graph = useMemo(() => buildRefsGraph(flow), [flow]);

  const list = Object.entries(listens)
    .filter(([n]) => !search || n.toLowerCase().includes(search.toLowerCase()))
    .sort(([a], [b]) => a.localeCompare(b));

  const onAdd = () => {
    let i = 1;
    while (listens[`listen_${i}`]) i++;
    const name = `listen_${i}`;
    addListen(name, {});
    setActivePanel({ kind: 'listenEdit', listenName: name });
    message.success(`已新建 ${name}`);
  };

  return (
    <>
      <Drawer
        title={
          <Space>
            <span>Listen ({Object.keys(listens).length})</span>
          </Space>
        }
        open={open}
        onClose={() => closePanel('listenPanel')}
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
          placeholder="搜索 listen 名"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          style={{ marginBottom: 8 }}
        />
        <List
          size="small"
          dataSource={list}
          rowKey={([name]) => name}
          locale={{ emptyText: '尚无 listen' }}
          renderItem={([name, def]) => {
            const kind = classifyListen(def);
            const refCount = graph.refCount.get(name) ?? 0;
            return (
              <List.Item
                onMouseEnter={() => setHoveredListen(name)}
                onMouseLeave={() => setHoveredListen(null)}
                actions={[
                  <a
                    key="edit"
                    onClick={() => setActivePanel({ kind: 'listenEdit', listenName: name })}
                  >
                    <EditOutlined />
                  </a>,
                  <Popconfirm
                    key="del"
                    title="删除此 listen？"
                    description={
                      refCount > 0
                        ? `仍被 ${refCount} 个 action 引用，删除后这些 listenRefs 会变成悬空引用（导出校验报错）。`
                        : '此 listen 是孤儿，可安全删除。'
                    }
                    onConfirm={() => {
                      removeListen(name);
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
                      <Tag color={listenKindTagColor[kind]}>{kind}</Tag>
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
      {editorOpen && <ListenEditor />}
    </>
  );
}
