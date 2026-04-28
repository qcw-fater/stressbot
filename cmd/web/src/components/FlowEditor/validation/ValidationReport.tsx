/**
 * 校验报告抽屉：列出 errors / warnings / infos，点击跳转到对应节点 / action / callback。
 */

import { Alert, Badge, Button, Drawer, Empty, List, Space, Tabs, Tag } from 'antd';
import { CloseCircleFilled, ExclamationCircleFilled, InfoCircleFilled, ReloadOutlined } from '@ant-design/icons';
import { useMemo } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useFlowStore } from '../store/flowStore';
import { useEditorStore } from '../store/editorStore';
import { validateFlow, type ValidationIssue } from './refsCheck';

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
      callbacks: s.callbacks,
    })),
  );
  const setActivePanel = useEditorStore((s) => s.setActivePanel);
  const setSelectedNode = useEditorStore((s) => s.setSelectedNode);

  const report = useMemo(() => validateFlow(flow), [flow]);

  const goto = (issue: ValidationIssue) => {
    if (!issue.location) return;
    // 先关闭校验抽屉，避免与 NodeEditorDrawer / CallbackEditor 视觉重叠
    onClose();
    if (issue.location.kind === 'node') {
      setSelectedNode(issue.location.id);
      setActivePanel({ kind: 'nodeEdit', nodeId: issue.location.id });
      return;
    }
    if (issue.location.kind === 'callback') {
      setActivePanel({ kind: 'callbackEdit', callbackName: issue.location.id });
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
            avatar={severity === 'error' ? <CloseCircleFilled style={{ color: '#ff4d4f' }} /> : severity === 'warning' ? <ExclamationCircleFilled style={{ color: '#faad14' }} /> : <InfoCircleFilled style={{ color: '#1677ff' }} />}
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
    <Drawer
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
      width={620}
      mask={false}
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
    </Drawer>
  );
}
