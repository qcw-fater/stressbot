# Robot Lua 单线程化 / 反应路径声明式改造设计

> 状态：设计方案（待 Phase 0 核实后实施）
> 范围：`robot` / `script` / `network` / `engine` / `conf`
> 目标：从机制层消除「Robot Lua VM 被并发踩栈」这一整类 bug，而非在错误机制上打补丁。

---

## 1. 背景与问题

### 1.1 现象

长时间压测中，`RankedTeamPrepare` 动作偶发失败，错误统一为：

```
[framework/53] script=ranked_team_prepare.lua: 执行脚本失败:
runtime error: invalid memory address or nil pointer dereference
stack traceback:
    [G]: in function 'type'
    listen_team_join.lua:17: in function 'safeString'
    listen_team_join.lua:99: in function <listen_team_join.lua:67>
    [G]: in function 'pcall'
    listen_team_join.lua:67
    [G]: in function 'sleep'
    ranked_team_prepare.lua:150
```

错误率很低（约 0.075%），且**不污染下一轮**（panic 后 `RankedTeamReset` 兜底成功，下一轮可正常 solo→匹配→战斗）。但它是框架级错误，且无法用 Lua 侧逻辑消除。

### 1.2 两次失败的 Lua 修复

曾尝试在 `listen_team_join.lua` 内加固：

1. 第一版：`safeString` 只对 string/number/boolean 调 `tostring`，userdata 返回占位串。→ panic 仍在。
2. 第二版：把 `safeString`/`safeNumber` 改成「先 `type(v)` 判别，永不裸 `v == nil`」。→ **panic 从 `v == nil` 行移到了 `type(v)` 行（`[G]: in function 'type'`）**，仍未消除。

### 1.3 铁证：这不是「传了坏值」，而是 VM 栈被踩坏

panic 栈最终停在 `[G]: in function 'type'`，即 `type(v)` 本身崩溃。而：

- 该调用在 `listen_team_join.lua:99`：`safeString(setErr)`，`setErr` 来自 `share.hash_set`。
- 读 `script/api_share.go:83` 的 `pushResult`：错误以 `lua.LString(err.Error())` 推回 Lua，是**普通 Lua 字符串**（模块注释亦明确「err ~= nil 为字符串错误」）。
- 在正常的 gopher-lua 里，`type("字符串")` **绝不可能** nil-pointer panic。

`type()` 只读类型标签、不解引用 Go 指针。它能 panic，唯一解释是：**调用 `type()` 时，该 LState 的栈已经处于错乱状态**。栈错乱只可能由「两个 goroutine 同时在这套栈上跑/挂起 Lua」造成。而且这是 Go 层 panic，**穿透 Lua `pcall`**（pcall 只能捕获 Lua 错误，捕获不了 Go panic），所以冒泡成 `framework/53` 动作失败。

结论：**根因不在脚本值，而在 VM 的并发访问机制。** 任何在 Lua 侧加 `type()/==nil/tostring` 防御都防不住——因为问题发生在值进入函数之前，栈已经坏了。

---

## 2. 错误机制剖析

### 2.1 当前并发模型

每个 Robot 持有：

- 1 个 gopher-lua `LState`（**非线程安全**）；
- 1 把 `luaMu`（`script/runtime.go:45`，`Context.LuaMu`）。

三类逻辑会跑 Lua：

| 逻辑 | 运行 goroutine | 触发方式 |
|---|---|---|
| 主流程（flow 动作） | Robot 主 goroutine M | Executor 同步遍历流程图 |
| 监听回调（带 `script` 的 listen） | listenLoop goroutine L | 推送到达即分发（`dispatchListen`，`cb!=nil` 分支） |
| 动态心跳 builder | 心跳定时 goroutine H | 每 tick 一次（`runHeartbeat`） |

阻塞型 Lua 函数（`tcp_request` / `tcp_listen` / `utils.sleep` / `share.*`）通过 `withReleasedMu`（`script/api_network.go:21`）**在阻塞期间释放 `luaMu`**，本意是让心跳 builder 等其他逻辑不被饿死。

### 2.2 gopher-lua 的隐含不变量

> **一个 LState 上，同一时刻最多只能有一个「从 Lua 调进 Go 并已挂起」的调用。**

原因：Go 函数从 Lua 被调用时，VM 已在共享栈上压入调用帧；该 Go 函数返回前，栈上这些帧不能被另一段 Lua 执行改写。gopher-lua 不支持「挂起一个 Go→Lua 边界调用、先去跑别的 Lua」。

### 2.3 当前设计如何破坏该不变量

踩栈的完整链路（以 `listen_team_join` panic 为例）：

1. 主流程 M 跑到 `ranked_team_prepare.lua:150` 的 `utils.sleep(500)`。
2. `utilsSleep`（`script/api_utils.go:480`）经 `withReleasedMu` **释放 `luaMu`**，M 挂起在 `time.After` 上。→ **第 1 个挂起的 Go→Lua 调用**（sleep）。
3. 推送到达，listenLoop goroutine L 抢到 `luaMu`，运行 `onMessage`。
4. 回调内调用 `share.hash_set` → `withReleasedMu` **再次释放 `luaMu`**，L 挂起在 Redis 调用上。→ **第 2 个挂起的 Go→Lua 调用**（hash_set）。
5. 此刻 `luaMu` 空闲。M 的 `time.After` 到期 → M 重新获取 `luaMu` → **在同一个 LState 上继续跑 `ranked_team_prepare`**（sleep 之后的字节码）。此时 L 的回调帧还挂在栈上没返回。
6. M 的 Lua 执行改写了共享栈；L 的 Redis 调用返回后重新获取 `luaMu`，向一个被 M 改写过的栈 `L.Push` → 栈错乱。
7. 后续任意 VM 操作（此处是回调里的 `type()`）读到垃圾 → Go panic → 穿透 pcall → `framework/53`。

> 注：心跳 builder（H）在主流程阻塞期间运行，同样可能成为「第 2 个挂起点」；只要 builder 或回调里调了任何阻塞函数就会触发。`network/heartbeat.go:58` 的注释已经意识到 `luaMu` TryLock 的脆弱，说明这套机制本就在「伺候并发」而非「消除并发」。

### 2.4 为什么补丁式修复不可接受

- **方案 ①（回调内 `withReleasedMu` 变 no-op）**：只是堵住「回调内释放」这一个窗口。主流程自身阻塞时释放 `luaMu`、心跳 builder 仍可能在该窗口跑 Lua——只要未来任何回调/builder 里出现阻塞调用，同类 bug 立刻复活。它在伺候错误机制，而非纠正机制。
- 用户原则：**不在错误架构上逐 bug 打补丁**。正确做法是让「两个挂起的 Go→Lua 调用」这件事**结构上不可能发生**。

### 2.5 已有的成功先例（本仓库）

`network/connection.go:406` 的 `StartDecodeLoop` 注释记录了一次同性质的架构修复：曾因 OnTraffic 在 gnet event loop 上同步跑 Lua Decode，导致「CONN_DROPPED 雪崩」；解法是把 Lua Decode 从 event loop 摘到独立 decode goroutine + 独立 LState **池**，让 event loop 永不接触 Lua。

**本设计（方案 B）正是把同一原则推广到 Robot 反应路径**：让推送分发与心跳构造永不接触 Robot 的 Lua VM。这与仓库已验证的模式一致，而非另起炉灶。

---

## 3. 设计目标与核心不变量

### 3.1 目标

1. 从结构上消除「Robot Lua VM 被并发踩栈」整类 bug。
2. 删除产生该 bug 的机制（`luaMu` + `withReleasedMu` 的「阻塞时释放锁」模式），而非保留并打补丁。
3. 保留现有能力：推送即时处理、定时心跳、跨 Robot 的 Redis 协调、多排组队成功率。
4. 契合 `CLAUDE.md` 已声明的设计哲学——「心跳、监听通过 JSON 配置 + 声明式动作表达，少量难通用行为才用 Lua」。当前带 `script` 的 listen 与动态心跳 builder **本就是对这一哲学的偏离**，本设计是回归。

### 3.2 核心不变量（B 成立后必须始终成立）

> **一个 Robot 的 Lua VM，有且仅有一个 goroutine 会触碰——主流程所在 goroutine。**
> **监听推送、心跳 tick 一律以 Go 原生方式处理，只读写线程安全的 state store，绝不调用 Robot 的 Lua VM。**

该不变量成立后：

- 主流程是唯一 Lua 线程，它阻塞时**没有任何别的 Lua 会插进来** → 不存在「第 2 个挂起的 Go→Lua 调用」 → 踩栈结构上不可能。
- `luaMu` 失去存在意义 → 可整体删除。

---

## 4. 方案 B：声明式反应路径

### 4.1 总体思路

把「会触碰 Robot Lua VM 的逻辑」收敛到**唯一一处**——主流程。其余两类异步逻辑改造为 Go 原生：

| 子系统 | 现状（触碰 Robot VM） | 改造后（Go 原生） |
|---|---|---|
| 监听推送 | 带 `script` 的 listen → `dispatchListen` 跑 Lua `onMessage` | 声明式 `store` 映射（Go 解析 proto → 写线程安全 state）；或 `cb==nil` buffer 由主流程 `tcpListen` 消费 |
| 心跳 | 动态 builder 每 tick 跑 Lua | 声明式 binding（从 state 取值）+ 定时 `tcpSend`，Go 原生构造/编码 |
| Redis 协调 | 主流程内（安全）+ 回调内（危险） | 全部在主流程内（唯一 Lua 线程，天然安全） |

### 4.2 三类顾虑的取舍解法

#### 顾虑一：listen 的 `script` 与 `tcpListen` 如何保证即时性？

`tcpListen` 与 B 都由推送事件直接唤醒；B 只是把「到达即跑 Lua」换成「到达即用 Go 原生写 state」。因此：

- **数据落地由消息事件直接触发**（与 listen script、tcpListen 都不受定时检查间隔限制）。
- 唯一挪走的是「到达即跑 Lua 逻辑」，挪进主流程。

**而这次挪动实际上零延迟损失**：现在回调「即时」产出的标志位/Redis 值，下游消费本就是**轮询**的（队员主流程在 `utils.sleep` 循环里轮询 join 结果；队长在 `waitForJoinedCount` 里轮询 Redis）。所以「即时回调」从来不是端到端即时——它喂给的是个轮询消费者。B 把「写标志位」从回调挪到主流程自己的轮询循环里：推送到达 → state 瞬间写好（Go 原生）→ 主流程下一轮 poll 读到 → 单线程内安全地写 Redis/置标志。**端到端延迟与现在完全一致**（都卡在下游 poll），却消除了 VM 并发。

> 唯一真正损失的场景：「主流程卡在长阻塞时，要即时响应一个**未预期**推送」。压测流程里推送都是预期且被轮询的，该场景不存在。

#### 顾虑二：心跳是 Lua 回调，如何取舍？

`script/api_network.go:1047` 的 `registerHeartbeat` 已有两种模式：

- **静态心跳**（不传 builder）：body 固定，注册时一次性预编码，运行时**零 Lua、零 luaMu**（`api_network.go:1098`）。
- **动态心跳**（传 builder）：每 tick 调 Lua builder + `TryLock(luaMu)` —— 这就是第二个 Lua 线程。

B 的取舍：**动态 builder 退化为声明式**。心跳本质 = 「每 N ms 用一组 binding 从 state 取值构造消息发出去」= **带定时器的 `tcpSend`**。引擎已有 `tcpSend` + binding 机制，复用即可：定时 goroutine 取 state（线程安全）→ Go 原生按 binding 构造 proto → 经 adapter 编码（adapter 用的是独立 Lua 池，与 Robot LState 无关，属已隔离关注点）→ 发送。全程不碰 Robot VM。

> **双模式补充（T2-B 落地时确立）**：stressbot 是通用工具，须覆盖主流游戏服的两类心跳——① **proto 心跳**（多数 protobuf 服）走 `c2sProto`+`bindings`（复用 tcpSend proto 构建，即本段原文方案）；② **raw-binary 心跳**（C++ 自研协议服 / 实时战斗同步，如本项目 battle 心跳：无 proto、wire format 是自定义小端二进制而非 protobuf）走声明式 `heartbeatFields` LE 布局（`engine.BuildHeartbeatBody`，Go-only）。两者互斥、同为 Go-only、都不碰 Robot VM。原方案只覆盖①，②是 raw-binary 现实的必要扩展。

> 注：adapter 编解码用的是独立 Lua 池（见 `StartDecodeLoop` 先例），本就不与 Robot LState 共享，不受本设计影响。

#### 顾虑三：Redis 协调怎么办？

`share.*` 调用分两处：

1. **主流程内**（队长招募、队员入队、start_match 写 team 状态……）：本来就在主流程。B 下主流程是**唯一** Lua 线程，它阻塞调 share 时**没有别的 Lua 会插进来** → 天然安全，无需任何额外保护。
2. **`listen_team_join` 回调内**的成员状态写入：挪进主流程（见 4.3），单线程内跑。

协调延迟：成员的 Redis 写入从「回调即时」变成「主流程 poll 到 join 后写入」。但队长侧本就 `waitForJoinedCount` 轮询 Redis，协调全程跑在 poll 时间尺度上——把成员侧写入挪到主流程 poll，端到端不变。

### 4.3 附带收益：删除 `luaMu` / `withReleasedMu`

不变量成立后，`luaMu` 这套「阻塞时释放锁」机制失去存在意义（它唯一作用是让异步 Lua 在主流程阻塞期间跑；而 B 后没有异步 Lua）。可分阶段**整体删除**：

- `script/runtime.go:45` 的 `Context.LuaMu`；
- `script/api_network.go:21` 的 `withReleasedMu` 及其在 `api_share.go` / `api_utils.go` / `api_network.go` 的全部调用点；
- 阻塞型函数简化为「直接阻塞当前 goroutine」（响应 ctx 取消即可），不再需要「释放-重获」锁。

**bug 类不是被「防住」，而是失去存在的土壤**——这正是用户要求的「优秀机制」。

---

## 5. 现状事实核查（已读码确认）

| 事实 | 位置 | 含义 |
|---|---|---|
| 监听分发双模式已存在 | `network/connection.go:372` `dispatchListen` | `cb!=nil`→跑脚本回调；`cb==nil`→写入 `listenMsg` 缓冲。B 只需删 `cb!=nil` 的脚本路径，buffer+poll 基础设施现成。 |
| 主流程消费缓冲已存在 | `network/connection.go:391` `GetListenResp` | `tcpListen` 动作已通过它消费缓冲。 |
| 声明式 store 映射已存在 | listen 配置的 `s2cProto` + `store`（如 `stateUpdate`） | Go 原生解析 proto + 写 state 已实现，可直接用于 B。 |
| 静态心跳零 Lua 已存在 | `script/api_network.go:1098` | 心跳「不碰 VM」模式有先例。 |
| 动态心跳是第二 Lua 线程 | `script/api_network.go:1047` + `network/heartbeat.go:90` | 确认顾虑二，需改造。 |
| share 错误是 Lua 字符串 | `script/api_share.go:83` `pushResult` | 证明 panic 非「坏值」，是栈错乱（见 1.3）。 |
| `luaMu` 定义 | `script/runtime.go:45` `Context.LuaMu` | 删除目标明确。 |
| 同性质修复先例 | `network/connection.go:406` `StartDecodeLoop` | 仓库已用「Lua 移出热路径」模式修过并发雪崩，B 与之同构。 |

### 当前带 `script` 的 listen（需改造的 3 个）

均位于 `conf/flow/rank.json` 的 `listens`：

1. **`teamUpdateInfo`**（5:3，`listen_team_update_info.lua`）：解析队伍更新字段写 state。→ 基本是纯 store 映射，可直接声明式化。
2. **`teamNotifyInvite`**（5:6，`listen_team_notify_invite.lua`）：解析邀请，过滤 `model==2`，写 `rankedTeamInvite` + `rankedTeamInviteReceived`。→ store 映射 + 一个 `model==2` 过滤（可声明式 filter，或主流程读 state 时判 model）。
3. **`teamJoin`**（5:10，`listen_team_join.lua`）：解析 code/teamId 等写 state；`code==0` 时置 `rankedTeamAcceptDone` + 写 Redis 成员状态。→ **panic 源头**。store 映射负责字段；条件 + Redis 写入挪进主流程（`ranked_team_prepare` 成员 join 检测处）。

---

## 6. 改造范围与分阶段计划

每阶段**独立可发布、可回归**。Phase 1 单独即消灭 panic；Phase 2–3 完成架构收敛。

### Phase 0 — 核实（无行为变更）

- 逐个读 3 个 `listen_team_*.lua`，列出各自逻辑、依赖的 state 键、Redis key，标注「可声明式化 / 需挪主流程」。
- 核实实际 flow 配置里心跳是否用了动态 builder（Lua），及 builder 具体构造了什么字段。
- 为每个「需挪主流程」的 listen 事件，定位主流程中已有的对应消费/轮询点（确认无新增延迟）。
- 全仓审计：除 listen-script 与动态心跳外，是否还有**任何**在 Robot LState 上的异步 Lua 路径。

### Phase 1 — 监听声明式化（消灭 panic）

目标：删除 `dispatchListen` 的脚本回调路径，监听全走声明式。

- `conf/flow/rank.json` / `flow.json`：3 个 listen 去 `script`，改 `s2cProto` + `store`。
  - `teamUpdateInfo` → store 映射。
  - `teamNotifyInvite` → store 映射（含 `inviteInfo.playerId` 等嵌套字段，`StoreMapping.field` 已支持嵌套路径）；`model==2` 过滤用声明式 filter 或主流程判 model。
  - `teamJoin` → store 映射（teamJoinCode/teamId/teamModel/teamGType/teamModeId/teamTsId）。
- `conf/scripts/listen_team_join.lua`：删除该回调。`code==0` 的 `rankedTeamAcceptDone` 与 Redis 成员状态写入，挪进 `ranked_team_prepare.lua` 成员侧 join 检测处（主流程观察到 `state:teamJoinCode==0` 时执行）。
- `conf/scripts/listen_team_notify_invite.lua`、`listen_team_update_info.lua`：删除或精简。
- `network/connection.go` / engine 监听分发：确保 `cb!=nil` 仅剩「Go 原生 store 映射 cb」，脚本 cb 路径下线。
- **验收**：长跑，`framework/53 ... listen_team_join` 归零；多排入队成功率不降。

### Phase 2 — 心跳声明式化

目标：删除动态心跳的 Lua builder 路径。

- 心跳配置改为声明式：`route` + `bindings`（从 state 取值），定时 `tcpSend`。
- `script/api_network.go` `registerHeartbeat`：下线 Lua builder 分支；保留/扩展静态预编码 + 运行时 Go 原生 binding 求值。
- `network/heartbeat.go` `runHeartbeat`：`Builder()` 改为 Go 原生（取 state → 构造 → adapter 编码）。
- **验收**：心跳按间隔稳定发送，字段正确，不再 `TryLock(luaMu)`。

### Phase 3 — 删除 `luaMu` / `withReleasedMu`

前置：Phase 0 审计确认「无任何异步路径触碰 Robot LState」。

- `script/runtime.go`：移除 `Context.LuaMu`（或保留为 no-op 作过渡）。
- 阻塞型函数（`api_share.go` / `api_utils.go` / `api_network.go` 的 `tcp_request`/`tcp_listen`/`sleep`/`share.*`）：去掉 `withReleasedMu` 包裹，改为直接阻塞 + 响应 ctx。
- 删除 `withReleasedMu`。
- **验收**：编译通过；长跑无 panic；阻塞行为正确（主流程阻塞期间网络收发、心跳仍由各自 goroutine 正常工作，因它们已不依赖 `luaMu`）。

### Phase 4 — 长稳验证

按 `CLAUDE.md` 验证流程：`go build ./...` → `cd cmd/web && npx tsc -b` → `npm run test` → flow.json 校验 → 跑 1–2 小时 → 日志 `grep -i "error\|warn\|失败"` 无异常。重点确认：

- 零 `framework/53` 与 nil-pointer panic；
- 多排入队成功率 ≥ 现状；
- 排位开窗期内匹配/战斗正常；
- 心跳不丢、推送字段正确落地。

---

## 7. 涉及文件清单

| 文件 | 阶段 | 改动 |
|---|---|---|
| `conf/flow/rank.json`、`conf/flow/flow.json` | 1 | listen 去 `script` 改 `store`；心跳声明式化 |
| `conf/scripts/listen_team_join.lua` 等 3 个 | 1 | 删除 / 逻辑挪主流程 |
| `conf/scripts/ranked_team_prepare.lua` | 1 | 接收 teamJoin 的条件 + Redis 写入 |
| `network/connection.go` | 1 | `dispatchListen` 脚本 cb 路径下线 |
| `engine/*`（监听 store 分发） | 1 | 确认/补齐声明式 store 映射分发 |
| `script/api_network.go` | 2、3 | 心跳 builder 下线；`withReleasedMu` 删除 |
| `network/heartbeat.go` | 2 | builder Go 原生化 |
| `script/api_share.go`、`script/api_utils.go` | 3 | 去 `withReleasedMu` |
| `script/runtime.go` | 3 | 去 `Context.LuaMu` |

---

## 8. 风险、迁移与回滚

### 风险

- **teamJoin 逻辑迁移**：条件 + Redis 写入挪主流程后，必须保证多排协调语义不变（成员 join 仍被队长及时观测到）。需重点测试双排/三排。
- **声明式 expressiveness**：个别 listen 若依赖 `get_field_map` 全量解析或复杂条件，可能超出当前 `store`/filter 表达力。Phase 0 必须逐个核实；若个别无法声明式化，则该推送走 buffer + 主流程消费（`tcpListen`），仍不碰 VM。
- **心跳 builder 能力**：若现有 builder 做了 binding 表达不了的事（如自增序号、动态路由），需在声明式配置中补少量 Go 原生能力（序号由 Go 维护计数器）。
- **删 `luaMu`**：必须在 Phase 0 确认无遗漏的异步 Lua 路径，否则过早删除会引入新并发 bug。建议 Phase 3 先把 `withReleasedMu` 改 no-op 观察一轮，再物理删除。

### 迁移

- 遵循 `MEMORY.md`「禁止兼容性兜底」：不写自动迁移函数、不保留 `??` fallback。listen 的 `script` 字段直接从配置删除，全链路一致更新。
- 配置与脚本同 commit 更新，避免半迁移状态。

### 回滚

- 每阶段一个 commit，独立可回滚。
- Phase 1 回滚点最关键（行为面最大）；若多排成功率回归，单独回滚 Phase 1 即可恢复脚本回调，不影响后续架构收敛判断。

---

## 9. 与其他方案对比

| 方案 | 不变量 | 是否删错误机制 | 代价 | 评价 |
|---|---|---|---|---|
| ① 回调内不释放锁 | 回调不产生第 2 挂起帧 | 否（保留 `luaMu`） | 极小 | 补丁；伺候错误机制，未来易复发 |
| ③ 回调专用 LState | 各 Lua 线程独立 VM | 部分 | 每 Robot 2× Lua 内存 | 干净但压测量级下内存不划算；用户已排除 |
| ④ 单协程 + 协程化调度（actor） | 单 goroutine 触碰 VM | 是（但需协程化） | 把所有阻塞函数改 `coroutine.yield` + 调度器，横跨 network/utils/share/engine | 最灵活、保留「回调里写 Lua」；但为不该有的能力付出整套协程运行时代价 |
| **B 声明式反应路径（本方案）** | **主流程是唯一 Lua 线程** | **是（删 `luaMu`）** | 中等（迁移 3 listen + 心跳声明式化） | 消除并发而非管理并发；契合 `CLAUDE.md` 声明式哲学；有 `StartDecodeLoop` 成功先例 |

---

## 10. 待 Phase 0 核实的开放问题

1. 3 个 `listen_team_*.lua` 的完整逻辑清单（逐字段、逐 Redis key），确认声明式化边界。
2. 实际 flow 配置中心跳是否动态 builder；若是，builder 构造的字段与逻辑（是否有 binding 表达不了的部分）。
3. 每个「挪主流程」的 listen 事件，主流程侧消费点是否就绪、是否真无新增延迟。
4. 全仓是否存在 listen-script / 动态心跳以外的「Robot LState 异步 Lua」路径（如某些 adapter 回调、GM 回调等）。
5. `store` 映射对子消息嵌套字段（如 `inviteInfo.playerId`、`matchData.sessionId`）的覆盖度，是否满足 3 个 listen 的字段提取需求。

---

## 附录 A：为什么 `type()` 会 panic（机制小结）

gopher-lua 的 `type()` 内部只读值的类型标签（`lua_type` → 读 `TValue.tt`），正常情况下不会解引用 Go 指针。但当 LState 栈被并发改写后：

- `type(v)` 取的 `v` 来自一个被另一 goroutine 改写过的栈槽，可能是半写入的 `TValue`（类型标签与 union 不一致）；
- 或 `v` 指向一个已被 GC/挪动的对象，类型标签读取阶段即触发 nil 解引用。

因此 `[G]: in function 'type'` 的 panic 不是「`type` 对某合法值崩溃」，而是「`type` 读到了非法栈内容」。这反向印证：**栈在被并发踩踏**，而非传入的 `setErr`（一个合法 Lua 字符串）有问题。
