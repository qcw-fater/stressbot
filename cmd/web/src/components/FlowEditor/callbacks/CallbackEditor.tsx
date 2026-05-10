/**
 * CallbackEditor 模态框：编辑单个 CallbackDef，三态切换。
 *
 *   silent       : 空对象 {}，收到推送后静默处理
 *   declarative  : s2cProto + store
 *   lua          : script（可选 s2cProto）
 *
 * 切换形态时弹确认；切回 30 秒内的形态自动恢复字段（避免误清）。
 * 还原按钮可恢复到打开时的快照；关闭时 lua 有未保存改动会弹确认。
 */

import { Alert, App as AntApp, Button, Input, Modal, Popconfirm, Space, Tabs, Tag } from 'antd';
import { UndoOutlined } from '@ant-design/icons';
import { useEffect, useMemo, useRef, useState } from 'react';
import type { CallbackDef } from '@/types/callback';
import { classifyCallback, type CallbackKind } from '@/types/callback';
import { useEditorStore } from '../store/editorStore';
import { useFlowStore } from '../store/flowStore';
import { ProtoBrowser } from '../proto/ProtoBrowser';
import { StoreTable } from '../editors/ActionEditor/StoreTable';
import { LuaForm } from '../editors/ActionEditor/LuaForm';
import { BackrefList } from './BackrefList';
import { SaveTemplateButton } from '../library/SaveTemplateButton';
import { callbackKindTagColor } from './callbackKindStyle';

export function CallbackEditor() {
  const { message: messageApi, modal } = AntApp.useApp();
  const activePanel = useEditorStore((s) => s.activePanel);
  const setActivePanel = useEditorStore((s) => s.setActivePanel);
  const callbackName = activePanel.kind === 'callbackEdit' ? activePanel.callbackName : null;

  const callback = useFlowStore((s) => (callbackName ? s.callbacks[callbackName] : undefined));
  const updateCallback = useFlowStore((s) => s.updateCallback);
  const replaceCallback = useFlowStore((s) => s.replaceCallback);
  const renameCallback = useFlowStore((s) => s.renameCallback);

  const [draftName, setDraftName] = useState(callbackName ?? '');
  const [protoOpen, setProtoOpen] = useState(false);
  const [luaDirty, setLuaDirty] = useState(false);
  // 形态切换时把当前字段缓存进 stash，便于切回恢复
  const [stash, setStash] = useState<Record<CallbackKind, CallbackDef>>({
    silent: {},
    declarative: {},
    lua: {},
  });
  // 本地 selectedKind：用户在 Tabs 上的当前选择。
  // 初始值 / callbackName 切换时由 classifyCallback 推断；之后由用户切换驱动，
  // 不再被 callback 的字段形态反向干扰（避免 declarative 形态下 s2cProto='' + store=[] 又被判回 silent）。
  const [selectedKind, setSelectedKind] = useState<CallbackKind>(() => classifyCallback(callback));

  // === 还原快照 ===
  const snapshotRef = useRef<{ name: string; def: CallbackDef } | null>(null);

  useEffect(() => {
    setDraftName(callbackName ?? '');
    setLuaDirty(false);
    if (callbackName && callback) {
      setSelectedKind(classifyCallback(callback));
      snapshotRef.current = { name: callbackName, def: JSON.parse(JSON.stringify(callback)) };
    } else {
      snapshotRef.current = null;
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [callbackName]);

  // dirty 判断：当前 callback 与快照不一致
  const cbDirty = useMemo(() => {
    if (!snapshotRef.current || !callback) return false;
    return JSON.stringify(callback) !== JSON.stringify(snapshotRef.current.def);
  }, [callback]);

  const onRevert = () => {
    const s = snapshotRef.current;
    if (!s) return;
    replaceCallback(callbackName!, JSON.parse(JSON.stringify(s.def)));
    setSelectedKind(classifyCallback(s.def));
    messageApi.success('已还原本次打开后的所有修改');
  };

  if (!callbackName || !callback) {
    return null;
  }

  const switchKind = (next: CallbackKind) => {
    if (next === selectedKind) return;
    // 缓存当前形态的字段（切回时恢复）
    setStash((s) => ({ ...s, [selectedKind]: { ...callback } }));
    const restored: CallbackDef =
      stash[next] && Object.keys(stash[next]).length > 0
        ? stash[next]
        : next === 'silent'
          ? {}
          : next === 'declarative'
            ? { s2cProto: '', store: [] }
            : { script: '' };
    // 必须用 replace 而非 update：partial merge 会保留 script/s2cProto 等旧字段，导致 classifyCallback 仍判旧 kind。
    replaceCallback(callbackName, restored);
    setSelectedKind(next);
  };

  const onApplyRename = () => {
    if (draftName === callbackName) return;
    if (!draftName) return;
    if (useFlowStore.getState().callbacks[draftName]) return;
    renameCallback(callbackName, draftName);
    setActivePanel({ kind: 'callbackEdit', callbackName: draftName });
  };

  const handleClose = () => {
    if (luaDirty) {
      modal.confirm({
        title: '脚本有未保存的改动',
        content: '关闭后未保存的内容将丢失，是否继续？',
        okText: '不保存',
        cancelText: '取消',
        onOk: () => setActivePanel({ kind: 'none' }),
      });
    } else {
      setActivePanel({ kind: 'none' });
    }
  };

  return (
    <Modal
      open
      title={
        <Space>
          <Tag color={callbackKindTagColor[selectedKind]}>{selectedKind}</Tag>
          <span>编辑 callback</span>
        </Space>
      }
      onCancel={handleClose}
      footer={[
        <Popconfirm
          key="revert"
          title="还原本次修改"
          description="将该 callback 恢复到本次打开编辑面板时的状态。"
          onConfirm={onRevert}
          disabled={!cbDirty}
        >
          <Button icon={<UndoOutlined />} disabled={!cbDirty}>
            还原修改
          </Button>
        </Popconfirm>,
        <Button key="close" onClick={handleClose}>
          关闭
        </Button>,
      ]}
      width={760}
    >
      <Space style={{ width: '100%', marginBottom: 12, justifyContent: 'space-between' }}>
        <Space.Compact style={{ width: 360 }}>
          <Input addonBefore="名称" value={draftName} onChange={(e) => setDraftName(e.target.value)} />
          <Button onClick={onApplyRename} disabled={draftName === callbackName || !draftName}>
            重命名
          </Button>
        </Space.Compact>
        <SaveTemplateButton kind="callback" name={callbackName} data={callback} />
      </Space>

      <Input.TextArea
        value={callback.description ?? ''}
        onChange={(e) => updateCallback(callbackName, { description: e.target.value })}
        placeholder="可选注释，显示在回调卡片上，不参与运行时逻辑"
        autoSize={{ minRows: 1, maxRows: 3 }}
        style={{ marginBottom: 12 }}
      />

      <Tabs
        activeKey={selectedKind}
        onChange={(k) => switchKind(k as CallbackKind)}
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
              <>
                <Alert
                  type="info"
                  showIcon
                  message="声明式回调：指定 s2cProto + store 映射，自动解析推送消息并存入 state。"
                />
                <DeclarativeCallbackBody
                  value={callback}
                  onChange={(v) => updateCallback(callbackName, v)}
                  onOpenProto={() => setProtoOpen(true)}
                />
              </>
            ),
          },
          {
            key: 'lua',
            label: 'lua',
            children: (
              <LuaForm
                mode="callback"
                script={callback.script}
                onChangeScript={(s) => updateCallback(callbackName, { script: s })}
                onDirtyChange={setLuaDirty}
              />
            ),
          },
        ]}
      />

      <div style={{ marginTop: 16, paddingTop: 12, borderTop: '1px solid var(--divider-bg)' }}>
        <BackrefList callbackName={callbackName} />
      </div>

      <ProtoBrowser
        open={protoOpen}
        onClose={() => setProtoOpen(false)}
        onSelect={(fullName) => {
          updateCallback(callbackName, { s2cProto: fullName });
          setProtoOpen(false);
        }}
      />
    </Modal>
  );
}

function DeclarativeCallbackBody({
  value,
  onChange,
  onOpenProto,
}: {
  value: CallbackDef;
  onChange: (v: CallbackDef) => void;
  onOpenProto: () => void;
}) {
  return (
    <div>
      <div style={{ marginBottom: 12 }}>
        <span style={{ marginRight: 8, color: 'var(--text-secondary)' }}>s2cProto:</span>
        <Space.Compact style={{ width: '70%' }}>
          <Input
            value={value.s2cProto ?? ''}
            onChange={(e) => onChange({ ...value, s2cProto: e.target.value })}
            placeholder="如 Game.MainStateUpdateS2C"
          />
          <Button onClick={onOpenProto}>浏览</Button>
        </Space.Compact>
      </div>
      <StoreTable
        s2cProto={value.s2cProto}
        value={value.store}
        onChange={(v) => onChange({ ...value, store: v })}
      />
    </div>
  );
}
