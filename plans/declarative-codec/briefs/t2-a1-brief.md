# T2-A1 Brief — listen queue 基础设施（安全非破坏）

> 你是 implementer。先读本 brief。参考 `plans/declarative-codec/02-track-backend-integration.md` §2-A（尤其「实施切片」第 2 项「Network 层先实现 queue 能力，不改业务语义」）、总纲（`plans/declarative-codec/00-master.md`）§1 不变量、`plans/declarative-codec/progress-ledger.md` §「全局约束」。
> 工作目录：worktree 根 `D:\Gitee\stressbot\.claude\worktrees\declarative-codec`。**不要 git commit。**

## 目标

在 `network` 包引入**固定容量环形队列 `listenQueue`**，把 `Connection` 的监听消息缓存从「单槽 map」升级为「per-route 环形队列 map」。**默认容量 1，行为与现状逐字节等价**——这是纯基础设施重构，零配置面改动、零 API 签名改动，为 2-A2（queueSize 可配 + 注册接线）和 2-C3（connectionPump）铺路。

## 为什么这一步必须先做且必须非破坏

- 当前监听缓存是 `Connection.listenMsg map[string]*Message`（单槽：同 routeKey 新消息**覆盖**旧消息）。
- 2-A2 要让高频 route 显式配 `queueSize>1`，但前提是网络层已有「队列」原语；本任务只建原语、不接配置。
- 「禁止兼容性兜底 / 新字段全链路一致」：**本任务不新增任何配置字段**（`ListenRef.QueueSize`、注册 API 的 `queueSize` 形参、`ListenDef.script` 禁用——全部归 2-A2）。本任务落地的唯一新行为是「容量 1 的队列」，与旧单槽完全等价。

## 范围（严格边界）

**做（仅 `network` 包）：**
- 新增 `network/listen_queue.go`：`listenQueue` 类型 + 方法 + `newListenQueue`。
- 改 `network/connection.go`：`listenMsg` → `listenQueues`，三处使用点改写（见下）。
- 新增 `network/listen_queue_test.go`：TDD。

**不做（明确推迟，碰了即视为越界）：**
- ❌ 不加 `engine.ListenRef.QueueSize` 字段、不动 flow 校验。
- ❌ 不改 `AddListener` / `ListenResponse` / `RegisterListen`（robot 层）签名。
- ❌ 不改 `EnsureTCPListener` / `EnsureUDPListener`（engine.NetSender）签名。
- ❌ 不删 `createListenCallback` 的 script 分支、不禁用 `ListenDef.script`。
- ❌ 不改 `gnet.OnTraffic` / `decodeLoop` / `listenLoop` 的调度模型（三协程照旧）。
- ❌ 不动 `conf/`、`engine/`、`robot/`、`script/`、`adapter/`、`admin/`、`agent/`、`cmd/`。

## 现状代码（已核对，行号以当前 worktree 为准）

`network/connection.go`：
- `:21` `type ListenCallBack func(message *Message)`。
- `:34` 字段 `listenMsg map[string]*Message`（注释「routeKey → 缓存消息（轮询模式，回调为 nil 时）」）。
- `:86` 初始化 `listenMsg: make(map[string]*Message)`。
- `:372-388` `dispatchListen`：查 `listenResp[routeKey]`；`cb != nil` 调回调；**否则（缓存 listen）`c.listenMsg[routeKey] = resp`（单槽覆盖）**。
- `:391-404` `GetListenResp(routeKey)`：`c.mu` 下读 `listenMsg[routeKey]` 并 `delete`（单槽弹出）。
- `:301` `listenLoop` 在 `ctx.Done` 时 `clear(c.listenMsg)`。
- `:37` `mu sync.Mutex` 注释「保护 responseMap / listenResp / listenMsg / 回调字段」。
- `:68-71` 已有常量块 `listenChSize` / `decodeChSize`。

`network/message.go`：`Message{ RouteKey, Data, HeaderErr, WireBytes, Timing }`，有 `NewMessage` / `Copy`。

**全仓 grep 实测**：`listenMsg` 仅出现在 `network/connection.go` 上述 6 处；**无任何 `*_test.go` 引用 `listenMsg`**（行为级测试，无白盒断言）。

## 设计

### `listenQueue`（`network/listen_queue.go`，包内不导出）

```go
// listenQueue 是固定容量的监听消息环形队列（network 包内部）。
// 每个 routeKey 一份；容量固定，队满时覆盖最旧（丢最旧、保最新），dropped 计数。
// 自带 sync.Mutex：dispatchListen 的 Push 与主流程 GetListenResp 的 Pop 可并发，
// 不依赖 Connection.mu（Connection.mu 只保护 listenQueues 这个 map 的键操作）。
type listenQueue struct {
    buf      []*Message
    head     int    // 最旧元素下标（下一个出队位置）
    size     int    // 当前元素数
    capacity int    // 固定容量，构造后不变； precondition: capacity >= 1
    dropped  uint64 // 队满覆盖累计丢弃数
    mu       sync.Mutex
}
```

方法（均线程安全，内部加 `mu`）：

- `newListenQueue(capacity int) *listenQueue`：`capacity` 为**前置条件 ≥1**（调用方保证；本任务唯一创建点传 `defaultListenQueueSize`）。不做静默 clamp——若需要防御，panic 暴露编程错误（不要写「<1 就当 1」的兼容兜底）。
- `Push(m *Message) (dropped bool)`：
  - 未满：`buf[(head+size)%capacity] = m; size++; return false`。
  - 已满：`buf[head] = m`（覆盖最旧）; `head = (head+1)%capacity`; `dropped++; return true`。size 不变（保持 capacity）。新消息位于 `(head-1+capacity)%capacity` 即最新位置。✓
- `Pop() (*Message, bool)`：`size>0` 时 `m = buf[head]; head=(head+1)%capacity; size--; return m, true`；空则 `return nil, false`。**FIFO**。
- `Dropped() uint64`：返回累计丢弃数（`mu` 下读）。
- `Clear()`：`mu` 下 `head=0; size=0`，并把 `buf` 各槽置 nil 助 GC（capacity 不变）。

> 写出位置统一用 `(head+size)%capacity` 派生，不单独存 tail。Push/Pop 各自 O(1)。

### `Connection` 改造（`network/connection.go`）

1. **字段**（`:34`）：
   ```go
   listenQueues map[string]*listenQueue // routeKey → 缓存队列（轮询模式，回调为 nil 时）
   ```
   删 `listenMsg`。`:37` 注释里 `listenMsg` 改为 `listenQueues`。

2. **常量**（`:68-71` 块内新增）：
   ```go
   defaultListenQueueSize = 1 // 监听缓存队列默认容量；与旧单槽语义等价。2-A2 起可由 ListenRef.queueSize 显式覆盖。
   ```

3. **初始化**（`:86`）：`listenMsg: make(map[string]*Message)` → `listenQueues: make(map[string]*listenQueue)`。

4. **dispatchListen 缓存分支**（`:383-387` 的 `else`）：用「按需创建队列（默认容量）+ Push」替换单槽覆盖：
   ```go
   } else {
       c.mu.Lock()
       q, ok := c.listenQueues[resp.RouteKey]
       if !ok {
           q = newListenQueue(defaultListenQueueSize)
           c.listenQueues[resp.RouteKey] = q
       }
       c.mu.Unlock()
       if q.Push(resp) {
           stresslog.Debug("[NETWORK] 监听队列已满，覆盖丢弃最旧消息",
               zap.String("service", c.serviceName), zap.String("routeKey", resp.RouteKey))
       }
   }
   ```
   - `c.mu` 仅保护 map 键的查找/创建；Push 在 `c.mu` **释放后**进行（per-queue `mu` 串行化 Push/Pop，无死锁）。
   - **注意**：默认容量 1 时，第 2 条及之后每条推送都会触发一次「覆盖丢弃」——这是单槽本就有的「保最新」语义，属预期；用 **Debug 级**日志，生产默认不刷屏。2-A2 让高频 route 显式配 `queueSize>1` 后自然减少。

5. **GetListenResp**（`:391-404`）：FIFO Pop：
   ```go
   func (c *Connection) GetListenResp(routeKey string) *Message {
       if c == nil || atomic.LoadInt32(&c.isClose) == 1 {
           return nil
       }
       c.mu.Lock()
       q, ok := c.listenQueues[routeKey]
       c.mu.Unlock()
       if !ok {
           return nil
       }
       m, _ := q.Pop()
       return m
   }
   ```
   - `c.mu` 仅查 map；Pop 走队列自身 `mu`。容量 1 时与旧「读+delete 单槽」行为一致（返回最新一条并清空）。

6. **listenLoop ctx.Done**（`:301`）：`clear(c.listenMsg)` → `clear(c.listenQueues)`。

### 不变量证明（容量 1 ≡ 旧单槽）

| 行为 | 旧单槽 | 新队列(容量1) |
|---|---|---|
| 首条 push | `listenMsg[k]=m` | size 0→1，buf[0]=m |
| 后续 push（已有一条） | 覆盖为最新 | 满→覆盖 buf[head]，head 不动，dropped++，buf 里仍是最新 |
| GetListenResp | 返回唯一条并 delete | Pop 返回唯一条，size→0 |
| ctx.Done | clear map | clear map |

GetListenResp 在两种实现下都返回「最近一条 push」并清空。**等价成立。**

## 并发模型（关键，决定加锁正确性）

- `listenLoop`（work-pool goroutine，由 `AddListener`/`ListenResponse` 启动）→ `dispatchListen` → `q.Push`。
- 主流程 goroutine → `GetListenResp` → `q.Pop`。
- 二者并发：`c.mu` 保护 `listenQueues` map 键集合；每个 `listenQueue.mu` 保护自身环形缓冲。**Push/Pop 均在 `c.mu` 释放后执行**，无锁序交叉。`Dropped()`/`Clear()` 同理。

## 关键约束

- **纯 `network` 包**：只动 `network/listen_queue.go`(新)、`network/listen_queue_test.go`(新)、`network/connection.go`。跨包改动 = 越界。
- **不新增配置字段 / 不改任何公开 API 签名**（`AddListener`/`ListenResponse`/`GetListenResp` 签名保持不变）。
- **不写兼容兜底**：`newListenQueue` 的 capacity<1 是编程错误（panic 或显式前置条件），不做静默修正。
- Go 最佳实践：godoc 包注释、日志中文（沿用 `stresslog` 即 `utils/log`）、错误用 `errcode` 体系（本任务预期不产生新框架错误码）。
- **不要 git commit。**

## 工作方式（TDD）

1. **RED** — `network/listen_queue_test.go`（纯单元，不依赖 Connection）：
   - `Push`/`Pop` 基本 FIFO：容量 3，push A/B/C → pop 依次 A/B/C，再 pop 返回 `(nil,false)`。
   - **覆盖最旧**：容量 2，push A/B（满）→ push C 返回 `dropped=true` → pop C、pop B（最旧 A 被丢），`Dropped()==1`。
   - 容量 1 等价单槽：push A → push B（`dropped=true`）→ Pop 返回 B（最新），再 Pop 空；`Dropped()==1`。
   - `Clear`：容量 3 push 2 条 → Clear → Pop 空，`size==0`，`Dropped()` 不被 Clear 重置（累计指标）。
   - **并发 smoke**：`go test -race` 若环境无 cgo 则用 goroutine smoke（如 8 goroutine 各 push/pop 各 50 次，断言无 panic、最终 size 一致），结构性论证线程安全。
2. **RED** — `network/connection_test.go`（若已存在则追加用例；否则新增）行为级：
   - 构造 Connection，对一个 routeKey 注册 **nil 回调**（缓存 listen，走 `dispatchListen` 的 else 分支）：push 2 条 → `GetListenResp` 返回最新 1 条（容量 1 等价单槽）→ 再 `GetListenResp` 返回 nil。
   - （若现有 connection 测试已有「缓存 listen」用例，确保它在新实现下仍绿。）
3. **GREEN** — 实现 `listen_queue.go` + 改 `connection.go`。
4. `go build ./...`、`go vet ./network/...`、`go test ./network/... -count=1` 全绿、输出干净。
5. 若环境允许（有 cgo），`go test ./network/... -race -count=1`；否则在报告里说明「无 cgo，并发安全为结构性论证 + goroutine smoke」。
6. 全仓 `go build ./...` 确认未误伤其他包（本任务不应改其他包，但构建会暴露越界）。
7. **不要 git commit。**

## 验收（self-review）

- `listenQueue`：`Push`(满覆盖最旧+dropped)、`Pop`(FIFO)、`Dropped`、`Clear`、`newListenQueue` 齐全；容量 1 等价单槽的用例通过。
- `Connection`：`listenMsg` 零残留（grep 仅 `listenQueues`）；`dispatchListen`/`GetListenResp`/`listenLoop` 三处改写正确；`c.mu` 与 per-queue `mu` 无死锁（Push/Pop 在 c.mu 释放后）。
- **零配置面改动**：无 `ListenRef.QueueSize`、无注册 API 形参变化、无 `engine/`/`robot/`/`script/` 改动。
- 仅 `network/` 改动；全仓 `go build ./...` 绿；中文日志；无兼容兜底。
- 现有 `network` 测试全绿（无行为回退）。

## 报告

写完整报告到 `plans/declarative-codec/reports/t2-a1-report.md`：实现内容、`listenQueue` 算法（Push 满/非满、Pop FIFO、dropped）、`Connection` 三处改写、不变量等价证明、并发加锁模型、TDD RED/GREEN、改动文件清单、self-review、concerns（含「容量 1 时 debug 覆盖日志会触发」的说明）、`-race` 是否跑了。

返回（<15 行）：Status、改动文件、一行测试摘要、concerns、报告路径。
