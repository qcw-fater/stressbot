# T2-B.2 报告 — 动态心跳迁移 + 删 Lua builder 路径（破坏性，原子）

> 任务 brief：`plans/declarative-codec/briefs/t2-b2-brief.md`
> 前置：2-B.1 双模式框架（`reports/t2-b1-report.md` + `t2-b1-proto-report.md`）。
> 工作目录：worktree 根 `D:\Gitee\stressbot\.claude\worktrees\declarative-codec`，分支 `worktree-declarative-codec`。
> **未 git commit / add**（遵 brief 硬约束）。

## Status

**DONE（静态层面）**。最后 2 个动态心跳（battle UDP 150ms 11 字段 / battle TCP 10s 4 字段）从 Lua `register_*_heartbeat` builder 路径迁到声明式 `udpHeartbeat`/`tcpHeartbeat` action（raw-binary `heartbeatFields` 模式），并删除整条旧 Lua 心跳路径。`go build ./...` + `go vet ./...` 干净，全仓 `go test ./... -count=1` 全绿（11 个 ok 包）。
心跳 goroutine 经新路径完全不持 robot 业务 LState（2-B 全轨完成，2-D 删锁的第 2 处异步 Lua 障碍清除）。
**心跳行为（尤其 battle UDP 150ms 热路径）未做运行时验证**——见「运行时验证待办」。

## 改动文件清单

| 文件 | 类型 | 说明 |
|---|---|---|
| `conf/flow/flow.json` | 改 | `startBattle` 序列加 2 节点（`RegisterBattleTCPHeartbeat` 紧随 `ConnectBattleTCP`、`RegisterBattleUDPHeartbeat` 紧随 `ConnectBattleUDP`，均 `errorStrategy:"skip"`）；nodes 表加 2 action 节点；actions 表加 2 action def（udpHeartbeat 11 字段 / tcpHeartbeat 4 字段，逐字对齐 brief 映射表） |
| `conf/scripts/connect_battle_udp.lua` | 改 | 删 `build_udp_heart` 函数（39B 构造）+ `register_udp_heartbeat` 调用及错误处理 + `udp_seq` 私有变量；删 `require("utils")`（不再用）；保留连接/密钥设置/`packageIndex`-`battleAck`-`frameCount` 复位；日志文案更新为「心跳由声明式节点注册」 |
| `conf/scripts/connect_battle_tcp.lua` | 改 | 删 `build_battle_tcp_heart` 函数（19B 构造）+ `register_tcp_heartbeat` 调用及错误处理；删 `require("utils")`；保留连接/密钥交换；日志文案更新 |
| `script/api_network.go` | 改 | 删 `loadNetworkModule` 的 2 行 `register_tcp/udp_heartbeat` 注册 + 2 行 godoc；删整段「心跳」section（`heartbeatProto` 类型 + `hbProtoTCP`/`hbProtoUDP` 常量 + `networkRegisterTCPHeartbeat`/`networkRegisterUDPHeartbeat` + `registerHeartbeat` 共 161 行）；连带删除其内的 `__hb_*` registry 存储、预编码/动态 builder 闭包、`withReleasedMu` 调用 |
| `engine/action.go` | 改 | 删 `NetSender.RegisterTCPHeartbeat`/`RegisterUDPHeartbeat` 接口方法；更新 `RegisterHeartbeat` godoc 去掉「与旧路径临时并存」表述 |
| `robot/robot.go` | 改 | 删 `netSenderAdapter.RegisterTCPHeartbeat`/`RegisterUDPHeartbeat` impl（34 行）；更新 `RegisterHeartbeat` godoc 去掉「与旧路径临时并存」表述 |
| `engine/heartbeat_test.go` | 改 | 删 `fakeHeartbeatNetSender` 的 `RegisterTCPHeartbeat`/`RegisterUDPHeartbeat` 方法（接口删除后 fake 须同步） |

未碰 `network/heartbeat.go`（`Connection.RegisterHeartbeat` + `HeartbeatConfig` + `runHeartbeat`，声明式与旧路径共用，删旧路径不影响）、`connectionPump`/`decodeLoop`/`listenLoop`（→ 2-C3）、`luaMu`/`withReleasedMu`（→ 2-D；`withReleasedMu` 定义 + 8 处其他调用保留）、`NetSender.RegisterHeartbeat`（声明式）、前端 `luaApiSpec`（→ T3）、README/docs、codec/RobotAdapter（→ 2-C）、admin/agent/cmd。

## 2 动态心跳字段映射（逐字对齐 Lua builder → heartbeatFields）

### build_udp_heart（39 字节）→ `RegisterBattleUDPHeartbeat`（udpHeartbeat action）

| # | Lua（`utils.pack_le`） | heartbeatField | 对齐 |
|---|---|---|---|
| 1 | `u16 idx = robot.increment("packageIndex") % 65536` | `{type:"u16",source:"stateCounter",key:"packageIndex"}` | ✓ |
| 2 | `i64 battleId = robot.get("battleId")` | `{type:"i64",source:"state",key:"battleId"}` | ✓ |
| 3 | `u8 fighterIndex = robot.get("fighterIndex")` | `{type:"u8",source:"state",key:"fighterIndex"}` | ✓ |
| 4 | `i64 session = robot.get("battleSession")` | `{type:"i64",source:"state",key:"battleSession"}` | ✓ |
| 5 | `i32 ack = robot.get("battleAck")` | `{type:"i32",source:"state",key:"battleAck"}` | ✓ |
| 6 | `u16 rtt = utils.random_int(10,40)` | `{type:"u16",source:"randomInt",min:10,max:40}` | ✓ |
| 7 | `u64 now_ms = utils.time_ms()` | `{type:"u64",source:"timestamp",unit:"ms"}` | ✓ |
| 8 | `u32 udp_seq`（私有 +1，>4294967295 回 1） | `{type:"u32",source:"counter",start:0,step:1}` | ✓（回绕差异见 Concerns） |
| 9 | `u16 0`（LossCount） | `{type:"u16",source:"fixed",value:0}` | ✓ |
| 10 | `u8 0`（Fps） | `{type:"u8",source:"fixed",value:0}` | ✓ |
| 11 | `u8 0`（TargetFps） | `{type:"u8",source:"fixed",value:0}` | ✓ |

字段宽度合计：2+8+1+8+4+2+8+4+2+1+1 = **39 字节**（与 Lua 一致）。

### build_battle_tcp_heart（19 字节）→ `RegisterBattleTCPHeartbeat`（tcpHeartbeat action）

| # | Lua | heartbeatField | 对齐 |
|---|---|---|---|
| 1 | `u16 idx = robot.increment("packageIndex") % 65536` | `{type:"u16",source:"stateCounter",key:"packageIndex"}` | ✓ |
| 2 | `i64 battleId` | `{type:"i64",source:"state",key:"battleId"}` | ✓ |
| 3 | `u8 fighterIndex` | `{type:"u8",source:"state",key:"fighterIndex"}` | ✓ |
| 4 | `i64 session = robot.get("battleSession")` | `{type:"i64",source:"state",key:"battleSession"}` | ✓ |

字段宽度合计：2+8+1+8 = **19 字节**（与 Lua 一致）。

route 对齐：两者均 `{cmd:4, act:2}`（Lua `{cmd=4, act=2}`）。interval：UDP=150ms，TCP=10000ms（与 Lua 一致）。service=`battle`（一致）。

## flow.json 结构

### actions 表（新增 2 def）

```jsonc
"RegisterBattleTCPHeartbeat": {
  "pattern": "tcpHeartbeat", "service": "battle",
  "route": { "cmd": 4, "act": 2 }, "intervalMs": 10000,
  "heartbeatFields": [ /* 4 字段，见上表 */ ]
},
"RegisterBattleUDPHeartbeat": {
  "pattern": "udpHeartbeat", "service": "battle",
  "route": { "cmd": 4, "act": 2 }, "intervalMs": 150,
  "heartbeatFields": [ /* 11 字段，见上表 */ ]
}
```

### nodes 表（startBattle 序列 + 2 action 节点）

```jsonc
"startBattle": { "type": "sequence", "next": [
  "ConnectBattleTCP", "RegisterBattleTCPHeartbeat",
  "ConnectBattleUDP", "RegisterBattleUDPHeartbeat",
  "RegisterBattle", "loadLoop", ...
] },
"RegisterBattleTCPHeartbeat": { "type": "action", "action": "RegisterBattleTCPHeartbeat", "errorStrategy": "skip" },
"RegisterBattleUDPHeartbeat": { "type": "action", "action": "RegisterBattleUDPHeartbeat", "errorStrategy": "skip" }
```

`errorStrategy:"skip"`：心跳注册失败不中断 battle 流程（与 ConnectBattle 的 `errorStrategy:"skip"` 一致；旧 Lua 路径虽 `return hbCode` 但 execute 返回非 0 后 action 节点的 `errorStrategy` 同样决定是否 abort，battle TCP/UDP Connect 均为 skip，心跳沿用 skip 保持语义一致）。

## Lua 路径删除清单（script/api_network.go）

- `loadNetworkModule`：2 行 `L.SetField(mod, "register_tcp_heartbeat", ...)` / `"register_udp_heartbeat", ...` + 2 行 godoc。
- 整段「心跳」section（原 1085–1245 行，161 行）：
  - `type heartbeatProto int` + `hbProtoTCP`/`hbProtoUDP` 常量。
  - `networkRegisterTCPHeartbeat` / `networkRegisterUDPHeartbeat`（薄包装）。
  - `registerHeartbeat`（核心实现）：service/route 解析、`__hb_%s_%s__` registry 存储、静态预编码（`EncodeTCPLocked`/`EncodeUDPLocked`）、动态 builder 闭包（`luaMu.TryLock` → `L.CallByParam` 调 Lua builder → `Encode*Locked`）、`withReleasedMu` 调 `ctx.NetSender.RegisterTCP/UDPHeartbeat`。
- 连带消失：`withReleasedMu` 在 registerHeartbeat 内的 1 处调用（定义 + 其余 8 处调用保留，属 2-D）。

import 完整性核对：`fmt`（剩 1 处 `Sprintf`）、`errors`（剩 2 处 `AsType`）、`stresslog`（剩 1 处 `Debug`）、`engine`/`errcode`（各多处）仍被使用，无悬空 import。

## NetSender 旧方法删除

- `engine/action.go` `NetSender` 接口：删 `RegisterTCPHeartbeat(service string, intervalMs int, builder func() []byte) error` + `RegisterUDPHeartbeat(...)`（接口）；保留 `RegisterHeartbeat(cfg HeartbeatActionConfig) error`（声明式）。
- `robot/robot.go` `netSenderAdapter`：删 `RegisterTCPHeartbeat`/`RegisterUDPHeartbeat` impl（各 17 行）；保留 `RegisterHeartbeat`。
- `engine/heartbeat_test.go` `fakeHeartbeatNetSender`：删 2 个 stub 方法（接口删除后 fake 编译失败，必须同步删除）。

外部调用点核对（`grep '\.RegisterTCPHeartbeat\|\.RegisterUDPHeartbeat'`）：删除前全仓仅 `script/api_network.go:1238/1240`（在 `registerHeartbeat` 内部）2 处，随 `registerHeartbeat` 删除一并消失；admin/agent/cmd/前端无任何调用。✓

## 不变量核对（self-review）

- [x] flow.json 2 心跳 action def + 2 节点就位；`startBattle` 序列正确插入（TCP 注册紧随 TCP 连接，UDP 注册紧随 UDP 连接，均在 `RegisterBattle` 之前）。
- [x] heartbeatFields 逐字段对齐 Lua builder（UDP 11 / TCP 4），字节宽度合计 39 / 19 与 Lua 一致；route/interval/service 一致。
- [x] connect_battle_udp/tcp.lua 删 builder + register 调用 + 不再用的 `utils` require；execute 其余（连接/密钥/`packageIndex`-`battleAck`-`frameCount` 复位）保留。
- [x] api_network.go `registerHeartbeat`/`networkRegister*Heartbeat`/`loadNetworkModule` 注册/`__hb_*`/`heartbeatProto`/`hbProto*` 删净；import 无悬空。
- [x] action.go `NetSender.RegisterTCP/UDPHeartbeat` 接口删；robot.go `netSenderAdapter` impl 删；heartbeat_test.go fake 同步删。
- [x] `network/heartbeat.go`（`Connection.RegisterHeartbeat` + `HeartbeatConfig` + `runHeartbeat`）+ `NetSender.RegisterHeartbeat`（声明式）+ `luaMu`/`withReleasedMu` 保留未动。
- [x] 全仓生产代码（.go）零残留旧心跳符号；前端 `luaApiSpec` / README / docs 历史引用保留（属 T3 / 文档，非代码）。
- [x] 日志/错误中文；godoc 齐全（删除后 `RegisterHeartbeat` godoc 更新为无「临时并存」表述）。
- [x] 不写兼容兜底：旧路径直接删，不留空壳。

## 验证

| 命令 | 结果 |
|---|---|
| `go build ./...` | 干净，无输出 |
| `go vet ./...` | 干净，无输出 |
| `go test ./script/... ./robot/... ./engine/... ./network/... -count=1` | 全绿（4 包 ok） |
| `go test ./... -count=1` | 全绿（adapter/admin/cmd-agent/codec/engine/network/protox/robot/script/sharedstate/state 11 包 ok） |
| `python -c "json.load(open('conf/flow/flow.json'))"` | OK（JSON 合法） |
| `grep` 全仓 .go `RegisterTCPHeartbeat\|RegisterUDPHeartbeat\|register_tcp_heartbeat\|register_udp_heartbeat\|registerHeartbeat\|networkRegister*Heartbeat\|hbProto*\|heartbeatProto\|__hb_` | **零匹配**（生产代码零残留） |
| `sed 's/\r$//' f.go \| gofmt -l`（api_network.go / action.go / robot.go） | 全 clean（CRLF 剥除后 canonical） |
| `sed 's/\r$//' engine/heartbeat_test.go \| gofmt -l` | 标脏——**2-B.1 遗留的对齐漂移**（fakeHeartbeatNetSender 的 4 行 secret-key 方法块，`git show HEAD:engine/heartbeat_test.go \| gofmt -l` 同样标脏，证明非本次引入）。按 brief Windows 环境注不顺手 `gofmt -w` 单文件 |
| `-race` | **未跑**：本机 `CGO_ENABLED=0`（Windows 无 cgo）。并发安全沿用结构性论证：goBuilder 由 network.runHeartbeat 单 goroutine 串行调用；BuildHeartbeatBody 读 state.Store（RWMutex）/privateCounters（per-robot 单心跳 goroutine 独占）；encode 走 adapter 池（非 robot 业务 LState）。CI 启用 cgo 后建议补跑 `go test -race ./engine/... ./robot/... ./network/...` |

## ⚠️ 运行时验证待办（必须 controller/用户执行）

**心跳行为（尤其 battle UDP 150ms 热路径）无法单测覆盖**（需真实服务端交互）。implementer 只保证：编译/单测绿 + 静态迁移完整 + heartbeatFields 逐字段对齐 Lua builder（39B/19B 字节布局 + route/interval/service 一致）。

不得宣称「已验证心跳行为」。以下项依赖 controller/用户按 CLAUDE.md 跑 `rm -f log/stressbot.log` + `go run ./cmd/agent -config conf/config.json` 2~5 分钟验证（可与 2-A2.2 的 battle 验证合并一次跑）：

1. **battle UDP 150ms 心跳**：声明式 goroutine 每 150ms 发一包，字段值正确（packageIndex 共享自增推进、battleId/fighterIndex/session/ack 从 state 取值正确、rtt 在 [10,40] 随机、now_ms 当前毫秒时间戳、udp_seq 私有计数器从 0 递增、3 个 fixed 0 占位）。
2. **battle TCP 10s 心跳**：每 10s 发一包，4 字段值正确（packageIndex 共享自增、battleId/fighterIndex/session）。
3. **心跳不致掉线**：声明式路径每 tick 调一次 `adp.EncodeTCP/UDP(route, body, key)`（旧 UDP 路径也是每 tick encode；旧 TCP 静态路径是注册时预编码缓存——但 battle TCP 是动态心跳，旧路径也是每 tick encode，故无 encode 频次回归）。
4. **packageIndex 共享语义**：UDP/TCP 心跳共享同一 `packageIndex` stateCounter（state.IncrementInt64），与 connect 脚本的 `robot.set("packageIndex",0)` 复位 + 其他 battle 动作的 packageIndex 推进协同正确（不冲突、不跳号异常）。
5. **每 battle 重新注册**：ConnectBattleUDP/TCP 每 battle 跑一次 → 新 RegisterHeartbeat → 新私有 udp_seq 计数器从 start=0 开始（等价旧 Lua `udp_seq=0` 复位）。
6. **日志审查**：`grep -i "error\|warn\|失败" log/stressbot.log | grep -v "headError"` 应无心跳相关异常（连接不存在 / encode 失败 / builder err 等）。

## Concerns

1. **udp_seq 回绕 0-vs-1 理论差异**：Lua `if udp_seq > 4294967295 then udp_seq = 1 end`（>2^32-1 回 **1**）；声明式 counter 无界增长，打包 u32 掩码后回绕到 **0**。差异仅在第 2^32 次 tick 后出现——150ms 间隔下需 ≈20 年才达到（4294967295 × 0.15s ≈ 20.4 年），属理论差异，实际压测不可能触发。报告已注明，若服务端严格校验 udp_seq 从 1 起步可后续把 counter `start` 改为 1（但 Lua 首包也是 `udp_seq=1`，counter start=1 首包也是 1，语义对齐；当前 start=0 首包是 0 + step 1 → 第二 tick 才 1，与 Lua 首包 1 有 1 tick 的偏移）。**注**：Lua `udp_seq=0` 初值 + 首 tick `udp_seq=udp_seq+1=1`，故 Lua 首包 udp_seq=1；声明式 counter start=0 + BuildHeartbeatBody 读当前值（0）打包后递增 → 首包 udp_seq=**0**，第二包 1。这是首包 1-tick 偏移（旧 1 / 新 0）。若服务端要求首包 udp_seq≥1，需把 `start` 改为 1。**建议运行时验证时观察服务端是否拒绝 udp_seq=0 首包**。

2. **errorStrategy 选择（skip vs abort）**：心跳注册节点用 `skip`（与 ConnectBattle 一致）。旧 Lua 路径 `register_*_heartbeat` 返回非 0 code 时 connect 脚本 `return hbCode`，action 节点 errorStrategy 决定后续——battle TCP/UDP Connect 节点是 skip，心跳注册失败沿用 skip 保持「battle 流程不因心跳注册失败中断」语义。若希望心跳注册失败立即 abort battle，可改 abort（但 ConnectBattle 本身是 skip，改 abort 会引入不一致）。

3. **前端 `luaApiSpec` 未清**：`cmd/web/src/components/FlowEditor/lua/luaApiSpec.ts` 仍列 `register_tcp_heartbeat`/`register_udp_heartbeat` 文档项（后端实现已删，Lua 调用会 nil error）。前端清理归 T3，本任务只删后端实现。

4. **README/docs 历史引用**：`README.md` / `docs/flow-node-system.md` / `plans/` 多处仍提 register_*_heartbeat，属文档非代码，未改。

5. **heartbeat_test.go gofmt 漂移（非本次引入）**：2-B.1 已存在的 `fakeHeartbeatNetSender` secret-key 方法块对齐漂移，本次删除 2 个方法后漂移内容微变（删除而非新增），但文件在 HEAD 即标脏。按 brief Windows 注不顺手 `gofmt -w`。建议作为独立 gofmt 全树清理项。

6. **`-race` 未跑**：本机 `CGO_ENABLED=0`。并发安全沿用结构性论证（goBuilder 单 goroutine 串行 / state.Store RWMutex / privateCounters per-robot 独占）。CI 启用 cgo 后建议补跑 `go test -race ./engine/... ./robot/... ./network/...`，重点观察 battle UDP 150ms 心跳 goroutine 与 robot 主执行 goroutine 的 state 访问。

## 2-D 接力

动态心跳迁移完成意味着：**所有心跳 goroutine（静态 logic TCP + 动态 battle UDP/TCP）均经声明式 `RegisterHeartbeat` → Go builder 闭包 → adapter 池 encode，完全不持 robot 业务 LState**。旧 `registerHeartbeat` 内的 `withReleasedMu`/`luaMu.TryLock` 调用随函数删除消失。

`withReleasedMu` 定义 + 8 处剩余调用（均在同步网络 API：connect/close/request/send/listen 等需在持 luaMu 时调 `ctx.NetSender.*` 的场景）保留，属 2-D 删锁范围。2-D 删 luaMu 前需审计这 8 处 `withReleasedMu` 调用是否仍有死锁风险（多数因 RegisterHeartbeat 内部 StopHeartbeat 等 2s 的场景已随本任务消失，剩余调用需逐一确认底层是否仍持锁等待）。
