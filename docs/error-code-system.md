# 动作错误码体系 — 技术文档

> 本文档对照实际代码（`errcode/code.go`、`errcode/error.go`、`errcode/registry.go`、`engine/errors.go`、`engine/action.go`、`network/connection.go`、`monitor/collector.go`、`monitor/snapshot.go`、`robot/robot.go`、`protocol/codec_resolver.go`）编写，反映当前已实施状态。§1.1、§14、§16 是体系演进的历史记录，仅供理解设计动机；现行规则以 `AGENTS.md` 与 `errcode/` 实现为准。

---

## 1. 概述

stressbot 使用单维数值错误码（`uint64`）区分框架内部错误和游戏服务端错误。核心设计包括：

- **单一 `code` 维度**唯一标识一类错误，码段契约：< 100 为框架保留段（工具自产，由 `errcode` 包 `codeRegistry` 分配），≥ 100 为业务段（服务器返回的 `headerErr` 原值）
- **`ActionError` 结构化类型**（`errcode/error.go`）替代 `fmt.Errorf` 自由格式字符串，携带 `{Code, Detail}` + cause，无 Kind 字段
- **monitor 错误分布按 `code` 单维聚合**，固定类别不再爆炸式增长；展示时按 `code < 100` 推导"框架"/"业务"标签
- **`CodedError` 接口**隔离 `monitor` 与错误产生方之间的依赖
- **`errors.json` 业务码映射**：扁平 `{"code":"中文描述"}`，加载期对 < 100 撞码硬报错（fail loud）

### 1.1 设计背景

改造前的问题：

| 问题 | 描述 |
|------|------|
| monitor error map 爆炸 | 所有错误通过 `fmt.Errorf` 生成自由格式字符串作为 key，不同参数组合产生独立条目 |
| 底层错误原因丢失 | 网络层请求-响应路径在所有失败路径统一返回无差异的失败值 |
| 超时未正确分类 | 所有超时场景用 `fmt.Errorf` 生成字符串，最终被归为 `ResultFailure` |
| 业务错误码未结构化 | 服务端 `headerErr` 被拼进字符串，无法按数值聚合 |
| callback 错误无感 | 推送回调失败只 Warn，monitor 完全无感 |

### 1.2 设计目标

1. 引入统一的单维数值 `code` 错误标识（码段契约 < 100 框架 / ≥ 100 业务）
2. 结构化 `ActionError` 类型替代自由格式字符串
3. 底层返回具体错误原因（连接层不再返回无差异的失败值）
4. monitor error map 按错误码聚合，相同 code 合并计数
5. 超时正确归类（通过错误码让 `classifyResult` 识别超时）
6. ctx 取消不计入失败率（`ResultCanceled`）
7. callback 路径接入错误聚合

---

## 2. 核心类型

### 2.1 errcode 顶层包 — `errcode/code.go` + `errcode/registry.go`

**为什么需要独立顶层包？**

动作层错误类型需要引用 `ErrorCode`；`monitor.recordError` 又需要从 error 中提取 Code。如果把常量放在 `monitor` 或 `engine`，会形成循环依赖。`errcode` 包只放常量、`ActionError` 和 `codeRegistry`，无业务依赖，任何包都可引用。

#### 2.1.1 ErrorCode + 码段契约

```go
// errcode/code.go:6
type ErrorCode uint64
```

统一错误码类型。**单一 code 唯一标识**，码段契约划分来源（不再需要 `Kind` 维度）：

| 码段 | 来源 | 分配方 |
|------|------|--------|
| `< 100` | 框架码（工具自产：连接/编码/Lua/配置等） | `errcode` 包 `codeRegistry` |
| `>= 100` | 业务码（服务器返回的 `headerErr` 原值） | `errors.json` 业务映射 |

码段以纯数值 `100` 为界。`protocol` 是通用零耦合模块、不 import `errcode`，因此撞码硬报错在 `protocol/codec_resolver.go:165-171` 用数值常量 `100` 表达同一契约。

#### 2.1.2 CodeInfo

```go
// errcode/code.go:62
type CodeInfo struct {
    Code uint64 `json:"code"` // 数值错误码
    Name string `json:"name"` // 大写下划线格式名称，如 "CONN_NOT_FOUND"
}
```

供 `GET /sbot/api/error-codes` 返回前端，用于 i18n 兜底与编辑器"框架保留码"展示。结构不含类别字段——前端按 `code < 100` 自行推导框架/业务标签。

---

## 3. 框架错误码完整列表（29 个）

框架错误码占据 `< 100` 保留段，按分类预留 10 个槽位（1-10, 11-20, ...），便于扩展。码段以数值 `100` 为界与业务码隔离，不再需要 Kind 维度。

**码 5 已退役且不得复用**：它曾用于 `CONN_DROPPED`，历史归档里可能仍有按 5 记录的样本，复用会让旧数据被贴上新含义的标签。连接被对端断开现在不产出独立的动作错误码——断开由 monitor 的 `Connections.Dropped` 计数呈现；主连接断开会停机器人，在途动作随 robot ctx 取消报 `ErrActionCanceled`（`errcode/code.go:12-15`）。

### 3.1 网络层（1-10，共 5 个）

| Code | 常量 | 名称 | 含义 |
|------|------|------|------|
| 1 | `ErrConnNotFound` | `CONN_NOT_FOUND` | 连接未建立（GetTCPConn/GetUDPConn 返回 nil） |
| 2 | `ErrConnClosed` | `CONN_CLOSED` | 连接已关闭（isClose == 1） |
| 3 | `ErrSendFailed` | `SEND_FAILED` | socket 写入失败（Send 返回错误）。含 HTTP 请求发送失败 |
| 4 | `ErrRecvTimeout` | `RECV_TIMEOUT` | 等待响应超时（select timeout）。`classifyResult` 按错误码归类为 `ResultTimeout` |
| 6 | `ErrActionCanceled` | `ACTION_CANCELED` | 流程/连接被取消（任务停止 / robot.Stop / 主连接断开触发的停机）。`Unwrap` 保留 `context.Canceled` 供 `errors.Is` 判定 |

### 3.2 协议层（11-20，共 2 个）

| Code | 常量 | 名称 | 含义 |
|------|------|------|------|
| 11 | `ErrEncodeFailed` | `ENCODE_FAILED` | codec 编码失败（TCP/UDP 编码均可能触发） |
| 12 | `ErrParseFailed` | `PARSE_FAILED` | S2C proto 解析失败 |

### 3.3 构建层（21-30，共 4 个）

| Code | 常量 | 名称 | 含义 |
|------|------|------|------|
| 21 | `ErrCreateMsg` | `CREATE_MSG` | 创建 C2S proto 消息失败 |
| 22 | `ErrBindField` | `BIND_FIELD` | 必需字段绑定失败（Required=true 的 binding 值为 nil） |
| 23 | `ErrSerialize` | `SERIALIZE` | C2S 消息序列化失败 |
| 24 | `ErrExecFailed` | `EXEC_FAILED` | 动作执行失败（onError.strategy=abort 时由 executor 产生） |

### 3.4 监听层（31-40，共 2 个）

| Code | 常量 | 名称 | 含义 |
|------|------|------|------|
| 31 | `ErrListenTimeout` | `LISTEN_TIMEOUT` | TCP/UDP Listen 轮询超时（超时时间内未收到匹配的推送） |
| 32 | `ErrListenRegister` | `LISTEN_REGISTER` | 注册持久监听失败 |

### 3.5 配置层（41-50，共 10 个）

| Code | 常量 | 名称 | 含义 |
|------|------|------|------|
| 41 | `ErrAddrEmpty` | `ADDR_EMPTY` | 连接地址为空（tcpConnect/udpConnect 配置的 address 为空） |
| 42 | `ErrURLEmpty` | `URL_EMPTY` | HTTP URL 为空 |
| 43 | `ErrURLScheme` | `URL_SCHEME` | HTTP URL 协议错误（缺 http:// 前缀） |
| 44 | `ErrUnknownPattern` | `UNKNOWN_PATTERN` | 未知动作模式（flow.json 中 pattern 配置错误） |
| 45 | `ErrHTTPBuild` | `HTTP_BUILD` | http.NewRequest 失败（含 JSON/form/default 三种 body 构建分支） |
| 46 | `ErrHTTPReadBody` | `HTTP_READ_BODY` | 读取 HTTP 响应体失败（io.ReadAll 返回错误） |
| 47 | `ErrMarshalBody` | `MARSHAL_BODY` | JSON/form 请求体序列化失败（覆盖两种序列化场景） |
| 48 | `ErrHTTPStatus` | `HTTP_STATUS` | HTTP 响应状态码非 2xx |
| 49 | `ErrHeartbeatConfig` | `HEARTBEAT_CONFIG` | 声明式心跳配置错误（intervalMs<=0 / route 缺失 / 字段非法） |
| 50 | `ErrStateConfig` | `STATE_CONFIG` | 状态动作配置错误（如 clearState 清除内置状态） |

### 3.6 Lua 层（51-60，共 4 个）

| Code | 常量 | 名称 | 含义 |
|------|------|------|------|
| 51 | `ErrLuaNotInit` | `LUA_NOT_INIT` | Lua 运行时未初始化（Robot 的 luaPool 或 l 为 nil） |
| 52 | `ErrLuaNoScript` | `LUA_NO_SCRIPT` | lua 动作缺少 script 配置（ActionDef.Script 为空） |
| 53 | `ErrLuaExecFailed` | `LUA_EXEC_FAILED` | Lua 脚本执行异常（加载失败 / await 非法 / 返回非法值等框架错误） |
| 54 | `ErrLuaScriptCheck` | `LUA_SCRIPT_CHECK` | 脚本校验失败（字段缺失/值不符等业务断言） |

### 3.7 回调层（61-70，共 2 个）

| Code | 常量 | 名称 | 含义 |
|------|------|------|------|
| 61 | `ErrCallbackLua` | `CALLBACK_LUA` | listen 脚本回调执行失败（`runListenScript` 中脚本运行出错，`robot/robot.go:1227`） |
| 62 | `ErrCallbackParse` | `CALLBACK_PARSE` | 推送消息解析失败（Go-store 回调物化待写值失败） |

### 3.8 错误码分配规则

- 框架码占据 `< 100` 保留段，业务码使用 `>= 100`，码段以数值 `100` 为界
- 每个分类预留 10 个槽位，便于扩展；退役的码（如 5）不回收复用
- 新增/重命名/废弃错误码只动 `codeRegistry`（单一数据源），`String()` 和 `AllCodes()` 从它派生

---

## 4. codeRegistry — 单一数据源

`codeRegistry` 是唯一真理源，定义在 `errcode/registry.go:3-33` 中（仅 `code` + `name` 两字段，按常量引用而非裸数字）：

```go
var codeRegistry = []CodeInfo{
    {uint64(ErrConnNotFound), "CONN_NOT_FOUND"},
    {uint64(ErrConnClosed), "CONN_CLOSED"},
    {uint64(ErrSendFailed), "SEND_FAILED"},
    {uint64(ErrRecvTimeout), "RECV_TIMEOUT"},
    {uint64(ErrActionCanceled), "ACTION_CANCELED"},
    {uint64(ErrEncodeFailed), "ENCODE_FAILED"},
    {uint64(ErrParseFailed), "PARSE_FAILED"},
    {uint64(ErrCreateMsg), "CREATE_MSG"},
    {uint64(ErrBindField), "BIND_FIELD"},
    {uint64(ErrSerialize), "SERIALIZE"},
    {uint64(ErrExecFailed), "EXEC_FAILED"},
    {uint64(ErrListenTimeout), "LISTEN_TIMEOUT"},
    {uint64(ErrListenRegister), "LISTEN_REGISTER"},
    {uint64(ErrAddrEmpty), "ADDR_EMPTY"},
    {uint64(ErrURLEmpty), "URL_EMPTY"},
    {uint64(ErrURLScheme), "URL_SCHEME"},
    {uint64(ErrUnknownPattern), "UNKNOWN_PATTERN"},
    {uint64(ErrHTTPBuild), "HTTP_BUILD"},
    {uint64(ErrHTTPReadBody), "HTTP_READ_BODY"},
    {uint64(ErrMarshalBody), "MARSHAL_BODY"},
    {uint64(ErrHTTPStatus), "HTTP_STATUS"},
    {uint64(ErrHeartbeatConfig), "HEARTBEAT_CONFIG"},
    {uint64(ErrStateConfig), "STATE_CONFIG"},
    {uint64(ErrLuaNotInit), "LUA_NOT_INIT"},
    {uint64(ErrLuaNoScript), "LUA_NO_SCRIPT"},
    {uint64(ErrLuaExecFailed), "LUA_EXEC_FAILED"},
    {uint64(ErrLuaScriptCheck), "LUA_SCRIPT_CHECK"},
    {uint64(ErrCallbackLua), "CALLBACK_LUA"},
    {uint64(ErrCallbackParse), "CALLBACK_PARSE"},
}
```

### 派生机制

1. **`codeNameIndex`** — 包初始化时从 `codeRegistry` 一次性建索引（`map[uint64]string`），`String()` O(1) 查询
2. **`String()`** — 自描述错误码，用于日志/CSV/前端 i18n 兜底。未注册的 code（含服务端 headerErr）返回空字符串
3. **`AllCodes()`** — 返回全部框架错误码定义的切片副本，供 `GET /sbot/api/error-codes` 透传给前端

**单一数据源约定（强制）**：新增/重命名/废弃错误码只动 `codeRegistry`，不要单独改 `String()` 或 `AllCodes()`。

---

## 5. ActionError 类型 — `errcode/error.go`

### 5.1 流程配置错误哨兵 — `engine/errors.go`

```go
var (
    ErrNodeNotFound     = errors.New("节点不存在")
    ErrUnknownNodeType  = errors.New("未知节点类型")
    ErrActionNotFound   = errors.New("动作不存在")
    ErrWeightedNoBranch = errors.New("weighted 节点权重之和 ≤ 0，无分支可选")
)
```

这些是 flow.json 配置错误，不是运行时动作错误。运行时动作错误统一使用 `ActionError.Code` 分类；超时类错误不依赖额外哨兵，由 `ErrRecvTimeout` / `ErrListenTimeout` 等错误码归类。`engine/errors.go` 另提供 `isCanceledError`（取消判据全包只此一处，`normalizeActionCancel` 与 `execRequest` 共用，避免两处判据漂移）。

### 5.2 ActionError 结构体

```go
// errcode/error.go:6
type ActionError struct {
    Code   ErrorCode // 单一 code 唯一标识（< 100 框架 / >= 100 业务）
    Detail string    // 上下文描述（service/route/elapsed 等），不含 [code] 前缀
    cause  error     // 可选下层错误，支持 errors.Is 链式判断
}
```

**字段说明**：

| 字段 | 用途 |
|------|------|
| `Code` | 单一错误码：框架码（`errcode.Err*`，< 100）或业务码（服务端 headerErr 原值，≥ 100） |
| `Detail` | 有限基数的上下文字符串（如 `service=logic route=CreateTeam`），不含 `[code]` 前缀，避免不同参数组合产生不同的聚合 key |
| `cause` | 可选下层 error，支持 `errors.Is()` 链式判断 |

### 5.3 构造器

#### NewActionError — 统一入口（框架码与业务码共用）

```go
// errcode/error.go:14
func NewActionError(code ErrorCode, detail string, cause ...error) *ActionError
```

框架码与业务码统一走此入口，无 `Kind` 维度区分。可选 `cause` 参数包装下层 error（如 proto 消息创建失败时透传原始 error）。

> 注：服务端 headerErr 直接以 `errcode.ErrorCode(headerErr)`（≥ 100）调用 `NewActionError`。调用方按 `code >= 100` 自行推导业务/框架属性。

### 5.4 方法

| 方法 | 签名 | 用途 |
|------|------|------|
| `Error()` | `string` | 格式：`[1] service=logic: cause` 或 `[1004] desc: route=CreateTeam` |
| `Unwrap()` | `error` | 返回 cause，支持 `errors.Is` 链式判断 |
| `ErrorCode()` | `uint64` | CodedError 接口方法，供 monitor 提取 code |
| `ErrorDetail()` | `string` | CodedError 接口方法，供 monitor 提取 Detail |

`Error()` 格式为 `[code] ...`，code 本身即区分来源（< 100 框架 / ≥ 100 业务），不需要 Kind 前缀。

---

## 6. CodedError 接口 — 避免循环依赖

`monitor` 不直接依赖动作层的具体错误类型。

### 6.1 接口定义 — `monitor/collector.go:141`

```go
// CodedError 带错误码的错误接口。monitor 包定义此接口以避免循环依赖 engine 包。
type CodedError interface {
    error
    ErrorCode() uint64   // 返回错误码（< 100 框架 / >= 100 业务）
    ErrorDetail() string // 返回错误详情（用于环形缓冲存储）
}
```

### 6.2 ActionError 实现接口

`errcode/error.go:36-39` 中 `ActionError` 实现该接口（只是把已有字段暴露成方法）：

```go
func (e *ActionError) ErrorCode() uint64   { return uint64(e.Code) }
func (e *ActionError) ErrorDetail() string { return e.Detail }
```

### 6.3 依赖关系

```
errcode（顶层包：常量 + ActionError + codeRegistry，无业务依赖）
    ↑
    ├── engine（引用 errcode 常量与 ActionError；errors.go 含哨兵与 isCanceledError）
    ├── monitor（引用 errcode（String()），定义 CodedError 接口）
    ├── network（引用 errcode 常量构造 ActionError）
    ├── protocol（零耦合，不 import errcode，用数值常量 100 表达码段契约）
    └── robot（引用 errcode + engine + monitor）
```

`monitor` 通过 `errors.AsType[CodedError](err)` 结构化提取，不依赖具体类型。

---

## 7. 各层错误码使用

### 7.1 连接层 — `network/connection.go`

请求-响应拆成两步：`Connection.SendRequest(ctx, data, routeKey, timeout)` 注册响应通道并立即发送，返回待响应句柄；等待侧由 `robotScheduler.awaitResponse`（`robot/scheduler.go`）在超时窗口内 select。各失败路径返回具体 ActionError：

| 条件 | 返回 |
|------|------|
| `c == nil` | `NewActionError(ErrConnNotFound, "routeKey=...")`（`connection.go:299`） |
| `isClose == 1` | `NewActionError(ErrConnClosed, serviceName + " routeKey=...")`（`connection.go:302`） |
| 调用方 ctx 已取消 | `canceledError` → `NewActionError(ErrActionCanceled, ...)`，`Unwrap` 保留 `ctx.Err()`（`connection.go:263-270`） |
| `Send()` 返回错误 | `NewActionError(ErrSendFailed, serviceName, sendErr)` |
| 等待窗口超时 | `NewActionError(ErrRecvTimeout, "service=... route=... timeout=...")`（`awaitResponse` 的 timer 分支） |
| 等待中 ctx 取消 / 响应通道被关闭 | `WaitOutcome{Canceled: true}` → 调用方转 `ErrActionCanceled`，monitor 归类 Canceled 而非 Timeout/Failure |

`Connection.Send(data)` 签名为 `(int, error)`：nil 连接 → `ErrConnNotFound`，已关闭 → `ErrConnClosed`，写失败 → `ErrSendFailed`（`connection.go:422-454`）。

### 7.2 网络委托接口 — `engine/action.go`

接口分两层（`engine/action.go:290-368`）：

**`NetSender`**（声明式动作与 Lua 运行时共用的基础能力面）：

| 方法 | 签名 |
|------|------|
| `TCPSend` / `UDPSend` | `(int, error)` |
| `TCPListen` / `UDPListen` | `(*NetExchange, error)`，`nil, nil` 表示超时 |
| `ConnectTCP` / `ConnectUDP` | `error` |
| `HTTPRequest` | `(*HTTPExchange, error)`（状态码、响应体、纯网络往返时长） |
| `CloseTCP` / `CloseUDP`、Get/Set 密钥、`GetTCPListenResp` / `GetUDPListenResp`、`EnsureTCPListener` / `EnsureUDPListener` | 无错误或非错误返回 |

**`ActionNetSender`**（`NetSender` 之上补两个取消感知的请求-响应方法）：

```go
TCPRequest(ctx context.Context, service string, packet []byte, routeKey string, timeout ...time.Duration) (*NetExchange, error)
UDPRequest(ctx context.Context, service string, packet []byte, routeKey string, timeout ...time.Duration) (*NetExchange, error)
```

拆两层而不是给 `NetSender` 全体方法加 ctx：请求-响应只有声明式动作在用（Lua 侧请求走 `script.Waiter` 协作式路径），而它是本层唯一「发出去后要等对端」的阻塞点，必须绑定流程 ctx 才能在任务停止时立刻退出在途请求。实现方契约：ctx 取消时必须尽快返回，且错误要能被识别为「取消」而非「超时」（`errcode.ErrActionCanceled` 或包 `context.Canceled` 的错误）。

心跳不再通过 NetSender 接口暴露注册入口，而是按 codec heartbeat 自动安装（`RegisterCodecHeartbeat`）；配置 `requireSecretKey` 的连接会延迟到 Set 密钥后启动。

### 7.3 netSenderAdapter — `robot/robot.go:1284`

所有网络操作通过 `netSenderAdapter` 适配，各方法在底层错误之上增加检查：

- `TCPSend`/`UDPSend`：连接 nil 时返回 `ErrConnNotFound`，否则透传 `Connection.Send` 的错误
- `TCPRequest`/`UDPRequest`：经 `robotScheduler.awaitResponse`（与 Lua `network.tcp_request` 同源）；连接 nil 时返回 `ErrConnNotFound`
- `ConnectTCP`/`ConnectUDP`：失败时返回 `ErrConnClosed`（`robot/robot.go:1530,1542`）
- `HTTPRequest`：URL 为空返回 `ErrURLEmpty`，协议错误返回 `ErrURLScheme`，构建失败返回 `ErrHTTPBuild`，发送失败返回 `ErrSendFailed`，读体失败返回 `ErrHTTPReadBody`（`robot/robot.go:1402-1459`）；HTTP 状态码非 2xx 在 action 层记 `ErrHTTPStatus`

### 7.4 Action 层 — `engine/action.go`

所有 `exec*` 方法使用结构化 ActionError。核心映射表：

| 方法 | 场景 | 使用的错误码 |
|------|------|--------|
| execTCPSend | buildBody 创建消息失败 | `NewActionError(ErrCreateMsg, "action=... proto=...")` |
| execTCPSend | buildBody 字段绑定失败 | `NewActionError(ErrBindField, "action=... field=...")` |
| execTCPSend | buildBody 序列化失败 | `NewActionError(ErrSerialize, ...)` |
| execTCPSend / execUDPSend | encode 失败 | `NewActionError(ErrEncodeFailed, "route=...")` |
| execTCPSend / execUDPSend | 发送失败 | 透传底层 ActionError |
| execTCPRequest / execUDPRequest | headerErr != 0 | `NewActionError(headerErr, ...)`（业务码 ≥ 100，经 `handleHeaderError` 补描述，`engine/action.go:1311`） |
| execTCPConnect / execUDPConnect | 地址为空 | `NewActionError(ErrAddrEmpty, "action=... service=...")` |
| execTCPConnect / execUDPConnect | 连接失败 | 透传 ConnectTCP/ConnectUDP 返回的 ActionError |
| execTCPListen / execUDPListen | headerErr != 0 | `NewActionError(headerErr, ...)`（业务码统一入口） |
| execTCPListen / execUDPListen | 事件等待超时 | `NewActionError(ErrListenTimeout, "service=... timeout=...")` |
| execHTTPRequest | URL 为空 | `NewActionError(ErrURLEmpty, "action=...")` |
| execHTTPRequest | JSON 序列化失败 | `NewActionError(ErrMarshalBody, "action=... type=json", err)` |
| execHTTPRequest | form 序列化失败 | `NewActionError(ErrMarshalBody, "action=... type=form", err)` |
| execHTTPRequest | 请求失败 | 透传 HTTPRequest 返回的 ActionError |
| execHTTPRequest | HTTP 状态码非 2xx | `NewActionError(ErrHTTPStatus, ...)` |
| execSetState / execClearState | 状态动作配置错误 | `NewActionError(ErrStateConfig, "action=...", err)`（`engine/action.go:998`） |
| parseAndStoreResponse | 解析失败 | `NewActionError(ErrParseFailed, "proto=...", err)` |
| Execute | 未知 pattern | `NewActionError(ErrUnknownPattern, "pattern=...")` |
| executor（`engine/executor.go:300`） | onError.strategy=abort | `NewActionError(ErrExecFailed, "action=...", err)` |
| executor（`engine/executor.go:381`） | 注册持久监听失败 | `NewActionError(ErrListenRegister, detail, err)` |

### 7.5 Lua 动作层 — `robot/robot.go` runLuaAction

| 场景 | 替换为 |
|------|--------|
| Lua 运行时未初始化 | `NewActionError(ErrLuaNotInit, "")` |
| lua 动作缺少 script 配置 | `NewActionError(ErrLuaNoScript, "")` |
| 脚本框架错误（加载失败 / await 非法 / 返回非法值） | `NewActionError(ErrLuaExecFailed, "script=...", err)` |
| 脚本 `return err table` | runtime 解析 table 重建 `*errcode.ActionError`（携带 Code/Detail）透传；脚本断言类失败（字段缺失/值不符）使用 `ErrLuaScriptCheck` |

### 7.6 Lua 桥 — `script/api_network.go`

NetSender 返回 err table 后，Lua API 把 err table 透传给脚本，WireBytes 由 `script.Context` 自动累计到当前 action/callback，**不折算回 errcode**：

```lua
-- 请求-响应类：双返回值 (err, data)
local err, body = network.tcp_request("logic", route, msg, "ResMsg")
-- 完整签名 network.tcp_request(service, route, msg [, s2c_proto [, timeout_sec]])
-- 少数请求路由和响应路由不同的接口可用 route 版本，返回约定一致
local err2, body2 = network.tcp_request_route("logic", reqRoute, respRoute, msg, "ResMsg")
-- err == nil → 成功，body 为响应数据
-- err 为 table → 失败（含 code/detail，框架错误码 1-99 或服务端错误码 >=100）

-- HTTP 请求：三返回值 (err, status, body)
-- 完整签名 network.http_request(url [, method [, content_type [, body]]])
-- content_type 为 "form"（默认）或 "json"，body 为键值对 table
local err, status, body = network.http_request(url, "POST", "form", { account = account })
-- err == nil → 成功

-- 发送/连接类：单返回值 err
local err = network.tcp_send("logic", route, msg)
local err = network.connect_tcp("logic", "1.2.3.4:9001")
-- err == nil → 成功；err 为 table → 失败
```

Go 侧调用 `ctx.NetSender.*` 时直接拿到 err table，原样返回给 Lua；脚本不接收 send/recv，也不处理 errcode 折算。

---

## 8. 错误分类 — classifyResult

`robot/robot.go:738` 中的 `classifyResult` 将 error 映射为 `monitor.ActionResult`：

```go
func classifyResult(err error) monitor.ActionResult {
    if err == nil {
        return monitor.ResultSuccess
    }
    // 任务取消优先级最高
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

func isTimeoutCode(code errcode.ErrorCode) bool { // ErrRecvTimeout / ErrListenTimeout
    ...
}
func isCanceledCode(code errcode.ErrorCode) bool { return code == errcode.ErrActionCanceled }
```

`ExecuteAction` 在分类前先经 `normalizeActionCancel`（`robot/robot.go:673`）：停止阶段 Lua 脚本可能因底层 ctx 取消让网络层返回 `ErrActionCanceled`，或脚本经 err table 重建出 `LISTEN_TIMEOUT`/`CONN_NOT_FOUND` 等 ActionError（实际根因是取消），在这里统一矫正为 `ErrActionCanceled`，避免 monitor 把 cancel 流量误归类为 Timeout/Failure。

### 8.1 ActionResult 分类

| 常量 | 值 | 含义 | 进 monitor 错误 map |
|------|---|------|---------------------|
| `ResultSuccess` | 0 | 执行成功 | 否 |
| `ResultFailure` | 1 | 执行失败（非超时） | 是（按 `code` 单维聚合） |
| `ResultTimeout` | 2 | 超时 | **是**（`recordError` 在 Failure 与 Timeout 分支都调用，`monitor/collector.go:629-631`） |
| `ResultCanceled` | 3 | ctx 取消（任务停止/连接断开） | 否 |

实现中没有 `ResultSkipped`，`classifyResult` 只有 4 个分支。`canceledCount` 的值计入 `ActionSnapshot.CanceledCount`，不参与 `sampleCount`（= success + failure + timeout）的计算；`totalActions` 也不计 canceled。

### 8.2 ctx.Err() 的归类

listen / 请求等待路径在 ctx 取消时返回 `context.Canceled`、`context.DeadlineExceeded` 或 `ErrActionCanceled`（Unwrap 保留 ctx.Err()），被 `classifyResult` 归类为 `ResultCanceled`。

- `ResultCanceled` 不进 monitor 错误 map，不污染 Apdex/SuccessRate
- `canceledCount` 不计入 `sampleCount`，取消不算样本

---

## 9. Monitor 错误聚合

### 9.1 ErrorEntry — `monitor/collector.go:100`

```go
type ErrorEntry struct {
    Code     uint64   `json:"code"`     // 错误码（< 100 框架 / >= 100 业务）
    CodeName string   `json:"codeName"` // ErrorCode.String()；业务码为 ""
    Messages []string `json:"msgs"`     // 最近若干条 Detail（单节点上界 3 条，环形缓冲）
    Count    int64    `json:"count"`    // 该错误累计出现次数
}
```

### 9.2 errKey — 聚合键

```go
type errKey struct {
    Code uint64 // 单一 code 维度
}
```

码段契约保证业务码（≥ 100）与框架码（< 100）数值天然不冲突，单一 code 即可作为聚合键；无需 Kind 区分。

### 9.3 errorBucket — 环形缓冲区

```go
const ErrorDetailRingSize = 3 // collector.go:97

type errorBucket struct {
    count   atomic.Int64                      // 累计出现次数
    msgRing [ErrorDetailRingSize]atomic.Value // 环形缓冲，存最近 3 条 Detail 字符串
    ringIdx atomic.Uint32                     // 环形缓冲写入位置（递增取模）
}
```

**环形缓冲机制**：

- `msgRing` 固定 3 个槽位，使用 `atomic.Value` 无锁读写
- `ringIdx` 持续递增，通过 `(ringIdx.Add(1) - 1) % 3` 确定写入位置
- 在 `uint32` 域内取模，确保结果在 `[0, 3)` 范围内，转 `int` 永远非负（避免 32 位系统上溢出）
- `snapshot()` 返回非空且不重复的 Detail 字符串列表

```go
func (b *errorBucket) record(detail string) {
    b.count.Add(1)
    idx := int((b.ringIdx.Add(1) - 1) % uint32(len(b.msgRing)))
    b.msgRing[idx].Store(detail)
}
```

### 9.4 recordError 实现

```go
func (c *MetricsCollector) recordError(am *actionMetrics, err error) {
    ce, ok := errors.AsType[CodedError](err)
    if !ok {
        return  // 无法提取 code 的 error，忽略
    }
    key := errKey{Code: ce.ErrorCode()}
    detail := ce.ErrorDetail()
    if len(detail) > 120 {
        detail = detail[:120]
    }

    if v, ok := am.errors.Load(key); ok {
        v.(*errorBucket).record(detail)
        return
    }
    b := &errorBucket{}
    b.record(detail)
    if actual, loaded := am.errors.LoadOrStore(key, b); loaded {
        actual.(*errorBucket).record(detail)
    }
}
```

**关键设计**：
- 使用 `errors.AsType[CodedError]` 提取接口，不依赖 `errcode.ActionError` 具体类型
- `LoadOrStore` 处理并发首次写入竞争
- Detail 截断至 120 字符，防止极端情况内存膨胀

### 9.5 RecordAction 签名

```go
func (c *MetricsCollector) RecordAction(name string, result ActionResult,
    timing ActionTiming, wallClock time.Duration,
    sendBytes, recvBytes int, err error)
```

（`monitor/collector.go:488`）携带完整耗时拆解（`timing`）与总耗时（`wallClock`），错误直接传 ActionError；`ResultFailure` 与 `ResultTimeout` 分支都会写入错误分布。

### 9.6 Callback 错误聚合

```go
func (c *MetricsCollector) RecordCallback(name string, result ActionResult,
    timing ActionTiming, wallClock time.Duration,
    sendBytes, recvBytes int, err error)   // 统一入口
func (c *MetricsCollector) RecordCallbackSuccess(name string)  // 成功：仅计数
func (c *MetricsCollector) RecordCallbackError(name string, err error)  // 失败：计数 + 错误聚合
```

回调名称以 `callback:` 前缀注册到 actions map 中，与普通 action 混排但可区分（回调不在 executing 口径内）。

---

## 10. MergeSnapshots 错误合并 — `monitor/snapshot.go`

分布式场景下合并多个 Agent 的 CollectorSnapshot 时（`MergeSnapshots` 返回 `(*CollectorSnapshot, error)`），错误按 `code` 单维聚合：

```go
const mergedErrorDetailLimit = 5 // snapshot.go:13

type mergedErrorKey struct{ Code uint64 }
errMap := make(map[mergedErrorKey]*ErrorEntry)

merge := func(es []ErrorEntry) {
    for _, e := range es {
        k := mergedErrorKey{Code: e.Code}
        if existing, ok := errMap[k]; ok {
            existing.Count += e.Count         // 计数累加
            // Messages 取并集去重
            for _, m := range e.Messages {
                if !slices.Contains(existing.Messages, m) {
                    existing.Messages = append(existing.Messages, m)
                }
            }
            if len(existing.Messages) > mergedErrorDetailLimit {
                existing.Messages = existing.Messages[:mergedErrorDetailLimit]
            }
        } else {
            cp := e
            errMap[k] = &cp
        }
    }
}
```

**合并规则**：
- Count：累加
- Messages：取并集去重，超过 5 条截断（单节点 3 条 × 多节点合并后最多 5 条——并集来自多个节点，各留一条样例比同一节点的三条更能反映故障面）
- Code/CodeName：相同 key 保持不变

---

## 11. 服务端错误码映射 — errors.json

### 11.1 文件位置

```
conf/adapter/
  ├── tcp_logic_codec.json   # 连接 codec：tcp:logic
  ├── tcp_battle_codec.json  # 连接 codec：tcp:battle
  ├── udp_battle_codec.json  # 连接 codec：udp:battle
  └── errors.json            # 可选：共享服务端错误码映射
```

### 11.2 格式

```json
{
  "1004": "金币不足",
  "1005": "等级不够"
}
```

`errors.json` 是扁平 `{"code":"中文描述"}` 映射。key 必须可解析为 `uint64` 且 **≥ 100**（码段契约：< 100 属框架保留段，业务码不得占用），value 为描述文本。加载由 `protocol/codec.LoadErrorMap`（`protocol/codec/errors.go:12`）完成。

### 11.3 Adapter 接口

```go
// protocol/adapter.go:50
type Adapter interface {
    // ... 其余方法 ...
    DescribeError(code uint64) string
}
```

### 11.4 LoadCodecResolver / SchemaAdapter 实现

生产加载流程（`protocol/codec_resolver.go`）：

1. `protocol.InferCodecMap(codecDir)` 扫描 `*_codec.json`，按文件名 `<proto>_<service>_codec.json` 推断 server 串 `<proto>:<service>`。
2. `protocol.LoadCodecResolver(codecDir, codecs, "errors.json")` 可选调用 `codec.LoadErrorMap` 加载共享错误码表。
3. 每份 codec schema 编译后与错误码 map 一起构建为 `SchemaAdapter`。
4. `SchemaAdapter.DescribeError(code)` 委托 codec 引擎，直接查询编译期注入的错误码 map；未配置或未命中返回空字符串。
5. 同一 codec 文件被多个 server 引用时 dedup，复用同一无状态 Adapter 实例。

`errors.json` 非空但加载失败（文件缺失、JSON 非法、key 非数字）会让 `LoadCodecResolver` 返回中文 error，启动期 fail loud；**码段撞码硬报错**——任一 key `< 100` 即视为占用框架保留段，返回形如 `codec 加载失败：错误码文件 "errors.json" 码 54 < 100 属框架保留段，业务码请使用 ≥ 100` 的 error（`protocol/codec_resolver.go:165-171`；`protocol` 是通用零耦合模块、不 import `errcode`，用纯数值常量 `100` 表达同一契约）。传空 `errorsFile` 则跳过错误码描述，Adapter 仍可用。

### 11.5 action 层集成

在 `tcpRequest` / `udpRequest` / listen 消费等收到非零 headerErr 的路径中，通过当前动作的 `<proto>:<service>` 解析对应 Adapter，再调用 `DescribeError(headerErr)` 补充描述（`engine/action.go:1311`）：

```go
func (ae *ActionExecutor) handleHeaderError(proto string, def *ActionDef, headerErr uint64, routeKey string, respBody []byte) *errcode.ActionError {
    ae.parseAndStoreResponse(def, respBody)
    desc := ae.describeError(proto, def.Service, headerErr)
    detail := "service=" + def.Service + " route=" + routeKey
    if desc != "" {
        detail = desc + ": " + detail
    }
    return errcode.NewActionError(errcode.ErrorCode(headerErr), detail)
}
```

`describeError` 内部使用 `CodecResolver.Resolve(proto + ":" + service)`；描述缺失不是致命错误，返回空串时仍按原 headerErr code（≥ 100 业务码）走统一 `NewActionError` 入口。

---

## 12. headerErr != 0 的行为（不变）

对 `headerErr != 0` 的处理：

| 维度 | 行为 |
|------|------|
| ActionResult | `ResultFailure` |
| failureCount | +1 |
| error map | 写入，key = headerErr 值（≥ 100 业务码，单一 code 维度） |
| RTT 直方图 / Apdex | 仍按真实 WireRTT 记录与打分（服务端正确处理了请求） |
| 响应解析 | 仍然 `parseAndStoreResponse`（后续 action 可用响应字段） |
| 字节统计 | sendBytes + recvBytes 都计入 |
| onError | 受 ignoreCodes / handler / retry / strategy 控制 |
| 流程中断 | 由 onError.strategy 决定 |

---

## 13. 前端适配

### 13.1 TypeScript 类型

```typescript
// cmd/web/src/types/api.ts:271
export interface ErrorEntry {
    code: number;        // 错误码数值（< 100 框架 / >= 100 业务）
    codeName: string;    // ErrorCode.String()，如 "RECV_TIMEOUT"；业务码为 ""
    msgs: string[];      // 最近 N 条 Detail，最多 3 条
    count: number;       // 累计计数
}
```

无 `kind` 字段——前端按 `code < 100` 自行推导"框架"/"业务"标签。

### 13.2 动作表展示（`cmd/web/src/components/monitoring/shared/ActionMetricsTable.tsx`）

- **错误展开行**：`[来源标签] [Code:CodeName] xCount | 最近 N 条 msgs`
  - 标签由 `code < 100` 推导：`< 100` 显示"框架"，`>= 100` 显示"业务"
- **"取消"列**：`canceledCount`，灰色
- **前端过滤**：`actionsOnly` 开关隐藏 `callback:*` 行（`ActionMetricsTable.tsx:226-246`）

### 13.3 错误码查询端点

```go
// admin/httpapi/routes.go:76
mux.HandleFunc("GET /sbot/api/error-codes", s.handleErrorCodeIndex)
```

前端启动时通过此端点拉取框架错误码全量表（29 条，全部 `< 100`），用于 i18n 兜底与编辑器"框架保留码"展示。返回数据量极小，可永久缓存。

### 13.4 适配器编辑器

适配器资源以声明式 JSON 为主：`*_codec.json` 描述各连接 codec，`errors.json` 提供可选共享业务错误码表。错误码文件可提供默认模板：

```json
{
  "1004": "金币不足"
}
```

### 13.5 errors.json 结构化表单 + 实时校验

前端 `ErrorMapEditor`（`cmd/web/src/components/modules/ErrorMapEditor.tsx`）把 `errors.json` 原文解析为行式 KV 表单（每行 = 一个码 + 描述），提供：

- **行级实时校验**（`validateErrorMap`）：码非正整数 / `< 100` 占用框架保留段 / 重复码 / 描述空，任一触发即标红并聚合到顶部 Alert（"N 处错误，保存前需全部修正"）。
- **保留码展示**：表单顶部以 Tag 列出全部框架保留码（`< 100`，由 `/sbot/api/error-codes` 拉取的 `frameworkCodes`），明确标注"不可用"。
- **序列化兜底**：`serializeErrorMap` 落库前丢弃码非数字或描述空的条目，保证写入 JSON 合法。
- **保存拦截**：`ProtocolConfigEditor` 在 `errors.json` 存在校验错误时 `message.error` 阻断保存，与后端 `LoadCodecResolver` 撞码硬报错形成前后端双重防线。

### 13.6 任务启动 multipart

`errors.json` 可选随 `*_codec.json` 一起提交；后端任务配置按资源文件保存并在运行时传给 `LoadCodecResolver`（撞码 `< 100` 会在 Agent 启动期 fail loud）。

---

## 14. 错误传播链路对比

### 改前

```
connection 请求-响应路径      → 无差异失败值         ← 原因丢失
  netSenderAdapter.TCPRequest → (nil, 0, false)      ← 原因丢失
    action.execTCPRequest     → fmt.Errorf(...)      ← 自由字符串，无法聚合
      robot.ExecuteAction     → errMsg string        ← 传字符串
        monitor.recordError   → key = 全字符串       ← 爆炸
```

### 改后

```
connection.SendRequest / awaitResponse → NewActionError(4/6/..., ...) ← 具体原因
  netSenderAdapter.TCPRequest           → (*NetExchange, error)       ← 透传 ActionError
    action.execTCPRequest               → err                         ← 透传或 NewActionError(1004, ...)
      robot.ExecuteAction               → err error                   ← 传 error
        monitor.recordError             → key = code                  ← 固定类别，可聚合
```

### monitor error map 对比

**改前**（每个参数组合一条）：
```
"TCP 请求失败: service=logic route=1001 respKey=1002 elapsed=2.3s"  → 15
"TCP 请求失败: service=logic route=1001 respKey=1002 elapsed=3.1s"  → 8
"服务端错误码 1004: service=logic route=CreateTeam"                  → 12
```

**改后**（按 `code` 单维聚合）：
```
4    → 26  msgs: ["service=logic respKey=1002 elapsed=2.3s", ...]  // RECV_TIMEOUT（框架码 < 100）
1004 → 15  msgs: ["service=logic route=CreateTeam"]                 // 业务码 >= 100
```

---

## 15. 当前涉及文件

| 文件 | 职责 |
|------|------|
| `errcode/code.go` | `ErrorCode` 类型 + 29 个框架错误码常量（含码 5 退役说明）+ `CodeInfo` |
| `errcode/error.go` | `ActionError` + `NewActionError` + `Error/Unwrap/ErrorCode/ErrorDetail`（实现 `monitor.CodedError`） |
| `errcode/registry.go` | `codeRegistry` 单一数据源 + `codeNameIndex` + `String()` + `AllCodes()` |
| `engine/errors.go` | 流程配置哨兵（4 个）+ `isCanceledError` 取消判据 |
| `engine/action.go` | `NetSender`/`ActionNetSender` 接口；所有 exec* 用结构化 ActionError；`handleHeaderError`/`describeError` |
| `engine/executor.go` | abort 时 `ErrExecFailed`；监听注册失败 `ErrListenRegister` |
| `engine/heartbeat.go` | 声明式心跳 body 构建（配置错误 fail loud） |
| `network/connection.go` | `SendRequest`/`Send` 各失败路径返回 ActionError；`canceledError` 统一取消错误 |
| `robot/scheduler.go` | `awaitResponse` 超时/取消分流（ErrRecvTimeout vs Canceled） |
| `robot/robot.go` | netSenderAdapter；executeLuaAction/runListenScript 用 ActionError；classifyResult + normalizeActionCancel；callback 接入 RecordCallback |
| `script/api_network.go` | Lua network API 透传 err table |
| `monitor/collector.go` | RecordAction/RecordCallback 签名；ErrorEntry/errKey/errorBucket；CodedError 接口；ResultCanceled |
| `monitor/snapshot.go` | MergeSnapshots 错误合并按 code key；ActionSnapshot 的 CanceledCount |
| `monitor/reporter.go` | 控制台输出 `[框架/业务 CodeName或#code]` 格式 |
| `protocol/adapter.go` | Adapter 接口的 `DescribeError` |
| `protocol/codec_resolver.go` | `LoadCodecResolver` 加载 `errors.json` 并撞码硬报错 |
| `protocol/codec/errors.go` | `LoadErrorMap` 读取 `errors.json` |
| `conf/adapter/errors.json` | 可选：服务端错误码映射 |
| `admin/httpapi/routes.go` | `GET /sbot/api/error-codes` 框架错误码全量表 |
| `agent/task/runner.go` | 下载适配器资源并传给 LoadCodecResolver |
| `standalone/app.go` / `agent/config.go` | 单机/Agent 装配：加载 CodecResolver、缺失 errors.json 时 Warn |
| 前端 | `types/api.ts`（ErrorEntry）+ `ActionMetricsTable` + `ErrorMapEditor` + `errorCodeRegistry` |

---

## 16. 历史设计决策记录

早期设计方案文档已从仓库移除，本节保留当时的关键取舍结论：

| 决策点 | 结论 |
|------|------|
| Kind 维度 | **已删除**：原计划 `(Kind, Code)` 二元组，改为单维 `code` + 码段契约（< 100 框架 / ≥ 100 业务），互不冲突 |
| Kind.String() | Kind 已删除；框架/业务标签由 `code < 100` 推导 |
| ResultSkipped | 未实施；实际只有 4 个值：Success/Failure/Timeout/Canceled |
| skippedCount | ActionSnapshot 中无此字段 |
| errors.json 未命中 | `DescribeError` 未命中返回空串，调用方仍保留原 code |
| RecordCallback 拆分 | 已实施：统一入口 `RecordCallback` + `RecordCallbackSuccess` / `RecordCallbackError` 包装 |
| 合并后 Messages 上限 | 5 条（`mergedErrorDetailLimit`） |
| 环形缓冲取模 | uint32 域取模，避免 32 位系统负索引 |
| ErrExecFailed / ErrHTTPStatus / ErrListenRegister / ErrHeartbeatConfig / ErrStateConfig | 早期清单之外补充的框架码，现均登记于 codeRegistry |
| codeRegistry 条目数 | 早期清单 24 个，现 29 个 |
| 码 5（CONN_DROPPED） | 已退役不得复用：断开由 `Connections.Dropped` 计数呈现，在途动作报 `ErrActionCanceled` |
| 超时进错误分布 | ResultTimeout 分支同样调用 `recordError`（早期只在 Failure 记录） |
| 前端 actionsOnly 过滤 | 已实施（隐藏 callback 行）；回调与动作混排展示 |
| errors.json 默认模板 | 扁平 JSON code→desc 映射 |
