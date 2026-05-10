/**
 * 流程管理面板 (IndexedDB)
 */

import { App as AntApp, Button, Input, Modal, Space, Table, Popconfirm } from 'antd';
import { DeleteOutlined, FolderOpenOutlined, SaveOutlined, SyncOutlined } from '@ant-design/icons';
import { useEffect, useState } from 'react';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import { getFlow, listFlows, saveFlow, deleteFlow, type ManagedFlow } from '../store/flowManagerStore';
import { useFlowStore } from '../store/flowStore';

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

  const loadFromTaskFlow = useFlowStore((s) => s.loadFromTaskFlow);

  const fetchFlows = async () => {
    setLoading(true);
    try {
      const data = await listFlows();
      setFlows(data);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (open) {
      fetchFlows();
      setSaveName(`未命名流程 ${dayjs().format('MMDD_HHmm')}`);
    }
  }, [open]);

  const handleSaveAs = async () => {
    if (!saveName.trim()) {
      message.error('流程名称不能为空');
      return;
    }
    setIsSaving(true);
    try {
      const state = useFlowStore.getState();
      const flow = state.toTaskFlow();
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
      const record = await getFlow(id);
      if (!record) {
        message.error('流程不存在或已损坏');
        return;
      }
      loadFromTaskFlow(record.flow, record.layout);
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
      render: (v: string) => <strong style={{ color: 'var(--text-primary)' }}>{v}</strong>,
    },
    {
      title: '更新时间',
      dataIndex: 'updatedAt',
      key: 'updatedAt',
      width: 160,
      render: (v: number) => <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>{dayjs(v).format('YYYY-MM-DD HH:mm:ss')}</span>,
    },
    {
      title: '操作',
      key: 'action',
      width: 140,
      render: (_, record) => (
        <Space size="middle">
          <Button type="link" size="small" icon={<FolderOpenOutlined />} onClick={() => handleOpen(record.id, record.name)}>
            打开
          </Button>
          <Popconfirm title={`用当前草稿覆盖 "${record.name}"？`} onConfirm={() => handleOverwrite(record.id, record.name)} okText="覆盖" cancelText="取消">
            <Button type="link" size="small" icon={<SyncOutlined />}>
              覆盖
            </Button>
          </Popconfirm>
          <Popconfirm title={`确定要删除 "${record.name}" 吗？`} onConfirm={() => handleDelete(record.id, record.name)} okText="删除" cancelText="取消" okButtonProps={{ danger: true }}>
            <Button type="text" danger size="small" icon={<DeleteOutlined />} />
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <Modal
      title="本地流程管理 (IndexedDB)"
      open={open}
      onCancel={onClose}
      footer={null}
      width={700}
      destroyOnClose
    >
      <div style={{ marginBottom: 16, display: 'flex', gap: 8 }}>
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
    </Modal>
  );
}
