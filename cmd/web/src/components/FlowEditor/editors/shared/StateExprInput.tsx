/**
 * state 表达式输入框。
 *
 * 接收不含 `state:` 前缀的纯表达式，自动剥离外部传入的 `state:` 前缀。
 * 提供：文本输入、浏览 state key 插入、表达式高亮预览。
 */

import type { InputRef } from 'antd';
import { Button, Input, Popover, Tag, Tooltip } from 'antd';
import { SearchOutlined } from '@ant-design/icons';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useFloatingWindowStore } from '../../store/floatingWindowStore';
import { useFlowStore } from '../../store/flowStore';
import { useRuntimeStore } from '@/services/runtimeStore';
import { getScript } from '@/services/resourcesStore';
import { collectStateKeys, collectUsedScriptNames, resolveStateKeyDisplayType } from '../ActionEditor/stateRegistry';
import { renderExprWithHighlights } from './conditionExprUtils';

export interface StateExprInputProps {
  /** 表达式值（含或不含 state: 前缀均可） */
  value?: string;
  onChange?: (v: string) => void;
  placeholder?: string;
}

const SOURCE_TYPE_LABEL: Record<string, { label: string; color: string }> = {
  store: { label: 'S2C', color: 'blue' },
  listenStore: { label: '推送', color: 'orange' },
  stateExtra: { label: '启动', color: 'volcano' },
  storeAs: { label: '中间值', color: 'green' },
  lua: { label: 'Lua', color: 'purple' },
};

export function StateExprInput({ value, onChange, placeholder }: StateExprInputProps) {
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;

  // 剥离 state: 前缀
  const tail = useMemo(() => {
    if (!value) return '';
    return value.startsWith('state:') ? value.slice(6) : value;
  }, [value]);

  const setTail = (t: string) => {
    onChange?.(t ? 'state:' + t : '');
  };

  const [browseOpen, setBrowseOpen] = useState(false);
  const [browseSearch, setBrowseSearch] = useState('');
  const inputRef = useRef<InputRef>(null);

  // 收集已知 state keys
  const actions = useFlowStore((s) => s.actions);
  const listens = useFlowStore((s) => s.listens);
  const nodes = useFlowStore((s) => s.nodes);
  const stateExtra = useRuntimeStore((s) => s.robotConfig.stateExtra);

  const usedScriptNames = useMemo(
    () => collectUsedScriptNames(actions, listens, nodes),
    [actions, listens, nodes],
  );

  const [luaScripts, setLuaScripts] = useState<Array<{ name: string; content: string }>>([]);
  useEffect(() => {
    if (usedScriptNames.size === 0) { setLuaScripts([]); return; }
    let cancelled = false;
    const entries: Array<{ name: string; content: string }> = [];
    let pending = usedScriptNames.size;
    for (const name of usedScriptNames) {
      getScript(name)
        .then((file) => { if (!cancelled && file) entries.push({ name: file.name, content: file.content }); })
        .catch(() => {})
        .finally(() => { if (!cancelled && --pending === 0) setLuaScripts(entries); });
    }
    return () => { cancelled = true; };
  }, [usedScriptNames]);

  const allKeys = useMemo(
    () => collectStateKeys(actions, listens, stateExtra, undefined, luaScripts),
    [actions, listens, stateExtra, luaScripts],
  );

  const filteredKeys = useMemo(() => {
    if (!browseSearch) return allKeys;
    const lower = browseSearch.toLowerCase();
    return allKeys.filter((k) => k.key.toLowerCase().includes(lower));
  }, [allKeys, browseSearch]);

  const insertKeyAtCursor = (key: string) => {
    const input = inputRef.current?.input;
    if (!input) {
      setTail(tail + key);
      return;
    }
    const start = input.selectionStart ?? tail.length;
    const end = input.selectionEnd ?? tail.length;
    const newTail = tail.slice(0, start) + key + tail.slice(end);
    setTail(newTail);
    setBrowseOpen(false);
    setBrowseSearch('');
    requestAnimationFrame(() => {
      const pos = start + key.length;
      input.setSelectionRange(pos, pos);
      input.focus();
    });
  };

  // 预览高亮
  const previewNodes = useMemo(() => {
    if (!tail.trim()) return null;
    return renderExprWithHighlights(tail, allKeys);
  }, [tail, allKeys]);

  const browseContent = (
    <div style={{ width: 300, maxHeight: 320, overflowY: 'auto' }}>
      <Input
        placeholder="搜索 state key"
        value={browseSearch}
        onChange={(e) => setBrowseSearch(e.target.value)}
        style={{ marginBottom: 8 }}
        size="small"
        allowClear
      />
      {filteredKeys.length === 0 && (
        <div style={{ fontSize: 11, color: 'var(--text-tertiary)', padding: '8px 0' }}>
          无匹配的 state key
        </div>
      )}
      {filteredKeys.map((k) => (
        <div
          key={k.key}
          onClick={() => insertKeyAtCursor(k.key)}
          style={{
            padding: '4px 6px',
            cursor: 'pointer',
            borderRadius: 4,
            display: 'flex',
            alignItems: 'center',
            gap: 6,
            fontSize: 12,
          }}
          onMouseEnter={(e) => (e.currentTarget.style.background = 'var(--hover-bg, rgba(0,0,0,0.04))')}
          onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
        >
          <code>{k.key}</code>
          <Tag
            color={SOURCE_TYPE_LABEL[k.sourceType]?.color ?? 'default'}
            style={{ fontSize: 10, lineHeight: '16px', padding: '0 4px', margin: 0 }}
          >
            {SOURCE_TYPE_LABEL[k.sourceType]?.label ?? k.sourceType}
          </Tag>
          {k.s2cProto && (
            <span style={{ fontSize: 10, color: 'var(--text-tertiary)' }}>
              ← {resolveStateKeyDisplayType(k) ?? k.s2cProto.split('.').pop()}
            </span>
          )}
        </div>
      ))}
    </div>
  );

  return (
    <div style={{ width: '100%' }}>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', width: '100%' }}>
        <Input
          ref={inputRef}
          value={tail}
          onChange={(e) => setTail(e.target.value)}
          placeholder={placeholder ?? '如 hp > 0 && alive'}
          style={{ flex: 1 }}
        />
        <Popover
          open={browseOpen}
          onOpenChange={setBrowseOpen}
          trigger="click"
          placement="bottomRight"
          content={browseContent}
          overlayStyle={{ zIndex: popupZ }}
        >
          <Tooltip title="浏览已有 state key 并插入到表达式">
            <Button icon={<SearchOutlined />}>
              浏览
            </Button>
          </Tooltip>
        </Popover>
      </div>
      <div style={{ marginTop: 4, fontSize: 11, color: 'var(--text-tertiary)', lineHeight: 1.5 }}>
        布尔值：<code>alive</code>；比较：<code>hp &gt; 0</code>；
        复合条件 <code>&&</code> <code>||</code> <code>!</code> 和括号：
        <code>hp &gt; 0 && (alive || isAdmin)</code>
      </div>
      {previewNodes && previewNodes.length > 0 && (
        <div
          style={{
            marginTop: 4,
            padding: '4px 8px',
            background: 'var(--hover-bg, rgba(0,0,0,0.02))',
            borderRadius: 4,
            fontSize: 11,
            lineHeight: '20px',
            display: 'flex',
            flexWrap: 'wrap',
            alignItems: 'center',
            gap: 2,
          }}
        >
          {previewNodes}
        </div>
      )}
    </div>
  );
}
