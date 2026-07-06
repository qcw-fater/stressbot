# 动作错误码体系 — 技术文档

> 本文档基于 `plans/design-error-codes.md` 设计方案，对照实际代码（`errcode/codes.go`、`engine/errors.go`、`monitor/collector.go`、`robot/robot.go`、`adapter/lua_adapter.go`）编写。反映已实施状态。

---

## 1. 概述

stressbot 使用单维数值错误码（`uint64`）区分框架内部错误和游戏服务端错误。核心设计包括：

- **单一 `code` 维度**唯一标识一类错误，码段契约：< 100 为框架保留段（工具自产，由 `errcode` 包 `codeRegistry` 分配），≥ 100 为业务段（服务器返回的 `headerErr` 原值）
- **`ActionError` 结构化类型**替代 `fmt.Errorf` 自由格式字符串，携带 `{Code, Detail}` + cause
- **monitor 错误分布按 `code` 单维聚合**，固定类别不再爆炸式增长；展示时按 `code < 100` 推导"框架"/"业务"标签
- **`CodedError` 接口**隔离 `monitor` 与 `engine` 之间的循环依赖
- **`errors.json` 业务码映射**：扁平 `{"code":"中文描述"}`，加载期对 < 100 撞码硬报错（fail loud）

### 1.1 设计背景

改造前的问题：

| 问题 | 描述 |
|------|------|
| monitor error map 爆炸 | 所有错误通过 `fmt.Errorf` 生成自由格式字符串作为 key，不同参数组合产生独立条目 |
| 底层错误原因丢失 | `network/connection.go` 的 `RequestResponse` 在所有失败路径统一返回 `(nil, 0)` |
| 超时未正确分类 | 所有超时场景用 `fmt.Errorf` 生成字符串，最终被归为 `ResultFailure` |
| 业务错误码未结构化 | 服务端 `headerErr` 被拼进字符串，无法按数值聚合 |
| callback 错误无感 | 推送回调失败只 Warn，monitor 完全无感 |

### 1.2 设计目标

1. 引入统一的单维数值 `code` 错误标识（码段契约 < 100 框架 / ≥ 100 业务）
2. 结构化 `ActionError` 类型替代自由格式字符串
3. 底层返回具体错误原因（Connection、Adapter 层不再返回无差异的失败值）
4. monitor error map 按错误码聚合，相同 code 合并计数
5. 超时正确归类（通过 `Unwrap()` 链让 `classifyResult` 识别超时）
6. ctx 取消不计入失败率（新增 `ResultCanceled`）
7. callback 路径接入错误聚合

---

## 2. 核心类型

### 2.1 errcode 顶层包 — `errcode/codes.go`

**为什么需要独立顶层包？**

`engine.ActionError.Code` 需要引用 `ErrorCode` 类型；`monitor.recordError` 又需要从 error 中提取 Code。如果把常量放在 `monitor` 或 `engine`，会形成循环依赖。`errcode` 包只放常量和 `codeRegistry`，无业务依赖，任何包都可引用。

#### 2.1.1 ErrorCode + 码段契约

```go
type ErrorCode uint64
```

统一错误码类型。**单一 code 唯一标识**，码段契约划分来源（不再需要 `Kind` 维度）：

| 码段 | 来源 | 分配方 |
|------|------|--------|
| `< 100` | 框架码（工具自产：连接/编码/Lua/配置等） | `errcode` 包 `codeRegistry` |
| `>= 100` | 业务码（服务器返回的 `headerErr` 原值） | `errors.json` 业务映射 |

码段以纯数值 `100` 为界，`adapter` 是通用零耦合模块、不 import `errcode`，因此撞码硬报错在 `adapter/codec_resolver.go` 用数值常量 `100` 表达同一契约。

#### 2.1.2 CodeInfo

```go
type CodeInfo struct {
    Code uint64 `json:"code"` // 数值错误码
    Name string `json:"name"` // 大写下划线格式名称，如 "CONN_NOT_FOUND"
}
```

供 `GET /sbot/api/error-codes` 返回前端，用于 i18n 兜底与编辑器"框架保留码"展示。结构不含类别字段——前端按 `code < 100` 自行推导框架/业务标签。

---

## 3. 框架错误码完整列表（29 个）

框架错误码占据 `< 100` 保留段，按分类预留 10 个槽位（1-10, 11-20, ...），便于扩展。码段以数值 `100` 为界与业务码隔离，不再需要 Kind 维度。

### 3.1 网络层（1-10）

| Code | 常量 | 名称 | 含义 |
|------|------|------|------|
| 1 | `ErrConnNotFound` | `CONN_NOT_FOUND` | 连接未建立（GetTCPConn/GetUDPConn 返回 nil）。调用方传了 nil connection，语义是"连接未建立/已被销毁" |
| 2 | `ErrConnClosed` | `CONN_CLOSED` | 连接已关闭（isClose == 1） |
| 3 | `ErrSendFailed` | `SEND_FAILED` | socket 写入失败（Send 返回错误）。含 HTTP 请求发送失败 |
| 4 | `ErrRecvTimeout` | `RECV_TIMEOUT` | 等待响应超时（select timeout）。`classifyResult` 按错误码归类为 `ResultTimeout` |
| 5 | `ErrConnDropped` | `CONN_DROPPED` | 等待期间连接被对端断开（gnet OnClose 触发的 ctx.Done） |
| 6 | `ErrActionCanceled` | `ACTION_CANCELED` | 等待期间连接被本地主动关闭（任务停止 / robot.Stop / 业务 Close） |

### 3.2 协议层（11-20）

| Code | 常量 | 名称 | 含义 |
|------|------|------|------|
| 11 | `ErrEncodeFailed` | `ENCODE_FAILED` | SchemaAdapter/codec 编码返回 nil（TCP/UDP 编码均可能触发） |
| 12 | `ErrParseFailed` | `PARSE_FAILED` | S2C proto 解析失败（factory.Parse 返回错误） |

### 3.3 构建层（21-30）

| Code | 常量 | 名称 | 含义 |
|------|------|------|------|
| 21 | `ErrCreateMsg` | `CREATE_MSG` | 创建 C2S proto 消息失败（factory.Create 返回错误） |
| 22 | `ErrBindField` | `BIND_FIELD` | 必需字段绑定失败（Required=true 的 binding 值为 nil） |
| 23 | `ErrSerialize` | `SERIALIZE` | C2S 消息序列化失败（proto.Marshal 返回错误） |
| 24 | `ErrExecFailed` | `EXEC_FAILED` | 动作执行失败（onError.strategy=abort 时由 executor 产生） |

### 3.4 监听层（31-40）

| Code | 常量 | 名称 | 含义 |
|------|------|------|------|
| 31 | `ErrListenTimeout` | `LISTEN_TIMEOUT` | TCP/UDP Listen 轮询超时（超时时间内未收到匹配的推送） |
| 32 | `ErrListenRegister` | `LISTEN_REGISTER` | 注册持久监听失败 |

### 3.5 配置层（41-50）

| Code | 常量 | 名称 | 含义 |
|------|------|------|------|
| 41 | `ErrAddrEmpty` | `ADDR_EMPTY` | 连接地址为空（tcpConnect/udpConnect 配置的 address 为空） |
| 42 | `ErrURLEmpty` | `URL_EMPTY` | HTTP URL 为空 |
| 43 | `ErrURLScheme` | `URL_SCHEME` | HTTP URL 协议错误（缺 http:// 或 https:// 前缀） |
| 44 | `ErrUnknownPattern` | `UNKNOWN_PATTERN` | 未知动作模式（flow.json 中 pattern 配置错误） |
| 45 | `ErrHTTPBuild` | `HTTP_BUILD` | http.NewRequest 失败（含 JSON/form/default 三种 body 构建分支） |
| 46 | `ErrHTTPReadBody` | `HTTP_READ_BODY` | 读取 HTTP 响应体失败（io.ReadAll 返回错误） |
| 47 | `ErrMarshalBody` | `MARSHAL_BODY` | JSON/form 请求体序列化失败（覆盖两种序列化场景） |
| 48 | `ErrHTTPStatus` | `HTTP_STATUS` | HTTP 响应状态码非 2xx |
| 49 | `ErrHeartbeatConfig` | `HEARTBEAT_CONFIG` | 声明式心跳配置错误（intervalMs<=0 / route 缺失 / 字段非法） |

### 3.6 Lua 层（51-60）

| Code | 常量 | 名称 | 含义 |
|------|------|------|------|
| 51 | `ErrLuaNotInit` | `LUA_NOT_INIT` | Lua 运行时未初始化（Robot 的 luaPool 或 l 为 nil） |
| 52 | `ErrLuaNoScript` | `LUA_NO_SCRIPT` | lua 动作缺少 script 配置（ActionDef.Script 为空） |
| 53 | `ErrLuaExecFailed` | `LUA_EXEC_FAILED` | Lua 脚本执行异常（RunActionScript 返回 error） |
| 54 | `ErrLuaScriptCheck` | `LUA_SCRIPT_CHECK` | 脚本校验失败（字段缺失/值不符等业务断言） |

### 3.7 回调层（61-70）

| Code | 常量 | 名称 | 含义 |
|------|------|------|------|
| 61 | `ErrCallbackLua` | `CALLBACK_LUA` | 预留的旧脚本回调错误码；当前 listen 脚本回调已移除 |
| 62 | `ErrCallbackParse` | `CALLBACK_PARSE` | 推送消息解析失败（factory.Parse 返回错误） |

### 3.8 错误码分配规则

- 框架码占据 `< 100` 保留段，业务码使用 `>= 100`，码段以数值 `100` 为界
- 每个分类预留 10 个槽位，便于扩展
- 新增/重命名/废弃错误码只动 `codeRegistry`（单一数据源），`String()` 和 `AllCodes()` 从它派生

---

## 4. codeRegistry — 单一数据源

`codeRegistry` 是唯一真理源，定义在 `errcode/codes.go` 中（仅 `code` + `name` 两字段，无类别列）：

```go
var codeRegistry = []CodeInfo{
    {1,  "CONN_NOT_FOUND"},
    {2,  "CONN_CLOSED"},
    {3,  "SEND_FAILED"},
    {4,  "RECV_TIMEOUT"},
    {5,  "CONN_DROPPED"},
    {6,  "ACTION_CANCELED"},
    {11, "ENCODE_FAILED"},
    {12, "PARSE_FAILED"},
    {21, "CREATE_MSG"},
    {22, "BIND_FIELD"},
    {23, "SERIALIZE"},
    {24, "EXEC_FAILED"},
    {31, "LISTEN_TIMEOUT"},
    {32, "LISTEN_REGISTER"},
    {41, "ADDR_EMPTY"},
    {42, "URL_EMPTY"},
    {43, "URL_SCHEME"},
    {44, "UNKNOWN_PATTERN"},
    {45, "HTTP_BUILD"},
    {46, "HTTP_READ_BODY"},
    {47, "MARSHAL_BODY"},
    {48, "HTTP_STATUS"},
    {49, "HEARTBEAT_CONFIG"},
    {51, "LUA_NOT_INIT"},
    {52, "LUA_NO_SCRIPT"},
    {53, "LUA_EXEC_FAILED"},
    {54, "LUA_SCRIPT_CHECK"},
    {61, "CALLBACK_LUA"},
    {62, "CALLBACK_PARSE"},
}
```

### 派生机制

1. **`codeNameIndex`** — 包初始化时从 `codeRegistry` 一次性建索引（`map[uint64]string`），`String()` O(1) 查询
2. **`String()`** — 自描述错误码，用于日志/CSV/前端 i18n 兜底。未注册的 code（含服务端 headerErr）返回空字符串
3. **`AllCodes()`** — 返回全部框架错误码定义的切片副本，供 `GET /sbot/api/error-codes` 透传给前端

**单一数据源约定（强制）**：新增/重命名/废弃错误码只动 `codeRegistry`，不要单独改 `String()` 或 `AllCodes()`。

---

## 5. ActionError 类型 — `engine/errors.go`

### 5.1 流程配置错误哨兵

```go
var (
    ErrNodeNotFound    = errors.New("节点不存在")       // 流程配置错误
    ErrUnknownNodeType = errors.New("未知节点类型")     // 流程配置错误
    ErrActionNotFound  = errors.New("动作不存在")       // 流程配置错误
)
```

运行时动作错误统一使用 `ActionError.Code` 分类；超时类错误不再依赖额外哨兵，而是由 `ErrRecvTimeout` / `ErrListenTimeout` 等错误码归类。

### 5.2 ActionError 结构体

```go
type ActionError struct {
    Code   errcode.ErrorCode // 单一 code 唯一标识（< 100 框架 / >= 100 业务）
    Detail string            // 上下文描述（service/route/elapsed 等），不含 [code] 前缀
    cause  error             // 可选下层错误，支持 errors.Is 链式判断
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
func NewActionError(code errcode.ErrorCode, detail string, cause ...error) *ActionError
```

框架码与业务码统一走此入口，无 `Kind` 维度区分。可选 `cause` 参数包装下层 error（如 `factory.Create` 失败时透传原始 error）。

> 注：服务端 headerErr 直接以 `errcode.ErrorCode(headerErr)`（≥ 100）调用 `NewActionError`。调用方按 `code >= 100` 自行推导业务/框架属性。

### 5.4 方法

| 方法 | 签名 | 用途 |
|------|------|------|
| `Error()` | `string` | 格式：`[1] service=logic: cause` 或 `[1004] desc: route=CreateTeam` |
| `Unwrap()` | `error` | 返回 cause，支持 `errors.Is` 链式判断 |
| `ErrorCode()` | `uint64` | CodedError 接口方法，供 monitor 提取 code |
| `ErrorDetail()` | `string` | CodedError 接口方法，供 monitor 提取 Detail |

`Error()` 格式为 `[code] ...`，code 本身即区分来源（< 100 框架 / ≥ 100 业务），不再需要 Kind 前缀。

---

## 6. CodedError 接口 — 避免循环依赖

`monitor` 不能直接 `import "stressbot/engine"`，否则形成 `engine -> monitor -> engine` 循环依赖。

### 6.1 接口定义 — `monitor/collector.go`

```go
// CodedError 带错误码的错误接口。monitor 包定义此接口以避免循环依赖 engine 包。
type CodedError interface {
    error
    ErrorCode() uint64   // 返回错误码（< 100 框架 / >= 100 业务）
    ErrorDetail() string // 返回错误详情（用于环形缓冲存储）
}
```

### 6.2 ActionError 实现接口

`engine/errors.go` 中 `ActionError` 实现该接口（只是把已有字段暴露成方法）：

```go
func (e *ActionError) ErrorCode() uint64   { return uint64(e.Code) }
func (e *ActionError) ErrorDetail() string { return e.Detail }
```

### 6.3 依赖关系

```
errcode（顶层包，无业务依赖）
    ↑
    ├── engine（引用 errcode，定义 ActionError 实现 CodedError 接口方法）
    ├── monitor（引用 errcode，定义 CodedError 接口）
    ├── network（引用 errcode，使用 ErrorCode 常量）
    └── robot（引用 errcode + engine + monitor）
```

`monitor` 通过 `errors.As(err, &ce)` 提取 `CodedError`，不依赖 `engine` 包的具体类型。

---

## 7. 各层错误码使用

### 7.1 连接层 — `network/connection.go`

`Connection.RequestResponse` 签名已从 `(*Message, int)` 改为 `(*Message, error)`，各失败路径返回具体 ActionError：

| 条件 | 返回 |
|------|------|
| `c == nil` | `NewActionError(ErrConnNotFound, "nil connection responseKey=...")` — 不能访问 `c.serviceName` |
| `isClose == 1` | `NewActionError(ErrConnClosed, c.serviceName + " responseKey=...")` |
| `Send()` 返回错误 | `NewActionError(ErrSendFailed, c.serviceName + " responseKey=...", sendErr)` |
| `ctx.Done()` | `NewActionError(ErrConnDropped, c.serviceName + " responseKey=...")` |
| 超时 | `NewActionError(ErrRecvTimeout, c.serviceName + " routeKey=..." + " timeout=...")` |

`Connection.Send` 签名已从 `(bool, int)` 改为 `(int, error)`。

### 7.2 NetSender 接口变更 — `engine/action.go`

7 个方法签名从 `bool`/`int` 改为 `error`：

| 方法 | 旧签名 | 新签名 |
|------|--------|--------|
| `TCPSend` | `(bool, int)` | `(int, error)` |
| `UDPSend` | `(bool, int)` | `(int, error)` |
| `TCPRequest` | `(body, headerErr, bool)` | `(body, headerErr, error)` |
| `UDPRequest` | `(body, headerErr, bool)` | `(body, headerErr, error)` |
| `ConnectTCP` | `bool` | `error` |
| `ConnectUDP` | `bool` | `error` |
| `HTTPRequest` | 签名不变 | 内部改用 ActionError |

11 个方法签名不变（Close、Get/Set 密钥、Ensure 监听器等）；心跳不再通过 NetSender 接口暴露注册入口，而是按 codec heartbeat 自动安装；配置 `requireSecretKey` 的连接会延迟到 Set 密钥后启动。

### 7.3 netSenderAdapter — `robot/robot.go`

所有网络操作通过 `netSenderAdapter` 适配为 `NetSender` 接口，各方法在底层错误之上增加连接不存在检查：

- `TCPSend`/`UDPSend`：连接 nil 时返回 `ErrConnNotFound`，否则透传 `Connection.Send` 的错误
- `TCPRequest`/`UDPRequest`：连接 nil 时返回 `ErrConnNotFound`，否则透传 `Connection.RequestResponse` 的错误
- `ConnectTCP`/`ConnectUDP`：失败时返回 `ErrConnClosed`
- `HTTPRequest`：URL 为空返回 `ErrURLEmpty`，协议错误返回 `ErrURLScheme`，构建失败返回 `ErrHTTPBuild`，发送失败返回 `ErrSendFailed`，读体失败返回 `ErrHTTPReadBody`

### 7.4 Action 层 — `engine/action.go`

所有 `exec*` 方法中的 `fmt.Errorf` 替换为结构化 ActionError。核心替换表：

| 方法 | 场景 | 替换为 |
|------|------|--------|
| execTCPSend | buildBody 创建消息失败 | `NewActionError(ErrCreateMsg, proto)` |
| execTCPSend | buildBody 字段绑定失败 | `NewActionError(ErrBindField, field)` |
| execTCPSend | buildBody 序列化失败 | `NewActionError(ErrSerialize, proto)` |
| execTCPSend | encode 返回 nil | `NewActionError(ErrEncodeFailed, "route=...")` |
| execTCPSend | 发送失败 | 透传底层 ActionError |
| execTCPRequest | encode 返回 nil | `NewActionError(ErrEncodeFailed, ...)` |
| execTCPRequest | !ok | 透传底层 ActionError |
| execTCPRequest | headerErr != 0 | `NewActionError(headerErr, "service=... route=...")`（业务码 ≥ 100，走统一入口） |
| execTCPConnect | 地址为空 | `NewActionError(ErrAddrEmpty, "service=...")` |
| execTCPConnect | 连接失败 | 透传 ConnectTCP 返回的 ActionError |
| execUDPConnect | 地址为空 | `NewActionError(ErrAddrEmpty, "service=...")` |
| execUDPConnect | 连接失败 | 透传 ConnectUDP 返回的 ActionError |
| execTCPListen | headerErr != 0 | `NewActionError(headerErr, ...)`（业务码统一入口） |
| execTCPListen | 轮询超时 | `NewActionError(ErrListenTimeout, "service=... timeout=...")` |
| execUDPSend | encode 返回 nil | `NewActionError(ErrEncodeFailed, ...)` |
| execUDPSend | 发送失败 | 透传底层 ActionError |
| execUDPRequest | encode/!ok/headerErr | 同 TCPRequest 模式 |
| execUDPListen | headerErr/超时 | 同 TCPListen 模式 |
| execHTTPRequest | URL 为空 | `NewActionError(ErrURLEmpty, "action=...")` |
| execHTTPRequest | JSON 序列化失败 | `NewActionError(ErrMarshalBody, "action=... type=json", err)` |
| execHTTPRequest | form 序列化失败 | `NewActionError(ErrMarshalBody, "action=... type=form", err)` |
| execHTTPRequest | 请求失败 | 透传 HTTPRequest 返回的 ActionError |
| execHTTPRequest | HTTP 状态码非 2xx | `NewActionError(ErrHTTPStatus, ...)` |
| parseAndStoreResponse | 解析失败 | `NewActionError(ErrParseFailed, "proto=...", err)` |
| Execute | 未知 pattern | `NewActionError(ErrUnknownPattern, "pattern=...")` |

### 7.5 Lua 动作层 — `robot/robot.go` executeLuaAction

| 场景 | 替换为 |
|------|--------|
| Lua 运行时未初始化 | `NewActionError(ErrLuaNotInit, "")` |
| lua 动作缺少 script 配置 | `NewActionError(ErrLuaNoScript, "")` |
| 脚本执行异常 | `NewActionError(ErrLuaExecFailed, "script=...", err)` |
| 脚本 `return err table` | runtime 解析 table 重建 `*ActionError`（携带 Code/Detail）透传；脚本断言类失败（字段缺失/值不符）使用 `ErrLuaScriptCheck` |
| 脚本返回 number 等非法值 | fail loud（报错），不再走旧式 code 退出码路径 |

### 7.6 Lua 桥适配 — `script/api_network.go`

NetSender 接口返回 err table 后，Lua API 直接把 err table 透传给脚本，WireBytes 由 `script.Context` 自动累计到当前 action/callback，**不再折算回 errcode**：

```lua
-- 请求-响应类：双返回值 (err, data)
local err, body = network.tcp_request("logic", route, msg, "ResMsg")
-- 少数请求路由和响应路由不同的接口可用 route 版本，返回约定一致
local err2, body2 = network.tcp_request_route("logic", reqRoute, respRoute, msg, "ResMsg")
-- err == nil → 成功，body 为响应数据
-- err 为 table → 失败（含 code/detail，框架错误码 1-99 或服务端错误码 >=100）

-- HTTP 请求：三返回值 (err, status, body)
-- content_type 为 "form"（默认）或 "json"，body 为键值对 table
local err, status, body = network.http_request(url, "POST", "form", { account = account })
-- err == nil → 成功

-- 发送/连接类：单返回值 err
local err = network.tcp_send("logic", route, msg)
local err = network.connect_tcp("logic", "1.2.3.4:9001")
-- err == nil → 成功；err 为 table → 失败
```

Go 侧改造：调用 `ctx.NetSender.*` 时直接拿到 err table，原样返回给 Lua；脚本不再接收或返回 send/recv，也不再处理 errcode 折算。

---

## 8. 错误分类 — classifyResult

`robot/robot.go` 中的 `classifyResult` 将 error 映射为 `monitor.ActionResult`：

```go
func classifyResult(err error) monitor.ActionResult {
    if err == nil {
        return monitor.ResultSuccess
    }
    // 任务取消优先级最高
    if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
        return monitor.ResultCanceled
    }
    if actionErr, ok := errors.AsType[*engine.ActionError](err); ok {
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

### 8.1 ActionResult 分类

| 常量 | 值 | 含义 | 进 monitor 错误 map |
|------|---|------|---------------------|
| `ResultSuccess` | 0 | 执行成功 | 否 |
| `ResultFailure` | 1 | 执行失败（非超时） | 是（按 `code` 单维聚合） |
| `ResultTimeout` | 2 | 超时 | 否 |
| `ResultCanceled` | 3 | ctx 取消（任务停止/连接断开） | 否 |

**与计划的差异**：计划中包含 `ResultSkipped`（值为 3），但实际实现中没有使用 `ResultSkipped`。`classifyResult` 只有 4 个分支。`canceledCount` 的值计入 `ActionSnapshot.CanceledCount`，不参与 `sampleCount`（= success + failure + timeout）的计算。

### 8.2 ctx.Err() 的归类

`execTCPListen`/`execUDPListen` 在 ctx 取消时返回 `context.Canceled` 或 `context.DeadlineExceeded`，被 `classifyResult` 归类为 `ResultCanceled`。

- `ResultCanceled` 不进 monitor 错误 map，不污染 Apdex/SuccessRate
- `canceledCount` 不计入 `sampleCount`，取消不算样本

---

## 9. Monitor 错误聚合

### 9.1 ErrorEntry — `monitor/collector.go`

```go
type ErrorEntry struct {
    Code     uint64   `json:"code"`     // 错误码（< 100 框架 / >= 100 业务）
    CodeName string   `json:"codeName"` // ErrorCode.String()；业务码为 ""
    Messages []string `json:"msgs"`     // 最近 N 条 Detail（最多 3 条，环形缓冲）
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
type errorBucket struct {
    count   atomic.Int64    // 累计出现次数
    msgRing [3]atomic.Value // 环形缓冲，存最近 3 条 Detail 字符串
    ringIdx atomic.Uint32   // 环形缓冲写入位置（递增取模）
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
    var ce CodedError
    if !errors.As(err, &ce) {
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
- 使用 `errors.As` 提取 `CodedError` 接口，不依赖 `engine.ActionError` 具体类型
- `LoadOrStore` 处理并发首次写入竞争
- Detail 截断至 120 字符，防止极端情况内存膨胀

### 9.5 RecordAction 签名

```go
func (c *MetricsCollector) RecordAction(name string, result ActionResult,
    duration time.Duration, sendBytes, recvBytes int, err error)
```

从设计前的 `errMsg string` 改为 `err error`，直接传递 ActionError。

### 9.6 Callback 错误聚合

```go
func (c *MetricsCollector) RecordCallbackSuccess(name string)  // 成功：仅计数
func (c *MetricsCollector) RecordCallbackError(name string, err error)  // 失败：计数 + 错误聚合
```

回调名称以 `callback:` 前缀注册到 actions map 中，与普通 action 混排但可区分。

---

## 10. MergeSnapshots 错误合并 — `monitor/snapshot.go`

分布式场景下合并多个 Agent 的 CollectorSnapshot 时，错误按 `code` 单维聚合：

```go
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
            if len(existing.Messages) > 5 {    // 超过 5 条截断
                existing.Messages = existing.Messages[:5]
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
- Messages：取并集去重，超过 5 条截断（单节点 3 条 × 多节点合并后最多 5 条）
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

`errors.json` 是扁平 `{"code":"中文描述"}` 映射。key 必须可解析为 `uint64` 且 **≥ 100**（码段契约：< 100 属框架保留段，业务码不得占用），value 为描述文本。实际项目中的 `conf/adapter/errors.json` 覆盖 antnet 框架业务码 + 区服/登录 256-299 + 各业务模块错误码（U2.1 清理后约 639 条，全部 ≥ 100）。

### 11.3 Adapter 接口扩展

```go
// adapter/adapter.go
type Adapter interface {
    // ... 现有方法 ...
    DescribeError(code uint64) string
}
```

### 11.4 LoadCodecResolver / SchemaAdapter 实现

生产加载流程：

1. `adapter.InferCodecMap(codecDir)` 扫描 `*_codec.json`，按文件名 `<proto>_<service>_codec.json` 推断 server 串 `<proto>:<service>`。
2. `adapter.LoadCodecResolver(codecDir, codecs, "errors.json")` 可选调用 `codec.LoadErrorMap` 加载共享错误码表。
3. 每份 codec schema 通过 `codec.LoadSchema` + `adapter.NewSchemaAdapter(schema, errorMap)` 编译为 `SchemaAdapter`。
4. `SchemaAdapter.DescribeError(code)` 委托 `codec.SchemaCodec.DescribeError(code)`，直接查询编译期注入的错误码 map；未配置或未命中返回空字符串。
5. 同一 codec 文件被多个 server 引用时 dedup，复用同一无状态 Adapter 实例。

`errors.json` 非空但加载失败（文件缺失、JSON 非法、key 非数字）会让 `LoadCodecResolver` 返回中文 error，启动期 fail loud；**码段撞码硬报错**——任一 key `< 100` 即视为占用框架保留段，`LoadCodecResolver` 返回形如 `codec 加载失败：错误码文件 "errors.json" 码 54 < 100 属框架保留段，业务码请使用 ≥ 100` 的 error，启动期 fail loud（`adapter` 是通用零耦合模块、不 import `errcode`，用纯数值常量 `100` 表达同一契约）。传空 `errorsFile` 则跳过错误码描述，Adapter 仍可用。

### 11.5 action 层集成

在 `tcpRequest` / `udpRequest` / listen 消费等收到非零 headerErr 的路径中，通过当前动作的 `<proto>:<service>` 解析对应 Adapter，再调用 `DescribeError(headerErr)` 补充描述：

```go
func (ae *ActionExecutor) handleHeaderError(proto string, def *ActionDef, headerErr uint64, routeKey string, respBody []byte) *ActionError {
    ae.parseAndStoreResponse(def, respBody)
    desc := ae.describeError(proto, def.Service, headerErr)
    detail := "service=" + def.Service + " route=" + routeKey
    if desc != "" {
        detail = desc + ": " + detail
    }
    return NewActionError(errcode.ErrorCode(headerErr), detail)
}
```

`describeError` 内部使用 `CodecResolver.Resolve(proto + ":" + service)`；描述缺失不是致命错误，返回空串时仍按原 headerErr code（≥ 100 业务码）走统一 `NewActionError` 入口。

---

## 12. headerErr != 0 的行为（不变）

对 `headerErr != 0` 的处理完全沿用现有行为：

| 维度 | 行为 |
|------|------|
| ActionResult | `ResultFailure` |
| failureCount | +1 |
| error map | 写入，key = headerErr 值（≥ 100 业务码，单一 code 维度） |
| 响应解析 | 仍然 `parseAndStoreResponse`（后续 action 可用响应字段） |
| 字节统计 | sendBytes + recvBytes 都计入 |
| onError | 受 ignoreCodes / handler / retry / strategy 控制 |
| 流程中断 | 由 onError.strategy 决定 |

---

## 13. 前端适配

### 13.1 TypeScript 类型

```typescript
export interface ErrorEntry {
    code: number;        // 错误码数值（< 100 框架 / >= 100 业务）
    codeName: string;    // ErrorCode.String()，如 "RECV_TIMEOUT"；业务码为 ""
    msgs: string[];      // 最近 N 条 Detail，最多 3 条
    count: number;       // 累计计数
}
```

无 `kind` 字段——前端按 `code < 100` 自行推导"框架"/"业务"标签。

### 13.2 ActionsTab 展示

- **错误展开行**：`[来源标签] [Code:CodeName] xCount | 最近 N 条 msgs`
  - 标签由 `code < 100` 推导：`< 100` 显示"框架"（蓝色），`>= 100` 显示"业务"（橙色）
- **新增"取消"列**：`canceledCount`，灰色，与"跳过"列并列
- **前端过滤**：
  - `actionsOnly` 开关：隐藏 `callback:*` 行
  - `callbacksOnly` 开关：只显示 `callback:*` 行
  - 两者互斥，默认混排，callback 行用"推送"标签标识

### 13.3 错误码查询端点

```go
// admin/handlers.go
mux.HandleFunc("GET /sbot/api/error-codes", s.handleErrorCodeIndex)
```

前端启动时通过此端点拉取框架错误码全量表（约 29 条，全部 `< 100`），用于 i18n 兜底与编辑器"框架保留码"展示。返回数据量极小，可永久缓存。

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
connection.RequestResponse  → (nil, 0)         ← 原因丢失
  netSenderAdapter.TCPRequest → (nil, 0, false) ← 原因丢失
    action.execTCPRequest     → fmt.Errorf(...)  ← 自由字符串，无法聚合
      robot.ExecuteAction     → errMsg string    ← 传字符串
        monitor.recordError   → key = 全字符串   ← 爆炸
```

### 改后

```
connection.RequestResponse  → (nil, NewActionError(4, ...))  ← 具体原因
  netSenderAdapter.TCPRequest → (nil, 0, err)               ← 透传 ActionError
    action.execTCPRequest     → err                          ← 透传或 NewActionError(1004, ...)
      robot.ExecuteAction     → err error                    ← 传 error
        monitor.recordError   → key = code                   ← 固定类别，可聚合
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

## 15. 涉及文件清单

| 文件 | 变更类型 | 说明 |
|------|----------|------|
| `errcode/codes.go` | 新建 | ErrorCode 常量 + codeRegistry + String() + AllCodes()（无 Kind） |
| `engine/errors.go` | 新建 | ActionError + NewActionError + CodedError 接口方法（无 Kind） |
| `network/connection.go` | 修改 | Send + RequestResponse 签名变更，各路径返回 ActionError |
| `network/heartbeat.go` | 修改 | 适配 Send 新签名 |
| `engine/action.go` | 修改 | NetSender 接口 7 个方法签名变更；所有 exec* 改用 ActionError；ErrExecFailed + ErrHTTPStatus |
| `engine/executor.go` | 修改 | onError.strategy=abort 时使用 ErrExecFailed |
| `script/api_network.go` | 修改 | 6 个 Lua API 适配 NetSender 新签名 |
| `robot/robot.go` | 修改 | netSenderAdapter 适配；executeLuaAction 改用 ActionError；HTTPRequest 替换；classifyResult 增加 ctx 判断；callback 接入 RecordCallbackError |
| `monitor/collector.go` | 修改 | RecordAction 签名变更；ErrorEntry 增加 Code/CodeName/Messages；errKey + errorBucket；CodedError 接口；ResultCanceled；RecordCallbackSuccess/Error |
| `monitor/snapshot.go` | 修改 | MergeSnapshots 错误合并改用 code key；ActionSnapshot 新增 CanceledCount |
| `monitor/reporter.go` | 修改 | 控制台输出 `[Code CodeName]` 格式 |
| `adapter/adapter.go` | 修改 | 接口新增 DescribeError |
| `adapter/codec_resolver.go` | 修改 | LoadCodecResolver 加载 `errors.json` 并注入 SchemaAdapter |
| `adapter/schema_adapter.go` | 修改 | SchemaAdapter.DescribeError 委托 SchemaCodec |
| `codec/errors.go` | 新建 | LoadErrorMap 读取 `errors.json` |
| `conf/adapter/errors.json` | 可选新增 | 服务端错误码映射 |
| `admin/handlers.go` | 修改 | 适配器资源基线/下发包含声明式 codec 与 errors.json |
| `agent/task_runner.go` | 修改 | 下载适配器资源并传给 LoadCodecResolver |
| `cmd/agent/main.go` | 修改 | 单机模式加载 CodecResolver/SchemaAdapter |
| 前端 | 修改 | types + ActionsTab + resourcesStore + baselineApi + taskActions + ResourcesDrawer + errorCodeRegistry |

---

## 16. 与计划的差异

| 章节 | 计划 | 实际 |
|------|------|------|
| Kind 维度 | 原计划 `(Kind, Code)` 二元组（Kind 为 `uint8`/`string`） | **已删除 Kind**：改单维 `code` + 码段契约（< 100 框架 / ≥ 100 业务），码段以数值 `100` 为界，互不冲突 |
| Kind.String() | switch-case 方法 | Kind 已删除，无需 String()；框架/业务标签由 `code < 100` 推导 |
| ErrActionSkip → ErrFieldNil | 重命名 + 删除 4 处吞咽逻辑 | 未实施重命名。executor 中保留了 skip 相关逻辑 |
| ResultSkipped | ActionResult 包含此值 | 实际只有 4 个值：Success/Failure/Timeout/Canceled |
| skippedCount | ActionSnapshot 含此字段 | ActionSnapshot 中无 skippedCount 字段 |
| errors.json 未命中 | 未找到时返回 `""` | SchemaCodec.DescribeError 未命中返回空串，调用方仍保留原 code |
| RecordCallback 拆分 | 拆为 Success/Error 两版 | 已实施：RecordCallbackSuccess / RecordCallbackError |
| 合并后 Messages 上限 | 5 条 | 已实施 |
| 环形缓冲取模 | uint32 域取模 | 已实施，避免 32 位系统负索引 |
| ErrExecFailed | 计划中未列出 | 实际代码中存在，executor abort 时使用 |
| ErrHTTPStatus | 计划中未列出 | 实际代码中存在，HTTP 响应非 2xx 时使用 |
| ErrListenRegister | 计划中未列出 | 实际代码中存在 |
| codeRegistry 条目数 | 计划列出 24 个 | 实际 29 个（多出 ErrExecFailed、ErrHTTPStatus、ErrListenRegister、ErrActionCanceled、ErrHeartbeatConfig） |
| Lua 桥适配 | 详细说明 6 个函数改造 | 已迁移为 LoadCodecResolver + SchemaAdapter 生产路径 |
| 前端 actionsOnly/callbacksOnly 互斥开关 | 详细设计 | 待前端实施 |
| errors.json 默认模板 | Lua 函数模板 | 当前为扁平 JSON code→desc 映射 |
