# gRPC Control Plane Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 使用原生 grpc-go 一次性替换 Admin↔Agent HTTP 控制面，建立可承载 5,000 个在线 Agent 的内网会话、可靠命令、资源包与可降级指标通道。

**Architecture:** Admin 保留浏览器 HTTPS REST 管理面，新增独立 gRPC listener；Agent 只主动拨号并复用一个 `grpc.ClientConn`。控制流、Bundle 服务端流、Telemetry 客户端流彼此隔离；控制命令先写 journal 再投递，Agent 通过稳定 ID、任务状态机和有界 command-ID LRU 幂等执行。所有高频流都采用单 Send owner、单 Recv owner、有界队列和 latest-wins 背压。

**Tech Stack:** Go 1.26、grpc-go 1.82.1、protobuf-go 1.36.x、Buf v2、Goose 3、DDSketch、ZIP/SHA-256。

---

## Scope and non-goals

- 浏览器管理面 `/sbot/*` REST/OpenAPI 保留。
- 删除内部 `api/openapi/control-plane.yaml`、Agent HTTP server/client、Admin HTTP dispatcher 和 pending-task 轮询。
- 不保留 HTTP fallback、双栈、端口兼容字段或旧配置兼容解析。
- 不引入 go-zero、etcd、Kafka、日志平台或旧 M6 业务改造。
- 用户已批准暂缓分布式运行和大规模性能验收；本阶段门禁只包含 proto 生成一致性、Go 编译和直接相关的低成本检查。

## Protocol invariants

- `protocol_version = 1`；不匹配返回 `codes.FailedPrecondition`。
- `agent.id` 是稳定的逻辑配置值，不提供密码学身份认证；控制端口必须只绑定受控私网地址并由防火墙限制 Agent 网段。
- Session 第一帧必须是 `Hello`；未 Hello 的其他帧返回 `codes.FailedPrecondition`。
- 每个流只有一个 Send owner 与一个 Recv owner；业务调用只投递有界队列。
- 同一 Agent 新会话生成递增 `generation` 并取消旧会话；旧 generation 的迟到帧不更新注册表。
- 心跳只更新内存；状态转换、命令 journal 才访问持久层。
- Telemetry 中间采样允许覆盖，最终报告必须走 Session 并收到 `ReportAck`。

## Task 1: Establish the protobuf and reproducible generation contract

**Files:**

- Create: `buf.yaml`
- Create: `buf.gen.yaml`
- Create: `controlplane/controlv1/control.proto`
- Generate: `controlplane/controlv1/control.pb.go`
- Generate: `controlplane/controlv1/control_grpc.pb.go`
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] Add `google.golang.org/grpc v1.82.1` as a direct runtime dependency without replacing the user's existing protobuf 1.36.12 edit.
- [ ] Configure Buf v2 to scan only `controlplane/controlv1`, so the dynamic business protos under `conf/proto` are never treated as generated control APIs.
- [ ] Pin remote generation plugins to `buf.build/protocolbuffers/go:v1.36.11` and `buf.build/grpc/go:v1.6.1`; use `paths=source_relative` and `require_unimplemented_servers=true`.
- [ ] Define three services exactly:

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

- [ ] Define `AgentEvent.oneof` with `hello`, `heartbeat`, `command_ack`, `task_finished`, `final_report`; define `AdminEvent.oneof` with `welcome`, `heartbeat_ack`, `command`, `report_ack`, `server_closing`, `protocol_error`.
- [ ] Define `Command.oneof` with `StartTask`, `StopTask`, `Shutdown`; include `command_id`, monotonically increasing `sequence`, `agent_id`, `task_id`, `created_at_unix_nano`.
- [ ] Define explicit DTOs for task assignment, ramp-up, Redis runtime settings, cleanup status, static/system information and all monitor snapshot fields. `HistogramSnapshot.sketch` is raw DDSketch bytes; nullable metric values use `optional` scalar fields. Do not add JSON blobs, `Any`, `Struct`, or map-based generic payloads.
- [ ] Define Bundle descriptor/request/chunk fields: SHA-256 bytes, total size, offset and chunk bytes. Set the application chunk constant outside generated code to `256 << 10`.
- [ ] Generate and format Go stubs; do not hand-edit generated files. The generated Go files are committed with the protocol source; choose and document a repository-wide generation toolchain before regenerating them.

## Task 2: Add lossless protobuf/domain conversion boundaries

**Files:**

- Create: `controlplane/convert_monitor.go`
- Create: `controlplane/convert_monitor_test.go`
- Create: `agent/grpc_convert.go`
- Create: `admin/grpc_convert.go`
- Modify: `agent/types.go`
- Modify: `admin/types.go`

- [ ] Keep `admin` and `agent` domain structs independent of generated transport structs. Shared monitor conversion lives in `controlplane` and imports only `monitor`; task/system conversion lives in each consumer package, avoiding `agent → controlplane → agent` or `admin → controlplane → admin` cycles.
- [ ] Implement explicit field-by-field conversions for task assignment, host system snapshots, `monitor.CollectorSnapshot`, action snapshots, summaries, windows, histograms, errors and cleanup status.
- [ ] Preserve nanosecond sums, DDSketch bytes, collection epoch, report sequence and optional pointer presence exactly. Clone byte slices crossing ownership boundaries.
- [ ] Reject invalid enum values, negative sizes/offsets, malformed SHA-256 lengths and missing required task IDs with typed errors before domain mutation.
- [ ] Remove `ConfigURL`, `ConfigFiles`, Agent `Address` and HTTP endpoint DTO fields from the domain contracts; add `BundleDigest` and `BundleSize` to Agent-side task assignment.
- [ ] Add focused round-trip tests for: empty histogram optionals, non-empty DDSketch payload, report window, ramp-up/reset, cleanup issue and nil optional system metrics.

**Checks:**

```powershell
$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test ./controlplane -run 'Test.*RoundTrip' -count=1
```

## Task 3: Build deterministic content-addressed task bundles

**Files:**

- Create: `admin/bundle_store.go`
- Create: `admin/bundle_store_test.go`
- Create: `agent/bundle_cache.go`
- Create: `agent/bundle_cache_test.go`
- Modify: `agent/task_runner.go`

- [ ] Implement `admin.BundleStore` rooted at `data/bundles`; build archives through a temporary file and atomically rename to lowercase SHA-256 filename.
- [ ] Sort every resource path, normalize separators to `/`, use fixed ZIP metadata and reject duplicate, absolute, empty or traversal paths. Archive exactly `flow/flow.json`, `proto/*`, `scripts/*`, `adapter/*`.
- [ ] Return an immutable `BundleDescriptor{Digest, Size}` and deduplicate an already-existing digest without rewriting it.
- [ ] Limit simultaneous file streams with a fixed semaphore; `DownloadBundle` opens by digest, validates offset and streams 256 KiB chunks from the requested position. Reuse a bounded buffer pool only inside the stream owner.
- [ ] Implement Agent cache under the task work root. Resume from `.part` size, verify every returned offset, fsync/close, hash the complete file, then atomically rename.
- [ ] Extract into a temporary directory and atomically publish it. Resolve each target with `filepath.Rel`; reject paths outside the extraction root, symlinks and non-regular entries.
- [ ] Change `TaskRunner` to consume an already verified bundle directory instead of issuing individual HTTP GET requests. Bundle download runs under the task context so Stop cancels it promptly.

**Checks:**

```powershell
$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test ./admin ./agent -run 'Test.*Bundle' -count=1
```

## Task 4: Add the command journal and storage abstraction

**Files:**

- Create: `admin/migrations/00004_agent_commands.sql`
- Modify: `admin/schema_postcheck.go`
- Create: `admin/command_store.go`
- Create: `admin/command_store_mysql.go`
- Create: `admin/command_store_memory.go`
- Create: `admin/command_store_test.go`

- [ ] Add `agent_commands` with logical references only: `command_id` PK, `sequence` BIGINT AUTO_INCREMENT/unique order, `agent_id`, nullable `task_id`, `kind`, protobuf `payload` MEDIUMBLOB, `state`, timestamps and rejection reason. Add `(agent_id,state,sequence)` and `(task_id,state)` indexes; do not add foreign keys.
- [ ] Extend schema post-checks to require the command table columns and replay index.
- [ ] Define a small consumer-owned interface:

```go
type CommandStore interface {
    CreateBatch(context.Context, []*controlv1.Command) error
    Get(context.Context, string) (*controlv1.Command, error)
    Pending(context.Context, string, uint64) ([]*controlv1.Command, error)
    Acknowledge(context.Context, string, controlv1.CommandAckStatus, string) error
    Close() error
}
```

- [ ] MySQL `CreateBatch` uses one transaction and one prepared insert for task fan-out. `sequence` is a globally ordered AUTO_INCREMENT unique column; each insert reads `LastInsertId`, writes it back to the immutable-before-publication command, and persisted payload is reconstructed with the column sequence on read.
- [ ] `Pending` is ordered by sequence and capped per replay batch; the caller loops rather than loading unbounded history.
- [ ] `Acknowledge` is idempotent and never moves an acknowledged command back to pending.
- [ ] When MySQL is absent, use a bounded in-memory implementation and emit one startup warning: transient development remains usable, but Admin restart durability is unavailable. Never log a false durability claim.

**Checks:**

```powershell
$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test ./admin -run 'Test(CommandStore|Schema)' -count=1
```

## Task 5: Implement Admin gRPC sessions and dispatch

**Files:**

- Create: `admin/session_registry.go`
- Create: `admin/grpc_server.go`
- Create: `admin/grpc_control_service.go`
- Create: `admin/grpc_bundle_service.go`
- Create: `admin/grpc_telemetry_service.go`
- Create: `admin/command_bus.go`
- Modify: `admin/admin.go`
- Modify: `admin/agent.go`
- Modify: `admin/management_server.go`
- Modify: `admin/config.go`
- Delete: `controlplane/tls.go`

- [ ] Build a plaintext internal `grpc.Server` with max control receive/send sizes, connection age/idle limits suitable for long sessions and keepalive enforcement no shorter than one minute. Do not expose this listener through the public management entry.
- [ ] Validate that Hello carries a non-empty logical Agent ID and reject malformed input with `InvalidArgument`; network admission is owned by the private listener and firewall rather than application certificates.
- [ ] `SessionRegistry.Attach` increments generation, cancels the old session, and stores a bounded command-ID queue plus a single-slot latest heartbeat ACK. Never hold the registry lock while calling stream Send or SQL.
- [ ] The Session handler consumes Hello synchronously, registers `AgentNode`, starts exactly one send-owner through the project work pool, then owns Recv until disconnect. Cancellation/EOF are normal; authentication/protocol errors are explicit gRPC statuses.
- [ ] Heartbeat validates generation, updates `AgentRegistry` in memory and overwrites the pending heartbeat ACK with a renewed lease deadline.
- [ ] `CommandBus.CreateBatch` persists commands first, caches the newly persisted immutable commands in a bounded recent-command cache, then offers only command IDs to online sessions. Send owners resolve from that cache and fall back to `CommandStore.Get`; queue-full/offline leaves commands pending and reconnect calls paged replay.
- [ ] ACK updates journal and invokes domain transitions. Rejected start synthesizes a failed node report; duplicate/applied are successful delivery outcomes.
- [ ] `ReportAck` is emitted only after final/stage report has been accepted by the existing task/report state logic.
- [ ] Telemetry Recv validates session generation, Agent ID, task ID and monotonic sequence. It converts and submits lightweight registry updates; it does not execute SQL or history archive inside Recv.
- [ ] Bundle service delegates only immutable file reads to `BundleStore` and respects cancellation between chunks.
- [ ] Replace `serveHTTPServers` with a management HTTP listener plus gRPC listener. Graceful shutdown order: stop accepting sessions, send `ServerClosing`, `GracefulStop` with bounded fallback to `Stop`, then shutdown management HTTP and shared resources.

**Checks:**

```powershell
$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test ./admin -run 'Test(Session|Command|Bundle|Telemetry)' -count=1
```

## Task 6: Implement the Agent connection supervisor, lease and command idempotence

**Files:**

- Create: `agent/grpc_client.go`
- Create: `agent/session.go`
- Create: `agent/telemetry.go`
- Create: `agent/command_executor.go`
- Create: `agent/recent_commands.go`
- Modify: `agent/agent.go`
- Modify: `agent/reporter.go`
- Modify: `agent/config.go`
- Modify: `cmd/agent/main.go`

- [ ] Replace URL/port/public-address settings with required `agent.id` and `agent.adminAddress` (`host:port`). Retain heartbeat, metrics and reconnect timing fields only where they still affect application behavior.
- [ ] Dial once per connection generation with `grpc.NewClient` and plaintext transport credentials; reuse the resulting `ClientConn` for Session, Bundle and Telemetry clients. Configure max message sizes and keepalive no shorter than one minute.
- [ ] A connection supervisor uses the existing full-jitter backoff helper (5s initial, 30s max). `Unauthenticated`, `PermissionDenied` and `FailedPrecondition` stop rapid retry; `Unavailable` reconnects.
- [ ] Start one Session Send owner, one Session Recv owner and one Telemetry Send owner. No per-heartbeat, per-command or per-report goroutines.
- [ ] Maintain one bounded sequential command executor. Start reserves the existing task state atomically and launches `TaskRunner`; Stop cancels matching task; Shutdown is idempotent. ACK is generated after semantic acceptance/rejection.
- [ ] Maintain a fixed-capacity command-ID LRU (for example 4,096 IDs) and the highest acknowledged sequence. Repeat IDs return `duplicate`; do not add a local database.
- [ ] `HeartbeatAck` stores the lease deadline atomically. A single lease timer cancels a busy task if the deadline expires; idle disconnection does not terminate the process.
- [ ] Reliable final/stage reports use a bounded outbox keyed by report ID. Session Send transmits them, Recv removes them on `ReportAck`; final task completion waits only up to `TaskReportTimeout`.
- [ ] Remove heartbeat HTTP request, re-registration on 404 and 30-second task polling. Reconnect Hello carries current task/status and last acknowledged command sequence.

**Checks:**

```powershell
$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test ./agent -run 'Test(Session|Lease|RecentCommand|Telemetry)' -count=1
```

## Task 7: Make telemetry latest-wins and non-blocking

**Files:**

- Modify: `agent/reporter.go`
- Modify: `agent/telemetry.go`
- Modify: `admin/grpc_telemetry_service.go`
- Modify: `admin/agent.go`
- Modify: `admin/metrics_window_store.go`

- [ ] Replace network-posting reporter interfaces with an `OfferStress`/`OfferSystem` sink. Snapshot creation remains at configured intervals; offering is O(1) and never waits on network I/O.
- [ ] Telemetry maintains exactly one pending stress slot and one pending system slot. Replacing a slot increments its `dropped_intervals`; a capacity-one notify channel wakes the owner without accumulating notifications.
- [ ] Preserve stress report sequence/window metadata when converting. The Admin rejects duplicate/out-of-order windows without refreshing metric freshness.
- [ ] On stream failure, retain only the latest unsent slots for the next stream. Final snapshots are excluded from this lossy path.
- [ ] Do not log every dropped interval. Expose cumulative drop count in Telemetry envelopes and log only stream state transitions or periodic summaries at Debug.

**Checks:**

```powershell
$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test ./agent ./admin -run 'Test.*Telemetry' -count=1
```

## Task 8: Integrate task/agent management and delete internal HTTP control

**Files:**

- Modify: `admin/handlers.go`
- Modify: `admin/types.go`
- Modify: `admin/server_routes_test.go`
- Modify: `conf/admin-config.json`
- Modify: `conf/agent-config.json`
- Delete: `agent/http_client.go`
- Delete: `agent/http_server.go`
- Delete: `agent/control_plane_api.go`
- Delete: `agent/http_server_test.go`
- Delete: `agent/config_url_test.go`
- Delete: `admin/agent_dispatcher.go`
- Delete: `admin/agent_dispatcher_test.go`
- Delete: `admin/control_plane_api.go`
- Delete: `admin/control_plane_server.go`
- Delete: `admin/control_plane_contract_test.go`
- Delete: `admin/api/controlplane.gen.go`
- Delete: `api/openapi/control-plane.yaml`
- Delete: `api/openapi/oapi-codegen-control-plane.yaml`
- Delete: `controlplane/openapi.go`
- Modify: `api/openapi/generate.go`

- [ ] `startTaskBackground` builds one deterministic Bundle, creates all Start commands in one batch, then marks commands as scheduled. It no longer constructs public config URLs or performs one HTTP POST per node.
- [ ] Stop/shutdown handlers create command batches and return accepted after journal+queue admission; they do not wait serially on node network timeouts.
- [ ] Agent list no longer exposes HTTP `address`; expose stable Agent ID/name/version/session status and heartbeat timestamps.
- [ ] Remove Admin Agent log proxy client initialization here only if no other non-log HTTP use remains; the logging phase removes the log endpoints themselves.
- [ ] Delete all `/sbot/agent/*`, `/agent/v1/*` and `/healthz` internal routing/codegen paths. Keep pprof/monitor HTTP endpoints because they are local diagnostics, not control APIs.
- [ ] Update sample config keys and tests to the certificate-free internal gRPC shape. The local sample binds loopback; production must explicitly select a private interface.
- [ ] Update generated OpenAPI entrypoint so it generates only the browser management contract.

## Task 9: Minimum verification and phase handoff

**Files:**

- Modify: `docs/superpowers/plans/2026-08-11-grpc-control-plane-implementation.md` (check completed boxes only after evidence)

- [ ] Format changed Go code with `gofmt`.
- [ ] Search separately for stale internal paths and types; every query must return zero and is reported as an expected zero-match check:

```powershell
Select-String -Path (Get-ChildItem agent,admin,api,controlplane -Recurse -File).FullName -Pattern '/agent/v1/','/sbot/agent/','AgentDispatcher','AdminClient'
```

- [ ] Run the minimum compile gate with repository-local cache:

```powershell
$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go build ./...
```

- [ ] Run only direct, low-cost tests that remain practical locally. Record any intentionally deferred distributed/runtime/performance verification as not executed, never as passed.
- [ ] Review the complete diff for locks held across I/O, unbounded channels/maps, per-message goroutines, mutable protobuf byte ownership and unrelated user edits.
- [ ] Before commit, show the exact gRPC-stage file list to the user and wait for confirmation. Stage only those paths; do not use `git add -A`.
