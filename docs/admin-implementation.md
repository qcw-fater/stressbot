# Admin 服务端实施文档

> **角色定位**：Admin 是分布式压测系统的**控制中枢**，对浏览器提供管理面 HTTP API（`/sbot/`），对 Agent 节点提供 gRPC 控制面（会话/命令/资源包/指标）。
> **本文档目标读者**：负责 `admin/` 包及 `cmd/admin/` 入口的开发者。
> **文档性质**：基于当前代码编写（2026-09 修订），标注 `文件:行号` 便于对照。
> 姊妹文档：`docs/agent-implementation.md`（Agent 端，协议契约可双向交叉验证）。

---

## 0. 文档约定

- 项目名/Go module：`stressbot`
- Admin 二进制：`admin.exe`（Linux：`admin`），来自 `cmd/admin`
- 监听面：浏览器管理面 HTTP **7718**（默认）、Agent 控制面 gRPC **7720**（默认）
- 配置文件：`conf/admin.toml`（TOML）
- 数据目录：`data/`（任务 JSON 持久化）、`data/bundles/`（资源包存储）
- 静态资源目录：`cmd/web/dist/`（前端构建产物）
- HTTP 路由前缀：`/sbot/`；OpenAPI 契约：`api/admin/openapi.yaml`

---

## 1. 模块职责

Admin 同时承担六类职责：

1. **集群管理**：Agent 注册表（gRPC 会话握手注册、心跳健康检测、实例重启识别）
2. **任务编排**：接收前端请求 → 单例校验 → 分配 → 组装命令 → 投递 → 汇总完成报告
3. **命令可靠性**：有界命令日志 + 会话投递窗口 + ACK/回执 + 重连重放
4. **指标聚合**：摄取 Agent 上报的压测窗口/系统指标，合并为集群快照供前端轮询
5. **历史归档**：运行期 10s 时序采样 + 终态归档至 MySQL，支持回查、对比、克隆、标签；流程/动作/监听模板库
6. **运维支持**：节点远程关机命令、前端静态资源托管、基线资源读写、codec 预览

不做的事：

- 不做用户认证（内网工具，假定可信网络；但 Redis 密码随控制面下发，控制端口必须只在受控私网可达）
- 不做任务排队调度（单活跃任务约束，并发启动直接拒绝 409）
- 不做指标告警
- 不做二进制存储与自动升级编排（升级为运维手动重启 Agent 进程）

---

## 2. 包结构与文件清单

```
admin/
  app.go                -- Server 装配与生命周期、终态归档、Agent 状态回调、autoStop/停止安全网/deadline 看门狗
  server.go             -- 管理面 HTTP 服务器构造、双监听面 Serve 与优雅关闭
  config.go             -- admin.toml 配置解析、默认值、controlPlane 联动强校验
  agent/
    registry.go         -- Registry：注册/心跳/Touch/健康扫描/状态恢复/指标快照留存
    types.go            -- Node、Status 枚举、HeartbeatRequest 等
  task/
    types.go            -- Task/State/Config/RobotConfig/Assignment/Dispatch/CompletionReport 等领域类型
    store.go            -- 泛型 Repository：并发安全任务存储 + 生命周期状态机（Transition/Begin）
    admin_store.go      -- Store 适配层：把 Task 接到 Repository Adapter（含重启恢复语义）
    assignment.go       -- Assigner：空闲节点比例分配 + debugMode 单节点优先
    distribution.go     -- ExpectedAgents、并发度等比拆分、ScaleRampUp、ValidateDistributedConcurrency
    completion.go       -- CompletionService：最终/阶段报告汇总与全员完成流转
    stage_plan.go       -- reset 边界 → 连续 1-based 段落号映射（归档对齐）
    persist.go          -- data/tasks/{id}.json 原子持久化与加载
    shared_cleanup.go   -- 共享状态待清理队列（持久化 + 定时重试）
  command/
    bus.go              -- Bus：命令写入、LRU 缓存解析、会话投递、ACK 落终态
    memory.go           -- MemoryStore：内存有界命令日志（全局递增 Sequence、容量淘汰）
    store.go            -- Store 接口
    dispatcher.go       -- Dispatcher：ScheduleStart/ScheduleStop/ScheduleShutdown 组装编排
  bundle/
    store.go            -- SHA-256 内容寻址 zip 存储：Build 打包落盘、Open 限并发流式下发
  grpcapi/
    server.go           -- gRPC 服务器构造（16MB 消息上限、keepalive）
    control_service.go  -- AgentControlService.Session：握手/注册/孤儿停止/收发循环
    bundle_service.go   -- AgentBundleService.DownloadBundle 服务端流
    metrics_service.go  -- AgentMetricsService.Report 客户端流
    session.go          -- Session（投递窗口/事件通道）+ SessionRegistry（generation 顶替）
    convert.go          -- 域模型 ⇄ controlpb 转换
    validation.go       -- requireAgentID 等入参校验
  metrics/
    ingestor.go         -- Ingestor：latest-wins 槽位 + 有界 worker 转换落库
    accept.go           -- AcceptanceService：压测帧归属校验
    window_store.go     -- WindowStore：任务×节点窗口/累计状态
    aggregator.go       -- Aggregator：集群压测聚合（含覆盖率）与系统聚合
    sampler.go          -- Sampler：运行期 10s 时序采样
    types.go            -- HistoryTrendPoint 等展示类型
  history/
    store.go            -- MySQL 归档/查询/对比/克隆/标签/清理
    types.go            -- Record/Detail/Filter 等归档模型
    policy.go           -- 归档策略
  template/
    flow_template.go    -- 流程模板库（flow_template 表）
    action_template.go  -- 共享动作模板库
    listen_template.go  -- 共享监听模板库
    *_snapshot.go       -- 三类库的快照整体替换实现
    common.go           -- 模板库公共逻辑
  mysql/
    open.go             -- 连接池打开（DSN 用驱动 FormatDSN 生成）
    schema.go           -- InitializeSchema：启动建表/结构校验
    schema_definition.go-- 内嵌 DDL：11 张表首版当前结构
  apierror/
    errors.go           -- Error 类型 + 21 个预定义错误码
  httpapi/
    routes.go           -- 全部 /sbot/ 路由注册 + recover/Schema 校验中间件 + capabilities
    handler.go          -- Handler 与 Dependencies
    task_routes.go      -- 任务创建/列表/详情/配置下载/启动/停止/删除
    agent_routes.go     -- 节点列表/详情/删除/关机
    metrics_routes.go   -- 压测/系统聚合查询
    history.go          -- 历史归档端点
    resources.go        -- 基线资源端点
    templates.go        -- 流程/动作/监听模板端点
    codec.go            -- codec 预览与算法元数据
    baseline_routes.go  -- 基线目录布局
    errors.go           -- writeError/writeJSON 等
    middleware.go       -- panic 边界 + 请求校验包裹
    docs.go openapi.go  -- Swagger UI / openapi.yaml 分发
    template_helpers.go -- 展示辅助

cmd/admin/
  main.go               -- 进程入口（顶层 recover、-d 守护进程、TOML 加载、日志、pprof）
```

**关键外部依赖**：`google.golang.org/grpc`（控制面）、`github.com/go-sql-driver/mysql`（DSN 生成 + 驱动）、`go.uber.org/zap`（结构化日志）。ID 生成用 `crypto/rand` 32 字符 hex（`admin/app.go:790-799`），数据库访问用标准 `database/sql`。

---

## 3. 数据模型（核心 DTO）

### 3.1 Task（`admin/task/types.go:50-90`）

```go
type Task struct {
    ID          string        `json:"id"`              // 随机 32 字符 hex
    Name        string        `json:"name"`
    State       State         `json:"state"`
    TotalBots   int           `json:"totalBots"`       // 集群总机器人数
    Config      Config        `json:"config"`
    Assignments []Assignment  `json:"assignments,omitempty"`   // 分配快照
    SucceededAgents []string  `json:"succeededAgents,omitempty"` // 实际成功接收命令的节点
    Reports     map[string]CompletionReport `json:"reports,omitempty"` // agentID → 最终报告
    CleanupSummary *robot.CleanupStatus      `json:"cleanupSummary,omitempty"`
    StageReports []CompletionReport          `json:"stageReports,omitempty"` // reset 段落报告
    AgentEvents  []AgentEvent                `json:"agentEvents,omitempty"`
    CreatedAt   time.Time     `json:"createdAt"`
    StartedAt   *time.Time    `json:"startedAt,omitempty"`
    StoppedAt   *time.Time    `json:"stoppedAt,omitempty"`
    ErrorMsg    string        `json:"errorMsg,omitempty"`
    SharedUsed  bool          `json:"sharedUsed,omitempty"`  // 脚本使用 require("share")
    SharedRunID string        `json:"sharedRunId,omitempty"` // = 任务 ID
    FlowTemplateID string     `json:"flowTemplateId,omitempty"` // 来源模板（溯源）
}
```

`PublicCopy()` 返回剥离内部 DDSketch 的浅拷贝，供面向浏览器的响应使用——sketch 字节只允许出现在 Agent→Admin 内部链路与归档中（`admin/task/types.go:281-306`）。

### 3.2 State / Result（`admin/task/types.go:17-47`）

```go
Pending / Starting / Running / Stopping / Stopped / Failed
IsActiveState = Starting || Running || Stopping        // 占据单例位
ResultCompleted / ResultStopped / ResultFailed
```

### 3.3 Config 与 RobotConfig（`admin/task/types.go:93-155`）

```go
type Config struct {
    FlowJSON    json.RawMessage   `json:"flowJson"`
    ProtoFiles  map[string][]byte `json:"protoFiles,omitempty"`
    LuaScripts  map[string][]byte `json:"luaScripts,omitempty"`
    Codecs      map[string][]byte `json:"codecs,omitempty"`   // *_codec.json（每连接一份）
    ErrorMap    []byte            `json:"errorMap,omitempty"` // 共享 errors.json
    RobotConfig RobotConfig       `json:"robotConfig"`
    Deadline    *time.Time        `json:"deadline,omitempty"`
}
```

`RobotConfig`：Concurrency、TimeoutSec、AccountPrefix、StartNumber、MainService、StateExtra、HeartbeatSec、HTTPTimeoutSec、ApdexT（毫秒）、DebugMode、LogLevel、RampUp。Config 同时实现 `bundle.Source` 接口供打包（`admin/task/types.go:110-123`）。

### 3.4 Assignment / Dispatch / SharedRuntimeAssignment（`admin/task/types.go:191-249`）

- `Assignment`：TaskID、AgentID、AgentName、StartNumber（账号编号基数上累加）、TotalBots。
- `Dispatch`（下发参数）：含 `StartIndex`（本节点在任务全局机器人序号中的 0-based 起点）、缩放后的 `RampUp`、可选 `Shared`（Redis 运行时下发，密码随内网控制面传输）。
- 超时字段统一 int 秒数存储，Dispatcher 转为 `"Ns"` duration 字符串下发（`admin/command/dispatcher.go:59-61`）。

### 3.5 CompletionReport（`admin/task/types.go:253-276`）

AgentID、TaskID、Result、ErrorMsg、FinishedAt、FinalSnapshot（整体终态或 reset 段落终态快照）、StageIndex（-1/0=最终；>0=reset 段落，值为「即将进入的配置阶段下标」，归档时映射为连续 1-based 段落号）、CleanupStatus（Agent 侧清理结果，`robot.CleanupStatus`）。

### 3.6 AgentEvent（`admin/task/types.go:178-189`）

Type ∈ `offline`（心跳超时）/ `reconnected`（分配节点恢复）/ `restarted`（节点进程实例已变化，任务丢失）。

### 3.7 聚合结果（`admin/metrics/aggregator.go:24-35`）

```go
type StressAggregate struct {
    Snapshot        *monitor.CollectorSnapshot
    ReportingAgents int     // 有窗口状态的节点
    TotalAgents     int     // 非离线节点
    OfflineAgents   int
    AssignedAgents  int     // CurrentTaskID 匹配的节点
    FreshAgents     int     // 窗口新鲜（<= max(3×上报周期, 15s)）
    StaleAgents     int
    CoverageRatio   float64 // 数据覆盖率
    AsOf            time.Time
}
```

系统聚合为 `metrics.ClusterSystemSnapshot`（CPU 均值/最大、内存求和、网络速率求和、在线/离线计数）。

### 3.8 历史归档模型（`admin/history/types.go`）

`Record`（列表行，含 starred/tags/note、activeAgentCount、stageCount）、`Detail`（完整内部数据）、`DetailResponse`（界面消费摘要）、`ConfigSummary`、`Filter`（state/时间范围/tags/tagsAll/starred/search/分页/排序白名单）。

---

## 4. 核心组件设计

### 4.1 Server 装配（`admin/app.go:43-223`）

```go
type Server struct {
    cfg Config
    tasks           *admintask.Store
    agents          *agent.Registry
    aggregator      *metrics.Aggregator
    metricsWindows  *metrics.WindowStore
    assigner        *admintask.Assigner
    bundles         *bundle.Store
    sessions        *grpcapi.SessionRegistry
    commandStore    command.Store
    commandBus      *command.Bus
    commandDispatch *command.Dispatcher
    completion      *admintask.CompletionService
    metricsAccept   *metrics.AcceptanceService
    metricsIngestor *metrics.Ingestor
    history         *history.Store              // 可选（MySQL 启用时）
    flows           *template.FlowTemplateStore // 可选
    actionTemplates *template.ActionTemplateStore
    listenTemplates *template.ListenTemplateStore
    sampler         *metrics.Sampler            // 可选
    db              *sql.DB                     // 全局共享 MySQL 连接池
    sharedCleanup   *admintask.SharedCleanup    // 共享状态待清理队列（Redis 启用时）
    managementSrv   *http.Server
    grpcSrv         *grpc.Server
    ...
}
```

**装配顺序**（`NewServer`，`admin/app.go:81-223`）：

1. MySQL（host 非空时）：`mysql.Open` + PING + `InitializeSchema`（首版当前结构，无迁移链）；失败即退出，不提供半健康服务
2. 任务存储 `admintask.NewStore("data")`（加载恢复，活跃任务重置 failed）；资源包存储 `bundle.NewStore("data/bundles", 128)`
3. `SessionRegistry`、命令日志 `command.NewMemoryStore(100_000)`、Bus（LRU 8192）、Dispatcher
4. `AgentRegistry`（unhealthyAfter/offlineAfter 阈值解析 + `onAgentStatusChange` 回调 + `SetOnRestart`）；`CompletionService`
5. `WindowStore`、`Aggregator`、`AcceptanceService`、`Ingestor`（latest-wins 槽位）
6. `Assigner`
7. MySQL 可用时：`HistoryStore` + `Sampler`（10s，硬编码）+ 三个模板 store（复用全局 db；未配 history 时模板库仍可用）
8. `SetOnTerminal`（终态回调）
9. Redis 可用时：连通性 PING 验证 + `SharedCleanup`

**Run**（`admin/app.go:226-283`）：初始化协程池 → 启动指标摄取器、健康检查、历史清理循环、deadline 看门狗、共享清理重试 → 解析 heartbeatInterval/leaseDuration → 构造管理面 HTTP 服务器与 `grpcapi.NewServer` → `serveServers` 双 listener 并行 Serve，任一退出即整体 Shutdown。

**Shutdown**（`admin/app.go:286-320` + `admin/server.go:101-131`）：先 `runtimeCancel` + close stopCh（避免关闭死锁，注释见 `admin/app.go:294-299`）→ `sessions.Close("Admin 正在关闭")`（广播 ServerClosing）→ gRPC `GracefulStop`（10s 上限后强制 Stop）→ HTTP 关闭 keep-alive 后 `Shutdown`（10s 超时；ReadHeaderTimeout 10s / IdleTimeout 120s 见 `admin/server.go:26-29`，Read/WriteTimeout 故意不设，history 导出可能很长）→ 协程池 Shutdown → HistoryStore 与全局 db 关闭。

### 4.2 任务存储与状态机（`admin/task/store.go`）

```go
type Repository[T, S] struct {
    mu    sync.RWMutex
    tasks map[string]T
    startMu  sync.Mutex   // 串行化启动入口
    activeID string       // 当前活跃任务 ID
    dataDir  string
    ...
    onTerminal   func(T)
    recoveredIDs []string
}
```

关键方法：`Create`（ID 冲突拒绝）、`Get/List/ListByState`（返回 Clone 副本）、`Update`（持锁变更 + 落盘）、`Delete`（活跃任务拒绝删除）、`Transition`（CAS 语义状态转换）、`Begin`（启动入口）、`ActiveTask/ActiveTaskID/HasActive`、`SetOnTerminal`。

**单活跃任务约束**：`Begin`（`admin/task/store.go:249-265`）在 `startMu` 下检查 `activeID`（冲突时返回 `TASK_CONFLICT` + 活跃任务详情）再执行 pending→starting。`activeID` 的登记/清除与状态写入收在同一把 `mu` 锁内（`admin/task/store.go:188-211`），消灭「starting 而 activeID 为空」的窗口（否则一次失败转换会让 activeID 永久指向已终结任务，只能重启 Admin，详见该处注释）。

**终态回调异步化**：`Transition` 进入终态时以 `TerminalClone`（JSON 深拷贝）副本提交协程池调用 `onTerminal`（`admin/task/store.go:207-210`），归档不阻塞状态机。

**Admin 重启恢复**：`NewRepository`（`admin/task/store.go:51-67`）加载 `data/tasks/*.json` 时把活跃状态任务经 Adapter.Recover 重置为 `Failed`（`errorMsg = "admin restart, task lost"`，`admin/task/admin_store.go:24-28`），记入 `recoveredIDs`；`SetOnTerminal` 注册后对这些任务补放一次归档回调（`admin/task/store.go:71-79`）。

**状态转换矩阵**（`admin/task/store.go:267-280`）：pending→starting/stopped/failed；starting→running/failed；running→stopping/stopped/failed；stopping→**running**（停止命令创建失败的回滚边，`admin/httpapi/task_routes.go:530-533`）/stopped/failed。

### 4.3 AgentRegistry（`admin/agent/registry.go`）

```go
type Registry struct {
    mu        sync.RWMutex
    agents    map[string]*Node
    instances map[string]agentInstance // startedAt + taskID，用于识别进程重启
    onChange  func(agentID string, from, to Status)
    onRestart func(agentID, taskID string)
    unhealthyThreshold, offlineThreshold time.Duration
}
```

- **Register**（`:79-125`）：gRPC 会话握手后调用；同 ID 直接覆盖为 idle 节点。实例重启检测：旧实例 `StartedAt` 与新 Hello 不同且旧实例仍持有任务 → 锁外触发 `onRestart`（`:90-94`、`:121-123`）
- **Heartbeat**（`:128-160`）：更新 CurrentTaskID/CurrentBots；任务切换清空 `LatestStress` 防串数据；状态字符串校验（busy 必须带 currentTaskId、idle 不得带，`:167-182`）；触发 unhealthy/offline→在线恢复
- **StartTask**（`:195-211`）：Start ACK 提交后同步节点状态，首个指标窗口无需等心跳
- **CompleteTask**（`:215-234`）：仅在 taskID 仍是当前任务时清理；返回 false 表示报告迟到
- **Touch**（`:237-250`）：任意请求路径刷新心跳；`touchLocked`（`:254-277`）处理 unhealthy/offline 恢复（有任务→busy，无任务→idle）
- **Get/List/ListByStatus**（`:308-340`）：返回浅拷贝（`cloneNodeForRead`，`:349-352`）——标量字段在写锁内就地改写，裸指针会造成 data race
- **UpdateSystem**（`:365-375`）：按快照 sequence 去重（旧帧丢弃）
- **健康扫描**：`StartHealthChecker` 5s 轮询（`:378-393`）；`scanAndMarkStatus`（`:395-442`）标记 unhealthy/offline，**offline 标记后立即删除**（无 24h 延迟清理）；状态变更收集后在锁外 `fireOnChange`，防 AB-BA 死锁

### 4.4 Assigner（`admin/task/assignment.go`）

`Assign(task, idleAgents, startNumber)`：过滤 idle 且 MaxBots>0 → 容量校验 → `debugMode` 单节点优先（容量够且 MaxBots 最小；无则降级）→ `proportionalAssign` 按比例分配（基础数下取整 + 余数按最大剩余小数补齐，`sum == totalBots`）。

### 4.5 命令链路（`admin/command/`）

- **MemoryStore**（`memory.go:22-46`）：内存命令日志，容量 100000；命令按 ID 索引、每 Agent 维持按全局递增 Sequence 的未决队列；落终态命令进 FIFO 供容量满时优先淘汰；`CreateBatch` 整批校验（身份/body/ID 唯一）后写入
- **Bus**（`bus.go:26-43`）：写路径 `CreateBatch` 持久化 + 唤醒目标会话；读路径 `Resolve` 走 LRU 缓存（8192，返回 proto 克隆）；`PendingBatch` 以会话投递游标（非 ACK 游标）拉取补发；`Acknowledge` 校验 AgentId/Sequence/TaskId 与日志一致后落终态
- **Dispatcher**（`dispatcher.go:34-110`）：
  - `ScheduleStart`：`bundles.Build` 打包 → `SplitGlobalValues` 拆并发度 → share 检测注入 `SharedRuntimeAssignment` → 逐节点组装 `Dispatch`（缺省参数回退默认并打 Warn）→ 转 proto → 批量写入 Bus → 返回目标节点列表
  - `ScheduleStop`：为目标节点生成 StopTask 命令（携带原因）
  - `ScheduleShutdown`：为在线节点生成 Shutdown 命令；空列表报错不空投递

### 4.6 资源包存储（`admin/bundle/store.go`）

- `Build`（`:91-161`）：flow/flow.json + proto/* + scripts/* + adapter/*_codec.json + adapter/errors.json 组装为 Deflate zip（条目排序保证确定性，`:163-197`），边写边算 SHA-256，临时文件 + rename 原子落盘；同摘要已存在则校验大小后复用
- `Open`（`:216-247`）：按摘要定位归档、校验 offset、seek 后交出句柄；并发受 `streamSem`（128）限制，ctx 取消时等待让位
- 下发分块 256KB，缓冲走 `sync.Pool` 复用（归还前清零，`:66-77`）

### 4.7 gRPC 控制面（`admin/grpcapi/`）

- 服务器构造（`server.go:18-33`）：16MB 消息上限、keepalive（MaxConnectionIdle 30min / Time 2min / Timeout 20s / MinTime 1min / PermitWithoutStream）；**不设 MaxConnectionAge**——压测任务没有时长上限，控制连接必须能无限期存活
- `Session`（`control_service.go:61-163`）：首帧必须 Hello → 协议版本校验 → `Sessions.Attach`（generation++，顶替旧会话）→ 注册节点 + **孤儿任务停止**（Hello 携带 currentTaskId 但 Admin 已不持有该任务 → ScheduleStop「停止孤儿压测」，`:106-123`）→ 回 Welcome（generation/heartbeatInterval/leaseDuration/serverTime）→ `Commands.Replay` 重放未决命令 → 发送循环（协程池）+ 接收循环（handler goroutine 独占 Recv）
- **SessionRegistry**（`session.go:139-241`）：per-Agent gate 串行化 Attach 与 WithCurrent（防旧流写入新会话）；`Close(reason)` 广播 ServerClosing 并取消全部会话
- **Session**（`session.go:17-134`）：commands/control/heartbeat 三通道；`CommandDispatchWindow = 16` 在途窗口；`orphanStop` 标记防孤儿停止命令重复下发
- `DownloadBundle`（`bundle_service.go:24-80`）：`requireAgentID` → `store.Open(offset)` → 256KB 分块流式发送，透传取消
- `Report`（`metrics_service.go:21-52`）：同流同 agent、generation 必须是当前会话；帧交给 `Ingestor.Offer`，结束时返回 accepted/rejected 计数

### 4.8 指标摄取与聚合（`admin/metrics/`）

- **Ingestor**（`ingestor.go:26-42`）：每 Agent×类型一个 pending 槽（latest-wins，容量 32768 键）；Recv 侧只做身份校验与槽位替换，昂贵的 monitor 转换/DDSketch 校验/落库在有界 worker（CPU 数夹 [4,32]）执行；`DropGeneration` 在会话结束时清理旧代槽位
- **AcceptanceService**（`accept.go:37-63`）：Touch → 节点存在 → `CurrentTaskID` 匹配（不匹配静默丢弃返回 nil）→ 上报周期合法 → 活跃任务匹配 → `WindowStore.AcceptWithDrops`（窗口序列号重复判 duplicate，droppedIntervals 补账）→ 回写节点最新快照
- **WindowStore**（`window_store.go:41-53`）：`AgentMetricState{LastSequence, DroppedIntervals, ReceivedAt(Admin 时钟，唯一新鲜度时间戳), Cumulative, LatestWindow}`
- **Aggregator**（`aggregator.go:37-80`）：累计面合并全部上报节点窗口快照；区间报告只合并 fresh 节点（新鲜度 `max(3×ExpectedEvery, 15s)`）；DDSketch 严格合并重算百分位，绝不简单平均 P99
- **Sampler**（`sampler.go:19-49`）：任务运行期每 10s 聚合压测+系统指标写 `task_timeseries`（含窗口指标列与 history_batch_token 幂等键）；写入失败仅 Warn

### 4.9 CompletionService（`admin/task/completion.go`）

- `Accept(report)`：Touch → 校验汇报者在预期名单（`ExpectedAgents`：SucceededAgents 优先，空则 Assignments）→ `StageIndex>0` 为阶段报告（同节点同阶段幂等覆盖）→ 最终报告写入 Reports → 全员完毕聚合 CleanupSummary → running/stopping→stopped
- `PermanentReportError`：汇报者不属于任务/任务不存在等不可重试错误——调用方应终态 NACK 丢弃，否则 Agent 无限重发
- `OwnsActiveTask`：会话接管与心跳路由判定（活跃任务的待汇报成员）

### 4.10 Agent 状态变更处理（`admin/app.go:421-546`）

| 场景 | 处理 |
|---|---|
| offline → idle/busy（恢复） | 记录 `reconnected` 事件 |
| 分配节点离线（→offline） | 记录 `offline` 事件；stopping 中合成 failed report（CleanupStatus=未知），全报即转 stopped；running 中 `checkAndStopIfAllLost` |
| 实例重启（onRestart） | 记录 `restarted` 事件 + 合成 failed report（「节点进程重启，任务已丢失」）+ `checkAndStopIfAllLost` |

`checkAndStopIfAllLost`（`admin/app.go:689-717`）：预期节点全部（已有 report / 已删除 / offline / CurrentTaskID 不匹配）→ `autoStopTask`。

`autoStopTask`（`admin/app.go:720-788`）：starting 直接转 failed；running→stopping → 对目标节点 ScheduleStop → 为未上报节点合成 failed report → stopping→failed。

**停止超时安全网**（`admin/app.go:616-683`）：`stopWaitTimeout = 60s`（≥ Agent closeRobotsTimeout 15s + Robot.Close 兜底 10s + 上报延迟，否则 Agent 真实上报到达前 Admin 已合成 fake report 并归档）；超时后为未上报节点合成 stopped report 并转 stopped；`GoWithStop` + `waitStopGuard` 让进程关闭时立即放弃等待。

### 4.11 终态归档（`admin/app.go:322-370`）

`onTaskTerminal`：停 Sampler → `cleanupSharedState`（Redis 命名空间清理登记）→ 协程池异步：优先 `buildFinalStressFromReports`（Agent 终止报告聚合，过滤空快照），空则兜底窗口聚合；`AggregateSystem`；`history.Archive`。history 未启用时 `metricsWindows.DropTask` 释放窗口状态。

---

## 5. HTTP API 完整路由表

路由注册：`admin/httpapi/routes.go:21-127`；契约：`api/admin/openapi.yaml`。共 **62 个操作 / 45 个路径**，另有 `/sbot/docs`、`/sbot/openapi.yaml` 与根路径静态托管。

### 5.1 任务（7）

| 方法 | 路由 | Handler | 说明 |
|---|---|---|---|
| `POST` | `/sbot/tasks` | `handleCreateTask` | 创建任务（multipart ≤32MB；flow.json 必需；codec 走 Schema 校验；rampUp 之和=totalBots；deadline 可选；flowTemplateId 可选校验存在；资源写磁盘基线） |
| `GET` | `/sbot/tasks` | `handleListTasks` | 列表（state 过滤 + offset/limit 分页） |
| `GET` | `/sbot/tasks/{id}` | `handleGetTask` | 详情（PublicCopy 剥离 sketch） |
| `GET` | `/sbot/tasks/{id}/config/{path}` | `handleGetTaskConfig` | 配置文件下载（flow/flow.json、config.json 摘要、adapter/*、proto、scripts） |
| `POST` | `/sbot/tasks/{id}/start` | `handleStartTask` | 启动（见 §7） |
| `POST` | `/sbot/tasks/{id}/stop` | `handleStopTask` | 停止（仅 running；停止命令创建失败回滚 stopping→running） |
| `DELETE` | `/sbot/tasks/{id}` | `handleDeleteTask` | 删除（仅终态，204） |

### 5.2 节点管理（5）

| 方法 | 路由 | 说明 |
|---|---|---|
| `GET` | `/sbot/agents` | 列出全部节点（含简要指标） |
| `GET` | `/sbot/agents/{id}` | 节点详情 |
| `DELETE` | `/sbot/agents/{id}` | 删除节点（仅 offline 可删） |
| `POST` | `/sbot/agents/{id}/shutdown` | 下发 Shutdown 命令（单个） |
| `POST` | `/sbot/agents/shutdown-all` | 下发 Shutdown 命令（全部在线） |

### 5.3 指标（7）

`GET /sbot/metrics`（集群压测聚合 + 覆盖率）、`/sbot/metrics/summary`（文本摘要）、`/sbot/metrics/agents`、`/sbot/metrics/agents/{id}`、`/sbot/system`（集群系统聚合）、`/sbot/system/agents`、`/sbot/system/agents/{id}`。

### 5.4 历史归档（11）

`GET /sbot/history`（分页过滤）、`/sbot/history/tags`、`/sbot/history/compare`（多任务对比）；`GET/PUT/DELETE /sbot/history/{id}`（PUT 更新 starred/tags/note，DELETE starred 需 force=true）；`GET /sbot/history/{id}/agents`、`/config`（摘要）、`/config/archive`（原始归档）、`/timeseries`（maxPoints 采样降采样）；`POST /sbot/history/{id}/clone`（创建 pending 新任务，不自动启动）。

### 5.5 基线资源（7）

`GET /sbot/baseline/proto/index.json`、`/sbot/baseline/proto/{name}`、`scripts/index.json`、`scripts/{name}`、`adapter/index.json`、`adapter/{name}`（多 `*_codec.json` + errors.json 透传）、`flow/flow.json`。

### 5.6 流程模板（7）

`GET/POST /sbot/flows`、`GET/PUT /sbot/flows/snapshot`（快照整体读取/替换）、`GET/PUT/DELETE /sbot/flows/{id}`。

### 5.7 动作模板（7）与监听模板（7）

`/sbot/action-templates`、`/sbot/action-templates/snapshot`、`/sbot/action-templates/{id}` 与 `/sbot/listen-templates`、`/sbot/listen-templates/snapshot`、`/sbot/listen-templates/{id}`，方法组合与流程模板一致。

### 5.8 其它（4）

`GET /sbot/api/error-codes`（错误码索引）、`GET /sbot/capabilities`（sharedState/flowLibrary/templateLibrary 能力探测，Redis 地址脱敏展示）、`POST /sbot/codec/preview`（声明式 codec 纯计算预览）、`GET /sbot/codec/algorithms`（算法元数据）。

### 5.9 静态资源与文档

`GET /sbot/docs`（Swagger UI）、`GET /sbot/openapi.yaml`（契约本体，`admin/httpapi/routes.go:23-24`）；`/` 由 `http.FileServer` 托管前端构建产物（`routes.go:111-112`）。

---

## 6. MySQL 结构（11 张表）

内嵌 DDL：`admin/mysql/schema_definition.go:8-172`。启动时 `mysql.InitializeSchema` 建当前结构；**首版不维护迁移版本链**（`conf/admin.toml:84`）。

```
task_history          -- 主表：state、active_agent_count、stage_count、flow_template_id、config_summary(JSON)
task_assignment       -- 分配快照（每节点一条）
task_report           -- 节点报告：final_snapshot/cleanup_status(JSON)、stage_index、idx_task_stage
task_aggregated       -- 集群终态快照，复合主键 (task_id, stage_index)
task_timeseries       -- 运行期采样：窗口指标列、uq_task_history_batch 幂等键、idx_task_stage_elapsed
task_config_archive   -- flow/proto/scripts/codecs/error_map(MEDIUMBLOB) + robot_config(JSON)
task_agent_events     -- 节点状态事件
task_meta             -- starred/tags/note，主键 (task_id, stage_index)，-1=任务级 / >=1=reset 段落
flow_template         -- 流程模板（flow_json/layout_json）
action_template       -- 共享动作模板（name 唯一，utf8mb4_bin）
listen_template       -- 共享监听模板（name 唯一，default_ref_json）
```

约定：主键 `id` 为 32 字符随机 hex；**只用逻辑外键**，无 FOREIGN KEY，级联删除由应用层处理。模板库与 history 共用全局 MySQL 连接池（`admin/app.go:176-191`）：MySQL 未启用时 HistoryStore 为 nil（`/sbot/history*` 返回 `HISTORY_DISABLED`，`admin/app.go:192-195`），三个模板 store 同样为 nil。

**归档事务**（`admin/history/store.go:88` 起）：task_history（幂等）+ task_assignment + task_report（含段落报告按连续段号）+ task_aggregated（-1 整体 + 各段落）+ task_config_archive + task_agent_events；reset 段落机制详见 `docs/ramp-up.md`。

**数据保留**：`retentionDays`（默认 90，<=0 回退 90）裁剪，24h 周期；starred 永不自动删除（`admin/history/store.go:1451`、`:1524`）。

---

## 7. 任务启动/停止时序

### 7.1 handleStartTask（`admin/httpapi/task_routes.go:380-463`）

1. `tasks.StartTask`（Begin：单例约束 + pending→starting）
2. 共享状态检测：脚本使用 share 但未配 Redis → starting→failed + `SHARED_STATE_UNAVAILABLE`
3. `Assigner.Assign`（空闲节点分配；失败 starting→failed）
4. `ValidateDistributedConcurrency`
5. 保存 assignments、启动 Sampler
6. 协程池 `startTaskBackground`（`task_routes.go:465-495`）：任务不存在即取消；`ScheduleStart` 创建 gRPC 启动命令 → 成功节点记入 `SucceededAgents` → starting→running（随后 `finishTaskIfFullyReported` 兜底瞬时完成）；命令创建失败 → starting→failed + 停 Sampler
7. 立即返回 202 + assignments

### 7.2 handleStopTask（`admin/httpapi/task_routes.go:497-548`）

1. 任务必须 running
2. running→stopping
3. 目标节点（SucceededAgents 优先）`ScheduleStop`；失败回滚 stopping→running 并报 500
4. `synthesizeOfflineReports`（离线节点合成 stopped report）；全报 → stopping→stopped，否则 60s 安全网
5. 返回 202 + 任务摘要

### 7.3 自然完成

Agent 最终报告（gRPC FinalReport）→ `CompletionService.Accept` → 全员完毕 → running/stopping→stopped；阶段报告（StageIndex>0）只入 StageReports 不触发状态转换。

### 7.4 Deadline 超时

`startDeadlineWatchdog`（`admin/app.go:802-828`）5s 检查活跃任务 `Config.Deadline`，超时 `autoStopTask("任务超时")`。

---

## 8. 锁顺序说明

| 锁 | 保护对象 | 备注 |
|---|---|---|
| `tasks.startMu` | 串行化启动入口 | `Repository.Begin` |
| `tasks.mu` | tasks map + activeID | activeID 与状态同临界区（`store.go:188-211`） |
| `agents.mu` | agents map + instances | `fireOnChange` 锁外触发回调 |
| `sessions.gates[agentID]` | 串行化 Attach 与 WithCurrent | 防旧流写入新会话 |
| `sampler.mu` | samplerJob | Start/Stop |

**顺序**：`agents.mu` → `tasks.mu`（`onAgentStatusChange` 由锁外回调进入 tasks 操作）；`Transition` 的终态回调经深拷贝 + 协程池异步，锁内不触发跨模块调用。

---

## 9. 配置参数（`conf/admin.toml` / `admin/config.go`）

```go
type Config struct {
    Server       ServerConfig        // [server] 管理面 HTTP
    ControlPlane ControlPlaneConfig  // [controlPlane] gRPC 控制面
    MySQL        *MySQLConfig        // [mysql]（nil 或 host 空=不启用）
    Redis        *shared.RedisConfig // [redis]（nil 或 host 空=不启用）
    Log          stresslog.Config    // [log]
    Pprof        *config.PprofConfig // [pprof]（可选）
    Daemon       bool                // 顶层 daemon（仅 Linux）
}
```

默认值（`admin/config.go:127-161`）：server.port 7718 / listenHost 127.0.0.1 / staticDir cmd/web/dist；controlPlane.port 7720 / heartbeatInterval 10s / unhealthyAfter 30s / **leaseDuration 45s** / offlineAfter 60s；mysql retentionDays 90 + pool（10/5/1h/5s/30s/30s）；log path log/admin.log。

**校验规则**（`admin/config.go:176-237`）：

- 两个 port 必须 >0；两个 listenHost 必填
- controlPlane 四时长强校验：`heartbeatInterval < unhealthyAfter`、`heartbeatInterval < leaseDuration`、`unhealthyAfter < offlineAfter`、`leaseDuration < offlineAfter`（租约让 Agent 侧先接管任务生死，且必须覆盖 Agent 重连最坏耗时——`reconnect.maxInterval` 的 2 倍抖动上界 + 拨号握手）
- Redis 启用时 `Resolve()` 校验；MySQL 启用时 DSN 与连接池时长加载期校验（写错即报错），连通性校验推迟到装配阶段（`Open` + PING）
- MySQL DSN 用驱动 `mysql.Config.FormatDSN()` 生成，密码/库名含特殊字符不会解析错位（`admin/config.go:73-102`）

配置加载为 TOML 严格模式（未知字段报错）+ 环境变量展开（`${VAR}` fail-loud / `${VAR:-default}` / `$$`，`config/load.go:30-46`、`:68`）。

---

## 10. 错误系统（`admin/apierror/errors.go`）

```go
type Error struct {
    Code       string         `json:"code"`
    HTTPStatus int            `json:"-"`
    Message    string         `json:"message"`
    Details    map[string]any `json:"details,omitempty"`
}
```

预定义错误码（`:39-59`）：`TASK_NOT_FOUND`(404)、`TASK_CONFLICT`(409)、`TASK_INVALID_STATE`(409)、`AGENT_NOT_FOUND`(404)、`AGENT_BUSY`(409)、`AGENT_OFFLINE`(409)、`CAPACITY_EXCEEDED`(400)、`INVALID_ARGUMENT`(400)、`HISTORY_DISABLED`(503)、`HISTORY_NOT_FOUND`(404)、`INTERNAL_ERROR`(500)、`HISTORY_STARRED`(409)、`SHARED_STATE_UNAVAILABLE`(400)、`FLOW_LIBRARY_DISABLED`(503)、`FLOW_TEMPLATE_NOT_FOUND`(404)、`FLOW_SNAPSHOT_CONFLICT`(409)、`TEMPLATE_LIBRARY_DISABLED`(503)、`ACTION_TEMPLATE_NOT_FOUND`(404)、`LISTEN_TEMPLATE_NOT_FOUND`(404)、`TEMPLATE_NAME_CONFLICT`(409)、`TEMPLATE_SNAPSHOT_CONFLICT`(409)。

错误响应示例（单例冲突，Details 由 `ConflictDetails` 生成，`admin/task/admin_store.go:33-40`）：

```json
{
  "code": "TASK_CONFLICT",
  "message": "",
  "details": {"activeTaskId": "...", "activeName": "...", "activeState": "running", "startedAt": "..."}
}
```

---

## 11. 持久化方案

- **任务**：`data/tasks/{id}.json`，`Create/Update/Transition` 后原子写入（tmp + rename），启动加载恢复（`admin/task/persist.go`）
- **命令**：内存命令日志（重启丢失）；Agent 未确认命令随会话重建重放，Admin 重启后 Agent Hello 触发孤儿任务停止
- **最终报告**：Task.Reports 随任务 JSON 持久化 + 归档 MySQL
- **共享状态清理**：`SharedCleanup` 待清理队列持久化（data 目录），重启后续试（`admin/task/shared_cleanup.go`）
- **不持久化**：AgentRegistry（节点重连即重注册）、指标窗口（内存，任务终态 DropTask 或 MarkTaskTerminal 释放）、资源包（内容寻址，可重建）

---

## 12. 进程入口（`cmd/admin/main.go`）

```go
var Version = "dev"  // -ldflags "-X main.Version=v1.0.0" 注入

func run(args []string) (exitCode int) {
    // 1. 进程级顶层 recover（stderr + 日志，exit 2）
    // 2. 解析 -config / -d 参数（默认 conf/admin.toml）
    // 3. -d 或配置 daemon：daemon.Daemon fork 子进程（仅 Linux）
    // 4. admin.LoadConfig（TOML 严格 + 校验）
    // 5. stresslog.InitLog（zap + lumberjack）
    // 6. admin.NewServer(cfg)（装配失败即退出，不提供半健康服务）
    // 7. 可选 pprof（默认 6060）
    // 8. server.Run(ctx)：阻塞至信号/监听面退出
}
```

启动命令：

```bash
go run ./cmd/admin -config conf/admin.toml
go run ./cmd/admin -config conf/admin.toml -d          # 守护进程（仅 Linux）
go build -o admin.exe ./cmd/admin -ldflags "-X main.Version=v1.0.0"
```

---

## 13. HTTP 状态码约定

| 场景 | 状态 |
|---|---|
| 成功（查询/返回数据） | `200` |
| 资源创建成功（任务/克隆/模板） | `201` |
| 异步接受（启动/停止任务） | `202` |
| 无内容（删除任务） | `204` |
| 客户端参数错误 | `400` |
| 资源未找到 | `404` |
| 状态冲突（单例/状态不允许/名称冲突） | `409` |
| 服务不可用（历史/模板库未启用） | `503` |
| 服务端内部错误 | `500` |

---

## 14. Panic 安全网

- 所有 `/sbot/` handler 被 `Wrap` 中间件包裹（`admin/httpapi/routes.go:114-126`）：捕获 panic → 写日志（path/method/panic/stack）→ 返回标准 500 JSON；请求体先经 OpenAPI Schema 校验，非法返回 `REQUEST_SCHEMA_INVALID`
- gRPC 会话循环由协程池双层恢复兜底（`internal/workpool`）
- 进程级顶层 recover（`cmd/admin/main.go:51-64`）：stderr + 日志后 exit 2

---

## 15. 协议契约（强约束）

### 15.1 Admin 必须遵守

1. `Welcome.HeartbeatIntervalNanos` / `LeaseDurationNanos` 必须与 controlPlane 配置一致（Agent 据此对齐心跳与租约）
2. StartTask 命令携带的 BundleDescriptor（SHA-256 + 大小）必须与 `AgentBundleService.DownloadBundle` 可下载的归档一致
3. Shutdown 命令的 `CommandReceipt` 回执必须持久化 ACK 之后才发——Agent 的进程退出延迟到该回执（`controlplane/proto/control.proto:122-128`）
4. 指标聚合只接受当前会话 generation 的帧；会话顶替后旧代帧整体丢弃（`admin/grpcapi/metrics_service.go:38-43`）

### 15.2 Admin 假定 Agent 提供

1. 首帧 Hello（协议版本一致），心跳按 Welcome 下发间隔（Agent 本地配置兜底）
2. 命令串行执行、`CommandAck` 至少一次送达（重放幂等由 Admin 命令日志 + Agent 结果 outbox 保证）
3. 任务最终报告重发直至 `ReportAck`（accepted 或永久拒绝）
4. `monitor.CollectorSnapshot` 字段与 `controlpb.CollectorSnapshot` 转换完全一致（`controlplane/monitor.go`）；DDSketch 配置两侧一致（`conf/agent.toml:60-66` 注释明确要求与 Admin 一致，否则合并结果错误）

任何一方修改 wire 字段必须同时改 `controlplane/proto/control.proto` 并重新生成 `controlplane/pb`。
