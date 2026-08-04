# 前端 API 接口文档

## 概述

前端仅与 Admin 服务器通信（不直连 Agent）。所有端点前缀 `/sbot/`。Admin 代理所有 Agent 相关请求。

本文档对照 `plans/api-monitor.md`（计划文档）和实际代码，详尽记录所有前端 API 端点、TypeScript 类型定义、错误处理策略和轮询方案。

### 与计划的差异汇总

| 差异点 | 计划 | 实际实现 |
|---|---|---|
| API 前缀 | `/api/` | `/sbot/`（通过 `services/env.ts` 的 `API_PREFIX` 配置） |
| Admin 端口 | `:8080` | 实际 `:7718`（默认配置），由 `conf/admin-config.json` 控制 |
| RobotConfig.authAddr | 必填 | 已移除（不再由前端指定 authAddr） |
| RobotConfig.authExtra | `Record<string,string>` | 改名为 `stateExtra`（`Record<string,string>`），语义更清晰 |
| RobotConfig.debugMode | 计划中 | 未作为独立字段；调试模式在前端 `editorStore.debugMode` 表达 |
| ActionMetric.skippedCount | 计划中有 | 实际 `types/api.ts` 中 **不存在**；该字段被移除 |
| ErrorBucket | 计划中 `msg+count` | 实际改为 `ErrorEntry`（`kind+code+codeName+msgs+count`），提供更详细的错误分类 |
| ConfigSummary.authAddr | 计划中有 | 实际不存在（authAddr 从 RobotConfig 中移除） |
| HistoryDetail.assignments.agentName | 计划中可选 | 实际类型中为可选 `agentName?` |
| agentsApi.shutdownAgent / shutdownAllAgents | 计划未提及 | 实际实现，对应 `POST /sbot/agents/{id}/shutdown` 和 `POST /sbot/agents/shutdown-all` |
| metricsApi.getClusterMetrics | 计划返回 `StressSnapshot` | 实际返回 `StressAggregate`（包含 `snapshot + reportingAgents + totalAgents`） |
| baselineApi | 计划未提及 | 实际新增模块，从 Admin 或 Vite 中间件读取 `conf/` 基线资源 |
| resourcesStore | 计划未提及 | 实际新增 IndexedDB 双 DB 资源管理（proto / scripts / adapter） |
| 基线资源端点 | 计划未提及 | Admin 后端新增 `GET /sbot/baseline/*` 系列，供前端读取 conf/ 资源 |
| 错误码端点 | 计划未提及 | Admin 后端新增 `GET /sbot/api/error-codes` |
| scriptSync | 计划未提及 | 实际新增 `services/scriptSync.ts`，自动同步 flow 引用的 Lua 脚本到 IDB |

---

## 1. 系统说明

### 1.1 系统组成

stressbot 分布式压测系统包含四类角色：

| 角色 | 说明 |
|---|---|
| **前端** | React + Ant Design + Vite，本文档覆盖范围 |
| **Admin** | 控制中枢，前端唯一通信对象，默认 `:7718` |
| **Agent** | N 个工作节点，分布在不同压测服务器 |
| **被压测游戏服务器** | 项目外，前端无需关心 |

### 1.2 通信拓扑

```
前端 ──HTTP/JSON──> Admin (:7718) ──HTTP──> Agent (:7070)
```

前端不直连 Agent。Admin 聚合所有 Agent 数据，代理转发 Agent 日志请求。

### 1.3 聚合 vs 单点

| 类型 | URL 模式 | 用途 |
|---|---|---|
| **聚合接口** | `/sbot/metrics`、`/sbot/system` | 所有 Agent 数据合并后的集群视图 |
| **单点接口** | `/sbot/metrics/agents`、`/sbot/metrics/agents/{id}` | 单个 Agent 的视图 |

### 1.4 关键概念

**动作（Action）**：压测系统最核心概念。每个 Action 代表机器人执行的一次行为（登录、匹配、战斗等）。指标数据按 Action 维度组织。

**任务单例**：任意时刻全集群最多 1 个"执行中"任务（`starting` / `running` / `stopping`）。`pending` 可多个，但只有 1 个 active。

---

## 2. 通用约定

### 2.1 Base URL 与代理

| 环境 | 前缀 |
|---|---|
| 开发（Vite dev） | `VITE_API_PREFIX` 默认 `/sbot`，Vite 代理到 Admin |
| 生产 | Admin 同源托管前端，前缀 `/sbot` |

Vite 代理配置（`cmd/web/vite.config.ts`）将 `/sbot` 代理到 Admin 地址。

### 2.2 基线资源前缀

`BASELINE_PREFIX` 默认 `/sbot/baseline`，用于读取 `conf/` 目录下的 proto / scripts / adapter / flow / config 资源。

### 2.3 通用请求头

```
Content-Type: application/json    // POST/PUT
Accept:       application/json
```

multipart 上传时 `Content-Type` 由浏览器自动设置为 `multipart/form-data`。

### 2.4 统一错误响应格式

```json
{
  "code": "TASK_NOT_FOUND",
  "message": "task task-01 not found",
  "details": {}
}
```

### 2.5 HTTP 状态码语义

| 状态码 | 含义 |
|---|---|
| `200` | 成功 |
| `201` | 创建成功 |
| `202` | 异步接受 |
| `204` | 成功但无返回体（DELETE） |
| `400` | 参数错误 |
| `404` | 资源不存在 |
| `409` | 状态冲突 |
| `500` | 服务端故障 |

### 2.6 错误码常量

| code | 含义 |
|---|---|
| `TASK_NOT_FOUND` | 任务不存在 |
| `TASK_INVALID_STATE` | 任务状态不允许此操作 |
| `TASK_CONFLICT` | 已有 active 任务（单例约束） |
| `AGENT_NOT_FOUND` | Agent 不存在 |
| `AGENT_BUSY` | Agent 正忙 |
| `AGENT_OFFLINE` | Agent 离线 |
| `CAPACITY_EXCEEDED` | 集群容量不足 |
| `HISTORY_NOT_FOUND` | 历史任务不存在 |
| `HISTORY_STARRED` | 试图删除收藏的历史任务 |
| `INVALID_ARGUMENT` | 参数非法 |
| `NETWORK_ERROR` | 前端自定义：网络断开 / CORS 错误 |

### 2.7 时间格式

RFC3339 字符串：`2026-04-29T10:30:00.123+08:00`。

### 2.8 轮询频率

| 接口 | 间隔 | 条件 |
|---|---|---|
| `/sbot/agents` | 10s（edit）/ 5s（running） | 持续 |
| `/sbot/system` | 10s（edit）/ 5s（running） | 持续 |
| `/sbot/metrics` | 5s | running / viewActive |
| `/sbot/tasks/{id}` | 5s | running / viewActive |

---

## 3. HTTP 基础方法

源文件：`cmd/web/src/services/api.ts`

### 3.1 ApiError 类

所有非 2xx 响应和网络错误统一抛出 `ApiError` 实例：

```typescript
export class ApiError extends Error {
  readonly code: string;
  readonly status: number;
  readonly details?: Record<string, unknown>;
}
```

- 网络断开 / CORS 错误包装为 `{ code: 'NETWORK_ERROR', status: 0 }`
- 非 JSON 响应体兜底为 `{ code: 'HTTP_ERROR', message: statusText }`
- `204 No Content` 返回 `undefined`

### 3.2 基础 HTTP 方法

| 方法 | 函数签名 | 说明 |
|---|---|---|
| GET JSON | `getJson<T>(path: string, init?: RequestInit): Promise<T>` | 带 `Accept: application/json` |
| POST JSON | `postJson<T>(path, body?, init?): Promise<T>` | 自动 `JSON.stringify(body)` |
| PUT JSON | `putJson<T>(path, body?, init?): Promise<T>` | 同上 |
| DELETE | `del<T = void>(path, init?): Promise<T>` | 204 返回 void |
| POST multipart | `postMultipart<T>(path, fd: FormData, init?): Promise<T>` | 不设 Content-Type（浏览器自动） |
| GET text | `getText(path, init?): Promise<string>` | 纯文本响应 |

### 3.3 辅助函数

```typescript
/** 兼容后端"裸数组"或"{items}"两种返回格式 */
function adaptList<T>(resp: unknown): { items: T[]; total: number }

/** 拼接 query string；undefined/null 忽略，数组展开为重复键 */
function buildQuery(params: Record<string, unknown>): string
```

---

## 4. 任务管理 API

源文件：`cmd/web/src/services/tasksApi.ts`

### 4.1 列出任务

```
GET /sbot/tasks?state=running&limit=20&offset=0
```

**前端函数**：`listTasks(params?: TasksListParams): Promise<TasksListResponse>`

**请求参数**（query，全部可选）：

| 参数 | 类型 | 说明 |
|---|---|---|
| `state` | `TaskState` | 过滤状态 |
| `limit` | `number` | 分页大小 |
| `offset` | `number` | 偏移量 |

**响应** `200 OK`：

```typescript
interface TasksListResponse {
  total: number;
  items: TaskBrief[];
}
```

> 内部通过 `adaptList()` 兼容后端直接返回数组或 `{items, total}` 两种格式。

### 4.2 任务详情

```
GET /sbot/tasks/{id}
```

**前端函数**：`getTask(id: string): Promise<TaskDetail>`

**响应** `200 OK`：返回 `TaskDetail`（见 §10 类型定义）。

### 4.3 创建任务

```
POST /sbot/tasks
Content-Type: multipart/form-data
```

**前端函数**：`createTask(fd: FormData): Promise<CreateTaskResponse>`

**FormData 字段**：

| 字段 | 类型 | 必需 | 说明 |
|---|---|---|---|
| `name` | string | 是 | 任务名 |
| `totalBots` | string (int) | 是 | 集群总机器人数 |
| `flow.json` | file (Blob) | 是 | 流程定义 JSON |
| `proto/<filename>` | file | 否 | 多个 .proto 文件 |
| `scripts/<filename>` | file | 否 | 多个 .lua 文件 |
| `robotConfig` | string (JSON) | 是 | RobotConfig 序列化 |
| `deadline` | string (RFC3339) | 否 | 自动停止时间 |
| `adapter/<name>_codec.json` | file | 否 | 声明式 codec 配置，可多份 |
| `adapter/errors.json` | file | 否 | 错误码映射 |

> 实际由 `services/taskActions.ts` 的 `startTask()` 组装 FormData，只提交 flow 引用到的脚本，proto 全量提交。

**响应** `201 Created`：

```typescript
interface CreateTaskResponse {
  id: string;
}
```

### 4.4 启动任务

```
POST /sbot/tasks/{id}/start
```

**前端函数**：`startTask(id: string): Promise<StartTaskResponse>`

无请求体。

**响应** `202 Accepted`：

```typescript
interface StartTaskResponse {
  taskId: string;
  assignments: Assignment[];
}
```

### 4.5 停止任务

```
POST /sbot/tasks/{id}/stop
```

**前端函数**：`stopTask(id: string): Promise<TaskBrief>`

**响应** `202 Accepted`：返回当前 `TaskBrief`（含 `state: "stopping"`）。前端立即更新 store，无需等下一轮轮询。

### 4.6 删除任务

```
DELETE /sbot/tasks/{id}
```

仅允许 `stopped` / `failed` 状态。**响应** `204 No Content`。

### 4.7 任务配置下载

```
GET /sbot/tasks/{id}/config/{path}
```

**前端函数**：`taskConfigUrl(id: string, path: string): string`（返回 URL，用于 `<a>` 标签下载）。

支持路径：`flow/flow.json`、`config.json`、`proto/<filename>`、`scripts/<filename>`。

---

## 5. Agent 管理 API

源文件：`cmd/web/src/services/agentsApi.ts`

### 5.1 列出所有 Agent

```
GET /sbot/agents
```

**前端函数**：`listAgents(): Promise<AgentsListResponse>`

**响应** `200 OK`：

```typescript
interface AgentsListResponse {
  items: AgentBrief[];
}
```

### 5.2 Agent 详情

```
GET /sbot/agents/{id}
```

**响应** `200 OK`：`AgentDetail`（含 `latestSystem?: SystemSnapshot`）。

### 5.3 强制注销 Agent

```
DELETE /sbot/agents/{id}
```

**前端函数**：`deleteAgent(id: string): Promise<void>`

仅允许 `offline` 状态。**响应** `204 No Content`。

### 5.4 关闭单个 Agent

```
POST /sbot/agents/{id}/shutdown
```

**前端函数**：`shutdownAgent(id: string): Promise<void>`

> 计划中未提及，实际实现。

### 5.5 关闭所有 Agent

```
POST /sbot/agents/shutdown-all
```

**前端函数**：`shutdownAllAgents(): Promise<{ succeeded: string[]; failed: string[] }>`

> 计划中未提及，实际实现。

---

## 6. 压测指标 API

源文件：`cmd/web/src/services/metricsApi.ts`

### 6.1 集群聚合压测指标

```
GET /sbot/metrics?taskId=task-01
```

**前端函数**：`getClusterMetrics(params?: MetricsParams): Promise<StressAggregate>`

> 计划中返回 `StressSnapshot`，实际返回 `StressAggregate`（包裹 `snapshot + reportingAgents + totalAgents`）。前端兼容新旧两种格式。

**响应** `200 OK`：

```typescript
interface StressAggregate {
  snapshot: StressSnapshot;
  reportingAgents: number;
  totalAgents: number;
}
```

无 active 任务时返回空快照（字段全 0，actions 为空数组）。

### 6.2 各 Agent 压测指标

```
GET /sbot/metrics/agents?taskId=task-01
```

**前端函数**：`getPerAgentMetrics(params?: MetricsParams): Promise<PerAgentMetrics>`

**响应** `200 OK`：

```typescript
interface PerAgentMetrics {
  items: PerAgentMetricsItem[];
}

interface PerAgentMetricsItem {
  agentId: string;
  agentName: string;
  snapshot: StressSnapshot;
  updatedAt: string;
}
```

### 6.3 单个 Agent 压测指标

```
GET /sbot/metrics/agents/{agentId}
```

**响应** `200 OK`：直接返回 `StressSnapshot`。

### 6.4 文本摘要

```
GET /sbot/metrics/summary
```

返回 `text/plain`，前端可用 `getText()` 获取。

### 6.5 指标计算说明

**Apdex**：当前展示的是 RTT Apdex，`(satisfied + tolerating * 0.5) / rttApdexSampleCount`。其中 `rttApdexSampleCount = rttSampleCount + rttFailedCount`；无响应帧的失败请求记 frustrated，进入 Apdex 分母但不进入 RTT 直方图。监听、单向发送和本地动作不打分。

**QPS**：累计 `summary.avgQps` 是全周期平均；当前 QPS 直接读取后端 `window.summary.avgQps`。前端不做计数差分，也不自行判定窗口过期。

**分位数**：由后端 DDSketch 计算。集群与跨动作总指标都先合并 sketch 再重算，非简单平均。前端直接读取 `StressSnapshot.summary` / `window.summary`，不计算 P50/P90/P95/P99；空分布字段为 `null` 并显示为 `—`。

**计时级别**：`timingDetail` 只控制额外客户端阶段计时，取值 `rtt` / `codec` / `full`；它不改变 Apdex 算法。界面耗时拆分按该字段隐藏未采集阶段。

---

## 7. 系统指标 API

源文件：`cmd/web/src/services/metricsApi.ts`

### 7.1 集群系统聚合

```
GET /sbot/system
```

**前端函数**：`getClusterSystem(): Promise<ClusterSystemSnapshot>`

**响应** `200 OK`：`ClusterSystemSnapshot`（见 §10 类型定义）。

### 7.2 各 Agent 系统指标

```
GET /sbot/system/agents
```

**响应** `200 OK`：`PerAgentSystem`（见 §10 类型定义）。

### 7.3 单个 Agent 系统指标

```
GET /sbot/system/agents/{agentId}
```

**响应** `200 OK`：`SystemSnapshot`。

---

## 8. 历史压测记录 API

源文件：`cmd/web/src/services/historyApi.ts`

### 8.1 历史列表

```
GET /sbot/history?limit=20&offset=0&state=stopped&tags=v1.2&starred=true
```

**前端函数**：`listHistory(filter?: HistoryFilter): Promise<HistoryListResponse>`

**Query 参数**（全部可选）：

| 参数 | 类型 | 说明 |
|---|---|---|
| `limit` | int | 默认 20，最大 100 |
| `offset` | int | 偏移量 |
| `state` | string | `stopped` / `failed` |
| `startedAfter` | RFC3339 | 起始时间 >= |
| `startedBefore` | RFC3339 | 起始时间 <= |
| `tags` | string（可重复） | 任意一个匹配 |
| `tagsAll` | string（可重复） | 全部匹配 |
| `starred` | bool | 仅收藏 |
| `search` | string | 模糊匹配 name + note |
| `orderBy` | string | 排序字段 |
| `includeStages` | bool | 含 reset 的渐进式加压父记录返回 `children`（阶段段落子记录）并置 `hasResetStages=true` |

### 8.2 历史详情

```
GET /sbot/history/{id}
GET /sbot/history/{id}?stageIndex=2   // reset 任务的第 2 段段落详情
```

**前端函数**：`getHistory(id: string, stageIndex?: number): Promise<HistoryDetail>`

`stageIndex > 0` 时返回该 reset 段落详情，响应附带 `recordKind="stage"` / `stageIndex` / `stageLabel` / `stageFrom` / `stageTo`，`totalBots` 为该段峰值机器人数。

### 8.3 更新历史记录

```
PUT /sbot/history/{id}
PUT /sbot/history/{id}?stageIndex=2   // 更新 reset 任务第 2 段的收藏/标签/备注
```

**前端函数**：`updateHistory(id: string, req: UpdateHistoryRequest, stageIndex?: number): Promise<HistoryDetail>`

**请求体**（所有字段可选，部分更新）：

```typescript
interface UpdateHistoryRequest {
  starred?: boolean;
  tags?: string[];
  note?: string;    // 最大 8KB，支持 markdown
}
```

> 含 `reset` 的渐进式加压任务，收藏/标签/备注**分属各阶段段落**：带 `stageIndex>0` 时写入段落级元数据
> （`task_stage_meta`），不带或 `<=0` 时写入任务级（`task_history`）。返回更新后的对应（段落或整体）详情。

### 8.4 删除历史记录

```
DELETE /sbot/history/{id}?force=false
```

**前端函数**：`deleteHistory(id: string, force?: boolean): Promise<void>`

starred=true 时必须 `?force=true`。

### 8.5 时序数据

```
GET /sbot/history/{id}/timeseries
GET /sbot/history/{id}/timeseries?stageIndex=2   // 仅第 2 段采样点
```

**前端函数**：`getHistoryTimeseries(id: string, maxPoints?: number, stageIndex?: number): Promise<TimeseriesResponse>`

`stageIndex > 0` 时仅返回该 reset 段落的采样点；采样点 `stageIndex` 字段表示其所属段落号（非 reset 任务恒为 -1）。

### 8.6 配置归档

```
GET /sbot/history/{id}/config
```

**前端函数**：`getHistoryConfig(id: string): Promise<HistoryConfigArchive>`

### 8.7 克隆历史任务

```
POST /sbot/history/{id}/clone
Content-Type: application/json
```

**前端函数**：`cloneHistory(id: string, req?: HistoryCloneRequest): Promise<{ id: string }>`

### 8.8 多任务对比

```
GET /sbot/history/compare?ids=task-a,task-b,task-c
GET /sbot/history/compare?targets=task-a:-1,task-b:2   // 支持阶段段落对比
```

**前端函数**：`compareHistory(targets: CompareTarget[]): Promise<HistoryCompareResponse>`，其中 `CompareTarget = string | { id: string; stageIndex?: number }`。

2~5 个对比目标。`targets=id:stageIndex` 中 `stageIndex=-1` 表示整体、`>0` 表示该 reset 段落；任一目标带段号即走 `targets=`，否则沿用旧 `ids=`。响应每项附带 `parentId` / `stageIndex` / `stageLabel`。

### 8.9 历史标签列表

```
GET /sbot/history/tags
```

**响应**：`{ tags: string[] }`

### 8.10 历史 Agent 报告

```
GET /sbot/history/{id}/agents
GET /sbot/history/{id}/agents?stageIndex=2   // 第 2 段段落节点报告
```

---

## 9. 日志查看器 API

源文件：`cmd/web/src/services/logsApi.ts`

### 9.1 查询 Admin 日志

```
GET /sbot/logs/admin?afterSeq=0&limit=200
```

**前端函数**：`getAdminLogs(params?: LogQueryParams): Promise<LogQueryResult>`

**Query 参数**：

| 参数 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `afterSeq` | uint64 | `0` | 游标：仅返回 seq > afterSeq 的条目 |
| `limit` | int | `200` | 最大返回条数（1~500） |
| `level` | string | - | 日志等级过滤 |
| `search` | string | - | 关键字搜索 |

### 9.2 查询 Agent 日志（代理转发）

```
GET /sbot/logs/agents/{id}?afterSeq=0&limit=200
```

**前端函数**：`getAgentLogs(agentId: string, params?: LogQueryParams): Promise<LogQueryResult>`

### 9.3 列出 Admin 日志文件

```
GET /sbot/logs/admin/files
```

**前端函数**：`getAdminLogFiles(): Promise<LogFileInfo[]>`

### 9.4 列出 Agent 日志文件

```
GET /sbot/logs/agents/{id}/files
```

**前端函数**：`getAgentLogFiles(agentId: string): Promise<LogFileInfo[]>`

### 9.5 下载日志文件

```
GET /sbot/logs/admin/files/{name}
GET /sbot/logs/agents/{id}/files/{name}
```

返回 `text/plain` + `Content-Disposition` 下载头。

---

## 10. 完整 TypeScript 类型定义

源文件：`cmd/web/src/types/api.ts`

### 10.1 基础枚举

```typescript
type TaskState = 'pending' | 'starting' | 'running' | 'stopping' | 'stopped' | 'failed';
type AgentStatus = 'idle' | 'busy' | 'unhealthy' | 'offline';
type TaskResult = 'completed' | 'stopped' | 'failed';
type OS = 'windows' | 'linux' | 'darwin';
type Arch = 'amd64' | 'arm64';
type LogLevel = 'debug' | 'info' | 'warn' | 'error';
type ErrorKind = 'framework' | 'server';
```

### 10.2 通用错误

```typescript
interface ApiErrorBody {
  code: string;
  message: string;
  details?: Record<string, unknown>;
}
```

### 10.3 Task 相关类型

```typescript
interface TaskBrief {
  id: string;
  name: string;
  state: TaskState;
  totalBots: number;
  agentCount: number;
  createdAt: string;
  startedAt?: string;
  stoppedAt?: string;
}

interface TaskConfig {
  robotConfig: RobotConfig;
  deadline?: string;
  flowFiles: string[];
}

interface RobotConfig {
  concurrency: number;
  timeoutSec: number;
  accountPrefix?: string;
  startNumber?: number;
  mainService?: string;
  stateExtra?: Record<string, string>;
  heartbeatSec?: number;
  httpTimeoutSec?: number;
  apdexT?: number;
  logLevel?: LogLevel;
  debugMode?: boolean;
  rampUp?: RampUpConfig;
}

interface RampUpStage {
  count: number;
  concurrency?: number;
  holdSec?: number;
  reset?: boolean;
}

interface RampUpConfig {
  stages: RampUpStage[];
}

interface Assignment {
  taskId: string;
  agentId: string;
  agentName: string;
  startNumber: number;
  totalBots: number;
}

interface TaskCompletionReport {
  agentId: string;
  taskId: string;
  result: TaskResult;
  errorMsg?: string;
  finishedAt: string;
}

interface TaskDetail extends TaskBrief {
  config: TaskConfig;
  assignments: Assignment[];
  errorMsg?: string;
  reports?: Record<string, TaskCompletionReport>;
  agentEvents?: AgentEvent[];
}

interface TasksListResponse {
  total: number;
  items: TaskBrief[];
}

interface CreateTaskResponse {
  id: string;
}

interface StartTaskResponse {
  taskId: string;
  assignments: Assignment[];
}
```

### 10.4 Agent 相关类型

```typescript
interface AgentEvent {
  agentId: string;
  agentName: string;
  type: string;       // "offline" | "reconnected" | "deregistered" | "restarted"
  timestamp: string;
  detail?: string;
}

interface StaticInfo {
  hostname: string;
  os: OS;
  arch: Arch;
  numCpu: number;
  memTotalMB: number;
  goVersion: string;
  kernelVer: string;
  startedAt: string;
}

interface AgentBrief {
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

interface AgentDetail extends AgentBrief {
  latestSystem?: SystemSnapshot;
}

interface AgentsListResponse {
  items: AgentBrief[];
}
```

### 10.5 压测指标类型

```typescript
interface RobotsView {
  started: number;
  running: number;
  stopped: number;
  errored: number;
}

interface ConnectionsView {
  established: number;
  failed: number;
  dropped: number;
}

interface BandwidthView {
  totalSendBytes: number;
  totalRecvBytes: number;
  sendMBps: number;
  recvMBps: number;
}

interface HistogramView {
  count: number;
  minMs: number | null;
  maxMs: number | null;
  avgMs: number | null;
  p50Ms: number | null;
  p90Ms: number | null;
  p95Ms: number | null;
  p99Ms: number | null;
}

interface ErrorEntry {
  code: number;
  codeName: string;
  msgs: string[];
  count: number;
}

interface ActionMetric {
  name: string;
  sampleCount: number;
  successCount: number;
  failureCount: number;
  timeoutCount: number;
  canceledCount: number;
  executing: number;
  successRate: number;
  kind: 'networked' | 'listen' | 'send' | 'local';
  rttApdex: number;
  rttApdexSampleCount: number;
  rttSampleCount: number;
  rtt: HistogramView;
  listenWait: HistogramView;
  listenWaitSampleCount: number;
  totalDuration: HistogramView;
  totalDurationSampleCount: number;
  nonRTTAvgMs: number;
  buildAvgMs: number;
  encodeAvgMs: number;
  sendAvgMs: number;
  decodeWaitAvgMs: number;
  decodeAvgMs: number;
  dispatchToActionWaitAvgMs: number;
  parseStoreAvgMs: number;
  avgQps: number;
  avgSendBytes: number; // 平均每次已记录动作发送的 WireBytes
  avgRecvBytes: number; // 平均每次已记录动作接收的 WireBytes
  timeoutAvgMs: number;
  errors?: ErrorEntry[];
}

interface SnapshotMetricsSummary {
  sampleCount: number;
  successCount: number;
  failureCount: number;
  timeoutCount: number;
  canceledCount: number;
  successRate: number;
  rttApdex: number;
  rttApdexSampleCount: number;
  rtt: HistogramView;
  listenWait: HistogramView;
  totalDuration: HistogramView;
  nonRTTAvgMs: number;
  clientCostCount: number;
  avgQps: number;
}

interface StressSnapshot {
  timestamp: string;
  collectionEpoch: number;
  uptimeSeconds: number;
  totalActions: number;
  apdexT: number;
  timingDetail: 'rtt' | 'codec' | 'full';
  summary: SnapshotMetricsSummary;
  robots: RobotsView;
  connections: ConnectionsView;
  bandwidth: BandwidthView;
  actions: ActionMetric[];
  window: ReportWindowView | null;
}

interface StressAggregate {
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

interface PerAgentMetricsItem {
  agentId: string;
  agentName: string;
  snapshot: StressSnapshot;
  updatedAt: string;
}

interface PerAgentMetrics {
  items: PerAgentMetricsItem[];
}
```

### 10.6 系统指标类型

```typescript
interface SystemSnapshot {
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

interface ClusterSystemSnapshot {
  timestamp: string;
  agentCount: number;
  onlineCount: number;
  offlineCount: number;
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

interface PerAgentSystemItem {
  agentId: string;
  agentName: string;
  status: AgentStatus;
  snapshot: SystemSnapshot;
  updatedAt: string;
  isStale: boolean;
}

interface PerAgentSystem {
  items: PerAgentSystemItem[];
}
```

### 10.7 历史记录类型

```typescript
interface ConfigSummary {
  concurrency: number;
  timeoutSec: number;
  flowSizeKB: number;
  protoCount: number;
  scriptCount: number;
}

interface HistoryRecord {
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
  stageCount?: number;
  // 阶段历史展示字段（仅含 reset 的渐进式加压任务）
  recordKind?: 'task' | 'stage';
  parentId?: string;
  stageIndex?: number;
  stageLabel?: string;     // 如「段 2 · S3-S4」
  stageFrom?: number;
  stageTo?: number;
  hasResetStages?: boolean;
  children?: HistoryRecord[];  // includeStages 时填充
}

interface HistoryListResponse {
  total: number;
  items: HistoryRecord[];
}

interface HistoryAgentReport {
  agentId: string;
  agentName: string;
  result: TaskResult;
  errorMsg?: string;
  finishedAt: string;
  finalSnapshot: StressSnapshot;
}

interface HistoryDetail extends HistoryRecord {
  assignments: Array<{
    taskId: string;
    agentId: string;
    agentName?: string;
    startNumber: number;
    totalBots: number;
  }>;
  agentReports: HistoryAgentReport[];
  agentEvents?: AgentEvent[];
  finalSnapshot: StressSnapshot;
  finalSystem: ClusterSystemSnapshot;
}

interface HistoryFilter {
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

interface UpdateHistoryRequest {
  starred?: boolean;
  tags?: string[];
  note?: string;
}

interface HistoryTagsResponse {
  tags: string[];
}

interface TimeseriesPoint {
  taskId: string;
  sampledAt: string;
  elapsedSec: number;
  dataType: 'stress' | 'system';
  snapshot: StressSnapshot | ClusterSystemSnapshot;
}

interface TimeseriesResponse {
  taskId: string;
  stress: TimeseriesPoint[];
  system: TimeseriesPoint[];
}

interface HistoryConfigArchive {
  taskId: string;
  name: string;
  totalBots: number;
  robotConfig: RobotConfig;
  flowJson: unknown;
  protoFiles: Record<string, string>;
  scripts: Record<string, string>;
}

interface HistoryCloneRequest {
  name?: string;
}

interface HistoryCompareResponse {
  tasks: HistoryDetail[];
  diff: {
    actions: Record<string, number[]>;
  };
}
```

### 10.8 日志类型

```typescript
interface LogField {
  key: string;
  value: string;
}

interface LogEntry {
  level: string;
  time: string;
  caller?: string;
  message: string;
  service?: string;
  fields?: LogField[];
}

interface LogQueryResult {
  entries: LogEntry[];
  hasMore: boolean;
  nextSeq: number;
}

interface LogFileInfo {
  name: string;
  size: number;
  modTime: string;
}
```

### 10.9 任务冲突 details 类型

```typescript
interface TaskConflictDetails {
  activeTaskId: string;
  activeName: string;
  activeState: TaskState;
  startedAt: string;
}
```

---

## 11. 运行时状态管理

源文件：`cmd/web/src/services/runtimeStore.ts`

### 11.1 RuntimeMode 状态机

```typescript
type RuntimeMode = 'edit' | 'viewActive' | 'running' | 'finalReport';
```

| Mode | 触发 | 行为 |
|---|---|---|
| `edit` | 默认；stopTask 完成后 | FlowEditor 可编辑；仅轮询 agents/system（10s） |
| `viewActive` | 发现已有 active 任务 | FlowEditor 只读；5s 轮询 |
| `running` | 当前会话 startTask 成功 | 同上 |
| `finalReport` | 任务终态 | 停止轮询；保留最后数据 |

### 11.2 RuntimeState 完整字段

```typescript
interface RuntimeState {
  mode: RuntimeMode;
  activeTask: TaskBrief | null;
  ownedTaskId: string | null;
  taskName: string;
  totalBots: number;
  robotConfig: RobotConfig;
  deadline: string | null;
  latestStress: StressSnapshot | null;
  latestSystem: ClusterSystemSnapshot | null;
  agents: AgentBrief[];
  stressHistory: StressSnapshot[];     // 滑窗 60 点
  systemHistory: ClusterSystemSnapshot[];  // 滑窗 60 点
  connectionLost: boolean;
  agentEvents: AgentEvent[];
  reportingAgents: number;
  totalAgents: number;
  offlineAgents: number;
  assignedAgents: number;
}
```

### 11.3 轮询策略

`pollingPolicy(mode)` 函数集中决定四组轮询开关和间隔：

| Mode | pollStress | pollSystem | pollAgents | pollActiveTask | 间隔 |
|---|---|---|---|---|---|
| `edit` | false | true | true | false | 10s |
| `running` | true | true | true | true | 5s |
| `viewActive` | true | true | true | true | 5s |
| `finalReport` | false | false | false | false | 30s |

### 11.4 持久化

仅缓存"启动表单"四个字段到 `localStorage`（key `stressbot:runtime-form`）：
`taskName` / `totalBots` / `robotConfig` / `deadline`。

运行态字段（mode / activeTask / agents 等）不持久化，每次刷新从 admin 重拉。

---

## 12. 任务启停编排

源文件：`cmd/web/src/services/taskActions.ts`

### 12.1 startTask 流程

```typescript
async function startTask(opts: StartTaskOptions): Promise<string>
```

完整流程：
1. `flowStore.toTaskFlow()` + `validateFlow()` 校验
2. `clearMonitorData()` + 清 metricsProvider
3. `syncFlowScriptsToIdb()` 同步脚本
4. 容量预检：`sum(online agents.maxBots) >= totalBots`
5. 组装 multipart（flow.json + proto + scripts + `*_codec.json` + errors.json）
6. `tasksApi.createTask(fd)` → `tasksApi.startTask(id)`
7. 更新 runtimeStore：mode='running', ownedTaskId, activeTask

### 12.2 stopTask 流程

```typescript
async function stopTask(taskId?: string): Promise<TaskBrief | null>
```

调用 `tasksApi.stopTask(id)`，立即更新 activeTask 到 store。

### 12.3 attachToActive 流程

```typescript
async function attachToActive(taskId: string): Promise<void>
```

1. 拉 task detail
2. stash 本地草稿到 LocalStorage
3. 拉远端 flow.json → 替换 flowStore
4. 同步脚本到 IDB
5. 清监控数据
6. 切 mode='viewActive'
7. 回补时序历史

### 12.4 草稿 stash/restore

- `restoreStashedDraft()`: 从 LocalStorage 恢复草稿
- `hasStashedDraft()`: 检查是否有 stash

---

## 13. 基线资源 API

源文件：`cmd/web/src/services/baselineApi.ts`

从 Admin 或 Vite 中间件读取 `conf/` 目录下的基线资源。

| 函数 | 路径 | 说明 |
|---|---|---|
| `fetchBaselineFlow()` | `/sbot/baseline/flow/flow.json` | 基线 flow.json |
| `fetchBaselineConfig()` | `/sbot/baseline/config.json` | 基线 config.json |
| `fetchBaselineScriptIndex()` | `/sbot/baseline/scripts/index.json` | 脚本文件列表 |
| `fetchBaselineScript(name)` | `/sbot/baseline/scripts/<name>` | 脚本内容 |
| `fetchBaselineProtoIndex()` | `/sbot/baseline/proto/index.json` | proto 文件列表 |
| `fetchBaselineProtoContent(name)` | `/sbot/baseline/proto/<name>` | proto 内容 |
| `fetchBaselineCodecIndex()` | `/sbot/baseline/adapter/index.json` | adapter 文件列表 |
| `fetchBaselineCodec(name)` | `/sbot/baseline/adapter/<name>` | 指定 codec/errors 文件 |

---

## 14. IndexedDB 资源管理

源文件：`cmd/web/src/services/resourcesStore.ts`

### 14.1 双 DB 架构

| 数据库 | Object Store | 内容 |
|---|---|---|
| `stressbot-resources-proto` | `data` | 用户上传的 .proto 文件 |
| `stressbot-resources-scripts` | `data` | 用户上传/编辑的 .lua 脚本 |
| `stressbot-resources-adapter` | `data` | `*_codec.json` / `errors.json` |

### 14.2 ResourceFile 数据模型

```typescript
interface ResourceFile {
  name: string;        // 文件名（作为 IDB key）
  content: string;     // utf-8 文本内容
  size: number;        // 字节长度
  uploadedAt: string;  // ISO 时间戳
}
```

### 14.3 Proto 操作

| 函数 | 说明 |
|---|---|
| `addProto(name, content)` | 添加单个 proto |
| `addProtos(files)` | 批量添加 |
| `getProto(name)` | 获取单个 |
| `listProto()` | 列出全部（按 name 排序） |
| `removeProto(name)` | 删除 |
| `clearProto()` | 清空 |

### 14.4 Script 操作

| 函数 | 说明 |
|---|---|
| `addScript(name, content)` | 添加单个脚本 |
| `addScripts(files)` | 批量添加 |
| `getScript(name)` | 获取单个 |
| `listScript()` | 列出全部 |
| `removeScript(name)` | 删除 |
| `clearScript()` | 清空 |

### 14.5 Adapter 操作

| 函数 | 说明 |
|---|---|
| `getCodecSchema(name)` | 获取单个 `*_codec.json` |
| `setCodecSchema(name, content)` | 设置单个 `*_codec.json` |
| `clearCodecSchema(name)` | 删除单个 codec 配置 |
| `listCodecFiles()` | 列出全部 `*_codec.json` |
| `getErrorMap()` | 获取共享 `errors.json` |
| `setErrorMap(content)` | 设置共享 `errors.json` |
| `clearErrorMap()` | 删除共享错误码映射 |

### 14.6 基线同步

`syncResourcesFromBaseline()` 对比 IDB 与基线，自动新增，冲突/删除返回给调用方。

```typescript
interface BaselineSyncResult {
  added: Array<{ type: ResourceType; name: string }>;
  unchanged: Array<{ type: ResourceType; name: string }>;
  conflicts: SyncDiff[];
  removed: SyncDiff[];
}
```

### 14.7 变更订阅

`subscribe(fn)` 注册回调，所有写操作完成后触发。

### 14.8 Legacy 迁移

模块加载时自动检测旧 `stressbot-resources` DB（v0 双 store），迁移 proto 数据到新 DB 后删除旧 DB。

---

## 15. 脚本同步服务

源文件：`cmd/web/src/services/scriptSync.ts`（由 `taskActions.ts` 引用）

### 15.1 设计目标

- flow 引用的脚本是唯一来源，IDB 是本地副本
- IDB 已存在的脚本永不覆盖（保护用户编辑稿）
- 缺失脚本从基线拉取

### 15.2 脚本名扫描范围

| 来源字段 | 说明 |
|---|---|
| `actions[].script` | lua 模式动作 |
| `listens[].script` | lua 模式回调 |
| `nodes[].condition`（`lua:` 前缀） | boolean/loop 条件 |
| `nodes[].breakCondition`（`lua:` 前缀） | loop break 条件 |

### 15.3 返回结果

```typescript
interface ScriptSyncResult {
  added: string[];    // 从基线拉回并写入 IDB
  skipped: string[];  // IDB 已有，未操作
  missing: string[];  // 基线也拉不到
}
```

---

## 16. 后端路由注册

源文件：`admin/handlers.go`

### 16.1 完整路由表

**Agent 上行**：

| 方法 | 路径 | Handler |
|---|---|---|
| POST | `/sbot/agent/register` | `handleAgentRegister` |
| POST | `/sbot/agent/{id}/heartbeat` | `handleAgentHeartbeat` |
| POST | `/sbot/agent/{id}/deregister` | `handleAgentDeregister` |
| POST | `/sbot/agent/stress` | `handleAgentStressReport` |
| POST | `/sbot/agent/system` | `handleAgentSystemReport` |
| POST | `/sbot/agent/{id}/task/{tid}/done` | `handleAgentTaskDone` |
| GET | `/sbot/agent/{id}/pending-task` | `handleAgentPendingTask` |

**前端 - 任务**：

| 方法 | 路径 | Handler |
|---|---|---|
| POST | `/sbot/tasks` | `handleCreateTask` |
| GET | `/sbot/tasks` | `handleListTasks` |
| GET | `/sbot/tasks/{id}` | `handleGetTask` |
| GET | `/sbot/tasks/{id}/config/{path...}` | `handleGetTaskConfig` |
| POST | `/sbot/tasks/{id}/start` | `handleStartTask` |
| POST | `/sbot/tasks/{id}/stop` | `handleStopTask` |
| DELETE | `/sbot/tasks/{id}` | `handleDeleteTask` |

**前端 - Agent**：

| 方法 | 路径 | Handler |
|---|---|---|
| GET | `/sbot/agents` | `handleListAgents` |
| GET | `/sbot/agents/{id}` | `handleGetAgent` |
| DELETE | `/sbot/agents/{id}` | `handleDeleteAgent` |
| POST | `/sbot/agents/{id}/shutdown` | `handleShutdownAgent` |
| POST | `/sbot/agents/shutdown-all` | `handleShutdownAllAgents` |

**前端 - 指标**：

| 方法 | 路径 | Handler |
|---|---|---|
| GET | `/sbot/metrics` | `handleGetMetrics` |
| GET | `/sbot/metrics/summary` | `handleGetMetricsSummary` |
| GET | `/sbot/metrics/agents` | `handleGetAgentMetrics` |
| GET | `/sbot/metrics/agents/{id}` | `handleGetSingleAgentMetrics` |
| GET | `/sbot/system` | `handleGetSystem` |
| GET | `/sbot/system/agents` | `handleGetSystemAgents` |
| GET | `/sbot/system/agents/{id}` | `handleGetSystemAgent` |

**历史归档**：

| 方法 | 路径 | Handler |
|---|---|---|
| GET | `/sbot/history` | `handleListHistory` |
| GET | `/sbot/history/tags` | `handleGetHistoryTags` |
| GET | `/sbot/history/{id}` | `handleGetHistory` |
| PUT | `/sbot/history/{id}` | `handleUpdateHistory` |
| DELETE | `/sbot/history/{id}` | `handleDeleteHistory` |
| GET | `/sbot/history/{id}/agents` | `handleGetHistoryAgents` |
| GET | `/sbot/history/{id}/config` | `handleGetHistoryConfig` |
| GET | `/sbot/history/{id}/timeseries` | `handleGetHistoryTimeseries` |
| POST | `/sbot/history/{id}/clone` | `handleCloneHistory` |
| GET | `/sbot/history/compare` | `handleCompareHistory` |

**日志**：

| 方法 | 路径 | Handler |
|---|---|---|
| GET | `/sbot/logs/admin` | `handleGetAdminLogs` |
| GET | `/sbot/logs/agents/{id}` | `handleGetAgentLogs` |
| GET | `/sbot/logs/admin/files` | `handleListAdminLogFiles` |
| GET | `/sbot/logs/admin/files/{name}` | `handleDownloadAdminLogFile` |
| GET | `/sbot/logs/agents/{id}/files` | `handleListAgentLogFiles` |
| GET | `/sbot/logs/agents/{id}/files/{name}` | `handleDownloadAgentLogFile` |

**基线资源**：

| 方法 | 路径 | Handler |
|---|---|---|
| GET | `/sbot/baseline/proto/index.json` | `handleBaselineProtoIndex` |
| GET | `/sbot/baseline/proto/{name}` | `handleBaselineProtoFile` |
| GET | `/sbot/baseline/scripts/index.json` | `handleBaselineScriptIndex` |
| GET | `/sbot/baseline/scripts/{name}` | `handleBaselineScriptFile` |
| GET | `/sbot/baseline/adapter/index.json` | `handleBaselineCodecIndex` |
| GET | `/sbot/baseline/adapter/{name}` | `handleBaselineCodecFile` |
| GET | `/sbot/baseline/flow/flow.json` | `handleBaselineFlow` |
| GET | `/sbot/baseline/config.json` | `handleBaselineConfig` |

**其他**：

| 方法 | 路径 | Handler |
|---|---|---|
| POST | `/sbot/resources/baseline` | `handleUpdateBaseline` |
| GET | `/sbot/api/error-codes` | `handleErrorCodeIndex` |

---

## 17. 接口分组与页面映射

| 页面/组件 | 主要 API | 辅助 API |
|---|---|---|
| EditorPage（HomeShellInner） | `agents`, `system`, `tasks` | — |
| RuntimeBar | `tasks`, `metrics` | — |
| TaskStartModal | `tasks` (create+start), `agents` (容量预检) | `resourcesStore` |
| ActiveTaskGuardModal | `tasks` (list+get) | — |
| MonitorDock（大盘/动作/错误/趋势/per-Agent/系统） | `metrics`, `system`, `agents` | — |
| ResourcesDrawer | `baselineApi` (基线同步), `resourcesStore` (IDB) | — |
| HistoryModal | `history` (list/detail/clone/compare/timeseries) | — |
| AgentsPanel | `agents` (list/delete/shutdown) | — |

---

## 18. 错误处理策略

### 18.1 全局错误拦截

所有 API 调用通过 `services/api.ts` 的 `request()` 函数统一拦截：

- 非 2xx 响应：解析 JSON body 为 `ApiError` 抛出
- 网络错误：包装为 `{ code: 'NETWORK_ERROR', status: 0 }`
- `204 No Content`：返回 `undefined`

### 18.2 TASK_CONFLICT 特殊处理

`TASK_CONFLICT` 错误走 `Modal.confirm`，让用户选择"查看运行中"或"留在编辑态"。`details` 含 `activeTaskId` / `activeName` / `activeState` / `startedAt`。

### 18.3 轮询失败处理

| 场景 | 策略 |
|---|---|
| 单次失败 | 静默吞掉 |
| 连续失败 >= 3 次 | UI 顶部显示红色横条 |
| 恢复成功 | 红条消失，toast "已恢复连接" |

### 18.4 空快照守门

`runtimeStore.pushStress()` 内置空快照守门：`uptimeSeconds=0 && actions 为空` 的快照会被静默丢弃，避免任务结束瞬间把最后的有效数据覆盖成空。
