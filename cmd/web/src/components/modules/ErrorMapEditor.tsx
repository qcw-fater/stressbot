/**
 * errors.json 结构化 KV 编辑器：每行一码 + 描述，行内实时校验。
 *
 * 业务码 ≥ 100；< 100 属框架保留段（与后端 U2.1 guard 一致）。
 * 组件受控：value 为 errors.json 原文字符串，onChange 回吐序列化后的 JSON。
 */
import { useMemo } from 'react';
import { Button, Input, InputNumber, Space, Tag, Alert } from 'antd';
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons';

export interface ErrorMapEntry {
  code: number;
  desc: string;
}
export interface ErrorMapError {
  index: number;
  message: string;
}

/** 把 errors.json 原文解析成条目数组（按码升序）。非法 JSON 抛错，调用方用 parseErrorMapSafe 兜底。 */
export function parseErrorMap(json: string): ErrorMapEntry[] {
  const trimmed = json.trim();
  if (!trimmed || trimmed === '{}') return [];
  const obj = JSON.parse(trimmed) as Record<string, string>;
  const entries = Object.entries(obj).map(([k, v]) => ({ code: Number(k), desc: v }));
  entries.sort((a, b) => a.code - b.code);
  return entries;
}

/** 序列化回 errors.json（缩进 2 空格，键按数组顺序）。码非数字或描述空的条目被丢弃，保证落库合法。 */
export function serializeErrorMap(entries: ErrorMapEntry[]): string {
  const obj: Record<string, string> = {};
  for (const e of entries) {
    if (!Number.isNaN(e.code) && e.desc !== '') {
      obj[String(e.code)] = e.desc;
    }
  }
  return JSON.stringify(obj, null, 2);
}

/** 安全解析：非法 JSON 返回 []。 */
export function parseErrorMapSafe(json: string): ErrorMapEntry[] {
  try {
    return parseErrorMap(json);
  } catch {
    return [];
  }
}

/** 行级实时校验。返回所有错误（码非正整数 / < 100 框架保留 / 重复码 / 描述空）。 */
export function validateErrorMap(entries: ErrorMapEntry[]): ErrorMapError[] {
  const errs: ErrorMapError[] = [];
  const seen = new Map<number, number>();
  entries.forEach((e, i) => {
    if (!Number.isInteger(e.code) || e.code <= 0) {
      errs.push({ index: i, message: `第 ${i + 1} 行：码必须为正整数` });
    } else if (e.code < 100) {
      errs.push({ index: i, message: `第 ${i + 1} 行：码 ${e.code} < 100 属框架保留段，不可用` });
    } else if (seen.has(e.code)) {
      errs.push({ index: i, message: `第 ${i + 1} 行：码 ${e.code} 与第 ${(seen.get(e.code) as number) + 1} 行重复` });
    } else {
      seen.set(e.code, i);
    }
    if (e.desc.trim() === '') {
      errs.push({ index: i, message: `第 ${i + 1} 行：描述不能为空` });
    }
  });
  return errs;
}

interface Props {
  value: string;
  onChange: (next: string) => void;
  frameworkCodes: { code: number; name: string }[];
}

export function ErrorMapEditor({ value, onChange, frameworkCodes }: Props) {
  const entries = useMemo(() => parseErrorMapSafe(value), [value]);
  const errs = useMemo(() => validateErrorMap(entries), [entries]);
  const update = (i: number, patch: Partial<ErrorMapEntry>) =>
    onChange(serializeErrorMap(entries.map((e, idx) => (idx === i ? { ...e, ...patch } : e))));
  const remove = (i: number) => onChange(serializeErrorMap(entries.filter((_, idx) => idx !== i)));
  const add = () => onChange(serializeErrorMap([...entries, { code: 1000, desc: '' }]));

  return (
    <div>
      {errs.length > 0 && (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 8 }}
          message={`${errs.length} 处错误，保存前需全部修正`}
          description={errs.slice(0, 5).map((e) => (
            <div key={e.index}>{e.message}</div>
          ))}
        />
      )}
      <div style={{ marginBottom: 8, padding: 8, background: 'var(--bg-secondary)', borderRadius: 4 }}>
        <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginBottom: 4 }}>
          框架保留码（&lt; 100，不可用）：
        </div>
        <Space size={[4, 4]} wrap>
          {frameworkCodes.map((c) => (
            <Tag key={c.code} style={{ fontSize: 11 }}>
              {c.code}={c.name}
            </Tag>
          ))}
        </Space>
      </div>
      {entries.map((e, i) => (
        <Space key={i} style={{ display: 'flex', marginBottom: 4 }} align="center">
          <InputNumber
            value={Number.isNaN(e.code) ? undefined : e.code}
            min={1}
            style={{ width: 110 }}
            status={errs.some((er) => er.index === i && /码|重复/.test(er.message)) ? 'error' : undefined}
            onChange={(v) => update(i, { code: Number(v) })}
          />
          <Input
            value={e.desc}
            style={{ width: 260 }}
            status={errs.some((er) => er.index === i && /描述/.test(er.message)) ? 'error' : undefined}
            onChange={(ev) => update(i, { desc: ev.target.value })}
          />
          <Button icon={<DeleteOutlined />} onClick={() => remove(i)} danger size="small" />
        </Space>
      ))}
      <Button icon={<PlusOutlined />} onClick={add} size="small" style={{ marginTop: 4 }}>
        新增业务码
      </Button>
    </div>
  );
}
