/**
 * Setter 路径输入框 — 用于 StoreMapping 的 setter 字段。
 *
 * 支持：自由文本输入（如 "loginResp.token"）、浏览已有 state key 树并选择路径。
 * 点击 key/子字段直接替换整个 setter 值。
 */

import type { InputRef } from 'antd';
import { Button, Input, Popover, Space, Tag, Tooltip } from 'antd';
import { RightOutlined, SearchOutlined } from '@ant-design/icons';
import { useCallback, useMemo, useRef, useState } from 'react';
import { useFloatingWindowStore } from '../../store/floatingWindowStore';
import {
  resolveStateKeyDisplayType,
  resolveSubFields,
  type StateKeySourceType,
} from '../ActionEditor/stateRegistry';
import { STATE_SOURCE_LABEL } from '../ActionEditor/stateKeyPresentation';
import { useStateKeyOptions } from '../ActionEditor/useStateKeyOptions';
import type { ProtoField } from '@/types/proto';
import { protoRegistry } from '../../proto/ProtoRegistry';

export interface SetterPathInputProps {
  value: string;
  onChange: (v: string) => void;
  style?: React.CSSProperties;
}

type ExpandedMap = Record<string, boolean>;

export function SetterPathInput({
  value,
  onChange,
  style,
}: SetterPathInputProps) {
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;

  const [browseOpen, setBrowseOpen] = useState(false);
  const [browseSearch, setBrowseSearch] = useState('');
  const [expanded, setExpanded] = useState<ExpandedMap>({});
  const inputRef = useRef<InputRef>(null);

  // 已知 state keys 由统一钩子提供（StateKeyOptionsProvider 下共享脚本加载）
  const { keys: allKeys } = useStateKeyOptions();

  const filteredKeys = useMemo(() => {
    if (!browseSearch) return allKeys;
    const lower = browseSearch.toLowerCase();
    return allKeys.filter((k) => k.key.toLowerCase().includes(lower));
  }, [allKeys, browseSearch]);

  const selectKey = useCallback((path: string) => {
    onChange(path);
    setBrowseOpen(false);
    setBrowseSearch('');
    requestAnimationFrame(() => {
      inputRef.current?.focus();
    });
  }, [onChange]);

  const toggleExpand = useCallback((path: string) => {
    setExpanded((prev) => ({ ...prev, [path]: !prev[path] }));
  }, []);

  const browseContent = (
    <div style={{ width: 320, maxHeight: 360, overflowY: 'auto' }}>
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
        <SetterKeyRow
          key={k.key}
          keyInfo={k}
          expanded={expanded}
          onToggleExpand={toggleExpand}
          onSelect={selectKey}
        />
      ))}
    </div>
  );

  return (
    <Space.Compact style={{ width: '100%', ...style }}>
      <Input
        ref={inputRef}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder="state key 或嵌套路径 (如 a.b.c)"
        size="small"
        style={{ flex: 1, minWidth: 0 }}
      />
      <Popover
        open={browseOpen}
        onOpenChange={setBrowseOpen}
        trigger="click"
        placement="bottomRight"
        content={browseContent}
        overlayStyle={{ zIndex: popupZ }}
      >
        <Tooltip title="浏览已有 state key">
          <Button size="small" icon={<SearchOutlined />} />
        </Tooltip>
      </Popover>
    </Space.Compact>
  );
}

// ─── 顶层 state key 行 ──────────────────────────────────────

interface SetterKeyRowProps {
  keyInfo: { key: string; sourceType: StateKeySourceType; s2cProto?: string; storeField?: string };
  expanded: ExpandedMap;
  onToggleExpand: (path: string) => void;
  onSelect: (path: string) => void;
}

function SetterKeyRow({ keyInfo, expanded, onToggleExpand, onSelect }: SetterKeyRowProps) {
  const isExpanded = expanded[keyInfo.key] ?? false;
  // STATE_SOURCE_LABEL 覆盖 StateKeySourceType 全集，无需 fallback。
  const sourceLabel = STATE_SOURCE_LABEL[keyInfo.sourceType];

  const subFields = useMemo(
    () => resolveSubFields(keyInfo as Parameters<typeof resolveSubFields>[0]),
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
        <span
          onClick={(e) => { e.stopPropagation(); if (hasChildren) onToggleExpand(keyInfo.key); }}
          style={{
            width: 16, height: 16, display: 'inline-flex', alignItems: 'center',
            justifyContent: 'center', cursor: hasChildren ? 'pointer' : 'default',
            color: hasChildren ? 'var(--text-secondary)' : 'transparent', flexShrink: 0,
          }}
        >
          {hasChildren && (
            <RightOutlined
              style={{ fontSize: 9, transition: 'transform 0.15s', transform: isExpanded ? 'rotate(90deg)' : 'rotate(0deg)' }}
            />
          )}
        </span>
        <span
          onClick={() => onSelect(keyInfo.key)}
          style={{ flex: 1, minWidth: 0, display: 'flex', alignItems: 'center', gap: 6 }}
        >
          <code style={{ whiteSpace: 'nowrap' }}>{keyInfo.key}</code>
          <Tag
            color={sourceLabel.color}
            style={{ fontSize: 10, lineHeight: '16px', padding: '0 4px', margin: 0 }}
          >
            {sourceLabel.label}
          </Tag>
          {keyInfo.s2cProto && (
            <span style={{ fontSize: 10, color: 'var(--text-tertiary)' }}>
              ← {resolveStateKeyDisplayType(keyInfo as Parameters<typeof resolveStateKeyDisplayType>[0]) ?? keyInfo.s2cProto.split('.').pop()}
            </span>
          )}
        </span>
      </div>
      {isExpanded && subFields && subFields.map((f) => (
        <SetterFieldRow
          key={f.name}
          field={f}
          pathPrefix={`${keyInfo.key}.${f.name}`}
          expanded={expanded}
          onToggleExpand={onToggleExpand}
          onSelect={onSelect}
          depth={1}
        />
      ))}
    </>
  );
}

// ─── proto 子字段行 ──────────────────────────────────────────

function isExpandable(field: ProtoField): boolean {
  if (field.kind === 'message' && field.messageName) return true;
  if (field.kind === 'map' && field.messageName) return true;
  return false;
}

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

interface SetterFieldRowProps {
  field: ProtoField;
  pathPrefix: string;
  expanded: ExpandedMap;
  onToggleExpand: (path: string) => void;
  onSelect: (path: string) => void;
  depth: number;
}

function SetterFieldRow({ field, pathPrefix, expanded, onToggleExpand, onSelect, depth }: SetterFieldRowProps) {
  const isExpanded = expanded[pathPrefix] ?? false;
  const canExpand = isExpandable(field);
  const maxDepth = 4;

  const insertPath = field.repeated ? `${pathPrefix}[0]` : pathPrefix;

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
          padding: '3px 6px', paddingLeft: 6 + depth * 16,
          cursor: 'pointer', borderRadius: 4, display: 'flex', alignItems: 'center',
          gap: 6, fontSize: 12,
        }}
        onMouseEnter={(e) => (e.currentTarget.style.background = 'var(--hover-bg, rgba(0,0,0,0.04))')}
        onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
      >
        <span
          onClick={(e) => { e.stopPropagation(); if (canExpand && childFields && childFields.length > 0) onToggleExpand(pathPrefix); }}
          style={{
            width: 16, height: 16, display: 'inline-flex', alignItems: 'center',
            justifyContent: 'center', cursor: canExpand ? 'pointer' : 'default',
            color: canExpand ? 'var(--text-secondary)' : 'transparent', flexShrink: 0,
          }}
        >
          {canExpand && childFields && childFields.length > 0 && (
            <RightOutlined
              style={{ fontSize: 9, transition: 'transform 0.15s', transform: isExpanded ? 'rotate(90deg)' : 'rotate(0deg)' }}
            />
          )}
        </span>
        <span
          onClick={() => onSelect(insertPath)}
          style={{ flex: 1, minWidth: 0, display: 'flex', alignItems: 'center', gap: 6 }}
        >
          <code style={{ whiteSpace: 'nowrap' }}>{field.name}{field.repeated ? '[]' : ''}</code>
          <span style={{ fontSize: 10, color: 'var(--text-tertiary)' }}>{shortFieldType(field)}</span>
        </span>
      </div>
      {isExpanded && childFields && childFields.map((f) => (
        <SetterFieldRow
          key={f.name}
          field={f}
          pathPrefix={`${pathPrefix}${field.repeated ? '[0]' : ''}.${f.name}`}
          expanded={expanded}
          onToggleExpand={onToggleExpand}
          onSelect={onSelect}
          depth={depth + 1}
        />
      ))}
    </>
  );
}
