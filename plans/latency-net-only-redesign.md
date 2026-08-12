# RecordAction Duration 重新设计：剥离客户端开销，只衡量服务端处理 + 网络往返

> 状态：草案 v2 — 待实施
> 作者：基于 stressbot 当前代码（2026/05）梳理
> 范围：`monitor` / `engine` / `robot` / `network` / `script` / 前端 ActionsTab / 历史归档
> 关联背景：用户反馈"压测核心关注服务端处理能力，当前 duration 包含客户端 proto 构建/序列化/解析等开销，会污染 RTT 统计"

---

## 1. 现状梳理

### 1.1 当前实现位置

| 位置 | 行为 |
|---|---|
| `robot/robot.go:341-362` `robotActionHandler.ExecuteAction` | `start := time.Now()` 在最外层，调用 `actionExec.Execute` 或 `executeLuaAction` 后 `time.Since(start)` 作为 duration 传给 `monitor.RecordAction` |
| `engine/action.go:106-138` `ActionExecutor.Execute` | 按 pattern 分派；返回 `(sendBytes, recvBytes, err)`，**不返回任何 duration** |
| `engine/action.go:802-877` `execRequest` | 内部已经测量了 `start := time.Now()` 紧贴 `protocolRequest` 之前，但**只用于 debug 日志**，未上抛 |
| `network/connection.go:118-176` `Connection.RequestResponse` | 内部也测了 `elapsed`，仅 debug 日志 |
| `robot/robot.go:380-402` `executeLuaAction` | 直接调用 `luaPool.RunActionScript` 拿 `(code, send, recv, err)`，**完全没有 duration 概念** |

### 1.2 当前 duration 包含了什么

以最常用的 `tcpRequest` 为例，duration 的实际组成（按时间顺序）：

```
ExecuteAction 入口（t0）
 │
 ├─ buildBody                        ← 客户端 CPU：proto.Create + bindFields（随机/过滤/state 查询）+ Serialize
 ├─ ExpectedRouteKey                 ← 客户端 CPU：极轻
 ├─ protocolSecretKey                ← 客户端 CPU：极轻
 ├─ protocolEncode (EncodeTCP/UDP)   ← 客户端 CPU：可能含加密（Lua codec.lua → luaPool.adapter）
 ├─ protocolRequest                  ← ★ Connection.Send + 等响应（真正的 RTT 窗口）
 ├─ parseAndStoreResponse            ← 客户端 CPU：Factory.Parse + GetFieldMap + store.Set
 │
ExecuteAction 退出（t1）

duration = t1 - t0   ← 当前传给 monitor 的值
```

只有第 4 行（`protocolRequest`）是用户真正关心的"服务端处理 + 网络往返"。前后的 build / encode / parse / store 都是客户端开销，在 robot 数较多（CPU 紧张）或字段绑定复杂、proto 解析重的场景下会显著夸大延迟数字。

### 1.3 各 Pattern 的 RTT 含义对照

| Pattern | RTT 含义 | 客户端开销占比 |
|---|---|---|
| `tcpRequest` / `udpRequest` | `Connection.Send` 完成 → 收到匹配 routeKey 的 `*Message` | 中等：proto 构建/序列化/解析、加密编码 |
| `tcpSend` / `udpSend` | 无响应，**无 RTT 概念**，最多算 OS write 时间 | 几乎全部都是客户端开销 |
| `tcpListen` / `udpListen` | 开始等待 → 服务端推送帧到达（由队列事件唤醒） | 仅包含真实等待窗口 |
| `httpRequest` | `http.Client.Do` 全量耗时（含 body 读完） | 中等：URL/body marshal、resp body 读 + parse |
| `tcpConnect` / `udpConnect` | 建连时长（TCP 握手）；当前已被算进 wall-clock | 客户端调度可见 |
| `setState` / `clearState` / `tcpClose` / `udpClose` | **纯客户端**，无网络含义 | 100% |
| `lua` | Lua 脚本整体时长（含脚本里多次 `network.*_request` 调用） | 难以拆分，需在 lua API 内部累计 |

### 1.4 涉及到的下游

- `monitor.actionMetrics.latency` 延迟直方图：min/max/avg/p50/p90/p95/p99
- `monitor.actionMetrics.timeoutTotalMs`：超时样本平均延迟
- Apdex 评分（`apdexSatisfied` / `apdexTolerating`），默认 T=100ms
- `MergeSnapshots` 跨 Agent 合并（直方图桶 + Apdex 计数）
- CSV 导出 `monitor/csv.go`（成功率 / 平均/min/max/p50/p90/p95/p99 都依赖 latency）
- 控制台 reporter `monitor/reporter.go` 输出 `avg p95 apdex tout`
- HTTP JSON 输出 `monitor/http.go`
- 前端 `ActionsTab.tsx`：avg/p50/p95/p99/max/超均 列
- 前端 `LatencyHistogram.tsx`、`MetricsBadge.tsx`
- 历史归档（`admin/history.go` 中 task_action_history 等表）

---

## 2. 设计目标

1. **主指标 `latency` 含义变更为"纯网络往返时间"（Network RTT）**：从消息字节进入网卡到响应字节从网卡返回的窗口。
2. **客户端构建/解析开销作为辅助字段保留**：方便用户诊断"高 RTT 是服务端慢还是客户端慢"。
3. **特殊 pattern 平稳处理**：纯客户端动作（setState / clearState / connect / close 等）不污染 RTT 统计；listen 类动作按帧到达时刻记录。
4. **Lua 动作支持纯 RTT 累计**：脚本里多次 `tcp_request / udp_request / http_request / tcp_listen / udp_listen` 应分别累加 net time，上抛给 monitor。
5. **可观测性提升**：调试时可看到"端到端 wall-clock"和"纯 RTT"两个值的对比。

---

## 3. 整体方案

### 3.1 核心数据结构

#### 3.1.1 新增 `ActionTiming` 结构（engine 包）

```go
// engine/action.go
// ActionTiming 单次 action 执行的耗时拆解。
//   - NetLatency：纯网络往返时间。
//     ▸ request 类：Send 完成到收到响应的窗口
//     ▸ listen 类：开始等待到推送帧到达
//     ▸ http 类：http.Client.Do + body 读完的总时长
//     ▸ send-only / connect / close / setState / clearState：始终为 0
//   - SamplesNet：贡献到 NetLatency 的网络调用次数。
//     声明式动作恒为 1（request/listen 成功）或 0（send-only/state/listen 超时），
//     lua 动作可能 ≥1（脚本里多次 request 累加）。
//     0 表示该次执行没有真正进入"send→recv"窗口，不参与 latency 直方图与 Apdex 统计。
type ActionTiming struct {
    NetLatency time.Duration
    SamplesNet int
}
```

> 放在 engine 包：ActionExecutor.Execute 是它的产出，避免循环依赖。monitor 包不依赖 engine。

#### 3.1.2 `NetSender` 接口扩展

让 request/HTTP 调用同时返回 net latency。**不增加新方法**，而是把原有方法的返回值扩展，所有声明式调用点同步改造。

```go
// engine/action.go
type NetSender interface {
    // 旧：TCPRequest(...) (body []byte, headerErr uint64, err error)
    // 新：增加 netLatency 返回值
    TCPRequest(service string, packet []byte, routeKey string,
        timeout ...time.Duration) (body []byte, headerErr uint64, netLatency time.Duration, err error)

    UDPRequest(service string, packet []byte, routeKey string,
        timeout ...time.Duration) (body []byte, headerErr uint64, netLatency time.Duration, err error)

    // 旧：HTTPRequest(...) (statusCode int, respBody []byte, err error)
    // 新：
    HTTPRequest(url, method, contentType string, body []byte) (
        statusCode int, respBody []byte, netLatency time.Duration, err error)

    // 其余方法保持不变（不存在 RTT 概念）
}
```

#### 3.1.3 `ActionExecutor.Execute` 签名升级

```go
// engine/action.go
// 旧：func (ae *ActionExecutor) Execute(ctx, def) (sendBytes, recvBytes int, err error)
// 新：
func (ae *ActionExecutor) Execute(ctx context.Context, def *ActionDef) (
    sendBytes, recvBytes int, timing ActionTiming, err error)
```

各 case 的填充策略：

| pattern | NetLatency 来源 | SamplesNet |
|---|---|---|
| `tcpRequest` / `udpRequest` | NetSender 返回值 | 1（成功/失败/header 错均记，因为 send→recv 窗口真实发生过） |
| `httpRequest` | NetSender 返回值 | 1 |
| `tcpListen` / `udpListen` | execListen 内部 `time.Since(start)`（poll 颗粒） | 命中 1，超时 0 |
| `tcpSend` / `udpSend` | 0 | 0 |
| `tcpConnect` / `udpConnect` / `tcpClose` / `udpClose` | 0 | 0 |
| `setState` / `clearState` | 0 | 0 |

#### 3.1.4 `monitor.RecordAction` 签名变更

```go
// monitor/collector.go
// 旧：RecordAction(name, result, duration, sendBytes, recvBytes, err)
// 新：
func (c *MetricsCollector) RecordAction(
    name string,
    result ActionResult,
    netLatency time.Duration,    // 主指标：纯 RTT，进直方图、Apdex
    clientCost time.Duration,    // 辅助：客户端构建+解析耗时（wall-clock - netLatency）
    netSamples int,              // 本次贡献的网络调用次数（0 表示该次不计入 latency/Apdex）
    sendBytes, recvBytes int,
    err error,
)
```

调用方（`robotActionHandler.ExecuteAction`）的新逻辑：

```go
func (h *robotActionHandler) ExecuteAction(actionDef *engine.ActionDef) error {
    if mc := monitor.Global(); mc != nil && actionDef.Name != "" {
        mc.RecordActionStart(actionDef.Name)
    }

    start := time.Now()
    var sendBytes, recvBytes int
    var timing engine.ActionTiming
    var err error

    if actionDef.Pattern == engine.PatternLua {
        sendBytes, recvBytes, timing, err = h.executeLuaAction(actionDef)
    } else {
        sendBytes, recvBytes, timing, err = h.robot.actionExec.Execute(h.robot.ctx, actionDef)
    }

    wallClock := time.Since(start)
    clientCost := wallClock - timing.NetLatency
    if clientCost < 0 {
        // 极端情况（系统时钟跳变 / 内部测量边界差异）防御性归零
        clientCost = 0
    }

    if mc := monitor.Global(); mc != nil && actionDef.Name != "" {
        result := classifyResult(err)
        mc.RecordAction(actionDef.Name, result,
            timing.NetLatency, clientCost, timing.SamplesNet,
            sendBytes, recvBytes, err)
    }

    return err
}
```

### 3.2 actionMetrics 内部存储

```go
// monitor/collector.go
type actionMetrics struct {
    // ... 现有字段保留 ...
    latency         *LatencyHistogram  // 含义改为：纯 RTT 直方图（仅成功且 netSamples > 0）

    clientCostSum   atomic.Int64       // 新增：客户端开销累计（纳秒）
    clientCostCount atomic.Int64       // 新增：贡献 clientCost 的样本数
    netSampleCount  atomic.Int64       // 新增：netSamples > 0 的样本数
}
```

ActionSnapshot 输出：

```go
type ActionSnapshot struct {
    // ... 现有字段全部保留 ...
    Latency        HistogramSnapshot  // 含义：纯 RTT
    ClientAvgMs    float64            // 新增：客户端构建+解析 avg
    NetSampleCount int64              // 新增：进入直方图的样本数
}
```

`MergeSnapshots` 合并规则：
- `Latency` 直方图：原算法不变
- `ClientAvgMs`：按 SampleCount 加权平均，与 `TimeoutAvgMs` 同款规则
- `NetSampleCount`：直接求和

### 3.3 各层改造细节

#### 3.3.1 network 层（network/connection.go）

```go
// RequestResponse 内部已经测过 elapsed（仅日志），现把它通过返回值上抛。
func (c *Connection) RequestResponse(
    sendData []byte, routeKey string, timeoutOverride ...time.Duration,
) (*Message, time.Duration, error) {
    // ... 原有逻辑 ...
    netStart := time.Now()
    n, sendErr := c.Send(sendData)
    if sendErr != nil {
        return nil, 0, sendErr
    }
    // ...
    select {
    case resp := <-ch:
        elapsed := time.Since(netStart)
        return resp, elapsed, nil
    case <-timeoutTimer:
        elapsed := time.Since(netStart)
        return nil, elapsed, engine.NewTimeoutError(...)
    case <-c.ctx.Done():
        elapsed := time.Since(netStart)
        return nil, elapsed, engine.NewActionError(errcode.ErrConnDropped, ...)
    }
}
```

> netStart 紧贴 Send 之前，elapsed 在收到响应那一刻测量。对所有结果分支（成功 / 超时 / ctx cancel）都返回 elapsed，由上层根据 err 决定是否使用。

#### 3.3.2 engine 层（engine/action.go）

`execRequest` 改为：

```go
func (ae *ActionExecutor) execRequest(protocol string, def *ActionDef) (
    sendBytes, recvBytes int, timing ActionTiming, err error,
) {
    // ... build / encode 客户端工作 ...

    respBody, headerErr, netLatency, reqErr := ae.protocolRequest(...)
    timing.NetLatency = netLatency
    timing.SamplesNet = 1  // send→recv 窗口真实发生过，无论结果如何

    if reqErr != nil { return len(packet), 0, timing, reqErr }
    if headerErr != 0 { ... return ..., timing, handleHeaderError(...) }

    // ... 客户端解析 ...
    return len(packet), len(respBody), timing, nil
}
```

`execListen` 改为：

```go
func (ae *ActionExecutor) execListen(ctx context.Context, protocol string, def *ActionDef) (
    recvBytes int, timing ActionTiming, err error,
) {
    // ... 注册 listener ...
    start := time.Now()
    exchange, err := netSender.TCPListen(ctx, service, routeKey, timeout)
    if err != nil || exchange == nil {
        // 超时分支：SamplesNet=0，netLatency 不上抛
        return 0, ActionTiming{}, NewTimeoutError(...)
    }
    timing.NetLatency = exchange.RecvFrameAt.Sub(start)
    timing.SamplesNet = 1
    return exchange.RecvWireBytes, timing, nil
}
```

`execSend` / `execHTTPRequest` 同理改造，send 类 timing 留空，HTTP 从 NetSender 返回值拿。

#### 3.3.3 robot 层（robot/robot.go netSenderAdapter）

```go
func (ns *netSenderAdapter) TCPRequest(...) ([]byte, uint64, time.Duration, error) {
    conn := ns.robot.client.GetTCPConn(service)
    if conn == nil { return nil, 0, 0, ... }
    resp, netLatency, err := conn.RequestResponse(packet, routeKey, timeout...)
    if err != nil { return nil, 0, netLatency, err }
    return resp.Data, resp.HeaderErr, netLatency, nil
}

func (ns *netSenderAdapter) HTTPRequest(...) (int, []byte, time.Duration, error) {
    // ...
    netStart := time.Now()
    resp, err := ns.robot.httpClient.Do(req)
    if err != nil {
        return 0, nil, time.Since(netStart), ...
    }
    defer resp.Body.Close()
    respBody, err := io.ReadAll(resp.Body)
    netLatency := time.Since(netStart)  // 含 body 读完
    if err != nil {
        return resp.StatusCode, nil, netLatency, ...
    }
    return resp.StatusCode, respBody, netLatency, nil
}
```

#### 3.3.4 lua 层（script/api_network.go + script/runtime.go）

**问题**：lua 脚本内可能多次调用 `network.tcp_request` / `udp_request` / `http_request` / `tcp_listen` / `udp_listen`，且这些 API 是单独 lua 调用，无法直接把 netLatency 累加到外层的 RunActionScript。

**方案**：在 `script.Context` 上加累加器，每次 lua 网络调用前后取时间差，累加到上下文：

```go
// script/runtime.go
type Context struct {
    // ... 原有字段 ...
    NetLatencyNs atomic.Int64  // 本次 RunActionScript 累计的纯网络耗时
    NetSamples   atomic.Int32  // 本次 RunActionScript 累计的网络调用次数
}
```

**lua API 累加规则**：

| Lua API | 累加规则 |
|---|---|
| `network.tcp_request` / `udp_request` / `http_request` | 调用即 `NetSamples++` + 累计 NetSender 返回的 netLatency |
| `network.tcp_listen` / `udp_listen` | **成功命中**才 `NetSamples++` + 累计 `time.Since(start)`；超时不累 |
| `network.tcp_send` / `udp_send` / `connect_*` / `close_*` / `set_*_secret_key` / `register_*_heartbeat` / `ensure_*_listener` | **不**累加 |
| `utils.sleep` | **不**累加（脚本主动等待是客户端行为） |

示例改造（`networkTCPRequest`）：

```go
func networkTCPRequest(L *lua.LState) int {
    // ... 准备工作 ...
    var respBody []byte
    var headerErr uint64
    var netLatency time.Duration
    var reqErr error
    withReleasedMu(ctx.LuaMu, func() {
        respBody, headerErr, netLatency, reqErr = ctx.NetSender.TCPRequest(
            service, packet, routeKey, time.Duration(timeout)*time.Second)
    })
    ctx.NetLatencyNs.Add(int64(netLatency))
    ctx.NetSamples.Add(1)  // 任何结果都计 1（send→recv 窗口已发生）
    // ... 后续返回值处理 ...
}
```

示例改造（`networkListen`）：

```go
func networkListen(L *lua.LState, protocol string) int {
    // ... 准备工作 ...
    var respBody []byte
    var timedOut bool
    var headerErr uint64
    listenStart := time.Now()
    withReleasedMu(ctx.LuaMu, func() {
        // ... 现有 poll 循环 ...
    })
    if !timedOut && respBody != nil {
        ctx.NetLatencyNs.Add(int64(time.Since(listenStart)))
        ctx.NetSamples.Add(1)
    }
    // 超时分支不累加
    // ...
}
```

`RunActionScript` 入口处清零累加器，返回时读取：

```go
func (rp *RuntimePool) RunActionScript(L *lua.LState, scriptName string) (
    code, send, recv int, timing engine.ActionTiming, err error,
) {
    ctx := GetContext(L)
    if ctx != nil {
        ctx.NetLatencyNs.Store(0)
        ctx.NetSamples.Store(0)
    }

    // ... 原有执行逻辑 ...

    if ctx != nil {
        timing.NetLatency = time.Duration(ctx.NetLatencyNs.Load())
        timing.SamplesNet = int(ctx.NetSamples.Load())
    }
    return
}
```

`executeLuaAction` 适配：

```go
func (h *robotActionHandler) executeLuaAction(actionDef *engine.ActionDef) (
    int, int, engine.ActionTiming, error,
) {
    // ...
    code, send, recv, timing, err := h.robot.luaPool.RunActionScript(h.robot.l, actionDef.Script)
    // ...
    return send, recv, timing, nil
}
```

### 3.4 处理 SamplesNet = 0 的特殊样本

真实场景：`conf/scripts/connect_battle_udp.lua` 这类 action 脚本只做 `connect_udp` / `set_secret_key` / `register_heartbeat`，没有任何 send→recv 窗口。设计上：

- **`successCount++`**：仍记为一次成功执行（用户需要在 ActionsTab 看到它跑过）
- **不进 latency 直方图**：避免 ~几 ms 的客户端 wall-clock 污染 P95
- **不进 Apdex 计数**：Apdex 的物理意义是"服务端响应是否令人满意"，纯客户端动作不参与
- **clientCost 仍累加**：用户能在 ClientAvgMs 列看到这类 action 的真实耗时

前端 `ActionsTab.tsx` 展示规则：

```
case netSampleCount == 0:
    avg / p50 / p95 / p99 / max / 超均 列显示 "—"
    Apdex 列显示 "—"
    successCount 正常显示
    clientAvgMs 列正常显示（这是该 action 真正有意义的耗时指标）

case netSampleCount == successCount  (绝大多数声明式 request / 单一 request 的 lua):
    latency 列正常显示，无 tooltip

case 0 < netSampleCount < successCount  (lua 内部分支只有部分调用网络):
    latency 列正常显示
    + tooltip "基于 N 次有效网络样本（共 M 次成功）"
```

### 3.5 前端改造

- `cmd/web/src/types/api.ts`：`ActionSnapshot` 新增 `clientAvgMs?: number` 与 `netSampleCount?: number`。
- `cmd/web/src/components/monitoring/tabs/ActionsTab.tsx`：
  - 在现有 `avg/p50/p95/p99/max/超均` 列**后面**新增"客户端开销 avg(ms)"列
  - 按 §3.4 规则处理 `netSampleCount == 0` 的展示
- `cmd/web/src/components/monitoring/shared/LatencyHistogram.tsx`：标题文案改为"网络往返延迟分布"。
- `cmd/web/src/components/FlowEditor/nodes/shared/MetricsBadge.tsx`：badge 上的 avg/p95 含义注释更新。
- 历史归档详情页（`HistoryDetailView`、`ReportHtml`）：同步显示 clientAvgMs；图表与对比视图沿用 latency 字段（自动适配）。
- UI 文案：前端术语不暴露技术细节，仍用"平均响应"等用户友好词，但在 tooltip 中明确"仅服务端处理 + 网络往返"。

### 3.6 历史归档

`admin/history.go` 与 `admin/history_schema.go`：

- `task_action_history` 表（或同名）若已有 latency_avg / latency_p95 / latency_p99 列，**含义自然变更**为纯 RTT，无需 schema 迁移。
- 新增列：
  ```sql
  ALTER TABLE task_action_history ADD COLUMN client_avg_ms FLOAT NULL;
  ALTER TABLE task_action_history ADD COLUMN net_sample_count BIGINT NULL;
  ```
  按项目"逻辑外键、不用 FOREIGN KEY"约定，不影响其他表。
- 归档时把 ActionSnapshot 的两个新字段写入。

---

## 4. 影响面汇总

| 文件 | 改动类型 | 关键改动 |
|---|---|---|
| `engine/action.go` | 中等 | NetSender 接口签名扩展；Execute 返回 ActionTiming；execRequest/execListen/execHTTPRequest/execSend 等返回签名调整 |
| `engine/executor.go` | 微小 | ActionHandler 接口不变（timing 留在 robot 层内部消化） |
| `network/connection.go` | 小 | RequestResponse 增加 netLatency 返回值 |
| `network/client.go` | 微小 | 仅入参/出参变动（如调用 RequestResponse 处） |
| `robot/robot.go` | 中等 | netSenderAdapter 3 个 Request 方法签名变更；ExecuteAction 测算 clientCost；executeLuaAction 返回 timing |
| `script/runtime.go` | 中等 | Context 加 NetLatencyNs/NetSamples；RunActionScript 返回 timing |
| `script/api_network.go` | 中等 | tcp_request/udp_request/http_request/tcp_listen/udp_listen 按 §3.3.4 规则累加 |
| `monitor/collector.go` | 中等 | RecordAction 签名变更；actionMetrics 加 clientCost/netSampleCount 字段 |
| `monitor/snapshot.go` | 小 | ActionSnapshot 加 ClientAvgMs/NetSampleCount；MergeSnapshots 合并新字段 |
| `monitor/csv.go` | 微小 | CSV 表头加"客户端开销(ms)"列 |
| `monitor/reporter.go` | 微小 | console 输出可加 `cli=XXms`（可选） |
| `cmd/web/src/types/api.ts` | 微小 | 类型定义补字段 |
| `cmd/web/src/components/monitoring/tabs/ActionsTab.tsx` | 小 | 新增一列 + netSampleCount 显示规则 + tooltip |
| `cmd/web/src/components/monitoring/shared/LatencyHistogram.tsx` | 微小 | 标题文案 |
| `cmd/web/src/components/monitoring/MonitorDock.tsx` | 微小 | tooltip / 文案 |
| `admin/history.go` | 小 | 归档新增列写入 |
| `admin/history_schema.go` | 小 | 加 client_avg_ms / net_sample_count 列 |
| `docs/monitoring-system.md` | 小 | 重写 §10 RecordAction 部分；新增 latency 含义说明 |
| `docs/admin-implementation.md` | 微小 | 归档表 schema 描述更新 |

---

## 5. 性能影响

- 每次 request 增加 2 次 `time.Now()`（在 network 层已有，仅返回值多一个 time.Duration），开销 < 100ns 级。
- monitor.actionMetrics 新增 3 个 atomic 字段（clientCostSum/clientCostCount/netSampleCount），每次 RecordAction 多 3 次原子 Add，总开销 < 50ns。
- script.Context 新增 2 个 atomic 字段，每次 lua 网络调用多 2 次原子 Add。
- 整体增加开销远低于 µs 量级，对压测主路径无可感知影响。

---

## 6. 验证计划

1. **单元测试**：
   - 新增 `monitor/collector_test.go`：RecordAction 传入 netLatency + clientCost，验证 actionMetrics 字段正确累加；netSamples=0 时 latency 直方图不变；MergeSnapshots 合并 clientAvgMs 加权平均。
   - 新增 `engine/action_test.go`（如不存在）：mock NetSender 让 TCPRequest 返回固定 netLatency，验证 ActionExecutor.Execute 把 timing 透传出去；execListen 命中/超时 SamplesNet 行为正确。
   - 新增 `script/runtime_test.go`：fake NetSender + lua 脚本里两次 tcp_request，验证累加；纯本地 lua 脚本 SamplesNet=0。

2. **集成验证**：
   - 单机模式跑 5 分钟，比对 ActionsTab 的 latency 与 clientAvgMs：理论上 latency < 旧版 duration，差值 ≈ clientAvgMs。
   - 对比同一 action 在不同 robot 数下：robot 多时 clientAvgMs 应明显上升（CPU 抢占增加），latency 应基本稳定（仅服务端 + 网络）。
   - 验证 `connect_battle_udp` 这类 action 在 ActionsTab 中 latency 列显示 `—`、clientAvgMs 列有数字。

3. **历史对比**：
   - 同一任务在旧版本跑一次、新版本跑一次，归档后用前端"历史对比"功能对照 latency 列：新版应明显低于旧版（取决于客户端开销）。

---

## 7. 开放问题与决策

| 问题 | 决策 |
|---|---|
| **Q1** listen 类动作超时是否算 timeoutAvg？ | **算**。execListen 超时仍走 ResultTimeout，进 timeoutTotalMs。docs 说明 "listen 超时是配置驱动而非服务端能力反映，调优时区分对待"。 |
| **Q2** HTTP 的 netLatency 是否包含 `io.ReadAll(resp.Body)`？ | **包含**。对 HTTP 来说"body 读完"才是 RTT 真正结束，与 TCP request 中"完整响应消息收齐"语义一致。 |
| **Q3** lua/声明式动作 SamplesNet=0 时是否参与 latency 直方图与 Apdex？ | **不参与**。详见 §3.4，前端展示规则同步给出。real-case：`connect_battle_udp.lua` 这种纯客户端 lua action。 |
| **Q4** 错误样本（ResultFailure）是否记 netLatency？ | **不记**。failureCount 不进 latency 直方图，保持现状语义。 |
| **Q5** 是否对 Apdex T 默认值做提示？ | **保持默认 100ms**，前端 ApdexT 输入框 tooltip 说明"统计的是纯网络往返时间，建议根据被压服务的目标 SLA（如 50/100/200ms）设置"。 |

---

## 8. 实施步骤（推荐顺序）

每个 Step 完成都做 `go build ./...` + `npx tsc -b` + `npm run test`，保证局部可验证。

### Step 1：基础设施
- 在 `engine/action.go` 新增 `ActionTiming` 类型
- 在 `network/connection.go` 改 `RequestResponse` 返回 netLatency
- 在 `monitor/collector.go` 改 `RecordAction` 签名 + actionMetrics 加字段
- 在 `monitor/snapshot.go` ActionSnapshot 加字段
- **验收点**：`go build ./...` 通过

### Step 2：声明式链路
- 改 `NetSender` 接口 + `netSenderAdapter` 实现
- 改 `ActionExecutor.Execute` / `execRequest` / `execListen` / `execHTTPRequest` / `execSend` 等
- 改 `robotActionHandler.ExecuteAction` 调用 `RecordAction` 的方式
- **验收点**：单机跑 1 分钟，控制台 reporter 看到 avg 数字下降；通过 HTTP JSON 端点确认 clientAvgMs 字段存在

### Step 3：Lua 链路
- 改 `script.Context` 加 `NetLatencyNs / NetSamples`
- 改 `script/api_network.go` tcp_request/udp_request/http_request/tcp_listen/udp_listen 按 §3.3.4 累加
- 改 `RunActionScript` 返回 timing
- 改 `executeLuaAction` 把 timing 透传
- **验收点**：lua 脚本驱动的 action 的 latency 数字也变化；`connect_battle_udp` 这种 action SamplesNet 始终为 0

### Step 4：snapshot + merge
- 改 `MergeSnapshots` 合并 clientAvgMs / netSampleCount
- 改 CSV 表头 / console reporter
- **验收点**：跨 Agent 模式下 Admin 聚合数字一致；CSV 导出新列

### Step 5：前端
- 改 `types/api.ts`
- 改 `ActionsTab.tsx` 加列 + netSampleCount=0 的 "—" 规则
- 改 `LatencyHistogram.tsx` 标题
- 改 `ReportHtml` / `HistoryDetailView` 显示新字段
- **验收点**：`npx tsc -b` 通过 + `npm run test` 通过；前端 ActionsTab 看到新列

### Step 6：历史归档
- 改 `admin/history_schema.go` 加列
- 改 `admin/history.go` 写入新字段
- **验收点**：跑完任务后 MySQL 中能查到新列数据

### Step 7：文档
- 更新 `docs/monitoring-system.md` §10 RecordAction
- 更新 `CLAUDE.md` 中"指标采集"段
- **验收点**：文档与代码一致

### Step 8：端到端验证
- 按 §6 验证计划逐项跑

---

## 9. 风险评估

| 风险 | 等级 | 缓解 |
|---|---|---|
| listen 队列中已有消息时等待时长不可测 | 中 | 单独记录 ready 次数，不生成 0ms 延迟样本 |
| HTTP keepalive 复用让首请求 netLatency 偏大（TCP 握手 + TLS） | 中 | 文档提示，建议预热 |
| Apdex T 用户已校准过的值在新口径下偏严或偏松 | 中 | 文档说明 + ApdexT 可运行期调整（SetApdexT 已支持） |
| 前端用户对 "—" 展示不理解 | 低 | tooltip 明确说明"此动作不参与网络往返度量"，并指向 clientAvgMs 列 |

---

## 附录 A：核心代码片段对照

### A.1 robotActionHandler.ExecuteAction（before vs after）

**Before**：
```go
func (h *robotActionHandler) ExecuteAction(actionDef *engine.ActionDef) error {
    if mc := monitor.Global(); mc != nil && actionDef.Name != "" {
        mc.RecordActionStart(actionDef.Name)
    }
    start := time.Now()
    var sendBytes, recvBytes int
    var err error
    if actionDef.Pattern == engine.PatternLua {
        sendBytes, recvBytes, err = h.executeLuaAction(actionDef)
    } else {
        sendBytes, recvBytes, err = h.robot.actionExec.Execute(h.robot.ctx, actionDef)
    }
    if mc := monitor.Global(); mc != nil && actionDef.Name != "" {
        result := classifyResult(err)
        mc.RecordAction(actionDef.Name, result, time.Since(start), sendBytes, recvBytes, err)
    }
    return err
}
```

**After**：
```go
func (h *robotActionHandler) ExecuteAction(actionDef *engine.ActionDef) error {
    if mc := monitor.Global(); mc != nil && actionDef.Name != "" {
        mc.RecordActionStart(actionDef.Name)
    }
    start := time.Now()
    var sendBytes, recvBytes int
    var timing engine.ActionTiming
    var err error
    if actionDef.Pattern == engine.PatternLua {
        sendBytes, recvBytes, timing, err = h.executeLuaAction(actionDef)
    } else {
        sendBytes, recvBytes, timing, err = h.robot.actionExec.Execute(h.robot.ctx, actionDef)
    }
    wallClock := time.Since(start)
    clientCost := wallClock - timing.NetLatency
    if clientCost < 0 { clientCost = 0 }
    if mc := monitor.Global(); mc != nil && actionDef.Name != "" {
        result := classifyResult(err)
        mc.RecordAction(actionDef.Name, result,
            timing.NetLatency, clientCost, timing.SamplesNet,
            sendBytes, recvBytes, err)
    }
    return err
}
```

### A.2 monitor.RecordAction 内部分支（after）

```go
func (c *MetricsCollector) RecordAction(
    name string, result ActionResult,
    netLatency, clientCost time.Duration, netSamples int,
    sendBytes, recvBytes int, err error,
) {
    if !c.enabled { return }
    c.totalActions.Add(1)
    am := c.getOrCreateAction(name)
    am.executing.Add(-1)

    // 客户端开销：所有结果分支都累计（无论成功/失败）
    if clientCost > 0 {
        am.clientCostSum.Add(clientCost.Nanoseconds())
        am.clientCostCount.Add(1)
    }

    switch result {
    case ResultSuccess:
        am.successCount.Add(1)
        // 仅当本次确实有网络调用时才进直方图与 Apdex
        if netSamples > 0 {
            am.latency.Record(netLatency)
            am.netSampleCount.Add(1)
            ms := netLatency.Milliseconds()
            c.cfgMu.RLock()
            T := int64(c.cfg.ApdexT)
            c.cfgMu.RUnlock()
            switch {
            case ms < T:    am.apdexSatisfied.Add(1)
            case ms < 4*T:  am.apdexTolerating.Add(1)
            }
        }
        if sendBytes > 0 { am.sendBytes.Add(int64(sendBytes)) }
        if recvBytes > 0 { am.recvBytes.Add(int64(recvBytes)) }
    case ResultFailure:
        am.failureCount.Add(1)
        if err != nil { c.recordError(am, err) }
    case ResultTimeout:
        am.timeoutCount.Add(1)
        am.timeoutTotalMs.Add(netLatency.Milliseconds()) // 仍记 netLatency
    case ResultCanceled:
        am.canceledCount.Add(1)
    }
}
```

---

**评审完成后**，按 §8 的 Step 1 ~ Step 8 顺序实施。
