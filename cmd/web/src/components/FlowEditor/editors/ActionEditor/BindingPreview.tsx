/**
 * Binding 预览：模拟 binding 运行时产出，无需后端参与。
 */

import type { ConditionDef, FieldBind } from '@/types/action';
import { protoRegistry } from '../../proto/ProtoRegistry';

type PreviewResult =
  | { kind: 'skipped'; reason: string }
  | { kind: 'concrete'; display: string }
  | { kind: 'placeholder'; display: string }
  | { kind: 'error'; message: string };

function simulateBinding(fb: FieldBind): PreviewResult {
  // 条件检查
  if (fb.condition) {
    const cond = conditionSummary(fb.condition);
    return { kind: 'placeholder', display: `条件: ${cond}` };
  }

  switch (fb.type) {
    case 'fixed':
      return { kind: 'concrete', display: JSON.stringify(fb.value ?? 'null') };

    case 'state':
      return { kind: 'placeholder', display: fmtState(fb.source, fb.path) };
    case 'stateFirst':
      return { kind: 'placeholder', display: `${fmtState(fb.source, fb.path)}[0]` };
    case 'stateRandom':
      return { kind: 'placeholder', display: `从 ${fmtState(fb.source, fb.path)} 随机取一个` + fmtFilters(fb.filters) };
    case 'stateRandomN':
      return { kind: 'placeholder', display: `从 ${fmtState(fb.source, fb.path)} 随机取 ${fb.count ?? 'N'} 个` };
    case 'stateMapKey':
      return { kind: 'placeholder', display: `从 state["${fb.source}"] map 中随机取一个 key` };
    case 'stateMapValue':
      return { kind: 'placeholder', display: `从 state["${fb.source}"] map 中随机取一个 value` + fmtFilters(fb.filters) };
    case 'listSize':
      return { kind: 'placeholder', display: `len(state["${fb.source}"])` };

    case 'randomPick': {
      const vals = fb.values ?? [];
      if (!vals.length) return { kind: 'error', message: 'values 为空' };
      const sample = vals[Math.floor(Math.random() * vals.length)];
      return { kind: 'concrete', display: `${JSON.stringify(sample)}  ← 从 ${JSON.stringify(vals)} 随机` };
    }
    case 'randomPickN': {
      const vals = fb.values ?? [];
      const n = fb.count ?? 1;
      return { kind: 'placeholder', display: `从 ${JSON.stringify(vals)} 随机取 ${n} 个` };
    }
    case 'randomPickMap':
      return { kind: 'placeholder', display: `按 state["${fb.keySource}"] 查表随机选一个` };

    case 'randomInt': {
      const min = fb.min ?? 0;
      const max = fb.max ?? 100;
      const sample = Math.floor(Math.random() * (max - min + 1)) + min;
      return { kind: 'concrete', display: `${sample}  ← [${min}, ${max}]` };
    }
    case 'randomBool': {
      const sample = Math.random() < 0.5;
      return { kind: 'concrete', display: `${sample}` };
    }
    case 'randomString': {
      const len = fb.length ?? 8;
      const sample = genSampleString(len);
      return { kind: 'concrete', display: `"${sample}"  ← length=${len}` };
    }
    case 'randomExclude':
      return { kind: 'placeholder', display: `从 ${JSON.stringify(fb.values ?? [])} 排除 state["${fb.excludeSource}"] 后随机` };

    case 'nested':
      return { kind: 'placeholder', display: `嵌套消息 ${fb.message ?? '?'} (${fb.bindings?.length ?? 0} 个子绑定)` };
    case 'nestedList':
      return { kind: 'placeholder', display: `嵌套列表 (${fb.items?.length ?? 0} 个 item)` };

    default:
      return { kind: 'error', message: `未知 type: ${fb.type}` };
  }
}

function fmtState(source?: string, path?: string): string {
  let s = `state["${source ?? '?'}"]`;
  if (path) s += `.${path}`;
  return s;
}

function fmtFilters(filters?: { path?: string; op: string }[]): string {
  if (!filters?.length) return '';
  return ` (filter: ${filters.map((f) => `${f.path || '.'} ${f.op}`).join(', ')})`;
}

function conditionSummary(c: ConditionDef): string {
  const lhs = c.path ? `state["${c.source}"].${c.path}` : `state["${c.source}"]`;
  if (c.op === 'notNil') return `${lhs} != nil`;
  if (c.op === 'isNil') return `${lhs} == nil`;
  const rhs = c.valueSource ? `state["${c.valueSource}"]` : JSON.stringify(c.value);
  return `${lhs} ${c.op} ${rhs}`;
}

function genSampleString(len: number): string {
  const chars = 'abcdefghijklmnopqrstuvwxyz0123456789';
  let s = '';
  for (let i = 0; i < Math.min(len, 16); i++) s += chars[Math.floor(Math.random() * chars.length)];
  return len > 16 ? s + '...' : s;
}

function checkTypeCompat(fb: FieldBind, messageFullName?: string): string | null {
  if (!messageFullName || !fb.field) return null;
  const field = protoRegistry.resolveField(messageFullName, fb.field);
  if (!field) return null;
  const ft = field.type;
  const isNum = /^(int|uint|float|double|fixed|sfixed)/.test(ft);
  const isStr = ft === 'string' || ft === 'bytes';
  if (isNum && fb.type === 'randomString') return `字段类型 ${ft}，binding 产出 string`;
  if (isStr && (fb.type === 'randomInt' || fb.type === 'randomBool')) return `字段类型 ${ft}，binding 产出 number/bool`;
  return null;
}

export interface BindingPreviewProps {
  binding: FieldBind;
  messageFullName?: string;
}

export function BindingPreview({ binding, messageFullName }: BindingPreviewProps) {
  const result = simulateBinding(binding);
  const warn = checkTypeCompat(binding, messageFullName);

  const colorMap: Record<string, string> = {
    concrete: 'var(--color-success)',
    placeholder: 'var(--text-tertiary)',
    error: 'var(--color-error)',
    skipped: 'var(--text-tertiary)',
  };

  return (
    <div style={{ fontSize: 11, lineHeight: 1.6 }}>
      <span style={{ color: 'var(--text-tertiary)' }}>预览: </span>
      <span style={{ color: colorMap[result.kind] }}>
        {result.kind === 'error' ? result.message : result.kind === 'skipped' ? result.reason : result.display}
      </span>
      {warn && (
        <div style={{ color: 'var(--color-error)', marginTop: 2 }}>
          ⚠ {warn}
        </div>
      )}
    </div>
  );
}
