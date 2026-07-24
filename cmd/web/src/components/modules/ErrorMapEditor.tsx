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
import { CheckOutlined, CloseOutlined, SearchOutlined } from '@ant-design/icons';
import {
  isDraftEngaged,
  matchesErrorQuery,
  nextBusinessCode,
  parseErrorMapSafe,
  serializeErrorMap,
  validateErrorDraft,
  validateErrorMap,
  type ErrorMapEntry,
} from '@/services/errorMapValidation';

export {
  isDraftEngaged,
  matchesErrorQuery,
  nextBusinessCode,
  parseErrorMap,
  parseErrorMapSafe,
  serializeErrorMap,
  validateErrorDraft,
  validateErrorMap,
} from '@/services/errorMapValidation';
export type { ErrorMapEntry, ErrorMapError } from '@/services/errorMapValidation';

interface Props {
  value: string;
  onChange: (next: string) => void;
  frameworkCodes: { code: number; name: string }[];
}

/** 标签交替配色（冷色+冷绿循环：蓝/绿/青/黄绿/极客蓝/紫），相邻不撞色。antd 预设色。 */
const TAG_PALETTE = ['blue', 'green', 'cyan', 'lime', 'geekblue', 'purple'] as const;

export function ErrorMapEditor({ value, onChange, frameworkCodes }: Props) {
  const entries = useMemo(() => parseErrorMapSafe(value), [value]);
  const errs = useMemo(() => validateErrorMap(entries), [entries]);
  // 预存数据中的结构性问题（码非法 / <100 / 重复）才弹红色 Alert；草稿非法由添加区行内提示。
  const structural = useMemo(() => errs.filter((e) => /码|重复|保留/.test(e.message)), [errs]);

  // 固定添加/编辑区草稿：editing 非 null 时表示正在编辑该码（自身不判重复）。
  const [code, setCode] = useState<number | null>(null);
  const [desc, setDesc] = useState('');
  const [editing, setEditing] = useState<number | null>(null);
  // 业务码搜索：按码或描述过滤（保留 origIdx 以正确关联结构性错误）。
  const [query, setQuery] = useState('');
  const visible = useMemo(
    () =>
      entries
        .map((entry, origIdx) => ({ entry, origIdx }))
        .filter(({ entry }) => matchesErrorQuery(entry.code, entry.desc, query)),
    [entries, query],
  );

  const codeRef = useRef<ComponentRef<typeof InputNumber>>(null);
  const descRef = useRef<InputRef>(null);

  const draftErr = validateErrorDraft(code ?? Number.NaN, desc, editing, entries);
  // 仅在草稿已介入（填了码 / 正在编辑 / 填了描述）时才展示行内错误，避免空载即报红。
  const visibleDraftErr = isDraftEngaged(code, desc, editing) ? draftErr : null;
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
  // 正常业务码按位置取交替色；错误(红)/警告(橙)状态优先，保留校验反馈。
  const tagColor = (s: 'error' | 'warning' | 'normal', i: number) =>
    s === 'error' ? 'error' : s === 'warning' ? 'warning' : TAG_PALETTE[i % TAG_PALETTE.length];
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
          status={visibleDraftErr && /正整数|保留|已存在/.test(visibleDraftErr) ? 'error' : undefined}
          onChange={(v) => setCode(typeof v === 'number' ? v : null)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') descRef.current?.focus();
          }}
        />
        <Input
          ref={descRef}
          value={desc}
          placeholder="中文描述"
          status={visibleDraftErr && /描述/.test(visibleDraftErr) ? 'error' : undefined}
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
      {visibleDraftErr && <div className="eme-draft-err">{visibleDraftErr}</div>}

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
        {frameworkCodes.length === 0 ? (
          <div className="eme-empty">未加载到框架保留码，请确认服务器已连接后重新切入本页（接口 /sbot/api/error-codes）</div>
        ) : (
          <Space size={[4, 4]} wrap>
            {frameworkCodes.map((c, i) => (
              <Tag key={c.code} className="eme-reserved-tag" color={TAG_PALETTE[i % TAG_PALETTE.length]}>
                {c.code}={c.name}
              </Tag>
            ))}
          </Space>
        )}
      </div>

      {/* 业务码标签：点标签编辑、× 删除 */}
      <div className="eme-framework">
        <div className="eme-biz-head">
          <div className="eme-framework-title">业务码（≥ 100，点标签编辑、点 × 删除）</div>
          <Input
            allowClear
            size="small"
            className="eme-search"
            placeholder="搜索码或描述"
            prefix={<SearchOutlined />}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
        </div>
        {entries.length === 0 ? (
          <div className="eme-empty">暂无业务码，在上方输入码与描述后点「添加」</div>
        ) : visible.length === 0 ? (
          <div className="eme-empty">未匹配到「{query.trim()}」</div>
        ) : (
          <Space size={[6, 6]} wrap>
            {visible.map(({ entry: e, origIdx }, visIdx) => {
              const s = tagStatus(origIdx, e);
              return (
                <Tooltip
                  key={e.code}
                  title={e.desc.trim() ? `${e.code}：${e.desc}` : `${e.code}：描述为空`}
                >
                  <Tag
                    color={tagColor(s, visIdx)}
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
