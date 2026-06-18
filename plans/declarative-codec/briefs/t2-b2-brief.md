# T2-B.2 Brief — 动态心跳迁移 + 删 Lua builder 路径（破坏性，原子）

> 你是 implementer。先读本 brief。**前置必读**：`plans/declarative-codec/reports/t2-b1-report.md` + `t2-b1-proto-report.md`（2-B.1 双模式框架：`BuildHeartbeatBody` 6 源 + `NetSender.RegisterHeartbeat` Go builder 闭包）、`plans/declarative-codec/00-master.md` §T2-B（双模式设计）、`plans/declarative-codec/progress-ledger.md` §「全局约束」+ §「gofmt/换行 环境注」。
> 工作目录：worktree 根 `D:\Gitee\stressbot\.claude\worktrees\declarative-codec`。**不要 git commit。**

## 目标

用 2-B.1 落地的声明式双模式框架，把最后 2 个**动态心跳**（robot-luaMu 触碰点）从 Lua `register_*_heartbeat` builder 路径迁到声明式 `tcpHeartbeat`/`udpHeartbeat` action（raw-binary `heartbeatFields` 模式），并**删除整条旧 Lua 心跳路径**。迁完后心跳 goroutine 完全不碰 robot 业务 LState（2-B 全轨完成，2-D 删锁的第 2 处异步 Lua 清除）。

## 关键背景（已读码核对）

- **2 个动态心跳**（唯一剩余的 Lua builder 路径用户）：
  - **battle UDP 150ms**：`connect_battle_udp.lua:81` `register_udp_heartbeat("battle",150,{cmd=4,act=2}, build_udp_heart)`；`build_udp_heart`(:11-37) 构造 39 字节 body。
  - **battle TCP 10s**：`connect_battle_tcp.lua:71` `register_tcp_heartbeat("battle",10000,{cmd=4,act=2}, build_battle_tcp_heart)`；`build_battle_tcp_heart`(:9-23) 构造 19 字节 body。
  - （logic TCP 静态心跳已在 2-B.1 迁到 `RegisterLogicHeartbeat` action，不动。）
- **旧 Lua 路径**（本任务删）：`script/api_network.go` `registerHeartbeat`(:1120-1245) + `networkRegisterTCPHeartbeat`/`networkRegisterUDPHeartbeat`(:1108/1116) + `loadNetworkModule` 注册(:88-89) + `__hb_*` registry 存储(:1160-1163)；`engine/action.go` `NetSender.RegisterTCPHeartbeat`/`RegisterUDPHeartbeat`(接口 :172-175)；`robot/robot.go` `netSenderAdapter.RegisterTCPHeartbeat`/`RegisterUDPHeartbeat`(impl :1207/1224)。
- **保留不动**：`network/heartbeat.go`（`Connection.RegisterHeartbeat` + `HeartbeatConfig` + runHeartbeat）——声明式路径与旧 Lua 路径都经它，删 Lua 路径不影响；`NetSender.RegisterHeartbeat`（2-B.1 新增，声明式路径）。
- **2-B.1 框架源已覆盖动态心跳全部字段**：`fixed`/`state`/`stateCounter`(共享自增)/`counter`(私有)/`timestamp`/`randomInt`。

## 字段映射（逐字对齐 Lua builder → heartbeatFields）

### build_udp_heart（39 字节）→ `udpHeartbeat` action 的 `heartbeatFields`
| # | Lua (utils.pack_le) | heartbeatField |
|---|---|---|
| 1 | `u16 idx = robot.increment("packageIndex") % 65536` | `{type:"u16",source:"stateCounter",key:"packageIndex"}` |
| 2 | `i64 battleId = robot.get("battleId")` | `{type:"i64",source:"state",key:"battleId"}` |
| 3 | `u8 fighterIndex = robot.get("fighterIndex")` | `{type:"u8",source:"state",key:"fighterIndex"}` |
| 4 | `i64 session = robot.get("battleSession")` | `{type:"i64",source:"state",key:"battleSession"}` |
| 5 | `i32 ack = robot.get("battleAck")` | `{type:"i32",source:"state",key:"battleAck"}` |
| 6 | `u16 rtt = utils.random_int(10,40)` | `{type:"u16",source:"randomInt",min:10,max:40}` |
| 7 | `u64 now_ms = utils.time_ms()` | `{type:"u64",source:"timestamp",unit:"ms"}` |
| 8 | `u32 udp_seq`（脚本私有，+1，>4294967295 回 1） | `{type:"u32",source:"counter",start:0,step:1}` |
| 9 | `u16 0` (LossCount) | `{type:"u16",source:"fixed",value:0}` |
| 10 | `u8 0` (Fps) | `{type:"u8",source:"fixed",value:0}` |
| 11 | `u8 0` (TargetFps) | `{type:"u8",source:"fixed",value:0}` |

### build_battle_tcp_heart（19 字节）→ `tcpHeartbeat` action 的 `heartbeatFields`
| # | Lua | heartbeatField |
|---|---|---|
| 1 | `u16 idx = robot.increment("packageIndex") % 65536` | `{type:"u16",source:"stateCounter",key:"packageIndex"}` |
| 2 | `i64 battleId` | `{type:"i64",source:"state",key:"battleId"}` |
| 3 | `u8 fighterIndex` | `{type:"u8",source:"state",key:"fighterIndex"}` |
| 4 | `i64 session = robot.get("battleSession")` | `{type:"i64",source:"state",key:"battleSession"}` |

> **回绕语义**：`%65536`/`%4294967295` 由打包器按 type 宽度掩码自动处理（u16→&0xFFFF，u32→&0xFFFFFFFF）。udp_seq 在 Lua 里 >4294967295 回 **1**；声明式 counter 无界增长、打包 u32 掩码后回绕到 **0**——差异仅在第 2^32 次 tick 后（150ms≈20 年才到，理论差异，可忽略；报告注明）。
> **udp_seq 重置**：Lua 在 `execute()` 每 battle 复位 `udp_seq=0`；声明式 counter 绑定心跳注册生命周期——每 battle 重新注册（ConnectBattleUDP 节点每 battle 跑一次 → 新 RegisterHeartbeat → 新私有计数器从 start=0 开始），等价复位。✓
> **packageIndex 共享**：`connect_battle_udp.lua:76 robot.set("packageIndex",0)` 复位仍保留（execute 不删该行），stateCounter 的 `state.IncrementInt64` 与之共操作同一 key，语义一致。✓

## 范围（严格边界）

**做（原子，5 步缺一则 flow 无法加载/心跳断流）：**
1. **flow.json**：加 2 个 action def + 2 个 action 节点：
   - `"RegisterBattleUDPHeartbeat": {"pattern":"udpHeartbeat","service":"battle","route":{"cmd":4,"act":2},"intervalMs":150,"heartbeatFields":[...11 字段...]}` —— 节点放在 ConnectBattleUDP 之后。
   - `"RegisterBattleTCPHeartbeat": {"pattern":"tcpHeartbeat","service":"battle","route":{"cmd":4,"act":2},"intervalMs":10000,"heartbeatFields":[...4 字段...]}` —— 节点放在 ConnectBattleTCP 之后。
2. **connect_battle_udp.lua**：删 `build_udp_heart` 函数(:11-37) + `register_udp_heartbeat` 调用及其错误处理(:81-89)；保留 execute 其余（连接、密钥、packageIndex/battleAck/frameCount 复位）。
3. **connect_battle_tcp.lua**：删 `build_battle_tcp_heart` 函数(:9-23) + `register_tcp_heartbeat` 调用及错误处理(:71-79)；保留 execute 其余。
4. **删旧 Lua 心跳路径**：`script/api_network.go` 删 `registerHeartbeat` + `networkRegisterTCPHeartbeat`/`networkRegisterUDPHeartbeat` + `loadNetworkModule` 的 2 行注册 + `__hb_*` registry 存储 + 不再用的 `hbProtoTCP/UDP` 常量（若删后无引用）。
5. **删旧 NetSender 心跳方法**：`engine/action.go` 删 `NetSender.RegisterTCPHeartbeat`/`RegisterUDPHeartbeat`(接口)；`robot/robot.go` 删 `netSenderAdapter.RegisterTCPHeartbeat`/`RegisterUDPHeartbeat`(impl)。**保留** `NetSender.RegisterHeartbeat`（声明式）+ `network/heartbeat.go`。

**不做：**
- ❌ 不动 `network/heartbeat.go`、connectionPump/decodeLoop/listenLoop（→ 2-C3）。
- ❌ 不删 `luaMu`/`withReleasedMu`（→ 2-D；registerHeartbeat 内的 withReleasedMu 调用随函数删除一并消失，但 withReleasedMu 定义与其他 29 处调用保留）。
- ❌ 不动前端 luaApiSpec（register_*_heartbeat 文档项）—— 前端清理归 T3，本任务只删后端实现。
- ❌ 不改 README/docs 的 register_*_heartbeat 文本（文档，非代码）。
- ❌ 不动 codec/RobotAdapter（→ 2-C）。

## 关键约束

- **原子**：5 步一起完成（删 Lua 路径前必须先迁完 2 动态心跳，否则 flow 调 register_*_heartbeat 报 nil）。
- **不写兼容兜底**：旧路径直接删，不留空壳。
- **字段逐字对齐**：heartbeatFields 必须与 Lua builder 字节布局逐字段一致（上表）。
- 日志/错误中文；godoc；`go build ./...` + `go vet` 通过。
- **Windows 环境注**：`gofmt -l` 标 .go 脏是 autocrlf CRLF，**不要**对单文件 `gofmt -w`；校验 canonical 用 `sed 's/\r$//' f.go | gofmt -l`。
- **不要 git commit。**

## 工作方式

1. **先读**：`conf/scripts/connect_battle_udp.lua` + `connect_battle_tcp.lua`（确认 builder 字段 + register 调用位置）、`script/api_network.go` `registerHeartbeat`/`networkRegister*Heartbeat` + `loadNetworkModule` 注册处 + `__hb_*`/`hbProto*`、`engine/action.go` `NetSender` 接口 RegisterTCP/UDPHeartbeat、`robot/robot.go` `netSenderAdapter.RegisterTCP/UDPHeartbeat`、`conf/flow/flow.json` ConnectBattleUDP/TCP 节点位置 + RegisterLogicHeartbeat（2-B.1 已加的静态心跳 action 作参照）。
2. **静态确认删除影响**：`grep -rn 'RegisterTCPHeartbeat\|RegisterUDPHeartbeat\|register_tcp_heartbeat\|register_udp_heartbeat\|registerHeartbeat\|networkRegisterTCPHeartbeat\|networkRegisterUDPHeartbeat'` 全仓，确认除上述删除点 + 前端/文档外无其他生产引用；若 `_test.go` 有引用，一并更新/删除。
3. **迁移配置/Lua**：flow.json 加 2 action def + 节点；2 connect 脚本删 builder + register 调用。
4. **删旧路径**：api_network.go + action.go + robot.go 删除上述符号。
5. **验证**：`go build ./...`、`go vet ./...`、`go test ./script/... ./robot/... ./engine/... ./network/... -count=1` 全绿。
6. **静态确认零残留**：全仓 grep `register_tcp_heartbeat`/`register_udp_heartbeat`/`RegisterTCPHeartbeat`/`RegisterUDPHeartbeat`（生产代码零残留，仅 README/docs/前端 spec 历史引用）。
7. **不要 git commit。**

## 验收（self-review）

- flow.json 2 心跳 action def + 节点就位，heartbeatFields 逐字段对齐 Lua builder（11/4 字段）。
- connect_battle_udp/tcp.lua 删 builder + register 调用；execute 其余（连接/密钥/复位）保留。
- api_network.go registerHeartbeat/networkRegister*Heartbeat/注册/__hb_* 删净；action.go NetSender.RegisterTCP/UDPHeartbeat 接口删；robot.go netSenderAdapter impl 删。
- `network/heartbeat.go` + `NetSender.RegisterHeartbeat`（声明式）保留未动。
- 全仓生产代码零残留旧心跳符号；go build/vet/test 全绿。

## ⚠️ 运行时验证依赖（必须在报告突出）

心跳行为（尤其 battle UDP 150ms 热路径）**无法单测覆盖**（需真实服务端交互）。implementer 只保证：编译/单测绿 + 静态迁移完整 + heartbeatFields 逐字段对齐。**battle 心跳在声明式 pull/builder 模型下对真实服务端的表现（字段值正确、packageIndex/udp_seq 推进、rtt 随机、150ms 间隔稳定、不掉线）依赖 controller/用户按 CLAUDE.md 跑 `go run ./cmd/agent` 2~5 分钟验证**。报告须列为「待运行时验证」项，不得宣称「已验证心跳行为」。可与 2-A2.2 的 battle 验证合并一次跑。

## 报告

写完整报告到 `plans/declarative-codec/reports/t2-b2-report.md`：实现内容、2 动态心跳字段映射表、flow.json action/节点、Lua 路径删除清单、NetSender 旧方法删除、TDD（若有）/静态零残留、改动文件、self-review、**运行时验证待办**、concerns（udp_seq 回绕 0-vs-1 理论差异、前端 luaApiSpec 待 T3 清理）。

返回（<15 行）：Status、改动文件、一行测试摘要、**运行时验证待办**、concerns、报告路径。
