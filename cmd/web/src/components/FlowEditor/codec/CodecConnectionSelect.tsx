import { Select, Typography } from 'antd';
import type { CodecConnection, CodecProtocol } from '@/services/codecConnections';
import { buildCodecConnectionName } from '@/services/codecConnections';

interface CodecServiceSelectProps {
  protocol: CodecProtocol;
  value?: string;
  onChange: (service: string | undefined) => void;
  connections: CodecConnection[];
  loading?: boolean;
  error?: string | null;
}

interface CodecServerSelectProps {
  value?: string;
  onChange: (server: string | undefined) => void;
  connections: CodecConnection[];
  loading?: boolean;
  error?: string | null;
  size?: 'small' | 'middle' | 'large';
}

export function CodecServiceSelect({ protocol, value, onChange, connections, loading, error }: CodecServiceSelectProps) {
  const options = connections
    .filter((c) => c.protocol === protocol)
    .map((c) => ({ value: c.service, label: c.service, conn: c.conn }));
  const expectedConn = value ? buildCodecConnectionName(protocol, value) : '';
  const missing = Boolean(value) && !options.some((o) => o.value === value);
  const mergedOptions = missing ? [{ value: value as string, label: value as string, conn: expectedConn }, ...options] : options;
  return (
    <div>
      <Select
        value={value || undefined}
        onChange={onChange}
        options={mergedOptions.map((o) => ({
          value: o.value,
          label: (
            <span>
              <code>{o.label}</code> <Typography.Text type="secondary">{o.conn}</Typography.Text>
            </span>
          ),
        }))}
        loading={loading}
        status={error || missing ? 'error' : undefined}
        placeholder="选择 service"
        showSearch
        optionFilterProp="value"
        style={{ width: '100%' }}
      />
      {(error || missing) && (
        <div style={{ fontSize: 11, color: 'var(--danger-color, #ff4d4f)', marginTop: 4 }}>
          {error ? `协议配置加载失败：${error}` : `当前 service 未在协议配置中创建：${expectedConn}`}
        </div>
      )}
    </div>
  );
}

export function CodecServerSelect({ value, onChange, connections, loading, error, size }: CodecServerSelectProps) {
  const options = connections.map((c) => ({ value: c.conn, label: c.conn }));
  const missing = Boolean(value) && !options.some((o) => o.value === value);
  const mergedOptions = missing ? [{ value: value as string, label: value as string }, ...options] : options;
  return (
    <div style={{ width: '100%' }}>
      <Select
        size={size}
        value={value || undefined}
        onChange={onChange}
        options={mergedOptions.map((o) => ({ value: o.value, label: <code>{o.label}</code> }))}
        loading={loading}
        status={error || missing ? 'error' : undefined}
        placeholder="选择 server"
        showSearch
        optionFilterProp="value"
        style={{ width: '100%' }}
      />
      {(error || missing) && (
        <div style={{ fontSize: 11, color: 'var(--danger-color, #ff4d4f)', marginTop: 4 }}>
          {error ? `协议配置加载失败：${error}` : '当前 server 未在协议配置中创建'}
        </div>
      )}
    </div>
  );
}
