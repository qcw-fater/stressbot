/**
 * 与后端服务器交互的全量 TS 类型定义。
 *
 * 单一事实源：docs/api-monitor.md §12；本文件结构与该文档保持完全一致，新增字段两边同步。
 */

// === 基础枚举 ===
export type TaskState = 'pending' | 'starting' | 'running' | 'stopping' | 'stopped' | 'failed';
export type AgentStatus = 'idle' | 'busy' | 'unhealthy' | 'offline';
export type TaskResult = 'completed' | 'stopped' | 'failed';
// OS / Arch 用于 StaticInfo（节点自报），保留仅用于展示，不再参与二进制平台匹配。
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
  activeAgentCount: number;
  createdAt: string;
  startedAt?: string;
  stoppedAt?: string;
}

export type LogLevel = 'debug' | 'info' | 'warn' | 'error';

/**
 * 任务级运行时配置。
 *
 * 字段分层：
 * - 必填（concurrency/timeoutSec）：每个任务一定要的核心参数；
 * - 可选（其余）：留空时由 admin 用合理默认值填充并下发给 agent。
 *
 * 协议约定：超时类用 int 秒数（前端表单友好），admin 转 duration 字符串给 agent。
 */
export interface RobotConfig {
  // ── 必填 ──
  concurrency: number;
  /** TCP/请求超时秒数（兜底）。也兼容旧字段语义。 */
  timeoutSec: number;

  // ── 业务可变（影响每任务的鉴权/连接行为）──
  /** 账号前缀，用于区分压测批次，默认 "bot_" */
  accountPrefix?: string;
  /**
   * 账号编号起点；admin 在分配时把它作为各 agent cursor 的起点，
   * 即第 N 个机器人的账号 = `accountPrefix + (startNumber + N)`。
   * 用法：已有 bot_0~bot_99 时设 100 可避免账号撞车。默认 0。
   */
  startNumber?: number;
  /** 主连接服务名，默认 "logic"；不同游戏命名不同 */
  mainService?: string;
  /**
   * State 扩展字段（version / channel / platform 等）。
   * 在 lua 脚本里通过 `robot.get("version")` 取到。
   * 不配置则 lua 取到 nil 走脚本兜底默认值，可能导致鉴权失败。
   */
  stateExtra?: Record<string, string>;

  // ── 性能/超时（通常用默认值即可）──
  /** 心跳间隔秒数，默认 5 */
  heartbeatSec?: number;
  /** HTTP 请求超时秒数，默认 10 */
  httpTimeoutSec?: number;
  /** Apdex 满意阈值毫秒，默认 100 */
  apdexT?: number;

  // ── 日志 ──
  /**
   * 任务期临时切换 节点进程日志等级；省略 = 沿用 Agent 启动配置（通常为 info）。
   * 任务结束后 Agent 会自动恢复为原等级，不影响后续任务。
   */
  logLevel?: LogLevel;
  /** 调试模式：单节点分配，历史中以系统级调试徽标展示 */
  debugMode?: boolean;
  /** 渐进式加压配置，不配时一次性创建全部机器人 */
  rampUp?: RampUpConfig;
}

/** 渐进式加压阶段配置。 */
export interface RampUpStage {
  /** 本阶段新增 bot 数（增量值），各阶段之和必须等于 totalBots */
  count: number;
  /** 覆盖全局并发数，0 或空则用全局值 */
  concurrency?: number;
  /** 阶段间等待秒数，最后阶段可不填 */
  holdSec?: number;
  /** 开始本阶段前清空所有已有机器人 */
  reset?: boolean;
}

/** 渐进式加压配置。 */
export interface RampUpConfig {
  stages: RampUpStage[];
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
  cleanupStatus?: CleanupStatus;
}

/**
 * 资源清理状态。
 *
 * - ok：清理完成，Lua 运行时已归还。
 * - partial：部分清理异常。
 * - timeout：清理超时，部分 Lua 运行时已隔离未归还。
 * - unknown：节点未上报或停止等待超时，清理状态未知。
 */
export type CleanupState = 'ok' | 'partial' | 'timeout' | 'unknown';

export interface CleanupIssue {
  robotId?: number;
  account?: string;
  phase?: string;
  waitDone?: boolean;
  closeAllDone?: boolean;
  message?: string;
}

export interface CleanupStatus {
  status: CleanupState;
  reason?: string;
  message?: string;
  durationMs?: number;
  totalRobots?: number;
  cleanedRobots?: number;
  timeoutRobots?: number;
  luaReturned?: number;
  luaSkipped?: number;
  issues?: CleanupIssue[];
}

export interface TaskDetail extends TaskBrief {
  config: TaskConfig;
  assignments: Assignment[];
  errorMsg?: string;
  reports?: Record<string, TaskCompletionReport>;
  cleanupSummary?: CleanupStatus;
  agentEvents?: AgentEvent[];
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

export interface AgentEvent {
  agentId: string;
  agentName: string;
  /** "offline" | "reconnected" | "deregistered" | "restarted" */
  type: string;
  timestamp: string;
  detail?: string;
}

export interface StaticInfo {
  hostname: string;
  os: OS;
  arch: Arch;
  numCpu: number;
  memTotalBytes: number;
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
  systemStale?: boolean;
  systemSnapshotAgeSeconds?: number | null;
  hostCpuPercent?: number | null;
  hostMemPercent?: number | null;
  processCpuPercent?: number | null;
  processRssBytes?: number | null;
  processGoroutines?: number;
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
  active: number;
  closed: number;
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
  minMs: number | null;
  maxMs: number | null;
  avgMs: number | null;
  p50Ms: number | null;
  p90Ms: number | null;
  p95Ms: number | null;
  p99Ms: number | null;
}

export interface ErrorEntry {
  code: number;
  codeName: string;
  msgs: string[];
  count: number;
}

/** 框架错误码（工具自产，< 100），来自 GET /sbot/api/error-codes */
export interface FrameworkCode {
  code: number;
  name: string;
}

export interface ActionMetric {
  name: string;
  sampleCount: number;
  successCount: number;
  failureCount: number;
  timeoutCount: number;
  canceledCount: number;
  executing: number;
  successRate: number;
  /**
   * 动作分类，决定哪个耗时是它的主指标：
   * networked=往返类(RTT，打 Apdex) / listen=监听类(等待时长，出分布不打分)
   * / send=发送类 / local=本地类。
   */
  kind: ActionKind;
  /** RTT Apdex。仅 kind=networked 有意义，其余类别应显示 — 而非 0 分 */
  rttApdex: number;
  /** RTT Apdex 完整分母，包含无响应帧且按 frustrated 计的失败请求 */
  rttApdexSampleCount: number;
  avgQps: number;
  avgSendBytes: number;
  avgRecvBytes: number;
  timeoutAvgMs: number;
  /** 客户端平均耗时（毫秒），所有结果分支累计 */
  nonRTTAvgMs: number;
  clientCostCount: number;
  buildAvgMs: number;
  encodeAvgMs: number;
  sendAvgMs: number;
  decodeWaitAvgMs: number;
  decodeAvgMs: number;
  dispatchToActionWaitAvgMs: number;
  parseStoreAvgMs: number;
  buildSampleCount: number;
  encodeSampleCount: number;
  sendSampleCount: number;
  decodeWaitSampleCount: number;
  decodeSampleCount: number;
  dispatchWaitSampleCount: number;
  parseStoreSampleCount: number;
  byteSampleCount: number;
  periodQps: number;
  /** 进入 RTT 直方图的样本数。0 表示该 action 没有 request-response RTT，RTT 列应显示 — */
  rttSampleCount: number;
  /** 等待时长可测的监听命中数。0 表示 listenWait 列应显示 — */
  listenWaitSampleCount: number;
  /** 进入总耗时直方图的 action 样本数。0 表示该 action 没有总耗时样本 */
  totalDurationSampleCount: number;
  /** RTT 直方图（WireRTT） */
  rtt: HistogramView;
  /** 监听等待直方图（开始等待 → 帧被内核收到），kind=listen 的主指标 */
  listenWait: HistogramView;
  /** 命中时消息已在队列的次数：等待时长不可测，不进分布 */
  listenReadyCount: number;
  /** 监听超时次数（不进分布，单独成率） */
  listenTimeoutCount: number;
  /** 监听超时率 = 超时 /（命中 + 已就绪 + 超时） */
  listenTimeoutRate: number;
  /** action 总耗时直方图（wallClock）。含施压机排队与故意 sleep，仅作诊断，不用于评分 */
  totalDuration: HistogramView;
  errors?: ErrorEntry[];
}

/** 动作的网络语义分类，由运行期实际发生的行为判定。 */
export type ActionKind = 'networked' | 'listen' | 'send' | 'local';

export type TimingDetailLevel = 'rtt' | 'codec' | 'full';

export interface SnapshotMetricsSummary {
  sampleCount: number;
  successCount: number;
  failureCount: number;
  timeoutCount: number;
  canceledCount: number;
  executing: number;
  successRate: number;
  rttApdex: number;
  rttApdexSampleCount: number;
  rtt: HistogramView;
  listenWait: HistogramView;
  totalDuration: HistogramView;
  nonRTTAvgMs: number;
  clientCostCount: number;
  buildAvgMs: number;
  encodeAvgMs: number;
  sendAvgMs: number;
  decodeWaitAvgMs: number;
  decodeAvgMs: number;
  dispatchToActionWaitAvgMs: number;
  parseStoreAvgMs: number;
  buildSampleCount: number;
  encodeSampleCount: number;
  sendSampleCount: number;
  decodeWaitSampleCount: number;
  decodeSampleCount: number;
  dispatchWaitSampleCount: number;
  parseStoreSampleCount: number;
  avgQps: number;
}

export interface WindowBandwidthView {
  sendBytes: number;
  recvBytes: number;
  sendMBps: number;
  recvMBps: number;
}

export interface ReportWindowView {
  startedAt: string;
  endedAt: string;
  durationSeconds: number;
  expectedIntervalSeconds: number;
  summary: SnapshotMetricsSummary;
  bandwidth: WindowBandwidthView;
  actions: ActionMetric[];
  invalidMetricSamples: number;
}

export interface ClusterInfo {
  agentCount: number;
  agentIds: string[];
  staleAgentIds: string[];
}

export interface StressAggregate {
  snapshot: StressSnapshot;
  reportingAgents: number;
  totalAgents: number;
  offlineAgents: number;
  assignedAgents: number;
  freshAgents: number;
  staleAgents: number;
  coverageRatio: number;
  asOf: string;
}

export interface RampUpSnapshot {
  currentStage: number; // 当前阶段（1-based，0 = 未启用）
  totalStages: number; // 总阶段数（0 = 未启用）
}

export interface StressSnapshot {
  timestamp: string;
  collectionEpoch: number;
  uptimeSeconds: number;
  totalActions: number;
  apdexT: number;
  timingDetail: TimingDetailLevel;
  summary: SnapshotMetricsSummary;
  robots: RobotsView;
  rampUp: RampUpSnapshot;
  connections: ConnectionsView;
  bandwidth: BandwidthView;
  actions: ActionMetric[];
  invalidMetricSamples: number;
  window: ReportWindowView | null;
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
  sequence: number;
  hostCpuPercent: number | null;
  hostMemTotalBytes: number | null;
  hostMemUsedBytes: number | null;
  hostMemPercent: number | null;
  hostNetSendBytesPerSec: number | null;
  hostNetRecvBytesPerSec: number | null;
  processCpuPercent: number | null;
  processRssBytes: number | null;
  processHeapBytes: number;
  processGoroutines: number;
  processThreads: number | null;
  processFds: number | null;
}

export interface AgentSystemBrief {
  agentId: string;
  name: string;
  status: AgentStatus | string;
  isStale: boolean;
  sampledAt: string | null;
  receivedAt: string | null;
  snapshotAgeSeconds: number | null;
  lastHeartbeatAt: string;
  hostCpuPercent: number | null;
  hostMemPercent: number | null;
  hostNetSendBytesPerSec: number | null;
  hostNetRecvBytesPerSec: number | null;
  processCpuPercent: number | null;
  processRssBytes: number | null;
  processHeapBytes: number | null;
  processGoroutines: number | null;
  processThreads: number | null;
  processFds: number | null;
}

export interface ClusterSystemSnapshot {
  timestamp: string;
  agentCount: number;
  onlineCount: number;
  unhealthyCount: number;
  offlineCount: number;
  reportingAgents: number;
  staleAgents: number;
  missingAgents: number;
  coverageRatio: number;

  hostCpuReportingAgents: number;
  avgHostCpuPercent: number | null;
  maxHostCpuPercent: number | null;
  hotHostCpuAgentId?: string;
  hotHostCpuAgentName?: string;

  hostMemoryReportingAgents: number;
  avgHostMemPercent: number | null;
  maxHostMemPercent: number | null;
  hotHostMemAgentId?: string;
  hotHostMemAgentName?: string;
  totalHostMemBytes: number | null;
  usedHostMemBytes: number | null;

  hostNetSendReportingAgents: number;
  hostNetRecvReportingAgents: number;
  totalHostNetSendBytesPerSec: number | null;
  totalHostNetRecvBytesPerSec: number | null;

  processCpuReportingAgents: number;
  avgProcessCpuPercent: number | null;
  maxProcessCpuPercent: number | null;
  hotProcessCpuAgentId?: string;
  hotProcessCpuAgentName?: string;

  processRssReportingAgents: number;
  totalProcessRssBytes: number | null;
  maxProcessRssBytes: number | null;
  hotProcessRssAgentId?: string;
  hotProcessRssAgentName?: string;
  totalProcessHeapBytes: number | null;
  totalProcessGoroutines: number | null;

  processThreadsReportingAgents: number;
  totalProcessThreads: number | null;
  processFdsReportingAgents: number;
  totalProcessFds: number | null;
  maxProcessFds: number | null;
  hotProcessFdsAgentId?: string;
  hotProcessFdsAgentName?: string;
  agents: AgentSystemBrief[];
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

// === 任务单例冲突错误 details ===
export interface TaskConflictDetails {
  activeTaskId: string;
  activeName: string;
  activeState: TaskState;
  startedAt: string;
}

// === History ===
export interface ConfigSummary {
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
  activeAgentCount: number;
  createdAt: string;
  startedAt?: string;
  stoppedAt?: string;
  durationSec: number;
  errorMsg?: string;
  starred: boolean;
  tags: string[];
  note?: string;
  debugMode: boolean;
  configSummary: ConfigSummary;
  stageCount?: number;

  // 阶段历史展示字段（虚拟，不落库）
  recordKind?: 'task' | 'stage';
  parentId?: string;
  stageIndex?: number;
  stageLabel?: string;
  stageFrom?: number;
  stageTo?: number;
  hasResetStages?: boolean;
  children?: HistoryRecord[];

  // 阶段段落指标摘要（从 task_aggregated 提取，仅 stage 子记录）
  totalActions?: number;
  successRate?: number;
  avgRttMs?: number;
  p95RttMs?: number;
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
  cleanupStatus?: CleanupStatus;
}

export interface HistoryHistogramSummary {
  count: number;
  minMs: number | null;
  maxMs: number | null;
  avgMs: number | null;
  p50Ms: number | null;
  p90Ms: number | null;
  p95Ms: number | null;
  p99Ms: number | null;
}

export interface HistoryActionMetric {
  name: string;
  sampleCount: number;
  successCount: number;
  failureCount: number;
  timeoutCount: number;
  canceledCount: number;
  executing: number;
  successRate: number;
  avgSendBytes: number;
  avgRecvBytes: number;
  kind: ActionKind;
  rttApdex: number;
  rttApdexSampleCount: number;
  rtt: HistoryHistogramSummary;
  listenWait: HistoryHistogramSummary;
  listenWaitSampleCount: number;
  listenTimeoutRate: number;
  totalDuration: HistoryHistogramSummary;
  nonRTTAvgMs: number;
  buildAvgMs: number;
  encodeAvgMs: number;
  sendAvgMs: number;
  decodeWaitAvgMs: number;
  decodeAvgMs: number;
  dispatchToActionWaitAvgMs: number;
  parseStoreAvgMs: number;
  rttSampleCount: number;
  totalDurationSampleCount: number;
  avgQps: number;
  errors?: ErrorEntry[];
}

export interface HistorySnapshotSummary {
  timestamp?: string;
  uptimeSeconds: number;
  totalActions: number;
  apdexT: number;
  timingDetail: TimingDetailLevel;
  summary: SnapshotMetricsSummary;
  robots: RobotsView;
  connections: ConnectionsView;
  bandwidth: BandwidthView;
  actions: HistoryActionMetric[];
}

export interface HistorySystemSummary {
  totalMemMB: number;
  usedMemMB: number;
  avgCpuPercent: number;
  maxCpuPercent: number;
  avgMemPercent: number;
  maxMemPercent: number;
  totalNetSendKBps: number | null;
  totalNetRecvKBps: number | null;
  totalGoroutines: number;
  totalThreads: number;
  totalFds: number;
  hotAgentName?: string;
  hotMemAgentName?: string;
}

export interface HistoryDetail extends HistoryRecord {
  agentReports: HistoryAgentReport[];
  agentEvents?: AgentEvent[];
  finalSnapshot: HistorySnapshotSummary;
  finalSystem: HistorySystemSummary;
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
  includeStages?: boolean;
}

export interface UpdateHistoryRequest {
  starred?: boolean;
  tags?: string[];
  note?: string;
}

export interface HistoryTagsResponse {
  tags: string[];
}

export interface HistoryTrendPoint {
  sampledAt: string;
  elapsedSec: number;
  stageIndex: number;
  windowFrom: string;
  windowTo: string;
  sampleCount: number;
  totalQps: number;
  rttApdex: number | null;
  listenWaitP99Ms: number | null;
  rttAvgMs: number | null;
  rttP50Ms: number | null;
  rttP90Ms: number | null;
  rttP95Ms: number | null;
  rttP99Ms: number | null;
  totalDurationAvgMs: number | null;
  totalDurationP95Ms: number | null;
  totalDurationP99Ms: number | null;
  nonRTTAvgMs: number | null;
  encodeAvgMs: number | null;
  decodeAvgMs: number | null;
  activeConnections: number | null;
  closedConnections: number | null;
  droppedConnections: number | null;
  netSendBytesPerSec: number | null;
  netRecvBytesPerSec: number | null;
  assignedAgents: number | null;
  reportingAgents: number | null;
  reportingCoverage: number | null;
  botsRunning: number;
  botsErrored: number;
  sendKBps: number;
  recvKBps: number;
  avgCpuPercent: number;
  maxCpuPercent: number;
  avgMemPercent: number;
  maxMemPercent: number;
  goroutines: number;
  threads: number;
  fds: number;
  onlineCount: number;
  offlineCount: number;
}

export interface TimeseriesResponse {
  taskId: string;
  points: HistoryTrendPoint[];
  sampled: boolean;
  originalCount: number;
  maxPoints: number;
}

export interface HistoryConfigSummary {
  taskId: string;
  name: string;
  totalBots: number;
  robotConfig: RobotConfig;
}

export interface HistoryConfigArchive extends HistoryConfigSummary {
  flowJson: unknown;
  protoFiles: Record<string, string>;
  scripts: Record<string, string>;
}

export interface HistoryCloneRequest {
  name?: string;
}

export interface HistoryCompareTask {
  id: string;
  name: string;
  startedAt?: string;
  durationSec: number;
  totalBots: number;
  parentId?: string;
  stageIndex?: number;
  stageLabel?: string;
  finalSnapshot: {
    totalActions: number;
    actions: Array<{
      name: string;
      sampleCount: number;
      kind: ActionKind;
      rttApdex: number;
      rtt: HistoryHistogramSummary;
      rttSampleCount: number;
      listenWait: HistoryHistogramSummary;
      listenWaitSampleCount: number;
      totalDuration: HistoryHistogramSummary;
      totalDurationSampleCount: number;
      /** 仅用于给缺 kind 的老归档就地推断类别 */
      avgSendBytes: number;
    }>;
  };
}

export interface HistoryCompareResponse {
  tasks: HistoryCompareTask[];
  diff: {
    actions: Record<string, Array<number | null>>;
  };
}
