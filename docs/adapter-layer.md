# 协议适配层 -- 三层架构技术文档

## 1. 概述

stressbot 采用 **Network / Adapter / Business** 三层架构。生产运行时通过声明式 `*_codec.json` 加载协议编解码规则，由 `CodecResolver` 按 `<proto>:<service>` 连接串解析到对应的 `SchemaAdapter`（Go codec），使 Go 引擎协议无关，支持多连接使用不同协议编解码配置。

**消息体序列化格式**：本工具固定使用 Protobuf（通过 `protox` 动态加载 `.proto` 文件）。协议头、路由键、加解密和错误码描述由每个连接绑定的声明式 codec 负责；`codec.lua` / `LuaAdapter` 不再参与生产编解码，仅作为测试真值与历史迁移参考保留。

### 1.1 重构目标

将协议编解码知识从 Go 引擎硬编码中抽离为声明式 codec 配置。生产路径启动时扫描/加载 `*_codec.json`，构建 `CodecResolver`，每条 `<proto>:<service>` 连接使用独立的 `SchemaAdapter`。切换服务器或连接协议只需更换对应 codec 配置、`flow.json` 和 `.proto` 文件，无需修改业务 Go 代码。

### 1.2 强制约束

| 约束 | 说明 |
|------|------|
| 声明式 codec | 生产编解码由 `conf/adapter/*_codec.json` 描述，文件名规约 `<proto>_<service>_codec.json`，映射到运行时 server 串 `<proto>:<service>` |
| gnet 热路径 | `OnTraffic` 每包都调用，`BodyLength` 必须是纯 Go 操作；由 `SchemaAdapter`/`SchemaCodec` 的编译产物直接解析 |
| 连接级适配器 | `CodecResolver` 按 `<proto>:<service>` 显式解析适配器，无默认 fallback；缺映射由调用方 fail loud |
| TCP/UDP 解码独立 | Adapter 接口提供 `DecodeTCP` 和 `DecodeUDP` 两个独立方法，允许对 TCP/UDP 使用不同的解码策略 |

## 2. 三层架构

### 2.1 层次职责

```
┌─────────────────────────────────────┐
│ Business Layer（engine + robot）      │  流程执行、状态管理、动作调度
├─────────────────────────────────────┤
│ Adapter Layer（adapter 包）           │  协议帧编解码（CodecResolver + SchemaAdapter）
├─────────────────────────────────────┤
│ Network Layer（network 包 + gnet）    │  TCP/UDP 连接管理、收发、心跳
└─────────────────────────────────────┘
```

- **Business Layer**：流程引擎执行 flow.json 中定义的节点图，通过 `ActionDef` 声明式描述消息收发。不感知任何协议细节。
- **Adapter Layer**：通过 `CodecResolver` 将 `<proto>:<service>` 连接串解析为对应 `SchemaAdapter`。Go 引擎只调用 `Adapter` 接口方法，不感知具体协议格式。
- **Network Layer**：基于 gnet 的连接管理，负责 TCP/UDP 帧分割、请求-响应匹配、持久化监听、心跳。

### 2.2 包依赖图

```
cmd/agent  ->  robot/  ->  engine/  ->  state/
                          ->  protox/
                          ->  adapter/  ->  codec/
               ->  network/ ->  adapter/
               ->  script/  ->  adapter/
                           ->  engine/
```

`adapter/` 包依赖 `codec/` 和标准库组装生产 `SchemaAdapter`/`CodecResolver`；历史 `LuaAdapter` 文件仅供测试对拍与迁移参考使用，不在生产加载路径中。

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
  -> adapter.DecodeTCP/UDP(data, secretKey)（SchemaAdapter / Go codec）
  -> Connection.OnReceive(routeKey, body, headerErr)
  -> responseMap[routeKey] 或 listenResp[routeKey]
```

## 3. Adapter 接口

`adapter/adapter.go` 定义了协议适配器接口，共 10 个方法。

### 3.1 完整接口签名

```go
type Adapter interface {
    // ─── 帧分割（纯 Go 实现）────────────────────────────────────

    // HeaderSize 返回消息头固定字节数。
    HeaderSize() int

    // BodyLength 从消息头字节中解析消息体长度。
    // 此方法在 gnet 热路径中被每包调用，禁止进行任何阻塞操作。
    BodyLength(headerData []byte) int

    // ─── 编解码（SchemaAdapter / Go codec）──────────────────────

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

    // Close 释放适配器持有的资源（SchemaAdapter 为 no-op）。
    Close()

    // DescribeError 将服务端协议头错误码映射为可读描述。
    DescribeError(code uint64) string
}
```

### 3.2 方法分类

**热路径方法（纯 Go，零阻塞）**：

| 方法 | 返回值 | 用途 | 调用频率 |
|------|--------|------|----------|
| `HeaderSize()` | 缓存的固定 int | 帧头大小 | gnet OnTraffic 每帧 |
| `BodyLength(headerData)` | 纯 Go 计算 | 从帧头解析 body 长度 | gnet OnTraffic 每帧 |

这两个方法来自 `SchemaCodec` 编译产物，gnet 的 `OnTraffic`（每帧必调）不执行脚本、不做 I/O。

**SchemaAdapter 方法**：

| 方法 | 用途 | 调用时机 |
|------|------|----------|
| `EncodeTCP(route, body, secretKey)` | TCP 消息编码（加密+封帧） | 发送请求/消息时 |
| `EncodeUDP(route, body, secretKey)` | UDP 消息编码（加密+封帧） | 发送 UDP 消息时 |
| `DecodeTCP(data, secretKey)` | TCP 消息解码 | gnet OnTraffic 收到完整帧时 |
| `DecodeUDP(data, secretKey)` | UDP 消息解码 | gnet OnTraffic 收到完整帧时 |
| `ExpectedRouteKey(route)` | 将 route 转换为 routeKey | 请求-响应匹配、监听注册时 |
| `DescribeError(code)` | 错误码描述 | headerErr != 0 时 |
| `Close()` | 释放适配器资源（SchemaAdapter 为 no-op） | 程序退出时 |

### 3.3 route 不透明设计

`route` 参数类型为 `any`，Go 引擎不解析其内部结构，逐字传递给连接对应的 `SchemaAdapter`。典型格式：

```json
{"cmd": 1, "act": 1}
```

JSON 中的数值被 Go 反序列化为 `float64`，`SchemaCodec` 按 codec schema 中声明的路径/类型读取并编码，Go 引擎不直接解释业务路由字段。

### 3.4 与计划的差异

| 差异点 | 计划设计 | 实际代码 |
|--------|----------|----------|
| Decode 方法 | 单一 `Decode(data, secretKey)` 返回 `(string, []byte, uint16)` | 分离为 `DecodeTCP` 和 `DecodeUDP`，返回 `(string, []byte, uint64)` |
| headerErr 类型 | `uint16` | `uint64` |
| Close 方法 | 无 | 有 `Close()` |
| DescribeError 方法 | 无 | 有 `DescribeError(code uint64) string` |
| UDPEncryptOffset | 有 `UDPEncryptOffset() int` 方法 | 无，偏移量由每份 `*_codec.json` 的 codec schema 声明 |
| ExpectedResponseKey | `ExpectedResponseKey(route)` | `ExpectedRouteKey(route)`（方法名不同） |

## 4. 生产适配器实现（CodecResolver + SchemaAdapter）

生产运行时不再加载 `LuaAdapter` / `codec.lua`。启动流程通过 `adapter.InferCodecMap` 扫描 `conf/adapter/*_codec.json`，再由 `adapter.LoadCodecResolver(codecDir, codecs, "errors.json")` 为每个 `<proto>:<service>` 连接构建 `SchemaAdapter`。同一 codec 文件被多个连接引用时会编译一次并复用同一个无状态 Adapter 实例。

`adapter/lua_adapter.go` 保留为测试 oracle 与历史迁移参考：单元测试用它和 `codec.lua` 做字节级对拍，确认声明式 Go codec 与迁移前行为一致；生产 main/task runner 不应调用 `NewLuaAdapter`。

### 4.1 CodecResolver

```go
type CodecResolver interface {
    Resolve(server string) Adapter
}
```

`server` 串固定为 `"<proto>:<service>"`，例如 `"tcp:logic"`、`"tcp:battle"`、`"udp:battle"`。resolver 内部是显式 map：

- `Resolve(server)` 命中时返回该连接的 `Adapter`。
- 缺映射返回 nil，不做默认 codec fallback；调用方负责报错或按当前路径定义处理。
- 构造完成后 map 只读，并发 Resolve 无需加锁。

### 4.2 SchemaAdapter

```go
type SchemaAdapter struct {
    c *codec.SchemaCodec
}
```

`SchemaAdapter` 是 `codec.SchemaCodec` 的 `adapter.Adapter` 薄包装：

- `HeaderSize` / `BodyLength` / `EncodeTCP` / `EncodeUDP` / `DecodeTCP` / `DecodeUDP` / `ExpectedRouteKey` / `DescribeError` 均委托给编译后的 `SchemaCodec`。
- 编译产物无可变状态，任意 goroutine 并发调用不需要加锁。
- `Close()` 是幂等 no-op。

### 4.3 加载流程

```
InferCodecMap(codecDir)
    |
    +-- 扫描 *_codec.json
    +-- 按文件名 <proto>_<service>_codec.json 推断 server 串 <proto>:<service>

LoadCodecResolver(codecDir, codecs, errorsFile)
    |
    +-- 可选加载共享 errors.json（codec.LoadErrorMap）
    +-- 按 server 串稳定排序遍历 codecs map
    +-- 每份 codec.LoadSchema(file)
    +-- NewSchemaAdapter(schema, errorMap)
    +-- 同一文件名 dedup，多个 server 复用同一 Adapter
    +-- NewCodecResolver(byServer)
```

**失败策略**：codec 映射为空、目录不可读、文件名无法解析、codec 文件缺失/解析失败/校验失败、`errors.json` 非空但加载失败，均返回中文 error，启动期 fail loud。

### 4.4 codec schema 方法映射

每份 `*_codec.json` 编译为不可变 `SchemaCodec` 后，`SchemaAdapter` 对外暴露统一 Adapter 接口：

| Adapter 方法 | SchemaCodec 行为 | 用途 |
|--------------|------------------|------|
| `HeaderSize()` | 返回 schema 中声明的 header 大小 | gnet 帧切割 |
| `BodyLength(headerData)` | 按 schema 声明的 offset/type/includesHeader 纯 Go 解析 | gnet 帧切割 |
| `EncodeTCP(route, body, secretKey)` | 按 TCP encode schema 写头、压缩/加密/校验、拼包 | TCP 发送 |
| `EncodeUDP(route, body, secretKey)` | 按 UDP encode schema 写头、偏移加密、拼包 | UDP 发送 |
| `DecodeTCP(data, secretKey)` | 按 TCP decode schema 解析 headerErr/routeKey/body | TCP 收包 |
| `DecodeUDP(data, secretKey)` | 按 UDP decode schema 解析 headerErr/routeKey/body | UDP 收包 |
| `ExpectedRouteKey(route)` | 按 schema 的 routeKey 声明从发送 route 计算响应键 | 请求匹配、监听注册 |
| `DescribeError(code)` | 查询共享 `errors.json` 编译出的 code→desc map | headerErr 描述 |
| `Close()` | no-op | 接口一致性 |

### 4.5 LuaAdapter 的保留范围

`LuaAdapter` / `codec.lua` 当前只用于：

1. **测试 oracle**：`adapter/schema_adapter_test.go` 用旧 Lua 编码结果与 `SchemaAdapter` 字节级对拍。
2. **迁移审计**：`codec/migration_test.go` 用 `error.lua` 条目数/抽样描述校验迁移后的 `errors.json` 覆盖率。
3. **历史文档/回溯**：解释声明式 codec 的迁移来源。

生产路径使用 `LoadCodecResolver` + `SchemaAdapter`，不得以 `NewLuaAdapter` 作为运行时兜底。

## 5. BodyLength 元信息 -- 消息体长度解析

声明式 codec schema 中定义。

### 5.1 结构

```go
{
  "frame": {
    "headerSize": 12,
    "bodyLength": {
      "offset": 0,
      "type": "uint32_le",
      "includesHeader": true
    }
  }
}
```

### 5.2 BodyLength 纯 Go 实现

`codec.SchemaCodec.BodyLength(headerData)` 在编译后的 schema 上执行，不调用 Lua、不访问网络、不分配业务对象。

支持 4 种字段类型：
- `uint32_le`：小端 32 位无符号整数
- `uint32_be`：大端 32 位无符号整数
- `uint16_le`：小端 16 位无符号整数
- `uint16_be`：大端 16 位无符号整数

处理逻辑：
1. 按 schema `type` 从 headerData 的 `offset` 位置读取原始值
2. 如果 `includesHeader == true`，减去 headerSize
3. 结果 < 0 时置为 0
4. headerData 长度不足时返回 0

## 6. Route 取值与类型转换

`route any` 来自 flow.json 的 action/listenRef 配置，Go 引擎保持不透明传递。`SchemaCodec` 根据 codec schema 中的字段路径读取 route 值，并在写 header 或计算 routeKey 时按声明类型转换。

关键规则：

| 输入来源 | 处理方式 | 说明 |
|----------|----------|------|
| `nil` | 按 schema 默认/空路由处理 | 如密钥交换等无路由请求 |
| `map[string]any` | 按 path 读取字段 | 支持嵌套路径 |
| JSON number (`float64`) | 按目标字段类型转整数/浮点 | 避免由 Go 引擎硬编码 cmd/act |
| `string` / `bool` | 按 schema 声明转换 | 转换失败在 codec 编译/执行路径报错 |

**关键细节**：路由键格式由每份 codec schema 的 routeKey 规则决定，典型格式仍可为 `"3:1"`，但也可以是其他协议需要的字符串。

## 7. codec 算法模块

生产 codec 的压缩、加密、校验等步骤由 `codec/` 包的算法注册表提供，schema 只声明步骤和参数，运行时调用 Go 实现。

当前迁移后的生产 codec 覆盖：

| 能力 | 说明 |
|------|------|
| GZIP | 使用 Go 标准库 `compress/gzip`，与服务器协议的 RFC 1952 格式一致 |
| XOR | 支持按 offset 部分加解密，UDP 可保留前 N 字节明文 |
| BCC | 对 header/body 执行协议要求的异或校验 |
| header 读写 | 按 schema 声明的 offset/type 写入或读取字段 |

Lua 侧 `bit` / `zlib` 模块仍随 `LuaAdapter` 文件保留，仅用于测试 oracle 运行旧 `codec.lua`。

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

路由键格式由连接对应的 codec schema 确定，典型格式为 `"{cmd}:{act}"`（如 `"3:1"`）。Go 引擎不假设任何特定格式。

### 8.3 Connection 结构体

`network/connection.go`

```go
type Connection struct {
    serviceName string          // 所属服务名
    robotName   string          // 所属机器人账号名
    secretKey   []byte          // 通信加密密钥

    responseMap      map[string]chan *Message  // routeKey -> 临时响应通道
    listenResp       map[string]ListenCallBack // routeKey -> 持久化推送回调
    listenQueues     map[string]*listenQueue   // routeKey -> 缓存队列（轮询模式）
    mu               sync.Mutex
    ctx              context.Context
    cancel           context.CancelFunc
    isClose          int32                      // 原子标记
    intentionalClose int32                      // 原子标记
    requestTimeout   time.Duration
    sendFunc         func(data []byte) error    // 由 Dialer 注入
    closeFunc        func() error               // 由 Dialer 注入
    onDisconnect     func()                     // 意外断开回调
    onClosed         func()                     // 关闭回调
    adp              adapter.Adapter            // pump 解码用 SchemaAdapter
    inboundCh        chan inboundFrame          // raw frame 输入队列
    controlCh        chan pumpCmd               // pump 控制命令（心跳/停止）
    pumpDone         chan struct{}              // connectionPump 退出信号
    hbMu             sync.Mutex
    hb               *heartbeatRuntime          // pump 持有的心跳 runtime
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
6. 超时时返回 `NewActionError(ErrRecvTimeout, ...)`
7. ctx 取消时返回 `NewActionError(ErrConnDropped)`

**OnReceive(routeKey, body, headerErr)**：

当前由 connectionPump 消费 inbound frame 后完成解码与分发：
1. 检查 `isClose` 标记
2. `headerErr != 0` 时记录协议头错误
3. 创建 `Message{RouteKey, Data, HeaderErr}`
4. 加锁查找 `responseMap[routeKey]`：
   - 找到 -> 非阻塞发送到 channel -> 返回
5. 查找 `listenResp[routeKey]`：
   - 找到 -> 写入对应监听队列；回调非 nil 时执行 Go-store 回调 -> 返回
6. 都不匹配 -> 消息丢弃

**RegisterListen(routeKey, cb, queueSize)**：

注册单个持久化推送监听，预创建 routeKey 对应的缓存队列。重复注册同 routeKey 会更新回调和队列容量，供主流程轮询消费。

**GetListenResp(routeKey)**：

轮询获取缓存的监听消息。找到后从 `listenQueues[routeKey]` 中弹出一条消息。

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
- `CloseAll()` -- 关闭所有连接并等待 connectionPump 退出

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

        // 6. 解码（SchemaAdapter / Go codec），分发
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

1. 校验连接未关闭、`Interval > 0` 且 `Builder` 非空
2. 通过 `controlCh` 投递心跳配置给 connectionPump
3. pump 串行替换旧心跳、重置 timer，并在 timer 到期时调用 builder 生成完整包

```go
case <-hb.timer.C:
    packet := hb.cfg.Builder()
    if packet != nil {
        c.Send(packet)
    }
    resetHeartbeatTimerLocked()
```

Builder 闭包由 Robot 建连后根据 codec 的 `heartbeat` 配置创建，内部调用 `adapter.EncodeTCP/UDP` 编码心跳包。

**StopHeartbeat()**：

投递停止命令给 pump；连接关闭时 pump 的退出路径也会停止 timer。

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

Lua 的 `network.tcp_request_route` / `network.udp_request_route` 只在第 2 步改用 `responseRoute` 计算等待响应的 routeKey；编码发送仍使用 `requestRoute`。底层 `Connection.RequestResponse` 本来就按传入的 routeKey 注册 `responseMap`，不需要额外兼容逻辑。

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
func (ae *ActionExecutor) handleHeaderError(proto string, def *ActionDef, headerErr uint64, routeKey string, respBody []byte) *ActionError {
    ae.parseAndStoreResponse(def, respBody)        // 尽量解析响应体
    desc := ae.describeError(proto, def.Service, headerErr) // 可选：获取错误描述
    detail := "service=" + def.Service + " route=" + routeKey
    if desc != "" { detail = desc + ": " + detail }
    return NewActionError(errcode.ErrorCode(headerErr), detail)
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
    RobotID int
    Index   int
    Account string
    Store   *state.Store
    Factory *protox.Factory
    Resolver  adapter.CodecResolver
    NetSender engine.NetSender
    Ctx       context.Context
    Shared    sharedstate.Store
    DefaultRequestTimeout time.Duration
    TimingLevel           int
}
```

`Context` 不再持有旧的 adapter 指针或脚本互斥锁。业务 Lua API 需要编解码时通过 `Resolver.Resolve("<proto>:<service>")` 获取当前连接的 `SchemaAdapter`；Lua 脚本仍运行在该 Robot 独占的脚本运行时中。阻塞型 Lua API（例如 `network.tcp_request`、`network.udp_request`、listen 轮询）只阻塞当前 Robot 的主流程，不会阻塞 codec 编解码或其他 Robot。

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
   - 通过 `resolver.Resolve(ref.Server)` 获取该连接的 SchemaAdapter
   - 调用 `adapter.ExpectedRouteKey(ref.Route)` 计算路由键
   - `ref.Listen == ""` -> 注册 nil 回调（轮询模式）
   - `ref.Listen != ""` -> 查找 ListenDef，创建 Go-store 回调函数；未找到时按 nil 回调注册，仅缓存供轮询
3. 按组注册到对应 Connection：
   - `proto == "udp"` -> `client.GetUDPConn(service)`
   - `proto == "tcp"` -> `client.GetTCPConn(service)`
   - 调用 `conn.RegisterListen(routeKey, cb, queueSize)`

### 11.3 parseServer

解析 `"协议:服务名"` 格式：

```go
func parseServer(server string) (proto, service string, ok bool)
```

支持 `"tcp:xxx"` 和 `"udp:xxx"` 两种格式。空字符串或格式错误返回 `(nil, nil, false)`。

### 11.4 回调类型

**已废弃脚本回调**（ListenDef.Script 非空）：
- listen 脚本回调已移除
- 注册阶段返回配置错误，要求改为主流程轮询或 Go-store 回调

**Go-store 回调**（ListenDef.S2CProto + Store 非空）：
- 解析推送消息 `factory.Parse(s2cProto, msg.Data)`
- 按 Store 映射写入 StateStore
- 成功/失败通过 `RecordCallback` 计入监控

**静默回调**（以上都不满足）：
- 返回 nil 回调函数，消息仅缓存供轮询

## 12. 声明式 codec schema 架构

### 12.1 协议规格（当前服务器）

生产配置位于 `conf/adapter/*_codec.json`，文件名按 `<proto>_<service>_codec.json` 映射为 `<proto>:<service>` 连接：

- `tcp_logic_codec.json` -> `tcp:logic`
- `tcp_battle_codec.json` -> `tcp:battle`
- `udp_battle_codec.json` -> `udp:battle`

当前协议头大小 12 字节，小端序，字段布局为 `[len:4][error:2][cmd:1][act:1][index:2][flags:1][bcc:1]`。codec schema 通过 `header` 数组声明字段 offset/size/type/role，通过 `pipeline` 声明 GZIP、XOR、BCC 等处理步骤。

### 12.2 可组合 pipeline

每个处理步骤由 JSON 对象声明，运行时编译为 Go codec 步骤。当前生产 schema 使用：

1. `compress` / `gzip`：按阈值压缩 body，并写入 compressed flag。
2. `encrypt` / `xor_carry_rol`：按 key 和 offset 加密 body，并产出 BCC 校验值。
3. header `checksumOut` 字段：把 pipeline 产出的 BCC 写入 header。

UDP schema 可独立声明 encode/decode offset，例如保留前 N 字节明文供服务端查密钥表。

### 12.3 routeKey 与错误码

- `routeKeyTemplate` 定义解码后和请求匹配使用的字符串键，例如 `"{cmd}:{act}"`。
- header 中 `role: "errorCode"` 的字段作为 headerErr 返回。
- `errors.json` 是共享错误码描述表，由 `LoadCodecResolver` 加载后注入所有 `SchemaAdapter`。

## 13. 历史迁移产物

以下 Lua 相关文件/概念已退出生产编解码路径，仅作为迁移参考或测试 oracle 保留：

| 文件/概念 | 当前状态 |
|-----------|----------|
| `adapter/lua_adapter.go` | 仅测试对拍/历史迁移参考，不作为生产 Adapter 加载 |
| `conf/adapter/codec.lua` | 旧实现真值，用于确认声明式 codec 字节级一致 |
| `conf/adapter/error.lua` | 旧错误码真值，迁移后由 `errors.json` 承载生产映射 |
| `network/protocol.go` / middleware 旧架构 | 已移除，协议处理收敛到 Adapter 接口 |

## 14. 设计决策

### 14.1 热路径纯 Go

帧解析（HeaderSize + BodyLength）由 `SchemaCodec` 编译产物直接执行，gnet OnTraffic 不调用脚本、不阻塞。

**性能影响**：
- `HeaderSize()` -- 直接返回 int，约 1ns
- `BodyLength()` -- 一次 binary read + 条件判断，约 5ns
- `DecodeTCP/DecodeUDP()` -- Go codec pipeline 执行，每完整帧一次

### 14.2 route 不透明化

Go 引擎不解析 cmd/act。`route` 类型为 `any`，在 Go 引擎中逐字传递，具体字段读取和 routeKey 生成由连接对应的 codec schema 决定。

好处：
- 支持任意路由格式（不限于 cmd:act）
- 不同连接的路由结构可以不同
- Go 引擎代码与具体协议解耦

### 14.3 字符串响应键

替代旧的整数 cmdAct，支持任意协议的键空间。格式由 codec schema 的 `routeKeyTemplate` 决定。

典型格式：`"{cmd}:{act}"` -> `"3:1"`，但也可以扩展为协议需要的其他字符串。

### 14.4 TCP/UDP 解码分离

`DecodeTCP` 和 `DecodeUDP` 独立方法，允许 TCP/UDP 使用不同 schema 和 pipeline。当前通过 `<proto>:<service>` 连接级 resolver 选择对应 `SchemaAdapter`。

### 14.5 业务 Lua 与 codec 隔离

业务脚本仍使用 per-robot 的独立 LState（通过 `script.RuntimePool`）。codec 编解码不走业务 Lua；业务 Lua API 需要发包/收包时经 `Context.Resolver` 取对应连接的 `SchemaAdapter`，阻塞型网络 API 只阻塞当前 Robot 主流程。

## 15. 与计划的差异汇总

| 差异点 | 计划设计 | 当前代码 |
|--------|----------|----------|
| Decode 方法 | 单一 `Decode()` | 分离为 `DecodeTCP()` + `DecodeUDP()` |
| headerErr 类型 | `uint16` | `uint64` |
| 生产 codec | Lua 函数 `encode/decode/expected_response_key` | 声明式 `*_codec.json` + `SchemaAdapter` |
| UDPEncryptOffset | 有接口方法 | 无，偏移量由每份 codec schema 声明 |
| ExpectedResponseKey | `ExpectedResponseKey()` | `ExpectedRouteKey()`（方法名不同） |
| Close 方法 | 无 | 有 `Close()`；SchemaAdapter 为 no-op |
| DescribeError 方法 | 无 | 有 `DescribeError(code uint64) string`，描述来自 `errors.json` |
| ListenRef 字段 | `ResponseKey string` + `Server string` + `Callback string` | `Route any` + `Server string`（`"proto:service"` 格式）+ `Listen string` |
| Server 字段格式 | 简单服务名 | `"tcp:logic"` / `"tcp:battle"` / `"udp:battle"` 等 `<proto>:<service>` 格式 |
| ActionDef 路由 | 有 `RespRoute` 字段 | 无 `RespRoute`；响应路由由 `ExpectedRouteKey` 从发送 route 计算 |
| NetSender 方法 | 约 12 个 | 21 个（TCP/UDP 分离，增加 headerErr 返回值） |
| Connection 字段 | 无 `intentionalClose` | 有 `intentionalClose` 原子标记区分主动/被动关闭 |
| Connection 回调 | 仅 `onDisconnect` | 有 `onDisconnect` + `onClosed` 双回调 |
| Client UDP | 单一 `udpConn *Connection` | `udpConn map[string]*Connection`（多 UDP 连接） |
| Dialer 方法签名 | `DialTCP(address, conn)` | `DialTCP(ctx, address, conn)`（支持 context 取消） |

## 16. 通用性边界说明

本次重构使 stressbot 成为对任意带二进制消息头的原始 TCP/UDP 游戏服务器（Protobuf 消息体）的通用压测工具。以下是已知的通用性边界：

| 维度 | 场景 | 支持情况 |
|------|--------|---------|
| 协议头 | 任意消息头格式（字段布局、长度字段位置、端序均可配置） | 声明式 codec schema 处理，支持 |
| 路由键 | 任意路由键格式（"3:1"、"LOGIN_RESP"、UUID 等） | 由 `routeKeyTemplate` / schema 规则生成，支持 |
| 加解密 | XOR / GZIP / BCC 等已注册算法 | schema 声明 pipeline，Go codec 执行；新算法需在 codec 包注册 |
| 消息体格式 | Protobuf（声明式 field binding / store 提取） | protox 层支持 |
| 消息体格式 | JSON / MessagePack / 其他格式 | 不内置为声明式主路径；复杂行为仍可在业务 Lua 中实现 |
| 连接模式 | TCP 长连接 + UDP | gnet 原生支持 |
| 连接模式 | WebSocket / TLS | 不支持，gnet 只处理原始 TCP 帧 |
| 连接模式 | HTTP 短连接 / 长轮询 | 不属于 adapter 层 TCP/UDP 帧协议范围 |
| 服务发现 | 动态地址（auth HTTP 响应携带） | 业务 Lua 提取后调用 network.connect_tcp，支持 |
| 服务发现 | 静态地址（直连） | 业务 Lua 或 flow state 配置，支持 |

**核心结论**：本工具针对原始 TCP/UDP + 自定义二进制协议头 + Protobuf 消息体的游戏服务器，做到开箱即用。切换服务器只需更换对应 `*_codec.json`、`flow.json` 和 `.proto` 文件，无需修改业务 Go 代码。

## 17. 新协议适配步骤

当需要接入另一台协议格式不同的服务器时：

### 17.1 编写新的 *_codec.json

新增或替换 `conf/adapter/<proto>_<service>_codec.json`，声明：

- `frame.headerSize`、长度字段和 header 字段布局
- `routeKeyTemplate`
- TCP/UDP 所需 pipeline（压缩、加密、校验等）
- header 字段的 source/role（length、route、errorCode、flags、checksumOut 等）

### 17.2 更新流程和资源

1. flow 中的 `service` 与 listenRef 的 `server` 使用对应 `<proto>:<service>`。
2. `.proto` 文件继续放在 proto 目录供 `protox` 动态加载。
3. 如需错误码描述，更新 `conf/adapter/errors.json`。

### 17.3 Go 层零改动

无需修改业务 Go 代码。`InferCodecMap` / `LoadCodecResolver` 会按文件名规约加载 codec，Adapter 接口的抽象层确保协议差异由 schema 承载。
