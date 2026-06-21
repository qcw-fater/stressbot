# T2-A2.1 报告 — QueueSize schema + 注册接线（Go，安全非破坏）

> 任务 brief：`plans/declarative-codec/briefs/t2-a2-1-brief.md`
> 工作目录：worktree 根 `D:\Gitee\stressbot\.claude\worktrees\declarative-codec`，分支 `worktree-declarative-codec`。
> **未 git commit / add**（遵 brief 硬约束）。

## Status

**DONE**。TDD RED→GREEN，`go build ./...` + `go vet ./...` 干净，`network` / `robot` / `engine` 三包测试全绿（network 22 测试函数、robot 1 测试函数 5 子用例、engine 既有测试）。全仓 `AddListener`/`ListenResponse` 源码零残留（仅历史 plans/docs 引用）。

## 改动文件清单

| 文件 | 类型 | 说明 |
|---|---|---|
| `engine/flow.go` | 改 | `ListenRef` 加 `QueueSize *int` 字段 + godoc（unset/显式/<=0 语义） |
| `engine/action.go` | 改 | `NetSender.EnsureTCPListener/EnsureUDPListener` 接口加 `queueSize int` 形参 |
| `network/connection.go` | 改 | 删 `AddListener`/`ListenResponse`；新增 `RegisterListen(routeKey, cb, queueSize) error`（预创建队列 + 冲突 fail-loud + 幂等 + CAS 启动 listenLoop）；删 `maps` import |
| `robot/robot.go` | 改 | 改造 `RegisterListen`（effective queueSize + 逐条注册 + 错误聚合）；新增纯函数 `effectiveListenQueueSize(ref)` + 常量 `defaultListenQueueSize=1`；`netSenderAdapter.EnsureTCP/UDPListener` 加 queueSize 透传 + 冲突记 Error 日志 |
| `script/api_network.go` | 改 | Lua 绑定 `networkEnsureTCP/UDPListener` 调用时传 `queueSize=1`（Lua 签名不变） |
| `network/connection_test.go` | 改 | 更新 5 处 `AddListener`→`RegisterListen(…, 1)`；新增 8 个 `RegisterListen` 专项用例 |
| `robot/register_listen_test.go` | 新增 | `TestEffectiveListenQueueSize`（5 子用例：nil→1 / 显式 1,3 / 显式 0 报错 / 负数报错） |

未碰 `conf/`、`admin/`、`agent/`、`cmd/`、`adapter/`、`monitor/`、前端。`createListenCallback` 的 Script 分支原样保留（T2-A2.2 处理）。

## QueueSize 全链路流转图

```
flow.json (schema)
  listenRefs[].queueSize  ──┐
                            │  *int（nil=未写，区分「未写」与「显式 0」）
                            ▼
engine.ListenRef.QueueSize  ── 解析 ──▶ robot.effectiveListenQueueSize(ref)
                                            │  nil → 1（默认，≡ 历史单槽）
                                            │  >0  → 取该值
                                            │  <=0 → 中文 error（fail loud，不 clamp）
                                            ▼
                                     int (有效 queueSize)
                                            │
                                            ▼
robotActionHandler.RegisterListen(refs)
  分组 (proto, service) → 收集 (routeKey, cb, queueSize) 三元组
  逐条调 conn.RegisterListen(routeKey, cb, queueSize)
                                            │
                                            ▼
network.Connection.RegisterListen(routeKey, cb, queueSize) error
  c.mu 下：冲突检测（queueSize/模式不一致 → error）/ 幂等（一致 → no-op）/ 新注册
  新注册：listenQueues[routeKey] = newListenQueue(queueSize)  ◀── 预创建队列容量=queueSize
  CAS 启动 listenLoop（未运行时）
                                            │
                                            ▼
                                     listenQueue{capacity: queueSize}
                                     dispatchListen → Push / GetListenResp → Pop（FIFO）

旁路（Lua 占位注册，不走 schema）:
  Lua ensure_tcp_listener(service, routeKey)   [签名不变，2 参]
    → api_network.go: EnsureTCPListener(service, routeKey, 1)  ◀── queueSize 固定 1
      → netSenderAdapter.EnsureTCPListener → conn.RegisterListen(routeKey, nil, 1)
```

**不变量**：queueSize=1（缺省）时，预创建队列容量 1 ≡ 2-A1「按需建容量 1 队列」，dispatchListen/GetListenResp 行为零回退。

## 冲突 / 幂等语义（`Connection.RegisterListen`）

`c.mu` 下读 `existingCb, hasCb := c.listenResp[routeKey]` 与 `existingQ, hasQ := c.listenQueues[routeKey]`：

| 已注册？ | queueSize | cb 模式 | 行为 |
|---|---|---|---|
| 否（`!hasCb && !hasQ`） | — | — | **新注册**：写 listenResp + 预创建队列（cap=queueSize），返回 nil |
| 是 | `existingQ.capacity == queueSize` | `(existingCb==nil) == (cb==nil)` | **幂等**：写回 cb（无副作用），不重建队列，返回 nil |
| 是 | `existingQ.capacity != queueSize` | — | **冲突**：返回中文 error（含双方 queueSize） |
| 是 | — | `(existingCb==nil) != (cb==nil)` | **冲突**：返回中文 error（含双方回调模式） |

冲突覆盖「同连接同 routeKey 跨次注册」：含同批多条 listenRef 与跨节点注册。**现有配置（19 条 listenRefs 全唯一）零误伤**——冲突检测只拦截未来配置错误。

**nil receiver / 已关闭**：返回中文 error（不 panic、不静默）。

## TDD RED / GREEN

### RED
- `network/connection_test.go`：先改 5 处 `AddListener`→`RegisterListen(…, 1)` + 追加 8 个 RegisterListen 用例。`go vet ./network/...` 报 `conn.RegisterListen undefined (type *Connection has no field or method RegisterListen)`，符合 RED。
- `robot/register_listen_test.go`：新建，引用 `engine.ListenRef.QueueSize`。`go vet ./robot/...` 报 `unknown field QueueSize in struct literal of type engine.ListenRef`，符合 RED。

### GREEN
- 实现 `flow.go`（QueueSize 字段）→ `connection.go`（RegisterListen）→ `robot.go`（effectiveListenQueueSize + RegisterListen 改造 + Ensure 透传）→ `action.go`（接口形参）→ `api_network.go`（Lua 传 1）。
- 全部测试转绿。

## 调用点更新清单

| 位置 | 旧 | 新 |
|---|---|---|
| `robot/robot.go` `RegisterListen` | `conn.ListenResponse(listenMap)` 批量 | 分组内逐条 `conn.RegisterListen(routeKey, cb, q)`（含错误聚合） |
| `robot/robot.go` `netSenderAdapter.EnsureTCPListener` | `conn.AddListener(routeKey, nil)` | `conn.RegisterListen(routeKey, nil, queueSize)`（queueSize 由形参传入） |
| `robot/robot.go` `netSenderAdapter.EnsureUDPListener` | 同上 | 同上 |
| `script/api_network.go` `networkEnsureTCPListener` | `EnsureTCPListener(service, routeKey)` | `EnsureTCPListener(service, routeKey, 1)` |
| `script/api_network.go` `networkEnsureUDPListener` | 同上 | 同上 |
| `network/connection_test.go:48` | `conn.AddListener(routeKey, nil)` | `conn.RegisterListen(routeKey, nil, 1)`（带 err 检查） |
| `network/connection_test.go:93` | `conn.AddListener(routeKey, cb)` | `conn.RegisterListen(routeKey, cb, 1)` |
| `network/connection_test.go:122,123` | 2× `conn.AddListener(…, nil)` | 2× `conn.RegisterListen(…, nil, 1)` |
| `network/connection_test.go:143` | `conn.AddListener("k", nil)` | `conn.RegisterListen("k", nil, 1)` |

全仓 `grep -rn '\.AddListener(\|\.ListenResponse('` 源码侧零残留；剩余命中均在 `plans/`（brief 自身、refactor-adapter-layer 历史文档）与 `docs/adapter-layer.md`，属历史文档引用，符合 brief「注释/历史 plan 除外」。

## `effectiveListenQueueSize` 单测（robot 包首个测试）

robot 包此前**零测试文件**（`Glob robot/*_test.go` 无命中）。Robot 初始化成本高（需 network/proto/lua 全栈），按 brief 建议把「有效 queueSize 计算」抽成纯函数 `effectiveListenQueueSize(ref engine.ListenRef) (int, error)`，单测覆盖：

| 子用例 | 输入 | 期望 |
|---|---|---|
| nil_缺省为1 | `QueueSize=nil` | `1, nil` |
| 显式3 | `QueueSize=&3` | `3, nil` |
| 显式1 | `QueueSize=&1` | `1, nil` |
| 显式0_报错 | `QueueSize=&0` | error，含 "queueSize" + server + listen 上下文 |
| 显式负数_报错 | `QueueSize=&-1` | error，含 "queueSize" + server + listen 上下文 |

## 验证

| 命令 | 结果 |
|---|---|
| `go build ./...` | 干净，无输出 |
| `go vet ./...` | 干净，无输出 |
| `go test ./network/... -count=1` | **ok stressbot/network**，22 测试函数全 PASS（14 connection + 8 listen_queue） |
| `go test ./robot/... -count=1` | **ok stressbot/robot**，`TestEffectiveListenQueueSize` 5 子用例全 PASS |
| `go test ./engine/... -count=1` | **ok stressbot/engine**，既有测试全 PASS |
| `sed 's/\r$//' f.go \| gofmt -l`（受改动文件） | ListenRef/QueueSize 区域 canonical（gofmt diff 不涉及）；FieldBind 预存对齐漂移见 concerns |
| 全仓 `grep AddListener\|ListenResponse` | 源码零残留，仅 plans/docs 历史引用 |
| `-race` | **未跑**：`CGO_ENABLED=0`（Windows 无 cgo），`-race requires cgo`。并发安全沿用 2-A1 结构性论证（per-queue mu + c.mu 仅护 map 键，Push/Pop 在 c.mu 释放后无锁序交叉）；RegisterListen 在 c.mu 内完成读-改+冲突判断，CAS 启动 loop 在 c.mu 外，无新增锁序 |

### RegisterListen 专项用例（8 个）

1. `TestConnection_RegisterListen_PrecreatesQueue` — queueSize=3 注册后 push 3 条 → FIFO pop A/B/C → 第 4 次 nil（验证预创建队列容量=queueSize）
2. `TestConnection_RegisterListen_DefaultQueueSizeEquivalent` — queueSize=1 经 RegisterListen 入口，与 2-A1 容量 1 等价语义一致（push 2 条取最新）
3. `TestConnection_RegisterListen_Idempotent` — 同 (nil-cb, q=3) 再注册 nil error，已缓存消息不丢
4. `TestConnection_RegisterListen_ConflictQueueSize` — q=3 后再注册 q=5 → error
5. `TestConnection_RegisterListen_ConflictMode` — nil-cb 后再注册非 nil-cb → error
6. `TestConnection_RegisterListen_ModeConsistentIdempotent` — 两个非 nil-cb + 同 q → 幂等 nil error
7. `TestConnection_RegisterListen_NilReceiver` — nil receiver → error
8. `TestConnection_RegisterListen_Closed` — 已关闭 → error

## Self-review

- [x] `ListenRef.QueueSize *int` 就位，godoc 说明 unset/显式/>0/<=0 语义；`*int` 区分「未写」与「显式 0」。
- [x] `Connection.AddListener`/`ListenResponse` 零残留（源码侧）；`RegisterListen(routeKey, cb, queueSize) error` 唯一注册入口；冲突 fail-loud（queueSize/模式不一致）、幂等（一致）、预创建队列容量=queueSize。
- [x] `NetSender.EnsureTCP/UDPListener` 带 queueSize；`netSenderAdapter` 透传 + 冲突记 Error 日志（不 panic）；Lua 传 1、签名 `ensure_tcp_listener(service, route_key)` 不变。
- [x] `robot.RegisterListen` 走 effective queueSize（<=0 中文报错）+ 逐条 RegisterListen + 错误聚合（含 proto/service/routeKey 上下文）。
- [x] 现有配置（queueSize 全缺省=1）行为零回退：network/robot/engine 既有测试全绿 + 新增用例全绿。
- [x] 全链路一致：QueueSize 从 schema → effectiveListenQueueSize → RegisterListen → newListenQueue 队列容量，无断点、无中间默认掉。
- [x] **未碰** script callback 分支 / ListenDef.script 校验 / conf/ / dispatchListen 调度模型 / connectionPump / ActionHandler.RegisterListen 签名 / executor.go:252 调用 / 前端。
- [x] 错误用 `fmt.Errorf` 带中文上下文（注册/配置错误，非 action 执行错误，不用 NewActionError）；日志中文沿用 stresslog。
- [x] 无兼容兜底：queueSize<=0 直接报错；`*int` 区分 unset/显式 0。
- [x] 避免双 API：删 AddListener/ListenResponse，只留 RegisterListen。
- [x] godoc 齐全（ListenRef.QueueSize、RegisterListen、effectiveListenQueueSize、EnsureTCP/UDPListener）。

## Concerns

1. **FieldBind 预存对齐漂移（非本次引入）**：`engine/flow.go` 的 `FieldBind` 结构体（209-210 行 `Values []any` / `Entries []MapEntryBind`）在 HEAD 版本即存在 gofmt 对齐不一致（这两个字段的 tag 对齐没跟上类型长度）。本次 `sed 's/\r$//' engine/flow.go | gofmt -l` 会因此把 flow.go 标为非 canonical，但 gofmt diff **不涉及 ListenRef/QueueSize**（已 grep 确认 `grep -c QueueSize` = 0）。按 brief Windows 环境注「不要对单文件 gofmt -w」（会因 CRLF 全树标脏），且修 FieldBind 超出 T2-A2.1 范围，故不顺手修。建议作为独立清理项处理（gofmt 全树一次性，需在 Linux/禁 autocrlf 环境做）。

2. **effective 校验在运行时注册点而非加载点**：brief 明确「engine 包没有后端 flow 校验函数，校验在前端编辑器」，故 `queueSize<=0` 报错落在 `robot.RegisterListen`（注册总入口，运行时）。权衡：配置错误在机器人首次注册监听时才暴露（而非启动加载时），但好处是 robot 持有 `h.robot.adp` 可算 routeKey、与现有 `parseServer`/回调解析同处一处，逻辑内聚。前端编辑器校验（QueueSize 字段的 UI 校验）归 T3。若未来要做后端加载期 schema 校验，可把 `effectiveListenQueueSize` 提到 engine 包复用。

3. **`defaultListenQueueSize` 在 network/robot 两包各定义一份**：robot 包需用此常量做缺省值，但 network 包的是未导出常量。为避免跨包依赖未导出符号（或反向导出 network 包内部常量），在 robot 包独立定义同名常量 `= 1`，注释标注「与 network.defaultListenQueueSize 保持一致」。两处若未来不同步会引入隐性不一致——但值固定为 1（语义锁死为「缺省等价单槽」），实际风险极低。

4. **`-race` 未跑**：本机 `CGO_ENABLED=0`（Windows 无 cgo），race detector 不可用。并发安全沿用 2-A1 结构性论证 + RegisterListen 在 c.mu 内完成 map 读-改、CAS 启动 loop 在 c.mu 外（与原 AddListener 同模式），无新增锁序交叉。CI 启用 cgo 后建议补跑 `go test -race ./network/... ./robot/...`。

## 下一步

T2-A2.1 完成，无遗留。可进入 **T2-A2.2**：配置迁移（flow.json 给高频 route 显式配 queueSize）+ 下线 `ListenDef.script` 回调分支 + 启用 `ListenDef.script` 禁用校验（破坏性，须原子提交）。本任务的 `RegisterListen` 冲突 fail-loud 已为 T2-A2.2 的配置正确性提供运行时保障。
