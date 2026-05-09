/**
 * 模板编辑器（独立 Drawer）：直接编辑 IndexedDB 中的模板本体（不触碰 flow.actions/callbacks）。
 *
 * 触发：在 NodePalette 中双击模板，或右键菜单选择「编辑模板」。
 *
 * 视觉与画布节点的编辑面板保持一致：
 *   - action 模板：PatternSelector + DeclarativeForm / LuaForm
 *   - callback 模板：Tabs(silent / declarative / lua)
 *
 * 保存：updateActionTemplate / updateCallbackTemplate，写回 IndexedDB 并广播 template-change。
 */

import { useEffect, useState } from 'react';
import { Alert, Button, Drawer, Input, Space, Tabs, Tag, message } from 'antd';
import { useShallow } from 'zustand/react/shallow';
import { useEditorStore } from '../store/editorStore';
import { PatternSelector } from '../editors/ActionEditor/PatternSelector';
import { DeclarativeForm } from '../editors/ActionEditor/DeclarativeForm';
import { LuaForm } from '../editors/ActionEditor/LuaForm';
import { StoreTable } from '../editors/ActionEditor/StoreTable';
import { ProtoBrowser } from '../proto/ProtoBrowser';
import {
  getActionTemplate,
  getCallbackTemplate,
  updateActionTemplate,
  updateCallbackTemplate,
  type ActionTemplate,
  type CallbackTemplate,
} from './templateStore';
import type { ActionDef, ActionPattern } from '@/types/action';
import type { CallbackDef } from '@/types/callback';
import { classifyCallback, type CallbackKind } from '@/types/callback';

export function TemplateEditorDrawer() {
  const { activePanel, setActivePanel } = useEditorStore(
    useShallow((s) => ({ activePanel: s.activePanel, setActivePanel: s.setActivePanel })),
  );
  const open = activePanel.kind === 'templateEdit';
  const templateKind = open ? activePanel.templateKind : null;
  const templateId = open ? activePanel.templateId : null;

  const [tpl, setTpl] = useState<ActionTemplate | CallbackTemplate | null>(null);
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [actionDef, setActionDef] = useState<ActionDef | null>(null);
  const [callbackDef, setCallbackDef] = useState<CallbackDef | null>(null);
  const [callbackKind, setCallbackKind] = useState<CallbackKind>('silent');

  useEffect(() => {
    if (!open || !templateKind || !templateId) {
      setTpl(null);
      return;
    }
    const fetcher = templateKind === 'action' ? getActionTemplate : getCallbackTemplate;
    void fetcher(templateId).then((t) => {
      if (!t) {
        message.error('模板不存在或已被删除');
        setActivePanel({ kind: 'none' });
        return;
      }
      setTpl(t);
      setName(t.name);
      setDescription(t.description ?? '');
      if (templateKind === 'action') {
        setActionDef((t as ActionTemplate).data);
      } else {
        const cb = (t as CallbackTemplate).data;
        setCallbackDef(cb);
        setCallbackKind(classifyCallback(cb));
      }
    });
  }, [open, templateKind, templateId, setActivePanel]);

  const onSwitchCallbackKind = (next: CallbackKind) => {
    if (next === callbackKind) return;
    let restored: CallbackDef;
    if (next === 'silent') restored = {};
    else if (next === 'declarative') restored = { s2cProto: '', store: [] };
    else restored = { script: '' };
    setCallbackDef(restored);
    setCallbackKind(next);
  };

  const onSave = async () => {
    if (!tpl || !templateKind) return;
    const trimName = name.trim() || tpl.name;
    if (templateKind === 'action' && actionDef) {
      await updateActionTemplate({
        ...(tpl as ActionTemplate),
        name: trimName,
        description: description.trim() || undefined,
        pattern: actionDef.pattern,
        data: actionDef,
      });
    } else if (templateKind === 'callback' && callbackDef) {
      await updateCallbackTemplate({
        ...(tpl as CallbackTemplate),
        name: trimName,
        description: description.trim() || undefined,
        kind: callbackKind,
        data: callbackDef,
      });
    }
    message.success('已保存');
    setActivePanel({ kind: 'none' });
  };

  return (
    <Drawer
      open={open}
      title={
        tpl ? (
          <Space>
            <Tag color={templateKind === 'action' ? 'magenta' : 'blue'}>
              {templateKind === 'action' ? 'action 模板' : 'callback 模板'}
            </Tag>
            <span>{tpl.name}</span>
          </Space>
        ) : (
          '模板编辑器'
        )
      }
      width={760}
      onClose={() => setActivePanel({ kind: 'none' })}
      destroyOnHidden
      footer={
        <Space style={{ width: '100%', justifyContent: 'flex-end' }}>
          <Button onClick={() => setActivePanel({ kind: 'none' })}>取消</Button>
          <Button type="primary" onClick={onSave} disabled={!tpl}>
            保存
          </Button>
        </Space>
      }
    >
      {tpl && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <Alert
            type="info"
            showIcon
            message="模板编辑"
            description="此处修改的是模板本身（IndexedDB），保存后只对今后从模板创建的节点生效；当前流程已使用的副本不受影响。"
          />
          <div>
            <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginBottom: 4 }}>名称</div>
            <Input value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div>
            <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginBottom: 4 }}>描述（可选）</div>
            <Input.TextArea rows={2} value={description} onChange={(e) => setDescription(e.target.value)} />
          </div>

          {templateKind === 'action' && actionDef && (
            <ActionTemplateBody def={actionDef} onChange={setActionDef} />
          )}

          {templateKind === 'callback' && callbackDef && (
            <CallbackTemplateBody
              def={callbackDef}
              kind={callbackKind}
              onChangeDef={setCallbackDef}
              onChangeKind={onSwitchCallbackKind}
            />
          )}
        </div>
      )}
    </Drawer>
  );
}

function ActionTemplateBody({ def, onChange }: { def: ActionDef; onChange: (d: ActionDef) => void }) {
  const onPatternChange = (p: ActionPattern) => onChange({ ...def, pattern: p });
  return (
    <div>
      <div style={{ marginBottom: 8 }}>
        <span style={{ marginRight: 8, color: 'var(--text-secondary)' }}>pattern:</span>
        <Space>
          <PatternSelector value={def.pattern} onChange={onPatternChange} />
          <Tag color="blue">{def.pattern}</Tag>
        </Space>
      </div>
      {def.pattern === 'lua' ? (
        <LuaForm
          mode="action"
          script={def.script}
          onChangeScript={(s) => onChange({ ...def, script: s })}
        />
      ) : (
        <DeclarativeForm action={def} onChange={onChange} />
      )}
    </div>
  );
}

function CallbackTemplateBody({
  def,
  kind,
  onChangeDef,
  onChangeKind,
}: {
  def: CallbackDef;
  kind: CallbackKind;
  onChangeDef: (d: CallbackDef) => void;
  onChangeKind: (k: CallbackKind) => void;
}) {
  const [protoOpen, setProtoOpen] = useState(false);
  return (
    <div>
      <Tabs
        activeKey={kind}
        onChange={(k) => onChangeKind(k as CallbackKind)}
        items={[
          {
            key: 'silent',
            label: 'silent',
            children: (
              <Alert
                type="info"
                showIcon
                message="静默消费：收到推送后不执行任何逻辑，静默处理。"
              />
            ),
          },
          {
            key: 'declarative',
            label: 'declarative',
            children: (
              <div>
                <Alert
                  type="info"
                  showIcon
                  message="声明式回调：指定 s2cProto + store 映射，自动解析推送消息并存入 state。"
                  style={{ marginBottom: 12 }}
                />
                <div style={{ marginBottom: 12 }}>
                  <span style={{ marginRight: 8, color: 'var(--text-secondary)' }}>s2cProto:</span>
                  <Space.Compact style={{ width: '70%' }}>
                    <Input
                      value={def.s2cProto ?? ''}
                      onChange={(e) => onChangeDef({ ...def, s2cProto: e.target.value })}
                      placeholder="如 Game.MainStateUpdateS2C"
                    />
                    <Button onClick={() => setProtoOpen(true)}>浏览</Button>
                  </Space.Compact>
                </div>
                <StoreTable
                  s2cProto={def.s2cProto}
                  value={def.store}
                  onChange={(v) => onChangeDef({ ...def, store: v })}
                />
              </div>
            ),
          },
          {
            key: 'lua',
            label: 'lua',
            children: (
              <LuaForm
                mode="callback"
                script={def.script}
                onChangeScript={(s) => onChangeDef({ ...def, script: s })}
              />
            ),
          },
        ]}
      />
      <ProtoBrowser
        open={protoOpen}
        onClose={() => setProtoOpen(false)}
        onSelect={(fullName) => {
          onChangeDef({ ...def, s2cProto: fullName });
          setProtoOpen(false);
        }}
      />
    </div>
  );
}
