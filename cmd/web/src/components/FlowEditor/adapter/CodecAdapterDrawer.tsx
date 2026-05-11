/**
 * 协议适配器（codec.lua）管理面板。
 *
 * codec.lua 存储在 IDB（与 proto/scripts 同模型），启动任务时自动上传。
 * 打开面板时从 IDB 读取，IDB 没有则从基线 `/conf/adapter/codec.lua` 拉取。
 */

import { useEffect, useState } from 'react';
import { Alert, App as AntApp, Button, Drawer, Space, Tabs, Upload } from 'antd';
import { InboxOutlined, UploadOutlined } from '@ant-design/icons';
import Editor from '@monaco-editor/react';
import type { UploadProps } from 'antd';
import { useEditorStore } from '../store/editorStore';
import { getAdapterScript, setAdapterScript, clearAdapterScript } from '@/services/resourcesStore';

const REQUIRED_FUNCTIONS: Array<{
  name: string;
  signature: string;
  desc: string;
  category: 'meta' | 'encode' | 'decode' | 'route';
}> = [
  {
    name: 'header_size',
    signature: 'header_size() -> integer',
    desc: '返回协议头固定字节数。初始化时调用一次并缓存。',
    category: 'meta',
  },
  {
    name: 'body_length_info',
    signature: 'body_length_info() -> { offset, field_type, includes_header }',
    desc: '描述如何从协议头字节中解析消息体长度。引擎使用此元信息进行高效解析（不再调用脚本）。',
    category: 'meta',
  },
  {
    name: 'encode_tcp',
    signature: 'encode_tcp(route, body, secret_key) -> string',
    desc: 'TCP 编码：根据 route + body + secret_key 拼装完整数据包（含 header）。secret_key 为 nil 表示不加密。',
    category: 'encode',
  },
  {
    name: 'encode_udp',
    signature: 'encode_udp(route, body, secret_key) -> string',
    desc: 'UDP 编码：与 encode_tcp 类似，但前 N 字节保持明文（供服务端按明文头查密钥表）。',
    category: 'encode',
  },
  {
    name: 'decode_tcp',
    signature: 'decode_tcp(data, secret_key) -> response_key, body, header_err',
    desc: 'TCP 解码：返回路由键（如 "3:1"）、消息体、协议头错误码。',
    category: 'decode',
  },
  {
    name: 'decode_udp',
    signature: 'decode_udp(data, secret_key) -> response_key, body, header_err',
    desc: 'UDP 解码：与 decode_tcp 分离，允许对 UDP 使用不同策略。',
    category: 'decode',
  },
  {
    name: 'expected_response_key',
    signature: 'expected_response_key(route) -> string',
    desc: '从发送 route 计算期望的响应路由键。用于 TCPRequest 注册临时通道做请求-响应匹配。',
    category: 'route',
  },
];

const TEMPLATE_LUA = `-- conf/adapter/codec.lua
-- 通用协议适配器模板。请按照下面的接口要求实现具体逻辑。

-- ─── 元信息（Go 初始化时调用一次） ────────────────────────────────
function header_size()
    return 12  -- TODO: 你的协议头字节数
end

function body_length_info()
    return {
        offset          = 0,           -- header 中 body 长度字段的起始字节
        field_type      = "uint32_le", -- "uint16_le" / "uint16_be" / "uint32_le" / "uint32_be"
        includes_header = false,       -- 长度字段是否包含 header 自身
    }
end

-- ─── 编码 ─────────────────────────────────────────────────────────
function encode_tcp(route, body, secret_key)
    -- TODO: 把 route + body 组装成完整 TCP 包（含 header）。
    --   - route 是 flow.json 中声明的不透明路由信息
    --   - body 是已序列化的 protobuf 字节
    --   - secret_key 非 nil 时需要按你的算法加密 body
    return body
end

function encode_udp(route, body, secret_key)
    -- TODO: 与 encode_tcp 类似，但若有 UDP 加密偏移须保持前 N 字节明文。
    return body
end

-- ─── 解码 ─────────────────────────────────────────────────────────
function decode_tcp(data, secret_key)
    -- TODO: 解析 data，返回 response_key（"cmd:act" 风格）、body、header_err。
    return "0:0", "", 0
end

function decode_udp(data, secret_key)
    return "0:0", "", 0
end

-- ─── 路由匹配 ─────────────────────────────────────────────────────
function expected_response_key(route)
    -- TODO: 根据 route 推导服务端响应包的 response_key，用于请求-响应匹配。
    return ""
end
`;

export function CodecAdapterDrawer() {
  const { message } = AntApp.useApp();
  const activePanel = useEditorStore((s) => s.activePanel.codecAdapter);
  const themeMode = useEditorStore((s) => s.theme);
  const monacoTheme = themeMode === 'dark' ? 'vs-dark' : 'light';
  const closePanel = useEditorStore((s) => s.closePanel);
  const open = activePanel?.kind === 'codecAdapter';

  const [content, setContent] = useState('');
  const [source, setSource] = useState<string | null>(null);
  const [loadError, setLoadError] = useState(false);

  useEffect(() => {
    if (!open) return;
    setLoadError(false);
    getAdapterScript().then((file) => {
      if (file) {
        setContent(file.content);
        setSource('已保存');
      } else {
        // 从基线拉取
        fetch('/conf/adapter/codec.lua')
          .then((r) => (r.ok ? r.text() : null))
          .then((text) => {
            if (text) {
              setContent(text);
              setSource('默认模板');
              setAdapterScript(text, true);
            } else {
              setContent('');
              setSource(null);
              setLoadError(true);
            }
          })
          .catch(() => {
            setContent('');
            setSource(null);
            setLoadError(true);
          });
      }
    });
  }, [open]);

  const onUpload: UploadProps['beforeUpload'] = async (file) => {
    const text = await file.text();
    setContent(text);
    setSource(file.name);
    setLoadError(false);
    await setAdapterScript(text);
    message.success(`已加载并保存：${file.name}`);
    return false;
  };

  const onUseTemplate = () => {
    setContent(TEMPLATE_LUA);
    setSource('模板（未保存）');
    setLoadError(false);
    message.info('已载入模板，编辑后点击保存');
  };

  const onSave = async () => {
    if (!content.trim()) {
      message.warning('内容为空');
      return;
    }
    await setAdapterScript(content);
    setSource('已保存');
    setLoadError(false);
    message.success('已保存到浏览器存储，启动任务时会自动上传');
  };

  const onClear = async () => {
    await clearAdapterScript();
    setContent('');
    setSource(null);
    setLoadError(true);
    message.success('已清空');
  };

  return (
    <Drawer
      open={open}
      title="协议适配器"
      width={760}
      onClose={() => closePanel('codecAdapter')}
      destroyOnHidden
    >
      <Tabs
        defaultActiveKey="edit"
        items={[
          {
            key: 'edit',
            label: '编辑',
            children: (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
                <Alert
                  type="info"
                  showIcon
                  message="协议适配器随任务下发"
                  description="编辑后点保存。启动任务时会自动上传到服务端，无需手动部署。"
                />
                {loadError && (
                  <Alert
                    type="error"
                    showIcon
                    message="未找到协议适配器"
                    description="未找到适配器文件且默认模板不可用。请导入文件或载入空模板后保存。"
                  />
                )}
                <Space>
                  <Upload accept=".lua,text/plain" beforeUpload={onUpload} showUploadList={false}>
                    <Button icon={<UploadOutlined />}>导入 .lua 文件</Button>
                  </Upload>
                  <Button onClick={onUseTemplate} icon={<InboxOutlined />}>
                    载入空模板
                  </Button>
                  <Button onClick={onSave} type="primary">
                    保存
                  </Button>
                  <Button onClick={onClear} danger>
                    清空
                  </Button>
                </Space>
                <div style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>
                  {source ? `来源：${source}` : '尚未加载文件'}
                </div>
                <Editor
                  height="55vh"
                  language="lua"
                  theme={monacoTheme}
                  value={content}
                  onChange={(v) => setContent(v ?? '')}
                  options={{
                    fontSize: 12,
                    minimap: { enabled: false },
                    scrollBeyondLastLine: false,
                  }}
                />
              </div>
            ),
          },
          {
            key: 'spec',
            label: '接口规范',
            children: (
              <div style={{ fontSize: 13, lineHeight: 1.7 }}>
                <Alert
                  type="warning"
                  showIcon
                  message="必须实现以下 7 个全局函数"
                  description="协议适配器是消息解析的核心。所有协议头解析、加解密、路由匹配都由你来实现。引擎只调用以下接口，不感知具体协议格式。"
                  style={{ marginBottom: 16 }}
                />
                <SpecBlock title="1. 元信息（初始化时调用一次，结果缓存）" items={REQUIRED_FUNCTIONS.filter((f) => f.category === 'meta')} />
                <SpecBlock title="2. 编码（每条出向消息调用）" items={REQUIRED_FUNCTIONS.filter((f) => f.category === 'encode')} />
                <SpecBlock title="3. 解码（每条入向消息调用）" items={REQUIRED_FUNCTIONS.filter((f) => f.category === 'decode')} />
                <SpecBlock title="4. 路由匹配（请求-响应配对）" items={REQUIRED_FUNCTIONS.filter((f) => f.category === 'route')} />
                <Alert
                  type="info"
                  showIcon
                  style={{ marginTop: 12 }}
                  message="运行时约束"
                  description={
                    <ul style={{ margin: 0, paddingLeft: 20 }}>
                      <li>运行时为 Lua 5.1，不支持 string.pack / string.unpack。</li>
                      <li>消息体长度解析在热路径中执行，必须通过 body_length_info 元信息配置。</li>
                      <li>每个机器人持有独立的运行环境，函数内禁止共享可变全局状态。</li>
                    </ul>
                  }
                />
              </div>
            ),
          },
        ]}
      />
    </Drawer>
  );
}

function SpecBlock({
  title,
  items,
}: {
  title: string;
  items: Array<{ name: string; signature: string; desc: string }>;
}) {
  return (
    <div style={{ marginBottom: 14 }}>
      <div style={{ fontWeight: 600, marginBottom: 6 }}>{title}</div>
      {items.map((it) => (
        <div
          key={it.name}
          style={{
            background: 'var(--bg-canvas)',
            borderLeft: '3px solid var(--node-action)',
            padding: '8px 10px',
            marginBottom: 6,
            borderRadius: 4,
          }}
        >
          <div style={{ fontFamily: 'monospace', color: 'var(--node-action-border-active)', fontWeight: 600 }}>
            {it.signature}
          </div>
          <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>{it.desc}</div>
        </div>
      ))}
    </div>
  );
}
