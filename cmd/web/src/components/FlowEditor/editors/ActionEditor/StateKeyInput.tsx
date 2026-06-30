/**
 * State key 自动补全输入框。
 *
 * 从 flow graph + 启动配置 + Lua 脚本中收集所有已知的 state key，
 * 提供 AutoComplete 建议。选中后通过 onProtoResolved 回调返回对应的 s2cProto。
 *
 * Lua 脚本只加载 flow 图中实际引用的（actions/listens 中 script 字段），
 * 不扫描本地存储中的全部脚本（模板库、资源列表中可能有未使用的脚本）。
 */

import { AutoComplete, Space, Tag } from 'antd';
import { useEffect, useMemo, useState } from 'react';
import type { FieldBind } from '@/types/action';
import { useFlowStore } from '../../store/flowStore';
import { useRuntimeStore } from '@/services/runtimeStore';
import { getScript } from '@/services/resourcesStore';
import { collectStateKeys, collectUsedScriptNames, resolveProtoForStateKey, resolveStateKeyDisplayType } from './stateRegistry';

export interface StateKeyInputProps {
  value?: string;
  onChange?: (v: string) => void;
  onProtoResolved?: (proto: string | undefined) => void;
  currentBindings?: FieldBind[];
  placeholder?: string;
  style?: React.CSSProperties;
}

const SOURCE_TYPE_LABEL: Record<string, { label: string; color: string }> = {
  store: { label: 'S2C', color: 'blue' },
  listenStore: { label: '推送', color: 'orange' },
  stateExtra: { label: '启动', color: 'volcano' },
  storeAs: { label: '中间值', color: 'green' },
  lua: { label: 'Lua', color: 'purple' },
  builtin: { label: '内置', color: 'cyan' },
};

export function StateKeyInput({
  value,
  onChange,
  onProtoResolved,
  currentBindings,
  placeholder,
  style,
}: StateKeyInputProps) {
  const [search, setSearch] = useState('');

  const actions = useFlowStore((s) => s.actions);
  const listens = useFlowStore((s) => s.listens);
  const nodes = useFlowStore((s) => s.nodes);
  const stateExtra = useRuntimeStore((s) => s.robotConfig.stateExtra);

  // 从 flow graph 推导需要加载的脚本名
  const usedScriptNames = useMemo(
    () => collectUsedScriptNames(actions, listens, nodes),
    [actions, listens, nodes],
  );

  // 异步加载引用到的脚本内容，当脚本名集合变化时重新加载
  const [luaScripts, setLuaScripts] = useState<Array<{ name: string; content: string }>>([]);

  useEffect(() => {
    if (usedScriptNames.size === 0) {
      setLuaScripts([]);
      return;
    }
    let cancelled = false;
    const entries: Array<{ name: string; content: string }> = [];
    let pending = usedScriptNames.size;

    for (const name of usedScriptNames) {
      getScript(name)
        .then((file) => {
          if (!cancelled && file) {
            entries.push({ name: file.name, content: file.content });
          }
        })
        .catch(() => {})
        .finally(() => {
          if (cancelled) return;
          pending--;
          if (pending === 0) {
            setLuaScripts(entries);
          }
        });
    }

    return () => { cancelled = true; };
  }, [usedScriptNames]);

  const allKeys = useMemo(
    () => collectStateKeys(actions, listens, stateExtra, currentBindings, luaScripts),
    [actions, listens, stateExtra, currentBindings, luaScripts],
  );

  const filtered = useMemo(() => {
    if (!search) return allKeys;
    const lower = search.toLowerCase();
    return allKeys.filter((k) => k.key.toLowerCase().includes(lower));
  }, [allKeys, search]);

  const options = filtered.map((k) => ({
    value: k.key,
    label: (
      <Space size={4}>
        <code style={{ fontSize: 12 }}>{k.key}</code>
        <Tag
          color={SOURCE_TYPE_LABEL[k.sourceType]?.color ?? 'default'}
          style={{ fontSize: 10, lineHeight: '16px', padding: '0 4px', marginRight: 0 }}
        >
          {SOURCE_TYPE_LABEL[k.sourceType]?.label ?? k.sourceType}
        </Tag>
        {k.s2cProto && (
          <span style={{ fontSize: 10, color: 'var(--text-tertiary)' }}>
            ← {resolveStateKeyDisplayType(k) ?? k.s2cProto.split('.').pop()}
          </span>
        )}
        {k.sourceType !== 'stateExtra' && k.sourceType !== 'storeAs' && k.sourceType !== 'builtin' && (
          <span style={{ fontSize: 10, color: 'var(--text-tertiary)' }}>
            ({k.sourceName})
          </span>
        )}
      </Space>
    ),
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
