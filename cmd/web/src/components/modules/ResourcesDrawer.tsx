/**
 * 资源管理 Drawer：管理用户上传到 IndexedDB 的 proto / lua / adapter 文件。
 *
 * 设计要点：
 * - 三个 Tab：proto / lua / adapter；前两者复用 ResourceTable，adapter 内嵌 Monaco 编辑器；
 * - 顶部提供「拉取」按钮做显式同步（svn update），不再自动拉取；
 * - 冲突通过 BaselineSyncModal 显式处理；本地改动会在启动任务时随配置一并提交到服务器，无需单独推送。
 */

import { DeleteOutlined, InboxOutlined, EditOutlined, CloudDownloadOutlined, CopyOutlined, PlusOutlined } from '@ant-design/icons';
import {
  Alert,
  App as AntApp,
  Button,
  Drawer,
  Empty,
  Flex,
  Input,
  Modal,
  Segmented,
  Select,
  Space,
  Table,
  Tabs,
  Tooltip,
  Typography,
  Upload,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import type { UploadProps } from 'antd';
import { useEffect, useMemo, useRef, useState } from 'react';
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
  subtractSyncResult,
  syncResourcesFromBaseline,
  getCodecSchema,
  setCodecSchema,
  setCodecSchemaFromBaseline,
  clearCodecSchema,
  listCodecFiles,
  getErrorMap,
  setErrorMap,
  setErrorMapFromBaseline,
  clearErrorMap,
  validateCodecSchema,
  collectCodecSchemaErrors,
} from '@/services/resourcesStore';
import { BaselineSyncModal } from './BaselineSyncModal';
import { fetchBaselineCodecIndex, fetchBaselineCodec } from '@/services/baselineApi';
import { parseCodecForEdit } from './codecEditor/codecEdit';
import { FrameLayoutEditor } from './codecEditor/FrameLayoutEditor';
import { PipelineEditor } from './codecEditor/PipelineEditor';
import { RouteKeyEditor } from './codecEditor/RouteKeyEditor';

export interface ResourcesDrawerProps {
  open: boolean;
  onClose: () => void;
}

export function ResourcesDrawer({ open, onClose }: ResourcesDrawerProps) {
  const { message } = AntApp.useApp();
  const { pendingSyncResult, setPendingSyncResult } = useEditorStore(
    useShallow((s) => ({
      pendingSyncResult: s.pendingSyncResult,
      setPendingSyncResult: s.setPendingSyncResult,
    })),
  );
  const [syncModalOpen, setSyncModalOpen] = useState(false);
  const [pulling, setPulling] = useState(false);

  const hasConflicts = pendingSyncResult != null && (pendingSyncResult.conflicts.length > 0 || pendingSyncResult.removed.length > 0);

  // 拉取（svn update）：与服务器对比，自动合并"仅服务器修改/新增"，冲突弹面板。
  const handlePull = async () => {
    setPulling(true);
    try {
      const sync = await syncResourcesFromBaseline();
      if (sync.conflicts.length > 0 || sync.removed.length > 0) {
        setPendingSyncResult(sync);
        setSyncModalOpen(true);
        message.warning(`检测到 ${sync.conflicts.length + sync.removed.length} 处冲突，请逐项确认`);
      } else if (sync.added.length > 0) {
        message.success(`已从服务器拉取，新增 ${sync.added.length} 个资源`);
      } else {
        message.success('已从服务器拉取完成');
      }
    } catch (e) {
      message.error(`拉取失败：${(e as Error).message}`);
    } finally {
      setPulling(false);
    }
  };

  return (
    <>
      <Drawer title="资源管理" open={open} onClose={onClose} width={760} maskClosable={false} destroyOnHidden={false} styles={{ body: { paddingBottom: 8 } }}>
        <Flex justify="space-between" align="center" style={{ marginBottom: 12 }}>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            从服务器拉取最新资源到本地；本地改动会在启动任务时随配置一并提交到服务器。
          </Typography.Text>
          <Tooltip title="从服务器拉取最新资源（仅服务器改动会自动合并，双方都改的会提示冲突）">
            <Button icon={<CloudDownloadOutlined />} loading={pulling} onClick={handlePull}>
              拉取
            </Button>
          </Tooltip>
        </Flex>
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
            { key: 'adapter', label: '协议配置', children: <AdapterTab /> },
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

/* ─── 协议配置 Tab — 按连接多份 codec.json + 共享 errors.json 源码 JSON 编辑器 ─── */

/** codec 文件名后缀。 */
const CODEC_FILE_SUFFIX = '_codec.json';
/** errors.json 固定文件名。 */
const ERRORS_JSON_KEY = 'errors.json';
/** 合法连接协议。 */
const CODEC_PROTOS = ['tcp', 'udp'] as const;

/**
 * 新建连接用的最小合法 codec.json 模板。
 * 设计目标：直接通过 validateCodecSchema（version=1、endian="le"、frame.headerSize=8、
 * 1 个 role:"length" + 2 个 role:"route" 字段、routeKeyTemplate 引用 route 字段、pipeline 空）。
 */
const CODEC_JSON_TEMPLATE = `{
  "version": 1,
  "endianDefault": "le",
  "frame": { "headerSize": 8, "trailerSize": 0, "lengthIncludesHeader": false, "lengthIncludesTrailer": false },
  "header": [
    { "name": "bodyLen", "offset": 0, "size": 4, "type": "u32", "endian": "le", "role": "length" },
    { "name": "cmd",     "offset": 4, "size": 1, "type": "u8",  "role": "route" },
    { "name": "act",     "offset": 5, "size": 1, "type": "u8",  "role": "route" },
    { "name": "index",   "offset": 6, "size": 2, "type": "u16", "endian": "le", "role": "value", "source": { "kind": "const", "value": 0 } }
  ],
  "routeKeyTemplate": "{cmd}:{act}",
  "pipeline": []
}
`;

const EMPTY_ERROR_MAP_TEMPLATE = `{
}
`;

/**
 * 连接名 ↔ 文件名互转。
 *   连接名 `<proto>:<service>`（如 `tcp:logic`）↔ 文件名 `<proto>_<service>_codec.json`（`tcp_logic_codec.json`）。
 * service 中不允许再出现 `_`（首个 `_` 之前是 proto，之后到 `_codec.json` 之前整体视为 service）。
 */
function connNameToFileName(conn: string): string {
  return `${conn.replace(':', '_')}${CODEC_FILE_SUFFIX}`;
}

function fileNameToConnName(name: string): string {
  const stripped = name.endsWith(CODEC_FILE_SUFFIX) ? name.slice(0, -CODEC_FILE_SUFFIX.length) : name;
  const idx = stripped.indexOf('_');
  if (idx < 0) return stripped;
  return `${stripped.slice(0, idx)}:${stripped.slice(idx + 1)}`;
}

/** 校验连接名 `<proto>:<service>`：proto∈{tcp,udp}、service 非空、不与已存文件重名。 */
function validateConnName(conn: string, existing: string[]): string | null {
  const idx = conn.indexOf(':');
  if (idx <= 0 || idx === conn.length - 1) {
    return '连接名格式应为 <协议>:<服务名>，例如 tcp:logic';
  }
  const proto = conn.slice(0, idx);
  const service = conn.slice(idx + 1);
  if (!(CODEC_PROTOS as readonly string[]).includes(proto)) {
    return `协议必须是 tcp 或 udp（当前 ${proto}）`;
  }
  if (!service || service.includes(':') || service.includes('_')) {
    return '服务名不能为空，也不能包含 ":" 或 "_"';
  }
  const fileName = connNameToFileName(conn);
  if (existing.includes(fileName)) {
    return `连接 ${conn} 已存在`;
  }
  return null;
}

function AdapterTab() {
  const { message, modal } = AntApp.useApp();
  const theme = useEditorStore((s) => s.theme);
  const monacoTheme = theme === 'dark' ? 'vs-dark' : 'light';

  // 连接列表（含每个文件名 / 连接名）
  const [files, setFiles] = useState<ResourceFile[]>([]);
  const [loading, setLoading] = useState(false);
  // 当前选中连接（null = 未选；'__errors__' = errors.json；否则 = 连接名如 'tcp:logic'）
  const [activeConn, setActiveConn] = useState<string | null>(null);
  const [content, setContent] = useState<string>('');
  const [source, setSource] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<boolean>(false);
  // 新建/复制弹窗
  const [createOpen, setCreateOpen] = useState(false);
  const [createMode, setCreateMode] = useState<'new' | 'copy'>('new');
  const [createValue, setCreateValue] = useState('');
  // 视图切换：结构化 | 源码（仅 codec 显示；errors.json 隐藏切换、强制源码）
  const [viewMode, setViewMode] = useState<'struct' | 'source'>('struct');

  // 结构化视图：把 content 解析为 raw（lossless）+ schema（typed 视图）+ error。
  const parsed = useMemo(() => parseCodecForEdit(content), [content]);
  const isErrorsView = activeConn === '__errors__';
  const showStructView = !isErrorsView && viewMode === 'struct' && parsed.schema !== null;

  const reloadFiles = async (): Promise<ResourceFile[]> => {
    setLoading(true);
    try {
      const list = await listCodecFiles();
      setFiles(list);
      return list;
    } finally {
      setLoading(false);
    }
  };

  // 加载某连接（或 errors.json）的内容到编辑器。
  const loadConn = async (conn: string | null) => {
    setLoadError(false);
    if (conn === null) {
      setContent('');
      setSource(null);
      return;
    }
    if (conn === '__errors__') {
      const file = await getErrorMap();
      if (file) {
        setContent(file.content);
        setSource('已保存');
      } else {
        setContent(EMPTY_ERROR_MAP_TEMPLATE);
        setSource('模板（未保存）');
      }
      return;
    }
    const name = connNameToFileName(conn);
    const file = await getCodecSchema(name);
    if (file) {
      setContent(file.content);
      setSource('已保存');
    } else {
      // 文件不存在（理论不应发生——选中项来自列表）：清空并提示。
      setContent('');
      setSource(null);
      setLoadError(true);
    }
  };

  useEffect(() => {
    void (async () => {
      const list = await reloadFiles();
      // 默认选中第一个连接；若没有则不自动选 errors.json
      if (list.length > 0) {
        const conn = fileNameToConnName(list[0].name);
        setActiveConn(conn);
        await loadConn(conn);
      } else {
        setActiveConn(null);
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handleSwitch = async (conn: string) => {
    setActiveConn(conn);
    await loadConn(conn);
  };

  const refreshBadge = async () => {
    const errors = await collectCodecSchemaErrors();
    useEditorStore.getState().setCodecSchemaErrors(errors);
  };

  // ─── 新建 / 复制 ───
  const openCreate = (mode: 'new' | 'copy') => {
    setCreateMode(mode);
    setCreateValue('');
    setCreateOpen(true);
  };

  const submitCreate = async () => {
    const conn = createValue.trim();
    const err = validateConnName(conn, files.map((f) => f.name));
    if (err) {
      message.error(err);
      return;
    }
    const fileName = connNameToFileName(conn);
    let initial: string;
    if (createMode === 'copy') {
      if (!activeConn || activeConn === '__errors__') {
        message.error('请先选中一个要复制的连接');
        return;
      }
      const src = await getCodecSchema(connNameToFileName(activeConn));
      initial = src?.content ?? CODEC_JSON_TEMPLATE;
    } else {
      initial = CODEC_JSON_TEMPLATE;
    }
    // 落库前先校验：模板/副本本身必须合法（不允许把已知非法 schema 写入）。
    const errs = validateCodecSchema(initial);
    if (errs.length > 0) {
      message.error(`模板未通过校验，无法创建：${errs[0]}`);
      return;
    }
    await setCodecSchema(fileName, initial);
    await reloadFiles();
    setActiveConn(conn);
    await loadConn(conn);
    await refreshBadge();
    setCreateOpen(false);
    message.success(`已创建连接 ${conn}`);
  };

  // ─── 删除 ───
  const handleDelete = (conn: string) => {
    if (conn === '__errors__') return; // errors.json 不在此处删（走清空）
    modal.confirm({
      title: '删除连接',
      content: `确认删除连接 ${conn}？该连接的配置将被移除。`,
      okType: 'danger',
      onOk: async () => {
        const name = connNameToFileName(conn);
        await clearCodecSchema(name);
        const list = await reloadFiles();
        // 切到剩余第一项，或清空
        if (list.length > 0) {
          const next = fileNameToConnName(list[0].name);
          setActiveConn(next);
          await loadConn(next);
        } else {
          setActiveConn(null);
          setContent('');
          setSource(null);
        }
        await refreshBadge();
        message.success(`已删除连接 ${conn}`);
      },
    });
  };

  // ─── 保存（codec 校验阻塞落库；errors.json 仅 JSON.parse 合法性） ───
  const onSave = async () => {
    if (activeConn === null) {
      message.warning('请先选择一个连接');
      return;
    }
    if (!content.trim()) {
      message.warning('内容为空');
      return;
    }
    if (activeConn === '__errors__') {
      try {
        JSON.parse(content);
      } catch (e) {
        message.error(`错误码映射不是合法 JSON：${(e as Error).message}`);
        return;
      }
      await setErrorMap(content);
      setSource('已保存');
      message.success('已保存错误码映射');
      return;
    }
    // codec.json：结构校验必须通过，否则拒绝落库。
    const errs = validateCodecSchema(content);
    if (errs.length > 0) {
      message.error(`协议配置未通过校验，已拒绝保存（共 ${errs.length} 处问题）：${errs[0]}`);
      return;
    }
    const name = connNameToFileName(activeConn);
    await setCodecSchema(name, content);
    setSource('已保存');
    await refreshBadge();
    message.success('已保存，启动任务时会自动上传');
  };

  // ─── 导入（当前选中连接；codec 走校验，errors 走 JSON.parse） ───
  const onUpload: UploadProps['beforeUpload'] = async (file) => {
    if (activeConn === null) {
      message.warning('请先选择一个连接');
      return false;
    }
    const text = await file.text();
    if (activeConn === '__errors__') {
      try {
        JSON.parse(text);
      } catch (e) {
        message.error(`错误码映射不是合法 JSON：${(e as Error).message}`);
        return false;
      }
      await setErrorMap(text);
      setContent(text);
      setSource(file.name);
      message.success(`已导入并保存：${file.name}`);
      return false;
    }
    const errs = validateCodecSchema(text);
    if (errs.length > 0) {
      message.error(`导入文件未通过校验，已拒绝保存（共 ${errs.length} 处问题）：${errs[0]}`);
      return false;
    }
    const name = connNameToFileName(activeConn);
    await setCodecSchema(name, text);
    await reloadFiles();
    setContent(text);
    setSource(file.name);
    await refreshBadge();
    message.success(`已导入并保存：${file.name}`);
    return false;
  };

  // ─── 清空当前 ───
  const onClear = async () => {
    if (activeConn === null) return;
    if (activeConn === '__errors__') {
      await clearErrorMap();
      setContent(EMPTY_ERROR_MAP_TEMPLATE);
      setSource('模板（未保存）');
      message.success('已清空错误码映射');
      return;
    }
    modal.confirm({
      title: '清空当前连接配置',
      content: `确认清空连接 ${activeConn} 的配置内容？文件将从本地删除。`,
      okType: 'danger',
      onOk: async () => {
        const name = connNameToFileName(activeConn);
        await clearCodecSchema(name);
        const list = await reloadFiles();
        if (list.length > 0) {
          const next = fileNameToConnName(list[0].name);
          setActiveConn(next);
          await loadConn(next);
        } else {
          setActiveConn(null);
          setContent('');
          setSource(null);
        }
        await refreshBadge();
        message.success('已清空');
      },
    });
  };

  // ─── 从基线载入（一次性把基线所有 codec + errors 拉到本地） ───
  const [pullingBaseline, setPullingBaseline] = useState(false);
  const onPullBaseline = async () => {
    setPullingBaseline(true);
    try {
      const index = await fetchBaselineCodecIndex();
      if (index.length === 0) {
        message.info('基线未提供协议配置文件');
        return;
      }
      let codecCount = 0;
      let errorsLoaded = false;
      for (const name of index) {
        const text = await fetchBaselineCodec(name);
        if (text == null) continue;
        if (name === ERRORS_JSON_KEY) {
          await setErrorMapFromBaseline(text);
          errorsLoaded = true;
        } else if (name.endsWith(CODEC_FILE_SUFFIX)) {
          await setCodecSchemaFromBaseline(name, text);
          codecCount++;
        }
      }
      const list = await reloadFiles();
      if (list.length > 0) {
        const conn = fileNameToConnName(list[0].name);
        setActiveConn(conn);
        await loadConn(conn);
      }
      await refreshBadge();
      const parts: string[] = [];
      if (codecCount > 0) parts.push(`${codecCount} 个连接配置`);
      if (errorsLoaded) parts.push('错误码映射');
      message.success(parts.length > 0 ? `已从基线载入：${parts.join('、')}` : '基线无可载入的协议配置');
    } catch (e) {
      message.error(`从基线载入失败：${(e as Error).message}`);
    } finally {
      setPullingBaseline(false);
    }
  };

  // 实时校验当前编辑器内容（仅 codec，提供 inline 提示，不阻塞输入）
  const liveErrors: string[] =
    activeConn !== null && activeConn !== '__errors__' && content.trim() !== ''
      ? validateCodecSchema(content)
      : [];

  // Select 选项：连接列表 + errors.json
  const selectOptions = [
    ...files.map((f) => ({ value: fileNameToConnName(f.name), label: fileNameToConnName(f.name) })),
    { value: '__errors__', label: '错误码映射（共享）' },
  ];

  return (
    <Flex vertical gap={8}>
      <Alert
        type="info"
        showIcon
        message="协议配置随任务下发"
        description="为每条连接维护一份配置，启动任务时随配置一并提交到服务端。错误码映射为所有连接共享的一份文件。"
      />
      {loadError && (
        <Alert type="error" showIcon message="未找到该连接的配置" description="配置文件不存在。请新建连接或从基线载入。" />
      )}

      {/* 连接选择 + 新建/复制/删除/从基线载入 */}
      <Flex justify="space-between" align="center" gap={8} wrap="wrap">
        <Space size={6} wrap>
          <Select
            size="small"
            style={{ minWidth: 200 }}
            value={activeConn ?? undefined}
            placeholder={files.length === 0 ? '暂无连接' : '选择连接'}
            loading={loading}
            onChange={handleSwitch}
            options={selectOptions}
          />
          <Tooltip title="新建连接（输入 <协议>:<服务名>，如 tcp:logic）">
            <Button size="small" icon={<PlusOutlined />} onClick={() => openCreate('new')}>新建</Button>
          </Tooltip>
          <Tooltip title="复制当前连接为新连接">
            <Button
              size="small"
              icon={<CopyOutlined />}
              disabled={activeConn === null || activeConn === '__errors__'}
              onClick={() => openCreate('copy')}
            >
              复制
            </Button>
          </Tooltip>
          <Tooltip title="删除当前连接">
            <Button
              size="small"
              danger
              icon={<DeleteOutlined />}
              disabled={activeConn === null || activeConn === '__errors__'}
              onClick={() => activeConn && handleDelete(activeConn)}
            >
              删除
            </Button>
          </Tooltip>
        </Space>
        <Space size={6} wrap>
          <Tooltip title="从服务器拉取全部协议配置到本地">
            <Button size="small" icon={<CloudDownloadOutlined />} loading={pullingBaseline} onClick={onPullBaseline}>
              从基线载入
            </Button>
          </Tooltip>
          <span style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>{source ?? '尚未加载'}</span>
        </Space>
      </Flex>

      {/* 编辑器工具栏 */}
      <Space size={4} wrap>
        <Upload accept=".json,application/json" beforeUpload={onUpload} showUploadList={false}>
          <Button icon={<InboxOutlined />} size="small" disabled={activeConn === null}>导入 .json</Button>
        </Upload>
        <Button onClick={onSave} type="primary" size="small" disabled={activeConn === null}>保存</Button>
        <Button onClick={onClear} danger size="small" disabled={activeConn === null}>清空</Button>
      </Space>

      {/* 实时校验提示（仅 codec，不阻塞输入；保存时再强制拦截） */}
      {activeConn !== null && activeConn !== '__errors__' && liveErrors.length > 0 && (
        <Alert
          type="warning"
          showIcon
          message={`当前配置有 ${liveErrors.length} 处问题`}
          description={
            <ul style={{ margin: 0, paddingLeft: 18, maxHeight: 120, overflow: 'auto' }}>
              {liveErrors.slice(0, 8).map((e, i) => (
                <li key={i} style={{ fontSize: 12 }}>{e}</li>
              ))}
              {liveErrors.length > 8 && <li style={{ fontSize: 12 }}>…（还有 {liveErrors.length - 8} 处）</li>}
            </ul>
          }
        />
      )}

      {/* 视图切换：结构化 | 源码（errors.json 强制源码，隐藏切换） */}
      {!isErrorsView && (
        <Flex justify="flex-start" align="center">
          <Segmented
            size="small"
            value={viewMode}
            onChange={(v) => setViewMode(v as 'struct' | 'source')}
            options={[
              { label: '结构化', value: 'struct' },
              { label: '源码', value: 'source' },
            ]}
          />
        </Flex>
      )}

      {/* 结构化视图：parse 失败 → 提示切源码 + 降级显示源码 Monaco；成功 → 帧布局/管线/路由键编辑器 */}
      {showStructView && parsed.raw && parsed.schema ? (
        <div
          style={{
            maxHeight: 'calc(100vh - 440px)',
            minHeight: 240,
            overflow: 'auto',
            border: '1px solid var(--border-color, rgba(0,0,0,0.06))',
            padding: 12,
          }}
        >
          <Space direction="vertical" size={12} style={{ width: '100%' }}>
            <FrameLayoutEditor raw={parsed.raw} schema={parsed.schema} onEdit={setContent} />
            <PipelineEditor raw={parsed.raw} schema={parsed.schema} onEdit={setContent} />
            <RouteKeyEditor raw={parsed.raw} schema={parsed.schema} onEdit={setContent} />
          </Space>
        </div>
      ) : (
        <>
          {!isErrorsView && viewMode === 'struct' && parsed.error && (
            <Alert
              type="warning"
              showIcon
              message="源码不是合法 JSON，请切到源码视图修正"
              description={parsed.error}
            />
          )}
          <div style={{ height: 'calc(100vh - 440px)', minHeight: 240, border: '1px solid var(--border-color, rgba(0,0,0,0.06))' }}>
            <Editor
              language="json"
              theme={monacoTheme}
              value={content}
              onChange={(v) => setContent(v ?? '')}
              options={{
                fontSize: 12,
                minimap: { enabled: false },
                scrollBeyondLastLine: false,
                fixedOverflowWidgets: true,
                automaticLayout: true,
              }}
            />
          </div>
        </>
      )}

      {/* 新建/复制连接 Modal */}
      <Modal
        title={createMode === 'new' ? '新建连接' : `复制「${activeConn ?? ''}」为新连接`}
        open={createOpen}
        onCancel={() => setCreateOpen(false)}
        onOk={submitCreate}
        okText="创建"
        cancelText="取消"
        destroyOnHidden
      >
        <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
          格式 <code>{'<协议>:<服务名>'}</code>，协议只能是 tcp 或 udp，服务名不能为空、不能含 “:” 或 “_”。例如 <code>tcp:logic</code>。
        </Typography.Paragraph>
        <Input
          autoFocus
          placeholder="tcp:logic"
          value={createValue}
          onChange={(e) => setCreateValue(e.target.value)}
          onPressEnter={submitCreate}
        />
      </Modal>
    </Flex>
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
