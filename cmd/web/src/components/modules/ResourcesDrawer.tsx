/**
 * 资源管理 Drawer：管理用户上传到 IndexedDB 的 proto / lua / adapter 文件。
 *
 * 设计要点：
 * - 三个 Tab：proto / lua / adapter；前两者复用 ResourceTable，adapter 内嵌 Monaco 编辑器；
 * - 顶部显示未处理的资源冲突 Alert + "处理冲突"按钮，打开 BaselineSyncModal；
 * - 基线同步在打开编辑器或启动任务时自动执行（内容对比驱动），无需手动导入。
 */

import { DeleteOutlined, InboxOutlined, EditOutlined } from '@ant-design/icons';
import {
  Alert,
  App as AntApp,
  Button,
  Drawer,
  Empty,
  Flex,
  Modal,
  Segmented,
  Space,
  Table,
  Tabs,
  Tooltip,
  Typography,
  Upload,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import type { UploadProps } from 'antd';
import { useEffect, useRef, useState } from 'react';
import Editor from '@monaco-editor/react';
import { useShallow } from 'zustand/react/shallow';
import { useEditorStore } from '../FlowEditor/store/editorStore';
import { useFloatingWindowStore } from '../FlowEditor/store/floatingWindowStore';
import { useProtoStore } from '../FlowEditor/proto/protoStore';
import {
  addProtos,
  addScripts,
  clearProto,
  clearScript,
  listProto,
  listScript,
  removeProto,
  removeScript,
  subscribe,
  type ResourceFile,
  getAdapterScript,
  setAdapterScript,
  setAdapterScriptFromBaseline,
  clearAdapterScript,
  validateAdapter,
  subtractSyncResult,
  getErrorMapScript,
  setErrorMapScript,
  setErrorMapScriptFromBaseline,
  clearErrorMapScript,
} from '@/services/resourcesStore';
import { BaselineSyncModal } from './BaselineSyncModal';
import { fetchBaselineAdapter, fetchBaselineErrorMap } from '@/services/baselineApi';

export interface ResourcesDrawerProps {
  open: boolean;
  onClose: () => void;
}

export function ResourcesDrawer({ open, onClose }: ResourcesDrawerProps) {
  const { pendingSyncResult, setPendingSyncResult } = useEditorStore(
    useShallow((s) => ({
      pendingSyncResult: s.pendingSyncResult,
      setPendingSyncResult: s.setPendingSyncResult,
    })),
  );
  const [syncModalOpen, setSyncModalOpen] = useState(false);

  const hasConflicts = pendingSyncResult != null && (pendingSyncResult.conflicts.length > 0 || pendingSyncResult.removed.length > 0);

  return (
    <>
      <Drawer title="资源管理" open={open} onClose={onClose} width={760} maskClosable={false} destroyOnHidden={false} styles={{ body: { paddingBottom: 8 } }}>
        {hasConflicts && (
          <Alert
            type="warning"
            showIcon
            style={{ marginBottom: 12 }}
            message="资源存在冲突"
            description={
              <Space>
                <span>{pendingSyncResult.conflicts.length + pendingSyncResult.removed.length} 处冲突待处理</span>
                <Button size="small" type="primary" onClick={() => setSyncModalOpen(true)}>
                  处理冲突
                </Button>
              </Space>
            }
          />
        )}
        <Tabs
          defaultActiveKey="proto"
          items={[
            { key: 'proto', label: 'Proto', children: <ResourceTable kind="proto" /> },
            { key: 'lua', label: 'Lua', children: <ResourceTable kind="lua" /> },
            { key: 'adapter', label: '适配器', children: <AdapterTab /> },
          ]}
        />
      </Drawer>

      {pendingSyncResult && (
        <BaselineSyncModal
          open={syncModalOpen}
          result={pendingSyncResult}
          onClose={() => {
            setSyncModalOpen(false);
          }}
          onResolved={() => {
            setPendingSyncResult(subtractSyncResult(pendingSyncResult, pendingSyncResult));
          }}
        />
      )}
    </>
  );
}

/* ─── Adapter Tab — 内嵌编辑器，支持 codec.lua + error.lua 双文件切换 ─── */

type AdapterFileKey = 'codec' | 'error';

const ADAPTER_FILE_LABELS: Record<AdapterFileKey, string> = {
  codec: 'codec.lua（必需）',
  error: 'error.lua（可选）',
};

const ADAPTER_TEMPLATE = `-- conf/adapter/codec.lua
-- 协议适配器模板。必须实现以下 7 个函数，引擎通过这些接口完成编解码，不感知具体协议格式。
-- 运行时环境：Lua 5.1，禁止使用 string.pack/unpack。

-- ─── 元信息（初始化时调用一次）──────────────────────────────────────
function header_size()
    return 12  -- 协议头固定字节数
end

function body_length()
    return {
        offset          = 0,           -- header 中 body 长度字段的起始字节偏移
        field_type      = "uint32_le", -- "uint16_le" / "uint16_be" / "uint32_le" / "uint32_be"
        includes_header = false,       -- 长度字段值是否包含 header 自身
    }
end

-- ─── 编码（每条出向消息调用）────────────────────────────────────────
function encode_tcp(route, body, secret_key)
    -- route: 不透明路由表（flow.json 中定义），典型 {cmd=3, act=1}
    -- body:  序列化后的消息体字节
    -- secret_key: 加密密钥（nil 表示不加密）
    -- 返回: 完整数据包（header + body）
    return body
end

function encode_udp(route, body, secret_key)
    -- 与 encode_tcp 签名相同，UDP 可使用不同的编码策略
    return body
end

-- ─── 解码（每条入向消息调用）────────────────────────────────────────
function decode_tcp(data, secret_key)
    -- data: 完整帧数据（已按 body_length 切帧）
    -- 返回: routeKey (string), body (string), headerErr (number)
    return "0:0", "", 0
end

function decode_udp(data, secret_key)
    -- 与 decode_tcp 分离，UDP 可使用不同的解码策略
    return "0:0", "", 0
end

-- ─── 路由匹配（请求-响应配对）────────────────────────────────────────
function expected_route_key(route)
    -- 从发送 route 计算期望的响应路由键，用于请求-响应匹配
    return ""
end
`;

const ERROR_MAP_TEMPLATE = `-- conf/adapter/error.lua
-- 可选：服务端错误码映射。未提供此文件时，引擎静默忽略，错误码以原始数字展示。
-- 结果按错误码永久缓存，运行时不可变，需重启才能更新。

function describe_error(code)
    local errors = {
        -- [1004] = "金币不足",
    }
    return errors[code] or ""
end
`;

function AdapterTab() {
  const { message } = AntApp.useApp();
  const theme = useEditorStore((s) => s.theme);
  const monacoTheme = theme === 'dark' ? 'vs-dark' : 'light';

  const [activeFile, setActiveFile] = useState<AdapterFileKey>('codec');
  const [contents, setContents] = useState<Record<AdapterFileKey, string>>({ codec: '', error: '' });
  const [sources, setSources] = useState<Record<AdapterFileKey, string | null>>({ codec: null, error: null });
  const [loadErrors, setLoadErrors] = useState<Record<AdapterFileKey, boolean>>({ codec: false, error: false });

  // 加载指定文件的内容
  const loadFile = async (key: AdapterFileKey) => {
    setLoadErrors((prev) => ({ ...prev, [key]: false }));
    if (key === 'codec') {
      const file = await getAdapterScript();
      if (file) {
        setContents((prev) => ({ ...prev, codec: file.content }));
        setSources((prev) => ({ ...prev, codec: '已保存' }));
      } else {
        const text = await fetchBaselineAdapter();
        if (text) {
          setContents((prev) => ({ ...prev, codec: text }));
          setSources((prev) => ({ ...prev, codec: '默认模板' }));
          void setAdapterScriptFromBaseline(text);
        } else {
          setContents((prev) => ({ ...prev, codec: '' }));
          setSources((prev) => ({ ...prev, codec: null }));
          setLoadErrors((prev) => ({ ...prev, codec: true }));
        }
      }
    } else {
      const file = await getErrorMapScript();
      if (file) {
        setContents((prev) => ({ ...prev, error: file.content }));
        setSources((prev) => ({ ...prev, error: '已保存' }));
      } else {
        const text = await fetchBaselineErrorMap();
        if (text) {
          setContents((prev) => ({ ...prev, error: text }));
          setSources((prev) => ({ ...prev, error: '默认模板' }));
          void setErrorMapScriptFromBaseline(text);
        } else {
          setContents((prev) => ({ ...prev, error: ERROR_MAP_TEMPLATE }));
          setSources((prev) => ({ ...prev, error: '模板（未保存）' }));
        }
      }
    }
  };

  // 切换文件时加载对应内容（未加载过的才请求）
  const loaded = useRef<Set<AdapterFileKey>>(new Set());
  const handleFileSwitch = (val: string | number) => {
    const key = val as AdapterFileKey;
    setActiveFile(key);
    if (!loaded.current.has(key)) {
      loaded.current.add(key);
      loadFile(key);
    }
  };

  // 首次加载 codec
  useEffect(() => {
    loaded.current.add('codec');
    loadFile('codec');
  }, []);

  const onUpload: UploadProps['beforeUpload'] = async (file) => {
    const text = await file.text();
    setContents((prev) => ({ ...prev, [activeFile]: text }));
    setSources((prev) => ({ ...prev, [activeFile]: file.name }));
    setLoadErrors((prev) => ({ ...prev, [activeFile]: false }));
    if (activeFile === 'codec') {
      await setAdapterScript(text);
    } else {
      await setErrorMapScript(text);
    }
    message.success(`已加载并保存：${file.name}`);
    return false;
  };

  const onUseTemplate = () => {
    const tmpl = activeFile === 'codec' ? ADAPTER_TEMPLATE : ERROR_MAP_TEMPLATE;
    setContents((prev) => ({ ...prev, [activeFile]: tmpl }));
    setSources((prev) => ({ ...prev, [activeFile]: '模板（未保存）' }));
    setLoadErrors((prev) => ({ ...prev, [activeFile]: false }));
    message.info('已载入模板，编辑后点击保存');
  };

  const onSave = async () => {
    const content = contents[activeFile];
    if (!content.trim()) {
      message.warning('内容为空');
      return;
    }
    if (activeFile === 'codec') {
      await setAdapterScript(content);
      setSources((prev) => ({ ...prev, codec: '已保存' }));
      setLoadErrors((prev) => ({ ...prev, codec: false }));
      const missing = await validateAdapter();
      useEditorStore.getState().setAdapterMissing(missing);
      if (missing.length > 0) {
        message.warning(`已保存，但缺少 ${missing.length} 个必需函数：${missing.join(', ')}`);
      } else {
        message.success('已保存，启动任务时会自动上传');
      }
    } else {
      await setErrorMapScript(content);
      setSources((prev) => ({ ...prev, error: '已保存' }));
      setLoadErrors((prev) => ({ ...prev, error: false }));
      message.success('已保存，启动任务时会自动上传');
    }
  };

  const onClear = async () => {
    if (activeFile === 'codec') {
      await clearAdapterScript();
      const missing = await validateAdapter();
      useEditorStore.getState().setAdapterMissing(missing);
    } else {
      await clearErrorMapScript();
    }
    setContents((prev) => ({ ...prev, [activeFile]: '' }));
    setSources((prev) => ({ ...prev, [activeFile]: null }));
    setLoadErrors((prev) => ({ ...prev, [activeFile]: true }));
    message.success('已清空');
  };

  return (
    <Tabs
      size="small"
      defaultActiveKey="edit"
      items={[
        {
          key: 'edit',
          label: '编辑',
          children: (
            <Flex vertical gap={8}>
              <Alert type="info" showIcon message="协议适配器随任务下发" description="编辑后点保存。启动任务时会自动上传到服务端，无需手动部署。" />
              {loadErrors[activeFile] && (
                <Alert type="error" showIcon message="未找到协议适配器" description="未找到适配器文件且默认模板不可用。请导入文件或载入空模板后保存。" />
              )}
              <Flex justify="space-between" align="center">
                <Segmented
                  size="small"
                  value={activeFile}
                  onChange={handleFileSwitch}
                  options={[
                    { value: 'codec', label: ADAPTER_FILE_LABELS.codec },
                    { value: 'error', label: ADAPTER_FILE_LABELS.error },
                  ]}
                />
                <span style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>{sources[activeFile] ?? '尚未加载'}</span>
              </Flex>
              <Space size={4} wrap>
                <Upload accept=".lua,text/plain" beforeUpload={onUpload} showUploadList={false}>
                  <Button icon={<InboxOutlined />} size="small">导入 .lua</Button>
                </Upload>
                <Button onClick={onUseTemplate} size="small">载入模板</Button>
                <Button onClick={onSave} type="primary" size="small">保存</Button>
                <Button onClick={onClear} danger size="small">清空</Button>
              </Space>
              <div style={{ height: 'calc(100vh - 400px)', border: '1px solid var(--border-color, rgba(0,0,0,0.06))' }}>
                <Editor
                  language="lua"
                  theme={monacoTheme}
                  value={contents[activeFile]}
                  onChange={(v) => setContents((prev) => ({ ...prev, [activeFile]: v ?? '' }))}
                  options={{
                    fontSize: 12,
                    minimap: { enabled: false },
                    scrollBeyondLastLine: false,
                    fixedOverflowWidgets: true,
                  }}
                />
              </div>
            </Flex>
          ),
        },
        {
          key: 'spec',
          label: '接口规范',
          children: activeFile === 'codec' ? (
            <div style={{ fontSize: 12, lineHeight: 1.6 }}>
              <Alert
                type="warning"
                message="接口规范"
                showIcon
                style={{ marginBottom: 12 }}
                description="codec.lua 必须实现以下 7 个全局函数。引擎只调用这些接口，不感知具体协议格式。"
              />
              <SpecBlock title="1. 元信息（初始化时调用一次，结果缓存）" items={CODEC_SPEC.filter((f) => f.category === 'meta')} />
              <SpecBlock title="2. 编码（每条出向消息调用）" items={CODEC_SPEC.filter((f) => f.category === 'encode')} />
              <SpecBlock title="3. 解码（每条入向消息调用）" items={CODEC_SPEC.filter((f) => f.category === 'decode')} />
              <SpecBlock title="4. 路由匹配（请求-响应配对）" items={CODEC_SPEC.filter((f) => f.category === 'route')} />
              <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginTop: 12 }}>
                运行时约束：Lua 5.1（不支持 string.pack/unpack）；帧分割由引擎根据 body_length 配置自动执行，解码函数收到的 data 已是完整帧；每个机器人独立运行环境，禁止共享可变全局状态。
              </Typography.Text>
            </div>
          ) : (
            <div style={{ fontSize: 12, lineHeight: 1.6 }}>
              <Alert
                type="info"
                showIcon
                style={{ marginBottom: 12 }}
                description="error.lua 为可选文件，用于将服务端协议头错误码映射为可读描述。未提供时引擎静默忽略，错误码以原始数字展示。"
              />
              <SpecBlock title="错误码映射" items={ERROR_MAP_SPEC} />
              <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginTop: 12 }}>
                运行时约束：结果按错误码永久缓存，加载后不可变，需重启才能更新。
              </Typography.Text>
            </div>
          ),
        },
      ]}
    />
  );
}

const CODEC_SPEC: Array<{ name: string; signature: string; desc: string; category: string }> = [
  { name: 'header_size', signature: 'header_size() -> int', desc: '返回协议头固定字节数。初始化时调用一次。', category: 'meta' },
  { name: 'body_length', signature: 'body_length() -> offset, field_type, includes_header', desc: '描述 body 长度字段在 header 中的位置和类型，引擎据此进行帧分割。', category: 'meta' },
  { name: 'encode_tcp', signature: 'encode_tcp(route, body, secret_key) -> string', desc: 'TCP 编码：将 route + body + secret_key 编码为完整数据包。route 为 nil 表示无路由请求。', category: 'encode' },
  { name: 'encode_udp', signature: 'encode_udp(route, body, secret_key) -> string', desc: 'UDP 编码：与 encode_tcp 签名相同，可使用不同的编码策略。', category: 'encode' },
  { name: 'decode_tcp', signature: 'decode_tcp(data, secret_key) -> routeKey, body, headerErr', desc: 'TCP 解码：从完整帧中解析路由键、消息体和协议头错误码。headerErr 非零时引擎仍继续路由。', category: 'decode' },
  { name: 'decode_udp', signature: 'decode_udp(data, secret_key) -> routeKey, body, headerErr', desc: 'UDP 解码：与 decode_tcp 分离，可使用不同的解码策略。', category: 'decode' },
  { name: 'expected_route_key', signature: 'expected_route_key(route) -> string', desc: '从发送 route 计算期望的响应路由键，用于请求-响应匹配。', category: 'route' },
];

const ERROR_MAP_SPEC: Array<{ name: string; signature: string; desc: string }> = [
  { name: 'describe_error', signature: 'describe_error(code) -> string', desc: '将协议头错误码映射为可读描述。返回空字符串表示未知错误码。' },
];

function SpecBlock({ title, items }: { title: string; items: Array<{ name: string; signature: string; desc: string }> }) {
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

/* ─── Proto / Lua 通用表格 ─── */

interface ResourceTableProps {
  kind: 'proto' | 'lua';
}

const KIND_LABEL: Record<ResourceTableProps['kind'], string> = {
  proto: 'Proto',
  lua: 'Lua',
};

const KIND_DESC: Record<ResourceTableProps['kind'], string> = {
  proto: 'Proto 文件：定义消息结构与序列化格式，启动任务时随流程配置一起提交。',
  lua: 'Lua 脚本：实现复杂业务逻辑，被流程节点中的 lua 动作引用。',
};

const KIND_EXT: Record<ResourceTableProps['kind'], string[]> = {
  proto: ['.proto'],
  lua: ['.lua'],
};

function ResourceTable({ kind }: ResourceTableProps) {
  const { modal, message } = AntApp.useApp();
  const [items, setItems] = useState<ResourceFile[]>([]);
  const [loading, setLoading] = useState(false);
  const reloadProtos = useProtoStore((s) => s.reload);
  const theme = useEditorStore((s) => s.theme);
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;

  const [editFile, setEditFile] = useState<ResourceFile | null>(null);
  const [editContent, setEditContent] = useState('');
  const [saving, setSaving] = useState(false);

  const load = async () => {
    setLoading(true);
    try {
      const data = kind === 'proto' ? await listProto() : await listScript();
      setItems(data);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    load();
    const unsub = subscribe(() => load());
    return () => unsub();
  }, [kind]);

  const fileInputRef = useRef<HTMLInputElement>(null);
  const [uploading, setUploading] = useState(false);
  const [uploadProgress, setUploadProgress] = useState({ current: 0, total: 0 });

  const handleBatchUpload = async (fileList: FileList) => {
    const files = Array.from(fileList);
    if (files.length === 0) return;
    setUploading(true);
    setUploadProgress({ current: 0, total: files.length });

    // 阶段 1：读取全部文件，按文件名去重（保留最后一个）
    const batch = new Map<string, { name: string; content: string }>();
    let readFail = 0;
    for (let i = 0; i < files.length; i++) {
      try {
        const text = await files[i].text();
        batch.set(files[i].name, { name: files[i].name, content: text });
      } catch {
        readFail++;
      }
      setUploadProgress({ current: i + 1, total: files.length });
    }

    if (batch.size === 0) {
      setUploading(false);
      message.error('全部文件读取失败');
      return;
    }

    // 阶段 2：检测与已有资源的同名冲突
    const existing = kind === 'proto' ? await listProto() : await listScript();
    const existingNames = new Set(existing.map((f) => f.name));
    const conflicts: string[] = [];
    const newFiles: { name: string; content: string }[] = [];
    for (const f of batch.values()) {
      if (existingNames.has(f.name)) conflicts.push(f.name);
      else newFiles.push(f);
    }

    let toWrite: { name: string; content: string }[];
    let overwritten = 0;

    if (conflicts.length > 0) {
      // 注意：点击遮罩/ESC 与点击取消按钮效果相同（resolve(false) → 跳过冲突，仅上传新文件），
      // antd modal.confirm API 无法区分这两种操作。
      const doOverwrite = await new Promise<boolean>((resolve) => {
        modal.confirm({
          title: '发现同名文件',
          content: (
            <div>
              <p>
                已选 {batch.size} 个文件中有 <b>{conflicts.length}</b> 个与已有资源同名：
              </p>
              <div style={{ maxHeight: 120, overflow: 'auto', lineHeight: 1.8, margin: '4px 0' }}>
                {conflicts.map((name) => (
                  <div key={name}>
                    <code>{name}</code>
                  </div>
                ))}
              </div>
            </div>
          ),
          okText: '覆盖全部',
          okType: 'primary',
          cancelText: conflicts.length < batch.size ? '仅上传新文件' : '取消',
          onOk: () => resolve(true),
          onCancel: () => resolve(false),
        });
      });

      if (doOverwrite) {
        toWrite = Array.from(batch.values());
        overwritten = conflicts.length;
      } else {
        toWrite = newFiles;
      }
    } else {
      toWrite = Array.from(batch.values());
    }

    // 阶段 3：写入 IndexedDB
    if (toWrite.length > 0) {
      if (kind === 'proto') {
        await addProtos(toWrite);
        await reloadProtos();
      } else {
        await addScripts(toWrite);
      }
    }

    setUploading(false);

    // 阶段 4：汇总提示
    const skipped = conflicts.length - overwritten;

    if (files.length === 1) {
      if (toWrite.length > 0) {
        message.success(overwritten > 0 ? `${files[0].name} 已上传（覆盖）` : `${files[0].name} 已上传`);
      } else {
        message.info(`已跳过同名文件 ${files[0].name}`);
      }
      return;
    }

    if (toWrite.length === 0 && readFail === 0) {
      message.info('已跳过全部同名文件');
      return;
    }

    const parts: string[] = [];
    if (toWrite.length > 0) {
      parts.push(
        overwritten > 0
          ? `${toWrite.length} 个已上传（含 ${overwritten} 个覆盖）`
          : `${toWrite.length} 个已上传`,
      );
    }
    if (skipped > 0) parts.push(`${skipped} 个同名跳过`);
    if (readFail > 0) parts.push(`${readFail} 个读取失败`);

    if (readFail === 0 && skipped === 0) {
      message.success(parts[0]);
    } else if (toWrite.length > 0) {
      message.warning(parts.join('，'));
    } else {
      message.error(parts.join('，'));
    }
  };

  const handleRemove = (name: string) => {
    modal.confirm({
      title: '删除资源',
      content: `确认删除 ${name}？`,
      okType: 'danger',
      onOk: async () => {
        if (kind === 'proto') {
          await removeProto(name);
          await reloadProtos();
        } else {
          await removeScript(name);
        }
        message.success(`${name} 已删除`);
      },
    });
  };

  const handleClearAll = () => {
    modal.confirm({
      title: `清空所有 ${KIND_LABEL[kind]}？`,
      content: `将删除 ${items.length} 个文件，此操作不可撤销。`,
      okType: 'danger',
      onOk: async () => {
        if (kind === 'proto') {
          await clearProto();
          await reloadProtos();
        } else {
          await clearScript();
        }
        message.success('已清空');
      },
    });
  };

  const handleSaveEdit = async () => {
    if (!editFile) return;
    setSaving(true);
    try {
      if (kind === 'proto') {
        await addProtos([{ name: editFile.name, content: editContent }]);
        await reloadProtos();
      } else {
        await addScripts([{ name: editFile.name, content: editContent }]);
      }
      message.success(`${editFile.name} 已保存`);
      setEditFile(null);
    } catch (e) {
      message.error(`保存失败：${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  };

  const columns: ColumnsType<ResourceFile> = [
    {
      title: '文件名',
      dataIndex: 'name',
      key: 'name',
      render: (v: string) => <code>{v}</code>,
    },
    {
      title: '大小',
      dataIndex: 'size',
      key: 'size',
      width: 100,
      render: (v: number) => formatBytes(v),
    },
    {
      title: '上传时间',
      dataIndex: 'uploadedAt',
      key: 'uploadedAt',
      width: 180,
      render: (v: string) => new Date(v).toLocaleString(),
    },
    {
      title: '操作',
      key: 'op',
      width: 100,
      render: (_, record) => (
        <Space size={4}>
          <Tooltip title="查看 / 编辑">
            <Button
              type="text"
              size="small"
              icon={<EditOutlined />}
              onClick={() => {
                setEditFile(record);
                setEditContent(record.content);
              }}
            />
          </Tooltip>
          <Tooltip title="删除">
            <Button
              type="text"
              size="small"
              danger
              icon={<DeleteOutlined />}
              onClick={() => handleRemove(record.name)}
            />
          </Tooltip>
        </Space>
      ),
    },
  ];

  return (
    <Flex vertical gap={12}>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        {KIND_DESC[kind]}
      </Typography.Text>
      <Space wrap>
        <input
          ref={fileInputRef}
          type="file"
          accept={KIND_EXT[kind].join(',')}
          multiple
          hidden
          onChange={(e) => {
            if (e.target.files?.length) handleBatchUpload(e.target.files);
            e.target.value = '';
          }}
        />
        <Button
          type="primary"
          icon={<InboxOutlined />}
          loading={uploading}
          onClick={() => fileInputRef.current?.click()}
        >
          {uploading
            ? `上传中 ${uploadProgress.current}/${uploadProgress.total}`
            : `上传 ${KIND_LABEL[kind]} 文件`}
        </Button>
        <Button danger disabled={items.length === 0} onClick={handleClearAll}>
          清空
        </Button>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          共 {items.length} 个文件 · 总 {formatBytes(items.reduce((sum, f) => sum + f.size, 0))}
        </Typography.Text>
      </Space>
      {items.length === 0 ? (
        <Empty
          description={
            <span>
              暂无 {KIND_LABEL[kind]} 文件。点击上方"上传"开始，或启动任务时自动从基线同步。
            </span>
          }
        />
      ) : (
        <Table<ResourceFile>
          rowKey="name"
          loading={loading}
          dataSource={items}
          columns={columns}
          size="small"
          pagination={false}
          scroll={{ y: 'calc(100vh - 280px)' }}
        />
      )}

      <Modal
        title={`编辑 ${editFile?.name}`}
        open={!!editFile}
        onCancel={() => setEditFile(null)}
        width={900}
        destroyOnHidden
        maskClosable={false}
        styles={{ mask: { zIndex: popupZ }, wrapper: { zIndex: popupZ + 1 } }}
        footer={[
          <Button key="cancel" onClick={() => setEditFile(null)}>取消</Button>,
          <Button key="save" type="primary" loading={saving} onClick={handleSaveEdit}>保存</Button>,
        ]}
      >
        <div style={{ height: '60vh', border: '1px solid var(--border-color)' }}>
          {editFile && (
            <Editor
              language={kind === 'proto' ? 'proto' : 'lua'}
              theme={theme === 'dark' ? 'vs-dark' : 'light'}
              value={editContent}
              onChange={(val) => setEditContent(val ?? '')}
              options={{ minimap: { enabled: false }, fontSize: 13, wordWrap: 'on', fixedOverflowWidgets: true }}
            />
          )}
        </div>
      </Modal>
    </Flex>
  );
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(2)} MB`;
}
