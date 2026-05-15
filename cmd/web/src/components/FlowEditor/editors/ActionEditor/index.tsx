/**
 * ActionEditor 主组件：
 *
 * - 顶部：action 名（map key）+ pattern 选择器
 * - 中部：DeclarativeForm 或 Lua 脚本编辑（按 pattern）
 * - lua 模式下通过「编辑」按钮弹出 Modal 打开 LuaForm，与 BooleanEditor/LoopEditor 统一
 *
 * 由 NodeEditorDrawer 在 node.type === 'action' 时调用。
 */

import { Alert, App as AntApp, AutoComplete, Button, Collapse, Form, Input, Modal, Select, Space, Switch, Tag } from 'antd';
import { EditOutlined, EyeOutlined } from '@ant-design/icons';
import { useMemo, useState, useEffect } from 'react';
import { useFlowStore } from '../../store/flowStore';
import { useFloatingWindowStore } from '../../store/floatingWindowStore';
import { PatternSelector } from './PatternSelector';
import { DeclarativeForm } from './DeclarativeForm';
import { LuaForm } from './LuaForm';
import { ListenRefsTable } from '../../listens/ListenRefsTable';
import { DelayInput } from '../shared/DelayInput';
import { SaveTemplateButton } from '../../library/SaveTemplateButton';
import { ActionPreview } from './ActionPreview';
import type { ActionDef, ActionPattern } from '@/types/action';
import { fetchBaselineScriptIndex } from '@/services/baselineApi';

export interface ActionEditorProps {
  nodeId: string;
}

export function ActionEditor({ nodeId }: ActionEditorProps) {
  const { modal } = AntApp.useApp();
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;
  const node = useFlowStore((s) => s.nodes[nodeId]);
  const actions = useFlowStore((s) => s.actions);
  const updateNode = useFlowStore((s) => s.updateNode);
  const addAction = useFlowStore((s) => s.addAction);
  const updateAction = useFlowStore((s) => s.updateAction);

  const [editorOpen, setEditorOpen] = useState(false);
  const [previewOpen, setPreviewOpen] = useState(false);
  const [luaDirty, setLuaDirty] = useState(false);
  const [files, setFiles] = useState<string[]>([]);

  const actionName = node?.action ?? '';
  const action = actions[actionName];

  // 若 action 引用了不存在的定义，本期允许在编辑器里"现场补一个空 action"
  const effectiveAction = useMemo<ActionDef>(() => {
    if (action) return action;
    return { pattern: 'tcpSend' as ActionPattern };
  }, [action]);

  // 拉取脚本列表（给 lua 模式的 AutoComplete 用）
  useEffect(() => {
    let cancel = false;
    fetchBaselineScriptIndex()
      .then((list) => { if (!cancel) setFiles(list); })
      .catch(() => undefined);
    return () => { cancel = true; };
  }, []);

  if (!node) return null;

  const onActionDefChange = (next: ActionDef) => {
    if (action) {
      updateAction(actionName, next);
    } else {
      // 第一次编辑：补上 ActionDef
      const name = actionName || nodeId;
      addAction(name, next);
      if (!actionName) updateNode(nodeId, { action: name });
    }
  };

  const onPatternChange = (p: ActionPattern) => {
    onActionDefChange({ ...effectiveAction, pattern: p });
  };

  const closeEditor = () => {
    if (luaDirty) {
      modal.confirm({
        title: '脚本有未保存的改动',
        content: '关闭后未保存的内容将丢失，是否继续？',
        okText: '不保存',
        cancelText: '取消',
        onOk: () => {
          setEditorOpen(false);
          setLuaDirty(false);
        },
      });
    } else {
      setEditorOpen(false);
    }
  };

  const isLua = effectiveAction.pattern === 'lua';

  return (
    <div>
      <Form layout="vertical" style={{ marginBottom: 8 }}>
        <Form.Item label="pattern">
          <Space>
            <PatternSelector value={effectiveAction.pattern} onChange={onPatternChange} />
            <SaveTemplateButton kind="action" name={actionName} data={effectiveAction} description={node.description} />
            {PREVIEWABLE_PATTERNS.includes(effectiveAction.pattern) && (
              <Button
                size="small"
                icon={<EyeOutlined />}
                disabled={!effectiveAction.c2sProto && !effectiveAction.s2cProto && effectiveAction.pattern !== 'setState'}
                onClick={() => setPreviewOpen(true)}
              >
                预览
              </Button>
            )}
          </Space>
        </Form.Item>
      </Form>

      {!action && (
        <Alert
          type="info"
          message={
            <span>
              actions 表中尚未存在 <code>{actionName || '(空)'}</code>，将在第一次保存字段时自动创建。
            </span>
          }
          showIcon
          style={{ marginBottom: 12 }}
        />
      )}

      {isLua ? (
        <>
          <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 8 }}>
            <span style={{ fontSize: 12, color: 'var(--text-secondary)', whiteSpace: 'nowrap' }}>脚本文件：</span>
            <AutoComplete
              style={{ flex: 1 }}
              value={effectiveAction.script ?? ''}
              onChange={(v) => onActionDefChange({ ...effectiveAction, script: v })}
              onBlur={() => {
                const cur = effectiveAction.script?.trim() ?? '';
                if (cur && !cur.endsWith('.lua')) {
                  onActionDefChange({ ...effectiveAction, script: cur + '.lua' });
                }
              }}
              options={files.map((f) => ({ value: f, label: f }))}
              placeholder="输入新文件名或选择已有脚本"
              allowClear
              filterOption={(input, option) =>
                (option?.value as string)?.toLowerCase().includes(input.toLowerCase()) ?? false
              }
            />
            {effectiveAction.script?.trim() && !effectiveAction.script.trim().endsWith('.lua') && (
              <Tag color="purple">.lua</Tag>
            )}
            <Button
              icon={<EditOutlined />}
              onClick={() => setEditorOpen(true)}
              disabled={!effectiveAction.script?.trim()}
              title={!effectiveAction.script?.trim() ? '先填写脚本文件名再编辑' : '在编辑器里编辑该脚本内容'}
            >
              编辑
            </Button>
          </div>
          <div style={{ fontSize: 11, color: 'var(--text-tertiary)', lineHeight: 1.5 }}>
            入口 <code>function execute(r)</code>，返回 <code>code, send, recv</code>（错误码 0=成功，发送/接收字节数）。
            点旁边的「编辑」按钮可在编辑器里直接写脚本，按 Ctrl+S 保存到本地。
          </div>
        </>
      ) : (
        <DeclarativeForm action={effectiveAction} onChange={onActionDefChange} />
      )}

      <Collapse
        style={{ marginTop: 16 }}
        items={[
          {
            key: 'node',
            label: '节点级字段（errorStrategy / delayMs）',
            children: (
              <Form layout="vertical">
                <Form.Item label="错误处理策略（动作失败时）">
                  <Select
                    value={node.errorStrategy || 'ignore'}
                    onChange={(v) => updateNode(nodeId, { errorStrategy: v === 'ignore' ? undefined : v })}
                    options={[
                      { value: 'ignore', label: '忽略（打日志，继续执行）' },
                      { value: 'abort', label: '中断（中止当前流程）' },
                    ]}
                  />
                </Form.Item>
                <Form.Item label="节点延迟 delayMs">
                  <DelayInput value={node.delayMs} onChange={(v) => updateNode(nodeId, { delayMs: v })} />
                </Form.Item>
              </Form>
            ),
          },
          {
            key: 'listen',
            label: `监听注册（${node.listenCallbacks?.length ?? 0}）`,
            children: <ListenRefsTable nodeId={nodeId} />,
          },
        ]}
      />

      {/* Lua 脚本编辑 Modal */}
      <Modal
        open={editorOpen}
        title={
          <span>
            编辑动作脚本 <code style={{ color: 'var(--text-secondary)' }}>{effectiveAction.script || '(未命名)'}</code>
          </span>
        }
        onCancel={closeEditor}
        footer={[
          <Button key="close" onClick={closeEditor}>
            完成
          </Button>,
        ]}
        width={900}
        destroyOnHidden
        focusTriggerAfterClose={false}
        styles={{ mask: { zIndex: popupZ }, wrapper: { zIndex: popupZ + 1 } }}
      >
        <div onKeyDown={(e) => e.stopPropagation()}>
        <LuaForm
          mode="action"
          script={effectiveAction.script}
          onChangeScript={(s) => onActionDefChange({ ...effectiveAction, script: s })}
          onDirtyChange={setLuaDirty}
        />
        </div>
      </Modal>

      {/* 消息预览 Modal */}
      <Modal
        open={previewOpen}
        title={
          <span>
            消息预览 <code style={{ color: 'var(--text-secondary)' }}>{actionName || '(未命名)'}</code>
          </span>
        }
        onCancel={() => setPreviewOpen(false)}
        footer={[
          <Button key="close" onClick={() => setPreviewOpen(false)}>
            关闭
          </Button>,
        ]}
        width={800}
        destroyOnHidden
        styles={{ mask: { zIndex: popupZ }, wrapper: { zIndex: popupZ + 1 } }}
      >
        <ActionPreview action={effectiveAction} />
      </Modal>
    </div>
  );
}

const PREVIEWABLE_PATTERNS: ActionPattern[] = [
  'tcpSend', 'tcpRequest', 'udpSend', 'udpRequest',
  'httpRequest', 'setState', 'tcpListen', 'udpListen',
];
