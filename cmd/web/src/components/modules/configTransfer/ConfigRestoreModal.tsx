import { ImportOutlined } from '@ant-design/icons';
import {
  Alert,
  App as AntApp,
  Button,
  Checkbox,
  Modal,
  Radio,
  Segmented,
  Spin,
  Typography,
} from 'antd';
import { useEffect, useMemo, useRef, useState } from 'react';

import type { RuntimeMode } from '@/services/runtimeStore';
import {
  assertBackupFileSize,
  parseBackupWithRegistry,
} from '@/services/configTransfer/backupCodec';
import {
  defaultSectionRegistry,
  type ConfigSectionRegistry,
} from '@/services/configTransfer/sectionRegistry';
import {
  executeRestorePlan,
  preflightRestore,
  resolveRestorePlanConflicts,
  RestoreExecutionError,
} from '@/services/configTransfer/restoreCoordinator';
import {
  BACKUP_SECTIONS,
  type BackupSection,
  type ConfigBackupBundle,
  type ConflictChoice,
  type MergeConflictPolicy,
  type RestoreMode,
  type RestorePlan,
  type RestoreResult as RestoreResultValue,
} from '@/services/configTransfer/types';
import { ConflictResolutionView } from './ConflictResolutionView';
import { RestoreResult } from './RestoreResult';
import './ConfigRestoreModal.css';

export interface ConfigRestoreServices {
  assertFileSize: (file: Pick<File, 'size'>) => void;
  parse: (text: string, registry: ConfigSectionRegistry) => ConfigBackupBundle;
  preflight: (
    bundle: ConfigBackupBundle,
    selectedSections: readonly BackupSection[],
    mode: RestoreMode,
    policy: MergeConflictPolicy,
  ) => Promise<RestorePlan>;
  resolve: (plan: RestorePlan, choices: Readonly<Record<string, ConflictChoice>>) => RestorePlan;
  execute: (plan: RestorePlan) => Promise<RestoreResultValue>;
}

const DEFAULT_SERVICES: ConfigRestoreServices = {
  assertFileSize: assertBackupFileSize,
  parse: parseBackupWithRegistry,
  preflight: preflightRestore,
  resolve: resolveRestorePlanConflicts,
  execute: executeRestorePlan,
};

function runtimeAdapter(registry: ConfigSectionRegistry, section: BackupSection) {
  return registry[section] as unknown as { label: string };
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export interface ConfigRestoreModalProps {
  open: boolean;
  runtimeMode: RuntimeMode;
  onClose: () => void;
  flowLibrary: boolean | undefined;
  registry?: ConfigSectionRegistry;
  services?: ConfigRestoreServices;
  confirmRestore?: (mode: RestoreMode) => Promise<boolean>;
}

export function ConfigRestoreModal({
  open,
  runtimeMode,
  onClose,
  flowLibrary,
  registry = defaultSectionRegistry,
  services,
  confirmRestore,
}: ConfigRestoreModalProps) {
  const { modal } = AntApp.useApp();
  const fileInputRef = useRef<HTMLInputElement>(null);
  const activeServices = useMemo(() => services ?? DEFAULT_SERVICES, [services]);
  const [fileName, setFileName] = useState('');
  const [bundle, setBundle] = useState<ConfigBackupBundle>();
  const [selected, setSelected] = useState<BackupSection[]>([]);
  const [restoreMode, setRestoreMode] = useState<RestoreMode>('merge');
  const [conflictPolicy, setConflictPolicy] = useState<MergeConflictPolicy>('prompt');
  const [plan, setPlan] = useState<RestorePlan>();
  const [choices, setChoices] = useState<Record<string, ConflictChoice>>({});
  const [fileError, setFileError] = useState<string>();
  const [planningError, setPlanningError] = useState<string>();
  const [planning, setPlanning] = useState(false);
  const [executing, setExecuting] = useState(false);
  const [result, setResult] = useState<RestoreResultValue>();
  const [executionError, setExecutionError] = useState<string>();

  const sectionLabel = (section: BackupSection) => runtimeAdapter(registry, section).label;
  const resolvedPreview = useMemo(() => {
    if (!plan || plan.conflicts.length === 0) return plan;
    try {
      return activeServices.resolve(plan, choices);
    } catch {
      return plan;
    }
  }, [activeServices, choices, plan]);

  useEffect(() => {
    if (!open) return;
    setFileName('');
    setBundle(undefined);
    setSelected([]);
    setRestoreMode('merge');
    setConflictPolicy('prompt');
    setPlan(undefined);
    setChoices({});
    setFileError(undefined);
    setPlanningError(undefined);
    setResult(undefined);
    setExecutionError(undefined);
  }, [open]);

  useEffect(() => {
    if (!bundle) return;
    setSelected(
      bundle.manifest.includedSections.filter(
        (section) => section !== 'flows' || flowLibrary === true,
      ),
    );
  }, [bundle, flowLibrary]);

  useEffect(() => {
    if (!bundle || selected.length === 0) {
      setPlan(undefined);
      setPlanning(false);
      setPlanningError(undefined);
      return;
    }
    let cancelled = false;
    setPlanning(true);
    setPlanningError(undefined);
    setChoices({});
    const policy = restoreMode === 'replace' ? 'overwrite' : conflictPolicy;
    void activeServices
      .preflight(bundle, selected, restoreMode, policy)
      .then((nextPlan) => {
        if (!cancelled) setPlan(nextPlan);
      })
      .catch((error) => {
        if (!cancelled) {
          setPlan(undefined);
          setPlanningError(errorMessage(error));
        }
      })
      .finally(() => {
        if (!cancelled) setPlanning(false);
      });
    return () => {
      cancelled = true;
    };
  }, [activeServices, bundle, conflictPolicy, restoreMode, selected]);

  const loadFile = async (file: File) => {
    setFileError(undefined);
    setPlanningError(undefined);
    setResult(undefined);
    setBundle(undefined);
    setSelected([]);
    setPlan(undefined);
    try {
      activeServices.assertFileSize(file);
      const parsed = activeServices.parse(await file.text(), registry);
      setFileName(file.name);
      setBundle(parsed);
    } catch (error) {
      setFileName(file.name);
      setFileError(errorMessage(error));
    }
  };

  const toggleSection = (section: BackupSection, checked: boolean) => {
    setSelected((current) =>
      checked
        ? BACKUP_SECTIONS.filter(
            (candidate) => candidate === section || current.includes(candidate),
          )
        : current.filter((candidate) => candidate !== section),
    );
  };

  const askForConfirmation = async (): Promise<boolean> => {
    if (confirmRestore) return confirmRestore(restoreMode);
    return new Promise((resolve) => {
      modal.confirm({
        title: restoreMode === 'replace' ? '确认完整恢复' : '确认合并导入',
        content:
          restoreMode === 'replace'
            ? '选中内容将与备份保持一致，备份中不存在的配置会被删除。'
            : '将按照预览结果合并选中的配置内容。',
        okText: '确认恢复',
        cancelText: '取消',
        onOk: () => resolve(true),
        onCancel: () => resolve(false),
      });
    });
  };

  const handleExecute = async () => {
    if (!resolvedPreview) return;
    setExecutionError(undefined);
    const finalPlan = resolvedPreview;
    try {
      if (finalPlan.conflicts.length > 0) throw new Error('仍有重复内容未处理');
      if (!(await askForConfirmation())) return;
      setExecuting(true);
      const nextResult = await activeServices.execute(finalPlan);
      setResult(nextResult);
    } catch (error) {
      const pendingSections = error instanceof RestoreExecutionError ? error.pendingSections : [];
      setExecutionError(errorMessage(error));
      setResult({
        ok: false,
        stats: finalPlan.stats,
        pendingSections,
        rolledBack: error instanceof RestoreExecutionError && pendingSections.length === 0,
      });
    } finally {
      setExecuting(false);
    }
  };

  const choicesComplete = resolvedPreview?.conflicts.length === 0;
  const executeDisabled =
    runtimeMode !== 'edit' ||
    !plan ||
    planning ||
    selected.length === 0 ||
    (plan.conflicts.length > 0 && !choicesComplete);
  const availableIncluded =
    bundle?.manifest.includedSections.filter(
      (section) => section !== 'flows' || flowLibrary === true,
    ) ?? [];

  const footer = result
    ? [
        <Button key="close" type="primary" onClick={onClose}>
          关闭
        </Button>,
      ]
    : [
        <Button key="cancel" disabled={executing} onClick={onClose}>
          取消
        </Button>,
        <Button
          key="restore"
          type="primary"
          icon={<ImportOutlined />}
          disabled={executeDisabled}
          loading={executing}
          onClick={() => void handleExecute()}
        >
          开始恢复
        </Button>,
      ];

  return (
    <Modal
      open={open}
      title="恢复配置"
      className="config-restore-modal"
      width={760}
      closable={!executing}
      keyboard={!executing}
      maskClosable={!executing}
      onCancel={() => {
        if (!executing) onClose();
      }}
      footer={footer}
      destroyOnHidden
    >
      {result ? (
        <RestoreResult result={result} errorMessage={executionError} sectionLabel={sectionLabel} />
      ) : (
        <>
          <div className="config-restore__file-picker">
            <Button icon={<ImportOutlined />} onClick={() => fileInputRef.current?.click()}>
              选择备份文件
            </Button>
            <Typography.Text type="secondary">{fileName || '尚未选择文件'}</Typography.Text>
            <input
              ref={fileInputRef}
              type="file"
              accept="application/json,.json"
              aria-label="选择配置备份文件"
              hidden
              onChange={(event) => {
                const file = event.target.files?.[0];
                if (file) void loadFile(file);
                event.target.value = '';
              }}
            />
          </div>

          {fileError && (
            <Alert
              type="error"
              showIcon
              message="无法读取备份"
              description={fileError}
              className="config-restore__alert"
            />
          )}

          {bundle && (
            <>
              <div className="config-restore__header">
                <Typography.Text>格式版本 {bundle.schemaVersion}</Typography.Text>
                <Typography.Text type="secondary">
                  导出时间 {new Date(bundle.exportedAt).toLocaleString()}
                </Typography.Text>
              </div>

              <div className="config-restore__selection-toolbar">
                <Typography.Text strong>选择恢复内容</Typography.Text>
                <div>
                  <Button type="link" size="small" onClick={() => setSelected(availableIncluded)}>
                    全选
                  </Button>
                  <Button type="link" size="small" onClick={() => setSelected([])}>
                    清空
                  </Button>
                </div>
              </div>
              <div className="config-restore__selection-list">
                {bundle.manifest.includedSections.map((section) => {
                  const flowDisabled = section === 'flows' && flowLibrary !== true;
                  return (
                    <div key={section} className="config-restore__selection-row">
                      <Checkbox
                        checked={selected.includes(section)}
                        disabled={flowDisabled}
                        onChange={(event) => toggleSection(section, event.target.checked)}
                      >
                        {sectionLabel(section)}
                      </Checkbox>
                      <Typography.Text type={flowDisabled ? 'danger' : 'secondary'}>
                        {flowDisabled
                          ? flowLibrary === undefined
                            ? '正在检查流程库'
                            : '服务器未启用流程库'
                          : `${bundle.manifest.counts[section] ?? 0} 项`}
                      </Typography.Text>
                    </div>
                  );
                })}
              </div>

              <div className="config-restore__mode-row">
                <Typography.Text strong>恢复方式</Typography.Text>
                <Segmented<RestoreMode>
                  value={restoreMode}
                  options={[
                    { label: '合并导入', value: 'merge' },
                    { label: '完整恢复', value: 'replace' },
                  ]}
                  onChange={setRestoreMode}
                />
              </div>

              {restoreMode === 'merge' ? (
                <div className="config-restore__mode-row">
                  <Typography.Text strong>重复内容</Typography.Text>
                  <Radio.Group
                    value={conflictPolicy}
                    onChange={(event) =>
                      setConflictPolicy(event.target.value as MergeConflictPolicy)
                    }
                  >
                    <Radio value="overwrite">全部覆盖</Radio>
                    <Radio value="prompt">逐个处理</Radio>
                    <Radio value="skip">全部忽略</Radio>
                  </Radio.Group>
                </div>
              ) : (
                <Alert
                  type="warning"
                  showIcon
                  message="完整恢复会删除选中内容中备份不存在的配置"
                  className="config-restore__alert"
                />
              )}

              {runtimeMode !== 'edit' && (
                <Alert
                  type="warning"
                  showIcon
                  message="请先返回编辑模式"
                  className="config-restore__alert"
                />
              )}

              {planningError && (
                <Alert
                  type="error"
                  showIcon
                  message="无法生成恢复预览"
                  description={planningError}
                  className="config-restore__alert"
                />
              )}

              <Spin spinning={planning}>
                {plan && (
                  <div className="config-restore__preview">
                    <RestoreResult
                      preview
                      result={{
                        ok: true,
                        stats: resolvedPreview?.stats ?? plan.stats,
                        pendingSections: [],
                      }}
                      sectionLabel={sectionLabel}
                    />
                  </div>
                )}
                {plan && plan.conflicts.length > 0 && (
                  <ConflictResolutionView
                    conflicts={plan.conflicts}
                    choices={choices}
                    onChange={setChoices}
                    sectionLabel={sectionLabel}
                  />
                )}
              </Spin>
            </>
          )}
        </>
      )}
    </Modal>
  );
}
