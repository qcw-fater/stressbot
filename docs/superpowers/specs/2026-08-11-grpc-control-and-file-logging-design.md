# gRPC 控制面与文件日志架构设计

## 1. 目标与范围

本轮只完成两个架构阶段：

1. 使用原生 `grpc-go` 一次性替换 Admin 与 Agent 之间的 HTTP 控制面。
2. 删除内置日志环形缓冲、日志查询/下载 API 和 stressbot 前端日志界面，使 Admin、Agent 只输出结构化文件日志。

浏览器访问 Admin 的 HTTPS 管理面继续保留，仍使用 REST/OpenAPI。Kafka、Vector、Filebeat、Loki、Elasticsearch、ClickHouse、Grafana 和 Kibana 均不进入本轮代码或部署配置。CEL、事件化 listen、sqlc 作为旧 M6 内容完整保留，等待后续单独讨论。

项目尚未上线，因此不提供旧内部 HTTP 控制面的兼容期、双栈、fallback 或自动迁移逻辑。Admin 与 Agent 按同一版本部署，协议版本不匹配时直接失败。

## 2. 总体架构

Admin 暴露两个相互隔离的入口：

- 管理面：现有 HTTPS REST API 和静态前端，只服务浏览器和管理者。
- 控制面：独立的内网明文 gRPC 监听端口，只服务 Agent，不经过浏览器管理面的 Nginx/HTTPS 入口。

Agent 不再监听任何控制 HTTP 端口，也不向 Admin 轮询任务。Agent 根据配置中的固定 Admin 地址主动建立一个长期复用的 `grpc.ClientConn`。单 Admin 架构不需要 go-zero、etcd、服务发现或负载均衡。

同一连接承载三个独立 RPC：

```proto
service AgentControlService {
  rpc Session(stream AgentEvent) returns (stream AdminEvent);
}

service AgentBundleService {
  rpc DownloadBundle(BundleRequest) returns (stream BundleChunk);
}

service AgentTelemetryService {
  rpc Report(stream TelemetryEnvelope) returns (TelemetryClose);
}
```

- `Session` 是高优先级双向长流，承载注册、应用心跳、任务命令、ACK、任务完成和最终报告。
- `DownloadBundle` 是服务端流，承载不可变任务资源包的分块下载。
- `Report` 是低优先级客户端流，承载可降级的压力指标和系统指标。

## 3. 协议与会话

协议包使用 `control.v1` 命名空间，所有消息使用显式 protobuf 字段和 `oneof`，不使用 JSON、`google.protobuf.Any` 或反射式动态载荷。`AgentEvent` 包含 `hello`、`heartbeat`、`commandAck`、`taskFinished` 和 `finalReport`；`AdminEvent` 包含 `welcome`、`heartbeatAck`、`command`、`serverClosing` 和协议错误，其中 `command` 再以 `oneof` 表达 `startTask`、`stopTask` 和 `shutdown`。

连接建立后的第一条 Agent 消息必须是 `hello`，至少包含：

- `protocolVersion`
- `agentId`
- Agent 软件版本和能力集合
- 当前任务 ID、状态和最近确认的命令序号

Admin 校验声明的 Agent ID 和协议版本。校验成功后建立带递增 `generation` 的会话并发送 `welcome`；同一 Agent 的新会话替换旧会话，旧流的迟到消息因 generation 不匹配被丢弃。Agent ID 为空或协议版本不同均返回明确 gRPC 状态并关闭流。

本轮明确接受以下信任边界：Agent ID 是逻辑标识，不提供密码学身份认证；能够进入控制面内网并访问 gRPC 端口的主机可以尝试冒充 Agent。部署必须让控制面只绑定受控私网地址，并由主机或网络防火墙仅放行 Agent 网段，禁止将该端口发布到公网。若未来出现跨公网、跨不可信网络或内部零信任需求，再单独引入 mTLS、每 Agent Token 或网络层身份方案；当前实现不保留证书配置和隐式安全模式切换。

Admin 的会话注册表只保存在线状态、最后心跳、流发送队列和 generation。每次心跳只更新内存并返回携带新租约截止时间的 `heartbeatAck`，不写数据库。心跳确认使用单槽覆盖语义，避免网络拥塞时累计过期 ACK；控制流发送 owner 保证租约确认不会被普通命令长期饿死。现有 unhealthy/offline 阈值继续生效；状态发生变化时才持久化。

Agent 断线后使用现有统一 backoff 工具做 full-jitter 指数退避，最大等待 30 秒。应用心跳负责业务健康和任务租约；gRPC keepalive 只负责探测死连接，间隔不低于一分钟。

## 4. 命令可靠性

命令采用至少一次投递和幂等执行，不追求跨网络的伪“恰好一次”。

Admin 新增 Goose migration 创建 `agent_commands` 逻辑关联表，保存：

- `command_id`
- `agent_id`
- `task_id`
- `kind`
- protobuf `payload`
- `state`
- `created_at`、`acked_at`
- 拒绝原因

Admin 必须先持久化命令，再把 command ID 投递到在线会话的有界发送队列。Agent 离线或队列暂满时，命令仍保持 pending；建立新会话后按序重放所有未确认命令。ACK 状态分为 `applied`、`duplicate` 和 `rejected`。

Agent 通过现有任务状态机实现语义幂等：

- 相同 task ID 的重复 start 不创建第二个 TaskRunner，直接返回 duplicate。
- stop 对已停止或不存在的相同任务返回 duplicate。
- shutdown 可重复执行。
- 单进程内保留一个有界的最近 command ID LRU，处理任务刚结束后发生的重放。

Agent 进程重启会终止原任务；此时 Admin 重放仍为 pending 的 start 是恢复任务，而不是与旧进程并行重复执行，因此不引入 Agent 本地数据库。

Admin 在 `welcome` 中下发任务租约。运行中的 Agent 如果在租约期内始终无法联系 Admin，必须停止当前任务，避免网络分区产生无人管理的压测流量。

## 5. 任务资源包

Start 命令只携带任务参数、Bundle 摘要和总大小，不内嵌 flow、proto、Lua、adapter 等资源。

Admin 在下发命令前生成确定性的归档包，以 SHA-256 作为内容地址，先写临时文件再原子重命名到 Bundle 缓存目录。相同摘要只保存一份。Agent 使用 `DownloadBundle` 请求摘要和起始 offset：

- 默认分块约 256 KiB。
- `.part` 文件长度作为续传 offset。
- 每块携带 offset，Agent 拒绝乱序数据。
- 下载完成后校验总大小和 SHA-256。
- 校验成功后原子替换并安全解包，拒绝绝对路径和目录穿越。
- 校验失败保留可继续使用的正确前缀或删除无效部分，不启动任务。

Bundle 本身已经归档压缩，不再启用 gRPC 消息压缩，避免重复压缩消耗 CPU。

## 6. 指标流与背压

Telemetry 使用明确的 protobuf DTO 表达现有压力快照、DDSketch、系统资源和采样元数据。Agent 的上报路径不得阻塞 Robot、Manager 或 monitor 热路径。

Agent 分别维护一个 stress 和 system 待发送槽位。新快照到达且旧快照尚未发送时覆盖旧值并增加 `droppedIntervals`。Telemetry 发送循环是该流唯一的 Send owner，不为每条快照创建 goroutine。

Admin 的 Recv 循环只做身份/任务/generation 校验和轻量入队；聚合、采样和历史归档交给现有有界工作机制，禁止在 Recv 循环中执行慢 SQL。队列压力继续向 telemetry 的“保最新”语义传播，不能影响 ControlSession。

任务最终报告不走 Telemetry；它属于可靠业务结果，走 `Session` 并等待 ACK。

## 7. 性能设计

性能目标以 5,000 个在线 Agent 为容量设计上限，分 10、100、1,000、5,000 四档观察。实现遵守以下约束：

- 每个 Agent 复用一个 ClientConn，不为心跳、命令或指标重复建连。
- 每条流只有一个 Send owner 和一个 Recv owner；其他调用方只向有界队列投递。
- 不为每条消息创建 goroutine，不在持锁期间调用流 Send、Recv 或数据库。
- 心跳不写数据库；命令和状态变化才持久化。
- 控制面消息设置明确的小体积上限，Bundle 使用固定分块，避免单消息放大内存。
- 首版使用 grpc-go 默认动态流控，不凭经验配置静态 HTTP/2 window。
- Bundle 下载限制 Admin 同时读取文件的数量，并验证下载期间控制命令延迟不被显著放大。
- Telemetry 队列容量固定、可观测且允许丢弃中间采样。
- protobuf 结构在循环外复用不变元数据；不得用 map/JSON 在热路径组装消息。

预期 gRPC 会降低 JSON 编解码、HTTP 请求对象、请求头和路由开销；长期 HTTP/2 连接会增加 Admin 的空闲连接内存。两者分别观测，不能用单一“性能提高”掩盖内存斜率。

## 8. 文件日志设计

删除整个进程内日志查询体系：

- 删除 `logview` 包及 RingBuffer zap core。
- 删除 Admin/Agent 日志查询、日志文件列表和下载 API。
- 删除 Admin 代理 Agent 日志的逻辑。
- 从管理 OpenAPI、生成客户端和前端服务中删除日志接口。
- 删除前端 `LogsTab`、日志 cursor/seek 状态和相关样式、测试。

Admin、Agent 继续使用 zap + lumberjack 输出本地文件。文件固定为一行一个 JSON 对象，基础字段使用可读且稳定的名称：

- `timestamp`：UTC RFC3339Nano
- `level`
- `message`
- `caller`
- `stacktrace`
- `service`：`admin` 或 `agent`
- `agentId`、`taskId`、`commandId`：有上下文时输出
- `component`
- `error`

控制台可继续使用人类可读格式，不属于外部日志契约。业务日志和错误信息保持中文。

文件 sink 使用 zap 的有界缓冲写入，默认缓冲 256 KiB、最长一秒刷新；进程正常退出和 Error/DPanic/Fatal 时主动 Sync。低级别热路径只用强类型 `zap.Field`，默认关闭 Debug，不在心跳、逐帧、逐指标循环输出 Info。日志轮转仍由 lumberjack 负责。

stressbot 不包含 Kafka producer 或任何外部采集 SDK。未来 Vector/Filebeat 只需 tail 这些 JSON 文件，不要求重新修改 Agent/Admin 数据面。

## 9. 删除和配置边界

gRPC 阶段删除：

- `api/openapi/control-plane.yaml` 及其生成配置、生成 Go 代码。
- `admin/agent_dispatcher.go` 和 HTTP dispatcher 测试。
- `agent/http_client.go`、`agent/http_server.go` 及相关测试。
- Admin Agent 上行 HTTP 路由和 pending-task 路由。
- Agent 控制 `port`、`publicUrl`、HTTP TLS 和 HTTP fallback 配置。
- Admin/Agent 控制面的 `certFile`、`keyFile`、`peerCaFile` 配置及证书身份绑定代码。

保留浏览器管理 OpenAPI；只删除其中不再存在的日志管理端点。Agent 不再开放 `/healthz`，Admin 通过 gRPC 会话和应用心跳判断 Agent 健康。

## 10. 故障处理

- gRPC `Canceled`：正常停止，不输出 Error。
- gRPC `Unavailable`：进入退避重连；运行任务受租约保护。
- `FailedPrecondition`：协议版本不匹配，fail loud，不降级 HTTP。
- Bundle `NotFound`/摘要失败：拒绝任务并 ACK rejected，不启动不完整配置。
- Telemetry 拥塞：覆盖旧采样并计数，不升级为任务失败。
- Control 发送失败：保留数据库 pending，等待新会话重放。
- Admin 关闭：停止接收新会话，给现有流发送关闭提示，在宽限期内完成 ACK 后关闭 gRPC server。

所有后台循环必须受 context 控制并可退出。gRPC 库内部 handler goroutine 不再额外包一层 goroutine；应用主动创建的后台任务继续走项目协程池。

## 11. 分阶段提交

所有修改直接在 `master` 工作副本完成，不创建分支或 worktree，并保护当前工作区内与本任务无关的用户修改。

1. 设计阶段：本设计文档。
2. gRPC 阶段：proto、生成代码、会话/命令/Bundle/Telemetry、Goose migration、配置切换以及内部 HTTP 控制面删除。
3. 日志阶段：RingBuffer、日志 API 和前端日志界面删除，结构化文件字段与缓冲写入落地。

每个实现阶段独立提交。提交前只暂存本阶段明确文件，绝不使用 `git add -A` 吞入现有用户修改。

## 12. 验证策略

用户已决定本地暂缓大规模性能验收和完整运行测试，因此以下项目不作为本轮实现提交的阻塞门禁：5,000 Agent 压测、Bundle 并发性能、2～5 分钟分布式运行验证和完整日志采集验证。

实现时仍执行必要的最低成本检查：protobuf/生成物一致、Go 编译、前端 TypeScript 编译，以及与变更直接相关的快速单元测试。无法在本地完成的项目明确记录为延后，不把“未执行”写成“通过”。

后续正式验收应保存旧 HTTP + RingBuffer 基线，再比较 CPU、RSS、goroutine、GC、网络字节、命令延迟、`ns/op`、`B/op` 和 `allocs/op`。预期 gRPC 的持续 CPU/分配/网络开销下降，日志编码分配和常驻内存必须下降；核心压测吞吐不得因控制面和日志改造回退。

## 13. 明确不做

- 不引入 go-zero、etcd、服务注册中心或消息队列。
- 不为 Admin↔Agent 控制面启用 TLS/mTLS；其网络隔离由私网监听和防火墙承担。
- 不实现 Kafka/Loki/Elasticsearch/ClickHouse/Grafana/Kibana 部署配置。
- 不在 stressbot 前端保留日志入口或跳转占位。
- 不保留旧 HTTP 控制面、日志 API 或配置兼容层。
- 不在本轮修改 CEL、condition parser、listen 调度或 SQL 查询生成方式。
