# Track 2 — 后端集成 + 删除 luaMu / withReleasedMu

> 依赖：Track 1（`adapter.NewSchemaAdapter` 就绪并对拍通过）
> 产出：业务 LState 成为唯一被单 goroutine 触碰的 VM；`luaMu`/`withReleasedMu`/`RobotAdapter`/codec 池/adapter Lua 模块**全部删除**
> 实现总纲 §1 不变量。

---

## 1. 目标

把所有「在 Robot 业务 LState 上跑的异步 Lua」逐个移除，使运行循环成为唯一 Lua 线程，然后删掉为伺候并发而存在的 `luaMu` + `withReleasedMu`。

异步 Lua 入口共 4 个，本轨道逐个消除：

| # | 异步 Lua 入口 | 跑在哪个 goroutine | 消除手段 | 子阶段 |
|---|---|---|---|---|
| 1 | listen 脚本回调 | listenLoop / connectionPump | 改为主流程 `tcpListen`/`udpListen` 消费；仅简单推送保留声明式 `store` | **2-A** |
| 2 | 心跳动态 builder | 心跳 goroutine / connectionPump timer | 声明式心跳：Go-only body builder + pump 编码发送 | **2-B** |
| 3 | codec encode/decode | decodeLoop（decode）/ 主+心跳（encode） | 用 Track 1 全局 Go adapter，**彻底无 Lua** | **2-C** |
| 4 | （消除以上后）业务阻塞 Lua 自身 | 运行循环（单 goroutine） | 单一所有者 → 阻塞不再需要丢锁 | **2-D** |

> ⚠️ **顺序强约束**：必须 2-A → 2-B → 2-C 全部完成，业务 LState 才真正单所有者，2-D（删锁）方可进行。删锁前做全仓审计（总纲 §5）。

---

## 2. 现状参考（已读码）

| 事实 | 位置 |
|---|---|
| listen 脚本回调路径 | `robot/robot.go:784` `createListenCallback`（`cbDef.Script!=""` 分支 798-828）；Go 原生 store 分支已存在 835-867 |
| listen 注册 | `robot/robot.go:716` `RegisterListen` → `conn.ListenResponse` |
| 分发双模式 | `network/connection.go:372` `dispatchListen`（`cb!=nil` 跑回调；`cb==nil` 写 buffer） |
| 心跳注册（动态 builder + TryLock） | `script/api_network.go:1047` `registerHeartbeat`（静态预编码 1098；动态 builder + `TryLock(luaMu)` 1125） |
| 心跳发送循环 | `network/heartbeat.go:90` `runHeartbeat`（调 `Builder()`） |
| RobotAdapter（codec 在业务 LState） | `adapter/robot_adapter.go`；创建于 `robot/robot.go:142` `NewRobotAdapter` |
| decode 调 adapter（自动加锁） | `network/connection.go:480` `decodeAndDispatch` → `adp.DecodeTCP/UDP` |
| 声明式编码调 adapter | `engine/action.go:966` `protocolEncode`；`:1006/1053/1128` `ExpectedRouteKey` |
| adapter Lua 模块（待删） | `script/api_network.go:1182` `loadAdapterModule`；注册于 `script/runtime.go:516` |
| `luaMu` 定义/锁点 | `robot/robot.go:51`、`:192`、`:614`、`:703`、`:798`；`adapter/robot_adapter.go:65-98`；`script/api_network.go:1125` |
| `Context.LuaMu` / `Context.Adapter` | `script/runtime.go:45` / `:42` |
| `withReleasedMu` 定义 + 调用点 | `script/api_network.go:19`（9 处）；`script/api_share.go`（20 处）；`script/api_utils.go:485`（sleep 1 处） |
| 密钥 API（**不改**） | `script/api_network.go:920-982`；`network/connection.go:115` `SetSecretKey/GetSecretKey` |

---

## 3. 子阶段实施

### 2-A：监听主流程消费化（可独立先行止血）

> 目标不是把所有推送都做成 callback，而是**下线脚本 callback**：decodeLoop 只做 decode + 分发/缓存/Go store，业务 Lua 只在主流程 goroutine 中执行。单独发布即消灭线上 `framework/53 ... listen_team_join` 类 panic。

- [ ] **ListenDef 形态收敛**：保留两类 listen：
  - 静默/缓存 listen：无 `script`、无 `store`，消息进入 route 队列，等待主流程 `tcpListen`/`udpListen` 或 `network.wait_listen` 消费。
  - 声明式 store listen：`s2cProto + store`，仅允许 Go 原生 parse + state.SetPath，适合简单状态型推送；默认**不再入 queue**，避免同一推送先 store 又被主流程二次处理。
  - **删除脚本回调 listen**：`ListenDef.script` 不再作为 callback 入口；`robot/robot.go:785-829` 脚本 cb 分支下线；v2 配置中出现 `script` 应直接校验失败。
- [ ] **listen 缓存从单槽升级为可配置小队列**：
  - 在 `ListenRef`（注册节点侧）增加 `queueSize *int`，缺省默认 `1`；显式配置 `queueSize<=0` 是配置错误（区分“未写默认”和“写了非法 0”，不做兼容兜底）。
  - `queueSize` 是**监听注册属性**，不是 `tcpListen`/`udpListen` 消费属性；消费动作不允许改底层缓存容量。
  - 注册 listen 时按 `(service, proto, routeKey)` 创建队列容量；同一连接同一 `routeKey` 重复注册必须完全一致才幂等，否则 fail loud（不同 queueSize、缓存/store 模式冲突、不同 store 定义都报错，不自动扩容/改模式）。
  - route 队列实现为固定容量 ring buffer：`map[string]*listenQueue` 查 route，`listenQueue` 内部 `[]*Message + head + size + dropped + sync.Mutex`；不用 `sync.Map`/channel/container list。
  - `dispatchListen` 对缓存 listen 写对应 ring queue；队满采用**覆盖 head（丢最旧、保最新）**策略，并记录 debug/metric（避免内存无界增长，且压测更关心最新事件）。
  - `GetListenResp` / `tcpListen` / `udpListen` 从 ring queue FIFO 弹出；`queueSize=1` 时表现等价于当前单槽。
  - `tcpListen`/`udpListen` v1 每次只 pop 一条；批量消费由 flow loop 或主流程 Lua 循环 `network.wait_listen` 实现，不在 action 上新增 `maxCount`。
  - 高频/事件型 route 显式配置大于 1；状态覆盖型 route 保持默认 1。
- [ ] **历史脚本 callback 迁移到主流程消费点**：
  - ranked：`teamNotifyInvite`、`teamJoin` 改为 memberPrepare 内 `network.wait_listen`/声明式 `tcpListen` 消费；`teamJoin code==0` 标志与 Redis 成员写入迁入 `ranked_team_prepare.lua` 主流程轮询点。
  - `teamUpdateInfo` 可按需要选择声明式 store（维护最近状态）或主流程短 poll。
  - guild：原 `listen_guild_*` 复杂过滤/自检/批量清理由 guild 主流程消费推送后执行，不再跑 callback Lua。
- [ ] 删除/精简 `conf/scripts/listen_team_*.lua`、`conf/scripts/listen_guild_*.lua`；仍需保留的复杂逻辑合并到对应主流程脚本。
- [ ] **验收**：全仓 `ListenDef.script` 作为 callback 零使用；decodeLoop/connectionPump 不进入业务 Lua；长跑 `framework/53 ... listen_*` 归零；多排/社团推送成功率不降。

#### 2-A 实施切片（按顺序落地）

1. **Schema 与校验先行**
   - `engine.ListenRef` 增加 `QueueSize *int json:"queueSize,omitempty"`。
   - flow 校验阶段计算有效值：缺省 `1`；显式 `queueSize<=0` 直接报配置错误。
   - `ListenDef.script` 进入禁用清单：v2 配置中出现 `script` 直接报错；不保留兼容迁移函数。
   - 校验同一连接同一 `routeKey` 的重复注册：完全一致才允许幂等；queue/store 模式或 queueSize 冲突时报错。

2. **Network 层先实现 queue 能力，不改业务语义**
   - 新增 `listenQueue` 固定容量 ring buffer，提供 `Push(*Message) dropped bool`、`Pop() (*Message, bool)`、`Dropped() uint64`、必要时 `Clear()`。
   - `Connection` 将现有 `listenMsg map[string]*Message` 替换为 `listenQueues map[string]*listenQueue`。
   - `dispatchListen` 命中缓存 listen 时 `Push`；队满覆盖最旧，记录 dropped 计数。
   - `GetListenResp(routeKey)` 改为 FIFO `Pop`，上层接口语义保持“一次取一条”。
   - 这一片完成后，即使暂时仍在旧 decodeLoop/listenLoop 下，也能先验证 queue 行为，降低 pump 一次性改动风险。

3. **Robot 注册接线**
   - `RegisterListen` 读取 `ListenRef.queueSize` 有效值，并按 listenRef 的 `server` 串 codec 计算 `routeKey`。
   - 静默/缓存 listen 注册为 queue listener；声明式 store listen 注册为 Go store callback，默认不入 queue。
   - 删除 `createListenCallback` 中 `cbDef.Script != ""` 分支；脚本 callback 不再生成 `ListenCallBack`。
   - `ListenResponse` / `AddListener` 收敛为带 queueSize 的注册入口：`RegisterListen(routeKey, cb, queueSize)`；旧名字可在同一切片内直接改掉，避免双 API 并存。

4. **Engine / NetSender 消费侧接线**
   - `engine.NetSender` 的 `EnsureTCPListener` / `EnsureUDPListener` 增加 `queueSize int` 参数；`robot.netSenderAdapter` 同步实现。
   - `tcpListen` / `udpListen` action 不新增容量字段，只按 routeKey 从 queue FIFO pop 一条。
   - Lua `network.wait_listen` / `network.ensure_listener` 若仍保留注册能力，默认 queueSize=1；复杂队列容量只由 flow `listenRefs` 显式配置。

5. **配置迁移：先 ranked/team，后 guild**
   - ranked：`teamNotifyInvite`、`teamJoin` 改为空 listen 或仅缓存 listen，由 `ranked_team_prepare.lua` 主流程用 `network.wait_listen` 消费；`teamJoin code==0` 的 Redis 成员状态写入迁回主流程。
   - `teamUpdateInfo` 可先保留声明式 store，若主流程需要逐条处理再改缓存 listen。
   - guild：`listen_guild_*` 不再作为 callback；主流程在需要维护 guild 状态的位置消费对应推送并执行原脚本逻辑。
   - 配置中所有 `script` callback 删除后再启用禁用校验，避免半迁移状态无法启动。

6. **验收切片**
   - 静态：全仓 `ListenDef.script` callback 用途、`RunCallbackScript` 注册路径、`listen_team_*.lua`/`listen_guild_*.lua` callback 引用归零。
   - 单测/小测：构造 queueSize=1/2 的推送，验证 FIFO、覆盖最旧、dropped 计数。
   - 功能：ranked 邀请/入队、guild 更新/退出/被踢在主流程消费下状态正确。
   - 长跑：`framework/53 ... listen_*` 归零，listen queue dropped 只出现在预期高频 route，且不导致业务卡死。

### 2-B：flow action 声明式心跳

> 已定：采用 flow action 注册心跳，而不是 Lua API `register_*_heartbeat(..., builder_fn)`。动态性限定为 state/counter/timestamp 等**动态取值**，不允许 heartbeat goroutine 执行业务 Lua/Redis/HTTP。
>
> ⚠️ **层级澄清（避免与总纲 §3.1.2 冲突）**：这里的 `counter`/`timestamp` 是**心跳 body binding**，在 **engine/pump runtime 层**求值（其状态由心跳 runtime 维护、写线程安全 state），**不进 codec**。总纲 §3.1.2 v1 拒绝的是 **codec 头字段 `role:"value"` 的 `counter`/`timestamp` 取值源**（那会让无状态 codec 单例持有每连接状态）。两者同名但分属不同层，各自结论均成立、互不矛盾。

- [ ] 新增声明式 action pattern：`tcpHeartbeat` / `udpHeartbeat`，字段包括 `service`、`intervalMs`、`route`、可选 `c2sProto`、`bindings`、`skipWhenMissing`；`skipWhen` 若保留到 v1.1，也只能支持 state 条件子集，禁止 Lua 条件。
- [ ] 心跳 Go builder 每 tick：读取线程安全 state → 按 bindings 构造 proto body（或空 body）→ 返回 `(body, skip, err)`；`connectionPump` 按连接当前密钥调用 `c.adp.EncodeTCP/UDP` → `Send`。缺 required 字段或 skipWhenMissing 命中则跳过本 tick 并按限频记录日志。
- [ ] 支持心跳专用动态源：`counter`（心跳私有计数器，必要时可写 state）、`timestamp`（ms/s）。v1 bindings 只允许安全子集：`fixed` / `state` / `counter` / `timestamp`，不开放随机类 binding，避免心跳携带业务随机逻辑。
- [ ] `network/heartbeat.go:90` 独立 `runHeartbeat` goroutine 下线；heartbeat runtime 并入 `connectionPump`，由 pump timer/control 驱动；删除 `TryLock(luaMu)` 相关注释/超时兜底（`heartbeat.go:57-67` 可简化）。
- [ ] `script/api_network.go:1047` `registerHeartbeat`：下线 Lua builder 分支（1108-1156 的 `TryLock`/`withReleasedMu`/`CallByParam`）；旧 Lua 注册 API 要么删除，要么只保留为调用声明式 action 的主流程同步入口（不得保存 Lua function）。
- [ ] **依赖说明**：2-B 的配置/action schema 可先落；真正纯 Go runtime 编码依赖 2-C 的 Go codec + CodecResolver。2-D 必须等 2-A、2-B runtime、2-C 全部完成。
- [ ] **验收**：心跳按间隔稳定发送、字段正确；主流程阻塞 `tcpListen`/Lua/Redis 时心跳仍发送；无 `TryLock(luaMu)`、无 Lua builder。

#### 2-B 实施切片（按顺序落地）

1. **Action schema 与校验先行**
   - `engine.ActionDef` 新增 pattern：`tcpHeartbeat` / `udpHeartbeat`。
   - 必填字段：`service`、`intervalMs`、`route`；`intervalMs<=0` 配置错误。
   - 可选字段：`c2sProto`、`bindings`、`skipWhenMissing`。
   - v1 不实现 Lua 条件；如保留 `skipWhen` 字段，也只能接受 state 条件子集，出现 `lua:` 直接校验失败。
   - heartbeat bindings v1 只允许 `fixed` / `state` / `counter` / `timestamp`；随机类、stateRandom 类、map/list 随机类全部配置错误，避免把业务随机逻辑塞进心跳 tick。

2. **Heartbeat binding 子集独立实现**
   - 不直接复用完整 `bindFields` 热路径，避免把过宽 binding 能力带进心跳；抽出或新增 `BuildHeartbeatBody` 使用安全子集。
   - `fixed`：固定值写 proto 字段。
   - `state`：从线程安全 state 读取；字段缺失时若 required/skipWhenMissing 命中则本 tick skip，否则按现有 binding 语义写 nil/零值失败。
   - `counter`：heartbeat runtime 私有计数器，支持 `start`、`step`，每次成功构建或每个 tick 的递增时机必须固定为“构建成功后递增”。
   - `timestamp`：支持 `unit: "ms" | "s"`，缺省 `ms`；由 Go runtime 获取当前时间，不通过 Lua `utils.time_ms`。
   - 有 `c2sProto` 时：创建 proto → 写 bindings → serialize → 返回 body；无 `c2sProto` 时：只允许无 bindings 或后续显式 raw body 方案，v1 不做 Lua/raw builder。

3. **ActionExecutor 注册路径**
   - `ActionExecutor.Execute` switch 增加 `tcpHeartbeat` / `udpHeartbeat` 分支。
   - 执行动作只做“注册/更新心跳”，不等待下一次发送，也不把每 tick 发送计入该 action 的网络延迟样本。
   - 编译 Go-only `HeartbeatBuilder`：闭包只捕获 state/proto factory/bindings/counter，不捕获 Lua LState，不调用 Redis/HTTP/network request。
   - 注册失败使用 `ActionError` 配置/网络类错误码；停止任务 context canceled 仍按 executor 取消语义向上传播。

4. **NetSender / robot 接线**
   - `engine.NetSender.RegisterTCPHeartbeat` / `RegisterUDPHeartbeat` 的参数从旧 Lua builder 语义收敛为 Go-only `HeartbeatConfig`。
   - `robot.netSenderAdapter` 按 transport+service 拼 `server` 串找连接；连接不存在、已关闭、连接(`server`串)未映射 codec 时 fail loud。
   - 2-B schema 可先接到旧 adapter；最终 2-C 后必须改为 `CodecResolver.Resolve("<proto>:<service>")` + 连接自持 `c.adp`。

5. **Network heartbeat runtime 先收敛语义，再并入 pump**
   - 过渡阶段可以先让 `network/heartbeat.go` 的旧 goroutine 调 Go-only body builder，删除 Lua TryLock/builderFn 路径，先实现“心跳不进 Lua”。
   - 2-C connectionPump 落地时，再删除独立 heartbeat goroutine，把 runtime/timer/control 并入 pump。
   - 最终 `HeartbeatBuilder` 返回 `(body []byte, skip bool, err error)`；network 层负责读取连接 secretKey、调用 `c.adp.EncodeTCP/UDP`、`Send`。
   - builder 错误或 skip 只影响本 tick，不关闭连接；连续错误可限频 warn，并暴露计数，避免热日志。

6. **旧 Lua 心跳 API 下线**
   - `script/api_network.go` 删除 `register_*_heartbeat(service, interval, route, builder_fn)` 的 Lua function 保存能力。
   - 若仍保留 Lua API 名称，只能作为主流程同步入口注册“无 Lua builder 的声明式心跳”，不得保存 Lua function；更推荐直接删除旧 API 并迁移 flow action。
   - 删除 `TryLock(luaMu)`、`withReleasedMu`、`CallByParam`、builder 超时兜底等旧心跳路径。

7. **配置迁移**
   - 把当前登录/连接后由 Lua 注册的 TCP/UDP 心跳迁移为 flow action 节点，位置放在对应连接和密钥交换完成之后。
   - 旧 Lua 脚本中的 heartbeat builder 若只构造固定包/状态字段/counter/timestamp，则改为 `tcpHeartbeat` / `udpHeartbeat` bindings。
   - 若发现 builder 做了 Redis/HTTP/业务分支，不能塞进 heartbeat；应改为主流程 state 维护，heartbeat 只读取 state 快照。

8. **验收切片**
   - 静态：`register_tcp_heartbeat` / `register_udp_heartbeat` 不再保存 Lua function；`TryLock(luaMu)` 心跳路径归零。
   - 功能：主流程阻塞 `network.wait_listen`、Lua `utils.sleep`、Redis share 调用时，心跳仍按间隔发送。
   - 字段：fixed/state/counter/timestamp 对拍旧 builder 输出；counter 构建失败时递增语义符合设计。
   - 压测：心跳 builder 错误限频日志，无热路径刷屏；停止任务时 heartbeat runtime 退出，无 goroutine 泄漏。

### 2-C：codec 移出业务 LState（核心）

> **按连接绑定（per-connection），非全局单例**：codec 以 `CodecResolver`（`server串(<proto>:<service>) → Adapter`）持有，每连接一份、可显式共享，详见总纲 §2/决策 #8 与下文形态。

#### CodecResolver 形态（已定：纯显式映射 + 接口 + 删 Dialer 兜底）

```go
// adapter/codec_resolver.go —— 接口（engine/robot/manager 依赖接口，可 mock）
// key = server 串 "<proto>:<service>"（如 "tcp:logic"/"tcp:battle"/"udp:battle"）
type CodecResolver interface {
    Resolve(server string) Adapter   // 未映射→nil，调用方 fail loud
}

// 纯显式映射，无 fallback（遵循「禁止兼容性兜底」：缺映射不兜底、直接报错）
type codecResolver struct {
    byServer map[string]Adapter          // 每条声明的连接(server串) → 其 codec
}
func (r *codecResolver) Resolve(server string) Adapter { return r.byServer[server] }
func NewCodecResolver(byServer map[string]Adapter) CodecResolver { ... }
```

- **构建（T4 loader 侧）**：**loader 枚举 config/flow 声明过的连接（`server` 串）**，按映射读各自 codec 文件 → `adapter.NewSchemaAdapter` → 填进 map。默认每连接一份；**多连接显式指向同一 codec 文件时 dedup**——同一文件编译一次、多连接共享同一无状态实例。**不靠 fallback 表达"共用一个 codec"**，靠 config 把相同的连接都指向同一文件。
- **fail loud**：flow action/listen 引用未登记的连接 → `Resolve` 返回 nil → 拨号/编码处当场报"连接 `<proto>:<service>` 无 codec 配置"，不在运行时冒诡异字节错。
- **key 构造**：encode 侧的 transport 来自 action pattern（`tcp*`/`udp*`），service 来自 `def.Service` → 拼 `<proto>:<service>`；dial 侧 transport 来自 `DialTCP`/`DialUDP`、service 来自连接 → 同样拼 `server` 串。
- **接线表**（替换所有"单一 adp"持有点）：

| 当前 | 改成 |
|---|---|
| `ManagerConfig.Adapter`（manager.go:41） | `ManagerConfig.CodecResolver`（共享 resolver 透传给每 Robot） |
| `Robot.adp`（robot.go:54）+ `NewRobotAdapter`（136-151） | `Robot.resolver CodecResolver`（删 RobotAdapter 整条） |
| `ActionExecutor.adp`（action.go:968/1006/1053/1128） | `ActionExecutor.resolver`；`resolver.Resolve(proto+":"+def.Service).Encode*/ExpectedRouteKey`（transport 由 pattern 定；`protocolEncode` 随之加 `server` 入参） |
| `Dialer.dial` 的 `d.server.adp` 兜底（gnet.go:366-368） | **删除兜底**；robot 拨号前 `resolver.Resolve(proto+":"+service)` 再传入，nil→报错 |
| `script.Context.Adapter`（runtime.go:42） | **删除**（adapter Lua 模块已删，业务 Lua 不再 encode） |

- **decode 侧不碰 resolver**：`Connection.adp` 在 dial 时按 `server` 串一次性解析定终身（`connection.go:54`/`StartDecodeLoop`），此后 decode 直接用 `c.adp`。resolver 只管"出站方向（encode/heartbeat）+ 拨号"。

#### 连接侧 goroutine 收敛（已定：不 inline decode，单 pump）

`connectionPump` 是 network 内部实现细节，不泄漏到 flow/engine/Lua。外层接口只表达三类能力：注册监听、消费监听、注册/停止心跳。

Go codec 后，旧的三协程模型（`decodeLoop` + `listenLoop` + heartbeat goroutine）不再长期保留，且**不采用 inline decode**。inline decode 虽最省 goroutine，但 codec pipeline 可能包含解压、校验、hash、加密等线性/重 CPU 步，放进 gnet event loop 会重新引入 event loop 卡顿风险。

因此本轨道固定采用 **connectionPump**：每条连接只保留一个 `pump` goroutine，统一处理 inbound decode、listen 队列/store 分发、heartbeat timer/control。

`connectionPump` 伪代码：

```go
func (c *Connection) pump() {
    for {
        if c.heartbeatDue() {       // 到期优先，避免 inbound backlog 饿死心跳
            c.sendHeartbeat()
            c.resetHeartbeatTimer()
            continue
        }

        select {
        case frame := <-c.inboundCh:
            c.handleInbound(frame)  // Go decode -> responseMap/listen queue/store
            c.drainInboundBounded() // 最多处理 N 条后回到外层，给 heartbeat/control 机会
        case <-c.heartbeatTimer.C:
            c.sendHeartbeat()
            c.resetHeartbeatTimer()
        case cmd := <-c.controlCh:
            c.handleControl(cmd)    // register listen / register heartbeat / stop 等
        case <-c.ctx.Done():
            c.drainAndReturnBuffers()
            return
        }
    }
}
```

- `gnet.OnTraffic` 只切帧并 `EnqueueInbound`，不做业务逻辑；`inboundCh` 满仍关闭连接释放压力。
- `connectionPump` 是 network 内部调度模型，不进入 flow/engine/Lua 配置语义；外层只感知注册监听、消费监听、注册心跳。
- `listenCh/listenLoop` 删除，listen 分发并入 pump：request-response 优先；命中 listen 后，要么 Go store callback，要么写 per-route ring queue（`queueSize`，FIFO，满时覆盖 head=丢最旧保最新）。
- listen 注册/消费不强制走 pump control：`Connection` 用 `listenMu + map` 管 route 注册表，pump dispatch 时只读注册表；每个 route queue 自带 `sync.Mutex` 支持主流程 `GetListenResp` 直接 FIFO pop。
- listen 队列采用固定容量 ring buffer：`map[string]*listenQueue` 管 route，`listenQueue` 内部 `[]*Message + head + size + dropped + sync.Mutex`；不用 `sync.Map`/channel/container list。
- heartbeat 不再启动独立 goroutine；`RegisterHeartbeat` / `StopHeartbeat` 通过 `controlCh` 更新 pump-owned runtime。heartbeat builder 必须是 Go-only body builder，不进 Lua/Redis/HTTP，不直接 encode/send。
- pump 不进入业务 LState；复杂推送业务由主流程 `tcpListen`/`udpListen` 消费后执行。
- 为保证心跳及时性，pump 必须具备两条硬约束：**heartbeat due 优先检查** + **inbound bounded batch**（禁止一直 drain inbound 导致 timer/control 饥饿）。

- [ ] 启动路径构造 **CodecResolver**（`server串 → Adapter`：T4 loader 枚举连接、各读 codec 文件 → `adapter.NewSchemaAdapter` 编译 + 同文件 dedup），替换 `NewLuaAdapter`。（loader 接线属 T4，本轨道改"拿到 schema 后如何编译/按连接绑定"。）
- [ ] `robot/robot.go`：删除 `r.adp` per-robot 字段（54）与 `NewRobotAdapter` 调用（136-151）；Robot 持有/引用 **CodecResolver**（经 `ManagerConfig` 透传，`manager.go:41`），不再持单一 adapter。
- [ ] `robot/robot.go` Dial：`DialTCP/DialUDP`（392/439）按**连接的 `<proto>:<service>`** 从 resolver 解析 adapter 再传入；`network/gnet.go:347` `dial` 启动 `connectionPump(adp,isUDP)`（替代 `StartDecodeLoop`），每条连接存自己的 `c.adp`。decode 变纯 Go、并发安全、按连接隔离。
- [ ] `engine.NetSender` / `robot.netSenderAdapter`：`EnsureTCPListener` / `EnsureUDPListener` 增加 `queueSize int` 参数；`ListenRef.route` 先经对应 `server` 串 codec 计算 `routeKey`，再注册到 `Connection.RegisterListen(routeKey, cb, queueSize)`。`queueSize` 只来自注册侧 `ListenRef`，`tcpListen`/`udpListen` 消费侧不传容量。
- [ ] **encode 侧**：发包按 action pattern 推导 transport + `def.Service` 拼 `server` 串，再从 resolver 取 adapter 调 `EncodeTCP/UDP`/`ExpectedRouteKey`（纯 Go、无锁）。`script/runtime.go` 的 `Context.Adapter`（42）随之删除或改为按 `server` 串解析的 helper；类型从 `*adapter.RobotAdapter` 改回接口/resolver。
- [ ] `script/api_network.go`：所有 `*Locked` 编码调用点（`buildPacket:192`、UDP `529/769/827`、`doTCPRequest:427` 的 `ExpectedRouteKeyLocked`）改为**按 `server` 串解析**的普通方法调用（纯 Go、无锁）。
- [ ] **删除 adapter Lua 模块**：`script/api_network.go:1182` `loadAdapterModule` 及 5 个函数；`script/runtime.go:516` 的注册行。（已确认 `conf/scripts` 无人用。）
- [ ] **从业务 LState 移除 codec 依赖**：不再向业务 LState 注入 codec.lua / bit / zlib / crypto（这些原在 `NewRobotAdapter` 内 `adapter/lua_adapter.go:456`）。业务 LState 仅保留 robot/proto/network/utils/log/json/share 模块。**已确认安全**：`conf/scripts` 对 `bit`/`crypto`/`zlib`/`adapter` 四模块**零依赖**（grep 实测无命中），直接删，无需逐脚本审计。
- [ ] 删除 `adapter/robot_adapter.go` 整个文件；`adapter/lua_adapter.go` 退化为只剩……→ 实际整文件可删（codec 走 Go，errorMap 走 Go），由 T4 决定 `NewLuaAdapter` 的最终去留（T2 这里确保无人再调它的 encode/decode/NewRobotAdapter）。
- [ ] **验收**：decode/encode 全程无 Lua；业务 LState 不含 codec 模块；`go build` 通过；功能回归正常。

#### 2-C 实施切片（按顺序落地）

1. **Adapter 接口保持不变，先引入 Go schema adapter**
   - `adapter.Adapter` 9 方法签名不变，所有调用方先不感知 codec 实现从 Lua 换成 Go。
   - Track 1 产出的 `adapter.NewSchemaAdapter(schema, errors)` 作为唯一新实现；不新增 `Decode*` 返回 err，不做 expose/headerFields。
   - `DescribeError` 从 `errors.json` 编译出的 map 读取；无映射返回空字符串。
   - 保持 `HeaderSize` / `BodyLength` 热路径纯 Go，供 gnet event loop 切帧。

2. **CodecResolver 拓扑替换 per-robot adapter**
   - 新增 `CodecResolver` 接口：`Resolve(server string) adapter.Adapter`（key=`<proto>:<service>`），未映射返回 nil。
   - `ManagerConfig.Adapter` 改为 `ManagerConfig.CodecResolver`；Manager 创建 Robot 时只透传 resolver。
   - `Robot.adp` 与 `NewRobotAdapter` 创建路径下线，Robot 不再持有业务 LState 绑定的 adapter。
   - resolver 显式枚举 config/flow 声明的连接（`server` 串）；默认每连接一份 codec 文件，多连接显式指向同一文件时 dedup 为同一无状态实例；必须由 loader 把每条连接都填入 map，不允许 fallback。
   - 所有 `Resolve(server)==nil` 的位置 fail loud，错误信息包含 `server` 串与动作/连接上下文。

3. **Dial / decode 侧先按连接固定 adapter**
   - `DialTCP` / `DialUDP` 前按 `<proto>:<service>` 解析 adapter，nil 直接报错，不进入 Dialer。
   - `Dialer.dial` 删除 `d.server.adp` 兜底；连接必须由上层注入 adapter。
   - `Connection` 在创建/启动时保存 `c.adp` 与 `isUDP`，该连接生命周期内不再查 resolver。
   - 旧 `StartDecodeLoop(adp,isUDP)` 过渡为 `StartPump(adp,isUDP)`；若 pump 分阶段落地，先保证 decodeLoop 使用的是连接固定 Go adapter。
   - decode 失败仍按现有 3-tuple 语义处理：空 routeKey 丢帧并 warn，请求侧自然超时。

4. **Encode 侧全部改为按连接解析**
   - `ActionExecutor` 不再持单一 `adp`，改持 resolver 或一个按 `server` 串 encode 的小接口。
   - `protocolEncode` 增加 `server` 入参，或增加 `transport+service` 入参后内部拼 `<proto>:<service>`；再按该 `server` 串解析 adapter 后调用 `EncodeTCP/UDP`。
   - `ExpectedRouteKey` 的所有调用点也按 `server` 串解析，避免不同连接协议 routeKey 规则混用。
   - `tcpRequest` / `udpRequest` / `tcpSend` / `udpSend` / listen routeKey 计算 / heartbeat encode 全部走同一 `server` 串解析规则。
   - 不允许“找不到连接 codec 就用默认 codec”这类兼容兜底。

5. **script/network API 去除 adapter 锁定调用**
   - `script.Context.Adapter` 删除或改为 resolver/server encode helper；业务 Lua 不再持有 adapter 对象。
   - `script/api_network.go` 中所有 `Encode*Locked` / `Decode*Locked` / `ExpectedRouteKeyLocked` 调用改为：按 Lua API 的 TCP/UDP 函数名 + service 参数拼 `server` 串解析 Go adapter → 普通 `Encode*` / `ExpectedRouteKey`。
   - Lua network API 仍可发包/请求/监听，但 Lua 只提供业务 proto/body，codec encode/decode 全部在 Go adapter 中完成。
   - 若某 Lua API 过去直接暴露 `adapter.*` 模块能力，则不迁移，直接删除；当前 `conf/scripts` 已确认无依赖。

6. **删除业务 LState codec 依赖**
   - 删除 `adapter/robot_adapter.go` 及其调用。
   - 删除业务 LState 注入的 `adapter` / `bit` / `zlib` / `crypto` 模块；这些只服务旧 codec.lua，不再进入 Robot 业务 VM。
   - `adapter/lua_adapter.go` / `conf/adapter/codec.lua` / `error.lua` 的最终删除由 T4 资源加载切换配合完成；T2 目标是保证 runtime encode/decode 无人再调用 Lua adapter。
   - `script/runtime.go` 的 `Context.Adapter`、`Context.LuaMu` 与 adapter 模块注册关系为 2-D 删除锁做前置清理。

7. **connectionPump 最终替换 decodeLoop/listenLoop/heartbeat goroutine**
   - `gnet.OnTraffic` 只切帧并 `EnqueueInbound(frame, recvAt)`，不调用 decode。
   - `Connection.StartPump(adp,isUDP)` 初始化 `inboundCh`、`controlCh`、`pumpDone`、连接固定 adapter 与协议方向。
   - pump 处理 inbound：decode → request-response 优先 → listen store/queue dispatch。
   - pump 处理 heartbeat：timer/control 更新 runtime；到期时调用 Go-only body builder → `c.adp.EncodeTCP/UDP` → `Send`。
   - 删除 `listenCh/listenLoop`；删除独立 heartbeat goroutine；`WaitDecodeDone` / `WaitListenDone` 收敛为 `WaitPumpDone` 或 Close 内部等待。
   - pump 退出时 drain/释放 inbound buffer，停止 timer，关闭 done；不得泄漏 goroutine。

8. **关闭与生命周期清理**
   - `Connection.Close` / `onClose` / `ctx.Done` 统一触发 pump 退出；主动 close 与被动断线仍保持现有 onDisconnect/onClosed 语义。
   - `RegisterListen`、`GetListenResp`、`RegisterHeartbeat` 在连接关闭后返回明确错误，不阻塞等待 controlCh。
   - `inboundCh` 满的策略保持关闭连接释放压力；日志包含 service/remote/route 上下文。
   - Stop 任务时不再存在等待 Lua builder 释放锁的超时路径。

9. **验收切片**
   - 静态：`RobotAdapter` / `NewRobotAdapter` / `loadAdapterModule` / `*Locked` codec 方法 / `Context.Adapter` 旧类型 / codec Lua 注入路径归零。
   - 静态：`Dialer.dial` 无 `server.adp` fallback；所有 encode/routeKey 计算都带 service。
   - 功能：TCP/UDP connect、request、send、listen、heartbeat 在多连接（`tcp:logic`/`tcp:battle`/`udp:battle`）配置下分别使用正确 codec。
   - 并发：`go test -race` 或小规模 race 回归，确认 Go schema adapter 无共享可变状态。
   - 长跑：无 decodeLoop/listenLoop/heartbeat goroutine 泄漏；主流程阻塞时 pump 仍处理响应/推送/心跳。

### 2-E：~~头字段暴露（expose）~~ — 已移除（v1 不做）

> 原计划让 decode 返回 `expose:true` 头字段给流程。已决定**砍除**：现协议无需读头字段（routeKey+headerErr 已够），`storeAs` 是发送侧机制（与 codec 无关、不删），`expose`/`headerFields` 延后 v1.1（总纲 §3.1.2/§3.2）。**因此 `DecodeTCP/UDP` 签名零改动**——`network`/`engine`/`tcp_request` 无需为头字段接线，本节任务全部取消。

### 2-D：删除 luaMu / withReleasedMu

> 前置：2-A/2-B/2-C 完成 + 全仓审计确认业务 LState 仅被运行循环触碰。

- [ ] 删 `script/api_network.go:19` `withReleasedMu` 定义 + 9 处调用（257/293/323/339/432/548/661/849/1162）：阻塞调用改为「直接阻塞当前 goroutine + 响应 `ctx`」。
- [ ] 删 `script/api_share.go` 20 处 `withReleasedMu`（直接阻塞 Redis 调用 + 响应 ctx）。
- [ ] 删 `script/api_utils.go:485` `utils.sleep` 的 `withReleasedMu`（直接 `select { <-time.After; <-ctx.Done }`）。
- [ ] 删 `robot/robot.go:51` `luaMu` 字段 + 全部 `Lock/Unlock`（192/614/703/798 等，回调路径已在 2-A 删）。
- [ ] 删 `script/runtime.go:45` `Context.LuaMu`。
- [ ] `network/connection.go:585` 关闭顺序、`network/heartbeat.go` 停止超时：复核并简化（不再有 `luaMu` 死锁风险）。
- [ ] **验收**：编译通过；长跑无 panic；主流程阻塞期间，网络收发/心跳由各自 goroutine 正常工作（它们已不依赖 `luaMu`，且 codec 纯 Go）。

#### 2-D 实施切片（按顺序落地）

1. **前置审计闸门：未通过不得删锁**
   - 确认 2-A：`ListenDef.script` callback 路径、`RunCallbackScript` 异步入口、listen 脚本注册全部归零。
   - 确认 2-B：heartbeat Lua builder、`TryLock(luaMu)`、保存 Lua function 的心跳注册路径归零。
   - 确认 2-C：codec encode/decode、adapter Lua 模块、`RobotAdapter`、业务 LState codec 注入路径归零。
   - 全仓搜索异步 goroutine 中访问 `*lua.LState` / `luaPool.Run*` / `L.CallByParam` 的路径；只允许 Robot 主流程 goroutine 执行 action/boolean Lua。
   - 该闸门必须作为实施前 checklist，不允许“先删锁再测”。

2. **删除 `withReleasedMu` 的调用点，保留阻塞语义**
   - `script/api_network.go`：请求、监听、连接、HTTP 等阻塞 API 直接在当前 Robot 主流程 goroutine 阻塞，并继续响应 `ctx.Done()`。
   - `script/api_share.go`：Redis/share 阻塞调用直接阻塞当前主流程 goroutine；取消时返回明确错误/失败码。
   - `script/api_utils.go`：`utils.sleep` 改为 `select { case <-time.After(d): case <-ctx.Done(): }`，不再释放锁。
   - 删除时不要引入后台 goroutine 帮 Lua 等待；业务 Lua 单所有者模型下，阻塞当前主流程就是目标语义。

3. **删除 `withReleasedMu` 定义与 Context 依赖**
   - 删除 `script/api_network.go` 中 `withReleasedMu` helper。
   - 删除 `script.Context.LuaMu` 字段及所有初始化赋值。
   - 所有 Lua API 函数不再假设可临时释放/重新获取锁；错误信息中也不再出现“获取 luaMu 超时/释放锁”等旧概念。
   - 编译期清理所有残留引用，而不是留下空实现兼容。

4. **删除 Robot 层 `luaMu` 字段与锁点**
   - 删除 `Robot.luaMu` 字段。
   - 删除主流程执行 action/boolean Lua 前后的 `Lock/Unlock`；主流程 goroutine 天然独占 LState。
   - 删除 listen callback、heartbeat builder、codec adapter 等已下线路径中的锁点残留。
   - 若发现仍有非主流程代码需要锁才能碰 LState，说明前置闸门失败，应回退到对应 2-A/2-B/2-C 修正，而不是保留局部锁。

5. **RuntimePool / Lua 调用语义收敛**
   - `RuntimePool.RunActionScript` / `RunBooleanScript` 保持同步调用，不再承担并发互斥职责。
   - `RunCallbackScript` 若仅为旧 listen callback 服务，删除或标记为无人调用后删除；避免保留未来误用入口。
   - Lua 脚本仍可以阻塞调用 network/share/utils；阻塞只影响当前 Robot 主流程，不影响 connectionPump 的网络收发/心跳。

6. **网络关闭与停止路径简化**
   - `network/heartbeat.go` / connectionPump 停止逻辑删除“等待 Lua builder 释放锁”的超时兜底。
   - `Connection.Close` / `Manager.Stop` / `Robot.Stop` 只需要取消 ctx、关闭连接、等待 pump/主流程退出。
   - 停止时如果主流程正阻塞在 network/share/sleep，应通过 ctx 取消唤醒；不得依赖释放 luaMu 让其他 goroutine 注入中断。

7. **日志与错误码清理**
   - 删除或改写所有含 `luaMu`、`withReleasedMu`、`TryLock`、`释放 Lua 锁`、`获取 Lua 锁失败` 的日志/注释。
   - 不新增兼容性错误码；若阻塞 API 因 ctx 取消返回，沿用现有 canceled/stop 语义，避免停止时刷 errorStrategy。
   - 保持日志中文，面向操作者说明“任务已取消/连接已关闭/等待被中断”。

8. **验收切片**
   - 静态：`grep` 清零 `luaMu`、`withReleasedMu`、`Context.LuaMu`、`TryLock`、`RunCallbackScript` 异步用途、`RobotAdapter`、`loadAdapterModule`、`*Locked` codec 方法。
   - 编译：`go build ./...`，必要时 `go vet ./...`。
   - 功能：Lua 主流程中执行 `network.wait_listen`、Redis share、`utils.sleep` 时，其他连接的 connectionPump 仍能处理响应/推送/心跳。
   - 停止：任务停止能取消阻塞中的 Lua API，日志不刷屏，无 goroutine 泄漏。
   - 长跑：1–2 小时无 Lua panic、无 `framework/53`、无心跳超时异常扩大。

---

## 4. 不需要改动的点（防止误改）

- **密钥 API**（`set_*_secret_key`/`get_*_secret_key`，`api_network.go:920-982`）：与 codec 无关，只读写 `Connection.secretKey`。**保持不变**，Go adapter 仍按参数接收 key。
- **`engine/action.go` 编码调用点（encode 侧）**：`EncodeTCP/EncodeUDP/ExpectedRouteKey` 签名不变，这些调用点**无需改**（实现从 Lua 换成 Go 对它们透明）。
- **decode 侧接口**：`DecodeTCP/DecodeUDP` 签名**零改动**（仍 3-tuple，expose 已砍，见 2-E/总纲 §3.2），connectionPump 只替换调度模型，不为头字段改接口。本轨道**无任何 codec 接口签名改动**。
- **`HeaderSize/BodyLength`**：本就纯 Go，gnet `OnTraffic` 仍只依赖它们做帧切割；OnTraffic 后续从 `decodeCh` 入队改为 `inboundCh` 入队。
- **业务流程 Lua 脚本的阻塞语义**：删锁后仍是直接阻塞当前 Robot 主流程 goroutine；网络收发/心跳在每连接 connectionPump 中继续。注意：原 listen callback 脚本的复杂逻辑会合并进对应主流程脚本，这是 2-A 的明确改动，不属于“误改”。

---

## 5. 验收（本轨道）

- [ ] 全仓 `grep`：`luaMu`、`withReleasedMu`、`RobotAdapter`、`NewRobotAdapter`、`loadAdapterModule`、`*Locked`、`Context.Adapter`(旧类型)、`ListenDef.script`/`RunCallbackScript`（异步 callback 用途）**零残留**（注释/历史 plan 除外）。（`storeAs` 是发送侧机制、保留不删，不在零残留清单内。）
- [ ] `go build ./...` + `go vet` 通过；现有测试通过。
- [ ] 长跑 1–2 小时：零 `framework/53`/nil-pointer panic；多排成功率 ≥ 现状；心跳/推送字段正确。
- [ ] 业务 LState 内存较现状下降（无 codec/bit/zlib/crypto 模块）。

---

## 6. 风险

| 风险 | 缓解 |
|---|---|
| 过早删锁引并发 bug | 严格 2-A→2-B→2-C→2-D 顺序；删前全仓审计无异步 Lua |
| teamJoin 迁移改变多排语义 | 重点测双排/三排（见 `robot-lua-single-thread-design.md` §8） |
| 心跳 builder 曾做 binding 表达不了的事（自增序号等） | 2-B 前核实现有 builder 逻辑；序号等由 Go 维护计数器 |
| decodeLoop 改全局 adapter 后的并发正确性 | 依赖 T1 保证 adapter 并发安全（无状态/不可变）；加 `-race` 跑回归 |
