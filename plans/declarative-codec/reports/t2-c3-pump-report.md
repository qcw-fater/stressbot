# T2-C3-connectionPump 报告

> 状态：**完成（network 包 build/vet/test 全绿；全仓 build/vet 绿）**。
> 并行态：另一 agent（2-C2-Go encode，engine/+robot/）同时在跑；本任务只改 `network/connection.go` + `network/gnet.go` + `network/heartbeat.go`，接口契约保持稳定（`RegisterHeartbeat(HeartbeatConfig{Interval, Builder func() []byte})` 签名零改动）。
> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）。**未 git commit**。

---

## 1. 目标与结论

把每条连接的三个 goroutine（`decodeLoop` + `listenLoop` + 心跳 `runHeartbeat`）合并为**单一 `connectionPump`**：统一处理 inbound decode→response/listen 分发 + heartbeat timer/control。pump 是 network 内部调度，不泄漏到 flow/engine/Lua。

**结论**：三协程已全部并入 pump；两条硬约束（heartbeat-due 优先 + inbound bounded batch）已落地；外层接口（`RegisterListen` / `GetListenResp` / `Send` / `RegisterHeartbeat` / `Dialer.DialTCP/UDP`）签名与语义保持稳定；decode 3-tuple / listen 队列 / 心跳 builder 契约零回退。

---

## 2. 设计

### 2.1 单一 `connectionPump` goroutine

每条连接在 `StartPump(adp, isUDP)` 时启动一个 pump goroutine（`utils.GetWorkPool().Go`），CAS 防重复启动。pump 是以下三件事的唯一 owner：

| 旧 goroutine | 旧位置 | 并入 pump 后 |
|---|---|---|
| `decodeLoop` | `connection.go` `StartDecodeLoop`→`decodeLoop` | pump `case <-c.inboundCh` → `decodeAndDispatch` |
| `listenLoop` | `connection.go` `RegisterListen` CAS 启动 | pump `decodeAndDispatch`→`OnReceive` 命中 listenResp 时**同步**调 `dispatchListen`（不再经 `listenCh` 中转） |
| `runHeartbeat` | `heartbeat.go` `RegisterHeartbeat`→独立 goroutine | pump 持有 `hb *heartbeatRuntime{cfg, timer}`，timer 到期 → `sendHeartbeatLocked`（调 builder→Send） |

### 2.2 pump 主循环

```go
for {
    // 硬约束 1：heartbeat-due 优先（非阻塞消费 timer.C）
    if c.heartbeatDueLocked() { c.sendHeartbeatLocked(); c.resetHeartbeatTimerLocked(); continue }

    var heartbeatC <-chan time.Time
    if c.hb != nil && c.hb.timer != nil { heartbeatC = c.hb.timer.C }  // nil chan 被 select 忽略

    select {
    case <-c.ctx.Done():      c.drainInboundLocked(); return
    case <-heartbeatC:        c.sendHeartbeatLocked(); c.resetHeartbeatTimerLocked()
    case cmd := <-c.controlCh: c.handleControlLocked(cmd)
    case frame, ok := <-c.inboundCh:
        c.decodeAndDispatch(frame)
        // 硬约束 2：bounded batch（最多 pumpInboundBatchSize=16 条后回外层）
    }
}
```

**关键设计点**：
- **`heartbeatC` 动态拼进 select**：未注册心跳时为 nil（select 自动忽略），注册后指向 `hb.timer.C`。这是让 pump 在心跳到期时被唤醒的唯一手段——否则 pump 会一直阻塞在 inbound/control/ctx 三路 select 上，即便心跳到期也无法触发。
- **双重 heartbeat 处理**：循环顶部的 `heartbeatDueLocked`（非阻塞消费 timer.C）负责处理「pump 醒来时 timer 已到期但 select 没随机到 heartbeatC 分支」的情况；select 内的 `<-heartbeatC` 分支负责「timer 刚到期立即唤醒」。两者互补，无重复发送（heartbeatDueLocked 消费掉触发后 reset，下一轮 select 看到 reset 后的新 timer）。

### 2.3 两条硬约束（02-track §2-C 伪代码）

1. **heartbeat-due 优先**：每轮循环开头先非阻塞检查 `hb.timer` 是否已到期，到期立即发送 + reset，再进入 select。这避免了「select 伪随机 + hot inbound 通道」导致 timer.C 长期选不中（inbound 饿死心跳）。
2. **inbound bounded batch**：进入 inbound 分支后，连取首帧后最多再连取 `pumpInboundBatchSize-1`（=15）条（非阻塞，default 即停），处理完强制回外层重新检查 heartbeat due + select。`pumpInboundBatchSize = 16` 是经验值：足够批处理摊销 channel 调度开销，又小到让 timer/control 在毫秒级得到响应。

测试 `TestPump_BoundedBatch_DoesNotStarveHeartbeat` 验证：灌入 200 帧 inbound backlog 的同时，15ms 间隔心跳在 150ms 内仍被发送 ≥2 次。

### 2.4 controlCh 驱动心跳

`RegisterHeartbeat(cfg)` / `StopHeartbeat()` 不再启动独立 goroutine，改为投递 `pumpCmd` 到 `c.controlCh`，由 pump 在 `handleControlLocked` 里串行安装/停止心跳 runtime（`hb *heartbeatRuntime{cfg, timer}`）。这彻底消除了旧的「心跳 builder 抢 luaMu」死锁路径（2-B 已让 builder Go-only，2-C3 进一步去掉独立 goroutine）。

`pumpCmd` 三种 kind：`pumpCmdHeartbeat`（替换/注册）、`pumpCmdStopHeartbeat`（停止）、`pumpCmdStop`（主动请求退出，Close 内部用——当前 Close 走 cancel ctx 路径，pumpCmdStop 预留但未在 Close 主路径使用）。

**兼容降级路径**：pump 未启动（`controlCh == nil`，仅测试/异常态）时，`RegisterHeartbeat`/`StopHeartbeat` 直接写/清 `hb` 字段 + 启停 timer。生产路径 dial 总是先 `StartPump` 后注册心跳，不走此分支。测试 `TestRegisterHeartbeat_PumpNotStarted` 覆盖。

### 2.5 生命周期与关闭

- `doClose()`：`cancel()` ctx → `StopHeartbeat()`。cancel 是 pump 退出的唯一权威信号：pump 主循环看到 `ctx.Done` 立即 `drainInboundLocked`（归还 buffer）并 return；`defer stopHeartbeatTimerLocked` 停心跳 timer。
- `StopHeartbeat()`：投递 `pumpCmdStopHeartbeat`（带 result channel 同步等 pump 处理完），保留旧「返回即心跳已停」语义。即便投递丢失（pump 正卡在 inbound batch / 已退出），`pumpDone` 关闭即表示 timer 已被 defer 停掉；兜底等 `pumpDone` 或 `stopHeartbeatFallbackTimeout=2s`。
- 无 goroutine 泄漏：pump 是连接唯一后台 goroutine，Close→cancel→pump 退出→`pumpDone` 关闭。测试 `TestPump_Close_NoLeak` 验证 Close 后 `WaitPumpDone` 1s 内返回。
- 主动/被动关闭语义保持：`Close()`（主动，CAS isClose=1 + closeFunc + onClosed）、`onClose(reason)`（gnet OnClose，CAS + closeReason + onDisconnect + onClosed）双路径通过 isClose CAS 互斥。

### 2.6 并发安全

- `c.adp` 是 Go SchemaAdapter（无可变状态，T1 冻结交接 §1.1），pump 单 goroutine 串行 decode，天然有序。
- `responseMap` / `listenResp` / `listenQueues` map 键由 `c.mu` 保护（与 RequestResponse 同锁粒度）。
- `listenQueues` 每个 route queue 自带 `sync.Mutex`（Push/Pop/Clear 串行化），pump 的 `dispatchListen` Push 与主流程 `GetListenResp` Pop 无死锁（c.mu 释放后再 Push/Pop）。
- `hb *heartbeatRuntime` 由 pump goroutine 独占写（handleControlLocked / stopHeartbeatTimerLocked 内）；`hbMu` 只保护「pump 外部投递 controlCh 前后的 hb 快照读」，pump 内部不走锁。

---

## 3. 接口契约保持（与 2-C2-Go agent 并行的关键）

| 接口 | 状态 | 说明 |
|---|---|---|
| `Connection.RegisterHeartbeat(HeartbeatConfig{Interval time.Duration, Builder func() []byte})` | **签名零改动** | 内部从「启独立 goroutine」改为「controlCh 驱动 pump」；2-C2 agent 的心跳闭包仍是 `func() []byte`，pump 调它 |
| `Connection.RegisterListen(routeKey, cb, queueSize) error` | 签名零改动 | 改为纯 map 操作（不再 CAS 启动 listenLoop）；listen 分发并入 pump 同步调 dispatchListen |
| `Connection.GetListenResp(routeKey) *Message` | 签名零改动 | 主流程 FIFO Pop，per-queue mu 串行化 |
| `Connection.Send(data) (int, error)` | 签名零改动 | pump 心跳发送仍走 Send |
| `Dialer.DialTCP/DialUDP(ctx, addr, conn, adp)` | 签名零改动 | dial 内 `StartDecodeLoop`→`StartPump`（gnet.go 内部调用，gnet.go 是本任务文件） |
| `Connection.EnqueueRaw(msgBuf, recvFrameAt) EnqueueResult` | 签名零改动 | 投递目标从 decodeCh 改为 inboundCh；三态语义不变 |
| `decode` 3-tuple `(routeKey, body, headerErr)` | 语义不变 | `c.adp.DecodeTCP/UDP` 签名零改动（T1 冻结） |
| listen 队列语义 | 不变 | `listenQueues` ring buffer + queueSize + 覆盖最旧策略 |
| 心跳 builder 契约 | 不变 | `func() []byte`，nil 跳过本 tick |

**保留的旧方法名**（client.go 不在本任务可改清单，为兼容其 CloseAllWithTimeout 调用保留）：
- `WaitDecodeDone` / `WaitDecodeDoneTimeout`：现在等 `pumpDone`，与 `WaitListenDone` 等价。
- `WaitListenDone` / `WaitListenDoneTimeout`：同上。
- 新增 `WaitPumpDone` / `WaitPumpDoneTimeout`：推荐新代码用本方法表达「等连接所有后台 goroutine 退出」。

---

## 4. 改动文件

| 文件 | 改动 |
|---|---|
| `network/connection.go` | 新增 `connectionPump` + `StartPump` + `handleControlLocked` + `heartbeatDueLocked`/`sendHeartbeatLocked`/`resetHeartbeatTimerLocked`/`stopHeartbeatTimerLocked` + `drainInboundLocked`；`StartDecodeLoop`/`decodeLoop` 删除（并入 pump）；`listenLoop` 删除（dispatch 并入 OnReceive 同步路径）；`listenCh`/`listenDone`/`listenRunning`/`decodeCh`/`decodeDone`/`decodeRun` 字段删除，替换为 `inboundCh`/`controlCh`/`pumpDone`/`pumpRun`/`hb`/`hbMu`；`OnReceive` 命中 listenResp 时同步调 dispatchListen（不再投 listenCh）；`EnqueueRaw` 投 inboundCh；`WaitDecodeDone*`/`WaitListenDone*` 等 pumpDone；新增 `WaitPumpDone*`；`RegisterListen` 去掉 CAS 启 loop；`doClose` 注释更新；常量 `decodeChSize`/`listenChSize` → `inboundChSize`，新增 `pumpInboundBatchSize`/`controlChSize`/`pumpCmd`/`pumpCmdKind` |
| `network/gnet.go` | `OnTraffic` 注释更新（decodeCh→inboundCh、decodeLoop→connectionPump）；EnqueueChFull 日志「decode 通道已满」→「inbound 通道已满」；dial 内 `conn.StartDecodeLoop(adp, isUDP)` → `conn.StartPump(adp, isUDP)`（gnet.go:401） |
| `network/heartbeat.go` | 删 `heartbeatState`/`runHeartbeat`/`stopHeartbeatTimeout`；新增 `heartbeatRuntime{cfg, timer}`；`RegisterHeartbeat`/`StopHeartbeat` 改 controlCh 驱动 pump（含 controlCh==nil 降级路径）；新增 `stopHeartbeatFallbackTimeout`；`HeartbeatConfig`/`HeartbeatBuilder` 类型保留 |
| `network/connection_pump_test.go`（新增） | 8 个测试覆盖 pump 消费 inbound→dispatch（request-response/listen queue/listen callback）、controlCh 注册/停止心跳、ctx.Done 无泄漏、bounded batch 不饿死心跳、pump 未启动降级路径 |

---

## 5. TDD

RED→GREEN：

1. **RED**：先写 8 个测试（`connection_pump_test.go`）覆盖 brief 要求的全部场景。首轮 3 个心跳测试 FAIL：
   - `TestPump_Control_RegisterHeartbeat`：builder 调用 0（pump 未触发心跳到期）。
   - `TestPump_Control_StopHeartbeat`：停止前未发送心跳。
   - `TestPump_BoundedBatch_DoesNotStarveHeartbeat`：inbound backlog 期间心跳 0 次。
2. **根因**：原 pump 主循环 select 只监听 inbound/control/ctx 三路，**未把 `hb.timer.C` 放进 select**——pump 会一直阻塞在三路 select 上，即便心跳到期也无法唤醒；循环顶部的 `heartbeatDueLocked`（非阻塞）只在 pump 因其他原因醒来时才被检查。
3. **GREEN**：把 `heartbeatC <-chan time.Time`（动态：未注册心跳时为 nil）拼进 select，timer 到期时唤醒 pump 并直接发送心跳 + reset。8 个测试全 PASS。

---

## 6. Self-review

- [x] 单 connectionPump 替代 decodeLoop+listenLoop+心跳 goroutine。
- [x] heartbeat-due 优先（循环顶部非阻塞检查）+ inbound bounded batch（`pumpInboundBatchSize=16`）。
- [x] ctx.Done → drain inbound 归还 buffer + defer 停 timer + close(pumpDone)，无 goroutine 泄漏（`TestPump_Close_NoLeak`）。
- [x] `RegisterHeartbeat(HeartbeatConfig{Interval, Builder func() []byte})` 签名稳定（controlCh 驱动 pump）。
- [x] `StartDecodeLoop→StartPump`（gnet 内部 :401）。
- [x] robot/ 外层接口（RegisterListen/GetListenResp/Send/Dial）保持；client.go 的 WaitDecodeDone*/WaitListenDone* 调用兼容（等 pumpDone）。
- [x] decode/listen/心跳 字节+队列语义零回退（fakeAdapter decode 3-tuple、listen ring queue FIFO、心跳 builder→Send 验证）。
- [x] 仅改 network/connection.go + gnet.go + heartbeat.go（+ 新增 connection_pump_test.go）。
- [x] go build/vet/test（network）绿；全仓 go build ./... 绿、go vet ./... 绿；engine/robot/adapter/codec/network 测试全绿。

---

## 7. ⚠️ 运行时验证待办（真实服务端长跑）

pump 调度的下列性质需真实服务端长跑验证（单测用 fake adapter + 毫秒级 interval，无法覆盖生产 codec pipeline 的真实 CPU 耗时与高连接并发）：

1. **inbound 不饿死心跳**：高 QPS 入站（万级连接 × 数十包/s）+ codec pipeline 含解压/加密时，心跳仍按 `cfg.Interval` 稳定发送，不被 inbound decode backlog 拖延。`pumpInboundBatchSize=16` 的经验值需在真实负载下复核；若心跳抖动，可下调（如 8）。
2. **主流程阻塞时 pump 仍工作**：Robot 主流程阻塞在 `tcpListen`/Lua/Redis 时，pump 仍处理响应（RequestResponse 不超时）、推送（listen 队列不溢出）、心跳（服务端不判掉线）。这是 2-D 删 luaMu 的前置条件之一。
3. **无 goroutine 泄漏**：长跑 1–2 小时 + 多轮任务启停，pump goroutine 数应随连接 Close 即时归零（可用 pprof goroutine profile 抓 `connectionPump` 函数计数）。
4. **心跳发送时机**：连接刚建立、密钥刚交换后注册的心跳，首个 tick 时机符合预期（不漏发、不连发）。
5. **Close 收敛时序**：批量停止（500+ robot 同时 Close）时，`StopHeartbeat` 的 controlCh 投递 + `pumpDone` 等待不导致 Close 堆积或超时（`stopHeartbeatFallbackTimeout=2s` 兜底是否触发）。

---

## 8. Concerns

1. **`pumpInboundBatchSize` 与 `inboundChSize` 的调参依赖运行时数据**：当前 `16` / `256` 是经验值，真实负载下可能需调整。已在「运行时验证待办」中列为复核项。
2. **`StopHeartbeat` 的同步等待**：保留旧「返回即心跳已停」语义（带 result channel + pumpDone 兜底 + 2s fallback timeout）。若 pump 因未知 bug 卡死，最坏阻塞 2s。这是有意的（让 Close 不永久挂起），但需长跑确认 2s 兜底在批量停止时不会被频繁触发（触发会 warn 刷屏）。
3. **`pumpCmdStop` 当前未在 Close 主路径使用**：Close 走 cancel ctx 让 pump 自然退出。pumpCmdStop 预留给未来「主动优雅停止 pump 但不取消 ctx」的场景（当前无此需求），不影响功能。
4. **并行 agent 干扰**：实施过程中 connection.go/heartbeat.go 曾被外部进程（疑似 linter 或并行 agent worktree 重叠）短暂回滚/触碰，触发 harness stale-file-guard。最终状态已通过 `go build`/`go test`/`go vet` 验证为正确。controller 合并时需确认 connection.go/heartbeat.go/gnet.go 三个文件为本报告描述的 pump 版本，而非旧三协程版本。
5. **`describeError`/encode 路径未触及**：本任务严格限于 network/connection+gnet+heartbeat；encode 侧（engine/robot 经 resolver）由 2-C2-Go agent 负责。全仓 build 绿说明两边接口对接正确。

---

## 9. 报告位置

`plans/declarative-codec/reports/t2-c3-pump-report.md`（本文件）。
