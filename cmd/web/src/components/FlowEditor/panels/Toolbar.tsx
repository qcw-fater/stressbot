/**
 * 顶部工具栏（编辑器侧）。
 *
 * 设计：
 *   1. 单行布局，左/右两段，左侧编辑器原生功能、右侧通过 `extra` 槽接 RuntimeBar；
 *   2. 视觉规范：所有按钮统一 size middle、图标 + 短中文；分组用 `Divider type="vertical"`；
 *   3. 低频项收 Dropdown：文件类（新建/加载/导入/导出）；视图开关（主题/监听边）下放至 RuntimeBar 的"设置"。
 *
 * 详见 design-web-editor.md §7（菜单栏布局）。
 */

import { Badge, Button, Divider, Dropdown, Space, Tooltip, App as AntApp } from 'antd';
import {
  ApiOutlined,
  CheckCircleOutlined,
  CodeOutlined,
  DeploymentUnitOutlined,
  DownOutlined,
  DownloadOutlined,
  FileAddOutlined,
  FileTextOutlined,
  FolderOpenOutlined,
  ImportOutlined,
  NotificationOutlined,
  RedoOutlined,
  ReloadOutlined,
  ThunderboltOutlined,
  UndoOutlined,
} from '@ant-design/icons';
import { useMemo, useRef, useState, type ReactNode } from 'react';
import { validateFlow } from '../validation/refsCheck';
import { redo, undo } from '../store/undoRedo';
import { clearDraft } from '../store/persistDraft';
import { useFlowReadOnly } from '../flowReadOnlyContext';
import type { MenuProps } from 'antd';

import { useShallow } from 'zustand/react/shallow';
import { useFlowStore } from '../store/flowStore';
import { useEditorStore } from '../store/editorStore';
import { useProtoStore } from '../proto/protoStore';
import { fetchBaselineFlow } from '@/services/baselineApi';
import { FlowManagerModal } from './FlowManagerModal';
import { useFlowFileIO } from './useFlowFileIO';
import { exportAllTemplates, importTemplates, type TemplateBundle } from '../library/templateStore';
import type { FlowJson } from '../codec/flowToJson';

export interface ToolbarProps {
  onOpenValidation?: () => void;
  /** 渲染到工具栏最右侧，用于挂运行控制条 + 跨模块入口 + 设置 */
  extra?: ReactNode;
}

const SECTION_DIVIDER = (
  <Divider type="vertical" style={{ margin: '0 6px', height: 22, borderColor: 'var(--border-color)' }} />
);

export function Toolbar({ onOpenValidation, extra }: ToolbarProps) {
  const readOnly = useFlowReadOnly();
  const { message } = AntApp.useApp();
  const loadFromTaskFlow = useFlowStore((s) => s.loadFromTaskFlow);
  const { importFlow, exportFlow, syncScriptsAfterLoad } = useFlowFileIO();
  const reset = useFlowStore((s) => s.reset);
  const applyAutoLayout = useFlowStore((s) => s.applyAutoLayout);
  const setActivePanel = useEditorStore((s) => s.setActivePanel);
  const protoStatus = useProtoStore((s) => s.status);
  const protoFileCount = useProtoStore((s) => s.fileCount);
  const listenCount = useFlowStore((s) => Object.keys(s.listens).length);

  const [flowManagerOpen, setFlowManagerOpen] = useState(false);

  // 实时校验：错误数量徽章（用浅采样：每次状态变化重新计算）
  const flowSnap = useFlowStore(
    useShallow((s) => ({
      nodes: s.nodes,
      actions: s.actions,
      listens: s.listens,
      defaultDelayMs: s.defaultDelayMs,
    })),
  );
  // routeKey 模板缓存版本：cache 加载/刷新时 bump，触发重算（与 ValidationReport 对齐）。
  const routeKeyTemplatesVersion = useEditorStore((s) => s.routeKeyTemplatesVersion);
  const validation = useMemo(() => validateFlow(flowSnap), [flowSnap, routeKeyTemplatesVersion]);
  const errorCount = validation.errors.length;
  const warnCount = validation.warnings.length;

  // 隐藏的 input[type=file]，由"文件 → 导入"菜单项触发
  const importInputRef = useRef<HTMLInputElement>(null);
  const templateImportRef = useRef<HTMLInputElement>(null);

  const onExportTemplates = async () => {
    const bundle = await exportAllTemplates();
    const blob = new Blob([JSON.stringify(bundle, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'stressbot-templates.json';
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  };

  const onImportTemplates = async (file: File) => {
    try {
      const bundle = JSON.parse(await file.text()) as TemplateBundle;
      const r = await importTemplates(bundle);
      message.success(`导入模板：${r.actions} action + ${r.listens} listen`);
    } catch (e) {
      message.error(`导入模板失败：${(e as Error).message}`);
    }
    return false;
  };

  const onLoadDefault = async () => {
    try {
      const flow = await fetchBaselineFlow<FlowJson>();
      if (!flow) throw new Error('flow.json 不存在');
      loadFromTaskFlow(flow);
      message.success('已加载基线流程');
      void syncScriptsAfterLoad(flow);
    } catch (e) {
      message.error(`加载失败：${(e as Error).message}`);
    }
  };

  const fileMenuItems: MenuProps['items'] = [
    {
      key: 'new',
      icon: <FileAddOutlined />,
      label: '新建',
      disabled: readOnly,
      onClick: () => {
        clearDraft();
        reset();
      },
    },
    {
      key: 'load-default',
      icon: <ReloadOutlined />,
      label: '加载基线流程',
      disabled: readOnly,
      onClick: onLoadDefault,
    },
    { type: 'divider' as const },
    {
      key: 'import',
      icon: <ImportOutlined />,
      label: '导入流程 JSON…',
      disabled: readOnly,
      onClick: () => importInputRef.current?.click(),
    },
    {
      key: 'export',
      icon: <DownloadOutlined />,
      label: '导出流程 JSON',
      onClick: exportFlow,
    },
    { type: 'divider' as const },
    {
      key: 'import-templates',
      icon: <ImportOutlined />,
      label: '导入模板库…',
      onClick: () => templateImportRef.current?.click(),
    },
    {
      key: 'export-templates',
      icon: <DownloadOutlined />,
      label: '导出模板库',
      onClick: onExportTemplates,
    },
  ];

  // Proto 徽章状态
  const protoBadgeStatus =
    protoStatus === 'ready'
      ? 'success'
      : protoStatus === 'loading'
        ? 'processing'
        : protoStatus === 'error'
          ? 'error'
          : 'default';
  const protoTip =
    protoStatus === 'ready'
      ? `Proto 已加载（${protoFileCount} 文件）`
      : protoStatus === 'loading'
        ? '正在加载 Proto…'
        : protoStatus === 'error'
          ? 'Proto 加载失败'
          : 'Proto 未加载';

  return (
    <div
      style={{
        // flexWrap 让工具栏在中等宽度换行而非裁切——主操作（撤销/重做/保存/校验）与
        // 运行命令组在窄屏会溢出 overflow:hidden 的外壳导致不可达。
        display: 'flex',
        flexWrap: 'wrap',
        alignItems: 'center',
        justifyContent: 'space-between',
        padding: '8px 16px',
        background: 'var(--bg-panel)',
        borderBottom: '1px solid var(--border-color, rgba(0,0,0,0.06))',
        boxShadow: '0 1px 2px rgba(0,0,0,0.03)',
        gap: 8,
      }}
    >
      <Space size={4} align="center">
        {/* Logo */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginRight: 4 }}>
          <ThunderboltOutlined style={{ color: 'var(--color-orange)', fontSize: 18 }} />
          <strong style={{ fontSize: 16, color: 'var(--text-primary)' }}>stressbot</strong>
        </div>

        {SECTION_DIVIDER}

        {/* 编辑组：撤销 / 重做 */}
        <Space.Compact>
          <Tooltip title="撤销 (Ctrl+Z)">
            <Button icon={<UndoOutlined />} onClick={() => undo()} disabled={readOnly} />
          </Tooltip>
          <Tooltip title="重做 (Ctrl+Shift+Z)">
            <Button icon={<RedoOutlined />} onClick={() => redo()} disabled={readOnly} />
          </Tooltip>
        </Space.Compact>

        {SECTION_DIVIDER}

        {/* 文件菜单：新建 / 加载 / 导入 / 导出 */}
        <Dropdown menu={{ items: fileMenuItems }} trigger={['click']}>
          <Button icon={<FileTextOutlined />}>
            文件 <DownOutlined style={{ fontSize: 10 }} />
          </Button>
        </Dropdown>
        <Button icon={<FolderOpenOutlined />} onClick={() => setFlowManagerOpen(true)}>
          流程管理
        </Button>

        {SECTION_DIVIDER}

        {/* 视图组：JSON / Proto / 回调 / 校验 */}
        <Space size={4}>
          <Tooltip title="预览生成的 flow.json">
            <Button icon={<CodeOutlined />} onClick={() => setActivePanel({ kind: 'jsonPreview' })}>
              JSON
            </Button>
          </Tooltip>
          <Tooltip title={protoTip}>
            <Badge status={protoBadgeStatus} offset={[-4, 4]}>
              <Button icon={<ApiOutlined />} onClick={() => setActivePanel({ kind: 'protoBrowser' })}>
                Proto
              </Button>
            </Badge>
          </Tooltip>
          <Tooltip title="管理监听脚本">
            <Badge count={listenCount} overflowCount={99} offset={[-4, 4]} color="blue">
              <Button
                icon={<NotificationOutlined />}
                onClick={() => setActivePanel({ kind: 'listenPanel' })}
              >
                监听
              </Button>
            </Badge>
          </Tooltip>
          <Tooltip title={errorCount > 0 ? `${errorCount} 处错误` : warnCount > 0 ? `${warnCount} 处警告` : '校验通过'}>
            <Badge count={errorCount > 0 ? errorCount : warnCount} overflowCount={99} offset={[-4, 4]} color={errorCount > 0 ? undefined : 'orange'}>
              <Button
                icon={<CheckCircleOutlined />}
                onClick={() => onOpenValidation?.()}
                danger={errorCount > 0}
              >
                校验
              </Button>
            </Badge>
          </Tooltip>
        </Space>

        {SECTION_DIVIDER}

        {/* 自动布局：作用于画布拓扑，归在编辑器侧；"适配器"已挪到 RuntimeBar 与"资源"同组（同属协议/资源准备） */}
        <Tooltip title="按拓扑自动布局节点">
          <Button
            icon={<DeploymentUnitOutlined />}
            onClick={() => applyAutoLayout('LR')}
          />
        </Tooltip>
      </Space>

      {/* 右侧：RuntimeBar（运行控制 + 跨模块入口 + 设置） */}
      <div style={{ display: 'flex', alignItems: 'center' }}>{extra}</div>

      {/* 隐藏 input：文件菜单"导入流程"触发 */}
      <input
        ref={importInputRef}
        type="file"
        accept="application/json,.json"
        style={{ display: 'none' }}
        onChange={(e) => {
          const f = e.target.files?.[0];
          if (f) importFlow(f);
          e.target.value = '';
        }}
      />
      {/* 隐藏 input：文件菜单"导入模板库"触发 */}
      <input
        ref={templateImportRef}
        type="file"
        accept="application/json,.json"
        style={{ display: 'none' }}
        onChange={async (e) => {
          const f = e.target.files?.[0];
          if (f) await onImportTemplates(f);
          e.target.value = '';
        }}
      />
      <FlowManagerModal open={flowManagerOpen} onClose={() => setFlowManagerOpen(false)} />
    </div>
  );
}
