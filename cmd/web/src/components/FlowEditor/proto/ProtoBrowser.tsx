/**
 * Proto 浏览器：左侧平铺 message 列表 → 右侧字段表 → 选中插入。
 *
 * 两种模式：
 *   - 独立模式（不传 open/onClose/onSelect）：由 activePanel 控制，渲染为 FloatingWindow
 *   - 选择器模式（传 open/onClose/onSelect）：嵌入编辑器中临时选消息，渲染为 Modal
 */

import { Button, Empty, Input, List, Typography } from 'antd';
import { useMemo, useState } from 'react';
import { createPortal } from 'react-dom';
import { useEditorStore } from '../store/editorStore';
import { protoRegistry } from './ProtoRegistry';
import { useProtoStore } from './protoStore';
import { FloatingWindow } from '../panels/FloatingWindow';
import type { ProtoMessage } from '@/types/proto';

export interface ProtoBrowserProps {
  /** 窗口 ID，用于区分多个实例（默认 protoBrowser） */
  windowId?: string;
  /** 受控显示（选择器模式）；省略则用独立模式（FloatingWindow） */
  open?: boolean;
  onClose?: () => void;
  /** 选中消息后的回调 */
  onSelect?: (fullName: string) => void;
  /** 过滤前缀 */
  filter?: (m: ProtoMessage) => boolean;
}

export function ProtoBrowser({ windowId: customWindowId, open: openProp, onClose, onSelect, filter }: ProtoBrowserProps) {
  const isPickerMode = openProp !== undefined;
  const activePanel = useEditorStore((s) => s.activePanel.protoBrowser);
  const closePanel = useEditorStore((s) => s.closePanel);
  const open = openProp ?? activePanel?.kind === 'protoBrowser';

  const windowId = customWindowId ?? (isPickerMode ? "protoPicker" : "protoBrowser");

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
  }, [search, filter, protoStatus, protoHash]);

  const detail = selected ? protoRegistry.lookupMessage(selected) : undefined;

  const handleClose = () => {
    onClose?.();
    if (!onClose) closePanel('protoBrowser');
  };

  const content = (
    <>
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
        <div style={{ display: 'flex', gap: 12, height: 'calc(100% - 48px)', minHeight: 300 }}>
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
                {isPickerMode && (
                  <div style={{ marginTop: 16 }}>
                    <Button
                      type="primary"
                      onClick={() => {
                        onSelect?.(detail.fullName);
                        handleClose();
                      }}
                    >
                      选择此消息
                    </Button>
                  </div>
                )}
              </div>
            )}
          </div>
        </div>
      )}
    </>
  );

  const windowNode = (
    <FloatingWindow
      windowId={windowId}
      title={isPickerMode ? "Proto 选择器" : "Proto 浏览器"}
      defaultSize={{ width: 780, height: 540 }}
      minSize={{ width: 500, height: 350 }}
      open={open}
      onClose={handleClose}
    >
      {content}
    </FloatingWindow>
  );

  // 选择器模式：使用 createPortal 挂载到 body，避免被 Drawer 等容器裁切
  if (isPickerMode) {
    return createPortal(windowNode, document.body);
  }

  // 独立模式：直接渲染
  return windowNode;
}
