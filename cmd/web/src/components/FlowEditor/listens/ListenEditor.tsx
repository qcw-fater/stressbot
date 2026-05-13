/**
 * ListenEditor 浮动窗口：编辑单个 ListenDef，三态切换。
 *
 *   silent       : 空对象 {}，收到推送后静默处理
 *   declarative  : s2cProto + store
 *   lua          : script（可选 s2cProto）
 *
 * 切换形态时弹确认；切回 30 秒内的形态自动恢复字段（避免误清）。
 * 还原按钮可恢复到打开时的快照；关闭时 lua 有未保存改动会弹确认。
 */

import { Alert, App as AntApp, AutoComplete, Button, Input, Modal, Popconfirm, Space, Tabs, Tag } from 'antd';
import { UndoOutlined, ApiOutlined, EditOutlined } from '@ant-design/icons';
import { useEffect, useMemo, useRef, useState } from 'react';
import type { ListenDef } from '@/types/listen';
import { classifyListen, type ListenKind } from '@/types/listen';
import { useEditorStore } from '../store/editorStore';
import { useFlowStore } from '../store/flowStore';
import { useFloatingWindowStore } from '../store/floatingWindowStore';
import { ProtoBrowser } from '../proto/ProtoBrowser';
import { StoreTable } from '../editors/ActionEditor/StoreTable';
import { LuaForm } from '../editors/ActionEditor/LuaForm';
import { BackrefList } from './BackrefList';
import { SaveTemplateButton } from '../library/SaveTemplateButton';
import { listenKindTagColor } from './listenKindStyle';
import { FloatingWindow } from '../panels/FloatingWindow';

export function ListenEditor() {
  const { message: messageApi, modal } = AntApp.useApp();
  const activePanel = useEditorStore((s) => s.activePanel.listenEdit);
  const setActivePanel = useEditorStore((s) => s.setActivePanel);
  const closePanel = useEditorStore((s) => s.closePanel);
  const listenName = activePanel?.kind === 'listenEdit' ? activePanel.listenName : null;

  const listen = useFlowStore((s) => (listenName ? s.listens[listenName] : undefined));
  const updateListen = useFlowStore((s) => s.updateListen);
  const replaceListen = useFlowStore((s) => s.replaceListen);
  const renameListen = useFlowStore((s) => s.renameListen);

  const [draftName, setDraftName] = useState(listenName ?? '');
  const [protoOpen, setProtoOpen] = useState(false);
  const [luaDirty, setLuaDirty] = useState(false);
  const [editorOpen, setEditorOpen] = useState(false);
  const [files, setFiles] = useState<string[]>([]);
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;
  // 形态切换时把当前字段缓存进 stash，便于切回恢复
  const [stash, setStash] = useState<Record<ListenKind, ListenDef>>({
    silent: {},
    declarative: {},
    lua: {},
  });
  // 本地 selectedKind：用户在 Tabs 上的当前选择。
  // 初始值 / listenName 切换时由 classifyListen 推断；之后由用户切换驱动，
  // 不再被 listen 的字段形态反向干扰（避免 declarative 形态下 s2cProto='' + store=[] 又被判回 silent）。
  const [selectedKind, setSelectedKind] = useState<ListenKind>(() => classifyListen(listen));

  // === 还原快照 ===
  const snapshotRef = useRef<{ name: string; def: ListenDef } | null>(null);

  useEffect(() => {
    setDraftName(listenName ?? '');
    setLuaDirty(false);
    if (listenName && listen) {
      setSelectedKind(classifyListen(listen));
      snapshotRef.current = { name: listenName, def: JSON.parse(JSON.stringify(listen)) };
    } else {
      snapshotRef.current = null;
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [listenName]);

  // 拉取脚本列表（给 lua 模式的 AutoComplete 用）
  useEffect(() => {
    let cancel = false;
    fetch('/conf/scripts/index.json')
      .then((r) => (r.ok ? r.json() : []))
      .then((list: string[]) => {
        if (!cancel) setFiles(list);
      })
      .catch(() => undefined);
    return () => { cancel = true; };
  }, []);

  // dirty 判断：当前 listen 与快照不一致
  const listenDirty = useMemo(() => {
    if (!snapshotRef.current || !listen) return false;
    return JSON.stringify(listen) !== JSON.stringify(snapshotRef.current.def);
  }, [listen]);

  const onRevert = () => {
    const s = snapshotRef.current;
    if (!s) return;
    replaceListen(listenName!, JSON.parse(JSON.stringify(s.def)));
    setSelectedKind(classifyListen(s.def));
    messageApi.success('已还原本次打开后的所有修改');
  };

  if (!listenName || !listen) {
    return null;
  }

  const switchKind = (next: ListenKind) => {
    if (next === selectedKind) return;
    // 缓存当前形态的字段（切回时恢复）
    setStash((s) => ({ ...s, [selectedKind]: { ...listen } }));
    const restored: ListenDef =
      stash[next] && Object.keys(stash[next]).length > 0
        ? stash[next]
        : next === 'silent'
          ? {}
          : next === 'declarative'
            ? { s2cProto: '', store: [] }
            : { script: '' };
    // 必须用 replace 而非 update：partial merge 会保留 script/s2cProto 等旧字段，导致 classifyListen 仍判旧 kind。
    replaceListen(listenName, restored);
    setSelectedKind(next);
  };

  const onApplyRename = () => {
    if (draftName === listenName) return;
    if (!draftName) return;
    if (useFlowStore.getState().listens[draftName]) return;
    renameListen(listenName, draftName);
    setActivePanel({ kind: 'listenEdit', listenName: draftName });
  };

  const handleClose = () => {
    closePanel('listenEdit');
  };

  return (
    <FloatingWindow
      windowId="listenEdit"
      title={
        <Space>
          <Tag color={listenKindTagColor[selectedKind]}>{selectedKind}</Tag>
          <span>编辑 listen</span>
        </Space>
      }
      open={activePanel?.kind === 'listenEdit'}
      onClose={handleClose}
      defaultSize={{ width: 680, height: 520 }}
      minSize={{ width: 500, height: 400 }}
      footer={
        <Space>
          <Popconfirm
            title="还原本次修改"
            description="将该 listen 恢复到本次打开编辑面板时的状态。"
            onConfirm={onRevert}
            disabled={!listenDirty}
          >
            <Button icon={<UndoOutlined />} disabled={!listenDirty}>
              还原修改
            </Button>
          </Popconfirm>
          <Button onClick={handleClose}>
            关闭
          </Button>
        </Space>
      }
    >
      <Space style={{ width: '100%', marginBottom: 12, justifyContent: 'space-between' }}>
        <Space.Compact style={{ width: 360 }}>
          <Input addonBefore="名称" value={draftName} onChange={(e) => setDraftName(e.target.value)} />
          <Button onClick={onApplyRename} disabled={draftName === listenName || !draftName}>
            重命名
          </Button>
        </Space.Compact>
        <SaveTemplateButton kind="listen" name={listenName} data={listen} description={listen.description} />
      </Space>

      <Input.TextArea
        value={listen.description ?? ''}
        onChange={(e) => updateListen(listenName, { description: e.target.value })}
        placeholder="可选注释，显示在节点面板上"
        autoSize={{ minRows: 1, maxRows: 3 }}
        style={{ marginBottom: 12 }}
      />

      <Tabs
        activeKey={selectedKind}
        onChange={(k) => switchKind(k as ListenKind)}
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
                  message="声明式监听：指定 s2cProto + store 映射，自动解析推送消息并存入 state。"
                />
                <DeclarativeListenBody
                  value={listen}
                  onChange={(v) => updateListen(listenName, v)}
                  onOpenProto={() => setProtoOpen(true)}
                />
              </>
            ),
          },
          {
            key: 'lua',
            label: 'lua',
            children: (
              <>
                <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 8 }}>
                  <span style={{ fontSize: 12, color: 'var(--text-secondary)', whiteSpace: 'nowrap' }}>脚本文件：</span>
                  <AutoComplete
                    style={{ flex: 1 }}
                    value={listen.script ?? ''}
                    onChange={(v) => updateListen(listenName, { script: v })}
                    options={files.map((f) => ({ value: f, label: f }))}
                    placeholder="输入新文件名或选择已有脚本"
                    allowClear
                    filterOption={(input, option) =>
                      (option?.value as string)?.toLowerCase().includes(input.toLowerCase()) ?? false
                    }
                  />
                  <Button
                    icon={<EditOutlined />}
                    onClick={() => setEditorOpen(true)}
                    disabled={!listen.script?.trim()}
                    title={!listen.script?.trim() ? '先填写脚本文件名再编辑' : '在编辑器里编辑该脚本内容'}
                  >
                    编辑
                  </Button>
                </div>
                <div style={{ fontSize: 11, color: 'var(--text-tertiary)', lineHeight: 1.5 }}>
                  入口 <code>function onMessage(r, msg)</code>，
                  未指定响应消息类型时 <code>msg</code> 为原始二进制数据。
                  点旁边的「编辑」按钮可在编辑器里直接写脚本，按 Ctrl+S 保存到本地。
                </div>
              </>
            ),
          },
        ]}
      />

      <div style={{ marginTop: 16, paddingTop: 12, borderTop: '1px solid var(--divider-bg)' }}>
        <BackrefList listenName={listenName} />
      </div>

      <ProtoBrowser
        windowId="protoPicker_listen"
        open={protoOpen}
        onClose={() => setProtoOpen(false)}
        onSelect={(fullName) => {
          updateListen(listenName, { s2cProto: fullName });
          setProtoOpen(false);
        }}
      />

      {/* Lua 脚本编辑 Modal */}
      <Modal
        open={editorOpen}
        title={
          <span>
            编辑监听脚本 <code style={{ color: 'var(--text-secondary)' }}>{listen.script || '(未命名)'}</code>
          </span>
        }
        onCancel={() => {
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
        }}
        footer={[
          <Button key="close" onClick={() => {
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
          }}>
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
          mode="listen"
          script={listen.script}
          onChangeScript={(s) => updateListen(listenName, { script: s })}
          onDirtyChange={setLuaDirty}
        />
        </div>
      </Modal>
    </FloatingWindow>
  );
}

function DeclarativeListenBody({
  value,
  onChange,
  onOpenProto,
}: {
  value: ListenDef;
  onChange: (v: ListenDef) => void;
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
          <Button icon={<ApiOutlined />} onClick={onOpenProto} />
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
