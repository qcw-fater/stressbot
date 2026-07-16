/**
 * state 表达式输入框。
 *
 * 接收不含 `state:` 前缀的纯表达式，自动剥离外部传入的 `state:` 前缀。
 * 提供：文本输入、浏览 state key 插入、嵌套字段展开、表达式高亮预览。
 */

import type { InputRef } from 'antd';
import { Button, Input, Popover, Tag, Tooltip } from 'antd';
import { RightOutlined, SearchOutlined } from '@ant-design/icons';
import { useCallback, useMemo, useRef, useState } from 'react';
import { useFloatingWindowStore } from '../../store/floatingWindowStore';
import {
  resolveStateKeyDisplayType,
  resolveSubFields,
  type StateKeyInfo,
} from '../ActionEditor/stateRegistry';
import { useStateKeyOptions } from '../ActionEditor/useStateKeyOptions';
import { STATE_SOURCE_LABEL } from '../ActionEditor/stateKeyPresentation';
import type { ProtoField } from '@/types/proto';
import { protoRegistry } from '../../proto/ProtoRegistry';
import { renderExprWithHighlights } from './conditionExprUtils';

export interface StateExprInputProps {
  /** 表达式值（含或不含 state: 前缀均可） */
  value?: string;
  onChange?: (v: string) => void;
  placeholder?: string;
}

/** 展开状态：key path → 是否展开 */
type ExpandedMap = Record<string, boolean>;

export function StateExprInput({ value, onChange, placeholder }: StateExprInputProps) {
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;

  // 剥离 state: 前缀
  const tail = useMemo(() => {
    if (!value) return '';
    return value.startsWith('state:') ? value.slice(6) : value;
  }, [value]);

  const setTail = useCallback((t: string) => {
    onChange?.(t ? 'state:' + t : '');
  }, [onChange]);

  const [browseOpen, setBrowseOpen] = useState(false);
  const [browseSearch, setBrowseSearch] = useState('');
  const [expanded, setExpanded] = useState<ExpandedMap>({});
  const inputRef = useRef<InputRef>(null);

  // 候选 state keys 由 useStateKeyOptions 统一加载（flow graph + 启动配置 + 异步 Lua 脚本）
  const { keys: allKeys } = useStateKeyOptions();

  const filteredKeys = useMemo(() => {
    if (!browseSearch) return allKeys;
    const lower = browseSearch.toLowerCase();
    return allKeys.filter((k) => k.key.toLowerCase().includes(lower));
  }, [allKeys, browseSearch]);

  const insertKeyAtCursor = useCallback((key: string) => {
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
  }, [tail, setTail]);

  const toggleExpand = useCallback((path: string) => {
    setExpanded((prev) => ({ ...prev, [path]: !prev[path] }));
  }, []);

  // 预览高亮
  const previewNodes = useMemo(() => {
    if (!tail.trim()) return null;
    return renderExprWithHighlights(tail, allKeys);
  }, [tail, allKeys]);

  const browseContent = (
    <div style={{ width: 340, maxHeight: 400, overflowY: 'auto' }}>
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
        <StateKeyRow
          key={k.key}
          keyInfo={k}
          expanded={expanded}
          onToggleExpand={toggleExpand}
          onInsert={insertKeyAtCursor}
        />
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
          placeholder={placeholder ?? '如 hp > 0 && index % 2 == 0'}
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
        示例：<code>alive</code>（布尔字段）、<code>hp &gt; 0</code>（比较）、<code>index % 2 == 0</code>（算术）、<code>role == "member"</code>（文字值需加双引号）；
        可用 <code>&&</code>（并且）、<code>||</code>（或者）、<code>!</code>（取反）与括号组合
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

// ─── 子字段行组件 ────────────────────────────────────────────

interface StateKeyRowProps {
  keyInfo: StateKeyInfo;
  expanded: ExpandedMap;
  onToggleExpand: (path: string) => void;
  onInsert: (path: string) => void;
}

/** 判断一个 proto 字段是否可以继续展开子字段 */
function isExpandable(field: ProtoField): boolean {
  if (field.kind === 'message' && field.messageName) return true;
  if (field.kind === 'map' && field.messageName) return true;
  return false;
}

/** 获取字段的短类型名 */
function shortFieldType(field: ProtoField): string {
  if (field.kind === 'map') {
    const val = field.messageName?.split('.').pop() ?? field.mapValue ?? field.type;
    return `map<${field.mapKey}, ${val}>`;
  }
  const base = field.kind === 'message'
    ? field.messageName?.split('.').pop() ?? field.type
    : field.kind === 'enum'
      ? field.enumName?.split('.').pop() ?? field.type
      : field.type;
  return field.repeated ? `${base}[]` : base;
}

/** 顶层 state key 行，支持展开子级字段 */
function StateKeyRow({ keyInfo, expanded, onToggleExpand, onInsert }: StateKeyRowProps) {
  const isExpanded = expanded[keyInfo.key] ?? false;

  // 获取子字段列表
  const subFields = useMemo(
    () => resolveSubFields(keyInfo),
    [keyInfo],
  );

  const hasChildren = subFields !== null && subFields.length > 0;

  return (
    <>
      <div
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
        {/* 展开箭头或占位 */}
        <span
          onClick={(e) => { e.stopPropagation(); if (hasChildren) onToggleExpand(keyInfo.key); }}
          style={{
            width: 16,
            height: 16,
            display: 'inline-flex',
            alignItems: 'center',
            justifyContent: 'center',
            cursor: hasChildren ? 'pointer' : 'default',
            color: hasChildren ? 'var(--text-secondary)' : 'transparent',
            flexShrink: 0,
          }}
        >
          {hasChildren && (
            <RightOutlined
              style={{ fontSize: 9, transition: 'transform 0.15s', transform: isExpanded ? 'rotate(90deg)' : 'rotate(0deg)' }}
            />
          )}
        </span>
        {/* key 名 + 类型标签 */}
        <span
          onClick={() => onInsert(keyInfo.key)}
          style={{ flex: 1, minWidth: 0, display: 'flex', alignItems: 'center', gap: 6 }}
        >
          <code style={{ whiteSpace: 'nowrap' }}>{keyInfo.key}</code>
          <Tag
            color={STATE_SOURCE_LABEL[keyInfo.sourceType].color}
            style={{ fontSize: 10, lineHeight: '16px', padding: '0 4px', margin: 0 }}
          >
            {STATE_SOURCE_LABEL[keyInfo.sourceType].label}
          </Tag>
          {keyInfo.s2cProto && (
            <span style={{ fontSize: 10, color: 'var(--text-tertiary)' }}>
              ← {resolveStateKeyDisplayType(keyInfo) ?? keyInfo.s2cProto.split('.').pop()}
            </span>
          )}
        </span>
      </div>
      {/* 展开的子字段 */}
      {isExpanded && subFields && subFields.map((f) => (
        <FieldRow
          key={f.name}
          field={f}
          pathPrefix={`${keyInfo.key}.${f.name}`}
          expanded={expanded}
          onToggleExpand={onToggleExpand}
          onInsert={onInsert}
          depth={1}
        />
      ))}
    </>
  );
}

// ─── proto 子字段行 ──────────────────────────────────────────

interface FieldRowProps {
  field: ProtoField;
  pathPrefix: string;
  expanded: ExpandedMap;
  onToggleExpand: (path: string) => void;
  onInsert: (path: string) => void;
  depth: number;
}

function FieldRow({ field, pathPrefix, expanded, onToggleExpand, onInsert, depth }: FieldRowProps) {
  const isExpanded = expanded[pathPrefix] ?? false;
  const canExpand = isExpandable(field);
  // 限制展开深度
  const maxDepth = 4;

  // repeated 字段插入时自动加 [0]
  const insertPath = field.repeated ? `${pathPrefix}[0]` : pathPrefix;

  // 获取子消息的字段列表（用于继续展开）
  const childFields = useMemo(() => {
    if (!canExpand || depth >= maxDepth) return null;
    if (!field.messageName) return null;
    const msg = protoRegistry.lookupMessage(field.messageName);
    return msg?.fields ?? null;
  }, [canExpand, depth, field.messageName]);

  return (
    <>
      <div
        style={{
          padding: '3px 6px',
          paddingLeft: 6 + depth * 16,
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
        {/* 展开箭头 */}
        <span
          onClick={(e) => { e.stopPropagation(); if (canExpand && childFields && childFields.length > 0) onToggleExpand(pathPrefix); }}
          style={{
            width: 16,
            height: 16,
            display: 'inline-flex',
            alignItems: 'center',
            justifyContent: 'center',
            cursor: canExpand ? 'pointer' : 'default',
            color: canExpand ? 'var(--text-secondary)' : 'transparent',
            flexShrink: 0,
          }}
        >
          {canExpand && childFields && childFields.length > 0 && (
            <RightOutlined
              style={{ fontSize: 9, transition: 'transform 0.15s', transform: isExpanded ? 'rotate(90deg)' : 'rotate(0deg)' }}
            />
          )}
        </span>
        {/* 字段名 + 类型 */}
        <span
          onClick={() => onInsert(insertPath)}
          style={{ flex: 1, minWidth: 0, display: 'flex', alignItems: 'center', gap: 6 }}
        >
          <code style={{ whiteSpace: 'nowrap' }}>
            {field.name}{field.repeated ? '[]' : ''}
          </code>
          <span style={{ fontSize: 10, color: 'var(--text-terti)' }}>
            {shortFieldType(field)}
          </span>
        </span>
      </div>
      {/* 展开的子字段 */}
      {isExpanded && childFields && childFields.map((f) => (
        <FieldRow
          key={f.name}
          field={f}
          pathPrefix={`${pathPrefix}${field.repeated ? '[0]' : ''}.${f.name}`}
          expanded={expanded}
          onToggleExpand={onToggleExpand}
          onInsert={onInsert}
          depth={depth + 1}
        />
      ))}
    </>
  );
}
