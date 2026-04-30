/**
 * 二进制管理 Drawer：上传 stressbot-agent 二进制 + 滚动升级。
 *
 * 上传时让用户填版本号；OS/Arch 可省略（后端会按文件名/PE 头探测）。
 * 滚动升级：
 *   - 选定一个版本，触发 upgradeAll；
 *   - 顶部显示进度条（每 3 秒拉一次 upgrade-status，显示 currentAgent / completed / failed）。
 */

import {
  App,
  Button,
  Drawer,
  Empty,
  Form,
  Input,
  Modal,
  Progress,
  Select,
  Space,
  Spin,
  Table,
  Tag,
  Upload,
} from 'antd';
import type { UploadFile } from 'antd';
import { CloudUploadOutlined, DeleteOutlined, DownloadOutlined, ReloadOutlined } from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import { useEffect, useState } from 'react';
import { agentsApi, binariesApi, showApiError } from '@/services';
import type { Arch, BinaryMeta, OS, UpgradeStatus } from '@/types/api';

export interface BinariesDrawerProps {
  open: boolean;
  onClose: () => void;
}

export function BinariesDrawer({ open, onClose }: BinariesDrawerProps) {
  const { message, modal } = App.useApp();
  const [items, setItems] = useState<BinaryMeta[]>([]);
  const [loading, setLoading] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [upgradeStatus, setUpgradeStatus] = useState<UpgradeStatus | null>(null);
  const [uploadForm] = Form.useForm();
  const [uploadOpen, setUploadOpen] = useState(false);
  const [pendingFile, setPendingFile] = useState<UploadFile | null>(null);

  const refresh = async () => {
    setLoading(true);
    try {
      const [bins, status] = await Promise.all([binariesApi.listBinaries(), agentsApi.getUpgradeStatus()]);
      setItems(bins.items);
      setUpgradeStatus(status);
    } catch (err) {
      showApiError(err);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (open) refresh();
  }, [open]);

  // 升级中持续轮询
  useEffect(() => {
    if (!open || !upgradeStatus?.inProgress) return;
    const t = setInterval(async () => {
      try {
        const s = await agentsApi.getUpgradeStatus();
        setUpgradeStatus(s);
        if (!s.inProgress) {
          message.success(
            s.failed > 0 ? `滚动升级完成，${s.failed} 个失败` : `滚动升级完成 (${s.completed}/${s.total})`,
          );
        }
      } catch {
        // 静默
      }
    }, 3000);
    return () => clearInterval(t);
  }, [open, upgradeStatus?.inProgress, message]);

  const submitUpload = async () => {
    try {
      const values = await uploadForm.validateFields();
      if (!pendingFile?.originFileObj) {
        message.error('请选择文件');
        return;
      }
      setUploading(true);
      await binariesApi.uploadBinary({
        file: pendingFile.originFileObj as File,
        version: values.version,
        os: values.os,
        arch: values.arch,
        force: values.force,
      });
      message.success('上传成功');
      setUploadOpen(false);
      uploadForm.resetFields();
      setPendingFile(null);
      refresh();
    } catch (err) {
      showApiError(err);
    } finally {
      setUploading(false);
    }
  };

  const onDelete = (b: BinaryMeta) => {
    modal.confirm({
      title: `删除 ${b.filename}？`,
      content: `版本: ${b.version} · ${b.os}/${b.arch} · ${(b.sizeBytes / 1024 / 1024).toFixed(1)}MB`,
      okButtonProps: { danger: true },
      onOk: async () => {
        try {
          await binariesApi.deleteBinary(b.filename);
          message.success('已删除');
          refresh();
        } catch (err) {
          showApiError(err);
        }
      },
    });
  };

  const onUpgradeAll = (b: BinaryMeta) => {
    if (upgradeStatus?.inProgress) {
      message.warning('已有滚动升级在进行中');
      return;
    }
    modal.confirm({
      title: `滚动升级所有 Agent 到 ${b.version}？`,
      content: '将逐个让 Agent 拉取该版本并重启；正在跑任务的 Agent 会跳过。',
      okText: '开始升级',
      onOk: async () => {
        try {
          const r = await agentsApi.upgradeAll({ version: b.version });
          message.success(r.message);
          refresh();
        } catch (err) {
          showApiError(err);
        }
      },
    });
  };

  const cancelUpgrade = async () => {
    try {
      await agentsApi.cancelUpgrade();
      message.success('已取消');
      refresh();
    } catch (err) {
      showApiError(err);
    }
  };

  const columns: ColumnsType<BinaryMeta> = [
    { title: '版本', dataIndex: 'version', key: 'version', width: 120, render: (v: string) => <code>{v}</code> },
    { title: '文件名', dataIndex: 'filename', key: 'filename', render: (v: string) => <span style={{ fontFamily: 'monospace', fontSize: 11 }}>{v}</span> },
    {
      title: '平台',
      key: 'platform',
      width: 130,
      render: (_, b) => `${b.os} / ${b.arch}`,
    },
    {
      title: '大小',
      dataIndex: 'sizeBytes',
      key: 'sizeBytes',
      width: 90,
      render: (v: number) => `${(v / 1024 / 1024).toFixed(2)} MB`,
    },
    {
      title: 'SHA256',
      dataIndex: 'sha256',
      key: 'sha256',
      width: 120,
      render: (v: string) => <code style={{ fontSize: 10 }}>{v.slice(0, 12)}…</code>,
    },
    {
      title: '上传时间',
      dataIndex: 'uploadedAt',
      key: 'uploadedAt',
      width: 140,
      render: (v: string) => dayjs(v).format('MM-DD HH:mm:ss'),
    },
    {
      title: '操作',
      key: 'actions',
      width: 220,
      fixed: 'right',
      render: (_, b) => (
        <Space size={2}>
          <Button
            type="link"
            size="small"
            icon={<DownloadOutlined />}
            href={binariesApi.binaryDownloadUrl(b.filename)}
            target="_blank"
            rel="noreferrer"
          >
            下载
          </Button>
          <Button
            type="link"
            size="small"
            disabled={upgradeStatus?.inProgress}
            onClick={() => onUpgradeAll(b)}
          >
            滚动升级
          </Button>
          <Button type="text" size="small" danger icon={<DeleteOutlined />} onClick={() => onDelete(b)} />
        </Space>
      ),
    },
  ];

  return (
    <Drawer
      title={
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <span>二进制 / 滚动升级</span>
          <Space>
            <Button type="primary" icon={<CloudUploadOutlined />} onClick={() => setUploadOpen(true)}>
              上传新版本
            </Button>
            <Button size="small" icon={<ReloadOutlined />} loading={loading} onClick={refresh}>
              刷新
            </Button>
          </Space>
        </div>
      }
      open={open}
      onClose={onClose}
      width={1000}
      destroyOnHidden
    >
      {upgradeStatus?.inProgress && (
        <div
          style={{
            border: '1px solid #faad14',
            background: '#fff7e6',
            padding: 12,
            borderRadius: 6,
            marginBottom: 12,
          }}
        >
          <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 8 }}>
            <span>
              <Tag color="processing">升级中</Tag>
              <code>{upgradeStatus.version}</code>
              {upgradeStatus.currentAgentId && (
                <span style={{ marginLeft: 8, fontSize: 12 }}>当前: <code>{upgradeStatus.currentAgentId.slice(0, 8)}</code></span>
              )}
            </span>
            <Button size="small" danger onClick={cancelUpgrade}>取消</Button>
          </div>
          <Progress
            percent={Math.round(((upgradeStatus.completed + upgradeStatus.failed) / Math.max(upgradeStatus.total, 1)) * 100)}
            success={{ percent: Math.round((upgradeStatus.completed / Math.max(upgradeStatus.total, 1)) * 100) }}
            status={upgradeStatus.failed > 0 ? 'exception' : 'active'}
            format={() => `${upgradeStatus.completed} / ${upgradeStatus.total}${upgradeStatus.failed > 0 ? ` (失败 ${upgradeStatus.failed})` : ''}`}
          />
        </div>
      )}

      {loading && items.length === 0 ? (
        <Spin />
      ) : items.length === 0 ? (
        <Empty description="尚未上传任何二进制" />
      ) : (
        <Table<BinaryMeta>
          rowKey="filename"
          size="small"
          dataSource={items}
          columns={columns}
          pagination={false}
        />
      )}

      <Modal
        title="上传 stressbot-agent 二进制"
        open={uploadOpen}
        onOk={submitUpload}
        onCancel={() => {
          setUploadOpen(false);
          uploadForm.resetFields();
          setPendingFile(null);
        }}
        confirmLoading={uploading}
        okText="上传"
      >
        <Form form={uploadForm} layout="vertical">
          <Form.Item label="文件" required>
            <Upload
              maxCount={1}
              beforeUpload={(file) => {
                setPendingFile({
                  uid: file.uid,
                  name: file.name,
                  size: file.size,
                  type: file.type,
                  originFileObj: file as never,
                });
                return false;
              }}
              onRemove={() => setPendingFile(null)}
              fileList={pendingFile ? [pendingFile] : []}
            >
              <Button>选择文件</Button>
            </Upload>
          </Form.Item>
          <Form.Item label="版本号" name="version" rules={[{ required: true, message: '必填' }]}>
            <Input placeholder="如 v0.1.0 或 2026.04.29" />
          </Form.Item>
          <Form.Item label="OS" name="os" tooltip="留空则按文件名/PE 头探测">
            <Select<OS>
              allowClear
              options={[
                { label: 'windows', value: 'windows' },
                { label: 'linux', value: 'linux' },
                { label: 'darwin', value: 'darwin' },
              ]}
            />
          </Form.Item>
          <Form.Item label="Arch" name="arch">
            <Select<Arch>
              allowClear
              options={[
                { label: 'amd64', value: 'amd64' },
                { label: 'arm64', value: 'arm64' },
              ]}
            />
          </Form.Item>
          <Form.Item label="强制覆盖同版本" name="force" valuePropName="checked">
            <input type="checkbox" />
          </Form.Item>
        </Form>
      </Modal>
    </Drawer>
  );
}
