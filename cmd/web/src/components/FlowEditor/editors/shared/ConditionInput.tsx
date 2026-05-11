/**
 * 条件表达式输入框：双模式（lua: / state:）。
 *
 * condition 字段在 boolean / loop 节点中复用，
 *   - "lua:check.lua"        → 调 Lua 脚本求值（入口 execute(r) 必须 return true / false）
 *   - "foo"                  → 读 state 中布尔值（state: 前缀自动添加）
 *   - "foo > 5"              → 表达式比较（支持 >= <= != == > <）
 *   - "a && b || !c"         → 复合条件（支持 && || ! 和括号）
 *
 * lua 模式下旁边的「编辑」按钮会弹出 LuaForm（mode='boolean'），
 * 给条件脚本提供与动作脚本同款的 Monaco 体验，但模板默认 `return false`，
 * 避免与 action 脚本的 `return code, send, recv` 三元约定混淆。
 * 关闭弹窗时若有未保存改动会弹确认。
 */

import { App as AntApp, Button, Input, Modal, Radio } from 'antd';
import { EditOutlined } from '@ant-design/icons';
import { useMemo, useState } from 'react';
import { LuaForm } from '../ActionEditor/LuaForm';

export interface ConditionInputProps {
  value?: string;
  onChange?: (v: string) => void;
  placeholder?: string;
}

export function ConditionInput({ value, onChange, placeholder }: ConditionInputProps) {
  const { modal } = AntApp.useApp();
  const mode = useMemo<'lua' | 'state'>(() => {
    if (value?.startsWith('lua:')) return 'lua';
    return 'state';
  }, [value]);

  const tail = value
    ? value.startsWith('lua:')
      ? value.slice(4)
      : value.startsWith('state:')
        ? value.slice(6)
        : ''
    : '';

  const [editorOpen, setEditorOpen] = useState(false);
  const [luaDirty, setLuaDirty] = useState(false);

  const setMode = (next: 'lua' | 'state') => {
    if (next === mode) return;
    if (next === 'lua') {
      onChange?.('lua:');
    } else {
      onChange?.('state:');
    }
  };

  const setTail = (t: string) => {
    let prefix = '';
    if (mode === 'lua') prefix = 'lua:';
    else if (mode === 'state') prefix = 'state:';
    onChange?.(prefix + t);
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

  const tip = (
    {
      lua: (
        <>
          <b>lua:</b> 调用 <code>conf/scripts/</code> 下的脚本求值。
          入口 <code>function execute(r)</code>，必须 <code>return true / false</code>
          （返回其它类型直接报错）。点旁边的 <b>编辑</b> 按钮可在编辑器里直接写脚本，
          按 Ctrl+S 保存到本地。
        </>
      ),
      state: (
        <>
          <b>state:</b> 前缀由系统自动添加，直接写表达式即可。
          读布尔字段：<code>matchSuccess</code>；比较：<code>hp &gt; 0</code>；
          复合条件 <code>&amp;&amp;</code> <code>||</code> <code>!</code> 和括号：
          <code>hp &gt; 0 &amp;&amp; (alive || isAdmin)</code>
        </>
      ),
    } as const
  )[mode];

  return (
    <div style={{ width: '100%' }}>
      {/* flex + 同一 size，避免 Space.Compact 中 Radio.Group(small) 与 Input(default) 高度错位 */}
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', width: '100%' }}>
        <Radio.Group value={mode} onChange={(e) => setMode(e.target.value)} buttonStyle="solid">
          <Radio.Button value="lua">lua:</Radio.Button>
          <Radio.Button value="state">state:</Radio.Button>
        </Radio.Group>
        <Input
          value={tail}
          onChange={(e) => setTail(e.target.value)}
          placeholder={placeholder ?? (mode === 'lua' ? '脚本文件名（如 check_role.lua）' : '如 hp > 0 && alive')}
          style={{ flex: 1 }}
        />
        {mode === 'lua' && (
          <Button
            icon={<EditOutlined />}
            onClick={() => setEditorOpen(true)}
            disabled={!tail.trim()}
            title={!tail.trim() ? '先填写脚本文件名再编辑' : '在编辑器里编辑该脚本内容'}
          >
            编辑
          </Button>
        )}
      </div>
      <div
        style={{
          marginTop: 4,
          fontSize: 11,
          color: 'var(--text-tertiary)',
          lineHeight: 1.5,
        }}
      >
        {tip}
      </div>

      {/* lua 脚本编辑 Modal */}
      <Modal
        open={editorOpen}
        title={
          <span>
            编辑条件脚本 <code style={{ color: 'var(--text-secondary)' }}>{tail || '(未命名)'}</code>
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
      >
        <LuaForm
          mode="boolean"
          script={tail}
          onChangeScript={(s) => setTail(s)}
          onDirtyChange={setLuaDirty}
        />
      </Modal>
    </div>
  );
}
