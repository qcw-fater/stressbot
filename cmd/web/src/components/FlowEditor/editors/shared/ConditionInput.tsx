/**
 * 条件表达式输入框：双模式（lua: / state:）。
 *
 * condition 字段在 boolean / loop 节点中复用，
 *   - "lua:check.lua"        → 调 Lua 脚本求值（入口 execute(r) 必须 return true / false）
 *   - "state:hp > 0"         → state 表达式（state: 前缀由系统自动添加）
 *
 * state 模式提供：
 *   - 纯文本输入（用户自由写括号、&&、||、操作符）
 *   - 「浏览 state」按钮 → 弹出已知 state key 列表 → 选中插入到光标位置
 *   - 下方预览条：已识别的 state key 高亮为彩色标签
 */

import { App as AntApp, Button, Input, Modal, Popover, Radio, Space, Tag } from 'antd';
import { EditOutlined, SearchOutlined } from '@ant-design/icons';
import { useEffect, useMemo, useRef, useState } from 'react';
import { LuaForm } from '../ActionEditor/LuaForm';
import { useFloatingWindowStore } from '../../store/floatingWindowStore';
import { useFlowStore } from '../../store/flowStore';
import { useRuntimeStore } from '@/services/runtimeStore';
import { getScript } from '@/services/resourcesStore';
import { collectStateKeys, collectUsedScriptNames } from '../ActionEditor/stateRegistry';
import { renderExprWithHighlights } from './conditionExprUtils';

export interface ConditionInputProps {
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

export function ConditionInput({ value, onChange, placeholder }: ConditionInputProps) {
  const { modal } = AntApp.useApp();
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;
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

  const [editorOpen, setEditorOpen] = useState(false);
  const [luaDirty, setLuaDirty] = useState(false);
  const [browseOpen, setBrowseOpen] = useState(false);
  const [browseSearch, setBrowseSearch] = useState('');
  const inputRef = useRef<HTMLInputElement>(null);

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
    // 恢复光标到插入点之后
    requestAnimationFrame(() => {
      const pos = start + key.length;
      input.setSelectionRange(pos, pos);
      input.focus();
    });
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
          <b>state:</b> 直接写表达式。布尔值：<code>alive</code>；比较：<code>hp &gt; 0</code>；
          复合条件 <code>&amp;&amp;</code> <code>||</code> <code>!</code> 和括号：
          <code>hp &gt; 0 &amp;&amp; (alive || isAdmin)</code>。
          点 <b>浏览</b> 可选择已有 state key 插入。
        </>
      ),
    } as const
  )[mode];

  // 预览高亮（state 模式且有内容时显示）
  const previewNodes = useMemo(() => {
    if (mode !== 'state' || !tail.trim()) return null;
    return renderExprWithHighlights(tail, allKeys);
  }, [mode, tail, allKeys]);

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
              ← {k.s2cProto.split('.').pop()}
            </span>
          )}
        </div>
      ))}
    </div>
  );

  return (
    <div style={{ width: '100%' }}>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', width: '100%' }}>
        <Radio.Group value={mode} onChange={(e) => setMode(e.target.value)} buttonStyle="solid">
          <Radio.Button value="lua">lua:</Radio.Button>
          <Radio.Button value="state">state:</Radio.Button>
        </Radio.Group>
        <Input
          ref={inputRef as never}
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
        {mode === 'state' && (
          <Popover
            open={browseOpen}
            onOpenChange={setBrowseOpen}
            trigger="click"
            placement="bottomRight"
            content={browseContent}
            overlayStyle={{ zIndex: popupZ }}
          >
            <Button icon={<SearchOutlined />} title="浏览已有 state key 并插入到表达式">
              浏览
            </Button>
          </Popover>
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

      {/* lua 脚本编辑 Modal — zIndex 高于 FloatingWindow 确保始终在最上层 */}
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
        focusTriggerAfterClose={false}
        styles={{ mask: { zIndex: popupZ }, wrapper: { zIndex: popupZ + 1 } }}
      >
        <div onKeyDown={(e) => e.stopPropagation()}>
        <LuaForm
          mode="boolean"
          script={tail}
          onChangeScript={(s) => setTail(s)}
          onDirtyChange={setLuaDirty}
        />
        </div>
      </Modal>
    </div>
  );
}
