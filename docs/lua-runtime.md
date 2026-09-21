# Lua 运行时与内存优化

> **本文对照实际代码编写，反映已实施状态。**
>
> 核对范围（2026-09-20，工作区 `D:\Gitee\stressbot`）：
> `script/`（runtime / trampoline / coroutine / api_robot / api_proto / api_network / api_utils / api_share / api_log / api_json / wire_lua / statekeys_http）、
> `protocol/protox/`（wirevalue / wirewalk / wireview / wireshadow / wirenav / dedup / dedup_frozen / frozen）、
> `robot/`（robot / scheduler）、`runner/resources.go`、`internal/timerpool/`、`engine/heartbeat_plan.go`、
> `network/connection.go`、`protocol/codec/`、根 `README.md` 第三部分、`AGENTS.md`「内存优化约束与已证实结论」。
> 文中 `文件:行号` 均指向当前源码；涉及历史剖面数据（如 029→031）的结论来自 AGENTS.md 的已证实结论记录，代码侧以当前形态与注释相互印证。

---

## 目录

1. [运行时模型](#1-运行时模型)
2. [协程与协作式调度](#2-协程与协作式调度)
3. [API 面概览](#3-api-面概览)
4. [robot.get 与 robot.get_view 使用边界（脚本作者契约）](#4-robotget-与-robotget_view-使用边界脚本作者契约)
5. [wire-first 消费路径](#5-wire-first-消费路径)
6. [内存优化纪要](#6-内存优化纪要2026-07-压测调优结论体系)
7. [观测](#7-观测)
8. [附录：关键文件索引](#8-附录关键文件索引)

---

## 1. 运行时模型

### 1.1 RuntimePool 与 per-robot 独占 LState

- `script.RuntimePool` 管理 LState 池（`sync.Pool`）与预编译脚本（`script/runtime.go:311-317`）。由 `runner.NewRuntimePool` 创建并预编译（`runner/resources.go:74-84`）。
- 每个 Robot 在生命周期内**独占**一个 LState：`NewRobot` 时 `luaPool.Acquire()`（`robot/robot.go:155`），Close 时 `luaPool.Release(r.l)`（`robot/robot.go:457`）。
- LState 以小栈初始值创建，避免默认预分配的固定开销 ×数千机器人（`script/runtime.go:327-341`）：数据栈（registry）1024 起步、上限 16384、步进 512，调用帧栈 256 段自增长，`MinimizeStackMemory: true`。栈不够自动扩，对脚本完全透明，仅省下空闲预分配的峰值内存。
- 归还池时的清理（`script/runtime.go:369-384`）：关闭本 Robot 的蹦床协程缓存（thread 持有从当前 Robot ctx 派生的子 ctx，不可跨 Robot 携带）、清 Context 绑定、删 baseline 之外的运行时全局、`L.RemoveContext()` 清掉已取消的 context（不清理的话下一个 Robot 复用时会立即 abort）。
- 脚本执行上下文 `script.Context`（`script/runtime.go:47-103`）经 registry 整数槽绑定到 LState（`SetContext`/`GetContext`，`script/runtime.go:244-271`）；整数槽直落 LTable array 部分，是热路径优化——字符串键约 25ns/次哈希，摊在每一个 Lua→Go API 上（`script/runtime.go:27-41`）。Robot 侧注入 Store / Factory / CodecResolver / NetSender / 共享状态后端 / Waiter 等。
  - 每 (proto, service) 的 adapter 解析结果按机器人缓存（结构体键不分配，`script/runtime.go:107-137`）；未命中不入缓存（配置错误的 fail-loud 路径，缓存会让运行期补上的映射永远看不见）。
  - 脚本内每次 request 的 WireBytes / RTT / 监听等待等指标经 Context 累加，每次 action 入口清零、收尾取出（`script/runtime.go:139-241`）——Lua 多次 request 不会被合并成一个 RTT 样本。
- 线程所有权约定：**业务 LState 的唯一所有者是执行器 goroutine**（`robot/scheduler.go:21-30` 的 actor 模型）。listen 回调由网络 pump 投递到任务队列（mailbox，只复制消息 + 投递），由执行器在安全点串行执行，绝不在 pump goroutine 内碰 LState——否则会与主流程并发抢占同一 LState 导致栈损坏（`script/runtime.go:616-619`）。

### 1.2 脚本预编译与按名加载

- `PrecompileScripts` 遍历脚本目录，`LoadFile` 提取 `FunctionProto`，以相对 `scriptDir` 的 slash 路径为脚本名注册（`script/runtime.go:488-526`）。进程级编译一次，之后每个 LState 实例化函数值。
- `loadScriptFn` 惰性加载入口函数（`script/runtime.go:401-442`）：每个 LState 每个脚本的 chunk 只跑一次，`execute` / `on_message` 入口捕获进 registry 整数槽缓存并移出全局表，后续直接取缓存调用。同名脚本重复注册只替换 proto、槽号不增长（`script/runtime.go:345-359`）。
- 全局表卫生（`script/runtime.go:386-399, 444-486`）：chunk 顶层定义的全局（函数、常量表）在首次加载后并入 baseline 集合（`__sb_gbaseline__`）；Release 时只清 baseline 之外的「运行时全局」，既保留脚本静态定义、又隔离 Robot 之间的运行时污染。
- 副作用约定：脚本顶层从「每次动作」变为「每个 LState 首次加载一次」。正常脚本顶层只有函数定义 / require，无影响；把可变状态写在顶层裸全局当作每次重置的写法属反模式，应改用 `robot.set/get`。

### 1.3 三种执行模式

| 模式 | 入口 | 驱动方 | 返回契约 |
| --- | --- | --- | --- |
| ActionScript | `function execute(r)` | lua 动作节点，`robotActionHandler` 调 `RunActionScript`（`robot/robot.go:792`） | `nil` 成功；`robot.error(code, detail)` 构造的 err table `{code, detail}` 失败；返回裸 number 会 fail loud（`script/runtime.go:528-537, 562-573`） |
| BooleanScript | `function execute(r)` | 条件表达式 `lua:` 前缀 / loop breakCondition，`RunBooleanScript`（`robot/robot.go:855`） | **必须** return boolean；number/nil/其他类型一律报错、调用方按 false 处理并打 error 日志（`script/runtime.go:680-688`）；条件脚本不存在时同样默认拒绝（`robot/robot.go:847-853`） |
| ListenCallback | `function on_message(r, msg)` | listen 配置 `script` 回调，Robot 调度器串行调用（`robot/robot.go:1195-1231`） | 返回值忽略。`msg` 为按 `ListenDef.s2cProto` 解码后的字段表；未配 `s2cProto` 时为二进制安全 Lua string（`script/runtime.go:610-649`） |

三种模式全部跑在子线程协程上、共用同一套协作式调度（见下节）：action / boolean 脚本同样可以调 await_*（如先 await 一次请求再据响应判定，`script/runtime.go:692-694`）；on_message 内可直接发请求回应推送，嵌套等待期间 drain 出另一回调也安全（`script/runtime.go:620-622`）。

`r` 句柄是 `LUserData` + 共享元表（`script/runtime.go:753-777`），`r:get("key")` 方法调用形式与 `robot` 模块函数等价（`robotIndex`，`script/api_robot.go:62-102`）；方法调用与模块调用靠 `firstRobotArg` 跳过 self 参数统一（`script/api_robot.go:367-375`）。脚本返回的 err table 由 `parseErrTable` 重建为 `*errcode.ActionError`（`script/runtime.go:564-567`）；框架错误（加载/resume/Waiter 缺失）与业务失败分开返回。

### 1.4 一次 lua 动作的端到端执行链

以 `pattern: "lua"` 动作节点为例，把上面各环节串成一条链：

1. `Executor` 遍历流程图，命中 action 节点后经 `ActionHandler` 委托到 `robotActionHandler`（engine 层与 network/robot 层解耦）。
2. 入口 `RunActionScript`（`script/runtime.go:539-546`）：清零 Context 指标累加器，`defer` 收回 send/recv/timing 供 monitor 记账。
3. `startActionCoroutine`（`script/coroutine.go:161-164`）：`loadScriptFn` 取缓存的 `execute` 入口 → `acquireTrampThread` 取（缓存或新建）蹦床 thread → 组装 `coroutine` 任务描述。
4. drive-loop（`script/runtime.go:557-586`）：`resumeCoroutine` 推进；脚本直跑到底 → DONE-yield，按 err-table 契约结算返回；中途 yield 出 WaitSpec → `ctx.Waiter.Await` 由 Robot 在等待窗口内 drain 任务队列（跑 listen 回调等），完成后 `buildResumeVals` 把 outcome 转回 Lua 返回值再 resume。
5. 收尾：`co.close()` 按 `doneOK` 归还或弃用 thread；业务失败以 `*errcode.ActionError` 上抛，交 monitor 按 `code` 单维聚合、按 onError 链路决定 resume/skip/abort。

同一 drive-loop 结构被 `RunBooleanScript`（条件求值）与 `runListenScriptValue`（on_message 回调）复用，三者共享蹦床缓存与 await 协议。

---

## 2. 协程与协作式调度

### 2.1 trampoline 长驻复用

旧实现每次脚本执行 `L.NewThread()` 新建子线程协程、脚本 return 后协程死亡：数千机器人 × 每秒数千次执行时，`newLState/newRegistry/newGlobal` 占进程总分配 50%+（线上 profile 实测 54%），GC 追不上分配速率，余量把 RSS 顶到 live 的 2 倍（`script/trampoline.go:11-18`）。

现在协程主函数是永不返回的蹦床循环（`script/trampoline.go:39-63`）：执行完一个任务就 `yield(哨兵, 结果...)` 停在栈顶等待下一个任务，thread 长驻复用，稳态创建速率 ≈ 0。协议要点：

- bootstrap（首次 Resume）喂 `(sentinel, fn, argc, a1, a2)` 立即执行首个任务；handoff（复用 thread）喂 `(fn, argc, a1, a2)` 作为 yield 返回值；DONE 哨兵是唯一 userdata、按指针相等判定，脚本无法伪造（`script/trampoline.go:42-51, 149-162`）。
- 归还规则：只有以 DONE-yield **干净收尾**的 thread 才回缓存；脚本错误（thread 已死）、裸 yield / await 中途放弃（栈停在半途）一律弃用、下次惰性重建（`script/trampoline.go:20-24`，`coroutine.close`，`script/coroutine.go:104-121`）。
- 每 Robot 空闲缓存上限 4 条（主流程 1 + 嵌套回调 1-2 + 极端嵌套余量）；单 thread 执行 512 个任务后退休重建——gopher-lua thread 数据栈只增不缩，深调用脚本会撑大并长期钉住，按次退休把常驻上界摊销为 1/N 创建成本（`script/trampoline.go:28-37`）。
- 嵌套场景天然成立：主流程脚本 await 挂起时其 thread 不在空闲缓存里，等待窗口 drain 出的 listen 回调取到另一条（或新建），互不干扰（`script/trampoline.go:23-24`）。
- 蹦床 chunk 进程级编译一次（`compileTrampoline`，编译失败属编程错误直接 panic），函数值按 LState 缓存进 registry、所有 thread 共享（`script/trampoline.go:124-147`）。

### 2.2 await_*：等待只暂停当前机器人主流程

协作式执行协议（`script/coroutine.go:12-21`，`script/runtime.go:548-587`）：

1. 脚本跑在子线程协程上，遇到 `await_*` 时 yield 出 `WaitSpec`（`script/coroutine.go:50-64`），控制权交回 drive-loop；不含 await 的脚本首次 Resume 即跑完，行为与旧 `CallByParam` 等价。
2. drive-loop 调 `Context.Waiter.Await`（Robot 实现的 `Waiter` 接口，`script/coroutine.go:80-85`）——Robot 在等待窗口内 drain 任务队列（跑 listen 回调等），**任何阻塞点都不饿死推送回调**、也不会出现「pump goroutine 抢占业务 LState」的并发栈损坏（`robot/scheduler.go:21-30`）。
3. 条件满足/超时后 resume 协程，把 `WaitOutcome` 转回 Lua 返回值（`buildResumeVals` 按 WaitKind 分派，`script/runtime.go:589-608`）。

等待类型四种（`script/coroutine.go:23-41`）：

| WaitKind | 触发 API | 等待机制 |
| --- | --- | --- |
| `WaitSleep` | `utils.sleep(ms)` | 纯计时等待，到时即 resume |
| `WaitListen` | `network.tcp_listen` / `udp_listen` | Waiter 等待队列通知并处理 Robot mailbox，命中或超时后 resume |
| `WaitResponse` | `network.tcp_request` / `udp_request`（及 `_route` 变体） | Waiter 发送已编码 Packet 并注册响应通道，select 通道 + drain 任务；响应到达由通道直接唤醒，WireRTT 用底层消息时间戳计算 |
| `WaitIO` | `share.*`（全部 20 个）、`network.http_request`、`connect_tcp/udp` | 阻塞调用投后台 goroutine 执行（`awaitIO`），执行器 goroutine 同步等待期间持续 drain 任务队列；完成后 renderer 回执行器 goroutine 产出 Lua 返回值（`script/coroutine.go:328-340`） |

Class B I/O 的硬边界（`script/coroutine.go:43-48`）：后台 job **绝不可访问 L 或业务 state**（只能用已捕获的 Go 值 + 线程安全句柄），IORenderer **只在执行器 goroutine 上被调用**；两者经 done 通道交接（happens-before，无竞态）。

**连接 pump 与心跳不受脚本等待影响**：每条连接的 connectionPump goroutine 统一处理 inbound decode、心跳 timer 到期发送与控制命令（`network/connection.go:62-71`），与机器人主流程完全独立；心跳是连接级声明式配置（`*_codec.json` 的 `heartbeat` 对象），运行时不调业务 Lua，主流程阻塞等待时心跳仍会继续发送。

### 2.3 顶层调用约束及其原因

await 类协作 API（`tcp_request` / `udp_request` / `tcp_listen` / `udp_listen` / `connect_*` / `http_request` / `utils.sleep` / 全部 `share.*`）**只能在动作脚本顶层（调度器直接 resume 的协程）直接调用**：

- 入口校验：`awaitYield` 检查 `ctx.topThread != L` 即 `RaiseError`（`script/coroutine.go:312-321`）；`topThread` 每次 resume 前重设，故协作式嵌套（回调 await 期间 drain 出另一回调）也能正确归位（`script/coroutine.go:174-179`）。
- 原因：gopher-lua 的 yield 不能穿越 pcall 的保护边界。若 `await_*` 被 pcall 吞掉，协程会以 ResumeOK「结束」并把 WaitSpec 当返回值带出（静默陷阱），或线程腐坏（Parent 被清空、执行却继续）后在下一次 yield 触发 panic 穿出 Resume——两种形态都被 `resumeCoroutine` 拦截：DONE 分支与 ResumeOK 分支检测返回值里的 WaitSpec（`script/coroutine.go:262-269, 285-293`），deferred recover 按父栈残留还原精确错误并清理父栈（`script/coroutine.go:190-219`）。错误文案统一指路：「await_* 只能在动作脚本顶层直接调用，不可置于 pcall/xpcall 或 coroutine.create 内」。
- 框架自身对脚本异常的 pcall 保护不受此约束影响（框架不在脚本协程栈上包 pcall；蹦床内无 pcall，脚本错误直接穿出由 Go 侧处置，`script/coroutine.go:255-258`）。
- 裸 `coroutine.yield` 同样 fail loud（`script/coroutine.go:282-284`）。

### 2.4 utils.sleep 响应取消

`utils.sleep(ms)` yield 出 `WaitSleep` 规格（`script/api_utils.go:479-489`）；`ms <= 0` 直接返回不进入等待。等待落在 `robotScheduler.wait`（`robot/scheduler.go:80-119`）：select 流程 ctx.Done / wake 通道 / `timerpool.GetTimer` 到期——ctx 取消时提前返回 Canceled，drive-loop 据此终止脚本，不会睡满剩余时长。休眠只暂停当前机器人主流程，不阻塞任何连接收发与心跳。

### 2.5 监听回调的投递与执行

listen 的两种消费形态（与 `store` 声明式映射互斥）：

- **声明式 store**：`s2cProto + store`，推送到达后 pump 解析物化、经 `SetPath` 投递执行器任务队列写入 state（纯 Go，无 Lua）。
- **Lua 回调**：`script` 指定脚本，`function on_message(r, msg)` 消费。网络 pump 解码后按 routeKey 查合表 `listenRoutes`（回调 + 队列一次取齐，`network/connection.go:78`）：有回调只执行回调、无回调才入队（二者互斥）；Lua 回调同样只投递任务队列，由执行器在安全点串行执行（`robot/robot.go:1195-1203` 的调用约束，`script/runtime.go:616-619`）。

回调消息体的三种进入形态（`robot/robot.go:1204-1228`）：

1. 配了 `s2cProto` 且 body 非空：**首选 wire 直转**（`RunListenScriptWire`）——`ValidateWire` 通过即构造 `WireValue`（msg 已是 pump 外深拷贝快照，字节所有权安全移交），`on_message` 拿到的 Lua table 与「解码 + 整表 Lua 化」逐字一致，dynamicpb 解码树整条消失（详见 5.1）。
2. 直转被拒（schema 降级 / 影子采样失配 / 结构异常）或 `ValidateWire` 失败：回落解码路径（`RunListenScript`），解码失败按 callback-parse 错误记指标。
3. 未配 `s2cProto`：`RunListenScriptRaw` 把原始消息体作为二进制安全 Lua string 传入（`script/runtime.go:645-649`）。

---

## 3. API 面概览

7 个模块共 **87 个函数**，注册入口 `registerAPIs`（`script/runtime.go:742-751`）；各模块注册计数逐文件核对：network 21（`script/api_network.go:47-76`）、robot 15（`script/api_robot.go:41-56`）、proto 10（`script/api_proto.go:29-38`）、utils 15（`script/api_utils.go:46-63`）、log 4（`script/api_log.go:21-24`）、json 2（`script/api_json.go:24-25`）、share 20（`script/api_share.go:39-62`）。

加载方式 `local mod = require("<name>")`。函数名 + 一句话语义如下（参数签名、可选参数与返回值细节见根 README.md 第三部分的详表，README.md:546-704，本文不重复展开）：

### network（21）

| 函数 | 语义 |
| --- | --- |
| `connect_tcp(service, address)` | 建立 TCP 连接（awaitIO 协作式，`script/api_network.go:270`） |
| `connect_udp(service, address)` | 建立 UDP 连接 |
| `close_tcp(service)` | 关闭 TCP 连接 |
| `close_udp(service)` | 关闭 UDP 连接 |
| `tcp_request(service, route, msg [, s2cProto [, timeout]])` | TCP 一发一收，返回 `err, data`；WireBytes 自动计入监控（`script/api_network.go:411`） |
| `tcp_request_route(service, reqRoute, respRoute, msg [, s2cProto [, timeout]])` | 请求路由用于编码、响应路由用于匹配 |
| `udp_request(service, route, body [, s2cProto [, timeout]])` | UDP 一发一收（`script/api_network.go:462`） |
| `udp_request_route(service, reqRoute, respRoute, body [, s2cProto [, timeout]])` | 同上的路由分离版 |
| `tcp_send(service, route, msg)` | TCP 发送不等待 |
| `udp_send(service, route, body)` | UDP 发送 |
| `http_request(url [, method [, contentType [, body]]])` | HTTP 请求返回 `err, status, body`；HTTP bytes 自动计入监控（`script/api_network.go:592`） |
| `tcp_listen(service, route [, s2cProto [, timeout]])` | 阻塞等待 TCP 推送（需先 ensure 占位；`script/api_network.go:864`） |
| `udp_listen(service, route [, s2cProto [, timeout]])` | 阻塞等待 UDP 推送 |
| `try_tcp_listen(service, route)` | 非阻塞单取最新 TCP 推送（无则返回 nil） |
| `try_udp_listen(service, route)` | 非阻塞单取最新 UDP 推送 |
| `set_tcp_secret_key(service, key)` / `get_tcp_secret_key(service)` | TCP 通信密钥读写 |
| `set_udp_secret_key(service, key)` / `get_udp_secret_key(service)` | UDP 通信密钥读写 |
| `ensure_tcp_listener(service, routeKey)` | 注册 TCP 监听占位（队列容量固定 1） |
| `ensure_udp_listener(service, routeKey)` | 注册 UDP 监听占位 |

> 心跳在对应连接的 `*_codec.json` 中以可选 `heartbeat` 对象配置（连接级，详见 [adapter-layer.md](adapter-layer.md)）。

### robot（15）

| 函数 | 语义 |
| --- | --- |
| `get(key)` | 读取 state 值（独立 Lua 表/标量，可自由加工；消息类 key 整树物化，见第 4 节） |
| `get_view(key)` | 借出消息类 key 的只读惰性视图 userdata（零物化，`proto.*` 窄读；大消息只读首选，误用报错指路） |
| `set(key, value)` | 写入 state（消息类 userdata 存 wire 引用 / Frozen 引用） |
| `has(key)` | 检查 key 是否存在 |
| `delete(key)` | 删除单个 key（框架身份键 `id`/`index`/`account` 不可删，尝试删除 Warn 并忽略） |
| `clear([key])` | 无参清空全部（保留身份键）；有参删除指定 key（保留键规则同 delete） |
| `increment(key)` | 原子自增并返回新值 |
| `keys()` | 返回所有 key 列表 |
| `get_path("a.b[0].c")` | 按路径读取嵌套值 |
| `set_path(path, value)` | 按路径写入嵌套值（自动创建中间节点，走 Overlay 覆盖层） |
| `get_id()` | 机器人编号 |
| `get_index()` | 任务全局序号（0-based，不含 `startNumber` 偏移） |
| `get_account()` | 账号名 |
| `get_context()` | 检查 context 是否已取消（配合长循环做协作式退出） |
| `error(code, detail)` | 构造 err table `{code, detail}`（`script/api_robot.go:269-277`） |

### proto（10）

| 函数 | 语义 |
| --- | --- |
| `create(name)` | 创建动态 proto 消息（返回 userdata） |
| `set_field(msg, field, val)` | 设置字段（value 支持标量/表/嵌套消息；Frozen 与 wire 视图 fail loud） |
| `get_field(msg, field)` | 获取字段值；视图上按需 wire 扫描（`GetFieldCompat`） |
| `get_path(msg, path)` | 按路径获取字段值（与 `get_field` 同函数注册，`script/api_proto.go:31-32`） |
| `serialize(msg)` | 序列化二进制；视图上直接返回原始字节 |
| `parse(name, data)` | 从二进制解析消息（可写） |
| `get_field_map(msg)` | 全字段转 Lua table；视图上走直转器整树建表 |
| `iter_list(msg, field)` | 顺序迭代 repeated（`for idx, item in`；message 元素为 userdata，wire 视图上 O(n) 游标） |
| `list_size(msg, field)` | repeated 字段长度（视图上单遍计数） |
| `list_get(msg, field, idx)` | 获取 repeated 元素（1-based；视图上 message 元素返回子视图） |

嵌套 proto 消息作为 userdata 保留；普通字段自动转 Lua 值。消息 userdata 支持 `__index` 方法调用，表语法（`view.foo`）与赋值语法（`msg.f = v`）触发教学报错并指路正确 API（`script/api_proto.go:44-75`）。

### utils（15）

随机类：`random_int(n)` [0,n-1]；`rand_range(min, max)` 闭区间；`random_bool()`；`random_string(length)`（<=0 回退 8）；`random_pick(table)`；`random_pick_n(table, n)`；`weighted_pick(items, weights)` 返回元素和索引；`rand_filter(items [, excludes [, count]])`；`rand_filter_one(items [, excludes])`；`shuffle(arr)` 原地 Fisher-Yates。二进制：`pack_le(format, ...)` 小端打包；`unpack_le(data, fmt1, ...)` 解包（大整数超 2^53 字符串返回）。其它：`sleep(ms)`（协作式，见 2.4）；`time_ms()`；`fnv_hash(str)` FNV-1a 64 位（hex 字符串，输入先去空白、剥 `v`/`V` 前缀转小写）。

### log（4）/ json（2）

`log.debug/info/warn/error(msg)`；`json.encode(table)` / `json.decode(str)`。

### share（20）

Redis 共撑的跨机器人/跨节点共享态（实现位于 `state/shared`），单机在 `stressbot.toml`、Admin 在 `admin.toml` 配 `[redis]`；未配置时所有调用返回 ErrNotEnabled 不中断脚本。命名空间：KV `set/get/del/exists/expire`；计数器 `incr`；分布式锁 `claim/release/owner/renew`；队列 `queue_push/queue_pop/queue_len/queue_expire`；Hash `hash_set/hash_get/hash_get_all/hash_del/hash_incr/hash_expire`。

**返回约定**（`script/api_share.go:20-22`）：写/判定类返回 `(value_or_ok, err)`；读类（get/hash_get/queue_pop）返回 `(value, ok, err)`，`ok=false` 区分「不存在/空队列」与「存储的 JSON null」；**两个例外** `owner(key)` 与 `hash_get_all(key)` 返回二值 `(value, err)`（实现见 `script/api_share.go:350-368`、`:536-544`）。`del/exists/expire` 作用于数据类型全集（kv+counter+queue+hash），不含 claim（`script/api_share.go:24-26`）。全部 20 个函数逐一经 `awaitIO` 协作式（`script/api_share.go:173-619`）。

### Go-Lua 类型转换规则

Go → Lua（`goValueToLua`，`script/api_robot.go:377-455`）：

- **int64/uint64 超 2^53（`maxSafeInt = 1 << 53`）以字符串返回**，保留精度（`script/api_robot.go:16, 389-402`）；proto 标量侧同款规则（`protoScalarToLua`，`script/api_proto.go:735-749`，枚举同 :760-765）。
- `[]byte` → Lua string；`[]any` → 预分配数组容量的 table；`map[string]any` → 预分配 hash 容量的 table（避免默认 32+32 双槽浪费）。
- `*protox.WireValue` → 优先 wire 单遍直转 Lua table，被拒回落解码（见第 5 节）；`*protox.Frozen` → 现场转真 Lua table（与旧展开 map 版逐字一致）；`state.ValueMaterializer`（Overlay）→ 全量物化后转 table。
- 兜底未知类型转 `fmt.Sprintf` 字符串（`:452-453`）。

Lua → Go（`luaToGoValue`，`script/api_robot.go:522-594`）：

- number → float64；table → **稠密表**（下标连续 1..N，`maxIdx == count`）按元素数分配 `[]any`，稀疏表退化为键转字符串的 `map[string]any`（防 `{[1e9]=1}` 类稀疏键按 maxIdx 分配撑爆内存，`script/api_robot.go:550-569`）。
- userdata：Frozen / respHandle / proto.Message 按各自语义透传；嵌套在表里的未知 userdata 转 nil 并打 error 日志（不静默——否则脚本看到的是「字段莫名消失」）。
- `robot.set/set_path` 专用入口 `luaToGoStoreValue`（`script/api_robot.go:457-501`）：函数/协程/channel 与未知 userdata 一律教学报错，不静默存 nil（否则脚本作者读 nil 会误以为 key 没写进去）。

---

## 4. robot.get 与 robot.get_view 使用边界（脚本作者契约）

一句话决策（`script/api_robot.go:123-134` 注释即源）：**整份数据要拿来自由加工/修改 → `get`；大消息只读挑着看 → `get_view`**。范例脚本 `conf/scripts/system_shop_buy.lua`。

| | `robot.get(key)` | `robot.get_view(key)` |
| --- | --- | --- |
| 产物 | 独立 Lua 表（整树物化，成本 ∝ 树大小；改它不影响 state） | wire 惰性视图 userdata（与 `await_listen` 给脚本的是同一种东西） |
| 读法 | 任意 Lua 语法，可加工后 set 回 | 仅 `proto.get_field/get_path/list_size/list_get/iter_list/serialize` 窄读，零物化零分配 |
| key 缺失 | nil | nil（可判空，与 get 一致） |
| 观测计数 | `get:` 行（calls/tables/wireDecodes） | `view:` 行（只计 calls） |

- **二者不可能混用出错值**：表与 userdata 语法互斥，误用即刻报错且报错文案指路正确 API——视图上 `view.foo` / `view.foo = v` / `pairs` 等表原语触发 `protoMsgIndex` / `protoMsgNewIndex` 的教学报错（`script/api_proto.go:59-75`）；`get_view` 用在标量、脚本 set 的 Lua 表、被 `set_path` 改写出 Overlay 的 key 上时按存储形态报错并指路 `robot.get`（`script/api_robot.go:154-159`）。
- **视图是借出时那份不可变字节的快照引用**：key 被覆盖不影响已借出视图，无失效协议；跨 await 挂起持有安全——底层是共享 `WireShared` 字节，不钉解码树，不触犯 029→031 钉扎结论（`script/api_robot.go:128-131`，`protocol/protox/wireview.go` 文件头）。视图永远只读：`set_field` 对 Frozen/WireValue fail loud（不能作改写目标，也不能作嵌套赋值的 value，`script/api_proto.go:246-258`）。
- **写侧不受影响、无需新 API**：`robot.set(key, 视图/响应)` 原样存 wire 引用（零转换零复制，`script/api_robot.go:481-484`）；`robot.set_path` 走 `Overlay` 写覆盖层（不解码不重编码，`state.ValueMaterializer` 物化见 `script/api_robot.go:449-451`）。响应句柄（`respHandle`）未改写时直接复用原始 body 快照构造 `WireValue`，已置脏（`set_field` 改写，同根子包装共享 dirty 标记）则 `proto.Marshal` 重编码（`respHandleStoreValue`，`script/api_robot.go:503-520`）——每机器人独有的大消息常驻由 ~600KB 解码树塌缩为 ~74KB 字节（`script/api_robot.go:459-466` 注释）。
- 语义注意：存消息按「存入时内容」固化——WireValue 是字节快照，脚本此后对同一 userdata 调 `proto.set_field` 不会反映到 state（`script/api_robot.go:467-470`）。
- schema 降级时视图仍可用：各访问器经 `findWireView` 检测降级后自动落到解码路径，脚本无感知（`script/api_robot.go:148-153`，`script/api_proto.go:198-214`）。

---

## 5. wire-first 消费路径

> 背景：8000 人剖面上，消费侧（robot.get 整读转换 14.8% + 帧解码 6.9% + 由此拉动的 GC 26%）曾是 CPU/分配主源——wire-first 首版只做了留存侧（state 存 `WireValue`），消费侧仍走 dynamicpb 整树。2026-07-29 D1/D2 补齐消费侧，留存与消费自此统一为 wire 形态。设计文档 `docs/superpowers/plans/2026-07-29-wire-first-state-plane.md`（`protocol/protox/wirevalue.go:21` 引用）。

### 5.1 消费侧四条路径总览

| 消费路径 | 形态 | 实现 |
| --- | --- | --- |
| `robot.get` / `robot.get_path` 整读 | WalkWire 单遍直转 Lua 表（零 dynamicpb 中间树） | `goValueToLua` 的 WireValue 分支（`script/api_robot.go:431-448`）→ `wireValueToLuaTable`（`script/wire_lua.go:22-34`） |
| listen 脚本 `on_message` 表 | 同上直转（表与「解码 + 整表 Lua 化」逐字一致） | Robot 侧首选 `RunListenScriptWire`（`robot/robot.go:1206-1216`，`script/runtime.go:632-643`） |
| `proto.get_field_map` | 直转器整树建表 | `script/api_proto.go:434-441` |
| `await_tcp/udp_listen` 大消息 | wire 惰性视图 userdata（持字节，读多少扫多少） | `listenResultValues`（`script/api_network.go:800-822`） |

存储侧前置条件：`WireValue` 构造点一律先过 `ValidateWire` 结构校验（零分配纯索引游走，`protocol/protox/wirevalue.go:677-741`）——校验通过 ≡ 解码必成功（差分 fuzz 保证等价），此后 wire 扫描的意外失败都指向扫描器 bug 而非数据。

### 5.2 直转器（WalkWire 单遍直转）

`wireValueToLuaTable` 先 `MaterializeAllowed()` 影子判定，再以 `luaTreeSink` 走 `Walk` 把遍历事件直接落成 Lua 表——中间树消失；被拒（schema 降级 / 采样失配 / 结构损坏）时 `ok=false` 回落解码路径，**Walk 的意外失败必经 `ReportWireFailure` 留失配证据日志，绝不静默回退**（`script/wire_lua.go:20-34`）。

walker 语义（`protocol/protox/wirewalk.go:12-26`）：

- 按 descriptor 字段序遍历**全部**字段（不仅限已设置）；未设置的单数 message、空 repeated/map 跳过；标量/枚举恒产出（未出现 → proto3 默认值）。
- 合并语义完整复刻 protobuf 解码器：标量 last-wins、单数 message 多次出现按序拼接（wire 拼接 ≡ proto merge）、oneoof 成员按出现序竞争、repeated 出现序即元素序且 packed 与非 packed 可混排、map 同 key 后者整体替换。
- string/bytes 一律复制，产物不与底层快照共享。

Lua 侧 sink 的标量装箱与 `protoMessageToLuaTable` 逐字一致（大整数转字符串、bytes 转 string；表容量预分配同款规则，`script/wire_lua.go:36-117`），产物等价性由 `script/wire_lua_test.go` 的差分用例守护。

### 5.3 await_listen 大消息视图

`listenResultValues` 命中推送后（`script/api_network.go:800-822`）：

- body ≥ `protox.DedupMinBytes`（1024 字节，`protocol/protox/dedup.go:38`）且 schema 未降级：经 `Factory.WireShared` 内容寻址取共享 `*WireValue`（同帧 60 接收方共享同一字节快照），包成视图 userdata 返回——整包解码消失，协程挂起钉住的只是共享 wire 快照本身。
- schema 降级：回落 `ParseFrozenShared` 共享解码（FrozenCache），正确性优先于形态。
- 小消息（< 1024 字节）：独占解码、消息可写（历史行为）；未改写时 `robot.set(resp)` 经句柄携带的 body 快照零成本转 WireValue。
- HeaderErr（服务端业务错误）同样算监听命中：帧到了、等待时长有效，不因业务失败丢样本（`script/api_network.go:784-790`）。

历史脉络（`script/api_network.go:805-810` 注释）：独占瞬态解码已被 029→031 剖面证伪（churn 放大 60 倍 + 协程钉解码树 +1.15GB）；共享解码（FrozenCache）解决了 churn 但仍常驻解码树 268MB 且单人局命中率仅 6%；视图形态无解码树，两个问题同时消失。

### 5.4 proto.* 窄读与 iter_list 游标

视图上的窄读 API 语义与解码路径逐字对齐（契约见 `protocol/protox/wireview.go:19-33`）：

- `GetFieldCompat(p) ≡ Factory.GetField(decode(raw), p)`、`ListLenCompat ≡ GetListLen`、`ListItemCompat ≡ GetListItem`（message 元素 → 子视图）。
- **含**「缺席的单数 message 按默认值实例继续下钻」这一 `GetField` 历史语义（≡ 在空字节上继续扫描）——`proto.get_path(resp,"record.address")` 在 record 缺席时返回 `""` 不是 nil，与 `NavigateSegs` 的 not-found 语义不同（`protocol/protox/wireview.go:24-28`）。未知字段 / 非法下标（`"[abc]"`）/ 越界 / 非法嵌套是 error（脚本侧 RaiseError），不是 nil。
- `proto.serialize` 在视图上直接返回原始字节（`wv.Raw()`），免解码免重编码——解码后重编码与原始字节语义等价（`script/api_proto.go:360-366`）。
- `proto.iter_list` 走 `WireListCursor`（`protocol/protox/wireview.go:263-320`，`script/api_proto.go:464-499`）：一遍收集全部元素跨度、message 元素惰性产出子视图（零解码零复制，脚本读哪个字段才扫哪个），整链 O(n)——替代逐下标 `list_get` 每次重扫父层级定位全部元素的 O(n²)。**降级回落时脚本可见形态不得漂移**是此处（也是所有消费 API）的硬约束：解码分支与 wire 游标统一产出——message 元素为 userdata（≡ `list_get`），标量元素装箱值；未知字段/非法路径 → nil，非 repeated（标量/map）→ 空迭代（`script/api_proto.go:512-522`）。游标跨度引用父快照字节，跨协程挂起、跨 key 覆盖均安全，无失效协议（`protocol/protox/wireview.go:270-273`）。
- `list_size` 单遍计数不展开元素（`script/api_proto.go:555-564`）；`list_get` 的 message 元素返回子视图（字节段共享父快照，只读性全树传播，`script/api_proto.go:588-601`）。

### 5.5 正确性三层防线

| 层 | 机制 | 位置 |
| --- | --- | --- |
| L1 离线 | 差分 fuzz：wire 路径 vs dynamicpb 解码 oracle 全量对拍 | `protocol/protox/wirediff_test.go`、`wirewalk_test.go`、`wireview_test.go`；Lua 直转产物另有 `script/wire_lua_test.go` |
| L2 线上首访全查 | 每个 (schema, 路径) 前 `shadowFirstK = 3` 次双读比对，oracle 为「dynamicpb 解码 + Frozen 导航」 | `protocol/protox/wireshadow.go:37-42` |
| L3 线上稳态采样 | 之后每 schema 每 `shadowSampleEvery = 8192` 次抽查一次；整树物化用伪路径 `"*"` 计数 | `protocol/protox/wireshadow.go:41, 74-93` |

失配处理是 **fail-safe 而非 fail-loud**（正确性优先于形态，`protocol/protox/wireshadow.go:27-34`）：以 oracle 结果返回本次、把该 schema 标记降级（`WireDegraded`，`:65-72`）——后续**存储点**不再为它构造 WireValue、回落解码态路径；已存的 WireValue 继续可读（每次读都走 oracle 兜底）。L2 是 O(路径集合)（配置固定、有界）的一次性成本，L3 摊薄后可忽略；稳态可用 `SetWireShadowEnabled(false)` 全关（容量压测建议先开满一轮再关，`protocol/protox/wireshadow.go:34-35, 62-63`）。

失配日志 `[WIRE] 影子验证失配，schema 降级回解码路径` 携带离线复现全要素：schema 全名、访问路径、两侧产物摘要、wire 字节 hex 转储（截断 4KB，`rawLen` 给全长，`mismatchDumpCap`，`protocol/protox/wireshadow.go:134-160`）——拿到日志即可写出复现用例。直转 Walk 在已过 `ValidateWire` 校验字节上的意外失败同样走 `ReportWireFailure` 上报（`protocol/protox/wireshadow.go:162-170`，调用点 `script/wire_lua.go:29-31`、`protocol/protox/wirevalue.go:108-110`）；**任何 wire 路径的回退都必须留日志，禁止静默回退**。

影子判定与 fd 预解析合并进导航驻留表，一次查表零分配（`protocol/protox/wirenav.go:1-25`）；采样语义相对旧实现有两处收紧：哈希碰撞/表满（防脚本动态 map-key 路径撑爆表）该路径不驻留、仅参与稳态采样；条目按描述符**身份**（接口指针）校验，proto 热重载后新描述符替换旧条目并重新首 K 全查。

### 5.6 FrozenCache 与降级回落的退役计划

- `protox.FrozenCache`（`protocol/protox/dedup_frozen.go:35-46`）是内容寻址的共享解码缓存：与 `WireCache`（服务**留存**，state 存共享字节，`protocol/protox/dedup.go:11-33`）互补，当前职责是 **schema 降级后的消费回落路径**（见 5.3 / 5.4 的降级分支）。体积按解码后估算计上界（256MB / 4096 条，`protocol/protox/dedup_frozen.go:35-39`）——它钉住的是解码树，与 WireCache 按原始字节设界（64MB / 4096 条，`protocol/protox/dedup.go:40-45`）不矛盾：两个缓存钉的东西不同。
- `protox.Frozen` 本体是解码后 proto 消息的不可变引用封装（`protocol/protox/frozen.go:6-28`）：类型系统天然挡住 setter，可多 goroutine 无锁并发读。
- **退役计划（AGENTS.md 2026-07-29 记，用户已确认方向，尚未实施）**：wire 消费路径（直转器 + 惰性视图）经数轮生产规模压测且 `/debug/wire` 持续零失配、影子验证按计划稳态关闭后，整体退役「schema 降级回落」——删除 `FrozenCache`、`Frozen` 消费路径与各访问器的解码分支，失配语义从 fail-safe 改为 fail-stop（直接报错停测）。这是信心问题而非技术问题。退役前 FrozenCache 常态为空、成本近零，**不要提前删**：回落必须落在共享解码上，独占解码回落已被 029→031 剖面证伪。
- 广播留存与消费统一走 `protox.WireCache`：容量按原始字节硬上界即真实钉住量，条目为 `*WireValue`，miss 只做零分配结构校验（`ValidateWire`）不解码（`protocol/protox/dedup.go:17-25`）。碰撞防御：哈希只做桶索引，命中判定必须 protoName 相等 + 全量 bytes.Equal（`protocol/protox/dedup.go:27-29, 57-68`）。**独占推送（动作响应）不得进任何去重缓存**（污染+换血，018→019 教训，`protocol/protox/dedup.go:31-33`）。
- 保留规划：写入 state 的路径导航产物可选做批量 span 复制决策（Σ 子 span 字节 vs 整包字节，偏向共享时零复制），解除对整包快照的钉扎（`PlanWireRetention`，`protocol/protox/wirevalue.go:811-871`）。

---

## 6. 内存优化纪要（2026-07 压测调优结论体系）

### 6.1 设计原则

**数据面改造必须「留存侧 + 消费侧」同步落地。** 除非不对称本身是明确设计并写下理由，否则不允许只改一侧：wire-first 首版只做留存侧（state 存 `WireValue`），消费侧仍走 dynamicpb 整树，结果 8000 人剖面上消费侧成为 CPU/分配主源（整读转换 14.8% + 帧解码 6.9% + GC 26%），又追加一轮改造（D1/D2，见第 5 节）才补齐。同类改造（缓存策略、编码形态、生命周期）一律先画出该数据的**全部读写路径**，逐一确认新形态覆盖或显式豁免。

### 6.2 硬约束

- 业务脚本（`conf/scripts/*.lua`）的**逻辑不可改**——包括「存哪些数据」（不允许按访问面裁剪留存字段）；**用法可以改**（等价 API 替换），但必须保持语义与留存数据集合不变。
- 不设置 `GOMEMLIMIT` 等运行时环境变量作为「架构方案」（可作为运维手段单独讨论）。优先设计层修复，而非业务脚本改写或运维参数（GOGC 等）兜底。
- `RSS ≈ 2× live` 是 GOGC=100 的正常余量；压 RSS 先压 live（churn 消除路径印证：`script/trampoline.go:14-16`）。

### 6.3 已被线上剖面证实的结论清单（勿重复尝试）

每条注明结论、量级与当前代码落点：

| 结论 | 量级 / 事实 | 代码落点 |
| --- | --- | --- |
| 每机器人独有大消息，任何**解码态**表示（dynamicpb / Go map / Lua table）都 ~600KB/机器人；唯一小一个数量级的形态是 **wire 字节本身**（~1/5~1/10，LoginPlayerDataS2C ~74KB wire） | 持字节 + 按需 wire 扫描是唯一可行方向 | `protocol/protox/wirevalue.go:18-26`；写侧塌缩注释 `script/api_robot.go:459-466` |
| `playerData` 改存 `protox.Frozen` 解码引用是**净退化**（dynamicpb 每条目固定开销 ≥ map 装箱） | 同相位 live +217MB @5000 人，已还原 | `protocol/protox/frozen.go:6-28`（Frozen 现仅作共享只读 / 降级形态） |
| 广播类消息内容寻址去重有效；**「脚本消费改独占瞬态解码」已被证伪** | 029→031 剖面：60 人相同帧逐机器人解码 churn 放大 60 倍（区间 ~1.3TB dynamicpb 分配），协程挂起钉解码树 live 单调 +1.15GB | 留存+消费统一 `WireCache`：`protocol/protox/dedup.go:11-45`；历史记录 `protocol/protox/dedup_frozen.go:13-21`、`script/api_network.go:805-810` |
| Lua 协程 trampoline 长驻复用消除 newLState/newRegistry churn | 旧形态占进程总分配 54% | `script/trampoline.go:11-24` |
| 直转器 scratch 池化；**归还前必须逐元素 clear**（残留引用会经池钉住大 wire 快照，029→031 同款钉扎形态） | 瞬态 scratch，GC 周期自动清空、常驻 ∝ 并发转换数，非「内存换 CPU」的缓存交易 | `protocol/protox/wirewalk.go:65-106`（reset + wireAccsPool）；隔离契约 `TestWalkWireAccsPoolNoContamination` |
| 产物形态（Lua 表 / Go map，归调用方所有）**不可池化**——这块的解法是视图而非池 | — | `protocol/protox/wirewalk.go:84-86` 注释；视图见第 4 节 |
| 全局 timer 池消除逐次 `time.NewTimer` | 8000 机器人 × 每帧一只，剖面周期 timer 分配 20GB+ 级；正确性依赖 Go 1.23+ timer 语义，**归还后不得再引用**（含其 C） | `internal/timerpool/timerpool.go:1-25`；使用点 `robot/scheduler.go:80-119`（帧循环 poll / 监听等待）、`:177`（请求超时窗） |
| 收包解码路径零分配化：gzip 按 trailer ISIZE 定长一次分配；流密码 `CipherInPlace` 原地解密；decode 头字段暂存用栈上定长数组 | 三项均有与复制版/旧行为的对拍测试 | `protocol/codec/compressors.go:87-121`；`protocol/codec/registry.go:35-44` + `engine.go:666`；`protocol/codec/engine.go:583, 731` |
| 解压去重缓存（显式内存换 CPU）：大广播帧按压缩字节内容寻址共享解压产物，**二见登记**防独占消息污染 | 首见只记 8 字节哈希标记（不存字节），二见才缓存；双上界 LRU 自清洁 | `protocol/codec/inflate_cache.go:1-25` |
| routeKey 驻留：解码/编码路由键走有界 COW 表驻留 | 每帧一个路由键小字符串（剖面周期 2 亿量级小分配）→ 一次无分配查表 | `protocol/codec/intern.go:1-18` |
| 压缩帧 work 缓冲池化：body 瞬态副本 `sync.Pool` 租借，**不外泄才归还** | 错误/keep 路径不归还（租借缓冲可能作为 body 外泄，交 GC 无污染风险） | `protocol/codec/engine.go:615-635` |
| 导航路径驻留表：影子采样判定 + fd 解析合并为有界 COW 查表 | 热路径 4 次堆分配 → 零分配零字符串拼接；按描述符身份处理热重载 | `protocol/protox/wirenav.go:1-25` |
| 心跳注册时编译布局：`CompileHeartbeatPlan` 把 raw-LE 心跳定型为「总长 + 动态槽位表」，fixed 槽编译期预填，每 tick 只覆写动态槽 | 零 map 查找（宽度编译期定型）、零 append 增长、body 缓冲跨 tick 复用、fixed 槽零重算；**编译失败不静默吞**（回落 oracle `BuildHeartbeatBody` 保持每 tick 报错可见性）；plan 非并发安全，每连接一份、仅 pump 串行调用 | `engine/heartbeat_plan.go:12-47, 62, 212`；对拍 `heartbeat_plan_test.go` |
| 收包路由分派合表：`listenRoutes map[string]*listenBinding` 一次持锁查齐回调与队列 | 旧路径每推送 3~4 次字符串查表 + 2~3 次连接锁；合表后「有回调却没队列」的偏斜状态结构上不再存在；`NewMessage` 挪到确定有消费方之后 | `network/connection.go:78, 125-137, 469-478` |
| 列表下标早退：`wireCollectList(b, fd, limit)` 只扫到第 i+1 个元素即停（repeated 元素只追加不覆盖，前缀即定值；packed 整块解出可能略多于 limit） | `xs[i]` 下标访问不再全量收集；层级结构在构造点已全量校验，跳过尾部不损失防线 | `protocol/protox/wirevalue.go:431-468`；调用点 `:233-247`（NavigateSegs）、`protocol/protox/wireview.go:99-106`（GetFieldCompat） |
| **早退的不可早退例外**：单数字段 / map 必须扫完整层——标量 last-wins、单数 message 多段拼接、map 同 key 替换都要求看完全部出现，这是 protobuf 合并语义的硬约束；终端整列表读（`get_path("...heroList")`）与游标遍历天然需要全量，无早退空间 | — | `protocol/protox/wirevalue.go:372-414`（singular 全量）、`:527-554`（map 全量 + 同 key 替换） |

---

## 7. 观测

### /debug/statekeys（`script/statekeys_http.go`）

输出 `robot.get` / `robot.get_path` 的按 key 调用计数，定位「高频整读大 key」热点（CPU 剖面只能看到 `goValueToLua` 总量，看不到 key 名；`script/statekeys_http.go:14-26`）：

- 三列判读：`calls` 调用次数；`tables` 返回值需构建 Lua 表（容器转换，成本 ∝ 树大小）；`wireDecodes` 容器来自 WireValue/Overlay 等 wire 形态（转换前还要先解码）。**calls 高 + tables 高的 key 就是整读热点；`wireDecodes ≈ tables` 说明该 key 走 wire-first 留存、直转器（D1）收益直接作用于它**（`script/statekeys_http.go:17-23`）。
- `view:` 前缀行只计 calls——视图零物化；迁移某热点 key 到 `get_view` 后应看到其 `get:` 行的 `tables/wireDecodes` 掉零、`view:` 行上量（`script/statekeys_http.go:95-102`）。
- **默认关闭**：埋点要按 `kind+":"+name` 现拼 map 键，这次拼接是一次堆分配、摊在 `robot.get`/`get_path` 每次调用上（基准实测 get 比只有调用前缀的 get_id 多整整 1 次分配），常态压测不该替诊断工具买单；`?enable=1` 打开、`?enable=0` 关闭（`script/statekeys_http.go:26-33, 118-127`）。响应带 `enabled` 字段，区分「没开埋点」和「真的没有 key 访问」（`script/statekeys_http.go:111-116`）。
- `?reset=1` 读后清零，分窗口对比（`script/statekeys_http.go:128-142`）；计数全进程聚合（跨全部机器人，atomic 自增零锁），key 集合由脚本字面量决定天然有界，防御性上限 4096 之后归入 `__overflow__`（`script/statekeys_http.go:35, 52-70`）。
- 行排序按 tables 降序、calls 次之（`script/statekeys_http.go:148-153`）。

### 其他相关端点（均挂 pprof 端口）

- `/debug/wire`（`protocol/protox/wireshadow.go:283-300`）：影子验证统计——`enabled` / `navigates` / `checks` / `mismatches` / 降级 schema 及首失配原因（`degraded` map + 排序后的 `degradedList`）。失配非零即应查 `[WIRE] 影子验证失配` 日志。
- `/debug/dedup`（`protocol/protox/dedup_http.go:64`）：WireCache/FrozenCache 命中与驱逐统计。
- `/debug/sched`：Go 调度延迟分位数，量化施压机负载对 Apdex/P99 的污染（压测中看 `sinceLast` 组）。

---

## 8. 附录：关键文件索引

| 主题 | 文件 |
| --- | --- |
| 运行时池 / 三种执行模式 | `script/runtime.go` |
| 蹦床协程复用 | `script/trampoline.go` |
| 协作式调度协议（WaitSpec/Waiter/awaitYield/awaitIO） | `script/coroutine.go` |
| robot 模块 / Go-Lua 类型转换 / get_view | `script/api_robot.go` |
| proto 模块 / 视图包装 / 游标接入 | `script/api_proto.go` |
| network / utils / share / log / json 模块 | `script/api_network.go`、`script/api_utils.go`、`script/api_share.go`、`script/api_log.go`、`script/api_json.go` |
| WireValue → Lua 直转器（Lua 侧 sink） | `script/wire_lua.go` |
| state key 观测端点 | `script/statekeys_http.go` |
| wire 存储 / 导航 / 校验 / 保留规划 | `protocol/protox/wirevalue.go` |
| WalkWire 直转器（Go 侧 + scratch 池） | `protocol/protox/wirewalk.go` |
| 视图窄读 / WireListCursor | `protocol/protox/wireview.go` |
| 影子验证与降级 / `/debug/wire` | `protocol/protox/wireshadow.go` |
| 导航驻留表 | `protocol/protox/wirenav.go` |
| WireCache（留存共享） | `protocol/protox/dedup.go`、`dedup_http.go` |
| FrozenCache（降级回落）/ Frozen | `protocol/protox/dedup_frozen.go`、`frozen.go` |
| Robot 执行器与协作式调度 | `robot/robot.go`、`robot/scheduler.go` |
| timer 池 / 心跳布局编译 / 路由合表 | `internal/timerpool/timerpool.go`、`engine/heartbeat_plan.go`、`network/connection.go` |
