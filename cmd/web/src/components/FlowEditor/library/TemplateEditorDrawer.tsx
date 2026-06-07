/**
 * 模板编辑器（独立 Drawer）：直接编辑 IndexedDB 中的模板本体（不触碰 flow.actions/listens）。
 *
 * 触发：在 NodePalette 中双击模板，或右键菜单选择「编辑模板」。
 *
 * 视觉与画布节点的编辑面板保持一致：
 *   - action 模板：PatternSelector + DeclarativeForm / LuaForm
 *   - listen 模板：Tabs(silent / declarative / lua)
 *
 * 保存：updateActionTemplate / updateListenTemplate，写回 IndexedDB 并广播 template-change。
 */

import { useEffect, useState } from 'react';
import { Alert, App as AntApp, Button, Input, Space, Tabs, Tag } from 'antd';
import { useShallow } from 'zustand/react/shallow';
import { useEditorStore } from '../store/editorStore';
import { PatternSelector } from '../editors/ActionEditor/PatternSelector';
import { DeclarativeForm } from '../editors/ActionEditor/DeclarativeForm';
import { LuaForm } from '../editors/ActionEditor/LuaForm';
import { StoreTable } from '../editors/ActionEditor/StoreTable';
import { ProtoBrowser } from '../proto/ProtoBrowser';
import {
  getActionTemplate,
  getListenTemplate,
  updateActionTemplate,
  updateListenTemplate,
  type ActionTemplate,
  type ListenTemplate,
  type ListenTemplateDefaultRef,
} from './templateStore';
import type { ActionDef, ActionPattern } from '@/types/action';
import type { ListenDef } from '@/types/listen';
import { classifyListen, type ListenKind } from '@/types/listen';
import { FloatingWindow } from '../panels/FloatingWindow';
import { RouteEditor } from '../listens/RouteEditor';
import { cloneListenDefaultRef } from './listenTemplateDefaults';

export function TemplateEditorDrawer() {
  const { message } = AntApp.useApp();
  const { activePanel, closePanel } = useEditorStore(
    useShallow((s) => ({ activePanel: s.activePanel.templateEdit, closePanel: s.closePanel })),
  );
  const open = activePanel?.kind === 'templateEdit';
  const templateKind = open ? activePanel.templateKind : null;
  const templateId = open ? activePanel.templateId : null;

  const [tpl, setTpl] = useState<ActionTemplate | ListenTemplate | null>(null);
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [actionDef, setActionDef] = useState<ActionDef | null>(null);
  const [listenDef, setListenDef] = useState<ListenDef | null>(null);
  const [listenKind, setListenKind] = useState<ListenKind>('silent');
  const [listenDefaultRef, setListenDefaultRef] = useState<ListenTemplateDefaultRef | undefined>();

  useEffect(() => {
    if (!open || !templateKind || !templateId) {
      setTpl(null);
      return;
    }
    const fetcher = templateKind === 'action' ? getActionTemplate : getListenTemplate;
    void fetcher(templateId).then((t) => {
      if (!t) {
        message.error('模板不存在或已被删除');
        closePanel('templateEdit');
        return;
      }
      setTpl(t);
      setName(t.name);
      setDescription(t.description ?? '');
      if (templateKind === 'action') {
        setActionDef((t as ActionTemplate).data);
      } else {
        const listenTpl = t as ListenTemplate;
        const cb = listenTpl.data;
        setListenDef(cb);
        setListenKind(classifyListen(cb));
        setListenDefaultRef(cloneListenDefaultRef(listenTpl.defaultRef));
      }
    });
  }, [open, templateKind, templateId, closePanel]);

  const onSwitchListenKind = (next: ListenKind) => {
    if (next === listenKind) return;
    let restored: ListenDef;
    if (next === 'silent') restored = {};
    else if (next === 'declarative') restored = { s2cProto: '', store: [] };
    else restored = { script: '' };
    setListenDef(restored);
    setListenKind(next);
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
    } else if (templateKind === 'listen' && listenDef) {
      await updateListenTemplate({
        ...(tpl as ListenTemplate),
        name: trimName,
        description: description.trim() || undefined,
        kind: listenKind,
        data: listenDef,
        defaultRef: cloneListenDefaultRef(listenDefaultRef),
      });
    }
    message.success('已保存');
    closePanel('templateEdit');
  };

  return (
    <FloatingWindow
      windowId="templateEdit"
      open={open}
      title={
        tpl ? (
          <Space>
            <Tag color={templateKind === 'action' ? 'magenta' : 'blue'}>
              {templateKind === 'action' ? 'action 模板' : 'listen 模板'}
            </Tag>
            <span>{tpl.name}</span>
          </Space>
        ) : (
          '模板编辑器'
        )
      }
      defaultSize={{ width: 720, height: 560 }}
      minSize={{ width: 400, height: 350 }}
      onClose={() => closePanel('templateEdit')}
      footer={
        <Space style={{ width: '100%', justifyContent: 'flex-end' }}>
          <Button onClick={() => closePanel('templateEdit')}>取消</Button>
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
            description="此处修改的是模板本身，保存后只对今后从模板创建的节点生效；当前流程已使用的副本不受影响。"
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

          {templateKind === 'listen' && listenDef && (
            <ListenTemplateBody
              def={listenDef}
              kind={listenKind}
              onChangeDef={setListenDef}
              onChangeKind={onSwitchListenKind}
              defaultRef={listenDefaultRef}
              onChangeDefaultRef={setListenDefaultRef}
            />
          )}
        </div>
      )}
    </FloatingWindow>
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

function ListenTemplateBody({
  def,
  kind,
  onChangeDef,
  onChangeKind,
  defaultRef,
  onChangeDefaultRef,
}: {
  def: ListenDef;
  kind: ListenKind;
  onChangeDef: (d: ListenDef) => void;
  onChangeKind: (k: ListenKind) => void;
  defaultRef?: ListenTemplateDefaultRef;
  onChangeDefaultRef: (ref?: ListenTemplateDefaultRef) => void;
}) {
  const [protoOpen, setProtoOpen] = useState(false);
  return (
    <div>
      <div style={{ marginBottom: 12, padding: 12, border: '1px solid var(--border-color)', borderRadius: 8 }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 8 }}>
          <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>默认注册</span>
          {defaultRef ? (
            <Button size="small" onClick={() => onChangeDefaultRef(undefined)}>清除</Button>
          ) : (
            <Button size="small" onClick={() => onChangeDefaultRef({ server: '', route: { cmd: 0, act: 0 } })}>添加</Button>
          )}
        </div>
        {defaultRef ? (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            <Input
              value={defaultRef.server}
              onChange={(e) => onChangeDefaultRef({ ...defaultRef, server: e.target.value })}
              placeholder="如 tcp:logic / udp:battle"
            />
            <RouteEditor
              value={defaultRef.route}
              onChange={(route) => onChangeDefaultRef({ ...defaultRef, route })}
            />
          </div>
        ) : (
          <Alert type="info" showIcon message="未设置默认注册；从 action 连到该 listen 时仍需手动填写 server 和 route。" />
        )}
      </div>
      <Tabs
        activeKey={kind}
        onChange={(k) => onChangeKind(k as ListenKind)}
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
                mode="listen"
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
