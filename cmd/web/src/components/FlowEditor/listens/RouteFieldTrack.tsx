import { ExpandAltOutlined, WarningOutlined } from '@ant-design/icons';
import { Button, Spin, Tooltip, Typography } from 'antd';
import { useMemo, useState } from 'react';
import type { ReactNode } from 'react';
import {
  buildRouteTemplateFields,
  extractRouteTemplatePlaceholders,
  isPlainRouteObject,
  updateRouteTemplateField,
} from './routeFormModel';
import './RouteFieldTrack.css';

export interface RouteFieldTrackProps {
  value: unknown;
  onChange: (value: unknown) => void;
  onOpenFloating: () => void;
  server?: string;
  routeKeyTemplate?: string;
  loading?: boolean;
  error?: string | null;
}

interface EditingField {
  name: string;
  draft: string;
  error?: string;
}

export function RouteFieldTrack({
  value,
  onChange,
  onOpenFloating,
  server,
  routeKeyTemplate,
  loading,
  error,
}: RouteFieldTrackProps) {
  const [editing, setEditing] = useState<EditingField | null>(null);
  const placeholders = useMemo(
    () => extractRouteTemplatePlaceholders(routeKeyTemplate ?? ''),
    [routeKeyTemplate],
  );
  const fields = useMemo(
    () => buildRouteTemplateFields(routeKeyTemplate ?? '', value),
    [routeKeyTemplate, value],
  );

  const commit = () => {
    if (!editing) return;
    const result = updateRouteTemplateField(value, editing.name, editing.draft);
    if (!result.ok) {
      setEditing({ ...editing, error: result.message });
      return;
    }
    onChange(result.route);
    setEditing(null);
  };

  let content: ReactNode;
  let canOpen = true;

  if (!server) {
    content = <Typography.Text type="secondary">请先选择目标连接</Typography.Text>;
    canOpen = false;
  } else if (loading) {
    content = <Spin size="small" />;
    canOpen = false;
  } else if (error) {
    content = (
      <Tooltip title={`协议配置加载失败：${error}`} mouseEnterDelay={0.4}>
        <Typography.Text type="danger">
          <WarningOutlined /> 配置加载失败
        </Typography.Text>
      </Tooltip>
    );
    canOpen = false;
  } else if (routeKeyTemplate === undefined) {
    content = (
      <Tooltip
        title={`未找到 ${server} 对应的 routeKeyTemplate，请先在协议配置中创建或修正`}
        mouseEnterDelay={0.4}
      >
        <Typography.Text type="danger">
          <WarningOutlined /> 缺少 route 模板
        </Typography.Text>
      </Tooltip>
    );
    canOpen = false;
  } else if (placeholders.length === 0) {
    content = <Typography.Text type="secondary">无需填写</Typography.Text>;
    canOpen = false;
  } else {
    content = (
      <div className="route-field-track__items">
        {!isPlainRouteObject(value) && value !== undefined && value !== null && (
          <Tooltip title="当前 route 不是对象；编辑字段后将按模板生成对象" mouseEnterDelay={0.4}>
            <WarningOutlined className="route-field-track__warning" />
          </Tooltip>
        )}
        {fields.map((field, index) => {
          const text = `${field.name}=${field.missing ? '未设置' : field.draft}`;
          const isEditing = editing?.name === field.name;
          return (
            <span className="route-field-track__item" key={field.name}>
              {index > 0 && <span className="route-field-track__separator">·</span>}
              {isEditing ? (
                <Tooltip open={Boolean(editing.error)} title={editing.error}>
                  <span
                    className={[
                      'route-field-track__value',
                      'route-field-track__value--editing',
                      editing.error
                        ? 'route-field-track__value--error'
                        : field.missing
                          ? 'route-field-track__value--missing'
                          : '',
                    ]
                      .filter(Boolean)
                      .join(' ')}
                  >
                    <span>{field.name}=</span>
                    <input
                      autoFocus
                      aria-invalid={Boolean(editing.error)}
                      aria-label={`route ${field.name}`}
                      className="route-field-track__value-input"
                      data-floating-window-escape-local
                      spellCheck={false}
                      style={{ width: `${Math.max(field.draft.length, 1)}ch` }}
                      value={editing.draft}
                      onBlur={commit}
                      onChange={(event) =>
                        setEditing({ name: field.name, draft: event.target.value })
                      }
                      onFocus={(event) => {
                        if (typeof event.currentTarget.scrollIntoView === 'function') {
                          event.currentTarget.scrollIntoView({
                            block: 'nearest',
                            inline: 'nearest',
                          });
                        }
                      }}
                      onKeyDown={(event) => {
                        if (event.key === 'Enter') {
                          event.preventDefault();
                          commit();
                        } else if (event.key === 'Escape') {
                          event.preventDefault();
                          setEditing(null);
                        }
                      }}
                    />
                  </span>
                </Tooltip>
              ) : (
                <Tooltip title={text} mouseEnterDelay={0.5}>
                  <button
                    aria-label={`编辑 route 字段 ${field.name}`}
                    className={`route-field-track__value${field.missing ? ' route-field-track__value--missing' : ''}`}
                    type="button"
                    onClick={() => setEditing({ name: field.name, draft: field.draft })}
                  >
                    {text}
                  </button>
                </Tooltip>
              )}
            </span>
          );
        })}
      </div>
    );
  }

  return (
    <div className="route-field-track">
      <div className="route-field-track__scroller">{content}</div>
      <Tooltip title="在浮动窗口编辑 route" mouseEnterDelay={0.4}>
        <Button
          aria-label="在浮动窗口编辑 route"
          className="route-field-track__expand"
          disabled={!canOpen}
          icon={<ExpandAltOutlined />}
          size="small"
          type="text"
          onClick={onOpenFloating}
        />
      </Tooltip>
    </div>
  );
}
