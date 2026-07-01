import { Select, Typography } from 'antd';
import type { CodecConnection, CodecProtocol, CodecRouteSpec } from '@/services/codecConnections';
import { RouteEditor } from '../listens/RouteEditor';
import { serviceFromTargetConnection, targetConnectionValue } from './connectionRouteModel';

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
  const options = connections
    .filter((c) => !protocol || c.protocol === protocol)
    .map((c) => ({ value: c.conn, label: c.conn }));
  const missing = Boolean(value) && !options.some((o) => o.value === value);
  const mergedOptions = missing ? [{ value: value as string, label: value as string }, ...options] : options;
  const spec = value ? specs.get(value) : undefined;

  const onTargetChange = (next: string | undefined) => {
    if (protocol) {
      onChangeService?.(serviceFromTargetConnection(next, protocol));
    } else {
      onChangeServer?.(next);
    }
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div>
        <Typography.Text style={{ fontSize: 12, color: 'var(--text-secondary)' }}>目标连接</Typography.Text>
        <Select
          size={size}
          value={value || undefined}
          onChange={onTargetChange}
          options={mergedOptions.map((o) => ({ value: o.value, label: <code>{o.label}</code> }))}
          loading={connectionsLoading}
          status={connectionsError || missing ? 'error' : undefined}
          placeholder="选择目标连接"
          showSearch
          optionFilterProp="value"
          style={{ width: '100%', marginTop: 4 }}
        />
        {(connectionsError || missing) && (
          <div style={{ fontSize: 11, color: 'var(--danger-color, #ff4d4f)', marginTop: 4 }}>
            {connectionsError ? `协议配置加载失败：${connectionsError}` : '当前目标连接未在协议配置中创建'}
          </div>
        )}
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
