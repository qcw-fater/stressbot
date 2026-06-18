# T2-A1 报告 — listen queue 基础设施（安全非破坏）

> 任务 brief：`plans/declarative-codec/briefs/t2-a1-brief.md`
> 工作目录：worktree 根 `D:\Gitee\stressbot\.claude\worktrees\declarative-codec`，分支 `worktree-declarative-codec`。
> **未 git commit / add**（遵 brief 硬约束）。

## Status

**DONE**。TDD RED→GREEN，全 `network` 包测试 14 个用例通过，全仓 `go build ./...` 与 `go vet ./...` 干净。

## 改动文件清单（仅 `network/` 包，零越界）

| 文件 | 类型 | 说明 |
|---|---|---|
| `network/listen_queue.go` | 新增 | `listenQueue` 类型 + `newListenQueue`/`Push`/`Pop`/`Dropped`/`Clear` |
| `network/listen_queue_test.go` | 新增 | 8 个测试函数（含前置条件/FIFO/覆盖最旧/容量1等价/累计dropped/Clear/nil槽/并发smoke×2） |
| `network/connection_test.go` | 新增 | 6 个测试函数（缓存 listen 容量1等价/未注册/非nil回调/多route/Close后/nil receiver）+ `TestMain` 初始化日志 |
| `network/connection.go` | 改 | 字段 `listenMsg`→`listenQueues`、常量新增 `defaultListenQueueSize=1`、初始化、`dispatchListen` else、`GetListenResp`、`listenLoop` ctx.Done，共 6 处 |

无其他包改动。`engine/`、`robot/`、`script/`、`adapter/`、`admin/`、`agent/`、`cmd/`、`conf/` 一律未碰。

## `listenQueue` 算法

环形缓冲，无单独 tail，写入位置用 `(head+size)%capacity` 派生。Push/Pop 各 O(1)。

- **newListenQueue(capacity)**：`capacity < 1` 为编程错误，`panic("...当前值=" + strconv.Itoa(capacity))`，**不静默 clamp**。前置条件由调用方（dispatchListen 唯一创建点）保证传 `defaultListenQueueSize=1`。
- **Push(m) (dropped bool)**：
  - 未满（`size < capacity`）：`buf[(head+size)%capacity] = m; size++; return false`
  - 已满：`buf[head] = m`（覆盖最旧）；`head = (head+1)%capacity`；`dropped++; return true`；size 不变。新消息位于 `(head-1+capacity)%capacity` 即最新位置。
- **Pop() (*Message, bool)**：`size==0` 返回 `(nil,false)`；否则 `m = buf[head]; buf[head]=nil`（助 GC）；`head=(head+1)%capacity; size--`。**FIFO**。
- **Dropped() uint64**：`mu` 下读累计丢弃数。累计指标，**不被 Clear 重置**。
- **Clear()**：`mu` 下把 `buf` 各槽置 nil、`head=0;size=0`；`capacity` 与 `dropped` 不变。

## `Connection` 三处改写

1. **dispatchListen 的 else 分支**（`connection.go:385-405`）：按需为 routeKey 创建默认容量队列并 `Push`。`c.mu` 仅保护 `listenQueues` map 键的查找/创建；**Push 在 `c.mu` 释放后**进行（per-queue `mu` 串行化 Push/Pop，无锁序交叉、无死锁）。队满覆盖时打 `stresslog.Debug`（中文「监听队列已满，覆盖丢弃最旧消息」）。
2. **GetListenResp**（`connection.go:410-423`）：`c.mu` 下查 map 拿 `*listenQueue` 后释放，再调 `q.Pop()`。容量 1 时返回最新一条并清空，与旧「读 `listenMsg[k]` + `delete`」逐字节等价。
3. **listenLoop ctx.Done**（`connection.go:305`）：`clear(c.listenResp)` 之后跟 `clear(c.listenQueues)`。

配套：字段 `listenMsg map[string]*Message` → `listenQueues map[string]*listenQueue`（`:34`）；`mu` 注释更新为「保护 ... listenQueues map 键 ...（各 listenQueue 自带 mu 串行化 Push/Pop）」（`:37`）；常量块新增 `defaultListenQueueSize = 1`（`:72`）；`NewConnection` 初始化改为 `make(map[string]*listenQueue)`（`:90`）。

## 不变量证明（容量 1 ≡ 旧单槽）

| 行为 | 旧单槽 | 新队列(容量1) | 等价 |
|---|---|---|---|
| 首条 push | `listenMsg[k]=m` | size 0→1，buf[0]=m | ✓ |
| 后续 push（已有） | 覆盖为最新 | 满→覆盖 buf[head]，head 不动，dropped++，buf 里仍是最新 | ✓ |
| GetListenResp | 返回唯一条并 delete | Pop 返回唯一条，size→0 | ✓ |
| ctx.Done | clear map | clear map | ✓ |

行为级测试 `TestConnection_CachedListen_Capacity1Equivalence` 断言：push A→push B→GetListenResp 返回 B→再 GetListenResp nil，逐字节验证等价成立。

## 并发加锁模型

- `listenLoop`（work-pool goroutine，由 `AddListener`/`ListenResponse` 启动）→ `dispatchListen` → `q.Push`。
- 主流程 goroutine → `GetListenResp` → `q.Pop`。
- `c.mu`（`Connection` 级）只保护 `listenQueues` map 键集合的查找/创建/删除；每个 `listenQueue.mu` 独立保护自身环形缓冲。
- **Push/Pop 均在 `c.mu` 释放后执行**，map 锁与队列锁之间无嵌套/无锁序交叉，无死锁可能。
- `Dropped()`/`Clear()` 同样只持 per-queue `mu`。

## TDD RED/GREEN

- **RED**：先写 `listen_queue_test.go` + `connection_test.go`。首次 `go vet ./network/...` 报 `undefined: newListenQueue`（`listen_queue.go` 未实现），符合 RED。中途发现 2 个测试自身断言写反，在 RED 阶段修正（详见 concerns）。
- **GREEN**：实现 `listen_queue.go` + 改 `connection.go` 三处+字段+常量+初始化。`go test ./network/... -count=1` 全绿。
- **测试初始化**：`connection_test.go` 新增 `TestMain`，用 `stresslog.InitLog(tmp, "test", &Config{PrintConsole:false, LogLevel:"error"}, "")` 初始化全局 logger 与 atomicLevel（network 代码在 `NewConnection`、`work_pool.Go` 等热路径调用 `stresslog.Debug`/`DebugEnabled`，未初始化会 nil panic）。沿用 `script/runtime_cache_test.go` 同款写法（`ReplaceLogger(zap.NewNop())` 不够——`DebugEnabled()` 走包级 `loglevel` 而非 logger，必须 `InitLog`）。

## 验证

| 命令 | 结果 |
|---|---|
| `go build ./...` | 干净，无输出 |
| `go vet ./network/...` | 干净 |
| `go vet ./...` | 干净 |
| `go test ./network/... -count=1` | **ok stressbot/network**，14 个测试函数全 PASS |
| `go test -race ./network/...` | **未跑**：`go env CGO_ENABLED=0`，`-race requires cgo`。并发安全为「结构性论证（per-queue mu + c.mu 仅护 map 键，Push/Pop 在 c.mu 释放后无锁序交叉）+ goroutine smoke（8 goroutine×50 次 Push/Pop、16 goroutine×100 次全 Push 容量1 队列断言 dropped=1599）」 |

### 测试用例（14 个函数）

`listen_queue_test.go`（8）：
1. `TestNewListenQueue_CapacityPrecondition`（子用例：capacity≥1 构造 / capacity<1 panic）
2. `TestListenQueue_PushPop_FIFO`（容量 3，push A/B/C → FIFO pop A/B/C → 空）
3. `TestListenQueue_Push_FullEvictsOldest`（容量 2，满后 push C dropped=true，FIFO pop B/C，A 被丢）
4. `TestListenQueue_Capacity1_EquivalentToSingleSlot`（容量 1 等价单槽：保最新）
5. `TestListenQueue_DroppedAccumulates`（容量 1 连 push 5 条 → dropped=4，pop 得 E）
6. `TestListenQueue_Clear`（Clear 后 Pop 空、capacity 不变、dropped 不被重置）
7. `TestListenQueue_Clear_NilBufSlots`（Clear 后 buf 各槽 nil 助 GC）
8. `TestListenQueue_ConcurrentSmoke` + `TestListenQueue_ConcurrentSmoke_Capacity1`（并发 smoke）

`connection_test.go`（6 + TestMain）：
1. `TestConnection_CachedListen_Capacity1Equivalence`（核心：nil 回调 push 2 条 → GetListenResp 最新 → 再 nil）
2. `TestConnection_CachedListen_NoListener`（未注册→丢弃）
3. `TestConnection_CachedListen_NonNilCallback`（非 nil 回调走回调不缓存）
4. `TestConnection_CachedListen_MultipleRoutes`（多 route 独立缓存）
5. `TestConnection_GetListenResp_Closed`（Close 后返回 nil）
6. `TestConnection_GetListenResp_NilReceiver`（nil receiver 安全）

## Self-review

- [x] `listenQueue`：`newListenQueue`（前置条件 panic）/`Push`（满覆盖最旧+dropped）/`Pop`（FIFO）/`Dropped`/`Clear` 齐全；容量 1 等价单槽用例通过。
- [x] `Connection`：`listenMsg` 在 Go 代码中零残留（仅 `connection.go:410` 注释里为说明等价性引用「旧读 listenMsg[k] + delete」，属文档性引用，符合 brief「等价证明」需要）。三处改写（`dispatchListen` else / `GetListenResp` / `listenLoop` ctx.Done）正确。`c.mu` 与 per-queue `mu` 无死锁。
- [x] 零配置面改动：无 `ListenRef.QueueSize`、无注册 API 形参变化、无 `engine/`/`robot/`/`script/`/`conf/` 改动。
- [x] 仅 `network/` 改动；全仓 `go build ./...` 与 `go vet ./...` 绿。
- [x] 中文日志（沿用 `stresslog`）；无兼容兜底（`newListenQueue(capacity<1)` 直接 panic）。
- [x] godoc 包注释齐全（`listenQueue` 类型与方法）。

## Concerns

1. **容量 1 时 Debug 覆盖日志会触发**：默认容量 1 下，同 routeKey 第 2 条及之后每条推送都会触发一次 `stresslog.Debug("[NETWORK] 监听队列已满，覆盖丢弃最旧消息", ...)`。这是旧单槽本就有的「保最新」语义（旧实现静默覆盖、无日志），属预期行为而非回退。用 **Debug 级**，生产默认 Info 级不刷屏；2-A2 让高频 route 显式配 `queueSize>1` 后自然减少。若调试期临时开 Debug，高频推送 route 的日志量需注意。

2. **`-race` 未跑**：本机 `CGO_ENABLED=0`（Windows 无 cgo），race detector 不可用。并发安全当前依赖「per-queue mu 串行化 Push/Pop + c.mu 仅护 map 键 + Push/Pop 在 c.mu 释放后无锁序交叉」的结构性论证，并辅以 goroutine smoke（多 goroutine 高并发交替 Push/Pop、全 Push 容量 1 队列断言 dropped 精确值）。若后续在 CI 上启用 cgo，建议补跑 `go test -race ./network/...`。

3. **测试 RED 修正**：首轮 RED 跑通后 `TestListenQueue_Push_FullEvictsOldest` 自身断言写反（期望 pop C 再 pop B，实际 FIFO 应先 pop B 再 pop C，最旧 A 被丢）。该错误是测试期望错而非实现错——已在 RED 阶段修正测试，最终全绿。`listen_queue.go` 的 Push 满→覆盖最旧、Pop FIFO 实现正确，与 brief 算法描述一致。

4. **无现有 network 测试**：本任务前 `network/` 包无 `*_test.go`，故 `connection_test.go` 为新建（非追加）。`TestMain` 初始化 logger 是 network 包首个测试基础设施，后续 T2-A2/C1/C3 在本包加测试可直接复用。

## 下一步

T2-A1 完成，无遗留。可进入 **T2-A2**：`engine.ListenRef.QueueSize` schema + flow 校验 + 注册接线（`AddListener`/`ListenResponse`/`EnsureTCPListener`/`EnsureUDPListener` 形参透传 queueSize 到 `newListenQueue`）+ 下线 `ListenDef.script` 回调分支。
