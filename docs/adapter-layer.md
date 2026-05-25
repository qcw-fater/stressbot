# 协议适配层 -- 三层架构技术文档

## 1. 概述

stressbot 采用 **Network / Adapter / Business** 三层架构。所有协议编解码知识从 Go 引擎移至用户编写的 Lua 适配器脚本（`codec.lua`），使 Go 引擎协议无关，支持任意二进制消息头协议的游戏服务器压测。

**消息体序列化格式**：本工具固定使用 Protobuf（通过 `protox` 动态加载 `.proto` 文件）。对于非 Protobuf 服务器，用户通过 Lua 脚本手动构建 body 字节（`c2sProto` 留空时 `buildBody` 返回 nil，Lua 负责全权构建）。

### 1.1 重构目标

将协议编解码知识完全移入用户编写的 Lua 适配器脚本，Go 引擎保持协议无关，从而实现对任意消息头协议的游戏服务器压测支持。切换服务器只需更换 `codec.lua` + `flow.json` + `.proto` 文件，无需修改任何 Go 代码。

### 1.2 强制约束

| 约束 | 说明 |
|------|------|
| Lua 版本 | 项目使用 `gopher-lua`，实现 Lua 5.1，不支持 `string.pack`/`string.unpack`。字节操作必须用 `string.byte`/`string.char` + `bit` 模块 |
| gnet 热路径 | `OnTraffic` 每包都调用，`BodyLength` 必须是纯 Go 操作；通过初始化时从 Lua 缓存元信息实现 |
| LuaAdapter 池隔离 | adapter 池 LState 只注册 `bit` 和 `zlib` 模块，不注册业务 API；script 层使用 per-robot LState，两者严格隔离 |
| TCP/UDP 解码独立 | Adapter 接口提供 `DecodeTCP` 和 `DecodeUDP` 两个独立方法，允许对 TCP/UDP 使用不同的解码策略 |

## 2. 三层架构

### 2.1 层次职责

```
┌─────────────────────────────────────┐
│ Business Layer（engine + robot）      │  流程执行、状态管理、动作调度
├─────────────────────────────────────┤
│ Adapter Layer（adapter 包）           │  协议帧编解码（Lua 桥接）
├─────────────────────────────────────┤
│ Network Layer（network 包 + gnet）    │  TCP/UDP 连接管理、收发、心跳
└─────────────────────────────────────┘
```

- **Business Layer**：流程引擎执行 flow.json 中定义的节点图，通过 `ActionDef` 声明式描述消息收发。不感知任何协议细节。
- **Adapter Layer**：通过 `Adapter` 接口桥接 Go 引擎与 Lua 适配器脚本。Go 只调用接口方法，不感知具体协议格式。
- **Network Layer**：基于 gnet 的连接管理，负责 TCP/UDP 帧分割、请求-响应匹配、持久化监听、心跳。

### 2.2 包依赖图

```
cmd/agent  ->  robot/  ->  engine/  ->  state/
                          ->  protox/
                          ->  adapter/  <-  (gopher-lua only)
               ->  network/ ->  adapter/
               ->  script/  ->  adapter/
                           ->  engine/
```

`adapter/` 包只依赖 `gopher-lua` 和标准库，不依赖项目内其他包，无循环依赖风险。

### 2.3 数据流（发送）

```
flow.json (route any)
  -> engine/ActionExecutor.buildBody()
  -> adapter.EncodeTCP(route, body, secretKey)
  -> netSender.TCPSend(service, packet)
  -> Connection.Send(data)
  -> gnet.AsyncWrite()
```

### 2.4 数据流（接收）

```
gnet.OnTraffic()
  -> adapter.HeaderSize()（纯 Go，缓存值）
  -> adapter.BodyLength(headerData)（纯 Go，缓存逻辑）
  -> adapter.DecodeTCP/UDP(data, secretKey)（Lua 调用）
  -> Connection.OnReceive(routeKey, body, headerErr)
  -> responseMap[routeKey] 或 listenResp[routeKey]
```

## 3. Adapter 接口

`adapter/adapter.go` 定义了协议适配器接口，共 10 个方法。

### 3.1 完整接口签名

```go
type Adapter interface {
    // ─── 帧分割（纯 Go 实现，无 Lua 调用）────────────────────────

    // HeaderSize 返回消息头固定字节数。
    HeaderSize() int

    // BodyLength 从消息头字节中解析消息体长度。
    // 此方法在 gnet 热路径中被每包调用，禁止进行任何 Lua 调用。
    BodyLength(headerData []byte) int

    // ─── 编解码（Lua 调用）──────────────────────────────────────

    // EncodeTCP 将路由信息+消息体编码为完整 TCP 数据包。
    EncodeTCP(route any, body []byte, secretKey []byte) []byte

    // EncodeUDP 将路由信息+消息体编码为 UDP 数据包。
    EncodeUDP(route any, body []byte, secretKey []byte) []byte

    // DecodeTCP 将 TCP 数据包解码为路由键、消息体和协议头错误码。
    DecodeTCP(data []byte, secretKey []byte) (routeKey string, body []byte, headerErr uint64)

    // DecodeUDP 将 UDP 数据包解码为路由键、消息体和协议头错误码。
    DecodeUDP(data []byte, secretKey []byte) (routeKey string, body []byte, headerErr uint64)

    // ExpectedRouteKey 从发送路由计算期望的响应路由键。
    ExpectedRouteKey(route any) string

    // ─── 生命周期 / 辅助 ─────────────────────────────────────────

    // Close 释放适配器持有的资源（如 LState 池）。
    Close()

    // DescribeError 将服务端协议头错误码映射为可读描述。
    DescribeError(code uint64) string
}
```

### 3.2 方法分类

**热路径方法（纯 Go，零 Lua 调用）**：

| 方法 | 返回值 | 用途 | 调用频率 |
|------|--------|------|----------|
| `HeaderSize()` | 缓存的固定 int | 帧头大小 | gnet OnTraffic 每帧 |
| `BodyLength(headerData)` | 纯 Go 计算 | 从帧头解析 body 长度 | gnet OnTraffic 每帧 |

这两个方法在初始化时从 Lua 获取并缓存到 Go 结构体。gnet 的 `OnTraffic`（每帧必调）**永不调用 Lua**。

**Lua 桥接方法**：

| 方法 | 用途 | 调用时机 |
|------|------|----------|
| `EncodeTCP(route, body, secretKey)` | TCP 消息编码（加密+封帧） | 发送请求/消息时 |
| `EncodeUDP(route, body, secretKey)` | UDP 消息编码（加密+封帧） | 发送 UDP 消息时 |
| `DecodeTCP(data, secretKey)` | TCP 消息解码 | gnet OnTraffic 收到完整帧时 |
| `DecodeUDP(data, secretKey)` | UDP 消息解码 | gnet OnTraffic 收到完整帧时 |
| `ExpectedRouteKey(route)` | 将 route 转换为 routeKey | 请求-响应匹配、监听注册时 |
| `DescribeError(code)` | 错误码描述 | headerErr != 0 时 |
| `Close()` | 释放 LState 池 | 程序退出时 |

### 3.3 route 不透明设计

`route` 参数类型为 `any`，Go 引擎不解析其内部结构，逐字传递给 Lua。典型格式：

```json
{"cmd": 1, "act": 1}
```

JSON 中的数值被 Go 反序列化为 `float64`，通过 `RouteToLuaValue` 转换为 Lua LNumber（整数值转为 int64 避免浮点问题）。

### 3.4 与计划的差异

| 差异点 | 计划设计 | 实际代码 |
|--------|----------|----------|
| Decode 方法 | 单一 `Decode(data, secretKey)` 返回 `(string, []byte, uint16)` | 分离为 `DecodeTCP` 和 `DecodeUDP`，返回 `(string, []byte, uint64)` |
| headerErr 类型 | `uint16` | `uint64` |
| Close 方法 | 无 | 有 `Close()` |
| DescribeError 方法 | 无 | 有 `DescribeError(code uint64) string` |
| UDPEncryptOffset | 有 `UDPEncryptOffset() int` 方法 | 无，偏移量完全由 codec.lua 内部管理 |
| ExpectedResponseKey | `ExpectedResponseKey(route)` | `ExpectedRouteKey(route)`（方法名不同） |

## 4. LuaAdapter 实现

`adapter/lua_adapter.go` -- 通过 gopher-lua LState 池调用适配器脚本实现 Adapter 接口。

### 4.1 结构体

```go
type LuaAdapter struct {
    states      chan *lua.LState   // 有界 channel 池，容量 = poolSize
    scriptProto *lua.FunctionProto // 预编译的适配器脚本字节码

    // 初始化时从 Lua 缓存的元信息
    headerSize  int            // 消息头固定字节数
    bodyLenInfo BodyLengthInfo // 消息体长度解析元信息

    // error.lua 错误码映射（可选功能）
    hasErrorMap    bool     // 是否成功加载了 error.lua
    errorDescCache sync.Map // uint64 -> string 永久缓存
}
```

编译时接口断言：`var _ Adapter = (*LuaAdapter)(nil)`

### 4.2 LState 池

- **池大小**：默认 CPU 核心数，可通过配置 `adapterPoolSize` 调整
- **实现**：有界 channel 池（`chan *lua.LState`），容量 = poolSize
- **获取超时**：30 秒（`lstateAcquireTimeout`），超时返回 nil
- **溢出释放**：池满时直接关闭多余 LState（防止泄漏）

```go
const lstateAcquireTimeout = 30 * time.Second

func (a *LuaAdapter) acquire() *lua.LState {
    select {
    case L := <-a.states:
        return L
    case <-time.After(lstateAcquireTimeout):
        return nil  // 超时
    }
}

func (a *LuaAdapter) release(L *lua.LState) {
    select {
    case a.states <- L:
    default:
        L.Close()  // 池满，关闭溢出 LState
    }
}
```

### 4.3 初始化流程

```
NewLuaAdapter(poolSize, scriptPath, errorMapPath)
    |
    +-- Step 1: 编译脚本
    |       tmpL.LoadFile(scriptPath) -> FunctionProto
    |
    +-- Step 2: 预创建 LState 池
    |       for i in 0..poolSize:
    |           newLState() -> 注册 bit + zlib 模块
    |           initLState() -> 执行脚本 + 缓存函数到 registry
    |
    +-- Step 3: 缓存元信息
    |       acquire() -> cacheMetaInfo() -> release()
    |       调用 header_size() 和 body_length() 一次
    |
    +-- Step 4: 可选加载 error.lua
            遍历所有 LState，加载 describe_error 函数
```

**Step 1 -- 编译脚本**：

使用临时 LState 编译 `codec.lua`，得到 `FunctionProto`（字节码）。后续每个池中 LState 通过 `NewFunctionFromProto` 创建函数，避免重复编译。

**Step 2 -- 预创建 LState 池**：

每个 LState 执行以下初始化：
1. `lua.NewState()` 创建空白 LState
2. `PreloadModule("bit", LoadBitModule)` 注册 bit 模块
3. `RegisterZlibModule(L)` 注册 zlib 模块
4. 从预编译的 FunctionProto 创建函数并执行脚本
5. 从全局表查找 7 个必需函数，缓存到 Lua registry（`__adapter_*` 前缀）
6. 清理全局函数名（置为 nil，防篡改）

初始化失败时清理已创建的 LState（`closeAll()`）。

**Step 3 -- 缓存元信息**：

调用 `cacheMetaInfo(L)` 一次性获取并缓存：
- `header_size()` -> `a.headerSize`
- `body_length()` -> `a.bodyLenInfo`（`BodyLengthInfo` 结构体）

**Step 4 -- 可选加载 error.lua**：

如果提供了 `errorMapPath`：
1. 读取 error.lua 文件内容
2. 在每个 LState 中执行 `DoString`
3. 从全局表查找 `describe_error` 函数
4. 缓存到 registry（`__adapter_describe_error`），清理全局名

### 4.4 Lua 函数注册表

每个 LState 中缓存以下 7 个必需函数（registry 前缀 `__adapter_`）：

| Lua 函数名 | registry 键 | 用途 |
|------------|-------------|------|
| `header_size` | `__adapter_header_size` | 返回消息头大小（仅初始化时调用） |
| `body_length` | `__adapter_body_length` | 返回 body 长度元信息（仅初始化时调用） |
| `encode_tcp` | `__adapter_encode_tcp` | TCP 消息编码 |
| `encode_udp` | `__adapter_encode_udp` | UDP 消息编码 |
| `decode_tcp` | `__adapter_decode_tcp` | TCP 消息解码 |
| `decode_udp` | `__adapter_decode_udp` | UDP 消息解码 |
| `expected_route_key` | `__adapter_expected_route_key` | 路由键计算 |

可选函数：

| Lua 函数名 | registry 键 | 用途 |
|------------|-------------|------|
| `describe_error` | `__adapter_describe_error` | 错误码描述映射 |

**与计划的差异**：计划中 Lua 函数名为 `encode`、`decode`、`expected_response_key`。实际代码中为 `encode_tcp`、`decode_tcp`、`decode_udp`、`expected_route_key`。函数名分离是因为实际代码将 TCP/UDP 编解码分为独立函数。

### 4.5 方法实现详解

**HeaderSize()**：

```go
func (a *LuaAdapter) HeaderSize() int { return a.headerSize }
```

直接返回缓存值，零 Lua 调用。

**BodyLength(headerData)**：

```go
func (a *LuaAdapter) BodyLength(headerData []byte) int {
    return ReadBodyLength(headerData, a.bodyLenInfo, a.headerSize)
}
```

委托给纯 Go 的 `ReadBodyLength`，零 Lua 调用。

**EncodeTCP(route, body, secretKey)**：

```go
func (a *LuaAdapter) EncodeTCP(route any, body []byte, secretKey []byte) []byte {
    return a.encode("__adapter_encode_tcp", route, body, secretKey)
}
```

通用 `encode` 方法：
1. 从池中 acquire LState
2. 从 registry 获取函数
3. 调用 `RouteToLuaValue(L, route)` 转换路由参数
4. 转换 body 和 secretKey 为 Lua 字符串（空值用 LNil）
5. `CallByParam` 调用 Lua 函数（NRet=1, Protect=true）
6. 读取返回值，Pop 栈，归还 LState
7. 调用失败时打印错误日志并返回 nil

**EncodeUDP(route, body, secretKey)**：

与 EncodeTCP 结构完全相同，调用 `__adapter_encode_udp`。

**DecodeTCP(data, secretKey)** / **DecodeUDP(data, secretKey)**：

```go
func (a *LuaAdapter) DecodeTCP(data []byte, secretKey []byte) (string, []byte, uint64) {
    return a.decode("__adapter_decode_tcp", data, secretKey)
}
```

通用 `decode` 方法：
1. acquire LState
2. 从 registry 获取函数
3. 转换 data 和 secretKey 为 Lua 值
4. `CallByParam`（NRet=3, Protect=true）
5. 按栈顺序读取：`headerErr(uint64)`, `body([]byte)`, `routeKey(string)`
6. Pop 3 个返回值，归还 LState
7. 调用失败时返回 `("", nil, 0)`

**ExpectedRouteKey(route)**：

1. acquire LState
2. 从 registry 获取 `__adapter_expected_route_key`
3. 转换 route 为 Lua 值
4. 调用 Lua 函数（NRet=1）
5. 返回字符串结果

**DescribeError(code)**：

1. 检查 `hasErrorMap` 标志
2. 查 `errorDescCache`（sync.Map）缓存
3. 缓存未命中时调用 `callDescribeError`
4. 结果（含空字符串）永久缓存

**Close()**：

```go
func (a *LuaAdapter) Close() { a.closeAll() }
```

`closeAll` 循环从 channel 中取出所有 LState 并 Close。

## 5. BodyLengthInfo -- 消息体长度元信息

`adapter/helpers.go` 定义。

### 5.1 结构

```go
type BodyLengthInfo struct {
    Offset         int    // header 中 body length 字段的字节偏移
    FieldType      string // "uint32_le" / "uint32_be" / "uint16_le" / "uint16_be"
    IncludesHeader bool   // length 值是否包含 header 自身大小
}
```

### 5.2 ReadBodyLength 纯 Go 实现

```go
func ReadBodyLength(headerData []byte, info BodyLengthInfo, headerSize int) int
```

支持 4 种字段类型：
- `uint32_le`：小端 32 位无符号整数
- `uint32_be`：大端 32 位无符号整数
- `uint16_le`：小端 16 位无符号整数
- `uint16_be`：大端 16 位无符号整数

处理逻辑：
1. 按 FieldType 从 headerData 的 Offset 位置读取原始值
2. 如果 `IncludesHeader == true`，减去 headerSize
3. 结果 < 0 时置为 0
4. headerData 长度不足时返回 0

## 6. RouteToLuaValue -- 类型转换

`adapter/helpers.go`

```go
func RouteToLuaValue(L *lua.LState, route any) lua.LValue
```

将 Go 的 `route any` 转换为 Lua 值。转换规则：

| Go 类型 | Lua 类型 | 说明 |
|---------|----------|------|
| `nil` | `LNil` | 无路由（如密钥交换） |
| `map[string]any` | `LTable` | 递归转换嵌套结构 |
| `float64`（整数值） | `LNumber(int64)` | 避免 "3.0" 格式问题 |
| `float64`（非整数） | `LNumber(float64)` | 保留小数 |
| `string` | `LString` | 直接转换 |
| `bool` | `LBool` | 直接转换 |
| `int` | `LNumber` | 直接转换 |
| `int64` | `LNumber` | 直接转换 |
| 其他 | `LString(fmt.Sprintf)` | 兜底格式化 |

**关键细节**：JSON 中的数值反序列化为 `float64`，整数值统一转为 `int64` 以保证路由键字符串一致（`"3:1"` 而非 `"3.0:1.0"`）。

## 7. Lua 辅助模块

### 7.1 bit 模块

`adapter/helpers.go` -- `LoadBitModule(L *lua.LState) int`

Lua 5.1 不支持位运算符，通过此模块提供 7 个位运算函数：

| 函数 | 签名 | 说明 |
|------|------|------|
| `bxor(a, b)` | `a ^ b` | 按位异或 |
| `band(a, b)` | `a & b` | 按位与 |
| `bor(a, b)` | `a \| b` | 按位或 |
| `bnot(a)` | `^a` | 按位取反 |
| `lshift(a, n)` | `a << n` | 左移 |
| `rshift(a, n)` | `int(uint(a) >> n)` | 右移（无符号） |
| `rol(a, n)` | 循环左移 8 位 | 字节旋转 |

### 7.2 zlib 模块

`adapter/lua_zlib.go` -- `RegisterZlibModule(L *lua.LState)`

使用 Go 标准库 `compress/gzip` 封装为 Lua 模块。codec.lua 通过 `local zlib = require("zlib")` 加载。

| 函数 | 签名 | 说明 |
|------|------|------|
| `zlib.compress(data)` | `string, nil/string` | GZIP 压缩（RFC 1952 格式） |
| `zlib.decompress(data)` | `string, nil/string` | GZIP 解压 |

错误时返回 `(nil, error_string)`。

**与计划的差异**：计划中使用 `compress/zlib`（zlib 格式），实际代码使用 `compress/gzip`（gzip 格式，RFC 1952），与服务器协议一致。

## 8. Network 层

### 8.1 消息模型

`network/message.go`

```go
type Message struct {
    RouteKey  string // 路由键字符串（由 adapter.Decode 产生）
    Data      []byte // 解码后的消息体字节
    HeaderErr uint64 // 协议头错误码，0 表示无错误
}
```

**与计划的差异**：计划中 `headerErr` 类型为 `uint16`，实际代码为 `uint64`，支持更大的错误码空间。

### 8.2 消息模型变化（字符串键 vs 整数键）

**旧设计**（已移除）：

```
responseMap map[int]chan *Message   // int(cmdAct) 组合整数作为键
listenResp  map[int]ListenCallBack  // 同上
```

**新设计**（当前）：

```
responseMap map[string]chan *Message   // 字符串路由键（如 "3:1"）
listenResp  map[string]ListenCallBack  // 同上
listenMsg   map[string]*Message        // 缓存消息（轮询模式）
```

路由键格式由 codec.lua 的 `decode_tcp/udp` 函数确定，典型格式为 `"{cmd}:{act}"`（如 `"3:1"`）。Go 引擎不假设任何特定格式。

### 8.3 Connection 结构体

`network/connection.go`

```go
type Connection struct {
    serviceName string          // 所属服务名
    robotName   string          // 所属机器人账号名
    secretKey   []byte          // 通信加密密钥

    responseMap      map[string]chan *Message  // routeKey -> 临时响应通道
    listenResp       map[string]ListenCallBack // routeKey -> 持久回调
    listenMsg        map[string]*Message       // routeKey -> 缓存消息
    listenCh         chan *Message              // 推送消息分发通道（buffer 128）
    listenDone       chan struct{}              // listenLoop 退出信号
    mu               sync.Mutex
    ctx              context.Context
    cancel           context.CancelFunc
    isClose          int32                      // 原子标记
    intentionalClose int32                      // 原子标记
    listenRunning    int32                      // 原子标记
    requestTimeout   time.Duration
    sendFunc         func(data []byte) error    // 由 Dialer 注入
    closeFunc        func() error               // 由 Dialer 注入
    heartbeat        *heartbeatState
    heartbeatMu      sync.Mutex
    onDisconnect     func()                     // 意外断开回调
    onClosed         func()                     // 关闭回调
}
```

### 8.4 关键方法

**NewConnection(serviceName, robotName, requestTimeout)**：

创建连接实例，初始化所有 map 和 channel，创建 context。不立即建立网络连接（网络连接由 Dialer 延迟建立）。

**RequestResponse(sendData, routeKey, timeoutOverride...)**：

1. 创建 buffered(1) channel
2. 注册到 `responseMap[routeKey]`
3. 调用 `Send(sendData)` 发送请求
4. select 等待：ctx.Done / ch 收到响应 / 超时
5. defer 清理 responseMap 条目和关闭 channel
6. 超时时返回 `NewTimeoutError(ErrRecvTimeout)`
7. ctx 取消时返回 `NewActionError(ErrConnDropped)`

**OnReceive(routeKey, body, headerErr)**：

由 gnet 层调用：
1. 检查 `isClose` 标记
2. `headerErr != 0` 时打印 Error 日志
3. 创建 `Message{RouteKey, Data, HeaderErr}`
4. 加锁查找 `responseMap[routeKey]`：
   - 找到 -> 非阻塞发送到 channel -> 返回
5. 查找 `listenResp[routeKey]`：
   - 找到 -> 非阻塞发送到 `listenCh` -> 返回
6. 都不匹配 -> 解锁，消息丢弃

**AddListener(routeKey, cb)**：

动态添加单个监听器。如果 `listenRunning == 0`，启动 `listenLoop` goroutine。

**ListenResponse(listenRespMap)**：

批量注册持久化推送监听（`map[string]ListenCallBack`）。如果 `listenRunning == 0`，启动 `listenLoop` goroutine。

**listenLoop()**：

独立 goroutine，从 `listenCh` 读取消息并分发：
- 查找 `listenResp[routeKey]` 对应的回调
- 回调为 nil -> 缓存到 `listenMsg`（轮询模式）
- 回调非 nil -> 直接调用回调函数
- ctx 取消时清空所有监听映射并退出

**GetListenResp(routeKey)**：

轮询获取缓存的监听消息。找到后从 `listenMsg` 中删除（一次性消费）。

**Close() / onClose()**：

双路径关闭机制：
- `Close()`：主动关闭，CAS 设置 `isClose=1`，设置 `intentionalClose=1`，调用 `closeFunc`，触发 `onClosed`，不触发 `onDisconnect`
- `onClose()`：被动关闭（gnet OnClose），CAS 设置 `isClose=1`，调用 `doClose`，触发 `onDisconnect` + `onClosed`
- 两者通过 CAS 互斥，保证 `doClose` 和回调只执行一次

### 8.5 Client 结构体

`network/client.go`

```go
type Client struct {
    name           string                  // 机器人账号名
    tcpConn        map[string]*Connection  // TCP 连接池
    udpConn        map[string]*Connection  // UDP 连接池
    requestTimeout time.Duration
    mu             sync.RWMutex
}
```

管理多个命名连接（TCP/UDP 分离）。同一服务名不能重复连接。

关键方法：
- `ConnectTCP/UDP(serviceName) bool` -- 创建连接占位
- `GetTCPConn/UDPConn(serviceName) *Connection` -- 获取连接
- `CloseTCP/UDP(serviceName)` -- 关闭并移除连接
- `CloseAll()` -- 关闭所有连接并等待 listenLoop 退出

### 8.6 gnet EventServer

`network/gnet.go`

```go
type EventServer struct {
    gnet.BuiltinEventEngine
    registry     *connRegistry     // gnet FD -> Connection 映射
    adp          adapter.Adapter   // 协议适配器
    tickInterval time.Duration     // 心跳定时器间隔
}
```

**connRegistry**：

```go
type connRegistry struct {
    mu      sync.RWMutex
    connMap map[int]*Connection  // gnet.Fd() -> Connection
}
```

管理 gnet 连接与业务层 Connection 的映射。

**OnTraffic（热路径）**：

帧分割核心逻辑：

```go
func (es *EventServer) OnTraffic(gconn gnet.Conn) gnet.Action {
    headSize := es.adp.HeaderSize()       // 纯 Go，缓存值

    conn := es.registry.get(gconn)

    for {
        // 1. 检查是否有足够字节读取 header
        available := gconn.InboundBuffered()
        if available < headSize { return gnet.None }

        // 2. 读取 header，解析 body 长度
        headBuf, _ := gconn.Peek(headSize)
        bodyLen := es.adp.BodyLength(headBuf)  // 纯 Go，缓存逻辑

        // 3. 非法 header 或包体过大 -> 关闭连接
        if bodyLen < 0 || bodyLen > maxBodyLen { return gnet.Close }

        // 4. 等待完整帧
        totalLen := headSize + bodyLen
        if available < totalLen { return gnet.None }

        // 5. 读取完整帧，统计带宽
        msgBuf := make([]byte, totalLen)
        gconn.Read(msgBuf)
        monitor.Global().AddBandwidth(0, int64(totalLen))

        // 6. 解码（Lua 调用），分发
        if conn != nil {
            secretKey := conn.GetSecretKey()
            var routeKey string; var body []byte; var headerErr uint64
            if gconn.RemoteAddr().Network() == "udp" {
                routeKey, body, headerErr = es.adp.DecodeUDP(msgBuf, secretKey)
            } else {
                routeKey, body, headerErr = es.adp.DecodeTCP(msgBuf, secretKey)
            }
            if routeKey != "" {
                conn.OnReceive(routeKey, body, headerErr)
            }
        }
    }
}
```

**maxBodyLen = 16MB**，防止畸形/恶意包导致 OOM。

**OnClose**：

从 registry 移除连接，调用 `conn.onClose()` 触发断开回调。

**bindConn**：

将 gnet 连接的发送/关闭函数注入到业务层 Connection：

```go
func bindConn(gconn gnet.Conn, conn *Connection) {
    conn.sendFunc = func(data []byte) error { return gconn.AsyncWrite(data, nil) }
    conn.closeFunc = func() error { return gconn.Close() }
}
```

### 8.7 Dialer

```go
type Dialer struct {
    client *gnet.Client
    server *EventServer
}
```

**NewDialer(adp adapter.Adapter, heartbeatInterval time.Duration)**：

创建 EventServer 和 gnet.Client。gnet.Client 在 `Start()` 时创建。

**DialTCP(ctx, address, conn) (gnet.Conn, error)**：

1. 在 goroutine 中调用 `d.client.Dial("tcp", address)`
2. 使用 select 等待结果或 ctx 取消
3. ctx 取消时排空结果并关闭 gnet 连接（避免 fd 泄漏）
4. 成功时调用 `bindConn` 注入发送/关闭函数
5. 注册到 connRegistry

**DialUDP(address, conn) (gnet.Conn, error)**：

同步拨号（UDP 无需超时）。其余步骤同 TCP。

### 8.8 心跳机制

`network/heartbeat.go`

```go
type HeartbeatBuilder func() []byte
type HeartbeatConfig struct {
    Interval time.Duration
    Builder  HeartbeatBuilder
}
```

**RegisterHeartbeat(cfg)**：

1. 停止已有心跳（`StopHeartbeat`）
2. 创建 `heartbeatState`
3. 通过 `utils.GetWorkPool().GoWithStop` 启动心跳 goroutine

**runHeartbeat(hb, stopCh)**：

```go
for {
    select {
    case <-c.ctx.Done():   return    // 连接关闭
    case <-hb.stop:        return    // 被替换/停止
    case <-stopCh:         return    // 协程池停止
    case <-ticker.C:
        packet := hb.cfg.Builder()  // 调用 builder 生成完整包
        if packet == nil { continue }
        c.Send(packet)
    }
}
```

Builder 闭包在创建时捕获 `adapter` 引用，内部调用 `adapter.EncodeTCP/UDP` 编码心跳包。

**StopHeartbeat()**：

CAS 标记 + close(stop channel) + 等待 done channel（确保 goroutine 退出）。

## 9. Engine 层与 Adapter 的交互

### 9.1 ActionExecutor 与 Adapter

`engine/action.go` 中的 `ActionExecutor` 持有 `adapter.Adapter` 引用，在消息构建流水线中使用：

```go
type ActionExecutor struct {
    netSender NetSender
    store     *state.Store
    factory   *protox.Factory
    adp       adapter.Adapter
}
```

**发送流程**：
1. `buildBody(def)` -- 构建 protobuf 消息体
2. `adp.ExpectedRouteKey(def.Route)` -- 计算路由键
3. `adp.EncodeTCP/UDP(def.Route, body, secretKey)` -- 编码完整包
4. `netSender.TCPSend/UDPSend(service, packet)` -- 发送

**接收流程**：
- 解码在 gnet OnTraffic 中完成（`adapter.DecodeTCP/UDP`）
- 结果通过 `Connection.OnReceive(routeKey, body, headerErr)` 传递到业务层
- `ActionExecutor` 通过 `netSender.TCPRequest/UDPRequest` 获取已解码的 body

### 9.2 协议策略辅助方法

消除 TCP/UDP 代码重复：

```go
func (ae *ActionExecutor) protocolEncode(protocol string, route any, body, key []byte) []byte {
    if protocol == "udp" { return ae.adp.EncodeUDP(route, body, key) }
    return ae.adp.EncodeTCP(route, body, key)
}
```

类似方法：`protocolSend`, `protocolSecretKey`, `protocolRequest`, `protocolEnsureListener`, `protocolListenResp`。

### 9.3 headerErr 处理

`handleHeaderError` 统一处理非零 headerErr：

```go
func (ae *ActionExecutor) handleHeaderError(def *ActionDef, headerErr uint64, routeKey string, respBody []byte) *ActionError {
    ae.parseAndStoreResponse(def, respBody)        // 仍然解析响应体
    desc := ae.adp.DescribeError(headerErr)        // 可选：获取错误描述
    detail := "service=" + def.Service + " route=" + routeKey
    if desc != "" { detail = desc + ": " + detail }
    return NewServerError(headerErr, detail)
}
```

### 9.4 ListenRef 路由键计算

`robot/robot.go` 中 `RegisterListen` 方法：

```go
routeKey := h.robot.adp.ExpectedRouteKey(ref.Route)
```

运行时通过 adapter 计算实际路由键，而非使用硬编码的 cmd/act 组合。

## 10. Robot 层与 Adapter 的交互

### 10.1 Robot 结构体

```go
type Robot struct {
    // ...
    adp         adapter.Adapter        // 协议适配器
    mainService string                 // 主连接服务名
    // ...
}
```

### 10.2 ScriptContext

```go
type Context struct {
    RobotID   int
    Account   string
    Store     *state.Store
    Factory   *protox.Factory
    Adapter   adapter.Adapter  // 替代旧的 Protocol
    NetSender engine.NetSender
    Ctx       context.Context
    LuaMu     *sync.Mutex
}
```

### 10.3 netSenderAdapter

`robot/robot.go` 中的 `netSenderAdapter` 实现 `engine.NetSender` 接口，桥接 engine 层与 network 层。

关键方法实现：

**TCPRequest(service, packet, routeKey, timeout...)**：
```go
func (ns *netSenderAdapter) TCPRequest(...) ([]byte, uint64, error) {
    conn := ns.robot.client.GetTCPConn(service)
    resp, err := conn.RequestResponse(packet, routeKey, timeout...)
    return resp.Data, resp.HeaderErr, nil
}
```

**GetTCPListenResp(service, routeKey)**：
```go
func (ns *netSenderAdapter) GetTCPListenResp(...) ([]byte, uint64) {
    conn := ns.robot.client.GetTCPConn(service)
    msg := conn.GetListenResp(routeKey)
    return msg.Data, msg.HeaderErr
}
```

**密钥管理**：
- `GetTCPSecretKey(service)` -> `conn.GetSecretKey()`
- `SetTCPSecretKey(service, key)` -> `conn.SetSecretKey(key)`
- UDP 同理，但从 `udpConn` map 获取连接

### 10.4 连接建立

`Robot.ConnectTCP(serviceName, address)`：

1. `client.ConnectTCP(serviceName)` 创建连接占位
2. `dialer.DialTCP(ctx, address, conn)` 建立实际 gnet 连接
3. 设置 `onClosed` 回调（监控计数）
4. 设置 `onDisconnect` 回调：主连接断开时停止机器人

## 11. ListenRef 与监听注册

### 11.1 ListenRef 结构

```go
type ListenRef struct {
    Route  any    `json:"route"`  // 不透明路由
    Server string `json:"server"` // "tcp:logic" 或 "udp:udp" 格式
    Listen string `json:"listen"` // 监听定义名称，空 = 仅轮询
}
```

### 11.2 RegisterListen 流程

`robotActionHandler.RegisterListen(refs)`：

1. 按 `(proto, service)` 分组（通过 `parseServer` 解析 `"tcp:logic"` 格式）
2. 对每个 ref：
   - 调用 `adp.ExpectedRouteKey(ref.Route)` 计算路由键
   - `ref.Listen == ""` -> 注册 nil 回调（轮询模式）
   - `ref.Listen != ""` -> 查找 ListenDef，创建回调函数
3. 按组注册到对应 Connection：
   - `proto == "udp"` -> `client.GetUDPConn(service)`
   - `proto == "tcp"` -> `client.GetTCPConn(service)`
   - 调用 `conn.ListenResponse(listenMap)`

### 11.3 parseServer

解析 `"协议:服务名"` 格式：

```go
func parseServer(server string) (proto, service string, ok bool)
```

支持 `"tcp:xxx"` 和 `"udp:xxx"` 两种格式。空字符串或格式错误返回 `(nil, nil, false)`。

### 11.4 回调类型

**Lua 回调**（ListenDef.Script 非空）：
- 设置 ScriptContext（含 Adapter 引用）
- 调用 `luaPool.RunCallbackScript(L, script, msg.Data, cbDef.S2CProto)`
- 错误时记录 `RecordCallbackError`

**声明式回调**（ListenDef.S2CProto + Store 非空）：
- 解析推送消息 `factory.Parse(s2cProto, msg.Data)`
- 按 Store 映射写入 StateStore

**静默回调**（以上都不满足）：
- 返回 nil 回调函数，消息仅缓存供轮询

## 12. codec.lua 架构

### 12.1 协议规格（当前服务器）

- 头部大小：12 字节，小端序
- 字段布局：`[len:4][error:2][cmd:1][act:1][index:2][flags:1][bcc:1]`
- 编码链：GZIP 压缩 -> XOR 加密 -> BCC 校验写入
- 解码链：XOR 解密 -> GZIP 解压
- UDP 偏移：前 11 字节保持明文

### 12.2 链式可组合设计

每个处理步骤定义为独立 local 函数，通过 `encode_chain` / `decode_chain` 列表按序调用。切换协议时修改链配置即可。

编码链（按顺序执行）：
1. `step_gzip_encode` -- GZIP 压缩 body
2. `step_xor_encode` -- XOR 加密 body

解码链（按顺序执行）：
1. `step_xor_decode` -- XOR 解密 body
2. `step_gzip_decode` -- GZIP 解压 body

BCC 校验在编码链之后独立计算（`step_bcc_encode`），因为需要校验最终字节。

### 12.3 必需函数（7 个）

| 函数 | 签名 | 说明 |
|------|------|------|
| `header_size()` | `-> number` | 返回消息头固定字节数 |
| `body_length()` | `-> table` | 返回 `{offset, field_type, includes_header}` |
| `encode_tcp(route, body, secret_key)` | `-> string` | TCP 消息编码 |
| `encode_udp(route, body, secret_key)` | `-> string` | UDP 消息编码（含偏移处理） |
| `decode_tcp(data, secret_key)` | `-> string, string, number` | TCP 消息解码 -> (routeKey, body, headerErr) |
| `decode_udp(data, secret_key)` | `-> string, string, number` | UDP 消息解码 |
| `expected_route_key(route)` | `-> string` | 路由键计算 |

### 12.4 可选函数

| 函数 | 签名 | 说明 |
|------|------|------|
| `describe_error(code)` | `-> string` | 错误码映射描述 |

### 12.5 XOR 加密/解密

对 `data[encrypt_offset+1..end]` 用 key 循环 XOR。`encrypt_offset > 0` 时前 N 字节保持明文（UDP 场景）。

为避免 Lua 5.1 的 `table.unpack` 栈深度限制（约 8000 字节），分段拼接（每 256 字节一段）。

### 12.6 BCC 校验

BCC（`header[11]`）是对 header 前 11 字节 + body 逐字节 XOR 累加的校验字节。BCC 与 XOR 加密完全独立（加密密钥来自登录密钥交换）。

### 12.7 gopher-lua 约束

Lua 5.1 环境（gopher-lua）：
- 不支持 `string.pack` / `string.unpack`（Lua 5.3+ 特性）
- 所有字节操作使用 `string.byte` / `string.char` + `bit` 模块
- 字节写入：`write_uint8(n)` -> `string.char(bit.band(n, 0xFF))`
- 字节读取：`read_uint8(s, offset)` -> `string.byte(s, offset + 1)`

## 13. 已删除的旧架构文件

以下文件在重构中移除：

| 文件/目录 | 原因 |
|-----------|------|
| `network/protocol.go` | 编解码完全移入 Lua 适配器 |
| `network/middleware.go` | PacketContext/PacketMiddleware 类型不再需要 |
| `network/middleware_gzip.go` | GZIP 由 codec.lua 处理 |
| `network/middleware_registry.go` | 中间件注册系统删除 |
| `network/middleware_lua.go` | LuaMiddlewarePool 由 adapter.LuaAdapter 替代 |
| `conf/header.json` | 协议头定义完全移入 codec.lua |
| `conf/middlewares/` | 中间件脚本目录删除 |

## 14. 设计决策

### 14.1 热路径零 Lua

帧解析（HeaderSize + BodyLength）纯 Go 缓存，gnet OnTraffic 永不调用 Lua。这是通过初始化时从 Lua 获取元信息（`header_size()` 和 `body_length()`），缓存到 Go 结构体实现的。

**性能影响**：
- `HeaderSize()` -- 直接返回 int，约 1ns
- `BodyLength()` -- 一次 binary read + 条件判断，约 5ns
- `Decode()` -- Lua 调用，约 10-50us（每完整帧一次）

### 14.2 route 不透明化

Go 引擎不解析 cmd/act，完全由 Lua codec.lua 处理。`route` 类型为 `any`，在 Go 引擎中逐字传递。

好处：
- 支持任意路由格式（不限于 cmd:act）
- 不同协议的路由结构可以完全不同（如 `{msg_type, session_id}` 或 `{service_id, method_id}`）
- Go 引擎代码与具体协议解耦

### 14.3 字符串响应键

替代旧的整数 cmdAct，支持任意协议的键空间。格式由 codec.lua 的 `decode_tcp/udp` 和 `expected_route_key` 函数确定。

典型格式：`"{cmd}:{act}"` -> `"3:1"`，但也可以是 `"LOGIN_RESP"` 或 `"uuid-xxx"`。

### 14.4 TCP/UDP 解码分离

`DecodeTCP` 和 `DecodeUDP` 独立方法，允许对 TCP/UDP 使用不同的解码策略。当前 codec.lua 中两者共享核心逻辑，但接口层预留了差异化空间。

### 14.5 LState 池隔离

adapter 池的 LState 只注册 `bit` 和 `zlib` 模块，不注册业务 API（network/robot/proto 等模块）。业务脚本使用 per-robot 的独立 LState（通过 `script.RuntimePool`），两者严格隔离，避免状态污染。

## 15. 与计划的差异汇总

| 差异点 | 计划设计 | 实际代码 |
|--------|----------|----------|
| Decode 方法 | 单一 `Decode()` | 分离为 `DecodeTCP()` + `DecodeUDP()` |
| headerErr 类型 | `uint16` | `uint64` |
| Lua 函数名 | `encode`, `decode`, `expected_response_key` | `encode_tcp`, `encode_udp`, `decode_tcp`, `decode_udp`, `expected_route_key` |
| zlib 模块 | 使用 `compress/zlib` | 使用 `compress/gzip`（gzip 格式） |
| UDPEncryptOffset | 有接口方法 | 无，偏移量完全由 codec.lua 内部管理 |
| ExpectedResponseKey | `ExpectedResponseKey()` | `ExpectedRouteKey()`（方法名不同） |
| Close 方法 | 无 | 有 `Close()` |
| DescribeError 方法 | 无 | 有 `DescribeError(code uint64) string` |
| ListenRef 字段 | `ResponseKey string` + `Server string` + `Callback string` | `Route any` + `Server string`（`"proto:service"` 格式）+ `Listen string` |
| Server 字段格式 | 简单服务名 | `"tcp:logic"` 或 `"udp:udp"` 格式 |
| ActionDef 路由 | 有 `RespRoute` 字段 | 无 `RespRoute`（响应路由与发送路由相同，由 codec.lua 的 `expected_route_key` 处理） |
| acquire 超时 | 阻塞等待（无超时） | 30 秒超时返回 nil |
| release 溢出 | 未定义 | 池满时关闭 LState |
| error.lua 支持 | 未提及 | 有 `DescribeError` + `errorDescCache` 永久缓存 |
| NetSender 方法 | 约 12 个 | 21 个（TCP/UDP 分离，增加 headerErr 返回值） |
| Connection 字段 | 无 `intentionalClose` | 有 `intentionalClose` 原子标记区分主动/被动关闭 |
| Connection 回调 | 仅 `onDisconnect` | 有 `onDisconnect` + `onClosed` 双回调 |
| Client UDP | 单一 `udpConn *Connection` | `udpConn map[string]*Connection`（多 UDP 连接） |
| Dialer 方法签名 | `DialTCP(address, conn)` | `DialTCP(ctx, address, conn)`（支持 context 取消） |

## 16. 通用性边界说明

本次重构使 stressbot 成为对任意带二进制消息头的原始 TCP/UDP 游戏服务器（Protobuf 消息体）的通用压测工具。以下是已知的通用性边界：

| 维度 | 场景 | 支持情况 |
|------|--------|---------|
| 协议头 | 任意消息头格式（字段布局、长度字段位置、端序均可配置） | codec.lua 全权处理，完全支持 |
| 路由键 | 任意路由键格式（"3:1"、"LOGIN_RESP"、UUID 均可） | codec.lua decode 返回字符串，完全支持 |
| 加解密 | XOR / GZIP / BCC / 自定义对称加密 | codec.lua + bit + zlib，支持；AES 等需 Go 层额外注册 Lua 模块 |
| 消息体格式 | Protobuf（声明式 field binding / store 提取） | protox 层，完全支持 |
| 消息体格式 | JSON / MessagePack / 其他格式 | 不内置支持；c2sProto="" + Lua 手动构建 body 可绕过，但失去声明式 binding |
| 连接模式 | TCP 长连接 + UDP | gnet 原生支持，完全支持 |
| 连接模式 | WebSocket / TLS | 不支持，gnet 只处理原始 TCP 帧 |
| 连接模式 | HTTP 短连接 / 长轮询 | 不支持，超出本工具设计范围 |
| 服务发现 | 动态地址（auth HTTP 响应携带） | Lua 脚本提取后调用 network.connect_tcp，支持 |
| 服务发现 | 静态地址（直连） | Lua 脚本硬写或读 flow state，支持 |

**核心结论**：本工具针对原始 TCP/UDP + 自定义二进制协议头 + Protobuf 消息体的游戏服务器，做到开箱即用。切换服务器只需更换 `codec.lua`（协议头）+ `flow.json`（流程）+ `.proto`（消息定义），无需修改任何 Go 代码。

## 17. 新协议适配步骤

当需要接入另一台协议格式不同的服务器时：

### 17.1 编写新的 codec.lua

假设新服务器使用 8 字节头、无 GZIP、仅 XOR 加密：

```lua
-- conf/adapter/simple_codec.lua
local bit = require("bit")

local HEADER_SIZE = 8
local encode_chain = { step_xor_encode }
local decode_chain = { step_xor_decode }

function header_size()
    return HEADER_SIZE
end

function body_length()
    return { offset = 0, field_type = "uint32_le", includes_header = true }
end

function encode_tcp(route, body, secret_key)
    local cmd = route and math.floor(route.cmd or 0) or 0
    local act = route and math.floor(route.act or 0) or 0
    body = body or ""

    local ctx = { body = body, flags = 0, secret_key = secret_key, encrypt_offset = 0 }
    for _, step in ipairs(encode_chain) do step(ctx) end

    local total = HEADER_SIZE + #ctx.body
    return string.char(
        bit.band(total, 0xFF), bit.band(bit.rshift(total, 8), 0xFF),
        bit.band(bit.rshift(total, 16), 0xFF), bit.band(bit.rshift(total, 24), 0xFF),
        cmd, act, ctx.flags, 0
    ) .. ctx.body
end

-- encode_udp, decode_tcp, decode_udp, expected_route_key 类似实现
```

### 17.2 更新配置文件

修改 `config.json` 中的 `adapterScript` 路径：

```json
{
  "adapterScript": "conf/adapter/simple_codec.lua"
}
```

### 17.3 Go 层零改动

无需修改任何 Go 代码。Adapter 接口的抽象层确保所有协议差异由 codec.lua 处理。
