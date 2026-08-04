# 监控指标准确性与 DDSketch 重构设计

## 背景

当前监控使用 16 个固定延迟桶。它的主要问题不仅是 P50/P90/P95/P99 精度较低，还包括：

- 计数、总和、Min/Max 与桶计数分别使用原子变量，快照可能读取到不同时间点的数据。
- 60 秒以上样本全部进入同一个溢出桶，无法表达长尾分布。
- 当前 QPS、带宽与历史趋势混用了累计平均值和前端差分值。
- 阶段耗时全部除以同一个动作样本数，未采集阶段被当成 0 摊薄。
- 未执行节点的取消补记会污染吞吐、字节均值和错误统计。
- Admin 会聚合已经过期的节点快照；连接数和系统网络速率也存在独立计数误差。

本设计以准确性优先，允许监控热路径付出有限的锁和计算成本，但必须通过分片、容量上限和基准测试控制开销。

## 与已有监控汇总设计的关系

本设计延续 `2026-08-03-monitor-summary-design.md` 中“后端是指标单一数据源”的原则，并替代其中以下内容：

- 不再保留旧节点快照回退或 `metricsVersion` 兼容逻辑。Admin、Agent 和前端同步升级。
- 前端删除“高级诊断转换”交互，只展示任务实际采用的 `timingDetail`。
- 历史趋势改用真实区间指标，不再把累计平均值作为趋势点。
- 固定桶及其跨节点兼容字段由 DDSketch 编码替代。

## 目标

1. P50/P90/P95/P99 在正常延迟范围内提供 1% 相对误差保证。
2. `Count/Sum/Min/Max` 使用与分布相同的样本集合，并保持数学边界一致。
3. 区分累计指标与真实上报区间指标；当前值和历史趋势不得由前端推算。
4. 分布式合并不能使用“平均数的平均数”或“分位数的平均数”。
5. 明确定义取消、错误、字节、Apdex、阶段耗时和连接指标的分母。
6. 上报失败、重复上报、过期节点与无效 sketch 必须可见且不会静默污染结果。
7. 监控 Record 热路径预热后保持零分配，并通过固定容量限制内存。

## 非目标

- 不修改 RTT 和监听等待的测点位置。
- 不改变 Apdex T 的任务级配置方式。
- 不为旧 Agent 或旧固定桶快照提供实时兼容。
- 不在网络中断期间持久化无限量本地区间；进程退出且 Admin 始终不可达时，最终指标仍可能无法送达。
- 不把 DDSketch 原始数据提供给浏览器计算。

## DDSketch 配置

依赖固定为 `github.com/DataDog/sketches-go v1.4.8`。

每个延迟分布使用：

```go
ddsketch.LogCollapsingLowestDenseDDSketch(0.01, 2048)
```

配置含义：

- `0.01`：1% 相对误差。
- `2048`：每个 store 的最大 bins 数量。
- `CollapsingLowest`：超出容量时优先折叠低值，保护压测最关注的高分位。
- 输入值使用纳秒的非负 `float64` 表示，输出时统一转换为毫秒。

1ns 到小时级延迟不会触发 2048 bins 的折叠边界。异常超大范围即使触发折叠，精确 Min/Max 仍由独立统计保留。

## 采集器结构

### 动作分片

每个 `actionMetrics` 固定包含 8 个 `actionShard`。一次动作完成时通过递增序号选择一个分片，并在同一个分片锁内更新：

- Success/Failure/Timeout/Canceled 计数。
- 实际执行样本数和字节样本数。
- 发送/接收字节总量。
- RTT Apdex satisfied/tolerating/failed 原始计数。
- RTT、监听等待、总耗时三个 DDSketch。
- 非 RTT 总和与样本数。
- 各阶段耗时总和及各自样本数。
- 监听 ready、timeout 等辅助计数。

`Executing` 是动作开始与结束之间的实时 gauge，继续使用原子计数，不强行并入完成事件快照。

错误详情环形缓冲继续按错误码维护；用于成功率、错误总数和历史汇总的结果计数来自分片状态，不从错误详情反推。

### 活动区间与累计状态

每个动作分片只保存当前尚未确认上报的活动区间。动作级 `committed` 状态保存已经截取区间的累计结果。

- `Snapshot()`：非破坏性读取 `committed + active`，用于阶段最终值、本地 HTTP 和最终归档。
- `TakeReportSnapshot()`：交换全部活动分片，将旧活动区间合并进 `committed`，并同时返回累计快照与被截取的区间快照。
- 区间交换和普通快照通过动作级 transition mutex 协调；Record 只获取一个分片锁。
- 每个样本只会位于 committed 或某个 active 分片，不会同时存在于两侧。

区间边界在不同动作之间可能有微秒级偏差，但单个动作内的结果、分布和分母是同一批样本，且所有样本只归属一个区间。

### HistogramSnapshot

公开统计包含：

- `count`
- `sumNs`
- `minMs`、`maxMs`、`avgMs`
- `p50Ms`、`p90Ms`、`p95Ms`、`p99Ms`
- Agent 到 Admin 合并所需的 DDSketch 二进制编码

Count、Sum、Min、Max 与 DDSketch 在同一分片锁内更新。分位数计算后限制在精确 `[Min, Max]` 内，修复任何近似值越界；该限制只修复数学边界，不作为解码或合并错误的回退。

DDSketch 使用官方 `Encode` 传输，Admin 使用 `DecodeDDSketch` 和 `MergeWith` 合并。Admin 聚合完成后从面向前端和历史 JSON 的对象中移除原始 sketch 字节。

## 快照契约

### 累计部分

`CollectorSnapshot` 顶层保留累计数据：

- `totalActions`
- `summary`
- `actions`
- `invalidMetricSamples`
- 机器人累计状态
- 连接累计状态
- 总发送/接收字节
- 系统快照

`summary.avgQps = summary.sampleCount / uptimeSeconds`，表示全周期平均吞吐。

### 区间部分

新增 `window`：

```text
sequence
startedAt / endedAt / durationSeconds
expectedIntervalSeconds
summary
qps
bandwidth.sendBytes / recvBytes / sendMBps / recvMBps
actions[]: name / sampleCount / qps
invalidMetricSamples（本区间）
```

Agent 快照的 `sequence` 从 1 开始，在同一 task 内单调递增。Admin 聚合后的公共 window 不暴露单节点 sequence，只暴露实际时间范围、汇总值和覆盖率。

顶层 `invalidMetricSamples` 是任务累计无效样本数，window 中同名字段只表示本区间无效样本数。

前端动作表从累计 `actions` 读取累计质量和延迟，从 `window.actions` 读取当前动作 QPS。实时总览和历史趋势的当前 QPS、带宽、延迟及 Apdex 只读取 `window`。

顶层 Bandwidth 只保留累计字节；速率只属于 window，避免同一字段同时表示累计平均速率和当前速率。

## 指标口径

### 结果与吞吐

```text
SampleCount = SuccessCount + FailureCount + TimeoutCount
TotalActions = sum(Action.SampleCount)
SuccessRate = SuccessCount / SampleCount
```

Canceled 不进入 SampleCount、TotalActions、成功率或 QPS。

执行器对根本没有开始的后续节点调用专用的 `RecordPendingCanceled`：只增加 `CanceledCount`。真实执行后被取消的动作也增加 `CanceledCount`，但其已发生的字节仍可进入实际执行字节统计。

### 字节

`ByteSampleCount` 是实际开始执行并形成记录的动作数，包含成功、失败、超时和真实执行后取消，不包含未执行取消补记。

```text
AvgSendBytes = TotalSendBytes / ByteSampleCount
AvgRecvBytes = TotalRecvBytes / ByteSampleCount
```

前端文案使用“每次已执行动作平均字节”，不再描述成成功动作均值。

### 错误

错误总数和错误排行只使用 Failure + Timeout。Canceled 不算错误；错误结果计数与错误码分布不相加，错误码分布只用于解释这些错误的构成。

### Apdex

只有运行时 Kind 为 `networked` 的请求 RTT 参与 Apdex：

- 收到完整响应帧的请求按真实 RTT 分为 satisfied、tolerating 或 frustrated；业务错误响应不例外。
- 已发起但没有响应帧的失败请求不进入 RTT 分布，但进入 Apdex 分母并记为 frustrated。
- 监听、单向发送和本地动作不参与 Apdex。

Admin 只合并原始 satisfied/tolerating/failed 计数，不合并各节点 Apdex 浮点值。

### 延迟与阶段耗时

RTT、监听等待和总耗时各自维护 DDSketch。Min/Max 不再先截断成整数毫秒。

`ClientAvgMs` 对外更名为 `NonRTTAvgMs`：

```text
NonRTT = max(0, wallClock - sum(WireRTT))
```

它表示非 RTT 剩余耗时，可能包含客户端工作、监听、sleep 和调度等待，不描述成纯客户端 CPU 开销。数据库内部已有列名可以保留，但 API、Go 字段和 UI 文案使用新名称。

`ClientTiming` 增加阶段存在位，区分“未采集”和“实际测得 0ns”。每个阶段使用自己的总和与样本数：

- BuildSampleCount
- EncodeSampleCount
- SendSampleCount
- DecodeWaitSampleCount
- DecodeSampleCount
- DispatchWaitSampleCount
- ParseStoreSampleCount

未采集阶段在 API 中携带 count=0，前端显示 `—`。

### timingDetail

`timingDetail` 保留，默认仍为 `rtt`，它只控制诊断测点开销：

- `rtt`：核心 RTT、发送、总耗时和非 RTT 剩余耗时。
- `codec`：增加编码、解码和解码等待。
- `full`：再增加构建、分发等待、解析和状态写入。

它不改变 Apdex 算法或主延迟口径。前端删除“高级诊断转换”按钮，只展示后端实际采集级别和相应可用列。

## Agent 上报可靠性

Agent 同一时刻最多有一个待确认区间：

1. 到达上报周期后，截取活动区间并赋予新 sequence。
2. POST 失败时保留同一 sequence 和同一数据，后续重试不重新截取。
3. 新样本继续进入新的活动区间。
4. Admin 确认待发送 sequence 后，Agent 才释放它；下一次再截取当前活动区间。

Admin 按 `(taskID, agentID, sequence)` 幂等处理：

- sequence 等于最后已接收值：视为重试，直接返回确认，不重复入队。
- sequence 等于最后值 + 1：校验并接收。
- sequence 出现跳号：拒绝并保留上一份有效状态。

任务停止时先重试待确认区间，再截取并发送最终活动区间。若 Admin 在整个最终 flush 时限内不可达，必须记录“最终指标未确认”，不得宣称归档完整。

## Admin 聚合与历史采样

### 累计聚合

累计 `summary/actions/totalActions` 使用各已分配节点最后一份已确认累计快照。节点暂时过期不会删除已经发生的累计事实，但覆盖率会指出它尚未上报最新值。

### 当前窗口

新鲜度基于 Admin 接收时间，不依赖 Agent 和 Admin 的墙钟同步。阈值：

```text
max(3 * expectedIntervalSeconds, 15s)
```

过期节点不参与当前 window、运行机器人、Executing、活动连接或系统资源聚合。

不同节点 window 时长不同时：

```text
ClusterQPS = sum(agent.window.sampleCount / agent.window.duration)
ClusterBandwidth = sum(agent.window.bytes / agent.window.duration)
```

延迟 DDSketch 和 Apdex 原始计数直接合并全部新鲜节点 window 样本。

### 历史区间队列

Admin 对每个任务保存已经幂等接收、但尚未被 Sampler 消费的节点区间。Sampler 每次按节点分组消费：

- 同一节点多个 window 的计数和 duration 分别求和。
- 节点区间 QPS = 该节点总样本数 / 该节点总 duration。
- 集群 QPS = 各节点区间 QPS 之和。
- 延迟和 Apdex 合并所有被消费区间的 sketch 与原始计数。

这样 Agent 5 秒上报、历史 10 秒采样时不会覆盖中间 window。没有任何已接受区间时，历史指标保存为空而不是 0。

历史点增加 reporting/assigned 覆盖率字段。旧历史行缺少新字段时返回 null/无数据；不引入 `metricsVersion`。

## 连接指标

`ConnectionSnapshot` 明确定义：

- `Active`：当前活动连接 gauge。
- `Established`：累计建立成功次数。
- `Failed`：累计建立失败次数。
- `Closed`：累计所有关闭次数。
- `Dropped`：累计意外断开次数，是 Closed 的子集。

前端直接使用 Active，不再计算 `Established - Dropped`。

连接关闭回调必须支持关闭后注册时立即补调，并通过一次性状态保证建立和关闭只配平一次。`onClosed` 负责 Active/Closed，`onDisconnect` 仅负责 Dropped 和业务断开处理。

## 系统指标

- 系统资源聚合使用与压力 window 相同的新鲜度原则。
- 前端节点 stale 状态直接消费后端结果，不再写死 30 秒判断。
- OS 网络累计计数小于上一值时视为计数器重置。本窗口速率返回空值并重建基线，禁止 uint64 下溢。

## 无效数据与失败策略

- DDSketch 初始化失败：监控初始化失败，任务不得退回固定桶运行。
- 负耗时或 DDSketch Add 失败：该延迟样本不进入分布，增加 `invalidMetricSamples`，结果计数仍按实际动作结果记录。
- sketch 解码失败、mapping 不一致或 Apdex T 不一致：Admin 拒绝本次上报，保留上一份有效累计快照，节点不计入当前覆盖率。
- Agent 对确定性无效上报限频记录中文错误并保留待确认区间，不静默丢弃。
- 不存在旧桶回退、分位数回退或前端二次计算。

## 前端改动

- TypeScript 类型逐字段对齐新的累计和 window 契约。
- 实时 KPI 直接读取 `window.qps`、window bandwidth 和 `window.summary`。
- 全周期平均 QPS 读取累计 `summary.avgQps`。
- 动作表当前 QPS 从 `window.actions` 按名称关联。
- 删除 `deriveIntervalQps`、浏览器 stale 时间判断和“高级诊断转换”状态。
- 延迟、阶段和覆盖率缺少样本时显示 `—`，不能将 null 显示为 0。
- 历史峰值从真实区间点取 max。
- 错误排行不再加入 canceled，也不重复叠加错误码分布。

## 测试设计

### DDSketch

- 已知分布覆盖 1ns、亚毫秒、秒级、超过 60 秒和小时级。
- P50/P90/P95/P99 相对误差不超过 1%。
- Count/Sum/Min/Max 与输入一致，所有分位数位于 `[Min, Max]`。
- 单 sketch 与拆分后多 sketch 合并结果一致。
- 强制接近 bins 上限，验证高分位仍受保护。

### 并发与区间

- 并发 Record/Snapshot/TakeReportSnapshot 通过 race 测试。
- 连续截取区间后，累计样本等于所有区间加当前活动样本，不丢失、不重复。
- Snapshot 不会消耗区间。
- `MetricsCollector.Reset` 清空累计和活动区间，但不重置 Reporter sequence；同一 task 的 sequence 始终单调递增，新 task 创建新 Reporter 后才从 1 开始。

### 指标语义

- 未执行取消与真实取消分别验证。
- 字节分母、阶段独立分母和 0ns 已采集阶段分别验证。
- 业务错误 RTT、无响应 frustrated、监听不参与 Apdex 分别验证。
- NonRTT 负差值按 0 截断并使用独立样本数。

### 分布式与可靠性

- Agent 相同 sequence 重试不重复合并。
- 跳号、无效 sketch、mapping 和 Apdex T 不一致被拒绝。
- 不同节点 window duration 的 QPS、带宽、DDSketch 和 Apdex 合并正确。
- 过期节点只退出当前指标，不删除累计事实。
- 历史 Sampler 恰好消费每个已接收 window 一次。

### 连接、系统与前端

- 快速关闭、关闭后注册、重复关闭、正常关闭和意外断开计数正确。
- OS 网络计数重置不会产生负值或异常大速率。
- Vitest 验证前端只映射后端 window、null 状态、后端 stale、阶段列和历史峰值。

## 性能与内存验收

- `RecordAction` 在分片和 store 预热后保持 `0 allocs/op`。
- 提供 1、8、32 并发写入 benchmark，检查 8 分片锁竞争。
- 每个 DDSketch store 上限 2048 bins；窗口交换复用已分配 store，避免每周期重新分配。
- 8000 机器人运行验证中检查监控 CPU、mutex profile 和内存。准确性优先，但监控不得成为主要 CPU 热点。

## 验证流程

1. 使用仓库内 GOCACHE 执行 `go build ./...`。
2. 执行 monitor/admin/agent 聚焦测试和 race 测试。
3. 执行 Go 全量测试。
4. 在 `cmd/web` 执行 `npx tsc -b` 和 `npm run test`。
5. 使用前端编辑器校验 flow.json 无错误。
6. 启动 Agent 运行 2 到 5 分钟，覆盖实际 RTT、监听、取消和连接关闭路径。
7. 审查日志，无未解释的 error、warn 或失败记录。
8. 记录 DDSketch benchmark、8000 机器人 CPU/mutex/内存对比结果。

## 实施边界

本次改动限定在 monitor、Agent 指标上报、Admin 聚合/采样/历史投影、连接与系统计数、监控前端类型和展示。不会修改业务 flow、Lua 脚本、协议 codec 或 RTT 测点。
