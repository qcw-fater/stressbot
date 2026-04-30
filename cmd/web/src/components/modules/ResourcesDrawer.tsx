/**
 * 资源管理 Drawer：管理用户上传到 IndexedDB 的 proto / lua 文件。
 *
 * 设计要点：
 * - 两个 Tab：proto / scripts；每 Tab 复用同一个表格 + 上传/删除/清空操作；
 * - 上传 proto / lua 完成后调用 `useProtoStore.reload()`（仅 proto 影响 ProtoBrowser），
 *   让 ProtoBrowser / ActionEditor 立即看到最新内容；
 * - "从默认基线导入"：通过 vite confMountPlugin 暴露的 /conf/proto/ 与 /conf/scripts/ 一键拉取，
 *   方便首次进入编辑态时快速搭建一份本地副本（生产期 Admin 同源也能工作）；
 * - 删除 / 清空操作走 antd Modal.confirm 二次确认，避免误清空。
 */

import { DeleteOutlined, ImportOutlined, InboxOutlined } from '@ant-design/icons';
import {
  Alert,
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
  message,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import type { UploadProps } from 'antd';
import { useEffect, useState } from 'react';
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
    <Drawer title="资源管理" open={open} onClose={onClose} width={760} destroyOnHidden={false}>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        message="proto / lua 资源持久化在浏览器 IndexedDB"
        description="启动压测任务时这些文件会随 flow.json 一起上传到 Admin。同一浏览器、同源页面共享。清空浏览器存储会丢失。"
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

const KIND_BASE_URL: Record<ResourceTableProps['kind'], { index: string; file: string }> = {
  proto: { index: '/conf/proto/index.json', file: '/conf/proto/' },
  lua: { index: '/conf/scripts/index.json', file: '/conf/scripts/' },
};

function ResourceTable({ kind }: ResourceTableProps) {
  const [items, setItems] = useState<ResourceFile[]>([]);
  const [loading, setLoading] = useState(false);
  const reloadProtos = useProtoStore((s) => s.reload);

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
    Modal.confirm({
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
    Modal.confirm({
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

  const handleImportFromBaseline = async () => {
    setLoading(true);
    try {
      const { index, file } = KIND_BASE_URL[kind];
      const r = await fetch(index);
      if (!r.ok) {
        throw new Error(`无法访问 ${index}（HTTP ${r.status}），仅在 dev / Admin 同源下可用`);
      }
      const names = (await r.json()) as string[];
      const fetched: Array<{ name: string; content: string }> = [];
      await Promise.all(
        names.map(async (name) => {
          const fr = await fetch(file + name);
          if (!fr.ok) throw new Error(`下载 ${name} 失败：HTTP ${fr.status}`);
          const text = await fr.text();
          fetched.push({ name, content: text });
        }),
      );
      if (kind === 'proto') {
        await addProtos(fetched);
        await reloadProtos();
      } else {
        await addScripts(fetched);
      }
      message.success(`从默认基线导入 ${fetched.length} 个 ${KIND_LABEL[kind]} 文件`);
    } catch (e) {
      message.error(`导入失败：${(e as Error).message}`);
    } finally {
      setLoading(false);
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
      title: '',
      key: 'op',
      width: 60,
      render: (_, record) => (
        <Tooltip title="删除">
          <Button
            type="text"
            size="small"
            danger
            icon={<DeleteOutlined />}
            onClick={() => handleRemove(record.name)}
          />
        </Tooltip>
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
        <Tooltip title={`从开发期挂载的 ${kind === 'proto' ? '/conf/proto/' : '/conf/scripts/'} 一键复制到本地存储`}>
          <Button icon={<ImportOutlined />} onClick={handleImportFromBaseline} loading={loading}>
            从默认基线导入
          </Button>
        </Tooltip>
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
              暂无 {KIND_LABEL[kind]} 文件。点击上方"上传"或"从默认基线导入"开始。
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
    </Flex>
  );
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(2)} MB`;
}
