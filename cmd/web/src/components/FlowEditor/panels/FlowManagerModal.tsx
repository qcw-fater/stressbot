/**
 * 流程管理面板。
 *
 * 已保存流程存放在服务器流程模板库（后端 MySQL），不再使用浏览器本地存储。
 */

import { App as AntApp, Button, Input, Modal, Space, Table, Popconfirm, Tooltip } from 'antd';
import { DeleteOutlined, DownloadOutlined, FolderOpenOutlined, ImportOutlined, SaveOutlined, SyncOutlined } from '@ant-design/icons';
import { useCallback, useEffect, useRef, useState } from 'react';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import { FLOW_NAME_MAX_LENGTH, getFlow, listFlows, saveFlow, deleteFlow, renameFlow, type ManagedFlow } from '../store/flowManagerStore';
import { useFlowStore } from '../store/flowStore';
import { useFlowFileIO } from './useFlowFileIO';
import { validateLatestFlow } from '../validation/validationStore';

export interface FlowManagerModalProps {
  open: boolean;
  onClose: () => void;
}

export function FlowManagerModal({ open, onClose }: FlowManagerModalProps) {
  const { message } = AntApp.useApp();
  const [flows, setFlows] = useState<ManagedFlow[]>([]);
  const [loading, setLoading] = useState(false);
  const [saveName, setSaveName] = useState('');
  const [isSaving, setIsSaving] = useState(false);
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const [renameValue, setRenameValue] = useState('');
  const [isRenaming, setIsRenaming] = useState(false);
  const committingRenameRef = useRef(false);

  const { importFlow, exportFlow } = useFlowFileIO();
  // 隐藏的 input[type=file]，由「导入流程」按钮触发
  const importInputRef = useRef<HTMLInputElement>(null);

  const loadFromTaskFlow = useFlowStore((s) => s.loadFromTaskFlow);

  const fetchFlows = useCallback(async () => {
    setLoading(true);
    try {
      const data = await listFlows();
      setFlows(data);
    } catch (e) {
      // 服务器未启用流程库等错误：给出提示并置空，避免残留旧列表。
      message.error(`加载流程列表失败：${(e as Error).message}`);
      setFlows([]);
    } finally {
      setLoading(false);
    }
  }, [message]);

  useEffect(() => {
    if (open) {
      fetchFlows();
      setSaveName(`未命名流程 ${dayjs().format('MMDD_HHmm')}`);
      setRenamingId(null);
      setRenameValue('');
      committingRenameRef.current = false;
    }
  }, [fetchFlows, open]);

  const startRename = (record: ManagedFlow) => {
    setRenamingId(record.id);
    setRenameValue(record.name);
    committingRenameRef.current = false;
  };

  const cancelRename = () => {
    setRenamingId(null);
    setRenameValue('');
    committingRenameRef.current = false;
  };

  const handleRenameCommit = async () => {
    if (!renamingId || committingRenameRef.current) return;
    committingRenameRef.current = true;
    const nextName = renameValue.trim();
    if (!nextName) {
      message.error('流程名称不能为空');
      committingRenameRef.current = false;
      return;
    }
    const current = flows.find((item) => item.id === renamingId);
    if (current && current.name.trim() === nextName) {
      cancelRename();
      return;
    }
    setIsRenaming(true);
    try {
      await renameFlow(renamingId, nextName);
      message.success(`已重命名为「${nextName}」`);
      cancelRename();
      fetchFlows();
    } catch (e) {
      message.error(`重命名失败：${(e as Error).message}`);
      committingRenameRef.current = false;
    } finally {
      setIsRenaming(false);
    }
  };

  const handleModalClose = () => {
    cancelRename();
    onClose();
  };

  const handleSaveAs = async () => {
    if (!saveName.trim()) {
      message.error('流程名称不能为空');
      return;
    }
    setIsSaving(true);
    try {
      const state = useFlowStore.getState();
      const flow = state.toTaskFlow();
      const report = validateLatestFlow(flow);
      if (report.errors.length > 0) {
        message.error(`流程存在 ${report.errors.length} 项错误，请修复后再保存`);
        return;
      }
      const layout = state.layout;
      await saveFlow(saveName, flow, layout);
      message.success(`已保存流程 "${saveName}"`);
      setSaveName('');
      fetchFlows();
    } catch (e) {
      message.error(`保存失败：${(e as Error).message}`);
    } finally {
      setIsSaving(false);
    }
  };

  const handleOpen = async (id: string, name: string) => {
    try {
      const detail = await getFlow(id);
      if (!detail) {
        message.error('流程不存在或已损坏');
        return;
      }
      loadFromTaskFlow(detail.flow, detail.layout);
      message.success(`已打开流程 "${name}"`);
      onClose();
    } catch (e) {
      message.error(`打开失败：${(e as Error).message}`);
    }
  };

  const handleDelete = async (id: string, name: string) => {
    try {
      await deleteFlow(id);
      message.success(`已删除流程 "${name}"`);
      fetchFlows();
    } catch (e) {
      message.error(`删除失败：${(e as Error).message}`);
    }
  };

  const handleOverwrite = async (id: string, name: string) => {
    try {
      const state = useFlowStore.getState();
      const flow = state.toTaskFlow();
      const report = validateLatestFlow(flow);
      if (report.errors.length > 0) {
        message.error(`流程存在 ${report.errors.length} 项错误，请修复后再保存`);
        return;
      }
      const layout = state.layout;
      await saveFlow(name, flow, layout, id);
      message.success(`已覆盖流程 "${name}"`);
      fetchFlows();
    } catch (e) {
      message.error(`覆盖失败：${(e as Error).message}`);
    }
  };

  const columns: ColumnsType<ManagedFlow> = [
    {
      title: '流程名称',
      dataIndex: 'name',
      key: 'name',
      render: (v: string, record) => {
        if (record.id === renamingId) {
          return (
            <Input
              size="small"
              value={renameValue}
              maxLength={FLOW_NAME_MAX_LENGTH}
              autoFocus
              disabled={isRenaming}
              onChange={(e) => setRenameValue(e.target.value)}
              onBlur={handleRenameCommit}
              onPressEnter={handleRenameCommit}
              onKeyDown={(e) => {
                if (e.key === 'Escape') {
                  e.stopPropagation();
                  cancelRename();
                }
              }}
              onClick={(e) => e.stopPropagation()}
            />
          );
        }
        return (
          <Tooltip title="双击重命名" mouseEnterDelay={0.4}>
            <strong
              style={{ color: 'var(--text-primary)', cursor: 'text' }}
              onDoubleClick={() => startRename(record)}
            >
              {v}
            </strong>
          </Tooltip>
        );
      },
    },
    {
      title: '节点',
      dataIndex: 'nodeCount',
      key: 'nodeCount',
      width: 70,
      render: (v: number) => <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>{v}</span>,
    },
    {
      title: '更新时间',
      dataIndex: 'updatedAt',
      key: 'updatedAt',
      width: 160,
      render: (v: string) => <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>{dayjs(v).format('YYYY-MM-DD HH:mm:ss')}</span>,
    },
    {
      title: '操作',
      key: 'action',
      width: 150,
      render: (_, record) => {
        const editingThisRow = record.id === renamingId;
        return (
          <Space size="middle">
            <Button type="link" size="small" icon={<FolderOpenOutlined />} disabled={editingThisRow} onClick={() => handleOpen(record.id, record.name)}>
              打开
            </Button>
            <Popconfirm title={`用当前草稿覆盖 "${record.name}"？`} onConfirm={() => handleOverwrite(record.id, record.name)} okText="覆盖" cancelText="取消">
              <Button type="link" size="small" icon={<SyncOutlined />} disabled={editingThisRow}>
                覆盖
              </Button>
            </Popconfirm>
            <Popconfirm title={`确定要删除 "${record.name}" 吗？`} onConfirm={() => handleDelete(record.id, record.name)} okText="删除" cancelText="取消" okButtonProps={{ danger: true }}>
              <Button type="text" danger size="small" icon={<DeleteOutlined />} disabled={editingThisRow} />
            </Popconfirm>
          </Space>
        );
      },
    },
  ];

  return (
    <Modal
      title="流程管理"
      open={open}
      onCancel={handleModalClose}
      footer={null}
      width={700}
      destroyOnHidden
    >
      <div style={{ marginBottom: 16, display: 'flex', gap: 8, alignItems: 'center' }}>
        <Input
          placeholder="输入流程名称保存当前草稿..."
          value={saveName}
          onChange={(e) => setSaveName(e.target.value)}
          onPressEnter={handleSaveAs}
          style={{ width: 300 }}
        />
        <Button type="primary" icon={<SaveOutlined />} onClick={handleSaveAs} loading={isSaving}>
          另存为新流程
        </Button>
        <div style={{ marginLeft: 'auto', display: 'flex', gap: 8 }}>
          <Tooltip title="从本地 JSON 文件导入流程">
            <Button icon={<ImportOutlined />} onClick={() => importInputRef.current?.click()}>
              导入流程
            </Button>
          </Tooltip>
          <Tooltip title="导出当前流程为 flow.json">
            <Button icon={<DownloadOutlined />} onClick={exportFlow}>
              导出流程
            </Button>
          </Tooltip>
        </div>
      </div>
      <Table<ManagedFlow>
        rowKey="id"
        columns={columns}
        dataSource={flows}
        loading={loading}
        size="small"
        pagination={{ pageSize: 10 }}
        locale={{ emptyText: '暂无保存的流程' }}
      />
      {/* 隐藏 input：「导入流程」按钮触发；成功后关闭弹窗回画布，与「打开」一致 */}
      <input
        ref={importInputRef}
        type="file"
        accept="application/json,.json"
        style={{ display: 'none' }}
        onChange={async (e) => {
          const f = e.target.files?.[0];
          if (f) {
            const ok = await importFlow(f);
            if (ok) handleModalClose();
          }
          e.target.value = '';
        }}
      />
    </Modal>
  );
}
