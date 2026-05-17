/**
 * 条件表达式输入框：双模式（lua: / state:）。
 *
 * 组合 StateExprInput + LuaScriptField，用于 boolean / loop 节点。
 * value 格式：
 *   - "lua:check.lua"  → lua 模式
 *   - "state:hp > 0"   → state 模式
 */

import { Radio } from 'antd';
import { useMemo } from 'react';
import { StateExprInput } from './StateExprInput';
import { LuaScriptField } from './LuaScriptField';

export interface ConditionInputProps {
  value?: string;
  onChange?: (v: string) => void;
  placeholder?: string;
}

export function ConditionInput({ value, onChange, placeholder }: ConditionInputProps) {
  const mode = useMemo<'lua' | 'state'>(() => {
    if (value?.startsWith('lua:')) return 'lua';
    return 'state';
  }, [value]);

  const tail = value
    ? value.startsWith('lua:')
      ? value.slice(4)
      : value.startsWith('state:')
        ? value.slice(6)
        : value
    : '';

  const setMode = (next: 'lua' | 'state') => {
    if (next === mode) return;
    if (next === 'lua') {
      onChange?.('lua:');
    } else {
      onChange?.('state:');
    }
  };

  return (
    <div style={{ width: '100%' }}>
      <div style={{ display: 'flex', gap: 8, alignItems: 'flex-start', width: '100%' }}>
        <Radio.Group value={mode} onChange={(e) => setMode(e.target.value)} buttonStyle="solid">
          <Radio.Button value="lua">lua:</Radio.Button>
          <Radio.Button value="state">state:</Radio.Button>
        </Radio.Group>
        <div style={{ flex: 1 }}>
          {mode === 'state' ? (
            <StateExprInput
              value={tail}
              onChange={(v) => onChange?.(v ? 'state:' + v : '')}
              placeholder={placeholder}
            />
          ) : (
            <LuaScriptField
              mode="boolean"
              value={tail}
              onChange={(v) => onChange?.(v ? 'lua:' + v : '')}
            />
          )}
        </div>
      </div>
    </div>
  );
}
