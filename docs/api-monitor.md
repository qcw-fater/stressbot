# 监控指标 HTTP API 文档

## 概述

stressbot 运行时监控系统通过 HTTP 接口对外暴露实时指标数据，供前端仪表盘轮询消费。

- **传输方式**：HTTP 轮询（推荐间隔 = `reportInterval`，默认 5s）
- **默认端口**：`6060`（与 pprof 共用，可通过 `monitor.httpPort` 配置）
- **数据格式**：JSON（`/metrics`）或纯文本（`/metrics/summary`）
- **字符编码**：UTF-8

## 端点列表

| 端点 | 方法 | Content-Type | 说明 |
|---|---|---|---|
| `/metrics` | GET | `application/json` | 完整监控快照（前端主数据源） |
| `/metrics/summary` | GET | `text/plain; charset=utf-8` | 纯文本摘要（调试/控制台） |
| `/debug/pprof/` | GET | HTML | Go pprof 性能分析（内置） |
| `/debug/pprof/profile` | GET | `application/octet-stream` | CPU profile |
| `/debug/pprof/heap` | GET | `application/octet-stream` | 堆内存 profile |

---

## GET /metrics

返回完整监控快照 JSON。**前端主数据源**，建议轮询间隔 3~5 秒。

### 请求

无参数。

### 响应

**Status**: `200 OK`

#### 顶层结构

```json
{
  "timestamp": "2026-04-28T10:30:00+08:00",
  "uptime": 150500000000,
  "uptimeSeconds": 150.5,
  "totalActions": 52843,
  "apdexT": 100,
  "system": { ... },
  "robots": { ... },
  "connections": { ... },
  "bandwidth": { ... },
  "actions": [ ... ]
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `timestamp` | string (ISO 8601) | 快照生成时间，含时区 |
| `uptime` | integer (nanoseconds) | 运行时长（Go Duration 纳秒），前端可忽略此字段，用 `uptimeSeconds` |
| `uptimeSeconds` | float | 运行时长（秒），前端自行格式化为 `HH:MM:SS` |
| `totalActions` | integer | 全局累计动作执行次数（含 success/failure/timeout，不含 skipped） |
| `apdexT` | integer | 当前 Apdex T 阈值（毫秒），用于前端解释 apdex 值 |

---

#### system — 系统资源

```json
{
  "goroutines": 142,
  "memAllocMB": 45.2,
  "memSysMB": 78.6,
  "gcCount": 12
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `goroutines` | integer | 当前 goroutine 数量 |
| `memAllocMB` | float | 已分配堆内存（MB） |
| `memSysMB` | float | 从 OS 申请的总内存（MB） |
| `gcCount` | integer | GC 完成次数 |

---

#### robots — 机器人状态

```json
{
  "started": 100,
  "running": 98,
  "stopped": 0,
  "errored": 2
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `started` | integer | 累计启动数量 |
| `running` | integer | 当前在线数量 |
| `stopped` | integer | 正常停止数量 |
| `errored` | integer | 异常退出数量 |

恒等关系：`started = running + stopped + errored`

---

#### connections — 连接指标

```json
{
  "established": 196,
  "failed": 4,
  "dropped": 2
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `established` | integer | 成功建立的 TCP/UDP 连接总数（每个机器人可能有多条连接） |
| `failed` | integer | 连接失败次数（DNS 解析失败、拒绝连接等） |
| `dropped` | integer | 运行中断连次数（服务器主动断开、网络中断等） |

---

#### bandwidth — 全局带宽

```json
{
  "totalSendBytes": 5242880,
  "totalRecvBytes": 10485760,
  "sendMBps": 0.53,
  "recvMBps": 1.07
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `totalSendBytes` | integer | 全局累计发送字节数（仅成功动作） |
| `totalRecvBytes` | integer | 全局累计接收字节数（仅成功动作） |
| `sendMBps` | float | 平均发送速率（MB/s），= `totalSendBytes / 1024 / 1024 / uptimeSeconds` |
| `recvMBps` | float | 平均接收速率（MB/s），= `totalRecvBytes / 1024 / 1024 / uptimeSeconds` |

---

#### actions[] — 动作指标数组

按首次出现顺序排列，包含声明式动作、Lua 动作、回调动作（前缀 `callback:`）。

```json
[
  {
    "name": "CreateNormalTeam",
    "sampleCount": 284,
    "successCount": 280,
    "failureCount": 4,
    "timeoutCount": 0,
    "skippedCount": 0,
    "executing": 3,
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
    "timeoutAvgMs": 0,
    "avgQps": 1.89,
    "periodQps": 0,
    "errors": [
      { "msg": "connection reset by peer", "count": 3 },
      { "msg": "deadline exceeded", "count": 1 }
    ]
  }
]
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `name` | string | 动作名称（`flow.json` 中 actions 的 key） |
| `sampleCount` | integer | 有效样本数 = `successCount + failureCount + timeoutCount`（不含 skipped） |
| `successCount` | integer | 成功次数 |
| `failureCount` | integer | 失败次数（非超时错误） |
| `timeoutCount` | integer | 超时次数（TCPRequest/WaitListen 等待响应超时） |
| `skippedCount` | integer | 跳过次数（必填字段为空，动作未执行） |
| `executing` | integer | 当前正在执行的机器人数量（实时并发数） |
| `successRate` | float | 成功率（0~1），= `successCount / sampleCount` |
| `avgSendBytes` | float | 成功样本平均发送字节数，Lua 动作为 0 |
| `avgRecvBytes` | float | 成功样本平均接收字节数，Lua 动作为 0 |
| `apdex` | float | Apdex 性能满意度（0.0~1.0），见下方公式 |
| `latency` | object | 延迟直方图快照，**仅含成功样本** |
| `timeoutAvgMs` | float | 超时样本平均等待时间（ms），`timeoutCount=0` 时为 0 |
| `avgQps` | float | 全程平均 QPS = `sampleCount / uptimeSeconds` |
| `periodQps` | float | 周期内 QPS（HTTP 端点始终为 0，仅控制台 Reporter 维护） |
| `errors` | array\|null | 错误分布列表，仅在 `failureCount + timeoutCount > 0` 时出现 |

**重要语义**：
- `latency.count` ≤ `sampleCount`（延迟直方图仅记录成功样本）
- `errors` 字段使用 `omitempty`，无错误时不输出
- `skippedCount` 不计入 `sampleCount`、`successRate`、`apdex` 的分母

#### errors[] — 错误分布

```json
[
  { "msg": "connection reset by peer", "count": 3 },
  { "msg": "deadline exceeded", "count": 1 }
]
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `msg` | string | 错误消息（截断至 120 字符） |
| `count` | integer | 该错误出现次数 |

---

#### latency — 延迟直方图

```json
{
  "count": 280,
  "minMs": 12.0,
  "maxMs": 450.6,
  "avgMs": 45.7,
  "p50Ms": 38.2,
  "p90Ms": 78.5,
  "p95Ms": 120.3,
  "p99Ms": 450.6
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `count` | integer | 采样数（仅成功样本） |
| `minMs` | float | 最小延迟（ms） |
| `maxMs` | float | 最大延迟（ms） |
| `avgMs` | float | 平均延迟（ms） |
| `p50Ms` | float | 中位数延迟（ms） |
| `p90Ms` | float | 90 百分位延迟（ms） |
| `p95Ms` | float | 95 百分位延迟（ms） |
| `p99Ms` | float | 99 百分位延迟（ms） |

`count=0` 时所有字段均为 0（零值快照）。

---

## GET /metrics/summary

返回纯文本监控摘要，适合终端查看或快速调试。

### 响应

**Status**: `200 OK`
**Content-Type**: `text/plain; charset=utf-8`

```
uptime: 2m30s
robots: started=100 running=98 stopped=0 errored=2
CreateNormalTeam: samples=284 success=280 timeout=0 failure=4 avg=45.7ms p99=450.6ms apdex=0.950 qps=1.89
SelectHero: samples=280 success=280 timeout=0 failure=0 avg=12.3ms p99=25.1ms apdex=1.000 qps=1.86
```

---

## 指标计算公式

### Apdex（性能满意度）

```
T = apdexT（默认 100ms）

satisfied   = 成功且延迟 < T
tolerating  = 成功且 T ≤ 延迟 < 4T
frustrated  = 失败 + 超时 + 成功但延迟 ≥ 4T（隐式，不单独统计）

apdex = (satisfied + tolerating × 0.5) / total
total = successCount + failureCount + timeoutCount（不含 skippedCount）
```

| Apdex | 含义 |
|---|---|
| 0.94~1.00 | 优秀 |
| 0.85~0.93 | 良好 |
| 0.70~0.84 | 一般 |
| < 0.70 | 需要关注 |

### QPS

| 指标 | 公式 | 说明 |
|---|---|---|
| `avgQps` | `sampleCount / uptimeSeconds` | 全程平均吞吐 |
| `periodQps` | `(currentSampleCount - prevSampleCount) / periodSeconds` | 最近周期吞吐（仅控制台 Reporter 维护，HTTP 端点始终为 0） |

**前端计算 periodQps 的方法**：轮询两次 `/metrics`，用相邻两次的 `sampleCount` 差值除以两次请求的时间差。

### 成功率

```
successRate = successCount / sampleCount
sampleCount = successCount + failureCount + timeoutCount
```

---

## 动作名称命名规则

| 来源 | 名称格式 | 示例 |
|---|---|---|
| 声明式动作 | `flow.json` 中 actions 的 key | `CreateNormalTeam` |
| Lua 动作 | 同上（Lua 脚本由 action 引用） | `BattleEnd` |
| 推送回调 | `callback:` + callback key | `callback:OnMatchSucceed` |

---

## 前端轮询建议

### 轮询间隔

| 阶段 | 间隔 | 说明 |
|---|---|---|
| 压测进行中 | 3~5s | 与 `reportInterval` 对齐 |
| 压测未启动/已结束 | 10~30s | 降低无效请求 |

### 自定义 periodQPS

HTTP 端点的 `periodQps` 始终为 0（服务端无状态）。前端可自行计算：

```javascript
let prevSampleCounts = {};
let prevTime = Date.now();

function computePeriodQps(actions) {
  const now = Date.now();
  const periodSec = (now - prevTime) / 1000;

  for (const action of actions) {
    const prev = prevSampleCounts[action.name] || 0;
    const diff = action.sampleCount - prev;
    action.periodQps = diff > 0 && periodSec > 0 ? diff / periodSec : 0;
    prevSampleCounts[action.name] = action.sampleCount;
  }
  prevTime = now;
}
```

### 零值处理

| 场景 | 处理方式 |
|---|---|
| `latency.count = 0` | 延迟面板显示 "-" 或隐藏 |
| `avgSendBytes = 0` / `avgRecvBytes = 0` | Lua 动作无字节数，显示 "-" |
| `errors` 字段不存在 | `omitempty` 序列化，前端应判断 `undefined` |
| `periodQps = 0` | HTTP 端点始终为 0，前端自行计算或忽略 |

---

## 配置

`conf/config.json` 中的 `monitor` 段控制 HTTP 服务行为：

```json
{
  "monitor": {
    "enabled": true,
    "reportInterval": "5s",
    "httpEnabled": true,
    "httpPort": 6060,
    "csvPath": "log/metrics.csv",
    "apdexT": 100
  }
}
```

| 字段 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `enabled` | boolean | `false` | 监控总开关，关闭后所有指标采集为零开销 no-op |
| `reportInterval` | string | `"5s"` | 控制台报告间隔（Go duration 格式） |
| `httpEnabled` | boolean | `false` | 是否启用 HTTP 指标端点 |
| `httpPort` | integer | `6060` | HTTP 服务端口，与 pprof 共用 |
| `csvPath` | string | `"log/metrics.csv"` | 压测结束时 CSV 导出路径 |
| `apdexT` | integer | `100` | Apdex T 阈值（毫秒） |

`enabled=false` 时即使 `httpEnabled=true` 也不会启动 HTTP 服务。

---

## 完整响应示例

```json
{
  "timestamp": "2026-04-28T10:30:00.123456789+08:00",
  "uptime": 150500000000,
  "uptimeSeconds": 150.5,
  "totalActions": 52843,
  "apdexT": 100,
  "system": {
    "goroutines": 142,
    "memAllocMB": 45.23,
    "memSysMB": 78.61,
    "gcCount": 12
  },
  "robots": {
    "started": 100,
    "running": 98,
    "stopped": 0,
    "errored": 2
  },
  "connections": {
    "established": 196,
    "failed": 4,
    "dropped": 2
  },
  "bandwidth": {
    "totalSendBytes": 5242880,
    "totalRecvBytes": 10485760,
    "sendMBps": 0.53,
    "recvMBps": 1.07
  },
  "actions": [
    {
      "name": "Auth",
      "sampleCount": 100,
      "successCount": 98,
      "failureCount": 2,
      "timeoutCount": 0,
      "skippedCount": 0,
      "executing": 0,
      "successRate": 0.98,
      "avgSendBytes": 256.0,
      "avgRecvBytes": 512.0,
      "apdex": 0.99,
      "latency": {
        "count": 98,
        "minMs": 5.2,
        "maxMs": 89.3,
        "avgMs": 15.7,
        "p50Ms": 12.0,
        "p90Ms": 32.5,
        "p95Ms": 55.1,
        "p99Ms": 89.3
      },
      "timeoutAvgMs": 0,
      "avgQps": 0.66,
      "periodQps": 0,
      "errors": [
        { "msg": "auth server returned 403", "count": 2 }
      ]
    },
    {
      "name": "CreateNormalTeam",
      "sampleCount": 284,
      "successCount": 280,
      "failureCount": 4,
      "timeoutCount": 0,
      "skippedCount": 0,
      "executing": 3,
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
      "timeoutAvgMs": 0,
      "avgQps": 1.89,
      "periodQps": 0
    },
    {
      "name": "callback:OnMatchSucceed",
      "sampleCount": 260,
      "successCount": 260,
      "failureCount": 0,
      "timeoutCount": 0,
      "skippedCount": 0,
      "executing": 0,
      "successRate": 1.0,
      "avgSendBytes": 0,
      "avgRecvBytes": 0,
      "apdex": 1.0,
      "latency": {
        "count": 0,
        "minMs": 0,
        "maxMs": 0,
        "avgMs": 0,
        "p50Ms": 0,
        "p90Ms": 0,
        "p95Ms": 0,
        "p99Ms": 0
      },
      "timeoutAvgMs": 0,
      "avgQps": 1.73,
      "periodQps": 0
    }
  ]
}
```
