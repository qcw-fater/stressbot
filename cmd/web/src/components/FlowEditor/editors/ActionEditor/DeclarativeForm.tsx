/**
 * 声明式动作主表单：根据 pattern 动态显示对应字段。
 */

import { Button, Form, Input, InputNumber, Select, Space } from 'antd';
import { ApiOutlined } from '@ant-design/icons';
import { useState } from 'react';
import type { ActionDef, ActionPattern } from '@/types/action';
import { ProtoBrowser } from '../../proto/ProtoBrowser';
import { BindingsTable } from './BindingsTable';
import { StoreTable } from './StoreTable';
import { SetStateEditor } from './SetStateEditor';
import { ClearStateEditor } from './ClearStateEditor';
import { useRuntimeStore } from '@/services/runtimeStore';
import { useFlowStore } from '../../store/flowStore';
import { protoRegistry } from '../../proto/ProtoRegistry';
import { useCodecConnections, useCodecRouteSpecs } from '../../codec/useCodecConnections';
import { actionProtocol } from '../../codec/connectionRouteModel';
import { TargetConnectionRouteEditor } from '../../codec/TargetConnectionRouteEditor';

export interface DeclarativeFormProps {
  action: ActionDef;
  onChange: (a: ActionDef) => void;
}

export function DeclarativeForm({ action, onChange }: DeclarativeFormProps) {
  const { pattern } = action;
  const requestTimeoutSec = useRuntimeStore((s) => s.robotConfig.timeoutSec);
  const stateExtra = useRuntimeStore((s) => s.robotConfig.stateExtra);
  const allActions = useFlowStore((s) => s.actions);
  const allListens = useFlowStore((s) => s.listens);
  const { connections, loading: connectionsLoading, error: connectionsError } = useCodecConnections();
  const { specs, loading: routeSpecsLoading, error: routeSpecsError } = useCodecRouteSpecs();
  const timeoutPlaceholder = pattern === 'tcpRequest' || pattern === 'udpRequest' ? String(requestTimeoutSec || 60) : '60';
  const set = (partial: Partial<ActionDef>) => onChange({ ...action, ...partial });

  // 用 ProtoBrowser 选 c2sProto / s2cProto（受控模式，不触碰 activePanel）
  const [protoTarget, setProtoTarget] = useState<'c2s' | 's2c' | null>(null);

  const showService = patternHas(pattern, ['service']);
  const showRoute = patternHas(pattern, ['route']);
  const showTargetConnection = showService && showRoute;
  const showAddress = patternHas(pattern, ['address']);
  const showC2S = patternHas(pattern, ['c2sProto']);
  const showS2C = patternHas(pattern, ['s2cProto']);
  const showBindings = patternHas(pattern, ['bindings']);
  const showStore = patternHas(pattern, ['store']);
  const showTimeout = pattern === 'tcpListen' || pattern === 'udpListen' || pattern === 'tcpRequest' || pattern === 'udpRequest';
  const showPollMs = pattern === 'tcpListen' || pattern === 'udpListen';
  const showURL = pattern === 'httpRequest';
  const showMethod = pattern === 'httpRequest';
  const showContentType = pattern === 'httpRequest';
  const protocol = actionProtocol(pattern);

  return (
    <Form layout="vertical">
      {showURL && (
        <Form.Item label="URL（请求地址，支持 state:key）">
          <Input
            value={action.url ?? ''}
            onChange={(e) => set({ url: e.target.value })}
            placeholder="如 http://192.168.1.1:8080/api/login 或 state:loginUrl"
          />
        </Form.Item>
      )}

      {showMethod && (
        <Form.Item label="Method（HTTP 方法）">
          <Select
            value={action.method || 'POST'}
            onChange={(v) => set({ method: v })}
            options={[
              { value: 'GET', label: 'GET' },
              { value: 'POST', label: 'POST' },
              { value: 'PUT', label: 'PUT' },
              { value: 'DELETE', label: 'DELETE' },
            ]}
            style={{ width: 200 }}
          />
        </Form.Item>
      )}

      {showContentType && (
        <Form.Item label="ContentType（请求体格式）">
          <Select
            value={action.contentType || 'json'}
            onChange={(v) => set({ contentType: v })}
            options={[
              { value: 'json', label: 'application/json' },
              { value: 'form', label: 'application/x-www-form-urlencoded' },
            ]}
            style={{ width: 260 }}
          />
        </Form.Item>
      )}

      {showTargetConnection && protocol && (
        <Form.Item label="目标连接 + route 模板字段">
          <TargetConnectionRouteEditor
            protocol={protocol}
            service={action.service}
            onChangeService={(v) => set({ service: v })}
            route={action.route}
            onChangeRoute={(v) => set({ route: v })}
            connections={connections}
            connectionsLoading={connectionsLoading}
            connectionsError={connectionsError}
            specs={specs}
            routeSpecsLoading={routeSpecsLoading}
            routeSpecsError={routeSpecsError}
          />
        </Form.Item>
      )}

      {showService && !showRoute && protocol && (
        <Form.Item label="目标连接">
          <TargetConnectionRouteEditor
            protocol={protocol}
            service={action.service}
            onChangeService={(v) => set({ service: v })}
            connections={connections}
            connectionsLoading={connectionsLoading}
            connectionsError={connectionsError}
            specs={specs}
            routeSpecsLoading={routeSpecsLoading}
            routeSpecsError={routeSpecsError}
          />
        </Form.Item>
      )}

      {showAddress && (
        <Form.Item label="address（地址，支持 state:key）">
          <Input
            value={action.address ?? ''}
            onChange={(e) => set({ address: e.target.value })}
            placeholder="如 192.168.1.1:8080 或 state:battleAddr"
          />
        </Form.Item>
      )}

      {showC2S && (
        <Form.Item label="c2sProto（C2S 消息全名）">
          <Space.Compact style={{ width: '100%' }}>
            <Input
              value={action.c2sProto ?? ''}
              onChange={(e) => set({ c2sProto: e.target.value })}
              placeholder="如 Game.LoginPlayerC2S"
            />
            <Button icon={<ApiOutlined />} onClick={() => setProtoTarget('c2s')} />
          </Space.Compact>
          <ProtoHint fullName={action.c2sProto} />
        </Form.Item>
      )}

      {showS2C && (
        <Form.Item label="s2cProto（S2C 响应全名）">
          <Space.Compact style={{ width: '100%' }}>
            <Input
              value={action.s2cProto ?? ''}
              onChange={(e) => set({ s2cProto: e.target.value })}
              placeholder="如 Game.LoginPlayerS2C"
            />
            <Button icon={<ApiOutlined />} onClick={() => setProtoTarget('s2c')} />
          </Space.Compact>
          <ProtoHint fullName={action.s2cProto} />
        </Form.Item>
      )}

      {pattern === 'setState' && (
        <div style={{ marginBottom: 24 }}>
          <SetStateEditor value={action.bindings} onChange={(bindings) => set({ bindings })} />
        </div>
      )}

      {showBindings && pattern !== 'setState' && (
        <div style={{ marginBottom: 24 }}>
          <BindingsTable
            messageFullName={pattern === 'httpRequest' ? undefined : action.c2sProto}
            value={action.bindings}
            onChange={(v) => set({ bindings: v })}
            label={pattern === 'httpRequest' ? 'bindings（请求体字段）' : 'bindings（C2S 字段绑定）'}
          />
        </div>
      )}

      {showStore && (
        <div style={{ marginBottom: 24 }}>
          <StoreTable
            s2cProto={pattern === 'httpRequest' ? undefined : action.s2cProto}
            value={action.store}
            onChange={(v) => set({ store: v })}
            label={pattern === 'httpRequest' ? 'store（JSON 响应 → state）' : 'store（S2C → state）'}
            actions={allActions}
            listens={allListens}
            stateExtra={stateExtra}
          />
        </div>
      )}

      {showTimeout && (
        <Form.Item label="超时（秒）">
          <Space>
            <Space.Compact>
              <InputNumber
                min={0}
                precision={0}
                step={1}
                placeholder={timeoutPlaceholder}
                value={action.timeout}
                onChange={(v) => set({ timeout: (v as number) ?? undefined })}
                style={{ width: 120 }}
              />
              <span style={{ display: 'flex', alignItems: 'center', padding: '0 8px', background: 'var(--container-bg)', border: '1px solid var(--border-color)', borderRadius: '0 6px 6px 0', fontSize: 13 }}>s</span>
            </Space.Compact>
            {showPollMs && (
              <>
                <span>轮询间隔</span>
                <Space.Compact>
                  <InputNumber
                    min={0}
                    precision={0}
                    step={1}
                    placeholder="100"
                    value={action.pollMs}
                    onChange={(v) => set({ pollMs: (v as number) ?? undefined })}
                    style={{ width: 120 }}
                  />
                  <span style={{ display: 'flex', alignItems: 'center', padding: '0 8px', background: 'var(--container-bg)', border: '1px solid var(--border-color)', borderRadius: '0 6px 6px 0', fontSize: 13 }}>ms</span>
                </Space.Compact>
              </>
            )}
          </Space>
        </Form.Item>
      )}

      {(pattern === 'tcpListen' || pattern === 'udpListen') && (
        <div style={{ fontSize: 11, color: 'var(--text-tertiary)', marginTop: -8, marginBottom: 16 }}>
          此 route 必须在之前节点的 listenRefs 中预注册（listen=null），否则运行时会超时
        </div>
      )}

      {pattern === 'clearState' && (
        <Form.Item label="keys（要清除的 state key 列表）">
          <ClearStateEditor value={action.keys} onChange={(keys) => set({ keys })} />
        </Form.Item>
      )}

      <ProtoBrowser
        windowId="protoPicker_action"
        open={protoTarget !== null}
        onClose={() => setProtoTarget(null)}
        onSelect={(fullName) => {
          if (protoTarget === 'c2s') set({ c2sProto: fullName });
          else if (protoTarget === 's2c') set({ s2cProto: fullName });
          setProtoTarget(null);
        }}
      />
    </Form>
  );
}

function patternHas(pattern: ActionPattern, fields: Array<keyof ActionDef>): boolean {
  const map: Partial<Record<ActionPattern, Array<keyof ActionDef>>> = {
    tcpSend: ['service', 'route', 'c2sProto', 'bindings'],
    tcpRequest: ['service', 'route', 'c2sProto', 's2cProto', 'bindings', 'store'],
    tcpConnect: ['service', 'address'],
    tcpClose: ['service'],
    tcpListen: ['service', 'route', 's2cProto', 'store'],
    udpSend: ['service', 'route', 'c2sProto', 'bindings'],
    udpRequest: ['service', 'route', 'c2sProto', 's2cProto', 'bindings', 'store'],
    udpConnect: ['service', 'address'],
    udpClose: ['service'],
    udpListen: ['service', 'route', 's2cProto', 'store'],
    httpRequest: ['bindings', 'store'],
    clearState: [],
    setState: ['bindings'],
    lua: [],
  };
  const allowed = map[pattern] ?? [];
  return fields.some((f) => allowed.includes(f));
}

function ProtoHint({ fullName }: { fullName?: string }) {
  if (!fullName || !protoRegistry.isLoaded()) return null;
  const msg = protoRegistry.lookupMessage(fullName);
  if (!msg?.comment) return null;
  return (
    <div style={{ fontSize: 11, color: 'var(--text-tertiary)', marginTop: 2 }}>
      {msg.comment}
    </div>
  );
}
