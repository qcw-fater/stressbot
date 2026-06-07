/**
 * Proto 浏览器：左侧平铺 message 列表 → 右侧字段表 → 选中插入。
 *
 * 两种模式：
 *   - 独立模式（不传 open/onClose/onSelect）：由 activePanel 控制，渲染为 FloatingWindow
 *   - 选择器模式（传 open/onClose/onSelect）：嵌入编辑器中临时选消息，渲染为临时 FloatingWindow
 */

import { Button, Empty, Input, List, Tooltip, Typography } from 'antd';
import { useEffect, useMemo, useState } from 'react';
import { createPortal } from 'react-dom';
import { useEditorStore } from '../store/editorStore';
import { useFloatingWindowStore } from '../store/floatingWindowStore';
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
  const closeWindow = useFloatingWindowStore((s) => s.closeWindow);

  const [search, setSearch] = useState('');
  const [selected, setSelected] = useState<string | undefined>();

  useEffect(() => {
    if (!open && isPickerMode) closeWindow(windowId);
  }, [open, isPickerMode, closeWindow, windowId]);

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
    if (isPickerMode) {
      closeWindow(windowId);
    } else if (!onClose) {
      closePanel('protoBrowser');
    }
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
          <div style={{ flex: 1, overflow: 'auto', padding: '0 4px' }}>
            {!detail ? (
              <Empty description="选择左侧 message 查看字段" />
            ) : (
              <div>
                <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 2 }}>
                  <Typography.Title level={5} style={{ margin: 0 }}>
                    {detail.fullName}
                  </Typography.Title>
                  <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                    {detail.fields.length} 个字段
                  </Typography.Text>
                </div>
                {detail.comment && (
                  <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginBottom: 8 }}>{detail.comment}</div>
                )}
                <table style={{ width: '100%', fontSize: 12, borderCollapse: 'collapse' }}>
                  <colgroup>
                    <col style={{ width: 32 }} />
                    <col style={{ width: '30%' }} />
                    <col style={{ width: '30%' }} />
                    <col />
                  </colgroup>
                  <thead>
                    <tr style={{ textAlign: 'left', color: 'var(--text-tertiary)' }}>
                      <th>#</th>
                      <th>字段名</th>
                      <th>类型</th>
                      <th>注释</th>
                    </tr>
                  </thead>
                  <tbody>
                    {detail.fields.map((f) => (
                      <tr key={f.number} style={{ borderTop: '1px solid var(--divider-bg)' }}>
                        <td style={{ color: 'var(--text-tertiary)', fontVariantNumeric: 'tabular-nums' }}>{f.number}</td>
                        <td><code style={{ fontSize: 12 }}>{f.name}</code></td>
                        <td>
                          <code style={{ fontSize: 12 }}>
                            {f.kind === 'map' ? `map<${f.mapKey}, ${f.mapValue}>` : f.type}
                          </code>
                          {f.repeated && (
                            <span style={{
                              marginLeft: 4, fontSize: 10, color: 'var(--color-primary)',
                              background: 'var(--node-selected-bg)',
                              padding: '0 4px', borderRadius: 3,
                            }}>repeated</span>
                          )}
                          {f.optional && !f.repeated && (
                            <span style={{
                              marginLeft: 4, fontSize: 10, color: 'var(--text-tertiary)',
                              background: 'var(--fill-quaternary, rgba(0,0,0,0.02))',
                              padding: '0 4px', borderRadius: 3,
                            }}>optional</span>
                          )}
                        </td>
                        <td style={{
                          color: 'var(--text-secondary)',
                          overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                        }}>
                          {f.comment ? (
                            <Tooltip title={f.comment} mouseEnterDelay={0.4}>
                              <span>{f.comment}</span>
                            </Tooltip>
                          ) : ''}
                        </td>
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
