const ROUTE_KEY_PLACEHOLDER_RE = /\{([A-Za-z_][A-Za-z0-9_]*)\}/g;

export interface RouteTemplateField {
  name: string;
  draft: string;
  value: unknown;
  missing: boolean;
}

export type RouteScalarParseResult =
  | { ok: true; value: string | number | boolean }
  | { ok: false; message: string };

export function extractRouteTemplatePlaceholders(template: string): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  ROUTE_KEY_PLACEHOLDER_RE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = ROUTE_KEY_PLACEHOLDER_RE.exec(template)) !== null) {
    const name = m[1];
    if (seen.has(name)) continue;
    seen.add(name);
    out.push(name);
  }
  return out;
}

export function isPlainRouteObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

export function formatRouteFieldDraft(value: unknown): string {
  if (value === undefined || value === null) return '';
  if (typeof value === 'string') return value;
  if (typeof value === 'number' || typeof value === 'boolean') return String(value);
  return JSON.stringify(value);
}

export function parseRouteScalarDraft(text: string): RouteScalarParseResult {
  const trimmed = text.trim();
  if (trimmed === '') return { ok: true, value: '' };
  try {
    const parsed = JSON.parse(trimmed) as unknown;
    if (typeof parsed === 'string' || typeof parsed === 'number' || typeof parsed === 'boolean') {
      return { ok: true, value: parsed };
    }
    return { ok: false, message: 'route 字段只支持字符串、数字或布尔值' };
  } catch {
    return { ok: true, value: text };
  }
}

export function buildRouteTemplateFields(template: string, route: unknown): RouteTemplateField[] {
  const record = isPlainRouteObject(route) ? route : {};
  return extractRouteTemplatePlaceholders(template).map((name) => {
    const value = record[name];
    return {
      name,
      value,
      draft: formatRouteFieldDraft(value),
      missing: value === undefined || value === null,
    };
  });
}

export function updateRouteTemplateField(
  route: unknown,
  fieldName: string,
  draft: string,
): { ok: true; route: Record<string, unknown> } | { ok: false; message: string } {
  const parsed = parseRouteScalarDraft(draft);
  if (!parsed.ok) return parsed;
  const next: Record<string, unknown> = isPlainRouteObject(route) ? { ...route } : {};
  next[fieldName] = parsed.value;
  return { ok: true, route: next };
}
