# RobotLocalAdapter 重构：将协议适配器下沉到 Robot 私有 LState

> 目标：让每个 Robot 用自己的 LState 完成 encode/decode/expected_route_key 调用，
> 消除"成千 Robot 共抢 N 个全局 adapter LState"这个尾延迟根源；接受单 Robot
> 内部所有 Lua 操作（业务 / decode / listen / heartbeat）在 luaMu 下串行。
>
> 旧工具的实际形态正是单 Robot 单 LState 串行，长期稳定。本次改造把 stressbot 对齐到同一拓扑。

---

## 1. 背景与现状

### 1.1 当前架构

```
┌──────────────────────────────────────────────────────────────────────────┐
│                 全局 adapter.LuaAdapter（NewLuaAdapter）                 │
│                                                                          │
│  - states chan *lua.LState（容量 = NumCPU * 4，上限 128，实测 32）       │
│  - HeaderSize / BodyLength：纯 Go，零 Lua 调用                           │
│  - EncodeTCP / EncodeUDP / DecodeTCP / DecodeUDP / ExpectedRouteKey：    │
│    从 states 取一个 LState，执行 codec.lua 注册的对应函数，归还          │
│  - DescribeError：用 sync.Map 永久缓存                                   │
└──────────────────────────────────────────────────────────────────────────┘
            ▲                          ▲                  ▲
            │ encode / decode           │ encode           │ decode
            │                          │                  │
   ┌────────┴────────┐         ┌───────┴──────┐  ┌────────┴────────┐
   │ gnet decodeLoop │         │ 业务 Lua 脚本│  │ 声明式动作执行   │
   │ (per-conn)      │         │ (持 luaMu)   │  │ (主 goroutine)  │
   │ × N 个 Robot    │         │ × N 个 Robot │  │ × N 个 Robot    │
   └─────────────────┘         └──────────────┘  └─────────────────┘
```

### 1.2 已知问题

- **跨 Robot 抢夺池**：1000+ Robot 同时 encode/decode 时，32 个 LState 远远不够，
  `acquire()` 在 channel 上排队。一旦排到 `lstateAcquireTimeout = 30s` 才会失败，
  实际表现为大量延迟尖刺（P95 飙升、`CONN_DROPPED cause=RST` 雪崩）。
- **GC / 内存压力**：每次 encode/decode 都跨 LState，Lua 栈频繁分配，
  老一辈数据进入 G1。
- **Lua-Go 切换抖动**：单次 EncodeTCP/Decode 完整流程经过 `acquire → CallByParam →
  push string → release`，每次都涉及多次内存拷贝（`L.LString(string(body))` 等）。
- **观察到的具体症状**：
  - `RECV_TIMEOUT script=connect_logic.lua` 大量
  - `CONN_DROPPED logic routeKey=1:1 cause=RST` 大量
  - `decode 通道已满，关闭连接以释放压力` 在停止阶段（已通过 EnqueueResult 三态修复，但运行期间偶发也是同一根源）

### 1.3 目标

把所有 codec 相关的 Lua 调用搬到 **Robot 私有的 LState** 上：

```
┌────────────────── 全局 adapter.LuaAdapter（仅保留元信息缓存）────────────────┐
│  - HeaderSize / BodyLength：纯 Go（gnet OnTraffic 热路径继续用这两个）    │
│  - DescribeError：sync.Map 缓存（低频，命中后零 Lua）                       │
│  - 维护单一 LState 用于解析 error.lua 的描述（仅 cache miss 时用）         │
└───────────────────────────────────────────────────────────────────────────┘
                       ▲ HeaderSize/BodyLength（gnet 热路径）
                       │
                       │
        ┌──────────────┴───────────────┐
        │ gnet event loop / OnTraffic  │  零 Lua，只切帧
        └──────────────┬───────────────┘
                       │ EnqueueRaw → decodeCh
                       ▼
┌───────────────── per-Robot RobotAdapter（包装 robot.L + robot.luaMu）─────┐
│                                                                          │
│  - 每个 Robot 独占一份 codec.lua 函数注册（写入 robot.L 的 registry）    │
│  - EncodeTCP / EncodeUDP / DecodeTCP / DecodeUDP / ExpectedRouteKey      │
│    内部直接在 robot.L 上 CallByParam，零跨 robot 竞争                    │
│  - 两套方法：                                                            │
│      EncodeXxx     → 内部自动 Lock/Unlock luaMu                          │
│      EncodeXxxLocked → 假设调用方已持 luaMu（避免自锁）                  │
│  - HeaderSize / BodyLength / DescribeError 直接代理到全局 adapter        │
└──────────────────────────────────────────────────────────────────────────┘
            ▲                       ▲                   ▲
            │ DecodeTCP/UDP         │ EncodeXxxLocked   │ EncodeTCP/UDP
            │ (decodeLoop goroutine,│ (业务 Lua 持锁)   │ (声明式动作执行)
            │  自动加锁)            │                   │ (自动加锁)
            │                       │                   │
   ┌────────┴────────┐    ┌─────────┴────────┐   ┌──────┴────────┐
   │ Connection      │    │ script/api_*.go  │   │ engine/       │
   │ .decodeLoop     │    │ Lua 网络 API     │   │ ActionExecutor│
   └─────────────────┘    └──────────────────┘   └───────────────┘
```

---

## 2. 设计原则与强制约束

| 约束 | 说明 |
|------|------|
| **gnet 热路径零 Lua** | `OnTraffic` 内只调 `HeaderSize / BodyLength`（纯 Go），不变；切帧后 `EnqueueRaw` 投递给 per-connection decodeLoop，decodeLoop 上才发生 Lua 解码 |
| **单 Robot 内部串行** | 同一个 Robot 的业务脚本、decode、listen 回调、heartbeat builder 都竞争同一个 `luaMu`，接受这种串行（旧工具就是这种拓扑）|
| **跨 Robot 不阻塞** | 不同 Robot 之间不再共享 LState，互不影响 |
| **声明式动作不持 luaMu** | `engine.ActionExecutor.Execute(...)` 由主 goroutine 直接调用，没有持锁；它调 `Adapter.EncodeTCP/EncodeUDP` 时走"自动加锁"版本 |
| **业务 Lua 持 luaMu** | `RunActionScript` 入口持 `luaMu`，脚本内调 `network.tcp_request` 等 Go API 时不能重入锁；改走 `*Locked` 版本 |
| **Adapter 接口保持兼容** | `adapter.Adapter` 接口签名不变；`RobotAdapter` 同时实现该接口（自动加锁版本）+ 提供额外的 `*Locked` 方法供持锁路径使用 |
| **关闭路径不死锁** | Robot 关闭时，`Close()` 流程会 cancel ctx，decodeLoop 通过 ctx.Done 退出；Lock 之前必须先检查 ctx，避免业务脚本死循环时关闭挂起 |
| **error.lua 仍走全局缓存** | `DescribeError` 已有 sync.Map 缓存，命中率极高（错误码集中），不进入 RobotAdapter；由全局 LuaAdapter 维护一个单独的 LState（带 mutex）专门用于 cache miss |

---

## 3. 各文件改动概览

| 文件 | 改动类型 | 关键内容 |
|------|----------|----------|
| `adapter/adapter.go` | 改 | 接口签名不变；注释里加入 RobotAdapter / GlobalAdapter 的职责说明 |
| `adapter/lua_adapter.go` | 改 | 池缩为 1（仅 DescribeError 用），新增 `NewRobotAdapter(L, luaMu)` 工厂；EncodeTCP/EncodeUDP/DecodeTCP/DecodeUDP/ExpectedRouteKey 标记为兼容性保留（无人调用） |
| `adapter/robot_adapter.go` | **新增** | 实现 `Adapter` 接口的 robot 私有版本，内部用 `robot.L + luaMu`，提供 *Locked 变体 |
| `adapter/helpers.go` | 增补 | 新增 `pushSecretKey / pushBodyValue` 等小工具消除重复（可选） |
| `script/runtime.go` | 改 | `Context.Adapter` 类型从 `adapter.Adapter` 改为 `*adapter.RobotAdapter`，便于业务 Lua API 调 *Locked 方法 |
| `script/api_network.go` | 改 | 所有 `ctx.Adapter.EncodeTCP/DecodeTCP/...` 改为 `ctx.Adapter.EncodeTCPLocked/...` |
| `script/api_network.go` | 改 | `loadAdapterModule` 暴露的 `adapter.encode_tcp` 等也改用 *Locked（脚本调用时已持 luaMu）|
| `network/connection.go` | 改 | `decodeLoop` 在调 `c.adp.DecodeTCP/UDP` 前后 Lock/Unlock luaMu；StartDecodeLoop 新增 luaMu 参数 |
| `network/gnet.go` | 改 | `DialTCP / DialUDP` 不再直接传 `d.server.adp`，改为接收 robot 的 `adapter` + `luaMu`，并把它们传给 `conn.StartDecodeLoop` |
| `robot/robot.go` | 改 | NewRobot 时调 `globalAdp.NewRobotAdapter(r.l, &r.luaMu)` 创建 `r.adp`；后续 SetContext / Dial 都用这个本地 adapter |
| `cmd/agent/main.go` | 改 | 创建全局 adapter 时池大小固定为 1（或保留默认）；不影响业务 |
| `agent/task_runner.go` | 检视 | 确认 Manager 传给 Robot 的是全局 adapter，由 Robot 内部派生 RobotAdapter |
| `engine/action.go` | 不动 | 仍接收 `adapter.Adapter` 接口；RobotAdapter 实现了该接口；自动加锁版本符合声明式动作的需求 |

---

## 4. Phase 1：新增 `adapter/robot_adapter.go`

### 4.1 设计

```go
// Package adapter 内新增类型。
//
// RobotAdapter 是 codec 适配器的 robot 私有版本：每个 Robot 在自己的 L 上注册
// 一份 codec.lua 的函数副本，所有 EncodeXxx / DecodeXxx / ExpectedRouteKey 调用
// 都在 robot.L 上直接执行，luaMu 串行化。
//
// 两套方法：
//   - 公共方法 EncodeTCP / EncodeUDP / DecodeTCP / DecodeUDP / ExpectedRouteKey：
//     内部自动 Lock/Unlock luaMu，供"未持锁"的调用方使用（decodeLoop、声明式动作执行）。
//   - 带 Locked 后缀的同名方法 EncodeTCPLocked / DecodeTCPLocked 等：
//     假设调用方已持有 luaMu，不再 Lock。供持锁的调用方使用（业务 Lua 网络 API、
//     回调脚本、心跳 builder）。
//
// HeaderSize / BodyLength / DescribeError / Close 直接代理到 parent（全局 adapter），
// 这三个方法本身就不需要每 robot 一份（HeaderSize/BodyLength 是元信息，
// DescribeError 有全局 sync.Map 缓存）。
type RobotAdapter struct {
    parent *LuaAdapter      // 共享元信息和 DescribeError 缓存
    L      *lua.LState      // 该 robot 私有的 LState（来自 script.RuntimePool）
    luaMu  *sync.Mutex      // 该 robot 的 luaMu，保护 L 的并发访问
}

// 编译时接口断言
var _ Adapter = (*RobotAdapter)(nil)
```

### 4.2 工厂方法（位于 `lua_adapter.go`）

```go
// NewRobotAdapter 为指定的 robot LState 创建一份 codec 适配器。
//
// 在 L 上执行 codec.lua + error.lua（如已加载），把 encode_tcp / encode_udp /
// decode_tcp / decode_udp / expected_route_key / describe_error 函数缓存到 registry，
// 后续 EncodeXxx 直接从 registry 取函数调用，零跨 robot 竞争。
//
// 调用前要求：
//   - L 必须是 script.RuntimePool.Acquire() 拿到的全功能 LState（已 PreloadModule
//     business 各模块；codec.lua 内只用 bit / zlib，所以也得 PreloadModule "bit" /
//     RegisterZlibModule）。
//   - luaMu 是该 robot 的 luaMu，所有 RobotAdapter 方法的自动加锁版本都用它。
//
// 此函数本身不持锁，由调用方（robot.NewRobot）保证此时 L 没有其他 goroutine 在用。
func (a *LuaAdapter) NewRobotAdapter(L *lua.LState, luaMu *sync.Mutex) (*RobotAdapter, error) {
    // 1. 注册 bit + zlib 模块（如未注册）
    L.PreloadModule("bit", LoadBitModule)
    RegisterZlibModule(L)

    // 2. 执行 codec.lua 脚本（复用 scriptProto 字节码，避免重复编译）
    fn := L.NewFunctionFromProto(a.scriptProto)
    L.Push(fn)
    if err := L.PCall(0, 0, nil); err != nil {
        return nil, fmt.Errorf("RobotAdapter: 执行 codec.lua 失败: %w", err)
    }

    // 3. 把函数缓存到 registry（与 LuaAdapter.initLState 完全一致）
    fnNames := []string{
        "header_size", "body_length", "encode_tcp", "encode_udp",
        "decode_tcp", "decode_udp", "expected_route_key",
    }
    reg := L.Get(lua.RegistryIndex)
    for _, name := range fnNames {
        fn := L.GetGlobal(name)
        if fn == lua.LNil {
            return nil, fmt.Errorf("RobotAdapter: codec.lua 缺少函数 %s", name)
        }
        L.SetField(reg, "__robot_adapter_"+name, fn)
        L.SetGlobal(name, lua.LNil)
    }

    // 4. 可选：error.lua（如全局 adapter 已加载，复用 errorScriptBytes 在该 L 上跑一遍）
    if a.errorScriptBytes != nil {
        if err := L.DoString(string(a.errorScriptBytes)); err == nil {
            if errFn := L.GetGlobal("describe_error"); errFn != lua.LNil {
                L.SetField(reg, "__robot_adapter_describe_error", errFn)
                L.SetGlobal("describe_error", lua.LNil)
            }
        }
        // 注意：错误码描述命中率极高（同一码 N 次只调一次 Lua），无需在 RobotAdapter
        // 单独缓存；DescribeError 直接代理到 parent（parent 用 sync.Map 全局共享）。
    }

    return &RobotAdapter{parent: a, L: L, luaMu: luaMu}, nil
}
```

### 4.3 方法实现（自动加锁 + Locked 双版本）

```go
// ───── HeaderSize / BodyLength：代理 parent（零 Lua 调用，元信息）─────

func (r *RobotAdapter) HeaderSize() int {
    return r.parent.HeaderSize()
}

func (r *RobotAdapter) BodyLength(header []byte) int {
    return r.parent.BodyLength(header)
}

// ───── Encode/Decode：自动加锁版本 + Locked 版本 ─────

func (r *RobotAdapter) EncodeTCP(route any, body, key []byte) []byte {
    r.luaMu.Lock()
    defer r.luaMu.Unlock()
    return r.EncodeTCPLocked(route, body, key)
}

func (r *RobotAdapter) EncodeTCPLocked(route any, body, key []byte) []byte {
    return r.callEncode("__robot_adapter_encode_tcp", route, body, key)
}

func (r *RobotAdapter) EncodeUDP(route any, body, key []byte) []byte {
    r.luaMu.Lock()
    defer r.luaMu.Unlock()
    return r.EncodeUDPLocked(route, body, key)
}

func (r *RobotAdapter) EncodeUDPLocked(route any, body, key []byte) []byte {
    return r.callEncode("__robot_adapter_encode_udp", route, body, key)
}

func (r *RobotAdapter) DecodeTCP(data, key []byte) (string, []byte, uint64) {
    r.luaMu.Lock()
    defer r.luaMu.Unlock()
    return r.DecodeTCPLocked(data, key)
}

func (r *RobotAdapter) DecodeTCPLocked(data, key []byte) (string, []byte, uint64) {
    return r.callDecode("__robot_adapter_decode_tcp", data, key)
}

func (r *RobotAdapter) DecodeUDP(data, key []byte) (string, []byte, uint64) {
    r.luaMu.Lock()
    defer r.luaMu.Unlock()
    return r.DecodeUDPLocked(data, key)
}

func (r *RobotAdapter) DecodeUDPLocked(data, key []byte) (string, []byte, uint64) {
    return r.callDecode("__robot_adapter_decode_udp", data, key)
}

func (r *RobotAdapter) ExpectedRouteKey(route any) string {
    r.luaMu.Lock()
    defer r.luaMu.Unlock()
    return r.ExpectedRouteKeyLocked(route)
}

func (r *RobotAdapter) ExpectedRouteKeyLocked(route any) string {
    L := r.L
    reg := L.Get(lua.RegistryIndex)
    fn := L.GetField(reg, "__robot_adapter_expected_route_key")
    if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, RouteToLuaValue(L, route)); err != nil {
        stresslog.Error("[ADAPTER] expected_route_key 调用失败", zap.Error(err))
        return ""
    }
    key := lua.LVAsString(L.Get(-1))
    L.Pop(1)
    return key
}

// callEncode / callDecode 是已持锁版本的内部实现，等价于 LuaAdapter.encode/decode
// 但跑在 r.L 上，无 acquire/release 开销。
func (r *RobotAdapter) callEncode(fnRegKey string, route any, body, secret []byte) []byte {
    L := r.L
    reg := L.Get(lua.RegistryIndex)
    fn := L.GetField(reg, fnRegKey)
    routeVal := RouteToLuaValue(L, route)
    bodyVal, keyVal := pushOptionalLString(body), pushOptionalLString(secret)
    if err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, routeVal, bodyVal, keyVal); err != nil {
        stresslog.Error("[ADAPTER] encode 调用失败", zap.String("fn", fnRegKey), zap.Error(err))
        return nil
    }
    out := []byte(lua.LVAsString(L.Get(-1)))
    L.Pop(1)
    return out
}

func (r *RobotAdapter) callDecode(fnRegKey string, data, secret []byte) (string, []byte, uint64) {
    L := r.L
    reg := L.Get(lua.RegistryIndex)
    fn := L.GetField(reg, fnRegKey)
    dataVal := lua.LString(string(data))
    keyVal := pushOptionalLString(secret)
    if err := L.CallByParam(lua.P{Fn: fn, NRet: 3, Protect: true}, dataVal, keyVal); err != nil {
        stresslog.Error("[ADAPTER] decode 调用失败", zap.String("fn", fnRegKey), zap.Error(err))
        return "", nil, 0
    }
    headerErr := uint64(lua.LVAsNumber(L.Get(-1)))
    body := []byte(lua.LVAsString(L.Get(-2)))
    routeKey := lua.LVAsString(L.Get(-3))
    L.Pop(3)
    return routeKey, body, headerErr
}

// pushOptionalLString：nil/空 → LNil，否则 → LString。消除两处重复。
func pushOptionalLString(b []byte) lua.LValue {
    if len(b) == 0 {
        return lua.LNil
    }
    return lua.LString(string(b))
}

// ───── 代理方法 ─────

func (r *RobotAdapter) DescribeError(code uint64) string {
    return r.parent.DescribeError(code)
}

func (r *RobotAdapter) Close() {
    // RobotAdapter 不拥有 L（由 script.RuntimePool 管理），无需关闭。
    // codec 函数挂在 registry 上，Robot 释放 L 时 GC 自然回收。
}
```

### 4.4 全局 LuaAdapter 改动

```go
// 原 LuaAdapter 保留，但角色变成"全局元信息源 + DescribeError 缓存提供者"，不再处理热路径 encode/decode。
// 调整：
//   - 池大小默认仍为 SuggestedPoolSize()，但实际上业务只在 NewRobotAdapter 期间用一次（执行 codec.lua）。
//     最低可以设为 1 节省内存；为兼容 DescribeError cache miss 的并发访问，保留 ≥ 1 即可。
//   - 新增字段 errorScriptBytes []byte：error.lua 原文，供 NewRobotAdapter 在每个 robot.L 上注入。
//   - NewLuaAdapter 内部新增：if errorMapPath != "" { adp.errorScriptBytes, _ = os.ReadFile(errorMapPath) }
//     （现有逻辑已读取并加载到池中的 LState；保留原逻辑不变，新增 errorScriptBytes 字段以供 RobotAdapter 复用）
//
// 编码/解码方法保留（兼容性）但运行时无人调用：
//   - 删除会破坏 var _ Adapter = (*LuaAdapter)(nil) 接口断言。
//   - 改用注释 "已弃用：业务路径请使用 RobotAdapter；本方法仅供单元测试或非 robot 场景兜底" 标记。
```

---

## 5. Phase 2：改造 `script/` 层

### 5.1 `script/runtime.go`

```diff
 type Context struct {
     RobotID   int
     Account   string
     Store     *state.Store
     Factory   *protox.Factory
-    Adapter   adapter.Adapter
+    Adapter   *adapter.RobotAdapter
     NetSender engine.NetSender
     Ctx       context.Context
     LuaMu     *sync.Mutex
     DefaultRequestTimeout time.Duration
     NetLatencyNs atomic.Int64
     NetSamples   atomic.Int64
 }
```

> **影响范围**：所有 `ctx.Adapter` 调用点都需要切到 `*Locked` 方法或保持原方法。
> 由于此 Context 是业务 Lua 脚本运行期间的上下文，**调用方一定持有 luaMu**（`RunActionScript`
> 入口处就是 robotActionHandler 内 `h.robot.luaMu.Lock()`，回调脚本同理），所以
> 业务 Lua API 内调 `ctx.Adapter.*` 都改成 `*Locked` 版本。

### 5.2 `script/api_network.go` — 调用点逐个改

| 行号 | 旧调用 | 新调用 |
|------|--------|--------|
| 159 | `ctx.Adapter.EncodeTCP(goRoute, msgData, secretKey)` | `ctx.Adapter.EncodeTCPLocked(goRoute, msgData, secretKey)` |
| 303 | `ctx.Adapter.ExpectedRouteKey(goRoute)` | `ctx.Adapter.ExpectedRouteKeyLocked(goRoute)` |
| 360 | `ctx.Adapter.ExpectedRouteKey(goRoute)` | `ctx.Adapter.ExpectedRouteKeyLocked(goRoute)` |
| 362 | `ctx.Adapter.EncodeUDP(goRoute, body, udpKey)` | `ctx.Adapter.EncodeUDPLocked(goRoute, body, udpKey)` |
| 559 | `ctx.Adapter.EncodeUDP(goRoute, body, udpKey)` | `ctx.Adapter.EncodeUDPLocked(goRoute, body, udpKey)` |
| 602 | `ctx.Adapter.ExpectedRouteKey(route)` | `ctx.Adapter.ExpectedRouteKeyLocked(route)` |
| 893 / 896 (心跳 builder 内部) | `adp.EncodeUDP / adp.EncodeTCP` | `adp.EncodeUDPLocked / adp.EncodeTCPLocked`（builder 闭包已在 TryLock 持锁路径下）|
| 955 / 957 (`adapterEncode`) | `ctx.Adapter.EncodeTCP / EncodeUDP` | `ctx.Adapter.EncodeTCPLocked / EncodeUDPLocked` |
| 998 / 1000 (`adapterDecode`) | `ctx.Adapter.DecodeTCP / DecodeUDP` | `ctx.Adapter.DecodeTCPLocked / DecodeUDPLocked` |
| 1019 (`adapterExpectedRouteKey`) | `ctx.Adapter.ExpectedRouteKey(route)` | `ctx.Adapter.ExpectedRouteKeyLocked(route)` |

### 5.3 心跳 builder（lines 850~910 区域）

`networkRegisterTCPHeartbeat / networkRegisterUDPHeartbeat` 内构造的闭包 builder：

```go
builder := func() []byte {
    ...
    if !luaMu.TryLock() { return nil }
    func() {
        defer luaMu.Unlock()
        ...
        // 执行 builder 脚本拿到 body
    }()

    // ↓ 这里现在在 luaMu 已释放状态下调 adp.EncodeXXX，需要重新评估
    if proto == hbProtoUDP {
        return adp.EncodeUDP(goRoute, body, secretKey)
    }
    return adp.EncodeTCP(goRoute, body, secretKey)
}
```

**关键变化**：心跳 builder 执行完 Lua 脚本后**已释放 luaMu**，此时调 `EncodeTCP/EncodeUDP`：
- 旧实现走全局 adapter 池，与 luaMu 无关，OK。
- 新实现 RobotAdapter 必须重新 Lock luaMu（即用**自动加锁版本** `EncodeTCP/EncodeUDP`，不带 Locked 后缀）。

**修改方案**：把 encode 调用挪进 `func() { defer luaMu.Unlock(); ... }` 内部，body 和 encode 都在持锁状态完成。或者保持现状用自动加锁版本：

```go
// 方案 A（推荐）：encode 也放在持锁块内
builder := func() []byte {
    ...
    if !luaMu.TryLock() { return nil }
    var packet []byte
    func() {
        defer luaMu.Unlock()
        // 1. 执行 builder Lua 脚本拿 body
        // 2. 立刻 encode（持锁）
        if proto == hbProtoUDP {
            packet = adp.EncodeUDPLocked(goRoute, body, secretKey)
        } else {
            packet = adp.EncodeTCPLocked(goRoute, body, secretKey)
        }
    }()
    return packet
}
```

> 这样保证 builder 整个 Lua 调用窗口只持锁一次，减少 lock/unlock 抖动。

### 5.4 `loadAdapterModule`（业务 Lua 高级用法）

`adapter.encode_tcp(...)` / `adapter.decode_tcp(...)` 等是给业务脚本（如 `connect_logic.lua`
密钥交换）直接调用的。业务脚本进入时**持 luaMu**，所以这里的实现也走 `*Locked` 版本：

```go
func adapterEncode(L *lua.LState, kind string) int {
    ctx := GetContext(L)
    // ...解析 route / body / key
    var result []byte
    if kind == "tcp" {
        result = ctx.Adapter.EncodeTCPLocked(route, body, key)
    } else {
        result = ctx.Adapter.EncodeUDPLocked(route, body, key)
    }
    ...
}
```

---

## 6. Phase 3：改造 `network/` 层

### 6.1 `network/connection.go`

`decodeLoop` 是当前持 luaMu 之外的唯一 Lua 调用源（gnet 之外的）。改造后它需要在调
`adp.DecodeTCP/UDP` 前后 Lock luaMu：

```diff
 // StartDecodeLoop 启动异步 decode goroutine（每个连接独占一个）。
-func (c *Connection) StartDecodeLoop(adp adapter.Adapter, isUDP bool) {
+// 新增 luaMu 参数：decodeLoop 调 Lua decode 前必须 Lock 此 mutex 与
+// 业务脚本 / 回调脚本 / 心跳 builder 串行；nil 表示不加锁（兼容场景，禁止生产使用）。
+func (c *Connection) StartDecodeLoop(adp adapter.Adapter, isUDP bool, luaMu *sync.Mutex) {
     if c == nil || adp == nil { return }
     if !atomic.CompareAndSwapInt32(&c.decodeRun, 0, 1) { return }
     c.adp = adp
     c.isUDP = isUDP
+    c.luaMu = luaMu
     c.decodeCh = make(chan []byte, decodeChSize)
     c.decodeDone = make(chan struct{})
     utils.GetWorkPool().Go(c.decodeLoop)
 }

 func (c *Connection) decodeAndDispatch(msgBuf []byte) {
     defer putMsgBuf(msgBuf)
     secretKey := c.GetSecretKey()
     var routeKey string
     var body []byte
     var headerErr uint64
+    // 串行化：decode 调 Lua codec 时与业务/回调/心跳 builder 共抢同一个 luaMu。
+    // 接受单 robot 内有序串行，换来不再有 1000 robot 抢全局 adapter 池。
+    if c.luaMu != nil {
+        c.luaMu.Lock()
+    }
     if c.isUDP {
         routeKey, body, headerErr = c.adp.DecodeUDP(msgBuf, secretKey)
     } else {
         routeKey, body, headerErr = c.adp.DecodeTCP(msgBuf, secretKey)
     }
+    if c.luaMu != nil {
+        c.luaMu.Unlock()
+    }
     if routeKey == "" {
         ...
     }
     c.OnReceive(routeKey, body, headerErr)
 }
```

**注**：由于 RobotAdapter 的 `DecodeTCP/UDP` 自动加锁版本已经内部 Lock，
也可以让 decodeLoop **直接调自动加锁版本，luaMu 参数省略**：

> 更倾向后者（直接调自动加锁版本）：
> - decodeLoop 不需要知道 luaMu 的存在，依赖 RobotAdapter 自动加锁。
> - StartDecodeLoop 不增加参数。
> - 减少 Connection 与 robot 内部耦合。

确定方案 = **decodeLoop 调 `c.adp.DecodeTCP/UDP`（自动加锁版本）**，
StartDecodeLoop 签名不变。

### 6.2 `network/gnet.go`

`OnTraffic` 内调 `es.adp.HeaderSize() / es.adp.BodyLength(...)` 仍走全局 adapter（这两个是纯 Go，不影响）。
`DialTCP/DialUDP` 内调 `conn.StartDecodeLoop(d.server.adp, false/true)` —— 这里需要改：

```diff
-func (d *Dialer) DialTCP(ctx context.Context, address string, conn *Connection) (gnet.Conn, error) {
+// adp 替代 d.server.adp，由调用方（robot.ConnectTCP）传入本 robot 的 RobotAdapter。
+// 这样 decodeLoop 调的是 robot 自己的 LState，零跨 robot 竞争。
+func (d *Dialer) DialTCP(ctx context.Context, address string, conn *Connection, adp adapter.Adapter) (gnet.Conn, error) {
     ...
     bindConn(gconn, conn)
     d.server.registry.register(gconn, conn)
-    conn.StartDecodeLoop(d.server.adp, false)
+    conn.StartDecodeLoop(adp, false)
     ...
 }

 // DialUDP 同理：增加 adp 参数。
```

> 实现细节：`d.server.adp` 仍保留，用于 OnTraffic 的 `HeaderSize/BodyLength`。

### 6.3 `EventServer` 字段说明

```go
type EventServer struct {
    gnet.BuiltinEventEngine
    registry     *connRegistry
    adp          adapter.Adapter // 仅用于 OnTraffic 的 HeaderSize/BodyLength（纯 Go 元信息）；
                                  // 真正的 encode/decode 在 per-Robot RobotAdapter 上执行
    tickInterval time.Duration
}
```

---

## 7. Phase 4：改造 `robot/` 层

### 7.1 `robot/robot.go`

```diff
 type Robot struct {
     id          int
     ...
-    adp            adapter.Adapter
+    adp         *adapter.RobotAdapter  // 本 robot 私有 codec 适配器
     ...
 }

-func NewRobot(cfg Config, flow *engine.TaskFlow, factory *protox.Factory,
-    adp adapter.Adapter, dialer *network.Dialer, luaPool *script.RuntimePool) *Robot {
+// adp 是全局 LuaAdapter（共享元信息源）；NewRobot 内部派生 RobotAdapter，
+// 让本 robot 的 encode/decode 都在 r.l 上跑，与其他 robot 完全独立。
+func NewRobot(cfg Config, flow *engine.TaskFlow, factory *protox.Factory,
+    globalAdp *adapter.LuaAdapter, dialer *network.Dialer, luaPool *script.RuntimePool) *Robot {
     ...
     if luaPool != nil {
         r.l = luaPool.Acquire()
     }
+    // 在 r.l 上注册 codec.lua 函数（每个 robot 各自一份）。
+    // 此时 r.l 是新拿到的 LState，没有任何 goroutine 在用，直接传 &r.luaMu。
+    robotAdp, err := globalAdp.NewRobotAdapter(r.l, &r.luaMu)
+    if err != nil {
+        // 致命错误：codec.lua 加载失败相当于 robot 不可用，直接 panic
+        // （主流程外层 recover；或返回 error 让上层处理）
+        panic(fmt.Errorf("NewRobotAdapter for %s failed: %w", cfg.Account, err))
+    }
+    r.adp = robotAdp
     ...
-    r.actionExec = engine.NewActionExecutor(r.state, &netSenderAdapter{robot: r}, r.factory, r.adp)
+    r.actionExec = engine.NewActionExecutor(r.state, &netSenderAdapter{robot: r}, r.factory, r.adp)
     ...
 }
```

> `engine.NewActionExecutor` 第 4 参数仍是 `adapter.Adapter` 接口，
> `*RobotAdapter` 实现了该接口（自动加锁版本），声明式动作执行器调 Encode/Decode 时自动加锁；
> 由于声明式动作是从主 goroutine（无 luaMu）调用的，自动加锁不会自锁。

`Start()` 内 `SetContext` 已经传 `Adapter: r.adp`，类型改为 `*adapter.RobotAdapter` 后保持一致（Context.Adapter 字段类型也改）。

`ConnectTCP / ConnectUDP` 内调 `r.dialer.DialTCP(r.ctx, address, conn)`：

```diff
-    _, err := r.dialer.DialTCP(r.ctx, address, conn)
+    _, err := r.dialer.DialTCP(r.ctx, address, conn, r.adp)
```

### 7.2 `robot/manager.go`

`ManagerConfig.Adapter` 字段类型从 `adapter.Adapter` 改为 `*adapter.LuaAdapter`
（明确表示这是全局 adapter）。Manager 创建 Robot 时把全局 adapter 透传给 NewRobot：

```diff
-    Adapter adapter.Adapter `json:"-"`
+    Adapter *adapter.LuaAdapter `json:"-"`
```

如果不想给 ManagerConfig 加紧耦合，可以让 NewRobot 仍接收 `adapter.Adapter` 接口，
内部用 type assertion 取 `*LuaAdapter` 然后调 NewRobotAdapter：

```go
func NewRobot(cfg Config, flow *engine.TaskFlow, factory *protox.Factory,
    globalAdp adapter.Adapter, dialer *network.Dialer, luaPool *script.RuntimePool) *Robot {
    ...
    lua, ok := globalAdp.(*adapter.LuaAdapter)
    if !ok {
        panic("NewRobot: globalAdp must be *adapter.LuaAdapter")
    }
    robotAdp, _ := lua.NewRobotAdapter(r.l, &r.luaMu)
    r.adp = robotAdp
    ...
}
```

> 倾向于显式参数类型 `*adapter.LuaAdapter`，避免运行时 type assertion 失败。

### 7.3 `agent/task_runner.go`

`TaskRunner.Run` 内部创建 LuaAdapter 后传给 Manager：

```go
adp, err := adapter.NewLuaAdapter(0, codecPath, errorPath)
// ↑ adp 类型仍是 *adapter.LuaAdapter，与 ManagerConfig.Adapter 类型对齐
mgrCfg := robot.ManagerConfig{
    ...
    Adapter: adp,
    ...
}
```

无需改动 task_runner.go 主体逻辑，只是字段类型确认。

### 7.4 `cmd/agent/main.go`

类似 task_runner，给 ManagerConfig 注入 LuaAdapter 时类型保持 `*adapter.LuaAdapter`。

---

## 8. Phase 5：保持兼容的细节

### 8.1 全局 LuaAdapter 池大小

- 旧实现 `NewLuaAdapter` 默认 `SuggestedPoolSize() = NumCPU * 4`（上限 128）。
- 新角色下，全局 adapter 仅在 `NewRobotAdapter` 中被并发调用（创建期），以及 `DescribeError` cache miss 时调用。
- 调整：**默认池大小保持 `SuggestedPoolSize()`**，避免改动配置；运行期间这些 LState 实际处于空闲状态，仅元信息查询 / DescribeError 偶尔用一下，开销可忽略。
- 可选优化：将默认池大小压到 `max(2, NumCPU)`，节省约 50% LState 内存。这一项独立做，不放本次重构关键路径。

### 8.2 错误场景：codec.lua 在 robot.L 加载失败

- `NewRobotAdapter` 返回 error → NewRobot 该如何处理？
  - 选项 1：panic（破坏其它机器人创建流程）。
  - 选项 2：log error 后返回 nil Robot，Manager 跳过这个 robot。
- **决定**：选项 2 更稳健。`NewRobot` 改返回 `(*Robot, error)`，Manager 创建失败时打 error 日志、跳过该 bot。

> 需配合改造 `robot.NewRobot` 的签名，以及 `manager.go` 内所有 `NewRobot` 调用点。
> 现网 robot 100% 走相同 codec.lua，正常情况下不会失败；这只是兜底防御。

### 8.3 LState 内存占用评估

- 每个 LState 基础内存 ~0.5MB（empty state） + codec.lua 注册函数后 ~0.5~1MB。
- 1000 robot × 1MB ≈ 1GB。在 16GB+ 服务器上可接受。
- 5000 robot × 1MB ≈ 5GB。若内存吃紧，需要评估降低 robot 上限或单进程拆分。
- **结论**：本次重构假设场景 ≤ 2000 robot/进程；超大规模需在后续设计独立 worker 隔离。

### 8.4 关闭路径

Robot 关闭：

```
Robot.Close()
  → r.cancel()                  // ctx 取消
  → r.client.CloseAll()         // 每条 connection.Close() → conn.cancel
                                //   → decodeLoop 看到 ctx.Done 退出
                                //   → 等 WaitDecodeDone / WaitListenDone
  → r.luaPool.Release(r.l)      // LState 归还到池（codec 函数还在 registry，下次 Acquire 时会被覆盖）
```

> **注意**：`r.l` 归还到 `script.RuntimePool` 后，**registry 里的 codec 函数仍保留**。
> 下一个 robot 拿到这个 LState 时，`NewRobotAdapter` 会再次执行 `L.DoString` 覆盖。
> 这里有一个**正确性风险**：如果 Release 时不清理 registry，下个 robot 拿到的 codec
> 函数其实是上一个 robot 留下的；只要 codec.lua 内容不变就没问题（注册函数是纯逻辑、无 closure）。
>
> **稳妥做法**：在 `RuntimePool.Release` 内清掉 `__robot_adapter_*` registry 字段；
> 或者直接重新 `L.DoString(codec.lua)` 一次（NewRobotAdapter 内已经做了）。
> 现行实现选择"NewRobotAdapter 每次都重置 registry"，无需修改 Release。

---

## 9. Phase 6：实施顺序

```
1. adapter/robot_adapter.go       ← 纯新增，可独立单元测试
2. adapter/lua_adapter.go         ← 增加 NewRobotAdapter 工厂 + errorScriptBytes 字段
3. script/runtime.go              ← Context.Adapter 类型改 *RobotAdapter
4. script/api_network.go          ← 13 处 *Locked 替换 + 心跳 builder 持锁路径整理
5. network/connection.go          ← decodeLoop 调自动加锁版本（其实不改）
6. network/gnet.go                ← DialTCP/DialUDP 增加 adp 参数
7. robot/robot.go                 ← NewRobot 派生 RobotAdapter，r.adp 类型变更
8. robot/manager.go               ← ManagerConfig.Adapter 类型对齐 *LuaAdapter
9. agent/task_runner.go / cmd/agent/main.go ← 字段类型确认（极少改动）
10. 跑 go build ./... 修编译错误
11. 跑 cmd/web npx tsc -b（前端无影响，可跳过）
12. 编写测试 / 运行 500 / 1000 robot 压测
```

每完成一步立刻 `go build ./...`，避免错误堆积。

---

## 10. 验证方案

### 10.1 编译与静态检查

```bash
go build ./...
go vet ./...
golangci-lint run ./adapter/... ./network/... ./robot/... ./script/...
cd cmd/web && npx tsc -b   # 前端无关，跳过亦可
cd cmd/web && npm run test
```

### 10.2 基线对比

跑两轮 500 robot × 5 分钟测试，对比指标：

| 指标 | 改造前（baseline） | 改造后期望 |
|------|--------------------|-----------|
| `CONN_DROPPED logic routeKey=1:1 cause=RST` | 数十次 | 接近 0 |
| `RECV_TIMEOUT script=connect_logic.lua` | 数百次 | 接近 0 |
| `decode 通道已满` warn | 偶发（停止阶段） | 不再出现于运行期 |
| Action P95 / P99 latency | 数百 ms ~ 秒级 | 显著下降，与 LState 池 acquire 排队脱钩 |
| LState acquire 超时（log keyword "[ADAPTER] acquire LState 超时"）| 偶发 | 消失（全局池基本不再被业务路径使用） |
| `PlayerLogin` Connect→Login 间隔 | 5~6 秒 | ≤ 1 秒（heartbeat 之前完成） |
| 内存 RSS | baseline | +N × 1MB（N = robot 数），可接受 |
| CPU | baseline | 与之相当或略低（少了一层 channel 池竞争） |

### 10.3 关键日志关键字

```
grep -i "adapter\|error\|warn\|失败" log/stressbot.log | head -100
grep -c "acquire LState 超时" log/stressbot.log    # 期望 = 0
grep -c "CONN_DROPPED" log/stressbot.log           # 与 baseline 对比，期望大幅下降
grep -c "decode 通道已满" log/stressbot.log         # 期望 = 0（运行期）
```

### 10.4 单元测试建议

- `adapter/robot_adapter_test.go`：
  - 构造一个最小 codec.lua，验证 EncodeTCP/DecodeTCP 闭环
  - 同一个 RobotAdapter 多 goroutine 并发调 Encode/Decode（覆盖自动加锁路径）
  - 持锁路径：测试 EncodeTCPLocked 在调用方持锁时不死锁
- `network/connection_decode_test.go`：
  - mock RobotAdapter 注入到 connection，验证 decodeLoop 正确调用且 luaMu 串行

---

## 11. 风险与回退预案

| 风险 | 可能性 | 后果 | 对策 |
|------|--------|------|------|
| 单 Robot 内 luaMu 竞争激化（业务/decode/listen 互抢）| 中 | decode 延迟增加 | 实测延迟数据；如严重，可考虑给 listen 回调单独一个 LState（不在本次范围）|
| BattleEnd 等高频 UDP 帧场景，业务持锁时间长，decode 排队 | 中 | UDP 帧延迟增加 | 检查 battle_end.lua 内是否有长 Lua 计算，必要时拆 withReleasedMu |
| codec.lua 在某 robot.L 加载失败 | 极低 | 该 robot 创建失败 | NewRobot 改为返回 error，Manager 跳过失败 robot；继续测试其它 |
| RobotAdapter 自动加锁版本被业务路径误用导致自锁 | 中 | 该 robot 卡死 | 编码 review + `go vet`；可考虑加 -race 检测；DEBUG 日志记录 lock holder |
| 内存超预算 | 低 | OOM | 监控 RSS；可设环境变量 STRESSBOT_MAX_ROBOTS_PER_PROC 限流 |

**回退预案**：
- 所有改动集中在 `adapter/` 新增 + 各文件局部替换，**与全局 LuaAdapter 共存**。
- 通过环境变量 `STRESSBOT_USE_ROBOT_ADAPTER=0` 关闭，回退到旧路径：
  - Context.Adapter 类型继续兼容 `adapter.Adapter` 接口；
  - NewRobot 检测环境变量，false 时 `r.adp = globalAdp`（直接用全局 adapter）；
  - decodeLoop / 业务 Lua API / 声明式动作执行器都按接口调用，零差异。
- 默认开启 RobotAdapter 模式；如果灰度测试出严重问题，一键回退。

> **建议**：本次重构默认就启用 RobotAdapter，不引入环境变量复杂度；只在出现严重问题
> 时通过 `git revert` 撤回这次提交。后续如确认稳定可继续保留即可。

---

## 12. 不在本次范围内

以下项明确排除：

- **listen 回调单独 LState**：不引入更细粒度的 LState 拆分。如有需求，后续单独设计。
- **engine.ActionExecutor 移到 Lua VM**：保持 Go 实现，避免重写。
- **全局 LuaAdapter 完全删除**：保留作为元信息源 + DescribeError 缓存，避免接口断言失败。
- **配置文件改动**：本次重构不动 `config.json` / `flow.json` / `codec.lua`。
- **前端改动**：本次重构对前端 zero impact。
- **error.lua 注册到 robot.L**：方案里仅做了"可选支持"，默认走全局 LuaAdapter 的 DescribeError；这部分如果发现 cache miss 路径过慢再优化。

---

## 13. 完成定义（Definition of Done）

- [ ] `go build ./...` 通过
- [ ] `go vet ./...` 无新 warning
- [ ] `cd cmd/web && npm run test` 通过（应无变化）
- [ ] 跑 500 robot × 5 分钟：
  - [ ] `CONN_DROPPED routeKey=1:1` 数量 vs baseline 显著下降
  - [ ] `RECV_TIMEOUT script=connect_logic.lua` 数量 vs baseline 显著下降
  - [ ] `decode 通道已满` warn 在运行期为 0
  - [ ] Action P95 / P99 latency 数据有改善
- [ ] 跑 1000 robot × 5 分钟，确认上述指标仍满足
- [ ] 内存 RSS 增长可控（< robot 数 × 1.5MB）
- [ ] 关闭任务流程正常，无死锁日志（"关闭等待超时" 不出现）
- [ ] 提交单一 commit，附带 changelog 注明影响范围与回退方式
