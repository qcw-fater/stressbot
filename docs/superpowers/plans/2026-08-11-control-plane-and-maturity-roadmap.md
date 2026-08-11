# Control Plane and Mature Infrastructure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 按“控制面 HTTPS/认证与 Lua Worker → Goose 迁移 → RC4/backoff/Zundo → OpenAPI/JSON Schema → TanStack Query → 基准驱动的 CEL、事件化 listen、sqlc”顺序，把当前手写基础设施替换为可验证、可灰度、可回滚的成熟方案。

**Architecture:** 整体拆成六个可独立发布的子项目，每个子项目都以兼容性测试开场、以生产门禁收尾，不把数据库、协议、安全和前端状态迁移塞进同一发布。内部 Admin↔Agent 控制面使用应用内 mTLS；面向浏览器的管理端只监听回环/受控私网，由 Caddy/Nginx/企业网关完成 HTTPS 与 OIDC 认证，进程托管可用 Supervisor、systemd 或容器编排。数据库迁移使用嵌入式 Goose Provider、MySQL advisory lock、失败即停止启动和前向修复；最后三个项目只有在差分测试与基准达到阈值时才进入替换阶段。

**Tech Stack:** Go 1.26、`net/http`、`crypto/tls`、`crypto/x509`、`crypto/rc4`、`github.com/pressly/goose/v3`、`github.com/cenkalti/backoff/v4`、React 18、Zustand 5、Zundo 2、OpenAPI 3.0.3、`oapi-codegen`、`openapi-typescript`、Ajv 2020、`github.com/santhosh-tekuri/jsonschema/v6`、TanStack Query 5、CEL-Go、sqlc。

---

## 1. 已锁定的设计决策

1. **内部控制面与浏览器管理面分开。** Admin 增加独立的内部控制面监听端口；Agent 上行和 Admin 下行 RPC 都走该端口。管理 API 和静态前端不在 mTLS 端口暴露，内部控制面也不暴露管理 API。
2. **内部控制面使用双向 TLS。** Admin 证书由 Admin CA 签发，Agent 只信任 Admin CA；Agent 证书由 Agent CA 签发，Admin 只信任 Agent CA。这样普通 Agent 证书不能调用其他 Agent 的管理端点。
3. **浏览器认证不在 Go 进程内重写 OIDC。** 管理监听地址显式绑定到 `127.0.0.1` 或受控私网，由现有反向代理、oauth2-proxy、Authelia 或企业网关负责 HTTPS/OIDC；Supervisor 完全可以托管 Go 进程，计划不依赖 systemd。
4. **数据库自动迁移默认 fail-stop。** MySQL 配置存在时，Admin 在开放 HTTP 端口前获取 advisory lock、执行 Goose `up`、做 schema post-check；任一步失败都关闭数据库并以非零状态退出。
5. **生产不自动执行 `down`。** MySQL DDL 可能隐式提交，回退策略是“数据库快照/备份 + 回滚应用二进制 + 前向修复 migration”，不是启动时自动逆向 DDL。
6. **OpenAPI 使用 3.0.3。** 当前稳定版 `oapi-codegen` 的主路径仍以 OpenAPI 3.0 为准；JSON Schema 单独使用 2020-12，避免为了 3.1 采用实验生成器。
7. **JSON Schema 只负责结构契约。** `refsCheck.ts` 的节点引用、proto 字段、codec pipeline 交叉引用等领域语义继续保留；Schema 不重复实现这些图和协议语义。
8. **TanStack Query 只接管服务器状态。** `runtimeStore` 继续拥有运行模式、断线状态、有限长度趋势序列和终态报告；Query 不接管画布业务状态、IndexedDB 资源或日志 cursor 流。
9. **CEL、事件化 listen、sqlc 都有“不采用”出口。** 基准、差分语义或维护收益未达到本计划阈值时，保留现有实现或先采用较小的编译 AST 改造，不为“用了成熟库”而强行迁移。

## 2. 里程碑、工期与发布边界

| 里程碑 | 建议 PR 数 | 预计工程日 | 发布门禁 |
|---|---:|---:|---|
| M0 基线与发布护栏 | 1 | 1–2 | 基线测试、配置快照、回滚演练记录齐全 |
| M1 Lua Worker + 控制面安全 | 4 | 8–12 | 反序响应正确；HTTP/HTTPS scheme 不丢；mTLS 正反例通过；管理端无法绕过认证代理 |
| M2 Goose 自动迁移 | 3 | 6–9 | 新库、旧库、失败恢复、并发启动四类演练通过 |
| M3 RC4/backoff/Zundo | 3 | 3–5 | 兼容向量、取消语义、50 步历史与派生状态一致 |
| M4 OpenAPI/JSON Schema | 4–6 | 12–18 | 生成物无漂移；契约测试通过；领域语义验证无回归 |
| M5 TanStack Query | 3–4 | 7–10 | 无重复轮询；AbortSignal 生效；断线/恢复与终态行为不变 |
| M6 基准驱动项目 | 每项 2 | 每项 3–6 | 各自达到量化阈值才合并替换 PR |

按单人串行计算约 8–12 周。Lua Worker 可作为 M1 的首个独立 PR 当天落地；M6 的三个项目互不绑定，不应合成一个 PR。

## 3. 文件结构总览

### 新建文件

- `controlplane/tls.go`：Admin/Agent 共用的证书、根证书池和 `tls.Config` 构造器。
- `controlplane/tls_test.go`：临时 CA/证书生成与 mTLS 正反例。
- `admin/control_plane_server.go`：只注册 Agent 上行路由的内部监听器。
- `admin/management_server.go`：管理 API 与静态资源监听器。
- `admin/control_plane_config.go`：内部端口、URL、TLS 配置解析和校验。
- `admin/migrations/embed.go`：嵌入 Goose SQL 文件并注册 Go migrations。
- `admin/migrations/00001_current_schema.sql`：全新数据库的当前 schema 基线。
- `admin/migrations/00002_reconcile_history.go`：旧历史表的可重入列/索引修复。
- `admin/migrations/00003_reconcile_templates.go`：旧模板表的可重入修复。
- `admin/migrator.go`：锁、Goose Provider、结果日志和 post-check。
- `admin/migration_lock.go`：基于 `GET_LOCK`/`RELEASE_LOCK` 的同连接锁。
- `admin/migrator_integration_test.go`：真实 MySQL 的新库、旧库、失败、并发集成测试。
- `cmd/web/src/components/FlowEditor/lua/luaSyntaxProtocol.ts`：Worker 请求/响应关联 ID 契约。
- `cmd/web/src/components/FlowEditor/lua/luaSyntaxClient.test.ts`：乱序响应和 Worker 崩溃测试。
- `utils/retry.go`、`utils/retry_test.go`：可取消、带 jitter 的统一退避执行器。
- `cmd/web/src/components/FlowEditor/store/flowHistory.ts`、`flowHistory.test.ts`：Zundo 的受控入口。
- `api/openapi/control-plane.yaml`：Admin↔Agent 固定 HTTP 契约。
- `api/openapi/admin.yaml`：浏览器管理 API 契约。
- `api/openapi/oapi-codegen.yaml`：固定 Go 生成配置。
- `api/openapi/generate.go`：固定版本 `go generate` 入口。
- `admin/api/generated.gen.go`：提交到仓库的 Go 生成物。
- `cmd/web/src/generated/admin-api.ts`：提交到仓库的 TypeScript 生成物。
- `schemas/flow.schema.json`、`schemas/codec.schema.json`：JSON Schema 2020-12。
- `schema/validator.go`、`schema/validator_test.go`：后端嵌入并执行结构校验。
- `cmd/web/src/services/schemaValidator.ts`、`schemaValidator.test.ts`：Ajv strict 模式结构校验。
- `cmd/web/src/services/queryClient.ts`、`queryKeys.ts`、`queryOptions.ts`：Query 全局配置与键工厂。
- `cmd/web/src/services/useRuntimeQueries.ts`、`useRuntimeQueries.test.tsx`：运行态轮询适配器。
- `engine/condition_benchmark_test.go`、`engine/condition_compat_test.go`：CEL 决策基准与差分语料。
- `network/listen_wait_benchmark_test.go`：轮询与事件通知基准。
- `sqlc.yaml`、`admin/sql/queries/*.sql`、`admin/dbgen/*.go`：sqlc 配置、固定查询与生成物。
- `deploy/supervisor/stressbot-admin.conf.example`、`deploy/supervisor/stressbot-agent.conf.example`：有限启动重试示例。
- `deploy/reverse-proxy/Caddyfile.example`：管理端 HTTPS 与认证代理边界示例。

### 主要修改文件

- `admin/config.go`、`admin/admin.go`、`admin/handlers.go`、`admin/agent_dispatcher.go`
- `agent/config.go`、`agent/http_client.go`、`agent/http_server.go`
- `cmd/admin/main.go`、`conf/admin-config.json`、`conf/agent-config.json`、`conf/config.json`
- `admin/mysql_schema.go`、`admin/history.go`、`admin/history_schema_test.go`
- `codec/ciphers.go`、`codec/registry_test.go`
- `admin/sampler.go`、`agent/backoff.go`
- `cmd/web/package.json`、`cmd/web/src/main.tsx`
- `cmd/web/src/components/FlowEditor/store/flowStore.ts`、`undoRedo.ts`
- `cmd/web/src/components/FlowEditor/index.tsx`、`panels/Toolbar.tsx`
- `cmd/web/src/services/api.ts`、`metricsApi.ts`、`resourcesStore.ts`、`usePolling.ts`
- `cmd/web/src/pages/EditorPage.tsx`、`cmd/web/src/components/modules/AgentsPanel.tsx`
- `engine/cond_parser.go`、`engine/action.go`、`network/listen_queue.go`
- `admin/history.go`、`admin/flow_template.go`、`admin/action_template.go`、`admin/listen_template.go`

---

### Task 0: 建立统一基线与停止条件

**Files:**
- Create: `docs/modernization/baseline-2026-08.md`
- Modify: `docs/superpowers/plans/2026-08-11-control-plane-and-maturity-roadmap.md`
- Test: repository-wide existing suites

- [x] **Step 1: 记录当前基线**

在 `docs/modernization/baseline-2026-08.md` 固定以下数据：`go test ./...` 总耗时、前端测试数量、Admin 空闲 CPU/RSS、10k Robot 下条件表达式 CPU/alloc、listen 空闲轮询 wakeup 数、前端 60 秒请求次数、MySQL 当前表/列/索引快照。敏感 DSN、证书和令牌不写入文档。

- [x] **Step 2: 运行基线验证**

```powershell
$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'
go build ./...
go test ./...
Set-Location cmd\web
npx tsc -b
npm run test
```

Expected: 四条命令全部 exit 0；把测试数量和耗时写入基线文档。

- [x] **Step 3: 定义统一回滚原则**

在基线文档写明：应用改造使用“上一二进制 + 上一配置”回滚；数据库 migration 不自动 down；证书切换保留上一组证书一个发布窗口；OpenAPI、Query、CEL 切换在删除旧路径前至少完成一个预发布窗口。

- [x] **Step 4: 提交基线**

```powershell
git add docs/modernization/baseline-2026-08.md docs/superpowers/plans/2026-08-11-control-plane-and-maturity-roadmap.md
git commit -m "docs: establish modernization rollout baseline"
```

---

### Task 1: 修复 Lua Worker 并发请求关联

**Files:**
- Create: `cmd/web/src/components/FlowEditor/lua/luaSyntaxProtocol.ts`
- Create: `cmd/web/src/components/FlowEditor/lua/luaSyntaxClient.test.ts`
- Modify: `cmd/web/src/components/FlowEditor/lua/luaSyntaxClient.ts`
- Modify: `cmd/web/src/components/FlowEditor/lua/luaSyntaxWorker.ts`

- [x] **Step 1: 写乱序响应失败测试**

测试注入 fake Worker，连续发出两次 `check`，按第二次、第一次顺序返回，断言两个 Promise 各自拿到自己的 issues；再触发 `onerror`，断言所有 pending 都完成且 Map 清空。

```ts
it('correlates out-of-order worker responses', async () => {
  const worker = new FakeWorker();
  const client = createLuaSyntaxClient(() => worker);
  const first = client.check('first', 'action');
  const second = client.check('second', 'listen');
  worker.reply(2, issue('second'));
  worker.reply(1, issue('first'));
  await expect(first).resolves.toEqual([issue('first')]);
  await expect(second).resolves.toEqual([issue('second')]);
});
```

- [x] **Step 2: 运行测试确认当前实现失败**

```powershell
Set-Location cmd\web
npx vitest run src/components/FlowEditor/lua/luaSyntaxClient.test.ts
```

Expected: FAIL，表现为第一个 Promise 不完成或拿到第二个响应。

- [x] **Step 3: 引入 requestId 协议和 pending Map**

```ts
export interface ParseRequest {
  type: 'parse';
  requestId: number;
  code: string;
  mode: LuaCheckMode;
}

export interface ParseResponse {
  type: 'result';
  requestId: number;
  issues: SyntaxIssue[];
}
```

客户端使用递增 `requestId` 和 `Map<number, Resolve>`；Worker 原样回传 `requestId`。Worker 崩溃时完成并清空全部 pending，保持当前“语法检查不可用不阻塞编辑”的行为。

- [x] **Step 4: 运行 Lua 与全前端测试**

```powershell
npx vitest run src/components/FlowEditor/lua/luaSyntaxClient.test.ts src/components/FlowEditor/lua/__tests__/luaApiSpec.test.ts
npm run test
npx tsc -b
```

Expected: 全部 PASS。

- [x] **Step 5: 提交独立修复**

```powershell
git add cmd/web/src/components/FlowEditor/lua
git commit -m "fix: correlate concurrent Lua worker checks"
```

---

### Task 2: 保留控制面 URL scheme 并收紧 HTTP 客户端

**Files:**
- Create: `admin/agent_dispatcher_test.go`
- Modify: `admin/agent_dispatcher.go:18-21,42-49,78-149`
- Modify: `agent/http_client.go`
- Modify: `agent/config.go`

- [x] **Step 1: 写 URL 兼容测试**

覆盖 `http://agent:7719`、`https://agent.example:7719/base`、无 scheme、非法 scheme 四种输入。明确目标：配置必须带 `http` 或 `https`；路径使用 `url.JoinPath`，不得字符串删 scheme 后再强制拼 `http://`。

```go
func TestAgentEndpointPreservesHTTPS(t *testing.T) {
    got, err := agentEndpoint("https://agent.example:7719/base", "/agent/v1/version")
    if err != nil { t.Fatal(err) }
    if got != "https://agent.example:7719/base/agent/v1/version" { t.Fatalf("got %q", got) }
}
```

- [x] **Step 2: 运行测试确认当前实现失败**

```powershell
$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'
go test ./admin -run 'TestAgentEndpoint' -v
```

Expected: HTTPS 用例 FAIL，证明当前 `normalizeAddr` 丢失 scheme。

- [x] **Step 3: 使用 `net/url` 构造端点**

```go
func agentEndpoint(baseURL, path string) (string, error) {
    u, err := url.Parse(baseURL)
    if err != nil { return "", fmt.Errorf("解析节点地址: %w", err) }
    if u.Scheme != "http" && u.Scheme != "https" {
        return "", fmt.Errorf("节点地址 scheme 必须是 http 或 https")
    }
    if u.Host == "" { return "", fmt.Errorf("节点地址缺少 host") }
    return u.JoinPath(path).String(), nil
}
```

Agent 的 `AdminUrl` 和 `PublicURL` 在 `Resolve()` 阶段做相同校验；错误信息使用中文。

- [x] **Step 4: 为两端 `http.Server` 增加边界参数**

Agent server 增加 `ReadHeaderTimeout: 10s`、`IdleTimeout: 120s`、`MaxHeaderBytes: 1<<20`。Admin/Agent 客户端显式设置 `Transport` 的 `TLSHandshakeTimeout`、`ResponseHeaderTimeout`、`MaxIdleConnsPerHost`，请求继续使用已有总超时。

- [x] **Step 5: 验证并提交**

```powershell
go test ./admin ./agent -run 'Endpoint|HTTP|Dispatcher' -v
go build ./...
git add admin/agent_dispatcher.go admin/agent_dispatcher_test.go agent/config.go agent/http_client.go agent/http_server.go
git commit -m "fix: preserve control plane URL schemes"
```

---

### Task 3: 拆分 Admin 管理面与内部控制面

**Files:**
- Create: `admin/control_plane_server.go`
- Create: `admin/management_server.go`
- Create: `admin/server_routes_test.go`
- Modify: `admin/handlers.go:25-135`
- Modify: `admin/admin.go:28-54,143-218`
- Modify: `admin/config.go`

- [x] **Step 1: 写路由隔离测试**

分别构造 management handler 和 control-plane handler：前者访问 `/sbot/agent/register` 必须 404，后者访问 `/sbot/tasks` 和 `/` 必须 404；对应合法端点不能返回 404。

- [x] **Step 2: 拆分路由注册函数**

```go
func (s *AdminServer) registerControlPlaneRoutes() http.Handler
func (s *AdminServer) registerManagementRoutes() http.Handler
```

`registerControlPlaneRoutes` 只包含 7 个 `/sbot/agent/...` 上行端点；`registerManagementRoutes` 包含任务、节点、指标、历史、日志、资源、模板、codec、能力和静态文件。两者都包 `recoverMiddleware`。

- [x] **Step 3: 增加明确配置**

```go
type ControlPlaneConfig struct {
    Port      int                    `json:"port"`
    PublicURL string                 `json:"publicUrl"`
    TLS       controlplane.TLSConfig `json:"tls"`
}

type Config struct {
    Port              int                `json:"port"`
    ListenHost        string             `json:"listenHost"`
    PublicURL         string             `json:"publicUrl"`
    ControlPlane      ControlPlaneConfig `json:"controlPlane"`
    // existing fields unchanged
}
```

管理面 `ListenHost` 的生产配置写 `127.0.0.1`；内部控制面默认端口 7720，`PublicURL` 必须是 `https://...`。

- [x] **Step 4: 启动两个 server，统一关闭**

`AdminServer` 保存 `managementSrv` 与 `controlPlaneSrv`。`Run()` 先完成数据库和依赖初始化，再并行监听；任一非 `ErrServerClosed` 错误都触发统一 Shutdown。所有后台启动走 `utils.GetWorkPool().Go()`。

- [x] **Step 5: 运行隔离与关闭测试**

```powershell
go test ./admin -run 'TestManagementRoutes|TestControlPlaneRoutes|TestShutdown' -v
go build ./...
```

Expected: 路由隔离、重复 Shutdown 和信号关闭全部 PASS。

- [x] **Step 6: 提交监听器拆分**

```powershell
git add admin/admin.go admin/config.go admin/handlers.go admin/control_plane_server.go admin/management_server.go admin/server_routes_test.go
git commit -m "refactor: isolate admin control plane listener"
```

---

### Task 4: 为 Admin↔Agent 启用 mTLS，并交付代理/进程托管示例

**Files:**
- Create: `controlplane/tls.go`
- Create: `controlplane/tls_test.go`
- Create: `admin/control_plane_tls_test.go`
- Create: `agent/control_plane_tls_test.go`
- Create: `deploy/reverse-proxy/Caddyfile.example`
- Create: `deploy/supervisor/stressbot-admin.conf.example`
- Create: `deploy/supervisor/stressbot-agent.conf.example`
- Modify: `admin/control_plane_server.go`
- Modify: `admin/agent_dispatcher.go`
- Modify: `agent/config.go`
- Modify: `agent/http_client.go`
- Modify: `agent/http_server.go`
- Modify: `conf/admin-config.json`
- Modify: `conf/agent-config.json`
- Modify: `conf/config.json`

- [x] **Step 1: 写 TLS 构造器失败测试**

覆盖缺 cert、缺 key、错误 CA、过低 TLS 版本、Admin CA 证书连接 Agent、Agent CA 证书连接 Admin。生成临时 CA/证书时给 server 证书写入实际 DNS/IP SAN，不依赖 Common Name。

- [x] **Step 2: 实现共用 TLS 配置**

```go
type TLSConfig struct {
    CertFile   string `json:"certFile"`
    KeyFile    string `json:"keyFile"`
    PeerCAFile string `json:"peerCaFile"`
}

func (c TLSConfig) Server() (*tls.Config, error)
func (c TLSConfig) Client() (*tls.Config, error)
```

两者固定 `MinVersion: tls.VersionTLS13`；Server 使用 `RequireAndVerifyClientCert`，Client 设置自己的证书和 `RootCAs`，不提供 `InsecureSkipVerify` 配置。

- [x] **Step 3: 接入两端 server/client**

Admin 内部 listener 使用 `tls.NewListener`；Admin dispatcher 和日志代理使用同一个带 mTLS 的 `http.Transport`；Agent server 使用 mTLS；Agent 注册、心跳、指标、任务完成上报使用带 mTLS 的 client。管理 listener 仍只绑定回环地址，由反向代理终止公网 TLS。

- [x] **Step 4: 添加 mTLS 集成测试**

使用 `httptest.NewUnstartedServer` 验证：无证书、错误 CA、过期证书连接失败；合法 Admin/Agent 双向调用成功；HTTPS URL 在 dispatcher 中保持 HTTPS。

- [x] **Step 5: 写 Caddy 与 Supervisor 示例**

`Caddyfile.example` 只把认证后的流量代理到 `127.0.0.1:7718`，`/sbot/agent/*` 不转发。Supervisor 示例使用 `autorestart=unexpected`、`startretries=3`、`stopsignal=TERM`、`stopasgroup=true`、`killasgroup=true`，证明进程托管不绑定 systemd。

- [ ] **Step 6: 分两次发布切换**

发布 A：二进制支持新配置，旧 HTTP 配置仍可运行但每次启动输出一次明确警告。给全部 Agent 下发证书和 HTTPS URL。发布 B：生产配置切到 mTLS，管理端只监听回环；验证一周后删除生产 HTTP 配置，不在代码中加入 HTTPS 失败后自动降级 HTTP 的逻辑。

- [x] **Step 7: 验证与提交**

```powershell
go test ./controlplane ./admin ./agent -run 'TLS|ControlPlane|Dispatcher' -v
go build ./...
git add controlplane admin agent conf deploy
git commit -m "feat: secure internal control plane with mutual TLS"
```

M1 出口：浏览器只能从认证代理访问管理面；Admin/Agent 错误证书均被拒；内部 mTLS 端口不存在管理路由；Lua Worker 乱序测试稳定通过。

---

### Task 5: 用 Goose Provider 建立版本化迁移基线

**Files:**
- Create: `admin/migrations/embed.go`
- Create: `admin/migrations/00001_current_schema.sql`
- Create: `admin/migrator.go`
- Create: `admin/migration_lock.go`
- Create: `admin/migrator_test.go`
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `admin/admin.go:82-110`
- Modify: `admin/history.go:74-107`
- Delete after cutover: `admin/mysql_schema.go`
- Replace: `admin/history_schema_test.go`

- [ ] **Step 1: 固定 Goose 版本并写 provider 构造失败测试**

```powershell
go get github.com/pressly/goose/v3@v3.27.2
```

测试必须验证 embedded FS 缺 migration、重复 version、未知 dialect 时构造失败；Provider 使用实例注册，不使用全局 registry。

- [ ] **Step 2: 把当前 schema 原样搬入基线 migration**

`00001_current_schema.sql` 使用顺序版本号，逐字搬入 `admin/mysql_schema.go:7-206` 当前 11 张表的完整 `CREATE TABLE IF NOT EXISTS`，顺序保持 `allDDL` 当前顺序。文件首行是 `-- +goose Up`，末尾是 `-- +goose Down` 和 `SELECT 1;`；迁移搬运 PR 通过规范化 SQL 测试逐表比较旧常量与新文件，确保列、默认值、索引、字符集零漂移。

生产禁止自动 down，所以基线 Down 明确为空操作；所有真实破坏性回退依赖备份恢复。

- [ ] **Step 3: 嵌入 migration 并构造 Provider**

```go
//go:embed *.sql
var Files embed.FS

provider, err := goose.NewProvider(
    goose.DialectMySQL,
    db,
    migrations.Files,
    goose.WithDisableGlobalRegistry(true),
    goose.WithGoMigrations(migrations.GoMigrations()...),
)
```

Provider 的 `Up(ctx)` 结果逐条写结构化日志：version、source、duration、direction；错误日志不输出 DSN。

- [ ] **Step 4: 实现 MySQL advisory lock**

锁必须持有专用 `*sql.Conn`，获取和释放在同一 session：

```go
SELECT GET_LOCK('stressbot_schema_migration', ?);
SELECT RELEASE_LOCK('stressbot_schema_migration');
```

锁超时默认 30 秒。返回 0 是超时，返回 NULL 是 MySQL 错误；无论 Provider 成功失败都 defer release 和 close。Goose 官方 Provider 默认不替 MySQL 加锁，因此此锁不是可省略项。

- [ ] **Step 5: 在 Admin 开放端口前运行 migration**

`NewAdminServer` 装配顺序改为 `openDB → acquire lock → provider.Up → postCheck → stores`。任一步失败都关闭 DB 并返回错误，`cmd/admin/main.go` 保持 exit 1；不得启动一个“数据库不可用但 HTTP 仍可用”的半健康 Admin。

- [ ] **Step 6: 用 SQL mock 验证失败传播和锁释放**

```powershell
$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'
go test ./admin -run 'TestMigrator|TestMigrationLock' -v
```

Expected: migration 第 N 步失败、post-check 失败、context cancel、锁超时四种路径均释放锁和连接；Admin server 未创建。

- [ ] **Step 7: 提交 Goose 基线**

```powershell
git add go.mod go.sum admin/migrations admin/migrator.go admin/migration_lock.go admin/migrator_test.go admin/admin.go admin/history.go
git commit -m "feat: introduce versioned Goose migrations"
```

---

### Task 6: 兼容旧库并建立迁移失败恢复演练

**Files:**
- Create: `admin/migrations/00002_reconcile_history.go`
- Create: `admin/migrations/00003_reconcile_templates.go`
- Create: `admin/schema_postcheck.go`
- Create: `admin/migrator_integration_test.go`
- Create: `docs/runbooks/mysql-migration.md`
- Modify: `cmd/admin/main.go`
- Modify: `deploy/supervisor/stressbot-admin.conf.example`
- Delete: `admin/mysql_schema.go`
- Rewrite: `admin/history_schema_test.go`

- [ ] **Step 1: 从真实旧 schema 快照写失败测试**

测试库至少覆盖三种起点：空数据库；只有早期 6 张历史表且缺 `active_agent_count/window_from/history_batch_token`；已有模板表但缺二进制唯一索引。每种起点执行两次 `Up`，第二次必须无变更且成功。

- [ ] **Step 2: 编写可重入 Go migrations**

每个 DDL 动作先查 `information_schema.columns/statistics/tables`，只在缺失时执行一个 `ALTER TABLE`。每个 migration 只修一组紧密相关的表；执行错误立即返回。不要把 `ALTER TABLE` 包装成“事务回滚一定有效”的假象。

```go
func addColumnIfMissing(ctx context.Context, db *sql.DB, table, column, ddl string) error {
    exists, err := columnExists(ctx, db, table, column)
    if err != nil || exists { return err }
    _, err = db.ExecContext(ctx, ddl)
    return err
}
```

- [ ] **Step 3: 增加 schema post-check**

post-check 固定验证运行时必需列、主键和唯一索引，不只检查 Goose version。至少验证 `task_timeseries.uq_task_history_batch`、`task_meta` 复合主键、模板名称二进制唯一索引、`task_history.flow_template_id`。

- [ ] **Step 4: 增加运维命令**

`cmd/admin` 增加 `-migration status|up|up-by-one|auto`。`auto` 是正常启动；其余命令只连接数据库、获取锁、输出结果后退出，不启动 HTTP。生产不提供 `down/reset` 快捷入口。

- [ ] **Step 5: 写失败注入集成测试**

使用环境变量 `STRESSBOT_TEST_MYSQL_DSN` 指向专用测试库；测试不得输出该值。通过一条专用测试 migration 在第一条 DDL 后返回 sentinel error，断言：Admin 未监听；Goose 未把该 version 标为完成；已经提交的 DDL 被 post-check 识别；修正版 migration 可再次前向完成。

- [ ] **Step 6: 写恢复 runbook**

`docs/runbooks/mysql-migration.md` 固定以下顺序：停止自动重启 → 保存 Admin 日志和 `-migration status` → 判断是锁、权限、磁盘、DDL 还是 post-check → 修正环境或发布前向 migration → staging 恢复演练 → 再启动。上线前使用 `mysqldump --single-transaction --routines --triggers` 或云数据库快照；涉及大表 ALTER 时先做容量和锁时长评估，改为发布前独立作业。

- [ ] **Step 7: 限制 Supervisor 启动重试**

示例设置 `startretries=3`；迁移失败连续三次后进入 FATAL，避免无限重启反复执行 DDL。人工修复后再 `supervisorctl start stressbot-admin`。

- [ ] **Step 8: 跑四类迁移演练**

```powershell
go test ./admin -run 'TestMigrationIntegration' -v
go run ./cmd/admin -config conf/admin-config.json -migration status
go build ./...
```

Expected: 空库、旧库、失败恢复、两个 Admin 并发启动均通过；并发场景只有一个持锁迁移者。

- [ ] **Step 9: 删除启动期手写 DDL 并提交**

```powershell
git add admin cmd/admin docs/runbooks deploy/supervisor go.mod go.sum
git commit -m "feat: make database migrations fail-safe and recoverable"
```

M2 出口：备份和恢复演练有记录；旧生产快照能升级；失败时 Admin 不监听；修复版本可前向恢复；Supervisor 不无限重试。

---

### Task 7: 用标准库 RC4 替换手写 KSA/PRGA

**Files:**
- Modify: `codec/ciphers.go:88-108,328-356`
- Modify: `codec/registry_test.go:294-313`

- [ ] **Step 1: 写标准向量和错误前不修改测试**

```go
func TestRC4KnownVector(t *testing.T) {
    c, _ := LookupCipher("rc4")
    got, err := c.Encrypt([]byte("Plaintext"), []byte("Key"), 0, nil)
    if err != nil { t.Fatal(err) }
    if hex.EncodeToString(got) != "bbf316e8d940af0ad3" { t.Fatalf("got %x", got) }
}
```

另测 257 字节 key 返回 `rc4.KeySizeError`，`DecryptInPlace` 在返回错误前保持 data 不变；空 key 继续按现有协议语义原样返回。

- [ ] **Step 2: 运行测试确认新增非法 key 用例失败**

```powershell
go test ./codec -run 'TestRC4' -v
```

- [ ] **Step 3: 调用 `crypto/rc4`**

删除 `applyRC4` 的手写 S-box；复制版先复制前缀和 body，再 `rc4.NewCipher(key)`、`XORKeyStream(out[off:], out[off:])`。原地版先构造 Cipher，成功后才修改 data。

- [ ] **Step 4: 全 codec 对拍并提交**

```powershell
go test ./codec -v
go test ./... -run 'Cipher|Codec' -count=1
git add codec/ciphers.go codec/registry_test.go
git commit -m "refactor: use standard library RC4"
```

RC4 仅为兼容既有游戏协议；文档保留“不可用于新安全设计”的警告。

---

### Task 8: 统一 cenkalti/backoff 策略与取消语义

**Files:**
- Create: `utils/retry.go`
- Create: `utils/retry_test.go`
- Modify: `agent/backoff.go`
- Modify: `admin/agent_dispatcher.go`
- Modify: `admin/sampler.go:101-124`

- [ ] **Step 1: 写可取消和 jitter 测试**

测试固定随机源/Clock，验证 1s→2s→4s、上限 30s、stop 立即返回、永久错误不重试、成功后停止。不要用真实 sleep。

- [ ] **Step 2: 提取统一 policy**

```go
type RetryPolicy struct {
    Initial time.Duration
    Max     time.Duration
    Factor  float64
    Jitter  float64
}

func NewExponentialBackOff(p RetryPolicy) *backoff.ExponentialBackOff
func RetryWithStop(stop <-chan struct{}, op func() error, notify func(error, time.Duration), b backoff.BackOff) error
```

`RetryWithStop` 使用项目 `utils.GetTimer/PutTimer` 等待下次尝试；收到 stop 后不得再执行一次 operation。

- [ ] **Step 3: 替换三处策略**

Agent 注册、Admin dispatcher 和 Sampler final flush 统一使用 helper；各自保留最大重试次数、永久 4xx、无限 final flush 的业务差异。删除 `agent.newExponentialBackoff` 和 `admin.newDispatcherBackoff` 的重复实现。

- [ ] **Step 4: 验证无 goroutine/timer 泄漏并提交**

```powershell
go test ./utils ./agent ./admin -run 'Backoff|Retry|FinalFlush|Dispatcher' -count=20
go test -race ./utils ./agent ./admin -run 'Backoff|Retry|FinalFlush|Dispatcher'
git add utils/retry.go utils/retry_test.go agent/backoff.go admin/agent_dispatcher.go admin/sampler.go
git commit -m "refactor: centralize cancellable retry backoff"
```

---

### Task 9: 用 Zundo 接管 FlowEditor 撤销/重做

**Files:**
- Create: `cmd/web/src/components/FlowEditor/store/flowHistory.ts`
- Create: `cmd/web/src/components/FlowEditor/store/flowHistory.test.ts`
- Modify: `cmd/web/src/components/FlowEditor/store/flowStore.ts:24-105`
- Modify: `cmd/web/src/components/FlowEditor/store/undoRedo.ts`
- Modify: `cmd/web/src/components/FlowEditor/index.tsx:124-132`
- Modify: `cmd/web/src/components/FlowEditor/panels/Toolbar.tsx:31`

- [ ] **Step 1: 把当前行为写成兼容测试**

覆盖：只追踪 `defaultDelayMs/nodes/actions/listens`；位置、layout、选中和派生数据不入历史；undo 后 `rfNodes/rfEdges/listenRefCount/nodesByListen` 与业务数据同步；load/reset 清空历史；上限 50；新修改清空 future。

- [ ] **Step 2: 运行测试确认 Zundo 尚未接入**

```powershell
Set-Location cmd\web
npx vitest run src/components/FlowEditor/store/flowHistory.test.ts
```

- [ ] **Step 3: 为 flowStore 包装 `temporal`**

```ts
type TrackedFlowState = Pick<FlowState, 'defaultDelayMs' | 'nodes' | 'actions' | 'listens'>;

const createFlowState: StateCreator<FlowState> = (set, get) => ({
  // 将 flowStore.ts:95 起现有 initializer 整体移动到这里，字段和 action 内容不改。
});

export const useFlowStore = create<FlowState>()(
  temporal(createFlowState, {
    limit: 50,
    partialize: (s): TrackedFlowState => ({
      defaultDelayMs: s.defaultDelayMs,
      nodes: s.nodes,
      actions: s.actions,
      listens: s.listens,
    }),
    equality: (a, b) =>
      a.defaultDelayMs === b.defaultDelayMs && a.nodes === b.nodes &&
      a.actions === b.actions && a.listens === b.listens,
  }),
);
```

- [ ] **Step 4: 用受控 wrapper 保持派生状态和布尔返回值**

`undoFlow()`/`redoFlow()` 先检查 past/future 长度，调用 temporal，再调用 `syncDerived()`，返回是否发生变化。`loadFromTaskFlow` 和 `reset` 在 pause/resume 之间写入，并在完成后 `clear()`，保证打开另一个流程不能撤销回上一个流程。

- [ ] **Step 5: 删除手写全局快照栈**

移除 `startHistory` useEffect；`undoRedo.ts` 删除，Toolbar 和快捷键改用 `flowHistory.ts`。不把 Zundo temporal store 暴露给普通组件。

- [ ] **Step 6: 前端验证并提交**

```powershell
npx vitest run src/components/FlowEditor/store/flowHistory.test.ts src/components/FlowEditor/store/flowStore.test.ts
npm run test
npx tsc -b
git add src/components/FlowEditor
git commit -m "refactor: use Zundo for flow history"
```

M3 出口：标准 RC4 向量通过；所有重试可取消且有 jitter；撤销/重做与现有 UI 语义逐项一致。

---

### Task 10: 先为内部控制面建立 OpenAPI 契约

**Files:**
- Create: `api/openapi/control-plane.yaml`
- Create: `api/openapi/oapi-codegen-control-plane.yaml`
- Create: `api/openapi/generate.go`
- Create: `admin/api/controlplane.gen.go`
- Create: `admin/control_plane_api.go`
- Create: `agent/control_plane_api.go`
- Create: `admin/control_plane_contract_test.go`
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `admin/control_plane_server.go`
- Modify: `agent/http_server.go`
- Modify: `agent/http_client.go`

- [ ] **Step 1: 枚举并冻结 16 个内部端点**

Spec 必须包含 Admin 的 7 个 Agent 上行端点：register、heartbeat、deregister、stress、system、task done、pending task；以及 Agent 的 task、stop、shutdown、version、status、logs、log files、healthz。每个 operation 有唯一 `operationId`、明确 2xx/4xx/5xx 响应、`ApiError`、security scheme `mutualTLS`。

- [ ] **Step 2: 使用 OpenAPI 3.0.3 写首个契约测试**

对当前 handler 发送合法/缺字段/错误 method 请求，用嵌入的 spec request validator 验证请求；再验证响应状态、Content-Type 和 body 能被 spec schema 解码。先让缺字段用例暴露现有宽松行为。

- [ ] **Step 3: 固定生成配置和版本**

```powershell
go get -tool github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.7.0
```

```yaml
package: api
generate:
  models: true
  std-http-server: true
  strict-server: true
  client: true
output: controlplane.gen.go
```

`go generate` 使用固定 module version，不使用 `@latest`；生成文件提交仓库。生成器配置 schema 也固定到同一 tag。

- [ ] **Step 4: 用 strict adapter 包住现有业务方法**

生成层只负责解析/序列化和状态码；`admin/control_plane_api.go`、`agent/control_plane_api.go` 把生成 DTO 映射到现有 `TaskAssignment`、metrics 和 registry 类型。第一轮不把业务结构体整体替换成生成类型，避免一次 PR 穿透 admin/agent/monitor。

- [ ] **Step 5: 加入 request validation 与认证顺序测试**

中间件顺序固定为：recover → request ID/log context → mTLS 已由 listener 完成 → OpenAPI request validation → strict handler。注意 `oapi-codegen` strict server 本身不完成全部入站校验，必须显式接 net/http validation middleware。

- [ ] **Step 6: 验证生成物与契约**

```powershell
go generate ./api/openapi
git diff --exit-code -- admin/api/controlplane.gen.go
go test ./admin ./agent -run 'Contract|ControlPlane' -v
go build ./...
```

Expected: 重新生成无 diff，控制面契约测试全部 PASS。

- [ ] **Step 7: 提交控制面契约**

```powershell
git add api/openapi admin/api admin/control_plane_api.go agent/control_plane_api.go admin agent go.mod go.sum
git commit -m "feat: define generated control plane contract"
```

---

### Task 11: 分组迁移管理 OpenAPI 与 TypeScript 类型

**Files:**
- Create: `api/openapi/admin.yaml`
- Create: `api/openapi/oapi-codegen-admin.yaml`
- Create: `admin/api/admin.gen.go`
- Create: `cmd/web/src/generated/admin-api.ts`
- Create: `cmd/web/src/services/generatedClient.ts`
- Create: `cmd/web/src/services/__tests__/generatedClient.test.ts`
- Modify: `cmd/web/package.json`
- Modify: `cmd/web/src/types/api.ts`
- Modify: `cmd/web/src/services/api.ts`
- Modify: `admin/handlers.go`

- [ ] **Step 1: 按风险从低到高迁移 endpoint group**

固定顺序：`capabilities/error-codes/codec metadata` → `agents/tasks` → `metrics/system` → `history` → `flows/action-templates/listen-templates` → `logs/baseline resources`。每组独立 PR；一组完成后其 Go/TS 手写 DTO 必须删除或明确保留为领域模型，禁止两个 API DTO 来源长期并存。

- [ ] **Step 2: 给每组写黑盒契约测试**

每个 endpoint 至少覆盖一个成功响应、一个业务错误响应、一个非法请求。metrics 必须覆盖 `null` 分位数、`uint64` JSON 边界、完整 `window`；列表接口统一在 spec 中选择当前实际裸数组或 `{items,total}`，迁移时不靠 `adaptList` 猜两种格式。

- [ ] **Step 3: 生成 Go 模型与 TypeScript path 类型**

Go 继续用 `oapi-codegen`；前端使用固定版本 `openapi-typescript` 生成 `admin-api.ts`，`openapi-fetch` 创建 typed client。现有 `ApiError` 继续作为统一异常，生成 client 的非 2xx 响应在 `generatedClient.ts` 映射成它。

```powershell
Set-Location cmd\web
npm install --save-exact openapi-fetch
npm install --save-dev --save-exact openapi-typescript
```

`package.json` 增加精确脚本：

```json
"generate:api": "openapi-typescript ../../api/openapi/admin.yaml -o src/generated/admin-api.ts"
```

- [ ] **Step 4: 迁移 services，不让组件直接使用 generated client**

组件仍只 import `services/*.ts`。每个 service 的函数签名从 generated paths 推导，保留中文领域错误和 `AbortSignal` 参数：

```ts
export function listAgents(signal?: AbortSignal): Promise<AgentListResponse> {
  return generatedGet('/agents', { signal });
}
```

- [ ] **Step 5: 删除不再需要的运行时手写 shape parser**

只有当契约测试和 server 输出都由 spec 约束后，删除 `metricsApi.ts` 中重复的 `requireCount/requireRatio`；外部不可信数据或需要额外数值不变量的地方保留领域校验。不要因为 TypeScript 生成了类型就假设运行时 JSON 自动可信。

- [ ] **Step 6: 每组运行生成、后端、前端验证**

```powershell
go generate ./api/openapi
go test ./admin -run 'Contract' -v
Set-Location cmd\web
npm run generate:api
git diff --exit-code -- src/generated/admin-api.ts
npx tsc -b
npm run test
```

- [ ] **Step 7: 每组单独提交**

提交信息使用 `refactor(api): generate <group> contracts`，不把多个 endpoint group 合并成一次大爆炸切换。

---

### Task 12: 用 JSON Schema 2020-12 统一 flow/codec 结构校验

**Files:**
- Create: `schemas/flow.schema.json`
- Create: `schemas/codec.schema.json`
- Create: `schemas/testdata/valid/*.json`
- Create: `schemas/testdata/invalid/*.json`
- Create: `schema/validator.go`
- Create: `schema/validator_test.go`
- Create: `cmd/web/src/services/schemaValidator.ts`
- Create: `cmd/web/src/services/schemaValidator.test.ts`
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `cmd/web/package.json`
- Modify: `cmd/web/src/services/resourcesStore.ts:342-510`
- Modify: `cmd/web/src/components/FlowEditor/validation/refsCheck.ts`

- [ ] **Step 1: 先写共享 corpus**

有效样本覆盖 9 种 node、14 种 action、17 种 binding、onError、listenRefs、heartbeat、codec pipeline。无效样本逐个只破坏一个结构约束：未知 enum、缺 required、错误类型、非法范围、额外字段。Go 与 Ajv 对每个样本必须给出相同 pass/fail。

- [ ] **Step 2: 写 Schema 元数据和闭合对象策略**

两个文件都声明：

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://stressbot.local/schemas/flow.schema.json"
}
```

对稳定配置对象使用 `unevaluatedProperties: false`；对明确允许任意业务字段的 map 不关闭。数字范围与 Go 当前校验一致，不把尚未支持的 enum 提前写进 Schema。

- [ ] **Step 3: 后端使用嵌入式 jsonschema/v6**

`schema/validator.go` 用 `embed.FS` 编译一次并缓存两个 `*jsonschema.Schema`，显式 `DefaultDraft(jsonschema.Draft2020)`。Admin 保存/下发、Agent 加载和 standalone 加载在领域校验前先做结构校验。

- [ ] **Step 4: 前端使用 Ajv 2020 strict 模式**

```ts
const ajv = new Ajv2020({ allErrors: true, strict: true });
const validateFlowStructure = ajv.compile(flowSchema);
const validateCodecStructure = ajv.compile(codecSchema);
```

错误统一映射成中文路径消息；不要把完整配置内容写入日志。

- [ ] **Step 5: 保留并缩小语义校验**

`refsCheck.ts` 继续负责图引用、action/listen 关联、onError handler、proto 路径、业务不变量；`resourcesStore.ts` 保留 codec flag/step/checksumOut 交叉引用和服务端算法清单校验。删除的只是不再需要的类型、required、enum、范围手写分支。

- [ ] **Step 6: 差分验证与生成检查**

```powershell
go test ./schema ./engine ./codec -run 'Schema|Validate' -v
Set-Location cmd\web
npx vitest run src/services/schemaValidator.test.ts src/components/FlowEditor/validation/refsCheck.test.ts
npx tsc -b
```

Expected: Go/Ajv corpus 结果逐文件一致；现有 flow/codec 全部通过；非法样本至少产生一个带 JSON pointer 的错误。

- [ ] **Step 7: 提交结构契约**

```powershell
git add schemas schema engine codec admin agent cmd/web go.mod go.sum
git commit -m "feat: validate configuration with shared JSON Schema"
```

M4 出口：OpenAPI 生成物在 CI 无漂移；所有固定 HTTP DTO 单一来源；Go/Ajv 共用 corpus；领域语义检查没有被 Schema 误删。

---

### Task 13: 引入 TanStack Query 基础设施并先迁移低风险读取

**Files:**
- Create: `cmd/web/src/services/queryClient.ts`
- Create: `cmd/web/src/services/queryKeys.ts`
- Create: `cmd/web/src/services/queryOptions.ts`
- Create: `cmd/web/src/services/queryOptions.test.tsx`
- Modify: `cmd/web/package.json`
- Modify: `cmd/web/src/main.tsx`
- Modify: `cmd/web/src/services/api.ts`
- Modify: `cmd/web/src/components/FlowEditor/panels/FlowManagerModal.tsx`
- Modify: `cmd/web/src/components/runtime/TaskStartModal.tsx`

- [ ] **Step 1: 增加 QueryClient 测试壳**

测试 client 默认 `retry: false`、`gcTime` 有界、window focus 不会在压测中造成意外请求；生产 client 对只读接口允许最多 2 次指数重试，对 mutation 不自动重试。

- [ ] **Step 2: 安装并挂 Provider**

```powershell
Set-Location cmd\web
npm install --save-exact @tanstack/react-query
```

`main.tsx` 在 antd `ConfigProvider` 内挂 `QueryClientProvider`。测试 helper 每个用例创建全新 QueryClient，避免 cache 跨测试污染。

- [ ] **Step 3: 定义稳定 query keys**

```ts
export const queryKeys = {
  flows: { all: ['flows'] as const, detail: (id: string) => ['flows', id] as const },
  agents: { all: ['agents'] as const },
  tasks: { all: ['tasks'] as const, detail: (id: string) => ['tasks', id] as const },
  metrics: { cluster: ['metrics', 'cluster'] as const, system: ['system', 'cluster'] as const },
};
```

所有动态参数都进入 key；禁止组件手写字符串 key。

- [ ] **Step 4: 让 services 接受 AbortSignal**

OpenAPI generated client 和遗留 `getJson` 都把 signal 传给 fetch。`ApiError` 包装时识别 `AbortError`，取消不计为连接失败，也不弹 toast。

- [ ] **Step 5: 迁移 flows/capabilities 等低频读取**

先替换 FlowManagerModal、TaskStartModal 的 `useEffect + cancelled`。保存、重命名、删除 mutation 成功后精确 invalidate `queryKeys.flows.all`；服务端错误继续走 `showApiError`。

- [ ] **Step 6: 验证 cache、取消和 mutation invalidation**

```powershell
npx vitest run src/services/queryOptions.test.tsx src/components/FlowEditor/panels/FlowManagerModal.test.tsx
npx tsc -b
npm run test
```

- [ ] **Step 7: 提交基础设施**

```powershell
git add package.json package-lock.json src/main.tsx src/services src/components/FlowEditor/panels/FlowManagerModal.tsx src/components/runtime/TaskStartModal.tsx
git commit -m "feat: introduce TanStack Query server state"
```

---

### Task 14: 迁移运行态轮询并保留 stressbot 状态机语义

**Files:**
- Create: `cmd/web/src/services/useRuntimeQueries.ts`
- Create: `cmd/web/src/services/useRuntimeQueries.test.tsx`
- Modify: `cmd/web/src/pages/EditorPage.tsx:251-361`
- Modify: `cmd/web/src/components/modules/AgentsPanel.tsx:61-94`
- Modify: `cmd/web/src/services/runtimeStore.ts`
- Delete after cutover: `cmd/web/src/services/usePolling.ts`

- [ ] **Step 1: 把现有轮询语义写成 fake-timer 测试**

覆盖：同一 query 不并发；连续 3 次失败才 lost；成功一次 restored；disabled 立即停止；切 taskId 取消旧请求；终态只触发一次 cleanup notification；metrics/system 每个 `dataUpdatedAt` 只 push 一次；AbortError 不增加失败计数。

- [ ] **Step 2: 用 refetchInterval 表达 policy**

task、stress、system、agents 各自使用稳定 key；`enabled` 和 `refetchInterval` 由现有 runtime policy 计算。运行态统一由 `EditorPage` 持有轮询 observer，其他组件读取相同 cache 时不再创建第二个 interval。

- [ ] **Step 3: 建立 Query→runtimeStore 适配器**

`useRuntimeQueries` 用 `dataUpdatedAt` effect 把 cluster metrics/system push 到有限窗口，把 task detail/agents 写入现有 store；用 `failureCount` 和成功时间戳调用 `reportConnectionHealth`。Query cache 是服务端快照事实源，runtimeStore 是 UI 状态机和趋势缓冲，职责不混合。

- [ ] **Step 4: 迁移 AgentsPanel**

Panel 打开时 `refetchQueries`，展示 Query cache 的 agents/per-agent metrics；shutdown mutation 成功后 invalidate agents 和 per-agent metrics。若 EditorPage 正在轮询，不再建立 `setInterval`。

- [ ] **Step 5: 保留日志 cursor 路径**

日志分页继续使用现有 cursor API，不改成 infinite query；它的丢页/截断/游标推进语义与普通列表 cache 不同。

- [ ] **Step 6: 删除 usePolling 并做请求数对比**

浏览器运行 60 秒，idle、running、finalReport 三种模式的请求数分别与 M0 基线对比。相同 endpoint 不得出现两个独立 timer；任务切换后旧 task 请求应被 AbortSignal 取消。

- [ ] **Step 7: 完整前端验证与提交**

```powershell
npx vitest run src/services/useRuntimeQueries.test.tsx
npm run test
npx tsc -b
npm run build
git add src/pages/EditorPage.tsx src/components/modules/AgentsPanel.tsx src/services
git commit -m "refactor: move runtime polling to TanStack Query"
```

M5 出口：断线/恢复、终态、指标趋势和节点列表行为与迁移前一致；重复轮询为零；取消请求不显示网络错误。

---

### Task 15: CEL 决策项目——先编译与差分，再决定是否替换

**Files:**
- Create: `engine/condition_compat_test.go`
- Create: `engine/condition_benchmark_test.go`
- Create if gate passes: `engine/cel_condition.go`
- Modify if gate passes: `engine/cond_parser.go`
- Modify if gate passes: `engine/flow.go`
- Modify if gate passes: `go.mod`
- Modify if gate passes: `go.sum`

- [ ] **Step 1: 建立不可协商的语义 corpus**

把 `cond_parser_test.go` 的所有表达式扩成表驱动 corpus，额外覆盖：缺 key 的 local-false + warn、`missing || fallback`、严格 bool、Go 整数除法/取模、原生 int、嵌套 path、短路时不读取右侧、语法错误加载期 fail-closed。记录结果值和是否产生错误，不只比 true/false。

- [ ] **Step 2: 先给当前 parser 写三组 benchmark**

```go
BenchmarkConditionCurrentParseAndEval
BenchmarkConditionCurrentCompiledAST
BenchmarkConditionCELCompiledProgram
```

数据集使用简单比较、复合布尔、算术、深 path 四档；每档报告 ns/op、B/op、allocs/op。不要用 pprof cumulative 百分比代替 benchmark。

- [ ] **Step 3: 实现“当前语法编译 AST”最小对照组**

把 token/parse 结果在 flow 加载期编译一次，运行期只绑定 `state.Store` 求值。当前 `cond_parser.go` 第 18 行“store 变化所以缓存无收益”的注释必须删除：store 值会变，不代表语法树需要重建。

- [ ] **Step 4: 在 spike 分支接 CEL-Go**

仓库已迁到 `cel-expr/cel-go` 组织，但当前 module path 仍是 `github.com/google/cel-go`；以固定 release tag 引入。加载期 `Compile`，运行期复用 thread-safe `cel.Program`。只暴露一个动态 `state` map 和经过审计的 helper，不开放任意宿主函数、宏或循环扩展。

- [ ] **Step 5: 写双向差分适配**

明确映射 `state.foo`、整数类型、缺值、错误和 warn。若 CEL 的 unknown/error 真值表不能无损复现现有 local-false 语义，不做静默兼容；记录为 gate 失败。

- [ ] **Step 6: 执行采用门禁**

只有同时满足以下条件才进入替换 PR：compat corpus 100% 一致；简单与复合表达式 `ns/op` 不劣于 compiled AST 10%；`allocs/op` 不高于 compiled AST；加载 10k flow 条件的内存增量可接受且记录在基线文档。否则交付 compiled AST 优化并关闭 CEL 项目。

- [ ] **Step 7: 若通过，灰度并删除双实现**

预发布环境运行一次双算采样并记录失配；失配为零后切 CEL。一个发布窗口后删除旧运行期 parser，只保留 corpus 作为兼容契约，不长期维护配置开关。

- [ ] **Step 8: 验证与提交决策结果**

```powershell
go test ./engine -run 'Condition|ParseExpr' -v
go test ./engine -bench 'BenchmarkCondition' -benchmem -count=5
```

通过时提交 `refactor: compile flow conditions with CEL`；未通过时提交 `perf: compile condition AST at flow load`，并在基线文档记录拒绝 CEL 的量化原因。

---

### Task 16: 事件化 listen 决策项目——消除轮询但保留 Robot mailbox

**Files:**
- Create: `network/listen_wait_benchmark_test.go`
- Create: `engine/listen_wait_test.go`
- Modify: `network/listen_queue.go`
- Modify: `network/listen_queue_test.go`
- Modify: `network/connection.go`
- Modify: `engine/action.go:1559-1639`
- Modify: `robot/robot.go:598-760`

- [ ] **Step 1: 记录轮询基线**

基准覆盖 1、1k、10k 个空闲 listener 等待 30 秒，以及消息立即到达、超时、ctx cancel、队列已就绪。记录 wakeup 数、CPU、分配和从 `RecvFrameAt` 到消费的 p50/p99。

- [ ] **Step 2: 写事件语义测试**

覆盖 FIFO、容量满丢最旧、Dropped 累计、Clear、Push/Wait 竞态、cancel、timeout、消息已在队列时立即返回。额外测试 listen 等待期间 Robot mailbox 中的回调仍按 owner 顺序执行；这是替换 `ae.sleep` 的硬门禁。

- [ ] **Step 3: 给 listenQueue 增加边沿通知**

```go
type listenQueue struct {
    // existing ring fields
    notify chan struct{} // capacity 1; only a wake hint, queue remains source of truth
}
```

`Push` 完成入队后非阻塞发送通知；Wait 总是先在锁内重查队列，再 select notify/context/timer，再循环，避免 lost wakeup。通知不携带 Message，队列 FIFO 和覆盖语义保持唯一来源。

- [ ] **Step 4: 把通知并入 Robot scheduler wait**

不能在 ActionExecutor 中直接阻塞 `<-notify`，否则会饿死 mailbox。扩展现有协作式 wait，使它同时 select：listen notify、Robot mailbox、ctx、deadline；收到 mailbox 先执行 owner callback，再继续等 listen。

- [ ] **Step 5: 替换 `execListen` poll loop**

保留 `RecvFrameAt` 作为监听等待终点、ready 消息不产生 0ms 样本、timeout/cancel 错误码和日志字段。`pollMs` 在一个发布窗口内继续被解析但不参与调度，前端标注“事件模式下已废弃”；下一次配置大版本再删除字段。

- [ ] **Step 6: 执行采用门禁**

10k 空闲 listener wakeup 至少下降 95%；空闲 CPU 至少下降 80%；消息消费 p99 不劣化；allocs/op 不增加；`go test -race ./network ./engine ./robot` 通过；mailbox 顺序测试 100% 通过。任一失败则不替换生产路径。

- [ ] **Step 7: 验证与提交**

```powershell
go test ./network ./engine ./robot -run 'Listen|Mailbox' -count=20
go test -race ./network ./engine ./robot -run 'Listen|Mailbox'
go test ./network ./engine -bench 'BenchmarkListen' -benchmem -count=5
```

通过后提交 `perf: wake listen actions from queue events`。

---

### Task 17: sqlc 决策项目——只迁移固定 SQL

**Files:**
- Create: `sqlc.yaml`
- Create: `admin/sql/queries/history.sql`
- Create: `admin/sql/queries/templates.sql`
- Create: `admin/dbgen/db.go`
- Create: `admin/dbgen/models.go`
- Create: `admin/dbgen/history.sql.go`
- Create: `admin/dbgen/templates.sql.go`
- Create: `admin/dbgen/querier.go`
- Create: `admin/dbgen_integration_test.go`
- Modify: `admin/history.go`
- Modify: `admin/flow_template.go`
- Modify: `admin/action_template.go`
- Modify: `admin/listen_template.go`

- [ ] **Step 1: 盘点 SQL 并分类**

把查询分成三类：固定列/固定 predicate，可直接迁移；可由 `sqlc.arg` 表达的有限可选项；运行期拼接 ORDER/filter/IN 的动态查询。第一轮只迁移第一类，第二类单独评估，第三类继续手写并集中到明确函数。

- [ ] **Step 2: 以 Goose migration 目录作为 schema 输入**

```yaml
version: "2"
sql:
  - engine: "mysql"
    schema: "admin/migrations"
    queries: "admin/sql/queries"
    gen:
      go:
        package: "dbgen"
        out: "admin/dbgen"
        sql_package: "database/sql"
        emit_interface: true
        emit_empty_slices: true
```

这样 Goose 是 schema 事实源，不维护第二份手写 `schema.sql`。

- [ ] **Step 3: 先迁移模板库固定 CRUD**

为 flow/action/listen template 的 Get/Create/Update/Delete/快照版本查询写命名 SQL；生成后用 `dbgen.New(db)` 注入 store。事务方法使用 `q.WithTx(tx)`，不在生成包外复制 SQL。

- [ ] **Step 4: 再迁移 history 固定查询**

优先迁移 assignment/report/event/config archive/meta/aggregated 的固定查询和 delete 事务。`HistoryStore.List`、动态 timeseries downsample、动态 tag/filter 保持手写，直到 sqlc 表达方案比现状更清楚。

- [ ] **Step 5: 写生成物与行为回归测试**

每个迁移的方法使用当前 sqlmock/集成测试数据断言同样的 null、JSON、time 和受影响行数。真实 MySQL 集成测试先 Goose up，再通过生成 Querier 执行 CRUD，证明 schema 与生成查询一致。

- [ ] **Step 6: 执行收益门禁**

至少迁移 30 条固定 SQL；删除等量手写 Scan/Exec boilerplate；`sqlc generate` 和 `sqlc vet` 通过；行为测试无回归；关键 Archive/List benchmark 不劣化 5%。达不到 30 条或动态适配代码反而更多时，仅保留模板库迁移，不扩展到 history。

- [ ] **Step 7: 固定 CI 生成检查**

```powershell
sqlc generate
sqlc vet
sqlc diff
$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'
go test ./admin/... -run 'Template|History|DBGen' -v
go build ./...
```

生成文件提交仓库，禁止手改 `admin/dbgen/*.go`。

- [ ] **Step 8: 分包提交**

模板库提交 `refactor(db): generate template queries with sqlc`；history 固定查询提交 `refactor(db): generate fixed history queries with sqlc`。两个提交都能独立回滚应用代码，不回滚 Goose schema。

---

### Task 18: 全路线发布验收

**Files:**
- Modify: `docs/modernization/baseline-2026-08.md`
- Modify: `docs/runbooks/mysql-migration.md`
- Modify: `README.md` only through the `update-readme` skill during execution

- [ ] **Step 1: 运行仓库规定的完整验证**

```powershell
$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'
go build ./...
Set-Location cmd\web
npx tsc -b
npm run test
```

- [ ] **Step 2: 运行安全与迁移专项**

验证错误证书、无证书、绕过代理、旧数据库、失败 migration、并发 Admin、有限 Supervisor 重试。所有失败都必须可观察且不能开放半健康服务。

- [ ] **Step 3: 运行 2–5 分钟 Agent 验证**

按仓库流程启动 standalone 与 distributed 各一次，确认任务注册、下发、停止、指标、日志、历史归档均走 HTTPS/mTLS，日志无非预期 error/warn。

- [ ] **Step 4: 前端手工验收**

打开 flow.json 查看结构与 refsCheck 报告；测试 50 步 undo/redo、流程切换历史清空、任务开始/停止、断网三次提示与恢复、AgentPanel 刷新、日志 cursor。

- [ ] **Step 5: 对照 M0 基线签字**

在基线文档追加每个 M 的实际请求数、CPU/RSS、benchmark、迁移演练和回滚结果。只有达到各 M 出口条件才开始下一 M；M6 每个项目独立签字。

## 4. CI 门禁

在现有 CI 中固定增加：

```text
go generate ./api/openapi && git diff --exit-code
npm run generate:api && git diff --exit-code
sqlc generate && sqlc vet && sqlc diff
go test ./schema ./admin ./agent ./engine ./network ./codec
npm run test && npx tsc -b
```

MySQL 集成测试使用独立数据库服务和一次性 database name；测试结束只删除该明确数据库，不对共享实例执行广泛清理。

## 5. 关键停止条件

- mTLS 证书轮换没有演练前，不关闭旧控制面端口。
- 生产数据库没有可恢复备份前，不执行第一条 Goose migration。
- 旧库 reconcile 不是可重入时，不启用自动迁移。
- OpenAPI 生成层尚未覆盖错误响应时，不删除手写 DTO/parser。
- Query 出现重复 observer timer 或取消被计为断线时，不删除 `usePolling`。
- CEL 语义差分不为零时，不替换现有条件语义。
- 事件 listen 饿死 mailbox 或改变队列丢弃语义时，不切生产路径。
- sqlc 让动态 SQL 更晦涩或生成适配代码超过删除量时，停止扩展迁移范围。

## 6. 官方参考

- Goose Provider 默认不自动加数据库锁，支持 `embed.FS`、实例 Provider 和 Go migration：https://pressly.github.io/goose/documentation/provider/
- Goose SQL annotation 与 non-transactional migration：https://pressly.github.io/goose/documentation/annotations/
- oapi-codegen strict server、生成配置与 request validation 边界：https://github.com/oapi-codegen/oapi-codegen
- Zundo `partialize`、`limit`、`pause/resume/clear`：https://github.com/charkour/zundo
- TanStack Query polling、query keys、cancellation：https://tanstack.com/query/latest/docs/framework/react/guides/polling
- JSON Schema Go v6 Draft 2020-12：https://pkg.go.dev/github.com/santhosh-tekuri/jsonschema/v6
- Ajv strict mode：https://ajv.js.org/strict-mode.html
- CEL-Go 编译与复用 program：https://github.com/cel-expr/cel-go
- sqlc MySQL 与 CI 生成检查：https://docs.sqlc.dev/en/stable/tutorials/getting-started-mysql.html
- Go 标准库 RC4 key 约束与原地 XOR：https://pkg.go.dev/crypto/rc4
