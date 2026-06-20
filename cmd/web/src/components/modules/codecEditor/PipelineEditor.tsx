/**
 * PipelineEditor — encode/decode 管线步骤的结构化编辑器。
 *
 * 卡片顺序即 encode 顺序；decode 由后端自动反序执行，UI 顶部标注「decode 自动反序」。
 *
 * 每张卡按 op 显示相关字段：
 *   - 通用：name / op（PIPELINE_OPS 下拉）/ algo（**文本输入——Batch 3 §3.4 接 algorithms 端点后改下拉**）
 *           / onError（fail|keep）/ flag（下拉 = 所有 role:"flags" 字段命名位的并集，可空）/ when（子表单）。
 *   - encrypt：keyLen / offset.{encode,decode}（发/收偏移可不同，如 UDP 发=11 收=0）/ produces。
 *   - checksum|hash：over（kind 下拉 OVER_KINDS；range 时 rangeStart/rangeEnd）/ produces。
 *   - params：**通用键值表（staging）**——Batch 3 §3.4 改按算法动态字段。
 *
 * 修改经 codecEdit helper（raw 无损）→ onEdit 回灌 content。
 * 非法值（如 op 不合法、name 重复）即时提示但不阻塞；最终校验交 validateCodecSchema。
 *
 * 单一数据源 = content 字符串（由 AdapterTab 的 setContent 回灌后重算 parsed）。
 */

import { Button, Card, InputNumber, Select, Space, Switch, Typography } from 'antd';
import { DeleteOutlined, DownOutlined, PlusOutlined, UpOutlined } from '@ant-design/icons';
import type { CodecSchema, FlagBit, PipelineStep } from '@/types/codec';
import {
  GUARD_OPS,
  ON_ERROR,
  OVER_KINDS,
  PIPELINE_OPS,
  PRODUCE_REGIONS,
} from '@/types/codec';
import {
  addPipelineStep,
  movePipelineStep,
  removePipelineStep,
  updatePipelineStep,
} from './codecEdit';
import './codecEditor.css';

export interface PipelineEditorProps {
  raw: Record<string, unknown>;
  schema: CodecSchema;
  onEdit: (nextContent: string) => void;
}

export function PipelineEditor({ raw, schema, onEdit }: PipelineEditorProps) {
  const steps: PipelineStep[] = Array.isArray(schema.pipeline) ? (schema.pipeline as PipelineStep[]) : [];

  // flag 下拉选项 = 所有 role:"flags" 字段的命名位 name 并集。
  const flagOptions = collectFlagBitNames(schema).map((n) => ({ label: n, value: n }));
  // when.appliesWith 下拉选项 = 已有 step.name。
  const stepNameOptions = steps
    .map((s) => s?.name)
    .filter((n): n is string => typeof n === 'string' && n !== '')
    .map((n) => ({ label: n, value: n }));

  return (
    <Card
      size="small"
      title={
        <Space size={8} align="center">
          <span>管线</span>
          <Typography.Text type="secondary" style={{ fontSize: 12, fontWeight: 'normal' }}>
            卡片顺序 = encode 顺序（decode 自动反序）
          </Typography.Text>
        </Space>
      }
      styles={{ body: { padding: 12 } }}
    >
      <Space direction="vertical" size={10} style={{ width: '100%' }}>
        {steps.length === 0 && (
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            暂无管线步骤
          </Typography.Text>
        )}
        {steps.map((step, i) => (
          <PipelineStepCard
            key={i}
            raw={raw}
            index={i}
            total={steps.length}
            step={step}
            flagOptions={flagOptions}
            stepNameOptions={stepNameOptions}
            onEdit={onEdit}
          />
        ))}
        <Button
          size="small"
          type="dashed"
          icon={<PlusOutlined />}
          onClick={() => onEdit(addPipelineStep(raw))}
        >
          添加步骤
        </Button>
      </Space>
    </Card>
  );
}

// ─── 单步卡片 ───────────────────────────────────────────────────────

interface PipelineStepCardProps {
  raw: Record<string, unknown>;
  index: number;
  total: number;
  step: PipelineStep;
  flagOptions: { label: string; value: string }[];
  stepNameOptions: { label: string; value: string }[];
  onEdit: (nextContent: string) => void;
}

function PipelineStepCard({
  raw,
  index,
  total,
  step,
  flagOptions,
  stepNameOptions,
  onEdit,
}: PipelineStepCardProps) {
  const op = step?.op ?? '';
  const isEncrypt = op === 'encrypt';
  const isStandaloneDigest = op === 'checksum' || op === 'hash';
  const opInvalid = !(PIPELINE_OPS as readonly string[]).includes(op);

  const patch = (p: Partial<PipelineStep>) => onEdit(updatePipelineStep(raw, index, p));

  return (
    <div
      style={{
        border: '1px solid var(--border-color, rgba(0,0,0,0.1))',
        borderRadius: 6,
        padding: 10,
      }}
    >
      <Space direction="vertical" size={8} style={{ width: '100%' }}>
        {/* 行 1：name + op + algo + onError + flag + 移序/删除 */}
        <Space size={8} wrap align="center">
          <Field label="name">
            <input
              className="flet-input"
              style={{ width: 120 }}
              value={step?.name ?? ''}
              placeholder="必填"
              onChange={(e) => patch({ name: e.target.value })}
            />
          </Field>
          <Field label="op">
            <Select
              size="small"
              style={{ width: 110 }}
              value={op}
              status={opInvalid ? 'error' : undefined}
              onChange={(v) => patch({ op: v })}
              options={(PIPELINE_OPS as readonly string[]).map((o) => ({ label: o, value: o }))}
            />
          </Field>
          {/* STAGING：algo 文本输入。Batch 3 §3.4 接 GET /sbot/codec/algorithms 后改下拉。 */}
          <Field label="algo">
            <input
              className="flet-input"
              style={{ width: 130 }}
              value={step?.algo ?? ''}
              placeholder="算法名"
              onChange={(e) => patch({ algo: e.target.value })}
            />
          </Field>
          <Field label="onError">
            <Select
              size="small"
              style={{ width: 88 }}
              allowClear
              value={step?.onError && step.onError !== '' ? step.onError : undefined}
              placeholder="fail"
              onChange={(v) => patch({ onError: v ?? '' })}
              options={(ON_ERROR as readonly string[]).map((o) => ({ label: o, value: o }))}
            />
          </Field>
          <Field label="flag">
            <Select
              size="small"
              style={{ width: 120 }}
              allowClear
              value={step?.flag && step.flag !== '' ? step.flag : undefined}
              placeholder="可选"
              onChange={(v) => patch({ flag: v ?? '' })}
              options={flagOptions}
            />
          </Field>
          <Space size={2}>
            <Button
              size="small"
              type="text"
              icon={<UpOutlined />}
              disabled={index === 0}
              onClick={() => onEdit(movePipelineStep(raw, index, -1))}
            />
            <Button
              size="small"
              type="text"
              icon={<DownOutlined />}
              disabled={index === total - 1}
              onClick={() => onEdit(movePipelineStep(raw, index, 1))}
            />
            <Button
              size="small"
              type="text"
              danger
              icon={<DeleteOutlined />}
              onClick={() => onEdit(removePipelineStep(raw, index))}
            />
          </Space>
        </Space>

        {/* op 非法即时提示（不阻塞编辑） */}
        {opInvalid && (
          <Typography.Text type="danger" style={{ fontSize: 12 }}>
            未知 op「{op}」（合法：compress|encrypt|checksum|hash）
          </Typography.Text>
        )}

        {/* encrypt 专属 */}
        {isEncrypt && (
          <Space size={12} wrap align="center">
            <Field label="keyLen">
              <InputNumber
                size="small"
                style={{ width: 80 }}
                min={0}
                value={typeof step.keyLen === 'number' ? step.keyLen : undefined}
                onChange={(v) => patch({ keyLen: typeof v === 'number' ? v : 0 })}
              />
            </Field>
            <Field label="offset.encode（发）">
              <InputNumber
                size="small"
                style={{ width: 80 }}
                min={0}
                value={step.offset?.encode ?? 0}
                onChange={(v) =>
                  patch({ offset: makeOffset(step.offset, 'encode', typeof v === 'number' ? v : 0) })
                }
              />
            </Field>
            <Field label="offset.decode（收）">
              <InputNumber
                size="small"
                style={{ width: 80 }}
                min={0}
                value={step.offset?.decode ?? 0}
                onChange={(v) =>
                  patch({ offset: makeOffset(step.offset, 'decode', typeof v === 'number' ? v : 0) })
                }
              />
            </Field>
            <Typography.Text type="secondary" style={{ fontSize: 11 }}>
              发/收偏移可不同，如 UDP 发=11 收=0
            </Typography.Text>
          </Space>
        )}

        {/* 独立 checksum/hash：over */}
        {isStandaloneDigest && (
          <OverSubform step={step} onPatch={patch} />
        )}

        {/* STAGING：params 通用键值表。Batch 3 §3.4 改按算法动态字段。 */}
        <ParamsSubform step={step} onPatch={patch} />

        {/* produces */}
        <ProducesSubform step={step} onPatch={patch} />

        {/* when（结构化条件子表单） */}
        <WhenSubform step={step} stepNameOptions={stepNameOptions} onPatch={patch} />
      </Space>
    </div>
  );
}

// ─── over 子表单（checksum/hash 独立步） ─────────────────────────────

function OverSubform({
  step,
  onPatch,
}: {
  step: PipelineStep;
  onPatch: (p: Partial<PipelineStep>) => void;
}) {
  const over = step.over ?? { kind: 'bodyPlain' };
  const kind = over.kind ?? '';
  const kindInvalid = !(OVER_KINDS as readonly string[]).includes(kind);
  const isRange = kind === 'range';

  return (
    <div>
      <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
        over（作用域）
      </Typography.Text>
      <Space size={8} wrap align="center">
        <Select
          size="small"
          style={{ width: 120 }}
          value={kind}
          status={kindInvalid ? 'error' : undefined}
          onChange={(v) => onPatch({ over: { ...over, kind: v } })}
          options={(OVER_KINDS as readonly string[]).map((k) => ({ label: k, value: k }))}
        />
        {isRange && (
          <>
            <Field label="rangeStart">
              <InputNumber
                size="small"
                style={{ width: 90 }}
                min={0}
                value={typeof over.rangeStart === 'number' ? over.rangeStart : 0}
                onChange={(v) => onPatch({ over: { ...over, rangeStart: typeof v === 'number' ? v : 0 } })}
              />
            </Field>
            <Field label="rangeEnd">
              <InputNumber
                size="small"
                style={{ width: 90 }}
                min={0}
                value={typeof over.rangeEnd === 'number' ? over.rangeEnd : 0}
                onChange={(v) => onPatch({ over: { ...over, rangeEnd: typeof v === 'number' ? v : 0 } })}
              />
            </Field>
          </>
        )}
      </Space>
    </div>
  );
}

// ─── params 通用键值表（STAGING） ────────────────────────────────────

function ParamsSubform({
  step,
  onPatch,
}: {
  step: PipelineStep;
  onPatch: (p: Partial<PipelineStep>) => void;
}) {
  const paramsObj = (step.params && typeof step.params === 'object' ? step.params : {}) as Record<string, unknown>;
  const entries = Object.entries(paramsObj);

  const setEntry = (oldKey: string, newKey: string, value: unknown) => {
    const next: Record<string, unknown> = {};
    for (const [k, v] of entries) {
      if (k === oldKey) {
        if (newKey !== '') next[newKey] = value;
      } else {
        next[k] = v;
      }
    }
    if (!entries.some(([k]) => k === oldKey) && newKey !== '') {
      next[newKey] = value;
    }
    onPatch({ params: next });
  };

  const addEntry = () => {
    const next: Record<string, unknown> = { ...paramsObj, '': '' };
    onPatch({ params: next });
  };

  const removeEntry = (key: string) => {
    const next: Record<string, unknown> = { ...paramsObj };
    delete next[key];
    onPatch({ params: next });
  };

  return (
    <div>
      <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
        params（键值表）
      </Typography.Text>
      <Space direction="vertical" size={4} style={{ width: '100%' }}>
        {entries.map(([k, v]) => (
          <Space size={4} key={k}>
            <input
              className="flet-input"
              style={{ width: 120 }}
              value={k}
              placeholder="key"
              onChange={(e) => setEntry(k, e.target.value, v)}
            />
            <input
              className="flet-input"
              style={{ width: 120 }}
              value={typeof v === 'string' ? v : String(v ?? '')}
              placeholder="value"
              onChange={(e) => setEntry(k, k, coerceParamValue(e.target.value))}
            />
            <Button size="small" type="text" danger icon={<DeleteOutlined />} onClick={() => removeEntry(k)} />
          </Space>
        ))}
        <Button size="small" type="dashed" icon={<PlusOutlined />} onClick={addEntry}>
          添加参数
        </Button>
      </Space>
    </div>
  );
}

/** value：纯数字串 → number，否则 string。 */
function coerceParamValue(raw: string): number | string {
  return raw !== '' && /^-?\d+$/.test(raw) ? Number(raw) : raw;
}

// ─── produces 子表单 ────────────────────────────────────────────────

function ProducesSubform({
  step,
  onPatch,
}: {
  step: PipelineStep;
  onPatch: (p: Partial<PipelineStep>) => void;
}) {
  const produces = Array.isArray(step.produces) ? step.produces : [];

  const update = (i: number, patchObj: Partial<{ name: string; algo: string; region: string }>) => {
    const next = produces.map((p, idx) => (idx === i ? { ...p, ...patchObj } : p));
    onPatch({ produces: next });
  };
  const add = () => onPatch({ produces: [...produces, { name: '', algo: '', region: 'ciphered' }] });
  const remove = (i: number) => onPatch({ produces: produces.filter((_, idx) => idx !== i) });

  return (
    <div>
      <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
        produces（派生产物）
      </Typography.Text>
      <Space direction="vertical" size={4} style={{ width: '100%' }}>
        {produces.map((p, i) => (
          <Space size={4} key={i} wrap>
            <input
              className="flet-input"
              style={{ width: 100 }}
              value={p?.name ?? ''}
              placeholder="name"
              onChange={(e) => update(i, { name: e.target.value })}
            />
            <input
              className="flet-input"
              style={{ width: 100 }}
              value={p?.algo ?? ''}
              placeholder="algo"
              onChange={(e) => update(i, { algo: e.target.value })}
            />
            <Select
              size="small"
              style={{ width: 110 }}
              value={p?.region ?? ''}
              onChange={(v) => update(i, { region: v })}
              options={(PRODUCE_REGIONS as readonly string[]).map((r) => ({ label: r, value: r }))}
            />
            <Button size="small" type="text" danger icon={<DeleteOutlined />} onClick={() => remove(i)} />
          </Space>
        ))}
        <Button size="small" type="dashed" icon={<PlusOutlined />} onClick={add}>
          添加产物
        </Button>
      </Space>
    </div>
  );
}

// ─── when 子表单 ────────────────────────────────────────────────────

function WhenSubform({
  step,
  stepNameOptions,
  onPatch,
}: {
  step: PipelineStep;
  stepNameOptions: { label: string; value: string }[];
  onPatch: (p: Partial<PipelineStep>) => void;
}) {
  const hasWhen = step.when !== undefined;
  const when = step.when ?? {};

  const toggle = (on: boolean) => {
    if (on) {
      onPatch({ when: {} });
    } else {
      // 删除 when：构造不含 when 的 patch 需经 helper 支持——这里传 undefined 让 updateStep
      // 的浅合并写入 undefined，序列化时 JSON.stringify 会丢弃 undefined 键，达到删除效果。
      onPatch({ when: undefined });
    }
  };

  const setField = (patchObj: Partial<typeof when>) => onPatch({ when: { ...when, ...patchObj } });

  const guards = Array.isArray(when.guards) ? when.guards : [];
  const updateGuard = (i: number, patchObj: Partial<{ field: string; op: string; value: number }>) => {
    const next = guards.map((g, idx) => (idx === i ? { ...g, ...patchObj } : g));
    setField({ guards: next });
  };
  const addGuard = () => setField({ guards: [...guards, { field: '', op: 'eq', value: 0 }] });
  const removeGuard = (i: number) => setField({ guards: guards.filter((_, idx) => idx !== i) });

  // 带 when 但未绑 flag：即时提示（与 validateCodecSchema 一致）。
  const missingFlag = hasWhen && (!step.flag || step.flag === '');

  return (
    <div>
      <Space size={8} align="center" style={{ marginBottom: 4 }}>
        <Switch size="small" checked={hasWhen} onChange={toggle} />
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          when（条件）
        </Typography.Text>
        {missingFlag && (
          <Typography.Text type="danger" style={{ fontSize: 11 }}>
            带 when 的步骤必须绑定 flag（否则 decode 无法复现 encode 决策）
          </Typography.Text>
        )}
      </Space>
      {hasWhen && (
        <Space direction="vertical" size={6} style={{ width: '100%', paddingLeft: 8 }}>
          <Space size={12} wrap align="center">
            <Field label="minBodyLen">
              <InputNumber
                size="small"
                style={{ width: 80 }}
                min={0}
                value={typeof when.minBodyLen === 'number' ? when.minBodyLen : undefined}
                onChange={(v) => setField({ minBodyLen: typeof v === 'number' ? v : undefined })}
              />
            </Field>
            <Field label="onlySmaller">
              <Switch
                size="small"
                checked={!!when.onlySmaller}
                onChange={(v) => setField({ onlySmaller: v })}
              />
            </Field>
            <Field label="requireKey">
              <Switch
                size="small"
                checked={!!when.requireKey}
                onChange={(v) => setField({ requireKey: v })}
              />
            </Field>
            <Field label="appliesWith">
              <Select
                size="small"
                style={{ width: 120 }}
                allowClear
                value={when.appliesWith && when.appliesWith !== '' ? when.appliesWith : undefined}
                placeholder="依赖步骤"
                onChange={(v) => setField({ appliesWith: v ?? '' })}
                options={stepNameOptions}
              />
            </Field>
          </Space>
          <div>
            <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
              guards
            </Typography.Text>
            <Space direction="vertical" size={4}>
              {guards.map((g, i) => (
                <Space size={4} key={i} wrap>
                  <input
                    className="flet-input"
                    style={{ width: 110 }}
                    value={g?.field ?? ''}
                    placeholder="field"
                    onChange={(e) => updateGuard(i, { field: e.target.value })}
                  />
                  <Select
                    size="small"
                    style={{ width: 80 }}
                    value={g?.op ?? 'eq'}
                    onChange={(v) => updateGuard(i, { op: v })}
                    options={(GUARD_OPS as readonly string[]).map((o) => ({ label: o, value: o }))}
                  />
                  <InputNumber
                    size="small"
                    style={{ width: 90 }}
                    value={typeof g?.value === 'number' ? g.value : 0}
                    onChange={(v) => updateGuard(i, { value: typeof v === 'number' ? v : 0 })}
                  />
                  <Button size="small" type="text" danger icon={<DeleteOutlined />} onClick={() => removeGuard(i)} />
                </Space>
              ))}
              <Button size="small" type="dashed" icon={<PlusOutlined />} onClick={addGuard}>
                添加 guard
              </Button>
            </Space>
          </div>
        </Space>
      )}
    </div>
  );
}

// ─── helpers ────────────────────────────────────────────────────────

/** 收集所有 role:"flags" 字段的命名位 name（去重，保序）。 */
function collectFlagBitNames(schema: CodecSchema): string[] {
  const header = Array.isArray(schema.header) ? schema.header : [];
  const seen = new Set<string>();
  const out: string[] = [];
  for (const f of header) {
    if (!f || f.role !== 'flags') continue;
    const bits = Array.isArray(f.bits) ? (f.bits as FlagBit[]) : [];
    for (const b of bits) {
      const n = b?.name;
      if (typeof n === 'string' && n !== '' && !seen.has(n)) {
        seen.add(n);
        out.push(n);
      }
    }
  }
  return out;
}

/** 小字段标签 + 控件的统一布局。 */
function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 2 }}>
        {label}
      </Typography.Text>
      {children}
    </div>
  );
}

/** 构造一个新的 offset 对象，保留另一方向原值（避免对象字面量键重复）。 */
function makeOffset(
  cur: PipelineStep['offset'] | undefined,
  side: 'encode' | 'decode',
  value: number,
): { encode: number; decode: number } {
  const encode = side === 'encode' ? value : typeof cur?.encode === 'number' ? cur.encode : 0;
  const decode = side === 'decode' ? value : typeof cur?.decode === 'number' ? cur.decode : 0;
  return { encode, decode };
}
