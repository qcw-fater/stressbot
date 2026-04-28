/**
 * 条件表达式输入框：双模式（lua: / state:）。
 *
 * 设计文档 §6.5：condition 字段在 boolean / loop 节点中复用，
 *   - "lua:check.lua"        → 调 Lua 脚本求值（脚本应 return true/false）
 *   - "state:foo"            → 读 state 中布尔值
 *   - "state:foo > 5"        → 表达式（保留扩展，引擎当前未实现）
 */

import { Input, Radio } from 'antd';
import { useMemo } from 'react';

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
          <b>lua:</b> 调用 <code>conf/scripts/</code> 下的脚本求值，脚本须 <code>return true/false</code>。
          示例：<code>lua:check_role.lua</code>
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
    </div>
  );
}
