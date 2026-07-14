/**
 * State key 自动补全输入框。
 *
 * 候选数据源由 useStateKeyOptions 统一加载（flow graph + 启动配置 + 异步 Lua 脚本），
 * 本组件只负责搜索过滤、展示（StateKeyOptionLabel）与选中后回传 s2cProto。
 * currentBindings 用于补全当前编辑中、尚未落盘的 storeAs 中间值。
 */

import { AutoComplete } from 'antd';
import { useMemo, useState } from 'react';
import type { FieldBind } from '@/types/action';
import { resolveProtoForStateKey } from './stateRegistry';
import { useStateKeyOptions } from './useStateKeyOptions';
import { StateKeyOptionLabel } from './stateKeyPresentation';

export interface StateKeyInputProps {
  value?: string;
  onChange?: (v: string) => void;
  onProtoResolved?: (proto: string | undefined) => void;
  currentBindings?: FieldBind[];
  placeholder?: string;
  style?: React.CSSProperties;
}

export function StateKeyInput({
  value,
  onChange,
  onProtoResolved,
  currentBindings,
  placeholder,
  style,
}: StateKeyInputProps) {
  const [search, setSearch] = useState('');

  const { keys: allKeys } = useStateKeyOptions(currentBindings);

  const filtered = useMemo(() => {
    if (!search) return allKeys;
    const lower = search.toLowerCase();
    return allKeys.filter((k) => k.key.toLowerCase().includes(lower));
  }, [allKeys, search]);

  const options = filtered.map((k) => ({
    value: k.key,
    label: <StateKeyOptionLabel info={k} />,
  }));

  return (
    <AutoComplete
      value={value ?? ''}
      options={options}
      placeholder={placeholder ?? 'state key（输入搜索已有 key）'}
      style={style ?? { width: 220 }}
      onSearch={(text) => setSearch(text)}
      onChange={(v) => {
        onChange?.(v);
        const proto = resolveProtoForStateKey(allKeys, v);
        onProtoResolved?.(proto);
      }}
      filterOption={false}
      allowClear
    />
  );
}
