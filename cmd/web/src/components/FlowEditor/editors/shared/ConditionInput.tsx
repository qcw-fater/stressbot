/**
 * 条件表达式输入框：三模式（lua: / state: / plain）。
 *
 * 设计文档 §6.5：condition 字段在 boolean / loop 节点中复用，
 *   - "lua:check.lua"        → 调 Lua 脚本求值（入口 execute(r) 必须 return true / false）
 *   - "state:foo"            → 读 state 中布尔值
 *   - "state:foo > 5"        → 表达式（保留扩展，引擎当前未实现）
 *
 * lua 模式下旁边的「编辑」按钮会弹出 LuaForm（mode='boolean'），
 * 给条件脚本提供与动作脚本同款的 Monaco 体验，但模板默认 `return false`，
 * 避免与 action 脚本的 `return code, send, recv` 三元约定混淆。
 * 内容会存到 IDB，启动任务时 taskActions.collectScripts 一并上传，避免
 * "脚本未预编译" 错误。
 */

import { Button, Input, Modal, Radio } from 'antd';
import { EditOutlined } from '@ant-design/icons';
import { useMemo, useState } from 'react';
import { LuaForm } from '../ActionEditor/LuaForm';

export interface ConditionInputProps {
  value?: string;
  onChange?: (v: string) => void;
  placeholder?: string;
}

export function ConditionInput({ value, onChange, placeholder }: ConditionInputProps) {
  const mode = useMemo<'lua' | 'state' | 'plain'>(() => {
    if (value?.startsWith('lua:')) return 'lua';
    if (value?.startsWith('state:')) return 'state';
    return 'plain';
  }, [value]);

  const tail = value
    ? value.startsWith('lua:')
      ? value.slice(4)
      : value.startsWith('state:')
        ? value.slice(6)
        : value
    : '';

  const [editorOpen, setEditorOpen] = useState(false);

  const setMode = (next: 'lua' | 'state' | 'plain') => {
    if (next === mode) return;
    let prefix = '';
    if (next === 'lua') prefix = 'lua:';
    else if (next === 'state') prefix = 'state:';
    onChange?.(prefix + tail);
  };

  const setTail = (t: string) => {
    let prefix = '';
    if (mode === 'lua') prefix = 'lua:';
    else if (mode === 'state') prefix = 'state:';
    onChange?.(prefix + t);
  };

  const tip = (
    {
      lua: (
        <>
          <b>lua:</b> 调用 <code>conf/scripts/</code> 下的脚本求值。
          入口 <code>function execute(r)</code>，必须 <code>return true / false</code>
          （返回其它类型直接报错）。点旁边的 <b>编辑</b> 按钮可在 Monaco 里直接写脚本，
          内容会存到本地 IDB，启动任务时随 multipart 一并提交。
        </>
      ),
      state: (
        <>
          <b>state:</b> 读 state 中布尔字段（同名 key 直接取值）。
          示例：<code>state:matchSuccess</code>；保留扩展形式 <code>state:hp &gt; 0</code>（引擎暂未实现，谨慎使用）。
        </>
      ),
      plain: (
        <>
          <b>原文</b>：不附加前缀，原样写入。仅适合自定义引擎或临时占位（不会被引擎识别为 lua/state）。
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
          <Radio.Button value="plain">原文</Radio.Button>
        </Radio.Group>
        <Input
          value={tail}
          onChange={(e) => setTail(e.target.value)}
          placeholder={placeholder ?? (mode === 'lua' ? '脚本文件名（如 check_role.lua）' : 'state 表达式')}
          style={{ flex: 1 }}
        />
        {mode === 'lua' && (
          <Button
            icon={<EditOutlined />}
            onClick={() => setEditorOpen(true)}
            disabled={!tail.trim()}
            title={!tail.trim() ? '先填写脚本文件名再编辑' : '在 Monaco 里编辑该脚本内容'}
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

      {/* lua 脚本编辑 Modal：复用 LuaForm 但用 mode='boolean'，
          这样模板和签名提示都对应 return true / false，避免误用 action 的三元返回。 */}
      <Modal
        open={editorOpen}
        title={
          <span>
            编辑条件脚本 <code style={{ color: 'var(--text-secondary)' }}>{tail || '(未命名)'}</code>
          </span>
        }
        onCancel={() => setEditorOpen(false)}
        footer={[
          <Button key="ok" type="primary" onClick={() => setEditorOpen(false)}>
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
        />
      </Modal>
    </div>
  );
}
