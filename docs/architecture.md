# stressbot 总体架构

> 本文档对照实际代码编写，反映已实施状态（技术知识库首篇，其余模块文档见第 5 节导览）。
> 所有技术声明均核对源码后写入，引用格式为 `文件:行号`，行号以当前仓库状态为准。
>
> **核对过的代码范围**：`cmd/{stressbot,agent,admin}/main.go`；`standalone/{config,app}.go`；
> `agent/{config,app}.go`；`admin/{config,server}.go`、`admin/app.go`、`admin/httpapi/routes.go`；
> `controlplane/proto/control.proto`；`api/admin/`（openapi.yaml + embed）；`runner/{resources,start}.go`；
> `engine/{executor,action}.go`；`network/{writetime,gnet,connection,client}.go`；
> `robot/{robot,manager,scheduler}.go`；`flow/flow.go`；`script/runtime.go` 与 7 个 `api_*.go`；
> `protocol/{adapter,codec_resolver}.go`；`errcode/code.go`；`monitor/collector.go`；
> `Makefile`；`.github/workflows/release.yml`；`docker-compose.yml`；`go.mod`；`conf/*.toml`；`cmd/web/package.json`。

---

## 1. 项目定位与核心设计原则

stressbot 是一个**可配置化通用游戏服务器压测工具**，Go 后端 + React 前端。核心设计：所有消息收发、字段填充、随机化、心跳、监听、条件跳转都通过 **JSON 流程配置 + 声明式动作** 表达，少量难以通用的行为通过 **Lua 脚本** 实现。一套 `conf/flow/flow.json`（或 `-flow` 指定的其他流程文件）+ `conf/scripts/*.lua` 即可驱动任意带类似协议头的游戏服务器压测，无需改代码重新编译。

三条设计红线贯穿全仓（详见 AGENTS.md「内存优化约束」一节，均有线上剖面佐证）：

1. **数据面改造必须「留存侧 + 消费侧」同步落地**——改任何数据形态前先画出全部读写路径。
2. **每机器人独有的大消息只存 wire 字节**（约 1/5~1/10 的解码态体积），消费侧走 wire 惰性视图 / 单遍直转，广播消息按内容寻址共享（`protocol/protox/` 的 `WireCache`/`WireShared`）。
3. **取消语义统一**——动作取消一律报 `errcode.ErrActionCanceled`，monitor 归类为 Canceled 而非 Timeout/Failure，避免停止机器人在监控面堆出假故障（`engine/action.go:249-258`）。

流程配置的表达能力（数据模型细节见 [flow-node-system.md](flow-node-system.md)）：

- **节点类型 9 种**（`flow/flow.go:146-154`）：控制流 `sequence`/`loop`/`boolean`/`switch`/`weighted`/`wait`，执行 `action`，循环控制 `break`/`continue`。
- **动作 pattern 14 种**（`flow/flow.go:122-135`）：请求-响应 `tcpRequest`/`udpRequest`，监听 `tcpListen`/`udpListen`，连接管理 `tcpConnect`/`udpConnect`/`tcpClose`/`udpClose`，发送 `tcpSend`/`udpSend`/`httpRequest`，状态与辅助 `setState`/`clearState`/`lua`。
- **Lua 兜底层**：7 个模块共 87 个函数——`network`(21)/`robot`(15)/`utils`(15)/`proto`(10)/`json`(2)/`log`(4)/`share`(20)，注册于 `script/runtime.go:744-750`，函数按 `L.SetField(mod, ...)` 逐一清点核对。

## 2. 三进程模型

同一仓库编译出三个独立二进制，共用同一套业务包（robot/engine/network/protocol/script 等）。

| 维度 | 单机 stressbot | Agent 节点 | Admin 服务器 |
|---|---|---|---|
| 入口 | `cmd/stressbot/main.go` | `cmd/agent/main.go` | `cmd/admin/main.go` |
| 应用包 | `standalone/`（config.go + app.go） | `agent/`（app/config + session/command/bundle/task/metrics 子包） | `admin/`（app/config/server + task/agent/command/bundle/metrics/history/template/httpapi/grpcapi/mysql/apierror 子包） |
| 配置文件 | `conf/stressbot.toml`（`cmd/stressbot/main.go:45`） | `conf/agent.toml`（`cmd/agent/main.go:41`） | `conf/admin.toml`（`cmd/admin/main.go:40`） |
| 监听面 | 无管理面；可选 pprof（默认 6060）与监控 HTTP（示例配置 6061，`conf/stressbot.toml:75-76`） | **无本地 HTTP 控制面**（`conf/agent.toml:9` 明确声明），可选 pprof | 管理面 HTTP **默认 127.0.0.1:7718** + 控制面 gRPC **默认 127.0.0.1:7720**（`admin/config.go:130-136`） |
| 对外行为 | 单进程内完成加载→拨号→批量/渐进加压 | 主动连接 Admin gRPC，注册→接任务→下载资源包→执行→上报 | 任务调度、Agent 管理、指标聚合、MySQL 历史归档、托管前端 |

三个入口都是薄壳：解析 flag 与 `-d` 守护进程化 → `LoadConfig` → 初始化日志/pprof → 调应用包的 Run。单机模式独有 `-flow`/`-proto`/`-scripts`/`-adapter` 四个资源路径覆盖 flag（`cmd/stressbot/main.go:46-49`，空值回退 `<conf>` 下子目录，`standalone/config.go:105-117`），可无挪文件切换压测场景。

### 单机模式启动序列

`standalone.Run`（`standalone/app.go:24`）按序完成装配，每步都有代码位置：

1. `runner.LoadResources` 加载并校验 codec/proto/流程三类资源（`standalone/app.go:38`，实现见第 3 节 runner 条目）；
2. `monitor.Init` 初始化监控采集器（`:54-59`）；
3. `runner.NewRuntimePool` 创建 Lua 运行时池并预编译脚本——预编译失败仅告警不阻断（`:61-66`）；
4. `openSharedStore`：扫描脚本目录是否使用 `share` 模块，用了但未配 Redis 则启动失败（`:73-79`、`:147-172`）；
5. 构造 `robot.ManagerConfig`（请求超时 60s、HTTP 超时 10s，`:81-94`）；
6. `runner.StartDialer` 启动 gnet 网络引擎（`:96`）；
7. `robot.NewManager` 后按是否配置 `rampUp.stages` 分叉：配置了走 `StartWithRampUp` 渐进加压，否则 `StartAll` 一次性启动（`:102-118`，实现在 `robot/manager.go:225`/`:244`）；各阶段 count 之和必须等于 totalBots，加载期校验（`standalone/config.go:84-92`）；
8. 阻塞等待 ctx 取消或运行时长到期，随后停机器人、导出 CSV（`:120-133`）。

Agent 与 Admin 的生命周期类似地收敛在 `agent/app.go:84`（`Agent.Run`）与 `admin/app.go:226`（`Server.Run`）。

### 进程协作（分布式拓扑）

```
浏览器 ──HTTP /sbot/──▶ Admin（HTTP 7718 托管 Web UI + REST；gRPC 7720 控制面）
                          │  AgentControlService.Session（双向流，control.proto:8）
                          │  AgentBundleService.DownloadBundle（服务端流，control.proto:12）
                          │  AgentMetricsService.Report（客户端流，control.proto:16）
                          ▼
                    Agent 节点 × N（无本地 HTTP，一切经 Admin）
                          │  gnet TCP/UDP
                          ▼
                    被测游戏服务器
```

- Admin 双监听面在 `admin/server.go:56`（`serveServers`）同时拉起：管理面 `http.Server` + gRPC `grpcSrv.Serve`，任一异常退出触发整体 Shutdown。
- Agent 心跳/租约/离线三阈值联动约束在 `admin/config.go:38-47` 与加载期校验 `admin/config.go:189-219`（heartbeat < unhealthy < lease < offline）。
- 管理面 HTTP API 契约以 OpenAPI 为准：契约文件 `api/admin/openapi.yaml`（embed 于 `api/admin/embed.go`），运行时可经 `GET /sbot/openapi.yaml` 与 Swagger UI `/sbot/docs` 获取（`admin/httpapi/routes.go:23-24`），请求经 oapi-codegen 中间件按契约校验（`admin/httpapi/middleware.go:13-15`）。

### 配置文件总览

三份 TOML 均支持 `#` 注释与 `${VAR}` / `${VAR:-default}` 环境变量展开（`config/load.go:170`，`envsubst`），严格模式（拼错字段名启动失败），各段结构逐一读自文件本身：

| 配置文件 | 段 | 关键内容 |
|---|---|---|
| `conf/stressbot.toml` | `log` / `monitor`（含 `http`、`sketch` 子段）/ `pprof` / `standalone`（`bot`/`stateExtra`/`rampUp.stages`）/ `redis` / `network` | gnet 调优（readBufferCap/numEventLoop/maxConcurrentDials/maxBodyLen，`standalone/config.go:32-39`）；monitor 示例配置 http 端口 6061、sketch 相对精度 0.01/2048 bins |
| `conf/agent.toml` | `log` / `monitor` / `pprof` / `agent`（含 `reconnect` 子段）/ `daemon` | 必填 `id` 与 `adminAddress`（`agent/config.go:109-117`）；默认 maxBots 5000、心跳 10s、重连退避 5s~30s（`agent/config.go:96-105`） |
| `conf/admin.toml` | `server` / `controlPlane` / `mysql`（含 `retentionDays`/`pool`）/ `redis` / `log` / `pprof` / `daemon` | 四个心跳/租约阈值默认 10s/30s/45s/60s；MySQL 留空 host 即不启用；DSN 用驱动 `FormatDSN` 生成避免密码分隔符错位（`admin/config.go:73-102`） |

时长类配置统一走 `config.ParseDuration` / `ParsePositiveDuration`（`config/duration.go:21`/`:37`）：**留空才回退默认，写错一律报错**，两者只差 0 是否合法——间隔与超时用 Positive（0 会退化成忙循环），MySQL 连接池等以 0 表示「不限制」的项用普通版。

## 3. 分层依赖图

依赖方向自上而下，下层不反向 import 上层（`engine` 对 `robot`/`network` 的解耦靠接口注入，见第 4 节）：

```
cmd/{stressbot,agent,admin}     薄入口：flag 解析、daemon 化、日志/pprof 初始化
        │
standalone / agent / admin      应用装配：配置模型 + 生命周期编排
        │                        （admin 含 httpapi/grpcapi/mysql/history 等；
        │                         agent 含 session/bundle/task/metrics 等）
        ├──▶ runner             单机与 Agent 共用的资源加载与运行清理
        │       └─▶ config/validation（flow/codec JSON Schema 契约校验）
        │
controlplane/                    Admin↔Agent gRPC 契约（proto/ 源 + pb/ 生成码）
api/admin/                       Admin 管理面 OpenAPI 契约（embed）
        │
flow + binding + engine         flow：节点 DAG/动作/监听/onError/条件模型
        │                        binding：字段取值与过滤规则
        │                        engine：图遍历与动作执行（不碰网络实现）
        │
robot                            Robot（单客户端实例）/ Manager（批量与渐进加压）
        │                        / robotScheduler（协作式调度，scheduler.go:21）
        │
        ├── network              gnet TCP/UDP 连接层（Client/Connection/Dialer）
        ├── script               Lua 运行时池（gopher-lua，每 Robot 独占 LState）
        ├── protocol             适配器接口 + CodecResolver + SchemaAdapter
        │     ├── codec          声明式编解码引擎（纯 Go，由 *_codec.json 驱动）
        │     └── protox         动态 protobuf 加载/反射/wire 惰性视图/去重缓存
        ├── state                单机器人键值状态；state/shared 为 Redis 跨节点共享
        │
        ├── errcode              统一错误码（<100 框架段 29 个 / ≥100 业务段，code.go）
        ├── monitor              指标采集（DDSketch/Apdex/窗口），RecordAction 见 collector.go:488
        └── internal/            进程内基础设施：daemon/debughttp/jsonx/lru/retry/
                                 stresslog/timerpool/workpool（协程池，封装 ants）
```

分层要点：

- **`engine` 与网络/Robot 解耦**：`Executor` 通过 `ActionHandler` 接口委托实际工作（`engine/executor.go:43-59`，含 `ExecuteAction`/`ExecuteCondition`/`RegisterListen`/`CooperativeSleep` 四方法）；`Robot` 层用 `robotActionHandler` 实现该接口（`robot/robot.go:605`），在 `NewRobot` 内完成装配（`robot/robot.go:179-186`）。
- **网络委托分两层**：`NetSender` 是声明式动作与 Lua 运行时共用的基础能力面；`ActionNetSender` 只给声明式动作追加两个取消感知的请求方法（首参 ctx），拆层原因与取消契约写在 `engine/action.go:341-364`。
- **协议层三件套**：`protocol/adapter.go:9-52` 定义 `Adapter` 接口 9 方法（HeaderSize/BodyLength/EncodeTCP/EncodeUDP/DecodeTCP/DecodeUDP/ExpectedRouteKey/Close/DescribeError）；`protocol.InferCodecMap` 从 adapter 目录文件名推断 `<proto>:<service>` 映射（`protocol/codec_resolver.go:52`），`LoadCodecResolver` 装配（`:151`）；实际编解码由 `protocol/codec/` 纯 Go 引擎按声明式 JSON 配置驱动，无 Lua codec 路径。
- **goroutine 统一走 `internal/workpool`**（封装 `panjf2000/ants`，`internal/workpool/workpool.go:35-70`，自带 recover）。

### 核心包职责表

| 包 | 职责 | 代表文件 |
|---|---|---|
| `flow/` | 节点 DAG、动作、监听、onError 与条件模型（含条件编译） | flow.go、condition_compile.go |
| `binding/` | 字段取值（17 种 type）与过滤规则（12 种 op）、store 映射 | field.go、filter.go、store.go |
| `engine/` | 图遍历与动作执行；心跳（含编译布局 `CompileHeartbeatPlan`）；取消语义 | executor.go、action.go、heartbeat_plan.go |
| `config/` + `config/validation/` | TOML 加载/环境变量展开/时长解析约定；flow 与 codec 的 JSON Schema 契约校验 | load.go、duration.go、validation/ |
| `robot/` | Robot 单实例（state/连接/Lua/执行器）、Manager 批量与渐进加压、协作式调度器 | robot.go、manager.go、scheduler.go |
| `network/` | gnet TCP/UDP：Client 多服务连接池、Connection 收发与请求-响应匹配、监听队列/回调、连接级心跳 | client.go、connection.go、gnet.go |
| `protocol/` | 适配器接口、CodecResolver、SchemaAdapter | adapter.go、codec_resolver.go |
| `protocol/codec/` | 声明式编解码引擎（checksum/cipher/compress/intern/去重缓存，零分配收包路径） | engine.go、ciphers.go、inflate_cache.go |
| `protocol/protox/` | 动态 protobuf 加载/工厂、wire 惰性视图、广播去重、影子验证 | factory.go、loader.go、wireshadow.go |
| `script/` | Lua 运行时池、7 模块 API、协作式 await、trampoline 复用、wire→Lua 直转 | runtime.go、api_*.go、wire_lua.go |
| `state/` + `state/shared/` | 单机器人线程安全键值状态（含 Overlay 写覆盖层）；Redis 跨节点共享 | store.go、overlay.go、shared/redis_store.go |
| `errcode/` | 统一错误码与码段契约、codeRegistry | code.go、registry.go |
| `monitor/` | 指标采集：计数器、DDSketch 直方图、Apdex、窗口与报告导出 | collector.go、histogram.go、reporter.go |
| `runner/` | 单机与 Agent 共用的资源加载（LoadResources）、网络引擎启停、清理 | resources.go、start.go、cleanup.go |
| `internal/` | daemon/debughttp/jsonx/lru/retry/stresslog/timerpool/workpool | — |

## 4. 单次动作数据流

以一次 `tcpRequest`（请求-响应）为例，完整链路与代码位置：

```
Executor.Run("main")                    engine/executor.go:92
  └ executeNode 图遍历（9 种节点分派）    engine/executor.go:107
      └ ActionHandler.ExecuteAction     robot/robot.go:605（robotActionHandler）
          └ ActionExecutor.Execute      engine/action.go:393（14 种 pattern 分派）
              │ ① 构建：BuildProtoBody 按 c2sProto + bindings 构造
              │    protobuf 消息并序列化为 body（binding 17 种取值类型，
              │    condition/optional/required/storeAs 通用属性在此生效）
              │ ② 编码：protocolEncode 按 "tcp:<service>" Resolve 出 adapter，
              │    EncodeTCP 打消息头（含加密）            engine/action.go:1482
              │ ③ 发送：ActionNetSender.TCPRequest（ctx 取消感知）
              │    → Connection.SendRequest 登记 responseMap[routeKey]
              │    并立即返回 PendingRequest               network/connection.go:297
              │ ④ gnet 事件循环 AsyncWrite，写完成回调登记起点时刻
              │    （见下文 RTT 测点）                     network/gnet.go:302-333
              │ ⑤ 服务端响应帧到达 gnet 入站缓冲，完整帧解析出
              │    的时刻记入 RecvFrameAt                  network/gnet.go:230
              │ ⑥ connectionPump 解码：adapter.DecodeTCP 得
              │    routeKey/body/headerErr，按 routeKey 投递给等待者
              │ ⑦ 校验与存储：parseAndStoreResponse——结构校验取代
              │    「解码即校验」，按 store 映射写入 state（wire 留存）  engine/action.go:1178
          └ monitor.RecordAction(name, result, timing, ...)  monitor/collector.go:488
              （robot 层汇总结果后调用，robot/robot.go:641）
```

协作式调度贯穿全程：单个 Robot 的主流程、listen 回调、await 等待都在该 Robot 的执行器 goroutine 内串行推进（`robot/scheduler.go:21` 的 `robotScheduler`）；阻塞型 Lua API 只暂停当前 Robot 主流程，connectionPump 与连接级心跳独立运行。声明式 httpRequest/tcpConnect/udpConnect 的阻塞调用经 `SetCooperativeIO` 注入的 `runIO` 在后台执行、等待期 drain mailbox（`robot/robot.go:180-184`、`engine/action.go:233-242`）。

链路上的几个关键语义：

- **routeKey 匹配**：协议没有序列号，`ExpectedRouteKey(route)` 算出的路由键是响应的唯一匹配依据，同一 routeKey 同时只能有一个在途请求——覆盖时告警而非静默（`network/connection.go:337-351`）。
- **监听路径**：`tcpListen`/`udpListen` 消费 `ListenRefs` 预注册的推送队列；收包路由经 `listenRoutes` 合表（`network/connection.go:78`）一次持锁查齐回调与队列（合表设计见 AGENTS.md「收包路由分派合表」）。推送到达后的处理分两路且都不在 pump goroutine 碰业务 LState：script 回调投递到 Robot 任务队列串行执行；声明式 store 直接写线程安全的 `state.Store`（`engine/executor.go:49-53`）。
- **留存形态**：S2C 响应不再为存储解码整条消息——`protox.ValidateWire` 做结构校验，整存映射存 `*protox.WireValue`（字节快照+惰性导航），路径映射在 wire 上导航取值；动作响应（逐机器人唯一）不进广播去重缓存（`engine/action.go:1165-1177`）。Lua 侧消费见 `robot.get`/`robot.get_view` 边界（AGENTS.md 2026-07-30 P1）。
- **onError 错误链路**：action 失败后 `ignoreCodes` 命中则 warn 并继续（monitor 保留失败样本）、`handler` 走普通节点调用边、`retry.maxRetries` 是当前 action 的额外重试次数、`strategy` 支持 resume/skip/abort（skip 由 sequence/loop/boolean/switch/weighted 捕获，abort 中断流程；内部信号见 `engine/executor.go:20-28`）。
- **计时分级**：`monitor.timingDetail` 三档 rtt/codec/full 控制每动作记录多细的耗时分解（`conf/stressbot.toml:62-66`），声明式路径按档位填充 Build/Encode/Send/Decode 各段（`engine/action.go:1544-1569`）。

### RTT 的两个测点

施压机自身的高 CPU 会污染延迟观测——若把「数据在待写队列里排队」或「Robot 被唤醒的调度延迟」算进 RTT，施压机越忙、测得的服务端延迟越大，Apdex 随之失真。因此两个测点都取在贴近内核的位置：

| 测点 | 位置 | 实现 |
|---|---|---|
| **起点**：数据真正交给内核的时刻 | `network/writetime.go:35-53` | `AsyncWrite` 返回时数据还在待写队列，真正的 write(2) 在事件循环后续轮次执行。`writeStamp.mark` 由 gnet 写完成回调调用（`network/gnet.go:321-333` 的 `writeDoneCallback`，**写失败不登记**，避免失败发送凭空造出 RTT 起点）；`start()` 取不到回调时刻则回退入队时刻——只会让 RTT 偏大不会偏小，回退计数入 `writeStampFallbacks`（观测端点 `/debug/rtt`） |
| **终点**：帧在 gnet 入站缓冲可读的时刻 | `network/gnet.go:230` | `recvFrameAt := time.Now()` 在完整帧从入站缓冲读出的瞬间打点，随 `EnqueueRaw` 一路透传到 `Message.Timing.RecvFrameAt`（`network/connection.go:872`），而非 Robot 被唤醒读到它的时刻 |

两点相减得到 `WireRTT`：`network/connection.go:384`（`t.WireRTT = safeSub(resp.Timing.RecvFrameAt, pr.stamp.start())`，见 `PendingRequest.Timing`，`:373-396`）。语义边界：WireRTT 只含「链路 + 服务端」，不含 decode、响应解析和 state 写入等客户端开销（`engine/action.go:353-354`）；监听等待时长同样从 `RecvFrameAt` 起算（`engine/action.go:267-272`），否则队列里缓存已久的消息会被测成接近 0ms。

### 调优观测端点

挂 pprof 端口（DefaultServeMux），启用 pprof 的进程自动获得，均已核对注册位置：

| 端点 | 位置 | 用途 |
|---|---|---|
| `/debug/rtt` | `network/writetime.go:76` | RTT 计时起点命中率（写完成回调 vs 回退入队时刻） |
| `/debug/sched` | `internal/debughttp/sched.go:100` | Go 调度延迟分位数，量化施压机负载对 Apdex/P99 的污染 |
| `/debug/statekeys` | `script/statekeys_http.go:119` | `robot.get/get_path/get_view` 按 key 计数，定位整读热点 |
| `/debug/wire` | `protocol/protox/wireshadow.go:286` | wire 消费路径影子采样失配情况 |
| `/debug/dedup` | `protocol/protox/dedup_http.go:64` | 广播 wire 去重缓存状态 |
| `/debug/inflate` | `protocol/codec/inflate_cache.go:136` | 大广播帧解压去重缓存状态 |

## 5. 关键子系统导览

各子系统有独立文档深入展开（本节各一段话导览）：

| 子系统 | 文档 | 一段话导览 |
|---|---|---|
| 流程引擎 | [flow-node-system.md](flow-node-system.md) | `flow`+`binding`+`engine` 三包的节点 DAG 模型：9 种节点、17 种 binding、12 种过滤器、复合条件表达式（`state:`/`lua:` 前缀）、`onError` 错误链路（ignoreCodes/handler/retry/strategy）。引擎只管图遍历，动作经 `ActionHandler` 接口委托。 |
| 协议编解码 | [adapter-layer.md](adapter-layer.md) | 每连接一份 `<proto>_<service>_codec.json` 声明式配置 + 共享 `errors.json`，由 `protocol/codec/` 纯 Go 引擎驱动；Adapter 接口 9 方法；UDP 偏移量部分加密；路由键匹配请求-响应与监听分发。 |
| 配置体系 | [configuration.md](configuration.md) | 三进程 TOML 全字段、加载管线（严格解码 + 环境变量展开）、时长契约、JSON Schema 契约校验、单机生命周期与 runner 资源链。 |
| Lua 运行时与内存优化 | [lua-runtime.md](lua-runtime.md) | `script/` 的运行时池与 trampoline 复用、7 模块 87 函数、协作式 await；以及 wire-first 消费路径（`robot.get_view` 惰性视图、广播去重缓存、直转器池化）与内存优化已证实结论。 |
| 动态 proto | [proto-schema.md](proto-schema.md) | .proto 动态加载与 protox wire 数据结构族、WireCache 内容寻址去重、FrozenCache 降级回落、三层正确性防线。 |
| 网络层 | [network-layer.md](network-layer.md) | `network/` 基于 gnet 的 TCP/UDP 连接层：多服务连接池、responseMap 请求-响应匹配、持久化监听队列/回调合表分发、连接级声明式心跳、RTT 测点与收包零分配优化。 |
| 状态层 | [state-store.md](state-store.md) | 单机器人状态存储（五种形态、路径与 Overlay）、过滤器比较语义、Redis 跨节点共享态与租约锁。 |
| 监控 | [monitoring-system.md](monitoring-system.md) | 原子计数 + DDSketch 延迟分布（可严格合并）、Apdex 只对 networked 动作打分、监听等待单列、动作按运行时网络行为分四类（networked/listen/send/local）、错误按 code 单维聚合；导出 Console/HTTP JSON/CSV/pprof。 |
| 错误码 | [error-code-system.md](error-code-system.md) | `errcode` 单维 `ErrorCode`（uint64）+ 码段契约：<100 框架保留段（29 个常量，`errcode/code.go` 清点）、≥100 业务段；`ActionError{Code, Detail}`；`errors.json` 加载期对 <100 撞码硬报错。 |
| 分布式架构 | [distributed-architecture.md](distributed-architecture.md)、[distributed-error-handling.md](distributed-error-handling.md)、[admin-implementation.md](admin-implementation.md)、[agent-implementation.md](agent-implementation.md) | Admin-Agent gRPC 控制面三服务、心跳/租约/离线判定联动、任务状态机（pending→starting→running→stopping→stopped/failed）与单例约束、资源包下发与指标聚合归档。 |
| 日志 | [logging.md](logging.md) | `internal/stresslog`：zap + lumberjack 轮转、日志错误中文、字段名不得与 zap 内建键 collide（`robot` 标识 / `submitter` 协程提交位置，`TestNoZapFieldsCollideWithEncoderKeys` 全仓守护）。 |

另有专题文档：渐进加压 [ramp-up.md](ramp-up.md)、资源同步 [resource-sync.md](resource-sync.md)、前端 API 契约 [frontend-api.md](frontend-api.md)、可视化流程编辑器 [visual-flow-editor.md](visual-flow-editor.md)、部署运维 [deployment.md](deployment.md)；知识库总入口见 [docs/README.md](README.md)。

前端（`cmd/web/`）：React 18 / Vite 8 / TypeScript 5.6 / Ant Design 5 / @xyflow/react 12 / Monaco / Zustand+Zundo / ECharts 6 / idb-keyval / protobufjs（`cmd/web/package.json` 版本逐一核对），构建产物 `dist/` 的托管形态见 [deployment.md](deployment.md)（生产推荐 nginx；Admin 内置 `staticDir` 直连当前与前端 base 不匹配）；前端请求收拢到 `services/api.ts` + `services/baselineApi.ts`，UI 文本不暴露技术术语（Agent→节点、Admin→服务器）。

## 6. 构建、发布与部署

### 6.1 本地构建（Makefile，逐目标核对）

| 目标 | 内容 | 位置 |
|---|---|---|
| `make frontend` | `cd cmd/web && npm install && npm run build` | Makefile:19-21 |
| `make build` | `build-linux` + `build-windows`，**交叉编译 agent 与 admin 到 `bin/`**（CGO_ENABLED=0，注入 `-X main.Version`）；`build-current` 构建当前平台 | Makefile:25-44 |
| `make dev-agent` / `dev-admin` / `dev-stressbot` | `go run ./cmd/{agent,admin,stressbot} -config conf/{agent,admin,stressbot}.toml` | Makefile:48-58 |
| `make check` | `go build ./...` + `cd cmd/web && npx tsc --noEmit` | Makefile:66-69 |
| `make test` | **仅前端测试** `cd cmd/web && npm run test`（Vitest） | Makefile:71-73 |

注意 `make build` 不编译 stressbot 单机二进制——单机入口用 `go build -o stressbot.exe ./cmd/stressbot` 或 `make dev-stressbot` 直接运行。

### 6.2 发布（.github/workflows/release.yml）

- **触发**：推送 `v*` tag（release.yml:3-6）。
- **矩阵**：6 平台交叉编译——linux/amd64、linux/arm64、windows/amd64、windows/arm64、darwin/amd64、darwin/arm64（release.yml:15-21），CGO_ENABLED=0，版本取 tag 名注入 `main.Version`。
- **产物**：每平台编译 **stressbot / agent / admin 三个二进制**（release.yml:46-48）；先 `npm ci && npm run build` 构建前端（release.yml:35-36）。
- **打包**：二进制 + `conf/`（含 flow/proto/scripts/adapter，加载期断言存在，release.yml:77-83）+ `cmd/web/dist`（断言 index.html）+ `cmd/web/nginx.conf`，Windows 打 zip、其余 tar.gz，逐项 `test -f/-x` 自检（release.yml:62-93）。
- **发布**：`softprops/action-gh-release@v3`，自动生成 release notes，上传全部归档（release.yml:96-102）。

### 6.3 演示部署栈（docker-compose.yml）

仓库根 `docker-compose.yml` 编排六个服务（逐服务核对）：

| 服务 | 镜像 | 用途 |
|---|---|---|
| mysql | mysql:latest | Admin 历史归档库（utf8mb4），3306 |
| redis | redis:latest | 跨节点共享状态（appendonly），6379 |
| nginx | nginx:latest | 托管前端 dist（挂载 `cmd/web/dist`）反代管理面，80，配置挂 `conf/deploy/nginx/nginx.conf` |
| loki | grafana/loki:latest | Agent 日志存储，3100 |
| grafana | grafana/grafana:latest | 日志查询面，3000，provisioning 挂 `conf/deploy/grafana/` |
| vector | timberio/vector:latest-alpine | 采集 `log/agent.log` → Loki；`AGENT_ID` 环境变量对应 `conf/agent.toml` 的 agent.id |

即「MySQL + Redis + nginx（应用三件套）+ Vector→Loki→Grafana（Agent 日志观测链）」的演示栈；Vector 的 checkpoint 用 named volume（挂 Windows 宿主目录会因 ENODATA 崩溃循环，docker-compose.yml:88-90 注释）。

## 7. 外部依赖清单

直接依赖读自 `go.mod`（require 主块，go.mod:5-26），按用途归类：

| 依赖 | 版本 | 用途（一句话） |
|---|---|---|
| `github.com/panjf2000/gnet/v2` | v2.10.0 | TCP/UDP 事件驱动网络引擎，`network/` 的收发底座 |
| `github.com/panjf2000/ants/v2` | v2.12.1 | goroutine 协程池，`internal/workpool` 封装（自带 recover） |
| `github.com/yuin/gopher-lua` | v1.1.2 | Lua 5.1 运行时，`script/` 的脚本引擎 |
| `google.golang.org/grpc` | v1.84.0 | Admin↔Agent 控制面 gRPC |
| `google.golang.org/protobuf` | v1.36.12 | protobuf 运行时（动态消息 / proto.Message 接口） |
| `github.com/bufbuild/protocompile` | v0.14.1 | 启动期动态编译加载 .proto 文件（`protocol/protox/loader.go`） |
| `go.uber.org/zap` | v1.28.0 | 结构化日志核心（`internal/stresslog`） |
| `gopkg.in/natefinch/lumberjack.v2` | v2.2.1 | 日志文件轮转（按大小/份数/天数） |
| `github.com/redis/go-redis/v9` | v9.22.0 | Redis 客户端，`state/shared` 跨节点共享状态 |
| `github.com/go-sql-driver/mysql` | v1.10.1 | MySQL 驱动，Admin 历史归档 |
| `github.com/fluxcd/pkg/envsubst` | v1.8.0 | TOML 配置的 `${VAR}` / `${VAR:-default}` 环境变量展开（`config/load.go:170`） |
| `github.com/pelletier/go-toml/v2` | v2.4.3 | TOML 配置解析 |
| `github.com/santhosh-tekuri/jsonschema/v6` | v6.0.3 | flow.json / codec JSON Schema 契约校验（`config/validation/`） |
| `github.com/DataDog/sketches-go` | v1.4.8 | DDSketch 可合并延迟分布（monitor P50~P99） |
| `github.com/shirou/gopsutil/v4` | v4.26.8 | 节点系统资源指标（CPU/内存，Agent 上报） |
| `github.com/bytedance/sonic` | v1.15.4 | 高性能 JSON（`internal/jsonx` 封装） |
| `github.com/getkin/kin-openapi` | v0.149.0 | OpenAPI 契约加载与请求校验（管理面中间件） |
| `github.com/oapi-codegen/nethttp-middleware` | v1.2.0 | net/http 的 OpenAPI 请求校验中间件接线 |
| `github.com/cenkalti/backoff/v4` | v4.3.0 | 指数退避重试（Agent gRPC 重连等，`internal/retry`） |
| `github.com/DATA-DOG/go-sqlmock` | v1.5.2 | MySQL 层单测的驱动 mock |
