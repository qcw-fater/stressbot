# 运行时监控系统 — 技术文档

> 本文档基于 `plans/design-monitor.md` 设计方案和 `plans/design-error-codes.md` 中监控相关章节，对照实际代码（`monitor/collector.go`、`monitor/snapshot.go`、`monitor/histogram.go`、`monitor/reporter.go`、`monitor/http.go`、`monitor/csv.go`、`robot/robot.go`）编写。反映已实施状态。

---

## 1. 概述

`monitor` 包提供零锁热路径的指标采集系统。核心特性：

- **所有计数器使用原子操作**，无 mutex/channel/外部依赖
- **`enabled=false` 时所有公共方法为空操作**，零性能开销
- **延迟使用固定桶直方图**（16 桶，覆盖 0ms ~ 60s+），全局累积
- **错误按 `(Kind, Code)` 元组聚合**，环形缓冲保留最近 3 条 Detail
- **RTT Apdex(T) 评分**，T 可配置（默认 100ms），按 RTT 样本数计算
- **支持分布式聚合**，MergeSnapshots 合并多 Agent 指标
- **声明式动作和 Lua 脚本动作统一自动采集**，用户无感知

### 1.1 与旧工具对比

| 指标 | 旧工具 | stressbot |
|------|--------|-----------|
| min/max/P50/P90/P95/P99 | 窗口值（最近 ~10240 样本） | **全局**（固定桶累积计数） |
| Avg | 窗口值 | **全局**（sum/count） |
| Apdex | 窗口值，`sample500Less` 命名有歧义 | **全局 RTT Apdex(T)**，T 可配置，分母为 RTT 样本数 |
| 内存/action | ~80KB（10240 x 8B） | **~200B**（固定桶 + 原子计数器） |
| 锁/channel | channel 传批次 | **无锁**，纯原子操作 |
| 外部依赖 | `go-metrics` | **无** |
| 系统资源 | 无 | goroutines/mem/GC |
| 连接健康 | 无 | 建立/失败/断连计数 |
| 全局带宽 | 无 | 发送/接收 MB/s |
| 错误分布 | 无 | 按 (Kind, Code) 聚合 + 环形缓冲 Detail |
| 取消统计 | 无 | canceledCount（ctx 取消不计入失败率） |
| 回调监控 | 无 | callback 成功/失败/错误聚合 |
| 并发执行 | 无 | per-action executing 计数 |
| 输出 | 控制台 + CSV + WebSocket | 控制台 + CSV + HTTP JSON |
| 分布式聚合 | 无 | MergeSnapshots 合并多 Agent |

---

## 2. 设计目标

1. 继承旧工具指标体系（per-action 成功率/延迟/字节 + 机器人状态 + CSV + 定时输出），修正根本缺陷（窗口统计 -> 全局累积）
2. 所有计数器纯原子操作，无锁、无 channel、无外部依赖
3. `enabled=false` 时所有方法为 no-op，零开销
4. 不修改任何公开接口（`ActionHandler`、`NetSender`、`Executor`）
5. 声明式动作和 Lua 脚本动作统一自动采集，用户无感知
6. 错误按结构化 `(Kind, Code)` 聚合，替代自由格式字符串爆炸
7. 支持分布式场景下的多 Agent 指标聚合

---

## 3. 核心设计

### 3.1 监控采集层：统一在 handler 层

监控采集统一在 `robotActionHandler.ExecuteAction` 中完成（`robot/robot.go`），不使用 `ActionExecutor` 回调：

```
ExecuteAction(actionDef)
  ├── RecordActionStart(name)              // executing++
  ├── start := time.Now()
  ├── if lua:
  │     sendBytes, recvBytes, timing, err = executeLuaAction()
  ├── else:
  │     sendBytes, recvBytes, timing, err = ActionExecutor.Execute()
  ├── classifyResult(err)                  // error → ActionResult
  ├── wallClock  := time.Since(start)
  ├── clientCost := wallClock - timing.NetLatency   // 客户端 CPU 部分
  ├── RecordAction(name, result,
  │       netLatency=timing.NetLatency,
  │       clientCost,
  │       netSamples=timing.SamplesNet,
  │       sendBytes, recvBytes, err)
  │     // executing--, 网络延迟入直方图，客户端开销单独累计
  └── return err
```

**延迟模型的两个维度**（latency 拆分模型，v2 起生效）：

| 维度 | 含义 | 入直方图？ | 字段 |
|------|------|----------|------|
| `NetLatency` | 纯网络往返（send→recv 窗口） | ✅ 仅 `netSamples > 0` 时进 | `latency.{avg,p50,p95,p99,max}Ms` |
| `ClientCost` | 客户端 proto 构建/序列化/解析/state 写入 | ❌ 不进直方图 | `clientAvgMs` |
| `NetSamples` | 本次贡献给 `NetLatency` 的网络调用次数 | — | `netSampleCount` |

各 pattern 的 `NetSamples` 约定：

- `tcpRequest` / `udpRequest` / `httpRequest`：恒为 1（请求成功或超时都计）
- `tcpListen` / `udpListen`：命中为 1，超时为 0
- `tcpSend` / `udpSend` / `tcpConnect` / `udpConnect` / `tcpClose` / `udpClose` / `setState` / `clearState`：恒为 0
- `lua`：脚本内多次 `network.*_request` / `network.*_request_route` 累加，纯客户端脚本为 0

**为什么不在 ActionExecutor 中采集？**

Lua 动作由 `robotActionHandler.executeLuaAction` 处理，声明式动作由 `ActionExecutor.Execute` 处理，两条路径。若把监控放在 `ActionExecutor`，Lua 动作会被遗漏。

`ActionExecutor.Execute` 只负责执行和返回字节数 + 网络耗时拆解，不包含监控逻辑：

```go
func (ae *ActionExecutor) Execute(ctx context.Context, def *ActionDef) (sendBytes, recvBytes int, timing ActionTiming, err error)
```

### 3.2 固定桶延迟直方图

16 个预定义桶（详见第 6 节），覆盖 0ms ~ 60s+。全局累积，不淘汰旧样本。每次 `Record(duration)` 纯原子操作。百分位通过桶计数前缀和 + 线性插值计算。

### 3.3 错误按 (Kind, Code) 聚合

替代设计前的自由格式字符串聚合。使用 `sync.Map` 存储 `errKey -> *errorBucket`，环形缓冲保留最近 3 条 Detail。

---

## 4. 文件结构

| 文件 | 职责 |
|------|------|
| `monitor/histogram.go` | `LatencyHistogram`（16 桶固定桶，纯原子操作）+ `HistogramSnapshot` + `MergeHistograms` |
| `monitor/collector.go` | `MetricsCollector` 全局单例 + `actionMetrics` + `ActionResult` + `CodedError` 接口 + `ErrorEntry` + `errorBucket` + 连接/带宽/回调钩子 |
| `monitor/snapshot.go` | `CollectorSnapshot` + `ActionSnapshot` + `RobotSnapshot` + `ConnectionSnapshot` + `BandwidthSnapshot` + `SystemSnapshot` + `Snapshot()` + `MergeSnapshots()` |
| `monitor/reporter.go` | 定时控制台报告（维护 prevCounts 计算 PeriodQPS） |
| `monitor/http.go` | `/metrics` JSON + `/metrics/summary` 文本（与 pprof 共用 DefaultServeMux） |
| `monitor/csv.go` | CSV 导出 |

---

## 5. MetricsCollector — 全局单例

### 5.1 配置 — CollectorConfig

```go
type CollectorConfig struct {
    Enabled     bool `json:"enabled"`     // 是否启用监控
    HTTPEnabled bool `json:"httpEnabled"` // 是否启用 HTTP JSON 端点
    HTTPPort    int  `json:"httpPort"`    // HTTP 端口号
    ApdexT      int  `json:"apdexT"`      // Apdex T 阈值（毫秒），默认 100
}
```

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `Enabled` | `false` | 总开关 |
| `HTTPEnabled` | `false` | HTTP 指标端点 |
| `HTTPPort` | `6060` | 与 pprof 共用端口 |
| `ApdexT` | `100` | Apdex T 值（毫秒），<= 0 时强制设为 100 |

### 5.2 MetricsCollector 结构体

```go
type MetricsCollector struct {
    enabled   bool            // 是否启用
    cfg       CollectorConfig // 运行期配置副本
    cfgMu     sync.RWMutex    // 保护 cfg.ApdexT 的运行期读写（任务级可调）
    startTime time.Time       // 收集器启动时间

    actions sync.Map   // string → *actionMetrics，按 action 名称索引
    namesMu sync.Mutex // 保护 names 切片的追加
    names   []string   // 按首次出现顺序排列的 action 名称，保证输出稳定

    robotsStarted atomic.Int64 // 已启动的机器人总数
    robotsRunning atomic.Int64 // 当前运行中的机器人数量
    robotsStopped atomic.Int64 // 正常停止的机器人数量
    robotsErrored atomic.Int64 // 异常退出的机器人数量
    totalActions  atomic.Int64 // 累计执行的动作总数（含回调）

    connEstablished atomic.Int64 // 成功建立的连接数
    connFailed      atomic.Int64 // 连接建立失败数
    connDropped     atomic.Int64 // 连接意外断开数

    totalSendBytes atomic.Int64 // 全局累计发送字节数
    totalRecvBytes atomic.Int64 // 全局累计接收字节数
}
```

### 5.3 并发机制

| 字段 | 类型 | 机制 |
|------|------|------|
| `actions` | `sync.Map` (string -> `*actionMetrics`) | 无锁并发 map，首次 LoadOrStore 后只做原子字段更新 |
| `names` / `namesMu` | `[]string` + `sync.Mutex` | 互斥保护有序名称列表（仅在首次出现新 action 时写） |
| `robotsStarted/Running/Stopped/Errored` | `atomic.Int64` x 4 | 无锁原子 |
| `connEstablished/Failed/Dropped` | `atomic.Int64` x 3 | 无锁原子 |
| `totalSendBytes/totalRecvBytes` | `atomic.Int64` x 2 | 无锁原子 |
| `totalActions` | `atomic.Int64` | 无锁原子 |
| `cfg.ApdexT` | `cfgMu sync.RWMutex` | 读写锁保护运行期修改 |
| `startTime` | 启动时写入，后续只读 | 无需同步 |

### 5.4 全局单例

```go
var (
    global    *MetricsCollector
    globalOnce sync.Once
)

func Init(cfg CollectorConfig)    // sync.Once 保证幂等
func Global() *MetricsCollector   // 返回全局单例
```

### 5.5 初始化与重置

- **Init**：`sync.Once` 保证幂等，多次调用不会重置。`ApdexT <= 0` 时强制设为 100
- **Reset**：重置所有计数器，用于新任务开始前清零。清空 actions sync.Map，重置 names 切片，所有原子计数器归零

### 5.6 SetApdexT

```go
func (c *MetricsCollector) SetApdexT(t int)
```

任务级调整 Apdex T 值（毫秒），`<= 0` 不修改。通过 `cfgMu` 写锁保护。

---

## 6. 延迟直方图 — LatencyHistogram

### 6.1 结构体

```go
type LatencyHistogram struct {
    count   atomic.Int64          // 采样总数
    sumNs   atomic.Int64          // 延迟总和（纳秒）
    minMs   atomic.Int64          // 全局最小延迟（毫秒），初始化为 math.MaxInt64
    maxMs   atomic.Int64          // 全局最大延迟（毫秒）
    buckets [numBuckets]atomic.Int64  // 16 个桶的计数
}
```

每个 action 约 136 字节（16 个 `atomic.Int64` + 4 个独立计数器）。

### 6.2 桶边界 — BucketBoundsMs

```go
const NumBuckets = 16

var BucketBoundsMs = [NumBuckets - 1]float64{
    1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 30000, 60000,
}
```

**完整区间列表**：

| 桶索引 | 区间 | 含义 |
|--------|------|------|
| 0 | [0, 1ms) | 亚毫秒 |
| 1 | [1ms, 2ms) | 极快 |
| 2 | [2ms, 5ms) | 很快 |
| 3 | [5ms, 10ms) | 快 |
| 4 | [10ms, 20ms) | 正常偏低 |
| 5 | [20ms, 50ms) | 正常 |
| 6 | [50ms, 100ms) | 正常偏高 |
| 7 | [100ms, 200ms) | 较慢 |
| 8 | [200ms, 500ms) | 慢 |
| 9 | [500ms, 1s) | 很慢 |
| 10 | [1s, 2s) | 极慢 |
| 11 | [2s, 5s) | 超时边缘 |
| 12 | [5s, 10s) | 严重超时 |
| 13 | [10s, 30s) | 极端 |
| 14 | [30s, 60s) | 异常 |
| 15 | [60s, +inf) | 溢出桶 |

**`buckets[i]` 记录落在 `(boundsMs[i-1], boundsMs[i]]` 区间的样本数**。`buckets[0]` 记录 == 0ms 的样本（极少见）。最后一个桶（index 15）为 > 60000ms 的溢出桶。

### 6.3 Record 操作

```go
func (h *LatencyHistogram) Record(d time.Duration)
```

每次调用：
1. `count.Add(1)` — 采样数
2. `sumNs.Add(d.Nanoseconds())` — 延迟总和，用于计算 avg
3. 原子 CAS 循环更新全局 min/max — **不丢失**
4. 遍历桶找归属区间，`buckets[i].Add(1)` — O(16)

**全部操作无 mutex，纯原子操作。**

### 6.4 minMs 初始化

`atomic.Int64` 零值为 0，但 min 应为 `MaxInt64`。**必须**通过 `newLatencyHistogram()` 构造：

```go
func newLatencyHistogram() *LatencyHistogram {
    h := &LatencyHistogram{}
    h.minMs.Store(math.MaxInt64)
    return h
}
```

### 6.5 HistogramSnapshot

```go
type HistogramSnapshot struct {
    Count int64   `json:"count"`
    MinMs float64 `json:"minMs"`
    MaxMs float64 `json:"maxMs"`
    AvgMs float64 `json:"avgMs"`
    P50Ms float64 `json:"p50Ms"`
    P90Ms float64 `json:"p90Ms"`
    P95Ms float64 `json:"p95Ms"`
    P99Ms float64 `json:"p99Ms"`

    // 跨节点聚合所需原始数据（omitempty 向后兼容单机模式）
    SumNs        int64   `json:"sumNs,omitempty"`
    BucketCounts []int64 `json:"bucketCounts,omitempty"`
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `Count` | int64 | 采样数（= 成功次数） |
| `MinMs` | float64 | 全局最小延迟 ms |
| `MaxMs` | float64 | 全局最大延迟 ms |
| `AvgMs` | float64 | 全局平均延迟 ms（= sumNs / count / 1e6） |
| `P50Ms` | float64 | P50 ms（桶计数前缀和 + 线性插值） |
| `P90Ms` | float64 | P90 ms |
| `P95Ms` | float64 | P95 ms |
| `P99Ms` | float64 | P99 ms |
| `SumNs` | int64 | 延迟总和纳秒（用于分布式合并） |
| `BucketCounts` | []int64 | 16 桶计数数组（用于分布式合并） |

### 6.6 百分位计算 — percentileFromBuckets

```go
func percentileFromBuckets(counts [numBuckets]int64, total int64, p float64) float64
```

桶计数前缀和 + 线性插值估算百分位（毫秒），O(16)。

算法：
1. 计算 `rank = ceil(p * total)`
2. 从第一个桶开始累加计数
3. 当累计计数 >= rank 时，在当前桶内线性插值：
   ```
   fraction = (rank - prevSum) / inBucket
   result = lo + fraction * (hi - lo)
   ```
4. `lo` / `hi` 为桶的上下边界

**为什么选固定桶而非排序样本？**

旧工具维护 10240 个样本的排序窗口，超出后淘汰。2 小时压测后 P99 只反映最后约 5 分钟。固定桶的 P99 代表整个生命周期的第 99 百分位。精度约 +/- 20%（由桶边界决定），对压测完全可接受。

### 6.7 MergeHistograms — 分布式合并

```go
func MergeHistograms(snaps []HistogramSnapshot) HistogramSnapshot
```

合并多个直方图快照：
1. Count、SumNs 累加
2. MinMs 取各节点最小值
3. MaxMs 取各节点最大值
4. BucketCounts 逐桶累加
5. AvgMs 重新计算
6. P50/P90/P95/P99 从合并后的桶计数重新计算

---

## 7. ActionResult 分类与指标归属

### 7.1 ActionResult 枚举

```go
type ActionResult int

const (
    ResultSuccess  ActionResult = iota // 执行成功
    ResultFailure                      // 执行失败（非超时）
    ResultTimeout                      // 超时（TCPRequest/WaitListen 无响应）
    ResultCanceled                     // ctx 取消（任务停止/连接断开）
)
```

### 7.2 分类规则 — classifyResult

```go
// robot/robot.go
func classifyResult(err error) monitor.ActionResult {
    if err == nil {
        return monitor.ResultSuccess
    }
    // 任务取消优先级最高
    if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
        return monitor.ResultCanceled
    }
    if errors.Is(err, engine.ErrTimeout) {
        return monitor.ResultTimeout
    }
    return monitor.ResultFailure
}
```

判断优先级：nil → Canceled → Timeout → Failure。

### 7.3 各结果的指标归属

| 结果 | 延迟直方图 | Apdex | QPS (sampleCount) | 错误分布 | executing |
|------|-----------|-------|-------------------|---------|-----------|
| Success | **记录** | satisfied 或 tolerating | 计入 | - | -1 |
| Failure | 不记录 | 隐式 frustrated | 计入 | **按 (Kind,Code) 记录** | -1 |
| Timeout | 不记录 | 隐式 frustrated | 计入 | - | -1 |
| Canceled | 不记录 | **不计入** | **不计入** | - | -1 |

**关键说明**：
- **延迟仅记录 Success**：失败/超时的耗时无意义
- **`latency.count <= sampleCount`**：前端需知延迟仅含成功请求
- **Canceled 不计入样本数**：取消 = 任务被用户主动停止，不代表服务器能力
- **sampleCount = successCount + failureCount + timeoutCount**（不含 canceledCount）

### 7.4 RTT Apdex(T) 公式

```
RTT Apdex = (satisfied + tolerating * 0.5) / rttSampleCount

其中 rttSampleCount = 有完整响应帧且 WireRTT > 0 的 request 数
      satisfied     = WireRTT < T（默认 100ms）
      tolerating    = T <= WireRTT < 4T（默认 100ms ~ 400ms）
      frustrated    = WireRTT >= 4T（隐式 0 分，不单独计数）
```

RTT Apdex 只评价网络 RTT 样本；纯客户端动作、超时、发送失败、取消且未收到响应帧的分支不进入分母。失败/超时对整体质量的影响通过 `failureCount`、`timeoutCount`、`successRate` 和错误分布单独展示。

**关键**：`float64(tolerating) * 0.5`，不是 `tolerating / 2`（Go 整数除法会丢精度）。

RTT Apdex 分类在遍历 `timing.Requests` 时进行：

```go
for _, req := range timing.Requests {
    if req.WireRTT <= 0 {
        continue
    }
    am.rtt.Record(req.WireRTT)
    am.rttSampleCount.Add(1)
    ms := req.WireRTT.Milliseconds()
    switch {
    case ms < T:
        am.apdexSatisfied.Add(1)
    case ms < 4*T:
        am.apdexTolerating.Add(1)
    }
}
```

### 7.5 QPS 计算

- **avgQPS** = `sampleCount / uptimeSeconds` — 全程平均吞吐
- **periodQPS** = `(currentSampleCount - prevSampleCount) / periodSeconds` — 最近周期吞吐
- 仅 Reporter 维护 `prevCounts` 计算控制台 periodQPS
- HTTP 端点传 `prevCounts=nil`，`periodQPS=0`，前端通过连续两次轮询自行计算
- **sampleCount 含 failure + timeout**：QPS 衡量服务器承受的实际负载，排除失败会掩盖问题

---

## 8. actionMetrics — Per-Action 指标

### 8.1 结构体

```go
type actionMetrics struct {
    successCount    atomic.Int64      // 成功次数
    failureCount    atomic.Int64      // 失败次数（非超时）
    timeoutCount    atomic.Int64      // 超时次数
    timeoutTotalMs  atomic.Int64      // 超时样本累计延迟（毫秒），用于计算平均超时延迟
    canceledCount   atomic.Int64      // 取消次数（ctx 取消）
    executing       atomic.Int64      // 当前正在执行中的并发数
    rtt             *LatencyHistogram // RTT 直方图：纯网络往返（WireRTT）
    sendBytes       atomic.Int64      // 累计发送 WireBytes（per-action，所有已记录结果分支）
    recvBytes       atomic.Int64      // 累计接收 WireBytes（per-action，所有已记录结果分支）
    apdexSatisfied  atomic.Int64      // RTT Apdex 满意样本：WireRTT < T
    apdexTolerating atomic.Int64      // RTT Apdex 容忍样本：T <= WireRTT < 4T
    errors          sync.Map          // errKey → *errorBucket，按 (Kind, Code) 聚合的错误分布
}
```

### 8.2 字段说明

| 字段 | 更新时机 | 说明 |
|------|---------|------|
| `successCount` | ResultSuccess | 成功样本计数 |
| `failureCount` | ResultFailure | 失败样本计数（非超时） |
| `timeoutCount` | ResultTimeout | 超时样本计数 |
| `timeoutTotalMs` | ResultTimeout | 超时样本延迟累加，用于计算 `timeoutAvgMs` |
| `canceledCount` | ResultCanceled | ctx 取消计数，不参与 sampleCount/Apdex/SuccessRate |
| `executing` | RecordActionStart (+1) / RecordAction (-1) | 当前并发执行数 |
| `rtt` | 遍历 `timing.Requests` 时 | 记录有完整响应帧且 WireRTT > 0 的 RTT 样本 |
| `sendBytes/recvBytes` | RecordAction/RecordCallback | 记录实际发生的 WireBytes；失败/超时/取消分支只要已有流量也计入 |
| `apdexSatisfied` | WireRTT < T | RTT Apdex 满意计数 |
| `apdexTolerating` | T <= WireRTT < 4T | RTT Apdex 容忍计数 |
| `errors` | ResultFailure | 按 (Kind, Code) 聚合的错误分布 |

### 8.3 CollectErrors — 只读快照

```go
func (am *actionMetrics) CollectErrors() []ErrorEntry
```

遍历 `errors sync.Map`，将每个 `errorBucket` 的数据转为 `ErrorEntry`：

```go
ErrorEntry{
    Kind:     k.Kind,
    Code:     k.Code,
    CodeName: errcode.ErrorCode(k.Code).String(),  // 框架错误有名称，服务端错误为 ""
    Messages: msgs,                                  // 环形缓冲去重后的 Detail 列表
    Count:    count,                                 // 累计出现次数
}
```

仅在 `failureCount > 0 || timeoutCount > 0` 时调用。

---

## 9. 错误聚合

### 9.1 ErrorEntry

```go
type ErrorEntry struct {
    Kind     errcode.Kind `json:"kind"`     // "framework" / "server"
    Code     uint64       `json:"code"`     // 错误码
    CodeName string       `json:"codeName"` // ErrorCode.String()；服务端错误为 ""
    Messages []string     `json:"msgs"`     // 最近 N 条 Detail（最多 3 条，环形缓冲）
    Count    int64        `json:"count"`    // 该错误累计出现次数
}
```

### 9.2 errKey — 聚合键

```go
type errKey struct {
    Kind errcode.Kind
    Code uint64
}
```

`Kind` 区分框架/服务端来源，避免游戏 code=1 与框架 code=1 数值冲突后合并到同一 bucket。

### 9.3 errorBucket — 环形缓冲区

```go
type errorBucket struct {
    count   atomic.Int64    // 累计出现次数
    msgRing [3]atomic.Value // 环形缓冲，存最近 3 条 Detail 字符串
    ringIdx atomic.Uint32   // 环形缓冲写入位置（递增取模）
}
```

**环形缓冲机制**：

1. `msgRing` 固定 3 个槽位，使用 `atomic.Value` 无锁读写
2. `ringIdx` 持续递增（`atomic.Uint32`，永不溢出为负）
3. 写入时先在 `uint32` 域取模，确保结果在 `[0, 3)` 范围内：
   ```go
   idx := int((b.ringIdx.Add(1) - 1) % uint32(len(b.msgRing)))
   ```
4. 取模在 `uint32` 域内完成，转为 `int` 永远非负（避免 32 位系统上 `int(overflow)` 为负导致 panic）
5. `snapshot()` 返回非空且不重复的 Detail 字符串列表

### 9.4 CodedError 接口

```go
type CodedError interface {
    error
    ErrorKind() errcode.Kind // 返回错误分类（framework / server）
    ErrorCode() uint64       // 返回错误码
    ErrorDetail() string     // 返回错误详情（用于环形缓冲存储）
}
```

**为什么用接口而不是直接引用 `engine.ActionError`？**

`monitor` 不能 `import "stressbot/engine"`，否则形成 `engine -> monitor -> engine` 循环依赖。通过接口隔离，`engine.ActionError` 在 `engine/errors.go` 中实现 `ErrorKind()/ErrorCode()/ErrorDetail()` 三个方法即可。

### 9.5 recordError 实现

```go
func (c *MetricsCollector) recordError(am *actionMetrics, err error) {
    var ce CodedError
    if !errors.As(err, &ce) {
        return  // 无法提取 code 的 error，忽略
    }
    key := errKey{Kind: ce.ErrorKind(), Code: ce.ErrorCode()}
    detail := ce.ErrorDetail()
    if len(detail) > 120 {
        detail = detail[:120]  // 截断防止极端情况内存膨胀
    }

    if v, ok := am.errors.Load(key); ok {
        v.(*errorBucket).record(detail)
        return
    }
    b := &errorBucket{}
    b.record(detail)
    if actual, loaded := am.errors.LoadOrStore(key, b); loaded {
        actual.(*errorBucket).record(detail)  // 竞争时另一条先入库
    }
}
```

**并发安全**：
- `LoadOrStore` 处理首次写入竞争（两个 goroutine 同时发现 key 不存在）
- 后续更新直接 `Load` 已有 bucket 并调用 `record`（原子操作）
- `sync.Map` 适用于写少（仅首次）读多的场景

---

## 10. RecordAction — 热路径

### 10.1 签名

```go
func (c *MetricsCollector) RecordAction(
    name string, result ActionResult,
    timing ActionTiming, wallClock time.Duration,
    sendBytes, recvBytes int, err error,
)
```

`timing.Requests` 中每个 `WireRTT > 0` 的 request 都会独立进入 RTT 直方图和 RTT Apdex；`wallClock` 用于客户端开销拆分与总耗时统计。`enabled=false` 时立即返回，零开销。

### 10.2 处理流程

```go
func (c *MetricsCollector) RecordAction(...) {
    if !c.enabled { return }
    c.totalActions.Add(1)
    am := c.getOrCreateAction(name)
    am.executing.Add(-1)

    clientCost := wallClock - timing.wireRTTSum()
    if clientCost > 0 {
        am.clientCostSum.Add(clientCost.Nanoseconds())
        am.clientCostCount.Add(1)
    }

    if wallClock > 0 {
        am.totalDuration.Record(wallClock)
        am.totalDurationSampleCount.Add(1)
    }

    for _, req := range timing.Requests {
        if req.WireRTT <= 0 { continue }
        am.rtt.Record(req.WireRTT)
        am.rttSampleCount.Add(1)
        T := int64(c.apdexT.Load())
        ms := req.WireRTT.Milliseconds()
        if ms < T { am.apdexSatisfied.Add(1) }
        else if ms < 4*T { am.apdexTolerating.Add(1) }
    }

    switch result {
    case ResultSuccess:
        am.successCount.Add(1)
        if sendBytes > 0 { am.sendBytes.Add(int64(sendBytes)) }
        if recvBytes > 0 { am.recvBytes.Add(int64(recvBytes)) }
    case ResultFailure:
        am.failureCount.Add(1)
        if err != nil { c.recordError(am, err) }
    case ResultTimeout:
        am.timeoutCount.Add(1)
        am.timeoutTotalMs.Add(wallClock.Milliseconds())
    case ResultCanceled:
        am.canceledCount.Add(1)
    }
}
```

**关键设计**：
- per-action 字节统计（`sendBytes`/`recvBytes`）仅记录成功样本，用于 ActionsTab 的平均列
- 全局带宽（`totalSendBytes`/`totalRecvBytes`）由 network 层的 `AddBandwidth` 统一统计，不在 RecordAction 中累加（避免双计）
- canceledCount 不参与 sampleCount/SuccessRate 计算；只有产生 WireRTT 的 request 才参与 RTT Apdex

### 10.3 RecordActionStart

```go
func (c *MetricsCollector) RecordActionStart(name string) {
    if !c.enabled { return }
    am := c.getOrCreateAction(name)
    am.executing.Add(1)
}
```

在 `ExecuteAction` 入口处调用，递增 executing 计数。与 `RecordAction` 中的 `executing.Add(-1)` 配对。

---

## 11. 回调监控

### 11.1 RecordCallbackSuccess

```go
func (c *MetricsCollector) RecordCallbackSuccess(name string) {
    if !c.enabled { return }
    c.totalActions.Add(1)
    am := c.getOrCreateAction("callback:" + name)
    am.successCount.Add(1)
}
```

### 11.2 RecordCallbackError

```go
func (c *MetricsCollector) RecordCallbackError(name string, err error) {
    if !c.enabled { return }
    c.totalActions.Add(1)
    am := c.getOrCreateAction("callback:" + name)
    am.failureCount.Add(1)
    if err != nil {
        c.recordError(am, err)
    }
}
```

回调名称以 `callback:` 前缀注册到 actions map。错误码使用 `ErrCallbackLua`（Lua 脚本执行失败）或 `ErrCallbackParse`（推送消息解析失败）。

### 11.3 调用点

在 `robot/robot.go` 的 `createListenCallback` 中：

- Go-store 回调成功 → `RecordCallback(cbName, ResultSuccess, ...)`
- 推送消息解析失败 → `RecordCallback(cbName, ResultFailure, ..., NewActionError(ErrCallbackParse, "proto=...", err))`
- 仅缓存监听（无 Go-store 回调）不额外记录 callback 行

---

## 12. 快照 — Snapshot

### 12.1 CollectorSnapshot

```go
type CollectorSnapshot struct {
    Timestamp    time.Time          `json:"timestamp"`
    Uptime       time.Duration      `json:"uptime"`
    UptimeSec    float64            `json:"uptimeSeconds"`
    TotalActions int64              `json:"totalActions"`
    ApdexT       int                `json:"apdexT"`
    System       SystemSnapshot     `json:"system"`
    Robots       RobotSnapshot      `json:"robots"`
    Connections  ConnectionSnapshot `json:"connections"`
    Bandwidth    BandwidthSnapshot  `json:"bandwidth"`
    Actions      []ActionSnapshot   `json:"actions"`
}
```

### 12.2 SystemSnapshot — 系统资源

```go
type SystemSnapshot struct {
    Goroutines int     `json:"goroutines"` // 当前 goroutine 数量
    MemAllocMB float64 `json:"memAllocMB"` // 已分配堆内存（MB）
    MemSysMB   float64 `json:"memSysMB"`   // 从系统申请的总内存（MB）
    GCCount    uint32  `json:"gcCount"`    // GC 完成次数
}
```

来源：`runtime.ReadMemStats`，在 `Snapshot()` 时采样。

### 12.3 RobotSnapshot — 机器人状态

```go
type RobotSnapshot struct {
    Started  int64 `json:"started"`  // 已启动总数
    Running  int64 `json:"running"`  // 当前运行数
    Stopped  int64 `json:"stopped"`  // 正常停止数
    Errored  int64 `json:"errored"`  // 异常退出数
}
```

生命周期：`Robot.Start()` 调用 `Started() + Running()`；正常退出调 `Stopped()`，异常退出调 `Errored()`。`Running()` 内部做 +1/-1 维护当前数。

### 12.4 ConnectionSnapshot — 连接健康

```go
type ConnectionSnapshot struct {
    Established int64 `json:"established"` // 累计成功建立的连接数
    Failed      int64 `json:"failed"`      // 累计连接建立失败数
    Dropped     int64 `json:"dropped"`     // 累计连接意外断开数
}
```

采集点：`robot/robot.go` 的 `ConnectTCP`/`ConnectUDP` 中记录成功/失败；`SetOnClosed` 回调中记录断连。

### 12.5 BandwidthSnapshot — 全局带宽

```go
type BandwidthSnapshot struct {
    TotalSendBytes int64   `json:"totalSendBytes"` // 累计发送字节数
    TotalRecvBytes int64   `json:"totalRecvBytes"` // 累计接收字节数
    SendMBps       float64 `json:"sendMBps"`       // 平均发送速率（MB/s）
    RecvMBps       float64 `json:"recvMBps"`       // 平均接收速率（MB/s）
}
```

带宽由 network 层的 `AddBandwidth` 统一统计（含心跳/监听等全部流量），不在 RecordAction 中累加。

### 12.6 ActionSnapshot — Per-Action 完整快照

```go
type ActionSnapshot struct {
    Name          string            `json:"name"`
    SampleCount   int64             `json:"sampleCount"`    // success + failure + timeout
    SuccessCount  int64             `json:"successCount"`
    FailureCount  int64             `json:"failureCount"`
    TimeoutCount  int64             `json:"timeoutCount"`
    CanceledCount int64             `json:"canceledCount"`  // ctx 取消，不参与 sampleCount
    Executing     int64             `json:"executing"`
    SuccessRate   float64           `json:"successRate"`
    AvgSendBytes  float64           `json:"avgSendBytes"`
    AvgRecvBytes  float64           `json:"avgRecvBytes"`
    Apdex         float64           `json:"apdex"`
    Latency       HistogramSnapshot `json:"latency"`
    TimeoutAvgMs  float64           `json:"timeoutAvgMs"`
    AvgQPS        float64           `json:"avgQps"`
    PeriodQPS     float64           `json:"periodQps"`
    Errors        []ErrorEntry      `json:"errors,omitempty"`

    // 跨节点聚合所需的原始数据（omitempty 向后兼容单机模式）
    LatencySumNs        int64   `json:"latencySumNs,omitempty"`
    LatencyBucketCounts []int64 `json:"latencyBucketCounts,omitempty"`
    ApdexSatisfied      int64   `json:"apdexSatisfied,omitempty"`
    ApdexTolerating     int64   `json:"apdexTolerating,omitempty"`
    TotalSendBytes      int64   `json:"totalSendBytes,omitempty"`
    TotalRecvBytes      int64   `json:"totalRecvBytes,omitempty"`
}
```

**计算公式**：

| 指标 | 公式 |
|------|------|
| `SampleCount` | `successCount + failureCount + timeoutCount`（不含 canceledCount） |
| `SuccessRate` | `float64(successCount) / float64(sampleCount)` |
| `Apdex` | `(float64(satisfied) + float64(tolerating) * 0.5) / float64(sampleCount)` |
| `AvgSendBytes` | `float64(totalSendBytes) / float64(successCount + failureCount + timeoutCount + canceledCount)` |
| `AvgRecvBytes` | `float64(totalRecvBytes) / float64(successCount + failureCount + timeoutCount + canceledCount)` |
| `AvgQPS` | `float64(sampleCount) / uptimeSec` |
| `PeriodQPS` | `float64(currentSampleCount - prevSampleCount) / periodSec` |
| `TimeoutAvgMs` | `float64(timeoutTotalMs) / float64(timeoutCount)` |

### 12.7 Snapshot 生成

```go
func (c *MetricsCollector) Snapshot(prevCounts map[string]int64, periodSec float64) *CollectorSnapshot
```

1. 读取系统资源（`runtime.ReadMemStats`）
2. 计算全局带宽（`totalSendBytes / uptimeSec` → MBps）
3. 读取当前 ApdexT（RLock）
4. 遍历 `ActionNames()` 生成每个 action 的 `ActionSnapshot`
5. 错误分布仅在 `failureCount > 0 || timeoutCount > 0` 时采集
6. `prevCounts` 由 Reporter 维护，传 nil 则 periodQPS = 0

---

## 13. MergeSnapshots — 分布式聚合

### 13.1 签名

```go
func MergeSnapshots(snaps []*CollectorSnapshot) *CollectorSnapshot
```

### 13.2 合并规则

| 指标 | 合并方式 |
|------|---------|
| `Timestamp` | 取当前时间（合并时刻） |
| `ApdexT` | 取第一个 snapshot 的值（所有 Agent 使用相同配置） |
| `UptimeSec` | 取所有 snapshot 中的**最大值** |
| `TotalActions` | 累加 |
| `Robots.*` | 全部累加（Started/Running/Stopped/Errored） |
| `Connections.*` | 全部累加（Established/Failed/Dropped） |
| `Bandwidth.TotalSend/RecvBytes` | 累加 |
| `Bandwidth.MBps` | 重新计算（`totalBytes / maxUptime`） |

### 13.3 Action 级别合并

按 action name 分组后，每个 action 的合并规则：

| 指标 | 合并方式 |
|------|---------|
| `SampleCount` | 累加 |
| `SuccessCount` | 累加 |
| `FailureCount` | 累加 |
| `TimeoutCount` | 累加 |
| `CanceledCount` | 累加 |
| `Executing` | 累加（各 Agent 当前并发数之和） |
| `LatencySumNs` | 累加 |
| `ApdexSatisfied` | 累加 |
| `ApdexTolerating` | 累加 |
| `TotalSendBytes/TotalRecvBytes` | 累加 |
| `Latency` | `MergeHistograms`（逐桶累加 + 重新计算百分位） |
| `SuccessRate` | 重新计算（`totalSuccess / totalSample`） |
| `Apdex` | 重新计算（标准公式） |
| `AvgSendBytes/AvgRecvBytes` | 重新计算（`totalBytes / (success + failure + timeout + canceled)`） |
| `AvgQPS` | 重新计算（`totalSample / maxUptime`） |
| `TimeoutAvgMs` | 重新计算（加权平均：`sum(timeoutAvgMs * timeoutCount) / totalTimeout`） |

### 13.4 错误合并

按 `(Kind, Code)` 二元组聚合：

```go
type mergedErrorKey struct{ Kind errcode.Kind; Code uint64 }
errMap := make(map[mergedErrorKey]*ErrorEntry)
```

**合并规则**：
1. Count：累加
2. Messages：取并集去重，超过 5 条截断
3. Kind/Code/CodeName：相同 key 保持不变

```go
for _, e := range a.Errors {
    k := mergedErrorKey{Kind: e.Kind, Code: e.Code}
    if existing, ok := errMap[k]; ok {
        existing.Count += e.Count
        for _, m := range e.Messages {
            if !slices.Contains(existing.Messages, m) {
                existing.Messages = append(existing.Messages, m)
            }
        }
        if len(existing.Messages) > 5 {
            existing.Messages = existing.Messages[:5]
        }
    } else {
        cp := e
        errMap[k] = &cp
    }
}
```

### 13.5 Action 顺序保持

使用 `order []string` 按 action 首次出现的顺序追加，保证合并后输出稳定。

---

## 14. 机器人生命周期钩子

| 方法 | 调用时机 | 原子操作 |
|------|---------|---------|
| `RobotStarted()` | Robot.Start() | `robotsStarted.Add(1)` |
| `RobotRunning()` | Robot.Start() | `robotsRunning.Add(1)` |
| `RobotStopped()` | Robot 正常退出 | `robotsRunning.Add(-1)` + `robotsStopped.Add(1)` |
| `RobotErrored()` | Robot 异常退出 | `robotsRunning.Add(-1)` + `robotsErrored.Add(1)` |

### 连接生命周期钩子

| 方法 | 调用时机 |
|------|---------|
| `ConnEstablished()` | ConnectTCP/ConnectUDP 成功 |
| `ConnFailed()` | ConnectTCP/ConnectUDP 失败（dial 错误） |
| `ConnDropped()` | SetOnClosed 回调（主动/被动关闭都触发） |

---

## 15. AddBandwidth — 带宽统计

```go
func (c *MetricsCollector) AddBandwidth(send, recv int64)
```

由 network 层调用，含心跳/监听等全部流量。单机模式通过此方法统计全局带宽。

**与 per-action 字节统计的关系**：
- `AddBandwidth`：全局统计（network 层上报），含心跳等非动作流量
- `am.sendBytes/recvBytes`：per-action 统计（RecordAction/RecordCallback 中记录），所有已记录结果分支按实际发生的 WireBytes 累计
- `RecordAction` 中**不再累加** `totalSendBytes/totalRecvBytes`，避免双计

---

## 16. 控制台 Reporter — `monitor/reporter.go`

### 16.1 Reporter 结构体

```go
type Reporter struct {
    collector  *MetricsCollector   // 指标收集器引用
    interval   time.Duration       // 报告间隔
    prevCounts map[string]int64    // 上次快照时各 action 的样本数，用于计算 periodQPS
    prevTime   time.Time           // 上次报告时间
    stopCh     chan struct{}       // 停止信号通道
    stopOnce   sync.Once           // 保证 stopCh 只关闭一次
}
```

### 16.2 输出格式

每 interval（默认 5s）输出一次：

```
[MONITOR] 4m25s | goroutines=16 | mem=12.3MB | gc=5 | total=380
[MONITOR] robots: 1运行 0停止 0错误 | conn: 3建立 0失败 0断连 | 0.5/1.2 MB/s
[MONITOR] 动作                    成功 失败 超时    avg    p95 apdex exec   qps
[MONITOR] CreateNormalTeam        280    4    0    46ms   120ms  0.95    2  0.95
[MONITOR] errors: CreateNormalTeam→[framework/4 RECV_TIMEOUT]×26 service=logic elapsed=2.3s (+2 more),
                      CreateNormalTeam→[server/1004]×15 service=logic route=CreateTeam
```

**四行结构**：
- 第一行：运行时间 + 系统资源 + 总动作数
- 第二行：机器人状态 + 连接健康 + 带宽
- 表格：每动作一行（名称 + 成功/失败/超时 + avg + p95 + apdex + executing + periodQPS）
- errors：仅在有失败时追加一行，格式 `动作→[Kind/Code CodeName]xCount msg`

### 16.3 错误输出格式

```
actionName→[kind/code codeName]×count firstMsg
```

超过 1 条 Messages 时追加 `(+N more)` 提示。错误按次数降序排列（`sortedErrors`）。

### 16.4 启停

```go
func NewReporter(c *MetricsCollector, interval time.Duration) *Reporter
func (r *Reporter) Start()   // 通过 work_pool 启动定时报告 goroutine
func (r *Reporter) Stop()    // close(stopCh)，保证只关闭一次
```

Reporter 使用 `utils.GetWorkPool().GoWithStop` 启动，同时响应自身 stopCh 和 work pool 的全局停止信号。

---

## 17. HTTP 端点 — `monitor/http.go`

### 17.1 RegisterHandlers

```go
func RegisterHandlers(c *MetricsCollector)
```

将 `/metrics` 和 `/metrics/summary` 注册到 `http.DefaultServeMux`。pprof 通过 `import _ "net/http/pprof"` 副作用注册到同一 mux，共享端口。

### 17.2 GET /metrics

完整 JSON 快照，供前端消费：

```json
{
  "timestamp": "2026-05-23T10:30:00+08:00",
  "uptime": 150500000000,
  "uptimeSeconds": 150.5,
  "totalActions": 52843,
  "apdexT": 100,
  "system": { "goroutines": 142, "memAllocMB": 12.3, "memSysMB": 45.6, "gcCount": 23 },
  "robots": { "started": 100, "running": 98, "stopped": 0, "errored": 2 },
  "connections": { "established": 300, "failed": 0, "dropped": 1 },
  "bandwidth": { "totalSendBytes": 5242880, "totalRecvBytes": 15728640, "sendMBps": 0.5, "recvMBps": 1.2 },
  "actions": [
    {
      "name": "createTeam",
      "sampleCount": 284,
      "successCount": 280,
      "failureCount": 4,
      "timeoutCount": 0,
      "canceledCount": 0,
      "executing": 2,
      "successRate": 0.9859,
      "avgSendBytes": 45.2,
      "avgRecvBytes": 1230.5,
      "apdex": 0.95,
      "latency": { "count": 280, "minMs": 12.0, "maxMs": 450.6, "avgMs": 45.7, "p50Ms": 38.2, "p90Ms": 78.5, "p95Ms": 120.3, "p99Ms": 450.6 },
      "timeoutAvgMs": 0,
      "avgQps": 1.89,
      "periodQps": 0,
      "errors": [
        { "kind": "framework", "code": 4, "codeName": "RECV_TIMEOUT", "msgs": ["service=logic elapsed=2.3s"], "count": 3 }
      ]
    }
  ]
}
```

**前端对接要点**：
- `timestamp` — ISO 8601
- `uptimeSeconds` — 数值类型，前端可自行格式化
- `successRate` — 0~1 小数，前端按需显示为百分比
- `latency` 嵌套对象 — 可独立渲染延迟图表
- `latency.count <= sampleCount` — 延迟仅含成功样本
- 所有数值字段保证非 null（零值兜底）
- `periodQps` 在 HTTP 端点为 0（前端轮询两次用 `timestamp` 差值自行计算）
- `errors` 仅在有失败时出现（`omitempty`）
- `errors[].kind` — `"framework"` / `"server"`，前端按此上色
- `errors[].codeName` — 框架错误有值（如 `"RECV_TIMEOUT"`），服务端错误为 `""`

### 17.3 GET /metrics/summary

文本摘要，供快速检查：

```
uptime: 4m25s
robots: started=100 running=98 stopped=0 errored=2
createTeam: samples=284 success=280 timeout=0 failure=4 avg=45.7ms p99=450.6ms apdex=0.950 qps=1.89
```

### 17.4 StartHTTPServer

```go
func StartHTTPServer(port int)
```

通过 work_pool 启动非阻塞 HTTP 服务器。日志输出 metrics 和 pprof 的 URL。

---

## 18. CSV 导出 — `monitor/csv.go`

```go
func ExportCSV(c *MetricsCollector, path string) error
```

### 18.1 表头

```
接口名,样本数,成功次数,超时次数,错误次数,成功率,平均响应(ms),最小响应(ms),最大响应(ms),
P50(ms),P90(ms),P95(ms),P99(ms),Apdex,平均发送字节,平均接收字节,平均QPS,压测时长(s)
```

### 18.2 说明

- 关闭时写入，默认路径 `log/metrics.csv`
- 自动创建目录（`os.MkdirAll`）
- 仅记录 action 级数据（不含系统/机器人/连接/带宽）
- CSV 写入错误不影响主流程

---

## 19. 配置

### 19.1 JSON 配置

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
|------|--------|------|
| `enabled` | `false` | 总开关 |
| `reportInterval` | `"5s"` | 控制台报告间隔 |
| `httpEnabled` | `false` | HTTP 指标端点 |
| `httpPort` | `6060` | 与 pprof 共用端口 |
| `csvPath` | `"log/metrics.csv"` | 关闭时 CSV 输出路径 |
| `apdexT` | `100` | Apdex T 值（毫秒） |

---

## 20. Lua 动作的指标

Lua 动作在 `robotActionHandler.ExecuteAction` 中统一采集：

- **延迟**：整个脚本执行时间（从调用 `executeLuaAction` 到返回）
- **成功/失败**：基于 `executeLuaAction` 返回的 error
- **错误分布**：使用结构化 ActionError（ErrLuaNotInit / ErrLuaNoScript / ErrLuaExecFailed / ErrLuaScriptCheck）
- **字节统计**：脚本内 `network.*` 调用产生的 WireBytes 由 `script.Context` 自动累计，脚本返回 nil（成功）或 err table（失败）
- **executing 计数**：同样适用

---

## 21. 设计说明

### 21.1 HTTP vs Reporter 的 periodQPS

HTTP 端点传 `prevCounts=nil`，`periodQps=0`。仅 Reporter 维护 `prevCounts` 计算控制台 periodQPS。前端可通过连续两次 `GET /metrics` 自行计算。

### 21.2 sync.Map 写模式

- `actions sync.Map` 写入仅在首次看到某 action name 时（`LoadOrStore`），后续全是对已存指针的原子字段更新
- `errors sync.Map` 同理，首次写入后只做原子累加 + 环形缓冲原子写入
- `errorDescCache sync.Map`（adapter 包）写入 once-only（首次查询后只读）

### 21.3 sampleCount 口径

```
sampleCount = successCount + failureCount + timeoutCount
```

- 不含 `canceledCount`（取消不算样本）
- 不含 skippedCount（当前实现中无此字段）
- 历史口径保持不变，改造前后数据可比较

### 21.4 全局带宽 vs per-action 字节

- **全局带宽**（`totalSendBytes`/`totalRecvBytes`）：由 network 层的 `AddBandwidth` 统计，含心跳/监听等全部流量
- **per-action 字节**（`am.sendBytes`/`am.recvBytes`）：由 `RecordAction`/`RecordCallback` 统计，所有已记录结果分支按实际发生的 WireBytes 累计
- 两者不重叠：RecordAction 中不再累加全局带宽

---

## 22. 与计划的差异

| 章节 | 计划 | 实际 |
|------|------|------|
| 桶数量 | 17 个桶 | **16 个桶**（`NumBuckets = 16`），15 个边界值 + 1 个溢出桶 |
| `skippedCount` | ActionSnapshot 含此字段 | **未实现**。ActionResult 中无 ResultSkipped，classifyResult 只有 4 个分支 |
| 错误聚合 | string → *atomic.Int64 | **errKey → *errorBucket**，按 (Kind, Code) 聚合 + 环形缓冲 |
| ErrorEntry | `{msg, count}` | **`{kind, code, codeName, msgs, count}`**，结构化替代字符串 |
| RecordAction 签名 | `(name, result, duration, send, recv, errMsg string)` | `(name, result, duration, send, recv, err error)` |
| CanceledCount | 未提及 | **已实现**。ActionSnapshot 含 CanceledCount，不参与 sampleCount |
| Callback 监控 | 未提及 | **已实现**。RecordCallbackSuccess / RecordCallbackError |
| TimeoutAvgMs | 未提及 | **已实现**。actionMetrics 含 timeoutTotalMs，ActionSnapshot 含 TimeoutAvgMs |
| 控制台错误格式 | `动作→错误消息(次数)` | `动作→[Kind/Code CodeName]×Count msg (+N more)` |
| CSV 表头 | 含"跳过次数"列 | **不含"跳过次数"列** |
| `totalSendBytes/totalRecvBytes` | 在 RecordAction 中累加 | **由 network 层的 AddBandwidth 统计**，RecordAction 中不累加（避免双计） |
| LatencyHistogram minMs 初始化 | 文档提及 | **已正确实现**（newLatencyHistogram 中 Store(math.MaxInt64)） |
| 分布式聚合 | 未提及 | **已实现**。MergeSnapshots + MergeHistograms，含完整错误合并规则 |
| ApdexT 运行期调整 | 未提及 | **已实现**。SetApdexT 方法，cfgMu 读写锁保护 |
