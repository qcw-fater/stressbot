/**
 * 顶部工具栏：新建 / 打开 / 保存 / 导入 / 导出 / 预览 / 校验 / 自动布局 / 监听边开关。
 */

import { Badge, Button, Space, Switch, Tooltip, Upload, message } from 'antd';
import {
  ApiOutlined,
  BgColorsOutlined,
  CheckCircleOutlined,
  CodeOutlined,
  DownloadOutlined,
  FileOutlined,
  ImportOutlined,
  LinkOutlined,
  NotificationOutlined,
  RedoOutlined,
  ReloadOutlined,
  SaveOutlined,
  UndoOutlined,
} from '@ant-design/icons';
import { useMemo } from 'react';
import { validateFlow } from '../validation/refsCheck';
import { redo, undo } from '../store/undoRedo';
import { clearDraft } from '../store/persistDraft';
import type { UploadProps } from 'antd';

import { useShallow } from 'zustand/react/shallow';
import { useFlowStore } from '../store/flowStore';
import { useEditorStore } from '../store/editorStore';
import { useProtoStore } from '../proto/protoStore';
import type { TaskFlow } from '@/types/flow';

export interface ToolbarProps {
  onOpenValidation?: () => void;
}

export function Toolbar({ onOpenValidation }: ToolbarProps) {
  const loadFromTaskFlow = useFlowStore((s) => s.loadFromTaskFlow);
  const reset = useFlowStore((s) => s.reset);
  const applyAutoLayout = useFlowStore((s) => s.applyAutoLayout);
  const setActivePanel = useEditorStore((s) => s.setActivePanel);
  const showListenEdges = useEditorStore((s) => s.showListenEdges);
  const toggleListenEdges = useEditorStore((s) => s.toggleListenEdges);
  const protoStatus = useProtoStore((s) => s.status);
  const protoFileCount = useProtoStore((s) => s.fileCount);
  const callbackCount = useFlowStore((s) => Object.keys(s.callbacks).length);
  const toggleTheme = useEditorStore((s) => s.toggleTheme);

  // 实时校验：错误数量徽章（用浅采样：每次状态变化重新计算）
  const flowSnap = useFlowStore(
    useShallow((s) => ({
      nodes: s.nodes,
      actions: s.actions,
      callbacks: s.callbacks,
      defaultDelayMs: s.defaultDelayMs,
    })),
  );
  const errorCount = useMemo(() => validateFlow(flowSnap).errors.length, [flowSnap]);

  const onImport: UploadProps['beforeUpload'] = async (file) => {
    try {
      const text = await file.text();
      const parsed = JSON.parse(text) as TaskFlow;
      loadFromTaskFlow(parsed);
      message.success(`已加载 ${file.name}`);
    } catch (e) {
      message.error(`导入失败：${(e as Error).message}`);
    }
    return false; // 阻止 antd 默认上传
  };

  const onExport = () => {
    const flow = useFlowStore.getState().toTaskFlow();
    const blob = new Blob([JSON.stringify(flow, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'flow.json';
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  };

  const onLoadDefault = async () => {
    try {
      const res = await fetch('/conf/flow.json');
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const flow = (await res.json()) as TaskFlow;
      loadFromTaskFlow(flow);
      message.success('已加载 conf/flow.json');
    } catch (e) {
      message.error(`加载失败：${(e as Error).message}`);
    }
  };

  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        padding: '8px 16px',
        background: 'var(--bg-panel)',
        borderBottom: '1px solid var(--border-color, rgba(0,0,0,0.06))',
        boxShadow: '0 1px 2px rgba(0,0,0,0.03)',
      }}
    >
      <Space>
        <strong style={{ fontSize: 16, marginRight: 12 }}>stressbot · 流程编辑器</strong>
        <Tooltip title="撤销 (Ctrl+Z)">
          <Button icon={<UndoOutlined />} onClick={() => undo()} />
        </Tooltip>
        <Tooltip title="重做 (Ctrl+Shift+Z)">
          <Button icon={<RedoOutlined />} onClick={() => redo()} />
        </Tooltip>
        <Button
          icon={<FileOutlined />}
          onClick={() => {
            clearDraft();
            reset();
          }}
        >
          新建
        </Button>
        <Button icon={<ReloadOutlined />} onClick={onLoadDefault} type="primary">
          加载 conf/flow.json
        </Button>
        <Upload accept="application/json" beforeUpload={onImport} showUploadList={false}>
          <Button icon={<ImportOutlined />}>导入</Button>
        </Upload>
        <Button icon={<DownloadOutlined />} onClick={onExport}>
          导出
        </Button>
        <Button icon={<SaveOutlined />} disabled>
          保存（待接 API）
        </Button>
        <Button icon={<CodeOutlined />} onClick={() => setActivePanel({ kind: 'jsonPreview' })}>
          JSON 预览
        </Button>
        <Tooltip
          title={
            protoStatus === 'ready'
              ? `Proto 已加载（${protoFileCount} 文件）`
              : protoStatus === 'loading'
                ? '正在加载 Proto…'
                : protoStatus === 'error'
                  ? 'Proto 加载失败'
                  : 'Proto 未加载'
          }
        >
          <Badge
            status={
              protoStatus === 'ready'
                ? 'success'
                : protoStatus === 'loading'
                  ? 'processing'
                  : protoStatus === 'error'
                    ? 'error'
                    : 'default'
            }
            offset={[-6, 6]}
          >
            <Button icon={<ApiOutlined />} onClick={() => setActivePanel({ kind: 'protoBrowser' })}>
              Proto
            </Button>
          </Badge>
        </Tooltip>
        <Badge count={callbackCount} overflowCount={99} offset={[-4, 6]} color="orange">
          <Button
            icon={<NotificationOutlined />}
            onClick={() => setActivePanel({ kind: 'callbackPanel' })}
          >
            Callbacks
          </Button>
        </Badge>
        <Badge count={errorCount} overflowCount={99} offset={[-4, 6]}>
          <Button
            icon={<CheckCircleOutlined />}
            onClick={() => onOpenValidation?.()}
            type={errorCount > 0 ? 'default' : 'default'}
            danger={errorCount > 0}
          >
            校验
          </Button>
        </Badge>
        <Button onClick={() => applyAutoLayout('LR')}>自动布局</Button>
        <Tooltip title="协议适配器（codec.lua）— 通用游戏服务器协议接入">
          <Button icon={<LinkOutlined />} onClick={() => setActivePanel({ kind: 'codecAdapter' })}>
            适配器
          </Button>
        </Tooltip>
      </Space>
      <Space>
        <Tooltip title="切换主题（浅色 / 深色）">
          <Button icon={<BgColorsOutlined />} onClick={() => toggleTheme()} size="small" />
        </Tooltip>
        <Tooltip title="显式监听边（连接 action 与 CallbackCard 的橙色虚线）">
          <span>监听边</span>
        </Tooltip>
        <Switch checked={showListenEdges} onChange={() => toggleListenEdges()} size="small" />
      </Space>
    </div>
  );
}
