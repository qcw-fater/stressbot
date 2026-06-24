/**
 * PreviewPanel — codec 编解码实时预览面板（T3 Batch-3 任务 B）。
 *
 * 设计目标：让用户在不发起真实网络任务的前提下，调后端真实 codec 引擎
 * （POST /sbot/codec/preview）跑一次 encode/decode，确认 schema 的编解码效果。
 *
 * 关键决策：
 *   - **手动「预览」按钮触发**（不自动每次按键）——避免抖动 / 频繁请求 / 错误闪烁；
 *     按钮带 loading 态，用户感知「正在跑」。
 *   - **错误双通道**：PreviewResult.error 非空（schema 编译失败 / 坏 hex 等，HTTP 200）
 *     → Alert 中文展示；previewCodec 自身抛错（网络 / 非 2xx）→ catch Alert 中文「预览失败：…」。
 *   - **纯编辑辅助**：不落 IDB、不改 baseline、不进任务下发（不调任何 set* / save）。
 *   - **schema 非法仍允许预览**——后端会回填中文 error，用户据此修 schema。
 *   - **请求收拢 services**：走 `services/codecApi.ts` 的 `previewCodec`，不直接 fetch。
 *
 * Props 的 `raw` 用作 preview 的 schema 入参（lossless 原对象，由 AdapterTab 的
 * parseCodecForEdit 提供）；`schema` 的 typed 视图读 route 字段清单；`transport` 来自连接名。
 */

import { useState } from 'react';
import { CopyOutlined } from '@ant-design/icons';
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Empty,
  Input,
  Segmented,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import type { CodecSchema, PreviewField, PreviewRequest, PreviewResult } from '@/types/codec';
import { previewCodec } from '@/services/codecApi';
import {
  buildRouteMap,
  collectRouteFields,
  type PreviewTransport,
} from './previewHelpers';

export interface PreviewPanelProps {
  /** codec.json 原对象（lossless）——直接作 preview 请求的 schema 入参。 */
  raw: Record<string, unknown>;
  /** 同一对象的 typed 宽松视图——读 route 字段清单。 */
  schema: CodecSchema;
  /** 传输协议（由连接名推导）。 */
  transport: PreviewTransport;
}

type Mode = 'encode' | 'decode';

/** route 字段表单值：fieldName → 用户输入字符串。 */
type RouteForm = Record<string, string>;

/**
 * hex 文本框：统一 `Input.TextArea`，monospace 小号；placeholder 各自给。
 * 非受控「预览」按钮触发，输入不立即请求。
 */
function HexInput(props: { value: string; onChange: (v: string) => void; placeholder: string; rows?: number }) {
  return (
    <Input.TextArea
      value={props.value}
      onChange={(e) => props.onChange(e.target.value)}
      placeholder={props.placeholder}
      rows={props.rows ?? 2}
      autoSize={{ minRows: 1, maxRows: 4 }}
      style={{ fontFamily: 'monospace', fontSize: 12 }}
    />
  );
}

/**
 * 把任意 hex 字符串规范化为「按字节分组展示」：
 *   - 抽出所有 [0-9a-fA-F] 字符，每两位一组（不足偶数时尾部留单字符）。
 *   - 字节之间用单空格分隔；每 8 字节（= 一段）末尾再加一个空格，增强长 hex 可读性。
 *   - 每 16 字节换行（行间视觉断句）。
 *
 * 仅用于展示，回灌输入仍走原始字符串（HexInput 的 onChange 保留用户原样）。
 */
function formatHexGrouped(raw: string): string {
  const cleaned = raw.replace(/[^0-9a-fA-F]/g, '');
  if (cleaned.length === 0) return '';
  const bytes: string[] = [];
  for (let i = 0; i < cleaned.length; i += 2) {
    bytes.push(cleaned.slice(i, i + 2));
  }
  const lines: string[] = [];
  for (let i = 0; i < bytes.length; i += 16) {
    const lineBytes = bytes.slice(i, i + 16);
    // 每 8 字节插一个额外空格作为「半行」分隔。
    const parts: string[] = [];
    for (let j = 0; j < lineBytes.length; j += 8) {
      parts.push(lineBytes.slice(j, j + 8).join(' '));
    }
    lines.push(parts.join('  '));
  }
  return lines.join('\n');
}

/** 一段可复制的 hex 结果展示（带复制按钮）。 */
function HexOutput(props: { label: string; value: string | undefined }) {
  const { message } = AntApp.useApp();
  const val = props.value ?? '';
  return (
    <div>
      <Space size={6} align="center" style={{ marginBottom: 4 }}>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {props.label}
        </Typography.Text>
        {val && (
          <Button
            size="small"
            type="text"
            icon={<CopyOutlined />}
            onClick={() => {
              if (!navigator.clipboard) {
                message.warning('当前环境不支持复制');
                return;
              }
              navigator.clipboard.writeText(val).then(
                () => message.success('已复制'),
                () => message.error('复制失败'),
              );
            }}
          />
        )}
      </Space>
      <div
        style={{
          fontFamily: 'monospace',
          fontSize: 12,
          padding: '6px 8px',
          background: 'var(--hover-bg)',
          borderRadius: 4,
          whiteSpace: 'pre-wrap',
          minHeight: 28,
          lineHeight: 1.6,
        }}
      >
        {val ? formatHexGrouped(val) : <Typography.Text type="secondary" style={{ fontSize: 12 }}>（空）</Typography.Text>}
      </div>
    </div>
  );
}

/** 字段解释表（encode/decode 共用，展示 PreviewField[]）。 */
function FieldsTable({ fields }: { fields: PreviewField[] | undefined }) {
  const cols: ColumnsType<PreviewField> = [
    { title: '字段', dataIndex: 'name', key: 'name', render: (v: string) => <code>{v}</code> },
    { title: '值', dataIndex: 'value', key: 'value' },
    { title: '偏移', dataIndex: 'offset', key: 'offset', width: 60 },
    { title: '字节', dataIndex: 'size', key: 'size', width: 60 },
  ];
  if (!fields || fields.length === 0) {
    return <Empty description={<span style={{ fontSize: 12 }}>无字段解释</span>} image={Empty.PRESENTED_IMAGE_SIMPLE} />;
  }
  return (
    <Table<PreviewField>
      rowKey={(r) => `${r.name}@${r.offset}`}
      size="small"
      pagination={false}
      columns={cols}
      dataSource={fields}
    />
  );
}

export function PreviewPanel({ raw, schema, transport }: PreviewPanelProps) {
  const [mode, setMode] = useState<Mode>('encode');

  // encode 输入
  const [routeForm, setRouteForm] = useState<RouteForm>({});
  const [bodyHex, setBodyHex] = useState<string>('');
  // decode 输入
  const [frameHex, setFrameHex] = useState<string>('');
  // 共用
  const [keyHex, setKeyHex] = useState<string>('');

  const [loading, setLoading] = useState(false);
  const [result, setResult] = useState<PreviewResult | null>(null);
  const [reqError, setReqError] = useState<string | null>(null);

  const routeFields = collectRouteFields(schema);

  const runPreview = async () => {
    setLoading(true);
    setReqError(null);
    try {
      const req: PreviewRequest =
        mode === 'encode'
          ? {
              schema: raw,
              mode: 'encode',
              transport,
              route: buildRouteMap(routeForm),
              bodyHex: bodyHex.trim(),
              keyHex: keyHex.trim() || undefined,
            }
          : {
              schema: raw,
              mode: 'decode',
              transport,
              frameHex: frameHex.trim(),
              keyHex: keyHex.trim() || undefined,
            };
      const res = await previewCodec(req);
      setResult(res);
    } catch (e) {
      // previewCodec 自身抛错（网络 / 非 2xx）——中文 Alert 展示，不吞。
      setReqError((e as Error).message);
      setResult(null);
    } finally {
      setLoading(false);
    }
  };

  const hasError = !!reqError || (result?.error && result.error !== '');
  const empty = !result && !reqError;

  return (
    <Card size="small" title="预览" styles={{ body: { padding: 12 } }}>
      <Space direction="vertical" size={10} style={{ width: '100%' }}>
        {/* 模式切换 + 协议标签 */}
        <Space size={8} wrap align="center">
          <Segmented<Mode>
            size="small"
            value={mode}
            onChange={(v) => {
              setMode(v);
              // 切换模式清空上次结果，避免显示另一种模式的过期输出。
              setResult(null);
              setReqError(null);
            }}
            options={[
              { label: '编码', value: 'encode' },
              { label: '解码', value: 'decode' },
            ]}
          />
          <Tag style={{ fontSize: 12 }}>{transport.toUpperCase()}</Tag>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            调用后端真实 codec 引擎，不发起网络任务
          </Typography.Text>
        </Space>

        <div className="split-2">
          {/* 左：输入 + 触发 */}
          <div className="split-2-left">
            <Space direction="vertical" size={10} style={{ width: '100%' }}>
              {mode === 'encode' ? (
                <>
                  <div>
                    <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                      路由字段（role:&quot;route&quot;，数值化取整）
                    </Typography.Text>
                    {routeFields.length === 0 ? (
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                        当前 header 无 role:&quot;route&quot; 字段
                      </Typography.Text>
                    ) : (
                      <Space size={6} wrap>
                        {routeFields.map((f) => (
                          <Space key={f.name} size={4} align="center">
                            <Typography.Text code style={{ fontSize: 12 }}>{f.name}</Typography.Text>
                            <Input
                              size="small"
                              style={{ width: 90 }}
                              placeholder="0"
                              value={routeForm[f.name] ?? ''}
                              onChange={(e) =>
                                setRouteForm((prev) => ({ ...prev, [f.name]: e.target.value }))
                              }
                            />
                          </Space>
                        ))}
                      </Space>
                    )}
                  </div>
                  <div>
                    <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                      body hex
                    </Typography.Text>
                    <HexInput value={bodyHex} onChange={setBodyHex} placeholder="body hex，如 0a 2b" />
                  </div>
                </>
              ) : (
                <div>
                  <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                    帧 hex（完整帧）
                  </Typography.Text>
                  <HexInput value={frameHex} onChange={setFrameHex} placeholder="粘贴完整帧 hex" />
                </div>
              )}
              <div>
                <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                  key hex（可空）
                </Typography.Text>
                <HexInput value={keyHex} onChange={setKeyHex} placeholder="secretKey hex，可空" />
              </div>
              <Button type="primary" size="small" loading={loading} onClick={runPreview}>
                预览
              </Button>
            </Space>
          </div>

          {/* 右：结果 */}
          <div className="split-2-right">
            {empty && (
              <Empty
                description={<span style={{ fontSize: 12 }}>点「预览」查看编解码结果</span>}
                image={Empty.PRESENTED_IMAGE_SIMPLE}
              />
            )}
            {reqError && <Alert type="error" showIcon message={reqError} />}
            {result && result.error && <Alert type="error" showIcon message={result.error} />}
            {result && !result.error && !reqError && (
              <Space direction="vertical" size={8} style={{ width: '100%' }}>
                {mode === 'encode' ? (
                  <>
                    <HexOutput label="帧 hex" value={result.frameHex} />
                    <div>
                      <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                        字段解释
                      </Typography.Text>
                      <FieldsTable fields={result.fields} />
                    </div>
                  </>
                ) : (
                  <>
                    {result.routeKey !== undefined && (
                      <div>
                        <Typography.Text type="secondary" style={{ fontSize: 12 }}>路由键：</Typography.Text>{' '}
                        <Typography.Text code style={{ fontSize: 12 }}>{result.routeKey || '（空）'}</Typography.Text>
                      </div>
                    )}
                    {result.headerErr !== undefined && result.headerErr !== 0 && (
                      <Alert
                        type="error"
                        showIcon
                        style={{ padding: '4px 12px' }}
                        message={<span style={{ fontSize: 12 }}>头 errorCode 非 0：<code>{result.headerErr}</code></span>}
                      />
                    )}
                    <div>
                      <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                        字段解释
                      </Typography.Text>
                      <FieldsTable fields={result.fields} />
                    </div>
                    <HexOutput label="body hex" value={result.bodyHex} />
                  </>
                )}
              </Space>
            )}
            {!hasError && !empty && (
              <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                结果仅用于核对，不会保存或影响任务下发。
              </Typography.Text>
            )}
          </div>
        </div>
      </Space>
    </Card>
  );
}
