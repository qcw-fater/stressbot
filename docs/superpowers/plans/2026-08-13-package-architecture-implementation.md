# Package Architecture Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 stressbot 后端迁移到已确认的功能模块目录，并保持所有运行模式、配置、协议和监控行为不变。

**Architecture:** 公共数据面按 `flow/binding/engine/robot/network/script/monitor/state/protocol` 分层；Admin、Agent 的专属实现进入各自 `internal`；运行装配收敛到 `runner`，单机应用进入 `standalone`；协议源和生成物分别放入 `controlplane/proto` 与 `controlplane/pb`。

**Tech Stack:** Go 1.26、gRPC/protobuf、gnet、gopher-lua、Redis、MySQL、React/Vite/Vitest。

---

## Task 1：建立迁移基线与文档

- [ ] 确认 `git status --short`，保留 `.zcode/`、`nul` 等用户文件。
- [ ] 记录当前 `go list ./...` 包集合和反向依赖。
- [ ] 使用仓库内 `.tmp/gocache` 运行 `go test ./...` 与 `go build ./...`，记录既有失败。
- [ ] 将本设计与实施计划加入 `docs/superpowers`，不自动提交。

## Task 2：迁移控制面、状态和基础设施包

**Files:**

- Move: `controlplane/controlv1/control.proto` → `controlplane/proto/control.proto`
- Move: `controlplane/controlv1/*.pb.go` → `controlplane/pb/`
- Move: `controlplane/convert_monitor.go` → `controlplane/monitor.go`
- Move: `sharedstate/*` → `state/shared/`
- Move: `utils/jsonx/*` → `internal/jsonx/`
- Move: `utils/log/*` → `internal/stresslog/`
- Move: `utils/{daemon,retry,timerpool,work_pool,sched_http}.go` → 对应 `internal` 子包
- Move: `utils/{config,duration,pprof}.go`、`schema/`、`schemas/` → `config/`

- [ ] 修改 proto 的 `package stressbot.control` 与 `go_package = "stressbot/controlplane/pb;controlpb"`。
- [ ] 使用仓库既有生成工具链重新生成 pb；若工具缺失，只做可核验的等价生成物迁移，不引入新工具配置。
- [ ] 将所有 `controlv1` 引用改为 `controlpb`，运行时协议版本字段保持不变。
- [ ] 将 `sharedstate` 包名改为 `shared` 并更新调用方。
- [ ] 为每个基础设施子包使用与目录一致的包名，更新生产代码与测试引用。
- [ ] 运行 `go test ./controlplane/... ./state/... ./config/... ./internal/...`。
- [ ] 运行 `go build ./...`。

## Task 3：收敛游戏协议处理

**Files:**

- Move: `adapter/*` → `protocol/`
- Move: `codec/*` → `protocol/codec/`
- Move: `protox/*` → `protocol/protox/`

- [ ] 保持根 `protocol` 包名为 `protocol`，接口名继续使用 `Adapter`、`CodecResolver`、`SchemaAdapter`。
- [ ] 更新 codec 与 protox 的内部 import 路径及全仓调用方。
- [ ] 检查协议热路径没有新增包装、复制或缓存层。
- [ ] 运行 `go test ./protocol/...` 和相关 `network/robot/script` 测试。
- [ ] 运行 `go build ./...`。

## Task 4：抽取流程模型与字段绑定

**Files:**

- Create: `binding/{field.go,filter.go,constants.go,validate.go}`
- Create: `flow/{flow.go,action.go,on_error.go,condition.go,condition_compile.go,condition_parser.go,prepare.go}`
- Modify: `engine/*.go` 仅保留执行、心跳计划和动作执行逻辑

- [ ] 先移动无执行状态的绑定类型、过滤器和常量到 `binding`。
- [ ] 再移动 `TaskFlow`、节点、动作、监听、onError、条件 AST/编译模型到 `flow`。
- [ ] 必要的运行期已编译字段通过 `flow` 导出只读接口访问，禁止复制两套条件模型。
- [ ] 更新 `robot/script/admin/agent/runner` 的模型引用。
- [ ] 运行 `go test ./binding/... ./flow/... ./engine/... ./robot/...`。
- [ ] 运行 `go build ./...`。

## Task 5：抽取公共运行装配和单机应用

**Files:**

- Create: `runner/{runner.go,config.go,resources.go,start.go,cleanup.go}`
- Create: `standalone/{app.go,config.go}`
- Modify: `cmd/stressbot/main.go`、Agent task runner 调用点

- [ ] 盘点单机入口和 Agent TaskRunner 的资源加载、codec/proto/flow 初始化、机器人启动及清理路径。
- [ ] 将共用装配按资源加载、启动、清理拆入 `runner`，保持两个消费侧同步。
- [ ] 将 TOML 单机配置与应用生命周期移入 `standalone`。
- [ ] 将 `cmd/stressbot` 缩减为 flag 解析和 `standalone.Run` 调用。
- [ ] 运行 `go test ./runner/... ./standalone/... ./cmd/stressbot/... ./agent/...`。
- [ ] 运行 `go build ./...`。

## Task 6：重组 Agent

**Files:**

- Keep: `agent/app.go`、`agent/config.go`
- Move: 会话与 gRPC 客户端 → `agent/internal/session/`
- Move: 命令执行与 outbox → `agent/internal/command/`
- Move: 资源包缓存 → `agent/internal/bundle/`
- Move: 任务执行与报告 outbox → `agent/internal/task/`
- Move: 系统与压力指标上报 → `agent/internal/metrics/`

- [ ] 为跨子包协作定义最小接口或参数对象，禁止形成环依赖。
- [ ] 根 `agent` 的 `App` 负责组装和生命周期，不重新暴露全部内部类型。
- [ ] 将测试跟随被测实现移动，跨模块行为测试保留在根包。
- [ ] 运行 `go test ./agent/... ./cmd/agent/...`。
- [ ] 运行 `go build ./...`。

## Task 7：重组 Admin

**Files:**

- Keep: `admin/app.go`、`admin/config.go`
- Move: 任务状态机和分配 → `admin/internal/task/`
- Move: 节点注册与健康状态 → `admin/internal/agent/`
- Move: 命令存储/总线 → `admin/internal/command/`
- Move: 内容寻址资源包 → `admin/internal/bundle/`
- Move: codec 基线分发 → `admin/internal/baseline/`
- Move: 指标摄取/窗口/聚合 → `admin/internal/metrics/`
- Move: 历史归档 → `admin/internal/history/`
- Move: 流程、动作、监听模板 → `admin/internal/template/`
- Move: HTTP 管理面 → `admin/internal/httpapi/`
- Move: gRPC 控制面 → `admin/internal/grpcapi/`
- Move: MySQL 迁移 → `admin/internal/mysql/migrations/`
- Move: OpenAPI 源和生成代码 → `api/admin/`

- [ ] 先提取存储/状态模块，再迁移 HTTP/gRPC 适配层，根 `admin.App` 最后装配。
- [ ] 保持 HTTP `/sbot/` 路由、gRPC 服务、数据库 schema 与任务状态机不变。
- [ ] 删除旧 `managementapi` 目录，不保留旧生成包别名。
- [ ] 运行 `go test ./admin/... ./api/admin/... ./cmd/admin/...`。
- [ ] 运行 `go build ./...`。

## Task 8：清理和架构审查

- [ ] 删除已经为空的 `adapter/codec/protox/sharedstate/schema/schemas/utils` 与 `controlplane/controlv1`。
- [ ] `rg` 检查旧 import 路径和禁用命名全部为零匹配。
- [ ] 使用 `go list -deps` 检查应用层、数据面和 internal 边界。
- [ ] 检查所有 goroutine 仍通过 `internal/workpool`。
- [ ] 检查控制面生成代码只位于 `controlplane/pb`，proto 只位于 `controlplane/proto`。
- [ ] 执行 backend-review，重点检查错误码、并发、接口同步、监控口径和 Admin/Agent 通信。

## Task 9：全量验证

- [ ] `$env:GOCACHE='<repo>/.tmp/gocache'; go build ./...`
- [ ] `$env:GOCACHE='<repo>/.tmp/gocache'; go test ./...`
- [ ] `cd cmd/web; npx.cmd tsc -b`
- [ ] `cd cmd/web; npm.cmd run test`
- [ ] 启动前端编辑器并打开当前 `flow.json`，确认校验报告无错误。
- [ ] 使用当前 TOML 配置启动受影响的后端模式 2–5 分钟，记录精确 PID 并在结束后只停止该 PID。
- [ ] 审查本轮日志中的 `error|warn|失败`，区分预期环境错误与回归。
- [ ] 展示完整变更文件清单，等待用户确认后再进行任何 Git 提交。
