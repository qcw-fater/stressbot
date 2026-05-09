/**
 * ActionEditor 主组件：
 *
 * - 顶部：action 名（map key）+ pattern 选择器
 * - 中部：DeclarativeForm 或 LuaForm（按 pattern）
 * - 兼容场景：node.action 引用的 ActionDef 不存在时，自动新建一个空 ActionDef
 *
 * 由 NodeEditorDrawer 在 node.type === 'action' 时调用。
 */

import { Alert, Collapse, Form, Input, Space, Switch } from 'antd';
import { useMemo } from 'react';
import { useFlowStore } from '../../store/flowStore';
import { PatternSelector } from './PatternSelector';
import { DeclarativeForm } from './DeclarativeForm';
import { LuaForm } from './LuaForm';
import { ListenRefsTable } from '../../callbacks/ListenRefsTable';
import { DelayInput } from '../shared/DelayInput';
import { SaveTemplateButton } from '../../library/SaveTemplateButton';
import type { ActionDef, ActionPattern } from '@/types/action';

export interface ActionEditorProps {
  nodeId: string;
}

export function ActionEditor({ nodeId }: ActionEditorProps) {
  const node = useFlowStore((s) => s.nodes[nodeId]);
  const actions = useFlowStore((s) => s.actions);
  const updateNode = useFlowStore((s) => s.updateNode);
  const addAction = useFlowStore((s) => s.addAction);
  const updateAction = useFlowStore((s) => s.updateAction);
  const renameAction = useFlowStore((s) => s.renameAction);

  const actionName = node?.action ?? '';
  const action = actions[actionName];

  // 若 action 引用了不存在的定义，本期允许在编辑器里"现场补一个空 action"
  const effectiveAction = useMemo<ActionDef>(() => {
    if (action) return action;
    return { pattern: 'tcpSend' as ActionPattern };
  }, [action]);

  if (!node) return null;

  const onActionNameChange = (newName: string) => {
    if (!newName) return;
    if (action) {
      // 已有 ActionDef → 重命名
      renameAction(actionName, newName);
    } else {
      // 仅是 node 字段引用变更
      updateNode(nodeId, { action: newName });
    }
  };

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

  return (
    <div>
      <Form layout="vertical" style={{ marginBottom: 8 }}>
        <Form.Item label="action 名（actions 表 key）">
          <Input
            value={actionName}
            onChange={(e) => onActionNameChange(e.target.value)}
            placeholder="如 PlayerLogin"
          />
        </Form.Item>
        <Form.Item label="pattern">
          <Space>
            <PatternSelector value={effectiveAction.pattern} onChange={onPatternChange} />
            <SaveTemplateButton kind="action" name={actionName || nodeId} data={effectiveAction} />
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

      {effectiveAction.pattern === 'lua' ? (
        <LuaForm
          mode="action"
          script={effectiveAction.script}
          onChangeScript={(s) => onActionDefChange({ ...effectiveAction, script: s })}
        />
      ) : (
        <DeclarativeForm action={effectiveAction} onChange={onActionDefChange} />
      )}

      <Collapse
        style={{ marginTop: 16 }}
        items={[
          {
            key: 'node',
            label: '节点级字段（breakOff / delayMs）',
            children: (
              <Form layout="vertical">
                <Form.Item label="breakOff（动作失败时是否中断流程）">
                  <Switch
                    checked={!!node.breakOff}
                    onChange={(v) => updateNode(nodeId, { breakOff: v })}
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
    </div>
  );
}
