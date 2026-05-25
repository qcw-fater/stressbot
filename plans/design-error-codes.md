# 动作错误码体系设计

> **v2 修订说明（2026-05-19）**
>
> 在 v1 基础上修正若干关键问题：
>
> 1. **消除 `engine` ↔ `monitor` 循环依赖**：新建 `errcode` 顶层包统一存放错误码常量。
> 2. **用显式 `Kind` 字段区分框架/服务端错误**：不再依赖"数值区间"约定（避免与游戏自身 1..N 编码冲突）。
> 3. **补 `script/api_network.go`（Lua 桥）改造**：NetSender 接口变更牵连 6 个 Lua API。
> 4. **补漏：HTTPRequest、execHTTPRequest 中遗漏的 `fmt.Errorf`**；`Connection.RequestResponse` 中 `c == nil` 时禁止访问 `c.serviceName`。
> 5. **`ctx.Err()` 不计入失败率**：任务取消视为正常退出（新增 `ResultCanceled`）。
> 6. **`DescribeError` 加内存缓存**：避免高频服务端错误码反复进 Lua 池。
> 7. **`ErrorEntry.Message` 去 `[code]` 前缀**：Code 已是独立字段，前端拼接显示。
> 8. **`errorBucket` 保留最近 N 条 Detail**：单字段覆盖会丢失上下文。
> 9. **callback 路径接入错误聚合**：当前 Lua 回调失败只 Warn，监控完全无感。
> 10. **新增 ErrUnknownPattern、ErrorCode.String()、NewActionError 带 cause 参数等**。
> 11. **`ErrJSONMarshal` 重命名为 `ErrMarshalBody`**：覆盖 JSON + form 两种序列化场景。
> 12. **`Error()` 格式从 `[code]` 变为 `[kind/code]`**：如有日志解析工具匹配 `[\\d+]` 格式需同步更新。
> 13. **`errcode` 单一数据源**：用 `codeRegistry []CodeInfo` 派生 `String()` 与 `AllCodes()`，避免双 switch 漂移导致的不一致 bug。
> 14. **`errorBucket.record` 改在 uint32 域内取模**：原 `int(Add(1)-1) % N` 在 32-bit 系统上 uint32→int 溢出会得到负索引并 panic。
> 15. **`sampleCount` 口径保持现状（succ+fail+tout）**：不含 skipped（历史口径）也不含 canceled（v2 新增），避免改造前后数据不可比。
> 16. **callback 前端展示**：保留 `actionsOnly` 开关 + 新增 `callbacksOnly` 开关（互斥），混排时 callback 行加 `推送` tag，不引入独立 Tab 或分组渲染。
> 17. **`RecordCallback` 直接删除**：不保留 alias，全库调用点仅 1 处，直接改名为 `RecordCallbackSuccess`。
>
> **注意**：文档中标注的行号均为编写时的代码快照，实施时以实际代码为准。

## 1. 背景与问题

当前压测动作层（engine/action → robot → network）的错误处理存在以下问题：

### 1.1 monitor error map 爆炸

所有错误通过 `fmt.Errorf` 生成自由格式字符串，作为 error map 的 key。每个不同参数组合产生独立条目：

```
"TCP 请求失败: service=logic route=1001 respKey=1002 elapsed=2.3s"  → 15
"TCP 请求失败: service=logic route=1001 respKey=1002 elapsed=3.1s"  → 8
"服务端错误码 1004: service=logic route=CreateTeam"                  → 12
"adapter.EncodeTCP 返回 nil，检查 codec.lua"                         → 1
```

相同类型的错误因参数差异无法聚合，条目数持续增长。

### 1.2 底层错误原因丢失

`network/connection.go` 的 `RequestResponse` 在所有失败路径统一返回 `(nil, 0)`：

| 失败原因 | 返回值 |
|----------|--------|
| 连接为 nil | `nil, 0` |
| 连接已关闭 | `nil, 0` |
| 发送失败 | `nil, 0` |
| ctx 取消（连接断开） | `nil, 0` |
| 等待超时 | `nil, 0` |

调用方无法区分失败原因。`robot/robot.go` 的 `netSenderAdapter.TCPRequest` 进一步丢失信息，对"连接不存在"和"请求超时"都返回 `(nil, 0, false)`。

### 1.3 超时未正确分类

`engine/errors.go` 中声明了哨兵值 `ErrTimeout`，`robot/robot.go` 的 `classifyResult` 也会检查它并归类为 `ResultTimeout`。但实际上没有任何代码路径产生包装了 `ErrTimeout` 的 error——所有超时场景都用 `fmt.Errorf` 生成字符串，最终被归为 `ResultFailure`。

### 1.4 业务错误码未结构化

游戏服务器通过协议适配器返回的 `headerErr`（`uint64`）是业务错误码。当前被拼进 `fmt.Errorf("服务端错误码 %d: ...")` 字符串，无法按错误码数值聚合。且框架错误码和业务错误码混在同一个 error map 中，无法区分。

---

## 2. 设计目标

1. **引入统一的 (Kind, Code) 二元组错误标识**：`Kind` 区分框架/服务端来源，`Code` 为 uint64 数值（框架由工具定义，服务端为 headerErr 原值）
2. **结构化 ActionError 类型**：替代自由格式字符串，携带 Code + Detail
3. **底层返回具体错误原因**：Connection 层、Adapter 层不再返回无差异的失败值
4. **monitor error map 按错误码聚合**：相同 Code 合并计数，不再爆炸式增长
5. **超时正确归类**：通过 `Unwrap()` 链让 `classifyResult` 识别超时
6. **行为不变**：`headerErr != 0` 仍算 ResultFailure，仍受 errorStrategy 控制，仍解析存储响应

---

## 3. 核心设计

### 3.1 统一错误码 — `errcode` 顶层包（新建）

**为什么不放在 `monitor`？**

`engine.ActionError.Code` 需要引用 `ErrorCode` 类型；`monitor.recordError` 又需要 `errors.As(err, &engine.ActionError{})` 取码。一旦把常量放在 `monitor`，会形成 `engine → monitor → engine` 的循环依赖。

**解决方案**：新建 `stressbot/errcode` 顶层包，只放常量和 `Kind` 枚举，无业务依赖，任何包（含 `network`/`engine`/`monitor`）都可引用。

```go
// errcode/codes.go
package errcode

// ErrorCode 统一错误码类型。
// 与 Kind 配合使用：(Kind, Code) 二元组才能唯一标识一类错误。
type ErrorCode uint64

// Kind 错误来源类别。
// 用显式枚举替代"数值区间"约定，避免与游戏自身 1..N 编码冲突。
type Kind uint8

const (
    KindFramework Kind = iota // 框架内部错误（连接/编码/Lua 等）
    KindServer                // 服务端 headerErr
)

const (
    // 网络层 (1-10)
    ErrConnNotFound ErrorCode = 1   // 连接未建立（GetTCPConn/GetUDPConn 返回 nil）
    ErrConnClosed   ErrorCode = 2   // 连接已关闭（isClose == 1）
    ErrSendFailed   ErrorCode = 3   // socket 写入失败（Send 返回 false）
    ErrRecvTimeout  ErrorCode = 4   // 等待响应超时（select timeout）
    ErrConnDropped  ErrorCode = 5   // 等待期间连接断开（ctx.Done）

    // 协议层 (11-20)
    ErrEncodeFailed ErrorCode = 11  // codec.lua 编码返回 nil
    ErrParseFailed  ErrorCode = 12  // S2C proto 解析失败

    // 构建层 (21-30)
    ErrCreateMsg    ErrorCode = 21  // 创建 C2S proto 消息失败
    ErrBindField    ErrorCode = 22  // 必需字段绑定失败（Required=true）
    ErrSerialize    ErrorCode = 23  // C2S 消息序列化失败

    // 监听层 (31-40)
    ErrListenTimeout ErrorCode = 31 // TCP/UDP Listen 轮询超时

    // 配置层 (41-50)
    ErrAddrEmpty       ErrorCode = 41 // 连接地址为空
    ErrURLEmpty        ErrorCode = 42 // HTTP URL 为空
    ErrURLScheme       ErrorCode = 43 // HTTP URL 协议错误（缺 http:// 前缀）
    ErrUnknownPattern  ErrorCode = 44 // 未知动作模式（配置错误）
    ErrHTTPBuild       ErrorCode = 45 // http.NewRequest 失败
    ErrHTTPReadBody    ErrorCode = 46 // 读取 HTTP 响应体失败
    ErrMarshalBody     ErrorCode = 47 // JSON/form 请求体序列化失败

    // Lua 层 (51-60)
    ErrLuaNotInit    ErrorCode = 51 // Lua 运行时未初始化
    ErrLuaNoScript   ErrorCode = 52 // lua 动作缺少 script 配置
    ErrLuaExecFailed ErrorCode = 53 // Lua 脚本执行异常
    ErrLuaExitCode   ErrorCode = 54 // Lua 脚本返回非零退出码

    // 回调层 (61-70)
    ErrCallbackLua   ErrorCode = 61 // Lua 回调脚本执行失败
    ErrCallbackParse ErrorCode = 62 // 推送消息解析失败
)

// CodeInfo 单条错误码元数据，HTTP 端点 `/sbot/api/error-codes` 返回此结构。
type CodeInfo struct {
    Code uint64 `json:"code"`
    Name string `json:"name"`
    Kind Kind   `json:"kind"`
}

// codeRegistry 是**唯一真理源**：新增/重命名错误码只动这里。
// String() 和 AllCodes() 都从它派生，避免两份独立 switch/数组漂移导致的不一致 bug。
var codeRegistry = []CodeInfo{
    {uint64(ErrConnNotFound),   "CONN_NOT_FOUND",   KindFramework},
    {uint64(ErrConnClosed),     "CONN_CLOSED",      KindFramework},
    {uint64(ErrSendFailed),     "SEND_FAILED",      KindFramework},
    {uint64(ErrRecvTimeout),    "RECV_TIMEOUT",     KindFramework},
    {uint64(ErrConnDropped),    "CONN_DROPPED",     KindFramework},
    {uint64(ErrEncodeFailed),   "ENCODE_FAILED",    KindFramework},
    {uint64(ErrParseFailed),    "PARSE_FAILED",     KindFramework},
    {uint64(ErrCreateMsg),      "CREATE_MSG",       KindFramework},
    {uint64(ErrBindField),      "BIND_FIELD",       KindFramework},
    {uint64(ErrSerialize),      "SERIALIZE",        KindFramework},
    {uint64(ErrListenTimeout),  "LISTEN_TIMEOUT",   KindFramework},
    {uint64(ErrAddrEmpty),      "ADDR_EMPTY",       KindFramework},
    {uint64(ErrURLEmpty),       "URL_EMPTY",        KindFramework},
    {uint64(ErrURLScheme),      "URL_SCHEME",       KindFramework},
    {uint64(ErrUnknownPattern), "UNKNOWN_PATTERN",  KindFramework},
    {uint64(ErrHTTPBuild),      "HTTP_BUILD",       KindFramework},
    {uint64(ErrHTTPReadBody),   "HTTP_READ_BODY",   KindFramework},
    {uint64(ErrMarshalBody),    "MARSHAL_BODY",     KindFramework},
    {uint64(ErrLuaNotInit),     "LUA_NOT_INIT",     KindFramework},
    {uint64(ErrLuaNoScript),    "LUA_NO_SCRIPT",    KindFramework},
    {uint64(ErrLuaExecFailed),  "LUA_EXEC_FAILED",  KindFramework},
    {uint64(ErrLuaExitCode),    "LUA_EXIT_CODE",    KindFramework},
    {uint64(ErrCallbackLua),    "CALLBACK_LUA",     KindFramework},
    {uint64(ErrCallbackParse),  "CALLBACK_PARSE",   KindFramework},
}

// 派生：包初始化时一次性建索引，String() O(1) 查询。
var codeNameIndex = func() map[uint64]string {
    m := make(map[uint64]string, len(codeRegistry))
    for _, c := range codeRegistry {
        m[c.Code] = c.Name
    }
    return m
}()

// String 自描述错误码（用于日志/CSV/前端 i18n 兜底）。
// 未注册的 code（含服务端 headerErr）返回空字符串。
func (c ErrorCode) String() string {
    if name, ok := codeNameIndex[uint64(c)]; ok {
        return name
    }
    return ""
}

// AllCodes 返回全部框架错误码定义，供 `GET /sbot/api/error-codes` 透传给前端。
// 返回切片副本，调用方修改不影响内部状态。
func AllCodes() []CodeInfo {
    out := make([]CodeInfo, len(codeRegistry))
    copy(out, codeRegistry)
    return out
}

func (k Kind) String() string {
    switch k {
    case KindFramework: return "framework"
    case KindServer:    return "server"
    default:            return "unknown"
    }
}
```

**单一数据源约定（强制）**：
- 新增/重命名/废弃错误码**只动 `codeRegistry`**，不要单独改 `String()` 或 `AllCodes()`。
- 单元测试用 `for _, c := range codeRegistry { require.Equal(t, c.Name, ErrorCode(c.Code).String()) }` 兜底，防止有人破坏约定。

**框架错误码分配规则**：
- 每个分类预留 10 个槽位（1-10, 11-20, ...），便于扩展
- **不再依赖"数值区间"区分框架/服务端错误**，改用 `Kind` 显式标识
- 前端通过 `GET /sbot/api/error-codes` 拉取全量 `(Code, String, Kind)` 表做 i18n

### 3.2 ActionError 类型 — `engine/errors.go`（新建）

当前 `ErrActionSkip` 和 `ErrTimeout` 定义在 `engine/action.go:22-26`。新建 `engine/errors.go` 将哨兵值和 `ActionError` 类型集中管理，`action.go` 不再包含错误定义。

```go
package engine

import (
    "errors"
    "fmt"

    "stressbot/errcode"
)

// 哨兵值（从 action.go 迁出）。
// ErrActionSkip 重命名为 ErrFieldNil，明确"字段为空"的语义，与 executor.errSkip 区分开（参见第 12 节）。
var (
    ErrFieldNil = errors.New("action skipped: required field is nil")
    ErrTimeout  = errors.New("action timeout")
)

// ActionError 携带错误码与来源类别的结构化错误，替代 fmt.Errorf。
// (Kind, Code) 二元组唯一标识一类错误：
//   - 框架错误：Kind=KindFramework, Code=errcode.Err*
//   - 服务端错误：Kind=KindServer,   Code=headerErr 原值
type ActionError struct {
    Kind   errcode.Kind      // 错误来源类别（必填）
    Code   errcode.ErrorCode // (Kind, Code) 二元组唯一标识
    Detail string            // 上下文：service / route / elapsed 等，**不含** [code] 前缀
    cause  error             // 可选：用于 errors.Is 链式判断（如 ErrTimeout）
}

// NewActionError 创建框架错误（最常用入口）。
// 可选 cause 参数用于包装下层 error（如 factory.Create 失败）。
func NewActionError(code errcode.ErrorCode, detail string, cause ...error) *ActionError {
    e := &ActionError{Kind: errcode.KindFramework, Code: code, Detail: detail}
    if len(cause) > 0 {
        e.cause = cause[0]
    }
    return e
}

// NewTimeoutError 创建带 ErrTimeout cause 的超时错误。
// classifyResult 通过 errors.Is(err, ErrTimeout) 识别并归类为 ResultTimeout。
func NewTimeoutError(code errcode.ErrorCode, detail string) *ActionError {
    return &ActionError{Kind: errcode.KindFramework, Code: code, Detail: detail, cause: ErrTimeout}
}

// NewServerError 包装服务端 headerErr 为 ActionError。
// Kind 显式标为 KindServer，便于 monitor/前端区分。
func NewServerError(serverCode uint64, detail string) *ActionError {
    return &ActionError{Kind: errcode.KindServer, Code: errcode.ErrorCode(serverCode), Detail: detail}
}

// Error 格式：`[framework/1] service=logic` 或 `[server/1004] desc: route=CreateTeam`。
// 包含 Kind 是为了日志一眼能分辨框架错误 vs 服务端错误，避免误把游戏 code=1 当成框架错误。
func (e *ActionError) Error() string {
    if e.Detail != "" {
        return fmt.Sprintf("[%s/%d] %s", e.Kind, e.Code, e.Detail)
    }
    return fmt.Sprintf("[%s/%d]", e.Kind, e.Code)
}

func (e *ActionError) Unwrap() error { return e.cause }

// IsServerError 判断是否为服务端错误码（基于 Kind，而非数值区间）。
func (e *ActionError) IsServerError() bool { return e.Kind == errcode.KindServer }
```

**关键设计点**：

1. `NewTimeoutError` 内嵌 `ErrTimeout` 作为 cause → `classifyResult` 中 `errors.Is(err, ErrTimeout)` **无需修改**即可正确归类为 `ResultTimeout`。
2. **`Kind` 字段替代"数值区间"约定**：游戏服务器可能用 `1..N` 编排业务码（如 `1=参数错误`），与框架 `ErrConnNotFound=1` 数值冲突；用 `Kind` 显式标记后，monitor 按 `(Kind, Code)` 聚合，互不干扰。
3. `NewActionError` 支持可选 `cause`，可包装下层错误（如 `factory.Create()` 失败时透传原始 error 信息），同时保留结构化 Code。
4. `Detail` **不含** `[code]` 前缀（避免冗余存储），格式化由 `Error()` 统一处理。

### 3.3 ErrorEntry 统一结构 — `monitor/collector.go`（修改）

```go
import "stressbot/errcode"

// ErrorEntry 错误分布条目（统一用于框架错误和服务端错误码）。
type ErrorEntry struct {
    Kind     errcode.Kind `json:"kind"`     // "framework" / "server"，前端按此上色
    Code     uint64       `json:"code"`     // 框架 errcode.Err* 或服务端 headerErr 原值
    CodeName string       `json:"codeName"` // ErrorCode.String()；服务端错误为空字符串（由 error.lua 补描述）
    Messages []string     `json:"msgs"`     // 最近 N 条 Detail（不含 [code] 前缀），最多保留 3 条
    Count    int64        `json:"count"`    // 累计出现次数
}
```

**改前** vs **改后**：

| | 改前 | 改后 |
|--|------|------|
| Code 类型 | 无（只有 Message 字符串） | `uint64`，固定数值 |
| 区分框架/服务端 | 无 | `Kind` 字段显式标识 |
| Message | 单条 `"TCP 请求失败: service=logic elapsed=2.3s"` | 多条数组 `["service=logic elapsed=2.3s", "service=match elapsed=5.0s"]` |
| 聚合 key | 完整 Message 字符串 → 爆炸 | `(Kind, Code)` 二元组 → 固定类别 |
| CodeName | 无 | `"RECV_TIMEOUT"` 等自描述（前端 i18n 兜底） |

### 3.4 改后 error map 示例

```
action "createTeam" (route=CreateTeam):
  errors map ((Kind, Code) → *errorBucket):
    (framework, 4) → {
      count: 26,
      codeName: "RECV_TIMEOUT",
      msgs: [
        "service=logic respKey=1002 elapsed=2.3s",
        "service=logic respKey=1002 elapsed=3.1s",
        "service=match respKey=2002 elapsed=5.0s",
      ],
    }
    (framework, 1) → {
      count: 2,
      codeName: "CONN_NOT_FOUND",
      msgs: ["service=logic"],
    }
    (server, 1004) → {
      count: 15,
      codeName: "",  // 由 error.lua 的 describe_error 补描述
      msgs: ["service=logic route=CreateTeam"],
    }
    (server, 1005) → {
      count: 3,
      codeName: "",
      msgs: ["service=logic route=CreateTeam"],
    }
```

注意：游戏服务器若使用 `1..N` 编排业务码（如 `1=参数错误`），**与框架 `ErrConnNotFound=1` 数值相同但 Kind 不同**，所以会落到 `(server, 1)` 和 `(framework, 1)` 两个不同的 bucket，互不污染。

---

## 4. 各层改造详情

### 4.1 连接层 — `network/connection.go`

#### 签名变更

```go
// 改前：
func (c *Connection) RequestResponse(sendData []byte, responseKey string, timeoutOverride ...time.Duration) (*Message, int)
//   第二个返回值 int 是发送字节数，但所有调用方都丢弃了这个值
//   带宽统计已在 Send() 内部通过 monitor.Global().AddBandwidth() 完成

// 改后：
func (c *Connection) RequestResponse(sendData []byte, responseKey string, timeoutOverride ...time.Duration) (*Message, error)
```

#### 各失败路径改造

| 行号 | 条件 | 改前返回 | 改后返回 |
|------|------|---------|---------|
| 115 | `c == nil` | `nil, 0` | `nil, NewActionError(errcode.ErrConnNotFound, "nil connection responseKey="+responseKey)` ← **不能访问 `c.serviceName`，c 已是 nil** |
| 118 | `isClose == 1` | `nil, 0` | `nil, NewActionError(errcode.ErrConnClosed, c.serviceName+" responseKey="+responseKey)` |
| 136 | `Send()` 返回 `err != nil` | `nil, 0` | `nil, NewActionError(errcode.ErrSendFailed, c.serviceName+" responseKey="+responseKey, sendErr)` ← 透传 sendErr 作 cause |
| 149 | `ctx.Done()` | `nil, 0` | `nil, NewActionError(errcode.ErrConnDropped, c.serviceName+" responseKey="+responseKey)` |
| 162 | 超时 | `nil, 0` | `nil, NewTimeoutError(errcode.ErrRecvTimeout, c.serviceName+" responseKey="+responseKey+" timeout="+timeout.String())` |
| 157 | 成功 | `resp, n` | `resp, nil` |

**说明**：
- 行 115 的 `ErrorCode` 用 `ErrConnNotFound`（不是 `ErrConnClosed`）—— 调用方传了 nil，语义是"连接未建立/已被销毁"，与"已 Close" 不同。
- 行 136 把底层 `sendErr` 通过 `cause` 参数传入，方便 debug 时 `errors.Unwrap()` 取原始 err。

### 4.2 NetSender 接口 — `engine/action.go`

完整接口变更：

```go
// 改前：
type NetSender interface {
    TCPSend(service string, packet []byte) (bool, int)
    TCPRequest(service string, packet []byte, responseKey string, timeout ...time.Duration) (body []byte, headerErr uint64, ok bool)
    HTTPRequest(url, method, contentType string, body []byte) (statusCode int, respBody []byte, err error)
    UDPSend(service string, data []byte) (bool, int)
    UDPRequest(service string, packet []byte, responseKey string, timeout ...time.Duration) (body []byte, headerErr uint64, ok bool)
    ConnectTCP(service, address string) bool
    ConnectUDP(service, address string) bool
    CloseTCP(service string)
    CloseUDP(service string)
    GetTCPListenResp(service string, responseKey string) ([]byte, uint64)
    GetUDPListenResp(service string, responseKey string) ([]byte, uint64)
    GetTCPSecretKey(service string) []byte
    SetTCPSecretKey(service string, key []byte)
    SetUDPSecretKey(service string, key []byte)
    GetUDPSecretKey(service string) []byte
    EnsureTCPListener(service string, responseKey string)
    EnsureUDPListener(service string, responseKey string)
    RegisterTCPHeartbeat(service string, intervalMs int, builder func() []byte)
    RegisterUDPHeartbeat(service string, intervalMs int, builder func() []byte)
}

// 改后：7 个方法签名变更，12 个不变
type NetSender interface {
    // ── 签名变更（bool/int → error）──
    TCPSend(service string, packet []byte) (int, error)                                    // (bool,int) → (int,error)
    UDPSend(service string, data []byte) (int, error)                                      // (bool,int) → (int,error)
    TCPRequest(service string, packet []byte, responseKey string, timeout ...time.Duration) (body []byte, headerErr uint64, err error)    // bool → error
    UDPRequest(service string, packet []byte, responseKey string, timeout ...time.Duration) (body []byte, headerErr uint64, err error)    // bool → error
    ConnectTCP(service, address string) error                                              // bool → error
    ConnectUDP(service, address string) error                                              // bool → error
    HTTPRequest(url, method, contentType string, body []byte) (statusCode int, respBody []byte, err error)                 // 签名不变，内部改用 ActionError

    // ── 签名不变 ──
    CloseTCP(service string)
    CloseUDP(service string)
    GetTCPListenResp(service string, responseKey string) ([]byte, uint64)     // nil 是正常轮询结果，不是错误
    GetUDPListenResp(service string, responseKey string) ([]byte, uint64)     // 同上
    GetTCPSecretKey(service string) []byte
    SetTCPSecretKey(service string, key []byte)
    SetUDPSecretKey(service string, key []byte)
    GetUDPSecretKey(service string) []byte
    EnsureTCPListener(service string, responseKey string)
    EnsureUDPListener(service string, responseKey string)
    RegisterTCPHeartbeat(service string, intervalMs int, builder func() []byte)
    RegisterUDPHeartbeat(service string, intervalMs int, builder func() []byte)
}
```

**不改变签名的方法说明**：
- `CloseTCP`/`CloseUDP`：fire-and-forget，无需错误码
- `GetTCPListenResp`/`GetUDPListenResp`：轮询接口，返回 nil 表示暂无数据（正常行为）
- `GetTCPSecretKey`/`SetTCPSecretKey` 等：状态访问器，无错误语义
- `EnsureTCPListener`/`EnsureUDPListener`：幂等注册，无错误语义
- `RegisterTCPHeartbeat`/`RegisterUDPHeartbeat`：注册回调，无错误语义

**HTTPRequest 签名不变**：已有 `(int, []byte, error)`，只需将内部 `fmt.Errorf` 改为 `NewActionError`。

### 4.2.1 前置依赖：Connection.Send

`TCPSend`/`UDPSend` 的底层是 `Connection.Send`，需同步改造：

```go
// 改前：
func (c *Connection) Send(data []byte) (bool, int)

// 改后：
func (c *Connection) Send(data []byte) (int, error)
```

**Send 调用方（2 处）**：
1. `Connection.RequestResponse:135` — 已在改，适配新签名
2. `heartbeat.go:92` — 心跳发送，忽略 error（只记录日志即可）

```go
// heartbeat.go 适配：
// 改前：
ok, n := c.Send(packet)
if !ok { stresslog.Warn(...) } else { stresslog.Debug(...) }

// 改后：
n, err := c.Send(packet)
if err != nil { stresslog.Warn(...) } else { stresslog.Debug(...) }
```

### 4.3 netSenderAdapter — `robot/robot.go`

#### TCPSend（~行 527）

```go
// 改前：
func (ns *netSenderAdapter) TCPSend(service string, packet []byte) (bool, int) {
    conn := ns.robot.client.GetTCPConn(service)
    if conn == nil { return false, 0 }
    return conn.Send(packet)
}

// 改后：
func (ns *netSenderAdapter) TCPSend(service string, packet []byte) (int, error) {
    conn := ns.robot.client.GetTCPConn(service)
    if conn == nil {
        return 0, NewActionError(errcode.ErrConnNotFound, "service="+service)
    }
    return conn.Send(packet)  // Connection.Send 改为返回 (int, error)
}
```

#### UDPSend（~行 631）同理。

#### TCPRequest（~行 536）

```go
// 改前：
func (ns *netSenderAdapter) TCPRequest(...) ([]byte, uint64, bool) {
    conn := ns.robot.client.GetTCPConn(service)
    if conn == nil {
        stresslog.Warn("[ACTION] TCPRequest 连接不存在", ...)
        return nil, 0, false
    }
    resp, _ := conn.RequestResponse(...)
    if resp == nil { return nil, 0, false }
    return resp.Data, resp.HeaderErr, true
}

// 改后：
func (ns *netSenderAdapter) TCPRequest(...) ([]byte, uint64, error) {
    conn := ns.robot.client.GetTCPConn(service)
    if conn == nil {
        return nil, 0, NewActionError(errcode.ErrConnNotFound, "service="+service)
    }
    resp, err := conn.RequestResponse(...)  // 透传 Connection 层的 ActionError
    if err != nil {
        return nil, 0, err
    }
    return resp.Data, resp.HeaderErr, nil
}
```

#### UDPRequest（~行 554）同理。

#### ConnectTCP（~行 640）

```go
// 改前：
func (ns *netSenderAdapter) ConnectTCP(service, address string) bool {
    return ns.robot.ConnectTCP(service, address)
}

// 改后：
func (ns *netSenderAdapter) ConnectTCP(service, address string) error {
    ok := ns.robot.ConnectTCP(service, address)
    if !ok {
        return NewActionError(errcode.ErrConnClosed, "service="+service+" address="+address)
    }
    return nil
}
```

#### ConnectUDP（~行 645）同理。

#### HTTPRequest（~行 572）— **完整版（v1 漏 5 处）**

签名不变（已是 `(int, []byte, error)`），所有 `fmt.Errorf` → `NewActionError`：

| 行号 | 改前 | 改后 |
|------|------|------|
| 574 | `fmt.Errorf("HTTP 请求 URL 为空")` | `NewActionError(errcode.ErrURLEmpty, "")` |
| 577 | `fmt.Errorf("HTTP 请求 URL 必须以 http:// 或 https:// 开头: %s", reqURL)` | `NewActionError(errcode.ErrURLScheme, "url="+reqURL)` |
| 588/598/604/611 | `fmt.Errorf("创建 HTTP 请求失败: %w", err)` （4 处分支） | `NewActionError(errcode.ErrHTTPBuild, "url="+reqURL, err)` |
| 618 | `fmt.Errorf("HTTP 请求失败: %w", err)` | `NewActionError(errcode.ErrSendFailed, "url="+reqURL, err)` |
| 624 | `fmt.Errorf("读取响应体失败: %w", err)` | `NewActionError(errcode.ErrHTTPReadBody, "url="+reqURL, err)` |

**说明**：
- 原始 `err` 通过 `cause` 参数透传（`NewActionError` v2 增加的可选参数）。
- 不再用 `Detail` 拼接 `err.Error()`，避免不同 URL 的失败被同一 Code 的不同 Detail 字符串再次"分裂"——Detail 保持 `"url=..."` 这种**有限基数**，原始 err 信息通过 `errors.Unwrap()` 取出（log 中打印，但不进 monitor 聚合 key）。

### 4.4 Action 层 — `engine/action.go`

所有 `exec*` 方法中的 `fmt.Errorf` 替换为结构化 ActionError。

#### execTCPSend 改造示例

```go
// 改前：
ok, n := ae.netSender.TCPSend(def.Service, packet)
if !ok {
    if def.Optional { return 0, nil }
    return 0, fmt.Errorf("TCP 发送失败: service=%s route=%s", def.Service, routeKey)
}

// 改后：
n, err := ae.netSender.TCPSend(def.Service, packet)
if err != nil {
    if def.Optional { return 0, nil }
    return 0, err  // 透传 ActionError（已有具体 ErrorCode）
}
```

#### execTCPConnect 改造示例

```go
// 改前：
ok := ae.netSender.ConnectTCP(def.Service, addr)
if !ok {
    return fmt.Errorf("TCP 连接建立失败: service=%s address=%s", def.Service, addr)
}

// 改后：
err := ae.netSender.ConnectTCP(def.Service, addr)
if err != nil {
    return err  // 透传 ActionError
}
```

#### execTCPRequest 改造示例

```go
// 改前：
respBody, headerErr, ok := ae.netSender.TCPRequest(def.Service, packet, respKey, reqTimeout...)
elapsed := time.Since(start)
if !ok {
    if def.Optional {
        stresslog.Debug("[ACTION] 可选 TCP 请求失败（已忽略）", ...)
        return 0, 0, nil
    }
    return len(packet), 0, fmt.Errorf("TCP 请求失败: service=%s route=%s respKey=%s elapsed=%v",
        def.Service, routeKey, respKey, elapsed)
}
if headerErr != 0 {
    if err := ae.parseAndStoreResponse(def, respBody); err != nil {
        return len(packet), 0, err
    }
    return len(packet), len(respBody), fmt.Errorf("服务端错误码 %d: service=%s route=%s",
        headerErr, def.Service, routeKey)
}

// 改后：
respBody, headerErr, err := ae.netSender.TCPRequest(def.Service, packet, respKey, reqTimeout...)
elapsed := time.Since(start)
if err != nil {
    if def.Optional {
        stresslog.Debug("[ACTION] 可选 TCP 请求失败（已忽略）", ...)
        return 0, 0, nil
    }
    return len(packet), 0, err  // 直接透传 ActionError（已有 ErrorCode）
}
if headerErr != 0 {
    if storeErr := ae.parseAndStoreResponse(def, respBody); storeErr != nil {
        return len(packet), 0, storeErr
    }
    return len(packet), len(respBody), NewServerError(headerErr,
        "service="+def.Service+" route="+routeKey)
}
```

#### 完整替换表

| 方法 | 原错误字符串 | 替换为 |
|------|------------|--------|
| **execTCPSend** | | |
| `buildBody` 失败 | `fmt.Errorf("创建 C2S 消息 ...")` | `NewActionError(ErrCreateMsg, proto)` |
| `buildBody` 字段绑定 | `fmt.Errorf("绑定 C2S 字段失败")` | `NewActionError(ErrBindField, field)` |
| `buildBody` 序列化 | `fmt.Errorf("序列化失败")` | `NewActionError(ErrSerialize, proto)` |
| encode 返回 nil | `fmt.Errorf("adapter.EncodeTCP 返回 nil")` | `NewActionError(ErrEncodeFailed, "route="+routeKey)` |
| 发送失败 | `fmt.Errorf("TCP 发送失败: ...")` | 透传底层 ActionError |
| **execTCPRequest** | | |
| encode 返回 nil | `fmt.Errorf("adapter.EncodeTCP 返回 nil")` | `NewActionError(ErrEncodeFailed, ...)` |
| !ok | `fmt.Errorf("TCP 请求失败: ...")` | 透传底层 ActionError |
| headerErr != 0 | `fmt.Errorf("服务端错误码 %d: ...")` | `NewServerError(headerErr, "service=... route=...")` |
| **execTCPConnect** | | |
| 地址为空 | `fmt.Errorf("TCP 连接地址为空")` | `NewActionError(ErrAddrEmpty, "service="+service)` |
| 连接失败 | `fmt.Errorf("TCP 连接建立失败")` | 透传 ConnectTCP 返回的 ActionError |
| **execUDPConnect** | | |
| 地址为空 | `fmt.Errorf("UDP 连接地址为空")` | `NewActionError(ErrAddrEmpty, "service="+service)` |
| 连接失败 | `fmt.Errorf("UDP 连接建立失败")` | 透传 ConnectUDP 返回的 ActionError |
| **execTCPListen** | | |
| headerErr != 0 | `fmt.Errorf("服务端错误码 %d: ...")` | `NewServerError(headerErr, ...)` |
| 轮询超时 | `fmt.Errorf("TCPListen 超时: ...")` | `NewTimeoutError(ErrListenTimeout, "service=... timeout=...")` |
| **execUDPSend** | | |
| encode 返回 nil | `fmt.Errorf("adapter.EncodeUDP 返回 nil")` | `NewActionError(ErrEncodeFailed, ...)` |
| 发送失败 | `fmt.Errorf("UDP 发送失败")` | 透传底层 ActionError |
| **execUDPRequest** | | |
| encode 返回 nil | `fmt.Errorf("adapter.EncodeUDP 返回 nil")` | `NewActionError(ErrEncodeFailed, ...)` |
| !ok | `fmt.Errorf("UDPRequest 失败: ...")` | 透传底层 ActionError |
| headerErr != 0 | `fmt.Errorf("服务端错误码 %d: ...")` | `NewServerError(headerErr, ...)` |
| **execUDPListen** | | |
| headerErr != 0 | `fmt.Errorf("服务端错误码 %d: ...")` | `NewServerError(headerErr, ...)` |
| 轮询超时 | `fmt.Errorf("UDPListen 超时: ...")` | `NewTimeoutError(ErrListenTimeout, ...)` |
| **execHTTPRequest** | | |
| URL 为空 | `fmt.Errorf("HTTP 请求 URL 为空")` | `NewActionError(errcode.ErrURLEmpty, "action="+def.Name)` |
| JSON 序列化失败 | `fmt.Errorf("JSON 序列化失败: %w", err)` | `NewActionError(errcode.ErrMarshalBody, "action="+def.Name+" type=json", err)` |
| form 序列化失败 | `fmt.Errorf("form 数据序列化失败: %w", err)` | `NewActionError(errcode.ErrMarshalBody, "action="+def.Name+" type=form", err)` |
| 请求失败 | `fmt.Errorf("HTTP 请求失败: ...")` | 透传 HTTPRequest 返回的 ActionError（不再二次包装） |
| **parseAndStoreResponse** | | |
| 解析失败 | `fmt.Errorf("解析 S2C 响应 ...")` | `NewActionError(errcode.ErrParseFailed, "proto="+def.S2CProto, err)` |
| **Execute** | | |
| 未知 pattern | `fmt.Errorf("未知的动作模式: %s", def.Pattern)` | `NewActionError(errcode.ErrUnknownPattern, "pattern="+def.Pattern)` |

#### 4.4.1 `ctx.Err()` 的归类 — **v1 未讨论**

`execTCPListen`/`execUDPListen` 在 ctx 取消时直接 `return ctx.Err()`（`engine/action.go:707/915`），返回值是 `context.Canceled` 或 `context.DeadlineExceeded`。

**问题**：当前 `classifyResult` 会把它归到 `ResultFailure`，导致**任务被用户主动停止时所有进行中的 Listen 都计为失败**，污染成功率/Apdex。

**改造**：

1. `monitor/collector.go` 新增 `ResultCanceled` 类型：

   ```go
   const (
       ResultSuccess  ActionResult = iota
       ResultFailure
       ResultTimeout
       ResultSkipped
       ResultCanceled                  // 新增：ctx 取消（任务停止/连接断开）
   )
   ```

2. `actionMetrics` 新增 `canceledCount atomic.Int64` 字段；`Apdex/SuccessRate` 计算分母**不包含** `canceledCount`（取消不算样本）。

3. `robot/robot.go` `classifyResult` 加判断：

   ```go
   func classifyResult(err error) monitor.ActionResult {
       if err == nil { return monitor.ResultSuccess }
       if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
           return monitor.ResultCanceled
       }
       if errors.Is(err, engine.ErrFieldNil)  { return monitor.ResultSkipped }
       if errors.Is(err, engine.ErrTimeout)   { return monitor.ResultTimeout }
       return monitor.ResultFailure
   }
   ```

4. `ExecuteAction` 看到 `ResultCanceled` 时**不上 monitor 错误 map**（避免出现 `context canceled` 字符串），只递增 canceledCount。

5. `executor.go:executeAction` 看到包含 `context.Canceled` 的 err 时，直接 `return err` 让 executor 正常退出，**不进入 errorStrategy 分支**（即使 `errorStrategy="abort"` 也不算"业务级中断"）：

   ```go
   if err != nil {
       if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
           return err
       }
       // 原有 errorStrategy 处理 ...
   }
   ```

前端 ActionsTab 增加"取消"列（与"跳过"并列，颜色弱化为灰色）。

### 4.5 Lua 层 — `robot/robot.go` executeLuaAction

| 原错误 | 替换为 |
|--------|--------|
| `fmt.Errorf("lua 运行时未初始化")` | `NewActionError(errcode.ErrLuaNotInit, "")` |
| `fmt.Errorf("lua 动作缺少 script 配置")` | `NewActionError(errcode.ErrLuaNoScript, "")` |
| `fmt.Errorf("执行 lua 脚本 %s 失败: %w", script, err)` | `NewActionError(errcode.ErrLuaExecFailed, "script="+script, err)` ← cause 透传 |
| `fmt.Errorf("lua 脚本 %s 返回错误码: %d", script, code)` | `NewActionError(errcode.ErrLuaExitCode, fmt.Sprintf("script=%s code=%d", script, code))` |

### 4.6 Lua 桥（`script/api_network.go`）适配 — **设计 v1 漏项**

**问题**：NetSender 接口签名一改（第 4.2 节），`script/api_network.go` 中通过 `ctx.NetSender.*` 调用的 6 个 Lua API 全部编译失败：

| Lua API | Go 函数 | 影响调用 |
|---------|---------|---------|
| `network.connect_tcp` | `networkConnectTCP` | `ctx.NetSender.ConnectTCP` 返回 `bool` → `error` |
| `network.connect_udp` | `networkConnectUDP` | `ctx.NetSender.ConnectUDP` 返回 `bool` → `error` |
| `network.tcp_request` | `networkTCPRequest` | `ctx.NetSender.TCPRequest` 返回 `(body, headerErr, bool)` → `(body, headerErr, error)` |
| `network.udp_request` | `networkUDPRequest` | `ctx.NetSender.UDPRequest` 同上 |
| `network.tcp_send` | `networkTCPSend` | `ctx.NetSender.TCPSend` 返回 `(bool, int)` → `(int, error)` |
| `network.udp_send` | `networkUDPSend` | `ctx.NetSender.UDPSend` 同上 |

**Lua API 行为不变原则**：

Lua 脚本（用户写的业务代码）**完全不感知**新 `ErrorCode` 体系。Lua API 仍按当前约定返回 `code, data, sent, recv` 四元组：

```lua
-- Lua 侧调用约定（保持不变）
local code, body, sent, recv = network.tcp_request("logic", route, msg, "ResMsg")
-- code == 0       → 成功
-- code == -1      → 框架请求失败（连接断开/超时/编码失败等）
-- code == -2      → 响应解析失败
-- code >  0       → 服务端错误码（headerErr 原值）
```

**Go 侧改造**：调用 `ctx.NetSender.*` 时，把新接口返回的 `error` 折算回 Lua 期望的 `-1`。

**Lua 路径暂不接入 monitor**（沿用当前行为，错误统计由 Lua 脚本自己决定）。netSenderAdapter 返回的 ActionError 在 Lua 桥这层被"消费"掉转成 -1 给 Lua，原始 err 仅用于 `stresslog.Debug` 排查，不进 monitor 聚合（避免 Lua 业务错误污染框架错误指标）。

改造点仅为：

```go
// networkConnectTCP 改造示例
func networkConnectTCP(L *lua.LState) int {
    // ...
    err := ctx.NetSender.ConnectTCP(service, address)
    L.Push(lua.LBool(err == nil))   // 行为不变，Lua 侧仍 boolean
    return 1
}

// networkTCPRequest 改造示例
var respBody []byte
var respErr error
var headerErr uint64
withReleasedMu(ctx.LuaMu, func() {
    respBody, headerErr, respErr = ctx.NetSender.TCPRequest(service, packet, respKey)
})
if respErr != nil {
    L.Push(lua.LNumber(-1))
    L.Push(lua.LNil)
    L.Push(lua.LNumber(pktLen))
    L.Push(lua.LNumber(0))
    return 4
}
// 后续 headerErr / 解析逻辑保持不变
```

**改造文件**：`script/api_network.go` 共 6 个函数 + 已存在的辅助逻辑，约 50 行修改。

**是否给 Lua 暴露错误码？** 暂不暴露。Lua 现有 `-1/-2/headerErr` 约定已被用户脚本广泛使用，破坏兼容会引发大量返工。后续如果需要细化（如 Lua 脚本想区分超时 vs 连接断开），可考虑加 `network.last_error_code()` 全局函数读最近一次错误码。

---

## 5. Monitor 层改造

### 5.1 数据结构变更 — `monitor/collector.go`

#### errorBucket 替换原有 `*atomic.Int64`

```go
// 改前：errors sync.Map 存储 string → *atomic.Int64
// 改后：errors sync.Map 存储 errKey → *errorBucket
//
// errKey 是 (Kind, Code) 二元组的哈希友好表示：
//   - 框架错误：errKey = (KindFramework, ErrConnNotFound)
//   - 服务端错误：errKey = (KindServer,    1004)
// 这样即使游戏 code=1 与框架 code=1 数值相同，也会落到不同 bucket。

type errKey struct {
    Kind errcode.Kind
    Code uint64
}

// errorBucket 单类错误的统计槽。
// 保留最近 N 条 Detail 而非单字段覆盖，避免 service/route 不同的实例被相互覆盖。
type errorBucket struct {
    count    atomic.Int64
    msgRing  [3]atomic.Value // 环形缓冲，存最近 3 条 Detail（非 Error()，不含 [code] 前缀）
    ringIdx  atomic.Uint32   // 写入位置（取模 3）
}

func (b *errorBucket) record(detail string) {
    b.count.Add(1)
    // 先在 uint32 域内取模，确保结果 ∈ [0, len(msgRing))，转 int 永远非负。
    // 不能写 int(Add(1)-1) % len(...)：在 32-bit 系统上 uint32→int 可能溢出为负数，
    // 负数 % 3 在 Go 是截断除法，会得到负索引（如 -1），后续访问数组直接 panic。
    idx := int((b.ringIdx.Add(1) - 1) % uint32(len(b.msgRing)))
    b.msgRing[idx].Store(detail)
}

func (b *errorBucket) snapshot() (count int64, msgs []string) {
    count = b.count.Load()
    seen := make(map[string]bool)
    for i := range b.msgRing {
        if v := b.msgRing[i].Load(); v != nil {
            s := v.(string)
            if s != "" && !seen[s] {
                msgs = append(msgs, s)
                seen[s] = true
            }
        }
    }
    return
}
```

#### actionMetrics 中的 errors map

```go
type actionMetrics struct {
    // ... 其他字段不变 ...
    canceledCount atomic.Int64  // 新增：ctx.Err() 归类（参见 4.4.1）
    errors        sync.Map      // errKey → *errorBucket
}
```

### 5.2 RecordAction 签名变更

```go
// 改前：
func (c *MetricsCollector) RecordAction(name string, result ActionResult,
    duration time.Duration, sendBytes, recvBytes int, errMsg string)

// 改后：
func (c *MetricsCollector) RecordAction(name string, result ActionResult,
    duration time.Duration, sendBytes, recvBytes int, err error)
```

调用方 `robot/robot.go` 对应改动：

```go
// 改前：
errMsg := ""
if err != nil { errMsg = err.Error() }
mc.RecordAction(actionDef.Name, result, time.Since(start), sendBytes, recvBytes, errMsg)

// 改后：
mc.RecordAction(actionDef.Name, result, time.Since(start), sendBytes, recvBytes, err)
```

### 5.3 ResultFailure / ResultCanceled 分支

```go
case ResultFailure:
    am.failureCount.Add(1)
    if err != nil {
        c.recordError(am, err)  // 框架错误和服务端错误码统一走同一入口
    }
case ResultCanceled:
    am.canceledCount.Add(1)     // 新增：不进 error map，不污染 Apdex/SuccessRate
```

### 5.4 recordError 实现（用接口避免循环依赖）

**关键点**：`monitor` 不能直接 `import "stressbot/engine"`，否则形成 `engine → monitor → engine` 循环依赖。改用接口隔离：

```go
// monitor/collector.go

// CodedError 任意能提供 (Kind, Code, Detail) 三元组的错误。
// 由 engine.ActionError 实现，monitor 不依赖具体类型。
type CodedError interface {
    error
    ErrorKind() errcode.Kind
    ErrorCode() uint64
    ErrorDetail() string
}

func (c *MetricsCollector) recordError(am *actionMetrics, err error) {
    var ce CodedError
    if !errors.As(err, &ce) {
        return  // 无法提取 code 的 error，忽略（也不会聚合非结构化字符串）
    }
    key := errKey{Kind: ce.ErrorKind(), Code: ce.ErrorCode()}
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

`engine/errors.go` 中 `ActionError` 实现该接口（只是把已有字段暴露成方法）：

```go
func (e *ActionError) ErrorKind() errcode.Kind { return e.Kind }
func (e *ActionError) ErrorCode() uint64       { return uint64(e.Code) }
func (e *ActionError) ErrorDetail() string     { return e.Detail }
```

### 5.5 CollectErrors 适配

```go
func (am *actionMetrics) CollectErrors() []ErrorEntry {
    var entries []ErrorEntry
    am.errors.Range(func(key, value any) bool {
        k := key.(errKey)
        count, msgs := value.(*errorBucket).snapshot()
        entries = append(entries, ErrorEntry{
            Kind:     k.Kind,
            Code:     k.Code,
            CodeName: errcode.ErrorCode(k.Code).String(),  // 服务端错误返回 "" 由 error.lua 补
            Messages: msgs,
            Count:    count,
        })
        return true
    })
    return entries
}
```

### 5.6 classifyResult 改动

```go
// robot/robot.go

func classifyResult(err error) monitor.ActionResult {
    if err == nil { return monitor.ResultSuccess }
    // 任务取消优先级最高：避免被 ErrFieldNil/ErrTimeout 误吃
    if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
        return monitor.ResultCanceled
    }
    if errors.Is(err, engine.ErrFieldNil) { return monitor.ResultSkipped }
    if errors.Is(err, engine.ErrTimeout)  { return monitor.ResultTimeout }
    return monitor.ResultFailure
}
```

`NewTimeoutError` 内嵌 `ErrTimeout` 作为 cause，`errors.Is(err, ErrTimeout)` 返回 true → 正确归类为 `ResultTimeout`。

### 5.6.1 ActionSnapshot 变更 — `monitor/snapshot.go`

```go
type ActionSnapshot struct {
    Name          string        `json:"name"`
    SampleCount   int64         `json:"sampleCount"`
    SuccessCount  int64         `json:"successCount"`
    FailureCount  int64         `json:"failureCount"`
    TimeoutCount  int64         `json:"timeoutCount"`
    SkippedCount  int64         `json:"skippedCount"`
    CanceledCount int64         `json:"canceledCount"`   // 新增
    // ... 延迟直方图、Apdex 等字段不变 ...
    Errors        []ErrorEntry  `json:"errors,omitempty"`
}
```

**Apdex / SuccessRate 计算分母（沿用现有口径）**：

```
sampleCount = successCount + failureCount + timeoutCount
            （不含 skippedCount —— 历史口径，与现 monitor/snapshot.go:150 一致）
            （不含 canceledCount —— v2 新增，取消不算样本）
```

`ComputeApdex` 和 `ComputeSuccessRate` 使用 `sampleCount` 作分母，`skippedCount` / `canceledCount` 均不参与计算。

> **口径保持原因**：当前 `monitor/snapshot.go:150` 实现就是 `total := succ + fail + tout`，
> `skip` 早已不参与 Apdex/SuccessRate。v2 若把 `skipped` 纳入分母，会让同一压测在改造前后跑出的 SuccessRate 数值突变，
> 历史归档数据无法与新数据比较。如确需变更口径，应作为独立 RFC 立项，不混在本次改造内。

### 5.7 MergeSnapshots 适配 — `monitor/snapshot.go`

Error 合并改用 `(Kind, Code)` 二元组做 key；Messages 取并集去重，超过上限截断：

```go
// 合并两个 snapshot 的 errors
type mergedKey struct{ Kind errcode.Kind; Code uint64 }
errorMap := make(map[mergedKey]*ErrorEntry)

merge := func(es []ErrorEntry) {
    for _, e := range es {
        k := mergedKey{Kind: e.Kind, Code: e.Code}
        if existing, ok := errorMap[k]; ok {
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
            cp := e // 浅拷贝
            errorMap[k] = &cp
        }
    }
}
merge(snap1.Errors)
merge(snap2.Errors)
```

### 5.8 Console Reporter 适配 — `monitor/reporter.go`

打印格式：`[Kind/Code CodeName] action ×Count : msg1`，超过 1 条 Messages 只打第 1 条 + `(...)` 提示：

```
[MONITOR] errors: createTeam→[framework/4 RECV_TIMEOUT]×26 service=logic elapsed=2.3s (+2 more),
                  createTeam→[server/1004]×15 service=logic route=CreateTeam
```

### 5.9 Callback 路径接入错误聚合 — **v1 漏项**

当前 `Connection.dispatchListen → cb(resp) → Lua 回调脚本` 失败时只 `stresslog.Error`，monitor 完全无感（参见 `robot/robot.go:486-518`）。改造：

1. `MetricsCollector` 拆分 `RecordCallback` 为成功 / 失败两版：

   ```go
   func (c *MetricsCollector) RecordCallbackSuccess(name string) {
       if !c.enabled { return }
       c.totalActions.Add(1)
       am := c.getOrCreateAction("callback:" + name)
       am.successCount.Add(1)
   }

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

2. `robot.createListenCallback` 在 Lua 失败分支调用错误版本：

   ```go
   if err := h.robot.luaPool.RunCallbackScript(...); err != nil {
       monitor.Global().RecordCallbackError(cbName,
           engine.NewActionError(errcode.ErrCallbackLua, "script="+cbDef.Script, err))
       stresslog.Error(...)
   }
   ```

3. 默认（非 Lua）回调中 `factory.Parse` 失败分支同理：

   ```go
   if err != nil {
       monitor.Global().RecordCallbackError(cbName,
           engine.NewActionError(errcode.ErrCallbackParse, "proto="+cbDef.S2CProto, err))
       return
   }
   ```

4. **直接删除原 `RecordCallback(name)` 方法**，不保留 alias。

   全库调用点仅 1 处（`robot/robot.go:466` createListenCallback 的 Lua 成功分支），直接改为 `RecordCallbackSuccess(cbName)` 即可。保留 alias 会让"这两个方法有何区别"成为长期认知负担，得不偿失。

   实施前先 `rg "RecordCallback\(" --type go` 确认调用点数量与位置。

**前端展示策略**：避免用户混淆 `callback:onPushMsg` 与同名 `onPushMsg` action。

ActionsTab 当前已有 `actionsOnly` 开关（隐藏 `callback:*` 行）。v2 在此基础上**追加 `callbacksOnly` 开关**，两个开关互斥（同时启用时按"仅回调"优先），默认两者均关，列表混排：

| 状态 | 显示内容 |
|------|---------|
| `actionsOnly=false, callbacksOnly=false`（默认） | 全部条目混排，callback 行用 `推送` tag 标识 |
| `actionsOnly=true` | 仅普通 action（隐藏 `callback:*`） |
| `callbacksOnly=true` | 仅 callback 行（隐藏普通 action） |

具体实现：

```typescript
// ActionsTab.tsx
const [actionsOnly, setActionsOnly] = useState(false);
const [callbacksOnly, setCallbacksOnly] = useState(false);

const rows = data.actions.filter(a => {
  const isCallback = a.name.startsWith('callback:');
  if (callbacksOnly) return isCallback;
  if (actionsOnly)   return !isCallback;
  return true;
});

// 渲染 name 列时去前缀：a.name.replace(/^callback:/, '')
// 并附 <Tag color="cyan">推送</Tag> 标识
```

不引入"分组渲染"（antd Table 不直接支持）和"独立 Tab"（用户切 tab 不便）方案。

---

## 6. 可选：服务端错误码映射 — error.lua

### 6.1 背景

框架错误码（`errcode.Err*` 常量）由工具定义、有固定含义，并通过 `ErrorCode.String()` 自描述。但服务端错误码（headerErr）是游戏服务器返回的 `uint64`，工具本身不知道 1004 代表什么。

已有先例：`conf/adapter/codec.lua` 适配协议编解码。同理，可以让用户提供一个可选的 `error.lua` 将服务端错误码映射为可读描述。

### 6.2 文件位置与格式

```
conf/adapter/
  ├── codec.lua      # 必需：协议编解码（已有）
  └── error.lua      # 可选：服务端错误码映射（新增）
```

```lua
-- conf/adapter/error.lua（可选）
-- 将服务端 headerErr 映射为可读描述。文件不存在时无任何影响。

function describe_error(code)
    local errors = {
        [1004] = "金币不足",
        [1005] = "等级不够",
        [1006] = "背包已满",
        [2001] = "匹配超时",
        [3001] = "战斗结算失败",
    }
    return errors[code] or ""
end
```

**设计要点**：
- 文件完全可选，不存在时 `DescribeError` 返回空字符串
- 只有一个必需函数 `describe_error(code: number) → string`
- 和 `codec.lua` 一样，用户按自己的游戏服务器协议填写
- 任务下发时随 `codec.lua` 一起打包分发给 Agent

### 6.3 Adapter 接口扩展

```go
// adapter/adapter.go — Adapter 接口新增方法：

type Adapter interface {
    // ... 现有 8 个方法不变 ...

    // DescribeError 将服务端错误码映射为可读描述。
    // 可选功能：error.lua 未加载时返回空字符串。
    DescribeError(code uint64) string
}
```

### 6.4 Lua 适配器实现（含内存缓存）

在 `adapter/` 包的 Lua 适配器实现中：

```go
// 加载阶段（初始化时，与 codec.lua 同时期）：
// 1. 检查 conf/adapter/error.lua 是否存在
// 2. 存在 → 加载到 LState，缓存 describe_error 函数引用
// 3. 不存在 → 标记 hasErrorMap = false，DescribeError 直接返回 ""

type LuaAdapter struct {
    // ... 现有字段 ...
    hasErrorMap    bool
    errorMapScript *lua.FunctionProto
    errorDescCache sync.Map // uint64 → string，避免高频 headerErr 反复进 Lua 池
}

// DescribeError 将服务端错误码映射为可读描述。
// 第一次查询后结果（含空字符串）会被永久缓存。error.lua 运行时不可变，重启才能更新。
func (a *LuaAdapter) DescribeError(code uint64) string {
    if !a.hasErrorMap {
        return ""
    }
    if v, ok := a.errorDescCache.Load(code); ok {
        return v.(string)
    }
    desc := a.callDescribeError(code)
    a.errorDescCache.Store(code, desc) // 即使是 "" 也缓存，避免重复查询
    return desc
}

func (a *LuaAdapter) callDescribeError(code uint64) string {
    L := a.acquire()
    if L == nil {
        return ""
    }
    defer a.release(L)
    reg := L.Get(lua.RegistryIndex)
    fn := L.GetField(reg, "__adapter_describe_error")
    if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, lua.LNumber(code)); err != nil {
        stresslog.Warn("[ADAPTER] describe_error 调用失败", zap.Uint64("code", code), zap.Error(err))
        return ""
    }
    desc := lua.LVAsString(L.Get(-1))
    L.Pop(1)
    return desc
}
```

**为什么必须缓存**：
- 服务端 headerErr 在压测中**极其高频**（如登录失败重试、房间已满、匹配队列拒绝），每秒可能触发数千次。
- 每次 `acquire LState → 调 Lua → release` 至少几十微秒，1000 QPS 下吃掉 ~30ms/s。
- 错误码映射表是**编译时确定的静态表**，进程生命周期内不会变（`error.lua` 重载需重启），缓存命中率 100%。
- `sync.Map` 写入是 once-only（首次填充后只读），并发读零锁开销。

### 6.5 action 层集成

在构造 `NewServerError` 时调用 `DescribeError` 补充描述：

```go
// execTCPRequest / execUDPRequest / execTCPListen / execUDPListen 中 headerErr != 0 的处理：
if headerErr != 0 {
    ae.parseAndStoreResponse(def, respBody)

    desc := ae.adp.DescribeError(headerErr)  // 调用 error.lua（可选）
    detail := "service=" + def.Service + " route=" + routeKey
    if desc != "" {
        detail = desc + ": " + detail
    }
    return len(packet), len(respBody), NewServerError(headerErr, detail)
}
```

### 6.6 效果对比

**无 error.lua**（当前行为不变）：
```
Code: 1004 → {count: 15, msg: "[1004] service=logic route=CreateTeam"}
Code: 1005 → {count: 3,  msg: "[1005] service=logic route=StartBattle"}
```

**有 error.lua**：
```
Code: 1004 → {count: 15, msg: "[1004] 金币不足: service=logic route=CreateTeam"}
Code: 1005 → {count: 3,  msg: "[1005] 等级不够: service=logic route=StartBattle"}
```

前端错误分布表同时展示 code + msg，游戏开发者一眼就能定位问题。

---

## 7. headerErr != 0 的行为（不变）

改后对 `headerErr != 0` 的处理**完全沿用当前行为**（第 6 节的 error.lua 仅影响描述文本，不影响行为）：

| 维度 | 行为 |
|------|------|
| ActionResult | `ResultFailure`（classifyResult 不变） |
| failureCount | +1 |
| error map | 写入，key = headerErr 值（如 1004） |
| 响应解析 | 仍然 `parseAndStoreResponse`（后续 action 可用响应字段） |
| 字节统计 | sendBytes + recvBytes 都计入 |
| errorStrategy | 受 `abort` / `skip` / `ignore` 控制 |
| 流程中断 | 由 errorStrategy 决定（同改前） |

---

## 8. 前端适配

### 8.1 TypeScript 类型 — `cmd/web/src/types/api.ts`

```typescript
// 改前：
interface ErrorBucket {
    msg: string;
    count: number;
}

// 改后：注意命名同步改为 ErrorEntry，与后端对齐
export type ErrorKind = 'framework' | 'server';

export interface ErrorEntry {
    kind: ErrorKind;     // 显式区分框架/服务端错误，不再依赖 code 数值区间
    code: number;        // 框架 errcode.Err* 数值，或服务端 headerErr 原值
    codeName: string;    // ErrorCode.String()，如 "RECV_TIMEOUT"；服务端错误为 ""（由 error.lua 补描述）
    msgs: string[];      // 最近 N 条 Detail（不含 [code] 前缀），最多 3 条
    count: number;       // 累计计数
}

// ActionMetric.errors 字段类型同步改为 ErrorEntry[]
```

### 8.2 ActionsTab 展示

- **错误展开行**结构：`[Kind 标签] [Code:CodeName] ×Count | 最近 N 条 msgs`
  - Kind 标签：`framework` 用蓝色 / `server` 用橙色（与 v1 草案"按数值区分"完全不同——数值会冲突）
  - 服务端错误时 `codeName` 为空：可调用 `errorCodeRegistry.lookup(kind, code)` 取 `error.lua` 提供的描述（前端可选缓存）
- **顶级表格新增列**（与"失败/超时/跳过"并列）：
  - **取消**：`canceledCount`，灰色，反映 ctx 取消次数（不计入失败率）
- 错误数量列 `errors.length` 由"出现的错误种类数"变为"`(Kind, Code)` 唯一组合数"（聚合后通常 ≤ 10）

### 8.3 适配器编辑器改造 — 支持 error.lua

#### 8.3.1 背景

当前适配器编辑器只管理单个文件 `codec.lua`：
- IDB 数据库 `stressbot-resources-adapter`，固定 key `codec.lua`
- `AdapterTab` 组件：一个 Monaco 编辑器 + 导入/模板/保存/清空
- 基线同步只对比 `conf/adapter/codec.lua`
- 任务启动 multipart 只提交 `adapter/codec.lua`
- 后端 `TaskConfig.AdapterScript []byte` 存单个文件

新增 `error.lua` 后，适配器 tab 需要管理两个文件（`codec.lua` 必需，`error.lua` 可选），全链路需适配。

#### 8.3.2 IDB 存储层 — `services/resourcesStore.ts`

复用现有 `stressbot-resources-adapter` 数据库，新增 key `error.lua`：

```typescript
const CODEC_LUA_KEY = 'codec.lua';
const ERROR_LUA_KEY = 'error.lua';

// --- 新增 error.lua 存取 ---

export async function getErrorMapScript(): Promise<ResourceFile | undefined> {
  return get<ResourceFile>(ERROR_LUA_KEY, adapterStore);
}

export async function setErrorMapScript(content: string): Promise<ResourceFile> {
  const file: ResourceFile = {
    name: ERROR_LUA_KEY,
    content,
    size: byteLength(content),
    uploadedAt: new Date().toISOString(),
  };
  await set(ERROR_LUA_KEY, file, adapterStore);
  notify();
  return file;
}

export async function clearErrorMapScript(): Promise<void> {
  await del(ERROR_LUA_KEY, adapterStore);
  notify();
}
```

**关键点**：
- `clearAdapterScript()` 原来是 `clear(adapterStore)` 清空整个 DB，现在改为只删 `codec.lua` key，不影响 `error.lua`
- 同理，新增 `clearAllAdapterScripts()` 供完全清空场景使用（如有必要）
- `validateAdapter()` 只检查 `codec.lua`，不涉及 `error.lua`

#### 8.3.3 基线同步适配

**`LastBaselineIndex` 扩展**：

```typescript
interface LastBaselineIndex {
  proto: string[];
  script: string[];
  adapter: boolean;       // codec.lua
  errorMap: boolean;      // error.lua（新增）
}
```

**`syncResourcesFromBaseline()` 新增 error.lua 同步**：

```typescript
// 并行拉取增加 error.lua 基线
const ERROR_BASELINE_URL = `${BASELINE_PREFIX}/adapter/error.lua`;

const [protoIndex, scriptIndex, adapterText, errorMapText] = await Promise.all([
  fetchIndex(`${BASELINE_PREFIX}/proto/index.json`),
  fetchIndex(`${BASELINE_PREFIX}/scripts/index.json`),
  fetchFileText(CODEC_BASELINE_URL),
  fetchFileText(ERROR_BASELINE_URL),   // 新增
]);

// --- Error Map 同步（与 adapter 同逻辑，但可选）---
const existingErrorMap = await getErrorMapScript();
if (errorMapText !== null) {
  if (!existingErrorMap) {
    await setErrorMapScript(errorMapText);
    result.added.push({ type: 'adapter', name: ERROR_LUA_KEY });
  } else if (existingErrorMap.content === errorMapText) {
    result.unchanged.push({ type: 'adapter', name: ERROR_LUA_KEY });
  } else {
    result.conflicts.push({
      type: 'adapter',
      name: ERROR_LUA_KEY,
      localContent: existingErrorMap.content,
      baselineContent: errorMapText,
    });
  }
} else if (existingErrorMap && hadErrorMap) {
  result.removed.push({
    type: 'adapter',
    name: ERROR_LUA_KEY,
    localContent: existingErrorMap.content,
    baselineContent: '',
  });
}

saveLastBaseline({
  proto: protoIndex,
  script: scriptIndex,
  adapter: adapterText !== null,
  errorMap: errorMapText !== null,   // 新增
});
```

**`applyConflictResolution()` 适配**：`adapter` 类型已有处理逻辑，`error.lua` 名称走同一分支。

**`pushResourcesToBaseline()` 适配**：

```typescript
export async function pushResourcesToBaseline(): Promise<void> {
  const [protos, scripts, adapter, errorMap] = await Promise.all([
    listProto(), listScript(), getAdapterScript(), getErrorMapScript()
  ]);
  const fd = new FormData();
  // ... proto / scripts 不变 ...
  if (adapter) {
    fd.append('adapter/codec.lua', new Blob([adapter.content]), 'codec.lua');
  }
  if (errorMap) {
    fd.append('adapter/error.lua', new Blob([errorMap.content]), 'error.lua');
  }
  await fetch(`${API_PREFIX}/resources/baseline`, { method: 'POST', body: fd });
}
```

#### 8.3.4 基线 API — `services/baselineApi.ts`

```typescript
/** 获取基线 adapter/error.lua 内容 */
export async function fetchBaselineErrorMap(): Promise<string | null> {
  return fetchText(`${BASELINE_PREFIX}/adapter/error.lua`);
}
```

#### 8.3.5 AdapterTab UI 改造 — `ResourcesDrawer.tsx`

**当前**：一个 Monaco 编辑器，绑定单个 `content` state，操作 `codec.lua`。

**改后**：顶部增加文件切换（Segmented / Radio），编辑器绑定当前选中文件的内容：

```typescript
const ADAPTER_FILES = [
  { key: 'codec', label: 'codec.lua（必需）' },
  { key: 'error', label: 'error.lua（可选）' },
] as const;

function AdapterTab() {
  const [activeFile, setActiveFile] = useState<'codec' | 'error'>('codec');
  const [codecContent, setCodecContent] = useState('');
  const [errorContent, setErrorContent] = useState('');
  // ... 各自的 source、loaded state ...

  // 根据 activeFile 切换编辑器内容
  const currentContent = activeFile === 'codec' ? codecContent : errorContent;
  const setCurrentContent = activeFile === 'codec' ? setCodecContent : setErrorContent;

  // 保存逻辑按 activeFile 分流
  const onSave = async () => {
    if (activeFile === 'codec') {
      await setAdapterScript(currentContent);
      // ... validateAdapter() ...
    } else {
      await setErrorMapScript(currentContent);
      // error.lua 无强制校验（只有一个可选函数 describe_error）
    }
  };

  // 清空同理

  return (
    <Tabs size="small" defaultActiveKey="edit" items={[
      {
        key: 'edit',
        label: '编辑',
        children: (
          <Flex vertical gap={8}>
            <Segmented
              options={ADAPTER_FILES}
              value={activeFile}
              onChange={(v) => setActiveFile(v as 'codec' | 'error')}
            />
            {activeFile === 'error' && (
              <Alert type="info" showIcon message="可选：服务端错误码映射"
                description="提供一个 describe_error(code) 函数，将游戏服务器返回的错误码映射为中文描述。文件不存在时不影响运行。" />
            )}
            <Space size={4} wrap>
              <Upload ...>
                <Button icon={<InboxOutlined />} size="small">导入 .lua</Button>
              </Upload>
              {activeFile === 'codec' && (
                <Button onClick={onUseTemplate} size="small">载入模板</Button>
              )}
              {activeFile === 'error' && (
                <Button onClick={onUseErrorTemplate} size="small">载入模板</Button>
              )}
              <Button onClick={onSave} type="primary" size="small">保存</Button>
              <Button onClick={onClear} danger size="small">清空</Button>
              <span ...>{source ?? '尚未加载'}</span>
            </Space>
            <div style={{ height: '...' }}>
              <Editor language="lua" theme={monacoTheme} value={currentContent}
                onChange={(v) => setCurrentContent(v ?? '')} ... />
            </div>
          </Flex>
        ),
      },
      // spec tab 不变（codec.lua 的 7 函数规范）
    ]} />
  );
}
```

**error.lua 默认模板**：

```lua
-- conf/adapter/error.lua（可选）
-- 将服务端 headerErr 映射为可读描述。

function describe_error(code)
    local errors = {
        -- [1004] = "金币不足",
        -- [1005] = "等级不够",
    }
    return errors[code] or ""
end
```

#### 8.3.6 任务启动 multipart — `services/taskActions.ts`

```typescript
const [protos, scripts, adapterRes, errorMapRes] = await Promise.all([
  listProto(), listScript(), getAdapterScript(), getErrorMapScript()
]);

const adapterContent = adapterRes?.content ?? null;
if (!adapterContent) {
  throw new ApiError({ code: 'INVALID_ARGUMENT', message: '缺少协议适配器...' }, 400);
}
// error.lua 可选，不校验

const errorMapContent = errorMapRes?.content ?? null;

// multipart 组装
fd.append('adapter/codec.lua', new Blob([adapterContent], { type: 'text/plain' }), 'codec.lua');
if (errorMapContent) {
  fd.append('adapter/error.lua', new Blob([errorMapContent], { type: 'text/plain' }), 'error.lua');
}
```

### 8.4 后端适配器全链路

#### 8.4.1 数据模型 — `admin/types.go`

```go
type TaskConfig struct {
    FlowJSON        json.RawMessage   `json:"flowJson"`
    ProtoFiles      map[string][]byte `json:"protoFiles,omitempty"`
    LuaScripts      map[string][]byte `json:"luaScripts,omitempty"`
    AdapterScript   []byte            `json:"adapterScript,omitempty"`
    ErrorMapScript  []byte            `json:"errorMapScript,omitempty"`   // 新增
    RobotConfig     RobotConfig       `json:"robotConfig"`
    Deadline        *time.Time        `json:"deadline,omitempty"`
}
```

#### 8.4.2 任务创建解析 — `admin/handlers.go`

```go
// 现有 codec.lua
if adapterFile, _, err := r.FormFile("adapter/codec.lua"); err == nil {
    adapterData, _ := io.ReadAll(adapterFile)
    adapterFile.Close()
    cfg.AdapterScript = adapterData
}

// 新增 error.lua（可选）
if errorMapFile, _, err := r.FormFile("adapter/error.lua"); err == nil {
    errorMapData, _ := io.ReadAll(errorMapFile)
    errorMapFile.Close()
    cfg.ErrorMapScript = errorMapData
}
```

#### 8.4.3 任务配置下载 — `admin/handlers.go` handleGetTaskConfig

```go
case "adapter/codec.lua":
    if task.Config.AdapterScript == nil {
        http.NotFound(w, r)
        return
    }
    w.Header().Set("Content-Type", "text/plain")
    w.Write(task.Config.AdapterScript)

case "adapter/error.lua":   // 新增
    if task.Config.ErrorMapScript == nil {
        http.NotFound(w, r)
        return
    }
    w.Header().Set("Content-Type", "text/plain")
    w.Write(task.Config.ErrorMapScript)
```

#### 8.4.4 配置文件清单 — `admin/handlers.go`

```go
if task.Config.AdapterScript != nil {
    configFiles = append(configFiles, "adapter/codec.lua")
}
if task.Config.ErrorMapScript != nil {   // 新增
    configFiles = append(configFiles, "adapter/error.lua")
}
```

#### 8.4.5 基线写入 — `admin/handlers.go`

```go
// handleBaselinePush 新增解析
if errorMapFile, _, err := r.FormFile("adapter/error.lua"); err == nil {
    errorMapData, _ := io.ReadAll(errorMapFile)
    errorMapFile.Close()
    if err := safeWriteFile("conf/adapter", "error.lua", errorMapData); err != nil {
        stresslog.Warn("基线更新错误映射失败", zap.Error(err))
    }
}

// writeBaselineFiles 新增
if cfg.ErrorMapScript != nil {
    if err := safeWriteFile("conf/adapter", "error.lua", cfg.ErrorMapScript); err != nil {
        stresslog.Warn("写入基线错误映射失败", zap.Error(err))
    }
}
```

#### 8.4.6 基线读取路由 — `admin/handlers.go`

```go
mux.HandleFunc("GET /sbot/baseline/adapter/codec.lua", s.handleBaselineAdapter)
mux.HandleFunc("GET /sbot/baseline/adapter/error.lua", s.handleBaselineErrorMap)  // 新增

func (s *AdminServer) handleBaselineErrorMap(w http.ResponseWriter, r *http.Request) {
    http.ServeFile(w, r, "conf/adapter/error.lua")
}
```

#### 8.4.7 错误码查询端点 — `admin/handlers.go`（可选）

前端启动时通过此端点拉取框架错误码全量表，用于 i18n 兜底（codeName 为空时展示中文描述）。

```go
mux.HandleFunc("GET /sbot/api/error-codes", s.handleErrorCodeIndex)

func (s *AdminServer) handleErrorCodeIndex(w http.ResponseWriter, _ *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(errcode.AllCodes())
}
```

`errcode.AllCodes()` 与 `CodeInfo` 已在 **3.1 节 `errcode/codes.go` 的单一数据源 `codeRegistry`** 中定义，admin 这里只是透传。

**说明**：
- 只返回 `KindFramework` 的常量，服务端错误码无静态映射（由 `error.lua` 动态提供）。
- 返回数据量极小（~25 条），前端可永久缓存，不需要轮询。
- `errorCodeRegistry.ts` 启动时 fetch 一次，渲染 ActionsTab 时 fallback 用。

#### 8.4.8 Agent task_runner — `agent/task_runner.go`

```go
// 3. 加载协议适配器
adapterScript := filepath.Join(confDir, "adapter", "codec.lua")
if _, err := os.Stat(adapterScript); err != nil {
    adapterScript = r.cfg.AdapterScript
}

// 可选：加载错误码映射
errorMapScript := filepath.Join(confDir, "adapter", "error.lua")
if _, err := os.Stat(errorMapScript); err != nil {
    errorMapScript = ""  // 不存在则不传
}

poolSize := runtime.NumCPU()
adp, err := adapter.NewLuaAdapter(poolSize, adapterScript, errorMapScript)  // 新增参数
```

#### 8.4.9 LuaAdapter 构造函数 — `adapter/` 包

```go
// 改前：
func NewLuaAdapter(poolSize int, scriptPath string) (*LuaAdapter, error)

// 改后：
func NewLuaAdapter(poolSize int, scriptPath string, errorMapPath string) (*LuaAdapter, error)
```

加载阶段：
1. 加载 `codec.lua`（必需，逻辑不变）
2. 检查 `errorMapPath` 是否非空 → 文件存在 → 加载到 LState，缓存 `describe_error` 函数引用
3. 不存在或为空 → 标记 `hasErrorMap = false`，`DescribeError` 直接返回 `""`

---

## 9. 涉及文件清单

| 文件 | 变更类型 | 改动说明 |
|------|----------|---------|
| **`errcode/codes.go`** | **新建（v2）** | `ErrorCode` / `Kind` 常量；`codeRegistry`（单一数据源）；派生 `String()` 与 `AllCodes()`；`CodeInfo` 供前端查询；无任何 stressbot 内部依赖 |
| `engine/errors.go` | 新建 | 从 `action.go` 迁出 `ErrFieldNil`(原 ErrActionSkip)/`ErrTimeout`；`ActionError` 类型；实现 `CodedError` 接口（`ErrorKind/Code/Detail` 方法）|
| `network/connection.go` | 修改 | Send 签名 `(bool, int)` → `(int, error)`；RequestResponse 签名 `(*Message, int)` → `(*Message, error)`；各路径返回 ActionError（注意 c==nil 不能访问 c.serviceName）|
| `network/heartbeat.go` | 修改 | 适配 Connection.Send 新签名（忽略 error） |
| `engine/action.go` | 修改 | NetSender 接口 7 个方法签名变更；所有 exec* 方法改用 ActionError；execHTTPRequest 内 JSON/form 序列化错误也替换；headerErr 处理调用 adp.DescribeError |
| **`script/api_network.go`** | **修改（v2 漏项）** | 6 个 Lua API（connect_tcp/udp、tcp/udp_request、tcp/udp_send）适配 NetSender 新签名；Lua 侧返回值约定**不变** |
| `robot/robot.go` | 修改 | netSenderAdapter 适配新接口；executeLuaAction 改用 ActionError；HTTPRequest 中 5 处 fmt.Errorf 替换；RecordAction 传 error；classifyResult 增加 context.Canceled/DeadlineExceeded 判断；createListenCallback 接入 RecordCallbackError |
| `monitor/collector.go` | 修改 | RecordAction errMsg string → err error；ErrorEntry 增加 Kind/Code/CodeName/Messages；`errKey` + `errorBucket`（环形缓冲）；定义 CodedError 接口；recordError 按 `(Kind, Code)` 聚合；新增 ResultCanceled / canceledCount；新增 RecordCallbackSuccess / RecordCallbackError |
| `monitor/snapshot.go` | 修改 | MergeSnapshots error 合并改用 `(Kind, Code)` 二元组做 key；Messages 取并集去重；ActionSnapshot 新增 CanceledCount；Apdex/SuccessRate 分母不含 canceledCount |
| `monitor/reporter.go` | 修改 | console 输出 `[Kind/Code CodeName]` 格式；适配 ErrorEntry.Messages 数组 |
| `cmd/web/src/types/api.ts` | 修改 | ErrorBucket → ErrorEntry，增加 `kind: 'framework' \| 'server'`、`code: number`、`codeName: string`、`msgs: string[]` |
| `cmd/web/src/components/monitoring/tabs/ActionsTab.tsx` | 修改 | 错误展开行展示 Kind 标签 + Code + CodeName + 最近 N 条 msgs；新增"取消"列与"跳过"列并列 |
| `cmd/web/src/services/runtimeStore.ts`（若有） | 修改 | 透传新 ErrorEntry 字段 |
| **`cmd/web/src/services/errorCodeRegistry.ts`** | **新建（v2，可选）** | 启动时 fetch `/sbot/api/error-codes`（admin 透传 `errcode` 常量表）缓存到全局，UI 渲染时 fallback 用 |
| `adapter/adapter.go` | 修改 | Adapter 接口新增 DescribeError(code uint64) string |
| `adapter/lua_adapter.go` | 修改 | NewLuaAdapter 新增 errorMapPath 参数；实现 DescribeError；**新增 errorDescCache sync.Map 缓存** |
| `conf/adapter/error.lua` | 可选新增 | 用户提供的错误码映射表 |
| `admin/types.go` | 修改 | TaskConfig 新增 ErrorMapScript []byte |
| `admin/handlers.go` | 修改 | multipart 解析 error.lua；配置下载/基线读写 error.lua；新增 baseline 路由；**新增 `GET /sbot/api/error-codes` 透传 errcode 全量表**（可选）|
| `admin/history.go`（若存在） | 修改 | MySQL errors 归档表加 `kind`/`code`/`code_name` 列；现有 `msg` 列改 TEXT 存 Messages JSON |
| `agent/task_runner.go` | 修改 | 下载并传递 error.lua 给 NewLuaAdapter |
| `cmd/agent/main.go` | 修改 | `loadAdapter` 单机模式也需读 `conf/adapter/error.lua` 传 NewLuaAdapter |
| `cmd/web/src/services/resourcesStore.ts` | 修改 | 新增 error.lua 存取；适配器基线同步含 error.lua；clearAdapterScript 改为按 key 删 |
| `cmd/web/src/services/baselineApi.ts` | 修改 | 新增 fetchBaselineErrorMap() |
| `cmd/web/src/services/taskActions.ts` | 修改 | multipart 提交 adapter/error.lua（可选） |
| `cmd/web/src/components/modules/ResourcesDrawer.tsx` | 修改 | AdapterTab 文件切换（codec/error），双编辑器状态管理 |

---

## 10. 变更顺序（依赖关系）

```
① errcode/codes.go                新建顶层包：ErrorCode/Kind 常量 + codeRegistry（单一数据源）+ String() + AllCodes() + CodeInfo，无任何依赖（消除循环）
② engine/errors.go                新建：ErrFieldNil/ErrTimeout（迁出）+ ActionError（含 CodedError 接口方法），依赖 ①
③ adapter/adapter.go              新增 DescribeError 方法签名
④ adapter/lua_adapter.go          构造函数加 errorMapPath；实现 DescribeError + errorDescCache
⑤ network/connection.go           依赖 ② 的 ActionError（Send + RequestResponse 签名变更，注意 c==nil）
⑥ network/heartbeat.go            依赖 ⑤ 的 Send 新签名
⑦ engine/action.go                依赖 ⑤ 新签名 + ② ActionError + ③ DescribeError；含 ErrUnknownPattern / execHTTPRequest 序列化错误替换
⑧ script/api_network.go           依赖 ⑦ 新 NetSender 接口（v1 漏项，必须改）
⑨ robot/robot.go                  依赖 ⑦ 新 NetSender；HTTPRequest 5 处替换；classifyResult 加 ctx 判断；createListenCallback 接入 RecordCallbackError
⑩ monitor/collector.go            依赖 ① + ② 的 CodedError 接口；新增 ResultCanceled / errKey / errorBucket / RecordCallbackError
⑪ monitor/snapshot.go             依赖 ⑩ 新 ErrorEntry；ActionSnapshot 新增 CanceledCount；Apdex 分母调整
⑫ monitor/reporter.go             依赖 ⑪
⑬ admin/types.go                  TaskConfig 新增 ErrorMapScript
⑭ admin/handlers.go               multipart 解析/配置下载/基线读写 error.lua；新增 `GET /sbot/api/error-codes`（仅透传 ① 的 errcode.AllCodes()）；可选 baseline 路由
⑮ admin/history.go                MySQL errors 归档表加 kind/code/code_name 列（如存在归档）
⑯ agent/task_runner.go            下载并传递 error.lua 给 NewLuaAdapter
⑰ cmd/agent/main.go               单机模式 loadAdapter 也需传 errorMapPath
⑱ conf/adapter/error.lua          可选，无编译依赖
⑲ 前端适配（types + ActionsTab + resourcesStore + baselineApi + taskActions + ResourcesDrawer + errorCodeRegistry）
```

---

## 11. 改前 vs 改后对比

### 错误传播链路

```
改前：
connection.RequestResponse  → (nil, 0)         ← 原因丢失
  netSenderAdapter.TCPRequest → (nil, 0, false) ← 原因丢失
    action.execTCPRequest     → fmt.Errorf(...)  ← 自由字符串，无法聚合
      robot.ExecuteAction     → errMsg string    ← 传字符串
        monitor.recordError   → key = 全字符串   ← 爆炸

改后：
connection.RequestResponse  → (nil, NewActionError(4, ...))  ← 具体原因
  netSenderAdapter.TCPRequest → (nil, 0, err)               ← 透传 ActionError
    action.execTCPRequest     → err                          ← 透传或 NewServerError
      robot.ExecuteAction     → err error                    ← 传 error
        monitor.recordError   → key = uint64(4)              ← 固定类别，可聚合
```

### monitor error map 对比

**改前**（每个参数组合一条）：
```
"TCP 请求失败: service=logic route=1001 respKey=1002 elapsed=2.3s"  → 15
"TCP 请求失败: service=logic route=1001 respKey=1002 elapsed=3.1s"  → 8
"TCP 请求失败: service=battle route=2001 respKey=2002 elapsed=5.0s" → 3
"adapter.EncodeTCP 返回 nil，检查 codec.lua"                         → 1
"服务端错误码 1004: service=logic route=CreateTeam"                   → 12
```

**改后**（按 uint64 Code 聚合）：
```
Code: 4    → 26  "[4] service=logic elapsed=2.3s"         // RECV_TIMEOUT（合并了所有超时）
Code: 11   → 1   "[11] route=1001"                          // ENCODE_FAILED
Code: 1004 → 12  "[1004] service=logic route=CreateTeam"   // 服务端错误码
```

---

## 12. 附：字段级 skip 问题

### 12.1 问题一：名称相撞

| 名称 | 位置 | 含义 |
|------|------|------|
| `ErrActionSkip` | `engine/action.go:23` | 字段缺失（`isImplicitRequired` 类型的 binding 值为 nil）导致动作跳过 |
| `errSkip` | `engine/executor.go:20`（未导出） | `errorStrategy: "skip"` 配置导致跳过当前 sequence/loop 剩余节点 |

两者语义完全不同但名称几乎一样，容易混淆。

**改法**：`ErrActionSkip` → `ErrFieldNil`（直接说明原因），`errSkip` 保持不变（仅 executor 内部使用）。

### 12.2 问题二：字段级 skip 被吞掉，监控误记为成功

当前链路（有问题）：

```
bindFields → ErrActionSkip（isImplicitRequired 字段为 nil）
  execTCPSend 捕获 → return (0, nil)              ← 吞掉了，不传播
    ActionExecutor.Execute → return (0, 0, nil)
      robot.ExecuteAction → classifyResult(nil) → ResultSuccess   ← 监控误记为成功
```

`ResultSkipped` 在 monitor 里已有，`classifyResult` 也认 `ErrActionSkip`，但 err 被提前吞了，永远走不到。

**触发条件**（`engine/flow.go:isImplicitRequired`）：以下 5 种 binding type 的字段值为 nil 时自动触发跳过（即使未标记 `Required`）：
- `stateFirst` — state 列表取第一个，列表为空则 nil
- `stateRandom` / `stateRandomN` — state 列表随机取，列表为空则 nil
- `stateMapKey` / `stateMapValue` — state map 取 key/value，map 为空则 nil

### 12.3 修复方案

**删除 4 处 `exec*` 方法中的 `ErrActionSkip` 吞咽逻辑**，让它透传到 `robot/robot.go` 的 `ExecuteAction`：

```go
// engine/action.go — 删除 exec* 中的吞咽：
// 改前（4 处）：
if errors.Is(err, ErrActionSkip) {
    stresslog.Debug("[ACTION] 字段缺失，跳过 TCPSend", ...)
    return 0, nil  // 或 return 0, 0, nil
}

// 改后：直接删除这些 if 块，让 err 正常返回

// bindFields 中的 Warn 日志保留（仍然知道是哪个字段缺失）
```

**在 `robot/robot.go` 的 `ExecuteAction` 中统一处理**：

```go
// 改前：
sendBytes, recvBytes, err = h.robot.actionExec.Execute(actionDef)
result := classifyResult(err)
errMsg := ""
if err != nil {
    errMsg = err.Error()
}
mc.RecordAction(actionDef.Name, result, time.Since(start), sendBytes, recvBytes, errMsg)
return err

// 改后：
sendBytes, recvBytes, err = h.robot.actionExec.Execute(actionDef)
result := classifyResult(err)
mc.RecordAction(actionDef.Name, result, time.Since(start), sendBytes, recvBytes, err)
if errors.Is(err, engine.ErrFieldNil) {  // 原 ErrActionSkip
    return nil  // 不影响 executor 流程，但 monitor 已正确记录为 ResultSkipped
}
return err
```

**效果**：

```
bindFields → ErrFieldNil
  execTCPSend → 透传（不捕获）
    ActionExecutor.Execute → return (0, 0, ErrFieldNil)
      robot.ExecuteAction:
        classifyResult(ErrFieldNil) → ResultSkipped   ← monitor 正确记录
        return nil                                    ← executor 正常继续
```

### 12.4 涉及文件

| 文件 | 改动 |
|------|------|
| `engine/action.go` | `ErrActionSkip` 重命名为 `ErrFieldNil`（定义在 L23，返回在 L152）；删除 4 处吞咽逻辑（L430/463/578/627）；`bindFields` Warn 日志更新引用 |
| `robot/robot.go` | `ExecuteAction` 统一处理 `ErrFieldNil`：记录 `ResultSkipped` 后返回 `nil`；`classifyResult`（L327）更新引用 |
| `monitor/collector.go` | `ResultSkipped` 注释（L19）更新引用：`ErrActionSkip` → `ErrFieldNil` |

### 12.5 重命名前的 baseline-grep（v2 补充）

`ErrActionSkip` 是 exported 符号，可能被外部代码引用。重命名前**必须**全库扫描，避免漏改：

```bash
# Go 代码
rg "ErrActionSkip" --type go

# 文档与配置（中文/英文混用情况）
rg "ErrActionSkip|action skipped|字段缺失.*跳过"

# 前端（不太可能，但保险）
rg "ErrActionSkip" cmd/web/src/
```

预期匹配点：
- `engine/action.go:23` 定义、L152 返回
- `engine/action.go:430/463/578/627` 4 处 `errors.Is` 吞咽
- `robot/robot.go:327` `classifyResult` 引用
- `monitor/collector.go:19` 注释
- `docs/error-handling.md`（如有相关说明）

如出现以上未列出的匹配点，必须**回写 spec 增补**，不允许"边改边发现"。

### 12.6 字段级 skip 后是否注册 listenCallbacks — **v2 决策**

设计 12.3 让 ExecuteAction 在 ErrFieldNil 时 `return nil`，executor 看到 nil 后会继续注册 listenCallbacks（`engine/executor.go:190`）。

**问题**：如果当前 action 是 `tcpConnect`/`udpConnect`，字段缺失意味着连接根本没建，后续 `RegisterListen` 找不到连接只会 `stresslog.Warn("[ROBOT] 无连接可注册监听")`，没有副作用但产生噪声。

**决策（保持现状 + 明确语义）**：

- ExecuteAction 在 ErrFieldNil 时仍返回 `nil`，executor 仍尝试注册 listenCallbacks。
- `RegisterListen` 内部找不到连接时**降级为 Debug 日志**（当前是 Warn），避免大量误报。
- 文档明确：**字段级 skip 不阻断 listenCallbacks 注册流程**，调用方需保证 listen 注册对"无连接"是幂等且无害的。

涉及文件追加：

| 文件 | 改动 |
|------|------|
| `robot/robot.go:439` | `RegisterListen` 中 `stresslog.Warn("[ROBOT] 无连接可注册监听")` → `stresslog.Debug(...)` |

---

## 13. 验证

### 13.1 编译

```bash
go build -buildvcs=false ./...
```

必须通过。重点关注：
- `errcode` 包能被 `engine` / `monitor` / `network` 同时引用，无循环依赖（`go list -deps ./engine | grep monitor` 应为空，反之亦然）
- `script/api_network.go` 6 个 Lua API 与新 NetSender 接口对齐

### 13.2 单元测试 — **v2 新增**

新建以下测试文件：

| 文件 | 覆盖点 |
|------|--------|
| `engine/errors_test.go` | `NewActionError` 三种构造器 / `Error()` 格式 / `IsServerError()` / `Unwrap()` 链 / `errors.Is(err, ErrTimeout)` 命中 / `CodedError` 接口实现 |
| `monitor/collector_test.go` | `recordError` 按 `(Kind, Code)` 聚合：不同 Kind 同 Code 不会合并；`errorBucket.record` 并发安全（go-test -race）；环形缓冲 N 条循环覆盖 |
| `monitor/snapshot_test.go` | `MergeSnapshots` 合并两个 snapshot 的 errors，Messages 取并集去重，Count 累加 |
| `adapter/lua_adapter_test.go` | `DescribeError` 缓存命中：第一次调用 Lua，第二次不调 Lua（用 mock LState 计数） |
| `robot/robot_test.go` | `classifyResult` 6 个分支：nil / context.Canceled / context.DeadlineExceeded / ErrFieldNil / ErrTimeout / 其他 |

### 13.3 改造前后回归对比

```bash
# 1. 改造前：跑 5 分钟任务，导出错误分布
git stash  # 暂存修改
go run ./cmd/agent -config conf/config.json &
sleep 300
curl http://localhost:6060/metrics > /tmp/errors-before.json
killall stressbot

# 2. 改造后：相同配置相同时长
git stash pop
go run ./cmd/agent -config conf/config.json &
sleep 300
curl http://localhost:6060/metrics > /tmp/errors-after.json

# 3. 对比关键指标
jq '.actions[] | {name, sample: .sampleCount, fail: .failureCount, errCount: (.errors | length)}' \
  /tmp/errors-before.json /tmp/errors-after.json
```

**预期变化**：

| 指标 | 改前 | 改后 |
|------|------|------|
| `sampleCount` 总和 | N | N（不变） |
| `failureCount` 总和 | M | ≤ M（部分 ctx.Canceled 移出失败统计） |
| `timeoutCount` | 偏少 | 增加（之前混在 failureCount 里的超时被正确归类） |
| `canceledCount` | 不存在 | 新增字段，对应 ctx.Err() 数量 |
| `errors[].length` | 上百条 | 个位数到十几条（按 Code 聚合后） |

### 13.4 功能验证（手动观察）

启动单机模式跑任务 2~5 分钟：

- error map 按 `(Kind, Code)` 聚合，同类错误不再爆炸
- 超时被正确归类为 ResultTimeout（不再混在 ResultFailure 里）
- 任务停止时 ResultCanceled 增加，failureCount 不增加
- `headerErr != 0` 行为不变（ResultFailure + errorStrategy + 仍解析响应）
- 服务端错误码以原值（如 1004）出现在 error map 中，Kind="server"
- error.lua 存在时 console 输出含中文描述
- error.lua 不存在时不影响运行

### 13.4.1 History 归档验证

若启用了 MySQL 历史归档，跑完一次任务后检查：

```sql
-- 确认 errors 表有 kind/code/code_name 列且正确写入
SELECT he.kind, he.code, he.code_name, COUNT(*) as cnt
FROM history_errors he
JOIN history_tasks ht ON he.task_id = ht.id
WHERE ht.id = <taskId>
GROUP BY he.kind, he.code, he.code_name;

-- 预期结果示例：
-- kind       | code | code_name     | cnt
-- framework  |    4 | RECV_TIMEOUT  |  26
-- framework  |    1 | CONN_NOT_FOUND|   2
-- server     | 1004 |               |  15
```

重点检查：
- `kind` 列只有 `framework` / `server` 两种值
- 框架错误的 `code_name` 非空，服务端错误的 `code_name` 为空（由 error.lua 补描述）
- 旧归档数据（改前）的 errors 记录兼容：缺少 kind/code 列时前端展示降级为旧格式

### 13.5 前端验证

- ActionsTab 错误展开行显示：`Kind 标签 + Code + CodeName + 最近 N 条 msgs`
- 框架错误（Kind=framework）和服务端错误（Kind=server）视觉上区分（如 tag 颜色）
- 新增"取消"列（灰色）与"跳过"列并列，颜色弱于"失败"列
- 适配器编辑器能在 codec.lua / error.lua 间切换
