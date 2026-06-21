# T2-A2.1 Brief — QueueSize schema + 注册接线（Go，安全非破坏）

> 你是 implementer。先读本 brief。参考 `plans/declarative-codec/02-track-backend-integration.md` §2-A（实施切片 1/3/4）+ §4「不需要改动的点」、`plans/declarative-codec/reports/t2-a1-report.md`（2-A1 落地的 listenQueue）、`plans/declarative-codec/progress-ledger.md` §「全局约束」与 §「gofmt/换行 环境注」。
> 工作目录：worktree 根 `D:\Gitee\stressbot\.claude\worktrees\declarative-codec`。**不要 git commit。**

## 目标

把 2-A1 落地的 `listenQueue` 基础设施**接上配置**：让 flow 的 `listenRefs` 能显式声明每条监听的缓存队列容量 `queueSize`，并把它从 schema 一路接到 `Connection` 的队列创建。**默认 queueSize=1，全链路与现状逐字节等价**——这是安全的接线重构，不碰脚本回调、不动调度模型。

## 关键背景（已读码核对）

1. **engine 包没有后端 flow 校验函数**（grep `func.*[Vv]alidate` 在 engine 无命中；校验在前端编辑器）。因此 queueSize 有效值计算 + `<=0` 报错 + 重复注册冲突检测**落在 `robot.RegisterListen`**（注册总入口，已持有 refs 与 `h.robot.adp` 可算 routeKey），不要去不存在的「flow 校验阶段」加。
2. **2-A1 已就位**：`network/listenQueue`（`newListenQueue(capacity)`，cap<1 panic）+ `Connection.listenQueues map[string]*listenQueue`（默认 `defaultListenQueueSize=1`，dispatchListen 按需建队列 + Push，GetListenResp FIFO Pop）。
3. **现有注册链**：`engine.executor` 执行带 `listenRefs` 的节点时调 `handler.RegisterListen(node.ListenRefs)`（executor.go:251-252，`ActionHandler` 接口，签名 `RegisterListen(refs []ListenRef) error` 不变）→ `robotActionHandler.RegisterListen`（robot.go:716-767）按 `(proto,service)` 分组、`routeKey = h.robot.adp.ExpectedRouteKey(ref.Route)`、组 `map[routeKey]ListenCallBack`、`conn.ListenResponse(listenMap)`（robot.go:763）→ `Connection.ListenResponse`（connection.go:357）`maps.Copy` 进 `listenResp` + 启动 listenLoop。
4. **`ListenDef.script` 当前唯一在用**：仅 `frameData`(udp:battle 4:11)→`listen_frame_data.lua`。**本任务不动它**（script callback 下线 + 启用 `ListenDef.script` 禁用校验归 T2-A2.2）。`createListenCallback` 的 Script 分支（robot.go:784-829）**保持原样**。
5. **现有配置零重复注册**（已普查）：3 个 listenRefs 节点共 19 条，按 `(server, route)` 全唯一。故本任务的「重复注册 fail-loud」**不会误伤任何现有配置**，只拦截未来配置错误。

## 范围（严格边界）

**做：**
- `engine.ListenRef` 加 `QueueSize *int` 字段。
- `robot.RegisterListen`：计算有效 queueSize（缺省 1、显式 `<=0` 报错），按 routeKey 注册并传 queueSize。
- `network.Connection`：注册 API 收敛为带 queueSize 的 `RegisterListen(routeKey, cb, queueSize) error`（预创建队列 + 冲突 fail-loud + 启动 listenLoop），**删掉** `AddListener`/`ListenResponse`（避免双 API 并存）。
- `engine.NetSender.EnsureTCPListener/EnsureUDPListener` 加 `queueSize int` 形参；`netSenderAdapter` 实现透传；Lua `ensure_tcp_listener/ensure_udp_listener` 调用时传 `queueSize=1`（Lua 不暴露 queueSize，复杂容量只由 flow `listenRefs` 配置）。
- 更新所有调用点（含 2-A1 留下的 `network/connection_test.go` 里的 `AddListener` 调用）。
- TDD 测试。

**不做（明确推迟，碰了即越界）：**
- ❌ 不删 `createListenCallback` 的 Script 分支、不加 `ListenDef.script` 禁用校验（→ T2-A2.2，须与配置迁移原子完成）。
- ❌ 不迁移任何 `conf/`（flow.json、scripts）配置（→ T2-A2.2）。
- ❌ 不动 `dispatchListen` 的调度模型、不碰 `gnet.OnTraffic`/`decodeLoop`/`listenLoop`、不碰 `connectionPump`（→ 2-C3）。
- ❌ 不改 `engine.ActionHandler.RegisterListen` 签名（queueSize 在 `ListenRef` 内，签名不变）、不改 `executor.go:252` 调用。
- ❌ 不动前端（QueueSize 字段的编辑器/校验支持是 T3）。
- ❌ 不改 `h.robot.adp.ExpectedRouteKey` 的调用方式（adp→resolver 替换是 2-C1；本任务 routeKey 计算照旧）。

## 设计

### 1. `engine.ListenRef.QueueSize`（engine/flow.go:71-75）

```go
type ListenRef struct {
    Route     any    `json:"route"`
    Server    string `json:"server"`
    Listen    string `json:"listen"`
    QueueSize *int   `json:"queueSize,omitempty"` // 监听缓存队列容量，缺省 1；显式 <=0 为配置错误（注册时报错）
}
```

- 用 `*int`（指针）区分「未写（→默认 1）」与「显式 0/负数（→报错）」——不做兼容兜底、不静默 clamp（遵循全局约束）。
- godoc 注释说明语义。

### 2. `robotActionHandler.RegisterListen`（robot.go:716-767）—— 有效 queueSize + 冲突拦截

改造要点：
- 遍历 refs，每条：
  - 解析 `proto, service`（`parseServer`，失败 warn+跳过，保持现状）。
  - **有效 queueSize**：`q := 1; if ref.QueueSize != nil { q = *ref.QueueSize; if q <= 0 { return fmt.Errorf("监听注册失败：连接 %q listen %q 的 queueSize=%d 非法，须 >= 1", ref.Server, ref.Listen, q) } }`（中文、fail loud、带 server+listen 上下文）。
  - `routeKey := h.robot.adp.ExpectedRouteKey(ref.Route)`（照旧）。
  - 按 `(proto, service)` 分组，把 `(routeKey, cb, q)` 收集起来。cb 计算**照旧**：`ref.Listen==""`→`nil`；否则 `createListenCallback(ref.Listen, cbDef)`（Script 分支**不动**）。
- 注册阶段：对每个分组拿 `conn`，**逐条**调 `conn.RegisterListen(routeKey, cb, q)`（替代原 `conn.ListenResponse(listenMap)` 一次性批量）。任一返回 error → 聚合并 `return`（fail loud；可返回第一条错误或聚合中文信息，含 server+routeKey+原因）。
- 不再构造 `map[string]ListenCallBack` 传给 `ListenResponse`（该方法将被删）。

> 注：逐条注册替代批量是可接受的——注册是连接建立时的一次性操作（非热路径），每条 `RegisterListen` 内部 CAS 启动 listenLoop 幂等且廉价。

### 3. `network.Connection` 注册 API 收敛（connection.go:267-370）

**删** `AddListener(routeKey, cb)`（:267-283）与 `ListenResponse(map)`（:357-370）。

**新增**：
```go
// RegisterListen 为指定 routeKey 注册持久化监听。
//   cb: nil = 缓存模式（消息进 queue，由 GetListenResp/main-flow 消费）；非 nil = 回调模式。
//   queueSize: 缓存队列容量（>=1，cap<1 由 newListenQueue panic）。首次注册时预创建队列。
// 重复注册同一 routeKey：queueSize 与 cb 是否为 nil 都一致 → 幂等（no-op）；否则返回 error（fail loud）。
// 注册时若 listenLoop 未运行则启动。
func (c *Connection) RegisterListen(routeKey string, cb ListenCallBack, queueSize int) error
```

实现要点：
- `c.mu.Lock()`。
- 读 `existingCb, hasCb := c.listenResp[routeKey]`、`existingQ, hasQ := c.listenQueues[routeKey]`。
- **冲突检测**（仅当 `hasCb || hasQ`）：
  - `hasQ && existingQ.capacity != queueSize` → 冲突（queueSize 不一致）。
  - `hasCb && (existingCb == nil) != (cb == nil)` → 冲突（缓存/回调模式不一致）。
  - 冲突 → `c.mu.Unlock()` 后返回 `fmt.Errorf("监听注册冲突：routeKey %q 已注册（queueSize=%d, 回调=%v），与本次（queueSize=%d, 回调=%v）不一致", routeKey, existingQ.capacity, existingCb!=nil... )`（中文、带双方参数）。
  - 一致 → 幂等 no-op：`c.mu.Unlock()` 返回 nil（可顺手 `c.listenResp[routeKey]=cb` 保持一致，无副作用）。
- **新注册**：`c.listenResp[routeKey] = cb`；若 `!hasQ` → `c.listenQueues[routeKey] = newListenQueue(queueSize)`（**预创建队列，容量=queueSize**；若 `hasQ` 但容量不同已被上面冲突拦下）。
- `needStart := atomic.LoadInt32(&c.listenRunning) == 0`；`c.mu.Unlock()`。
- `needStart` 时 CAS 启动 listenLoop（沿用现有 AddListener:277-282 的 CAS 逻辑）。
- return nil。

**dispatchListen（connection.go:372-388）微调**：缓存分支取队列时，队列已在注册时预创建；保留 get-or-create 兜底（防御性，cap 用 `existingQ.capacity` 或 `defaultListenQueueSize`——实际上注册过的必有队列，兜底用 default 即可）。Push + Debug 守卫逻辑**不动**（2-A1 已做）。

**GetListenResp / listenLoop ctx.Done**：不动（2-A1 已是 FIFO Pop / clear map）。

> 不变量：queueSize=1（缺省）时，预创建队列容量 1，与 2-A1「按需建容量 1 队列」行为完全一致 → 全链路等价。

### 4. `engine.NetSender` 接口 + `netSenderAdapter`（action.go:166-171 / robot.go:1128-1146）

接口加 queueSize：
```go
EnsureTCPListener(service string, routeKey string, queueSize int)
EnsureUDPListener(service string, routeKey string, queueSize int)
```
`netSenderAdapter` 实现：
```go
func (ns *netSenderAdapter) EnsureTCPListener(service string, routeKey string, queueSize int) {
    conn := ns.robot.client.GetTCPConn(service)
    if conn == nil { ...保持现状 warn/return... }
    if err := conn.RegisterListen(routeKey, nil, queueSize); err != nil {
        stresslog.Error("[ROBOT] TCP 监听占位注册失败", zap.String("service", service), zap.String("routeKey", routeKey), zap.Error(err))
    }
}
```
（UDP 同理。）注意：`RegisterListen` 返回 error，EnsureTCPListener 当前是 void；冲突时记 Error 日志（Lua ensure 路径本就是占位注册，冲突说明配置异常，fail loud 由日志暴露；不向上 panic）。

编译期断言 `_ engine.NetSender = (*netSenderAdapter)(nil)`（robot.go:1213）保持。

### 5. Lua 绑定（api_network.go:988-1014）

`networkEnsureTCPListener`/`networkEnsureUDPListener` 的 Go 调用加 queueSize=1：
```go
ctx.NetSender.EnsureTCPListener(service, routeKey, 1)
ctx.NetSender.EnsureUDPListener(service, routeKey, 1)
```
- **Lua 签名不变**：`network.ensure_tcp_listener(service, response_key)` 仍 2 参（Lua 不暴露 queueSize；复杂容量只由 flow `listenRefs` 配置，符合轨道文档决策）。
- 注释补一句「queueSize 固定为 1，大容量请用 flow listenRefs 的 queueSize 配置」。

### 6. 调用点更新（全仓）

- `robot/robot.go:763` `conn.ListenResponse(listenMap)` → 删，改为分组内逐条 `conn.RegisterListen(routeKey, cb, q)`。
- `robot/robot.go:1135, 1145` `conn.AddListener(routeKey, nil)` → `conn.RegisterListen(routeKey, nil, queueSize)`（注意 EnsureTCPListener 现在收到 queueSize 形参，透传）。
- `network/connection_test.go:48,93,122,123,143`（2-A1 留下的 5 处）`conn.AddListener(routeKey, …)` → `conn.RegisterListen(routeKey, …, 1)`，处理新增的 error 返回（测试无冲突，err 应为 nil；`if err := …; err != nil { t.Fatal(err) }`）。
- **实现前先 `grep -rn "\.AddListener(\|\.ListenResponse(" 全仓**确认无遗漏调用点（尤其 `_test.go` 里的 NetSender fake/mock——grep 已知只有 `netSenderAdapter` 一个 NetSender 实现，但仍需确认无测试 mock）。

## 并发 / 不变量

- `RegisterListen` 在 `c.mu` 内完成 listenResp/listenQueues 的读-改 + 冲突判断，CAS 启动 loop 在 `c.mu` 外（沿用现状）。无锁序变化。
- queueSize=1 缺省时，预创建队列容量 1 ≡ 2-A1 按需建容量 1 队列，dispatchListen/GetListenResp 行为不变。
- 冲突检测覆盖「同连接同 routeKey 跨次注册」（含同批多条 listenRef 与跨节点注册）；现有配置无此情形，故零误伤。

## 关键约束

- **不写兼容兜底**：queueSize<=0 直接报错（不静默改 1）；`*int` 区分 unset 与显式 0。
- **新字段全链路一致**：QueueSize 从 schema → robot.RegisterListen → Connection.RegisterListen → 队列容量，一处不漏；不顺手在中间「默认」掉。
- **避免双 API**：删 AddListener/ListenResponse，只留 RegisterListen（单条）；robot 不再用批量 ListenResponse。
- Go 最佳实践：godoc、错误用 `fmt.Errorf` 带中文上下文（这是配置/注册错误，非 action 执行错误，不用 NewActionError——RegisterListen 在 ActionHandler 路径上返回 error 由 executor 处理；EnsureTCPListener 冲突用日志）、日志中文。
- 仅改 engine(flow.go,action.go) / network(connection.go) / robot(robot.go) / script(api_network.go) + 对应测试。**不动 conf/、admin/、agent/、cmd/**。
- **不要 git commit。**
- **Windows 环境注**：`gofmt -l` 会把所有工作树 .go 标为脏（autocrlf CRLF），**不要**对单文件 `gofmt -w`。校验内容 canonical 用 `sed 's/\r$//' f.go | gofmt -l`（空即 OK）。

## 工作方式（TDD）

1. **RED** — `network/connection_test.go` 追加（更新现有 5 处 AddListener→RegisterListen 后）：
   - `RegisterListen` 新注册：预创建队列容量 = queueSize（注册后 push 3 条到 queueSize=3 → GetListenResp 依次 pop 3 条 FIFO）。
   - queueSize=1 缺省等价（沿用 2-A1 等价用例，改走 RegisterListen 入口）。
   - **幂等**：同 routeKey 同 (cb-nil, queueSize) 再注册 → nil error，不重复建队列。
   - **冲突 fail-loud**：同 routeKey 不同 queueSize → error；同 routeKey 一 nil-cb 一非 nil-cb → error。
   - 启动 listenLoop（注册后 dispatchListen 缓存分支能 push 进预创建队列）。
2. **RED** — `robot/robot_test.go`（若无则新建最小用例，或用现有 robot 测试设施）：
   - `RegisterListen` 有效 queueSize：`QueueSize=nil`→1、`=3`→3、`=0`→error（中文，含 server+listen）、`=-1`→error。
   - （若 robot 包测试初始化成本高，可用一个轻量 helper 把「有效 queueSize 计算」抽成可单测的纯函数 `effectiveListenQueueSize(ref) (int, error)` 并单测之；主流程 RegisterListen 调它。推荐这样拆，便于 TDD 且职责清晰。）
3. **RED** — 接口同步：`netSenderAdapter` 实现新签名（编译期断言保证）；Lua 绑定传 1（无需 Lua 测试，编译过即可）。
4. **GREEN** — 实现 flow.go / connection.go / robot.go / action.go / api_network.go 改动 + 更新调用点。
5. `go build ./...`、`go vet ./...`、`go test ./network/... ./robot/... ./engine/... -count=1` 全绿。
6. 全仓 `grep -rn '\.AddListener(\|\.ListenResponse('` 确认零残留（除注释/历史 plan）。
7. **不要 git commit。**

## 验收（self-review）

- `ListenRef.QueueSize *int` 就位，godoc 说明 unset/显式语义。
- `Connection.AddListener`/`ListenResponse` 零残留；`RegisterListen(routeKey, cb, queueSize) error` 唯一注册入口；冲突 fail-loud（queueSize/模式不一致）、幂等（一致）、预创建队列容量=queueSize。
- `NetSender.EnsureTCPListener/EnsureUDPListener` 带 queueSize；`netSenderAdapter` 透传；Lua 传 1、签名不变。
- `robot.RegisterListen` 走有效 queueSize（<=0 报错）+ 逐条 RegisterListen + 错误聚合。
- 现有配置（queueSize 全缺省=1）行为零回退：network/robot/engine 既有测试全绿 + 新增用例全绿。
- 全链路一致：QueueSize 从 schema 到队列容量无断点。
- **未碰** script callback / ListenDef.script 校验 / conf/ / connectionPump（越界即错）。

## 报告

写完整报告到 `plans/declarative-codec/reports/t2-a2-1-report.md`：实现内容、QueueSize 全链路流转图（schema→robot→Connection→queue）、冲突/幂等语义、TDD RED/GREEN、调用点更新清单（含 connection_test.go）、effectiveListenQueueSize 单测、self-review、concerns（如 effective 校验在运行时注册点而非加载点的取舍）、改动文件。

返回（<15 行）：Status、改动文件、一行测试摘要、concerns、报告路径。
