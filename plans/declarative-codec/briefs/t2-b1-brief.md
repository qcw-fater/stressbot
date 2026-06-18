# T2-B.1 Brief — 声明式二进制心跳框架 + 静态心跳迁移（Go-only builder，非破坏新增）

> 你是 implementer。先读本 brief。参考 `plans/declarative-codec/02-track-backend-integration.md` §2-B（实施切片 1/2/3/4）、`plans/declarative-codec/reports/t2-a2-1-report.md`（RegisterListen/ActionHandler 模式）、`plans/declarative-codec/progress-ledger.md` §「全局约束」+ §「gofmt/换行 环境注」。
> 工作目录：worktree 根 `D:\Gitee\stressbot\.claude\worktrees\declarative-codec`。**不要 git commit。**

## 目标

落地**声明式二进制心跳**框架：新增 `tcpHeartbeat`/`udpHeartbeat` action pattern + 一个 **Go-only 二进制布局打包器**（`BuildHeartbeatBody`，支持 `fixed/state/stateCounter/counter/timestamp/randomInt` 源 + 小端打包），让心跳包构造**完全在 Go 内完成**（读线程安全 state、私有计数器、时间、随机；不触碰业务 LState）。并用它迁移**静态 logic TCP 心跳**（本就 Lua-free）作为端到端验证。

**用户已定方案（Option 1）**：raw-binary 声明式 binding（游戏包格式留配置，不进 Go 代码）。

## 关键背景（已读码核对）

- **3 个心跳**：①logic TCP 静态（`connect_logic.lua:34` `register_tcp_heartbeat("logic",5000,{cmd=2,act=1})`，空 body 预编码，运行时零 Lua）— **本任务迁移**；②battle UDP 动态（`connect_battle_udp.lua:81` 150ms `build_udp_heart`）；③battle TCP 动态（`connect_battle_tcp.lua:71` 10s `build_battle_tcp_heart`）。②③是 robot-luaMu 触碰点，**本任务不动**（保留 Lua `register_*_heartbeat` 路径，2-B.2 迁移）。
- **心跳运行时与 builder 构造解耦**：`network/heartbeat.go` 的 `runHeartbeat` 只调 `hb.cfg.Builder() []byte` → `c.Send(packet)`，**不关心** builder 怎么构造。`Connection.RegisterHeartbeat(HeartbeatConfig{Interval, Builder})` 签名稳定。故声明式路径只需构造一个 Go builder 闭包塞进 `HeartbeatConfig.Builder`，**无需改 network 层、无需改 `NetSender.RegisterTCPHeartbeat` 签名**。
- **当前 Lua 路径**（`script/api_network.go:1121 registerHeartbeat`）：静态预编码（`EncodeTCPLocked(route,nil,key)` 缓存）；动态 TryLock(luaMu)+CallByParam(builder_fn)+EncodeLocked。本任务**保留**这条路径（②③仍用）。
- **动态 builder 字段语义**（2-B.2 迁移要用，本任务只设计打包器要能覆盖）：`build_udp_heart`(39B)=u16 packageIndex(共享自增)/i64 battleId(state)/u8 fighterIndex(state)/i64 session(state)/i32 ack(state:battleAck)/u16 rtt(randomInt 10-40)/u64 now_ms(timestamp)/u32 udp_seq(私有 counter)/u16 0(fixed)/u8 0/u8 0；`build_battle_tcp_heart`(19B)=u16 packageIndex(共享自增)/i64 battleId/u8 fighterIndex/i64 session。打包器源类型须覆盖：fixed/state/stateCounter(共享自增)/counter(私有)/timestamp/randomInt。
- **engine 包无后端 flow 校验**（2-A2.1 已证）：心跳字段校验落在 ActionExecutor 执行点 / 打包器入口，不在不存在的 flow 校验阶段。
- **`utils.pack_le` 是 Lua**（script 包）；Go 侧无等价 raw-LE 打包器（protox 是 protobuf，非 raw LE）。本任务在 engine 内写一个小的 LE 打包器。

## 范围（严格边界）

**做：**
- `engine`：`HeartbeatField`/`HeartbeatActionConfig` 类型 + `BuildHeartbeatBody`（Go-only 打包器：6 源 + LE 打包 + 类型宽度掩码）+ LE 打包 helper + 校验。
- `engine`：`PatternTCPHeartbeat`/`PatternUDPHeartbeat` 常量 + `ActionDef` 心跳字段（`HeartbeatFields []HeartbeatField`、复用 `Service`/`Route`、`IntervalMs`、`SkipWhenMissing`）+ `ActionExecutor.Execute` switch 增加 tcpHeartbeat/udpHeartbeat 分支。
- `engine`：`NetSender` 新增 `RegisterHeartbeat(cfg HeartbeatActionConfig) error` 方法（**新增，不改**旧 `RegisterTCPHeartbeat`/`RegisterUDPHeartbeat`）。
- `robot`：`netSenderAdapter.RegisterHeartbeat(cfg)` —— 构造 Go builder 闭包（`BuildHeartbeatBody` + 私有计数器 + 成功后递增 + adapter encode）→ `conn.RegisterHeartbeat(network.HeartbeatConfig{Interval, goBuilder})`。
- 迁移静态 logic TCP 心跳：`conf/scripts/connect_logic.lua` 删 `register_tcp_heartbeat`；`conf/flow/flow.json` 在 ConnectLogicTCP 后加 `tcpHeartbeat` action 节点（service=logic, route{cmd=2,act=1}, intervalMs=5000, 空 heartbeatFields）。
- TDD（打包器 + 校验 + ActionExecutor 分支）。

**不做（明确推迟）：**
- ❌ 不迁移 ②③动态心跳（UDP 150ms / TCP 10s）—— 2-B.2。
- ❌ 不删 Lua `register_*_heartbeat`/`registerHeartbeat` 路径、不改 `NetSender.RegisterTCPHeartbeat/UDPHeartbeat` 签名（②③仍用）—— 2-B.2。
- ❌ 不动 `network/heartbeat.go`（运行时与 builder 构造解耦，无需改）。
- ❌ 不动 connectionPump/decodeLoop/listenLoop、不动 luaMu/withReleasedMu（→ 2-C3/2-D）。
- ❌ 不动前端、admin/agent/cmd。
- ❌ 不碰密钥 API、不碰 `RobotAdapter`/codec（→ 2-C）。

## 设计

### 1. 二进制布局类型（engine/heartbeat.go 新文件）

```go
// HeartbeatField 心跳二进制包的一个字段（声明式 raw-LE 布局）。
type HeartbeatField struct {
    Type   string `json:"type"`             // u8/u16/u32/u64/i8/i16/i32/i64（小端）
    Source string `json:"source"`           // fixed/state/stateCounter/counter/timestamp/randomInt
    Value  *int64 `json:"value,omitempty"`  // source=fixed
    Key    string `json:"key,omitempty"`    // source=state|stateCounter（state 键名）
    Min    *int64 `json:"min,omitempty"`    // source=randomInt（含）
    Max    *int64 `json:"max,omitempty"`    // source=randomInt（含）
    Start  *int64 `json:"start,omitempty"`  // source=counter（私有计数器初值，缺省 0）
    Step   *int64 `json:"step,omitempty"`   // source=counter（步长，缺省 1）
    Unit   string `json:"unit,omitempty"`   // source=timestamp（"ms"|"s"，缺省 ms）
}

// HeartbeatActionConfig 声明式心跳配置（tcpHeartbeat/udpHeartbeat action）。
type HeartbeatActionConfig struct {
    Transport      string            // "tcp"|"udp"（由 pattern 决定）
    Service        string
    IntervalMs     int
    Route          any               // {cmd, act}（与 ActionDef.Route 同构）
    Fields         []HeartbeatField  // 二进制布局；空 = 空 body（静态心跳）
    SkipWhenMissing bool             // state 源缺失时跳过本 tick（true）而非报错
}
```

### 2. `BuildHeartbeatBody`（Go-only 打包器，engine/heartbeat.go）

```go
// BuildHeartbeatBody 按声明式布局构造心跳 body（Go-only，零 Lua）。
//   state: 线程安全 state.Store（读 state/stateCounter 源）。
//   privateCounters: 调用方持有的「私有计数器」当前值映射（key = 字段在 Fields 的下标）；
//     仅读取当前值参与打包；递增时机由调用方在「构建成功后」执行（见 netSenderAdapter）。
// 返回：(body, skip, err)。skip=true 表示本 tick 因 SkipWhenMissing 命中跳过（非错误）。
func BuildHeartbeatBody(fields []HeartbeatField, state *state.Store, privateCounters map[int]int64) (body []byte, skip bool, err error)
```

逐源解析（值统一用 int64 中转，打包时按 Type 掩码+LE）：
- `fixed`：`*Value`（nil → err「fixed 源缺 value」）。
- `state`：`state.Get(Key)`；缺失 → `SkipWhenMissing` 则 `skip=true` 返回，否则 err。值转 int64（按 state 存储类型：int/int64/float/uint）。
- `stateCounter`：`state.Increment(Key)` 返回的新值（**共享计数器自增**，对应 packageIndex）→ int64。这是唯一的 state 写副作用（自增），合法因为心跳语义本就推进包序号。
- `counter`：`privateCounters[idx]`（私有计数器当前值）；初值由 Start（缺省 0）。打包用当前值，**递增由调用方在成功后做**。
- `timestamp`：`Unit=="s"` → `time.Now().Unix()`；否则 `time.Now().UnixMilli()`。**用 time 包，不走 Lua utils.time_ms**。
- `randomInt`：`Min`/`Max` 必填（含）；`Min + rand.Int63n(Max-Min+1)`（`crypto/rand` 或 `math/rand`均可，选 math/rand 加种子一次即可——心跳 rtt 是模拟值不需密码学随机）。

LE 打包 + 类型宽度掩码（**关键：掩码等价 Lua 的 %回绕**）：
- u8/i8 → 1 字节；u16/i16 → `&0xFFFF` → 2 字节 LE；u32/i32 → `&0xFFFFFFFF` → 4 字节 LE；u64/i64 → 8 字节 LE。
- 有符号（iN）：先转无符号位模式（`uintN(int64Value)` 截断），再 LE 写。即 `binary.LittleEndian.PutUintN(buf, uintN(v))`。
- 未知 Type → err（中文）。
- 实现：一个小 helper `appendLE(buf, type string, v int64) ([]byte, error)`，用 `encoding/binary`。不要 import 任何 Lua。

### 3. ActionDef + ActionExecutor 接线（engine/flow.go, engine/action.go）

- `flow.go` 加常量 `PatternTCPHeartbeat = "tcpHeartbeat"` / `PatternUDPHeartbeat = "udpHeartbeat"`，godoc 说明。
- `ActionDef` 已有 `Service`/`Route`；新增字段：
  ```go
  IntervalMs      int              `json:"intervalMs,omitempty"`
  HeartbeatFields []HeartbeatField `json:"heartbeatFields,omitempty"`
  SkipWhenMissing bool             `json:"skipWhenMissing,omitempty"`
  ```
  （复用 `Service`/`Route`，不重复定义。）
- `ActionExecutor.Execute` switch 增加 `PatternTCPHeartbeat`/`PatternUDPHeartbeat` 分支：构造 `HeartbeatActionConfig{Transport, Service:def.Service, IntervalMs:def.IntervalMs, Route:def.Route, Fields:def.HeartbeatFields, SkipWhenMissing:def.SkipWhenMissing}` → `ae.netSender.RegisterHeartbeat(cfg)`。返回 nil（注册动作本身不计入该 action 的网络延迟样本，不等待发送）。
- `IntervalMs<=0` → 返回 `NewActionError(errcode.ErrUnknownPattern-or-config, ...)`（配置类错误，中文；若无合适码用 `ErrAddrEmpty` 所在的配置段码——**若需新框架码先在 errcode/codes.go 注册**，参考全局约束）。
- `Route` 缺失 → 配置错误。

### 4. `NetSender.RegisterHeartbeat`（engine/action.go 接口 + robot 实现）

```go
// engine/action.go NetSender 接口新增（不改旧 RegisterTCPHeartbeat/UDPHeartbeat）：
RegisterHeartbeat(cfg HeartbeatActionConfig) error
```

`robot/netSenderAdapter.RegisterHeartbeat(cfg)`（robot.go）：
- 取 conn（按 cfg.Transport + cfg.Service：tcp→GetTCPConn，udp→GetUDPConn）；conn==nil → 配置/连接错误（中文，记 Error 或返回 error）。
- 构造 **Go builder 闭包**：
  - 持有：`cfg.Fields`、`ns.robot.state`、`privateCounters map[int]int64`（按 Fields 下标初始化为 Start/0）、`ns.robot.adp`、`cfg.Route`、`cfg.Transport`、`cfg.SkipWhenMissing`、`ns.robot.client`（取 secretKey）。
  - 每次调用（每 tick）：`body, skip, err := engine.BuildHeartbeatBody(cfg.Fields, state, privateCounters)`；`skip`→return nil；`err`→Warn 日志+return nil；成功 → **递增 privateCounters**（每个 counter 源字段 `+=Step`）→ 取 conn 当前 secretKey → `adp.EncodeTCP/UDP(cfg.Route, body, key)` → 返回 packet。
  - ctx 取消 → return nil（与现有 builder 一致）。
- `conn.RegisterHeartbeat(network.HeartbeatConfig{Interval: time.Duration(cfg.IntervalMs)*time.Millisecond, Builder: goBuilder})`。
- 返回 nil（或 conn==nil 的 error）。

> 注：`adp.EncodeTCP/UDP` 用 adapter 自身 LState 池（非 robot luaMu），故该 builder 闭包**不触碰 robot luaMu**——这正是 2-B 消除心跳 robot-luaMu 依赖的关键。Encode 在 2-C 后变纯 Go，本任务不依赖。

### 5. 静态 logic TCP 心跳迁移

- `conf/scripts/connect_logic.lua:34`：删 `local hbCode = network.register_tcp_heartbeat("logic", 5000, {cmd=2, act=1})` 及其错误处理（hbCode 检查）。保留 connect_logic.lua 其余逻辑（连接+密钥）。
- `conf/flow/flow.json`：在 ConnectLogicTCP 节点之后（或其 sequence 内合适位置）加一个 `tcpHeartbeat` action 节点：
  ```json
  {
    "type": "action",
    "action": "RegisterLogicHeartbeat"
  }
  ```
  并在 actions 表加：
  ```json
  "RegisterLogicHeartbeat": {
    "pattern": "tcpHeartbeat",
    "service": "logic",
    "route": {"cmd": 2, "act": 1},
    "intervalMs": 5000
  }
  ```
  （无 `heartbeatFields` = 空 body，等价旧静态心跳。）

## 不变量 / 语义保持

- 静态 logic TCP 心跳迁移后行为等价：空 body + route{2,1} + 5s 间隔 + adapter 编码。旧=注册时预编码缓存；新=每 tick 空 body + encode。**细微差异**：新路径每 tick 调一次 adapter.EncodeTCP（旧路径缓存只 encode 一次）。encode 开销小（adapter 池），且 5s 一次频率极低，可忽略。报告需指出此差异。
- 动态心跳 ②③ 走旧 Lua 路径，**行为完全不变**。
- 心跳 goroutine 经新路径时**不持 robot luaMu**（Go builder；encode 走 adapter 池）。

## 关键约束

- **新字段全链路一致**：HeartbeatFields 从 flow.json → ActionDef → HeartbeatActionConfig → BuildHeartbeatBody，一处不漏。
- **不写兼容兜底**：未知 Type/Source、fixed 缺 value、randomInt 缺 min/max → 直接 err（中文），不静默默认。
- **避免双 API 长期并存**：新 `RegisterHeartbeat` 与旧 `RegisterTCPHeartbeat` 并存是 2-B.1→2-B.2 的**临时过渡**（旧路径 2-B.2 删）；报告注明。
- **Go-only**：BuildHeartbeatBody 不 import gopher-lua、不调 Lua；时间用 `time`、随机用 `math/rand`。
- **框架/业务分离**：打包器是通用 raw-LE 布局（无游戏概念）；游戏包格式（battleId/39B 布局）只在 flow.json 配置，**不进 Go 代码**。2-B.1 只迁静态心跳（空 body），不写任何游戏字段。
- 日志/错误中文；godoc；`go build ./...` + `go vet` 通过。
- **Windows 环境注**：`gofmt -l` 标工作树 .go 脏是 autocrlf CRLF，**不要**对单文件 `gofmt -w`；校验 canonical 用 `sed 's/\r$//' f.go | gofmt -l`。
- **不要 git commit。**

## 工作方式（TDD）

1. **先读** `engine/action.go`（ActionExecutor.Execute switch、NetSender 接口、ActionDef）、`engine/flow.go`（pattern 常量、ActionDef）、`robot/robot.go`（netSenderAdapter.RegisterTCPHeartbeat 现状 + adp/state 访问）、`network/heartbeat.go`（确认无需改）、`conf/scripts/connect_logic.lua`、`conf/flow/flow.json` ConnectLogicTCP 节点位置。
2. **RED — `engine/heartbeat_test.go`（新）**：
   - `appendLE` / 类型掩码：u16 负数/大数掩码、i32 位模式、u64、未知 type err。
   - `BuildHeartbeatBody` 逐源：fixed（缺 value err）、state（命中/缺失+SkipWhenMissing→skip / 缺失无 skip→err）、stateCounter（自增返回新值）、counter（读当前值）、timestamp（ms/s）、randomInt（范围内、缺 min/max err）。
   - 组合布局：构造一个类 build_battle_tcp_heart 的 4 字段布局（u16 stateCounter/i64 state/u8 state/i64 state），断言字节序与手算一致（**证明打包器能覆盖 2-B.2 的动态心跳**）。
3. **RED — ActionExecutor/校验**：intervalMs<=0 err、route 缺 err、tcpHeartbeat 分支调 netSender.RegisterHeartbeat（用 fake NetSender 记录调用）。
4. **GREEN**：实现 heartbeat.go + flow.go 常量 + action.go ActionDef 字段 + Execute 分支 + NetSender.RegisterHeartbeat + netSenderAdapter.RegisterHeartbeat + 迁移 connect_logic.lua/flow.json。
5. `go build ./...`、`go vet ./...`、`go test ./engine/... ./robot/... ./network/... ./script/... -count=1` 全绿。
6. **静态心跳迁移校验**：确认 flow.json 新增节点 JSON 合法（可选：若有 flow 加载测试，跑之）。
7. **不要 git commit。**

## 验收（self-review）

- `HeartbeatField`/`HeartbeatActionConfig` + `BuildHeartbeatBody`（6 源 + LE 打包 + 类型掩码）齐全；零 Lua import；游戏概念零进 Go。
- `PatternTCPHeartbeat/UDPHeartbeat` + ActionDef 字段 + Execute 分支 + `NetSender.RegisterHeartbeat` 新增（旧 RegisterTCPHeartbeat 不动）。
- `netSenderAdapter.RegisterHeartbeat` 构造 Go builder 闭包（BuildHeartbeatBody + 私有计数器成功后递增 + adapter encode + conn.RegisterHeartbeat）。
- 静态 logic TCP 心跳迁移到 `RegisterLogicHeartbeat` tcpHeartbeat action；connect_logic.lua 删 register 调用。
- 动态心跳 ②③ 旧 Lua 路径**未动**、行为不变。
- go build/vet/test 全绿；打包器组合用例证明能覆盖 2-B.2 动态布局。

## 报告

写完整报告到 `plans/declarative-codec/reports/t2-b1-report.md`：实现内容、BuildHeartbeatBody 6 源语义 + LE 掩码、ActionExecutor 接线、netSenderAdapter Go builder 闭包（encode 走 adapter 池不持 robot luaMu）、静态心跳迁移 + 与旧预编码的细微差异、TDD RED/GREEN、改动文件、self-review、concerns（含「RegisterHeartbeat 与旧 RegisterTCPHeartbeat 临时并存，2-B.2 删」）、**2-B.2 接力**（动态心跳迁移用本打包器 + 删 Lua 路径）。

返回（<15 行）：Status、改动文件、一行测试摘要、concerns、报告路径。
