# Package Architecture Correction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修正首次包迁移中过度使用 `internal`、应用包承载 CLI、无效 OpenAPI Go 生成链和首版数据库历史迁移等问题，使 Admin、Agent、Standalone 使用同一套职责边界。

**Architecture:** `cmd/admin`、`cmd/agent`、`cmd/stressbot` 统一负责参数、daemon、日志、pprof、顶层 panic 和退出码；`admin`、`agent`、`standalone` 负责应用装配与运行。Admin/Agent 专属功能包直接位于各自应用目录下一层。管理面以 `api/admin/openapi.yaml` 为唯一契约，后端运行时校验、前端类型生成和 Swagger UI 共用它；首版数据库只执行幂等 schema 初始化，不维护 migration 版本历史。

**Tech Stack:** Go 1.26、OpenAPI 3.0、Swagger UI、gRPC/protobuf、MySQL、React/Vite/Vitest。

---

### Task 1: 固定 OpenAPI 与首版数据库边界

**Files:**
- Delete: `api/admin/generate.go`
- Delete: `api/admin/generate.yaml`
- Delete: `api/admin/admin.gen.go`
- Modify: `api/admin/embed.go`
- Create: `admin/mysql/schema_definition.go`
- Create: `admin/mysql/schema.go`
- Delete: `admin/internal/mysql/migrations/`
- Delete: `admin/internal/mysql/migrator*.go`
- Modify: `admin/app.go`
- Modify: `go.mod`

- [ ] 增加契约测试，证明生产代码只需要嵌入的 OpenAPI 文档，并确认生成 Go 模型没有调用方。
- [ ] 运行定向测试，确认测试因待删除生成链或待实现 schema 初始化接口失败。
- [ ] 用代码内 `currentSchema` 的 `CREATE TABLE IF NOT EXISTS` 初始化空库；不创建 Goose 版本表，不兼容旧 schema。
- [ ] 将 OpenAPI 文档作为唯一契约提供给运行时校验、前端生成与 Swagger UI。
- [ ] 删除 `oapi-codegen`、Goose 以及仅由它们引入的间接依赖后执行 `go mod tidy`。
- [ ] 运行 `go test ./api/admin ./admin/mysql/... ./admin/...`。

### Task 2: 修正 Admin 包结构与入口

**Files:**
- Move: `admin/internal/{agent,apierror,bundle,command,grpcapi,history,httpapi,metrics,mysql,task,template}` → `admin/{...}`
- Merge: `admin/internal/baseline/resources.go` → `admin/httpapi/resources.go`
- Delete: `admin/cli.go`
- Modify: `cmd/admin/main.go`
- Rename: `admin/management_server.go` → `admin/server.go`

- [ ] 先通过包列表测试固定 `admin/internal` 不再存在、功能包直接位于 `admin` 下一层。
- [ ] 上浮包并机械更新全部 import；删除 `baseline` 单文件包并收进唯一使用方 `httpapi`。
- [ ] 将参数、daemon、日志、pprof、panic 和退出码移回 `cmd/admin/main.go`。
- [ ] 保持 `admin.NewAdminServer`、`Run`、`Shutdown` 以及 HTTP/gRPC 行为不变。
- [ ] 运行 `go test ./admin/... ./cmd/admin/...` 和 `go build ./cmd/admin`。

### Task 3: 修正 Agent 包结构与入口

**Files:**
- Move: `agent/internal/{bundle,command,metrics,session,task}` → `agent/{...}`
- Delete: `agent/cli.go`
- Modify: `cmd/agent/main.go`
- Modify: `agent/app.go`
- Modify: `agent/config.go`
- Modify: `agent/session.go`
- Modify: `agent/grpc_client.go`
- Modify: `agent/grpc_convert.go`

- [ ] 先通过包列表测试固定 `agent/internal` 不再存在、功能包直接位于 `agent` 下一层。
- [ ] 上浮包并更新 import，保持根包中的应用级会话协调代码，不制造反向依赖或循环依赖。
- [ ] 将参数、daemon、日志、pprof、panic 和退出码移回 `cmd/agent/main.go`。
- [ ] 保持命令可靠性、报告确认、租约、重连和任务状态语义不变。
- [ ] 运行 `go test ./agent/... ./cmd/agent/...` 和 `go build ./cmd/agent`。

### Task 4: 统一 Standalone 与命令入口

**Files:**
- Modify: `standalone/app.go`
- Modify: `standalone/config.go`
- Modify: `standalone/app_test.go`
- Modify: `cmd/stressbot/main.go`
- Create: `cmd/stressbot/main_test.go`

- [ ] 先增加测试，固定资源路径解析和配置加载可由 `cmd/stressbot` 调用，而 `standalone` 不再读取全局 flags 或调用 `os.Exit`。
- [ ] 将 CLI、daemon、日志、pprof、panic、退出码移到 `cmd/stressbot/main.go`。
- [ ] 为 `standalone` 暴露显式配置加载、路径解析和 `Run` 入口。
- [ ] 保持流程、协议、Lua、Redis、监控、CSV 与停止行为不变。
- [ ] 运行 `go test ./standalone/... ./cmd/stressbot/...` 和 `go build ./cmd/stressbot`。

### Task 5: 文档、依赖和全量验证

**Files:**
- Modify: `AGENTS.md`
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-08-13-package-architecture-design.md`
- Modify: `docs/runbooks/mysql-migration.md`
- Modify: `docs/superpowers/plans/2026-08-13-package-architecture-implementation.md`

- [ ] 删除所有 `admin/internal`、`agent/internal`、`admin.Main`、`agent.Main`、`standalone.Main`、Goose 和 `oapi-codegen` 残留引用。
- [ ] 更新架构文档为直接功能子包、首版 schema 初始化和统一命令入口。
- [ ] 使用仓库内 `.tmp/gocache` 执行 `go test ./...` 与 `go build ./...`。
- [ ] 执行 `npx.cmd tsc -b`、`npm.cmd run test` 和 `npm.cmd run generate:api` 后检查生成文件无意外差异。
- [ ] 核对工作树，仅报告本次重构结果，不提交、不暂存。
