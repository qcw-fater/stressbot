# T2-B.1 报告 — 声明式二进制心跳框架 + 静态心跳迁移（Go-only builder，非破坏）

> 任务 brief：`plans/declarative-codec/briefs/t2-b1-brief.md`
> 工作目录：worktree 根 `D:\Gitee\stressbot\.claude\worktrees\declarative-codec`，分支 `worktree-declarative-codec`。
> **未 git commit / add**（遵 brief 硬约束）。

## Status

**DONE**。TDD RED→GREEN，`go build ./...` + `go vet ./...` 干净，全仓 `go test ./...` 全绿（11 个 ok 包）。
声明式二进制心跳框架落地：Go-only raw-LE 打包器（6 源 + 类型宽度掩码）+ tcpHeartbeat/udpHeartbeat action pattern + NetSender.RegisterHeartbeat（新增）+ netSenderAdapter Go builder 闭包（不持 robot luaMu）。
静态 logic TCP 心跳迁移到 `RegisterLogicHeartbeat` tcpHeartbeat action；动态心跳 ②③ 旧 Lua 路径未动（2-B.2 处理）。

## 改动文件清单

| 文件 | 类型 | 说明 |
|---|---|---|
| `errcode/codes.go` | 改 | 新增框架码 `ErrHeartbeatConfig=49`（HEARTBEAT_CONFIG）+ codeRegistry 注册；顺带修复「构建层」「配置层」常量块的 gofmt 对齐漂移（被新最长行触发） |
| `engine/heartbeat.go` | 新增 | `HeartbeatField`/`HeartbeatActionConfig` 类型 + `appendLE`（类型宽度掩码 LE 打包）+ `BuildHeartbeatBody`（6 源解析）+ `resolveHeartbeatField` + 源类型常量 |
| `engine/heartbeat_test.go` | 新增 | 22 个测试（appendLE 11 子用例 + 宽度/未知 type；BuildHeartbeatBody 6 源逐源 + skip/error + 组合布局字节对拍；ActionExecutor 分支 5 用例 + fake NetSender） |
| `engine/flow.go` | 改 | 加常量 `PatternTCPHeartbeat`/`PatternUDPHeartbeat`；`ActionDef` 加 `IntervalMs`/`HeartbeatFields`/`SkipWhenMissing` 字段；顺带修复 Pattern 常量块 gofmt 对齐漂移 |
| `engine/action.go` | 改 | `NetSender` 接口新增 `RegisterHeartbeat(cfg HeartbeatActionConfig) error`（不改旧 RegisterTCP/UDPHeartbeat）；`Execute` switch 加 tcpHeartbeat/udpHeartbeat 分支 + `execHeartbeat`（IntervalMs<=0 / Route 缺失 → ErrHeartbeatConfig） |
| `robot/robot.go` | 改 | `netSenderAdapter.RegisterHeartbeat`：构造 Go builder 闭包（BuildHeartbeatBody + 私有计数器成功后递增 + adapter EncodeTCP/UDP + conn.RegisterHeartbeat），不持 robot luaMu |
| `conf/scripts/connect_logic.lua` | 改 | 删 `register_tcp_heartbeat` 调用 + 错误处理；保留连接 + 密钥交换 |
| `conf/flow/flow.json` | 改 | `logicLogin` sequence 加 `RegisterLogicHeartbeat` 节点；actions 表加 `RegisterLogicHeartbeat` tcpHeartbeat action（service=logic, route{2,1}, intervalMs=5000, 空 fields） |

未碰 `network/heartbeat.go`、`network/connection.go`、`network/client.go`、`script/api_network.go`（register_*_heartbeat Lua 路径）、`adapter/`、`admin/`、`agent/`、`cmd/`、前端、密钥 API、codec。

## BuildHeartbeatBody 6 源语义

签名：`BuildHeartbeatBody(fields []HeartbeatField, st *state.Store, privateCounters map[int]int64, skipWhenMissing bool) (body []byte, skip bool, err error)`

| 源 | 取值 | 缺失/非法行为 |
|---|---|---|
| `fixed` | `*Value` | `Value==nil` → 中文 err「缺 value」（不静默默认） |
| `state` | `state.Get(Key)` → `state.ToInt64` | 缺失：`skipWhenMissing`→skip=true 返回；否则 err |
| `stateCounter` | `state.IncrementInt64(Key)`（共享自增，返回新值） | 唯一 state 写副作用（心跳语义本就推进包序号）；Key 空 → err |
| `counter` | `privateCounters[idx]`（私有，当前值）；初值 Start/0；递增由调用方成功后执行 | 私有计数器映射无该下标 → 按 Start（缺省 0）初始化 |
| `timestamp` | `Unit=="s"` → `time.Now().Unix()`；否则 `time.Now().UnixMilli()` | 用 `time` 包，不走 Lua utils.time_ms |
| `randomInt` | `Min + rand.Int63n(Max-Min+1)`（含两端） | `Min==nil`/`Max==nil` → err；`Min>Max` → err；用 `math/rand`（rtt 模拟值无需密码学随机） |

### LE 打包 + 类型宽度掩码（关键）

`appendLE(buf, type, v int64) ([]byte, error)`：值统一 int64 中转，按 type 截断/掩码后小端写入：

| Type | 宽度 | 掩码/位模式 |
|---|---|---|
| u8/i8 | 1 | `byte(uint64(v))` |
| u16/i16 | 2 | `uint16(uint64(v))`（等价 `&0xFFFF`） |
| u32/i32 | 4 | `uint32(uint64(v))`（等价 `&0xFFFFFFFF`） |
| u64/i64 | 8 | `uint64(v)` 全 64 位 |

有符号（iN）按无符号位模式写入（`uintN(int64Value)` 截断），等价 Lua `%` 回绕。未知 type → 中文 err。**不 import 任何 Lua 包**（heartbeat.go 仅 import `encoding/binary`/`fmt`/`math/rand`/`time`/`stressbot/state`）。

### 组合布局用例（证明覆盖 2-B.2 动态布局）

`TestBuildHeartbeatBody_ComboLayout_BattleTcpHeartShape` 构造类 `build_battle_tcp_heart`(19B) 的 4 字段布局：`u16 stateCounter(packageIndex)` / `i64 state(battleId)` / `u8 state(fighterIndex)` / `i64 state(session)`，断言字节序与手算一致（19 字节逐字节对拍）。打包器源类型已覆盖 2-B.2 动态心跳所需的全部字段语义（packageIndex 共享自增 / battleId-fighterIndex-session state / udp_seq 私有 counter / now_ms timestamp / rtt randomInt / fixed 占位）。

## ActionExecutor 接线

`Execute` switch 增加 `PatternTCPHeartbeat`/`PatternUDPHeartbeat` → `ae.execHeartbeat("tcp"|"udp", def)`。

`execHeartbeat(transport, def)`：
1. `def.IntervalMs <= 0` → `NewActionError(ErrHeartbeatConfig, ...)`（中文，含 intervalMs/action/service 上下文）。
2. `def.Route == nil` → `NewActionError(ErrHeartbeatConfig, ...)`（缺 route）。
3. 装配 `HeartbeatActionConfig{Transport, Service, IntervalMs, Route, Fields:HeartbeatFields, SkipWhenMissing}` → `ae.netSender.RegisterHeartbeat(cfg)`。
4. 返回 nil（注册动作本身不产生网络延迟样本、不等待发送）。

## netSenderAdapter Go builder 闭包（核心：不持 robot luaMu）

`netSenderAdapter.RegisterHeartbeat(cfg)`（`robot/robot.go`）：

1. 取 conn（按 `cfg.Transport`：tcp→GetTCPConn，udp→GetUDPConn）；conn==nil → `ErrConnNotFound`（中文 Warn 日志）。
2. 按 `cfg.Fields` 下标 + `Start` 初始化 `privateCounters map[int]int64`（counter 源字段）。
3. 防御性拷贝 `fields`（避免共享 cfg 切片头）。
4. 构造 Go builder 闭包 `goBuilder func() []byte`，每 tick：
   - `BuildHeartbeatBody(fields, st, privateCounters, skipWhenMissing)` → `(body, skip, err)`；
   - `err` → Warn 日志 + `return nil`（跳过本 tick）；
   - `skip` → `return nil`；
   - `conn.GetSecretKey()` → `key`；
   - `adp.EncodeTCP/UDP(route, body, key)` → `packet`；
   - **构建成功后递增私有计数器**：每个 counter 源字段 `privateCounters[i] += Step`（缺省 1）；
   - `return packet`。
5. `conn.RegisterHeartbeat(network.HeartbeatConfig{Interval, goBuilder})`。

**关键**：encode 走 `ns.robot.adp`（adapter 自身 LState 池，非 robot 业务 LState），故 builder 闭包**不持 robot luaMu** —— 这是 2-B 消除心跳 robot-luaMu 依赖的核心。encode 在 2-C 后变纯 Go，本任务不依赖该前置。

闭包捕获：`fields`/`st`/`adp`/`route`/`skipWhenMissing`/`transport`/`conn`/`privateCounters`。`cfg.Service` 字符串在闭包内仅用于日志（捕获值不可变）。

## 静态 logic TCP 心跳迁移

| 项 | 旧（Lua 预编码） | 新（声明式 tcpHeartbeat） |
|---|---|---|
| 注册位置 | `connect_logic.lua:34` `register_tcp_heartbeat("logic",5000,{2,1})` | `flow.json` actions.RegisterLogicHeartbeat + logicLogin sequence 节点 |
| body 构造 | 注册时 `EncodeTCPLocked(route,nil,key)` 预编码缓存，运行时零 encode | 每 tick `BuildHeartbeatBody([])`（空 fields → 空 body）→ `adp.EncodeTCP` |
| route | `{cmd=2, act=1}` | `{cmd:2, act:1}` |
| interval | 5000ms | 5000ms |
| 触发 | Lua 脚本顺序执行（密钥交换后） | ConnectLogicTCP 节点后 RegisterLogicHeartbeat 节点（errorStrategy=abort） |

**细微差异（报告必注）**：新路径每 tick（5s 一次）调一次 `adp.EncodeTCP(route, nil, key)`；旧路径注册时只 encode 一次然后缓存。encode 开销极小（adapter 池），且 5s 一次频率极低，可忽略。行为语义等价：空 body + route{2,1} + 5s 间隔。

## TDD RED / GREEN

### RED
- `engine/heartbeat_test.go`：先写全（appendLE 11 子用例 + 3 辅助 + BuildHeartbeatBody 6 源逐源 + skip/error + 组合布局 + ActionExecutor 5 分支用例 + fakeHeartbeatNetSender）。
- `go vet ./engine/...` 报 `undefined: HeartbeatActionConfig` / `undefined: appendLE` / `undefined: BuildHeartbeatBody` / `undefined: PatternTCPHeartbeat` / `fakeHeartbeatNetSender` 未实现 `RegisterHeartbeat`（NetSender 接口缺方法），符合 RED。

### GREEN
- 实现 `errcode/codes.go`（新码）→ `engine/heartbeat.go`（类型+打包器）→ `engine/flow.go`（常量+ActionDef 字段）→ `engine/action.go`（NetSender.RegisterHeartbeat + Execute 分支 + execHeartbeat）→ `robot/robot.go`（netSenderAdapter.RegisterHeartbeat）→ 迁移 `connect_logic.lua`/`flow.json`。
- 全部测试转绿。

## 验证

| 命令 | 结果 |
|---|---|
| `go build ./...` | 干净，无输出 |
| `go vet ./...` | 干净，无输出 |
| `go test ./... -count=1` | 全绿（adapter/admin/cmd/agent/codec/engine/network/protox/robot/script/sharedstate/state 11 包 ok） |
| `go test ./engine/... -run "Heartbeat\|AppendLE\|Execute_TCPHeartbeat\|Execute_UDPHeartbeat" -v` | 22 测试全 PASS（含 11 子用例 + 组合布局字节对拍） |
| `python -c "json.load(open('conf/flow/flow.json'))"` | OK（JSON 合法） |
| `sed 's/\r$//' f.go \| gofmt -l`（受改动文件） | heartbeat.go / heartbeat_test.go / action.go / robot.go / errcode/codes.go 全 clean；flow.go 仅余**预存 FieldBind 对齐漂移**（line 209，T2-A2.1 concern #1 已记录，非本次引入，未触碰） |
| `-race` | **未跑**：`CGO_ENABLED=0`（Windows 无 cgo）。并发安全：BuildHeartbeatBody 读 state.Store（RWMutex）/privateCounters（per-robot 单心跳 goroutine 独占，无并发）/math/rand（全局锁）；goBuilder 由 network.runHeartbeat 单 goroutine 串行调用，无锁序交叉 |

## Self-review

- [x] `HeartbeatField`/`HeartbeatActionConfig` + `BuildHeartbeatBody`（6 源 + LE 打包 + 类型宽度掩码）齐全；heartbeat.go 零 Lua import（仅 binary/fmt/math/rand/time/state）；游戏概念零进 Go（打包器通用 raw-LE）。
- [x] `PatternTCPHeartbeat/UDPHeartbeat` + ActionDef 字段（IntervalMs/HeartbeatFields/SkipWhenMissing）+ Execute 分支 + `NetSender.RegisterHeartbeat` 新增（旧 RegisterTCPHeartbeat/UDPHeartbeat **未改签名**）。
- [x] `netSenderAdapter.RegisterHeartbeat` 构造 Go builder 闭包（BuildHeartbeatBody + 私有计数器成功后递增 + adapter EncodeTCP/UDP + conn.RegisterHeartbeat），**不持 robot luaMu**（encode 走 adapter 池）。
- [x] 静态 logic TCP 心跳迁移到 `RegisterLogicHeartbeat` tcpHeartbeat action；`connect_logic.lua` 删 register 调用（保留连接+密钥）。
- [x] 动态心跳 ②③（battle UDP 150ms / battle TCP 10s）旧 Lua 路径 `register_*_heartbeat`/`registerHeartbeat`/`RegisterTCPHeartbeat` **未动**、行为不变。
- [x] 新字段全链路一致：`HeartbeatFields` flow.json → ActionDef → HeartbeatActionConfig → BuildHeartbeatBody（测试 `TestExecute_TCPHeartbeat_PassesFieldsAndSkip` 验证透传）。
- [x] 不写兼容兜底：未知 Type/Source、fixed 缺 value、randomInt 缺 min/max → 直接中文 err。
- [x] 日志/错误中文；godoc 齐全（HeartbeatField/HeartbeatActionConfig/appendLE/BuildHeartbeatBody/execHeartbeat/RegisterHeartbeat）。
- [x] `network/heartbeat.go` 未动（运行时与 builder 构造解耦，签名稳定）。
- [x] 未碰 connectionPump/decodeLoop/listenLoop/luaMu/withReleasedMu、前端/admin/agent/cmd、密钥 API、RobotAdapter/codec。

## Concerns

1. **RegisterHeartbeat 与旧 RegisterTCPHeartbeat/RegisterUDPHeartbeat 临时并存**：旧路径（`register_tcp_heartbeat` Lua → `RegisterTCPHeartbeat` → 预编码/动态 builder）仍服务动态心跳 ②③。这是 2-B.1→2-B.2 的**临时过渡**，2-B.2 迁移动态心跳到声明式打包器后删除旧路径（含 Lua `register_*_heartbeat`/`registerHeartbeat`、`NetSender.RegisterTCPHeartbeat/UDPHeartbeat`、`script/api_network.go:registerHeartbeat`）。NetSender 接口暂有两个心跳注册入口。

2. **静态心跳迁移的 encode 频次差异**：旧路径注册时 `EncodeTCPLocked` 一次缓存；新路径每 tick（5s）调一次 `EncodeTCP`。5s 频率 + adapter 池 encode 开销极小，可忽略。若未来心跳频率提升或 encode 成本变化需复测。

3. **flow.go 预存 FieldBind 对齐漂移（非本次引入）**：T2-A2.1 concern #1 已记录的 `FieldBind` 结构体（Values/Entries 类型长于同辈但 tag 未对齐）。本次 `sed 's/\r$//' engine/flow.go | gofmt -l` 仍因此标脏，但 gofmt diff **不涉及本次新增的 ActionDef 心跳字段 / Pattern 常量**（已 grep 确认）。按 brief Windows 环境注不顺手修，建议作为独立 gofmt 全树清理项。

4. **`-race` 未跑**：本机 `CGO_ENABLED=0`（Windows 无 cgo），race detector 不可用。并发安全沿用结构性论证：goBuilder 由 network.runHeartbeat **单 goroutine 串行调用**（per-connection 单心跳），privateCounters 无并发访问；state.Store 自带 RWMutex；math/rand 全局锁。CI 启用 cgo 后建议补跑 `go test -race ./engine/... ./robot/... ./network/...`。

5. **counter 源的「成功后递增」时机**：私有计数器在 BuildHeartbeatBody 读当前值打包后、由 goBuilder 在 encode 成功后递增。若 encode 返回 nil（失败）则不递增（与「成功发送」语义一致——但严格说 encode 失败不等于发送失败；encode 失败时 packet=nil，runHeartbeat 会 `continue` 不发送）。语义：encode 失败 → 不推进序号 → 下次重试同序号。这是合理保守选择，2-B.2 动态心跳迁移时复测。

## 2-B.2 接力

动态心跳迁移（②③）：
- 用本任务的 `BuildHeartbeatBody` + `HeartbeatField` 配置表达 `build_udp_heart`(39B) / `build_battle_tcp_heart`(19B) 布局（本任务的组合布局用例已证明覆盖能力）：
  - `build_battle_tcp_heart`：`u16 stateCounter(packageIndex)` + `i64 state(battleId)` + `u8 state(fighterIndex)` + `i64 state(session)`。
  - `build_udp_heart`：`u16 stateCounter` + `i64 state(battleId)` + `u8 state(fighterIndex)` + `i64 state(session)` + `i32 state(battleAck)` + `u16 randomInt(10,40)` + `u64 timestamp(ms)` + `u32 counter(udp_seq)` + `u16 fixed(0)` + `u8 fixed(0)` + `u8 fixed(0)`。
- 在 flow.json 用 `tcpHeartbeat`/`udpHeartbeat` action + `heartbeatFields` 配置替代 Lua `register_*_heartbeat` + `build_*_heart`。
- 删 Lua 路径：`script/api_network.go` 的 `registerHeartbeat`/`register_tcp_heartbeat`/`register_udp_heartbeat`、`NetSender.RegisterTCPHeartbeat/UDPHeartbeat` 接口及 `netSenderAdapter` 实现、connect_battle_tcp.lua/connect_battle_udp.lua 的 builder/register 调用。
- 验证：动态心跳 goroutine 经新路径**完全不持 robot luaMu**（关键收益，为 2-D 删 luaMu 铺路）。
