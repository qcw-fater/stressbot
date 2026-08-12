# Structured File Logging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 删除 stressbot 内置日志留存、查询、下载与前端展示链路，使 Admin、Agent 只输出适合 Vector/Filebeat tail 的稳定 JSON Lines 文件日志，并降低同步写与重复编码成本。

**Architecture:** zap 仍是唯一日志 API，lumberjack 负责轮转；文件 core 使用可读字段名、UTC RFC3339Nano 和 `zapcore.BufferedWriteSyncer`，控制台 core 保持人类可读。进程退出和高严重级别显式刷新。stressbot 不持有日志 RingBuffer，不暴露日志 HTTP API，也不包含外部日志 SDK。

**Tech Stack:** Go 1.26、zap 1.28、zapcore.BufferedWriteSyncer、lumberjack 2、OpenAPI 3.0.3、oapi-codegen、openapi-typescript、React 18。

---

## Task 1: Replace the logger core with a buffered JSON file sink

**Files:**

- Modify: `utils/log/logger.go`
- Create: `utils/log/logger_test.go`
- Modify: `cmd/admin/main.go`
- Modify: `cmd/agent/main.go`

- [x] Replace short file keys `T/L/M/C/S/SR` with `timestamp`, `level`, `message`, `caller`, `stacktrace`, `service`.
- [x] Encode file timestamps as UTC RFC3339Nano. Keep duration numeric encoding stable and caller short.
- [x] Keep console output as a separate console encoder; console formatting is not an external contract.
- [x] Wrap only the file sink with `zapcore.BufferedWriteSyncer{Size: 256 << 10, FlushInterval: time.Second}`. Retain handles for explicit `Sync`/`Stop`; do not buffer stdout.
- [x] Make initialization return/record a close function that flushes buffered bytes and closes lumberjack exactly once. Normal Admin/Agent main paths defer it immediately after initialization.
- [x] Ensure `Error`, `DPanic`, `Fatal` and top-level panic paths call Sync before returning/exiting. Ignore only known invalid-handle sync errors from stdout/stderr; preserve real file flush errors in stderr.
- [x] Keep structured `zap.Field` context at call sites for `service`, `agentId`, `taskId`, `commandId`, `component`; no `context.Value` lookup or redundant helper layer was introduced.
- [x] Keep dynamic AtomicLevel behavior and `DebugEnabled` fast path.

**Checks:**

```powershell
$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test ./utils/log -run 'Test(FileEncoder|BufferedFlush)' -count=1
```

## Task 2: Remove the in-process ring buffer and backend log APIs

**Files:**

- Delete: `logview/attach.go`
- Delete: `logview/capture.go`
- Delete: `logview/ringbuffer.go`
- Modify: `cmd/admin/main.go`
- Modify: `cmd/agent/main.go`
- Modify: `admin/admin.go`
- Modify: `admin/handlers.go`
- Modify: `admin/helpers.go`
- Modify: `api/openapi/admin.yaml`
- Generate: `admin/managementapi/admin.gen.go`

- [x] Remove `AttachRingBuffer`, `GetRingBuffer`, log cursor query helpers and log file path helpers. Keep `ReplaceLogger` only as the existing cross-package test injection seam.
- [x] Remove Admin log proxy/download clients and the six `/sbot/logs/*` handlers/routes.
- [x] Remove Admin and Agent file-list/download implementations; do not leave redirects, disabled routes or Grafana jump endpoints.
- [x] Delete log schemas/operations from management OpenAPI and regenerate Go management models.
- [x] Verify management OpenAPI runtime validation still loads and non-log endpoints retain their operation IDs/types.

**Checks:**

```powershell
go generate ./api/openapi
$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go build ./cmd/admin ./cmd/agent
```

## Task 3: Remove the frontend log surface and regenerate the browser client

**Files:**

- Delete: `cmd/web/src/components/monitoring/tabs/LogsTab.tsx`
- Delete: `cmd/web/src/components/monitoring/tabs/LogsTab.css`
- Delete: `cmd/web/src/components/monitoring/tabs/logLanguage.ts`
- Delete: `cmd/web/src/components/monitoring/tabs/logViewModel.ts`
- Delete: `cmd/web/src/components/monitoring/tabs/logViewModel.test.ts`
- Delete: `cmd/web/src/services/logsApi.ts`
- Modify: `cmd/web/src/pages/EditorPage.tsx`
- Modify: `cmd/web/src/components/runtime/RuntimeBar.tsx`
- Modify: `cmd/web/src/services/index.ts`
- Modify: `cmd/web/src/types/api.ts`
- Generate: `cmd/web/src/generated/admin-api.ts`

- [x] Remove lazy import, state, callback, floating window and toolbar button for logs. Do not retain disabled UI or third-party links.
- [x] Remove `logsApi` export and handwritten `LogEntry`, `LogQueryResult`, `LogFileInfo` types.
- [x] Regenerate the typed client from the reduced management OpenAPI rather than manually deleting generated ranges.
- [x] Search global CSS for selectors that only supported Monaco log rendering and remove them only when no editor/notepad consumer remains.

**Checks:**

```powershell
Set-Location 'D:\Gitee\stressbot\cmd\web'; npm.cmd run generate:api; npx.cmd tsc -b
```

## Task 4: Final consistency and performance review

**Files:**

- Modify: `docs/superpowers/plans/2026-08-11-file-logging-implementation.md` (check completed boxes only after evidence)

- [x] Confirm no live-code imports or strings remain for `logview`, `LogsTab`, `logsApi`, `/sbot/logs/` or `/agent/v1/logs`; only negative route regression assertions retain `/sbot/logs/*`.
- [x] Confirm file logs are one JSON object per line with stable field names and UTC timestamps; console output may differ.
- [x] Review that RingBuffer duplicate encoding and retained entry slices are gone, and that the file path has one encoder and one bounded buffer.
- [x] Run `go build ./...`, `npx.cmd tsc -b`, full Go/Vitest suites, production frontend build and local Admin-Agent gRPC runtime validation. External collector validation remains deferred because collectors are intentionally outside this repository.
- [ ] Before commit, show the exact logging-stage file list to the user and wait for confirmation. Stage only those paths; preserve unrelated FlowEditor/usePolling/go.mod edits.
