import { Alert, Input, Space, Spin, Tooltip, Typography } from 'antd';
import { WarningOutlined } from '@ant-design/icons';
import { useMemo, useState } from 'react';
import {
  buildRouteTemplateFields,
  extractRouteTemplatePlaceholders,
  isPlainRouteObject,
  updateRouteTemplateField,
} from './routeFormModel';
import './RouteEditor.css';

export interface RouteEditorProps {
  value: unknown;
  onChange: (v: unknown) => void;
  server?: string;
  routeKeyTemplate?: string;
  loading?: boolean;
  error?: string | null;
  /** 输入框尺寸（用于嵌入表格的紧凑场景） */
  size?: 'small' | 'middle' | 'large';
  layout?: 'stacked' | 'inline';
}

export function RouteEditor({
  value,
  onChange,
  server,
  routeKeyTemplate,
  loading,
  error,
  size,
  layout = 'stacked',
}: RouteEditorProps) {
  const [draftErrors, setDraftErrors] = useState<Record<string, string>>({});
  const placeholders = useMemo(() => extractRouteTemplatePlaceholders(routeKeyTemplate ?? ''), [routeKeyTemplate]);
  const fields = useMemo(() => buildRouteTemplateFields(routeKeyTemplate ?? '', value), [routeKeyTemplate, value]);
  const inline = layout === 'inline';

  if (!server) {
    if (inline) return <Typography.Text type="secondary">请先选择目标连接</Typography.Text>;
    return <Alert type="info" showIcon message="请先选择连接服务" />;
  }
  if (loading) {
    return <Spin size="small" />;
  }
  if (error) {
    if (inline) {
      return (
        <Tooltip title={`协议配置加载失败：${error}`} mouseEnterDelay={0.4}>
          <Typography.Text type="danger"><WarningOutlined /> 配置加载失败</Typography.Text>
        </Tooltip>
      );
    }
    return <Alert type="error" showIcon message={`协议配置加载失败：${error}`} />;
  }
  if (routeKeyTemplate === undefined) {
    if (inline) {
      return (
        <Tooltip
          title={`未找到 ${server} 对应的 routeKeyTemplate，请先在协议配置中创建或修正`}
          mouseEnterDelay={0.4}
        >
          <Typography.Text type="danger"><WarningOutlined /> 缺少 route 模板</Typography.Text>
        </Tooltip>
      );
    }
    return <Alert type="error" showIcon message={`未找到 ${server} 对应的 routeKeyTemplate，请先在协议配置中创建或修正`} />;
  }
  if (placeholders.length === 0) {
    if (inline) return <Typography.Text type="secondary">无需填写</Typography.Text>;
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

  if (inline) {
    return (
      <div className="route-editor-inline">
        {!isPlainRouteObject(value) && value !== undefined && value !== null && (
          <Tooltip title="当前 route 不是对象；编辑字段后将按模板生成对象" mouseEnterDelay={0.4}>
            <WarningOutlined className="route-editor-inline__warning" />
          </Tooltip>
        )}
        {fields.map((f) => {
          const input = (
            <Space.Compact className="route-editor-inline__field">
              <span className="route-editor-inline__prefix"><code>{f.name}</code></span>
              <Input
                aria-label={`route ${f.name}`}
                size={size}
                value={f.draft}
                status={draftErrors[f.name] ? 'error' : f.missing ? 'warning' : undefined}
                onChange={(e) => updateField(f.name, e.target.value)}
              />
            </Space.Compact>
          );
          return draftErrors[f.name]
            ? <Tooltip key={f.name} title={draftErrors[f.name]} mouseEnterDelay={0.4}>{input}</Tooltip>
            : <span key={f.name}>{input}</span>;
        })}
      </div>
    );
  }

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
