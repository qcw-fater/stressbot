/**
 * 资源管理 Drawer：管理用户上传到本地存储的定义文件与脚本。
 *
 * 设计要点：
 * - 两个 Tab：Proto / Lua；二者复用 ResourceTable。
 * - 协议配置（*_codec.json / errors.json）已抽离为独立的 ProtocolConfigEditor。
 * - 顶部提供「拉取」按钮做显式同步（拉取服务器基线），不再自动拉取；
 * - 冲突通过 BaselineSyncModal 显式处理；本地改动会在启动任务时随配置一并提交到服务器，无需单独推送。
 */

import { DeleteOutlined, InboxOutlined, EditOutlined, CloudDownloadOutlined } from '@ant-design/icons';
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
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useCallback, useEffect, useRef, useState } from 'react';
import Editor from '@monaco-editor/react';
import { useShallow } from 'zustand/react/shallow';
import { useEditorStore } from '../FlowEditor/store/editorStore';
import { useFloatingWindowStore } from '../FlowEditor/store/floatingWindowStore';
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
  subtractSyncResult,
  syncResourcesFromBaseline,
} from '@/services/resourcesStore';
import { BaselineSyncModal } from './BaselineSyncModal';

export interface ResourcesDrawerProps {
  open: boolean;
  onClose: () => void;
}

export function ResourcesDrawer({ open, onClose }: ResourcesDrawerProps) {
  const { message } = AntApp.useApp();
  const { pendingSyncResult, setPendingSyncResult } = useEditorStore(
    useShallow((s) => ({
      pendingSyncResult: s.pendingSyncResult,
      setPendingSyncResult: s.setPendingSyncResult,
    })),
  );
  const [syncModalOpen, setSyncModalOpen] = useState(false);
  const [pulling, setPulling] = useState(false);

  const reloadProtos = useProtoStore((s) => s.reload);

  const hasConflicts = pendingSyncResult != null && (pendingSyncResult.conflicts.length > 0 || pendingSyncResult.removed.length > 0);

  // 拉取（svn update）：与服务器对比，自动合并"仅服务器修改/新增"，冲突弹面板。
  // 拉取成功后必须刷新内存 protoRegistry：syncResourcesFromBaseline 只改 IndexedDB/资源列表，
  // 不会通知 ProtoStore 重新编译；否则 ProtoBrowser/字段选择/校验继续用旧定义，
  // 而任务下发却用新 proto，形成前后端语义错位。
  const handlePull = async () => {
    setPulling(true);
    try {
      const sync = await syncResourcesFromBaseline();
      await reloadProtos();
      if (sync.conflicts.length > 0 || sync.removed.length > 0) {
        setPendingSyncResult(sync);
        setSyncModalOpen(true);
        message.warning(`检测到 ${sync.conflicts.length + sync.removed.length} 处冲突，请逐项确认`);
      } else if (sync.added.length > 0) {
        message.success(`已从服务器拉取，新增 ${sync.added.length} 个资源`);
      } else {
        message.success('已从服务器拉取完成');
      }
    } catch (e) {
      message.error(`拉取失败：${(e as Error).message}`);
    } finally {
      setPulling(false);
    }
  };

  return (
    <>
      <Drawer title="资源管理" open={open} onClose={onClose} width={760} maskClosable={false} destroyOnHidden={false} styles={{ body: { paddingBottom: 8 } }}>
        <Flex justify="space-between" align="center" style={{ marginBottom: 12 }}>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            从服务器拉取最新资源到本地；本地改动会在启动任务时随配置一并提交到服务器。
          </Typography.Text>
          <Tooltip title="从服务器拉取最新资源（仅服务器改动会自动合并，双方都改的会提示冲突）">
            <Button icon={<CloudDownloadOutlined />} loading={pulling} onClick={handlePull}>
              拉取
            </Button>
          </Tooltip>
        </Flex>
        {hasConflicts && (
          <Alert
            type="warning"
            showIcon
            style={{ marginBottom: 12 }}
            message="资源存在冲突"
            description={
              <Space>
                <span>{pendingSyncResult.conflicts.length + pendingSyncResult.removed.length} 处冲突待处理</span>
                <Button size="small" type="primary" onClick={() => setSyncModalOpen(true)}>
                  处理冲突
                </Button>
              </Space>
            }
          />
        )}
        <Tabs
          defaultActiveKey="proto"
          items={[
            { key: 'proto', label: 'Proto', children: <ResourceTable kind="proto" /> },
            { key: 'lua', label: 'Lua', children: <ResourceTable kind="lua" /> },
          ]}
        />
      </Drawer>

      {pendingSyncResult && (
        <BaselineSyncModal
          open={syncModalOpen}
          result={pendingSyncResult}
          onClose={() => {
            setSyncModalOpen(false);
          }}
          onResolved={() => {
            setPendingSyncResult(subtractSyncResult(pendingSyncResult, pendingSyncResult));
            // 冲突解决同样改写了 proto 的 IDB（采用服务器版本或删除本地），
            // 必须刷新内存 protoRegistry，否则编辑器继续用旧 proto。
            void reloadProtos();
          }}
        />
      )}
    </>
  );
}

/* ─── Proto / Lua 通用表格 ─── */

interface ResourceTableProps {
  kind: 'proto' | 'lua';
}

const KIND_LABEL: Record<ResourceTableProps['kind'], string> = {
  proto: 'Proto',
  lua: 'Lua',
};

const KIND_DESC: Record<ResourceTableProps['kind'], string> = {
  proto: 'Proto 文件：定义消息结构与序列化格式，启动任务时随流程配置一起提交。',
  lua: 'Lua 脚本文件：实现复杂业务逻辑，由流程中的脚本动作引用。',
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
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;

  const [editFile, setEditFile] = useState<ResourceFile | null>(null);
  const [editContent, setEditContent] = useState('');
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const data = kind === 'proto' ? await listProto() : await listScript();
      setItems(data);
    } finally {
      setLoading(false);
    }
  }, [kind]);

  useEffect(() => {
    load();
    const unsub = subscribe(() => load());
    return () => unsub();
  }, [load]);

  const fileInputRef = useRef<HTMLInputElement>(null);
  const [uploading, setUploading] = useState(false);
  const [uploadProgress, setUploadProgress] = useState({ current: 0, total: 0 });

  const handleBatchUpload = async (fileList: FileList) => {
    const files = Array.from(fileList);
    if (files.length === 0) return;
    setUploading(true);
    setUploadProgress({ current: 0, total: files.length });

    // 阶段 1：读取全部文件，按文件名去重（保留最后一个）
    const batch = new Map<string, { name: string; content: string }>();
    let readFail = 0;
    for (let i = 0; i < files.length; i++) {
      try {
        const text = await files[i].text();
        batch.set(files[i].name, { name: files[i].name, content: text });
      } catch {
        readFail++;
      }
      setUploadProgress({ current: i + 1, total: files.length });
    }

    if (batch.size === 0) {
      setUploading(false);
      message.error('全部文件读取失败');
      return;
    }

    // 阶段 2：检测与已有资源的同名冲突
    const existing = kind === 'proto' ? await listProto() : await listScript();
    const existingNames = new Set(existing.map((f) => f.name));
    const conflicts: string[] = [];
    const newFiles: { name: string; content: string }[] = [];
    for (const f of batch.values()) {
      if (existingNames.has(f.name)) conflicts.push(f.name);
      else newFiles.push(f);
    }

    let toWrite: { name: string; content: string }[];
    let overwritten = 0;

    if (conflicts.length > 0) {
      // 注意：点击遮罩/ESC 与点击取消按钮效果相同（resolve(false) → 跳过冲突，仅上传新文件），
      // antd modal.confirm API 无法区分这两种操作。
      const doOverwrite = await new Promise<boolean>((resolve) => {
        modal.confirm({
          title: '发现同名文件',
          content: (
            <div>
              <p>
                已选 {batch.size} 个文件中有 <b>{conflicts.length}</b> 个与已有资源同名：
              </p>
              <div style={{ maxHeight: 120, overflow: 'auto', lineHeight: 1.8, margin: '4px 0' }}>
                {conflicts.map((name) => (
                  <div key={name}>
                    <code>{name}</code>
                  </div>
                ))}
              </div>
            </div>
          ),
          okText: '覆盖全部',
          okType: 'primary',
          cancelText: conflicts.length < batch.size ? '仅上传新文件' : '取消',
          onOk: () => resolve(true),
          onCancel: () => resolve(false),
        });
      });

      if (doOverwrite) {
        toWrite = Array.from(batch.values());
        overwritten = conflicts.length;
      } else {
        toWrite = newFiles;
      }
    } else {
      toWrite = Array.from(batch.values());
    }

    // 阶段 3：写入本地存储
    if (toWrite.length > 0) {
      if (kind === 'proto') {
        await addProtos(toWrite);
        await reloadProtos();
      } else {
        await addScripts(toWrite);
      }
    }

    setUploading(false);

    // 阶段 4：汇总提示
    const skipped = conflicts.length - overwritten;

    if (files.length === 1) {
      if (toWrite.length > 0) {
        message.success(overwritten > 0 ? `${files[0].name} 已上传（覆盖）` : `${files[0].name} 已上传`);
      } else {
        message.info(`已跳过同名文件 ${files[0].name}`);
      }
      return;
    }

    if (toWrite.length === 0 && readFail === 0) {
      message.info('已跳过全部同名文件');
      return;
    }

    const parts: string[] = [];
    if (toWrite.length > 0) {
      parts.push(
        overwritten > 0
          ? `${toWrite.length} 个已上传（含 ${overwritten} 个覆盖）`
          : `${toWrite.length} 个已上传`,
      );
    }
    if (skipped > 0) parts.push(`${skipped} 个同名跳过`);
    if (readFail > 0) parts.push(`${readFail} 个读取失败`);

    if (readFail === 0 && skipped === 0) {
      message.success(parts[0]);
    } else if (toWrite.length > 0) {
      message.warning(parts.join('，'));
    } else {
      message.error(parts.join('，'));
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
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        {KIND_DESC[kind]}
      </Typography.Text>
      <Space wrap>
        <input
          ref={fileInputRef}
          type="file"
          accept={KIND_EXT[kind].join(',')}
          multiple
          hidden
          onChange={(e) => {
            if (e.target.files?.length) handleBatchUpload(e.target.files);
            e.target.value = '';
          }}
        />
        <Button
          type="primary"
          icon={<InboxOutlined />}
          loading={uploading}
          onClick={() => fileInputRef.current?.click()}
        >
          {uploading
            ? `上传中 ${uploadProgress.current}/${uploadProgress.total}`
            : `上传 ${KIND_LABEL[kind]} 文件`}
        </Button>
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
          scroll={{ y: 'calc(100vh - 280px)' }}
        />
      )}

      <Modal
        title={`编辑 ${editFile?.name}`}
        open={!!editFile}
        onCancel={() => setEditFile(null)}
        width={900}
        destroyOnHidden
        maskClosable={false}
        styles={{ mask: { zIndex: popupZ }, wrapper: { zIndex: popupZ + 1 } }}
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
              options={{ minimap: { enabled: false }, fontSize: 13, wordWrap: 'on', fixedOverflowWidgets: true }}
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
