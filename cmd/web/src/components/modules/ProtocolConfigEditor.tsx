/**
 * 协议配置编辑器：按连接多份 codec.json + 共享 errors.json 源码 / 结构化 JSON 编辑。
 *
 * 从 ResourcesDrawer 抽离（B1-1），为后续挂到独立浮窗做准备。本组件只做功能平移，不重构样式/逻辑。
 *
 * 设计要点：
 * - 连接选择器 + 视图切换（结构化 / 源码）+ 保存 / 校验 / 从基线载入 + 新建 / 复制 / 删除连接。
 * - 内嵌「新建 / 复制连接」Modal 走 floatingWindowStore zIndex 合规 pattern（浮窗基线 1000+，内嵌 Modal 更高）。
 */

import { CopyOutlined, DeleteOutlined, InboxOutlined, PlusOutlined, CloudDownloadOutlined } from '@ant-design/icons';
import {
  Alert,
  App as AntApp,
  Button,
  Collapse,
  Flex,
  Input,
  Modal,
  Segmented,
  Select,
  Tabs,
  Tooltip,
  Typography,
  Upload,
} from 'antd';
import type { UploadProps } from 'antd';
import './codecEditor/codecEditor.css';
import { useEffect, useMemo, useState } from 'react';
import Editor from '@monaco-editor/react';
import { useEditorStore } from '../FlowEditor/store/editorStore';
import { useFloatingWindowStore } from '../FlowEditor/store/floatingWindowStore';
import {
  clearCodecSchema,
  clearErrorMap,
  collectCodecSchemaErrors,
  getCodecSchema,
  getErrorMap,
  listCodecFiles,
  setCodecSchema,
  setCodecSchemaFromBaseline,
  setErrorMap,
  setErrorMapFromBaseline,
  type ResourceFile,
  validateCodecSchema,
} from '@/services/resourcesStore';
import { fetchBaselineCodec, fetchBaselineCodecIndex } from '@/services/baselineApi';
import { getErrorCodes } from '@/services/api';
import type { FrameworkCode } from '@/types/api';
import { parseCodecForEdit } from './codecEditor/codecEdit';
import { ErrorMapEditor, validateErrorMap, parseErrorMapSafe } from './ErrorMapEditor';
import { FrameLayoutEditor } from './codecEditor/FrameLayoutEditor';
import { PipelineEditor } from './codecEditor/PipelineEditor';
import { RouteKeyEditor } from './codecEditor/RouteKeyEditor';
import { PreviewPanel } from './codecEditor/PreviewPanel';
import { deriveTransport } from './codecEditor/previewHelpers';

/* ─── 协议配置 — 按连接多份 codec.json + 共享 errors.json 源码 JSON 编辑器 ─── */

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
    { "name": "field0", "offset": 0, "size": 4, "type": "u32", "endian": "le", "role": "length" },
    { "name": "field1", "offset": 4, "size": 1, "type": "u8", "role": "route" },
    { "name": "field2", "offset": 5, "size": 1, "type": "u8", "role": "route" },
    { "name": "field3", "offset": 6, "size": 2, "type": "u16", "endian": "le", "role": "value", "source": { "kind": "const", "value": 0 } }
  ],
  "routeKeyTemplate": "{field1}:{field2}",
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

export function ProtocolConfigEditor() {
  const { message, modal } = AntApp.useApp();
  const theme = useEditorStore((s) => s.theme);
  const monacoTheme = theme === 'dark' ? 'vs-dark' : 'light';
  // zIndex 合规：浮窗基线 1000+，内嵌 Modal 需更高，照搬 ResourceTable pattern。
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;

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
  // errors.json 结构化表单：框架保留码（只读展示）+ 行级校验错误（保存前 gate）
  const [frameworkCodes, setFrameworkCodes] = useState<FrameworkCode[]>([]);
  const [errorMapErrors, setErrorMapErrors] = useState<string[]>([]);
  useEffect(() => {
    getErrorCodes()
      .then(setFrameworkCodes)
      .catch(() => setFrameworkCodes([]));
  }, []);

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
      const initial = file ? file.content : EMPTY_ERROR_MAP_TEMPLATE;
      setContent(initial);
      setSource(file ? '已保存' : '模板（未保存）');
      setErrorMapErrors(validateErrorMap(parseErrorMapSafe(initial)).map((e) => e.message));
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
      if (errorMapErrors.length > 0) {
        message.error(`errors.json 有 ${errorMapErrors.length} 处错误（含 < 100 或重复码），无法保存`);
        return;
      }
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
      setErrorMapErrors(validateErrorMap(parseErrorMapSafe(text)).map((e) => e.message));
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
      setErrorMapErrors([]);
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
  const activeLabel = activeConn === '__errors__' ? '错误码映射' : (activeConn ?? '未选择连接');
  const validationSummary = isErrorsView
    ? errorMapErrors.length === 0
      ? '校验通过'
      : `${errorMapErrors.length} 处问题：${errorMapErrors[0]}`
    : liveErrors.length === 0
      ? '校验通过'
      : `${liveErrors.length} 处问题：${liveErrors[0]}`;

  return (
    <Flex vertical gap={0} className="pce-shell">
      <div className="pce-cmdbar">
        <div className="pce-cmdbar-target">
          <Typography.Text className="pce-target-kicker">当前对象</Typography.Text>
          <Select
            size="small"
            style={{ minWidth: 200 }}
            value={activeConn ?? undefined}
            placeholder={files.length === 0 ? '暂无连接' : '选择连接'}
            loading={loading}
            onChange={handleSwitch}
            options={selectOptions}
          />
          <Typography.Text code className="pce-target-name">{activeLabel}</Typography.Text>
          <span className="pce-source-label">{source ?? '尚未加载'}</span>
        </div>

        <div className="pce-cmdbar-actions">
          <div className="pce-cmdbar-group">
            <Tooltip title="新建连接（输入 <协议>:<服务名>，如 tcp:logic）">
              <Button size="small" icon={<PlusOutlined />} onClick={() => openCreate('new')}>新建</Button>
            </Tooltip>
            <Tooltip title="复制当前连接为新连接">
              <Button size="small" icon={<CopyOutlined />} disabled={activeConn === null || activeConn === '__errors__'} onClick={() => openCreate('copy')}>复制</Button>
            </Tooltip>
            <Tooltip title="删除当前连接">
              <Button size="small" danger icon={<DeleteOutlined />} disabled={activeConn === null || activeConn === '__errors__'} onClick={() => activeConn && handleDelete(activeConn)}>删除</Button>
            </Tooltip>
          </div>
          <span className="pce-cmdbar-divider" />
          <div className="pce-cmdbar-group">
            <Tooltip title="从服务器拉取全部协议配置到本地">
              <Button size="small" icon={<CloudDownloadOutlined />} loading={pullingBaseline} onClick={onPullBaseline}>从基线载入</Button>
            </Tooltip>
            <Upload accept=".json,application/json" beforeUpload={onUpload} showUploadList={false}>
              <Button icon={<InboxOutlined />} size="small" disabled={activeConn === null}>导入</Button>
            </Upload>
          </div>
          <span className="pce-cmdbar-divider" />
          <div className="pce-cmdbar-group">
            <Button onClick={onSave} type="primary" size="small" disabled={activeConn === null}>保存</Button>
            <Button onClick={onClear} danger size="small" disabled={activeConn === null}>清空</Button>
          </div>
          {!isErrorsView && (
            <Segmented
              size="small"
              value={viewMode}
              onChange={(v) => setViewMode(v as 'struct' | 'source')}
              options={[
                { label: '结构化', value: 'struct' },
                { label: '源码', value: 'source' },
              ]}
            />
          )}
        </div>
      </div>

      <div className={`pce-status${liveErrors.length > 0 || loadError ? ' pce-status-warn' : ''}`}>
        <Typography.Text className="pce-status-title">
          {loadError ? '配置文件不存在' : validationSummary}
        </Typography.Text>
        <Typography.Text className="pce-status-note">
          {loadError ? '请新建连接或从基线载入。' : '协议配置启动任务时随连接配置一起下发。'}
        </Typography.Text>
        {activeConn !== null && activeConn !== '__errors__' && liveErrors.length > 1 && (
          <Collapse
            ghost
            size="small"
            className="pce-status-details"
            items={[{
              key: 'errors',
              label: `查看全部 ${liveErrors.length} 处问题`,
              children: (
                <ul className="pce-status-list">
                  {liveErrors.map((e, i) => <li key={i}>{e}</li>)}
                </ul>
              ),
            }]}
          />
        )}
      </div>

      {/* 主舞台：随 tab 切换；源码模式隐藏 Tabs 显示 Monaco */}
      <div className="pce-stage">
        {showStructView && parsed.raw && parsed.schema ? (
          <Tabs
            size="small"
            className="pce-tabs"
            defaultActiveKey="frame"
            items={[
              { key: 'frame', label: '帧布局', children: <FrameLayoutEditor raw={parsed.raw} schema={parsed.schema} onEdit={setContent} /> },
              { key: 'pipeline', label: '管线', children: <PipelineEditor raw={parsed.raw} schema={parsed.schema} onEdit={setContent} /> },
              { key: 'route', label: '路由键', children: <RouteKeyEditor raw={parsed.raw} schema={parsed.schema} onEdit={setContent} /> },
              { key: 'preview', label: '预览', children: <PreviewPanel raw={parsed.raw} schema={parsed.schema} transport={deriveTransport(activeConn)} /> },
            ]}
          />
        ) : (
          <>
            {!isErrorsView && viewMode === 'struct' && parsed.error && (
              <Alert
                type="warning"
                showIcon
                message="源码不是合法 JSON，请切到源码视图修正"
                description={parsed.error}
                style={{ margin: 8 }}
              />
            )}
            {isErrorsView ? (
              <ErrorMapEditor
                value={content}
                onChange={(next) => {
                  setContent(next);
                  setErrorMapErrors(validateErrorMap(parseErrorMapSafe(next)).map((e) => e.message));
                }}
                frameworkCodes={frameworkCodes}
              />
            ) : (
              <div className="pce-source-editor">
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
            )}
          </>
        )}
      </div>

      {/* 新建/复制连接 Modal — zIndex 合规：照搬 ResourceTable 的 floatingWindowStore pattern */}
      <Modal
        title={createMode === 'new' ? '新建连接' : `复制「${activeConn ?? ''}」为新连接`}
        open={createOpen}
        onCancel={() => setCreateOpen(false)}
        onOk={submitCreate}
        okText="创建"
        cancelText="取消"
        destroyOnHidden
        styles={{ mask: { zIndex: popupZ }, wrapper: { zIndex: popupZ + 1 } }}
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
