import { Select, Tooltip, Typography } from 'antd';
import { WarningOutlined } from '@ant-design/icons';
import type { CodecConnection, CodecProtocol, CodecRouteSpec } from '@/services/codecConnections';
import { RouteEditor } from '../listens/RouteEditor';
import { serviceFromTargetConnection, targetConnectionValue } from './connectionRouteModel';
import './TargetConnectionRouteEditor.css';

interface Props {
  protocol?: CodecProtocol;
  service?: string;
  onChangeService?: (service: string | undefined) => void;
  server?: string;
  onChangeServer?: (server: string | undefined) => void;
  route?: unknown;
  onChangeRoute?: (route: unknown) => void;
  connections: CodecConnection[];
  connectionsLoading?: boolean;
  connectionsError?: string | null;
  specs: Map<string, CodecRouteSpec>;
  routeSpecsLoading?: boolean;
  routeSpecsError?: string | null;
  size?: 'small' | 'middle' | 'large';
}

export interface TargetConnectionSelectProps {
  protocol?: CodecProtocol;
  service?: string;
  onChangeService?: (service: string | undefined) => void;
  server?: string;
  onChangeServer?: (server: string | undefined) => void;
  connections: CodecConnection[];
  loading?: boolean;
  error?: string | null;
  size?: 'small' | 'middle' | 'large';
  inline?: boolean;
}

export function TargetConnectionSelect({
  protocol,
  service,
  onChangeService,
  server,
  onChangeServer,
  connections,
  loading,
  error,
  size,
  inline = false,
}: TargetConnectionSelectProps) {
  const value = targetConnectionValue({ server, protocol, service });
  const options = connections
    .filter((c) => !protocol || c.protocol === protocol)
    .map((c) => ({ value: c.conn, label: c.conn }));
  const missing = Boolean(value) && !options.some((o) => o.value === value);
  const mergedOptions = missing ? [{ value: value as string, label: value as string }, ...options] : options;
  const issue = error ? `协议配置加载失败：${error}` : missing ? '当前目标连接未在协议配置中创建' : undefined;

  const onTargetChange = (next: string | undefined) => {
    if (protocol) {
      onChangeService?.(serviceFromTargetConnection(next, protocol));
    } else {
      onChangeServer?.(next);
    }
  };

  return (
    <div className={`target-connection-select target-connection-select--${inline ? 'inline' : 'stacked'}`}>
      <Select
        aria-label="目标连接"
        size={size}
        value={value || undefined}
        onChange={onTargetChange}
        options={mergedOptions.map((o) => ({ value: o.value, label: <code>{o.label}</code> }))}
        loading={loading}
        status={issue ? 'error' : undefined}
        placeholder="选择目标连接"
        showSearch
        optionFilterProp="value"
        style={{
          width: '100%',
          minWidth: inline ? 0 : 120,
          flex: inline ? '1 1 0' : undefined,
        }}
      />
      {issue && inline && (
        <Tooltip title={issue} mouseEnterDelay={0.4}>
          <WarningOutlined className="target-connection-select__warning" />
        </Tooltip>
      )}
      {issue && !inline && (
        <div className="target-connection-select__error">
          {issue}
        </div>
      )}
    </div>
  );
}

export function TargetConnectionRouteEditor({
  protocol,
  service,
  onChangeService,
  server,
  onChangeServer,
  route,
  onChangeRoute,
  connections,
  connectionsLoading,
  connectionsError,
  specs,
  routeSpecsLoading,
  routeSpecsError,
  size,
}: Props) {
  const value = targetConnectionValue({ server, protocol, service });
  const spec = value ? specs.get(value) : undefined;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div>
        <Typography.Text style={{ fontSize: 12, color: 'var(--text-secondary)' }}>目标连接</Typography.Text>
        <div style={{ marginTop: 4 }}>
          <TargetConnectionSelect
            protocol={protocol}
            service={service}
            onChangeService={onChangeService}
            server={server}
            onChangeServer={onChangeServer}
            connections={connections}
            loading={connectionsLoading}
            error={connectionsError}
            size={size}
          />
        </div>
      </div>
      {onChangeRoute && (
        <div>
          <Typography.Text style={{ fontSize: 12, color: 'var(--text-secondary)' }}>route 模板字段</Typography.Text>
          <div style={{ marginTop: 4 }}>
            <RouteEditor
              size={size}
              value={route}
              server={value}
              routeKeyTemplate={spec?.routeKeyTemplate}
              loading={routeSpecsLoading}
              error={routeSpecsError}
              onChange={onChangeRoute}
            />
          </div>
        </div>
      )}
    </div>
  );
}
