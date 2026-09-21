# 运行时监控系统 — 技术文档

> 本文档对照实际代码（`monitor/collector.go`、`monitor/snapshot.go`、`monitor/histogram.go`、`monitor/reporter.go`、`monitor/http.go`、`monitor/csv.go`、`robot/robot.go`、`robot/scheduler.go`）编写，反映当前已实施状态。

---

## 1. 概述

`monitor` 包提供准确性优先、可分布式合并的指标采集系统。核心特性：

- **计数器使用原子操作，DDSketch 与 per-action 聚合由短临界区（`am.mu`）保护**：同名动作快照一致，不同动作并行记录
- **`enabled=false` 时所有公共方法为空操作**，且全部对 nil receiver 安全（monitor 未 `Init` 时热路径 `monitor.Global().Xxx()` 静默 no-op），零性能开销
- **延迟使用 DDSketch**（默认 1% 相对精度、最多 2048 bins，可经 `monitor.sketch` 配置），同时精确保存 count/sum/min/max；P50/P90/P95/P99 从 sketch 读取，跨 Agent/窗口/动作**先合并 sketch 再算分位**
- **累计面 + 顺序区间窗口双轨**：`TakeReportSnapshot` 在记录边界原子切换活动窗口，Agent 周期上报携带窗口指标，窗口序号从 1 递增供 Admin 做去重与顺序校验
- **错误按 `code` 单维聚合**（展示时按 code<100 推导"框架"/code≥100 推导"业务"标签），环形缓冲保留最近 3 条 Detail
- **RTT Apdex(T) 评分只对 `networked` 类动作**，T 可配置（默认 100ms），无响应帧的失败请求进入分母记 frustrated；总耗时（wallClock）只留直方图作诊断，不打分
- **监听等待独立成列**：`ListenWait` 直方图从 `RecvFrameAt` 起算，另有 `ListenReadyCount`（已在队列、不产 0ms 样本）与 `ListenTimeoutCount`/超时率
- **支持分布式聚合**，`MergeSnapshots` 合并多 Agent 指标；内部报告携带 sketch，公共 API 经 `PublicCopy()` 剥离
- **声明式动作和 Lua 脚本动作统一自动采集**，用户无感知

### 1.1 与旧工具对比

| 指标 | 旧工具 | stressbot |
|------|--------|-----------|
| min/max/P50/P90/P95/P99 | 窗口值（最近 ~10240 样本） | **累计 + 顺序上报窗口**（DDSketch） |
| Avg | 窗口值 | **全局**（sum/count） |
| Apdex | 窗口值，`sample500Less` 命名有歧义 | **全局 RTT Apdex(T)**，仅 networked 类，T 可配置，分母为发起过的 request 样本数 |
| 内存/action | ~80KB（10240 x 8B） | **有界**（每个分布最多 2048 bins） |
| 锁/channel | channel 传批次 | 计数原子操作，分布更新与快照使用短 mutex |
| 外部依赖 | `go-metrics` | `DataDog/sketches-go` |
| 系统资源 | 无 | goroutines/mem/GC |
| 连接健康 | 无 | 建立/活跃/关闭/失败/断连计数 |
| 全局带宽 | 无 | 发送/接收 MB/s |
| 错误分布 | 无 | 按 code 单维聚合 + 环形缓冲 Detail |
| 取消统计 | 无 | canceledCount（ctx 取消不计入失败率） |
| 回调监控 | 无 | callback 成功/失败/错误聚合 |
| 并发执行 | 无 | per-action executing 计数（瞬时量，见 §12.8） |
| 输出 | 控制台 + CSV + WebSocket | 控制台 + CSV + HTTP JSON |
| 分布式聚合 | 无 | MergeSnapshots 合并多 Agent |

---

## 2. 设计目标

1. 继承旧工具指标体系（per-action 成功率/延迟/字节 + 机器人状态 + CSV + 定时输出），修正根本缺陷（窗口统计 -> 全局累积）
2. 计数器纯原子操作，分布与快照只持短时聚合锁，无 channel、无外部时序依赖
3. `enabled=false` / 未初始化时所有方法为 no-op，零开销
4. 不修改任何公开接口（`ActionHandler`、`NetSender`、`Executor`）
5. 声明式动作和 Lua 脚本动作统一自动采集，用户无感知
6. 错误按结构化 `code` 单维聚合（码段 <100 框架 / ≥100 业务），替代自由格式字符串爆炸
7. 支持分布式场景下的多 Agent 指标聚合

---

## 3. 核心设计

### 3.1 监控采集层：统一在 handler 层

监控采集统一在 `robotActionHandler.ExecuteAction` 中完成（`robot/robot.go:632`），不使用 `ActionExecutor` 回调：

```
ExecuteAction(ctx, actionDef)
  ├── RecordActionStart(name)                  // executing++（仅累计采集器）
  ├── result := runActionBody(ctx, actionDef)  // lua → runLuaAction；否则 runDeclarativeAction
  ├── result.err = normalizeActionCancel(ctx, result.err)
  │     // 停止期把 LISTEN_TIMEOUT/CONN_NOT_FOUND 等副作用错误统一矫正为
  │     // ErrActionCanceled，避免把 cancel 流量记成 Timeout/Failure
  ├── classifyResult(result.err)               // error → ActionResult
  ├── RecordAction(name, result, toMonitorTiming(result.timing),
  │       result.wallClock, sendBytes, recvBytes, err)
  └── return result.err
```

**为什么不在 ActionExecutor 中采集？**

Lua 动作由 `robotActionHandler.runLuaAction` 处理，声明式动作由 `ActionExecutor.Execute` 处理，两条路径。若把监控放在 `ActionExecutor`，Lua 动作会被遗漏。

`ActionExecutor.Execute` 只负责执行和返回字节数 + 耗时拆解（`engine.ActionTiming`），不包含监控逻辑；`toMonitorTiming`（`robot/robot.go:705`）负责把 engine 侧 timing 转成 monitor 侧结构：

```go
func (ae *ActionExecutor) Execute(ctx context.Context, def *ActionDef) (sendBytes, recvBytes int, timing ActionTiming, err error)
```

### 3.2 耗时拆解模型

`ActionTiming`（`monitor/collector.go:58`）是监控的输入模型：

| 字段 | 含义 | 去向 |
|------|------|------|
| `Requests []RequestTiming` | 每次 request-response 的独立样本（`WireRTT`/`SendCost`/`DecodeWait`/`DecodeCost`/`DispatchToActionWait` + `Observed` 阶段位图） | `WireRTT > 0` 或已观测 StageRTT 的进 RTT 直方图与 Apdex |
| `FailedRequests int` | 发出去但没等回响应帧的请求数（超时/断连/发送失败） | 只进 RTT Apdex 分母记 frustrated |
| `ListenWaits []time.Duration` | 监听命中等待时长样本 | ListenWait 直方图 |
| `ListenReady int` | 命中时消息已在队列（等待时长不可测） | 只计数 `listenReadyCount` |
| `ListenTimeouts int` | 监听超时次数 | 只计数 `listenTimeoutHits`，单独成率 |
| `Client ClientTiming` | 客户端侧 build/encode/send/decodeWait/decode/dispatchWait/parseStore 分项 + `Observed` | 各分项均值列（timingDetail 级别够时才有值） |

**WireRTT 的两个测点都贴近内核**（`network/connection.go:372-386`）：起点是 `AsyncWrite` 的写完成回调（数据真正交给内核，而非入队时刻），终点是 `RecvFrameAt`（帧在 gnet 入站缓冲里可读的时刻，而非 Robot 被唤醒读到它的时刻）。这样避免协作式调度把施压机自身的排队算进服务端延迟。

`TimingStage` 位标志（`monitor/collector.go:31-41`）区分"没有采集"与"实际测得 0ns"：`Observed` 未置位且值为 0 才跳过，测得 0 照记（本机/极快响应确实能量到 0，丢掉会系统性抬高 P50）。

**监听等待同样从 `RecvFrameAt` 起算**（开始等待 → 帧被内核收到），避开轮询唤醒时刻的量化误差；已在队列里的消息记 `ListenReady` 计数而不产生 0ms 样本。

### 3.3 DDSketch 延迟直方图

延迟以纳秒加入 DDSketch，相对精度默认 1%（`relativeAccuracy = 0.01`），store 最多 2048 bins，均可用 `monitor.sketch` 配置（`monitor/collector.go:247-252`，`monitor/histogram.go:14-18`）；同时精确累计 count/sum/min/max。**DDSketch 合并要求两侧精度参数一致，Admin 与所有 Agent 必须配置相同值**。累计面与活动区间窗口各持有独立的可合并分布，`TakeReportSnapshot` 在 `transitionMu` 写锁内切换。

### 3.4 错误按 code 单维聚合

使用 `sync.Map` 存储 `errKey -> *errorBucket`，按 `code` 单维聚合，环形缓冲保留最近 3 条 Detail（`ErrorDetailRingSize`，`monitor/collector.go:94-97`）。码段约定：`code < 100` 为框架错误，`code >= 100` 为业务错误；展示标签由 code 推导，不持久化 Kind 字段。

---

## 4. 文件结构

| 文件 | 职责 |
|------|------|
| `monitor/histogram.go` | `LatencyHistogram`（DDSketch + 精确 count/sum/min/max）+ `HistogramSnapshot` + 严格 `MergeHistograms` |
| `monitor/collector.go` | `MetricsCollector` 全局单例 + `ActionTiming`/`RequestTiming`/`ClientTiming` + `TimingDetailLevel` + `actionMetrics` + `ActionResult` + `CodedError` 接口 + `ErrorEntry`/`errorBucket` + 连接/带宽/回调/渐进加压钩子 |
| `monitor/snapshot.go` | `CollectorSnapshot` + `ActionSnapshot` + `SnapshotSummary` + `ActionKind` 分类 + `ReportWindow`/`TakeReportSnapshot` + `PublicCopy` + `Snapshot()` + `MergeSnapshots()` |
| `monitor/reporter.go` | 定时控制台报告（基于 `TakeReportSnapshot` 的区间窗口） |
| `monitor/http.go` | `/metrics` JSON（`PublicCopy` 剥离 sketch）+ `/metrics/summary` 文本 |
| `monitor/csv.go` | CSV 导出 |

---

## 5. MetricsCollector — 全局单例

### 5.1 配置 — CollectorConfig

```go
type CollectorConfig struct {
    ApdexThresholdMs int          `toml:"apdexThresholdMs" json:"apdexThresholdMs"` // Apdex T 阈值（毫秒），默认 100
    TimingDetail     string       `toml:"timingDetail"     json:"timingDetail"`     // 计时细分级别：rtt / codec / full，默认 rtt
    ReportInterval   string       `toml:"reportInterval"   json:"reportInterval"`   // 控制台上报间隔（如 "5s"），仅单机模式用
    HTTP             *HTTPConfig  `toml:"http"             json:"http"`             // nil = 不启用 HTTP JSON 端点
    Sketch           SketchConfig `toml:"sketch"           json:"sketch"`           // DDSketch 延迟分布精度参数
}
```

（`monitor/collector.go:238-257`）

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `ApdexThresholdMs` | `100` | Apdex T 值（毫秒），<= 0 时强制设为 100 |
| `TimingDetail` | `"rtt"` | 计时细分级别，空串/非法值回退 rtt 并 Warn（`NormalizeTimingDetail`） |
| `ReportInterval` | `"5s"` | 单机控制台报告间隔（解析失败回退 5s，`standalone/app.go:191-196`） |
| `HTTP` | `nil` | 整段省略即不启用 HTTP 端点；启用时配置 `port` |
| `Sketch.RelativeAccuracy` | `0.01` | DDSketch 相对误差 |
| `Sketch.MaxBins` | `2048` | DDSketch 最大桶数 |

启用/禁用不在配置内表达：调用方（`standalone/app.go:54`、`agent/config.go:50`）只在 `cfg.Monitor != nil` 时调用 `monitor.Init`，collector 固定 `enabled=true`。

### 5.2 MetricsCollector 结构体

```go
type MetricsCollector struct {
    enabled         bool              // 是否启用（NewCollector 固定 true）
    cfg             CollectorConfig   // 运行期配置副本（除 ApdexThresholdMs 外）
    cfgMu           sync.RWMutex      // 保护 cfg 非热路径字段
    apdexT          atomic.Int32      // Apdex T 阈值（毫秒）热路径独立原子读写，与 cfgMu 解耦
    timingDetail    TimingDetailLevel // 计时细分级别
    windowOnly      bool              // true 表示活动区间子收集器，禁止递归创建 window
    transitionMu    sync.RWMutex      // Record 持读锁；TakeReportSnapshot 持写锁切换窗口
    window          atomic.Pointer[MetricsCollector]
    collectionEpoch atomic.Uint64     // 累计指标代次；Reset 后递增，允许 Admin 识别合法清零
    startTime       atomic.Int64      // 启动时间（UnixNano），原子避免 time.Time 撕裂

    actions sync.Map   // string → *actionMetrics，按 action 名称索引
    namesMu sync.Mutex // 保护 names 切片的追加
    names   []string   // 按首次出现顺序排列的 action 名称，保证输出稳定

    robotsStarted / robotsRunning / robotsStopped / robotsErrored atomic.Int64
    totalActions  atomic.Int64 // 累计执行的动作总数（含回调；ResultCanceled 不计）

    connEstablished atomic.Int64 // 成功建立的连接数
    connActive      atomic.Int64 // 当前活跃连接数
    connClosed      atomic.Int64 // 累计关闭连接数（主动 + 异常）
    connFailed      atomic.Int64 // 连接建立失败数
    connDropped     atomic.Int64 // 连接意外断开数（服务端关闭/网络异常）

    totalSendBytes / totalRecvBytes atomic.Int64 // 全局累计收发字节（network 层上报）
    invalidMetricSamples            atomic.Int64 // 被拒绝的负耗时或 DDSketch Add 失败样本

    rampUpCurrentStage / rampUpTotalStages atomic.Int32 // 渐进加压阶段
}
```

（`monitor/collector.go:261-295`）

### 5.3 并发机制

| 字段 | 类型 | 机制 |
|------|------|------|
| `actions` | `sync.Map` (string -> `*actionMetrics`) | 无锁并发 map，首次 LoadOrStore 后由 `am.mu` 短临界区保护一致快照 |
| `names` / `namesMu` | `[]string` + `sync.Mutex` | 互斥保护有序名称列表（仅在首次出现新 action 时写） |
| `am.mu` | `sync.Mutex`（per action） | RecordAction/快照持锁，同名动作一致、不同动作并行 |
| `apdexT` | `atomic.Int32` | 热路径无锁读写 |
| `transitionMu` + `window` | `RWMutex` + `atomic.Pointer` | Record 持读锁同时写累计面与窗口面；TakeReportSnapshot 持写锁原子换窗口 |
| robots/conn/bandwidth/计数器 | `atomic.Int64` | 无锁原子 |
| `startTime` / `collectionEpoch` | `atomic.Int64` / `atomic.Uint64` | Reset 写、Uptime/快照读 |

### 5.4 全局单例与独立实例

```go
var (
    global    *MetricsCollector
    globalOnce sync.Once
)

func Init(cfg CollectorConfig)    // sync.Once 保证幂等（调用方保证 cfg.Monitor != nil 才调）
func Global() *MetricsCollector   // 返回全局单例（可能为 nil，所有方法对 nil 安全）
func NewCollector(cfg CollectorConfig) *MetricsCollector // 独立实例，供测试与 Agent 任务级使用
```

Agent 任务切换时**不能**再调 `Init`（会替换 global 而旧引用仍被 reporter 持有），只用 `Reset` + `SetApdexT`（`agent/task/runner.go:131-138`）。

### 5.5 初始化与重置

- **Init**：`sync.Once` 保证幂等；`TimingDetail` 空串或非法时 Warn 并回退 rtt；`ApdexThresholdMs <= 0` 强制 100；同时创建首个活动窗口
- **Reset**：重置所有计数器与 `actions`/`names`，`collectionEpoch` 递增（Admin 据此识别合法清零而非数据丢失），持 `transitionMu` 写锁替换新窗口

### 5.6 SetApdexT

```go
func (c *MetricsCollector) SetApdexT(t int)
```

任务级调整 Apdex T 值（毫秒），`<= 0` 不修改。热路径只读原子字段 `apdexT`；同时同步 `cfg.ApdexThresholdMs`（快照导出用）与活动窗口。

---

## 6. 延迟直方图 — LatencyHistogram

### 6.1 结构体

```go
type LatencyHistogram struct {
    mu     sync.Mutex
    sketch *ddsketch.DDSketch
    count  int64
    sumNs  int64
    minNs  int64
    maxNs  int64
}
```

DDSketch 使用 `LogCollapsingLowestDenseDDSketch(relativeAccuracy, maxBins)`：默认相对精度 1%、单个 store 最多 2048 bins，构造参数来自 `CollectorConfig.Sketch`（零值回退默认，`monitor/histogram.go:42-54`）。值以纳秒存入 sketch，避免亚毫秒值在入库前损失精度。初始化失败直接 panic（配置期错误不进运行期）。

### 6.2 Record 操作

```go
func (h *LatencyHistogram) Record(d time.Duration) error
```

负耗时被拒绝（`ErrInvalidMetricSample`），由调用方计入 `invalidMetricSamples`；DDSketch Add 失败同样拒绝并计数。有效样本在同一临界区内加入 sketch 并更新精确 count/sum/min/max，快照不会出现撕裂状态。

### 6.3 HistogramSnapshot

```go
type HistogramSnapshot struct {
    Count  int64    `json:"count"`
    MinMs  *float64 `json:"minMs"`
    MaxMs  *float64 `json:"maxMs"`
    AvgMs  *float64 `json:"avgMs"`
    P50Ms  *float64 `json:"p50Ms"`
    P90Ms  *float64 `json:"p90Ms"`
    P95Ms  *float64 `json:"p95Ms"`
    P99Ms  *float64 `json:"p99Ms"`
    SumNs  int64    `json:"sumNs,omitempty"`
    Sketch []byte   `json:"sketch,omitempty"`
}
```

`Count/SumNs/MinMs/MaxMs` 是精确值；分位数从 DDSketch 读取，并限制在精确 Min/Max 边界内。`Count == 0`（空分布）时七个展示值全部是 `null`，明确表示没有样本。`Sketch` 只允许出现在 Agent 到 Admin 的内部报告，公共 API 通过 `PublicCopy()` 移除（`monitor/snapshot.go:241-306`）。快照生成失败意味着内部数据已损坏，直接 panic。

### 6.4 MergeHistograms — 分布式合并

```go
func MergeHistograms(snaps []HistogramSnapshot) (HistogramSnapshot, error)
```

非空分布必须同时携带完整展示值和有效 sketch，且 sketch 内样本数必须等于 `Count`；任何损坏都返回错误，不回退到估算值、不对分位数做二次平均。合并时累加 Count/SumNs、取精确 Min/Max，并调用 DDSketch `MergeWith` 后重新计算 P50/P90/P95/P99。

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

（`monitor/collector.go:87-92`）

### 7.2 分类规则 — classifyResult

```go
// robot/robot.go:738
func classifyResult(err error) monitor.ActionResult {
    if err == nil {
        return monitor.ResultSuccess
    }
    if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
        return monitor.ResultCanceled
    }
    if actionErr, ok := errors.AsType[*errcode.ActionError](err); ok {
        if isCanceledCode(actionErr.Code) {
            return monitor.ResultCanceled
        }
        if isTimeoutCode(actionErr.Code) {
            return monitor.ResultTimeout
        }
    }
    return monitor.ResultFailure
}
```

判断优先级：nil → Canceled（ctx 或 `ErrActionCanceled`）→ Timeout（`ErrRecvTimeout`/`ErrListenTimeout`）→ Failure。`ExecuteAction` 在分类前还经 `normalizeActionCancel`（`robot/robot.go:673`）把停止期的副作用错误统一矫正为 `ErrActionCanceled`——否则任务停止会在监控面堆出一批"假失败/假超时"。

### 7.3 请求结果的指标归属

| 运行时事实 | RTT 直方图 | RTT Apdex | QPS (sampleCount) |
|------|-----------|-------|-------------------|
| 收到完整响应帧（含业务错误） | **按真实 WireRTT 记录**（含量到 0ms 的极快响应） | 按 RTT 分类 | 计入 |
| 已发起请求但无响应帧（超时/断连/发送失败） | 不记录（没有可用时延值） | **进入分母，记 frustrated** | 失败/超时结果计入 |
| 监听/单向发送/本地动作 | 不适用 | 不参与 | 按动作结果计入 |
| 任务取消且未形成失败请求 | 不记录 | 不参与 | 不计入 |

**关键说明**：

- **RTT 记录响应帧而不是业务成功**：服务器返回业务错误时仍有真实 RTT，正常进入直方图和 Apdex
- **`rttSampleCount <= rttApdexSampleCount`**：无响应帧请求只影响评分分母，不污染分位数
- **Canceled 不计入样本数**：取消 = 任务被用户主动停止，不代表服务器能力；`totalActions` 也不计 canceled（`monitor/collector.go:512-514`）
- **sampleCount = successCount + failureCount + timeoutCount**（不含 canceledCount）

### 7.4 RTT Apdex(T) 公式

```
RTT Apdex = (satisfied + tolerating * 0.5) / rttApdexSampleCount

其中 rttSampleCount = 有完整响应帧且实际测过 RTT 的 request 数（含 0ms 样本）
      rttFailedCount = 已发起但没有完整响应帧的失败 request 数
      rttApdexSampleCount = rttSampleCount + rttFailedCount
      satisfied     = WireRTT < T（默认 100ms）
      tolerating    = T <= WireRTT < 4T（默认 100ms ~ 400ms）
      frustrated    = WireRTT >= 4T 或无响应帧（隐式 0 分，不单独计数）
```

Apdex 分类在遍历 `timing.Requests` 时进行（`monitor/collector.go:565-587`），阈值按 `time.Duration` 比较而非整毫秒取整：

```go
for _, req := range timing.Requests {
    if !req.Observed.Has(StageRTT) && req.WireRTT == 0 {
        continue // 根本没测过；StageRTT 置位的 0ms 样本照记
    }
    if err := am.rtt.Record(req.WireRTT); err != nil { c.invalidMetricSamples.Add(1); continue }
    am.rttSampleCount.Add(1)
    threshold := time.Duration(c.apdexT.Load()) * time.Millisecond
    switch {
    case req.WireRTT < threshold:
        am.apdexSatisfied.Add(1)
    case req.WireRTT < 4*threshold:
        am.apdexTolerating.Add(1)
    }
}
if timing.FailedRequests > 0 {
    am.rttFailedCount.Add(int64(timing.FailedRequests)) // 只进分母记 frustrated
}
```

**关键**：`float64(tolerating) * 0.5`，不是 `tolerating / 2`（Go 整数除法会丢精度）。

**总耗时（wallClock）只留直方图作诊断，不再打 Apdex**（`monitor/collector.go:535-537`）：它是「施压机排队 + 故意 sleep + 服务端响应」的混合量，施压机越忙这个数越大，用它评分等于把施压机自身的负载算成服务端的账。评分统一由 RTT 承担。

### 7.5 QPS 计算

- **avgQPS** = `sampleCount / uptimeSeconds` — 全程平均吞吐
- **periodQPS** = 区间窗口样本数 / 窗口时长 — `TakeReportSnapshot` 对窗口各 action 赋 `AvgQPS = SampleCount / duration` 并令 `PeriodQPS = AvgQPS`（`monitor/snapshot.go:594-598`）；控制台 Reporter 输出窗口 QPS，Agent 周期上报同源
- HTTP 端点走 `Snapshot(nil, 0)`，`periodQPS=0`，前端通过连续两次轮询自行计算
- **sampleCount 含 failure + timeout**：QPS 衡量服务器承受的实际负载，排除失败会掩盖问题

---

## 8. actionMetrics — Per-Action 指标

### 8.1 结构体

```go
type actionMetrics struct {
    mu              sync.Mutex
    successCount    atomic.Int64      // 成功次数
    failureCount    atomic.Int64      // 失败次数（非超时）
    timeoutCount    atomic.Int64      // 超时次数
    timeoutTotalNs  atomic.Int64      // 超时样本累计延迟（纳秒），用于计算平均超时延迟
    canceledCount   atomic.Int64      // 取消次数（ctx 取消）
    executing       atomic.Int64      // 当前正在执行中的并发数
    rtt             *LatencyHistogram // RTT 直方图：纯网络往返（收到响应帧的请求，不论业务成败）
    listenWait      *LatencyHistogram // 监听等待直方图：开始等待 → 帧被内核收到
    totalDuration   *LatencyHistogram // 总耗时直方图：单次 action wallClock
    sendBytes       atomic.Int64      // 发送字节数（per-action，用于 ↑avg 列）
    recvBytes       atomic.Int64      // 接收字节数（per-action，用于 ↓avg 列）
    apdexSatisfied  atomic.Int64      // RTT Apdex 满意样本：响应时间 < T
    apdexTolerating atomic.Int64      // RTT Apdex 容忍样本：响应时间 >= T 且 < 4T
    rttFailedCount  atomic.Int64      // 无响应帧的请求数：进 RTT Apdex 分母记 frustrated
    errors          sync.Map          // errKey → *errorBucket，按 code 单维聚合的错误分布

    // 客户端开销与样本计数（clientCostSum/Count、rttSampleCount、totalDurationSampleCount、
    // byteSampleCount、build/encode/send/decodeWait/decode/dispatchWait/parseStore 各分项 Sum+Count）
    clientCostSum / clientCostCount / buildCostSum / ... atomic.Int64
    rttSampleCount / totalDurationSampleCount / byteSampleCount atomic.Int64

    // 监听类计数
    listenWaitCount   atomic.Int64 // 等待时长样本数（listenWait 直方图分母）
    listenReadyCount  atomic.Int64 // 命中时消息已在队列：等待时长不可测，只计次
    listenTimeoutHits atomic.Int64 // 监听超时次数，单独成率，不进直方图
}
```

（完整字段见 `monitor/collector.go:148-201`；三个直方图用 `cfg.Sketch` 精度创建）

### 8.2 字段说明（与 RecordAction 的对应）

| 字段 | 更新时机 | 说明 |
|------|---------|------|
| `successCount` / `failureCount` / `timeoutCount` | 对应 result 分支 | 样本计数 |
| `timeoutTotalNs` | ResultTimeout 且 wallClock > 0 | 纳秒累计，用于 `timeoutAvgMs` |
| `canceledCount` | ResultCanceled | 不参与 sampleCount/Apdex/SuccessRate |
| `executing` | RecordActionStart (+1) / RecordAction (-1) | 当前并发执行数（仅累计采集器，见 §12.8） |
| `rtt` / `rttSampleCount` | 遍历 `timing.Requests` | 有完整响应帧且测过 RTT 的样本（含 0ms） |
| `listenWait` / `listenWaitCount` | 遍历 `timing.ListenWaits`（wait > 0） | 监听等待分布 |
| `listenReadyCount` | `timing.ListenReady > 0` | 已就绪命中，只计次 |
| `listenTimeoutHits` | `timing.ListenTimeouts > 0` | 超时次数 |
| `totalDuration` / `totalDurationSampleCount` | `result != Canceled \|\| wallClock > 0` | wallClock 直方图（诊断用） |
| `sendBytes/recvBytes/byteSampleCount` | 所有结果分支 | 按实际发生的 WireBytes 累计；分母是 byteSampleCount |
| `apdexSatisfied/apdexTolerating` | Requests 循环 | RTT Apdex 分类计数 |
| `rttFailedCount` | `timing.FailedRequests` | 无响应帧请求数 |
| 客户端分项 Sum/Count | 同 totalDuration 条件 | build/encode/send/decodeWait/decode/dispatchWait/parseStore；分子分母同一采样条件 |
| `errors` | ResultFailure / ResultTimeout | 按 code 单维聚合的错误分布 |

### 8.3 CollectErrors — 只读快照

```go
func (am *actionMetrics) CollectErrors() []ErrorEntry
```

遍历 `errors sync.Map`，将每个 `errorBucket` 的数据转为 `ErrorEntry`：

```go
ErrorEntry{
    Code:     k.Code,
    CodeName: errcode.ErrorCode(k.Code).String(),  // 框架错误（code<100）有名称，业务错误为 ""
    Messages: msgs,                                  // 环形缓冲去重后的 Detail 列表
    Count:    count,                                 // 累计出现次数
}
```

仅在 `failureCount > 0 || timeoutCount > 0` 时调用（`monitor/snapshot.go:488-491`）。

---

## 9. 错误聚合

### 9.1 ErrorEntry

```go
type ErrorEntry struct {
    Code     uint64   `json:"code"`     // 错误码（<100 框架，>=100 业务）
    CodeName string   `json:"codeName"` // ErrorCode.String()；业务错误为 ""
    Messages []string `json:"msgs"`     // 最近若干条 Detail（单节点上界 ErrorDetailRingSize=3，环形缓冲）
    Count    int64    `json:"count"`    // 该错误累计出现次数
}
```

### 9.2 errKey — 聚合键

```go
type errKey struct {
    Code uint64 // 错误码
}
```

按 `code` 单维聚合。码段约定 `<100` 为框架错误、`>=100` 为业务错误，避免数值冲突；框架/业务区分仅在展示层由 code 推导，聚合层不持久化 Kind。

### 9.3 errorBucket — 环形缓冲区

```go
const ErrorDetailRingSize = 3 // 单节点每类错误保留的 Detail 条数上界（collector.go:97）

type errorBucket struct {
    count   atomic.Int64                      // 累计出现次数
    msgRing [ErrorDetailRingSize]atomic.Value // 环形缓冲，存最近 3 条 Detail 字符串
    ringIdx atomic.Uint32                     // 环形缓冲写入位置（递增取模）
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

容量说明（源码注释）：纯展示层容量——错误定位靠 Count 和 code，样例只要够看清参数形态即可，多留没有额外信息量，还要按 action × code 常驻占内存。

### 9.4 CodedError 接口

```go
type CodedError interface {
    error
    ErrorCode() uint64   // 返回错误码
    ErrorDetail() string // 返回错误详情（用于环形缓冲存储）
}
```

**为什么用接口而不是直接引用 `errcode.ActionError`？**

`monitor` 不能 import 会反向依赖它的动作层；`errcode.ActionError`（`errcode/error.go:6`）实现 `ErrorCode()/ErrorDetail()` 两个方法即满足接口，monitor 通过 `errors.AsType[CodedError](err)` 结构化提取，不依赖具体类型。

### 9.5 recordError 实现

```go
func (c *MetricsCollector) recordError(am *actionMetrics, err error) {
    ce, ok := errors.AsType[CodedError](err)
    if !ok {
        return  // 无法提取 code 的 error，忽略
    }
    key := errKey{Code: ce.ErrorCode()}
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

（`monitor/collector.go:641-660`）

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

（`monitor/collector.go:488-494`）`timing.Requests` 中每个实际测过 RTT 的 request 独立进入 RTT 直方图和 RTT Apdex；`wallClock` 用于客户端开销拆分与总耗时分母。`enabled=false` 或 nil receiver 时立即返回，零开销。

### 10.2 处理流程（要点，`monitor/collector.go:496-638`）

```go
func (c *MetricsCollector) recordAction(..., trackExecuting bool) {
    if c == nil || !c.enabled { return }
    // 持 transitionMu 读锁（累计采集器），同名动作 am.mu 短临界区
    am := c.getOrCreateAction(name)
    if result != ResultCanceled { c.totalActions.Add(1) }
    if trackExecuting { am.executing.Add(-1) }

    // 记录 wallClock/客户端分项的判据：result != Canceled || wallClock > 0。
    // 只排除"取消补记"（executor 对未执行节点的配平记账，Canceled 且 wallClock=0）：
    // 否则 0ms 样本会压低分位数、令 Apdex 虚高；真实执行后才取消的样本照常记录；
    // 成功/失败/超时一律记录——Windows 时钟粒度粗，快动作可能测得 0。
    if result != ResultCanceled || wallClock > 0 {
        clientCost := max(wallClock-timing.wireRTTSum(), 0) // 客户端开销 = 总耗时 - ΣWireRTT
        if clientCost > 0 { am.clientCostSum.Add(...) }
        am.clientCostCount.Add(1)
        am.totalDuration.Record(wallClock)          // 只留直方图作诊断
        // build/encode/send/decodeWait/decode/dispatchWait/parseStore：
        // Observed 未置位且值为 0 跳过；负值计 invalidMetricSamples；分子分母同条件累加
    }
    // Requests 循环：RTT 直方图 + Apdex 分类（见 §7.4）
    // FailedRequests → rttFailedCount（只进 Apdex 分母）
    // ListenWaits/ListenReady/ListenTimeouts → 监听等待列（见 §3.2）

    if sendBytes > 0 { am.sendBytes.Add(int64(sendBytes)) }   // 所有结果分支都计
    if recvBytes > 0 { am.recvBytes.Add(int64(recvBytes)) }
    am.byteSampleCount.Add(1)

    switch result {
    case ResultSuccess: am.successCount.Add(1)
    case ResultFailure:
        am.failureCount.Add(1)
        if err != nil { c.recordError(am, err) }
    case ResultTimeout:
        am.timeoutCount.Add(1)
        if wallClock > 0 { am.timeoutTotalNs.Add(wallClock.Nanoseconds()) }
        if err != nil { c.recordError(am, err) }              // 超时也进错误分布
    case ResultCanceled: am.canceledCount.Add(1)
    }
    if !c.windowOnly {
        c.currentWindowCollector().recordAction(..., false)   // 转发窗口面，不重复计 executing
    }
}
```

**关键设计**：

- per-action 字节统计（`sendBytes`/`recvBytes`）记录所有已形成记录的结果分支（含失败/超时/取消），均值分母是 `byteSampleCount`
- 全局带宽（`totalSendBytes`/`totalRecvBytes`）由 network 层的 `AddBandwidth` 统一统计，不在 RecordAction 中累加（避免双计）
- `ResultFailure` 与 `ResultTimeout` 都写入错误分布；`ResultCanceled` 不进错误分布、不计数 sampleCount
- canceledCount 不参与 sampleCount/SuccessRate；RTT Apdex 评价发起过的 request，有 WireRTT 时按真实值分类，无响应帧失败时记 frustrated

### 10.3 RecordActionStart

```go
func (c *MetricsCollector) RecordActionStart(name string) {
    if c == nil || !c.enabled { return }
    am := c.getOrCreateAction(name)
    am.mu.Lock(); am.executing.Add(1); am.mu.Unlock()
}
```

（`monitor/collector.go:438-446`）在 `ExecuteAction` 入口处调用，递增 executing 计数，与 `RecordAction` 中的 `executing.Add(-1)` 配对。

**只作用于累计采集器，不转发给区间采集器**：executing 是瞬时量，而区间采集器每个上报周期换新，跨周期的「开始」与「结束」会落在两个采集器上，只减不加会算出负数。区间报告里的 executing 由 `TakeReportSnapshot` 从同一时刻的累计快照回填（§12.8）。

### 10.4 RecordPendingCanceled

```go
func (c *MetricsCollector) RecordPendingCanceled(name string)
```

（`monitor/collector.go:450-465`）记录由于上游分支中止而**从未开始执行**的动作（executor 的配平记账）：只增加 canceledCount，不进入吞吐、字节、错误、Apdex 或 executing；同时转发累计面与窗口面。与 §10.2 的采样判据配合，保证这类伪样本不污染分位数与 Apdex。

---

## 11. 回调监控

### 11.1 RecordCallback — 统一入口

```go
func (c *MetricsCollector) RecordCallback(
    name string, result ActionResult,
    timing ActionTiming, wallClock time.Duration,
    sendBytes, recvBytes int, err error,
)
```

（`monitor/collector.go:743-749`）内部以 `"callback:" + name` 走 `recordAction(..., trackExecuting=false)`——回调不在 executing 口径内。

### 11.2 便捷包装

```go
func (c *MetricsCollector) RecordCallbackSuccess(name string)  // RecordCallback(name, ResultSuccess, 零值..., nil)
func (c *MetricsCollector) RecordCallbackError(name string, err error)  // RecordCallback(name, ResultFailure, ..., err)
```

### 11.3 调用点

在 `robot/robot.go` 的 `createListenCallback`（`robot/robot.go:1039`）及 `runListenScript` 中，回调分三种形态：

- **listen 脚本回调**（`cbDef.Script`）：消息快照投递 Robot 任务队列，由执行器 goroutine 在节点边界串行执行 `on_message`；脚本执行失败记 `ErrCallbackLua`，成功/失败均带 wallClock 与 recvBytes
- **Go-store 回调**（`cbDef.S2CProto` + `cbDef.Store`）：pump 内 wire-first 物化待写值（大广播走内容寻址去重），SetPath 投递执行器任务队列；解析失败记 `ErrCallbackParse`，成功样本的时长含队列等待（反映「推送到达 → 状态可见」的真实延迟）
- **纯缓存 listen**（无回调定义）：返回 nil，消息仅入监听队列供主流程消费，不记录 callback 行

---

## 12. 快照 — Snapshot

### 12.1 CollectorSnapshot

```go
type CollectorSnapshot struct {
    Timestamp            time.Time          `json:"timestamp"`
    CollectionEpoch      uint64             `json:"collectionEpoch"` // 累计指标代次，Reset 后递增
    Uptime               time.Duration      `json:"uptime"`
    UptimeSec            float64            `json:"uptimeSeconds"`
    TotalActions         int64              `json:"totalActions"`
    ApdexT               int                `json:"apdexT"`
    TimingDetail         TimingDetailLevel  `json:"timingDetail"`    // 实际级别：rtt / codec / full
    Summary              SnapshotSummary    `json:"summary"`         // 跨动作统一汇总
    System               SystemSnapshot     `json:"system"`
    Robots               RobotSnapshot      `json:"robots"`
    RampUp               RampUpSnapshot     `json:"rampUp"`          // 渐进加压阶段
    Connections          ConnectionSnapshot `json:"connections"`
    Bandwidth            BandwidthSnapshot  `json:"bandwidth"`
    Actions              []ActionSnapshot   `json:"actions"`         // 契约保证非 nil（空数组而非 null）
    InvalidMetricSamples int64              `json:"invalidMetricSamples"`
    Window               *ReportWindow      `json:"window"`          // TakeReportSnapshot 时携带的区间指标
}
```

（`monitor/snapshot.go:191-208`）

`Summary` 是实时总览和历史采样的唯一跨动作指标来源。它包含结果计数、成功率、RTT Apdex 及完整分母、合并后的 RTT/监听等待/总耗时直方图、客户端阶段均值和总 QPS。跨动作 P95/P99 由 DDSketch 合并后重算，不平均单动作百分位；Apdex 由原始满意/容忍计数与完整请求分母计算，展示层不得再从单动作指标二次聚合。

`TimingDetail` 三档（`monitor/collector.go:211-220`）：`rtt`（仅 RTT + 发送）、`codec`（追加编解码分项）、`full`（再追加构建、排队、分发等待、解析/状态写入）。它只控制额外客户端阶段计时，**与 Apdex 计算无关**；分布式合并时取所有节点的最低级别（`monitor/snapshot.go:786-788`）。

`Actions` 始终是非 nil 数组——历史 bug：nil slice 序列化成 `"actions": null` 曾让前端 `for...of` 抛错崩掉整个页面，现源头兜底（`monitor/snapshot.go:554-561`）。

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
    Active      int64 `json:"active"`      // 当前活跃连接数
    Closed      int64 `json:"closed"`      // 累计关闭连接数（主动 + 异常）
    Failed      int64 `json:"failed"`      // 累计连接建立失败数
    Dropped     int64 `json:"dropped"`     // 累计连接意外断开数
}
```

采集点：`robot/robot.go` 的 `ConnectTCP`/`ConnectUDP` 成功记 `ConnEstablished`（established/active 各 +1）、dial 失败记 `ConnFailed`；`SetOnClosed` 回调记 `ConnClosed`（active -1、closed +1）；`SetOnDisconnect` 回调记 `ConnDropped`（只累计 dropped，active 由 ConnClosed 统一扣减）。

### 12.5 BandwidthSnapshot — 全局带宽

```go
type BandwidthSnapshot struct {
    TotalSendBytes int64   `json:"totalSendBytes"` // 累计发送字节数
    TotalRecvBytes int64   `json:"totalRecvBytes"` // 累计接收字节数
    SendMBps       float64 `json:"sendMBps"`       // 平均发送速率（MB/s）
    RecvMBps       float64 `json:"recvMBps"`       // 平均接收速率（MB/s）
}
```

带宽由 network 层的 `AddBandwidth` 统一统计（发送侧 `network/connection.go:457`、接收侧 `network/gnet.go:253`，含心跳/监听等全部流量），不在 RecordAction 中累加。

### 12.6 RampUpSnapshot — 渐进加压

```go
type RampUpSnapshot struct {
    CurrentStage int `json:"currentStage"` // 当前阶段（1-based，0 = 未启用）
    TotalStages  int `json:"totalStages"`  // 总阶段数（0 = 未启用）
}
```

由 `Manager` 在每个阶段开始时调 `SetRampUpStage(current, total)` 写入；分布式合并取各节点最小 `currentStage`（最慢的 Agent 决定整体阶段进度，`monitor/snapshot.go:806-809`）。

### 12.7 ActionSnapshot — Per-Action 关键字段

```go
type ActionSnapshot struct {
    Name          string  `json:"name"`
    SampleCount   int64   `json:"sampleCount"`    // success + failure + timeout
    SuccessCount  int64   `json:"successCount"`
    FailureCount  int64   `json:"failureCount"`
    TimeoutCount  int64   `json:"timeoutCount"`
    CanceledCount int64   `json:"canceledCount"`  // ctx 取消，不参与 sampleCount
    Executing     int64   `json:"executing"`       // 瞬时量：累计面计数、区间面回填
    SuccessRate   float64 `json:"successRate"`
    AvgSendBytes  float64 `json:"avgSendBytes"`
    AvgRecvBytes  float64 `json:"avgRecvBytes"`
    ByteSampleCount int64 `json:"byteSampleCount"` // 实际开始并形成记录的动作数
    Kind          ActionKind `json:"kind"`          // networked / listen / send / local

    RTTApdex            float64           `json:"rttApdex"`           // 仅 Kind=networked 时有意义
    RTT                 HistogramSnapshot `json:"rtt"`                // WireRTT 分布
    ListenWait          HistogramSnapshot `json:"listenWait"`         // 监听等待分布
    ListenReadyCount    int64             `json:"listenReadyCount"`
    ListenTimeoutCount  int64             `json:"listenTimeoutCount"`
    ListenTimeoutRate   float64           `json:"listenTimeoutRate"`  // 超时 /（命中 + 已就绪 + 超时）
    TotalDuration       HistogramSnapshot `json:"totalDuration"`      // wallClock，仅作诊断
    TimeoutAvgMs        float64           `json:"timeoutAvgMs"`

    ClientAvgMs         float64 `json:"nonRTTAvgMs"`                 // 非 RTT 平均耗时
    BuildAvgMs / EncodeAvgMs / SendAvgMs / DecodeWaitAvgMs /
    DecodeAvgMs / DispatchToActionWaitAvgMs / ParseStoreAvgMs float64 // 客户端分项均值
    BuildSampleCount / ... / ParseStoreSampleCount int64               // 各分项样本数
    RTTSampleCount      int64   `json:"rttSampleCount"`               // 实际测得 RTT 的 request 数（含 0ms）
    RTTApdexSampleCount int64   `json:"rttApdexSampleCount"`          // 分母：有响应帧 + 无响应帧失败请求
    ListenWaitSampleCount    int64 `json:"listenWaitSampleCount"`
    TotalDurationSampleCount int64 `json:"totalDurationSampleCount"`

    AvgQPS    float64      `json:"avgQps"`
    PeriodQPS float64      `json:"periodQps"`
    Errors    []ErrorEntry `json:"errors,omitempty"`

    // 跨节点聚合所需的原始计数；延迟原始分布位于各 HistogramSnapshot.Sketch
    ApdexSatisfied / ApdexTolerating / RTTFailedCount int64
    TotalSendBytes / TotalRecvBytes / TimeoutTotalNs int64
    ClientCostSumNs / ClientCostCount、
    Build/Encode/Send/DecodeWait/Decode/DispatchWait/ParseStore 各 CostSumNs int64
}
```

（完整定义见 `monitor/snapshot.go:80-140`）

**计算公式**：

| 指标 | 公式 |
|------|------|
| `SampleCount` | `successCount + failureCount + timeoutCount`（不含 canceledCount） |
| `SuccessRate` | `float64(successCount) / float64(sampleCount)` |
| `RTTApdex` | `(satisfied + tolerating * 0.5) / rttApdexSampleCount` |
| `RTTApdexSampleCount` | `rttSampleCount + rttFailedCount` |
| `AvgSendBytes` | `float64(totalSendBytes) / float64(byteSampleCount)` |
| `AvgRecvBytes` | `float64(totalRecvBytes) / float64(byteSampleCount)` |
| `AvgQPS` | `float64(sampleCount) / uptimeSec`（区间报告中为 `sampleCount / 窗口时长`） |
| `PeriodQPS` | 区间报告中 `= AvgQPS`；HTTP 累计快照中为 0 |
| `TimeoutAvgMs` | `float64(timeoutTotalNs) / float64(timeoutCount) / 1e6` |
| `ListenTimeoutRate` | `listenTimeoutCount / (listenWaitSampleCount + listenReadyCount + listenTimeoutCount)` |

### 12.8 ActionKind — 动作分类与 TakeReportSnapshot

**ActionKind 四分类**（`monitor/snapshot.go:40-64`、`classifyActionKind` 732-743 行）按运行期实际发生的网络行为定型，取最强语义（优先级 networked > listen > send > local）：

| Kind | 判定 | 主指标 | Apdex |
|------|------|--------|-------|
| `networked` | 有往返样本（rttApdexSampleCount > 0） | RTT | 打分 |
| `listen` | 无往返但有监听事件 | 等待时长分布 + 超时率 | 不打 |
| `send` | 无往返无监听但有发送字节 | 执行耗时 | 不打 |
| `local` | 都没有（setState、纯计算） | 执行耗时 | 不打 |

由运行期事实判定而非配置：Lua 脚本里做了什么只有跑起来才知道。前端按 kind 选主指标列；监听/发送/本地类在 UI 显示「不适用」——这三类耗时主体是服务端业务时长或客户端执行时长，没有可比的统一阈值，掺进总分会让分数随动作构成漂移。

**TakeReportSnapshot — 区间窗口**（`monitor/snapshot.go:567-624`）：

```go
func (c *MetricsCollector) TakeReportSnapshot(meta ReportMeta) *CollectorSnapshot
```

1. 持 `transitionMu` 写锁：取出旧活动窗口、换上新窗口、生成同一时刻的累计快照（记录边界一致）
2. 区间采集器自身的 executing 结构性恒为 0（`RecordActionStart` 不转发、`recordAction` 对窗口传 `trackExecuting=false`）；区间报告的 executing 从同一时刻的累计快照回填——窗口收口那一刻有多少在执行
3. 区间各行 `AvgQPS = SampleCount / duration`、`PeriodQPS = AvgQPS`
4. 汇总的 Executing 取累计面总数而非区间各行之和（某动作可能整个窗口没跑完一次但确有机器人卡在里面）
5. 返回的累计快照挂 `Window *ReportWindow`（序号、起止、时长、区间 summary/带宽/actions）

单机控制台 Reporter 与 Agent 的 StressReporter（`agent/metrics/reporter.go:70-79`）都经此方法周期上报；窗口 `Sequence` 从 1 递增，Admin 侧据此做去重与顺序校验。

### 12.9 Snapshot 生成

```go
func (c *MetricsCollector) Snapshot(prevCounts map[string]int64, periodSec float64) *CollectorSnapshot
```

1. 持 `transitionMu` 读锁
2. 读取系统资源（`runtime.ReadMemStats`）
3. 计算全局带宽（`totalSendBytes / uptimeSec` → MBps）
4. 读取当前 ApdexT 与 TimingDetail
5. 遍历 `ActionNames()` 生成每个 action 的 `ActionSnapshot`（同名动作持 `am.mu`，快照一致）
6. 错误分布仅在 `failureCount > 0 || timeoutCount > 0` 时采集
7. 调用 `BuildSnapshotSummary(actions)` 生成跨动作统一汇总（内部合并直方图失败会 panic——监控数据损坏不该静默）
8. `prevCounts` 传 nil 则 periodQPS = 0

---

## 13. MergeSnapshots — 分布式聚合

### 13.1 签名

```go
func MergeSnapshots(snaps []*CollectorSnapshot) (*CollectorSnapshot, error)
```

（`monitor/snapshot.go:749`）返回错误而非静默降级：任一分布损坏（缺展示值/缺 sketch/样本数不一致）都会让合并失败。契约保证返回的 `Actions` 字段始终非 nil。

### 13.2 顶层合并规则

| 指标 | 合并方式 |
|------|---------|
| `Timestamp` | 取当前时间（合并时刻） |
| `ApdexT` | 取第一个 snapshot 的值（所有 Agent 使用相同配置） |
| `TimingDetail` | 取所有节点的最低级别 |
| `CollectionEpoch` | 取最大值 |
| `UptimeSec` | 取所有 snapshot 中的**最大值** |
| `TotalActions` / `InvalidMetricSamples` | 累加 |
| `Robots.*` | 全部累加（Started/Running/Stopped/Errored） |
| `Connections.*` | 全部累加（Established/Active/Closed/Failed/Dropped） |
| `RampUp.TotalStages` | 取第一个；`CurrentStage` 取各节点最小值 |
| `Bandwidth.TotalSend/RecvBytes` | 累加 |
| `Bandwidth.MBps` | 重新计算（`totalBytes / maxUptime`） |

### 13.3 Action 级别合并

按 action name 分组后，每个 action 的合并规则：

| 指标 | 合并方式 |
|------|---------|
| 各结果计数 / `CanceledCount` / `Executing` | 累加 |
| `RTTSampleCount` / `RTTFailedCount` / `ListenWaitSampleCount` / `ListenReadyCount` / `ListenTimeoutCount` / `TotalDurationSampleCount` / `ByteSampleCount` | 累加 |
| `ApdexSatisfied` / `ApdexTolerating` | 累加 |
| `TotalSendBytes/TotalRecvBytes/TimeoutTotalNs` 及各客户端分项 Sum/Count | 累加 |
| `RTT` / `ListenWait` / `TotalDuration` | `MergeHistograms`（合并 DDSketch + 重新计算百分位） |
| `SuccessRate` | 重新计算（`totalSuccess / totalSample`） |
| `RTTApdex` | 按 `RTTSampleCount + RTTFailedCount` 重新计算 |
| `Kind` | 用合并后的计数重新 `classifyActionKind` |
| `AvgSendBytes/AvgRecvBytes` | 重新计算（`totalBytes / byteSampleCount`） |
| `AvgQPS` | 重新计算（`totalSample / maxUptime`） |
| `TimeoutAvgMs` / 各客户端均值 | 重新计算（按合并后的 Sum/Count） |

动作合并结束后再次调用同一个 `BuildSnapshotSummary`。实时前端与 Admin Sampler 只读取该 `summary`，不再从 action 数组加权 Apdex 或百分位数。

Admin 聚合时 Executing 按**新鲜节点**口径重算（`admin/metrics/aggregator.go:63-208`）：失联超过 `max(3×ExpectedEvery, 15s)` 或 Offline 的节点被剔除，逐行与汇总必须同口径——否则滞留节点的在执行数会变成永远降不下来的并发数。

### 13.4 错误合并

按 `code` 单维聚合：

```go
const mergedErrorDetailLimit = 5 // 跨节点合并后每类错误保留的 Detail 条数上界（snapshot.go:13）

type mergedErrorKey struct{ Code uint64 }
errMap := make(map[mergedErrorKey]*ErrorEntry)
```

**合并规则**：
1. Count：累加
2. Messages：取并集去重，超过 5 条截断（比单节点的 3 条大：并集来自多个节点，各留一条样例比同一节点的三条更能反映故障面）
3. Code/CodeName：相同 key 保持不变

### 13.5 Action 顺序保持

使用 `order []string` 按 action 首次出现的顺序追加，保证合并后输出稳定。

---

## 14. 机器人与连接生命周期钩子

| 方法 | 调用时机 | 原子操作 |
|------|---------|---------|
| `RobotStarted()` | Robot.Start() | `robotsStarted.Add(1)` |
| `RobotRunning()` | Robot.Start() | `robotsRunning.Add(1)` |
| `RobotStopped()` | Robot 正常退出 | `robotsRunning.Add(-1)` + `robotsStopped.Add(1)` |
| `RobotErrored()` | Robot 异常退出 | `robotsRunning.Add(-1)` + `robotsErrored.Add(1)` |
| `SetRampUpStage(current, total)` | Manager 每阶段开始 | 写 rampUp 两个原子字段 |

以上方法全部对 nil receiver 安全（monitor 未 Init 时静默 no-op）。

### 连接生命周期钩子

| 方法 | 调用时机 | 原子操作 |
|------|---------|---------|
| `ConnEstablished()` | ConnectTCP/ConnectUDP 成功 | `established+1`、`active+1` |
| `ConnFailed()` | ConnectTCP/ConnectUDP dial 失败 | `failed+1` |
| `ConnClosed()` | `SetOnClosed` 回调（主动/被动关闭都触发） | `active-1`、`closed+1` |
| `ConnDropped()` | `SetOnDisconnect` 回调（服务端关闭/网络异常） | `dropped+1`（active 由 ConnClosed 统一扣减） |

---

## 15. AddBandwidth — 带宽统计

```go
func (c *MetricsCollector) AddBandwidth(send, recv int64)
```

由 network 层调用（发送 `network/connection.go:457`、接收 `network/gnet.go:253`），含心跳/监听等全部流量；累计面与窗口面同时累加。

**与 per-action 字节统计的关系**：
- `AddBandwidth`：全局统计（network 层上报），含心跳等非动作流量
- `am.sendBytes/recvBytes`：per-action 统计（RecordAction/RecordCallback 中记录），所有已记录结果分支按实际发生的 WireBytes 累计
- `RecordAction` 中**不累加** `totalSendBytes/totalRecvBytes`，避免双计

---

## 16. 控制台 Reporter — `monitor/reporter.go`

### 16.1 Reporter 结构体

```go
type Reporter struct {
    collector    *MetricsCollector // 指标收集器引用
    interval     time.Duration     // 报告间隔
    prevTime     time.Time         // 上次报告时间，用于窗口起止
    nextSequence uint64            // 窗口序号（从 1 递增）
    stopCh       chan struct{}     // 停止信号通道
    stopOnce     sync.Once         // 保证 stopCh 只关闭一次
}
```

（`monitor/reporter.go:13-20`）每周期调 `TakeReportSnapshot`，控制台表格里的 qps 列输出**区间窗口 QPS**（`snap.Window.Actions` 的 AvgQPS）。

### 16.2 输出格式

每 interval（默认 5s）输出一次：

```
[MONITOR] 4m25s | goroutines=16 | mem=12.3MB | gc=5 | total=380
[MONITOR] robots: 1运行 0停止 0错误 | conn: 3建立 0失败 0断连 | 0.5/1.2 MB/s
[MONITOR] 动作                      成功 失败 超时   RTT avg   RTT p95  client apdex exec   qps
[MONITOR] CreateNormalTeam          280    4    0     46ms     120ms     2ms  0.95    2   0.95 tout=2300ms
[MONITOR] errors: CreateNormalTeam→[框架 RECV_TIMEOUT]×26 service=logic elapsed=2.3s（另 2 条）,
                      CreateNormalTeam→[业务 #1004]×15 service=logic route=CreateTeam
```

**四段结构**：
- 第一行：运行时间 + 系统资源 + 总动作数
- 第二行：机器人状态 + 连接健康（建立/失败/断连）+ 带宽
- 表格：每动作一行（成功/失败/超时 + RTT avg/p95 + client + apdex + exec + 窗口 qps；有超时时追加 `tout=` 平均超时延迟；RTT 无样本显示 `--`）
- errors：仅在有失败时追加一行，格式 `动作→[框架/业务 CodeName或#code]×count msg`

### 16.3 错误输出格式

```
actionName→[框架 RECV_TIMEOUT]×count firstMsg       // code<100
actionName→[业务 #1004]×count firstMsg              // code>=100，CodeName 为空时显示 #code
```

多条 Messages 时首条后追加 `（另 N 条）`；消息超 40 字符截断。错误按次数降序排列（`sortedErrors`）。

### 16.4 启停

```go
func NewReporter(c *MetricsCollector, interval time.Duration) *Reporter
func (r *Reporter) Start()   // workpool.Default().GoWithStop 启动定时报告
func (r *Reporter) Stop()    // close(stopCh)，保证只关闭一次
```

Reporter 使用 `internal/workpool` 协程池启动，同时响应自身 stopCh 和协程池的全局停止信号。

---

## 17. HTTP 端点 — `monitor/http.go`

### 17.1 RegisterHandlers

```go
func RegisterHandlers(c *MetricsCollector)
```

将 `/metrics` 和 `/metrics/summary` 注册到 `http.DefaultServeMux`。pprof 由 `config.StartPprofServer` 独立管理（`[pprof]` 配置段），不与监控端点共用端口。

### 17.2 GET /metrics

完整 JSON 快照（经 `PublicCopy()` 剥离 sketch 与内部原始计数），供前端消费：

```json
{
  "timestamp": "2026-09-20T10:30:00+08:00",
  "collectionEpoch": 1,
  "uptime": 150500000000,
  "uptimeSeconds": 150.5,
  "totalActions": 52843,
  "apdexT": 100,
  "timingDetail": "rtt",
  "summary": { "sampleCount": 530, "successRate": 0.99, "rttApdex": 0.97, "rtt": { "count": 520, "p95Ms": 120.3, ... }, ... },
  "system": { "goroutines": 142, "memAllocMB": 12.3, "memSysMB": 45.6, "gcCount": 23 },
  "robots": { "started": 100, "running": 98, "stopped": 0, "errored": 2 },
  "rampUp": { "currentStage": 0, "totalStages": 0 },
  "connections": { "established": 300, "active": 297, "closed": 2, "failed": 0, "dropped": 1 },
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
      "byteSampleCount": 284,
      "kind": "networked",
      "rttApdex": 0.95,
      "rtt": { "count": 280, "minMs": 12.0, "maxMs": 450.6, "avgMs": 45.7, "p50Ms": 38.2, "p90Ms": 78.5, "p95Ms": 120.3, "p99Ms": 450.6 },
      "listenWait": { "count": 0, "minMs": null, ... },
      "listenReadyCount": 0,
      "listenTimeoutCount": 0,
      "listenTimeoutRate": 0,
      "totalDuration": { "count": 284, ... },
      "timeoutAvgMs": 0,
      "nonRTTAvgMs": 1.8,
      "avgQps": 1.89,
      "periodQps": 0,
      "errors": [
        { "code": 4, "codeName": "RECV_TIMEOUT", "msgs": ["service=logic elapsed=2.3s"], "count": 3 }
      ]
    }
  ],
  "invalidMetricSamples": 0
}
```

**前端对接要点**：
- `timestamp` — ISO 8601；`uptimeSeconds` — 数值类型，前端可自行格式化
- `successRate` — 0~1 小数，前端按需显示为百分比
- `rtt` 嵌套对象 — 纯网络往返分布（含业务错误响应的样本）；`rtt.count <= sampleCount`，且空分布的展示值为 `null`
- `kind` — 前端按 kind 选主指标列，非 networked 的 apdex 显示「不适用」
- `periodQps` 在 HTTP 端点为 0（前端轮询两次用 `timestamp` 差值自行计算）
- `errors` 仅在有失败/超时时出现（`omitempty`）
- `errors[].code` — 错误码，前端按 `code < 100` 推导"框架"标签、`code >= 100` 推导"业务"标签并上色
- `errors[].codeName` — 框架错误（code<100）有值（如 `"RECV_TIMEOUT"`），业务错误（code>=100）为 `""`，前端可 fallback 为 `#code`
- 响应经 `PublicCopy()`：不含 `sketch`/`sumNs` 及跨节点聚合原始计数

### 17.3 GET /metrics/summary

文本摘要，供快速检查：

```
uptime: 4m25s
robots: started=100 running=98 stopped=0 errored=2
createTeam: samples=284 success=280 timeout=0 failure=4 avg=45.7ms p99=450.6ms apdex=0.950 qps=1.89
```

### 17.4 StartHTTPServer

```go
func StartHTTPServer(port int) (stop func(), err error)
```

通过 `internal/workpool` 提交非阻塞 HTTP 服务，返回优雅关闭函数（3s 超时 Shutdown + 关 KeepAlive）。

---

## 18. CSV 导出 — `monitor/csv.go`

```go
func ExportCSV(c *MetricsCollector, path string) error
```

### 18.1 表头

```
接口名,样本数,成功次数,超时次数,错误次数,网络样本数,成功率,平均网络往返(ms),客户端开销(ms),
最小网络往返(ms),最大网络往返(ms),P50(ms),P90(ms),P95(ms),P99(ms),Apdex,平均发送字节,平均接收字节,
平均QPS,压测时长(s)
```

"网络延迟"列语义是纯网络往返（不含客户端构建/解析）；`网络样本数` 为 `rttSampleCount`；空分布的直方图列输出空串。

### 18.2 说明

- 单机模式压测结束时写入 `metrics/metrics_<yyyyMMdd_HHmmss>.csv`（`standalone/app.go:217-231`），文件名带时间戳避免覆盖
- 自动创建目录（`os.MkdirAll`）
- 无动作时跳过写入；仅记录 action 级数据（不含系统/机器人/连接/带宽）
- CSV 写入错误不影响主流程（仅记日志）

---

## 19. 配置

### 19.1 TOML 配置（conf/stressbot.toml / conf/agent.toml）

```toml
[monitor]
apdexThresholdMs = 100      # Apdex T 阈值（毫秒）；分布式模式可被 Admin 下发的 apdexT 覆盖
timingDetail = "rtt"        # rtt / codec / full
reportInterval = "5s"       # 控制台摘要间隔（仅单机模式）

[monitor.http]              # 整段省略 = 不启用 HTTP 端点
port = 6061

[monitor.sketch]            # DDSketch 精度（高级调优）
relativeAccuracy = 0.01     # Admin 与所有 Agent 必须一致，否则合并结果错误
maxBins = 2048
```

Agent 模式不启用 monitor HTTP 端点（指标经 gRPC 上报给 Admin）。配置加载由 `config.ParseDuration` 类规则约束：时长写错一律报错，不留静默回退。

---

## 20. Lua 动作的指标

Lua 动作在 `robotActionHandler.ExecuteAction` 中统一采集（`runLuaAction`，`robot/robot.go:775`）：

- **耗时**：`wallClock` 覆盖主流程同步执行脚本的总耗时（进 totalDuration 直方图）；脚本内每次 request-response 产生独立的 `RequestTiming`（WireRTT 样本）与监听等待样本
- **成功/失败**：基于脚本返回（nil / err table / 框架错误）
- **错误分布**：使用结构化 ActionError（ErrLuaNotInit / ErrLuaNoScript / ErrLuaExecFailed；脚本 `robot.error(code, detail)` 构造的 err table 重建后透传，业务断言类失败使用 ErrLuaScriptCheck）
- **字节统计**：脚本内 `network.*` 调用产生的 WireBytes 由 `script.Context` 自动累计
- **executing 计数**：同样适用

---

## 21. 设计说明

### 21.1 HTTP vs Reporter 的 periodQPS

HTTP 端点走 `Snapshot(nil, 0)`，`periodQps=0`。Reporter 与 Agent 上报走 `TakeReportSnapshot` 的区间窗口，窗口 QPS = 窗口样本数 / 窗口时长。前端可通过连续两次 `GET /metrics` 自行计算。

### 21.2 sync.Map 写模式

- `actions sync.Map` 写入仅在首次看到某 action name 时（`LoadOrStore`），后续全是对已存指针的原子字段更新
- `errors sync.Map` 同理，首次写入后只做原子累加 + 环形缓冲原子写入

### 21.3 sampleCount 口径

```
sampleCount = successCount + failureCount + timeoutCount
```

- 不含 `canceledCount`（取消不算样本）
- 不含 skippedCount（实现中无此字段）

### 21.4 全局带宽 vs per-action 字节

- **全局带宽**（`totalSendBytes`/`totalRecvBytes`）：由 network 层的 `AddBandwidth` 统计，含心跳/监听等全部流量
- **per-action 字节**（`am.sendBytes`/`am.recvBytes`）：由 `RecordAction`/`RecordCallback` 统计，所有已记录结果分支按实际发生的 WireBytes 累计
- 两者不重叠：RecordAction 中不累加全局带宽

### 21.5 调优观测端点

以下端点挂在 pprof 端口（`[pprof]` 配置启用后自动注册到 `http.DefaultServeMux`），用于压测中的现场定位：

| 端点 | 实现 | 用途 |
|------|------|------|
| `/debug/sched` | `internal/debughttp/sched.go` | Go 调度延迟分位数，量化施压机负载对 Apdex/P99 的污染（压测中看 `sinceLast` 组） |
| `/debug/statekeys` | `script/statekeys_http.go` | `robot.get/get_path` 按 key 计数，`tables`/`wireDecodes` 列定位整读热点（`?reset=1` 分窗口） |
| `/debug/dedup` | `protocol/protox/dedup_http.go` | 广播 wire 去重缓存命中与钉扎量 |
| `/debug/wire` | `protocol/protox/wireshadow.go` | wire 消费路径影子采样失配统计（失配自动降级该 schema） |
