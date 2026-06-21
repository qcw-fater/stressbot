# T2-A2.2 Brief — frameData 迁移 + 下线 listen script callback + 启用禁用校验（破坏性，原子）

> 你是 implementer。先读本 brief。参考 `plans/declarative-codec/02-track-backend-integration.md` §2-A（实施切片 1 禁用校验、5 配置迁移）、`plans/declarative-codec/reports/t2-a2-1-report.md`（2-A2.1 的 RegisterListen / 队列）、`plans/declarative-codec/progress-ledger.md` §「全局约束」+ §「gofmt/换行 环境注」。
> 工作目录：worktree 根 `D:\Gitee\stressbot\.claude\worktrees\declarative-codec`。**不要 git commit。**

## 目标

下线最后一个 listen 脚本回调 `frameData`（udp:battle 4:11 → `listen_frame_data.lua`），让业务 LState 不再被 listenLoop 触碰（为 2-D 删锁扫清最后一处异步 Lua）。**用户已定方案（Option A）**：用**非阻塞单次 pop** 把 battleAck 追踪从「push 回调」迁到「主流程 pull 消费」，battleAck 语义保留、热路径零停滞。

三步**必须原子**（任一步缺失则 flow 无法加载或行为回退）：
1. 新增非阻塞 pop Lua API；`sync_frame_data.lua` 改为主流程消费最新 ack 写 battleAck。
2. `frameData` listen 去掉 `script`（变纯缓存 listen，queueSize=1 保最新）；删除 `listen_frame_data.lua`。
3. 删 `createListenCallback` 的 Script 分支；`robot.RegisterListen` 对 `ListenDef.script` fail-loud。

## 关键背景（已读码核对）

- **frameData 回调链**：`ConnectBattleUDP` 节点 listenRefs（flow.json:385-394）注册 `frameData`(route {cmd:4,act:11}, server `udp:battle`)；`frameData` listen def（flow.json:2090-2092）`{"script":"listen_frame_data.lua"}`；`createListenCallback` 的 Script 分支（robot.go:784-829）在 listenLoop goroutine 上跑 `RunCallbackScript`（持 luaMu）。
- **battleAck 语义**：`listen_frame_data.lua` 解析 UDP 原始字节 `byte[13..16]`（Lua 1-based）小端 uint32 = frameIndex → `robot.set("battleAck", frameIndex)`。高频回调，异常按 1/10/100 次限频 warn，`robot.increment("frameDataInvalidCount")`。
- **消费方**：`sync_frame_data.lua`（action `SyncFrameData`，flow.json:1249-1252）每轮 READ `battleAck`（:22）→ 塞进出帧 Ack 字段（:30）→ `network.udp_send("battle",{cmd=4,act=11},frame)` → `utils.sleep(60)`（~16fps）。**松散「保最新」**，非 lockstep。`connect_battle_udp.lua:15` 也读 battleAck、:77 初始化为 0（**无需改**）。
- **现有 udp_listen**（api_network.go:809-914 `networkListen`）：阻塞轮询，`timeout` 为**整秒**，不适合 60ms sync loop（无 ack 时阻塞 ≤1s，热路径停滞风险）。故需**非阻塞** pop。
- **2-A2.1 已就位**：`Connection.RegisterListen(routeKey, cb, queueSize)`（预创建队列）；`GetUDPListenResp` → queue FIFO `Pop`（非阻塞，空返回 nil）。`frameData` listenRef 已在 ConnectBattleUDP 注册（cache 模式 queueSize=1），迁完后 sync loop 用非阻塞 pop 从该 queue 取最新。
- **engine 包无后端 flow 校验**（2-A2.1 已证）：`ListenDef.script` 的 fail-loud 落在 `robot.RegisterListen`（与 queueSize 校验同层），不在不存在的「flow 校验阶段」。
- **frameDataInvalidCount 仅 listen_frame_data.lua 引用**（grep 实测无其他读者）——纯诊断计数，随脚本删除一并下线，不保留。

## 范围（严格边界）

**做：**
- `script/api_network.go`：新增 `network.try_tcp_listen(service, route)` / `network.try_udp_listen(service, route)`（**非阻塞单次 pop**，返回原始 body 字符串）+ 在 network 模块注册。
- `conf/scripts/sync_frame_data.lua`：每轮迭代**开头**用 `network.try_udp_listen` 取最新 ack 帧 → 解析 frameIndex → `robot.set("battleAck", …)`（保留 battleAck 追踪）。
- `conf/flow/flow.json`：`frameData` listen def 去掉 `script`，变纯缓存 listen（`{}`，由 listenRef 注册 queueSize=1）。
- 删除 `conf/scripts/listen_frame_data.lua`。
- `robot/robot.go`：删 `createListenCallback` 的 Script 分支（784-829）；`RegisterListen` 对 `cbDef.Script != ""` fail-loud（中文 error）。
- TDD（Go 机械部分）+ 说明运行时验证依赖。

**不做（明确推迟，碰了即越界）：**
- ❌ 不动 `connectionPump` / `decodeLoop` / `listenLoop` 调度模型（→ 2-C3）。
- ❌ 不动心跳 / `luaMu` / `withReleasedMu`（→ 2-B / 2-D）。
- ❌ 不改 `udp_listen`/`tcp_listen` 现有阻塞语义（新增 `try_*` 并存，不改旧的）。
- ❌ 不动前端、不动 admin/agent/cmd。
- ❌ 不给其他 listen 加 queueSize（frameData 保持 queueSize=1 缺省，保最新即可）。

## 设计

### 1. 非阻塞 pop：`network.try_tcp_listen` / `network.try_udp_listen`（api_network.go）

签名：`network.try_udp_listen(service, route)` → `code(number), data(string|nil)`。
- **非阻塞**：调一次 `ctx.NetSender.GetUDPListenResp(service, routeKey)`（或 TCP），**不轮询、不 sleep**。
- 有消息：`code=0`，`data` = 原始 body 字符串（**不解析 proto**——try_* 是「原始 drain」原语，需 proto 解析的消费用阻塞版 udp/tcp_listen）。
- 无消息（queue 空）：`code=31`（`errcode.ErrListenTimeout`），`data=nil`。
- `HeaderErr != 0`：`code=HeaderErr`，`data` = 原始 body 字符串（与 `networkListen` 一致）。
- route 解析、`recordBytes`、`rememberHeaderErr` 沿用 `networkListen` 同款逻辑（抽公共或直接复用；**不要**走 `withReleasedMu`——非阻塞 pop 不阻塞、不需释放 luaMu，直接调即可）。

实现建议：新增 `networkTryListen(L, protocol)` 私有函数，与 `networkListen` 平行（不复用其阻塞循环）。`networkTryTCPListen`/`networkTryUDPListen` 各一行委托。

**注册**：在 network Lua 模块表注册处（grep `tcp_listen`/`udp_listen` 的注册点，如 api_network.go 内的模块构造或 runtime.go），加 `"try_tcp_listen": networkTryTCPListen`、`"try_udp_listen": networkTryUDPListen`。

> 注意：try_* **不**走 `withReleasedMu`（非阻塞、瞬时）。这是 2-D 删锁前的过渡——当前 luaMu 仍在，但 try_* 不持有它。

### 2. `sync_frame_data.lua` 主流程消费（保留 battleAck）

在 `execute(r)` **开头**（line 22 读 battleAck **之前**）插入非阻塞消费最新 ack：
```lua
-- 非阻塞消费最新服务端 ack 帧（CMD=4, ACT=11），更新 battleAck（取代已下线的 listen_frame_data 回调）。
-- queueSize=1 保证取到的是最新；无 ack 则 battleAck 保持上轮值（松散「保最新」语义）。
local code, data = network.try_udp_listen("battle", {cmd=4, act=11})
if code == 0 and type(data) == "string" and #data >= 16 then
    local b1, b2, b3, b4 = string.byte(data, 13, 16)
    if b1 and b2 and b3 and b4 then
        robot.set("battleAck", b1 + b2 * 256 + b3 * 65536 + b4 * 16777216)
    end
end
```
- 解析逻辑与旧 `listen_frame_data.lua:42-53` **逐字一致**（byte[13..16] 小端 uint32）。
- **不**保留 `frameDataInvalidCount`（无读者，纯诊断，随回调下线）；无效数据（code!=0 / 短包 / 缺字节）静默跳过，battleAck 保持上轮值。
- 后续逻辑（读 battleAck、构帧、udp_send、sleep 60）**不动**。

### 3. flow.json `frameData` listen def（flow.json:2090-2092）

`{"script":"listen_frame_data.lua"}` → `{}`（纯缓存 listen：无 proto、无 store、无 script）。listenRef（:385-394）不变，仍把 route {cmd:4,act:11} 注册到 udp:battle 连接的 queueSize=1 queue。

### 4. 删除 `conf/scripts/listen_frame_data.lua`

整文件删除（回调脚本不再被引用）。

### 5. `robot.go`：删 Script 分支 + fail-loud（robot.go:716-767, 784-829）

- **`RegisterListen` fail-loud**：在 `cbDef, ok := h.flow.Listen(ref.Listen)` 取到 cbDef 后、调 `createListenCallback` 前，加：
  ```go
  if cbDef.Script != "" {
      return fmt.Errorf("监听 %q 仍配置已废弃的 script %q；v2 不再支持 listen 脚本回调，请改用主流程 tcpListen/udpListen（或 network.try_*_listen / tcp_listen）或声明式 store 消费",
          ref.Listen, cbDef.Script)
  }
  ```
  （中文、fail loud、带 listen+script 上下文。frameData 的 script 在本任务同步移除，故无现有配置触发。）
- **删 `createListenCallback` 的 Script 分支**（784-829）：保留 store 分支（831-867）与 `return nil`（无 proto/store）。删除后 `createListenCallback` 只剩「s2cProto+Store → store 回调；否则 nil」。

## 不变量 / 语义保持

- **battleAck 追踪保留**：旧=push 回调每帧写；新=sync loop 每轮非阻塞 pop 最新写。queueSize=1 保最新 → battleAck 始终反映「最近收到的服务端 ack 帧」。松散语义下二者等价（sync loop 只需最新值，不需每帧）。
- **listenLoop 不再跑业务 Lua**：frameData 回调下线后，listenLoop 只做 decode→分发/缓存/Go-store，不触碰 LState（2-D 删锁的最后一处异步 Lua 清除）。
- **frameData route 仍注册**（listenRef 不变），queueSize=1 缓存供 try_udp_listen 消费。

## 关键约束

- **原子提交**：5 步必须一起完成（否则 flow 加载失败或 battleAck 断流）。implementer 不 commit；controller 在批次边界统一提交。
- **不写兼容兜底**：`ListenDef.script` 直接 fail-loud（不留「忽略 script」的静默路径）；不写「script→store 自动迁移」。
- **不改 udp_listen/tcp_listen 语义**：新增 try_* 并存。
- 日志/错误中文；godoc；`go build ./...` + `go vet` 通过。
- **Windows 环境注**：`gofmt -l` 把工作树 .go 标脏是 autocrlf CRLF 现象，**不要**对单文件 `gofmt -w`；校验 canonical 用 `sed 's/\r$//' f.go | gofmt -l`。
- **不要 git commit。**

## 工作方式

1. **先读** `conf/scripts/sync_frame_data.lua`、`conf/scripts/listen_frame_data.lua`、`script/api_network.go` 的 `networkListen`（801-914）与 network 模块注册处（grep `tcp_listen` 注册）、`robot/robot.go` RegisterListen（716-767）+ createListenCallback（784-829）、`conf/flow/flow.json` frameData（2090-2092）与 ConnectBattleUDP listenRefs（385-394）。
2. **Go 先行（可单测）**：
   - RED：`script/api_network_test.go`（若不存在则新建）单测 `networkTryListen` 的纯逻辑（若有现成 Lua 测试设施；若无，至少编译期保证注册 + 一个最小 Lua 调用 smoke）。若 Lua 测试设施成本高，跳过 Go 单测，但必须在报告说明。
   - RED：`robot/register_listen_test.go` 追加：`RegisterListen` 对 `cbDef.Script != ""` 返回中文 error（构造一个带 script 的 ListenDef + flow mock，或抽一个 `validateListenDef(cbDef)` 纯函数单测——推荐抽纯函数便于 TDD）。
   - GREEN：实现 try_* + 注册 + RegisterListen fail-loud + 删 Script 分支。
3. **配置/Lua 迁移**：改 sync_frame_data.lua（开头插消费）、flow.json frameData（去 script）、删 listen_frame_data.lua。
4. `go build ./...` + `go vet ./...` + `go test ./script/... ./robot/... ./engine/... ./network/... -count=1` 全绿。
5. **静态确认**：全仓 grep `listen_frame_data`（应仅 plans/docs 历史引用，conf/scripts/ 与 flow.json 零引用）；grep `createListenCallback` 的 Script 分支已删；`ListenDef.script` fail-loud 就位。
6. **不要 git commit。**

## 验收（self-review）

- `network.try_tcp_listen` / `try_udp_listen` 非阻塞单次 pop（不轮询、不持 luaMu、不 sleep）；注册就位；返回原始 body（不解析 proto）。
- `sync_frame_data.lua` 每轮开头消费最新 ack 写 battleAck（解析与旧回调逐字一致）；后续逻辑不动。
- `frameData` listen def 无 script（纯缓存）；`listen_frame_data.lua` 已删；frameDataInvalidCount 随之下线。
- `createListenCallback` Script 分支已删；`RegisterListen` 对 `cbDef.Script != ""` fail-loud（中文）。
- go build/vet/test 全绿；grep 零残留（conf 引用）。
- **battleAck 追踪语义保留**（pull 取代 push，queueSize=1 保最新）。

## ⚠️ 运行时验证依赖（必须在报告突出说明）

本任务的**战斗流程正确性无法用单测覆盖**（需真实服务端 battle 帧交互）。implementer 只保证：编译/单测绿 + 静态迁移完整 + 语义自洽。**battleAck 在主流程 pull 模型下是否与真实服务端表现一致，依赖 controller/用户按 CLAUDE.md 验证流程跑 `go run ./cmd/agent` 2~5 分钟**，确认 `BattleEnd ≥ 2`、无 error/warn、战斗不卡。报告须把此作为「待运行时验证」项列出，不得宣称「已验证战斗行为」。

## 报告

写完整报告到 `plans/declarative-codec/reports/t2-a2-2-report.md`：实现内容、try_* 非阻塞 pop 设计、sync_frame_data.lua 迁移（push→pull）、frameData 配置变更、createListenCallback 瘦身 + fail-loud、TDD、改动文件、self-review、**运行时验证依赖（battle 流程）**、concerns。

返回（<15 行）：Status、改动文件、一行测试摘要、运行时验证待办、concerns、报告路径。
