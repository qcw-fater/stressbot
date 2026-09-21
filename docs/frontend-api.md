# 前端 API 接口文档

> **契约来源**：浏览器管理 API 以 `api/admin/openapi.yaml` 为唯一契约，由 `npm run generate:api`（openapi-typescript）生成 `cmd/web/src/generated/admin-api.ts`；服务器在 `/sbot/docs` 展示同一契约。

## 概述

前端仅与 Admin 服务器通信（不直连 Agent）。所有浏览器管理端点前缀 `/sbot/`。

**契约 + 生成客户端模式**：

- `api/admin/openapi.yaml` 是唯一契约源；后端路由注册（`admin/httpapi/routes.go`）与前端类型都从它出发。
- `npm run generate:api` 生成 `cmd/web/src/generated/admin-api.ts`（`cmd/web/package.json:13`）。
- `cmd/web/src/services/generatedClient.ts` 用 `openapi-fetch` 的 `createClient<paths>` 构造类型安全客户端 `managementClient`，并把其判别联合返回值转换为项目既有的 `Promise/ApiError` 契约（`unwrapGenerated`）；AbortError 透传不包装。
- 服务层按域拆分模块：`api.ts`（fetch 基座 + ApiError）、`baselineApi.ts`、`tasksApi.ts`、`agentsApi.ts`、`metricsApi.ts`、`historyApi.ts`、`flowsApi.ts`、`templatesApi.ts`、`capabilitiesApi.ts`、`codecApi.ts`。**组件禁止直接 fetch**。
- 基线资源读取走 `BASELINE_PREFIX`（默认 `/sbot/baseline`），不进 OpenAPI 契约（文本端点）。

---

## 1. 系统说明

### 1.1 系统组成

stressbot 分布式压测系统包含四类角色：

| 角色 | 说明 |
|---|---|
| **前端** | React + Ant Design + Vite，本文档覆盖范围 |
| **Admin** | 控制中枢，前端唯一通信对象，浏览器管理面默认 `:7718`（`conf/admin.toml:25`） |
| **Agent** | N 个工作节点，分布在不同压测服务器 |
| **被压测游戏服务器** | 项目外，前端无需关心 |

### 1.2 通信拓扑

```
前端 ──HTTP/JSON──> Admin (:7718，浏览器管理面 /sbot/)
Admin ──gRPC──> Agent（控制面 :7720，任务下发/指标上报，不经浏览器）
```

前端不直连 Agent。Admin-Agent 控制面使用 gRPC 长连接（`conf/admin.toml:36-38` controlPlane），浏览器只看到 Admin 聚合后的数据。

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

前缀由 `cmd/web/src/services/env.ts` 的 `API_PREFIX` 集中管理。Vite 代理配置（`cmd/web/vite.config.ts`）将 `/sbot` 代理到 Admin 地址。

### 2.2 基线资源前缀

`BASELINE_PREFIX` 默认 `/sbot/baseline`，用于读取 `conf/` 目录下的 proto / scripts / adapter / flow 资源。

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

### 2.6 常见错误码

完整展示文案见 `cmd/web/src/services/errorHandler.ts` 的 `TOAST_MAP`：

| code | 含义 |
|---|---|
| `TASK_NOT_FOUND` | 任务不存在 |
| `TASK_INVALID_STATE` | 任务状态不允许此操作 |
| `TASK_CONFLICT` | 已有 active 任务（单例约束） |
| `AGENT_NOT_FOUND` / `AGENT_BUSY` / `AGENT_OFFLINE` | 节点状态类 |
| `CAPACITY_EXCEEDED` | 集群容量不足 |
| `HISTORY_NOT_FOUND` / `HISTORY_STARRED` / `HISTORY_DISABLED` | 历史归档类 |
| `ACTION_TEMPLATE_NOT_FOUND` / `LISTEN_TEMPLATE_NOT_FOUND` / `TEMPLATE_NAME_CONFLICT` / `TEMPLATE_SNAPSHOT_CONFLICT` | 模板库类 |
| `TEMPLATE_LIBRARY_DISABLED` / `FLOW_LIBRARY_DISABLED` | 服务器未启用对应库（MySQL 未配置） |
| `INVALID_ARGUMENT` | 参数非法 |
| `NETWORK_ERROR` | 前端自定义：网络断开 / CORS 错误 |

### 2.7 时间格式

RFC3339 字符串：`2026-04-29T10:30:00.123+08:00`。

### 2.8 轮询频率

轮询由 `runtimeStore.pollingPolicy(mode)` 集中决定（见 §11.3）：`running` / `viewActive` 全量 5s；`edit` 仅 agents/system 10s；`finalReport` 停止轮询（30s 兜底间隔）。

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
- 主动取消（AbortError）原样抛出，不伪装成网络故障（`isAbortError`）
- 非 JSON 响应体兜底为 `{ code: 'HTTP_ERROR', message: statusText }`
- `204 No Content` 返回 `undefined`

### 3.2 基础 HTTP 方法

| 方法 | 函数签名 | 说明 |
|---|---|---|
| GET JSON | `getJson<T>(path, init?): Promise<T>` | 带 `Accept: application/json` |
| POST JSON | `postJson<T>(path, body?, init?): Promise<T>` | 自动 `JSON.stringify(body)` |
| PUT JSON | `putJson<T>(path, body?, init?): Promise<T>` | 同上 |
| DELETE | `del<T = void>(path, init?): Promise<T>` | 204 返回 void |
| POST multipart | `postMultipart<T>(path, fd, init?): Promise<T>` | 不设 Content-Type（浏览器自动） |

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

**前端函数**：`listTasks(params?, signal?): Promise<TasksListResponse>`

**请求参数**（query，全部可选）：`state`（TaskState）、`limit`、`offset`。

**响应** `200 OK`：

```typescript
interface TasksListResponse {
  total: number;
  items: TaskBrief[];
}
```

### 4.2 任务详情

```
GET /sbot/tasks/{id}
```

**前端函数**：`getTask(id, signal?): Promise<TaskDetail>`

### 4.3 创建任务

```
POST /sbot/tasks
Content-Type: multipart/form-data
```

**前端函数**：`createTask(fd): Promise<CreateTaskResponse>`

**FormData 字段**（由 `services/taskActions.ts` 的 `startTask` 组装）：

| 字段 | 类型 | 必需 | 说明 |
|---|---|---|---|
| `name` | string | 是 | 任务名 |
| `totalBots` | string (int) | 是 | 集群总机器人数（渐进加压时为各阶段之和） |
| `robotConfig` | string (JSON) | 是 | RobotConfig 序列化（rampUp 在其内） |
| `flow.json` | file (Blob) | 是 | 流程定义 JSON |
| `flowTemplateId` | string | 否 | 来源流程模板 ID，仅用于历史溯源 |
| `deadline` | string (RFC3339) | 否 | 自动停止时间 |
| `proto/<filename>` | file | 否 | 多个 .proto 文件（全量提交） |
| `scripts/<filename>` | file | 否 | 多个 .lua 文件（只提交 flow 引用到的） |
| `adapter/<name>_codec.json` | file | 否 | 声明式 codec 配置，可多份（至少一份，否则启动前拦截） |
| `adapter/errors.json` | file | 否 | 错误码映射（未配置则不下发） |

**响应** `201 Created`：

```typescript
interface CreateTaskResponse { id: string }
```

### 4.4 启动任务

```
POST /sbot/tasks/{id}/start
```

**前端函数**：`startTask(id): Promise<StartTaskResponse>`

无请求体。**响应** `202 Accepted`：

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

**前端函数**：`stopTask(id): Promise<TaskBrief>`

**响应** `202 Accepted`：返回当前 `TaskBrief`（含 `state: "stopping"`）。前端立即更新 store，无需等下一轮轮询。

### 4.6 删除任务

```
DELETE /sbot/tasks/{id}
```

仅允许 `stopped` / `failed` 状态。**响应** `204 No Content`。

### 4.7 任务配置读取

```
GET /sbot/tasks/{id}/config/{path}
```

**前端函数**：
- `getTaskConfigJson<T>(id, path): Promise<T>` -- 解析为 JSON
- `taskConfigUrl(id, path): string` -- 返回 URL，用于 `<a>` 标签下载

支持路径：`flow/flow.json`、`config.json`、`proto/<filename>`、`scripts/<filename>`。

---

## 5. Agent 管理 API

源文件：`cmd/web/src/services/agentsApi.ts`

### 5.1 列出所有 Agent

```
GET /sbot/agents
```

**前端函数**：`listAgents(signal?): Promise<AgentsListResponse>`

**响应** `200 OK`：

```typescript
interface AgentsListResponse { items: AgentBrief[] }
```

### 5.2 强制注销 Agent

```
DELETE /sbot/agents/{id}
```

**前端函数**：`deleteAgent(id): Promise<void>`

仅允许 `offline` 状态。**响应** `204 No Content`。

### 5.3 关闭单个 Agent

```
POST /sbot/agents/{id}/shutdown
```

**前端函数**：`shutdownAgent(id): Promise<void>`

### 5.4 关闭所有 Agent

```
POST /sbot/agents/shutdown-all
```

**前端函数**：`shutdownAllAgents(): Promise<{ succeeded: string[]; failed: string[] }>`

---

## 6. 压测指标 API

源文件：`cmd/web/src/services/metricsApi.ts`

### 6.1 集群聚合压测指标

```
GET /sbot/metrics?taskId=task-01
```

**前端函数**：`getClusterMetrics(params?, signal?): Promise<StressAggregate>`

**响应** `200 OK`：

```typescript
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
```

`parseStressAggregate` 负责把后端可能缺省的节点健康字段归一为安全数值。无 active 任务时返回空快照（字段全 0，actions 为空数组）。

### 6.2 各 Agent 压测指标

```
GET /sbot/metrics/agents?taskId=task-01
```

**前端函数**：`getPerAgentMetrics(params?, signal?): Promise<PerAgentMetrics>`

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

### 6.3 文本摘要

```
GET /sbot/metrics/summary
```

返回 `text/plain` 摘要。

### 6.4 指标计算说明

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

**前端函数**：`getClusterSystem(signal?): Promise<ClusterSystemSnapshot>`；`parseClusterSystemSnapshot` 做字段归一。

### 7.2 各 Agent 系统指标

```
GET /sbot/system/agents
GET /sbot/system/agents/{agentId}
```

`GET /sbot/system/agents` 返回 `PerAgentSystem`；单 Agent 端点返回 `SystemSnapshot`（在 OpenAPI 契约中定义，前端当前经聚合视图消费）。

---

## 8. 历史压测记录 API

源文件：`cmd/web/src/services/historyApi.ts`

### 8.1 历史列表

```
GET /sbot/history?limit=20&offset=0&state=stopped&tags=v1.2&starred=true
```

**前端函数**：`listHistory(filter?, init?): Promise<HistoryListResponse>`

**Query 参数**（全部可选）：

| 参数 | 类型 | 说明 |
|---|---|---|
| `limit` | int | 默认 20，最大 100 |
| `offset` | int | 偏移量 |
| `state` | string | `stopped` / `failed` |
| `startedAfter` / `startedBefore` | RFC3339 | 起始时间过滤 |
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

**前端函数**：`getHistory(id, stageIndex?, init?): Promise<HistoryDetail>`

`stageIndex > 0` 时返回该 reset 段落详情，响应附带 `recordKind="stage"` / `stageIndex` / `stageLabel` / `stageFrom` / `stageTo`，`totalBots` 为该段峰值机器人数。

### 8.3 更新历史记录

```
PUT /sbot/history/{id}
PUT /sbot/history/{id}?stageIndex=2   // 更新 reset 任务第 2 段的收藏/标签/备注
```

**前端函数**：`updateHistory(id, req, stageIndex?, init?): Promise<HistoryDetail>`

**请求体**（所有字段可选，部分更新）：

```typescript
interface UpdateHistoryRequest {
  starred?: boolean;
  tags?: string[];
  note?: string;    // 最大 8KB，支持 markdown
}
```

> 含 `reset` 的渐进式加压任务，收藏/标签/备注**分属各阶段段落**：带 `stageIndex>0` 时写入段落级元数据（`task_meta`），不带或 `<=0` 时写入任务级。返回更新后的对应（段落或整体）详情。

### 8.4 删除历史记录

```
DELETE /sbot/history/{id}?force=false
```

**前端函数**：`deleteHistory(id, force?): Promise<void>`

starred=true 时必须 `?force=true`。

### 8.5 时序数据

```
GET /sbot/history/{id}/timeseries
GET /sbot/history/{id}/timeseries?stageIndex=2   // 仅第 2 段采样点
```

**前端函数**：`getHistoryTimeseries(id, maxPoints?, stageIndex?): Promise<TimeseriesResponse>`

`stageIndex > 0` 时仅返回该 reset 段落的采样点；采样点 `stageIndex` 字段表示其所属段落号（非 reset 任务恒为 -1）。

### 8.6 配置归档

```
GET /sbot/history/{id}/config          // 配置摘要（HistoryConfigSummary）
GET /sbot/history/{id}/config/archive  // 完整归档（proto/scripts 文件内容）
```

**前端函数**：
- `getHistoryConfig(id, init?): Promise<HistoryConfigSummary>`
- `getHistoryConfigArchive(id): Promise<HistoryConfigArchive>`

### 8.7 多任务对比

```
GET /sbot/history/compare?ids=task-a,task-b,task-c
GET /sbot/history/compare?targets=task-a:-1,task-b:2   // 支持阶段段落对比
```

**前端函数**：`compareHistory(targets: CompareTarget[], init?): Promise<HistoryCompareResponse>`，其中 `CompareTarget = string | { id: string; stageIndex?: number }`。

2~5 个对比目标。`targets=id:stageIndex` 中 `stageIndex=-1` 表示整体、`>0` 表示该 reset 段落；任一目标带段号即走 `targets=`，否则沿用旧 `ids=`。响应每项附带 `parentId` / `stageIndex` / `stageLabel`。

### 8.8 历史标签列表

```
GET /sbot/history/tags
```

**响应**：`{ tags: string[] }`

### 8.9 历史 Agent 报告

```
GET /sbot/history/{id}/agents
GET /sbot/history/{id}/agents?stageIndex=2   // 第 2 段段落节点报告
```

---

## 9. 流程模板库 API

源文件：`cmd/web/src/services/flowsApi.ts`。服务器 MySQL 存储；服务器未配置时接口返回 `FLOW_LIBRARY_DISABLED`。

| 端点 | 前端函数 | 说明 |
|---|---|---|
| `GET /sbot/flows` | `listFlowTemplates(signal?)` | 流程模板列表（不含 flow/layout） |
| `POST /sbot/flows` | `createFlowTemplate(req)` | 新建模板（name + flow + layout） |
| `GET /sbot/flows/snapshot` | `getFlowSnapshot()` | 全量快照（含 revision） |
| `PUT /sbot/flows/snapshot` | `replaceFlowSnapshot(req)` | 乐观锁整库替换（expectedRevision + idPolicy） |
| `GET /sbot/flows/{id}` | `getFlowTemplate(id)` | 模板详情（含 flow/layout） |
| `PUT /sbot/flows/{id}` | `updateFlowTemplate(id, req)` | 覆盖更新 |
| `DELETE /sbot/flows/{id}` | `deleteFlowTemplate(id)` | 删除 |

流程模板只存 flow + layout，不内嵌 Lua/proto/adapter（资源走独立管理链路）。

## 10. Action/Listen 模板库 API

源文件：`cmd/web/src/services/templatesApi.ts`，导出 `actionTemplatesApi` 与 `listenTemplatesApi` 两组同构实例。两类模板存储均可用时 `capabilities.templateLibrary=true`，否则相关接口返回 `TEMPLATE_LIBRARY_DISABLED`。

| 端点 | 说明 |
|---|---|
| `GET /sbot/action-templates` / `GET /sbot/listen-templates` | 模板列表 |
| `POST /sbot/action-templates` / `POST /sbot/listen-templates` | 新建模板 |
| `GET /sbot/action-templates/snapshot` / `GET /sbot/listen-templates/snapshot` | 全量快照（含 revision） |
| `PUT /sbot/action-templates/snapshot` / `PUT /sbot/listen-templates/snapshot` | 乐观锁整库替换（`TemplateIdPolicy`: `preserve` / `generate-missing`） |
| `GET /sbot/action-templates/{id}` / `GET /sbot/listen-templates/{id}` | 模板详情 |
| `PUT /sbot/action-templates/{id}` / `PUT /sbot/listen-templates/{id}` | 覆盖更新 |
| `DELETE /sbot/action-templates/{id}` / `DELETE /sbot/listen-templates/{id}` | 删除 |

冲突错误码：`TEMPLATE_NAME_CONFLICT`（同名）、`TEMPLATE_SNAPSHOT_CONFLICT`（快照 revision 过期，需刷新重试）。

## 11. 服务器能力与 Codec 元数据 API

### 11.1 服务器能力

```
GET /sbot/capabilities
```

**前端函数**：`getCapabilities(init?): Promise<CapabilitiesResponse>`（类型直接取自生成契约 `components['schemas']['CapabilitiesResponse']`）

```typescript
interface CapabilitiesResponse {
  sharedState: boolean;      // 是否已配置共享状态（Redis）
  sharedAddr?: string;       // 脱敏展示地址
  flowLibrary: boolean;      // 服务器流程库是否启用
  templateLibrary: boolean;  // 共享 Action/Listen 模板库是否启用
}
```

启动任务前用它做 share 预检：脚本使用 `share` 但 `sharedState=false` 时前端提前拦截（`taskActions.ts:213-234`）。

### 11.2 Codec 预览与算法元数据

源文件：`cmd/web/src/services/codecApi.ts`

| 端点 | 前端函数 | 说明 |
|---|---|---|
| `POST /sbot/codec/preview` | `previewCodec(req): Promise<PreviewResult>` | 纯计算：对样例输入跑声明式 codec 编解码预览 |
| `GET /sbot/codec/algorithms` | `fetchCodecAlgorithms(): Promise<AlgoMeta[]>` | 可用压缩/加密算法元数据 |

## 12. 基线资源 API

源文件：`cmd/web/src/services/baselineApi.ts`。从 Admin 或 Vite 中间件读取 `conf/` 目录下的基线资源，全部只读：

| 函数 | 路径 | 说明 |
|---|---|---|
| `fetchBaselineResource(kind, name)` | `/sbot/baseline/<dir>/<name>` | 三态单文件读取：`ok` / `missing`（服务器权威 404）/ `error`（网络失败，有无未知） |
| `fetchBaselineFlow()` | `/sbot/baseline/flow/flow.json` | 基线 flow.json |
| `fetchBaselineScriptIndex()` | `/sbot/baseline/scripts/index.json` | 脚本文件列表 |
| `fetchBaselineScript(name)` | `/sbot/baseline/scripts/<name>` | 脚本内容 |
| `fetchBaselineProtoIndex()` | `/sbot/baseline/proto/index.json` | proto 文件列表 |
| `fetchBaselineProtoContent(name)` | `/sbot/baseline/proto/<name>` | proto 内容 |
| `fetchBaselineCodecIndex()` | `/sbot/baseline/adapter/index.json` | adapter 文件列表 |
| `fetchBaselineCodec(name)` | `/sbot/baseline/adapter/<name>` | 指定 codec/errors 文件 |

三态接口的用途：删除对账必须区分「服务器说没有」（404，可删本地）与「请求失败」（断网/5xx，服务器上到底有没有未知，不得删本地）。index 系列返回 `null` 表示请求失败，返回 `string[]`（含空数组）表示服务器权威清单。

不存在「写基线」的独立 HTTP 端点：基线写入只发生在创建任务时（后端 `writeBaselineFiles`）。

## 13. 运行时状态管理

源文件：`cmd/web/src/services/runtimeStore.ts`

### 13.1 RuntimeMode 状态机

```typescript
type RuntimeMode = 'edit' | 'viewActive' | 'running' | 'finalReport';
```

| Mode | 触发 | 行为 |
|---|---|---|
| `edit` | 默认；任务终态退出后 | FlowEditor 可编辑；仅轮询 agents/system（10s） |
| `viewActive` | 发现已有 active 任务（attachToActive） | FlowEditor 只读；5s 轮询 |
| `running` | 当前会话 startTask 成功 | 同上 |
| `finalReport` | 任务终态 | 停止轮询；保留最后数据 |

`starting` / `stopping` 是后端 TaskState 值，不属于 RuntimeMode。运行中可「临时返回编辑」（`detachToEdit` 保留 `detachedActiveTask` 供无刷新重新进入监控），终态退出用 `detachFromActive`，彻底清空用 `reset`。

### 13.2 RuntimeState 主要字段

```typescript
interface RuntimeState {
  mode: RuntimeMode;
  activeTask: TaskBrief | null;        // 当前查看的 active 任务
  detachedActiveTask: TaskBrief | null; // 临时返回编辑时保留的任务引用
  ownedTaskId: string | null;          // 本端发起的任务 ID（停止校验用）
  taskName: string;
  totalBots: number;
  robotConfig: RobotConfig;
  deadline: string | null;
  rampUpEnabled: boolean;              // 渐进加压开关（持久化）
  rampUpStages: RampUpStage[];         // 阶段配置（持久化）
  latestStress: StressSnapshot | null;
  latestSystem: ClusterSystemSnapshot | null;
  agents: AgentBrief[];
  agentsLoaded: boolean;               // 区分「还没拿到」与「拿到了、集群确实空」
  connectionLost: boolean;             // 连续查询失败横幅
  agentEvents: AgentEvent[];
  reportingAgents: number;             // 节点健康（来自 StressAggregate）
  totalAgents: number;
  offlineAgents: number;
  assignedAgents: number;
}
```

### 13.3 轮询策略

`pollingPolicy(mode)`（`runtimeStore.ts:317-352`）集中决定四组轮询开关和间隔：

| Mode | pollStress | pollSystem | pollAgents | pollActiveTask | 间隔 |
|---|---|---|---|---|---|
| `edit` | false | true | true | false | 10s |
| `running` | true | true | true | true | 5s |
| `viewActive` | true | true | true | true | 5s |
| `finalReport` | false | false | false | false | 30s |

React Query 全局默认（`queryClient.ts`）：`retry` 2 次（AbortError 永不重试）、`retryDelay` 指数退避封顶 3s、`gcTime` 10 分钟、`refetchOnWindowFocus` 关闭（关窗不重取）、断线重连时重取。mutations 不重试。

### 13.4 持久化

仅缓存"启动表单"到 `localStorage`（key `stressbot:runtime-form`，version 4）：
`taskName` / `totalBots` / `robotConfig` / `deadline` / `rampUpEnabled` / `rampUpStages`。

运行态字段（mode / activeTask / agents 等）不持久化，每次刷新从 Admin 重拉。反序列化时与 `DEFAULT_ROBOT_CONFIG` 合并，老版本缺的新字段自动补全，用户已设置的字段不覆盖。

## 14. 任务启停编排

源文件：`cmd/web/src/services/taskActions.ts`

### 14.1 startTask 流程

```typescript
async function startTask(opts: StartTaskOptions): Promise<string>
```

完整流程（`taskActions.ts:100-297`，任意一步失败抛出，runtimeStore 不变更）：
1. `syncProtosToIdb()` proto gap-fill（基线补缺不覆盖本地）；拉回新文件则重载 proto 注册表
2. `validateFlow()` 校验，有 error 直接拒绝
3. `syncFlowScriptsToIdb()` 脚本 gap-fill，仍缺失脚本则抛错
4. proto import 依赖完整性校验（缺依赖拦截，不把残缺任务下发）
5. codec 连接校验：flow 引用的每条 `<protocol>:<service>` 连接必须有对应 codec 文件
6. 共享状态预检：脚本用 share 但服务器 `capabilities.sharedState=false` 时拦截
7. 容量预检：`assignableCapacity(agents) >= totalBots`（口径与服务端分配一致，`taskCapacity.ts`）
8. 组装 multipart（flow.json + 全量 proto + 引用到的 scripts + 全部 codec + errors.json）→ `tasksApi.createTask(fd)` → `markResourcesAsBaselineSynced` 刷新 baseHash → `tasksApi.startTask(id)`
9. 更新 runtimeStore：清监控数据、mode='running'、ownedTaskId、activeTask

### 14.2 stopTask 流程

```typescript
async function stopTask(taskId?): Promise<TaskBrief | null>
```

调用 `tasksApi.stopTask(id)`，立即更新 activeTask 到 store。

### 14.3 attachToActive 流程

1. 拉 task detail
2. stash 本地草稿到 LocalStorage（`stressbot:flow:stash`）
3. 拉远端 flow.json → 替换 flowStore
4. 同步脚本到 IDB
5. 清监控数据
6. 切 mode='viewActive'

### 14.4 草稿 stash/restore

- `restoreStashedDraft()`: 从 LocalStorage 恢复草稿
- `hasStashedDraft()`: 检查是否有 stash

## 15. IndexedDB 资源管理

源文件：`cmd/web/src/services/resourcesStore.ts`（详细机制见 `docs/resource-sync.md`）

### 15.1 三库架构

| 数据库 | Object Store | 内容 |
|---|---|---|
| `stressbot-resources-proto` | `data` | 用户上传的 .proto 文件 |
| `stressbot-resources-scripts` | `data` | 用户上传/编辑的 .lua 脚本 |
| `stressbot-resources-adapter` | `data` | 多份 `*_codec.json` + 共享 `errors.json` |

### 15.2 ResourceFile 数据模型

```typescript
interface ResourceFile {
  name: string;         // 文件名（作为 IDB key）
  content: string;      // utf-8 文本内容
  size: number;         // 字节长度
  uploadedAt: string;   // ISO 时间戳
  baseHash?: string | null; // 上次确认同步到的服务器内容 hash
}
```

### 15.3 主要操作

proto / script / adapter 各有一组 CRUD（`addXxx` / `addXxxFromBaseline` / `getXxx` / `listXxx` / `removeXxx` / `clearXxx` / `replaceXxxFiles`），adapter 侧另有 `getCodecSchema` / `setCodecSchema` / `clearCodecSchema` / `listCodecFiles` / `getErrorMap` / `setErrorMap` / `clearErrorMap`。写操作完成后经 `subscribe(fn)` 通知订阅方刷新。

### 15.4 Legacy 迁移

模块加载时自动检测旧 `stressbot-resources` DB（v0 双 store），迁移数据到新库后删除旧 DB（`resourcesStore.ts:30,116`）。

## 16. 后端路由注册

源文件：`admin/httpapi/routes.go`（`registerManagementRoutes`）。完整契约见 `api/admin/openapi.yaml`；`GET /sbot/openapi.yaml` 与 `GET /sbot/docs`（Swagger UI）随服务发布。

**任务**：

| 方法 | 路径 | Handler |
|---|---|---|
| POST | `/sbot/tasks` | `handleCreateTask`（含 `writeBaselineFiles` 写回基线） |
| GET | `/sbot/tasks` | `handleListTasks` |
| GET | `/sbot/tasks/{id}` | `handleGetTask` |
| GET | `/sbot/tasks/{id}/config/{path...}` | `handleGetTaskConfig` |
| POST | `/sbot/tasks/{id}/start` | `handleStartTask` |
| POST | `/sbot/tasks/{id}/stop` | `handleStopTask` |
| DELETE | `/sbot/tasks/{id}` | `handleDeleteTask` |

**Agent**：

| 方法 | 路径 | Handler |
|---|---|---|
| GET | `/sbot/agents` | `handleListAgents` |
| DELETE | `/sbot/agents/{id}` | `handleDeleteAgent` |
| POST | `/sbot/agents/{id}/shutdown` | `handleShutdownAgent` |
| POST | `/sbot/agents/shutdown-all` | `handleShutdownAllAgents` |

**指标**：

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
| GET | `/sbot/history/compare` | `handleCompareHistory` |
| GET | `/sbot/history/{id}` | `handleGetHistory` |
| PUT | `/sbot/history/{id}` | `handleUpdateHistory` |
| DELETE | `/sbot/history/{id}` | `handleDeleteHistory` |
| GET | `/sbot/history/{id}/agents` | `handleGetHistoryAgents` |
| GET | `/sbot/history/{id}/config` | `handleGetHistoryConfig` |
| GET | `/sbot/history/{id}/config/archive` | `handleGetHistoryConfigArchive` |
| GET | `/sbot/history/{id}/timeseries` | `handleGetHistoryTimeseries` |

**基线资源（只读）**：

| 方法 | 路径 | Handler |
|---|---|---|
| GET | `/sbot/baseline/proto/index.json` | `handleBaselineProtoIndex` |
| GET | `/sbot/baseline/proto/{name}` | `handleBaselineProtoFile` |
| GET | `/sbot/baseline/scripts/index.json` | `handleBaselineScriptIndex` |
| GET | `/sbot/baseline/scripts/{name}` | `handleBaselineScriptFile` |
| GET | `/sbot/baseline/adapter/index.json` | `handleBaselineCodecIndex` |
| GET | `/sbot/baseline/adapter/{name}` | `handleBaselineCodecFile` |
| GET | `/sbot/baseline/flow/flow.json` | `handleBaselineFlow` |

**流程模板库**：`GET/POST /sbot/flows`、`GET/PUT /sbot/flows/snapshot`、`GET/PUT/DELETE /sbot/flows/{id}`（§9）。

**Action/Listen 模板库**：`GET/POST /sbot/action-templates`、`/sbot/listen-templates` 及其 `/snapshot`、`/{id}` 变体（§10）。

**能力与 Codec**：`GET /sbot/capabilities`、`POST /sbot/codec/preview`、`GET /sbot/codec/algorithms`（§11）。

**错误码**：`GET /sbot/api/error-codes`（`handleErrorCodeIndex`）。

Agent 上行（注册/心跳/指标上报/任务完成）不走这些 HTTP 路由——Admin-Agent 控制面是 gRPC 长连接（`admin/grpcapi/`），对浏览器不可见。

## 17. 接口分组与页面映射

| 页面/组件 | 主要 API | 辅助 API |
|---|---|---|
| EditorPage（HomeShellInner） | `agents`, `system`, `tasks`（useRuntimeQueries） | — |
| RuntimeBar | `tasks`, `metrics` | — |
| TaskStartModal | `tasks` (create+start), `agents` (容量预检) | `capabilities`（share 预检）, `resourcesStore` |
| ActiveTaskGuardModal | `tasks` (list+get) | — |
| MonitorDock（负载/吞吐/质量/动作表/系统） | `metrics`, `system`, `agents` | — |
| ResourcesDrawer / ProtocolConfigEditor | `baselineApi` (基线同步), `resourcesStore` (IDB) | — |
| FlowManagerModal | `flowsApi`（服务器流程库） | — |
| NodePalette 模板区 | `templatesApi`（服务器模板库） | `capabilities` |
| HistoryModal / HistoryDetailView | `history` (list/detail/compare/timeseries/config/archive) | — |
| AgentsPanel | `agents` (list/delete/shutdown) | — |
| NotepadTab | 本地（无后端 API） | — |

## 18. 错误处理策略

### 18.1 集中展现

`services/errorHandler.ts` 的 `showApiError` 按 `TOAST_MAP` 把错误码翻译成中文提示；message/modal 实例由 App 顶层注入（动态主题），未注入时回退静态实例。

### 18.2 TASK_CONFLICT 特殊处理

`TASK_CONFLICT` 走 `Modal.confirm`，让用户选择"查看运行中"或"留在编辑态"。`details` 含 `activeTaskId` / `activeName` / `activeState` / `startedAt`。处理器经 `registerTaskConflictHandler` 注入（避免 services 反向依赖 store）。

### 18.3 轮询失败处理

| 场景 | 策略 |
|---|---|
| 单次失败 | 静默吞掉（React Query retry 2 次后放弃） |
| 连续失败 | UI 顶部显示红色横幅（`connectionLost`） |
| 恢复成功 | 红条消失 |

### 18.4 空快照守门

`runtimeStore.pushStress()` 内置空快照守门：`uptimeSeconds=0 && actions 为空` 的快照会被静默丢弃，避免任务结束瞬间把最后的有效数据覆盖成空。
