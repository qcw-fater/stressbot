import { Alert, Button } from 'antd';
import { useCallback, useEffect, useState } from 'react';

import { recoverPendingRestore } from '@/services/configTransfer/restoreCoordinator';
import type { BackupSection, RestoreResult } from '@/services/configTransfer/types';
import './RecoveryGuard.css';

const SECTION_LABELS: Record<BackupSection, string> = {
  flows: '已保存流程',
  draft: '当前编辑稿',
  protoFiles: 'Proto 文件',
  luaFiles: '脚本文件',
  codecFiles: '协议配置',
  errorMap: '错误码',
  actionTemplates: '动作模板',
  listenTemplates: '监听模板',
  notepadFiles: '记事本文件',
};

interface RecoveryIssue {
  pendingSections: BackupSection[];
  error?: string;
}

export interface RecoveryGuardProps {
  recover?: () => Promise<RestoreResult>;
}

export function RecoveryGuard({ recover = recoverPendingRestore }: RecoveryGuardProps) {
  const [issue, setIssue] = useState<RecoveryIssue>();
  const [checking, setChecking] = useState(false);

  const retry = useCallback(async () => {
    setChecking(true);
    try {
      const result = await recover();
      if (result.ok && result.pendingSections.length === 0) {
        setIssue(undefined);
      } else {
        setIssue({ pendingSections: result.pendingSections });
      }
    } catch (error) {
      setIssue({
        pendingSections: [],
        error: error instanceof Error ? error.message : String(error),
      });
    } finally {
      setChecking(false);
    }
  }, [recover]);

  useEffect(() => {
    void retry();
  }, [retry]);

  if (!issue) return null;

  const pending = issue.pendingSections.map((section) => SECTION_LABELS[section]).join('、');
  const description = issue.error
    ? `自动撤销失败：${issue.error}`
    : pending
      ? `以下内容仍需撤销：${pending}`
      : '部分配置仍需撤销，请重试。';

  return (
    <Alert
      className="config-recovery-guard"
      type="warning"
      showIcon
      banner
      message="配置恢复未完成"
      description={description}
      action={
        <Button size="small" type="link" loading={checking} onClick={() => void retry()}>
          重试恢复
        </Button>
      }
    />
  );
}
