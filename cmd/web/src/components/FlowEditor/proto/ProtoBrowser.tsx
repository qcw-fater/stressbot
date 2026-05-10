/**
 * Proto 浏览器：左侧平铺 message 列表 → 右侧字段表 → 选中插入。
 *
 * 入口：ActionEditor / CallbackEditor 中点击"浏览"按钮，
 * 或 Toolbar → JSON 预览旁的 [Proto] 按钮。
 */

import { Drawer, Empty, Input, List, Typography } from 'antd';
import { useMemo, useState } from 'react';
import { useEditorStore } from '../store/editorStore';
import { protoRegistry } from './ProtoRegistry';
import { useProtoStore } from './protoStore';
import type { ProtoMessage } from '@/types/proto';

export interface ProtoBrowserProps {
  /** 受控显示，未提供时由 editorStore.activePanel.kind === 'protoBrowser' 决定 */
  open?: boolean;
  onClose?: () => void;
  /** 选中消息后的回调 */
  onSelect?: (fullName: string) => void;
  /** 过滤前缀（如 "Game.LoginPlayerC2S" 只显示 C2S 消息） */
  filter?: (m: ProtoMessage) => boolean;
}

export function ProtoBrowser({ open: openProp, onClose, onSelect, filter }: ProtoBrowserProps) {
  const activePanel = useEditorStore((s) => s.activePanel);
  const setActivePanel = useEditorStore((s) => s.setActivePanel);
  const open = openProp ?? activePanel.kind === 'protoBrowser';

  // 订阅 proto 加载状态：load 完成后会驱动下面的 useMemo 重新计算 message 列表
  const protoStatus = useProtoStore((s) => s.status);
  const protoHash = useProtoStore((s) => s.hash);

  const [search, setSearch] = useState('');
  const [selected, setSelected] = useState<string | undefined>();

  const messages = useMemo(() => {
    if (protoStatus !== 'ready' || !protoRegistry.isLoaded()) return [];
    let list = protoRegistry.listMessages();
    if (filter) list = list.filter(filter);
    if (search) {
      const lo = search.toLowerCase();
      list = list.filter(
        (m) => m.fullName.toLowerCase().includes(lo) || m.shortName.toLowerCase().includes(lo),
      );
    }
    return list;
    // protoHash 变化代表实际 proto 数据更新，也作为依赖以触发重算
  }, [search, filter, protoStatus, protoHash]);

  const detail = selected ? protoRegistry.lookupMessage(selected) : undefined;

  const handleClose = () => {
    onClose?.();
    if (!onClose) setActivePanel({ kind: 'none' });
  };

  return (
    <Drawer
      title="Proto 浏览器"
      open={open}
      onClose={handleClose}
      width={720}
      mask={false}
    >
      <Input.Search
        placeholder="搜索 message 名（如 LoginPlayerC2S）"
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        style={{ marginBottom: 12 }}
        allowClear
      />
      {protoStatus !== 'ready' || !protoRegistry.isLoaded() ? (
        <Empty
          description={
            protoStatus === 'loading'
              ? 'Proto 正在加载…'
              : protoStatus === 'error'
                ? 'Proto 加载失败，请检查 conf/proto/'
                : 'Proto 尚未加载'
          }
        />
      ) : (
        <div style={{ display: 'flex', gap: 12, height: 'calc(100vh - 180px)' }}>
          <div
            style={{
              width: '40%',
              overflow: 'auto',
              borderRight: '1px solid var(--border-color, rgba(0,0,0,0.06))',
            }}
          >
            <Typography.Text type="secondary" style={{ fontSize: 12, paddingLeft: 4 }}>
              共 {messages.length} 条
            </Typography.Text>
            <List
              size="small"
              dataSource={messages}
              renderItem={(m) => {
                const isActive = selected === m.fullName;
                return (
                  <List.Item
                    onClick={() => setSelected(m.fullName)}
                    style={{
                      cursor: 'pointer',
                      padding: '4px 8px',
                      background: isActive ? 'var(--node-selected-bg, rgba(22,119,255,0.12))' : undefined,
                      borderRadius: 4,
                    }}
                  >
                    <div style={{ width: '100%', minWidth: 0 }}>
                      <div
                        style={{
                          fontSize: 13,
                          fontWeight: isActive ? 600 : 400,
                          overflow: 'hidden',
                          textOverflow: 'ellipsis',
                          whiteSpace: 'nowrap',
                        }}
                      >
                        {m.shortName}
                      </div>
                      <div
                        style={{
                          fontSize: 11,
                          color: 'var(--text-tertiary)',
                          overflow: 'hidden',
                          textOverflow: 'ellipsis',
                          whiteSpace: 'nowrap',
                        }}
                      >
                        {m.fullName}
                      </div>
                    </div>
                  </List.Item>
                );
              }}
            />
          </div>
          <div style={{ flex: 1, overflow: 'auto' }}>
            {!detail ? (
              <Empty description="选择左侧 message 查看字段" />
            ) : (
              <div>
                <Typography.Title level={5} style={{ marginTop: 0 }}>
                  {detail.fullName}
                </Typography.Title>
                <table style={{ width: '100%', fontSize: 12, borderCollapse: 'collapse' }}>
                  <thead>
                    <tr style={{ textAlign: 'left', color: 'var(--text-tertiary)' }}>
                      <th style={{ width: 40 }}>#</th>
                      <th>字段名</th>
                      <th>类型</th>
                      <th style={{ width: 80 }}>repeated</th>
                    </tr>
                  </thead>
                  <tbody>
                    {detail.fields.map((f) => (
                      <tr key={f.number} style={{ borderTop: '1px solid var(--divider-bg)' }}>
                        <td style={{ color: 'var(--text-tertiary)' }}>{f.number}</td>
                        <td><code>{f.name}</code></td>
                        <td>{f.kind === 'map' ? `map<${f.mapKey}, ${f.mapValue}>` : f.type}</td>
                        <td>{f.repeated ? '✓' : ''}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                <div style={{ marginTop: 16 }}>
                  <button
                    style={{
                      padding: '6px 12px',
                      background: 'var(--node-action)',
                      color: '#fff',
                      border: 'none',
                      borderRadius: 4,
                      cursor: 'pointer',
                    }}
                    onClick={() => {
                      onSelect?.(detail.fullName);
                      handleClose();
                    }}
                  >
                    选择此消息
                  </button>
                </div>
              </div>
            )}
          </div>
        </div>
      )}
    </Drawer>
  );
}
