import { Alert, Input, Space, Spin, Typography } from 'antd';
import { useMemo, useState } from 'react';
import {
  buildRouteTemplateFields,
  extractRouteTemplatePlaceholders,
  isPlainRouteObject,
  updateRouteTemplateField,
} from './routeFormModel';

export interface RouteEditorProps {
  value: unknown;
  onChange: (v: unknown) => void;
  server?: string;
  routeKeyTemplate?: string;
  loading?: boolean;
  error?: string | null;
  /** 输入框尺寸（用于嵌入表格的紧凑场景） */
  size?: 'small' | 'middle' | 'large';
}

export function RouteEditor({ value, onChange, server, routeKeyTemplate, loading, error, size }: RouteEditorProps) {
  const [draftErrors, setDraftErrors] = useState<Record<string, string>>({});
  const placeholders = useMemo(() => extractRouteTemplatePlaceholders(routeKeyTemplate ?? ''), [routeKeyTemplate]);
  const fields = useMemo(() => buildRouteTemplateFields(routeKeyTemplate ?? '', value), [routeKeyTemplate, value]);

  if (!server) {
    return <Alert type="info" showIcon message="请先选择 service/server" />;
  }
  if (loading) {
    return <Spin size="small" />;
  }
  if (error) {
    return <Alert type="error" showIcon message={`协议配置加载失败：${error}`} />;
  }
  if (routeKeyTemplate === undefined) {
    return <Alert type="error" showIcon message={`未找到 ${server} 对应的 routeKeyTemplate，请先在协议配置中创建或修正`} />;
  }
  if (placeholders.length === 0) {
    return <Alert type="info" showIcon message="当前 routeKeyTemplate 不包含占位字段，无需填写 route" />;
  }

  const updateField = (name: string, draft: string) => {
    const result = updateRouteTemplateField(value, name, draft);
    if (!result.ok) {
      setDraftErrors((prev) => ({ ...prev, [name]: result.message }));
      return;
    }
    setDraftErrors((prev) => {
      const next = { ...prev };
      delete next[name];
      return next;
    });
    onChange(result.route);
  };

  return (
    <div>
      {!isPlainRouteObject(value) && value !== undefined && value !== null && (
        <Alert
          type="warning"
          showIcon
          message="当前 route 不是对象；编辑字段后将按模板生成对象"
          style={{ marginBottom: 6 }}
        />
      )}
      <Space wrap size={8}>
        {fields.map((f) => (
          <div key={f.name} style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
            <Typography.Text style={{ fontSize: 11 }}>
              <code>{f.name}</code>{f.missing ? <Typography.Text type="danger"> *</Typography.Text> : null}
            </Typography.Text>
            <Input
              size={size}
              value={f.draft}
              status={draftErrors[f.name] ? 'error' : f.missing ? 'warning' : undefined}
              onChange={(e) => updateField(f.name, e.target.value)}
              style={{ width: size === 'small' ? 96 : 140 }}
            />
            {draftErrors[f.name] && (
              <Typography.Text type="danger" style={{ fontSize: 11 }}>{draftErrors[f.name]}</Typography.Text>
            )}
          </div>
        ))}
      </Space>
    </div>
  );
}
