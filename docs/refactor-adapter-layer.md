# 三层架构重构：Network / Adapter / Business — 详细实施方案

## 1. 重构目标与设计约束

### 1.1 目标

将协议编解码知识完全移入用户编写的 Lua 适配器脚本，Go 引擎保持协议无关，从而实现对任意消息头协议的游戏服务器压测支持。

**消息体序列化格式**：本工具固定使用 **Protobuf**（通过 `protox` 动态加载 `.proto` 文件）。消息体序列化层不在本次通用化范围内——对于非 Protobuf 服务器，用户通过 Lua 脚本手动构建 body 字节即可（`c2sProto` 留空时 `buildBody` 返回 nil，Lua 负责全权构建）。将来如有必要，可引入 `BodySerializer` 接口在不改动现有代码的前提下扩展。

### 1.2 强制约束（实施前必读）

| 约束 | 说明 |
|------|------|
| **Lua 版本** | 项目使用 `gopher-lua`，实现 **Lua 5.1**，**不支持** `string.pack` / `string.unpack`（Lua 5.3+ 特性）。所有 codec.lua 中的字节操作必须用 `string.byte` / `string.char` + `bit` 模块实现 |
| **gnet 热路径** | `OnTraffic` 每包都调用，`BodyLength` 必须是纯 Go 操作；通过初始化时从 Lua 缓存元信息实现，禁止每包调用 Lua |
| **LuaAdapter 池隔离** | adapter 池 LState 只注册 `bit` 和 `zlib` 模块，不注册业务 API；script 层继续使用 per-robot `r.L`，两者严格隔离 |
| **BCC 与 XOR 独立** | BCC（header[11]）是对 header 前 11 字节 + body 逐字节 XOR 累加的校验字节，与 XOR 加密**完全无关**；XOR 加密密钥来自登录密钥交换所得的 secretKey，两步骤在链中独立执行 |
| **UDP 编码独立方法** | `Adapter` 接口提供 `EncodeTCP`（TCP）和 `EncodeUDP` 两个方法，后者内部应用 `UDP_ENC_OFFSET`（前 N 字节明文供服务端查密钥表）；Go 层无需知道具体偏移值，`UDPEncryptOffset()` 从接口移除 |
| **本次范围** | 不包含 `FilterDef` 新字段扩展、消息体序列化通用化（均与本次重构无关）|

---

## 2. 整体架构变化

### 2.1 旧架构

```
flow.json(cmd/act) → engine/ActionExecutor → protocol.BuildPacket(cmd,act,body,key)
                                            → NetSender.TCPRequest(svc, cmd, act, pkt)
header.json → Protocol → middleware链(bcc/xor/gzip) → Connection.OnReceive(HeadDecode, body)
                                                     ↑
                                          responseMap[int(cmdAct)]
```

### 2.2 新架构

```
flow.json(route any) → engine/ActionExecutor → adapter.EncodeTCP(route, body, key)
                                              → NetSender.TCPRequest(svc, pkt, respKey)
conf/adapter/codec.lua → LuaAdapter → Connection.OnReceive(responseKey string, body)
                                     ↑
                          responseMap[string(responseKey)]
```

### 2.3 包依赖图（新增 adapter/ 包）

```
cmd/agent  →  robot/  →  engine/  →  state/
                          →  protox/
                          →  adapter/  ←  (gopher-lua only)
               →  network/ →  adapter/
               →  script/  →  adapter/
                           →  engine/
```

`adapter/` 包只依赖 gopher-lua，不依赖项目内其他包，无循环依赖风险。

---

## 3. Phase 1：创建 `adapter/` 包

### 3.1 `adapter/adapter.go` — 接口定义

```go
package adapter

// Adapter 协议适配器接口。
// 所有消息编解码、帧分割、路由键提取都通过此接口。
// Go 引擎只调用此接口，不感知具体协议格式。
type Adapter interface {
    // ─── 帧分割（纯 Go 实现，无 Lua 调用）───────────────────────────────
    
    // HeaderSize 返回消息头固定字节数。
    // 初始化时从 Lua 缓存，运行时无 Lua 调用。
    HeaderSize() int
    
    // BodyLength 从消息头字节中解析消息体长度。
    // 初始化时从 Lua 获取字段偏移/类型元信息后，在 Go 层原生实现。
    // 此方法在 gnet 热路径中被每包调用，禁止进行任何 Lua 调用。
    BodyLength(headerData []byte) int
    
    // ─── 编解码（Lua 调用）──────────────────────────────────────────────
    
    // EncodeTCP 将路由信息+消息体编码为完整 TCP 数据包（含消息头）。
    // route 为不透明类型，由 flow.json 中声明，原样传给 Lua。
    // secretKey 为连接加密密钥，nil 表示不加密。
    // route 为 nil 时，适配器应视为"无路由请求"（如密钥交换 cmd=0,act=0）。
    EncodeTCP(route any, body []byte, secretKey []byte) []byte

    // EncodeUDP 将路由信息+消息体编码为 UDP 数据包。
    // 与 EncodeTCP 的区别：内部应用 UDP 加密偏移量（前 N 字节保持明文，
    // 供服务端通过明文头部查找密钥表）。偏移值由 codec.lua 内部定义，Go 层无需知晓。
    // route 为 nil 时行为同 EncodeTCP。
    EncodeUDP(route any, body []byte, secretKey []byte) []byte
    
    // Decode 将完整数据包解码为路由键、消息体和协议头错误码。
    // responseKey 是字符串路由键，用于请求-响应匹配和监听分发。
    // 格式由适配器决定，典型格式："{cmd}:{act}"，如 "3:1"。
    // headerErr 为协议头中的错误码字段（无该字段的协议返回 0）。
    // 非零时 Connection.OnReceive 记录告警，仍继续路由（让请求正常完成）。
    // TCP 和 UDP 使用同一 Decode（接收侧无偏移问题）。
    Decode(data []byte, secretKey []byte) (responseKey string, body []byte, headerErr uint16)
    
    // ExpectedResponseKey 从发送路由计算期望的响应路由键。
    // 用于 TCPRequest 等待响应时注册临时通道。
    ExpectedResponseKey(route any) string
}
```

### 3.2 `adapter/helpers.go` — 辅助函数

从 `network/middleware_lua.go` 迁移并扩展：

```go
package adapter

import (
    "encoding/binary"
    "math"
    lua "github.com/yuin/gopher-lua"
)

// BodyLengthInfo 消息体长度字段元信息。
// 初始化时从 Lua 适配器脚本的 body_length() 函数获取并缓存。
type BodyLengthInfo struct {
    Offset         int    // header 中 body length 字段的字节偏移
    FieldType      string // 字段类型："uint32_le"/"uint16_le"/"uint32_be"/"uint16_be"
    IncludesHeader bool   // length 值是否包含 header 自身大小（true 则减去 HeaderSize）
}

// ReadBodyLength 从 header 字节中原生读取 body 长度。
// 纯 Go 实现，无任何 Lua 调用。
func ReadBodyLength(headerData []byte, info BodyLengthInfo, headerSize int) int {
    if len(headerData) < info.Offset+4 {
        return 0
    }
    var raw uint32
    switch info.FieldType {
    case "uint32_le":
        raw = binary.LittleEndian.Uint32(headerData[info.Offset:])
    case "uint32_be":
        raw = binary.BigEndian.Uint32(headerData[info.Offset:])
    case "uint16_le":
        raw = uint32(binary.LittleEndian.Uint16(headerData[info.Offset:]))
    case "uint16_be":
        raw = uint32(binary.BigEndian.Uint16(headerData[info.Offset:]))
    default:
        raw = binary.LittleEndian.Uint32(headerData[info.Offset:])
    }
    n := int(raw)
    if info.IncludesHeader {
        n -= headerSize
        if n < 0 {
            n = 0
        }
    }
    return n
}

// RouteToLuaValue 将 Go 的 route any 转换为 Lua 值。
// JSON 中的数值反序列化为 float64，整数值统一转为 int 以保证路由键字符串一致。
// 转换规则：
//   - nil              → LNil
//   - map[string]any   → LTable（递归转换）
//   - float64（整数值）→ LNumber（整数，避免 "3.0" 格式问题）
//   - float64（非整数）→ LNumber（浮点）
//   - string           → LString
//   - bool             → LBool
//   - 其他             → LString（fmt.Sprintf）
func RouteToLuaValue(L *lua.LState, route any) lua.LValue {
    if route == nil {
        return lua.LNil
    }
    switch v := route.(type) {
    case map[string]any:
        tbl := L.NewTable()
        for k, val := range v {
            L.SetField(tbl, k, RouteToLuaValue(L, val))
        }
        return tbl
    case float64:
        // 整数化：避免 "3.0:1.0" 与 "3:1" 不匹配
        if v == math.Trunc(v) && !math.IsInf(v, 0) {
            return lua.LNumber(int64(v))
        }
        return lua.LNumber(v)
    case string:
        return lua.LString(v)
    case bool:
        return lua.LBool(v)
    case int:
        return lua.LNumber(v)
    case int64:
        return lua.LNumber(v)
    default:
        return lua.LString(fmt.Sprintf("%v", v))
    }
}

// LoadBitModule 注册 bit 模块到 LState（从 middleware_lua.go 迁移）。
// Lua 5.1 不支持位运算符，通过此模块提供位运算原语。
func LoadBitModule(L *lua.LState) int {
    mod := L.NewTable()
    L.SetField(mod, "bxor", L.NewFunction(bitBxor))
    L.SetField(mod, "band", L.NewFunction(bitBand))
    L.SetField(mod, "bor", L.NewFunction(bitBor))
    L.SetField(mod, "bnot", L.NewFunction(bitBnot))
    L.SetField(mod, "lshift", L.NewFunction(bitLshift))
    L.SetField(mod, "rshift", L.NewFunction(bitRshift))
    L.SetField(mod, "rol", L.NewFunction(bitRol))
    L.Push(mod)
    return 1
}

// --- bit 模块内部函数（同 middleware_lua.go，迁移过来） ---
// bitBxor / bitBand / bitBor / bitBnot / bitLshift / bitRshift / bitRol
// 实现略（与现有 middleware_lua.go 完全一致）
```

### 3.2b `adapter/lua_zlib.go` — Lua zlib 模块实现

**新增文件**，将 Go 标准库 `compress/zlib` 封装为 Lua 模块，供 `newLState()` 调用 `RegisterZlibModule(L)` 注册。

```go
package adapter

import (
    "bytes"
    "compress/zlib"
    "io"

    lua "github.com/yuin/gopher-lua"
)

// RegisterZlibModule 向 LState 预加载 zlib Lua 模块。
// codec.lua 通过 local zlib = require("zlib") 加载。
func RegisterZlibModule(L *lua.LState) {
    L.PreloadModule("zlib", func(L *lua.LState) int {
        mod := L.NewTable()
        L.SetField(mod, "compress",   L.NewFunction(luaZlibCompress))
        L.SetField(mod, "decompress", L.NewFunction(luaZlibDecompress))
        L.Push(mod)
        return 1
    })
}

// luaZlibCompress：Lua zlib.compress(data) → compressed_string
func luaZlibCompress(L *lua.LState) int {
    data := []byte(L.CheckString(1))
    var buf bytes.Buffer
    w := zlib.NewWriter(&buf)
    if _, err := w.Write(data); err != nil {
        L.Push(lua.LNil)
        L.Push(lua.LString(err.Error()))
        return 2
    }
    w.Close()
    L.Push(lua.LString(buf.String()))
    return 1
}

// luaZlibDecompress：Lua zlib.decompress(data) → original_string
func luaZlibDecompress(L *lua.LState) int {
    r, err := zlib.NewReader(bytes.NewReader([]byte(L.CheckString(1))))
    if err != nil {
        L.Push(lua.LNil)
        L.Push(lua.LString(err.Error()))
        return 2
    }
    defer r.Close()
    out, err := io.ReadAll(r)
    if err != nil {
        L.Push(lua.LNil)
        L.Push(lua.LString(err.Error()))
        return 2
    }
    L.Push(lua.LString(string(out)))
    return 1
}
```

`newLState()` 中调用 `RegisterZlibModule(L)` 即可（替代原 [注释 A] 内联代码）。

### 3.3 `adapter/lua_adapter.go` — Lua 桥接实现

**结构体定义：**

```go
package adapter

import (
    "fmt"
    lua "github.com/yuin/gopher-lua"
)

// LuaAdapter 通过 gopher-lua LState 池调用适配器脚本实现 Adapter 接口。
// 
// 热路径优化策略：
//   - headerSize / bodyLenInfo 在初始化时从 Lua 一次性获取并缓存到 Go 字段。
//   - BodyLength() 基于缓存的元信息在 Go 层原生实现，零 Lua 调用。
//   - HeaderSize() 直接返回缓存值。
//   - udpEncryptOffset 不再缓存到 Go 层，由 EncodeUDP 调用 codec.lua 中的 encode_udp 内部处理。
//   - EncodeTCP() / Decode() / ExpectedResponseKey() 从有界 channel 池获取 LState 执行 Lua，完成后归还。
type LuaAdapter struct {
    states      chan *lua.LState  // 有界 channel 池（容量 = poolSize）
    scriptProto *lua.FunctionProto // 预编译的适配器脚本

    // 初始化时缓存的元信息（热路径零 Lua 调用）
    headerSize  int            // 消息头大小，HeaderSize() 直接返回
    bodyLenInfo BodyLengthInfo // BodyLength() 纯 Go 计算，无需调 Lua
    // 注意：udpEncryptOffset 不在此缓存，由 encode_udp() Lua 函数内部维护
}

// NewLuaAdapter 创建并初始化 Lua 适配器池。
// scriptPath: codec.lua 路径；poolSize: LState 池大小（建议 = CPU 核心数）。
func NewLuaAdapter(poolSize int, scriptPath string) (*LuaAdapter, error) {
    if poolSize <= 0 {
        poolSize = runtime.NumCPU()
    }
    
    // Step 1: 编译脚本
    tmpL := lua.NewState()
    defer tmpL.Close()
    fn, err := tmpL.LoadFile(scriptPath)
    if err != nil {
        return nil, fmt.Errorf("编译适配器脚本 %s 失败: %w", scriptPath, err)
    }
    proto := fn.Proto

    adp := &LuaAdapter{
        states:      make(chan *lua.LState, poolSize),
        scriptProto: proto,
    }
    
    // Step 2: 预创建 LState 池，每个 LState 执行一次脚本注册函数
    for i := 0; i < poolSize; i++ {
        L := adp.newLState()
        if err := adp.initLState(L); err != nil {
            return nil, fmt.Errorf("初始化 LState[%d] 失败: %w", i, err)
        }
        adp.states <- L
    }
    
    // Step 3: 初始化时获取元信息并缓存到 Go 字段
    L := adp.acquire()
    defer adp.release(L)
    
    if err := adp.cacheMetaInfo(L); err != nil {
        return nil, fmt.Errorf("获取适配器元信息失败: %w", err)
    }
    
    return adp, nil
}

// newLState 创建已注册 bit 模块的 LState
func (a *LuaAdapter) newLState() *lua.LState {
    L := lua.NewState()
    L.PreloadModule("bit", LoadBitModule)
    return L
}

// initLState 在 LState 中执行适配器脚本，将函数缓存到 registry。
// 每个 LState 独立执行一次，函数保存在各自的 registry 中。
func (a *LuaAdapter) initLState(L *lua.LState) error {
    fn := L.NewFunctionFromProto(a.scriptProto)
    L.Push(fn)
    if err := L.PCall(0, 0, nil); err != nil {
        return fmt.Errorf("执行适配器脚本失败: %w", err)
    }
    
    // 从全局变量缓存到 registry，避免每次从全局表查找
    // encode_udp：UDP 包编码（内部应用偏移量，Go 层不感知具体偏移值）
    fnNames := []string{
        "header_size", "body_length", "encode", "encode_udp",
        "decode", "expected_response_key",
    }
    reg := L.Get(lua.RegistryIndex)
    for _, name := range fnNames {
        fn := L.GetGlobal(name)
        if fn == lua.LNil {
            return fmt.Errorf("适配器脚本缺少必需函数: %s()", name)
        }
        L.SetField(reg, "__adapter_"+name, fn)
        L.SetGlobal(name, lua.LNil) // 清理全局名字
    }
    return nil
}

// cacheMetaInfo 调用 Lua 的元信息函数，缓存到 Go 结构体字段。
// 仅在 NewLuaAdapter 初始化时调用一次。
// 注意：udp_encrypt_offset 不再缓存到 Go 层，UDP 编码偏移由 EncodeUDP/encode_udp 内部处理。
func (a *LuaAdapter) cacheMetaInfo(L *lua.LState) error {
    reg := L.Get(lua.RegistryIndex)
    
    // header_size()
    fn := L.GetField(reg, "__adapter_header_size")
    if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}); err != nil {
        return fmt.Errorf("调用 header_size() 失败: %w", err)
    }
    a.headerSize = int(lua.LVAsNumber(L.Get(-1)))
    L.Pop(1)
    
    // body_length() → table { offset, field_type, includes_header }
    fn = L.GetField(reg, "__adapter_body_length")
    if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}); err != nil {
        return fmt.Errorf("调用 body_length() 失败: %w", err)
    }
    tbl, ok := L.Get(-1).(*lua.LTable)
    L.Pop(1)
    if !ok {
        return fmt.Errorf("body_length() 必须返回 table")
    }
    a.bodyLenInfo = BodyLengthInfo{
        Offset:         int(lua.LVAsNumber(tbl.RawGetString("offset"))),
        FieldType:      string(lua.LVAsString(tbl.RawGetString("field_type"))),
        IncludesHeader: lua.LVAsBool(tbl.RawGetString("includes_header")),
    }
    
    return nil
}

// ─── Adapter 接口实现 ────────────────────────────────────────────────────────

// HeaderSize 返回消息头大小（零 Lua 调用）
func (a *LuaAdapter) HeaderSize() int { return a.headerSize }

// BodyLength 纯 Go 实现，使用缓存的元信息（零 Lua 调用）。
// gnet 热路径调用此方法，不产生任何 Lua 开销。
func (a *LuaAdapter) BodyLength(headerData []byte) int {
    return ReadBodyLength(headerData, a.bodyLenInfo, a.headerSize)
}

// EncodeTCP 调用 Lua encode(route, body, secret_key) 函数，用于 TCP 包编码。
func (a *LuaAdapter) EncodeTCP(route any, body []byte, secretKey []byte) []byte {
    L := a.acquire()
    defer a.release(L)
    
    reg := L.Get(lua.RegistryIndex)
    fn := L.GetField(reg, "__adapter_encode")
    
    routeVal := RouteToLuaValue(L, route)
    
    var bodyVal lua.LValue = lua.LNil
    if len(body) > 0 {
        bodyVal = lua.LString(string(body))
    }
    
    var keyVal lua.LValue = lua.LNil
    if len(secretKey) > 0 {
        keyVal = lua.LString(string(secretKey))
    }
    
    if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, routeVal, bodyVal, keyVal); err != nil {
        stresslog.Error("[ADAPTER] encode() 调用失败", zap.Error(err))
        return nil
    }
    
    result := []byte(lua.LVAsString(L.Get(-1)))
    L.Pop(1)
    return result
}

// Decode 调用 Lua decode(data, secret_key) 函数，返回 responseKey + body + headerErr。
// Lua decode() 须返回 3 个值：responseKey(string), body(string), headerErr(number)。
// 对于协议头无错误字段的服务器，Lua 返回第 3 值为 0 即可。
func (a *LuaAdapter) Decode(data []byte, secretKey []byte) (string, []byte, uint16) {
    L := a.acquire()
    defer a.release(L)
    
    reg := L.Get(lua.RegistryIndex)
    fn := L.GetField(reg, "__adapter_decode")
    
    dataVal := lua.LString(string(data))
    
    var keyVal lua.LValue = lua.LNil
    if len(secretKey) > 0 {
        keyVal = lua.LString(string(secretKey))
    }
    
    if err := L.CallByParam(lua.P{Fn: fn, NRet: 3, Protect: true}, dataVal, keyVal); err != nil {
        stresslog.Error("[ADAPTER] decode() 调用失败", zap.Error(err))
        return "", nil, 0
    }
    
    headerErr := uint16(lua.LVAsNumber(L.Get(-1)))
    body       := []byte(lua.LVAsString(L.Get(-2)))
    responseKey := lua.LVAsString(L.Get(-3))
    L.Pop(3)
    return responseKey, body, headerErr
}

// EncodeUDP 调用 Lua encode_udp(route, body, secret_key) 函数，用于 UDP 包编码。
// codec.lua 的 encode_udp 与 encode 内部共享逻辑，区别仅在 encrypt_offset 不同。
func (a *LuaAdapter) EncodeUDP(route any, body []byte, secretKey []byte) []byte {
    L := a.acquire()
    defer a.release(L)
    
    reg := L.Get(lua.RegistryIndex)
    fn := L.GetField(reg, "__adapter_encode_udp")
    
    routeVal := RouteToLuaValue(L, route)
    var bodyVal lua.LValue = lua.LNil
    if len(body) > 0 { bodyVal = lua.LString(string(body)) }
    var keyVal lua.LValue = lua.LNil
    if len(secretKey) > 0 { keyVal = lua.LString(string(secretKey)) }
    
    if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, routeVal, bodyVal, keyVal); err != nil {
        stresslog.Error("[ADAPTER] encode_udp() 调用失败", zap.Error(err))
        return nil
    }
    result := []byte(lua.LVAsString(L.Get(-1)))
    L.Pop(1)
    return result
}

// ExpectedResponseKey 调用 Lua expected_response_key(route) 函数。
func (a *LuaAdapter) ExpectedResponseKey(route any) string {
    L := a.acquire()
    defer a.release(L)
    
    reg := L.Get(lua.RegistryIndex)
    fn := L.GetField(reg, "__adapter_expected_response_key")
    
    routeVal := RouteToLuaValue(L, route)
    
    if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, routeVal); err != nil {
        stresslog.Error("[ADAPTER] expected_response_key() 调用失败", zap.Error(err))
        return ""
    }
    
    key := lua.LVAsString(L.Get(-1))
    L.Pop(1)
    return key
}

// acquire 从池中获取 LState（阻塞等待）
func (a *LuaAdapter) acquire() *lua.LState { return <-a.states }

// release 将 LState 归还到池中
func (a *LuaAdapter) release(L *lua.LState) { a.states <- L }
```

---

## 4. Phase 2：改造 `engine/` 层

### 4.1 `engine/flow.go` — 结构体变更

**ListenRef：删除 Cmd/Act，新增 ResponseKey**

```go
// ListenRef 监听回调引用。
// ResponseKey 是直接的响应路由键字符串（如 "3:1"），由 codec.lua 的 decode 函数产生。
// 语义：直接监听，无需通过 ExpectedResponseKey 推导，避免歧义。
type ListenRef struct {
    ResponseKey string `json:"responseKey"` // 直接响应路由键（替代旧的 cmd/act）
    Server      string `json:"server"`      // 服务名（如 "logic"）
    Callback    string `json:"callback"`    // 回调定义名称
}
```

> **向后兼容**：在 `nodeRaw` 中保留 `Cmd`/`Act` 字段的旧式别名解析，`UnmarshalJSON` 时若 `responseKey` 为空则用 `"{cmd}:{act}"` 自动拼接，保持旧 flow.json 可用。

```go
// nodeRaw 中额外加入：
type listenRefRaw struct {
    ResponseKey string `json:"responseKey"`
    Cmd         uint8  `json:"cmd"`  // 旧式，兼容
    Act         uint8  `json:"act"`  // 旧式，兼容
    Server      string `json:"server"`
    Callback    string `json:"callback"`
}

// UnmarshalJSON 兼容处理：
func (r *ListenRef) UnmarshalJSON(data []byte) error {
    var raw listenRefRaw
    if err := json.Unmarshal(data, &raw); err != nil {
        return err
    }
    r.ResponseKey = raw.ResponseKey
    if r.ResponseKey == "" && (raw.Cmd != 0 || raw.Act != 0) {
        r.ResponseKey = fmt.Sprintf("%d:%d", raw.Cmd, raw.Act)
    }
    r.Server = raw.Server
    r.Callback = raw.Callback
    return nil
}
```

**ActionDef：删除 Cmd/Act/RespCmd/RespAct，新增 Route/RespRoute**

```go
type ActionDef struct {
    Pattern  string `json:"pattern"`
    Service  string `json:"service"`
    Route    any    `json:"route"`    // 不透明路由（替代 cmd/act），原样传给 adapter.EncodeTCP
    RespRoute any   `json:"respRoute"` // 可选响应路由（替代 respCmd/respAct），nil 时用 Route 推导
    // ...其余字段不变（Path, Script, Address, C2SProto, S2CProto, Bindings, Store 等）
}
```

> **向后兼容**：`ActionDef.UnmarshalJSON` 中，若 `route` 为 nil 且 `cmd` 非零，则自动构造 `map[string]any{"cmd": cmd, "act": act}` 作为 route。

```go
type actionDefRaw struct {
    Pattern   string  `json:"pattern"`
    Service   string  `json:"service"`
    Route     any     `json:"route"`
    RespRoute any     `json:"respRoute"`
    Cmd       uint8   `json:"cmd"`     // 旧式兼容
    Act       uint8   `json:"act"`     // 旧式兼容
    RespCmd   uint8   `json:"respCmd"` // 旧式兼容
    RespAct   uint8   `json:"respAct"` // 旧式兼容
    // ...
}

func (a *ActionDef) UnmarshalJSON(data []byte) error {
    var raw actionDefRaw
    json.Unmarshal(data, &raw)
    a.Route = raw.Route
    if a.Route == nil && (raw.Cmd != 0 || raw.Act != 0) {
        a.Route = map[string]any{"cmd": float64(raw.Cmd), "act": float64(raw.Act)}
    }
    a.RespRoute = raw.RespRoute
    if a.RespRoute == nil && (raw.RespCmd != 0 || raw.RespAct != 0) {
        a.RespRoute = map[string]any{"cmd": float64(raw.RespCmd), "act": float64(raw.RespAct)}
    }
    // ...赋值其余字段
}
```

### 4.2 `engine/action.go` — 接口和执行器变更

**删除 ProtocolEncoder 接口**，新增 `AdapterRef`：

```go
import "stressbot/adapter"

// ActionExecutor 声明式动作执行器
type ActionExecutor struct {
    netSender NetSender
    store     *state.Store
    factory   *protox.Factory
    adapter   adapter.Adapter // 替代 protocol ProtocolEncoder
}

func NewActionExecutor(store *state.Store, sender NetSender, factory *protox.Factory, adp adapter.Adapter) *ActionExecutor {
    return &ActionExecutor{netSender: sender, store: store, factory: factory, adapter: adp}
}
```

**NetSender 接口变更**（删除 cmd/act 参数，改用预编码包 + responseKey）：

```go
type NetSender interface {
    // TCP 发送预编码数据包（调用方已通过 adapter.EncodeTCP 构建完整字节）
    TCPSend(service string, packet []byte) (bool, int)
    
    // TCP 请求-响应：发送预编码包，等待指定 responseKey 的响应
    // responseKey 由 adapter.ExpectedResponseKey(route) 计算
    TCPRequest(service string, packet []byte, responseKey string) ([]byte, bool)
    
    // UDP 发送预编码数据包
    UDPSend(packet []byte) bool
    
    // 注册持久化监听（responseKey 替代 cmdAct int）
    EnsureListener(service string, responseKey string)
    GetListenResp(service string, responseKey string) []byte
    
    // 心跳：builder 闭包内部调用 adapter.EncodeTCP，签名不变
    RegisterHeartbeat(target, service string, intervalMs int, builder func() []byte)
    
    // 连接管理（不变）
    ConnectTCP(service, address string) bool
    ConnectUDP(address string) bool
    CloseTCP(service string)
    CloseUDP()
    
    // HTTP（不变）
    HTTPPost(path string, formData map[string]string) (statusCode int, body []byte, err error)
    
    // 密钥（不变）
    GetSecretKey(service string) []byte
    SetSecretKey(service string, key []byte)
    SetUDPSecretKey(key []byte)
    GetUDPSecretKey() []byte
}
```

**新增辅助函数 `computeRespKey`**：

```go
// computeRespKey 计算响应路由键。
// 若 def.RespRoute 非空，用它计算；否则用 def.Route。
// nil route 时 adapter.ExpectedResponseKey 返回适配器定义的默认值（如 "0:0"）。
func (ae *ActionExecutor) computeRespKey(def *ActionDef) string {
    route := def.RespRoute
    if route == nil {
        route = def.Route
    }
    return ae.adapter.ExpectedResponseKey(route)
}
```

**`buildBody` 函数**（替代旧 `buildPacket`，只构建消息体，编码由调用方完成）：

```go
// buildBody 构建消息体字节（序列化 proto 消息）。
// 与旧 buildPacket 的区别：不调用 protocol.BuildPacket，只返回 body bytes。
func (ae *ActionExecutor) buildBody(def *ActionDef) ([]byte, error) {
    if def.C2SProto == "" {
        return nil, nil
    }
    msg, err := ae.factory.Create(def.C2SProto)
    if err != nil {
        return nil, fmt.Errorf("创建 C2S 消息 %s 失败: %w", def.C2SProto, err)
    }
    if err := ae.bindFields(msg, def.Bindings); err != nil {
        return nil, err
    }
    return ae.factory.Serialize(msg)
}
```

**各 pattern 处理器变更概览**：

| Pattern | 旧编码方式 | 新编码方式 |
|---------|-----------|-----------|
| `tcpSend` | `protocol.BuildPacket(cmd,act,body,key)` → `TCPSend(svc,cmd,act,pkt)` | `buildBody()` → `adapter.EncodeTCP(route,body,key)` → `TCPSend(svc,pkt)` |
| `tcpRequest` | `buildPacket()` → `TCPRequest(svc,cmd,act,pkt)` | `buildBody()` → `EncodeTCP()` → `TCPRequest(svc,pkt,respKey)` |
| `exchangeKey` | `BuildPacket(0,0,nil,nil)` → `TCPRequest(svc,0,0,pkt)` | `buildBody(def)` → `EncodeTCP(def.Route,body,nil)` → `TCPRequest(svc,pkt,computeRespKey)` → `SetSecretKey`（route 为 nil 时 codec.lua 默认 cmd=0,act=0）|
| `udpSendProto` | `buildPacket()` → `UDPSendPacket(cmd,act,body)` | `buildBody()` → `EncodeUDP(route,body,udpKey)` → `UDPSend(pkt)` |
| `udpSendRaw` | `buildRawBody()` → `UDPSendPacket(cmd,act,body)` | `buildRawBody()` → `EncodeUDP(route,body,udpKey)` → `UDPSend(pkt)` |
| `waitListen` | `EnsureListener(svc,cmd,act)` + `GetListenResp(svc,cmdAct)` | `computeRespKey()` → `EnsureListener(svc,respKey)` + `GetListenResp(svc,respKey)` |
|| `registerHeartbeat` | builder 内调 `protocol.BuildPacketWithOffset(cmd,act,body,key,udpOffset)` | builder 内调 `adapter.EncodeTCP`（TCP）或 `adapter.EncodeUDP`（UDP），按 target 区分，偏移由适配器内部处理 |

**`udpSendProto` / `udpSendRaw` 中 UDP 加密偏移处理**：

```go
// 旧：UDPSendPacket 内部调 protocol.BuildPacketWithOffset(cmd, act, body, key, udpOffset)
// 新：UDPSend 接收完整包；由 adapter.EncodeTCP 内部根据 udp_encrypt_offset() 常量处理偏移
// Go 层无需关心具体偏移量。

// execUDPSendProto 示例：
func (ae *ActionExecutor) execUDPSendProto(def *ActionDef) error {
    body, err := ae.buildBody(def)
    if err != nil { return err }
    udpKey := ae.netSender.GetUDPSecretKey()
    // 使用 EncodeUDP 而非 EncodeTCP：前 N 字节明文（由 codec.lua 内部处理 UDP_ENC_OFFSET）
    packet := ae.adapter.EncodeUDP(def.Route, body, udpKey)
    // Lua encode 失败时返回 nil（如协议脚本错误），提前返回避免 UDPSend(nil)
    if packet == nil { return fmt.Errorf("adapter.EncodeUDP 返回 nil，检查 codec.lua") }
    ok := ae.netSender.UDPSend(packet)
    if !ok { return fmt.Errorf("UDP 发送失败") }
    return nil
}

// execTCPSend / execTCPRequest 中同样加 nil 检查（模式一致）：
// packet := ae.adapter.EncodeTCP(def.Route, body, key)
// if packet == nil { return fmt.Errorf("adapter.EncodeTCP 返回 nil，检查 codec.lua") }
```

---

## 5. Phase 3：改造 `network/` 层

### 5.1 `network/connection.go`

**结构体变更：**

```go
type Connection struct {
    serviceName string
    robotName   string
    secretKey   []byte
    
    // ─── 路由键类型：int → string ─────────────────────────────────────────
    responseMap map[string]chan *Message   // responseKey → 临时响应通道
    listenResp  map[string]ListenCallBack  // responseKey → 持久回调
    listenMsg   map[string]*Message        // responseKey → 缓存消息（轮询）
    listenCh    chan *Message
    
    mu              sync.Mutex
    ctx             context.Context
    cancel          context.CancelFunc
    isClose         int32
    listenRunning   int32
    requestTimeout  time.Duration          // 可配置超时（替代硬编码 1 分钟）
    sendFunc        func(data []byte) error
    closeFunc       func() error
    heartbeat       *heartbeatState
    heartbeatMu     sync.Mutex
}
```

**NewConnection 签名：**

```go
// NewConnection 创建网络连接
// requestTimeout：TCPRequest 等待响应的超时时间（由配置传入，替代硬编码 1 分钟）
func NewConnection(serviceName, robotName string, requestTimeout time.Duration) *Connection {
    // protocol 字段移除，连接不再持有协议编解码器
    // ...
}
```

**RequestResponse 变更：**

```go
// RequestResponse 发送请求并同步等待响应。
// responseKey 为字符串路由键（替代旧的 responseId int）。
func (c *Connection) RequestResponse(sendData []byte, responseKey string) (*Message, int) {
    // ...
    timeout := time.After(c.requestTimeout) // 使用可配置超时
    // 超时日志中显示 responseKey 而非 cmd/act
    stresslog.Warn("[NETWORK] RequestResponse 等待超时",
        zap.String("service", c.serviceName),
        zap.String("responseKey", responseKey),
        zap.String("robot", c.robotName))
    // ...
}
```

**AddListener / GetListenResp 签名变更：**

```go
func (c *Connection) AddListener(responseKey string, cb ListenCallBack)
func (c *Connection) GetListenResp(responseKey string) *Message
func (c *Connection) ListenResponse(listenRespMap map[string]ListenCallBack)
```

**OnReceive 签名变更：**

```go
// OnReceive 由 gnet 层调用，传入 adapter.Decode 已解析的路由键、消息体和协议头错误码。
// 不再接收 *HeadDecode（协议细节对 Connection 完全透明）。
// headerErr 非零时记录告警；仍继续路由，确保 TCPRequest 不因此超时。
func (c *Connection) OnReceive(responseKey string, body []byte, headerErr uint16) {
    if headerErr != 0 {
        stresslog.Warn("[NETWORK] 服务端协议头错误码非零",
            zap.String("service", c.serviceName),
            zap.String("key", responseKey),
            zap.Uint16("headerErr", headerErr))
    }
    resp := &Message{ResponseKey: responseKey, Data: body}
    
    c.mu.Lock()
    ch, exists := c.responseMap[responseKey]
    if exists {
        c.mu.Unlock()
        select {
        case ch <- resp:
        default:
            stresslog.Warn("[NETWORK] OnReceive 响应通道已满", zap.String("key", responseKey))
        }
        return
    }
    _, exists = c.listenResp[responseKey]
    if exists {
        c.mu.Unlock()
        select {
        case c.listenCh <- resp:
        default:
            stresslog.Warn("[NETWORK] OnReceive 监听通道已满", zap.String("key", responseKey))
        }
        return
    }
    c.mu.Unlock()
}
```

**删除**：`BuildPacket(cmd, act uint8, body []byte)` 便捷方法（不再需要）。

### 5.2 `network/client.go`

```go
type Client struct {
    name           string
    tcpConns       map[string]*Connection
    udpConn        *Connection
    mu             sync.RWMutex
    requestTimeout time.Duration  // 替代 protocol *Protocol
}

// NewClient 创建网络客户端
func NewClient(name string, requestTimeout time.Duration) *Client {
    // 不再持有 Protocol，连接通过 requestTimeout 创建 Connection
}

// ConnectTCP 建立 TCP 连接
func (c *Client) ConnectTCP(serviceName string) bool {
    conn := NewConnection(serviceName, c.name, c.requestTimeout)
    // ...
}

// 删除 GetProtocol() 方法
```

### 5.3 `network/gnet.go` — OnTraffic 热路径

```go
import "stressbot/adapter"

type EventServer struct {
    gnet.BuiltinEventEngine
    registry     *connRegistry
    adapter      adapter.Adapter  // 替代 protocol *Protocol
    tickInterval time.Duration
}

func NewEventServer(adp adapter.Adapter, heartbeatInterval time.Duration) *EventServer {
    return &EventServer{registry: newConnRegistry(), adapter: adp, tickInterval: heartbeatInterval}
}

// OnTraffic 热路径：使用 adapter 接口的纯 Go 方法（HeaderSize/BodyLength）做帧分割，
// Lua 仅在 Decode 时调用（每完整包一次）。
func (es *EventServer) OnTraffic(gconn gnet.Conn) (action gnet.Action) {
    headSize := es.adapter.HeaderSize()  // 无 Lua 调用，返回缓存值
    
    for {
        available := gconn.InboundBuffered()
        if available < headSize {
            return gnet.None
        }
        
        headBuf, err := gconn.Peek(headSize)
        if err != nil || len(headBuf) < headSize {
            return gnet.None
        }
        
        bodyLen := es.adapter.BodyLength(headBuf)  // 无 Lua 调用，纯 Go 计算
        if bodyLen < 0 {
            // BodyLength < 0 表示协议头无效（magic 错误或长度溢出）。
            // 压测工具无需逐字节尝试恢复同步，直接关闭连接更安全，
            // 避免损坏帧反复触发无效解析（gconn.Discard(1) 可能死循环）。
            stresslog.Warn("[NETWORK] 协议头非法，关闭连接",
                zap.String("service", conn.ServiceName()))
            return gnet.Close
        }
        
        totalLen := headSize + bodyLen
        if available < totalLen {
            return gnet.None
        }
        
        msgBuf := make([]byte, totalLen)
        if _, err = gconn.Read(msgBuf); err != nil {
            stresslog.Error("[GNET] 读取消息失败", zap.Error(err))
            return gnet.None
        }
        
        conn := es.registry.get(gconn)
        if conn != nil {
            secretKey := conn.GetSecretKey()
            responseKey, body, headerErr := es.adapter.Decode(msgBuf, secretKey)  // Lua 调用（每完整包一次）
            if responseKey != "" {
                conn.OnReceive(responseKey, body, headerErr)
            }
        }
    }
}
```

**NewDialer 签名变更：**

```go
func NewDialer(adp adapter.Adapter, heartbeatInterval time.Duration) *Dialer {
    server := NewEventServer(adp, heartbeatInterval)
    return &Dialer{server: server}
}
```

### 5.4 `network/message.go` — 简化

```go
// Message 网络消息（简化版，移除 HeadDecode 依赖）
type Message struct {
    ResponseKey string // 路由键字符串（由 adapter.Decode 产生）
    Data        []byte // 消息体字节
}

func NewMessage(responseKey string, data []byte) *Message {
    return &Message{ResponseKey: responseKey, Data: data}
}

func (m *Message) Copy() *Message {
    if m == nil { return nil }
    data := make([]byte, len(m.Data))
    copy(data, m.Data)
    return &Message{ResponseKey: m.ResponseKey, Data: data}
}

// 删除：CmdAct() / Cmd() / Act() / Error() — 这些字段由 codec.lua 在 decode 中处理
// 如需 error 字段，在适配器脚本中存入 responseKey 或由 Lua 回调脚本解析
```

### 5.5 删除的文件

| 文件 | 原因 |
|------|------|
| `network/protocol.go` | 编解码完全移入 Lua 适配器 |
| `network/middleware.go` | PacketContext/PacketMiddleware 类型不再需要 |
| `network/middleware_gzip.go` | GZIP 由 codec.lua 处理 |
| `network/middleware_registry.go` | 中间件注册系统删除 |
| `network/middleware_lua.go` | LuaMiddlewarePool 功能由 adapter/lua_adapter.go 替代 |
| `network/heartbeat.go` | 心跳 builder 签名不变，文件可保留；若无其他依赖可保留 |

---

## 6. Phase 4：改造 `robot/` 层

### 6.1 `robot/robot.go`

**Robot 结构体新增字段：**

```go
type Robot struct {
    // ...现有字段...
    adapter     adapter.Adapter  // 替代通过 client.GetProtocol() 获取
    udpServices map[string]bool  // 哪些服务名对应 UDP 连接（从配置读取）
    defaultListenServer string   // 默认监听服务（空 server 时使用）
}
```

**NewRobot 签名变更：**

```go
func NewRobot(cfg RobotConfig, flow *engine.TaskFlow, factory *protox.Factory,
    adp adapter.Adapter, dialer *network.Dialer, luaPool *script.RuntimePool,
    requestTimeout time.Duration, udpServices map[string]bool, defaultListenServer string) *Robot {
    
    r := &Robot{
        // ...
        client:              network.NewClient(cfg.Account, requestTimeout),
        adapter:             adp,
        udpServices:         udpServices,
        defaultListenServer: defaultListenServer,
        // ...
    }
    // ...
}
```

**`Start()` 中 ScriptContext 变更：**

```go
script.SetContext(r.L, &script.ScriptContext{
    RobotID:   r.id,
    Account:   r.account,
    Store:     r.state,
    Factory:   r.factory,
    Adapter:   r.adapter,          // 替代 Protocol
    NetSender: &netSenderAdapter{robot: r},
    Ctx:       r.ctx,
    LuaMu:     &r.luaMu,
})
```

**`robotActionHandler.ExecuteAction` 变更：**

```go
func (h *robotActionHandler) ExecuteAction(actionDef *engine.ActionDef) error {
    if actionDef.Pattern == "lua" {
        return h.executeLuaAction(actionDef)
    }
    ae := engine.NewActionExecutor(
        h.robot.state,
        &netSenderAdapter{robot: h.robot},
        h.robot.factory,
        h.robot.adapter,  // 替代 h.robot.client.GetProtocol()
    )
    return ae.Execute(actionDef)
}
```

**`resolveListenConn` 变更（消除硬编码 UDP 服务名）：**

```go
// 旧：switch 硬编码 "udp", "battleUDP" 等
// 新：从配置读取 udpServices 集合

func (h *robotActionHandler) resolveListenConn(server string) *network.Connection {
    if server == "" {
        server = h.robot.defaultListenServer
        if server == "" {
            server = "logic" // 最终默认值
        }
    }
    if h.robot.udpServices[server] {
        return h.robot.client.GetUDPConn()
    }
    return h.robot.client.GetTCPConn(server)
}
```

**`RegisterListen` 变更：**

```go
func (h *robotActionHandler) RegisterListen(refs []engine.ListenRef) error {
    // 按 server 分组
    groups := make(map[string]map[string]network.ListenCallBack) // string key 替代 int key
    
    for _, ref := range refs {
        server := ref.Server
        if server == "" {
            server = h.robot.defaultListenServer
        }
        if _, ok := groups[server]; !ok {
            groups[server] = make(map[string]network.ListenCallBack)
        }
        
        // 直接使用 ResponseKey 字符串，无需调用 adapter.ExpectedResponseKey
        if ref.Callback == "" {
            groups[server][ref.ResponseKey] = nil
            continue
        }
        
        cbDef, ok := h.flow.GetCallback(ref.Callback)
        if !ok {
            stresslog.Warn("[ROBOT] 回调定义不存在", zap.String("callback", ref.Callback))
            continue
        }
        groups[server][ref.ResponseKey] = h.createListenCallback(cbDef)
    }
    
    for server, listenMap := range groups {
        conn := h.resolveListenConn(server)
        if conn == nil {
            stresslog.Warn("[ROBOT] 无连接可注册监听", zap.String("server", server))
            continue
        }
        conn.ListenResponse(listenMap)
    }
    return nil
}
```

**`createListenCallback` 变更（更新 ScriptContext）：**

```go
func (h *robotActionHandler) createListenCallback(cbDef *engine.ListenDef) network.ListenCallBack {
    if cbDef.Script != "" {
        return func(msg *network.Message) {
            h.robot.luaMu.Lock()
            defer h.robot.luaMu.Unlock()
            
            script.SetContext(h.robot.L, &script.ScriptContext{
                RobotID:   h.robot.id,
                Account:   h.robot.account,
                Store:     h.robot.state,
                Factory:   h.robot.factory,
                Adapter:   h.robot.adapter,  // 替代 Protocol
                NetSender: &netSenderAdapter{robot: h.robot},
                Ctx:       h.robot.ctx,
                LuaMu:     &h.robot.luaMu,
            })
            // ...其余不变
        }
    }
    // ...声明式回调不变
}
```

**`netSenderAdapter` 方法逐一变更：**

```go
// TCPSend
// 旧: func (ns *netSenderAdapter) TCPSend(service string, cmd, act uint8, headAndBody []byte) (bool, int)
// 新:
func (ns *netSenderAdapter) TCPSend(service string, packet []byte) (bool, int) {
    conn := ns.robot.client.GetTCPConn(service)
    if conn == nil { return false, 0 }
    return conn.Send(packet)
}

// TCPRequest
// 旧: TCPRequest(service, cmd, act, headAndBody) / TCPRequestFor(svc, sendCmd, sendAct, pkt, respCmd, respAct)
// 新: 合并为一个（respKey 参数覆盖 TCPRequestFor 的场景）
func (ns *netSenderAdapter) TCPRequest(service string, packet []byte, responseKey string) ([]byte, bool) {
    conn := ns.robot.client.GetTCPConn(service)
    if conn == nil { return nil, false }
    resp, _ := conn.RequestResponse(packet, responseKey)
    if resp == nil { return nil, false }
    return resp.Data, true
}

// UDPSend（签名不变，但调用方已通过 adapter.EncodeTCP 构建完整包）
func (ns *netSenderAdapter) UDPSend(packet []byte) bool {
    conn := ns.robot.client.GetUDPConn()
    if conn == nil { return false }
    ok, _ := conn.Send(packet)
    return ok
}

// 删除：UDPSendPacket（调用方自行 adapter.EncodeTCP）

// EnsureListener
// 旧: EnsureListener(service string, cmd, act uint8)
// 新:
func (ns *netSenderAdapter) EnsureListener(service string, responseKey string) {
    conn := ns.robot.resolveListenConnByName(service)
    if conn == nil { return }
    conn.AddListener(responseKey, nil)
}

// GetListenResp
// 旧: GetListenResp(service string, cmdAct int) []byte
// 新:
func (ns *netSenderAdapter) GetListenResp(service string, responseKey string) []byte {
    conn := ns.robot.resolveListenConnByName(service)
    if conn == nil { return nil }
    msg := conn.GetListenResp(responseKey)
    if msg == nil { return nil }
    return msg.Data
}

// RegisterHeartbeat（签名不变，builder 闭包内容由调用方改用 adapter.EncodeTCP）
```

**新增辅助方法 `resolveListenConnByName`**（供 `netSenderAdapter` 使用，等价于现有 `resolveListenConn`）：

```go
// resolveListenConnByName 按服务名解析连接，区分 UDP / TCP。
// 统一 resolveListenConn 的职责到 Robot 上，供 netSenderAdapter 和 handler 共享。
func (r *Robot) resolveListenConnByName(name string) *network.Connection {
    if r.udpServices[name] {
        return r.client.GetUDPConn()
    }
    if name == "" {
        name = r.defaultListenServer
    }
    return r.client.GetTCPConn(name)
}
```

> 原 `resolveListenConn` 仍保留用于 handler 内部调用（或直接改为调用此方法）。

### 6.2 `robot/manager.go`

**ManagerConfig 新增字段：**

```go
type ManagerConfig struct {
    // 现有字段
    AccountPrefix string
    StartNumber   int
    Count         int
    ConcurrentNum int
    AuthBaseURL   string
    Version       string
    Channel       string
    Platform      string
    
    // 新增
    Adapter             adapter.Adapter   // 协议适配器（共享）
    RequestTimeout      time.Duration     // TCP 请求超时（配置驱动）
    UDPServices         []string          // 哪些 server 名对应 UDP（如 ["udp","battleUDP"]）
    DefaultListenServer string            // 空 server 时的默认监听服务
}
```

**Manager 结构体：**

```go
type Manager struct {
    cfg     ManagerConfig
    flow    *engine.TaskFlow
    factory *protox.Factory
    // 删除 protocol *network.Protocol
    dialer  *network.Dialer
    luaPool *script.RuntimePool
    // ...
}
```

**NewManager：**

```go
func NewManager(cfg ManagerConfig, flow *engine.TaskFlow, factory *protox.Factory,
    dialer *network.Dialer, luaPool *script.RuntimePool) *Manager {
    // protocol 参数移除，adapter 从 cfg.Adapter 获取
}
```

**StartAll 内部构建 udpServicesMap：**

```go
udpServicesMap := make(map[string]bool, len(m.cfg.UDPServices))
for _, svc := range m.cfg.UDPServices {
    udpServicesMap[svc] = true
}

r := NewRobot(RobotConfig{...}, m.flow, m.factory,
    m.cfg.Adapter, m.dialer, m.luaPool,
    m.cfg.RequestTimeout, udpServicesMap, m.cfg.DefaultListenServer)
```

---

## 7. Phase 5：改造 `script/` 层

### 7.1 `script/runtime.go`

**ScriptContext：**

```go
type ScriptContext struct {
    RobotID   int
    Account   string
    Store     *state.Store
    Factory   *protox.Factory
    Adapter   adapter.Adapter  // 替代 Protocol engine.ProtocolEncoder
    NetSender engine.NetSender
    Ctx       context.Context
    LuaMu     *sync.Mutex
}
```

### 7.2 `script/api_network.go`

**所有函数变更汇总：**

**`extractNetArgs` 变更**（cmd/act 参数 → route table/value，并保持向后兼容）：

```go
// extractNetArgs 从 Lua 栈提取 service + route + msg + s2cProto。
//
// 支持两种调用方式（向后兼容）：
//   新：network.request("logic", {cmd=1, act=1}, c2sMsg, "RespProto")
//   旧：network.request("logic", 1, 1, c2sMsg, "RespProto")  ← 数字 cmd/act 转为 table
//
// 识别规则：
//   - 第 2 参数若为 table 或 nil → 新格式，直接作为 route
//   - 第 2 参数若为 number → 旧格式，第 3 参数也取 number，构造 table {cmd=N, act=M}
func extractNetArgs(L *lua.LState) (service string, route lua.LValue, msg proto.Message, s2cProto string) {
    argIdx := 0
    for i := 1; i <= L.GetTop(); i++ {
        v := L.Get(i)
        if ud, ok := v.(*lua.LUserData); ok {
            if pm, ok := ud.Value.(proto.Message); ok {
                msg = pm
            }
            continue
        }
        argIdx++
        switch argIdx {
        case 1:
            service = lua.LVAsString(v)
        case 2:
            if _, isNum := v.(lua.LNumber); isNum {
                // 旧格式：第 2/3 参数为 cmd/act 数字，构造 table {cmd=N, act=M}
                cmd := float64(v.(lua.LNumber))
                var actVal float64
                if i+1 <= L.GetTop() {
                    if n, ok := L.Get(i+1).(lua.LNumber); ok {
                        actVal = float64(n)
                        i++ // 跳过 act 参数，argIdx 不额外增加
                    }
                }
                tbl := L.NewTable()
                L.SetField(tbl, "cmd", lua.LNumber(cmd))
                L.SetField(tbl, "act", lua.LNumber(actVal))
                route = tbl
            } else {
                route = v // 新格式：直接是 LTable 或 LNil
            }
        default:
            if s2cProto == "" {
                s2cProto = lua.LVAsString(v)
            }
        }
    }
    return
}
```

**`buildPacket` 变更**（使用 adapter.EncodeTCP）：

```go
// buildPacket 构建完整数据包（header + body）
// 旧：ctx.Protocol.BuildPacket(cmd, act, body, key)
// 新：ctx.Adapter.EncodeTCP(route, body, key)
func buildPacket(ctx *ScriptContext, service string, route lua.LValue, msgData []byte) []byte {
    secretKey := ctx.NetSender.GetSecretKey(service)
    // 将 Lua route 值转为 Go any（反向转换）
    goRoute := luaValueToRoute(route)
    return ctx.Adapter.EncodeTCP(goRoute, msgData, secretKey)
}

// luaValueToRoute 将 Lua table/nil 转换为 Go any（route）
func luaValueToRoute(v lua.LValue) any {
    if v == lua.LNil {
        return nil
    }
    if tbl, ok := v.(*lua.LTable); ok {
        result := make(map[string]any)
        tbl.ForEach(func(k, val lua.LValue) {
            key := lua.LVAsString(k)
            switch v := val.(type) {
            case lua.LNumber:
                n := float64(v)
                if n == math.Trunc(n) {
                    result[key] = int64(n)
                } else {
                    result[key] = n
                }
            case lua.LString:
                result[key] = string(v)
            case lua.LBool:
                result[key] = bool(v)
            }
        })
        return result
    }
    return nil
}
```

**`networkRequest` 新签名（Lua API 调用方式）：**

```lua
-- 新 Lua API 调用方式
local code, resp = network.request("logic", {cmd=1, act=1}, c2s_msg, "RespS2C")
-- 带不同响应路由：
local code, resp = network.request("logic", {cmd=2, act=16}, c2s_msg, "RespS2C", {cmd=1, act=2})
```

```go
func networkRequest(L *lua.LState) int {
    ctx := GetContext(L)
    service, route, msg, s2cProto := extractNetArgs(L)
    
    // 可选第5参数：respRoute（响应路由，不同于发送路由时使用）
    var respRoute lua.LValue = lua.LNil
    if L.GetTop() >= 5 {
        respRoute = L.Get(5)
    }
    
    // 序列化 msg
    var msgData []byte
    if msg != nil {
        var err error
        msgData, err = proto.Marshal(msg)
        if err != nil { ... }
    }
    
    packet := buildPacket(ctx, service, route, msgData)
    
    // 计算期望响应键
    goRespRoute := luaValueToRoute(respRoute)
    if goRespRoute == nil {
        goRespRoute = luaValueToRoute(route)
    }
    respKey := ctx.Adapter.ExpectedResponseKey(goRespRoute)
    
    respBody, ok := ctx.NetSender.TCPRequest(service, packet, respKey)
    // ...解析响应（同现有逻辑）
}
```

**`networkSend` 变更：**

```lua
-- 旧 Lua API
network.send("logic", 6, 15, msg)
-- 新 Lua API
network.send("logic", {cmd=6, act=15}, msg)
```

**`networkExchangeKey` 变更（不再硬编码 0/0）：**

```go
func networkExchangeKey(L *lua.LState) int {
    ctx := GetContext(L)
    service := L.CheckString(1)
    
    // 可选第 2 参数：route（table 或 nil）
    // 不填则为 nil → codec.lua 默认 cmd=0, act=0（当前服务器密钥交换指令）
    // 其他服务器可在 flow.json 或 Lua 脚本中传入实际 route
    routeVal := L.Get(2)
    goRoute := luaValueToRoute(routeVal)
    
    packet := ctx.Adapter.EncodeTCP(goRoute, nil, nil)
    if packet == nil {
        L.Push(lua.LBool(false))
        return 1
    }
    respKey := ctx.Adapter.ExpectedResponseKey(goRoute)
    
    respBody, ok := ctx.NetSender.TCPRequest(service, packet, respKey)
    if !ok || len(respBody) == 0 {
        L.Push(lua.LBool(false))
        return 1
    }
    ctx.NetSender.SetSecretKey(service, respBody)
    L.Push(lua.LBool(true))
    return 1
}
```

**`networkUDPSend` 变更（已是完整包，无需再包装）：**

```lua
-- 旧 Lua API
network.udp_send_msg(4, 1, body)  -- 内部调 protocol.BuildPacketWithOffset（含偏移）

-- 新 Lua API 方式 A（推荐）：直接用 network.udp_send_msg，签名改为接收 route table
network.udp_send_msg({cmd=4, act=1}, body)
-- Go 层 networkUDPSendMsg 内部调 ctx.Adapter.EncodeUDP(route, body, udpKey)，再调 UDPSend
```

```go
// networkUDPSendMsg：Lua 调用方式 A 的 Go 实现
func networkUDPSendMsg(L *lua.LState) int {
    ctx := GetContext(L)
    routeVal := L.Get(1)            // route: table {cmd, act}
    body := []byte(L.CheckString(2)) // body bytes
    
    goRoute := luaValueToRoute(routeVal)
    udpKey := ctx.NetSender.GetUDPSecretKey()
    packet := ctx.Adapter.EncodeUDP(goRoute, body, udpKey) // 注意：EncodeUDP
    ok := ctx.NetSender.UDPSend(packet)
    L.Push(lua.LBool(ok))
    return 1
}
```

```lua

-- 新 Lua API 方式 B（手动控制）：通过 adapter 模块直接编码后发送
local adapter = require("adapter")
local udp_key = network.get_udp_secret_key()
local packet = adapter.encode_udp({cmd=4, act=1}, body, udp_key)  -- 注意：encode_udp，非 encode
network.udp_send(packet)
```

> `adapter` Lua 模块由 `script/api_network.go` 中的 `loadAdapterModule` 注册，暴露 `encode` / `encode_udp` / `decode` / `expected_response_key` 四个函数。

**`networkRegisterHeartbeat` 变更：**

```lua
-- 旧 Lua API
network.register_heartbeat("target", "service", 5000, cmd, act, "heartbeat_builder.lua")
-- 新 Lua API
network.register_heartbeat("target", "service", 5000, {cmd=9, act=1}, "heartbeat_builder.lua")
```

```go
func networkRegisterHeartbeat(L *lua.LState) int {
    // 参数：target, service, intervalMs, route(table), scriptName
    target    := L.CheckString(1)
    service   := L.CheckString(2)
    interval  := int(L.CheckNumber(3))
    routeVal  := L.Get(4)
    scriptName := L.CheckString(5)
    goRoute   := luaValueToRoute(routeVal)
    
    builder := func() []byte {
        ctx.LuaMu.Lock()
        defer ctx.LuaMu.Unlock()
        
        // 执行 builder 脚本获取 body
        body := runHeartbeatBuilder(ctx.L, ctx, scriptName)
        if body == nil { return nil }
        
        // 按 target 选择编码方法和密钥
        var key []byte
        if target == "udp" {
            key = ctx.NetSender.GetUDPSecretKey()
            return ctx.Adapter.EncodeUDP(goRoute, body, key) // UDP 偏移由适配器内部处理
        }
        key = ctx.NetSender.GetSecretKey(service)
        return ctx.Adapter.EncodeTCP(goRoute, body, key)
    }
    ctx.NetSender.RegisterHeartbeat(target, service, interval, builder)
    return 0
}
```

**新增 `adapter` Lua 模块（供业务脚本直接调用编解码）：**

```go
// loadAdapterModule 暴露适配器编解码给 Lua 业务脚本。
// Lua 用法：
//   local adapter = require("adapter")
//   local packet     = adapter.encode({cmd=4, act=1}, body, tcp_key)
//   local udp_packet = adapter.encode_udp({cmd=4, act=1}, body, udp_key)  -- 含 UDP 偏移处理
//   local key, body  = adapter.decode(data, secret_key)
//   local rkey       = adapter.expected_response_key({cmd=4, act=1})
func loadAdapterModule(L *lua.LState) int {
    mod := L.NewTable()
    L.SetField(mod, "encode",                L.NewFunction(adapterEncode))
    L.SetField(mod, "encode_udp",            L.NewFunction(adapterEncodeUDP))
    L.SetField(mod, "decode",                L.NewFunction(adapterDecode))
    L.SetField(mod, "expected_response_key", L.NewFunction(adapterExpectedResponseKey))
    L.Push(mod)
    return 1
}
```

---

## 8. Phase 6：改造 `cmd/agent/main.go`

### 8.1 Config 结构变更

```go
type Config struct {
    Bot struct {
        AccountPrefix string `json:"accountPrefix"`
        StartNumber   int    `json:"startNumber"`
        Count         int    `json:"count"`
        ConcurrentNum int    `json:"concurrentNum"`
    } `json:"bot"`
    
    Auth struct {
        Address  string `json:"address"`
        Version  string `json:"version"`
        Channel  string `json:"channel"`
        Platform string `json:"platform"`
    } `json:"auth"`
    
    Network struct {
        TCPTimeout          string   `json:"tcpTimeout"`
        HeartbeatInterval   string   `json:"heartbeatInterval"`
        UDPServices         []string `json:"udpServices"`         // 新增：标记为 UDP 的服务名列表
        DefaultListenServer string   `json:"defaultListenServer"` // 新增：默认 TCP 监听服务名
        AdapterPoolSize     int      `json:"adapterPoolSize"`     // 新增：LuaAdapter LState 池大小，0=自动（CPU 核心数）
    } `json:"network"`
    
    // .proto 消息定义文件目录（固定使用 Protobuf，目录名保持 conf/proto/ 不变）
    Proto struct {
        Dirs  []string `json:"dirs"`
        Files []string `json:"files"`
    } `json:"proto"`
    
    AdapterScript string `json:"adapterScript"` // 新增：替代 header
    Flow          string `json:"flow"`
    Script struct {
        Dirs []string `json:"dirs"`
    } `json:"script"`
    
    // 删除：Header string / Middleware struct（adapterScript 替代）
}
```

### 8.2 启动流程变更

```
旧流程：initMiddleware → loadProtocol(header.json) → NewDialer(protocol)
新流程：loadAdapter(adapterScript) → NewDialer(adapter)
```

```go
func main() {
    // ...
    
    // 旧：initMiddleware + loadProtocol
    // 新：
    adp, err := loadAdapter(cfg)
    if err != nil {
        stresslog.Fatal("加载适配器失败", zap.Error(err))
    }
    stresslog.Info("[MAIN] 适配器已初始化",
        zap.Int("headerSize", adp.HeaderSize()))
        // 注意：UDPEncryptOffset() 已从 Adapter 接口移除，偏移量由 codec.lua 内部管理
    
    // ...加载 proto, flow（不变）...
    
    dialer := network.NewDialer(adp, heartbeatInterval)  // 不再传 protocol
    
    // ...
    
    timeout, _ := time.ParseDuration(cfg.Network.TCPTimeout)
    if timeout == 0 { timeout = 60 * time.Second }
    
    mgrCfg := robot.ManagerConfig{
        // 现有字段...
        Adapter:             adp,
        RequestTimeout:      timeout,
        UDPServices:         cfg.Network.UDPServices,
        DefaultListenServer: cfg.Network.DefaultListenServer,
    }
    mgr := robot.NewManager(mgrCfg, flow, factory, dialer, luaPool)  // 不再传 protocol
    // ...
}

// loadAdapter 从配置加载 Lua 适配器
func loadAdapter(cfg *Config) (*adapter.LuaAdapter, error) {
    scriptPath := cfg.AdapterScript
    if scriptPath == "" {
        scriptPath = "conf/adapter/codec.lua"
    }
    // 池大小可通过配置调整；高并发场景（机器人数 >> CPU 数）建议设为机器人数的 1/4。
    // 默认 runtime.NumCPU()（EncodeTCP/Decode 调用极短，CPU 核数通常足够）。
    poolSize := cfg.Network.AdapterPoolSize
    if poolSize <= 0 {
        poolSize = runtime.NumCPU()
    }
    return adapter.NewLuaAdapter(poolSize, scriptPath)
}
```

---

## 9. Phase 7：编写 `conf/adapter/codec.lua`

此文件为当前服务器协议的 Lua 适配器实现。

**协议规格（来自 header.json）：**
- 头部大小：12 字节，小端序
- 字段布局：`[len:4][error:2][cmd:1][act:1][index:2][flags:1][bcc:1]`
- 处理链（编码顺序）：GZIP 压缩 → XOR 加密 → BCC 校验写入
- 处理链（解码顺序）：BCC 读取 → XOR 解密 → GZIP 解压

### 9.1 设计思路：Lua 内部链式结构

Go 层的 PacketMiddleware 链已移除，但**链式可组合**的思路在 Lua 层保留。
codec.lua 将每个处理步骤定义为独立 local 函数，通过 `encode_chain` / `decode_chain` 列表按序调用。
切换协议时，只需修改链的注册顺序，或换一个 codec.lua 文件，Go 层 `Adapter` 接口无需任何变化。

```lua
-- conf/adapter/codec.lua
-- 当前服务器协议适配器（12 字节小端头，链式：GZIP → XOR → BCC）
--
-- 协议头字段布局：
--   offset  0 : uint32_le — 包总长（含 header 12 字节）
--   offset  4 : uint16_le — 错误码
--   offset  6 : uint8     — cmd
--   offset  7 : uint8     — act
--   offset  8 : uint16_le — index（序号，压测中固定 0）
--   offset 10 : uint8     — flags（bit0=已加密，bit1=已压缩）
--   offset 11 : uint8     — bcc（header+body 的 XOR 校验字节）
--
-- 运行时约束：gopher-lua (Lua 5.1)，禁止使用 string.pack/unpack。
-- 字节操作使用 string.byte / string.char + bit 模块实现。

local bit  = require("bit")
local zlib = require("zlib")  -- 由 Go 层在 LuaAdapter.newLState() 中注册（见注释 [A]）

-- ─── 协议常量 ────────────────────────────────────────────────────────────────
local HEADER_SIZE    = 12
local UDP_ENC_OFFSET = 11   -- UDP 包前 11 字节保持明文，供服务端查密钥表
local GZIP_THRESHOLD = 256  -- body 超过此字节数才尝试 GZIP 压缩

-- ─── 元信息接口（Go 初始化时一次性调用，结果缓存到 Go 层）──────────────────

function header_size()
    return HEADER_SIZE
end

-- 供 Go 层原生实现 BodyLength（gnet 热路径，零 Lua 调用）
function body_length()
    return {
        offset          = 0,           -- header[0:4] 是 uint32_le 总长
        field_type      = "uint32_le",
        includes_header = true          -- 总长包含 header 12 字节，Go 层需减去
    }
end

-- udp_encrypt_offset() 已从 Go 侧接口移除，Go 层不再直接获取此值。
-- UDP 编码偏移由 encode_udp() 内部应用，外部不感知。

-- ─── 字节工具（Lua 5.1 兼容，无 string.pack）────────────────────────────────

local function write_uint8(n)
    return string.char(bit.band(n, 0xFF))
end

local function write_uint16_le(n)
    return string.char(
        bit.band(n, 0xFF),
        bit.band(bit.rshift(n, 8), 0xFF)
    )
end

local function write_uint32_le(n)
    return string.char(
        bit.band(n, 0xFF),
        bit.band(bit.rshift(n, 8),  0xFF),
        bit.band(bit.rshift(n, 16), 0xFF),
        bit.band(bit.rshift(n, 24), 0xFF)
    )
end

local function read_uint8(s, offset)
    return string.byte(s, offset + 1)  -- Lua 字符串下标从 1 开始
end

-- XOR 加密/解密：对 data[encrypt_offset+1 .. end] 用 key 循环 XOR。
-- encrypt_offset > 0 时，前 N 字节保持明文（UDP 场景）。
-- 注意：Lua 5.1 的 string.char(table.unpack(...)) 有栈深度限制，
-- 超过约 8000 字节时需分段拼接。
local function xor_crypt(data, key, encrypt_offset)
    if not key or #key == 0 or #data == 0 then return data end
    encrypt_offset = encrypt_offset or 0
    local key_len = #key
    local chunks = {}
    local chunk_size = 256  -- 分段避免 unpack 超栈限制

    -- 明文前缀原样保留
    if encrypt_offset > 0 then
        chunks[#chunks + 1] = data:sub(1, encrypt_offset)
    end

    -- 分段 XOR
    local i = encrypt_offset + 1
    while i <= #data do
        local j = math.min(i + chunk_size - 1, #data)
        local buf = {}
        for k = i, j do
            local ki = (k - 1 - encrypt_offset) % key_len + 1
            buf[#buf + 1] = bit.bxor(string.byte(data, k), string.byte(key, ki))
        end
        chunks[#chunks + 1] = string.char(table.unpack(buf))
        i = j + 1
    end

    return table.concat(chunks)
end

-- ─── 处理步骤（各步骤独立 local 函数，按链顺序调用）────────────────────────

-- BCC 步骤（编码）：计算校验字节并写入 header[11]
-- ctx.header_no_bcc 是前 11 字节，ctx.body 是当前 body（已经过 gzip+xor）
local function step_bcc_encode(ctx)
    local v = 0
    for i = 1, #ctx.header_no_bcc do
        v = bit.bxor(v, string.byte(ctx.header_no_bcc, i))
    end
    for i = 1, #ctx.body do
        v = bit.bxor(v, string.byte(ctx.body, i))
    end
    ctx.bcc = bit.band(v, 0xFF)
end

-- XOR 步骤（编码）：加密 body，设置 encrypt flag
local function step_xor_encode(ctx)
    if ctx.secret_key and #ctx.secret_key > 0 and #ctx.body > 0 then
        ctx.body = xor_crypt(ctx.body, ctx.secret_key, ctx.encrypt_offset or 0)
        ctx.flags = bit.bor(ctx.flags, 1)  -- bit0 = encrypted
    end
end

-- GZIP 步骤（编码）：压缩 body，设置 compress flag
local function step_gzip_encode(ctx)
    if #ctx.body < GZIP_THRESHOLD then return end
    local ok, compressed = pcall(zlib.compress, ctx.body)
    if ok and compressed and #compressed < #ctx.body then
        ctx.body = compressed
        ctx.flags = bit.bor(ctx.flags, 2)  -- bit1 = compressed
    end
end

-- XOR 步骤（解码）：解密 body
local function step_xor_decode(ctx)
    if bit.band(ctx.flags, 1) ~= 0 and ctx.secret_key and #ctx.secret_key > 0 then
        ctx.body = xor_crypt(ctx.body, ctx.secret_key, 0)
    end
end

-- GZIP 步骤（解码）：解压 body
local function step_gzip_decode(ctx)
    if bit.band(ctx.flags, 2) ~= 0 then
        local ok, decompressed = pcall(zlib.decompress, ctx.body)
        if ok and decompressed then
            ctx.body = decompressed
        end
    end
end

-- ─── 链配置（修改此处即可切换协议处理顺序）──────────────────────────────────
-- 编码链：先 GZIP → 再 XOR → 最后 BCC（BCC 必须最后，因为要校验最终字节）
local encode_chain = { step_gzip_encode, step_xor_encode }
-- 解码链：先 XOR → 再 GZIP（与编码顺序相反，BCC 校验可选跳过）
local decode_chain = { step_xor_decode, step_gzip_decode }

-- ─── 主接口函数 ───────────────────────────────────────────────────────────────

-- _do_encode：encode / encode_udp 共用的内部实现。
-- encrypt_offset: 0 = TCP（全部加密），UDP_ENC_OFFSET = UDP（前 N 字节明文）。
local function _do_encode(route, body, secret_key, encrypt_offset)
    local cmd = 0
    local act = 0
    if route ~= nil then
        cmd = math.floor(route.cmd or 0)
        act = math.floor(route.act or 0)
    end
    body = body or ""

    -- 构造上下文，各步骤通过修改 ctx 传递状态
    local ctx = {
        body           = body,
        flags          = 0,
        secret_key     = secret_key,
        encrypt_offset = encrypt_offset,
    }

    -- 运行编码链（GZIP → XOR）
    for _, step in ipairs(encode_chain) do
        step(ctx)
    end

    -- 构建 header 前 11 字节（不含 BCC）
    local total_len = HEADER_SIZE + #ctx.body
    ctx.header_no_bcc = (
        write_uint32_le(total_len) ..  -- offset 0
        write_uint16_le(0)          ..  -- offset 4: error = 0
        write_uint8(cmd)            ..  -- offset 6
        write_uint8(act)            ..  -- offset 7
        write_uint16_le(0)          ..  -- offset 8: index = 0
        write_uint8(ctx.flags)          -- offset 10
    )

    -- BCC（必须在 header 和 body 都确定后计算）
    step_bcc_encode(ctx)

    return ctx.header_no_bcc .. write_uint8(ctx.bcc) .. ctx.body
end

-- encode(route, body, secret_key) → 完整 TCP 数据包字节
-- route: table {cmd=N, act=N} 或 nil（nil 表示密钥交换，cmd=0, act=0）
function encode(route, body, secret_key)
    return _do_encode(route, body, secret_key, 0)
end

-- encode_udp(route, body, secret_key) → 完整 UDP 数据包字节
-- 前 UDP_ENC_OFFSET 字节保持明文（供服务端查密钥表），余下部分加密。
function encode_udp(route, body, secret_key)
    return _do_encode(route, body, secret_key, UDP_ENC_OFFSET)
end

-- decode(data, secret_key) → (responseKey string, body string, headerErr number)
-- headerErr：协议头 offset 4~5 的 uint16 错误码，0 表示无错误。
-- Go 层 Decode() 调用时约定 NRet=3，无错误字段的协议此处返回 0 即可。
function decode(data, secret_key)
    if #data < HEADER_SIZE then return "", "", 0 end

    local cmd        = read_uint8(data, 6)     -- offset 6
    local act        = read_uint8(data, 7)     -- offset 7
    local header_err = read_uint16_le(data, 4) -- offset 4：服务端错误码（0 = 正常）
    local flags      = read_uint8(data, 10)    -- offset 10
    -- bcc = read_uint8(data, 11)              -- 压测中跳过完整性校验

    local ctx = {
        body       = data:sub(HEADER_SIZE + 1),
        flags      = flags,
        secret_key = secret_key,
    }

    -- 运行解码链（XOR → GZIP）
    for _, step in ipairs(decode_chain) do
        step(ctx)
    end

    -- responseKey 格式严格为整数 "{cmd}:{act}"，无小数点
    local response_key = math.floor(cmd) .. ":" .. math.floor(act)
    return response_key, ctx.body, header_err
end

-- expected_response_key(route) → string
-- 当前服务器：响应 cmd:act 与发送相同（服务端原路应答）。
-- 若有跨 cmd/act 响应，在 flow.json 的 respRoute 字段覆盖，无需修改此函数。
function expected_response_key(route)
    if route == nil then
        return "0:0"  -- 密钥交换响应固定为 0:0
    end
    local cmd = math.floor(route.cmd or 0)
    local act = math.floor(route.act or 0)
    return cmd .. ":" .. act
end
```

> **[注释 A] zlib 模块注册**：`gopher-lua` 无内置 zlib。实现位于 **`adapter/lua_zlib.go`**（见 Section 3.2b），`newLState()` 中调用：
>
> ```go
> RegisterZlibModule(L)  // adapter/lua_zlib.go，使用标准库 compress/zlib 实现
> ```
>
> 若无 GZIP 压缩需求，将 `GZIP_THRESHOLD = math.huge` 即可禁用，无需删除 `require("zlib")`。

### 9.2 新协议适配器示例（对比说明）

当需要接入另一台协议格式不同的服务器时（如 8 字节头、无 GZIP），只需提供一个新 codec.lua 并更新 `config.json` 的 `adapterScript` 路径，**Go 层零改动**：

```lua
-- conf/adapter/simple_codec.lua（示例：8 字节头，仅 XOR，无压缩）
local HEADER_SIZE    = 8
local UDP_ENC_OFFSET = 0
local encode_chain   = { step_xor_encode }  -- 去掉 GZIP
local decode_chain   = { step_xor_decode }

function header_size()      return HEADER_SIZE end
-- udp_encrypt_offset() 不对外暴露，Go 层不再调用；偏移由 encode_udp() 内部处理
function body_length()
    return { offset=0, field_type="uint32_le", includes_header=false }
end
-- encode / decode / expected_response_key 按新头格式实现
```

---

## 10. Phase 8：配置文件更新

### 10.1 `conf/config.json`（新版）

```json
{
  "bot": {
    "accountPrefix": "bot_",
    "startNumber": 4,
    "count": 1,
    "concurrentNum": 0
  },
  "auth": {
    "address": "http://192.168.61.161:20000",
    "version": "0.31.49.171222",
    "channel": "mine",
    "platform": "1000"
  },
  "network": {
    "tcpTimeout": "60s",
    "heartbeatInterval": "5s",
    "udpServices": ["udp", "battleUDP", "battle_udp", "battleudp"],
    "defaultListenServer": "logic"
  },
  "proto": {
    "dirs": ["conf/proto"],
    "files": []
  },
  "adapterScript": "conf/adapter/codec.lua",
  "flow": "conf/flow/flow.json",
  "script": {
    "dirs": ["conf/scripts"]
  }
}
```

**删除字段**：`header`、`middleware`（`adapterScript` 替代；`proto` 字段和 `conf/proto/` 目录保持不变）。

### 10.2 `conf/flow/flow.json` 迁移

**Action 节点：`cmd/act` → `route`**

```jsonc
// 旧
{"pattern": "tcpRequest", "cmd": 1, "act": 1, "c2sProto": "...", ...}

// 新
{"pattern": "tcpRequest", "route": {"cmd": 1, "act": 1}, "c2sProto": "...", ...}

// 有不同响应路由的场景（旧 respCmd/respAct）
// 旧
{"pattern": "tcpRequest", "cmd": 2, "act": 16, "respCmd": 1, "respAct": 2, ...}
// 新
{"pattern": "tcpRequest", "route": {"cmd": 2, "act": 16}, "respRoute": {"cmd": 1, "act": 2}, ...}

// exchangeKey（route 留空，适配器默认 cmd=0, act=0）
// 旧
{"pattern": "exchangeKey", "service": "logic"}
// 新（route 不填或显式 null，向后兼容逻辑会处理）
{"pattern": "exchangeKey", "service": "logic"}
```

**listenCallbacks：`cmd/act` → `responseKey`**

```jsonc
// 旧
"listenCallbacks": [
  {"cmd": 3, "act": 1, "server": "logic", "callback": "matchPoll"},
  {"cmd": 1, "act": 2, "server": "logic", "callback": null}
]

// 新
"listenCallbacks": [
  {"responseKey": "3:1", "server": "logic", "callback": "matchPoll"},
  {"responseKey": "1:2", "server": "logic", "callback": null}
]
```

> **向后兼容**：由于 `ListenRef.UnmarshalJSON` 自动将 `{"cmd":N,"act":M}` 转换为 `responseKey:"N:M"`，旧格式 flow.json **无需立即修改**即可继续工作。

**Lua 业务脚本 API 变更**：

```lua
-- 旧 API
local code, resp = network.request("logic", 1, 1, msg, "RespS2C")
network.send("logic", 6, 15, msg)
network.wait_listen("logic", 3, 1, "MatchSucceedS2C", 600)
network.register_heartbeat("tcp", "logic", 5000, 9, 1, "hb_builder.lua")

-- 新 API
local code, resp = network.request("logic", {cmd=1, act=1}, msg, "RespS2C")
network.send("logic", {cmd=6, act=15}, msg)
network.wait_listen("logic", "3:1", "MatchSucceedS2C", 600)  -- responseKey 字符串
network.register_heartbeat("tcp", "logic", 5000, {cmd=9, act=1}, "hb_builder.lua")
```

---

## 11. Phase 9：删除旧文件

| 文件/目录 | 原因 |
|-----------|------|
| `conf/header.json` | 协议定义完全移入 codec.lua |
| `network/protocol.go` | 编解码移入 adapter 包 |
| `network/middleware.go` | PacketContext / PacketMiddleware 类型不再使用 |
| `network/middleware_gzip.go` | GZIP 由 codec.lua 处理 |
| `network/middleware_registry.go` | 中间件注册系统整体删除 |
| `network/middleware_lua.go` | LuaMiddlewarePool 由 adapter.LuaAdapter 替代 |
| `conf/middlewares/` 整个目录 | 中间件脚本由 codec.lua 统一实现 |

---

## 12. 各阶段实施顺序与依赖关系

```
Phase 1 (adapter/)
    ↓
Phase 2 (engine/) — 依赖 adapter/ 包的接口定义
    ↓
Phase 3 (network/) — 依赖 adapter/ 包
    ↓
Phase 4 (robot/) — 依赖 adapter/ + engine/ + network/
    ↓
Phase 5 (script/) — 依赖 adapter/ + engine/
    ↓
Phase 6 (cmd/main.go) — 依赖所有包
    ↓
Phase 7 (codec.lua) — 可并行编写，最终联调
    ↓
Phase 8 (配置迁移) — config.json / flow.json 迁移（conf/proto/ 目录和 proto 字段保持不变）
    ↓
Phase 9 (删除旧文件) — 确认编译通过后执行
```

---

## 13. 验证步骤

每个 Phase 完成后立即执行：

```bash
# Step 1: 编译检查
go build ./...

# Step 2: 配置校验
go run ./cmd/validate conf/flow/flow.json

# Step 3: 运行测试（完成 Phase 6-9 后）
rm -f log/stressbot.log
go run ./cmd/agent -config conf/config.json
# 运行 2~5 分钟

# Step 4: 日志审查
grep -i "error\|warn\|失败" log/stressbot.log | grep -v "headError"
grep -c "BattleEnd" log/stressbot.log           # 应 >= 2
grep "SyncFrame: frame=" log/stressbot.log       # 应有持续递增帧号
```

---

## 14. 关键风险与对策

| 风险 | 影响 | 对策 |
|------|------|------|
| codec.lua BCC 实现错误 | 加密握手失败 | 参照旧 `conf/middlewares/xor.lua`（若存在）逐字节校验 |
| zlib 模块缺失 | GZIP 压缩功能失效 | 在 `LuaAdapter.newLState()` 中注册 Go 实现的 zlib Lua 模块 |
| `string.table.unpack` 兼容性 | 大 body 时 `string.char(table.unpack(result))` 可能超栈限制 | 分段拼接（每 100 字节一段），或用 Go 层实现 XOR |
| responseKey 字符串对齐 | 请求-响应匹配失败 | codec.lua 中 decode / expected_response_key 统一使用 `math.floor` 确保整数格式 |
| LuaAdapter 池耗尽 | gnet OnTraffic 阻塞 | BodyLength 纯 Go 实现（无 Lua 调用），池仅用于 Decode/EncodeTCP；高并发可通过配置 `adapterPoolSize` 加大池 |
|| Lua 脚本 API 不兼容 | 业务脚本需批量改造 | `extractNetArgs` 兼容层支持旧 `(svc, cmd, act, msg)` 和新 `(svc, {cmd=N,act=N}, msg)` 两种调用方式（见 Phase 5）|

---

## 15. 不在本次范围内

以下变更明确排除在本 PR 之外：

- `FilterDef` 新增 `StartHourField`/`StartMinField`/`EndHourField`/`EndMinField`（与本重构主题无关）
- `engine/action.go` 中 `compareValues` / `dailyTimeWindow` 的字段名可配置化
- 消息体序列化通用化（JSON / MessagePack / `BodySerializer` 接口）：本次固定使用 Protobuf，非 Protobuf 服务器通过 Lua 脚本手动构建 body 处理，不引入 `SchemaFactory` 抽象

---

## 16. 通用性边界说明

本次重构使 stressbot 成为 **对任意带二进制消息头的原始 TCP/UDP 游戏服务器（Protobuf 消息体）** 的通用压测工具。以下是已知的通用性边界：

| 维度 | 场景 | 支持情况 |
|------|--------|---------|
| **协议头** | 任意消息头格式（字段布局、长度字段位置、端序均可配置） | codec.lua 全权处理，**完全支持** |
| **路由键** | 任意路由键格式（"3:1"、"LOGIN_RESP"、UUID 均可） | codec.lua decode() 返回字符串，**完全支持** |
| **加解密** | XOR / GZIP / BCC / 自定义对称加密 | codec.lua + bit + zlib，**支持**；AES 等复杂算法需 Go 层额外注册 Lua 模块 |
| **消息体格式** | **Protobuf**（声明式 field binding / store 提取） | protox 层，**完全支持** |
| **消息体格式** | JSON / MessagePack / 其他格式 | **不内置支持**；c2sProto=""+ Lua 手动构建 body 可绕过，但失去声明式 binding |
| **连接模式** | TCP 长连接 + UDP | gnet 原生支持，**完全支持** |
| **连接模式** | WebSocket / TLS | **不支持**，gnet 只处理原始 TCP 帧 |
| **连接模式** | HTTP 短连接 / 长轮询 | **不支持**，超出本工具设计范围 |
| **服务发现** | 动态地址（auth HTTP 响应携带） | Lua 脚本提取后调用 `network.connect_tcp`，**支持** |
| **服务发现** | 静态地址（直连） | Lua 脚本硬写或读 flow state，**支持** |

**核心结论**：本工具针对 **原始 TCP/UDP + 自定义二进制协议头 + Protobuf 消息体** 的游戏服务器，做到开箱即用。切换服务器只需更换 `codec.lua`（协议头）+ `flow.json`（流程）+ `.proto`（消息定义），无需修改任何 Go 代码。
