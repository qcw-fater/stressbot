/**
 * 资源管理 Drawer：管理用户上传到 IndexedDB 的 proto / lua 文件。
 *
 * 设计要点：
 * - 两个 Tab：proto / scripts；每 Tab 复用同一个表格 + 上传/删除/清空操作；
 * - 上传 proto / lua 完成后调用 `useProtoStore.reload()`（仅 proto 影响 ProtoBrowser），
 *   让 ProtoBrowser / ActionEditor 立即看到最新内容；
 * - 基线同步在任务启动时自动执行（fromBaseline 标记控制更新策略），无需手动导入；
 * - 删除 / 清空操作走 antd Modal.confirm 二次确认，避免误清空。
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
  Space,
  Table,
  Tabs,
  Tooltip,
  Typography,
  Upload,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import type { UploadProps } from 'antd';
import { useEffect, useState } from 'react';
import Editor from '@monaco-editor/react';
import { useEditorStore } from '../FlowEditor/store/editorStore';
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
} from '@/services/resourcesStore';

export interface ResourcesDrawerProps {
  open: boolean;
  onClose: () => void;
}

export function ResourcesDrawer({ open, onClose }: ResourcesDrawerProps) {
  return (
    <Drawer title="资源管理" open={open} onClose={onClose} width={760} maskClosable={false} destroyOnHidden={false}>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        message="协议文件与脚本资源管理"
        description="启动压测任务时这些文件会随流程配置一起提交。同一浏览器共享。清空浏览器存储会丢失。"
      />
      <Tabs
        defaultActiveKey="proto"
        items={[
          { key: 'proto', label: 'Proto 文件 (.proto)', children: <ResourceTable kind="proto" /> },
          { key: 'lua', label: 'Lua 脚本 (.lua)', children: <ResourceTable kind="lua" /> },
        ]}
      />
    </Drawer>
  );
}

interface ResourceTableProps {
  kind: 'proto' | 'lua';
}

const KIND_LABEL: Record<ResourceTableProps['kind'], string> = {
  proto: 'Proto',
  lua: 'Lua',
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

  // 编辑器状态
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
    const unsub = subscribe(() => {
      load();
    });
    return () => {
      unsub();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind]);

  const handleUpload: UploadProps['customRequest'] = async ({ file, onSuccess, onError }) => {
    try {
      const f = file as File;
      const text = await f.text();
      if (kind === 'proto') {
        await addProtos([{ name: f.name, content: text }]);
        await reloadProtos();
      } else {
        await addScripts([{ name: f.name, content: text }]);
      }
      message.success(`${f.name} 已上传`);
      onSuccess?.({}, new XMLHttpRequest());
    } catch (e) {
      const msg = (e as Error).message;
      message.error(`上传失败：${msg}`);
      onError?.(e as Error);
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
      <Space wrap>
        <Upload
          accept={KIND_EXT[kind].join(',')}
          multiple
          showUploadList={false}
          customRequest={handleUpload}
        >
          <Button type="primary" icon={<InboxOutlined />}>
            上传 {KIND_LABEL[kind]} 文件
          </Button>
        </Upload>
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
          scroll={{ y: 'calc(100vh - 360px)' }}
        />
      )}

      <Modal
        title={`编辑 ${editFile?.name}`}
        open={!!editFile}
        onCancel={() => setEditFile(null)}
        width={900}
        destroyOnClose
        maskClosable={false}
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
              options={{ minimap: { enabled: false }, fontSize: 13, wordWrap: 'on' }}
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
