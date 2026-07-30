import { DownloadOutlined } from '@ant-design/icons';
import { Alert, Button, Checkbox, Modal, Spin, Typography } from 'antd';
import { useEffect, useMemo, useState } from 'react';

import { loadDraft, type DraftSnapshot } from '@/components/FlowEditor/store/persistDraft';
import { createBackupBundle, downloadBackupBundle } from '@/services/configTransfer/backupCodec';
import {
  defaultSectionRegistry,
  type ConfigSectionRegistry,
} from '@/services/configTransfer/sectionRegistry';
import { BACKUP_SECTIONS, type BackupSection } from '@/services/configTransfer/types';
import './ConfigBackupModal.css';

interface SectionSummary {
  count: number;
  bytes: number;
  error?: string;
}

interface SectionGroup {
  label: string;
  sections: BackupSection[];
}

const SECTION_GROUPS: SectionGroup[] = [
  { label: '流程', sections: ['flows', 'draft'] },
  { label: '资源', sections: ['protoFiles', 'luaFiles', 'codecFiles', 'errorMap'] },
  { label: '模板库', sections: ['actionTemplates', 'listenTemplates'] },
  { label: '工具', sections: ['notepadFiles'] },
];

function runtimeAdapter(registry: ConfigSectionRegistry, section: BackupSection) {
  return registry[section] as unknown as {
    label: string;
    read: () => Promise<unknown>;
    count: (value: unknown) => number;
  };
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MiB`;
}

function measureValue(value: unknown): number {
  return new Blob([JSON.stringify(value)]).size;
}

export interface ConfigBackupModalProps {
  open: boolean;
  onClose: () => void;
  flowLibrary: boolean | undefined;
  templateLibrary: boolean | undefined;
  registry?: ConfigSectionRegistry;
  readDraft?: () => DraftSnapshot | null;
}

export function ConfigBackupModal({
  open,
  onClose,
  flowLibrary,
  templateLibrary,
  registry = defaultSectionRegistry,
  readDraft = loadDraft,
}: ConfigBackupModalProps) {
  const activeRegistry = useMemo<ConfigSectionRegistry>(
    () => ({
      ...registry,
      draft: {
        ...registry.draft,
        read: async () => readDraft(),
      },
    }),
    [readDraft, registry],
  );
  const [selected, setSelected] = useState<BackupSection[]>([]);
  const [summaries, setSummaries] = useState<Partial<Record<BackupSection, SectionSummary>>>({});
  const [loading, setLoading] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const [downloadError, setDownloadError] = useState<string>();

  const availableSections = useMemo(
    () => BACKUP_SECTIONS.filter((section) => {
      if (section === 'flows') return flowLibrary === true;
      if (section === 'actionTemplates' || section === 'listenTemplates') {
        return templateLibrary === true;
      }
      return true;
    }),
    [flowLibrary, templateLibrary],
  );

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setSelected(availableSections);
    setDownloadError(undefined);
    setLoading(true);

    void Promise.all(
      availableSections.map(async (section) => {
        const adapter = runtimeAdapter(activeRegistry, section);
        try {
          const value = await adapter.read();
          return [
            section,
            {
              count: adapter.count(value),
              bytes: measureValue(value),
            },
          ] as const;
        } catch (error) {
          return [
            section,
            {
              count: 0,
              bytes: 0,
              error: error instanceof Error ? error.message : String(error),
            },
          ] as const;
        }
      }),
    ).then((entries) => {
      if (cancelled) return;
      setSummaries(Object.fromEntries(entries));
      setLoading(false);
    });

    return () => {
      cancelled = true;
    };
  }, [activeRegistry, availableSections, open]);

  const selectSection = (section: BackupSection, checked: boolean) => {
    setSelected((current) =>
      checked
        ? BACKUP_SECTIONS.filter(
            (candidate) => candidate === section || current.includes(candidate),
          )
        : current.filter((candidate) => candidate !== section),
    );
  };

  const handleDownload = async () => {
    setDownloading(true);
    setDownloadError(undefined);
    try {
      const bundle = await createBackupBundle(selected, activeRegistry);
      downloadBackupBundle(bundle);
    } catch (error) {
      setDownloadError(error instanceof Error ? error.message : String(error));
    } finally {
      setDownloading(false);
    }
  };

  const totalBytes = selected.reduce(
    (total, section) => total + (summaries[section]?.bytes ?? 0),
    0,
  );

  return (
    <Modal
      open={open}
      title="备份配置"
      width={600}
      onCancel={onClose}
      footer={[
        <Button key="cancel" onClick={onClose}>
          取消
        </Button>,
        <Button
          key="download"
          type="primary"
          icon={<DownloadOutlined />}
          disabled={selected.length === 0}
          loading={downloading}
          onClick={() => void handleDownload()}
        >
          下载备份
        </Button>,
      ]}
    >
      <div className="config-backup-modal__summary">
        <Typography.Text type="secondary">
          已选择 {selected.length} 个分区，估算 {formatBytes(totalBytes)}
        </Typography.Text>
        <div className="config-backup-modal__actions">
          <Button type="link" size="small" onClick={() => setSelected(availableSections)}>
            全选
          </Button>
          <Button type="link" size="small" onClick={() => setSelected([])}>
            清空
          </Button>
        </div>
      </div>

      {downloadError && (
        <Alert
          type="error"
          showIcon
          message="备份未生成"
          description={downloadError}
          className="config-backup-modal__alert"
        />
      )}

      <Spin spinning={loading}>
        <div className="config-backup-modal__groups">
          {SECTION_GROUPS.map((group) => (
            <section key={group.label} aria-label={group.label}>
              <Typography.Text strong>{group.label}</Typography.Text>
              <div className="config-backup-modal__section-list">
                {group.sections.map((section) => {
                  const adapter = runtimeAdapter(activeRegistry, section);
                  const summary = summaries[section];
                  const flowDisabled = section === 'flows' && flowLibrary !== true;
                  const templateDisabled =
                    (section === 'actionTemplates' || section === 'listenTemplates') &&
                    templateLibrary !== true;
                  const disabled = flowDisabled || templateDisabled;
                  const unavailableText = flowDisabled
                    ? flowLibrary === undefined
                      ? '正在检查流程库'
                      : '服务器未启用流程库'
                    : templateDisabled
                      ? templateLibrary === undefined
                        ? '正在检查共享模板库'
                        : '服务器未启用共享模板库'
                      : undefined;
                  return (
                    <div key={section} className="config-backup-modal__section-row">
                      <Checkbox
                        checked={selected.includes(section)}
                        disabled={disabled}
                        onChange={(event) => selectSection(section, event.target.checked)}
                      >
                        {adapter.label}
                      </Checkbox>
                      <Typography.Text
                        type={summary?.error || disabled ? 'danger' : 'secondary'}
                        className="config-backup-modal__section-status"
                      >
                        {unavailableText
                          ? unavailableText
                          : summary?.error
                            ? `读取失败：${summary.error}`
                            : summary
                              ? `${summary.count} 项 · ${formatBytes(summary.bytes)}`
                              : '读取中'}
                      </Typography.Text>
                    </div>
                  );
                })}
              </div>
            </section>
          ))}
        </div>
      </Spin>

      <Typography.Paragraph type="secondary" className="config-backup-modal__notice">
        备份文件可能包含业务协议、脚本和笔记内容，请妥善保管；不包含服务器连接、数据库凭据、运行历史和界面偏好。
      </Typography.Paragraph>
    </Modal>
  );
}
