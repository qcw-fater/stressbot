# T2-C2-Go Brief — encode 侧（Go）切 CodecResolver

> 你是 implementer，与另一 agent（connectionPump，network/）**并行**跑。**严格文件边界：只改 `engine/action.go` + `robot/robot.go`，绝不碰 `network/`、`script/`、`adapter/`**（另一 agent 在改 network/；script/adapter 留给后续 Phase 2）。
> 参考：`plans/declarative-codec/02-track-backend-integration.md` §2-C（切片 4）+ §4「不需要改动的点」、`reports/t2-c1-report.md`（resolver 已接 dial/decode）、`progress-ledger.md` §「全局约束」+ §「gofmt/换行 环境注」。
> 工作目录：worktree 根。**不要 git commit。**

## 目标

把 **Go 侧 encode**（ActionExecutor 的 protocolEncode/ExpectedRouteKey/DescribeError + 心跳 goBuilder encode + listen routeKey）从 per-robot `r.adp`（Lua RobotAdapter）切到 **CodecResolver**（按 `<proto>:<service>` 解析的 Go SchemaAdapter）。**保留 `r.adp`/`RobotAdapter`/`NewRobotAdapter`/`Context.Adapter`**（Lua encode + 清理留给 Phase 2，本任务不删）。

## 范围（严格边界）

**只改 `engine/action.go` + `robot/robot.go`：**
- `engine/action.go`：`ActionExecutor.adp adapter.Adapter` 字段 → `resolver adapter.CodecResolver`；`NewActionExecutor` 形参 `adp` → `resolver`；`protocolEncode` 加 `service string` 入参，内部 `ae.resolver.Resolve(proto+":"+service)` 取 adapter（nil→`ErrEncodeFailed` fail loud 中文）再 Encode；3 处 `ExpectedRouteKey`（execSend/execRequest/execListen，约 :1080/:1127/:1202）+ `DescribeError`（:897）同样按 `def.Service`+pattern 推 proto 解析。pattern→proto：`tcp*`→"tcp"、`udp*`→"udp"。
- `robot/robot.go`：`NewActionExecutor(…, r.adp, …)` → `NewActionExecutor(…, r.resolver, …)`（`:160`）；心跳 goBuilder（`netSenderAdapter.RegisterHeartbeat` 闭包，`:1254/1295`）encode 由 `ns.robot.adp` 改为 `ns.robot.resolver.Resolve(cfg.Transport+":"+cfg.Service)`（nil→Warn+skip）；listen routeKey（`robotActionHandler.RegisterListen`，`:749`）由 `h.robot.adp.ExpectedRouteKey` 改为 `h.robot.resolver.Resolve(server).ExpectedRouteKey`（server=`parseServer(ref.Server)` 得到的 proto:service；nil→fail loud）。

**保留不动（Phase 2 / 另一 agent）：**
- ❌ `r.adp` 字段、`NewRobotAdapter`、`RobotAdapter`、`ManagerConfig.Adapter`、`Context.Adapter`、业务 Lua encode（`script/`）—— Phase 2。
- ❌ `network/`（connectionPump agent 在改）。
- ❌ `adapter/`。

## 接口契约（与 connectionPump agent 并行的关键）

- **心跳 builder 签名不变**：`network.HeartbeatConfig.Builder func() []byte`；`Connection.RegisterHeartbeat(HeartbeatConfig)` 调用不变（你只改闭包内 encode 源 r.adp→resolver，不改签名/调用）。
- **`NetSender.RegisterHeartbeat(cfg HeartbeatActionConfig)` 不变**。
- resolve 出的 adapter nil 时 **fail loud**（心跳 Warn+skip 本 tick；listen/encode 返回 ActionError），不静默兜底。

## 关键约束

- **不写兼容兜底**：Resolve nil 直接报错。
- **新字段全链路一致**：resolver 从 `Robot.resolver`（2-C1 已就位）→ NewActionExecutor → ActionExecutor.resolver → 各 encode 调用 Resolve。
- **保留 r.adp**（Lua 还用，Phase 2 删）——不要删 r.adp/RobotAdapter。
- 仅改 `engine/action.go` + `robot/robot.go`。跨文件即越界。
- 错误用 NewActionError（复用 ErrEncodeFailed/ErrConnNotFound 等）；日志中文；godoc。
- **Windows 环境注**：`gofmt -l` 标 .go 脏是 autocrlf CRLF，**不要**对单文件 `gofmt -w`。
- **不要 git commit。**

## 工作方式（TDD）

1. 先读 `engine/action.go`（ActionExecutor 结构 :105、NewActionExecutor :195、protocolEncode :1002、6 处 adp 调用）、`robot/robot.go`（NewActionExecutor :160、心跳闭包 :1254/1295、listen routeKey :749、RegisterListen parseServer）、`adapter/codec_resolver.go`（Resolve 接口）。
2. RED：ActionExecutor 用 fake resolver，按 service Resolve 出对应 fake adapter，验证 encode/ExpectedRouteKey 用对 adapter；Resolve nil→fail loud。
3. GREEN：ActionExecutor.adp→resolver + protocolEncode+service + 3 ExpectedRouteKey + DescribeError；robot.go NewActionExecutor(r.resolver) + 心跳/listen resolve。
4. `go build ./...`、`go vet ./...`、`go test ./engine/... ./robot/... -count=1` 全绿。（network/ 由另一 agent 改，可能临时不可编译——若 `go build ./...` 因 network/ 失败，仅 build `./engine/... ./robot/...` 验证本任务，报告中注明 network/ 并行态。）

## 验收（self-review）

- ActionExecutor.adp→resolver；protocolEncode+service 按 `<proto>:<service>` Resolve；ExpectedRouteKey×3 + DescribeError 同款；nil→fail loud。
- robot.go：NewActionExecutor(r.resolver)；心跳 goBuilder encode 走 resolver；listen routeKey 走 resolver。
- r.adp/RobotAdapter/NewRobotAdapter/Context.Adapter **保留未动**（Phase 2）。
- 仅改 engine/action.go + robot/robot.go。
- go build/vet/test（engine/robot）绿。

## 报告

写 `plans/declarative-codec/reports/t2-c2-go-report.md`：实现、resolver 全链路（ActionExecutor/心跳/listen）、接口契约保持（与 connectionPump 并行）、TDD、改动文件、self-review、concerns、**Phase 2 接力**（Lua encode→resolver + 删 r.adp/RobotAdapter/Lua 模块）。
返回（<15 行）：Status、改动文件、测试摘要、concerns、报告路径。
