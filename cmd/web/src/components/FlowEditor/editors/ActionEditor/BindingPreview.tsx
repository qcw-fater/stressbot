/**
 * Binding 值模拟：根据 binding 类型生成预览值，无需后端参与。
 */

import type { FieldBind } from '@/types/action';

export type PreviewResult =
  | { kind: 'skipped'; reason: string }
  | { kind: 'concrete'; display: string }
  | { kind: 'placeholder'; display: string }
  | { kind: 'error'; message: string };

export function simulateBinding(fb: FieldBind): PreviewResult {
  if (fb.condition) {
    return { kind: 'placeholder', display: `条件: ${fb.condition}` };
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
    case 'randomFloat': {
      const min = fb.min ?? 0;
      const max = fb.max ?? 1;
      const prec = fb.precision ?? 2;
      const sample = Number((Math.random() * (max - min) + min).toFixed(prec));
      return { kind: 'concrete', display: `${sample}  ← [${min}, ${max}] 精度=${prec}` };
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

function genSampleString(len: number): string {
  const chars = 'abcdefghijklmnopqrstuvwxyz0123456789';
  let s = '';
  for (let i = 0; i < Math.min(len, 16); i++) s += chars[Math.floor(Math.random() * chars.length)];
  return len > 16 ? s + '...' : s;
}
