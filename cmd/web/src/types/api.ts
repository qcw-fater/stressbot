/**
 * 与 Admin（默认 :8080）交互的全量 TS 类型定义。
 *
 * 单一事实源：docs/api-monitor.md §11；本文件结构与该文档保持完全一致，新增字段两边同步。
 */

// === 基础枚举 ===
export type TaskState = 'pending' | 'starting' | 'running' | 'stopping' | 'stopped' | 'failed';
export type AgentStatus = 'idle' | 'busy' | 'unhealthy' | 'offline' | 'upgrading';
export type TaskResult = 'completed' | 'stopped' | 'failed';
export type UpgradePhase = 'queued' | 'sent' | 'upgrading' | 'success' | 'failed';
export type OS = 'windows' | 'linux' | 'darwin';
export type Arch = 'amd64' | 'arm64';

// === 通用错误 ===
export interface ApiErrorBody {
  code: string;
  message: string;
  details?: Record<string, unknown>;
}

// === Task ===
export interface TaskBrief {
  id: string;
  name: string;
  state: TaskState;
  totalBots: number;
  agentCount: number;
  createdAt: string;
  startedAt?: string;
  stoppedAt?: string;
}

export interface RobotConfig {
  authAddr: string;
  concurrency: number;
  timeoutSec: number;
}

export interface TaskConfig {
  robotConfig: RobotConfig;
  deadline?: string;
  flowFiles: string[];
}

export interface Assignment {
  taskId: string;
  agentId: string;
  agentName: string;
  startNumber: number;
  totalBots: number;
}

export interface TaskCompletionReport {
  agentId: string;
  taskId: string;
  result: TaskResult;
  errorMsg?: string;
  finishedAt: string;
}

export interface TaskDetail extends TaskBrief {
  config: TaskConfig;
  assignments: Assignment[];
  errorMsg?: string;
  reports?: Record<string, TaskCompletionReport>;
}

export interface TasksListResponse {
  total: number;
  items: TaskBrief[];
}

export interface CreateTaskResponse {
  id: string;
}

export interface StartTaskResponse {
  taskId: string;
  assignments: Assignment[];
}

// === Agent ===
export interface StaticInfo {
  hostname: string;
  os: OS;
  arch: Arch;
  numCpu: number;
  memTotalMB: number;
  goVersion: string;
  kernelVer: string;
  startedAt: string;
}

export interface AgentBrief {
  agentId: string;
  name: string;
  address: string;
  appVersion: string;
  maxBots: number;
  status: AgentStatus;
  currentTaskId?: string;
  currentBots: number;
  staticInfo: StaticInfo;
  lastHeartbeatAt: string;
  stressUpdatedAt?: string;
  systemUpdatedAt?: string;
  cpuPercent?: number;
  memPercent?: number;
  numGoroutine?: number;
}

export interface AgentDetail extends AgentBrief {
  latestSystem?: SystemSnapshot;
}

export interface AgentsListResponse {
  items: AgentBrief[];
}

// === Stress 指标 ===
export interface RobotsView {
  started: number;
  running: number;
  stopped: number;
  errored: number;
}

export interface ConnectionsView {
  established: number;
  failed: number;
  dropped: number;
}

export interface BandwidthView {
  totalSendBytes: number;
  totalRecvBytes: number;
  sendMBps: number;
  recvMBps: number;
}

export interface HistogramView {
  count: number;
  minMs: number;
  maxMs: number;
  avgMs: number;
  p50Ms: number;
  p90Ms: number;
  p95Ms: number;
  p99Ms: number;
}

export interface ErrorBucket {
  msg: string;
  count: number;
}

export interface ActionMetric {
  name: string;
  sampleCount: number;
  successCount: number;
  failureCount: number;
  timeoutCount: number;
  skippedCount: number;
  executing: number;
  successRate: number;
  apdex: number;
  avgQps: number;
  avgSendBytes: number;
  avgRecvBytes: number;
  timeoutAvgMs: number;
  latency: HistogramView;
  errors?: ErrorBucket[];
}

export interface ClusterInfo {
  agentCount: number;
  agentIds: string[];
  staleAgentIds: string[];
}

export interface StressSnapshot {
  timestamp: string;
  uptimeSeconds: number;
  totalActions: number;
  apdexT: number;
  robots: RobotsView;
  connections: ConnectionsView;
  bandwidth: BandwidthView;
  actions: ActionMetric[];
  clusterInfo?: ClusterInfo;
}

export interface PerAgentMetricsItem {
  agentId: string;
  agentName: string;
  snapshot: StressSnapshot;
  updatedAt: string;
}

export interface PerAgentMetrics {
  items: PerAgentMetricsItem[];
}

// === System 指标 ===
export interface SystemSnapshot {
  timestamp: string;
  cpuPercent: number;
  cpuPerCore: number[];
  loadAvg1: number;
  loadAvg5: number;
  loadAvg15: number;
  memTotalMB: number;
  memUsedMB: number;
  memPercent: number;
  swapUsedMB: number;
  processRssMB: number;
  processHeapMB: number;
  processSysMB: number;
  numGoroutine: number;
  numThread: number;
  numFd: number;
  netSendKBps: number;
  netRecvKBps: number;
  gcCount: number;
  gcPauseAvgMs: number;
}

export interface ClusterSystemSnapshot {
  timestamp: string;
  agentCount: number;
  onlineCount: number;
  offlineCount: number;
  upgradingCount: number;
  totalMemMB: number;
  usedMemMB: number;
  avgCpuPercent: number;
  maxCpuPercent: number;
  totalNetSendKBps: number;
  totalNetRecvKBps: number;
  totalGoroutines: number;
  totalThreads: number;
  totalFds: number;
  hotAgentId?: string;
  hotAgentName?: string;
}

export interface PerAgentSystemItem {
  agentId: string;
  agentName: string;
  status: AgentStatus;
  snapshot: SystemSnapshot;
  updatedAt: string;
  isStale: boolean;
}

export interface PerAgentSystem {
  items: PerAgentSystemItem[];
}

// === 二进制 / 升级 ===
export interface BinaryMeta {
  version: string;
  filename: string;
  os: OS;
  arch: Arch;
  sha256: string;
  sizeBytes: number;
  uploadedAt: string;
}

export interface BinariesListResponse {
  items: BinaryMeta[];
}

export interface AgentUpgradeState {
  phase: UpgradePhase;
  startedAt?: string;
  error?: string;
}

export interface UpgradeStatus {
  inProgress: boolean;
  version: string;
  startedAt?: string;
  total: number;
  completed: number;
  failed: number;
  currentAgentId?: string;
  perAgent: Record<string, AgentUpgradeState>;
}

export interface UpgradeRequest {
  version: string;
}

export interface UpgradeResponse {
  agentId: string;
  message: string;
}

export interface UpgradeAllRequest {
  version: string;
}

export interface UpgradeAllResponse {
  total: number;
  message: string;
}

// === 任务单例冲突错误 details ===
export interface TaskConflictDetails {
  activeTaskId: string;
  activeName: string;
  activeState: TaskState;
  startedAt: string;
}

// === History ===
export interface ConfigSummary {
  authAddr: string;
  concurrency: number;
  timeoutSec: number;
  flowSizeKB: number;
  protoCount: number;
  scriptCount: number;
}

export interface HistoryRecord {
  id: string;
  name: string;
  state: 'stopped' | 'failed';
  totalBots: number;
  agentCount: number;
  createdAt: string;
  startedAt?: string;
  stoppedAt?: string;
  durationSec: number;
  errorMsg?: string;
  starred: boolean;
  tags: string[];
  note?: string;
  configSummary: ConfigSummary;
}

export interface HistoryListResponse {
  total: number;
  items: HistoryRecord[];
}

export interface HistoryAgentReport {
  agentId: string;
  agentName: string;
  result: TaskResult;
  errorMsg?: string;
  finishedAt: string;
  finalSnapshot: StressSnapshot;
}

export interface HistoryDetail extends HistoryRecord {
  assignments: Array<{
    taskId: string;
    agentId: string;
    startNumber: number;
    totalBots: number;
  }>;
  agentReports: HistoryAgentReport[];
  finalSnapshot: StressSnapshot;
  finalSystem: ClusterSystemSnapshot;
}

export interface HistoryFilter {
  state?: 'stopped' | 'failed';
  startedAfter?: string;
  startedBefore?: string;
  tags?: string[];
  tagsAll?: string[];
  starred?: boolean;
  search?: string;
  orderBy?: string;
  limit?: number;
  offset?: number;
}

export interface UpdateHistoryRequest {
  starred?: boolean;
  tags?: string[];
  note?: string;
}

export interface HistoryTagsResponse {
  tags: string[];
}

export interface TimeseriesPoint {
  taskId: string;
  sampledAt: string;
  elapsedSec: number;
  dataType: 'stress' | 'system';
  snapshot: StressSnapshot | ClusterSystemSnapshot;
}

export interface TimeseriesResponse {
  taskId: string;
  stress: TimeseriesPoint[];
  system: TimeseriesPoint[];
}

export interface HistoryConfigArchive {
  taskId: string;
  name: string;
  totalBots: number;
  robotConfig: RobotConfig;
  flowJson: unknown;
  headerJson: unknown;
  protoFiles: Record<string, string>;
  scripts: Record<string, string>;
}

export interface HistoryCloneRequest {
  name?: string;
}

export interface HistoryCompareResponse {
  tasks: HistoryDetail[];
  diff: {
    actions: Record<string, number[]>;
  };
}
