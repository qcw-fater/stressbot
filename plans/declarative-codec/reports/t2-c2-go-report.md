# T2-C2-Go 报告 — encode 侧（Go）切 CodecResolver

> 状态：**DONE**（工作树未提交，待批次确认）。
> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）。
> ⚠️ 与 connectionPump agent（network/）并行：`network/connection.go` / `heartbeat.go` / `gnet.go`
> 当前处于 connectionPump 中途态（本任务验证时**临时 stash** 这三文件，验证后已恢复），
> 全仓 `go build ./...` 因 network/ 中途态**失败**（非本任务引入）；本任务范围 `./engine/... ./robot/...` 全绿。
> ⚠️ 运行时验证待办（见 §6）：encode 走 Go codec 字节级与旧 Lua RobotAdapter 一致（T1 冻结对拍已证理论一致），
> 实际多连接 encode/listen routeKey/心跳正确性需 controller 跑 `go run ./cmd/agent` 2~5 分钟。

## 1. 目标与边界

把 **Go 侧 encode**（`ActionExecutor` 的 `protocolEncode` / `ExpectedRouteKey` / `DescribeError` +
心跳 goBuilder encode + listen routeKey）从 per-robot `r.adp`（Lua RobotAdapter）切到 **CodecResolver**
（按 `<proto>:<service>` Resolve 出的 Go SchemaAdapter）。**保留 `r.adp` / `RobotAdapter` / `NewRobotAdapter` /
`Context.Adapter`**（Lua encode + 清理留给 Phase 2 / 2-C3）。

**严格文件边界**：仅改 `engine/action.go` + `robot/robot.go`；未触碰 `network/` / `script/` / `adapter/`。
（network/* 的 working tree 变更属于并行 connectionPump agent，本任务验证期间临时 stash 后恢复，未触碰其内容。）

## 2. 实现内容

### 2.1 `engine/action.go`

1. **`ActionExecutor.adp adapter.Adapter` → `resolver adapter.CodecResolver`**（字段类型变更）。
   godoc 注明 T2-C2 起按 `<proto>:<service>` Resolve，nil 由调用方 fail loud。
2. **`NewActionExecutor` 形参** `adp adapter.Adapter` → `resolver adapter.CodecResolver`。
3. **新增三个纯辅助方法**（encode 侧统一入口）：
   - `resolveAdapter(proto, service string) adapter.Adapter`：拼 `proto+":"+service` Resolve；resolver==nil → nil。
   - `expectedRouteKey(proto, service string, route any) string`：Resolve 后 ExpectedRouteKey；nil → 空串。
   - `describeError(proto, service string, code uint64) string`：Resolve 后 DescribeError；nil → 空串。
4. **`protocolEncode` 加 `service string` 入参**（原签名仅 protocol）：内部 `resolveAdapter(proto, service)`，
   nil → 返回 nil（调用方 fail loud）；非 nil → EncodeTCP/UDP。
5. **3 处 `ExpectedRouteKey` 切换**（execSend / execRequest / execListen）：
   `ae.adp.ExpectedRouteKey(def.Route)` → `ae.expectedRouteKey(protocol, def.Service, def.Route)`。
6. **`handleHeaderError` 加 `proto string` 入参** + DescribeError 走 `ae.describeError(proto, def.Service, ...)`；
   两处调用点（execRequest / execListen）同步传 proto。
7. **fail loud 文案增强**：`protocolEncode` 返回 nil 时 ErrEncodeFailed detail 带 `service=` + server 串
   （"codec 未映射（resolver.Resolve(tcp:logic) nil）"），便于排查 codec 配置遗漏。

### 2.2 `robot/robot.go`

1. **`NewActionExecutor(r.state, ..., r.adp, ...)` → `NewActionExecutor(r.state, ..., r.resolver, ...)`**（:179）。
2. **listen routeKey（`RegisterListen` ~:794）**：
   `h.robot.adp.ExpectedRouteKey(ref.Route)` → 先 `h.robot.resolver.Resolve(ref.Server)` 取 serverAdp，
   **nil → fail loud（中文 error，带 server 串）**；非 nil → `serverAdp.ExpectedRouteKey(ref.Route)`。
   （`ref.Server` 经 parseServer 已校验为 `proto:service` 格式，直接喂 Resolve 安全。）
3. **心跳 goBuilder encode（`RegisterHeartbeat` 闭包）**：
   闭包捕获由 `ns.robot.adp` 改为 `resolver := ns.robot.resolver`；每次 tick encode 前
   `adp := resolver.Resolve(transport+":"+cfg.Service)`，**nil → Warn 日志 + return nil（skip 本 tick）**，
   非 nil → EncodeTCP/UDP。godoc 同步更新。
4. **保留**（Phase 2 / 2-C3 删）：`r.adp` 字段 / `r.adp = robotAdp`（:170）/ `Context.Adapter: r.adp`（:223）。

### 2.3 不变量

- **接口契约保持**（与 connectionPump 并行关键）：
  - 心跳 builder 签名 `func() []byte` 不变；`Connection.RegisterHeartbeat(HeartbeatConfig)` 调用不变；
    `NetSender.RegisterHeartbeat(cfg HeartbeatActionConfig)` 签名不变。仅改闭包内 encode 源。
- **fail loud 不兜底**：
  - encode（ActionExecutor）/ listen routeKey：Resolve nil → 中文 error 上抛（ErrEncodeFailed / RegisterListen error）。
  - 心跳：Resolve nil → Warn + skip 本 tick（与 2-B 心跳 tick 容错语义一致；单 tick 失败不应终止整条心跳 goroutine）。
- **保留**：r.adp / RobotAdapter / NewRobotAdapter / ManagerConfig.Adapter / Context.Adapter（Phase 2 / 2-C3 删）。

## 3. resolver 全链路（encode 侧闭环）

```
main.go runStandalone / task_runner Run（T2-C1 已接线）
  └─ NewRobot(..., globalAdp, resolver, ...)
        ├─ r.resolver = resolver（T2-C1 已就位）
        └─ r.actionExec = NewActionExecutor(r.state, netSender, r.factory, r.resolver, engineTimingLevel)  # T2-C2 改：r.adp → r.resolver
              ├─ execSend / execRequest（protocolEncode + expectedRouteKey）
              │     └─ ae.resolveAdapter(proto, def.Service) = resolver.Resolve("tcp:logic") → Go SchemaAdapter
              │           ├─ nil → ErrEncodeFailed（带 server 串，fail loud）
              │           └─ 非 nil → EncodeTCP/UDP + ExpectedRouteKey
              ├─ execListen（expectedRouteKey）
              │     └─ ae.expectedRouteKey(proto, def.Service, def.Route)
              └─ handleHeaderError（describeError）
                    └─ ae.describeError(proto, def.Service, code) = Resolve → DescribeError
        ├─ netSenderAdapter.RegisterHeartbeat（goBuilder 闭包，T2-C2 改 encode 源）
        │     └─ resolver.Resolve(cfg.Transport+":"+cfg.Service) → adp
        │           ├─ nil → Warn + skip 本 tick
        │           └─ 非 nil → EncodeTCP/UDP
        └─ robotActionHandler.RegisterListen（routeKey）
              └─ resolver.Resolve(ref.Server) → serverAdp
                    ├─ nil → fail loud（中文 error）
                    └─ 非 nil → serverAdp.ExpectedRouteKey(ref.Route)
```

T2-C2 完成后 **encode / decode 全程无 Lua codec**（decode Go 自 2-C1；encode Go 自 2-C2）。
r.adp 仍保留给业务 Lua `Context.Adapter`（script/ 层）—— 2-C3 删 r.adp / RobotAdapter / Lua 模块整条。

## 4. TDD（RED → GREEN）

### 4.1 RED（测试先行，确认编译期失败）

- `engine/action_resolver_test.go`（新）：引用 `ActionExecutor{resolver: ...}`（unknown field）→ build failed。

### 4.2 GREEN（实现 → 全 PASS）

| 测试 | 期望 | 结果 |
|---|---|---|
| `TestProtocolEncode_ResolverDispatchesByProtoService` | tcp:logic 走 EncodeTCP、udp:battle 走 EncodeUDP、未映射 nil | PASS |
| `TestProtocolEncode_ResolverNil_ReturnsNil` | Resolve nil → protocolEncode 返回 nil（fail loud 由调用方） | PASS |
| `TestResolveAdapterForPattern` | resolveAdapter 按 proto+service 命中/未映射 nil | PASS |
| `TestExpectedRouteKeyViaResolver` | ExpectedRouteKey 走 Resolve 出的 adapter | PASS |
| `TestDescribeErrorViaResolver` | DescribeError 走 Resolve 出的 adapter，code 透传 | PASS |
| `TestResolveAdapterServerStringFormat` | server 串格式 `proto:service`（防 regression） | PASS |
| `TestRegisterListen_ResolverNil_FailLoud` | listen routeKey Resolve nil → 中文 error（含 codec/server） | PASS |
| `TestRegisterListen_ResolverHit_NoError` | resolver 命中 + 无 conn → 跳过该 group，不报错 | PASS |

engine 既有测试（含心跳 proto/raw-binary、map binding、filter、cond_parser）+ robot 既有测试
（dial Resolve、effectiveListenQueueSize、validateListenDef）全绿无回归。

## 5. 改动文件

| 文件 | 改动 |
|---|---|
| `engine/action.go` | `ActionExecutor.adp`→`resolver`；`NewActionExecutor` 形参；新增 `resolveAdapter` / `expectedRouteKey` / `describeError`；`protocolEncode`+service；3 处 ExpectedRouteKey + handleHeaderError 切 resolver；fail-loud 文案增强 |
| `engine/action_resolver_test.go`（新） | 6 个 encode/routeKey/describe resolver 测试 + stubResolverE / encodeSpyAdapter |
| `robot/robot.go` | `NewActionExecutor(r.resolver)`；listen routeKey 走 resolver.Resolve(server)（nil→fail loud）；心跳 goBuilder encode 走 resolver（nil→Warn+skip）；godoc 更新 |
| `robot/listen_resolver_test.go`（新） | 2 个 RegisterListen resolver fail-loud / hit 测试 |

**未触碰**：`network/`（connectionPump agent 中途态）、`script/`（业务 Lua / Context.Adapter）、
`adapter/`（RobotAdapter / LuaAdapter 保留 Phase 2）、`cmd/` / `agent/`（resolver 接线 2-C1 已完成）、
前端 / admin。未 git commit。

## 6. Self-review（对照 brief 验收清单）

- [x] `ActionExecutor.adp`→`resolver`；`protocolEncode` 加 service + `resolver.Resolve(proto+":"+service)`；
      3 处 `ExpectedRouteKey`（execSend/execRequest/execListen）+ `DescribeError`（handleHeaderError）同款切 resolver。
- [x] `robot.go`：`NewActionExecutor(r.resolver)`；心跳 goBuilder encode 走 `resolver.Resolve(transport:service)`
      （nil→Warn+skip）；listen routeKey 走 `resolver.Resolve(ref.Server)`（nil→fail loud）。
- [x] **保留** r.adp / RobotAdapter / NewRobotAdapter / Context.Adapter / ManagerConfig.Adapter（Phase 2 / 2-C3）。
- [x] 仅改 `engine/action.go` + `robot/robot.go`（+ 2 新测试）；未越界。
- [x] **接口契约保持**：心跳 builder 签名 / `RegisterHeartbeat` 调用 / `NetSender.RegisterHeartbeat` 不变。
- [x] fail loud 不兜底：encode → ErrEncodeFailed（带 server 串）；listen → 中文 error；心跳 → Warn+skip（容错语义）。
- [x] 错误用 NewActionError（复用 ErrEncodeFailed）；日志中文；godoc 完整。
- [x] `go build ./engine/... ./robot/...` + `go vet ./engine/... ./robot/...` + `go test ./engine/... ./robot/... -count=1` 全绿。
- [x] Windows 注：未对单文件 gofmt -w（autocrlf CRLF 环境注）；改动文件 gofmt canonical 由 git autocrlf 在 commit 时归一。
- [x] 未 git commit。

## 7. 并行态构建说明

另一 agent（connectionPump）正在改 `network/connection.go` / `heartbeat.go` / `gnet.go`（替换三协程 / 引入
heartbeatRuntime 等）。本任务**验证期间**临时 `git stash push` 这三个 network 文件，使本任务范围
（`./engine/... ./robot/...`）可独立 build/vet/test 全绿；**验证后 `git stash pop` 恢复**，network/* 的内容
完全属于 connectionPump agent，本任务**未触碰**其内容。

因此**当前工作树** `go build ./...` **会失败**（network/ 中途态：heartbeatRuntime / StartDecodeLoop 等符号
未定义），非本任务引入。connectionPump agent 完成后全仓 build 应恢复绿。

## 8. ⚠️ 运行时验证待办

单测仅覆盖 resolver 路径分发 + nil fail-loud。以下需 controller/用户跑真实服务端验证
（可与 2-A2.2 / 2-B.2 / 2-C1 运行时验证待办**合并一次** `go run`）：

1. **encode 字节级一致**：tcpSend / tcpRequest / udpRequest 走 Go SchemaAdapter encode（codec.json）
   与旧 Lua RobotAdapter encode（codec.lua）字节一致（T1 冻结对拍已证理论一致，2-C2 实战闭环）。
2. **多连接 codec 解析**：tcp:logic / tcp:battle / udp:battle 各连接 encode 用**正确**的 codec（非混用）。
3. **listen routeKey 正确**：routeKey 由 Go SchemaAdapter 算（与 decode 侧的 Go adapter 一致，
   闭环双 codec；旧 Lua 路径已下线）。
4. **心跳 encode 正确**：battle UDP 150ms / TCP 10s 心跳字段值正确、稳定、不掉线。
5. **fail-loud 行为**：若某连接 codec 未配置，tcpSend/request/listen 注册应中文 error 暴露配置遗漏
   （非静默兜底）；心跳应 Warn + skip。

验证步骤（CLAUDE.md §验证流程）：
```
rm -f log/stressbot.log
go run ./cmd/agent -config conf/config.json
# 运行 2~5 分钟
# grep -i "error\|warn\|失败" log/stressbot.log | grep -v "headError"
# 期望：无「codec 未映射」类异常（生产 3 份 codec.json 已配齐 tcp:logic/tcp:battle/udp:battle）
```

## 9. Concerns

1. **network/ 并行态阻断全仓 build**：connectionPump agent 中途改 connection.go/heartbeat.go/gnet.go
   使 network/ 不可编译。本任务验证用 `git stash` 隔离，工作树最终态 network/* 不属本任务。
   批次合并时须确保 connectionPump agent 已完成；若两任务先后提交，按 network/* → engine+robot 顺序可保 build 绿。
2. **心跳 fail-loud 是 Warn+skip 而非 ErrEncodeFailed**：与 encode/listen 的 fail-loud 不对称，
   原因是心跳 tick 容错语义（单 tick 失败不应终止整条 goroutine，2-B 设计）。若 codec 长期未映射，
   心跳会持续 skip 导致连接被服务端踢，届时日志 Warn 会暴露问题（不静默）。这是有意的设计分歧，非 bug。
3. **handleHeaderError 的 DescribeError nil 不 fail loud**：Resolve nil 时 DescribeError 返回空串
   （headerErr 描述缺失非致命），仅 detail 不含人类可读前缀；headerErr 错误码本身仍按 NewServerError 上抛。
   与 encode/listen 的 fail-loud 不同，因为 headerErr 描述是增强信息而非核心路径。
4. **stubResolver / encodeSpyAdapter 测试桩**：engine 测试桩与 robot 测试桩（dial_resolver_test.go 的
   stubResolver）类型名重复但分属不同包（engine / robot），不冲突。encodeSpyAdapter 仅验分发不验字节正确性
   （字节级由 T1 冻结对拍覆盖）；若 2-C3 需更深测试，应改用 LoadCodecResolver 真实路径。
5. **buildProtoBody 临时 ActionExecutor receiver**（2-B.1 遗留）：未触动，与 T2-C2 无关。
6. **gofmt CRLF**：本 worktree autocrlf=true，所有 .go 检出为 CRLF，`gofmt -l` 全标脏。改动文件内容 canonical
   （`sed 's/\r$//' | gofmt -l` 空），未对单文件 gofmt -w（按 Windows 环境注）。

## 10. Phase 2 接力（2-C3：删 Lua codec 整条）

T2-C2 完成后 encode/decode 全程无 Lua codec，2-C3 可安全删 Lua codec 残留：

| 当前（2-C2 后） | 2-C3 改成 |
|---|---|
| `r.adp *adapter.RobotAdapter` 字段（robot.go） | 删 |
| `NewRobotAdapter`（adapter/robot_adapter.go） | 删整文件 |
| `RobotAdapter` / `LuaAdapter` 类型 | 删（codec.lua 不再加载） |
| `ManagerConfig.Adapter *LuaAdapter`（robot/manager.go） | 删 |
| `Context.Adapter`（script/，业务 Lua 编解码入口） | 删或改 resolver helper |
| `cmd/agent/main.go` LuaAdapter 构造路径 | 删 |
| `agent/task_runner.go` LuaAdapter 加载 | 删 |
| codec.lua / error.lua 文件 | 删（codec.json 已替代） |
| connectionPump：三协程替换（decodeLoop / heartbeat / listenLoop） | per-connection Go codec |

2-C3 完成后 2-D 可删 `luaMu` / `withReleasedMu`（须 2-A/B/C 全完 + 审计闸门）。
