/**
 * ActionEditor 主组件：
 *
 * - 顶部：action 名（map key）+ pattern 选择器
 * - 中部：DeclarativeForm 或 Lua 脚本编辑（按 pattern）
 * - lua 模式下通过「编辑」按钮弹出 Modal 打开 LuaForm，与 BooleanEditor/LoopEditor 统一
 *
 * 由 NodeEditorDrawer 在 node.type === 'action' 时调用。
 */

import { Alert, Button, Collapse, Form, Modal, Select, Space } from 'antd';
import { EyeOutlined } from '@ant-design/icons';
import { useMemo, useState } from 'react';
import { useFlowStore } from '../../store/flowStore';
import { useFloatingWindowStore } from '../../store/floatingWindowStore';
import { PatternSelector } from './PatternSelector';
import { DeclarativeForm } from './DeclarativeForm';
import { ListenRefsTable } from '../../listens/ListenRefsTable';
import { DelayInput } from '../shared/DelayInput';
import { SaveTemplateButton } from '../../library/SaveTemplateButton';
import { ActionPreview } from './ActionPreview';
import { LuaScriptField } from '../shared/LuaScriptField';
import type { ActionDef, ActionPattern } from '@/types/action';

export interface ActionEditorProps {
  nodeId: string;
}

export function ActionEditor({ nodeId }: ActionEditorProps) {
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;
  const node = useFlowStore((s) => s.nodes[nodeId]);
  const actions = useFlowStore((s) => s.actions);
  const updateNode = useFlowStore((s) => s.updateNode);
  const addAction = useFlowStore((s) => s.addAction);
  const updateAction = useFlowStore((s) => s.updateAction);

  const [previewOpen, setPreviewOpen] = useState(false);

  const actionName = node?.action ?? '';
  const action = actions[actionName];

  // 若 action 引用了不存在的定义，本期允许在编辑器里"现场补一个空 action"
  const effectiveAction = useMemo<ActionDef>(() => {
    if (action) return action;
    return { pattern: 'tcpSend' as ActionPattern };
  }, [action]);

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
        <LuaScriptField
          mode="action"
          value={effectiveAction.script}
          onChange={(s) => onActionDefChange({ ...effectiveAction, script: s })}
        />
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
