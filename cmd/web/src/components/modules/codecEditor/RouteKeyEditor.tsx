/**
 * RouteKeyEditor — routeKeyTemplate 结构化编辑器。
 *
 * - routeKeyTemplate 输入框 → setRouteKeyTemplate（raw 无损）→ onEdit 回灌 content。
 * - 实时校验：模板里所有 `{name}` 占位都需对应某个 role:"route" 字段；未知占位红色列出
 *   （与 validateCodecSchema 的 routeKeyTemplate 校验一致）。
 * - 展示 route 字段清单（可用作占位）+ 样例 routeKey（占位替换为字段名示例值）。
 *
 * 单一数据源 = content 字符串。
 */

import { Alert, Card, Input, Space, Tag, Typography } from 'antd';
import type { CodecSchema } from '@/types/codec';
import { setRouteKeyTemplate } from './codecEdit';

/** 占位正则（与 resourcesStore.ts 的 ROUTE_KEY_PLACEHOLDER_RE 对齐）。 */
const ROUTE_KEY_PLACEHOLDER_RE = /\{([A-Za-z_][A-Za-z0-9_]*)\}/g;

export interface RouteKeyEditorProps {
  raw: Record<string, unknown>;
  schema: CodecSchema;
  onEdit: (nextContent: string) => void;
}

export function RouteKeyEditor({ raw, schema, onEdit }: RouteKeyEditorProps) {
  const template: string = typeof schema.routeKeyTemplate === 'string' ? schema.routeKeyTemplate : '';

  // route 字段清单
  const routeFields = collectRouteFieldNames(schema);

  // 占位校验
  const placeholders = extractPlaceholders(template);
  const unknown = placeholders.filter((p) => !routeFields.includes(p));

  // 样例 routeKey：未知占位保留原 `{name}`，已知占位替换为字段名。
  const sample = renderSample(template, routeFields);

  return (
    <Card
      size="small"
      className="pce-bench route-bench"
      title={
        <Space size={8} align="center">
          <span className="pce-bench-title">路由键</span>
          <Typography.Text type="secondary" className="pce-bench-meta">模板与示例</Typography.Text>
        </Space>
      }
      styles={{ body: { padding: 12 } }}
    >
      <div className="split-2">
        {/* 左：模板编辑 + 校验 */}
        <div className="split-2-left">
          <Space direction="vertical" size={8} style={{ width: '100%' }}>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              模板中的 {`{name}`} 占位需对应 role:&quot;route&quot; 字段，如 {`{cmd}:{act}`}
            </Typography.Text>
            <Input
              size="small"
              value={template}
              placeholder="{cmd}:{act}"
              onChange={(e) => onEdit(setRouteKeyTemplate(raw, e.target.value))}
            />
            <div>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>可用 route 字段：</Typography.Text>
              {routeFields.length === 0 ? (
                <Typography.Text type="secondary" style={{ fontSize: 12, marginLeft: 6 }}>
                  （当前 header 无 role:&quot;route&quot; 字段）
                </Typography.Text>
              ) : (
                <Space size={4} wrap style={{ marginTop: 4 }}>
                  {routeFields.map((n) => (
                    <Tag key={n} style={{ fontSize: 12 }}>{`{${n}}`}</Tag>
                  ))}
                </Space>
              )}
            </div>
            {unknown.length > 0 && (
              <Alert
                type="error"
                showIcon
                style={{ padding: '6px 12px' }}
                message={
                  <span style={{ fontSize: 12 }}>
                    未知占位：{unknown.map((u) => `{${u}}`).join(' ')}（必须指向某个 route 字段）
                  </span>
                }
              />
            )}
          </Space>
        </div>

        {/* 右：实时样例 */}
        <div className="split-2-right">
          <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 6 }}>
            样例 routeKey
          </Typography.Text>
          <div style={{
            fontFamily: 'monospace', fontSize: 16, fontWeight: 600,
            padding: '10px 12px', background: 'var(--hover-bg)', borderRadius: 6,
            wordBreak: 'break-all', minHeight: 44, display: 'flex', alignItems: 'center',
          }}>
            {sample || <Typography.Text type="secondary" style={{ fontSize: 12 }}>（空）</Typography.Text>}
          </div>
        </div>
      </div>
    </Card>
  );
}

// ─── helpers ────────────────────────────────────────────────────────

/** 收集 role:"route" 字段名（保序）。 */
function collectRouteFieldNames(schema: CodecSchema): string[] {
  const header = Array.isArray(schema.header) ? schema.header : [];
  const out: string[] = [];
  for (const f of header) {
    if (f && f.role === 'route' && typeof f.name === 'string' && f.name !== '') {
      out.push(f.name);
    }
  }
  return out;
}

/** 提取模板中所有 {name} 占位（去重保序）。 */
function extractPlaceholders(template: string): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  ROUTE_KEY_PLACEHOLDER_RE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = ROUTE_KEY_PLACEHOLDER_RE.exec(template)) !== null) {
    const name = m[1];
    if (!seen.has(name)) {
      seen.add(name);
      out.push(name);
    }
  }
  return out;
}

/** 渲染样例 routeKey：已知占位 → 字段名，未知占位原样保留。 */
function renderSample(template: string, routeFields: string[]): string {
  const known = new Set(routeFields);
  // 用函数 replacement 避免 `$` 特殊语义。
  return template.replace(ROUTE_KEY_PLACEHOLDER_RE, (_full, name: string) => (known.has(name) ? name : `{${name}}`));
}
