import { Alert, Result, Typography } from 'antd';

import {
  BACKUP_SECTIONS,
  type BackupSection,
  type RestoreResult as RestoreResultValue,
} from '@/services/configTransfer/types';
import './ConfigRestoreModal.css';

export interface RestoreResultProps {
  result: RestoreResultValue;
  preview?: boolean;
  errorMessage?: string;
  sectionLabel?: (section: BackupSection) => string;
}

export function RestoreResult({
  result,
  preview = false,
  errorMessage,
  sectionLabel = (section) => section,
}: RestoreResultProps) {
  const sections = BACKUP_SECTIONS.filter((section) => result.stats[section] !== undefined);
  const status = result.ok ? 'success' : result.pendingSections.length > 0 ? 'warning' : 'error';
  const title = preview
    ? '预计变化'
    : result.ok
      ? '恢复完成'
      : result.pendingSections.length > 0
        ? '配置恢复未完成'
        : result.rolledBack
          ? '恢复失败，已撤销本次修改'
          : '恢复失败，未修改配置';

  return (
    <div className="config-restore__result">
      {!preview && <Result status={status} title={title} subTitle={errorMessage} />}
      {preview && <Typography.Title level={5}>{title}</Typography.Title>}
      <div className="config-restore__stats">
        {sections.map((section) => {
          const stats = result.stats[section];
          if (!stats) return null;
          return (
            <div key={section} className="config-restore__stats-row">
              <Typography.Text strong>{sectionLabel(section)}</Typography.Text>
              <div className="config-restore__stats-values">
                <span>新增 {stats.added}</span>
                <span>覆盖 {stats.overwritten}</span>
                <span>删除 {stats.deleted}</span>
                <span>忽略 {stats.skipped}</span>
                <span>保留两份 {stats.copied}</span>
              </div>
            </div>
          );
        })}
      </div>
      {result.pendingSections.length > 0 && (
        <Alert
          type="warning"
          showIcon
          message="仍有内容等待撤销"
          description={result.pendingSections.map(sectionLabel).join('、')}
        />
      )}
    </div>
  );
}
