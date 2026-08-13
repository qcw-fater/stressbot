# 日志查看器

> **已废弃（2026-08-13）**：内置日志查询和代理能力已经删除。Admin/Agent 只写本地文件，生产环境由外部日志采集与查询系统负责；本文仅保留历史设计记录。

> 本文档描述 stressbot 的实时日志查看系统的完整实现。
> 基于内存环形缓冲区（RingBuffer），通过 zapcore.Core 集成到日志系统，
> 前端通过 Admin 代理查看 Admin 和 Agent 的日志输出。

**设计目标**：日志文件可达 500MB，不能全量传输或加载；不引入外部依赖（ELK/Loki）；复用现有 HTTP 轮询模式。

---

## 目录

- [1. 方案选型](#1-方案选型)
- [2. 数据流](#2-数据流)
- [3. 包结构](#3-包结构)
- [4. 核心数据结构：RingBuffer](#4-核心数据结构ringbuffer)
  - [4.1 RingBuffer 结构体](#41-ringbuffer-结构体)
  - [4.2 写入：Append](#42-写入append)
  - [4.3 查询：Query](#43-查询query)
  - [4.4 辅助函数](#44-辅助函数)
  - [4.5 内存开销](#45-内存开销)
  - [4.6 并发安全分析](#46-并发安全分析)
- [5. Zap 集成：captureCore](#5-zap-集成capturecore)
  - [5.1 captureCore 结构体](#51-capturecore-结构体)
  - [5.2 zapcore.Core 接口实现](#52-zapcorecore-接口实现)
  - [5.3 Service 字段提取](#53-service-字段提取)
  - [5.4 与原有 core 的关系](#54-与原有-core-的关系)
- [6. 公共 API：attach.go](#6-公共-apiattachgo)
  - [6.1 AttachRingBuffer](#61-attachringbuffer)
  - [6.2 GetRingBuffer](#62-getringbuffer)
  - [6.3 调用时机](#63-调用时机)
- [7. utils/log 集成](#7-utilslog-集成)
- [8. Admin API 实现](#8-admin-api-实现)
  - [8.1 查询 Admin 日志](#81-查询-admin-日志)
  - [8.2 代理查询 Agent 日志](#82-代理查询-agent-日志)
  - [8.3 Admin 日志文件列表](#83-admin-日志文件列表)
  - [8.4 Admin 日志文件下载](#84-admin-日志文件下载)
  - [8.5 Agent 日志文件列表（代理）](#85-agent-日志文件列表代理)
  - [8.6 Agent 日志文件下载（代理）](#86-agent-日志文件下载代理)
  - [8.7 辅助函数](#87-辅助函数)
- [9. Agent 日志端点](#9-agent-日志端点)
  - [9.1 路由注册](#91-路由注册)
  - [9.2 日志查询 Handler](#92-日志查询-handler)
  - [9.3 文件列表 Handler](#93-文件列表-handler)
  - [9.4 文件下载 Handler](#94-文件下载-handler)
- [10. HTTP 端点完整列表](#10-http-端点完整列表)
- [11. 请求参数与响应格式](#11-请求参数与响应格式)
  - [11.1 请求参数](#111-请求参数)
  - [11.2 成功响应](#112-成功响应)
  - [11.3 空缓冲区响应](#113-空缓冲区响应)
  - [11.4 错误响应](#114-错误响应)
- [12. 前端实现](#12-前端实现)
  - [12.1 整体架构](#121-整体架构)
  - [12.2 核心文件](#122-核心文件)
  - [12.3 日志格式化](#123-日志格式化)
  - [12.4 自定义 Monaco 语言](#124-自定义-monaco-语言)
  - [12.5 轮询与数据管理](#125-轮询与数据管理)
  - [12.6 Monaco 编辑器同步](#126-monaco-编辑器同步)
  - [12.7 Find Widget 中文化](#127-find-widget-中文化)
  - [12.8 日志文件下载](#128-日志文件下载)
- [13. 性能分析](#13-性能分析)
- [14. 文件变更清单](#14-文件变更清单)
- [15. 与计划的差异](#15-与计划的差异)

---

## 1. 方案选型

| 方案 | 优点 | 缺点 |
|------|------|------|
| A. 读日志文件 | 可看历史 | 每次请求要解析大文件，性能差；需维护文件偏移量 |
| B. MySQL 存储 | 支持查询和持久化 | 引入数据库依赖；写入路径变复杂 |
| **C. 内存环形缓冲区** | O(1) 写入、O(N) 查询无磁盘 IO；天然有界；零外部依赖 | 仅保留最近 N 条，无法看更早历史 |

**选择方案 C**：通过 `zapcore.Core` 包装器将每条日志同时写入文件和内存环形缓冲区。Admin 缓冲区保留最近 5000 条，Agent 缓冲区保留最近 50000 条。缓冲区容量覆盖压测高峰数秒至数分钟的输出，满足"实时查看最近状态"的核心需求。

**补充方案 A**：提供日志文件列表和下载端点，用于查看更早历史。Admin 直接读取本地日志目录，Agent 通过 Admin 代理转发。

---

## 2. 数据流

```
                        Agent 本地
zap → ringBuffer ──────────────────→ GET /agent/v1/logs
         │                                │
         │ GET /agent/v1/logs/files       │ Admin 代理
         │ GET /agent/v1/logs/files/{n}   ▼
         │                     GET /sbot/logs/agents/{id}
         │                            │
Admin 本地                            │
zap → ringBuffer ──→ GET /sbot/logs/admin
                        │                   │
                        │ GET /sbot/logs/admin/files
                        │ GET /sbot/logs/admin/files/{n}
                        │
                        └───── 前端轮询 ────┘
                                    │
                          LogsTab (Monaco Editor)
```

- **Admin 日志**：直接从内存环形缓冲区读取，零延迟。
- **Agent 日志**：Admin 收到前端请求后，向 Agent 的 `GET /agent/v1/logs` 发起 HTTP 请求，将 JSON 响应原样透传给前端。
- **日志文件下载**：Admin 直接读取本地日志目录；Agent 的文件列表和下载请求由 Admin 代理转发。
- **前端轮询**：正常 3 秒间隔，有积压（`hasMore=true`）时缩短至 100ms 快速追平。

---

## 3. 包结构

环形缓冲区和 Zap 集成封装在独立的 `logview` 包中，与 `utils/log` 解耦。

| 文件 | 说明 |
|------|------|
| `logview/ringbuffer.go` | `RingBuffer` 数据结构、`Entry`/`Field`/`QueryParams`/`QueryResult` 类型、`Append`/`Query` 方法、`fieldsToFields` 辅助函数 |
| `logview/capture.go` | `captureCore` 实现 `zapcore.Core`，将日志写入 RingBuffer |
| `logview/attach.go` | `AttachRingBuffer` / `GetRingBuffer` 公共 API |

---

## 4. 核心数据结构：RingBuffer

### 4.1 RingBuffer 结构体

**代码位置**：`logview/ringbuffer.go`

```go
// RingBuffer 线程安全的固定大小环形缓冲区。
type RingBuffer struct {
    mu    sync.RWMutex
    buf   []entryWithSeq
    size  int
    head  int           // 下一个写入位置
    count int           // 当前条目数（增长到 size 后不变）
    seq   atomic.Uint64 // 单调递增序列号
}
```

**内部条目类型**：

```go
type entryWithSeq struct {
    level   string
    time    time.Time
    caller  string
    message string
    service string
    fields  []zapcore.Field
    seq     uint64
}
```

**对外暴露的查询类型**：

```go
// Entry 单条结构化日志。
type Entry struct {
    Level   string    `json:"level"`
    Time    time.Time `json:"time"`
    Caller  string    `json:"caller,omitempty"`
    Message string    `json:"message"`
    Service string    `json:"service,omitempty"`
    Fields  []Field   `json:"fields,omitempty"`
}

// Field 序列化后的 zap 字段键值对。
type Field struct {
    Key   string `json:"key"`
    Value string `json:"value"`
}

// QueryParams 查询参数（仅游标分页，不含 level/search — 过滤由前端完成）。
type QueryParams struct {
    AfterSeq uint64 // 游标：仅返回 seq > AfterSeq 的条目
    Limit    int    // 最大返回条数
}

// QueryResult 查询结果。
type QueryResult struct {
    Entries []Entry `json:"entries"`
    HasMore bool    `json:"hasMore"` // 是否还有更多未返回的条目
    NextSeq uint64  `json:"nextSeq"` // 下次查询使用的游标
}
```

**设计要点**：
- `entryWithSeq` 内部存储 `[]zapcore.Field`（原始 zap 字段），避免 Append 时序列化开销。
- `Entry` 是面向外部的序列化类型，`Fields` 在 `toEntry()` 时通过 `fieldsToFields` 转换为 `[]Field`。
- `seq` 使用 `atomic.Uint64`，在无锁的原子操作中递增，避免 Append 路径上的额外同步开销。

### 4.2 写入：Append

**代码位置**：`logview/ringbuffer.go::Append`

```go
// Append 写入一条日志（O(1)，Write Lock）。
func (rb *RingBuffer) Append(level string, t time.Time, caller, message, service string,
    fields []zapcore.Field) {
    s := rb.seq.Add(1) // 原子递增序列号

    rb.mu.Lock()
    rb.buf[rb.head] = entryWithSeq{
        level:   level,
        time:    t,
        caller:  caller,
        message: message,
        service: service,
        fields:  fields,
        seq:     s,
    }
    rb.head = (rb.head + 1) % rb.size
    if rb.count < rb.size {
        rb.count++
    }
    rb.mu.Unlock()
}
```

**执行步骤**：
1. `seq.Add(1)` 原子递增序列号（在锁外完成，不影响写锁的持有时间）。
2. 获取写锁（`mu.Lock()`）。
3. 写入 `buf[head]`，覆盖最旧的条目（如果缓冲区已满）。
4. `head` 循环推进：`head = (head + 1) % size`。
5. `count` 递增直到等于 `size`，之后不再变化。
6. 释放写锁。

**时间复杂度**：O(1)。

**字段存储**：`fields` 参数是 `[]zapcore.Field` 原始 zap 字段，直接存储不做序列化，序列化延迟到 `Query` 时的 `fieldsToFields` 调用。

### 4.3 查询：Query

**代码位置**：`logview/ringbuffer.go::Query`

```go
// Query 按游标查询（Read Lock）。过滤由前端负责。
func (rb *RingBuffer) Query(params QueryParams) QueryResult {
    limit := params.Limit
    if limit <= 0 {
        limit = 200
    }

    rb.mu.RLock()
    count := rb.count
    if count == 0 {
        rb.mu.RUnlock()
        return QueryResult{Entries: []Entry{}, HasMore: false, NextSeq: 0}
    }

    start := (rb.head - count + rb.size) % rb.size
    buf := rb.buf
    size := rb.size
    rb.mu.RUnlock()

    var entries []Entry
    lastSeq := uint64(0)
    collected := 0
    hasMore := false

    for i := 0; i < count; i++ {
        idx := (start + i) % size
        item := &buf[idx]

        if item.seq <= params.AfterSeq {
            continue
        }

        collected++
        if collected > limit {
            hasMore = true
            break
        }
        entries = append(entries, item.toEntry())
        lastSeq = item.seq
    }

    if entries == nil {
        entries = []Entry{}
    }

    return QueryResult{
        Entries: entries,
        HasMore: hasMore,
        NextSeq: lastSeq,
    }
}
```

**执行步骤**：
1. 参数校验：`limit <= 0` 时使用默认 200。
2. 获取读锁（`mu.RLock()`），读取 `count`、计算 `start`（最老条目的位置）、复制 `buf` 和 `size` 的引用/值。
3. 立即释放读锁（`mu.RUnlock()`），后续遍历无锁。
4. 从 `start` 开始遍历所有条目（`count` 个）：
   - 跳过 `seq <= AfterSeq` 的条目（游标之前的数据）。
   - 收集最多 `limit` 条。
   - 若还有未收集的条目（遍历未结束），`hasMore = true`。
5. `NextSeq` 为结果中最后一条的 seq（用于下次查询的游标）。

**关键设计**：读锁持有时间极短（仅读取元数据和 buf 切片引用），释放后遍历 buf 切片。由于 buf 切片本身不会重新分配（固定大小），遍历过程中即使有并发写入，也只是覆盖了旧的槽位，不会导致 panic。但可能读到部分覆盖的数据——这是可接受的权衡（日志查看不要求强一致性）。

**游标分页的正确性保证**：
- `seq` 是全局单调递增的，每条日志有唯一的 seq。
- 客户端用上次响应的 `NextSeq` 作为下次查询的 `AfterSeq`，保证不重复不遗漏。
- 当缓冲区已满时，旧数据被覆盖。如果客户端的 `AfterSeq` 对应的数据已被覆盖，查询会从当前最老的可读条目开始（因为那些 seq <= AfterSeq 的条目已被覆盖，不再被遍历到）。

### 4.4 辅助函数

**`fieldsToFields`**（`logview/ringbuffer.go`）：

将 `[]zapcore.Field` 转为 `[]Field`。仅在查询时调用（不在 Append 热路径上）。

```go
func fieldsToFields(fields []zapcore.Field) []Field {
    if len(fields) == 0 {
        return nil
    }
    out := make([]Field, 0, len(fields))
    for _, f := range fields {
        enc := zapcore.NewMapObjectEncoder()
        f.AddTo(enc)
        for k, v := range enc.Fields {
            out = append(out, Field{Key: k, Value: fmt.Sprintf("%v", v)})
        }
    }
    return out
}
```

使用 `zapcore.NewMapObjectEncoder()` 将 zap 字段转为 `map[string]interface{}`，然后格式化为字符串。一个 zap Field 可能产生多个 output Field（例如 `zap.Object` 类型）。

**`ParseUint64OrDefault`**（`logview/ringbuffer.go`）：

```go
func ParseUint64OrDefault(s string, def uint64) uint64 {
    if s == "" { return def }
    v, err := strconv.ParseUint(s, 10, 64)
    if err != nil { return def }
    return v
}
```

解析 uint64 字符串，空字符串或解析失败返回默认值。用于从 HTTP 查询参数中解析 `afterSeq`。

**`toEntry`**（`logview/ringbuffer.go::entryWithSeq.toEntry`）：

```go
func (e *entryWithSeq) toEntry() Entry {
    return Entry{
        Level:   e.level,
        Time:    e.time,
        Caller:  e.caller,
        Message: e.message,
        Service: e.service,
        Fields:  fieldsToFields(e.fields),
    }
}
```

将内部存储类型转为对外暴露的类型，在此触发 `fieldsToFields` 的序列化。

### 4.5 内存开销

| 实例 | 条目数 | 每条估算 | 总内存 |
|------|--------|---------|--------|
| Admin | 5,000 | ~500 字节 | ~2.5MB |
| Agent | 50,000 | ~500 字节 | ~25MB |

每条目估算依据：`entryWithSeq` 包含 level（~5B）、time（24B）、caller（~30B）、message（~100B）、service（~10B）、fields（动态，平均 ~200B）、seq（8B）。

**容量差异说明**：Admin 仅管理控制面日志，5000 条足够；Agent 运行压测产生大量业务日志，50000 条保留更完整的运行上下文。

### 4.6 并发安全分析

| 操作 | 锁类型 | 热路径影响 |
|------|--------|-----------|
| `Append` | `sync.RWMutex`（写锁） | 每条日志一次写锁，持有时间极短（一个数组赋值 + 两个整数操作） |
| `Query` | `sync.RWMutex`（读锁） | 持有时间极短（读取 3 个字段后释放），遍历无锁 |
| `seq` 递增 | `atomic.Uint64` | 无锁原子操作，在写锁外执行 |

**读写比例**：Append 在每条日志时调用（高频率），Query 在前端轮询时调用（3s 一次）。`sync.RWMutex` 允许多个 Query 并发执行而不互相阻塞，仅在 Append 时阻塞。

**Query 的一致性语义**：由于 Query 在读锁释放后遍历 buf，可能读到 Append 中间状态的条目。这是可接受的：日志查看不要求强一致性，偶尔一条日志字段不完整不影响诊断。

---

## 5. Zap 集成：captureCore

### 5.1 captureCore 结构体

**代码位置**：`logview/capture.go`

```go
// captureCore 将日志追加到 RingBuffer，不影响原有 core 链。
type captureCore struct {
    ring   *RingBuffer
    fields []zapcore.Field
}
```

`fields` 字段用于 `With()` 方法链式累积 zap 字段（如 logger 初始化时通过 `zap.Fields(zap.String("SR", "admin"))` 注入的服务名字段）。

接口合规检查：
```go
var _ zapcore.Core = (*captureCore)(nil)
```

### 5.2 zapcore.Core 接口实现

**`Enabled`**：

```go
func (c *captureCore) Enabled(zapcore.Level) bool { return true }
```

始终返回 `true`，捕获所有级别的日志。级别过滤由原有 core 链处理。

**`With`**：

```go
func (c *captureCore) With(fields []zapcore.Field) zapcore.Core {
    return &captureCore{
        ring:   c.ring,
        fields: append(c.fields[:len(c.fields):len(c.fields)], fields...),
    }
}
```

创建新的 `captureCore` 实例，累积字段。使用 `append(c.fields[:len(c.fields):len(c.fields)], fields...)` 的技巧：
- `c.fields[:len(c.fields):len(c.fields)` 限制了底层数组的容量为 `len(c.fields)`，确保 append 时必须分配新数组，不会修改原 `c.fields`。
- 这保证了 `With` 的不可变语义（每次返回新实例，不修改原实例）。

**`Check`**：

```go
func (c *captureCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
    return ce.AddCore(ent, c)
}
```

将自身添加到 CheckedEntry 的 core 列表中，zap 会随后调用 `Write`。

**`Write`**：

```go
func (c *captureCore) Write(ent zapcore.Entry, fields []zapcore.Field) error {
    merged := fields
    if len(c.fields) > 0 {
        merged = make([]zapcore.Field, 0, len(c.fields)+len(fields))
        merged = append(merged, c.fields...)
        merged = append(merged, fields...)
    }

    service := ""
    for _, f := range merged {
        if f.Key == "SR" {
            if f.String != "" {
                service = f.String
            } else {
                service = fmt.Sprintf("%v", f.Interface)
            }
            break
        }
    }

    c.ring.Append(ent.Level.String(), ent.Time, ent.Caller.TrimmedPath(), ent.Message, service, merged)
    return nil
}
```

执行步骤：
1. 合并 `c.fields`（初始字段，如 `SR`）和 `fields`（本次日志的字段）。
2. 从合并后的字段中提取 `Key == "SR"` 的字段作为 `service`。
3. 调用 `RingBuffer.Append` 写入环形缓冲区。
4. 始终返回 `nil`（写入不会失败）。

**`Sync`**：

```go
func (c *captureCore) Sync() error { return nil }
```

环形缓冲区是纯内存结构，无需 sync。

### 5.3 Service 字段提取

在 `Write` 方法中遍历 fields 查找 `Key == "SR"` 的字段。这是因为在 `utils/log.InitLog` 中通过 `zap.Fields(zap.String("SR", serviceName))` 注入了服务名。

提取逻辑：
- 优先使用 `f.String`（字符串类型直接取值）。
- 回退使用 `fmt.Sprintf("%v", f.Interface)`（接口类型格式化输出）。

Service 字段用于前端区分 Admin 日志和 Agent 日志。

### 5.4 与原有 core 的关系

**关键设计**：`captureCore` 不包装原有 core 链，而是通过 `zapcore.NewTee` 并接在原有 core 旁边。

```
原有 core 链（文件 + 控制台）───┐
                                ├── zapcore.NewTee ──→ logger
captureCore（环形缓冲区）───────┘
```

原有 core（文件 + 控制台 + lumberjack 轮转）完全不受影响。`captureCore` 只是额外接收一份日志写入环形缓冲区，两者的 `Write` 独立执行。

---

## 6. 公共 API：attach.go

### 6.1 AttachRingBuffer

**代码位置**：`logview/attach.go`

```go
func AttachRingBuffer(logger *zap.Logger, size int, initialFields ...zap.Field) *zap.Logger {
    rb := NewRingBuffer(size)
    globalRingBuffer = rb
    cc := &captureCore{ring: rb}
    if len(initialFields) > 0 {
        fields := make([]zapcore.Field, len(initialFields))
        copy(fields, initialFields)
        cc = cc.With(fields).(*captureCore)
    }
    return logger.WithOptions(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
        return zapcore.NewTee(core, cc)
    }))
}
```

**执行步骤**：
1. 创建指定大小的 `RingBuffer`。
2. 设置包级 `globalRingBuffer`（供 HTTP handler 通过 `GetRingBuffer()` 访问）。
3. 创建 `captureCore`，如果提供了 `initialFields`，通过 `With` 方法预先注入（如 `zap.String("SR", "admin")`）。
4. 使用 `zap.WrapCore` 将 `captureCore` 通过 `zapcore.NewTee` 并接到原有 core 旁边。
5. 返回修改后的 logger（调用方需通过 `ReplaceLogger` 同步回 `utils/log`）。

**`initialFields` 参数的作用**：logger 创建时通过 `zap.Fields(zap.String("SR", serviceName))` 注入的字段已经存在于原 core 的字段链中，但 `captureCore` 作为独立的 core 不会自动获得这些字段。`initialFields` 参数让 `captureCore` 也能感知这些初始字段，在 `Write` 时正确提取 `service` 名称。

### 6.2 GetRingBuffer

```go
func GetRingBuffer() *RingBuffer {
    return globalRingBuffer
}

var globalRingBuffer *RingBuffer
```

返回全局 `RingBuffer` 实例。未调用 `AttachRingBuffer` 时返回 `nil`。

Handler 中通过此函数获取 RingBuffer，如果为 `nil` 则返回空结果或 503 错误。

### 6.3 调用时机

**Admin 端**（`cmd/admin/main.go`）：

```go
stresslog.InitLog(cfg.Log.Path, "admin", logConf, "")
newLogger := logview.AttachRingBuffer(stresslog.GetLogger(), 5000, zap.String("SR", "admin"))
stresslog.ReplaceLogger(newLogger)
```

**Agent 端**（`cmd/agent/main.go`）：

```go
stresslog.InitLog(logPath, logTag, logConf, "")
newLogger := logview.AttachRingBuffer(stresslog.GetLogger(), 50000, zap.String("SR", logTag))
stresslog.ReplaceLogger(newLogger)
```

调用顺序：
1. `InitLog` 初始化原有日志系统（文件 + 控制台 + lumberjack）。
2. `AttachRingBuffer` 创建环形缓冲区并注入到 logger。
3. `ReplaceLogger` 将修改后的 logger 同步回 `utils/log` 包的全局变量。

---

## 7. utils/log 集成

**代码位置**：`utils/log/logger.go`

新增三个接口：

```go
var logFilePath string

// InitLog 中记录日志文件路径：
func InitLog(logPath, serviceName string, conf *Config, buildLogLevel string) {
    logFilePath = logPath
    // ... 原有初始化逻辑
}

// GetLogFilePath 返回 InitLog 配置的日志文件路径。
func GetLogFilePath() string {
    return logFilePath
}

// ReplaceLogger 替换内部 logger 实例（供 logview.AttachRingBuffer 后同步）。
func ReplaceLogger(l *zap.Logger) {
    logger = l
    sugarLogger = l.Sugar()
}
```

- `logFilePath`：记录 `InitLog` 传入的日志文件路径，供日志文件下载端点使用。
- `GetLogFilePath()`：返回日志文件路径。Agent 和 Admin 的日志文件列表/下载 Handler 通过此函数定位日志目录。
- `ReplaceLogger()`：替换 `utils/log` 包内部的 `logger` 和 `sugarLogger` 全局变量。在 `AttachRingBuffer` 返回新 logger 后调用，确保后续通过 `stresslog.Info/Warn/Error` 等方法写入的日志也进入环形缓冲区。

---

## 8. Admin API 实现

### 8.1 查询 Admin 日志

**路由**：`GET /sbot/logs/admin`
**Handler**：`admin/handlers.go::handleGetAdminLogs`

```go
func (s *AdminServer) handleGetAdminLogs(w http.ResponseWriter, r *http.Request) {
    rb := logview.GetRingBuffer()
    if rb == nil {
        writeJSON(w, http.StatusOK, logview.QueryResult{})
        return
    }
    params := parseLogQueryParams(r)
    result := rb.Query(params)
    writeJSON(w, http.StatusOK, result)
}
```

**行为**：
- 缓冲区未初始化时返回空结果（`entries: []`, `hasMore: false`, `nextSeq: 0`）。
- 参数解析由 `parseLogQueryParams` 完成。
- 直接查询内存，零延迟。

### 8.2 代理查询 Agent 日志

**路由**：`GET /sbot/logs/agents/{id}`
**Handler**：`admin/handlers.go::handleGetAgentLogs`

```go
func (s *AdminServer) handleGetAgentLogs(w http.ResponseWriter, r *http.Request) {
    agentID := r.PathValue("id")
    agent, ok := s.agents.Get(agentID)
    if !ok {
        writeError(w, ErrAgentNotFound)
        return
    }
    if agent.Status == AgentOffline {
        writeError(w, ErrAgentOffline.WithMessage("agent is offline, logs unavailable"))
        return
    }

    url := fmt.Sprintf("http://%s/agent/v1/logs?%s",
        normalizeAddr(agent.Address), r.URL.RawQuery)
    proxyReq, err := http.NewRequestWithContext(r.Context(), "GET", url, nil)
    if err != nil {
        writeError(w, ErrInvalidArgument.WithMessage("invalid proxy request"))
        return
    }

    resp, err := s.logsProxyClient.Do(proxyReq) // 5s 超时客户端
    if err != nil {
        writeError(w, ErrAgentOffline.WithMessage("agent unreachable: "+err.Error()))
        return
    }
    defer resp.Body.Close()

    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    w.WriteHeader(resp.StatusCode)
    if _, err := io.Copy(w, resp.Body); err != nil {
        io.Copy(io.Discard, resp.Body) // 确保响应体被完全消耗，允许连接复用
    }
}
```

**处理流程**：
1. 从 `AgentRegistry` 查找 Agent，获取 `Address`。
2. 检查 Agent 状态不为 `offline`（离线则返回 `503 AGENT_OFFLINE`）。
3. 构造请求 `GET http://{agentAddr}/agent/v1/logs?{原样透传查询参数}`。
4. 使用 `logsProxyClient`（`http.Client{Timeout: 5s}`）发起请求。
5. 将 Agent 返回的 JSON 响应原样写入 `http.ResponseWriter`。
6. 如果 `io.Copy` 失败（客户端断开），仍然消耗完响应体以允许 HTTP 连接复用。

`logsProxyClient` 在 `NewAdminServer`（`admin/admin.go`）中创建，5 秒超时：
```go
s.logsProxyClient = &http.Client{Timeout: 5 * time.Second}
```

`normalizeAddr`（`admin/agent_dispatcher.go`）处理地址格式，确保包含 `http://` 前缀。

### 8.3 Admin 日志文件列表

**路由**：`GET /sbot/logs/admin/files`
**Handler**：`admin/handlers.go::handleListAdminLogFiles`

```go
func (s *AdminServer) handleListAdminLogFiles(w http.ResponseWriter, r *http.Request) {
    files, err := listLogFiles(stresslog.GetLogFilePath())
    if err != nil {
        writeError(w, ErrInternal.WithMessage(err.Error()))
        return
    }
    writeJSON(w, http.StatusOK, files)
}
```

使用 `listLogFiles` 工具函数（见 §8.7）读取本地日志目录。

### 8.4 Admin 日志文件下载

**路由**：`GET /sbot/logs/admin/files/{name}`
**Handler**：`admin/handlers.go::handleDownloadAdminLogFile`

```go
func (s *AdminServer) handleDownloadAdminLogFile(w http.ResponseWriter, r *http.Request) {
    name := r.PathValue("name")
    if strings.Contains(name, "/") || strings.Contains(name, "\\") || name == ".." {
        writeError(w, ErrInvalidArgument.WithMessage("invalid file name"))
        return
    }
    dir := filepath.Dir(stresslog.GetLogFilePath())
    serveLogFile(w, r, dir, name)
}
```

**安全校验**：文件名不含 `/`、`\`、`..`，防止路径遍历攻击。

`serveLogFile` 使用 `http.ServeContent` 流式下载，支持 Range 请求。

### 8.5 Agent 日志文件列表（代理）

**路由**：`GET /sbot/logs/agents/{id}/files`
**Handler**：`admin/handlers.go::handleListAgentLogFiles`

```go
func (s *AdminServer) handleListAgentLogFiles(w http.ResponseWriter, r *http.Request) {
    // 查找 Agent，校验状态
    url := fmt.Sprintf("http://%s/agent/v1/logs/files", normalizeAddr(agent.Address))
    // 使用 logsProxyClient 代理请求
    // 原样透传 JSON 响应
}
```

### 8.6 Agent 日志文件下载（代理）

**路由**：`GET /sbot/logs/agents/{id}/files/{name}`
**Handler**：`admin/handlers.go::handleDownloadAgentLogFile`

```go
func (s *AdminServer) handleDownloadAgentLogFile(w http.ResponseWriter, r *http.Request) {
    // 查找 Agent，校验状态
    name := r.PathValue("name")
    // 安全校验文件名
    url := fmt.Sprintf("http://%s/agent/v1/logs/files/%s",
        normalizeAddr(agent.Address), url.PathEscape(name))
    // 使用 60s 超时的独立 HTTP 客户端（大文件传输需要更长超时）
    client := &http.Client{Timeout: 60 * time.Second}
    // 透传响应头和响应体
}
```

**注意**：Agent 文件下载使用 60 秒超时的独立 HTTP 客户端（大文件可能达到数百 MB），而非 `logsProxyClient` 的 5 秒超时。`url.PathEscape` 对文件名进行 URL 编码，防止特殊字符问题。

### 8.7 辅助函数

**`parseLogQueryParams`**（`admin/helpers.go`）：

```go
func parseLogQueryParams(r *http.Request) logview.QueryParams {
    q := r.URL.Query()
    limit := parseIntOrDefault(q.Get("limit"), 200)
    if limit <= 0 || limit > 500 {
        limit = 200
    }
    return logview.QueryParams{
        AfterSeq: logview.ParseUint64OrDefault(q.Get("afterSeq"), 0),
        Limit:    limit,
    }
}
```

参数校验：`limit` 超出 [1, 500] 范围时使用默认值 200。

**`listLogFiles`**（`admin/handlers.go`）：

```go
type LogFileInfo struct {
    Name    string `json:"name"`
    Size    int64  `json:"size"`
    ModTime string `json:"modTime"`
}

func listLogFiles(logPath string) ([]LogFileInfo, error) {
    dir := filepath.Dir(logPath)
    base := filepath.Base(logPath)
    prefix := strings.TrimSuffix(base, filepath.Ext(base))

    entries, err := os.ReadDir(dir)
    // ...
    for _, e := range entries {
        if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
            continue
        }
        info, _ := e.Info()
        files = append(files, LogFileInfo{
            Name:    e.Name(),
            Size:    info.Size(),
            ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
        })
    }
    return files, nil
}
```

按日志文件名前缀匹配（如 `stressbot`），列出同目录下所有相关文件（含 lumberjack 轮转的 `.gz` 文件）。

**`serveLogFile`**（`admin/handlers.go`）：

```go
func serveLogFile(w http.ResponseWriter, r *http.Request, dir, name string) {
    path := filepath.Join(dir, name)
    f, err := os.Open(path)
    // ...
    w.Header().Set("Content-Type", "text/plain; charset=utf-8")
    w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
    http.ServeContent(w, r, name, stat.ModTime(), f)
}
```

使用 `http.ServeContent` 流式下载，支持：
- Content-Type 自动检测。
- Range 请求（部分下载）。
- Last-Modified / If-Modified-Since 条件请求。

---

## 9. Agent 日志端点

### 9.1 路由注册

**代码位置**：`agent/http_server.go::startHTTPServer`

```go
mux.HandleFunc("/agent/v1/logs", a.handleLogs)
mux.HandleFunc("/agent/v1/logs/files", a.handleListLogFiles)
mux.HandleFunc("/agent/v1/logs/files/", a.handleDownloadLogFile)
```

路由说明：
- `/agent/v1/logs` — 日志查询（afterSeq + limit 分页）。
- `/agent/v1/logs/files` — 日志文件列表。
- `/agent/v1/logs/files/` — 日志文件下载（尾部斜杠匹配子路径）。

### 9.2 日志查询 Handler

**代码位置**：`agent/http_server.go::handleLogs`

```go
func (a *Agent) handleLogs(w http.ResponseWriter, r *http.Request) {
    rb := logview.GetRingBuffer()
    if rb == nil {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusServiceUnavailable)
        json.NewEncoder(w).Encode(map[string]string{"error": "log ring buffer not enabled"})
        return
    }

    q := r.URL.Query()
    limit := parseIntOrDefault(q.Get("limit"), 200)
    if limit <= 0 || limit > 500 {
        limit = 200
    }

    result := rb.Query(logview.QueryParams{
        AfterSeq: logview.ParseUint64OrDefault(q.Get("afterSeq"), 0),
        Limit:    limit,
    })

    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    json.NewEncoder(w).Encode(result)
}
```

**行为差异**（与 Admin 端对比）：
- Admin 端缓冲区未初始化时返回空结果（`200 OK`）。
- Agent 端缓冲区未初始化时返回 `503 Service Unavailable`（`{"error": "log ring buffer not enabled"}`）。

这个差异是因为 Agent 的日志端点主要被 Admin 代理调用，明确区分"缓冲区未启用"比返回空结果更有诊断价值。

参数解析直接在 Handler 中完成（Admin 端使用 `parseLogQueryParams` 辅助函数，Agent 端内联解析）。两者的逻辑完全一致。

### 9.3 文件列表 Handler

**代码位置**：`agent/http_server.go::handleListLogFiles`

```go
func (a *Agent) handleListLogFiles(w http.ResponseWriter, r *http.Request) {
    logPath := stresslog.GetLogFilePath()
    if logPath == "" {
        writeJSONError(w, http.StatusServiceUnavailable, "log file not configured")
        return
    }

    dir := filepath.Dir(logPath)
    base := filepath.Base(logPath)
    prefix := strings.TrimSuffix(base, filepath.Ext(base))

    entries, err := os.ReadDir(dir)
    if err != nil {
        writeJSONError(w, http.StatusInternalServerError, "read log dir: %v", err)
        return
    }

    type fileInfo struct {
        Name    string `json:"name"`
        Size    int64  `json:"size"`
        ModTime string `json:"modTime"`
    }

    var files []fileInfo
    for _, e := range entries {
        if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
            continue
        }
        info, err := e.Info()
        if err != nil { continue }
        files = append(files, fileInfo{
            Name:    e.Name(),
            Size:    info.Size(),
            ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
        })
    }
    if files == nil {
        files = []fileInfo{} // 避免返回 null
    }

    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    json.NewEncoder(w).Encode(files)
}
```

日志文件路径通过 `stresslog.GetLogFilePath()` 获取。按文件名前缀匹配（如 `stressbot`），列出日志目录下所有相关文件。

### 9.4 文件下载 Handler

**代码位置**：`agent/http_server.go::handleDownloadLogFile`

```go
func (a *Agent) handleDownloadLogFile(w http.ResponseWriter, r *http.Request) {
    logPath := stresslog.GetLogFilePath()
    if logPath == "" {
        writeJSONError(w, http.StatusServiceUnavailable, "log file not configured")
        return
    }

    // 提取文件名：/agent/v1/logs/files/{name}
    name := strings.TrimPrefix(r.URL.Path, "/agent/v1/logs/files/")
    if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") || name == ".." {
        writeJSONError(w, http.StatusBadRequest, "invalid file name")
        return
    }

    path := filepath.Join(filepath.Dir(logPath), name)
    f, err := os.Open(path)
    if err != nil {
        http.Error(w, "log file not found", http.StatusNotFound)
        return
    }
    defer f.Close()

    stat, err := f.Stat()
    if err != nil {
        http.Error(w, "stat failed", http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "text/plain; charset=utf-8")
    w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
    http.ServeContent(w, r, name, stat.ModTime(), f)
}
```

**安全校验**：
- 文件名不能为空。
- 文件名不含 `/` 和 `\`（防止路径分隔符注入）。
- 文件名不等于 `..`（防止父目录遍历）。
- 文件名通过 `strings.TrimPrefix` 从 URL 路径提取，而非从查询参数获取。

使用 `http.ServeContent` 流式下载，支持 Range 请求和条件请求。

---

## 10. HTTP 端点完整列表

### Admin 端点（`admin/handlers.go::registerRoutes`）

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/sbot/logs/admin` | 查询 Admin 日志（从内存环形缓冲区） |
| `GET` | `/sbot/logs/agents/{id}` | 查询指定 Agent 日志（Admin 代理到 Agent） |
| `GET` | `/sbot/logs/admin/files` | 列出 Admin 日志文件 |
| `GET` | `/sbot/logs/admin/files/{name}` | 下载 Admin 日志文件 |
| `GET` | `/sbot/logs/agents/{id}/files` | 列出 Agent 日志文件（Admin 代理） |
| `GET` | `/sbot/logs/agents/{id}/files/{name}` | 下载 Agent 日志文件（Admin 代理） |

### Agent 端点（`agent/http_server.go::startHTTPServer`）

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/agent/v1/logs` | Agent 本地日志查询（afterSeq + limit） |
| `GET` | `/agent/v1/logs/files` | Agent 本地日志文件列表 |
| `GET` | `/agent/v1/logs/files/{name}` | Agent 本地日志文件下载 |

---

## 11. 请求参数与响应格式

### 11.1 请求参数

```
GET /sbot/logs/admin?afterSeq=12345&limit=200
GET /sbot/logs/agents/{id}?afterSeq=12345&limit=200
GET /agent/v1/logs?afterSeq=12345&limit=200
```

| 参数 | 类型 | 必填 | 默认 | 说明 |
|------|------|------|------|------|
| `afterSeq` | uint64 | 否 | `0` | 增量游标。首次传 0，后续传上次响应的 `nextSeq` |
| `limit` | int | 否 | `200` | 单次最大返回条数，上限 500 |

> level/search 过滤由前端在本地缓存中完成，不经过后端。

### 11.2 成功响应

```json
{
  "entries": [
    {
      "level": "info",
      "time": "2026-05-08T14:30:05.123456+08:00",
      "caller": "admin/task.go:45",
      "message": "任务状态变更",
      "service": "admin",
      "fields": [
        {"key": "taskId", "value": "t-001"},
        {"key": "from", "value": "pending"},
        {"key": "to", "value": "starting"}
      ]
    },
    {
      "level": "warn",
      "time": "2026-05-08T14:30:10.456789+08:00",
      "caller": "admin/agent.go:112",
      "message": "agent 心跳恢复",
      "service": "admin",
      "fields": [
        {"key": "agentId", "value": "uuid-xxx"},
        {"key": "status", "value": "idle"}
      ]
    }
  ],
  "hasMore": false,
  "nextSeq": 12547
}
```

### 11.3 空缓冲区响应

```json
{
  "entries": [],
  "hasMore": false,
  "nextSeq": 0
}
```

### 11.4 错误响应

| 场景 | HTTP 状态 | 响应 |
|------|-----------|------|
| Agent 不存在 | `404` | `{"code":"AGENT_NOT_FOUND","message":"..."}` |
| Agent 离线 | `503` | `{"code":"AGENT_OFFLINE","message":"agent is offline, logs unavailable"}` |
| Agent 不可达（网络错误） | `503` | `{"code":"AGENT_OFFLINE","message":"agent unreachable: ..."}` |
| Admin 缓冲区未启用 | `200` | 返回空 `{entries:[], hasMore:false, nextSeq:0}` |
| Agent 缓冲区未启用 | `503` | `{"error":"log ring buffer not enabled"}` |
| 日志文件路径未配置 | `503` | `{"code":"STATUS_503","message":"log file not configured"}` |
| 无效文件名 | `400` | `{"code":"STATUS_400","message":"invalid file name"}` |
| 文件不存在 | `404` | `log file not found` |

---

## 12. 前端实现

### 12.1 整体架构

前端使用 Monaco 只读编辑器（非 antd Table），提供类似 VSCode 只读文本编辑器的体验：

- 光标、键盘导航（Home/End/Ctrl+Home/End、上下左右）
- Ctrl+F 内置搜索（高亮、上/下一个、大小写/正则/全词匹配）
- 级别着色（自定义 `stressbot-log` Monarch tokenizer 语言）
- 最新日志在底部，自动滚底
- 前端过滤（level 下拉 + 文本搜索）
- 日志文件下载弹窗

### 12.2 核心文件

| 文件 | 说明 |
|------|------|
| `cmd/web/src/components/monitoring/tabs/LogsTab.tsx` | 主组件，Monaco 编辑器 + 工具栏 + 轮询 |
| `cmd/web/src/components/monitoring/tabs/logLanguage.ts` | 自定义 `stressbot-log` Monarch tokenizer 和主题 |
| `cmd/web/src/components/monitoring/tabs/LogsTab.css` | Monaco find-widget 样式修复 |
| `cmd/web/src/services/logsApi.ts` | 日志 API 封装 |
| `cmd/web/src/types/api.ts` | `LogEntry`、`LogQueryResult`、`LogFileInfo` 类型定义 |

### 12.3 日志格式化

每条日志在到达时一次性格式化为纯文本行，后续过滤和拼接只操作 `text` 字符串：

```
TIMESTAMP  LEVEL(pad7)  CALLER  SERVICE  MESSAGE  JSON
```

示例：
```
2026/05/08 16:46:50.356608+0800  info    admin/admin.go:85  admin  agent 心跳恢复  {"agentId":"uuid-xxx","status":"idle"}
```

**格式化规则**：
- **TIMESTAMP**：从后端 RFC3339Nano 转换为 `YYYY/MM/DD HH:mm:ss.ffffff+ZZZZ` 格式。
- **LEVEL**：固定 7 字符宽度（含尾部空格），使 caller 列对齐。
- **CALLER**：源码位置（如 `admin/admin.go:85`）。
- **SERVICE**：服务名（`admin` / `agent` / `stressbot`）。
- **MESSAGE**：日志消息。
- **JSON**：附加字段序列化为 JSON 对象（仅在有字段时显示）。

### 12.4 自定义 Monaco 语言

**`logLanguage.ts`** 注册 Monarch 状态机 tokenizer，按位置解析日志行的各个字段：

```
tsGuard → sep1 → expectLevel → fieldCaller → sep3 → fieldService → sep4 → message
```

**Token 类型与着色**：

| Token | 含义 | 暗色前景 | 亮色前景 |
|-------|------|---------|---------|
| `log-timestamp` | 时间戳 | `#565f89` | `#b0bac6` |
| `log-level-debug` | Debug 级别 | `#7982a9` | `#7888a0` |
| `log-level-info` | Info 级别 | `#7aa2f7` | `#2b78ef` |
| `log-level-warn` | Warn 级别 | `#e0af68` | `#efa030` |
| `log-level-error` | Error 级别 | `#f7768e` | `#f06070` |
| `log-level-fatal` | DPanic/Panic/Fatal | `#bb9af7` | `#a88aff` |
| `log-source` | Caller/Service | `#7dcfff` | `#22bcd8` |
| `log-json` | JSON 字段 | `#9ece6a` | `#55c070` |

两个自定义主题：`stressbot-log-dark`（base: vs-dark）和 `stressbot-log-light`（base: vs）。

**状态机特点**：每个状态都 `include: '@tsGuard'`，确保新的时间戳行能正确触发重新解析（处理截断行的情况）。

### 12.5 轮询与数据管理

**轮询策略**：

- **正常间隔**：3000ms
- **追赶间隔**：100ms（当 `hasMore=true` 时，说明缓冲区有积压，快速追平）
- **切换 target 时**：立即触发一次拉取（`requestAnimationFrame` 技巧），不等 3s 周期

**增量拉取**：

- 首次 `afterSeq=0`，后续 `afterSeq=上次响应的 nextSeq`
- 过滤条件变更时重置 `afterSeq=0` 重新拉取全量

**客户端缓存上限**：

- Admin：`MAX_ENTRIES_ADMIN = 5000`
- Agent：`MAX_ENTRIES_AGENT = 50000`
- 超出时丢弃最旧的条目（FIFO）

### 12.6 Monaco 编辑器同步

**增量追加优化**：

1. 首次加载：`model.setValue(fullText)` + 滚到底部
2. 后续更新：检测新文本是否以旧文本 + `\n` 开头
   - 是：仅追加新增部分（`executeEdits`），避免全量替换
   - 否：全量替换（过滤条件变化等场景）

**自动滚底**：

- 监听 `onDidScrollChange`，判断用户是否在底部 30px 以内
- 仅在 `autoScrollRef.current === true` 时自动滚底
- 用户手动上滚查看历史时暂停自动滚底

### 12.7 Find Widget 中文化

Monaco 自带 find widget 的 tooltip 在 Ant Design 容器内会出现闪烁。解决方案：

1. CSS 完全隐藏 Monaco 自带 hover 弹层（`display: none !important`）
2. 在 `document.body` 上自绘一个跟随主题的浮层，通过 `aria-label` 匹配 Monaco 按钮的英文标签，显示对应中文提示
3. 350ms hover 延迟避免快速划过时闪烁

**修复的 CSS 问题**（`LogsTab.css`）：
- Monaco find-widget 按钮的 `box-sizing` 被 Ant Design 全局 `border-box` 影响导致错位，通过 `content-box !important` 修复。

### 12.8 日志文件下载

工具栏"下载日志"按钮打开 Modal 弹窗，显示日志文件列表（文件名、大小、修改时间），点击"下载"链接触发浏览器下载。

- Admin 文件：`GET /sbot/logs/admin/files/{name}`
- Agent 文件：`GET /sbot/logs/agents/{agentId}/files/{name}`

---

## 13. 性能分析

| 操作 | 开销 | 说明 |
|------|------|------|
| `Append` | <100ns | Write Lock，写一个数组槽位 + 原子递增 seq |
| `Query`（无过滤） | ~500us | Read Lock（极短），遍历缓冲区，复制最多 500 条 |
| 内存（Admin） | ~2.5MB | 5000 条 x 500 字节 |
| 内存（Agent） | ~25MB | 50000 条 x 500 字节 |
| 网络传输 | <50KB/次 | 200 条 x 约 250 字节 JSON |
| Admin 代理延迟 | +5~50ms | 取决于 Admin 与 Agent 之间的网络距离 |
| Monaco 增量追加 | <1ms | `executeEdits` 仅追加新行，不做全量替换 |

**压测影响**：Append 在每条日志的热路径上增加约 100ns（一次 mutex lock + 数组写入）。以 500 条/秒的峰值计算，额外 CPU 开销约 50us/秒，完全可忽略。

**fieldsToFields 的延迟序列化**：`entryWithSeq` 存储原始 `[]zapcore.Field`，仅在 Query 时通过 `fieldsToFields` 序列化。这意味着 Append 路径不承担序列化开销，序列化成本分摊到查询路径。

---

## 14. 文件变更清单

### 新增文件

| 文件 | 说明 |
|------|------|
| `logview/ringbuffer.go` | 环形缓冲区数据结构和查询 |
| `logview/capture.go` | `captureCore` 实现 `zapcore.Core`，将日志写入 RingBuffer |
| `logview/attach.go` | `AttachRingBuffer` / `GetRingBuffer` 公共 API |
| `cmd/web/src/components/monitoring/tabs/LogsTab.tsx` | 日志标签页主组件（Monaco 编辑器） |
| `cmd/web/src/components/monitoring/tabs/logLanguage.ts` | 自定义 `stressbot-log` Monarch tokenizer 和主题 |
| `cmd/web/src/components/monitoring/tabs/LogsTab.css` | Monaco find-widget 样式修复 |
| `cmd/web/src/services/logsApi.ts` | 日志 API 封装 |

### 修改文件

| 文件 | 改动 |
|------|------|
| `utils/log/logger.go` | 新增 `logFilePath` 变量、`GetLogFilePath()`、`ReplaceLogger()` |
| `admin/handlers.go` | 新增 6 个日志相关路由和 handler |
| `admin/helpers.go` | 新增 `parseLogQueryParams()` |
| `admin/admin.go` | 新增 `logsProxyClient` 字段 |
| `agent/http_server.go` | 新增 3 个日志相关路由和 handler |
| `cmd/admin/main.go` | `InitLog` 后调用 `logview.AttachRingBuffer` + `ReplaceLogger` |
| `cmd/agent/main.go` | `InitLog` 后调用 `logview.AttachRingBuffer` + `ReplaceLogger` |
| `cmd/web/src/types/api.ts` | 新增 `LogEntry`、`LogQueryResult`、`LogFileInfo` 类型 |

---

## 15. 与计划的差异

本节列出实际实现与 `plans/design-log-viewer.md` 之间的差异。

### 15.1 路由前缀

**计划中**：日志 API 路径为 `/api/logs/admin`、`/api/logs/agents/{id}` 等。

**实际实现**：所有路由使用 `/sbot/logs/` 前缀，与项目中其他 API 的 `/sbot/` 前缀保持一致。

具体映射：
| 计划路径 | 实际路径 |
|---------|---------|
| `/api/logs/admin` | `/sbot/logs/admin` |
| `/api/logs/agents/{id}` | `/sbot/logs/agents/{id}` |
| `/api/logs/admin/files` | `/sbot/logs/admin/files` |
| `/api/logs/admin/files/{name}` | `/sbot/logs/admin/files/{name}` |
| `/api/logs/agents/{id}/files` | `/sbot/logs/agents/{id}/files` |
| `/api/logs/agents/{id}/files/{name}` | `/sbot/logs/agents/{id}/files/{name}` |

### 15.2 AttachRingBuffer 签名

**计划中**：`AttachRingBuffer(logger *zap.Logger, size int) *zap.Logger`。

**实际实现**：`AttachRingBuffer(logger *zap.Logger, size int, initialFields ...zap.Field) *zap.Logger`。

增加了 `initialFields` 可变参数，用于传递 logger 创建时的初始字段（如 `zap.String("SR", "admin")`），使 `captureCore` 在 `Write` 时能正确提取 `service` 名称。计划中未考虑 `zap.Fields` 注入的字段在 `captureCore` 中不可见的问题。

调用示例：
```go
logview.AttachRingBuffer(stresslog.GetLogger(), 5000, zap.String("SR", "admin"))
logview.AttachRingBuffer(stresslog.GetLogger(), 50000, zap.String("SR", logTag))
```

### 15.3 captureCore.With 的字段累积

**计划中**：`With` 返回自身（共享同一个 RingBuffer）。

**实际实现**：`With` 返回新的 `captureCore` 实例，累积 fields。

```go
func (c *captureCore) With(fields []zapcore.Field) zapcore.Core {
    return &captureCore{
        ring:   c.ring,
        fields: append(c.fields[:len(c.fields):len(c.fields)], fields...),
    }
}
```

这是因为 `captureCore` 需要维护自己的 fields 链（用于在 `Write` 时提取 `SR` 字段），不能简单地返回自身。新的实例共享同一个 `RingBuffer` 指针，但拥有独立的 fields 切片。

### 15.4 Agent 端缓冲区未启用时的响应

**计划中**：Admin 端缓冲区未启用返回空结果；Agent 端缓冲区未启用返回 `503` + 错误信息。

**实际实现**：与计划一致。Admin 的 `handleGetAdminLogs` 返回 `200` + 空结果；Agent 的 `handleLogs` 返回 `503` + `{"error": "log ring buffer not enabled"}`。

### 15.5 文件下载的独立客户端

**计划中**：未明确区分日志代理客户端和文件下载客户端的超时。

**实际实现**：
- 日志查询代理：使用 `logsProxyClient`（5s 超时）。
- 日志文件下载代理：使用 `&http.Client{Timeout: 60 * time.Second}`（60s 超时，临时创建）。

大文件传输需要更长超时，使用独立客户端避免影响日志查询的响应时间。

### 15.6 Agent 文件列表的 fileInfo 类型

**计划中**：使用 `LogFileInfo` 共享类型。

**实际实现**：Agent 端的 `handleListLogFiles` 中使用局部 `fileInfo` 类型（非导出），Admin 端使用 `LogFileInfo`（导出）。两者字段完全相同（`Name`/`Size`/`ModTime`），但类型定义在不同位置。

### 15.7 helpers.go 文件

**计划中**：`parseLogQueryParams` 在 `admin/helpers.go` 中。

**实际实现**：与计划一致，`parseLogQueryParams` 位于 `admin/helpers.go`。此文件还包含 `stringOr`、`intOr`、`secsOr` 等配置辅助函数和 `parseUint64OrDefault` 工具函数。

### 15.8 Query 的读锁优化

**计划中**：Query 的读锁描述较为简单。

**实际实现**：Query 在获取读锁后仅读取 `count`、`head`、`buf` 和 `size` 四个字段，然后立即释放读锁。后续遍历 `buf` 切片时无锁。这是一个重要的性能优化：读锁持有时间极短（几个内存读取），不阻塞 Append 操作。

### 15.9 路由注册方式

**计划中**：路由使用 Go 1.22 的 `METHOD /path/{param}` 语法。

**实际实现**：与计划一致。Admin 端使用 `mux.HandleFunc("GET /sbot/logs/admin", ...)` 等新模式注册路由。Agent 端使用传统模式 `mux.HandleFunc("/agent/v1/logs", ...)`。

### 15.10 空文件列表处理

**计划中**：未明确说明空文件列表的行为。

**实际实现**：Agent 端在 `files == nil` 时显式设置为 `files = []fileInfo{}`，避免 JSON 序列化返回 `null`。Admin 端的 `listLogFiles` 返回的 `[]LogFileInfo` 在无文件时为 `nil`，`writeJSON` 的 JSON encoder 会将其序列化为 `null`——这是一个微小的差异，但不影响前端处理（前端统一处理 `null` 和 `[]` 两种情况）。
