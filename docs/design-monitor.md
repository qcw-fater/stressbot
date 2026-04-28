# 运行时监控系统 — 实施文档

> 本文档反映 monitor 包的实际实现状态，作为代码参考和前端对接依据。

## 1. 设计目标

- 继承旧 Robot 工具指标体系（per-action 成功率/延迟/字节 + 机器人状态 + CSV + 定时输出），修正其根本缺陷（窗口统计 → 全局累积）
- 所有计数器**纯原子操作**，无锁、无 channel、无外部依赖
- `enabled=false` 时所有方法为 no-op，**零开销**
- 不修改任何公开接口（`ActionHandler`、`NetSender`、`Executor`）
- 声明式动作和 Lua 脚本动作**统一自动采集**，用户无感知

### 1.1 与旧工具对比

| 指标 | 旧工具 | 新工具 |
|---|---|---|
| min/max/P50/P90/P95/P99 | 窗口值（最近 ~10240 样本） | **全局**（固定桶累积计数） |
| Avg | 窗口值 | **全局**（sum/count） |
| Apdex | 窗口值，`sample500Less` 命名有歧义 | **全局**，标准 Apdex(T) 公式，T 可配置 |
| 内存/action | ~80KB（10240×8B） | **~200B**（固定桶 + 原子计数器） |
| 锁/channel | channel 传批次 | **无锁**，纯原子操作 |
| 外部依赖 | `go-metrics` | **无** |
| 系统资源 | 无 | goroutines/mem/GC |
| 连接健康 | 无 | 建立/失败/断连计数 |
| 全局带宽 | 无 | 发送/接收 MB/s |
| 错误分布 | 无 | error message → count |
| 并发执行 | 无 | per-action executing 计数 |
| 输出 | 控制台 + CSV + WebSocket | 控制台 + CSV + HTTP JSON |

---

## 2. 核心设计

### 2.1 监控采集层：统一在 handler 层

**问题**：Lua 动作由 `robotActionHandler.executeLuaAction` 处理，声明式动作由 `ActionExecutor.Execute` 处理，两条路径。若把监控放在 `ActionExecutor`，Lua 动作会被遗漏。

**方案**：监控采集统一在 `robotActionHandler.ExecuteAction` 中完成（`robot/robot.go`），**不使用 `ActionExecutor` 回调**：

```
ExecuteAction(actionDef)
  ├── RecordActionStart(name)        // executing++
  ├── if lua:
  │     err = executeLuaAction()
  ├── else:
  │     sendBytes, recvBytes, err = ActionExecutor.Execute()
  ├── classifyResult(err)            // error → ActionResult
  ├── RecordAction(name, result, duration, bytes, errMsg)
  │     // executing--, 记录延迟/成功/失败/错误分布
  └── return err
```

`ActionExecutor.Execute` 只负责执行和返回字节数，不包含监控逻辑：

```go
func (ae *ActionExecutor) Execute(def *ActionDef) (sendBytes, recvBytes int, err error)
```

网络型 `exec*` 方法返回实际字节数，控制型（connect/close/clearState 等）由 `Execute` 返回 `(0, 0, err)`。

### 2.2 固定桶延迟直方图（`monitor/histogram.go`）

17 个预定义桶边界，覆盖 0ms ~ 60s+：

```
[0,1) [1,2) [2,5) [5,10) [10,20) [20,50) [50,100) [100,200) [200,500)
[500,1000) [1s,2s) [2s,5s) [5s,10s) [10s,30s) [30s,60s) [60s,+∞)
```

每次 `Record(duration)`：
1. `count.Add(1)`, `sumNs.Add(ns)` — 用于 avg
2. 原子 CAS 循环更新全局 min/max — **不丢失**
3. 遍历桶找归属区间，`buckets[i].Add(1)` — O(17)

百分位计算（`percentileFromBuckets`）：桶计数前缀和 + 线性插值，O(17)。

**全部操作无 mutex，纯原子操作**。每个 action ~136 字节（17 个 `atomic.Int64`）。

### 2.3 ActionResult 分类与指标归属

| 结果 | 含义 | 延迟直方图 | Apdex | QPS | 错误分布 |
|---|---|---|---|---|---|
| Success | 执行成功 | **记录** | satisfied 或 tolerating | 计入 | — |
| Failure | 执行失败（非超时） | 不记录 | 隐式 frustrated | 计入 | **记录 errMsg** |
| Timeout | 等待响应超时 | 不记录 | 隐式 frustrated | 计入 | — |
| Skipped | 必填字段为空跳过 | 不记录 | **不计入** | **不计入** | — |

- **Skipped 不计入样本数**：跳过 = 动作未执行，不代表服务器能力
- **延迟仅记录 Success**：失败/超时的耗时无意义
- **`latency.count` ≤ `sampleCount`**：前端需知延迟仅含成功请求

### 2.4 Apdex(T) 标准公式

```
Apdex = (satisfied + tolerating × 0.5) / total

其中 total = success + failure + timeout（不含 skipped）
  satisfied  = 成功且延迟 < T（默认 100ms）
  tolerating = 成功且 T ≤ 延迟 < 4T（100ms ~ 400ms）
  frustrated = 其余（失败 + 超时 + 成功但延迟 ≥ 4T）
```

**关键**：`float64(tolerating) * 0.5`，不是 `tolerating / 2`（Go 整数除法会丢精度）。

### 2.5 QPS 计算

- **avgQPS** = `totalSamples / uptimeSeconds` — 全程平均吞吐
- **periodQPS** = `(currentSamples - prevSamples) / periodSeconds` — 最近周期吞吐（仅 Reporter 维护 prevCounts）
- **total 含 failure + timeout**：QPS 衡量服务器承受的实际负载，排除失败会掩盖问题
- 前端可通过 `avgQPS × successRate` 得到成功 QPS

---

## 3. 指标体系总览

### 3.1 系统资源（`SystemSnapshot`）

| 字段 | 类型 | 说明 |
|---|---|---|
| `goroutines` | int | 当前 goroutine 数 |
| `memAllocMB` | float64 | 已分配内存 MB |
| `memSysMB` | float64 | 系统分配内存 MB |
| `gcCount` | uint32 | GC 执行次数 |

来源：`runtime.ReadMemStats`，在 `Snapshot()` 时采样。

### 3.2 机器人状态（`RobotSnapshot`）

| 字段 | 类型 | 说明 |
|---|---|---|
| `started` | int64 | 已启动总数 |
| `running` | int64 | 当前运行数 |
| `stopped` | int64 | 正常停止数 |
| `errored` | int64 | 异常退出数 |

生命周期：`Robot.Start()` 调用 `Started() + Running()`；正常退出调 `Stopped()`，异常退出调 `Errored()`。`Running()` 内部做 +1/-1 维护当前数。

### 3.3 连接健康（`ConnectionSnapshot`）

| 字段 | 类型 | 说明 |
|---|---|---|
| `established` | int64 | 连接建立成功总数（TCP + UDP） |
| `failed` | int64 | 连接建立失败总数 |
| `dropped` | int64 | 意外断连总数 |

采集点：`robot/robot.go` 的 `ConnectTCP`/`ConnectUDP` 中记录成功/失败；`SetOnDisconnect` 回调中记录断连。

### 3.4 全局带宽（`BandwidthSnapshot`）

| 字段 | 类型 | 说明 |
|---|---|---|
| `totalSendBytes` | int64 | 总发送字节数 |
| `totalRecvBytes` | int64 | 总接收字节数 |
| `sendMBps` | float64 | 平均发送带宽 MB/s |
| `recvMBps` | float64 | 平均接收带宽 MB/s |

在 `RecordAction` 中，成功的动作同步累加 `totalSendBytes/totalRecvBytes`。

### 3.5 Per-Action 指标（`ActionSnapshot`）

| 字段 | 类型 | 说明 |
|---|---|---|
| `name` | string | 动作名称（flow.json actions map 的 key） |
| `sampleCount` | int64 | 样本数（success + failure + timeout） |
| `successCount` | int64 | 成功次数 |
| `failureCount` | int64 | 失败次数 |
| `timeoutCount` | int64 | 超时次数 |
| `skippedCount` | int64 | 跳过次数（不计入 sampleCount） |
| `executing` | int64 | 当前正在执行的机器人数 |
| `successRate` | float64 | 成功率 [0, 1] |
| `avgSendBytes` | float64 | 平均发送字节（仅成功样本） |
| `avgRecvBytes` | float64 | 平均接收字节（仅成功样本） |
| `apdex` | float64 | Apdex 评分 [0, 1] |
| `latency` | HistogramSnapshot | 延迟分布（仅成功样本） |
| `avgQps` | float64 | 全程平均 QPS |
| `periodQps` | float64 | 最近周期 QPS |
| `errors` | []ErrorEntry | 错误分布（仅有失败时） |

### 3.6 延迟直方图（`HistogramSnapshot`）

| 字段 | 类型 | 说明 |
|---|---|---|
| `count` | int64 | 采样数（= successCount） |
| `minMs` | float64 | 全局最小延迟 ms |
| `maxMs` | float64 | 全局最大延迟 ms |
| `avgMs` | float64 | 全局平均延迟 ms |
| `p50Ms` | float64 | 全局 P50 ms |
| `p90Ms` | float64 | 全局 P90 ms |
| `p95Ms` | float64 | 全局 P95 ms |
| `p99Ms` | float64 | 全局 P99 ms |

### 3.7 错误分布（`ErrorEntry`）

| 字段 | 类型 | 说明 |
|---|---|---|
| `msg` | string | 错误消息（截断至 120 字符） |
| `count` | int64 | 出现次数 |

仅 failure 类型记录（timeout 不记录具体消息）。使用 `sync.Map` 存储 error → *atomic.Int64，无锁累加。

---

## 4. 文件结构

```
monitor/
  histogram.go   — LatencyHistogram（17 桶固定桶，纯原子操作）
  collector.go   — MetricsCollector 全局单例 + actionMetrics + ActionResult + 连接/带宽钩子
  snapshot.go    — SystemSnapshot/RobotSnapshot/ConnectionSnapshot/BandwidthSnapshot/ActionSnapshot + Snapshot()
  reporter.go    — 定时控制台报告（维护 prevCounts 计算 PeriodQPS）
  http.go        — /metrics JSON + /metrics/summary 文本（与 pprof 共用 DefaultServeMux）
  csv.go         — 关闭时 CSV 导出
```

修改的外部文件：

| 文件 | 改动 |
|---|---|
| `engine/flow.go` | `ActionDef` 新增 `Name string \`json:"-"\`` |
| `engine/action.go` | 新增 `ErrTimeout`；`Execute` 改为返回 `(sendBytes, recvBytes int, err error)`；网络型 `exec*` 改签名 |
| `robot/robot.go` | `ExecuteAction` 统一包裹 `RecordActionStart` + 计时 + `RecordAction`（含 Lua 分支）；`ConnectTCP`/`ConnectUDP` 记录连接事件；`Start`/`Close` 生命周期 |
| `cmd/stressbot/main.go` | monitor 配置解析、Name 回填、Reporter 启停、HTTP 注册、CSV 导出 |
| `conf/config.json` | 新增 `monitor` 配置段 |

不修改：`ActionHandler` 接口、`NetSender` 接口、`Executor`、`network/` 包、`state/` 包。

---

## 5. HTTP JSON 端点

### `GET /metrics`

完整 JSON 快照，供前端消费：

```json
{
  "timestamp": "2026-04-28T10:30:00+08:00",
  "uptime": 150500000000,
  "uptimeSeconds": 150.5,
  "totalActions": 52843,
  "apdexT": 100,
  "system": {
    "goroutines": 142,
    "memAllocMB": 12.3,
    "memSysMB": 45.6,
    "gcCount": 23
  },
  "robots": {
    "started": 100,
    "running": 98,
    "stopped": 0,
    "errored": 2
  },
  "connections": {
    "established": 300,
    "failed": 0,
    "dropped": 1
  },
  "bandwidth": {
    "totalSendBytes": 5242880,
    "totalRecvBytes": 15728640,
    "sendMBps": 0.5,
    "recvMBps": 1.2
  },
  "actions": [
    {
      "name": "CreateNormalTeam",
      "sampleCount": 284,
      "successCount": 280,
      "failureCount": 4,
      "timeoutCount": 0,
      "skippedCount": 0,
      "executing": 2,
      "successRate": 0.9859,
      "avgSendBytes": 45.2,
      "avgRecvBytes": 1230.5,
      "apdex": 0.95,
      "latency": {
        "count": 280,
        "minMs": 12.0,
        "maxMs": 450.6,
        "avgMs": 45.7,
        "p50Ms": 38.2,
        "p90Ms": 78.5,
        "p95Ms": 120.3,
        "p99Ms": 450.6
      },
      "avgQps": 1.89,
      "periodQps": 0,
      "errors": [
        { "msg": "TCP 发送失败: service=logic route=...", "count": 3 }
      ]
    }
  ]
}
```

**前端对接要点**：
- `timestamp` — ISO 8601，用于时间序列对齐
- `uptimeSeconds` — 数值类型，前端可自行格式化
- `successRate` — 0~1 小数，前端按需显示为百分比
- `latency` 嵌套对象 — 可独立渲染延迟图表
- `latency.count` ≤ `sampleCount` — 延迟仅含成功样本
- 所有数值字段保证非 null（零值兜底）
- `periodQps` 在 HTTP 端点为 0（前端轮询两次用 `timestamp` 差值自行计算）
- `errors` 仅在有失败时出现（`omitempty`）

### `GET /metrics/summary`

文本摘要，供快速检查。

---

## 6. 控制台输出

每 5s 输出一次（可配置 `reportInterval`）：

```
[MONITOR] 4m25s | goroutines=16 | mem=12.3MB | gc=5 | total=380
[MONITOR] robots: 1运行 0停止 0错误 | conn: 3建立 0失败 0断连 | 0.5/1.2 MB/s
[MONITOR] 动作                    成功 失败 超时    avg    p95 apdex exec   qps
[MONITOR] CreateNormalTeam        280    4    0    46ms   120ms  0.95    2  0.95
[MONITOR] SelectHero              278    2    0    23ms    52ms  0.97    0  0.93
[MONITOR] errors: CreateNormalTeam→TCP 发送失败(3)
```

精简设计：
- 第一行：运行时间 + 系统资源 + 总动作数
- 第二行：机器人状态 + 连接健康 + 带宽
- 表格：每动作一行（avg + p95 + apdex + executing + qps）
- errors：仅在有失败时追加一行，格式为 `动作→错误消息(次数)`

---

## 7. 配置

```json
"monitor": {
  "enabled": true,
  "reportInterval": "5s",
  "httpEnabled": true,
  "httpPort": 6060,
  "csvPath": "log/metrics.csv",
  "apdexT": 100
}
```

| 字段 | 默认值 | 说明 |
|---|---|---|
| `enabled` | `false` | 总开关 |
| `reportInterval` | `"5s"` | 控制台报告间隔 |
| `httpEnabled` | `false` | HTTP 指标端点 |
| `httpPort` | `6060` | 与 pprof 共用端口 |
| `csvPath` | `"log/metrics.csv"` | 关闭时 CSV 输出路径 |
| `apdexT` | `100` | Apdex T 值（毫秒） |

---

## 8. CSV 导出

关闭时写入 `log/metrics.csv`：

```
接口名,样本数,成功次数,超时次数,错误次数,跳过次数,成功率,平均响应(ms),最小响应(ms),最大响应(ms),P50(ms),P90(ms),P95(ms),P99(ms),Apdex,平均发送字节,平均接收字节,平均QPS,压测时长(s)
```

---

## 9. 设计说明

### 9.1 为什么选固定桶而非排序样本

旧工具维护 10240 个样本的排序窗口，超出后淘汰。2 小时压测后 P99 只反映最后 ~5 分钟。固定桶的 P99 代表整个生命周期的第 99 百分位。精度 ±20%（由桶边界决定），对压测完全可接受。

### 9.2 `minMs` 初始化

`atomic.Int64` 零值为 0，但 min 应为 MaxInt64。**必须**通过 `newLatencyHistogram()` 构造，`actionMetrics` 用指针字段 `latency *LatencyHistogram` 确保正确初始化。

### 9.3 Lua 动作的指标

Lua 动作在 `robotActionHandler.ExecuteAction` 中统一采集：延迟（整个脚本执行时间）、成功/失败、错误分布。字节统计为 0（Lua 内部网络调用无法在引擎层捕获）。executing 计数同样适用。

### 9.4 HTTP vs Reporter 的 periodQPS

HTTP 端点传 `prevCounts=nil`，`periodQps=0`。仅 Reporter 维护 `prevCounts` 计算控制台 periodQPS。前端可通过连续两次 `GET /metrics` 自行计算。

### 9.5 `sync.Map` 写模式

`actions sync.Map` 写入仅在首次看到某 action name 时（`LoadOrStore`），后续全是对已存指针的原子字段更新。`errors sync.Map` 同理，首次写入后只做原子累加。
