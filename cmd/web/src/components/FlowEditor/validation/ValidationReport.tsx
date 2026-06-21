/**
 * 校验报告浮动窗口：列出 errors / warnings / infos，点击跳转到对应节点 / action / listen。
 */

import { Alert, Badge, Button, Empty, List, Space, Tabs, Tag } from 'antd';
import { CloseCircleFilled, ExclamationCircleFilled, InfoCircleFilled, ReloadOutlined } from '@ant-design/icons';
import { useMemo } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useFlowStore } from '../store/flowStore';
import { useEditorStore } from '../store/editorStore';
import { validateFlow, type ValidationIssue } from './refsCheck';
import { FloatingWindow } from '../panels/FloatingWindow';

export interface ValidationReportDrawerProps {
  open: boolean;
  onClose: () => void;
}

export function ValidationReportDrawer({ open, onClose }: ValidationReportDrawerProps) {
  const flow = useFlowStore(
    useShallow((s) => ({
      defaultDelayMs: s.defaultDelayMs,
      nodes: s.nodes,
      actions: s.actions,
      listens: s.listens,
    })),
  );
  const setActivePanel = useEditorStore((s) => s.setActivePanel);
  const setSelectedNode = useEditorStore((s) => s.setSelectedNode);
  // routeKey 模板缓存版本：cache 加载/刷新时 bump，触发重算（消除 cache 未就绪时
  // 误报的 ROUTEKEY_CODEC_MISSING warning）。
  const routeKeyTemplatesVersion = useEditorStore((s) => s.routeKeyTemplatesVersion);

  const report = useMemo(() => validateFlow(flow), [flow, routeKeyTemplatesVersion]);

  const goto = (issue: ValidationIssue) => {
    if (!issue.location) return;
    if (issue.location.kind === 'node') {
      setSelectedNode(issue.location.id);
      setActivePanel({ kind: 'nodeEdit', nodeId: issue.location.id });
      return;
    }
    if (issue.location.kind === 'listen') {
      setActivePanel({ kind: 'listenEdit', listenName: issue.location.id });
      return;
    }
    if (issue.location.kind === 'action') {
      // 找出第一个引用此 action 的 node，跳过去（ActionEditor 嵌入 NodeEditorDrawer）
      const actionName = issue.location.id;
      for (const [id, n] of Object.entries(flow.nodes)) {
        if (n.type === 'action' && n.action === actionName) {
          setSelectedNode(id);
          setActivePanel({ kind: 'nodeEdit', nodeId: id });
          return;
        }
      }
    }
  };

  const renderList = (items: ValidationIssue[], severity: ValidationIssue['severity']) => (
    <List
      size="small"
      dataSource={items}
      locale={{ emptyText: <Empty description={`无 ${severity}`} /> }}
      renderItem={(it) => (
        <List.Item
          actions={
            it.location
              ? [
                  <a key="goto" onClick={() => goto(it)}>
                    跳转
                  </a>,
                ]
              : []
          }
        >
          <List.Item.Meta
            avatar={severity === 'error' ? <CloseCircleFilled style={{ color: 'var(--color-error)' }} /> : severity === 'warning' ? <ExclamationCircleFilled style={{ color: 'var(--color-warning)' }} /> : <InfoCircleFilled style={{ color: 'var(--color-blue)' }} />}
            title={
              <Space>
                <Tag>{it.code}</Tag>
                {it.location && <Tag color="default">{it.location.kind}: {it.location.id}</Tag>}
              </Space>
            }
            description={it.message}
          />
        </List.Item>
      )}
    />
  );

  return (
    <FloatingWindow
      windowId="validationReport"
      title={
        <Space>
          <span>校验报告</span>
          <Badge count={report.errors.length} color="red" />
          <Badge count={report.warnings.length} color="orange" />
          <Badge count={report.infos.length} color="blue" />
        </Space>
      }
      open={open}
      onClose={onClose}
      defaultSize={{ width: 580, height: 460 }}
      minSize={{ width: 400, height: 300 }}
      extra={
        <Button icon={<ReloadOutlined />} size="small" onClick={() => 0}>
          已实时刷新
        </Button>
      }
    >
      {report.total === 0 ? (
        <Alert
          type="success"
          showIcon
          message="校验通过"
          description="所有节点 / 动作 / 回调引用合法。可放心导出 flow.json。"
        />
      ) : (
        <Tabs
          defaultActiveKey={report.errors.length > 0 ? 'errors' : report.warnings.length > 0 ? 'warnings' : 'infos'}
          items={[
            {
              key: 'errors',
              label: `Errors (${report.errors.length})`,
              children: renderList(report.errors, 'error'),
            },
            {
              key: 'warnings',
              label: `Warnings (${report.warnings.length})`,
              children: renderList(report.warnings, 'warning'),
            },
            {
              key: 'infos',
              label: `Infos (${report.infos.length})`,
              children: renderList(report.infos, 'info'),
            },
          ]}
        />
      )}
    </FloatingWindow>
  );
}
