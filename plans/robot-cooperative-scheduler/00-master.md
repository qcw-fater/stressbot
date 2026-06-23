# Robot 协作式 Lua 调度器 — 总纲

> 目标：把 Robot 从「同步执行一个 Lua 脚本直到返回」升级为「单线程协作式 Lua 调度器」（skynet 式 actor + coroutine 模型）。
> 前置：`declarative-codec` 已落地（connectionPump 单 owner goroutine、listen_queue、codec_resolver）。本计划与 codec 正交——codec 管线缆格式，本计划管 Robot 运行时并发模型。
> 状态：方案已经源码 + spike 验证（见 §2），等待拆 track 落地。

---

## 1. 背景与问题

v2 出于「业务 Lua LState 单所有者」目标，把 `ListenDef.script` fail-loud 禁掉了（`engine/flow.go:278` 标注「已废弃」），原因不是「Lua 回调概念错」，而是**旧执行方式**错：

```
network goroutine 收到推送 → 抢 luaMu/LState → 在网络线程里跑 Lua onMessage
  → 与主流程并发争用 LState（非线程安全）→ 死锁 / 抖动 / 阻塞网络线程
```

当前 `guild_drain.lua` 这类「主流程每轮开头 try_listen 批量 pop」是**局部补丁**，不实时、每模块都要手插 drain、常驻推送难接入，mail/activity/friend/redpoint/roleState 都会再撞同样的问题。

**根因有两层**：
1. 后台 goroutine（pump / 心跳）想用 Lua，但 Lua 不可重入；
2. 即使后台不碰 Lua，主流程一个 `tcp_listen`/`sleep` 阻塞 60s，也会让 listen 回调 / 心跳构造 / timer 全部饿死（§9）。

只做「任务队列」解决第 1 层，解决不了第 2 层。第 2 层必须靠 **coroutine yield** 在阻塞点让出 VM。

---

## 2. 已验证结论（源码 + spike，非纸面推理）

针对本方案最关键、最易踩坑的「Go 函数能否 yield」做过实测（`gopher-lua v1.1.2`）：

### 2.1 核心机制成立 ✅

- `LState.Yield(...)` 即 `return -1`（`state.go:2217`）。
- VM 在 `callGFunction` 见 Go 函数返回负值 → `switchToParentThread` 挂起协程、控制权交回 `Resume` 调用方（`vm.go:200-210`）。
- spike「顶层 await」：连续两次 yield，scheduler `Resume(co, nil, code, msg)` 喂回的值**直接成为 Lua 里该调用的返回值**。跑通。

**机制要点（设计必须遵守）**：await 型 Go 函数在 `Yield` 处**彻底返回**，resume **不会**回到该 Go 函数内部。所以 `tcp_listen` 必须在 yield 前算完「等什么」（构造 WaitSpec），yield 出去；**最终返回值由 scheduler resume 时喂回**，Go 侧不能在 resume 后做后处理。

### 2.2 两条静默陷阱（spike 实测，文档此前未提，必须做成 fail-loud）⚠️

| 场景 | spike 实测结果 | 危害 |
|---|---|---|
| `await_*` 在 `pcall`/`xpcall` 内 | 协程首次 resume 即以 OK 结束、返回 waitSpec 垃圾，await 后代码不执行 | **静默错乱**，不报错 |
| `await_*` 在脚本自建 `coroutine.create` 内 | yield 被内层 `coroutine.resume` 截获，到不了 scheduler | await 等错地方 |

两者同源：gopher-lua（同 Lua 5.1）yield 只能到达**最近一层** resume，无法跨 pcall 的 Go 边界、也越不过用户 coroutine。

→ **硬约束**：await API 运行时必须校验「自己处于 scheduler 直接 resume 的顶层协程」，否则 `RaiseError` fail-loud；绝不允许静默 yield 到错误层级。

---

## 3. 目标架构

### 3.1 模型：每 Robot 一个单线程协作式调度器（skynet 式）

```
每个 Robot：
  ┌─ Robot Scheduler goroutine（唯一 Lua owner，独占该 Robot 的 LState）
  │    run loop：
  │      select {
  │        taskCh   ← 异步来源投递的 task（listen 回调 / 心跳构造 / timer / go-only）
  │        timerCh  ← WaitSpec 截止时间到
  │        wakeupCh ← 推送/响应到达、ctx cancel
  │      }
  │      → 把可执行的 task/恢复条件满足的 coroutine 串行 Resume
  │      → 任意时刻只有一个 coroutine running（协作式，非并行）
  │
  └─ connectionPump goroutine（网络层，每连接一个，已存在）
       收包 decode → 命中 listen route → **enqueue task + notify scheduler**
       （不再在 pump 里直接跑 Lua 回调）
```

**关键不变量**：
- 后台 goroutine（pump / 心跳 timer）**只投递任务 + notify**，绝不调用业务 Lua；
- Robot scheduler 是**唯一**能调 gopher-lua 的地方；
- 任意时刻只有一个 coroutine running；只有当前 coroutine yield/return/error 后才能 resume 下一个。

**安全点（scheduler 可 drain 队列 / resume 其他 coroutine 的时机）= 两类**：
1. **node 边界**：flow 遍历一个 node 执行完、下一个 node 开始前；
2. **await yield 点**：主流程 coroutine 在 `await_*`（如 `tcp_listen`/`sleep`）处 yield、把 VM 交回 scheduler 的整个等待窗口。

第 2 类正是 §9 的解法 —— 主流程等 60s listen 期间，scheduler 用这段窗口跑 listen 回调 / 心跳构造 / timer，等条件满足再 resume 主流程。没有第 2 类，长阻塞 action 仍会饿死队列。

### 3.2 统一抽象「一个 task 一个 coroutine」

action / listen 回调 / timer / 心跳构造**统一**当成「scheduler resume 的一个 coroutine」，不做主流程 vs 回调的架构分类（差异只是「第一版谁允许 yield」的策略，不是结构）。

需要的核心组件：

- **Robot Scheduler**：执行 flow node/action、drain task queue、管理 WaitSpec、响应 wakeup、处理 cancel/timeout。
- **Robot Task Queue**：接收 listen 回调 task / 心跳 task / timer task / go-only task / deferred task。
- **WaitSpec**：coroutine yield 出的等待条件 —— `WaitSleep{deadline}` / `WaitListen{service, routeKey, s2cProto, deadline}` / `WaitResponse{service, routeKey, deadline}` / `WaitIO{done chan, deadline}`（见 §3.4 两类等待）。
- **Wakeup**：异步来源通知 scheduler（新 listen 消息 / 响应到达 / timer 到期 / ctx cancel / task enqueue）。

### 3.3 v2 listen 三形态（恢复 script，但改执行模型，无需新增 mode）

按字段判断即可，**不引入 `mode:"event"`**：

| ListenDef 形态 | 行为 |
|---|---|
| `s2cProto` + `store` | Go 侧声明式实时 store（简单无条件状态更新，最快） |
| `s2cProto` + `script` | 入 Robot task queue，scheduler 安全点串行执行 Lua `onMessage(r, msg)` |
| `{}` | 纯缓存，主流程用 `tcpListen`/`udpListen`/`try_*_listen` 主动消费 |

**`store` 与 `script` 互斥**：同时配置 fail-loud（避免一条消息既被 Go store 又被 Lua 改 state、顺序语义混乱）。真要两者就把 store 写进 Lua handler。

`ListenDef.script` 从「已废弃」**恢复为 v2 正式能力**，但语义是「入队 + 主流程安全点串行执行」，不是旧的「网络线程立即抢 Lua」。这是重新定义安全语义，不是兼容兜底。

### 3.4 等待型 API 全量清单（凡"会等"的都要协作式，不止 sleep/listen）

**原则**：任何在 scheduler goroutine 里**同步阻塞**的 Lua API，都会卡死整个 Robot 的协作式调度（等待期间其他 task 全饿死）。必须全部改成「yield WaitSpec → scheduler 等待期间干别的 → 条件满足 resume」。按"唤醒来源"分两类，机制不同：

**Class A — scheduler 已观测到唤醒事件，无需额外 goroutine**（park 协程，事件到了 scheduler 直接 resume）：

| Lua API | 现状 | WaitSpec |
|---|---|---|
| `utils.sleep` | `time.Sleep`（`api_utils.go:489`） | `WaitSleep`（scheduler timer） |
| `network.tcp_listen` / `udp_listen` | 阻塞等推送 + timeout（`api_network.go:61-62`） | `WaitListen`（connectionPump 收到推送 → notify） |
| `network.tcp_request` / `tcp_request_route` / `udp_request` / `udp_request_route` | `RequestResponse` 等回包 + timeout（`api_network.go:52-55`、`doTCPRequest:452`） | `WaitResponse`（pump decode 命中 responseMap → notify） |

> Class A 的唤醒事件（timer 到期、推送到达、响应到达）scheduler / connectionPump **本就在观测**，所以只要把"直接调回调/直接写 channel"改成"notify scheduler + 由 scheduler 匹配 WaitSpec 后 resume"，不需要新增 I/O goroutine。`WaitResponse` 因此首版就纳入（与 listen 共用 pump 通路）。

**Class B — Go 侧主动阻塞 I/O，scheduler 观测不到，需要后台 worker 实际执行**（dispatch 到后台 goroutine 跑阻塞调用 → 完成后 post 回 scheduler → resume）：

| Lua API | 现状 | 处理 |
|---|---|---|
| `share.*`（set/get/del/incr/claim/queue_*/hash_* 等全部 Redis 操作） | **同步阻塞 Robot 主流程 goroutine**（`api_share.go:29` 自注释） | `WaitIO`：投递到 per-robot I/O worker（或共享 worker pool）跑 Redis 往返，完成 post 回 scheduler |
| `network.http_request` | HTTP client 阻塞 Do（`api_network.go:56`） | `WaitIO`：同上，后台跑 HTTP |
| `network.connect_tcp` / `connect_udp` | 拨号阻塞至连接建立（`api_network.go:47-48`） | `WaitIO`/`WaitConnect`：后台拨号，完成 resume |

> Class B 比 Class A 复杂：唤醒需要"真的去做那次阻塞调用"，scheduler 不能内联做（会卡死自己）。需要一个后台 I/O 执行者（每 Robot 一个 I/O goroutine，或全局 worker pool）跑阻塞调用，完成后通过 wakeup 通道把结果交还 scheduler resume。**这是本计划里 Class B 的核心增量**，不能简单当成"和 sleep 一样"。

**无需改动（非阻塞）**：`try_tcp_listen` / `try_udp_listen`（poll 立即返回）、`tcp_send` / `udp_send`（AsyncWrite 非阻塞）、random/pack/time_ms/hash 等纯计算。

> 阶段化建议：阶段 2 先把 **Class A 全量**（sleep/listen/request）转 await（机制统一、风险低）；**Class B（Redis/HTTP/connect）单列**，在 Class A 跑通后再做（需要先定 I/O worker 形态）。**两类均已落地**（Class A：阶段 2/2.5/4；Class B：阶段 3）：所有"会等"的 Lua API 全部协作式，等待窗口内调度器持续 drain mailbox，无裸阻塞执行器的等待点。Class B 经 `WaitIO`：作业投递协程池后台跑阻塞调用，renderer 在执行器 goroutine 产出 Lua 值。

### 3.5 心跳三档（心跳 goroutine 永不直接调 Lua）

| 心跳类型 | 实现 |
|---|---|
| 空 / 固定 body / 简单 state 字段 | 继续 Go-only 声明式心跳（已有，最快） |
| Lua 复杂构造、允许轻微抖动 | 投递 `LuaHeartbeatTask`，scheduler 执行 Lua 构造并发送 |
| Lua 复杂构造、发送时机要准 | 主流程定期刷新 cached body，心跳 tick 只发缓存 body |

---

## 4. 与现有代码的接缝（已读码确认）

| 现状 | 改动 |
|---|---|
| `script/runtime.go:408` `RunActionScript` 用 `CallByParam{Protect:true}`（内含 PCall） | 改为子线程 `L.Resume(thread, fn, args)`；外包 PCall 去掉（否则挡住顶层 await），错误用 `ResumeError` 处理 |
| `network/connection.go` connectionPump 命中 listen route 后**在 pump goroutine 直接调 `ListenCallBack`** | Lua 回调改为 **enqueue task + notify scheduler**；Go-only store 回调可继续在 pump 内联（不碰 Lua） |
| `engine/flow.go:278` `ListenDef.Script`「已废弃」 | 恢复为正式字段；新增 `store`/`script` 互斥校验 |
| `script/api_utils.go` `utils.sleep`、`script/api_network.go` `tcp_listen`/`udp_listen` | 新增 `await_*` 版本（yield WaitSpec）；旧阻塞版分阶段保留/迁移 |
| `robot/robot.go` | 新增 Robot scheduler + task queue + WaitSpec 管理；每 Robot 一个 scheduler goroutine 独占 LState |
| `engine/executor.go` | 支持 action 暂停/恢复（node 遍历在 await 点可让出） |

> 天然护栏：若在非 coroutine 上下文（main LState 直接 CallByParam）调 await，`switchToParentThread` 因 `parent==nil` 抛 "can not yield from outside of a coroutine"（`vm.go:177-180`）——"忘了用 coroutine 跑"会立刻 fail-loud。

---

## 5. 阶段化落地（§18 方向正确，phase 0 已完成）

- **阶段 0 — 技术 spike**：✅ **已完成**（本计划 §2 即 spike 结论：核心机制成立 + 两条静默陷阱）。
- **阶段 1 — Robot 任务队列**：✅ **已实现**。恢复**安全版** Lua listen 回调。pump 收包 → `Robot.enqueueTask`（仅复制消息 + 投递，不碰业务 LState）→ 执行器 goroutine 在 node 边界 `RunPendingTasks` 串行执行 `on_message(r, msg)`。**此阶段只有「node 边界」一类安全点**（await 尚未引入），故仍不解决长阻塞 action，回调必须短平快（不许 yield）。
  - 落地点：`engine.ActionHandler.RunPendingTasks(ctx)`（`executor.go` `executeNode` 入口调用）；`robot.Robot.taskCh`/`enqueueTask` + `robotActionHandler.RunPendingTasks`/`runListenScript`；`script.RuntimePool.RunListenScript`（`on_message` 约定）；`validateListenDef` 改为「store 与 script 互斥」（script 不再 fail-loud）。
  - 队列满（`robotTaskQueueSize=256`）丢最新 + `taskDropped` 计数；单边界最多 drain `maxDrainPerBoundary=64` 个，防回调洪峰饿死流程。
- **阶段 2 — Class A 协作式 API**（§3.4 Class A）：✅ **已实现**（sleep + listen + request）。
  - ✅ **协程运行时**：`RunActionScript` 由 `CallByParam{Protect}` 改为子线程 `L.Resume` drive-loop（`script/coroutine.go`）。不含 await 的脚本首次 resume 即跑完，与旧行为等价。
  - ✅ **await_sleep**（`utils.await_sleep`，`WaitSleep`）+ **await_tcp_listen / await_udp_listen**（`network.await_*_listen`，`WaitListen`，复用 `listenResultValues` 与同步版返回契约一致）。**「await yield 点」安全点随之生效** —— 等待窗口内 `robotWaiter.cooperativeWait` drain 任务队列（跑 listen 回调），§9 长阻塞饿死在 sleep/listen 路径被解决。listen 首版用「轮询监听队列 + taskWake 唤醒」实现（无需改 network 层 pump notify）。
  - ✅ **await_tcp_request / await_udp_request**（`network.await_*_request`，`WaitResponse`，复用 `requestResultValues` 与同步版返回契约一致）。network 层把 `RequestResponse` 拆出 `Connection.SendRequest`（注册响应通道 + 立即发送，返回 `PendingRequest` 句柄）+ `PendingRequest.C()/Timing()/Close()`；await 函数构建编码包后 yield，由 `robot.awaitResponse` 发送并 **select 响应通道 + drain 任务队列**。关键：用通道 select（非轮询）即时唤醒，保证 `WireRTT` 测量不被轮询间隔污染。命中 / 超时（`ErrRecvTimeout`）/ 取消（`ErrActionCanceled`）/ 发送失败分别经 `WaitOutcome{Exchange|Err|Canceled}` 回传。
  - ✅ **两条静默陷阱 fail-loud**（spike §2.2）：await 在 `pcall` 内 → drive-loop 检测 ResumeOK 携带 WaitSpec 返回值报错；await 在 `coroutine.create` 协程内 → `awaitYield` 校验 `topThread` 报错；无 Waiter → 报错。均有单测覆盖。
  - 测试：`network/pending_request_test.go`（SendRequest 注册/发送/投递/Close 幂等/默认超时）、`script/request_result_test.go`（结果映射五分支）、`robot/await_response_test.go`（连接缺失 / 发送失败接线）。
- **阶段 2.5 — 对标 skynet 的 actor 运行时收口**：✅ **已实现**（本轮）。把"会等待的点全部协作式"做成统一约束，并把调度抽成独立组件。
  - ✅ **独立 `robotScheduler` 组件**（`robot/scheduler.go`）：mailbox（`taskCh`/`taskWake`/`taskDropped`）+ 统一等待 pump（`wait`）+ `enqueue`/`drain`/`awaitResponse`，Robot 持有 `sched` 委托。所有"会等待"的点经唯一 pump 收敛（actor 运行时：任何阻塞点都不裸阻塞）。
  - ✅ **wait 节点 + nodeDelay 走协作式**（`engine/executor.go` + `ActionHandler.CooperativeSleep`）：节点延迟/wait 不再裸 `time.After`，改 `sched.wait` 在延迟窗口持续 drain mailbox，§9 长阻塞饿死在 delay/wait 路径也被解决。
  - ✅ **listen 回调改 coroutine drive-loop**（`script/runtime.go` `RunListenScript`）：`on_message` 与 action 一致跑在子线程协程上，**回调内可直接 `await_*`**；嵌套（回调 await 期间 drain 出另一回调）安全（每次 resume 前重设 `topThread`）。
  - ✅ **布尔/条件脚本也协程化**（`RunBooleanScript`）：条件脚本同样可 `await_*`；至此 action / listen / boolean **三类 Lua 入口全部协程驱动**，无 `CallByParam` 残留 → 协作式 API 在任何 Lua 上下文均可用。
  - ✅ **`tcp_request_route` / `udp_request_route`**：补齐发送/响应路由分离的协作式请求。
  - ✅ **`PendingRequest.Close` 并发幂等**（`sync.Once`）。
  - ✅ **声明式等待节点也走协作式**：`engine.ActionExecutor` 注入 `SetCooperativeSleeper`，声明式 `tcpListen`/`udpListen`（`execListen` 轮询间隔）改 `Robot.cooperativeSleep`（drain mailbox）；声明式 `tcpRequest`/`udpRequest` 经 `netSenderAdapter.TCPRequest/UDPRequest` → `sched.awaitResponse`，与 Lua 请求**同一协作式路径**（`execRequest` 零改动）。至此声明式与脚本两条路径的所有等待点都不裸阻塞。engine 无 robot 时回退裸阻塞，保持解耦。
  - ✅ **API 去冗余**：`tcp_listen`/`sleep`/`tcp_request` 等 canonical 名本身即协作式，删除 `await_*` 别名（曾是阻塞/协作并存期的过渡名，现单一实现下纯冗余且 `await_` 前缀失去区分意义）。fail-loud 错误信息改用 canonical 名。
- **阶段 4 — 旧阻塞 API 转协作式**：✅ **已实现**（本轮）。`utils.sleep` / `network.tcp_listen` / `udp_listen` / `tcp_request` / `tcp_request_route` / `udp_request` / `udp_request_route` 的 **canonical 名直接指向协作式实现**，`await_*` 为同语义显式别名；旧同步实现（`networkListen`/`doTCPRequest`/`doUDPRequest`/`utilsSleep`/`pollListen` 等）已删除，单一实现路径。现有业务脚本无需改动即享协作式调度（已校验 `conf/scripts` 无在 `pcall`/`coroutine` 内调用这些 API 的反模式；`go run agent` 烟测无 panic / 协程 / fail-loud 异常）。
- **阶段 3 — Class B 协作式 I/O**（§3.4 Class B）：✅ **已实现**（本轮）。`share.*`（Redis 全部 24 个操作）/ `network.http_request` / `network.connect_tcp` / `connect_udp` 全部改成 `WaitIO`：执行器 goroutine 读完 Lua 入参后 yield，由调度器把阻塞调用投递到后台执行，等待窗口内持续 drain mailbox，作业完成返回 renderer 在执行器 goroutine 上产出 Lua 返回值。
  - ✅ **`WaitIO` + `IORenderer` + `awaitIO` 统一抽象**（`script/coroutine.go`）：`WaitSpec.IOJob func() IORenderer` 在后台 goroutine 跑阻塞调用（**绝不碰 L/业务 state**），返回的 `IORenderer` 只在执行器 goroutine 上被 `buildResumeVals` 调用产出 Lua 值（goValueToLua / ctx.recordRequest / rememberErr 等非并发安全操作都在此做）。两者经 done 通道交接（happens-before，无竞态）。
  - ✅ **`robotScheduler.awaitIO`**（`robot/scheduler.go`）：作业投递到**协程池**（非裸 go；池容量为 0 无限且执行器常驻占池 ≥ Robot 数，不会死锁），执行器同步等待期间 `drain` mailbox + `select{done, taskWake}`。不监听 `ctx.Done` 提前返回——作业均受 ctx/超时约束（share opCtx / http client ctx / 拨号超时），取消时作业会很快带正确 arity 的错误 renderer 经 done 返回，提前返回会用错 arity resume。作业 panic 由池 recover + 本地 recover 兜底（renderer=nil → 空返回值）。
  - ✅ **`share.*` 全量协作式**（`script/api_share.go`）：每函数「读 Lua 入参（执行器）→ awaitIO（后台 opContext+Redis 往返）→ renderer（执行器 goValueToLua+返回）」。新增 `resultVals`/`resultVals3` 渲染辅助（`pushResult`/`pushResult3` 的返回值版）。未启用共享状态（`Shared==nil`）仍同步返回 `ErrNotEnabled`，不 yield。
  - ✅ **`http_request` / `connect_*` 协作式**（`script/api_network.go`）：HTTP Do / 拨号在后台跑；指标累计、错误记录、ctx 取消判定都在 renderer（执行器）做，返回值契约与旧同步版完全一致。`connect` 拨号前已取消则不投递作业直接返回取消码。
  - ✅ **声明式 http/connect 也协作式（补齐最后的声明式缺口）**：`robotScheduler` 抽出通用原语 `runIO(job func())`（后台跑 job + 等待窗口 drain mailbox），`awaitIO` 即在其上薄封装。`engine.ActionExecutor` 注入 `SetCooperativeIO(sched.runIO)`，声明式 `httpRequest`/`tcpConnect`/`udpConnect` 的阻塞调用经 `ae.runIO` 包裹（仅包阻塞那一步，body 构建/timing/store/parse 仍在执行器 goroutine 串行）。至此**声明式与脚本两条路径的 http/connect 同一协作式原语**，与阶段 2.5 的声明式 listen/request 一起，声明式侧再无裸阻塞等待点。engine 无 robot（独立运行/测试）时回退同步调用，保持解耦。
  - 测试：`robot/cooperative_wait_test.go::TestAwaitIO_DispatchesAndDrains`（后台作业 + 等待窗口 drain + renderer 产出值）、`script/api_share_io_test.go`（share.set/get 走 WaitIO 往返 + 未启用不 yield）、`engine/cooperative_io_test.go::TestDeclarativeIO_RoutesThroughCoopIO`（声明式 http/connect 全经 coopIO + 无 coopIO 回退同步）。

---

## 6. 实现要点（集中在后端底层，不拆独立 track 文档）

> 本计划只动后端运行时，改动高度耦合、集中在一处，**不拆成 declarative-codec 那样的逐 track 文档**；以下为单一实现清单，按阶段推进。

- **coroutine runtime**（`script/runtime.go`）：`RunActionScript` 的 `CallByParam{Protect:true}` → 子线程 `L.Resume`；coroutine 生命周期 / `ResumeError` 错误处理；await-eligibility 守卫（拦 pcall / 用户 coroutine 内调用，fail-loud）。
- **Robot scheduler + task queue + WaitSpec + wakeup**（`robot/robot.go`、`engine/executor.go`）：单 select run loop，统一 wakeup；executor 支持在 await 点暂停/恢复 node 遍历；bound（max pending coroutine / max wait / resume batch）。
- **Class A await API + pump 接缝**（`script/api_utils.go`、`script/api_network.go`、`network/connection.go`）：`await_sleep`/`await_*_listen`/`await_*_request` yield `WaitSleep`/`WaitListen`/`WaitResponse`；connectionPump 的 listen/response 分发由「pump 内直接调回调 / 写 channel」改「notify scheduler + scheduler 匹配 WaitSpec resume」（Go-only store 回调仍可内联）；恢复 `ListenDef.script`。
- **Class B I/O worker**（`script/api_share.go` Redis、`api_network.go` http/connect）：定 per-robot I/O goroutine 或共享 worker pool；`share.*`/`http_request`/`connect_*` 改 `WaitIO`（后台跑阻塞调用 → post 回 scheduler resume）。这是 Class B 的核心增量。
- **心跳 + 配置/校验 + 文档**：心跳三档（§3.5）；`store`/`script` 互斥校验；flow-config 技能文档；Lua 协作式规范（两 yield 点间不被打断、勿用 Lua 全局存可变业务状态、状态放 robot state/share）。

---

## 7. 风险

| 风险 | 缓解 |
|---|---|
| await 跨 pcall / 用户 coroutine 静默错乱 | §2.2 硬约束：await 运行时校验顶层协程，否则 fail-loud；文档红线 + 校验 |
| coroutine 生命周期 / 内存泄漏（pending coroutine 堆积） | 从一开始就 bound：max pending coroutine、max wait timeout、max resume batch、callback 超时 |
| `CallByParam`→`Resume` 改写影响所有现有脚本 | 阶段化：先恢复回调（不 yield），再加显式 `await_*`，最后才改旧 API 语义；每阶段全量验证 |
| 全局变量协作式 interleaving（两 yield 点间其他 task 改了全局） | 文档明确「yield 后其他 task 可能运行，勿用 Lua 全局存可变业务状态，放 robot state/share」（同 JS await 语义） |
| scheduler 与 connectionPump 之间的 notify 竞态 / 死锁 | scheduler 单 select 统一 wakeup；pump 只非阻塞投递 + notify，不等回执 |

---

## 8. 已定决策

1. **不拆 track**：集中在后端底层、改动高度耦合，保持单一主纲 + §6 实现清单（已定）。
2. **安全点 = node 边界 + await yield 点**（§3.1）：阶段 1 仅 node 边界；阶段 2 引入 await 后，await 等待窗口自动成为安全点，§9 长阻塞问题在阶段 2 解决（已定）。
3. **凡"会等"的 API 全部纳入协作式**（§3.4，已定）：不止 sleep/listen —— `WaitResponse`（请求-响应）、`WaitIO`（Redis `share.*` / `http_request` / `connect_*`）都要做。按 Class A（scheduler 已观测唤醒，阶段 2）/ Class B（需后台 I/O worker，阶段 3）分批，机制不同但目标一致：任何同步阻塞 scheduler 的点都消除。
