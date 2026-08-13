# Package Architecture Closeout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 收敛包架构重构后的类型所有权、命名、HTTP 文件职责、当前文档和 Git 暂存边界，保持运行行为与数据契约不变。

**Architecture:** 领域类型只从真实所属包导出，调用方使用包限定名，不通过 `engine`、`httpapi`、`history`、`metrics` 或 `template` 再导出别名。`admin/httpapi` 仍是单一 HTTP 包，只把大型 `routes.go` 按资源职责拆文件。控制面保留用户确认的 `stressbot.control` 与 `Metrics*` 首版名称，不恢复 `v1` 或 `Telemetry`。

**Tech Stack:** Go 1.26、net/http、gRPC/protobuf、React/TypeScript、Markdown、PowerShell。

---

### Task 1: 建立结构回归契约

**Files:**
- Create: `internal/architecturetest/package_boundaries_test.go`

- [x] **Step 1: 写失败的架构测试**

  用 `go/parser` 扫描 `admin/`、`agent/`、`engine/` 的非测试 Go 文件，拒绝 `type X = Y`；单独拒绝 `engine.NewActionError` 变量转发；要求 `admin/httpapi` 存在 `task_routes.go`、`agent_routes.go`、`metrics_routes.go`、`baseline_routes.go`，并要求 `routes.go` 不再包含这些资源 handler；拒绝活动 Go 源码中的 `TelemetrySink`。

- [x] **Step 2: 运行 RED**

  Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test -count=1 ./internal/architecturetest`

  Expected: FAIL，报告现有导出别名、`engine.NewActionError`、缺失的路由职责文件以及 `TelemetrySink`。

### Task 2: 收敛错误与指标命名

**Files:**
- Modify: `engine/errors.go`
- Modify: `engine/*.go`, `network/*.go`, `robot/*.go`, `script/*.go` 及对应测试中的 `ActionError`/`NewActionError` 引用
- Modify: `agent/metrics/reporter.go`

- [x] **Step 1: 删除 `engine` 的错误类型和构造器转发**

  `ActionError` 与 `NewActionError` 统一改为 `errcode.ActionError` 与 `errcode.NewActionError`，保留 `engine/errors.go` 中三个流程配置错误哨兵。

- [x] **Step 2: 将 `TelemetrySink` 改为 `MetricsSink`**

  只改 Go 接口名称和构造函数参数类型，不改 protobuf 字段、RPC 方法或发送行为。

- [x] **Step 3: 运行定向测试**

  Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test -count=1 ./engine ./network ./robot ./script ./agent/metrics`

  Expected: PASS。

### Task 3: 删除应用功能包的导出别名门面

**Files:**
- Modify: `admin/httpapi/*.go`
- Modify: `admin/history/*.go`
- Modify: `admin/metrics/*.go`
- Modify: `admin/template/*.go`
- Modify: `agent/session.go`

- [x] **Step 1: 更新生产调用点为包限定名**

  `httpapi` 直接使用 `admintask`、`agent`、`metrics`、`history`、`template`、`apierror`；`history` 直接使用 `admintask` 与 `metrics`；`metrics` 直接使用 `agent`；`template` 只保留真实 `IDPolicy`/`Snapshot` 类型；`agent` 根包直接使用 `session.Sender`。

- [x] **Step 2: 更新测试构造值**

  测试代码直接导入真实所属包，不在生产文件保留仅供测试的别名；测试专用 helper 仅允许存在于 `_test.go`。

- [x] **Step 3: 运行 Admin/Agent 定向测试**

  Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test -count=1 ./admin/... ./agent/...`

  Expected: PASS。

### Task 4: 按资源职责拆分 HTTP 路由实现

**Files:**
- Modify: `admin/httpapi/routes.go`
- Create: `admin/httpapi/task_routes.go`
- Create: `admin/httpapi/agent_routes.go`
- Create: `admin/httpapi/metrics_routes.go`
- Create: `admin/httpapi/baseline_routes.go`

- [x] **Step 1: 保留统一注册入口**

  `routes.go` 只保留 `baselineResources`、`registerManagementRoutes`、`CapabilitiesResponse` 和 `handleCapabilities`。

- [x] **Step 2: 原样移动 handler**

  任务 CRUD/启动停止放 `task_routes.go`，节点管理放 `agent_routes.go`，指标和系统资源放 `metrics_routes.go`，基线资源和安全写盘放 `baseline_routes.go`。移动过程不改变有效路由、状态码、JSON 字段或错误处理；另以独立测试删除无调用且把 TOML 当 JSON 返回的废弃 `/sbot/baseline/config.json`。

- [x] **Step 3: 运行 HTTP/OpenAPI 回归测试**

  Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test -count=1 ./admin/httpapi`

  Expected: PASS。

### Task 5: 同步当前文档与工具忽略规则

**Files:**
- Modify: `.gitignore`
- Modify: `README.md`
- Modify: `AGENTS.md`
- Modify: `docs/superpowers/specs/2026-08-13-package-architecture-design.md`
- Modify: current implementation documents under `docs/`
- Modify: `cmd/web/src/services/taskActions.ts`

- [x] **Step 1: 修正当前入口与包名**

  README 使用 `stressbot.toml`、`agent.toml`、`admin.toml` 及 `state/shared`；AGENTS 的运行验证使用实际入口和日志文件；前端注释使用 `state/shared.UsesShare`。

- [x] **Step 2: 记录控制面首版命名决策**

  架构设计明确字段号和业务语义保持不变，但 protobuf package、服务与消息名称按首版最终命名调整，因此不支持与重构前二进制混部。

- [x] **Step 3: 处理过时实现文档**

  当前实现文档改为现有 gRPC、独立三进程和外部日志栈；纯历史设计/实施计划保留当时名称。无法经济更新且整体已失效的文档在标题处明确标记历史，避免被当作当前契约。

- [x] **Step 4: 忽略工具会话产物**

  在 `.gitignore` 的 AI/工具产物段加入 `.zcode/`，不删除用户已有文件，也不强制纳入提交。

### Task 6: GREEN 与提交前验证

**Files:**
- Modify: `docs/superpowers/plans/2026-08-13-package-architecture-closeout.md`（勾选完成项）

- [x] **Step 1: 运行结构契约与格式检查**

  Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test -count=1 ./internal/architecturetest`

  Run: `gofmt -w <本次修改的 Go 文件>`

  Run: `git diff --check`

- [x] **Step 2: 运行完整后端验证**

  Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go build ./...`

  Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test -count=1 ./...`

  Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go vet ./...`

  Run: `go mod verify`

  Run: `go mod tidy -diff`

- [x] **Step 3: 运行完整前端验证**

  Run: `cd cmd/web; npx.cmd tsc -b`

  Run: `cd cmd/web; npm.cmd run test -- --run`

  Run: `cd cmd/web; npm.cmd run build`

- [x] **Step 4: 审查待提交边界**

  用 `git status --short --untracked-files=all`、`git diff --stat` 和逐组 diff 核对全部重构文件；确认 `.zcode/` 被忽略且未暂存。向用户展示按“代码重构 / 文档同步”分组的完整文件清单，取得确认后才能暂存和提交。
