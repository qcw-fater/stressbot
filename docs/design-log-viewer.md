# 日志查看功能设计

> **目标**：在前端 MonitorDock 新增"日志"标签页，支持实时查看 Admin 和各 Agent 的日志输出。
> **核心约束**：日志文件可达 500MB，不能全量传输或加载；不引入外部依赖（ELK/Loki）；复用现有 HTTP 轮询模式。

---

## 1. 方案选型

| 方案 | 优点 | 缺点 |
|------|------|------|
| **A. 读日志文件** | 可看历史 | 每次请求要解析大文件，性能差；需维护文件偏移量 |
| **B. MySQL 存储** | 支持查询和持久化 | 引入数据库依赖；写入路径变复杂 |
| **C. 内存环形缓冲区** ✅ | O(1) 写入、O(N) 查询无磁盘 IO；天然有界；零外部依赖 | 仅保留最近 N 条，无法看更早历史 |

**选择方案 C**：通过 zapcore.Core 包装器将每条日志同时写入文件和内存环形缓冲区。Admin 缓冲区保留最近 5000 条，Agent 缓冲区保留最近 50000 条。缓冲区容量覆盖压测高峰数秒至数分钟的输出，满足"实时查看最近状态"的核心需求。

**补充方案 A**：提供日志文件列表和下载端点，用于查看更早历史。Admin 直接读取本地日志目录，Agent 通过 Admin 代理转发。

---

## 2. 数据流

```
                        Agent 本地
zap → ringBuffer ──────────────────→ GET /agent/v1/logs
         │                                │
         │ GET /agent/v1/logs/files       │ Admin 代理
         │ GET /agent/v1/logs/files/{n}   ▼
         │                     GET /api/logs/agents/{id}
         │                            │
Admin 本地                            │
zap → ringBuffer ──→ GET /api/logs/admin
                        │                   │
                        │ GET /api/logs/admin/files
                        │ GET /api/logs/admin/files/{n}
                        │
                        └───── 前端轮询 ────┘
                                    │
                          LogsTab (Monaco Editor)
```

- **Admin 日志**：直接从内存读取，零延迟。
- **Agent 日志**：Admin 收到前端请求后，向 Agent 的 `GET /agent/v1/logs` 发起 HTTP 请求，将 JSON 响应原样透传给前端。
- **日志文件下载**：Admin 直接读取本地日志目录；Agent 的文件列表和下载请求由 Admin 代理转发。
- **前端轮询**：正常 3 秒间隔，有积压（`hasMore=true`）时缩短至 100ms 快速追平。

---

## 3. 后端：logview 包

环形缓冲区和 Zap 集成封装在独立的 `logview` 包中，与 `utils/log` 解耦。

### 3.1 包结构

| 文件 | 说明 |
|------|------|
| `logview/ringbuffer.go` | `RingBuffer` 数据结构、`Entry`/`Field`/`QueryParams`/`QueryResult` 类型、`Append`/`Query` 方法、`fieldsToFields` 辅助函数 |
| `logview/capture.go` | `captureCore` 实现 `zapcore.Core`，将日志写入 RingBuffer |
| `logview/attach.go` | `AttachRingBuffer` / `GetRingBuffer` 公共 API |

### 3.2 核心数据结构

**`logview/ringbuffer.go`**：

```go
package logview

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
    Limit    int    // 最大返回条数（默认 200，上限 500）
}

// QueryResult 查询结果。
type QueryResult struct {
    Entries []Entry `json:"entries"`
    HasMore bool    `json:"hasMore"` // 是否还有更多未返回的条目
    NextSeq uint64  `json:"nextSeq"` // 下次查询使用的游标
}

// RingBuffer 线程安全的固定大小环形缓冲区。
type RingBuffer struct {
    mu    sync.RWMutex
    buf   []entryWithSeq
    size  int
    head  int       // 下一个写入位置
    count int       // 当前条目数（增长到 size 后不变）
    seq   atomic.Uint64
}

type entryWithSeq struct {
    level   string
    time    time.Time
    caller  string
    message string
    service string
    fields  []zapcore.Field
    seq     uint64
}

func NewRingBuffer(size int) *RingBuffer

// Append 写入一条日志（O(1)，Write Lock）。
func (rb *RingBuffer) Append(level string, t time.Time, caller, message, service string, fields []zapcore.Field)

// Query 按游标查询（Read Lock）。不做 level/search 过滤，过滤由前端负责。
func (rb *RingBuffer) Query(params QueryParams) QueryResult

// ParseUint64OrDefault 解析 uint64，失败返回默认值。
func ParseUint64OrDefault(s string, def uint64) uint64
```

**`Append` 实现**：原子递增 `seq`，写入 `buf[head]`，`head = (head+1) % size`，`count = min(count+1, size)`。

**`Query` 实现**：
1. 从最老条目开始遍历（`start = (head - count + size) % size`）。
2. 跳过 `seq <= AfterSeq` 的条目。
3. 收集最多 `Limit` 条。若还有未收集的条目，`HasMore = true`。
4. `NextSeq` 为结果中最后一条的 seq。

**内存开销**：
- Admin：5000 条 x 约 500 字节/条 = 约 **2.5MB**
- Agent：50000 条 x 约 500 字节/条 = 约 **25MB**

### 3.3 Zap 集成：captureCore

**`logview/capture.go`**：

```go
package logview

// captureCore 将日志追加到 RingBuffer，不影响原有 core 链。
type captureCore struct {
    ring *RingBuffer
}
```

实现 `zapcore.Core` 接口：

- `Enabled` -> 始终返回 `true`
- `With` -> 返回自身（共享同一个 RingBuffer）
- `Check` -> `ce.AddCore(ent, c)` 并返回
- `Write` -> 从 `[]zapcore.Field` 中提取 `SR` 字段作为 `service`，调用 `ring.Append(level, time, caller, message, service, fields)`，返回 `nil`
- `Sync` -> 返回 `nil`

**关键设计**：`captureCore` 不包装原有 core 链，而是通过 `zapcore.NewTee` 并接在原有 core 旁边。原有 core（文件+控制台）完全不受影响，`captureCore` 只是额外接收一份日志写入环形缓冲区。

**Service 字段提取**：在 `Write` 方法中遍历 fields 查找 `Key == "SR"` 的字段，取其值作为 `service`。这是因为在 `utils/log.InitLog` 中通过 `zap.Fields(zap.String("SR", serviceName))` 注入了服务名。

### 3.4 公共 API：attach.go

**`logview/attach.go`**：

```go
package logview

var globalRingBuffer *RingBuffer

// AttachRingBuffer 将环形缓冲区捕获 core 挂到给定 logger 上，
// 返回修改后的 logger（调用方需通过 ReplaceLogger 同步回 utils/log）。
func AttachRingBuffer(logger *zap.Logger, size int) *zap.Logger {
    rb := NewRingBuffer(size)
    globalRingBuffer = rb
    cc := &captureCore{ring: rb}
    return logger.WithOptions(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
        return zapcore.NewTee(core, cc)
    }))
}

// GetRingBuffer 返回全局 RingBuffer（未 Attach 时为 nil）。
func GetRingBuffer() *RingBuffer {
    return globalRingBuffer
}
```

### 3.5 utils/log 集成

**修改 `utils/log/logger.go`**，新增两个函数：

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

**调用时机**：

- `cmd/admin/main.go`：
  ```go
  stresslog.InitLog(cfg.Log.Path, "admin", logConf, "")
  newLogger := logview.AttachRingBuffer(stresslog.GetLogger(), 5000)
  stresslog.ReplaceLogger(newLogger)
  ```
- `cmd/agent/main.go`：
  ```go
  stresslog.InitLog(logPath, "stressbot", logConf, "")
  newLogger := logview.AttachRingBuffer(stresslog.GetLogger(), 50000)
  stresslog.ReplaceLogger(newLogger)
  ```

**容量差异说明**：Admin 仅管理控制面日志，5000 条足够；Agent 运行压测产生大量业务日志，50000 条保留更完整的运行上下文。

---

## 4. 后端：Admin API

### 4.1 查询 Admin 日志

```
GET /api/logs/admin
```

**查询参数**：

| 参数 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `afterSeq` | uint64 | `0` | 游标：仅返回 seq > 此值的条目 |
| `limit` | int | `200` | 最大返回条数，上限 500 |

**响应**：

```json
{
  "entries": [
    {
      "level": "warn",
      "time": "2026-05-08T14:30:05.123456+08:00",
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
  "nextSeq": 12345
}
```

**空缓冲区时**：

```json
{
  "entries": [],
  "hasMore": false,
  "nextSeq": 0
}
```

**Handler 实现**（`admin/handlers.go`）：

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

### 4.2 代理查询 Agent 日志

```
GET /api/logs/agents/{id}
```

查询参数与 Admin 日志接口完全相同（`afterSeq` + `limit`），原样透传到 Agent。

**处理流程**：

1. 从 `AgentRegistry` 查找 Agent，获取 `Address`
2. 检查 Agent 状态不为 `offline`（离线则返回 `503 AGENT_OFFLINE`）
3. 构造请求 `GET http://{agentAddr}/agent/v1/logs?{原样透传查询参数}`
4. 使用 `logsProxyClient`（5 秒超时）发起请求
5. 将 Agent 返回的 JSON 响应原样写入 `http.ResponseWriter`

**Handler 实现**（`admin/handlers.go`）：

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

    url := fmt.Sprintf("http://%s/agent/v1/logs?%s", normalizeAddr(agent.Address), r.URL.RawQuery)
    proxyReq, _ := http.NewRequestWithContext(r.Context(), "GET", url, nil)

    resp, err := s.logsProxyClient.Do(proxyReq) // 独立 5s 超时客户端
    if err != nil {
        writeError(w, ErrAgentOffline.WithMessage("agent unreachable: "+err.Error()))
        return
    }
    defer resp.Body.Close()

    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    w.WriteHeader(resp.StatusCode)
    io.Copy(w, resp.Body)
}
```

### 4.3 日志文件列表和下载

**Admin 本地文件**：

```
GET /api/logs/admin/files          — 列出日志文件
GET /api/logs/admin/files/{name}   — 下载指定日志文件
```

**Agent 文件（Admin 代理）**：

```
GET /api/logs/agents/{id}/files          — 列出 Agent 日志文件（代理到 Agent）
GET /api/logs/agents/{id}/files/{name}   — 下载 Agent 日志文件（代理到 Agent）
```

**文件列表响应**：

```json
[
  {"name": "stressbot.log", "size": 524288000, "modTime": "2026-05-08 14:30:05"},
  {"name": "stressbot-2026-05-07.log.gz", "size": 12345678, "modTime": "2026-05-07 23:59:59"}
]
```

**实现细节**：

- `listLogFiles(logPath)` 工具函数：读取 `filepath.Dir(logPath)` 目录，按 `filepath.Base(logPath)` 的前缀匹配文件，返回 `[]LogFileInfo`。
- `serveLogFile(w, r, dir, name)` 工具函数：安全打开文件（验证文件名不含路径分隔符和 `..`），使用 `http.ServeContent` 流式下载。
- Agent 下载使用 60 秒超时的独立 HTTP 客户端（大文件传输需要更长超时）。

### 4.4 路由注册

在 `admin/handlers.go` 的 `registerRoutes` 中新增：

```go
// 日志
mux.HandleFunc("GET /api/logs/admin", s.handleGetAdminLogs)
mux.HandleFunc("GET /api/logs/agents/{id}", s.handleGetAgentLogs)
mux.HandleFunc("GET /api/logs/admin/files", s.handleListAdminLogFiles)
mux.HandleFunc("GET /api/logs/admin/files/{name}", s.handleDownloadAdminLogFile)
mux.HandleFunc("GET /api/logs/agents/{id}/files", s.handleListAgentLogFiles)
mux.HandleFunc("GET /api/logs/agents/{id}/files/{name}", s.handleDownloadAgentLogFile)
```

### 4.5 辅助函数

在 `admin/helpers.go` 中：

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

---

## 5. 后端：Agent Log 端点

### 5.1 Agent HTTP 端点

在 `agent/http_server.go` 的路由注册中新增：

```go
mux.HandleFunc("GET /agent/v1/logs", a.handleLogs)
mux.HandleFunc("GET /agent/v1/logs/files", a.handleListLogFiles)
mux.HandleFunc("GET /agent/v1/logs/files/", a.handleDownloadLogFile)
```

### 5.2 日志查询 Handler

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

### 5.3 文件列表 Handler

```go
func (a *Agent) handleListLogFiles(w http.ResponseWriter, r *http.Request) {
    logPath := stresslog.GetLogFilePath()
    if logPath == "" {
        writeJSONError(w, http.StatusServiceUnavailable, "log file not configured")
        return
    }
    // 读取 filepath.Dir(logPath)，按前缀过滤，返回 []fileInfo{name, size, modTime}
}
```

### 5.4 文件下载 Handler

```go
func (a *Agent) handleDownloadLogFile(w http.ResponseWriter, r *http.Request) {
    // 提取文件名：/agent/v1/logs/files/{name}
    // 安全校验：不含 / \ ..
    // 使用 http.ServeContent 流式下载
}
```

---

## 6. 前端实现

### 6.1 整体架构

前端使用 Monaco 只读编辑器（非 antd Table），提供类似 VSCode 只读文本编辑器的体验：

- 光标、键盘导航（Home/End/Ctrl+Home/End、上下左右）
- Ctrl+F 内置搜索（高亮、上/下一个、大小写/正则/全词匹配）
- 级别着色（自定义 `stressbot-log` Monarch tokenizer 语言）
- 最新日志在底部，自动滚底
- 前端过滤（level 下拉 + 文本搜索）
- 日志文件下载弹窗

**核心文件**：

| 文件 | 说明 |
|------|------|
| `cmd/web/src/components/monitoring/tabs/LogsTab.tsx` | 主组件，Monaco 编辑器 + 工具栏 + 轮询 |
| `cmd/web/src/components/monitoring/tabs/logLanguage.ts` | 自定义 `stressbot-log` Monarch tokenizer 和主题 |
| `cmd/web/src/components/monitoring/tabs/LogsTab.css` | Monaco find-widget 样式修复 |
| `cmd/web/src/services/logsApi.ts` | 日志 API 封装 |
| `cmd/web/src/types/api.ts` | `LogEntry`、`LogQueryResult`、`LogFileInfo` 类型定义 |

### 6.2 日志格式化

每条日志在到达时一次性格式化为纯文本行，后续过滤和拼接只操作 `text` 字符串：

```
TIMESTAMP  LEVEL(pad7)  CALLER  SERVICE  MESSAGE  JSON
```

示例：
```
2026/05/08 16:46:50.356608+0800  info    admin/admin.go:85  admin  agent 心跳恢复  {"agentId":"uuid-xxx","status":"idle"}
```

**格式化规则**：
- **TIMESTAMP**：从后端 RFC3339Nano 转换为 `YYYY/MM/DD HH:mm:ss.ffffff+ZZZZ` 格式（对齐后端控制台输出风格）
- **LEVEL**：固定 7 字符宽度（含尾部空格），使 caller 列对齐
- **CALLER**：源码位置（如 `admin/admin.go:85`）
- **SERVICE**：服务名（`admin` / `agent` / `stressbot`）
- **MESSAGE**：日志消息
- **JSON**：附加字段序列化为 JSON 对象（仅在有字段时显示）

### 6.3 自定义 Monaco 语言

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

### 6.4 轮询与数据管理

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

### 6.5 Monaco 编辑器同步

**增量追加优化**：

1. 首次加载：`model.setValue(fullText)` + 滚到底部
2. 后续更新：检测新文本是否以旧文本 + `\n` 开头
   - 是：仅追加新增部分（`executeEdits`），避免全量替换
   - 否：全量替换（过滤条件变化等场景）

**自动滚底**：

- 监听 `onDidScrollChange`，判断用户是否在底部 30px 以内
- 仅在 `autoScrollRef.current === true` 时自动滚底
- 用户手动上滚查看历史时暂停自动滚底

### 6.6 Find Widget 中文化

Monaco 自带 find widget 的 tooltip 在 Ant Design 容器内会出现闪烁。解决方案：

1. CSS 完全隐藏 Monaco 自带 hover 弹层（`display: none !important`）
2. 在 `document.body` 上自绘一个跟随主题的浮层，通过 `aria-label` 匹配 Monaco 按钮的英文标签，显示对应中文提示
3. 350ms hover 延迟避免快速划过时闪烁

**修复的 CSS 问题**（`LogsTab.css`）：

- Monaco find-widget 按钮的 `box-sizing` 被 Ant Design 全局 `border-box` 影响导致错位，通过 `content-box !important` 修复

### 6.7 日志文件下载

工具栏"下载日志"按钮打开 Modal 弹窗，显示日志文件列表（文件名、大小、修改时间），点击"下载"链接触发浏览器下载。

- Admin 文件：`GET /api/logs/admin/files/{name}`
- Agent 文件：`GET /api/logs/agents/{agentId}/files/{name}`

---

## 7. 前端接口汇总

### 7.1 接口列表

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/logs/admin` | 查询 Admin 日志（从内存环形缓冲区） |
| `GET` | `/api/logs/agents/{id}` | 查询指定 Agent 日志（Admin 代理到 Agent） |
| `GET` | `/api/logs/admin/files` | 列出 Admin 日志文件 |
| `GET` | `/api/logs/admin/files/{name}` | 下载 Admin 日志文件 |
| `GET` | `/api/logs/agents/{id}/files` | 列出 Agent 日志文件（Admin 代理） |
| `GET` | `/api/logs/agents/{id}/files/{name}` | 下载 Agent 日志文件（Admin 代理） |
| `GET` | `/agent/v1/logs` | Agent 本地日志查询端点 |
| `GET` | `/agent/v1/logs/files` | Agent 本地日志文件列表 |
| `GET` | `/agent/v1/logs/files/{name}` | Agent 本地日志文件下载 |

### 7.2 请求参数

```
GET /api/logs/admin?afterSeq=12345&limit=200
```

| 参数 | 类型 | 必填 | 默认 | 说明 |
|------|------|------|------|------|
| `afterSeq` | uint64 | 否 | `0` | 增量游标。首次传 0，后续传上次响应的 `nextSeq` |
| `limit` | int | 否 | `200` | 单次最大返回条数，上限 500 |

> level/search 过滤由前端在本地缓存中完成，不经过后端。

### 7.3 响应格式

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
    }
  ],
  "hasMore": true,
  "nextSeq": 12547
}
```

### 7.4 错误响应

| 场景 | HTTP 状态 | code |
|------|-----------|------|
| Agent 不存在 | `404` | `AGENT_NOT_FOUND` |
| Agent 离线 | `503` | `AGENT_OFFLINE` |
| Agent 不可达（网络错误） | `503` | `AGENT_OFFLINE` |
| 环形缓冲区未启用 | `200` | 返回空 `{entries:[], hasMore:false, nextSeq:0}` |
| Agent 环形缓冲区未启用 | `503` | `{"error": "log ring buffer not enabled"}` |

---

## 8. 文件变更清单

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
| `admin/handlers.go` | 新增日志查询、文件列表、文件下载路由和 handler |
| `admin/helpers.go` | 新增 `parseLogQueryParams()` |
| `agent/http_server.go` | 新增日志查询、文件列表、文件下载路由和 handler |
| `cmd/admin/main.go` | `InitLog` 后调用 `logview.AttachRingBuffer` + `ReplaceLogger` |
| `cmd/agent/main.go` | `InitLog` 后调用 `logview.AttachRingBuffer` + `ReplaceLogger` |
| `cmd/web/src/types/api.ts` | 新增 `LogEntry`、`LogQueryResult`、`LogFileInfo` 类型 |

---

## 9. 性能分析

| 操作 | 开销 | 说明 |
|------|------|------|
| `Append` | <100ns | Write Lock，写一个数组槽位 + 原子递增 seq |
| `Query`（无过滤） | ~500us | Read Lock，遍历缓冲区，复制最多 500 条 |
| 内存（Admin） | ~2.5MB | 5000 条 x 500 字节 |
| 内存（Agent） | ~25MB | 50000 条 x 500 字节 |
| 网络传输 | <50KB/次 | 200 条 x 约 250 字节 JSON |
| Admin 代理延迟 | +5~50ms | 取决于 Admin 与 Agent 之间的网络距离 |
| Monaco 增量追加 | <1ms | `executeEdits` 仅追加新行，不做全量替换 |

**压测影响**：Append 在每条日志的热路径上增加约 100ns（一次 mutex lock + 数组写入）。以 500 条/秒的峰值计算，额外 CPU 开销约 50us/秒，完全可忽略。
