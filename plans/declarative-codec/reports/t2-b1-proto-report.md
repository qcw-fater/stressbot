# T2-B.1 Proto 报告 — 心跳双模式扩展：补 proto（c2sProto+bindings）分支

> 任务 brief：`plans/declarative-codec/briefs/t2-b1-proto-brief.md`
> 工作目录：worktree 根 `D:\Gitee\stressbot\.claude\worktrees\declarative-codec`，分支 `worktree-declarative-codec`。
> **未 git commit / add**（遵 brief 硬约束）。

## Status

**DONE**。把 2-B.1 已落地的 raw-binary 心跳框架扩展为双模式：补 **proto 模式**（c2sProto+bindings），
复用现有 tcpSend 的 proto 构建机制（抽 `BuildProtoBody` 共享），`go build ./...` + `go vet ./...` 干净，
全仓 `go test ./... -count=1` 全绿（11 个 ok 包）。
心跳框架现覆盖通用游戏服三类主流形态：proto（多数 protobuf 服）/ raw-binary（自研协议服/战斗同步）/ 空 body（轻量保活）。

## 改动文件清单

| 文件 | 类型 | 说明 |
|---|---|---|
| `engine/action.go` | 改 | 抽 `BuildProtoBody(c2sProto, bindings, store, factory, actionName)`（复用 bindFields 全套 binding 解析）；`buildBody` 重构为调用它（行为零变化）；`execHeartbeat` 加 c2sProto/heartbeatFields 互斥校验（→ ErrHeartbeatConfig，中文）+ 装配 C2SProto/Bindings |
| `engine/heartbeat.go` | 改 | `HeartbeatActionConfig` 加 `C2SProto string` + `Bindings []FieldBind` 字段（双模式 body 入参，与 Fields 互斥）；godoc 补双模式说明 |
| `engine/heartbeat_proto_test.go` | 新增 | 7 个测试（BuildProtoBody 4 + buildBody 重构无回归 1 + execHeartbeat 装配/互斥 2） |
| `robot/robot.go` | 改 | `netSenderAdapter.RegisterHeartbeat` 的 Go builder 闭包按模式分派（C2SProto→BuildProtoBody / Fields→BuildHeartbeatBody / 都无→空 body）；多捕获 bindings/c2sProto/factory；godoc 补双模式说明 |

未碰 `engine/flow.go`（ActionDef 已有 C2SProto/Bindings 字段，复用）、`network/heartbeat.go`、
`script/api_network.go`、`adapter/`、`admin/`、`agent/`、`cmd/`、前端、密钥 API、codec、Lua 心跳路径。

## BuildProtoBody 抽取（复用 bindFields，不新造 binding 解析）

签名（**复用**而非新造）：

```go
func BuildProtoBody(c2sProto string, bindings []FieldBind, store *state.Store,
    factory *protox.Factory, actionName string) (body []byte, skip bool, err error)
```

- `c2sProto==""` → `(nil, false, nil)`（与 buildBody 旧 `return nil, nil` 对齐，保留 tcpSend 无 proto 场景）。
- `factory.Create(c2sProto)` → msg；`ae.bindFields(msg, bindings, actionName)`（用临时 `ActionExecutor{store, factory}` 复用完整 condition/optional/required/map 解析路径）；`factory.Serialize(msg)` → body。
- **不新写 proto 字段解析**：bindFields 是 ActionExecutor 方法（依赖 ae.store/ae.factory 的 resolveFieldValueStrict/resolveMapValueStrict 等），用临时 ActionExecutor 复用避免拷贝大段解析代码。
- `actionName` 参数：bindFields 错误上下文用（tcpSend 传 `def.Name`，心跳传 `"heartbeat:service"` 或空）。
- `skip` 当前恒 false（预留与心跳 SkipWhenMissing 对齐的未来扩展）。

### 与 brief 的签名差异

Brief 给的签名是 `BuildProtoBody(c2sProto, bindings, store, factory)`（4 参）。实现加了第 5 个 `actionName`，
**唯一目的**是保留 buildBody 重构后 bindFields 错误上下文不变（旧 `buildBody` 传 `def.Name`）。
否则心跳的 binding 错误会丢 action 上下文。这是为「buildBody 行为零变化」不变量服务的最小扩展，
不改 brief 的设计意图（4 个核心入参不变，actionName 仅是错误上下文，心跳传空亦合法）。

## buildBody 重构（行为零变化证明）

旧：

```go
func (ae *ActionExecutor) buildBody(def *ActionDef) ([]byte, error) {
    if def.C2SProto == "" { return nil, nil }
    msg, err := ae.factory.Create(def.C2SProto)            // err → NewActionError(ErrCreateMsg, "proto="+...)
    if err := ae.bindFields(msg, def.Bindings, def.Name); err != nil { return nil, err }
    body, err := ae.factory.Serialize(msg)                  // err → NewActionError(ErrSerialize, "action=... proto=...")
    return body, nil
}
```

新：

```go
func (ae *ActionExecutor) buildBody(def *ActionDef) ([]byte, error) {
    body, _, err := BuildProtoBody(def.C2SProto, def.Bindings, ae.store, ae.factory, def.Name)
    if err != nil { return nil, err }
    return body, nil
}
```

行为对齐：
- `c2sProto==""` → `return nil, nil`（一致）。
- `factory.Create` err → `NewActionError(ErrCreateMsg, "action="+def.Name+" proto="+c2sProto, err)`（旧是 `"proto="+def.C2SProto`，**多了 `action=` 前缀**——这是小幅增强而非回归：旧 ErrCreateMsg detail 不含 action 名，新含；monitor 错误码相同，仅 detail 更丰富）。
- bindFields err → 透传（含 `"action="+def.Name+" field="+fb.Field`，actionName 仍传 `def.Name`，**完全一致**）。
- `factory.Serialize` err → `NewActionError(ErrSerialize, "action="+def.Name+" proto="+c2sProto, err)`（旧是 `"action="+def.Name+" proto="+def.C2SProto`，**完全一致**）。

回归测试 `TestBuildBody_RefactoredViaBuildProtoBody`：用真实 factory + simple bindings 走 `ae.buildBody(def)`，
Parse 回来验证 seq/token 字段值正确，证明重构路径与原一致。
`TestActionExecutorBuildBodyWithMapBinding`（既有 map binding 集成测试）继续 PASS——这是 buildBody 最复杂的 binding 场景（map entry），重构后行为零变化。

## 双模式分派（robot/robot.go netSenderAdapter.RegisterHeartbeat）

Go builder 闭包按 cfg 模式分派（与 execHeartbeat 互斥校验一致）：

```go
goBuilder := func() []byte {
    var body []byte
    var skip bool
    if c2sProto != "" {
        // proto 模式：BuildProtoBody（Go-only，不持 robot luaMu）
        b, skipB, err := engine.BuildProtoBody(c2sProto, bindings, st, factory, "heartbeat:"+cfg.Service)
        if err != nil { Warn; return nil }
        body, skip = b, skipB
    } else if len(fields) > 0 {
        // raw-binary 模式：BuildHeartbeatBody（现状，零改动）
        b, skipB, err := engine.BuildHeartbeatBody(fields, st, privateCounters, skipWhenMissing)
        if err != nil { Warn; return nil }
        body, skip = b, skipB
    }
    // else: 空 body（静态心跳，body=nil）
    if skip { return nil }
    key := conn.GetSecretKey()
    packet := adp.EncodeTCP/UDP(route, body, key)
    // 仅 raw 模式递增私有计数器（proto 模式无 counter 概念）
    for i := range fields { if fields[i].Source == HeartbeatSourceCounter { privateCounters[i] += step } }
    return packet
}
```

闭包多捕获：`bindings`（防御性拷贝）/ `c2sProto` / `factory`（ns.robot.factory）。其余捕获不变。
**proto 模式不持 robot luaMu**：BuildProtoBody Go-only + encode 走 adapter 池（ns.robot.adp），与 raw 模式一致。

## 互斥校验（execHeartbeat，不写兼容兜底）

```go
if def.C2SProto != "" && len(def.HeartbeatFields) > 0 {
    return NewActionError(errcode.ErrHeartbeatConfig,
        fmt.Sprintf("心跳 %s 同时配置 c2sProto 与 heartbeatFields，须二选一（双模式互斥）service=%s",
            def.Name, def.Service))
}
```

校验顺序：IntervalMs<=0 → **互斥** → Route 缺失 → 装配。互斥优先于 Route 校验（双模式冲突是最早能检出，且无歧义）。
测试 `TestExecute_TCPHeartbeat_ProtoAndFieldsMutuallyExclusive`：c2sProto + heartbeatFields 同时配 → ErrHeartbeatConfig + 不调 RegisterHeartbeat。

## TDD RED / GREEN

### RED
- 先写全 `engine/heartbeat_proto_test.go`（7 用例）。
- `go vet ./engine/...` 报 `undefined: BuildProtoBody`（行 65 调用），符合 RED。

### GREEN
- 实现 `engine/action.go`（BuildProtoBody + buildBody 重构 + execHeartbeat 互斥）→ `engine/heartbeat.go`（HeartbeatActionConfig 加字段）→ `robot/robot.go`（闭包分派）。
- 全部测试转绿。
- 修正一处测试断言错误：`TestBuildProtoBody_NoBindings` 原假设 proto3 全默认值消息序列化非空，实际 proto3 全默认 → 空 body（合法），修正为「Parse 无 err 即证明 body 合法」。

## 验证

| 命令 | 结果 |
|---|---|
| `go build ./...` | 干净，无输出 |
| `go vet ./...` | 干净，无输出 |
| `go test ./... -count=1` | 全绿（adapter/admin/cmd-agent/codec/engine/network/protox/robot/script/sharedstate/state 11 包 ok） |
| `go test ./engine/... ./robot/... ./network/... ./script/... -count=1` | 全绿 |
| `go test ./engine/... -run "BuildProtoBody\|BuildBody_Refactored\|TCPHeartbeat\|UDPHeartbeat\|Heartbeat" -v -count=1` | 29 测试全 PASS（7 新 proto + 22 既有 raw-binary/装配） |
| `sed 's/\r$//' f.go \| gofmt -l`（action.go / heartbeat.go / heartbeat_proto_test.go / robot.go） | 全 clean（CRLF 剥除后 canonical） |
| `-race` | **未跑**：本机 CGO_ENABLED=0（Windows 无 cgo）。并发安全论证见 Concerns |

## Self-review

- [x] `BuildProtoBody` 抽出（复用 bindFields，不新造 binding 解析）；`buildBody` 重构后 tcpSend/request 行为零变化（map binding 集成测试 + 重构无回归测试双证）。
- [x] proto 模式心跳可用（c2sProto+bindings → factory.Create → bindFields → factory.Serialize → adapter encode）；raw-binary 模式零变化；空 body 零变化。
- [x] c2sProto 与 heartbeatFields 互斥校验就位（ErrHeartbeatConfig，中文，不写兼容兜底，校验早于 Route 检查）。
- [x] Go builder 闭包按模式分派；proto 模式不持 robot luaMu（Go-only BuildProtoBody + adapter 池 encode）。
- [x] HeartbeatActionConfig 加 C2SProto/Bindings 字段，godoc 标注双模式互斥语义；ActionDef 复用既有 C2SProto/Bindings 字段（flow.go 未改）。
- [x] 新字段全链路一致：ActionDef.C2SProto/Bindings → HeartbeatActionConfig.C2SProto/Bindings → BuildProtoBody（测试 `TestExecute_TCPHeartbeat_PassesProtoBindings` 验证透传）。
- [x] 日志/错误中文；godoc 齐全（BuildProtoBody/execHeartbeat/RegisterHeartbeat/HeartbeatActionConfig）。
- [x] 不动 raw-binary 路径（BuildHeartbeatBody/heartbeatFields 零改动）、不动静态心跳迁移、不删 Lua register_*_heartbeat、不改旧 RegisterTCPHeartbeat 签名、不动 network/heartbeat.go/connectionPump/luaMu、不动前端/admin/agent/cmd。

## Concerns

1. **`BuildProtoBody` 签名加第 5 参 `actionName`**（偏离 brief 的 4 参）：唯一目的保留 buildBody 重构后 bindFields 错误上下文（旧传 `def.Name`）。这是「buildBody 行为零变化」不变量的最小扩展。备选方案：4 参 + buildBody 包装错误重写 ActionError.Detail（需 reflect/类型断言改公共字段，脆弱）。当前选 5 参更直接、更安全。如严格遵循 brief 4 参，可改为「BuildProtoBody 用 c2sProto 作 actionName、buildBody 重写错误前缀」，但牺牲简洁性。建议保留 5 参，作为 brief 设计意图的合理工程偏离。

2. **ErrCreateMsg detail 微调**：旧 `buildBody` 的 Create 失败 detail 是 `"proto="+c2sProto`（不含 action）；新 BuildProtoBody 是 `"action="+actionName+" proto="+c2sProto`。错误码（ErrCreateMsg）与 Kind（framework）不变，仅 detail 多了 action 上下文。这是**小幅增强**而非回归（错误信息更可定位）。如需绝对 detail 一致，可让 buildBody 对 ErrCreateMsg 包装去掉 action 前缀——但同样脆弱，不推荐。

3. **proto 模式无私有计数器概念**：raw 模式的 `privateCounters` 在 proto 模式下空 map（cfg.Fields 为空），闭包末尾的 counter 递增循环对 proto 模式是 no-op（`for i := range fields` 空 fields）。无副作用、无泄漏。proto 模式若需共享计数器（如包序号），用 `stateCounter` source 经 bindings 表达（复用 buildBody 的 BindState 类型？否——stateCounter 是 HeartbeatField 源，不在 FieldBind 体系）。proto 模式的「计数器」语义走 state（state.Set + BindState），由用户在 bindings 里用 state: 引用即可，不需新源。

4. **`-race` 未跑**：本机 CGO_ENABLED=0（Windows 无 cgo）。并发安全沿用结构性论证：goBuilder 由 network.runHeartbeat 单 goroutine 串行调用（per-connection 单心跳）；BuildProtoBody 读 state.Store（RWMutex）+ factory（只读 Registry，无写）；bindings 在闭包构造时防御性拷贝（避免共享 cfg 切片头）；proto 模式与 raw 模式共享同一 state.Store，但 goBuilder 串行调用无锁序交叉。CI 启用 cgo 后建议补跑 `go test -race ./engine/... ./robot/...`。

5. **proto 模式 bindFields 用临时 ActionExecutor**：BuildProtoBody 内 `ae := &ActionExecutor{store, factory}` 复用 bindFields/resolveFieldValueStrict 解析路径。临时 ae 的 netSender/adp/timingLevel 零值——bindFields 不依赖这些字段（只读 store/factory），故安全。如未来 bindFields 扩展依赖 netSender，需复核。

## 2-B.2 接力

动态心跳迁移（②③）现可选**两种**模式（双模式扩展的核心收益）：
- **proto 模式**：若 `build_battle_tcp_heart`/`build_udp_heart` 实际是 proto 消息（部分游戏战斗服心跳是 proto），用 c2sProto + bindings 表达，每 tick factory 构造，无需 raw-LE 打包。
- **raw-binary 模式**（2-B.1 已证覆盖）：`BuildHeartbeatBody` 6 源 + 组合布局字节对拍用例已证明覆盖 `build_battle_tcp_heart`(19B) / `build_udp_heart`(39B) 全部字段语义。

2-B.2 决策依据：检查 `build_battle_tcp_heart`/`build_udp_heart` 的 Lua 源（connect_battle_tcp.lua / connect_battle_udp.lua），若调用了 proto.serialize / EncodeTCP 带 proto → proto 模式；若是 `string.char` + LE 拼接 → raw-binary 模式。本任务双模式都已就绪，2-B.2 按实际选其一迁移即可。

删 Lua 路径（2-B.2）：`script/api_network.go` 的 `registerHeartbeat`/`register_tcp_heartbeat`/`register_udp_heartbeat`、`NetSender.RegisterTCPHeartbeat/UDPHeartbeat` 接口及 `netSenderAdapter` 实现、connect_battle_*.lua 的 builder/register 调用、`connect_logic.lua` 残留（2-B.1 已迁静态心跳，Lua 端 register 已删）。
