import { Input } from 'antd';
import { useEffect, useState } from 'react';

export type JsonDraftInputMode = 'json' | 'jsonArray' | 'jsonOrString';

export type JsonDraftParseResult =
  | { ok: true; value: unknown }
  | { ok: false };

export interface JsonDraftInputProps {
  value: unknown;
  onChange: (value: unknown) => void;
  mode: JsonDraftInputMode;
  emptyValue?: unknown;
  placeholder?: string;
  size?: 'small' | 'middle' | 'large';
  style?: React.CSSProperties;
}

export function formatJsonDraftValue(value: unknown, mode: JsonDraftInputMode): string {
  if (value === undefined) return '';
  if (mode === 'jsonOrString' && typeof value === 'string') return value;
  const text = JSON.stringify(value);
  return text === undefined ? '' : text;
}

export function parseJsonDraftValue(
  text: string,
  mode: JsonDraftInputMode,
  emptyValue?: unknown,
): JsonDraftParseResult {
  if (text === '') return { ok: true, value: emptyValue };

  try {
    const parsed = JSON.parse(text) as unknown;
    if (mode === 'jsonArray' && !Array.isArray(parsed)) return { ok: false };
    return { ok: true, value: parsed };
  } catch {
    return { ok: false };
  }
}

export function JsonDraftInput({
  value,
  onChange,
  mode,
  emptyValue,
  placeholder,
  size,
  style,
}: JsonDraftInputProps) {
  const [draft, setDraft] = useState<string | null>(null);
  const [invalid, setInvalid] = useState(false);

  useEffect(() => {
    if (draft === null) setInvalid(false);
  }, [draft, value]);

  const text = draft ?? formatJsonDraftValue(value, mode);

  return (
    <Input
      value={text}
      status={invalid ? 'error' : undefined}
      placeholder={placeholder}
      size={size}
      style={style}
      onChange={(e) => {
        const next = e.target.value;
        setDraft(next);
        const parsed = parseJsonDraftValue(next, mode, emptyValue);
        setInvalid(!parsed.ok);
        if (parsed.ok) onChange(parsed.value);
      }}
      onBlur={() => {
        if (draft === null) return;
        const parsed = parseJsonDraftValue(draft, mode, emptyValue);
        if (parsed.ok) {
          onChange(parsed.value);
          setDraft(null);
          setInvalid(false);
        } else if (mode === 'jsonOrString') {
          onChange(draft);
          setDraft(null);
          setInvalid(false);
        } else {
          // JSON 编辑允许暂时处于不完整状态。失焦后保留草稿和错误提示，
          // 用户回来可继续输入，不会被强制还原为上一次合法值。
          setInvalid(true);
        }
      }}
    />
  );
}
