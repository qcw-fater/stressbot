# 分布式压测管理系统 — 技术文档

> 本文档基于当前代码实现编写（2026-09 修订），反映 gRPC 控制面架构下 `admin/`、`agent/`、`controlplane/` 的真实结构。
> 总纲性说明另见 `README.md` 与 `AGENTS.md`；控制面 wire 契约以 `controlplane/proto/control.proto` 为准，浏览器管理面 API 契约以 `api/admin/openapi.yaml` 为准。

---

## 1. 系统概述

### 1.1 设计目标

- **分布式执行**：多个 Agent 节点并行执行压测，模拟大规模用户负载
- **集中管理**：Admin 统一调度任务、聚合指标、服务前端
- **三进程独立二进制**：`cmd/stressbot`（单机）、`cmd/agent`（Agent 节点）、`cmd/admin`（Admin 服务器），各自持有独立 TOML 配置
- **完整可观测性**：除压测指标（成功率/延迟/Apdex）外，还采集 Agent 所在物理机的系统指标（CPU/内存/网络/线程/协程）
- **简单可靠**：Admin-Agent 控制面为 gRPC 长连接（命令日志 + ACK + 重放），浏览器管理面为 HTTP 轮询；无外部消息队列/服务发现；历史归档使用 MySQL（可选），跨节点共享状态使用 Redis（可选）

### 1.2 通信面划分

Admin 同时开两个监听面（`admin/server.go:56-99` 双 listener 并行 Serve）：

| 面 | 协议 | 端口 | 对端 | 说明 |
|---|---|---|---|---|
| 浏览器管理面 | HTTP | 7718（默认） | 前端 | Web UI 静态托管 + `/sbot/` REST API（`admin/config.go:131`） |
| Agent 控制面 | gRPC | 7720（默认） | Agent 节点 | 会话/心跳/租约/命令/资源包下载/指标上报（`admin/config.go:136`） |

Agent **不开放本地 HTTP 控制面**：所有管理操作经 Admin 下发（`conf/agent.toml:5-6`）。可选 pprof 端口（默认 6060）上注册了 `/debug/pprof` 与 `/metrics` 路由，仅供本机诊断（`conf/agent.toml:68-73`、`cmd/agent/main.go:104-111`）。

### 1.3 系统拓扑

```
+───────────────────+
|   Web Frontend    |   React + Ant Design + Vite
|   :5173 (dev)     |   vite proxy -> admin :7718
+────────┬──────────+
         | /sbot/* (HTTP 轮询)
         v
+────────────────────────────────+
|         Admin Server           |   cmd/admin/main.go
|  管理面 :7718 (HTTP)           |
|  控制面 :7720 (gRPC)           |
|  任务调度 + 指标聚合 + MySQL 归档 |
+──┬──────────────────┬──────────+
   | gRPC 控制会话(双向流)| gRPC 指标流/资源包流
   v                  v
+────────+ +────────+       +────────+
|Agent 1 | |Agent 2 |  ...  |Agent N |   每个节点独立采集
+────────+ +────────+       +────────+   所在主机的系统指标
    |           |                |
    +────────── TCP/UDP ─────────+
               -> 游戏服务器
```

### 1.4 三个角色

| 角色 | 进程 | 配置文件 | 职责 |
|---|---|---|---|
| **Admin** | `cmd/admin/main.go` | `conf/admin.toml` | 浏览器管理 API、任务生命周期调度、Agent 注册表、命令日志与投递、压测/系统指标聚合、MySQL 历史归档、流程/动作/监听模板库 |
| **Agent** | `cmd/agent/main.go` | `conf/agent.toml` | 主动建立 gRPC 控制会话、接收并执行命令（含资源包下载校验）、驱动机器人、上报压测/系统指标 |
| **Frontend** | `cmd/web/` | — | Dashboard 监控、任务管理、节点管理、历史归档、资源管理（FlowEditor） |

---

## 2. 关键设计决策

### 2.1 Admin-Agent 通信：gRPC 长连接（Agent → Admin 主动外连）

Agent 主动拨号连接 Admin 控制面，无需在 Agent 侧开放任何入站端口（防火墙友好）。控制面三个服务定义于 `controlplane/proto/control.proto:7-17`：

| 服务 | 方法 | 流向 | 职责 |
|---|---|---|---|
| `AgentControlService` | `Session`（双向流） | Agent ⇄ Admin | Hello/Welcome 握手、心跳/租约、命令下发/ACK/回执、最终报告/确认 |
| `AgentBundleService` | `DownloadBundle`（服务端流） | Admin → Agent | 按 SHA-256 内容寻址分块下发任务资源包（`BundleRequest.offset` 支持断点续传） |
| `AgentMetricsService` | `Report`（客户端流） | Agent → Admin | 压测窗口指标与系统指标帧流式上报 |

可靠性语义：命令经 Admin 侧**有界命令日志**持久（`admin/command/memory.go:34-46`，容量 100000 条）写入后投递，Agent 以 `CommandAck` 确认、Admin 落终态并回 `CommandReceipt`；会话重连后按投递游标重放未决命令（`admin/command/bus.go:99-114`）。任务最终报告在 Agent 侧进入**待确认队列**（`agent/session/report_outbox.go:22-36`，容量 128），Admin `ReportAck` 确认前会话重建时重放。

### 2.2 前端获取指标：HTTP 轮询（Frontend → Admin）

前端轮询 `/sbot/metrics` 与 `/sbot/system`，Admin 返回基于 Agent 最新上报窗口聚合的快照；聚合结果含覆盖率信息（`reportingAgents`/`freshAgents`/`coverageRatio`，`admin/metrics/aggregator.go:25-35`）。历史趋势数据由前端自行累积，归档任务另有 `/sbot/history/{id}/timeseries`。

### 2.3 Agent 发现：配置文件指定

Agent 配置中指定 Admin 控制面地址（`agent.adminAddress`，host:port，`agent/config.go:61`），启动即外连；连接失败按指数退避 + 抖动重连（`agent/session/supervisor.go:31-85`），默认无限重试（`reconnectMaxRetries = -1`）。无需 etcd/consul。

### 2.4 任务分配：Admin 切分机器人编号区间

Admin 将总机器人数按各节点 `maxBots` 比例分配给空闲节点（余数按最大剩余小数补齐，`admin/task/assignment.go:79-123`）；任务 `debugMode=true` 时优先选**容量够且 maxBots 最小**的单节点，无单节点容量够时降级比例分配（`admin/task/assignment.go:48-68`）。全局并发度按各节点分得机器人数等比拆分、保底 1、总和守恒（`admin/task/distribution.go:69-124`）。

### 2.5 配置下发：内容寻址资源包（Admin 打包 → Agent 按需流式下载）

Admin 启动任务时把 flow.json、proto、Lua 脚本、`*_codec.json` 与 `errors.json` 构建为 Deflate zip，按内容 SHA-256 落盘复用（`admin/bundle/store.go:91-161`），随 `StartTask` 命令只下发**资源包描述符**（摘要 + 大小，`controlplane/proto/control.proto:130-133`）；Agent 收令后经 `DownloadBundle` 流式下载，支持 `.part` 断点续传、SHA-256 与大小校验、`.ready` 标记复用（`agent/bundle/cache.go:40-148`）。

### 2.6 系统监控独立采集与上报

Agent 常驻 `SystemMonitor`（`agent/metrics/system.go:85-102`）按 `metricsInterval` 采集主机/进程指标，经 `AgentMetricsService.Report` 客户端流持续上报——空闲期也上报；压测窗口指标只在任务运行期由 `StressReporter` 切窗上报（`agent/metrics/reporter.go:22-32`）。两类帧共用 latest-wins 单槽缓冲（`agent/metrics/stream.go:26-34`），断流期间被覆盖的帧计数进 `droppedIntervals` 随下一帧补账。

### 2.7 任务租约：Agent 侧自主判活

Admin 在 `Welcome` 与每条 `HeartbeatAck` 中下发租约截止时间（`controlplane/proto/control.proto:74-80`、`:92-96`）；Agent 断连后租约倒计时继续，超过 `leaseDuration`（默认 45s）仍未续租则取消当前任务（`agent/session.go:128-143`）。这让「任务生死」由 Agent 侧租约先接管，而不是等 Admin 的离线清理，两套判定窗口不重叠（`admin/config.go:38-47` 联动约束）。

---

## 3. 包结构

### 3.1 Admin（`admin/`）

```
admin/
  app.go                -- Server 装配：组件编排、终态归档、Agent 状态回调、autoStop/停止安全网/deadline 看门狗
  server.go             -- 双监听面生命周期：HTTP 管理面 + gRPC 控制面的 Serve/优雅关闭
  config.go             -- admin.toml 配置：server/controlPlane/mysql/redis/log/pprof + 联动校验
  agent/                -- Registry：注册、心跳健康判定（unhealthy/offline）、指标快照留存
  task/                 -- 任务域：Repository 状态机、Assignment 分配、Dispatch 下发、Completion 汇总、
  │                        分布式拆分（SplitGlobalValues/ScaleRampUp）、stage_plan、持久化
  command/              -- 控制命令：Bus（投递+LRU 缓存）、MemoryStore（有界命令日志）、Dispatcher（组装下发）
  bundle/               -- SHA-256 内容寻址资源包存储：Build 打包落盘、Open 限并发流式下发
  grpcapi/              -- 控制面 gRPC 服务：SessionRegistry/会话、control/bundle/metrics 三服务、转换与校验
  metrics/              -- 指标域：Ingestor（latest-wins 摄取）、AcceptanceService（窗口校验）、WindowStore、
  │                        Aggregator（聚合）、Sampler（10s 时序采样）
  history/              -- MySQL 历史归档 Store：归档事务、查询/对比/克隆、标签元数据、过期清理
  template/             -- 流程模板库（flow_template）与共享 Action/Listen 模板库
  mysql/                -- MySQL 连接打开 + schema_definition.go 内嵌 DDL（11 张表）
  apierror/             -- 浏览器管理面统一错误类型 + 预定义错误码
  httpapi/              -- /sbot/ HTTP 传输层：路由、参数编解码、OpenAPI 校验、panic 边界、Swagger UI
```

### 3.2 Agent（`agent/`）

```
agent/
  app.go                -- Agent 运行时：生命周期、任务槽位（预留/启动/释放）、executeTask、shutdown
  config.go             -- agent.toml 配置：[agent] 段解析校验 + 静态信息采集
  session.go            -- 单条控制会话：Hello/Welcome、四路循环编排、租约循环
  grpc_client.go        -- gRPC 连接监督入口（RunSupervisor 包装）
  grpc_convert.go       -- 域模型 ⇄ controlpb 转换
  types.go              -- Status（idle/busy）枚举
  session/              -- 会话基础设施：supervisor（重连退避）、transport（收发循环/心跳生产）、
  │                        report_outbox（最终报告待确认队列）
  command/              -- 命令执行器：串行队列、StartTask 三段式（预留/下载/启动）、结果 outbox
  bundle/               -- 本地内容寻址资源包缓存：Ensure 下载校验发布
  task/                 -- 任务执行：Assignment/CompletionReport 类型、Runner（加载资源→起 Manager）
  metrics/              -- SystemMonitor 采集、StressReporter/SystemReporter、LatestMetrics 流发送
```

### 3.3 控制面（`controlplane/`）

```
controlplane/
  proto/control.proto   -- 三服务 wire 契约（AgentControlService/AgentBundleService/AgentMetricsService）
  pb/                   -- protoc 生成代码（go_package: stressbot/controlplane/pb）
  monitor.go            -- monitor.CollectorSnapshot → controlpb.CollectorSnapshot 转换
```

---

## 4. 浏览器管理面 HTTP API

统一前缀 `/sbot/`，唯一契约 `api/admin/openapi.yaml`（Swagger UI 见 `/sbot/docs`，文档本体 `/sbot/openapi.yaml`，`admin/httpapi/routes.go:23-24`）。路由注册于 `admin/httpapi/routes.go:21-127`，全部 handler 经 panic recover + 请求 Schema 校验中间件包裹（`admin/httpapi/routes.go:114-126`）。

### 4.1 端点分组（62 个操作 / 45 个路径）

| 分组 | 数量 | 端点 |
|---|---|---|
| 任务 | 7 | `GET/POST /sbot/tasks`、`GET/DELETE /sbot/tasks/{id}`、`GET /sbot/tasks/{id}/config/{path}`、`POST /sbot/tasks/{id}/start`、`POST /sbot/tasks/{id}/stop` |
| 节点管理 | 5 | `GET /sbot/agents`、`GET/DELETE /sbot/agents/{id}`、`POST /sbot/agents/{id}/shutdown`、`POST /sbot/agents/shutdown-all` |
| 指标 | 7 | `GET /sbot/metrics`、`/sbot/metrics/summary`、`/sbot/metrics/agents`、`/sbot/metrics/agents/{id}`、`/sbot/system`、`/sbot/system/agents`、`/sbot/system/agents/{id}` |
| 历史归档 | 11 | `GET /sbot/history`、`/sbot/history/tags`、`/sbot/history/compare`；`GET/PUT/DELETE /sbot/history/{id}`；`GET /sbot/history/{id}/agents`、`/config`、`/config/archive`、`/timeseries`；`POST /sbot/history/{id}/clone` |
| 基线资源 | 7 | `GET /sbot/baseline/proto/index.json`、`/sbot/baseline/proto/{name}`、`scripts/index.json`、`scripts/{name}`、`adapter/index.json`、`adapter/{name}`、`flow/flow.json` |
| 流程模板 | 7 | `GET/POST /sbot/flows`、`GET/PUT /sbot/flows/snapshot`、`GET/PUT/DELETE /sbot/flows/{id}` |
| 动作模板 | 7 | `GET/POST /sbot/action-templates`、`GET/PUT /sbot/action-templates/snapshot`、`GET/PUT/DELETE /sbot/action-templates/{id}` |
| 监听模板 | 7 | `GET/POST /sbot/listen-templates`、`GET/PUT /sbot/listen-templates/snapshot`、`GET/PUT/DELETE /sbot/listen-templates/{id}` |
| 其它 | 4 | `GET /sbot/api/error-codes`、`GET /sbot/capabilities`、`POST /sbot/codec/preview`、`GET /sbot/codec/algorithms` |

另有 `GET /sbot/docs`（Swagger UI）、`GET /sbot/openapi.yaml` 与根路径静态资源托管（`admin/httpapi/routes.go:111-112`）。任务创建仍走 `multipart/form-data`（上限 32MB，`admin/httpapi/task_routes.go:26-63`），上传资源同步写入磁盘基线目录（`admin/httpapi/task_routes.go:212-213`）。

### 4.2 错误响应

统一 `apierror.Error{code, message, details}` 结构（`admin/apierror/errors.go`），预定义 21 个错误码（`TASK_NOT_FOUND`/`TASK_CONFLICT`/`AGENT_NOT_FOUND`/`HISTORY_DISABLED`/`SHARED_STATE_UNAVAILABLE`/`FLOW_TEMPLATE_NOT_FOUND`/`TEMPLATE_NAME_CONFLICT` 等，`admin/apierror/errors.go:39-59`）。

---

## 5. 数据模型（核心）

### 5.1 Task 与状态机（`admin/task/types.go:17-90`）

```go
type Task struct {
    ID, Name        string
    State           State        // pending/starting/running/stopping/stopped/failed
    TotalBots       int
    Config          Config       // FlowJSON/ProtoFiles/LuaScripts/Codecs/ErrorMap/RobotConfig/Deadline
    Assignments     []Assignment // 分配快照
    SucceededAgents []string     // 实际成功接收任务的节点（部分节点可能推送失败）
    Reports         map[string]CompletionReport // agentID → 最终报告
    CleanupSummary  *robot.CleanupStatus        // 全节点清理状态汇总
    StageReports    []CompletionReport          // reset 边界阶段段落报告
    AgentEvents     []AgentEvent                // 任务期间节点状态变化事件
    SharedUsed      bool                        // 脚本是否使用 require("share")（启动时自动检测）
    SharedRunID     string                      // 共享状态命名空间（= 任务 ID）
    FlowTemplateID  string                      // 来源流程模板（溯源用逻辑外键）
    ...
}
```

状态转换矩阵（`admin/task/store.go:267-280`）：

| from \ to | starting | running | stopping | stopped | failed |
|---|---|---|---|---|---|
| **pending** | YES | | | YES | YES |
| **starting** | | YES | | | YES |
| **running** | | | YES | YES | YES |
| **stopping** | | YES（停止命令创建失败回滚） | | YES | YES |

**Active 状态定义**（占据单例位，`admin/task/types.go:33-35`）：`starting`、`running`、`stopping`。`Begin`（`admin/task/store.go:249-265`）以 `startMu` 串行化 pending→starting，与 `activeID` 登记同锁完成（`admin/task/store.go:188-211`），保证全集群同时最多一个活跃任务。

### 5.2 CompletionReport（`admin/task/types.go:254-276`）

Agent 最终报告：`Result`（completed/stopped/failed）、`ErrorMsg`、`FinalSnapshot`（含可合并 DDSketch，公共 API 经 `Task.PublicCopy()` 剥离，`admin/task/types.go:281-306`）、`StageIndex`（>0 为 reset 阶段段落报告）、`CleanupStatus`（Agent 侧机器人清理结果）。

### 5.3 Dispatch 与 Assignment

- `Assignment`：任务分配快照（agentID、startNumber、totalBots，`admin/task/types.go:192-203`）。
- `Dispatch`：Admin → Agent 的任务运行参数（含 `StartIndex` 全局机器人序号起点与 `Shared` 共享状态运行时下发，`admin/task/types.go:206-240`），经 `grpcapi.TaskAssignmentToProto` 转 proto（`admin/command/dispatcher.go:66`）。

### 5.4 Agent 节点模型（`admin/agent/types.go`）

- 状态枚举：`idle` / `busy`（Agent 上报）/ `unhealthy`（心跳超 unhealthyAfter）/ `offline`（超 offlineAfter，标记后**立即从注册表删除**，`admin/agent/registry.go:429-437`）。
- 节点静态信息 `StaticInfo`：hostname、os、arch、cpu_count、mem_total_bytes、go_version、started_at（`controlplane/proto/control.proto:267-276`）；`kernel_version` 字段保留但 Agent 未采集赋值（`agent/metrics/types.go:29-38`）。
- 心跳语义字段 `CurrentTaskID`/`CurrentBots` 只能由心跳路径更新；任务切换时清空 `LatestStress` 防串数据（`admin/agent/registry.go:136-140`）。

---

## 6. 压测指标链路

### 6.1 Agent 侧

- `StressReporter`（任务期）：按 `metricsInterval` 从 `MetricsCollector` 切**顺序区间窗口**（`TakeReportSnapshot`，窗口序列号从 1 递增），Stop 时同步冲刷最后一帧（`agent/metrics/reporter.go:48-79`）。
- `SystemReporter`（常驻）：按同周期递交系统快照（`agent/metrics/reporter.go:82-114`）。
- `LatestMetrics`：每类指标单槽 latest-wins 缓冲，发送循环被新帧唤醒后经 `AgentMetricsService.Report` 流发出；发送失败把帧放回槽位重试，覆盖丢弃计数随下一帧带给 Admin 补账（`agent/metrics/stream.go:41-105`）。

### 6.2 Admin 侧

- `Ingestor`：gRPC Recv 只做身份校验与槽位替换，昂贵的转换/校验/落库在有界 worker 集上执行（`admin/metrics/ingestor.go:26-42`）。
- `AcceptanceService`：校验节点存在、任务归属与上报周期，窗口序列号重复的帧判 duplicate 丢弃；任务已切换时静默丢弃（`admin/metrics/accept.go:37-63`）。
- `WindowStore`：按任务×节点维护最新累计/窗口对（`admin/metrics/window_store.go:41-53`）。
- `Aggregator`：合并窗口快照 → 集群累计 + 覆盖率（fresh/stale 判定取 `max(3×上报周期, 15s)`，`admin/metrics/aggregator.go:37-80`）；DDSketch 严格合并后重算百分位，空分布展示值为 null。

### 6.3 聚合规则

| 指标类型 | 聚合方式 |
|---|---|
| 计数器（success/failure/timeout/canceled） | 求和 |
| 延迟直方图（RTT/ListenWait/TotalDuration） | 合并 DDSketch → 重算 P50/P90/P95/P99（1% 相对精度） |
| Apdex | 合并后的 satisfied/tolerating 重算，仅 networked 动作打分 |
| 错误分布 | 相同 `code` 计数相加 |
| 系统指标 | CPU 均值/最大、内存求和、网络速率求和（`ClusterSystemSnapshot`） |

---

## 7. Agent 系统监控

`SystemSnapshot` 字段（`agent/metrics/types.go:10-26`）：timestamp、sequence、host_cpu_percent、host 内存总量/已用/百分比、主机网络收发速率（差分）、process_cpu_percent（按核数归一）、process_rss_bytes、process_heap_bytes、process_goroutines、process_threads、process_fds。

采集要点（`agent/metrics/system.go:154-210`）：

- CPU 百分比首个采样只建基线，第二次起才有值（gopsutil 语义）
- 主机内存用 `total - available` 计算已用量
- 网络速率为主机级计数器差分；计数器回绕/时间倒退时置空（`counterRate`）
- 快照携带递增 `sequence`；Admin 侧 `UpdateSystem` 对乱序/重复帧按序列号去重（`admin/agent/registry.go:365-375`）

---

## 8. 任务生命周期

```
1. 创建任务
   前端 POST /sbot/tasks (multipart: flow.json + proto/* + scripts/* + adapter/* + robotConfig)
   -> flow/codec Schema 校验（config/validation）
   -> rampUp 校验（各阶段 count 之和 == totalBots）
   -> Admin 创建 Task(state=pending) 并落盘 data/tasks/{id}.json
   -> 资源写入磁盘基线目录（conf/）

2. 启动任务
   前端 POST /sbot/tasks/{id}/start
   -> Store.Begin()：startMu 串行化 + 单活跃任务约束 + pending→starting
   -> 共享状态检测：脚本使用 require("share") 但未配 Redis → starting→failed 拒绝
   -> Assigner.Assign()：空闲节点比例分配（或 debugMode 单节点）
   -> ValidateDistributedConcurrency（并发度 >= 节点数）
   -> 保存 assignments、启动 Sampler
   -> 异步 startTaskBackground：
        a. bundle.Store.Build() 打包资源（SHA-256 内容寻址）
        b. 按 SplitGlobalValues 拆分并发度、ScaleRampUp 缩放 rampUp
        c. 组装 StartTask 命令批量写入命令日志并投递
        d. 成功节点记入 SucceededAgents → starting→running
        e. 命令创建失败 → starting→failed + 停 Sampler

3. Agent 执行（命令三段式）
   a. Executor 收到 StartTask 命令 → 预留任务槽位（idle→busy）
   b. bundle.Cache.Ensure()：内容寻址下载（.ready 复用 / .part 断点续传 / SHA-256 校验）
   c. 启动 executeTask → Runner.Run：
        加载 codec resolver → 编译 proto → 构建流程 → 起网络引擎/Lua 池
        → robot.Manager.StartAll（或 StartWithRampUp）→ 等待完成
   d. StressReporter 任务期切窗上报；SystemReporter 常驻上报
   e. 结束：StopAll 采集清理状态 → 最终报告进 ReportOutbox 等 Admin 确认

4. 前端监控
   前端轮询 GET /sbot/metrics（压测聚合）与 GET /sbot/system（系统聚合）

5. 停止任务
   前端 POST /sbot/tasks/{id}/stop（仅 running 可停）
   -> running→stopping → 为目标节点批量创建 StopTask 命令
   -> 离线节点立即合成 stopped report；全部已报 → stopping→stopped
   -> 否则启动 60s 停止超时安全网（覆盖 Agent closeRobots 15s + Robot.Close 10s + 上报延迟，
      admin/app.go:616-620）
   -> Agent：任务 ctx 取消 → StopAll → 最终报告（result=stopped）→ 等 ReportAck

6. 终态归档
   Transition 到 stopped/failed → onTerminal 回调（TerminalClone 深拷贝 + 协程池异步）
   -> 停 Sampler → 清理该任务 Redis 共享状态命名空间（登记待清理队列，失败重试）
   -> 优先用 Agent 终止报告聚合最终快照，兜底用窗口聚合
   -> HistoryStore.Archive()：事务写入 MySQL 归档表
```

自然完成路径：`CompletionService.Accept` 收最终报告 → 全部预期节点（`SucceededAgents`，空则回退 Assignments）汇报完毕 → 聚合清理摘要 → running/stopping → stopped（`admin/task/completion.go:59-100`）。

---

## 9. 任务分配算法（`admin/task/assignment.go`）

1. 过滤 `status=idle` 且 `maxBots > 0` 的节点；无可用节点 → `CAPACITY_EXCEEDED`
2. 总容量校验：`sum(maxBots) >= task.TotalBots`
3. `debugMode=true`：在容量足够的节点中选 `maxBots` 最小者单点全量分配；无单点容量够 → 降级比例分配
4. 比例分配（`proportionalAssign`，`admin/task/assignment.go:79-123`）：
   - 基础数 = `totalBots × agentMaxBots / totalCapacity`（下取整）
   - 余数按「最大剩余小数」逐个 +1，保证 `sum(bots) == totalBots`
   - 各节点 `StartNumber` 从任务起点累加；`StartIndex`（全局 0-based 序号）随 Dispatch 下发（`admin/task/assignment.go:74-76`）

分布式并发度拆分（`admin/task/distribution.go:69-124`）：每节点保底 1、按小数补齐不足、从分得最多的节点回收超发，总和守恒；`ValidateDistributedConcurrency` 在入口拒绝显式配置 `0 < concurrency < 节点数` 的组合（`admin/task/distribution.go:30-47`）。

---

## 10. 命令与投递可靠性

```
Dispatcher.ScheduleStart/ScheduleStop/ScheduleShutdown
   │ 组装 controlpb.Command（含全局递增 Sequence）
   v
command.Bus.CreateBatch ──► MemoryStore（有界命令日志，容量 100000，落终态 FIFO 淘汰）
   │ 唤醒目标节点会话
   v
Session 发送循环（CommandDispatchWindow=16 在途窗口，admin/grpcapi/session.go:134）
   │ stream: AdminEvent.Command（携带 delivery_generation）
   v
Agent Executor（串行队列）──► CommandAck（applied/duplicate/rejected）
   │                                    │
   │                                    v
   │                       Admin Bus.Acknowledge ──► Store 落终态 ──► CommandReceipt 回执
   v                                                                    │
任务执行 / 关机延迟到 Receipt 到达（agent/command/executor.go:223-226）◄─┘
```

- 会话重连：`Attach` 递增 generation 顶替旧会话（`admin/grpcapi/session.go:154-177`）；投递游标 `lastSent` 重置，`PendingBatch` 重放未 ACK 命令（`admin/command/bus.go:99-114`）。
- Agent 幂等：同 `commandId` 重放直接回放既有结果（`agent/command/executor.go:96-98`）；重复 StartTask 且任务相同 → `DUPLICATE`。
- Shutdown 命令的进程退出延迟到 Admin 持久化 ACK 并回 `CommandReceipt` 之后（`controlplane/proto/control.proto:122-128` 注释、`agent/command/executor.go:223-226`），避免 ACK 丢失导致 Admin 重发时进程已死。

---

## 11. 配置文件（TOML）

三个进程均为 TOML 配置，加载路径 `config.LoadTOML`（`config/load.go:30-46`）：

- 严格模式：未知字段（拼写错误）导致启动失败（`DisallowUnknownFields`，`config/load.go:37`）
- 环境变量展开：`${VAR}`（未定义报错）、`${VAR:-default}`、`$$` 字面转义（`config/load.go:68` 起 `ExpandConfigStrings`）
- 时长类配置统一 `config.ParseDuration` / `ParsePositiveDuration`（`config/duration.go:21-50`）：**留空回退默认，写错一律报错**

### 11.1 Admin（`conf/admin.toml`）

| 段 | 关键项 | 默认 |
|---|---|---|
| `[server]` | port / listenHost / staticDir | 7718 / 127.0.0.1 / cmd/web/dist |
| `[controlPlane]` | port / listenHost | 7720 / 127.0.0.1 |
| | heartbeatInterval / unhealthyAfter / leaseDuration / offlineAfter | 10s / 30s / 45s / 60s |
| `[mysql]` | host（留空=不启用）/ port / username / password / database / retentionDays / [mysql.pool] | 90 天 |
| `[redis]` | host（留空=不启用）/ keyPrefix / [redis.pool] | — |
| `[log]` `[pprof]` `daemon` | 常规 | — |

controlPlane 四时长强校验（`admin/config.go:189-219`）：`heartbeatInterval < unhealthyAfter < offlineAfter` 且 `heartbeatInterval < leaseDuration < offlineAfter`。

### 11.2 Agent（`conf/agent.toml`）

| 项 | 默认 | 说明 |
|---|---|---|
| `agent.id` | 必填 | 节点全局唯一 ID |
| `agent.adminAddress` | 必填 | Admin 控制面 gRPC 地址（host:port） |
| `agent.name` | 留空=hostname，再空=id | 显示名 |
| `agent.maxBots` | 5000 | 单节点最大机器人数 |
| `agent.heartbeatInterval` | 10s | 心跳间隔（Welcome 下发值优先） |
| `agent.metricsInterval` | 5s | 压测/系统指标上报周期 |
| `agent.reconnectMaxRetries` | -1 | 0 视为 -1（`agent/config.go:159-164`） |
| `agent.taskWorkDir` | 留空=系统临时目录 | 资源包缓存根 |
| `agent.reconnect.initialInterval / maxInterval / taskReportTimeout` | 5s / 30s / 30s | 重连退避与报告确认超时 |

### 11.3 单机（`conf/stressbot.toml`）

`log`/`monitor`/`pprof`/`standalone`（`bot`/`stateExtra`/`rampUp`）+ 可选 `redis`/`network`/`daemon`；资源路径（flow/proto/scripts/adapter）为 CLI flag。详见 `AGENTS.md`。

---

## 12. 历史归档（MySQL）

### 12.1 表结构（11 张，`admin/mysql/schema_definition.go:8-172`）

```
task_history          -- 主表：state、起止时间、active_agent_count、stage_count、flow_template_id
task_assignment       -- 集群分配快照（每节点一条）
task_report           -- 节点完成报告 + final_snapshot/cleanup_status（JSON）+ stage_index
task_aggregated       -- 集群聚合终态快照（复合主键 task_id + stage_index）
task_timeseries       -- 运行期采样（Sampler 10s；含窗口指标列与 uq_task_history_batch 幂等键）
task_config_archive   -- 配置归档（flow/proto/scripts/codecs/error_map/robot_config）
task_agent_events     -- 节点状态变化事件
task_meta             -- 收藏/标签/备注（(task_id, stage_index) 键，-1=任务级）
flow_template         -- 流程模板库
action_template       -- 共享动作模板库（name 唯一）
listen_template       -- 共享监听模板库（name 唯一）
```

- 内嵌 Go DDL 定义首版当前结构，**不维护数据库迁移版本**（`admin/mysql/schema_definition.go`，`conf/admin.toml:84` 注释）
- 只用逻辑外键，无 FOREIGN KEY；级联删除由应用层处理
- `retentionDays`（默认 90）过期裁剪，24h 周期执行；starred 不清理（`admin/history/store.go:1451`、`:1524`）

### 12.2 归档数据流

- 运行期：任务 starting → Sampler 启动（10s 间隔），聚合压测+系统指标写 `task_timeseries`，reset 任务采样点按段号写入
- 终态：onTerminal → 停 Sampler → 清理共享状态 → 异步 Archive（优先 Agent 终止报告聚合、兜底窗口聚合）
- 标记：`task_meta` 支持任务级与 reset 段落级 starred/tags/note（`UpsertMeta`，`admin/history/store.go:1183`）
- 克隆/对比：`POST /sbot/history/{id}/clone` 创建 pending 新任务（不自动启动）；`GET /sbot/history/compare` 多任务对比
- MySQL 未配置（host 空）时 HistoryStore 为 nil，`/sbot/history*` 返回 `HISTORY_DISABLED`（`admin/app.go:192-195`）

---

## 13. 共享状态（Redis，可选）

- Admin 配置 `[redis]` 且任务脚本使用 `require("share")` 时，StartTask 命令随附 `SharedRuntimeAssignment{runId, redis 配置}`（`admin/command/dispatcher.go:41-48`）；runId = 任务 ID，同一任务所有节点同命名空间
- 启动前检测：脚本用 share 但未配 Redis → 任务直接 failed（`admin/httpapi/task_routes.go:395-402`）
- 任务终态由 **Admin 统一清理**该 runId 命名空间（登记 `SharedCleanup` 待清理队列，立即尝试 + 定时/重启重试，`admin/app.go:372-386`），避免多节点各自清理的竞态
- Agent 侧只负责连接生命周期（Close），不做删除（`agent/task/runner.go:173-199`）

---

## 14. 故障处理（概要，详见 `docs/distributed-error-handling.md`）

| 故障 | 行为 |
|---|---|
| Admin 挂掉、Agent 空闲 | gRPC 重连退避（5s 起倍增至 30s 上限 + 抖动），SystemReporter 持续采集本地缓存 |
| Admin 挂掉、Agent 执行中 | 租约倒计时继续，超 `leaseDuration`（45s）未续租 → 取消任务（`agent/session.go:128-143`）；未确认报告留在 outbox，重连后重放 |
| Admin 重启 | 加载时活跃任务重置 failed（`admin/task/store.go:57-66`）并补归档；Agent 重连后 Hello 携带 currentTaskId，Admin 发现孤儿任务即下发 StopTask（`admin/grpcapi/control_service.go:106-123`） |
| Agent 宕机 | unhealthyAfter(30s)→unhealthy；offlineAfter(60s)→offline 并删除；任务 stopping 中合成 report，running 中检查是否全部节点失效 |
| Agent 进程重启（同 ID 新实例） | Hello 的 StartedAt 变化触发 `onRestart`：记录 restarted 事件、合成 failed report、检查全失效（`admin/agent/registry.go:90-94`、`admin/app.go:522-546`） |
| 全部分配节点失效 | `autoStopTask`：starting→failed；running→stopping→（stop 命令）→failed（`admin/app.go:720-788`） |
| 停止超时 | 60s 安全网为未上报节点合成 stopped report（`admin/app.go:637-683`） |
| deadline 超时 | 5s 看门狗检测 → autoStopTask（`admin/app.go:802-828`） |

---

## 15. 部署防火墙要求

| 方向 | 端口 | 说明 |
|---|---|---|
| 前端 → Admin | 7718/tcp | REST API + 静态文件托管 |
| Agent → Admin | 7720/tcp | gRPC 控制面（会话/命令/资源包下载/指标上报） |
| Admin → MySQL | 3306/tcp | 历史归档（可选） |
| Agent/单机 → Redis | 6379/tcp | 共享状态（可选；Redis 密码随控制面内网下发，控制端口必须只在受控私网可达） |
| Agent → 游戏服务器 | 按协议 | TCP/UDP 压测流量 |

---

## 16. 本机模拟分布式验证

```bash
go build -o admin.exe ./cmd/admin
go build -o agent.exe ./cmd/agent

# 终端 1：Admin
./admin.exe -config conf/admin.toml

# 终端 2/3：两个 Agent 节点（各自 conf 中 agent.id 不同即可）
./agent.exe -config conf/agent.toml

# 终端 4：操作
curl http://localhost:7718/sbot/agents
curl -X POST http://localhost:7718/sbot/tasks ...        # multipart 创建
curl -X POST http://localhost:7718/sbot/tasks/{id}/start
curl http://localhost:7718/sbot/metrics
curl http://localhost:7718/sbot/system
curl -X POST http://localhost:7718/sbot/tasks/{id}/stop
```
