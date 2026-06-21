# T2-B.1 Proto 模式扩展 Brief — 心跳双模式：补 proto（c2sProto+bindings）分支

> 你是 implementer。先读本 brief。**前置必读**：`plans/declarative-codec/reports/t2-b1-report.md`（2-B.1 已落地的 heartbeatFields raw-binary 框架）、`plans/declarative-codec/00-master.md` §T2-B（已更新为**双模式**设计）、`plans/declarative-codec/progress-ledger.md` §「全局约束」+ §「gofmt/换行 环境注」。
> 工作目录：worktree 根 `D:\Gitee\stressbot\.claude\worktrees\declarative-codec`。**不要 git commit。**

## 目标

把已落地的 2-B.1 心跳框架（仅 raw-binary `heartbeatFields` 模式）**扩展为双模式**：补 **proto 模式**（`c2sProto` + `bindings`），复用现有 `tcpSend` 的 proto 构建机制。这样心跳框架覆盖通用游戏服的三类主流形态（proto 心跳 = 多数 protobuf 服；raw-binary 心跳 = 自研协议服/战斗同步；空 body = 轻量心跳）。

**为何扩展**（用户决策）：stressbot 是**通用**压测工具，不止一个游戏。大多数游戏服心跳是 proto 消息（`HeartbeatC2S`/`PingC2S`），仅 raw-binary 覆盖不了它们。proto 模式是 plan 原方案，raw-binary 是本项目现实的必要扩展——双模式两者都要。

## 现状（2-B.1 已落地，**保留不返工**）

- `engine/heartbeat.go`：`HeartbeatField` + `HeartbeatActionConfig` + `BuildHeartbeatBody`（raw-binary 模式，6 源 + LE 打包）。
- `engine/flow.go`：`PatternTCPHeartbeat`/`PatternUDPHeartbeat` + `ActionDef.HeartbeatFields`/`IntervalMs`/`SkipWhenMissing`。
- `engine/action.go`：`NetSender.RegisterHeartbeat(cfg)` + `execHeartbeat`（装配 HeartbeatActionConfig）。
- `robot/robot.go`：`netSenderAdapter.RegisterHeartbeat` 构造 Go builder 闭包（目前只走 BuildHeartbeatBody）。
- 静态 logic TCP 心跳已迁到 `RegisterLogicHeartbeat`（空 body）。
- **本任务只补 proto 模式，不动 raw-binary 路径、不动静态心跳迁移。**

## 范围（严格边界）

**做：**
- `engine/action.go`：抽出 `buildBody(def)` 的 proto 构建核心为可复用函数 `BuildProtoBody(c2sProto, bindings, store, factory) (body []byte, skip bool, err error)`；`buildBody` 改为调用它（**不改变 tcpSend/request 现有行为**，纯重构）。
- `HeartbeatActionConfig` 加 `C2SProto string` + `Bindings []Binding` 字段（proto 模式入参）。
- `execHeartbeat`：**校验互斥**（`C2SProto != ""` 与 `len(HeartbeatFields) > 0` 不能同时成立，否则 `ErrHeartbeatConfig`）；把 `def.C2SProto`/`def.Bindings` 装进 config。
- `netSenderAdapter.RegisterHeartbeat` 的 Go builder 闭包**按模式分派**：
  - `C2SProto != ""` → `engine.BuildProtoBody(c2sProto, bindings, state, factory)`（proto 模式）。
  - `len(Fields) > 0` → `engine.BuildHeartbeatBody(fields, state, privateCounters, skipWhenMissing)`（raw-binary 模式，现状）。
  - 两者皆无 → 空 body（现状静态心跳）。
  - 随后统一 `adp.EncodeTCP/UDP(route, body, key)` → 返回 packet（现状不变）。
- TDD：proto 模式单测（构造 proto 心跳 body，对拍 factory 手算）；互斥校验；`buildBody` 重构不回归 tcpSend。

**不做：**
- ❌ 不动 raw-binary `BuildHeartbeatBody`/`heartbeatFields` 路径（已工作）。
- ❌ 不动静态 logic 心跳迁移、不迁动态心跳（→ 2-B.2）。
- ❌ 不删 Lua `register_*_heartbeat`、不改旧 `RegisterTCPHeartbeat` 签名（→ 2-B.2）。
- ❌ 不动 `network/heartbeat.go`、connectionPump、luaMu（→ 2-C3/2-D）。
- ❌ 不动前端/admin/agent/cmd。

## 设计要点

### 1. 抽出 `BuildProtoBody`（engine/action.go，纯重构 + 复用）

现状 `func (ae *ActionExecutor) buildBody(def *ActionDef) ([]byte, error)`（action.go:1037 调用）用 `ae.factory` + `ae.store` + `def.C2SProto` + `def.Bindings` 构造 proto body。抽出核心：

```go
// BuildProtoBody 按 c2sProto + bindings 构造 proto body（复用现有 binding 解析，Go-only）。
// 供 tcpSend/tcpRequest（经 buildBody）与心跳 proto 模式共享。
// store/factory 由调用方传入（ActionExecutor 传 ae.store/ae.factory；心跳闭包传 robot.state/robot.factory）。
// 返回 (body, skip, err)：skip=true 表示因 binding 缺失且语义为跳过（与心跳 SkipWhenMissing 对齐）；
//   buildBody 现有调用不期望 skip（保持 err 语义），故 buildBody 内 skip 当 false 处理（或按现有行为）。
func BuildProtoBody(c2sProto string, bindings []Binding, store *state.Store, factory *protox.Factory) (body []byte, skip bool, err error)
```

- `buildBody(def)` 改为 `return BuildProtoBody(def.C2SProto, def.Bindings, ae.store, ae.factory)`（忽略 skip 或按原行为；**确保 tcpSend/request 既有测试不回归**）。
- **不要新造 binding 解析**——复用 `buildBody` 现有的 bindFields 调用链，只是搬进 `BuildProtoBody`。
- 若 `buildBody` 当前对「无 c2sProto」有特殊处理（空 body / raw body），保留该语义：`BuildProtoBody` 在 `c2sProto==""` 时返回与原 `buildBody` 一致的结果（不破坏 tcpSend 无 proto 的场景）。

### 2. `HeartbeatActionConfig` 加 proto 字段

```go
type HeartbeatActionConfig struct {
    Transport       string  // "tcp"|"udp"
    Service         string
    IntervalMs      int
    Route           any
    // 双模式 body 构造（互斥）：
    C2SProto        string           // proto 模式：proto 全名
    Bindings        []Binding        // proto 模式：字段绑定（复用 tcpSend binding）
    Fields          []HeartbeatField // raw-binary 模式：LE 布局
    SkipWhenMissing bool
}
```

### 3. `execHeartbeat` 互斥校验 + 装配（engine/action.go）

```go
func (ae *ActionExecutor) execHeartbeat(transport string, def *ActionDef) error {
    if def.IntervalMs <= 0 { /* ErrHeartbeatConfig，现状 */ }
    // 互斥校验
    if def.C2SProto != "" && len(def.HeartbeatFields) > 0 {
        return NewActionError(errcode.ErrHeartbeatConfig,
            fmt.Sprintf("心跳 %s 同时配置 c2sProto 与 heartbeatFields，须二选一", def.Name))
    }
    if def.Route == nil { /* ErrHeartbeatConfig，现状 */ }
    cfg := HeartbeatActionConfig{
        Transport: transport, Service: def.Service, IntervalMs: def.IntervalMs,
        Route: def.Route, C2SProto: def.C2SProto, Bindings: def.Bindings,
        Fields: def.HeartbeatFields, SkipWhenMissing: def.SkipWhenMissing,
    }
    return ae.netSender.RegisterHeartbeat(cfg)
}
```

### 4. Go builder 闭包按模式分派（robot/robot.go netSenderAdapter.RegisterHeartbeat）

现有闭包只调 `BuildHeartbeatBody`。改为：

```go
goBuilder := func() []byte {
    var body []byte
    var skip bool
    if c2sProto != "" {
        body, skip, err = engine.BuildProtoBody(c2sProto, bindings, st, factory)
    } else if len(fields) > 0 {
        body, skip, err = engine.BuildHeartbeatBody(fields, st, privateCounters, skipWhenMissing)
        // 成功后递增私有计数器（raw 模式现有逻辑，保留）
    } else {
        body = nil // 空 body
    }
    if err != nil { log.Warn; return nil }
    if skip { return nil }
    // raw 模式递增 privateCounters（仅 raw 模式有 counter 源，保留现有逻辑）
    key := conn.GetSecretKey()
    ... adp.EncodeTCP/UDP(route, body, key) ...
}
```

- 闭包需多捕获 `c2sProto`、`bindings`、`factory`（`ns.robot.factory`）。`state`/`adp`/`route`/`conn` 现有已捕获。
- proto 模式无私有计数器概念，递增逻辑只对 raw 模式生效（保留现有）。

## 不变量

- raw-binary 模式（`heartbeatFields`）行为**零变化**（2-B.1 已验证）。
- 静态 logic 心跳（空 body）**零变化**。
- `tcpSend`/`tcpRequest` 的 `buildBody` 重构后**行为零变化**（既有测试全绿）。
- proto 模式心跳：每 tick Go-only 构造 proto body（factory+bindings）→ adapter 编码（adapter 池，非 robot luaMu）→ **不持 robot luaMu**。
- 双模式互斥：同一心跳不能同时配 c2sProto + heartbeatFields。

## 关键约束

- **复用，不新造**：proto 模式必须复用现有 `buildBody`/bindFields 的 binding 解析，**不要**为心跳新写一套 proto 字段解析。
- **不写兼容兜底**：c2sProto+heartbeatFields 同时配 → 直接 err（互斥）。
- **不返工 raw 模式**：BuildHeartbeatBody/heartbeatFields 路径不动。
- Go-only：BuildProtoBody 不碰 Lua。
- 日志/错误中文；godoc；`go build ./...` + `go vet` 通过。
- **Windows 环境注**：`gofmt -l` 标 .go 脏是 autocrlf CRLF，**不要**对单文件 `gofmt -w`；校验 canonical 用 `sed 's/\r$//' f.go | gofmt -l`。
- **不要 git commit。**

## 工作方式（TDD）

1. 先读 `engine/action.go` `buildBody`（找定义，理解 factory+bindings+store 构造 proto body 的完整流程）、`execHeartbeat`（现状）、`robot/robot.go` `netSenderAdapter.RegisterHeartbeat`（现状闭包）、`engine/heartbeat.go`（BuildHeartbeatBody/HeartbeatActionConfig 现状）。
2. RED：
   - `engine/action_test.go`（或 heartbeat_test.go）：`BuildProtoBody` 用真实 factory + 一个简单 c2sProto + bindings 构造 body，对拍手算字节（证明 proto 模式可用）。
   - 互斥校验：`execHeartbeat` 对 c2sProto+heartbeatFields 同时配返回 ErrHeartbeatConfig。
   - 回归：`buildBody` 重构后，现有 tcpSend/request 相关测试全绿（若有；若无，至少 buildBody 对 c2sProto+bindings 的既有用例不回归）。
3. GREEN：抽 BuildProtoBody + buildBody 重构 + HeartbeatActionConfig 加字段 + execHeartbeat 互斥校验 + 闭包分派。
4. `go build ./...`、`go vet ./...`、`go test ./engine/... ./robot/... ./network/... ./script/... -count=1` 全绿。
5. **不要 git commit。**

## 验收（self-review）

- `BuildProtoBody` 抽出；`buildBody` 重构后 tcpSend/request 行为零变化（既有测试绿）。
- proto 模式心跳可用（c2sProto+bindings → factory 构造 → adapter 编码）；raw-binary 模式零变化；空 body 零变化。
- c2sProto 与 heartbeatFields 互斥校验就位（ErrHeartbeatConfig，中文）。
- Go builder 闭包按模式分派；proto 模式不持 robot luaMu。
- go build/vet/test 全绿。

## 报告

写完整报告到 `plans/declarative-codec/reports/t2-b1-proto-report.md`：实现内容、BuildProtoBody 抽取（复用 bindFields）、双模式分派、互斥校验、buildBody 重构无回归证明、TDD RED/GREEN、改动文件、self-review、concerns（如 buildBody skip 语义处理）、**2-B.2 接力**（动态心跳用 raw 模式 heartbeatFields 迁移 + 删 Lua 路径）。

返回（<15 行）：Status、改动文件、一行测试摘要、concerns、报告路径。
