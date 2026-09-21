# 网络层与机器人管理

> **本文档对照实际代码编写，反映已实施状态。**
> 核对范围（2026-09-20，工作区 `D:\Gitee\stressbot`）：
> `network/`（gnet.go、connection.go、client.go、heartbeat.go、listen_queue.go、message.go、writetime.go、bufpool.go）、
> `robot/`（robot.go、scheduler.go、manager.go、cleanup.go）、
> `state/`（store.go、overlay.go、shared/）、
> `engine/`（action.go、heartbeat.go、heartbeat_plan.go）、
> `protocol/codec/`（engine.go、compressors.go、ciphers.go、inflate_cache.go、intern.go）、
> `protocol/protox/wirenav.go`、`internal/timerpool/timerpool.go`、`monitor/snapshot.go`。
> 所有 `文件:行号` 引用均经源码核对。

## 目录

1. [gnet 集成：事件循环与帧解析](#1-gnet-集成事件循环与帧解析)
2. [Connection 结构与生命周期](#2-connection-结构与生命周期)
3. [请求-响应机制](#3-请求-响应机制)
4. [持久化监听](#4-持久化监听)
5. [RTT 计时](#5-rtt-计时)
6. [连接级心跳](#6-连接级心跳)
7. [性能优化清单](#7-性能优化清单)
8. [机器人管理](#8-机器人管理)
9. [状态层](#9-状态层)

---

## 1. gnet 集成：事件循环与帧解析

### 1.1 Dialer 与事件循环配置

`Dialer` 封装 gnet 客户端引擎（network/gnet.go:336-341）。启动时显式配置多 eventloop（network/gnet.go:370-392）：

- `WithMulticore(true) + WithNumEventLoop(...)`：gnet 默认单 eventloop，瞬时建连密度高时（如 250 个机器人同时进入建连步骤）单 loop 的 channel 容易堆积，导致首包延迟落入服务端「建连后 N 秒未鉴权」窗口被 RST；多 loop 用 leastConnections 负载均衡把连接分散到不同 loop，不同连接的回调真正并发（network/gnet.go 注释）。事件循环数默认 `runtime.NumCPU()`，可经 `[network]` 段覆盖（`network.EngineConfig`，零值=默认，network/engine_config.go）。
- `WithLockOSThread(true)`：每个 eventloop 绑定独立 OS 线程，减少 Go 调度器把 loop goroutine 切到不同 OS 线程时的 syscall 抖动（network/gnet.go:368-369）。
- 读/写缓冲各 32KB、单包体上限 16MB，防畸形/恶意包 OOM；连同并发拨号上限 512 均可经 `[network]` 段调优（`network.EngineConfig.normalize`，network/engine_config.go），零值回退编译期默认。
- 同一时刻阻塞在 gnet Dial 内的连接数由 `dialTokens`（默认容量 512，network/gnet.go）限制，避免任务停止留下大量拨号 goroutine 持有 Lua action 与 Robot 资源（network/gnet.go 注释）。

并发安全性：`EventServer` 自身无可变共享状态；`connRegistry`（gnet fd → 业务 Connection 映射）用 RWMutex（network/gnet.go:30-57）；同一连接由 gnet 负载均衡固定绑定到一个 loop，不会跨 loop 并发。

### 1.2 拨号与 OnOpen：消除「注册前首包被丢弃」竞态

拨号经 `Dialer.dial`（network/gnet.go:428-489）：

1. ctx 快速拒绝（429-446）：任务停止后不再进入新拨号；
2. adapter nil 防御（450-457）：上层 `Robot.ConnectTCP/UDP` 已 Resolve 非 nil 注入，nil 视为编程错误直接报错，不静默回退全局默认 codec；
3. `gnet.Client.DialContext` 携带 `dialPayload{conn, adp, isUDP}` 透传给 OnOpen（464-468）。

gnet 保证同一连接的 OnOpen 先于任何 OnTraffic 处理，且 EnrollContext 会等 OnOpen 完成后才从 DialContext 返回。因此 bind（注入 sendFunc/closeFunc）+ registry 注册 + StartPump 全部在 `OnOpen` 内完成（network/gnet.go:84-115），连接在首个 OnTraffic 触发前即已注册且 pump 就绪，首包不再被丢弃（竞态编号 H5，network/gnet.go:85-87 注释）。`StartPump` 用 CAS 幂等 + 协程池异步起 pump goroutine，从事件循环 goroutine 调用安全非阻塞（network/gnet.go:106-107、610-612）；提交失败回滚全部 pump 状态并返回错误，允许重试（network/connection.go:619-627）。

拨号返回后若发现 ctx 已取消/Dialer 已关闭，主动关闭 gnet 连接触发 OnClose 回收（network/gnet.go:472-481）。

### 1.3 OnTraffic 帧解析流水线：事件线程零解码

`OnTraffic` 运行在 gnet 事件循环 goroutine 上，**严禁同步阻塞**——阻塞会让该 loop 上所有连接的 I/O 一起冻结，是服务端 RST 断连雪崩的源头（network/gnet.go:179-190 注释）。它只做两件纯 Go 操作（network/gnet.go:191-290）：

1. **帧分割循环**：
   - `InboundBuffered() < headSize` → 不足一个头，返回等下次可读（196-199）；
   - `Peek(headSize)` → `es.adp.BodyLength(headBuf)`（编译期定型的纯字段读取，无解析开销，network/gnet.go:68-69）；
   - `bodyLen < 0 || > maxBodyLen` → 协议头非法，Warn + 关闭连接（208-223）；
   - 不足整帧（`available < totalLen`）→ 等待更多数据（225-228）；
   - `Read` 整帧出缓冲（233-250）。
2. **投递**：
   - `recvFrameAt = time.Now()` 在 Read 成功后立刻记录——帧在内核可读的时刻（network/gnet.go:230）；
   - 帧缓冲从分级池 `getMsgBuf(totalLen)` 获取（232）；
   - 入站字节全部计入全局带宽统计（253），与 `Connection.Send` 的出站统计配对，monitor 拿到「网卡级」双向流量；
   - 连接已注销（close 后残余字节）→ Debug 级丢弃（255-264，历史 4 分钟任务曾刷 6500+ 条 warn 噪音）；
   - `EnqueueRaw` 三态返回见下。

`EnqueueRaw` 三态（network/connection.go:882-914）：

| 返回值 | 含义 | OnTraffic 的处理 |
|--------|------|------------------|
| `EnqueueOK` | 入队成功 | buffer 由 pump 在 decodeAndDispatch 处理后归还 |
| `EnqueueClosed` | 连接已关 / pump 未启动 | 静默丢包归还 buffer，不报警（任务停止 / battle_end close / EOF 后在途字节属预期） |
| `EnqueueChFull` | `inboundCh`（容量 256，network/connection.go:111）真满 | 真**反压**：pump decode 严重落后，Warn + `gnet.Close` 关闭连接释放压力，避免整体雪崩 |

三态拆分本身是修正：早期 bool 返回把「连接已关」和「通道真满」混为一谈都关连接 + 打 decode 满 warn，任务停止时一次实测 567 条误报、95% 集中在 stopping 后 5 秒内（network/connection.go:878-881 注释）。

### 1.4 与 connectionPump 的分工

decode + 分发全部异步化到每连接一个的 `connectionPump` goroutine（network/connection.go:631-710）：

- **动机**：codec 管线含解压/校验/hash/解密等线性 CPU 步，inline 进 gnet event loop 会引入 loop 卡顿风险，故 decode 挪到连接自己的 goroutine；inbound decode / listen 分发 / 心跳 timer / control 命令合并进同一 goroutine 后无需跨协程同步（network/connection.go:586-591）。
- **主循环结构**：
  1. 每轮顶部先非阻塞检查心跳是否到期（`heartbeatDueLocked` 消费掉一次触发），到期立即发送 + 重置——防持续可读的 inbound 在 select 伪随机选择中饿死 timer（network/connection.go:654-662）；
  2. 心跳 timer.C 动态拼进 select（未注册心跳时为 nil channel，select 自动忽略），只负责「唤醒」，实际处理在循环顶部（664-671、677-681）；
  3. 四路 select：`ctx.Done`（drain inbound 归还 buffer 后退出）/ `heartbeatC` / `controlCh`（注册/停心跳、stop）/ `inboundCh`（decodeAndDispatch）（673-708）；
  4. inbound 批处理上限 `pumpInboundBatchSize = 16`（network/connection.go:34）：单次内层循环最多连取 16 条（非阻塞），处理完强制回外层重新检查心跳 due + select，防海量 inbound 饿死 timer/control（690-707）。
- **退出收尾**（defer）：`stopHeartbeatTimerLocked` 停 timer + `close(pumpDone)` 通知等待方（651-652）；`drainInboundLocked` 排空 inboundCh 归还 buffer 池（715-727）。

---

## 2. Connection 结构与生命周期

### 2.1 命名连接池 Client

`Client` 管理一个机器人的所有网络连接：`tcpConn map[string]*Connection` + `udpConn map[string]*Connection`，RWMutex 保护（network/client.go:22-30）。

- `ConnectTCP/ConnectUDP` 创建**占位** Connection（仅分配结构体与 ctx，尚未拨号，network/client.go:44-73）；已存在同名连接则跳过返回 false。
- 实际拨号由 `Robot.ConnectTCP` 完成：占位 → `resolver.Resolve("tcp:<service>")` 取 codec（nil → fail loud 拨号中止，robot/robot.go:486-496）→ `Dialer.DialTCP` → 注册心跳 → 注册 onClosed/onDisconnect（robot/robot.go:475-536）。
- `CloseTCP/CloseUDP` 从池中摘除（锁内 delete）后调 `conn.Close()`（network/client.go:90-117）。

### 2.2 Connection 的关键成员

见 network/connection.go:70-108：

| 成员 | 作用 |
|------|------|
| `responseMap map[string]chan *Message` | routeKey → 在途请求的响应通道（SendRequest 注册、PendingRequest.Close 注销） |
| `listenRoutes map[string]*listenBinding` | routeKey → 监听绑定（回调 + 缓存队列，注册时定型，见 4.1） |
| `mu sync.Mutex` | 保护两张 map 的键 + binding.cb；各 listenQueue 自带 per-queue mu 串行化 Push/Pop |
| `ctx / cancel` | 连接生命周期上下文；cancel 是 pump 退出的唯一权威信号 |
| `isClose / intentionalClose atomic.Int32` | CAS 互斥保证关闭逻辑只执行一次；区分主动/意外关闭 |
| `sendFunc sendBackend` / `closeFunc` | 由 Dialer 在 OnOpen 内注入（network/gnet.go:302-312）；sendFunc 挂 gnet AsyncWrite，可选写完成回调 |
| `onDisconnect / onClosed` | 生命周期回调（见 2.3） |
| `secretKey atomic.Value` | 加密密钥，immutable 约定：Set 整体替换副本、Get 返回只读快照（network/connection.go:218-253），避免每收发包加锁复制一次密钥 |
| `adp / isUDP` | StartPump 时一次性注入后只读：decode 全程用该 adapter，不再查 resolver（network/connection.go:592-595） |
| `inboundCh / controlCh / pumpDone / pumpRun` | pump 的输入/控制/退出信号；`hbMu` 只保护 pump 外部的 hb 快照读（network/connection.go:92-105） |

### 2.3 双路径关闭与回调语义

关闭有两条互斥路径（`isClose.CompareAndSwap(0,1)`，network/connection.go:974-1009）：

- **`Close()`（主动）**：CAS → `intentionalClose=1` → 调 `closeFunc()`（gnet Close）→ `doClose()` → `publishClosed()`。**不**触发 onDisconnect——主动关闭不算意外断开。
- **`onClose()`（被动，由 gnet OnClose 事件触发）**：CAS → `doClose()` → `intentionalClose==0` 时 `publishDisconnected()` → `publishClosed()`。

`doClose` 是共享收尾（network/connection.go:957-972）：`cancel()` 取消 ctx（pump 看到 ctx.Done 立即 drain 归还、defer 停心跳 timer）+ `StopHeartbeat()`（投递 `pumpCmdStopHeartbeat` 让 pump 尽快丢掉心跳 runtime 的快速路径；即便投递丢失，pump 的 defer 兜底也会停 timer，无泄漏）。

两个回调的分工（network/connection.go:87-89）：

- **`onDisconnect`（意外断开）**：仅非主动 Close 导致的断开触发（服务端关闭连接、网络异常）。业务语义是「主连接挂了就停机器人」：`Robot.ConnectTCP` 注册的回调中 `serviceName == mainService` 时 Warn + `r.Stop()`，防止僵尸机器人占着账号不再产生有效压力（robot/robot.go:522-533）；同时 `monitor.ConnDropped()`。非主服务连接断开只 Debug。
- **`onClosed`（所有关闭）**：主动/被动均触发，调 `monitor.ConnClosed()`，与拨号成功时的 `ConnEstablished()` 配对保证「当前连接数 = 建立 - 关闭」准确（robot/robot.go:518-521、512）。

两个事件都支持「先发生、后注册」：`lifecycleEvent{occurred, delivered}` 保证全生命周期只交付一次，`SetOnDisconnect/SetOnClosed` 注册时若事件已发生且未交付则补调（network/connection.go:161-216）。

gnet `OnClose` 把错误归一化为短标签——EOF / RST（forcibly closed / connection reset）/ broken-pipe / local-close / timeout，兜底截断 64 字符防 detail 污染聚合维度——并分级记日志：正常 FIN 与本地关闭 Debug 级（短战斗场景每次 BattleEnd 都触发服务端 EOF，Warn 级曾每分钟刷 100+ 条噪音），真正异常断开 Warn 级（network/gnet.go:117-177）。

### 2.4 发送路径

`sendBackend func(data []byte, onWritten WriteDoneFunc) error`（network/connection.go:415-419）：

- `Send(data)`：即发即忘（心跳、帧同步、tcpSend），onWritten 传 nil——这条路径量级每秒十万级，不为每次发送多付一个闭包分配（network/connection.go:421-424）；
- `sendTimed(data, onWritten)`：请求-响应路径，登记写完成时刻供 WireRTT 起点（network/connection.go:426-429）。

发送失败返回 `ErrSendFailed`；出站字节计入全局带宽统计（network/connection.go:446-458）。

### 2.5 CloseAll：分阶段等待 pump 退出

`CloseAllWithTimeout`（network/client.go:127-202）：

1. 锁内快照并清空两张连接池 map（128-133）——后续新连接进新 map；
2. 逐条 `conn.Close()`（139-144，TCP 与 UDP 各一轮）；
3. 分两轮（decode 轮 + listen 轮）对每条连接等 `WaitPumpDoneTimeout`（165-188）。

decode/listen 已并入同一 pump，两轮等待落在同一个 `pumpDone` 信号上：第一轮成功后第二轮立即返回，超时则 `DecodeTimeouts`/`ListenTimeouts` 一起上量，保持 `CloseAllResult` 对上层（robot 清理超时日志）的既有口径（network/client.go:161-164 注释）。pump 是连接唯一后台 owner goroutine，已分发的 listen 回调都是同步调用，pump 退出即代表所有后台工作收尾完毕（network/connection.go:916-919）。`timeout<=0` 表示不设超时保持旧阻塞语义；超时时 Warn 并把 `Done=false` 带回（network/client.go:189-200）。

---

## 3. 请求-响应机制

### 3.1 PendingRequest：发送与等待拆分

协议没有序列号，routeKey 是响应的唯一匹配依据，**同一 routeKey 同时只能有一个在途请求**。`SendRequest`（network/connection.go:297-335）：

1. 连接 nil / `isClose==1` / 调用方 ctx 已取消 → 早退；已取消时不再注册不再发送（停止阶段再发一个没人等的请求只会给服务端和统计添噪），返回 `ErrActionCanceled`——码固定为取消码（与等满窗口的 `ErrRecvTimeout` 区分开），Unwrap 保留 ctx.Err() 供 `errors.Is(context.Canceled)` 判定（network/connection.go:255-271、297-306）；
2. `registerResponseChan` 在 `c.mu` 内登记 `chan *Message`（缓冲 1）；若覆盖了已在途请求，锁外 Warn——前一个等待者从此收不到任何响应、只能等满自己的超时窗口，这种流程编排错误不能静默（network/connection.go:311-318、337-351）。检测与写入共用同一次持锁、日志留在锁外：`c.mu` 是每个入站包都要经过的热锁，不能被同步日志写入堵住 pump；
3. `sendTimed` 发送并登记写完成时刻（WireRTT 起点，见第 5 节）；发送失败内部已 `pr.Close()`，不泄漏 responseMap 条目（network/connection.go:322-333）。

`PendingRequest` 句柄（network/connection.go:273-288）暴露：

- `C()`：响应通道，供调用方 select；
- `Timeout()`：有效超时（`timeout<=0` 回退连接默认 `requestTimeout`，network/connection.go:307-309）；
- `Timing(resp)`：按两端内核测点计算 `RequestTiming`（见第 5 节）。

**无论命中/超时/取消都必须调 `Close()`**：`sync.Once` 保证幂等，在 `c.mu` 内删除 map 条目（仅当仍是自己的通道时，防误删后来者的注册）后 `close(ch)`；与 pump 的 OnReceive 同锁，保证不会向已关闭通道发送（network/connection.go:398-413）。

### 3.2 awaitResponse：五路 select

等待方是 `robotScheduler.awaitResponse`（robot/scheduler.go:148-206）。按连接类型取连接（157-165，找不到返回 `ErrConnNotFound`）→ `SendRequest`（167-173，发送失败区分取消与真实错误）→ `defer pr.Close()`（174）→ 单一池化 timer 计满整个超时窗口（176-178）→ 进入 select 循环。

五个分支（robot/scheduler.go:183-204）：

| 分支 | 语义 |
|------|------|
| `case t := <-s.taskCh` | **协作 drain 任务队列**：就地执行 listen 回调任务后回到循环顶。这是调度核心约束——任何等待点都不饿死推送回调（robot/scheduler.go:21-39）。**为何必须有它**：listen 回调（Lua on_message / Go store 写入）投递在任务队列里，若等待响应期间不消费，密集推送会把队列打满触发丢弃 |
| `case resp := <-pr.C()` | 响应到达；`resp == nil` 表示通道被 Close，返回 Canceled。命中后构造 `engine.NetExchange`，Timing 用 `pr.Timing(resp)` 计算，字节统计双向（SendWireBytes=请求包长 / RecvWireBytes=入站帧长） |
| `case <-callerDone` | 调用方 ctx 取消（声明式动作传流程 ctx，Lua 传 Robot ctx） |
| `case <-robotDone` | 机器人生命周期 ctx 取消——两者任一取消都立刻退出在途请求，**不等满超时窗口** |
| `case <-timer.C` | 超时，返回 `ErrRecvTimeout`（detail 带 service/route/timeout） |

任务队列通道与响应通道在同一个 select 里公平竞争：响应即时命中，不被 drain 批次挡住（robot/scheduler.go:140-143 注释）。循环顶每次迭代都先查双 ctx 取消（179-182）。

取消的监控归类链路：网络层取消统一报 `ErrActionCanceled`（Unwrap 保留 `context.Canceled`），动作外壳 `normalizeActionCancel` 在停止期把底层副作用错误（LISTEN_TIMEOUT/CONN_NOT_FOUND 等）也归一为取消码——否则停止一批机器人时监控面板会堆出大量「假失败」，掩盖真实服务端业务错误（robot/robot.go:667-690）。

### 3.3 OnReceive 分发

pump 解码出 routeKey 后同步调 `OnReceive`（network/connection.go:1023-1092，由 decodeAndDispatch 在 871 行调用）：

1. `isClose==1` 早退（1024-1026）；
2. 一次持 `c.mu` 决出「等待中的请求 / 监听绑定 / 无人认领」：
   - 先查 responseMap：命中则构造 Message 并在**锁内**向通道发送（防 `PendingRequest.Close` 在 unlock 与 send 之间 `close(ch)` 导致 panic）；通道满（理论上不可能，缓冲 1 且 Close 前必然只有一个等待者）则 Warn 丢弃（1041-1061）；
   - 未命中请求则查 listenRoutes：命中把 `cb` 与 `queue` 一并取出，释放锁后 `dispatchListen`（1064-1082）；
3. `Message` 在确定有消费方之后才构造——无人认领的广播不再白白分配（1040 注释）；
4. 都未命中：Debug 日志（1085-1091）。

分派代价：一次持锁 + 至多两次 map 查找（responseMap 常为空表，Go 对空 map 查找有快路径，network/connection.go:1017-1018 注释）。

---

## 4. 持久化监听

### 4.1 listenRoutes 合表设计

旧实现把回调与队列分放 `listenResp` / `listenQueues` 两张 map，每条推送要 3~4 次字符串 map 查找 + 2~3 次 `c.mu`：OnReceive 查一次 listenResp 判断「是不是监听」却丢掉查到的回调，dispatchListen 再加锁重查拿回调，缓存模式还要第三次加锁查队列——在广播密集的压测里是纯开销（network/connection.go:127-135 注释）。

现合表为 `listenRoutes map[string]*listenBinding`，`listenBinding{cb ListenCallBack, queue *listenQueue}` 注册时一次性定型（network/connection.go:125-140）：

- `cb != nil` = 回调模式；`cb == nil` = 缓存模式（消息进 queue，由 `GetListenResp`/主流程消费）——**二者互斥**；
- pump 在 OnReceive 的同一次持锁期内一次查找取齐回调与队列，分发路径不再回查 map、不再二次加锁（network/connection.go:524-546、1038-1082）；
- 合表后「有回调却没队列」这种旧双表可能的偏斜状态在结构上不再存在。

并发约定（network/connection.go:133-136）：`cb` 由 `c.mu` 保护（RegisterListen 幂等重注册回写最新值，pump 在持锁期内拷出局部变量）；`queue` 在 binding 发布进 map 之前创建、之后只读；Push/Pop 由 **per-queue mu** 串行化，故 pump 在释放 `c.mu` 之后再 Push，与主流程 `GetListenResp` 的 Pop 无锁序交叉、无死锁。

### 4.2 注册语义与 queueSize

`RegisterListen` 是唯一注册入口（network/connection.go:481-514）：

- 新注册：写 map + 预创建容量 queueSize 的队列（一次性纯 map 操作，不起任何 goroutine）；
- 幂等：同 routeKey 再注册且（queueSize 一致 && cb 是否为 nil 一致）→ 不重建队列、回写 `binding.cb` 为最新值，返回 nil；
- 冲突 fail-loud：queueSize 或 cb 模式不一致 → 返回中文 error。

robot 层注册流程 `robotActionHandler.RegisterListen`（robot/robot.go:887-971）：逐条 ListenRef 计算 queueSize → 解析 `server`（`tcp:<service>` 格式非法报错）→ Resolve 出连接 adapter 后 `ExpectedRouteKey` 计算 routeKey（Resolve nil → fail loud）→ 引用的 listen 定义不存在 / store 与 script 同时配置同样 fail-loud → 按 (proto, service) 分组逐条下发给 Connection。连接尚未建立时跳过注册但 Warn——这是合法编排顺序问题（流程可能稍后才 connect），但该监听事实上丢了必须可见（robot/robot.go:952-960）。

queueSize 语义（`effectiveListenQueueSize`，robot/robot.go:973-992）：

- `QueueSize == nil`（未写）→ 默认 1（与历史单槽语义逐字节等价）；
- 显式 `> 0` → 取该值；
- 显式 `<= 0` → 配置错误返回中文 error（遵循「禁止兼容性兜底」，不静默 clamp）。

`newListenQueue` 对 `capacity < 1` panic（network/listen_queue.go:38-40）。Lua `ensure_tcp_listener/ensure_udp_listener` 走占位注册（cb=nil，queueSize 由调用方传入，robot/robot.go:1619-1642）。

### 4.3 队列与边沿通知

`listenQueue` 是固定容量环形队列（network/listen_queue.go:23-31），设计上替代旧的单槽 `map[string]*Message`（新覆盖旧）：

- 写入位置 `(head+size)%capacity` 派生，不单独存 tail；Push/Pop 各 O(1)（71-106）；
- 队满覆盖最旧（保最新）、`dropped` 累计丢弃数且不被 Clear 重置（69-88、109-113）；Push 返回 dropped=true 时 dispatchListen Warn 并带累计值（network/connection.go:538-545）；
- Pop 置 nil 槽助 GC（99）；
- `Notify()` 返回容量 1 的边沿唤醒通道（51-53）：通知只表示「队列可能非空」，**不携带消息**，允许合并多次 Push；消费者被唤醒后必须重新 Pop，消息事实源始终是环形队列。Push 成功 `signalLocked`，Pop 到空 `clearSignalLocked`（55-67）；
- `ListenNotify` 对未注册、回调模式、nil 或已关闭连接返回 nil channel——select nil channel 自然禁用该分支并等待 ctx/deadline（network/connection.go:566-583）。

### 4.4 回调三种形态：单写方化

`createListenCallback` 按定义分派（robot/robot.go:1039-1111）：

1. **Lua 脚本回调**（`cbDef.Script`）：pump 内只 `msg.Copy()` 深拷贝快照（pump 可能复用 msg.Data 底层数组，入队前必须快照）+ `sched.enqueue` 投递任务队列；`runListenScript` 由执行器 goroutine 在等待窗口的 select 内就地串行执行，可安全访问业务 LState——绝不在 pump goroutine 内碰它（消除旧版跨 goroutine LState 抢占，robot/robot.go:1040-1052、1191-1197）。脚本内 s2cProto 首选 wire 直转（`RunListenScriptWire`），ValidateWire 失败或 schema 降级回落解码路径（robot/robot.go:1204-1224）。
2. **Go 声明式 store**（`S2CProto` + `Store`）：**单写方化不变量**——取值在 pump 内完成（`buildListenStorePuts`，wire-first：大消息 ≥ DedupMinBytes 走 `WireShared` 内容寻址去重、小消息独立快照、路径映射惰性导航；产物为新建容器/不可变引用，跨 goroutine 移交所有权安全），`SetPath` 写入投递任务队列由**执行器 goroutine** 执行（robot/robot.go:1081-1109、1119-1167）。pump 不再持有对 msg 的引用；时长含队列等待，反映「推送到达 → 状态可见」的真实延迟。store 与 script 互斥由 `validateListenDef` 注册前 fail-loud（robot/robot.go:1003-1012）。
3. **纯缓存**（如 frameData）：返回 nil cb，消息仅入队列供主流程消费（robot/robot.go:1054-1056）。

监听等待路径 `waitListen`（robot/robot.go:1579-1596）：wake 通道（ListenNotify）+ check 函数（GetTCP/UDPListenResp → Pop 队列）进 `sched.wait`；wake 只是提示，队列才是事实源。声明式 `tcpListen/udpListen` 动作与 Lua await_listen 都收敛到这一路径。

### 4.5 监听等待指标

监听等待单列，从 `NetExchange.RecvFrameAt`（帧在内核可读的时刻）算起而非轮询唤醒时刻，避开协作式调度的量化误差。三态分类 `ClassifyListenWait(waitStart, recvFrameAt)`（engine/action.go:165-177）：

- 帧时刻早于（或等于）等待起点 → `ListenWaitReady`：消息已在队列，等待时长不可测，**只计次不产 0ms 样本**——计 0 会把「消息早就在队列里」和「服务端瞬间响应」混为一谈，把 P50 拉向 0（engine/action.go:111-113）；
- 晚于 → `ListenWaitMeasured`：产出实测样本进 ListenWait 分布；
- 无帧时刻（网络层未透传/测试桩）→ `ListenWaitUnknown`：不统计。

超时记 `ListenTimeoutCount` 单独成率（`ListenTimeoutRate` = 超时 /（命中 + 已就绪 + 超时）），不进分布——没有时延值，硬塞会把 P99 顶到超时上限掩盖真实分布（engine/action.go:114-116；monitor/snapshot.go:96-98、911-913）。采集侧字段见 monitor/collector.go:64-68。

监听等待与请求 RTT 是不同物理量故分开统计：请求-响应测服务端处理单请求的延迟（毫秒级、有公认阈值、可打 Apdex）；监听等待测服务端业务时长（匹配、等队友，秒级、无普遍阈值，打 Apdex 是伪精度）（engine/action.go:104-110）。

---

## 5. RTT 计时

WireRTT 的两个测点都取在贴近内核的位置，避免把施压机自身的排队算进服务端延迟（AGENTS.md「单次动作数据流」约定）：

- **起点 = 写完成时刻**（network/writetime.go:1-53）：`AsyncWrite` 只把数据挂进事件循环的待写队列就返回，真正的 write(2) 在事件循环后续轮次里执行；旧实现把 AsyncWrite 返回时刻当 RTT 起点，「数据在待写队列里排了多久」被整段算进服务端延迟——施压机 CPU 越忙这段越长，RTT 被系统性高估，Apdex 随之失真。gnet 写完成回调给出数据真正交给内核的时刻：
  - `bindConn` 在 OnOpen 内把 `writeDoneCallback` 挂进 sendFunc（network/gnet.go:297-312），回调在事件循环 goroutine 上只做一次原子写，不引入额外等待；
  - 写失败不登记时间戳（失败发送凭空造出 RTT 起点会污染 WireRTT 与 Apdex），只记日志；起点回退到入队时刻（network/gnet.go:314-333）；
  - `writeStamp`（network/writetime.go:35-53）：`mark` 原子记录写完成时刻；`start()` 取 `writtenNs > 0` 用真实写完成时刻（hits 计数），否则回退 `enqueuedAt`（fallbacks 计数）——该回退只会让 RTT 偏大不会偏小，**宁可高估也不造出比真实更好看的数**；占比过高说明写回调链路有问题应当查；
  - 观测端点 `/debug/rtt`（network/writetime.go:74-84，输出 hits/fallbacks/hitRate）。
- **终点 = RecvFrameAt**：OnTraffic 在 Read 整帧成功后立刻记录（network/gnet.go:230），经 `inboundFrame.RecvFrameAt` 透传给 pump（network/connection.go:114-118），存入 `Message.Timing.RecvFrameAt`（network/message.go:6-11）。终点是帧在 gnet 入站缓冲可读的时刻，而非 Robot 被唤醒读到它的时刻。

`PendingRequest.Timing` 据此产出 `RequestTiming`（network/connection.go:369-396）：

| 字段 | 计算 | 含义 |
|------|------|------|
| `SendCost` | 发送调用耗时 | 含 encode 后的 AsyncWrite 提交 |
| `WireRTT` | `RecvFrameAt - stamp.start()` | 链路 + 服务端，两端贴内核 |
| `DecodeWait` | `DecodeStart - RecvFrameAt` | 帧到达 → 开始解码（pump 排队） |
| `DecodeCost` | `DecodeEnd - DecodeStart` | codec 管线耗时 |
| `DispatchToActionWait` | 调用方被唤醒 - `DispatchStart` | 分发 → 业务可见 |

Decode/Dispatch 细分受 `monitor.timingDetail` 配置控制（`TimingCodecDetail` / `TimingFullDetail`，network/connection.go:388-394）。

---

## 6. 连接级心跳

### 6.1 配置驱动与延迟启动

心跳由 codec 配置（`<proto>_<service>_codec.json` 的 heartbeat 段）驱动：连接建立后 `RegisterCodecHeartbeat` 经 `protocol.Heartbeat(resolver, "<transport>:<service>")` 取配置、字段条件编译为 engine 绑定（robot/robot.go:1673-1701）。`requireSecretKey: true` 时不立即安装——`deferHeartbeatUntilSecretKey` 挂进 `pendingHeartbeats` map，`SetTCPSecretKey/SetUDPSecretKey` 写入密钥后经 `startPendingHeartbeatAfterSecretKey` 取出补装（robot/robot.go:1707-1737）。

### 6.2 pump 内的单 goroutine 模型

`RegisterHeartbeat` 投递 `pumpCmdHeartbeat` 到 controlCh（容量 16，network/connection.go:39），由 pump 在自己的 select 分支串行安装 cfg + 重置 timer（network/connection.go:46-58、731-754；network/heartbeat.go:40-79）。心跳发送、inbound decode、listen 分发共享同一 pump goroutine，无跨协程同步。

- pump 未启动时注册被拒绝并记 Error 日志（调用序被破坏，属需修代码的缺陷而非可容忍的运行态，network/heartbeat.go:57-63）；
- 已有心跳则替换（Stop 旧 timer → 装新 cfg → Reset，network/connection.go:733-740）；
- `StopHeartbeat` 经 result channel 同步等 pump 处理完，保留「返回即心跳已停」语义，方便 doClose/Robot.Close 后续逻辑；投递失败兜底等 `pumpDone`，上限 `stopHeartbeatFallbackTimeout = 2s`（network/heartbeat.go:89-126）；
- 心跳到期发送：`sendHeartbeatLocked` 调 builder 构包 → `Send`（即发即忘，不登记写完成时刻）；连接已关或 builder 返回 nil 跳过；失败 Warn 不重试（network/connection.go:771-800）。

### 6.3 双模式 body 与注册时编译

body 由 Go builder 闭包构造（**不触碰业务 LState**），三形态（robot/robot.go:1834-1876）：

- proto 模式（`C2SProto != ""`）：`hbExec.BuildProtoBody`（复用同一 ActionExecutor，由本连接 pump 单 goroutine 串行调用无并发，robot/robot.go:1830-1832）；
- raw-binary 模式（`len(Fields) > 0`）：定长小端流；
- 空 body（两者皆无）：静态心跳。

raw-binary 心跳每 tick 打的是同一份定长小端字节流：字段个数、每字段宽度与偏移、哪些槽恒定，注册那刻已全部确定。而 `BuildHeartbeatBody` 的逐 tick 形态把这些常量工作重做一遍——每字段查一次 `heartbeatTypeWidth` map、body 从 nil 起 append 增长（每 tick 一次分配 + 扩容拷贝）、fixed 源恒定也重算。心跳是本进程最高频的 Go 侧循环（每连接 40~150ms 一发，8000 人规模每秒数十万 tick），故注册时一次性编译（engine/heartbeat_plan.go:11-37；robot/robot.go:1755-1759）：

- **`engine.CompileHeartbeatPlan`**（engine/heartbeat_plan.go:62-99）：先算总长再分配（避免 append 增长）+ 逐字段校验宽度；fixed 槽编译期直接预填进复用缓冲（86-93），动态槽记入槽位表（`hbSlot{off, width, idx, f}`，40-45）。每 tick 的 `Build`（119-135）只做动态槽求值 + `putLE` 定长覆写——零 map 查找、零分配、fixed 槽零重算。编译期校验 `validateHeartbeatField`（140-186）与运行期拒绝条件一一对应，保证「编译通过 ⇒ 运行期不会因配置非法失败」，运行期只可能因 state 缺失 skip。
- **`engine.CompileHeartbeatCounters`**（engine/heartbeat_plan.go:212-226）：摘出 counter 源字段的推进表（字段下标 + Step），替代每 tick 全字段扫描；body 构建成功后 `AdvanceHeartbeatCounters` 推进（230-234），read-then-increment 语义不变（首包用 Start，其后每次 +Step）。
- **resolver key 驻留**：`"<transport>:<service>"` 字符串与首次 Resolve 成功的 adapter 在注册时定型缓存（robot/robot.go:1824-1830、1883-1893），不再每 tick 拼串 + map 查表；nil 不缓存，保留「每 tick 重试 + Warn」语义（codec 映射可能晚于心跳安装就绪）。

**编译失败不静默吞**：回落 `engine.BuildHeartbeatBody`（逐 tick append 打包，engine/heartbeat.go:124-141）作为 oracle 保留，维持原「每 tick Warn」可见性——坏配置不能因为多了一层编译就悄悄消失（engine/heartbeat_plan.go:32-34；robot/robot.go:1807-1811）。对拍测试 `engine/heartbeat_plan_test.go` 覆盖多 tick 逐字节一致、同 key 读/自增交错序、skip/错误语义、零分配。

**缓冲复用的安全前提**（robot/robot.go:1853-1856；engine/heartbeat_plan.go:113-117）：plan 的 `Build` 返回内部复用缓冲，调用方必须在本 tick 内消费完——`codec` encode 把 body `copy` 进新分配的整包（`out := make(...)` + `copy(out[c.headerSize:], work)`，protocol/codec/engine.go:227、270），且各 cipher 的 `Encrypt` 一律 make+copy 不原地改写入参（protocol/codec/ciphers.go:193-429 各实现）。这条一旦被破坏，心跳会发出上一 tick 的密文残留。skip 的 tick 缓冲可能只被部分覆写，但 skip 不发包、下一 tick 每个动态槽位都会被完整重写，残留不外泄。

**并发约定**：plan 持有复用缓冲**非并发安全**，每连接编译独立实例、仅由该连接 pump goroutine 串行调用，与 `privateCounters` 同一约定（engine/heartbeat_plan.go:35-36；robot/robot.go:1809-1811）。

---

## 7. 性能优化清单

以下各项均核对源码（多数为 2026-07-30 前后压测调优产物，背景与红线见 AGENTS.md「内存优化约束与已证实结论」）：

| # | 优化 | 位置 | 要点 |
|---|------|------|------|
| 1 | **收包分级缓冲池** | network/bufpool.go:8-54 | 5 档容量桶（256B/1K/4K/16K/64K），按「≤大小→落桶」匹配避免大包驱逐小包劣化命中率；池存 `*[]byte` 防 slice header 逃逸；超大包直接 make 不入池；归还后不得再访问底层数组 |
| 2 | **全局 timer 池** | internal/timerpool/timerpool.go:1-39 | 高频等待点（`robotScheduler.wait` 帧循环 poll、`awaitResponse` 每请求超时窗）统一 `GetTimer/PutTimer`，消除每等待点一只 `time.NewTimer`（8000 机器人规模剖面周期 20GB+ 级分配）。正确性依赖 **Go 1.23+** 无缓冲 timer channel 语义（Stop/Reset 后不会收到旧触发，归还前 Stop 即可无需排空）；**归还后不得再引用**（含其 C）；同一 timer 单协程串行使用 |
| 3 | **gzip 定长一次分配** | protocol/codec/compressors.go:102-150 | `gzipSizeHint` 从 trailer ISIZE（末 4 字节小端，解压后长度 mod 2^32）读尺寸提示；上限 16MB 防损坏 trailer 巨额分配（104）；`readAllSized` 按 hint 一次分配 + 单字节探针判定 EOF，替代 `io.ReadAll` 512B 起步倍增扩容（大帧一次解压多轮 growslice 曾是剖面 36GB 分配主源）；提示偏短追加、偏长截断，两端兜底语义不变 |
| 4 | **流密码原地解密** | protocol/codec/engine.go:664-683；protocol/codec/registry.go:35-44 | decode 的 work 是函数私有副本，`CipherInPlace.DecryptInPlace` 原地解密免整体复制；in-place 实现约束「报错前不动 data」故 keep 语义与复制版一致；无 InPlace 实现时回退复制版 Decrypt（顶替后旧租借缓冲死亡归还） |
| 5 | **decode 头字段栈上数组** | protocol/codec/engine.go:583-592 | 路由/校验字段值记入栈上定长数组（`maxHeaderScratch=16`，engine.go:731-732），替代旧每消息 routeMap/checksumOut 两个临时 map（每帧两次 map 分配 + 哈希）；字段更多的罕见 schema 回退堆分配 |
| 6 | **解压去重缓存** | protocol/codec/inflate_cache.go:1-152 | 大广播帧（≥1024B）按压缩字节内容寻址共享解压产物，8000 接收方塌缩为一次 gunzip；**二见登记防污染**：首见只记 8 字节哈希标记（不存字节），二见才登记真条目——独占请求-响应永远停在标记层零字节污染（018→019 教训机制化）；双上界 LRU（1024 条 / 48MB）；共享产物只读流转、只有 compress 是 decode 反序管线最后执行步才可共享（`inflateShareSafe`，protocol/codec/engine.go:750-766）；观测 `/debug/inflate`（inflate_cache.go:134-151） |
| 7 | **routeKey 驻留** | protocol/codec/intern.go:20-59 | 解码/编码路由键规范实例共享：相异 routeKey 全场仅几十种而每帧产一个小分配（剖面 2 亿量级）；COW 表读侧原子 Load + map 读无锁，写侧整表复制 + CAS（预热后不再增长）；容量上限 4096 防损坏帧产出无界垃圾键，超限退化普通分配正确性无损 |
| 8 | **压缩帧 work 缓冲池化** | protocol/codec/engine.go:768-795 | 压缩帧 body 瞬态副本从 `sync.Pool` 租借（`willInflate` 在复制前判定，736-748）；非压缩帧的 work 会作为最终 body 外泄故必须独立分配；**归还纪律**：只有 work 被解压产物/共享产物/复制版解密产物顶替后才归还，任何可能外泄的路径（解压失败 keep、提前 return）一律不归还交 GC（770-774） |
| 9 | **导航路径驻留表** | protocol/protox/wirenav.go:3-67 | wire 读路径的影子采样判定与 fd 解析合并为有界 COW 查表：`(schema, 路径)` 驻留为条目（maphash → `*navPathInfo`），稳态读 = 1 次哈希 + 1 次 COW map 查找 + 原子计数，零分配零字符串拼接；条目携带预解析 fd 链免逐层 `Fields().ByName`；容量 8192（防脚本动态 map-key 路径撑爆）；按描述符**身份**校验，proto 热重载后新描述符替换旧条目重新首 K 全查 |
| 10 | **心跳注册时编译** | engine/heartbeat_plan.go:62-234 | 见第 6.3 节：布局编译 + 计数器推进表 + resolver key 驻留 |
| 11 | **收包路由分派合表** | network/connection.go:125-140、1023-1092 | 见第 4.1 节：一次持锁取齐回调与队列，消除 3~4 次 map 查找 + 2~3 次锁往返 |
| 12 | **列表下标早退** | protocol/protox（`wireCollectList`，AGENTS.md 2026-07-30 P6 记录） | `xs[i]` 下标访问只扫到第 i+1 个元素即停，不再全量收集解码；**单数字段/map 不可早退**——标量 last-wins、单数 message 多段拼接、map 重复 key 替换是 protobuf 合并语义硬约束 |

观测端点（挂 pprof 端口，启用 pprof 调试服务的进程自动获得）：`/debug/rtt`（写起点命中率）、`/debug/inflate`（解压去重 + routeKey 驻留数 + work 复用计数）、`/debug/sched`（调度延迟分位数）、`/debug/statekeys`（robot.get/get_view 按 key 计数）、`/debug/dedup`、`/debug/wire`（影子验证）。

---

## 8. 机器人管理

### 8.1 Robot 生命周期

`Robot` 持有独立的 state、网络 Client、独占 LState、流程执行器与协作调度器（robot/robot.go:38-80）。取消链 **task → manager → robot**：Robot ctx 由 Manager ctx 派生（Manager ctx 又派生自任务级 ctx，robot/manager.go:110-113），任务/Manager 取消立即沿链传播到每个 Robot，含正在拨号/执行 Lua 的机器人（robot/robot.go:105-107）。

```
NewRobot ──Start──> 执行 goroutine（executor.Run）──┐
   │                                              │ 流程跑完 / ctx 取消
   │  Stop() = cancel()          Wait() = <-done  │
   └──────────────────────────────────────────────┴─> cleanup（Close）
                                                        ├─ 并行等待 executor 退出 + CloseAllWithTimeout(10s)
                                                        ├─ 关闭 HTTP 空闲连接
                                                        ├─ 归还 LState 到池（超时路径隔离不归还）
                                                        └─ state.Clear()
```

- **NewRobot**（robot/robot.go:108-189）：resolver nil 或 Lua 池未提供 LState 时返回错误；**codec 配置错误不在创建期暴露**——拨号/首次 encode 时按 fail-loud 上报，便于定位到具体连接（service 串）而非整 Robot 创建失败。stateExtra 先写、框架保留键（`id`/`index`/`account`）后写，命中保留键 Warn 忽略——被覆盖后整批机器人共用同一身份，日志与服务端会话全部对不上（robot/robot.go:166-177、301-306）。
- **Start**（robot/robot.go:207-279）：经 `workpool.Default().Submit` 提交执行 goroutine（CAS 防重复启动）；**提交被拒时执行收尾**（cleanup + 关闭 execDone/done + 复位 running）并返回错误，调用方不得把该 Robot 计为已启动。goroutine 内：LState 绑定生命周期 ctx（正在执行的死循环脚本会在下一个指令检查点被取消打断，cleanup 不再因不可中断 Lua 永久卡死，robot/robot.go:220-225）→ 注入 script.Context → `executor.Run(r.ctx)` → 按退出形态记 monitor（异常且非取消 → RobotErrored）→ cleanup → `onDone` 回调 Manager。
- **Stop**：只调 `cancel()`（robot/robot.go:282-284）；**Wait**：等 `done` 通道（287-289）。
- **Close/cleanup**（robot/robot.go:356-472）：`sync.Once` 幂等。提交后台 goroutine 跑 `CloseAllWithTimeout(robotCloseTimeout)`，与 executor 退出**并行**等待（select 两路 + 总超时 `robotCloseTimeout = 10s`，robot/robot.go:291-299）；**协程池不可用（进程 shutdown 阶段）时改同步执行连接清理**，不再依赖同一个已停的池空等超时（robot/robot.go:377-384）。全部完成后：关闭 HTTP 空闲连接、`luaPool.Release(r.l)` 归还 LState、`state.Clear()`；超时/清理失败路径**隔离 LState 不归还**（避免复用可能仍在使用的运行时，进程退出由 OS 回收，轻微泄漏可接受）并返回带 `CleanupIssue` 的状态。清理结果四态 OK/Partial/Timeout/Unknown、原因枚举见 robot/cleanup.go:8-40。

### 8.2 robotScheduler 协作调度

`robotScheduler` 是单个 Robot 的协作式调度核心（actor 运行时，对标 skynet service 语义，robot/scheduler.go:21-44）：

- **mailbox（taskCh，容量 `robotTaskQueueSize = 256`**，robot/robot.go:312-317）：网络 pump goroutine 投递异步 Lua 工作（listen 回调），投递方只复制消息 + enqueue，**绝不在 pump goroutine 内碰业务 LState**；
- `enqueue` 队满丢最新并 Warn 计数（`taskDropped`），绝不阻塞投递方——listen 回调属推送热路径，丢弃语义与监听缓存队列「保最新」一致：宁可丢回调也不阻塞 I/O 平面（robot/scheduler.go:54-71）；
- **owner（执行器 goroutine）是唯一消费者**：在等待窗口的 select 内就地处理任务；owner 不在 select 上时（正跑业务 Lua/推进流程），任务在 taskCh 排队等下个等待窗口；
- 终端条件纯语义化（就绪/响应/done/超时/取消）：taskCh 的 channel 收发本身就是 owner 与 pump 之间的唤醒信号；drain 只发生在空闲窗口（节点延迟 / wait 节点 / Lua await），flat-out 流程不 drain。

三个统一等待原语：

- `wait`（robot/scheduler.go:80-138）：deadline + check（listen 轮询）+ wake（容量 1 边沿提示）+ 双 ctx；每处理完一个任务回 loop 顶重查 check/deadline——天然实现 per-callback 的就绪检查；timer 从池取、exec 前 Stop（回调里嵌套 wait 会再从池取，互不干扰）。
- `awaitResponse`（148-206）：见第 3.2 节。
- `runIO`（243-274）：Class B 阻塞 I/O（share.* Redis / http / 拨号）投协程池后台执行，调用 goroutine 等待期间 select taskCh 就地处理回调；job 内**绝不可访问业务 LState/state**（只能用线程安全句柄）；job panic 由协程池 recover + 本地 recover 兜底并始终 signal done，避免调用方永久阻塞；**Lua await_\*（awaitIO）与声明式 http/connect 动作（coopIO）共用这一原语**，两条路径协作式语义统一（注入点 robot/robot.go:180-182；接口 engine/action.go:212-217）。

节点延迟、wait 节点和 onError 重试间隔统一走 `cooperativeSleep` → `sched.wait`，保证等待期间不饿死 mailbox（robot/robot.go:867-884）。

### 8.3 Manager：批量启动

`Manager` 持 robots 切片 + `robotIdx` 下标索引（`onRobotDone` 据此 O(1) swap-delete，避免大规模机器人同时退出时 O(n²) 线性扫描清理，robot/manager.go:72-75、490-506），生命周期计数 `active` / `generation` / `creationDone` + `doneCh`（robot/manager.go:80-90）：

- **active**：当前代存活数（startBatch 创建时 +1，onRobotDone -1）；
- **generation**：代号，resetBots 递增；onRobotDone 按创建时捕获的代号归属，旧代回调不得触碰当前代 active/doneCh，隔离阶段重置的异步回调污染；
- **creationDone**：创建阶段是否结束；**doneCh 关闭的唯一条件 `creationDone && active==0`**——同时消灭「错过完成事件（永不结束）」「阶段间瞬时归零（提前结束）」「旧代回调污染计数」三类问题（robot/manager.go:83-90 注释）。

- **StartAll**（robot/manager.go:225-241）：一次建全部，内部 `startBatch(0, count, concurrentNum)`；`defer finishCreation()` 保证所有返回路径都标记创建阶段结束。
- **startBatch 限速**（robot/manager.go:141-200）：每启动 `conc` 个暂停 1 秒（select ctx.Done / timer）；**仅批次中间触发，最后一个机器人启动后不再等**（`(i+1) < count` 条件，robot/manager.go:182）——避免阶段切换时多等 1s。NewRobot 单个失败（resolver nil / LState 不可用，重试无用）跳过告警继续；**整批全失败返回错误防「静默空跑」**——上层看到 err==nil 会继续推进阶段而实际压力为零（196-198）。`addAndStartRobot` 先登记（robots + active +1）再 Start，启动失败原子撤销登记；「先登记 active 再 Start」杜绝 robot 秒退时 -1 早于 +1（203-222）。
- **StartWithRampUp**（robot/manager.go:244-350）逐阶段执行，每阶段独立语义：
  - `count`：本阶段**新增** bot 数（offset 累加，robot/manager.go:309、318）；
  - `concurrency`：0 用全局值（285-288）；
  - `holdSec`：**0 表示未填、回退默认 30s（`defaultStageHoldSec`，robot/manager.go:29）；显式配置的值一律照办、不做下限钳制**——旧实现 `max(HoldSec, 30)` 把显式写的 5s 悄悄抬成 30s，「配置写了不生效比报错更难查」（289-294）。取 30s 是让上一阶段机器人跑满至少一个监控窗口再叠加下一批，否则指标区间混着两个负载水位（robot/manager.go:26-28）；
  - `reset: true` 且当前有机器人 → `resetBots()`：先递增 generation（隔离旧代回调）→ 锁内快照并清空 robots + robotIdx → `closeRobotsConcurrent` 并发关闭 → `active.Store(0)` 重置新代基线（旧代回调因 gen 不符不会再减 active，直接 Store 安全）（robot/manager.go:447-467）；随后 sleep 1s 给「机器人末次 IO + 计数器最终值」留 flush 窗口（可被 ctx 取消打断）并触发 `OnStageReset` 上报刚结束段落的累计指标（270-283）；
  - 阶段间保持 holdSec（**最后一个阶段不等**，robot/manager.go:330-340），等待期间 select ctx 取消立即返回；
  - `OnStageChange` 每阶段开始时回调（含第一阶段），供监控更新加压进度（305-307）。

### 8.4 StopAll / 并发关闭 / workpool 拒绝处理

- **StopAll**（robot/manager.go:419-441）：`cancel()` → **锁内快照 robots + 读 prior**（此前已自然完成机器人的累计清理结果，由各自 Start goroutine 经 onRobotDone → recordCleanup 累积进 `m.cleanupSummary`）——快照与读 prior 在同一把 `m.mu` 内完成，与 onRobotDone 的「摘除 + 记账同锁」配对，两部分对同一机器人不重复计数也不漏计（每个机器人要么在 prior、要么在 closeResult）→ `closeRobotsConcurrent` 并发关闭 → 关闭 doneCh → `MergeCleanupStatus` 合并返回。
- **closeRobotsConcurrent**（robot/manager.go:362-407）：每个 Robot 的 `cleanup(reason, false)` 提交协程池并发执行；**提交被拒（workpool 满 / 进程停机）改同步清理**该机器人，确保资源实际释放，不再等待超时后伪造未执行的清理结果（374-378）；整体超时 `closeRobotsTimeout = 15s`（robot/manager.go:352-357）——单个 robot 内部已有 10s 兜底且全部并发，15s 覆盖调度延迟；**必须显著小于 Admin 的 stopWaitTimeout（60s）**，避免 Agent 还在 Close 时 Admin 已合成 fake report 把任务标 stopped 导致真实上报被丢弃；超时后未返回者按 Timeout 记账（`LuaSkipped` 计数）强制推进。
- **onRobotDone**（robot/manager.go:471-488）：摘除 + 记账同一临界区；旧代回调（gen 不符）到此为止；当前代号 `active.Add(-1) == 0 && creationDone` 时关闭 doneCh。`finishCreation`（513-518）与 onRobotDone 的关闭判定交叉覆盖（一方先置 creationDone 再读 active，另一方先减 active 再读 creationDone），`stopOnce` 去重防双关。
- **定时停止** `startDurationTimer`（robot/manager.go:550-575）：`Duration > 0` 时提交协程池跑定时器，到期自动 StopAll；提交失败返回错误向上传播。

---

## 9. 状态层

### 9.1 state.Store 并发模型

`Store` 是 RWMutex 保护的 `map[string]any`（state/store.go:58-61）。并发契约（P1b 单写方化后，state/store.go:46-57）：

- **嵌套容器（map/list）的写只发生在执行器 goroutine**——脚本 robot.set、声明式 store、Go-store 监听（pump 物化后经任务队列投递，见 4.4）；
- **pump goroutine 只做顶层标量访问**：心跳 bindings 读（Get/GetInt64/GetString）与 stateCounter 自增（IncrementInt64），均在锁内完成、不触碰嵌套容器内部；
- `GetPath` 返回的容器是内部结构的**别名**（免深拷贝），仅供执行器 goroutine 消费——执行器是嵌套容器唯一写方，自己读自己写无并发；导航全程持读锁，与 pump 的顶层标量写互斥（state/store.go:549-578）；
- RWMutex 保留的意义：保护 pump 侧标量访问与执行器写的互斥（心跳有硬时序要求，不能改投执行器队列）；单写方化消除的是「容器读需深拷贝切别名」这一层 churn，而非锁本身——无争用 RWMutex 开销可忽略（state/store.go:55-57）。

API 全景：

| 方法 | 说明 |
|------|------|
| `Get / Set` | 基础读写（state/store.go:69-81） |
| `GetInt / GetInt32 / GetInt64 / GetString` | 类型化读取，缺失回零值/空串（84-116） |
| `GetList / GetMap` | 顶层浅拷贝副本；服务执行器与 pump 双侧读者（心跳 BuildProtoBody bindings），执行器整体替换顶层切片时 pump 持有的旧副本仍自洽；惰性值（WireValue/Overlay）物化后直通（124-161） |
| `ListLen` | 免拷贝长度判断，供 listSize（165-172） |
| `PickFromList / PickMapKey / PickMapValue` | 读锁内零拷贝随机选取（pick 回调不得再访问本 Store 防重入死锁），供 `stateRandom`/`stateMapKey`/`stateMapValue` 等 binding（180-250） |
| `SetPath / GetPath` | 嵌套路径写入/读取，支持 `a.b[0].c`（259-329、545-579） |
| `Increment / IncrementInt64` | 锁内自增（397-413） |
| `Delete / Clear / Has / Keys` | 管理（383-432） |

### 9.2 路径语义与 Overlay

- `SplitPath("a.b[0].c")` → `["a", "b", "[0]", "c"]`（state/store.go:626-656）；导出版每次新分配，内部热路径走 `splitPathCached`（`sync.Map` 缓存分段结果，只读消费跨协程共享安全，state/store.go:608-623）。
- 包级 `NavigatePath(v, path)` 可对任意值导航（state/store.go:331-352）。
- `SetPath`：中间 map 不存在自动创建（类 mkdir -p）；已存在但非容器时覆盖为新 map；**遇到 PathNavigator（整存的 protox.WireValue / Frozen）例外——包进 Overlay 继续下探（COW），未触碰的兄弟子树保留基座惰性形态不会被整键清掉**（state/store.go:252-329）；`[N]` 数组索引段要求对应位置已是 `[]any` 且长度足够，否则跳过不设置（不自动创建数组元素）。
- 不透明值经两个接口参与导航（state/store.go:17-42）：
  - `PathNavigator.NavigateSegs(segs)`：剩余路径段整体交给实现惰性取值；返回值必须与内部结构无可变别名、实现并发只读安全；
  - `ValueMaterializer.MaterializeValue()`：全量物化为纯 Go 树（产物现场新建调用方接管所有权），供 robot.get 整读转 Lua table、GetMap 等需要完整容器视图的调用方。

**Overlay 是 SetPath 对 wire-first 整存值的写时物化（COW）写模型**（state/overlay.go:1-13）：

- 结构：`base PathNavigator`（不可变基座）+ `overrides map[string]any`（覆盖层）（overlay.go:22-25）；
- 读：先查覆盖层（nil 写入为**墓碑** tombstone，读视为不存在、物化时删键——与旧 map 语义差别仅在保键/除键，GetPath 读到的都是 nil），未命中回落基座惰性导航（33-52）；
- 写：`childForWrite` 沿写路径把「基座的子节点」按需物化进覆盖层——message 子节点继续包 Overlay、repeated/map 终端物化为可变 `[]any`/`map[string]any`，**未触碰的兄弟子树保持基座字节形态零展开**（119-164）；终端赋值 `setChild`，nil 写墓碑（167-176）；
- **常驻体积 ∝ 实际写入的路径数，而非消息 schema 大小**——这是 wire-first 留存 + 按需写覆盖的核心收益；
- `MaterializeValue`：基座全量物化后套用覆盖层产出纯 `map[string]any` 树（嵌套导航值递归物化、容器复制，不改写内部状态）（74-114）；
- 写侧**不解码不重编码**整条消息；并发契约与嵌套容器一致——overrides 只由执行器 goroutine 在 Store 写锁内改写（overlay.go:12-13）。

### 9.3 state/shared：Redis 共享态

`shared.Store` 接口（state/shared/store.go:18-58）提供五组操作 + 生命周期：

- **KV**：`Set/Get/Delete/Exists/Expire`（ttl==0 表示不过期）；
- **Counter**：`Incr`（delta=0 合法，作「+0 读当前值」用，redis_store.go:239-245）；
- **Claim/Lock**：见下；
- **Queue/List**：`QueuePush/QueuePop/QueueLen/QueueExpire`（FIFO 尾进头出）；
- **Hash**：`HashSet/HashGet/HashGetAll/HashDelete/HashIncr/HashExpire`；
- `DefaultClaimTTL`/`OpTimeout`（Lua 层据此派生 opCtx）+ `Cleanup`（删该 runId 全部 key 与索引集合）/ `Close`。

约定：key 不存在不是错误（读取类返回 `(零值, false, nil)`）；所有 key 由实现内部加 `keyPrefix:runId:type` 前缀，调用方只传 userKey。`RedisStore` 是唯一实现；任务未启用时 `Context.Shared` 为 nil，Lua 层返回 `ErrNotEnabled`（store.go:9-10）。

**Claim/Release 租约式锁**（state/shared/redis_store.go:249-271）：

- `Claim(key, owner, ttl)`：以 owner 身份竞争锁——锁不存在或已超时被清则获取成功并设置租约（Lua 脚本原子判定）；已被他人持有则失败；`ttl<=0` 用 `DefaultClaimTTL`；
- `Release(key, owner)`：仅当锁仍由该 owner 持有时删除（Lua 校验 owner，防误删他人锁）；
- 配套 `Owner`（查询持有者，不存在返回 `"", false, nil`）与 `Renew`（仅当仍持有时重置 TTL，租约过期即失效，redis_store.go:273-290）。

跨节点机器人由此实现「同一资源只被一个节点抢占」类协调（如全局唯一账号段分配、任务级一次性初始化）。

---

## 附：与本文相关的其他文档

- `docs/monitoring-system.md` — 监控指标体系（Apdex 口径、动作分类、ListenWait 指标聚合）
- `docs/ramp-up.md` — 渐进加压配置详解
- `docs/adapter-layer.md` / `docs/error-code-system.md` — 声明式 codec 配置与错误码契约
- 根 README.md 第六部分 — 本层的概览级摘要
