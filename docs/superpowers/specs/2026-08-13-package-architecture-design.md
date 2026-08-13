# 后端包架构重构设计

## 目标

在不改变压测流程、控制面字段编号与业务语义、监控口径和配置格式的前提下，按真实业务职责组织 Go 后端。目录名应直接说明用途，进程入口、应用装配和可复用能力之间保持单向依赖。

## 设计原则

1. `cmd/*` 负责进程级事务：CLI、daemon、日志初始化、pprof、系统信号、顶层 panic 和退出码。
2. `admin`、`agent`、`standalone` 负责各自应用的装配与运行，通过 `context.Context` 接收生命周期控制，不解析全局 flag，也不调用 `os.Exit`。
3. Admin/Agent 的功能包直接位于应用目录下一层。这里的包名已经提供足够边界，不再套一层只增加深度的应用内 `internal`。
4. 只有跨应用复用且不属于业务域的基础设施放在仓库根 `internal`。
5. 游戏协议适配、声明式 codec 和动态 protobuf 统一位于 `protocol`。
6. `flow` 保存流程模型，`binding` 保存字段绑定和过滤规则，`engine` 只负责执行。
7. 本地状态位于 `state`，Redis 跨节点共享状态位于 `state/shared`。
8. 控制面协议源文件与生成物分离：`controlplane/proto` 保存 `.proto`，`controlplane/pb` 只保存生成代码。
9. `api/admin/openapi.yaml` 是浏览器管理 API 的唯一契约。后端运行时校验、前端类型生成和 Swagger UI 都使用它，不生成未被生产代码使用的 Go API 模型。
10. 当前是第一版数据库，只初始化当前 schema，不维护 migration 版本、旧库协调逻辑或迁移锁。

控制面命名在首版正式定型为 protobuf package `stressbot.control`，指标服务和消息统一使用 `AgentMetricsService` / `Metrics*`。生成代码仍与源 `.proto` 分目录保存，不引入目录或包名版本后缀。该命名调整保留既有字段编号与业务语义，但 RPC 全名发生变化，因此重构前后的 Admin/Agent 二进制不支持混合部署，必须同批升级。

## 最终目录

```text
stressbot/
├─ cmd/
│  ├─ admin/
│  │  └─ main.go
│  ├─ agent/
│  │  └─ main.go
│  └─ stressbot/
│     └─ main.go
├─ admin/
│  ├─ app.go
│  ├─ config.go
│  ├─ server.go
│  ├─ agent/
│  ├─ apierror/
│  ├─ bundle/
│  ├─ command/
│  ├─ grpcapi/
│  ├─ history/
│  ├─ httpapi/
│  ├─ metrics/
│  ├─ mysql/
│  │  ├─ open.go
│  │  ├─ schema.go
│  │  └─ schema_definition.go
│  ├─ task/
│  └─ template/
├─ agent/
│  ├─ app.go
│  ├─ config.go
│  ├─ grpc_client.go
│  ├─ grpc_convert.go
│  ├─ session.go
│  ├─ types.go
│  ├─ bundle/
│  ├─ command/
│  ├─ metrics/
│  ├─ session/
│  └─ task/
├─ standalone/
│  ├─ app.go
│  └─ config.go
├─ runner/
├─ flow/
├─ binding/
├─ engine/
├─ robot/
├─ network/
├─ script/
├─ monitor/
├─ state/
│  └─ shared/
├─ protocol/
│  ├─ codec/
│  └─ protox/
├─ controlplane/
│  ├─ monitor.go
│  ├─ proto/
│  │  └─ control.proto
│  └─ pb/
│     ├─ control.pb.go
│     └─ control_grpc.pb.go
├─ api/
│  └─ admin/
│     ├─ embed.go
│     └─ openapi.yaml
├─ config/
│  └─ validation/
├─ errcode/
└─ internal/
   ├─ daemon/
   ├─ debughttp/
   ├─ jsonx/
   ├─ lru/
   ├─ retry/
   ├─ stresslog/
   ├─ timerpool/
   └─ workpool/
```

测试文件与各实现放在同一包目录，未在上图逐项列出。

`config/validation` 负责 flow/codec JSON Schema 配置契约校验；领域解码与业务不变量校验由各调用方负责。

## 入口契约

- `cmd/admin`：加载 `admin.Config`，初始化日志/pprof和信号上下文，创建 `admin.AdminServer` 并调用 `Run(ctx)`。
- `cmd/agent`：加载 `agent.AppConfig`，初始化日志/pprof和信号上下文，通过 `agent.NewFromConfig` 创建节点并调用 `Run(ctx)`。
- `cmd/stressbot`：加载 `standalone.Config`、解析资源路径、初始化日志/pprof和信号上下文，调用 `standalone.Run(ctx, cfg, paths)`。

三个应用包都不暴露 `Main` 包装函数。只有 `cmd/*` 决定进程退出码。

## 依赖方向

```text
cmd/* → admin | agent | standalone
admin / agent → controlplane / runner / 各自功能包
standalone → runner
robot → engine / network / script / monitor
network / script → engine
engine → flow / binding / protocol / state
```

`protocol`、`flow`、`binding`、`engine`、`robot` 等公共包不得反向依赖 Admin、Agent 或 standalone。Admin 与 Agent 的功能包只由对应应用树使用，但不再靠多余的目录层级表达这一事实。

## OpenAPI 与数据库边界

- `/sbot/openapi.yaml` 返回嵌入的 `api/admin/openapi.yaml`。
- `/sbot/docs` 使用同一契约展示 Swagger UI。
- 后端继续进行 OpenAPI 请求校验；前端通过 `openapi-typescript` 生成 TypeScript 类型。
- 不保留 `oapi-codegen` Go 模型生成入口及其工具依赖。
- `admin/mysql/schema_definition.go` 以内置 Go DDL 保存当前完整建表语句，Admin 在开放监听端口前调用 `InitializeSchema`。
- 不创建 Goose 版本表，不保留编号 migration、旧 schema reconcile、迁移锁或迁移运维手册。
