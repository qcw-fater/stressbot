/**
 * services 层统一出口。
 *
 * 通过 `import { listTasks, useRuntimeStore } from '@/services'` 一处引入；
 * 浏览器 console 也可以 `await import('/src/services').then(m => m.listTasks())` 手动调用。
 */

export * from './api';
export * as tasksApi from './tasksApi';
export * as agentsApi from './agentsApi';
export * as metricsApi from './metricsApi';
export * as historyApi from './historyApi';
export * as logsApi from './logsApi';
export * as capabilitiesApi from './capabilitiesApi';
export type { CapabilitiesResponse } from './capabilitiesApi';

export { usePolling } from './usePolling';
export type { UsePollingOptions } from './usePolling';

export {
  useRuntimeStore,
  pollingPolicy,
} from './runtimeStore';
export type { RuntimeMode, RuntimeState } from './runtimeStore';

export {
  buildNodeMetricsMap,
  makeMetricsProvider,
  classifyApdex,
  computeWeightedMetrics,
} from './metricsBinding';
export type { ApdexLevel, FlowSlice, NodeMetricsMap } from './metricsBinding';

export {
  showApiError,
  registerTaskConflictHandler,
  setMessageApi,
} from './errorHandler';
export type { TaskConflictHandler } from './errorHandler';

export * as resourcesStore from './resourcesStore';
export type { ResourceFile } from './resourcesStore';

export * from './flowsApi';
export * from './errorMapValidation';
export * from './configTransfer/types';
export * from './configTransfer/backupCodec';
export * from './configTransfer/restorePlanner';
export * from './configTransfer/sectionRegistry';
export * from './configTransfer/restoreCoordinator';
export * from './configTransfer/recoveryJournal';

export {
  collectFlowScriptNames,
  syncFlowScriptsToIdb,
} from './scriptSync';
export type { ScriptSyncResult } from './scriptSync';

export {
  collectFlowCodecConnections,
  connNameToCodecFileName,
  codecFileNameToConnName,
  findMissingCodecConnections,
} from './taskResourceDiff';

export {
  startTask,
  stopTask,
  attachToActive,
  detachToEditWithRestore,
  detachFromActiveWithRestore,
  restoreStashedDraft,
  hasStashedDraft,
} from './taskActions';
export type { StartTaskOptions } from './taskActions';
