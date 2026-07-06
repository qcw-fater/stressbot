import { Alert, Button, Card, Input, InputNumber, Radio, Space, Switch, Typography } from 'antd';
import { ApiOutlined } from '@ant-design/icons';
import { useState } from 'react';
import type { CodecHeartbeat, CodecSchema } from '@/types/codec';
import type { HeartbeatField } from '@/types/action';
import { setHeartbeat, updateHeartbeat } from './codecEdit';
import { BindingsTable } from '../../FlowEditor/editors/ActionEditor/BindingsTable';
import { HeartbeatFields } from '../../FlowEditor/editors/ActionEditor/HeartbeatFields';
import { ProtoBrowser } from '../../FlowEditor/proto/ProtoBrowser';
import {
  buildRouteTemplateFields,
  updateRouteTemplateField,
} from '../../FlowEditor/listens/routeFormModel';

export interface HeartbeatEditorProps {
  raw: Record<string, unknown>;
  schema: CodecSchema;
  onEdit: (nextContent: string) => void;
}

type HeartbeatBodyMode = 'empty' | 'proto' | 'raw';

function detectBodyMode(hb: CodecHeartbeat): HeartbeatBodyMode {
  if (Object.prototype.hasOwnProperty.call(hb, 'c2sProto')) return 'proto';
  if (Object.prototype.hasOwnProperty.call(hb, 'heartbeatFields')) return 'raw';
  return 'empty';
}

export function HeartbeatEditor({ raw, schema, onEdit }: HeartbeatEditorProps) {
  const hb = schema.heartbeat;
  const [protoPickerOpen, setProtoPickerOpen] = useState(false);
  const routeFields = buildRouteTemplateFields(schema.routeKeyTemplate ?? '', hb?.route);

  const patch = (p: Partial<CodecHeartbeat>) => onEdit(updateHeartbeat(raw, p));
  const bodyMode = hb ? detectBodyMode(hb) : 'empty';

  const switchMode = (mode: HeartbeatBodyMode) => {
    if (!hb || mode === bodyMode) return;
    if (mode === 'empty') {
      onEdit(updateHeartbeat(raw, { c2sProto: undefined, bindings: undefined, heartbeatFields: undefined, skipWhenMissing: undefined }));
    } else if (mode === 'proto') {
      onEdit(updateHeartbeat(raw, { c2sProto: '', heartbeatFields: undefined, skipWhenMissing: undefined }));
    } else {
      onEdit(updateHeartbeat(raw, { heartbeatFields: [], c2sProto: undefined, bindings: undefined }));
    }
  };

  return (
    <Card
      size="small"
      className="pce-bench heartbeat-bench"
      title={(
        <Space size={8} align="center">
          <span className="pce-bench-title">心跳</span>
          <Typography.Text type="secondary" className="pce-bench-meta">连接级可选配置</Typography.Text>
        </Space>
      )}
      extra={hb ? <Button size="small" danger onClick={() => onEdit(setHeartbeat(raw, null))}>删除心跳</Button> : null}
      styles={{ body: { padding: 12 } }}
    >
      {!hb ? (
        <Space direction="vertical" size={10} style={{ width: '100%' }}>
          <Alert
            type="info"
            showIcon
            message="当前连接未启用心跳"
            description="没有 heartbeat 对象时，连接成功后不会自动发送保活包。"
          />
          <Button
            type="primary"
            onClick={() => onEdit(setHeartbeat(raw, { intervalMs: 5000, route: {} }))}
          >
            添加心跳
          </Button>
        </Space>
      ) : (
        <Space direction="vertical" size={14} style={{ width: '100%' }}>
          <div className="pce-inline-row">
            <Typography.Text className="pce-inline-label">intervalMs</Typography.Text>
            <InputNumber
              min={1}
              value={hb.intervalMs}
              onChange={(v) => patch({ intervalMs: (v as number) ?? undefined as unknown as number })}
              style={{ width: 160 }}
            />
            <Typography.Text type="secondary">毫秒</Typography.Text>
          </div>

          <div className="pce-inline-row">
            <Typography.Text className="pce-inline-label">requireSecretKey</Typography.Text>
            <Switch checked={!!hb.requireSecretKey} onChange={(v) => patch({ requireSecretKey: v })} />
            <Typography.Text type="secondary">等待连接密钥设置后再启动心跳</Typography.Text>
          </div>

          <div>
            <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 6 }}>
              route（按当前 routeKeyTemplate 生成字段）
            </Typography.Text>
            {routeFields.length === 0 ? (
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                当前 routeKeyTemplate 不包含占位字段，无需填写 route。
              </Typography.Text>
            ) : (
              <Space direction="vertical" size={6} style={{ width: '100%' }}>
                {routeFields.map((f) => (
                  <div className="pce-route-field" key={f.name}>
                    <Typography.Text code className="pce-route-field-name">{f.name}</Typography.Text>
                    <Input
                      size="small"
                      value={f.draft}
                      status={f.missing ? 'warning' : undefined}
                      onChange={(e) => {
                        const result = updateRouteTemplateField(hb.route, f.name, e.target.value);
                        if (result.ok) patch({ route: result.route });
                      }}
                    />
                  </div>
                ))}
                {routeFields.some((f) => f.missing) && (
                  <Typography.Text type="warning" style={{ fontSize: 12 }}>
                    未填写的 route 字段会导致保存校验失败。
                  </Typography.Text>
                )}
              </Space>
            )}
          </div>

          <div>
            <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 6 }}>
              body 模式
            </Typography.Text>
            <Radio.Group value={bodyMode} onChange={(e) => switchMode(e.target.value as HeartbeatBodyMode)}>
              <Radio.Button value="empty">空 body</Radio.Button>
              <Radio.Button value="proto">protobuf</Radio.Button>
              <Radio.Button value="raw">二进制字段</Radio.Button>
            </Radio.Group>
          </div>

          {bodyMode === 'proto' && (
            <Space direction="vertical" size={10} style={{ width: '100%' }}>
              <div className="pce-inline-row pce-inline-row-full">
                <Typography.Text className="pce-inline-label">c2sProto</Typography.Text>
                <Space.Compact style={{ flex: 1 }}>
                  <Input value={hb.c2sProto ?? ''} onChange={(e) => patch({ c2sProto: e.target.value })} />
                  <Button icon={<ApiOutlined />} onClick={() => setProtoPickerOpen(true)} />
                </Space.Compact>
              </div>
              <BindingsTable
                messageFullName={hb.c2sProto}
                value={hb.bindings}
                onChange={(v) => patch({ bindings: v })}
                label="bindings（心跳 C2S 字段绑定）"
              />
            </Space>
          )}

          {bodyMode === 'raw' && (
            <Space direction="vertical" size={10} style={{ width: '100%' }}>
              <HeartbeatFields
                value={hb.heartbeatFields as HeartbeatField[] | undefined}
                onChange={(v) => patch({ heartbeatFields: v })}
              />
              <div className="pce-inline-row">
                <Typography.Text className="pce-inline-label">skipWhenMissing</Typography.Text>
                <Switch checked={!!hb.skipWhenMissing} onChange={(v) => patch({ skipWhenMissing: v })} />
                <Typography.Text type="secondary">state 源缺失时跳过本 tick</Typography.Text>
              </div>
            </Space>
          )}

          {bodyMode === 'empty' && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              空 body 表示每个 tick 只按 route 编码一个空包体心跳。
            </Typography.Text>
          )}
        </Space>
      )}
      <ProtoBrowser
        windowId="protoPicker_codecHeartbeat"
        open={protoPickerOpen}
        onClose={() => setProtoPickerOpen(false)}
        onSelect={(fullName) => {
          patch({ c2sProto: fullName });
          setProtoPickerOpen(false);
        }}
      />
    </Card>
  );
}
