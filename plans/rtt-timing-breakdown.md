# RTT 计时拆分设计

## 背景与目标

压测工具最核心的指标是服务端往返时间。当前请求动作的 `netLatency` 语义更接近“发送完成到业务动作拿到已解码响应”的等待窗口，其中可能混入客户端侧 decode、channel 分发、调度等待等工具自身耗时。

目标是把客户端可观测的服务端往返时间从工具端耗时中拆出来：

- RTT 只统计客户端发包完成到客户端收到完整响应帧的时间。
- encode、decode、proto parse、store、Lua 执行、channel 分发等待等全部作为客户端侧耗时独立统计。
- request-response、listen、send-only、connect、heartbeat 使用不同计时语义，避免不同类型动作互相污染。

严格意义的服务端处理耗时需要服务端在协议中提供时间戳。当前客户端单边能准确统计的是 client-observed wire RTT：

```text
WireRTT = 客户端 Send 返回 -> 客户端收到完整响应帧
```

该时间包含网络往返、服务端排队和服务端处理，不包含客户端 decode/parse/store。

## 时间点定义

一次 request-response 动作拆分为以下时间点：

```text
T0  actionStart
T1  buildStart
T2  buildEnd
T3  encodeStart
T4  encodeEnd
T5  sendStart
T6  sendDone
T7  recvFrameAt
T8  decodeStart
T9  decodeEnd
T10 dispatchStart
T11 actionUnblocked
T12 parseStoreDone
T13 actionEnd
```

| 时间点 | 含义 |
|---|---|
| `T0 actionStart` | 动作开始 |
| `T1 buildStart` | 开始绑定字段、构建 proto、序列化 body |
| `T2 buildEnd` | proto body 构建和序列化完成 |
| `T3 encodeStart` | 开始调用 adapter encode |
| `T4 encodeEnd` | 得到完整 packet |
| `T5 sendStart` | 开始调用 network send |
| `T6 sendDone` | send 返回，客户端认为请求已交给网络层 |
| `T7 recvFrameAt` | gnet 收到完整响应帧，尚未 decode |
| `T8 decodeStart` | decodeLoop 开始 decode |
| `T9 decodeEnd` | decode 完成，得到 routeKey/body/headerErr |
| `T10 dispatchStart` | 开始向 response channel 或 listen queue 投递 |
| `T11 actionUnblocked` | action goroutine 从 channel 收到响应 |
| `T12 parseStoreDone` | S2C proto parse + store 完成 |
| `T13 actionEnd` | 动作结束 |

## 指标语义

核心 RTT：

```text
WireRTT = T7 - T6
```

工具端耗时：

```text
BuildCost       = T2 - T1
EncodeCost      = T4 - T3
SendCost        = T6 - T5
DecodeQueueWait = T8 - T7
DecodeCost      = T9 - T8
DispatchToActionWait = T11 - T10
ParseStoreCost  = T12 - T11
ActionWall      = T13 - T0
ClientCost      = ActionWall - WireRTT
```

`ClientCost` 是客户端总开销兜底指标，细分项用于定位瓶颈。

RTT 直方图、P50/P90/P95/P99、Apdex 必须只使用 `WireRTT`。

## 网络层设计

### 入站帧结构

新增入站帧结构，用于在 gnet 收到完整 packet 时携带时间戳到 decodeLoop：

```go
type inboundFrame struct {
    Data        []byte
    RecvFrameAt time.Time
}
```

gnet 拆出完整帧时记录：

```go
recvFrameAt := time.Now()
decodeCh <- inboundFrame{Data: packet, RecvFrameAt: recvFrameAt}
```

`decodeCh` 从 `chan []byte` 改成 `chan inboundFrame`。

### Message timing

扩展 `network.Message`：

```go
type MessageTiming struct {
    RecvFrameAt   time.Time
    DecodeStart   time.Time
    DecodeEnd     time.Time
    DispatchStart time.Time
}

type Message struct {
    RouteKey  string
    Data      []byte
    HeaderErr uint64
    Timing    MessageTiming
}
```

decodeLoop 中记录：

```go
decodeStart := time.Now()
routeKey, body, headerErr := adapter.DecodeTCP(frame.Data, key)
decodeEnd := time.Now()

msg := &Message{
    RouteKey: routeKey,
    Data: body,
    HeaderErr: headerErr,
    Timing: MessageTiming{
        RecvFrameAt: frame.RecvFrameAt,
        DecodeStart: decodeStart,
        DecodeEnd: decodeEnd,
    },
}
```

### Request timing

新增请求级 timing：

```go
type RequestTiming struct {
    SendCost             time.Duration
    WireRTT              time.Duration
    DecodeWait           time.Duration
    DecodeCost           time.Duration
    DispatchToActionWait time.Duration
}
```

`Connection.RequestResponse` 返回值调整为：

```go
func (c *Connection) RequestResponse(...) (*Message, RequestTiming, error)
```

发送侧记录：

```go
sendStart := time.Now()
n, err := c.Send(packet)
sendDone := time.Now()
```

收到响应后记录：

```go
actionUnblocked := time.Now()

timing.WireRTT = msg.Timing.RecvFrameAt.Sub(sendDone)
timing.DecodeWait = msg.Timing.DecodeStart.Sub(msg.Timing.RecvFrameAt)
timing.DecodeCost = msg.Timing.DecodeEnd.Sub(msg.Timing.DecodeStart)
timing.DispatchToActionWait = actionUnblocked.Sub(msg.Timing.DispatchStart)
timing.SendCost = sendDone.Sub(sendStart)
```

所有 duration 计算应通过 helper 做负值保护，遇到零时间点返回 0。

### 分发侧记录

`Connection.OnReceive` 投递 response channel 或 listen queue 时记录 `DispatchStart`。

不使用 `DispatchDone` 作为核心指标，原因是 channel send 的“写入成功时间”只有 send 返回后才知道；如果发送后再修改已投递的 `Message`，接收方可能已经读取，容易形成竞态或语义不稳定。

统一使用：

```text
DispatchToActionWait = actionUnblocked - DispatchStart
```

该值表示“从开始向 channel/queue 分发，到 action goroutine 真正拿到响应”的总工具端等待。它包含：

- channel 满时的发送阻塞；
- channel 已写入后，action goroutine 被调度并接收的等待。

伪代码：

```go
resp.Timing.DispatchStart = time.Now()
ch <- resp
```

`RequestResponse` 收到响应后记录 `actionUnblocked := time.Now()`，并计算 `DispatchToActionWait`。

对于非阻塞 channel，若 channel 满导致丢弃，应保留现有 warn，并补充 service/robot/len/cap 信息。

## ActionTiming 设计

当前 `engine.ActionTiming` 应从单一 `NetLatency` 迁移到分层结构。

建议：

```go
type RequestTiming struct {
    SendCost             time.Duration
    WireRTT              time.Duration
    DecodeWait           time.Duration
    DecodeCost           time.Duration
    DispatchToActionWait time.Duration
}

type ClientTiming struct {
    BuildCost      time.Duration
    EncodeCost     time.Duration
    ParseStoreCost time.Duration
    OtherCost      time.Duration
}

type ActionTiming struct {
    Requests []RequestTiming
    Client   ClientTiming
}
```

`Requests` 保存本动作产生的每一次 request-response 计时。声明式 request 通常只有 1 个元素；Lua 脚本可能有多个元素；send-only/connect/heartbeat/listen 为空。

RTT 直方图必须逐个记录 `Requests[i].WireRTT`，不能把多次 request 的 RTT 累加成一个值再塞入直方图。

## 声明式 request 计时

声明式 `tcpRequest/udpRequest` 的流程：

```text
build proto body
encode packet
send and wait response
parse S2C proto and store
```

计时伪代码：

```go
buildStart := time.Now()
body, err := ae.buildRequestBody(def)
buildEnd := time.Now()

encodeStart := time.Now()
packet := ae.protocolEncode(protocol, def.Route, body, secretKey)
encodeEnd := time.Now()

resp, reqTiming, err := ae.protocolRequest(protocol, def.Service, packet, routeKey, timeout...)

parseStart := time.Now()
err = ae.parseAndStoreResponse(def, resp.Data)
parseEnd := time.Now()

timing := ActionTiming{
    Requests: []RequestTiming{reqTiming},
    Client: ClientTiming{
        BuildCost:      buildEnd.Sub(buildStart),
        EncodeCost:     encodeEnd.Sub(encodeStart),
        ParseStoreCost: parseEnd.Sub(parseStart),
    },
}
```

失败语义：

- encode 失败：`Requests` 为空，记录 encode/client error。
- send 失败：`Requests` 为空，记录 send cost，但不进 RTT。
- timeout：没有 `recvFrameAt`，`Requests` 为空，单独记录 timeout 耗时。
- headerErr 非零但收到响应帧：`Requests` 有 1 个元素，RTT 仍可记录，错误进入 error map。

## Lua request 计时

Lua API 里的 `network.tcp_request/udp_request/http_request` 当前通过 `script.Context` 累加网络时间。这里不能只累加 `WireRTT` 总和，否则 Lua 脚本里多次 request 会把“总 RTT”当成单个直方图样本，导致 P95/P99 严重失真。

必须保留每一次 request 的独立 `RequestTiming` 样本：

```go
type AccumulatedTiming struct {
    Requests []RequestTiming

    EncodeCostNs atomic.Int64
    ParseCostNs  atomic.Int64
}
```

如果担心高频 Lua request 产生 slice 分配，可以后续使用对象池或小数组优化，但语义上必须是一请求一样本。

Lua `network.tcp_request` 中：

```go
encodeStart := time.Now()
packet := ctx.Adapter.EncodeTCPLocked(...)
ctx.recordEncode(time.Since(encodeStart))

resp, reqTiming, err := ctx.NetSender.TCPRequest(...)
ctx.appendRequestTiming(reqTiming)
```

Lua 脚本整体 wallClock 仍由 robotActionHandler 记录。脚本结束后从 `RuntimePool.RunActionScript` 返回结构化 `ActionTiming`，其中 `Requests` 包含脚本内每一次 request 的独立 RTT 样本。

## listen 计时

listen 没有明确的 client sendDone，不能计算 RTT。

对于 `tcpListen/udpListen`，建议统计：

```text
ListenWait = action 开始等待 -> 收到完整帧
DecodeWait
DecodeCost
DispatchToActionWait
```

但：

```text
Requests 为空
```

listen 不进入 RTT 直方图，避免把推送等待时间混入服务端 request-response RTT。

如果未来需要展示 listen 指标，应单独增加 `ListenWait` 列或图表。

## send-only / connect / heartbeat

### send-only

`tcpSend/udpSend` 没有响应，不能计算 RTT。

可统计：

```text
EncodeCost
SendCost
ClientCost
Requests 为空
```

### connect

connect 可统计连接耗时，但不算 RTT：

```text
ConnectCost
Requests 为空
```

### heartbeat

后台心跳不属于某个 action，不进入 action RTT。

可后续增加全局心跳指标：

```text
heartbeatEncodeCost
heartbeatSendCost
heartbeatSkipped
```

但不得污染业务 action RTT。

## 接口迁移影响面

RTT 拆分会跨越 network、robot、engine、script、monitor、admin/front-end DTO，不是单点改动。需要按编译链路自底向上迁移。

主要影响接口：

- `network.Connection.RequestResponse`：返回 `RequestTiming`。
- `network.Message` / decode queue：携带 `MessageTiming`。
- `engine.NetSender.TCPRequest/UDPRequest`：透传 `RequestTiming`。
- `engine.ActionExecutor.protocolRequest`：接收并组装 `ActionTiming.Requests`。
- `script.Context` / Lua `network.tcp_request/udp_request`：保存每次 request 的独立 `RequestTiming`，不能只保存累加值。
- `script.RuntimePool.RunActionScript`：返回包含多 request 样本的 `ActionTiming`。
- `robotActionHandler.ExecuteAction`：把结构化 timing 传给 monitor。
- `monitor.MetricsCollector.RecordAction`：逐个 RTT 样本写直方图。
- `monitor.ActionSnapshot` / admin 聚合 / 历史归档 / 前端表格：字段从 netLatency 迁移到 RTT 和客户端分项。

迁移原则：

1. 不保留旧字段兼容期，不增加自动迁移、fallback 或双字段映射。
2. 底层 `RequestResponse` 开始返回 `RequestTiming` 后，编译驱动逐层改调用方。
3. Monitor、Admin DTO、历史归档和前端类型一次性从 `netLatency` / `netSampleCount` / `latency` 切换到 `WireRTT` / `rttSampleCount` / `rtt`。
4. 前端、历史详情、报告和导出只读取新字段；旧历史数据不做兼容展示。

## Monitor 设计

### RecordAction 入参

建议调整为：

```go
func (c *MetricsCollector) RecordAction(
    name string,
    result ActionResult,
    timing engine.ActionTiming,
    wallClock time.Duration,
    sendBytes, recvBytes int,
    err error,
)
```

Monitor 内部计算：

```go
for _, req := range timing.Requests {
    if req.WireRTT <= 0 {
        continue
    }
    am.rtt.Record(req.WireRTT)
    am.rttSampleCount.Add(1)
}

clientCost := wallClock - sumWireRTT(timing.Requests)
```

注意 `clientCost` 需要负值保护。Lua 动作可能包含多次 request，`clientCost` 应减去所有 request 的 WireRTT 总和；RTT 直方图则逐个记录 request 样本。

### ActionSnapshot 字段

一次性替换为新字段：

```go
type ActionSnapshot struct {
    Name string

    SampleCount   uint64
    SuccessCount  uint64
    FailureCount  uint64
    TimeoutCount  uint64
    CanceledCount uint64
    Executing     int64

    RTTSampleCount uint64
    RTT            LatencySnapshot
    Apdex          float64
    AvgQPS         float64

    ClientAvgMs               float64
    BuildAvgMs                float64
    EncodeAvgMs               float64
    SendAvgMs                 float64
    DecodeWaitAvgMs           float64
    DecodeAvgMs               float64
    DispatchToActionWaitAvgMs float64
    ParseStoreAvgMs           float64

    AvgSendBytes float64
    AvgRecvBytes float64
    TimeoutAvgMs float64
    Errors       []ErrorEntry
}
```

删除旧命名：

```text
netLatency
netSampleCount
latency
netAvg
```

对外 JSON 使用：

```text
rttSampleCount
rtt
clientAvgMs
buildAvgMs
encodeAvgMs
sendAvgMs
decodeWaitAvgMs
decodeAvgMs
dispatchToActionWaitAvgMs
parseStoreAvgMs
```

## 前端展示建议

### 类型字段

`cmd/web/src/types/api.ts` 中 `ActionMetric` 直接切换到新字段：

```ts
export interface ActionMetric {
  name: string;
  sampleCount: number;
  successCount: number;
  failureCount: number;
  timeoutCount: number;
  canceledCount: number;
  executing: number;
  successRate: number;
  apdex: number;
  avgQps: number;
  avgSendBytes: number;
  avgRecvBytes: number;
  timeoutAvgMs: number;

  rttSampleCount: number;
  rtt: HistogramView;

  clientAvgMs: number;
  buildAvgMs: number;
  encodeAvgMs: number;
  sendAvgMs: number;
  decodeWaitAvgMs: number;
  decodeAvgMs: number;
  dispatchToActionWaitAvgMs: number;
  parseStoreAvgMs: number;

  errors?: ErrorEntry[];
}
```

删除：

```text
netSampleCount
latency
```

### 实时动作表

`cmd/web/src/components/monitoring/tabs/ActionsTab.tsx` 建议列：

```text
动作 | 样本 | 成功 | 失败 | 超时 | 取消 | RTT avg | RTT p95 | RTT p99 | client | encode | decode | parse/store | 发送 | 接收 | 并发 | QPS | Apdex | 错误
```

展示规则：

- RTT 列读取 `rtt.avgMs` / `rtt.p95Ms` / `rtt.p99Ms`。
- RTT 列仅在 `rttSampleCount > 0` 时显示数值，否则显示 `—`。
- Apdex 使用 `rttSampleCount` 判断是否有有效 RTT 样本。
- 删除 `net avg(ms)` 文案，统一显示 `RTT avg(ms)`。
- `callback:*` 推送行默认可以继续通过“仅展示动作”隐藏；推送行没有 RTT 时显示 `—`，不做特殊兼容逻辑。

推荐 tooltip：

```text
RTT：从客户端请求发送完成，到客户端收到完整响应帧；不包含客户端解码、解析和状态写入耗时。
client：压测工具端平均开销，约等于动作总耗时扣除 RTT 后的客户端处理时间。
encode：协议编码平均耗时。
decode：收到完整响应帧后的协议解码平均耗时，不计入 RTT。
parse/store：响应 protobuf 解析与状态写入平均耗时。
```

### 趋势图

`cmd/web/src/components/monitoring/tabs/TrendsTab.tsx` 增加“RTT 与客户端成本”趋势卡片。

实时滑动窗口可从每个 snapshot 的 actions 聚合：

```text
RTT p95：按动作用 rttSampleCount 加权聚合，或第一阶段使用所有动作 RTT p95 的最大值表示尾延迟风险。
client avg：按 sampleCount 加权平均 clientAvgMs。
encode avg：按 sampleCount 加权平均 encodeAvgMs。
decode avg：按 rttSampleCount 加权平均 decodeAvgMs。
```

视觉建议：

- RTT p95 使用主色和更粗线宽，作为核心性能线。
- client / encode / decode 使用较细辅助线。
- 图标题使用“RTT 与客户端成本”，不要写“服务端耗时”。

### 历史详情与报告

以下位置与实时动作表共用字段语义：

- `cmd/web/src/components/modules/history/HistoryDetailView.tsx`
- `cmd/web/src/components/modules/history/report/ReportHtml.tsx`
- `cmd/web/src/components/modules/history/report/reportCharts.ts`

要求：

- 历史动作表使用 `rtt` / `rttSampleCount`。
- 报告中的动作表同步显示 `RTT avg/p95/p99 + client/encode/decode/parse-store`。
- 历史趋势点如需要展示 RTT 趋势，新增字段：

```ts
export interface HistoryTrendPoint {
  sampledAt: string;
  elapsedSec: number;
  totalQps: number;
  apdex: number;

  rttAvgMs: number;
  rttP95Ms: number;
  rttP99Ms: number;
  clientAvgMs: number;
  encodeAvgMs: number;
  decodeAvgMs: number;

  botsRunning: number;
  botsErrored: number;
  sendKBps: number;
  recvKBps: number;
  avgCpuPercent: number;
  maxCpuPercent: number;
  memPercent: number;
  goroutines: number;
  threads: number;
  fds: number;
  onlineCount: number;
  offlineCount: number;
}
```

不读取旧历史中的 `latency` 或 `netSampleCount`，旧记录字段缺失时按后端返回结果展示。

## 命名迁移

现有 `netLatency` 容易误导，应迁移为：

```text
WireRTT
```

前端文案可以显示为“服务端 RTT”，tooltip 解释：

```text
从客户端请求发送完成，到客户端收到完整响应帧；不包含客户端解码、解析和状态写入耗时。
```

内部代码避免使用 `serverLatency`，因为没有服务端时间戳时无法证明是纯服务端处理时间。

## 计时开销控制

高并发压测下，`time.Now()` 本身也有成本，不能为了拆分指标在每个请求上无条件记录所有 14 个时间点。

### 最小必需时间点

阶段 1 只为修正 RTT 核心语义，默认只记录 request-response 必需的 4 个时间点：

```text
sendStart   // 计算 SendCost，可选但便宜且有诊断价值
sendDone    // RTT 起点
recvFrameAt // RTT 终点，必须在完整帧形成时记录
actionEnd / wallClock 已由动作层已有计时覆盖
```

如果只追求 RTT，最小集合甚至可以是：

```text
sendDone
recvFrameAt
```

但建议保留 `sendStart`，因为 send 阻塞能解释本机网络栈压力。

### 细分计时按配置启用

细分项应按诊断价值分层控制。第二阶段优先增加 encode/decode，因为它们最容易污染“服务端 RTT”的判断，也是压测工具端最关键的协议处理成本。

```go
type TimingDetailLevel int

const (
    TimingRTTOnly TimingDetailLevel = iota // 默认：只记录 WireRTT + SendCost
    TimingCodecDetail                      // 增加 EncodeCost/DecodeWait/DecodeCost
    TimingFullDetail                       // 增加 DispatchToActionWait/Build/ParseStore 等完整客户端细分
)
```

默认生产压测使用 `TimingRTTOnly`，避免每个请求额外 10+ 次 `time.Now()`。

### 各等级记录点

`TimingRTTOnly`：

```text
T5 sendStart
T6 sendDone
T7 recvFrameAt
```

`TimingCodecDetail` 额外记录：

```text
T3 encodeStart
T4 encodeEnd
T8 decodeStart
T9 decodeEnd
```

可计算：

```text
EncodeCost = T4 - T3
DecodeWait = T8 - T7
DecodeCost = T9 - T8
```

`TimingFullDetail` 额外记录：

```text
T1 buildStart
T2 buildEnd
T10 dispatchStart
T11 actionUnblocked
T12 parseStoreDone
T13 actionEnd
```

### 性能原则

- `RecvFrameAt` 是 RTT 准确性的关键，必须保留。
- encode/decode 是第二优先级诊断指标，用于判断协议编解码是否成为压测瓶颈。
- dispatch/build/parse-store 是完整客户端诊断指标，默认不应强制开启。
- 细分计时关闭时，对应 duration 置 0，不影响 RTT 直方图。
- 配置可放在 monitor 配置下，例如 `monitor.timingDetail: "rtt" | "codec" | "full"`。
- 实现时避免在热路径中构造复杂对象；`RequestTiming` 使用值类型，Lua 多 request 使用 slice 追加，后续可按需池化。

## 实施步骤

### 阶段 1：后端计时模型落地

1. 在 `network` 包新增 `inboundFrame`、`MessageTiming`、`RequestTiming` 和 duration 安全计算 helper。
2. 将 `Connection.decodeCh` 从 `chan []byte` 改为 `chan inboundFrame`。
3. gnet 收到完整帧时立即记录 `RecvFrameAt`，随 packet 投递到 decodeLoop。
4. decodeLoop 在配置允许时记录 `DecodeStart` / `DecodeEnd`，并写入 `Message.Timing`。
5. `Connection.OnReceive` 投递 response channel 或 listen queue 前记录 `DispatchStart`。
6. `Connection.RequestResponse` 记录 `sendStart` / `sendDone`，返回 `(*Message, RequestTiming, error)`。
7. timeout、send error、context cancel 等无完整响应帧的分支不产生 RTT 样本。

### 阶段 2：engine / robot / script 调用链迁移

1. `engine.NetSender.TCPRequest/UDPRequest` 改为透传 `network.RequestTiming`。
2. `engine.ActionTiming` 改为 `Requests []RequestTiming + Client ClientTiming`。
3. 声明式 `tcpRequest/udpRequest` 记录 build、encode、parse/store 成本，并把单次 `RequestTiming` 放入 `ActionTiming.Requests`。
4. `tcpSend/udpSend` 只记录 encode/send/client 成本，不写入 `Requests`。
5. `tcpListen/udpListen` 不写入 `Requests`，避免推送等待污染 RTT。
6. Lua `network.tcp_request/udp_request/http_request` 将每次 request 的 `RequestTiming` 追加到脚本上下文，不能累加成单个 RTT。
7. `script.RuntimePool.RunActionScript` 返回完整 `engine.ActionTiming`。
8. `robotActionHandler.ExecuteAction` 以 wall clock + `ActionTiming` 调用 monitor。

### 阶段 3：Monitor / Admin DTO / 历史归档替换字段

1. `monitor.MetricsCollector.RecordAction` 改为接收 `engine.ActionTiming` 和 `wallClock`。
2. 对 `timing.Requests` 逐个记录 `WireRTT` 到 RTT 直方图。
3. `ClientCost = wallClock - sum(WireRTT)`，负值保护后累加到 client 平均值。
4. 累加 build、encode、send、decodeWait、decode、dispatch、parse/store 平均值。
5. `monitor.ActionSnapshot` 删除 `latency` / `netSampleCount`，新增 `rtt` / `rttSampleCount` 和客户端分项字段。
6. Admin 聚合逻辑同步合并新字段，RTT 直方图按样本聚合，客户端平均值按对应样本数加权。
7. 历史 final snapshot、agent report、timeseries 结构同步存储新字段。
8. 历史归档不做旧字段迁移；旧记录如缺新字段，由后端当前 DTO 行为决定展示结果。

### 阶段 4：前端实时监控适配

1. 更新 `cmd/web/src/types/api.ts`：`ActionMetric` 删除 `latency` / `netSampleCount`，新增 `rtt` / `rttSampleCount` 和分项字段。
2. 更新 `ApdexCell` 入参命名，从 `netSampleCount` 改为 `rttSampleCount`。
3. 更新 `ActionsTab.tsx`：所有延迟列读取 `rtt.*`，表头从 `net avg(ms)` 改成 `RTT avg(ms)`。
4. 动作表增加 `encode(ms)`、`decode(ms)`、`parse/store(ms)` 列；保留 `client(ms)`。
5. 所有 RTT 列以 `rttSampleCount > 0` 判断是否显示数值。
6. 更新 tooltip 文案，明确 RTT 不包含客户端解码、解析和状态写入。
7. 更新 `TrendsTab.tsx`，新增“RTT 与客户端成本”趋势卡片。
8. 前端不写 `?? oldField`、不读旧字段、不做兼容 fallback。

### 阶段 5：历史详情与报告适配

1. 更新 `HistoryDetailView.tsx` 动作表列，与实时动作表同字段同文案。
2. 更新 `HistoryTrendPoint` 类型，新增 RTT 和客户端成本趋势字段。
3. 更新历史趋势图，增加 RTT p95 / client / encode / decode 展示。
4. 更新 `ReportHtml.tsx` 的报告表格，展示 RTT avg/p95/p99 和客户端分项。
5. 更新 `reportCharts.ts`，报告图表使用新趋势字段。
6. 旧字段全部删除，不保留 `net avg`、`latency`、`netSampleCount` 展示逻辑。

### 阶段 6：验证

1. 运行 `go build ./...`。
2. 运行 `cd cmd/web && npx tsc -b`。
3. 运行 `cd cmd/web && npm run test`。
4. 启动 Admin + 前端，检查动作表显示 `RTT avg/p95/p99` 和客户端分项列。
5. 执行一次有 request-response 的任务，确认 RTT 样本数大于 0。
6. 执行一次 listen 或 send-only 行为，确认 RTT 列显示 `—`，且不会进入 Apdex。
7. 打开历史详情和报告，确认没有 `net avg`、`latency`、`netSampleCount` 文案或字段引用。

## 风险与注意事项

- `RecvFrameAt` 必须在 gnet 完整帧形成时记录，不能在 decodeLoop 记录，否则仍会包含 decode 队列等待。
- `WireRTT` 可能因为系统调度、gnet 事件循环延迟而略大于真实网络 RTT，但不会包含 Lua decode/parse/store。
- `sendDone` 是 Go send 调用返回时间，不等于 NIC 真实发出时间；这是客户端侧最稳定可获得边界。
- `time.Now()` 不是零成本；默认只启用 RTT 必需时间点，完整细分计时必须通过配置开启。
- decode 队列满、response channel 满时应保留错误/告警，但不要让这些等待污染 RTT。
- 不做旧字段兼容；前端和历史归档必须与后端 DTO 同步切换到 `rtt` / `rttSampleCount` 等新字段。

## 最终语义

```text
RTT / WireRTT:
  sendDone -> recvFrameAt
  核心服务端往返指标，不含客户端 decode/parse/store。

ClientCost:
  actionWall - WireRTT
  压测工具端总体开销。

Encode/Decode/Parse/Dispatch:
  ClientCost 的可解释子项。
```
