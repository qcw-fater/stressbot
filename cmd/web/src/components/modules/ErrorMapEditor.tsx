/**
 * errors.json 结构化 KV 编辑器：每行一码 + 描述，行内实时校验。
 *
 * 业务码 ≥ 100；< 100 属框架保留段（与后端 U2.1 guard 一致）。
 * 组件受控：value 为 errors.json 原文字符串，onChange 回吐序列化后的 JSON。
 */
import { useMemo, useRef, useState } from 'react';
import type { ComponentRef } from 'react';
import { Alert, Button, Input, InputNumber, Space, Tag, Tooltip } from 'antd';
import type { InputRef } from 'antd';
import { CheckOutlined, CloseOutlined } from '@ant-design/icons';

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
  const obj = JSON.parse(trimmed) as Record<string, unknown>;
  if (obj === null || typeof obj !== 'object' || Array.isArray(obj)) return [];
  const entries = Object.entries(obj).map(([k, v]) => ({ code: Number(k), desc: typeof v === 'string' ? v : '' }));
  entries.sort((a, b) => a.code - b.code);
  return entries;
}

/**
 * 序列化回 errors.json（缩进 2 空格，键按数组顺序）。
 *
 * 仅丢弃「无有效正整数码」的条目（无法作为 JSON 键）；空描述与 < 100 的码都保留，
 * 以便受控编辑器中「新增的空行 / 正在修正的非法码」能完整往返、不丢行。
 * 落库合法性由 validateErrorMap 在保存前把关（重复码 / < 100 / 空描述均会报错）。
 */
export function serializeErrorMap(entries: ErrorMapEntry[]): string {
  const obj: Record<string, string> = {};
  for (const e of entries) {
    if (Number.isInteger(e.code) && e.code > 0) {
      obj[String(e.code)] = e.desc;
    }
  }
  return JSON.stringify(obj, null, 2);
}

/** 取首个未占用的业务码（默认从 1000 起），供「新增业务码」避免与已有码重复而在序列化时塌缩。 */
export function nextBusinessCode(entries: ErrorMapEntry[], start = 1000): number {
  const used = new Set(entries.map((e) => e.code));
  let code = start;
  while (used.has(code)) code += 1;
  return code;
}

/**
 * 草稿校验：固定添加区输入的 code + desc 是否可提交。
 * editingCode 非 null 表示正在编辑该码（自身不判重复，允许改描述 / 改码到自身）。
 * 返回中文错误文案，或 null 表示通过。
 */
export function validateErrorDraft(
  code: number,
  desc: string,
  editingCode: number | null,
  entries: ErrorMapEntry[],
): string | null {
  if (!Number.isInteger(code) || code <= 0) return '码必须为正整数';
  if (code < 100) return `码 ${code} < 100 属框架保留段，不可用`;
  if (entries.some((e) => e.code === code && e.code !== editingCode)) return `码 ${code} 已存在`;
  if (desc.trim() === '') return '描述不能为空';
  return null;
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
  // 预存数据中的结构性问题（码非法 / <100 / 重复）才弹红色 Alert；草稿非法由添加区行内提示。
  const structural = useMemo(() => errs.filter((e) => /码|重复|保留/.test(e.message)), [errs]);

  // 固定添加/编辑区草稿：editing 非 null 时表示正在编辑该码（自身不判重复）。
  const [code, setCode] = useState<number | null>(null);
  const [desc, setDesc] = useState('');
  const [editing, setEditing] = useState<number | null>(null);

  const codeRef = useRef<ComponentRef<typeof InputNumber>>(null);
  const descRef = useRef<InputRef>(null);

  const draftErr = validateErrorDraft(code ?? Number.NaN, desc, editing, entries);
  const suggestCode = nextBusinessCode(entries);

  const commit = () => {
    if (draftErr) return;
    const codeVal = code as number;
    const descVal = desc.trim();
    if (editing != null) {
      onChange(serializeErrorMap(entries.map((e) => (e.code === editing ? { code: codeVal, desc: descVal } : e))));
      setEditing(null);
    } else {
      onChange(serializeErrorMap([...entries, { code: codeVal, desc: descVal }]));
    }
    setCode(null);
    setDesc('');
    // 连续添加：焦点回到码输入框
    codeRef.current?.focus();
  };

  const startEdit = (c: number) => {
    const e = entries.find((x) => x.code === c);
    if (!e) return;
    setCode(e.code);
    setDesc(e.desc);
    setEditing(c);
    descRef.current?.focus();
  };

  const cancelEdit = () => {
    setEditing(null);
    setCode(null);
    setDesc('');
  };

  const removeTag = (c: number) => {
    onChange(serializeErrorMap(entries.filter((e) => e.code !== c)));
    if (editing === c) cancelEdit();
  };

  const tagStatus = (i: number, e: ErrorMapEntry): 'error' | 'warning' | 'normal' => {
    if (structural.some((er) => er.index === i)) return 'error';
    if (e.desc.trim() === '') return 'warning';
    return 'normal';
  };
  const tagColor = (s: 'error' | 'warning' | 'normal') =>
    s === 'error' ? 'error' : s === 'warning' ? 'warning' : 'blue';
  const truncate = (s: string) => (s.length > 18 ? `${s.slice(0, 18)}…` : s);
  const labelOf = (e: ErrorMapEntry) => `${e.code}=${truncate(e.desc.trim() || '未填写')}`;

  return (
    <div className="eme-root">
      {/* 固定添加/编辑区：码 + 描述 + 添加/保存（常驻，不靠点新增弹出） */}
      <div className="eme-draft">
        <InputNumber
          ref={codeRef}
          value={code ?? undefined}
          min={1}
          placeholder={`如 ${suggestCode}`}
          status={draftErr && /正整数|保留|已存在/.test(draftErr) ? 'error' : undefined}
          onChange={(v) => setCode(typeof v === 'number' ? v : null)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') descRef.current?.focus();
          }}
        />
        <Input
          ref={descRef}
          value={desc}
          placeholder="中文描述"
          status={draftErr && /描述/.test(draftErr) ? 'error' : undefined}
          onChange={(e) => setDesc(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !draftErr) commit();
          }}
        />
        <Button type="primary" icon={<CheckOutlined />} disabled={!!draftErr} onClick={commit} size="small">
          {editing != null ? '保存' : '添加'}
        </Button>
        {editing != null && (
          <Button icon={<CloseOutlined />} onClick={cancelEdit} size="small">
            取消
          </Button>
        )}
      </div>
      {draftErr && <div className="eme-draft-err">{draftErr}</div>}

      {/* 预存数据中的结构性错误（手改源码导致） */}
      {structural.length > 0 && (
        <Alert
          type="error"
          showIcon
          className="eme-alert"
          message={`${structural.length} 处已存错误`}
          description={structural.slice(0, 5).map((e) => (
            <div key={e.index}>{e.message}</div>
          ))}
        />
      )}

      {/* 框架保留码（只读标签） */}
      <div className="eme-framework">
        <div className="eme-framework-title">框架保留码（&lt; 100，只读）</div>
        <Space size={[4, 4]} wrap>
          {frameworkCodes.map((c) => (
            <Tag key={c.code} className="eme-reserved-tag">
              {c.code}={c.name}
            </Tag>
          ))}
        </Space>
      </div>

      {/* 业务码标签：点标签编辑、× 删除 */}
      <div className="eme-framework">
        <div className="eme-framework-title">业务码（≥ 100，点标签编辑、点 × 删除）</div>
        {entries.length === 0 ? (
          <div className="eme-empty">暂无业务码，在上方输入码与描述后点「添加」</div>
        ) : (
          <Space size={[6, 6]} wrap>
            {entries.map((e, i) => {
              const s = tagStatus(i, e);
              return (
                <Tooltip
                  key={e.code}
                  title={e.desc.trim() ? `${e.code}：${e.desc}` : `${e.code}：描述为空`}
                >
                  <Tag
                    color={tagColor(s)}
                    onClick={() => startEdit(e.code)}
                    className={`eme-biz-tag${editing === e.code ? ' eme-biz-tag-editing' : ''}`}
                  >
                    <span className="eme-biz-label">{labelOf(e)}</span>
                    <CloseOutlined
                      className="eme-biz-close"
                      onClick={(ev) => {
                        ev.stopPropagation();
                        removeTag(e.code);
                      }}
                    />
                  </Tag>
                </Tooltip>
              );
            })}
          </Space>
        )}
      </div>
    </div>
  );
}
