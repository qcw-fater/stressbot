export interface ErrorMapEntry {
  code: number;
  desc: string;
}

export interface ErrorMapError {
  index: number;
  message: string;
}

export function parseErrorMap(json: string): ErrorMapEntry[] {
  const trimmed = json.trim();
  if (!trimmed || trimmed === '{}') return [];
  const obj = JSON.parse(trimmed) as Record<string, unknown>;
  if (obj === null || typeof obj !== 'object' || Array.isArray(obj)) return [];
  const entries = Object.entries(obj).map(([key, value]) => ({
    code: Number(key),
    desc: typeof value === 'string' ? value : '',
  }));
  entries.sort((left, right) => left.code - right.code);
  return entries;
}

export function serializeErrorMap(entries: ErrorMapEntry[]): string {
  const result: Record<string, string> = {};
  for (const entry of entries) {
    if (Number.isInteger(entry.code) && entry.code > 0) {
      result[String(entry.code)] = entry.desc;
    }
  }
  return JSON.stringify(result, null, 2);
}

export function nextBusinessCode(entries: ErrorMapEntry[], start = 1000): number {
  const used = new Set(entries.map((entry) => entry.code));
  let code = start;
  while (used.has(code)) code += 1;
  return code;
}

export function validateErrorDraft(
  code: number,
  desc: string,
  editingCode: number | null,
  entries: ErrorMapEntry[],
): string | null {
  if (!Number.isInteger(code) || code <= 0) return '码必须为正整数';
  if (code < 100) return `码 ${code} < 100 属框架保留段，不可用`;
  if (entries.some((entry) => entry.code === code && entry.code !== editingCode)) {
    return `码 ${code} 已存在`;
  }
  if (desc.trim() === '') return '描述不能为空';
  return null;
}

export function parseErrorMapSafe(json: string): ErrorMapEntry[] {
  try {
    return parseErrorMap(json);
  } catch {
    return [];
  }
}

export function isDraftEngaged(
  code: number | null,
  desc: string,
  editing: number | null,
): boolean {
  return editing !== null || code !== null || desc.trim() !== '';
}

export function matchesErrorQuery(code: number, desc: string, query: string): boolean {
  const normalized = query.trim().toLowerCase();
  if (!normalized) return true;
  return String(code).includes(normalized) || desc.toLowerCase().includes(normalized);
}

export function validateErrorMap(entries: ErrorMapEntry[]): ErrorMapError[] {
  const errors: ErrorMapError[] = [];
  const seen = new Map<number, number>();
  entries.forEach((entry, index) => {
    if (!Number.isInteger(entry.code) || entry.code <= 0) {
      errors.push({ index, message: `第 ${index + 1} 行：码必须为正整数` });
    } else if (entry.code < 100) {
      errors.push({ index, message: `第 ${index + 1} 行：码 ${entry.code} < 100 属框架保留段，不可用` });
    } else if (seen.has(entry.code)) {
      errors.push({
        index,
        message: `第 ${index + 1} 行：码 ${entry.code} 与第 ${(seen.get(entry.code) as number) + 1} 行重复`,
      });
    } else {
      seen.set(entry.code, index);
    }
    if (entry.desc.trim() === '') {
      errors.push({ index, message: `第 ${index + 1} 行：描述不能为空` });
    }
  });
  return errors;
}
