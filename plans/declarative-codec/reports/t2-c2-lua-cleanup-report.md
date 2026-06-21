# T2-C2-Lua + T2-C3-cleanup 报告 — Phase 2：业务 Lua encode→resolver + 删 Lua codec 生产路径

> 状态：**DONE**（工作树未提交，待批次确认）。
> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）。
> **不要 git commit。**
> ⚠️ 运行时验证待办（见 §6）：业务 Lua encode（sync_frame_data 的 udp_send / tcp_request 等）走 Go
> resolver 字节级与旧 Lua RobotAdapter 一致（T1 冻结对拍已证理论一致），实际多连接 / listen routeKey /
> 心跳正确性需 controller 跑 `go run ./cmd/agent` 2~5 分钟（可与 2-A2.2 / 2-B.2 / 2-C1 / 2-C2-Go / 2-C3 运行时
> 验证待办**合并一次** `go run`）。

## 1. 目标与边界

闭环 2-C：把**业务 Lua 的 encode**（`script/api_network.go` 的 22 处 + `buildPacket` + `rememberHeaderErr`）
从 `Context.Adapter`（`*adapter.RobotAdapter`，Lua codec.lua）切到 **CodecResolver**
（`Context.Resolver`，Go SchemaAdapter），并**删除 Lua codec 的全部生产路径**（`RobotAdapter`、
`Context.Adapter`、`loadAdapterModule`、`r.adp`、`ManagerConfig.Adapter`、启动 `NewLuaAdapter`、
业务 LState 的 bit/zlib/crypto 注入）。**保留 `lua_adapter.go` 核心 + `codec.lua`/`error.lua` 作测试 oracle**
（T1 一致性测试用）。完成后业务 LState 无 codec 模块 → 2-D 删锁最后障碍清除。

**严格文件边界**：`script/{runtime,api_network}.go`、`adapter/{robot_adapter(删),lua_adapter}.go`、
`robot/{robot,manager}.go`、`cmd/agent/main.go`、`agent/task_runner.go`、`engine/action.go`（仅注释更新）。
未触碰 `codec/`（T1 冻结）、`network/`（connectionPump agent，2-C3）、前端 / admin、`agent/config.go`。

## 2. 实现内容

### 2.1 `script/runtime.go` — Context 字段 + registerAPIs

1. **`Context.Adapter *adapter.RobotAdapter` → `Context.Resolver adapter.CodecResolver`**：字段类型变更 +
   godoc 更新（注明 T2-C2-Lua 起业务 Lua API 通过 `ctx.Resolver.Resolve("<proto>:<service>")` 取该连接
   的 Go SchemaAdapter，Resolve nil 由调用方 fail loud）。
2. **删 `L.PreloadModule("adapter", loadAdapterModule)`**（registerAPIs）：`conf/scripts` 经 grep 确认零依赖
   adapter 模块；godoc 同步注明下线原因 + codec.lua/error.lua 仅由 LuaAdapter（测试 oracle）加载。

### 2.2 `script/api_network.go` — 22 处 Lua API + buildPacket + rememberHeaderErr

1. **`buildPacket`**（TCP encode）：`ctx.Resolver.Resolve("tcp:"+service).EncodeTCP(goRoute, msgData, secretKey)`；
   Resolver nil 或 Resolve nil → 返回 nil，调用方 fail loud（ErrEncodeFailed）。
2. **`doTCPRequest`**：encode 走 buildPacket；`ExpectedRouteKey` 走 `ctx.Resolver.Resolve("tcp:"+service).ExpectedRouteKey`
   （Resolve nil → ErrEncodeFailed，detail 带 service+routeKey 解析失败，不静默兜底）。
3. **`doUDPRequest`**：开头 Resolve `"udp:"+service` 一次性取 adp；encode + ExpectedRouteKey 同源；
   Resolve nil → ErrEncodeFailed fail loud。
4. **`networkUDPSend`**：`ctx.Resolver.Resolve("udp:"+service).EncodeUDP`；Resolve nil → ErrEncodeFailed。
5. **`networkListen` / `networkTryListen`**：`ctx.Resolver.Resolve("<proto>:"+service).ExpectedRouteKey`；
   Resolve nil → ErrEncodeFailed（与阻塞/非阻塞 listen 一致；routeKey 依赖 codec，缺 codec 必须暴露配置错误）。
6. **`rememberHeaderErr`**（新增 `resolveDescribeError` helper）：DescribeError 经 `ctx.Resolver.Resolve` 取的
   adapter（codec 无关，共享 errors.json）。server hint 双探测（tcp:/udp: + service），任一命中即取描述；
   未命中 → 描述降级空串（headerErr 错误码本身仍按 NewServerError 上抛，非致命，与 2-C2-Go handleHeaderError
   fail-loud 不对称是有意设计——headerErr 描述是增强信息而非核心路径）。
7. **guards**（`networkUDPRequest` / `networkUDPRequestRoute` / `networkUDPSend` / `networkListen` /
   `networkTryListen`）：`ctx.Adapter == nil` → `ctx.Resolver == nil`。
8. **删 `loadAdapterModule` + adapter 模块函数**（`adapterEncodeTCP` / `adapterEncodeUDP` / `adapterEncode` /
   `adapterDecodeTCP` / `adapterDecodeUDP` / `adapterDecode` / `adapterExpectedRouteKey`）：整条下线，
   保留历史注释说明下线原因 + codec.lua/error.lua 仅测试 oracle 用。

### 2.3 `adapter/robot_adapter.go` — 整文件删除

`RobotAdapter` 类型（生产用，含自动加锁 + `*Locked` 版本 + `callEncode`/`callDecode`）全删。
T1 一致性测试用的是 `LuaAdapter` 自身的 Adapter 接口方法（`EncodeTCP`/`DecodeTCP`/`DescribeError` 等），
**不引用** `RobotAdapter`/`NewRobotAdapter`，故删除安全（grep 核实：测试代码零引用 `*RobotAdapter`）。

### 2.4 `adapter/lua_adapter.go` — 瘦身（留核心 oracle）

1. **删 `NewRobotAdapter` 方法**（在 robot LState 上注册 codec.lua + bit/zlib/crypto + `__robot_adapter_*`）
   + `robotAdapterFnNames` 辅助：整段删除，保留历史注释说明下线。
2. **删 `errorScriptBytes` 字段**（仅 NewRobotAdapter 复用）+ NewLuaAdapter 内对其的赋值。
3. **保留 LuaAdapter 核心**：LState 池、`scriptProto`（codec.lua 字节码）、EncodeTCP/EncodeUDP/DecodeTCP/
   DecodeUDP/ExpectedRouteKey/HeaderSize/BodyLength/DescribeError（接口方法，T1 测试 oracle 用）。
4. godoc 更新：角色从「业务路径」改为「仅测试 oracle」（T1 一致性测试字节级真值），生产路径不再构造。

### 2.5 `robot/robot.go` — r.adp 删除 + NewRobot 签名收窄 + SetContext 切 resolver

1. **删 `adp *adapter.RobotAdapter` 字段**（含 godoc）。
2. **`NewRobot` 签名收窄**：去 `globalAdp *adapter.LuaAdapter` 形参（→ 6 参），删 `globalAdp == nil` 校验、
   `globalAdp.NewRobotAdapter(r.l, &r.luaMu)` 调用、`r.adp = robotAdp`、对应 LState 归还错误路径。
   保留 `resolver == nil` 校验 + `r.l == nil`（LState 可用性）校验。godoc 重写：全 codec 路径共享同一份
   resolver；codec 配置错误在拨号/首次 encode 时 fail-loud 上报，不在 NewRobot 暴露（便于定位到连接）。
3. **`SetContext`**：`Adapter: r.adp` → `Resolver: r.resolver`。

### 2.6 `robot/manager.go` — ManagerConfig.Adapter 删除

1. **删 `Adapter *adapter.LuaAdapter` 字段**（含 godoc）+ 「双 codec 过渡态」注释。
2. **`startBatch` 的 `NewRobot` 调用**：去 `m.cfg.Adapter` 实参（NewRobot 签名收窄对齐）。
3. godoc 更新：CodecResolver 注释从「dial/decode 侧」改为「全 codec 路径」（dial/decode/encode/心跳/listen/Lua）。

### 2.7 `cmd/agent/main.go` + `agent/task_runner.go` — 启动路径清理

1. **删 `NewLuaAdapter(...)` 创建** + `adp.HeaderSize()` 初始日志 + `defer adp.Close()` / `adp.Close()`。
2. **`mgrCfg` 去掉 `Adapter: adp`** 赋值。
3. resolver 接线（2-C1 已就位）保留；godoc 从「双 codec 过渡态」改为「全 codec 路径 Go SchemaAdapter」。
4. **双 codec 过渡态结束**：生产路径不再构造 LuaAdapter。

### 2.8 业务 LState 瘦身

随 NewRobotAdapter 删除，robot LState 不再注入 `adapter` / `bit` / `zlib` / `crypto` 模块
（仅保留 robot/proto/network/utils/log/json/share）。grep 核实：`PreloadModule("bit")` /
`RegisterZlibModule` / `RegisterCryptoModule` 仅剩 `LuaAdapter.newLState`（测试 oracle 池）一处调用，
业务 `script.RuntimePool` 从未注入这些模块。

## 3. resolver 全链路（业务 Lua encode 闭环）

```
main.go runStandalone / task_runner Run（T2-C1 已接线）
  └─ NewRobot(..., resolver, ...)（T2-C2-Lua 改：去 globalAdp 形参）
        ├─ r.resolver = resolver
        ├─ r.actionExec = NewActionExecutor(r.state, netSender, r.factory, r.resolver, ...)  # T2-C2-Go 已改
        └─ Start() → SetContext(r.l, &script.Context{Resolver: r.resolver, ...})  # T2-C2-Lua 改：Adapter → Resolver
              └─ 业务 Lua API（api_network.go，已持 luaMu）：
                    ├─ buildPacket(service, route, msgData)
                    │     └─ ctx.Resolver.Resolve("tcp:"+service) → adp
                    │           ├─ nil → 返回 nil → doTCPRequest/networkTCPSend fail loud (ErrEncodeFailed)
                    │           └─ 非 nil → EncodeTCP
                    ├─ doTCPRequest / doUDPRequest
                    │     └─ Resolve("<proto>:"+service) → adp
                    │           ├─ nil → ErrEncodeFailed（带 service+routeKey 解析失败）
                    │           └─ 非 nil → ExpectedRouteKey + Encode（UDP 直 encode）
                    ├─ networkUDPSend → Resolve("udp:"+service).EncodeUDP
                    ├─ networkListen / networkTryListen → Resolve("<proto>:"+service).ExpectedRouteKey
                    └─ rememberHeaderErr → resolveDescribeError(Resolve("tcp:/udp:"+service).DescribeError)
                          （codec 无关，共享 errors.json；未命中→空串，headerErr 错误码仍上抛）
```

T2-C2-Lua 完成后 **encode / decode / dial / 心跳 / listen / 业务 Lua 全程无 Lua codec**
（Go 自 2-C1/2-C2-Go/2-C3；Lua 自 2-C2-Lua）。业务 LState 不再含 codec 模块 → 2-D 删锁最后障碍清除。

## 4. TDD（RED → GREEN）

### 4.1 RED（编译期 + 静态）

无新增测试（业务 Lua encode 走 resolver 路径与 2-C2-Go 的 ActionExecutor 共享同一 resolver；
单测层面 2-C2-Go 的 `action_resolver_test.go` 已覆盖 resolver 分发 + nil fail-loud，本任务不重复）。
本任务的关键验证是「T1 一致性测试（LuaAdapter oracle）仍绿」+「生产代码零残留旧 codec 符号」。

### 4.2 GREEN（实现 → 全 PASS）

| 验证 | 期望 | 结果 |
|---|---|---|
| `go build ./...` | 全绿 | PASS |
| `go vet ./...` | 全绿 | PASS |
| `go test ./... -count=1` | 全绿（含 T1 一致性测试） | PASS |
| T1 `TestSchemaAdapter_ParityWithLuaAdapter` | LuaAdapter oracle 对拍 SchemaAdapter 仍绿 | PASS |
| T1 `TestDecodeTCP_Parity_LuaAdapter` / `TestDecodeUDP_Parity_LuaAdapter` | decode 字节级对拍仍绿 | PASS |
| T1 `TestMigration_TCPLogic_ParityWithLuaAdapter` | migration 对拍仍绿 | PASS |
| codec/engine_bench_test（LuaAdapter oracle） | benchmark 仍可跑 | PASS |
| 静态零残留 | 生产代码无 RobotAdapter/NewRobotAdapter/Context.Adapter/ctx.Adapter/ManagerConfig.Adapter/loadAdapterModule | PASS（仅 lua_adapter 核心注释 + 测试 + 历史 plan） |
| 业务 LState 无 codec 注入 | bit/zlib/crypto/adapter 仅 LuaAdapter.newLState（测试 oracle） | PASS |

## 5. 改动文件

| 文件 | 改动 |
|---|---|
| `script/runtime.go` | `Context.Adapter`→`Resolver`（类型 + godoc）；删 `PreloadModule("adapter")`（+ godoc） |
| `script/api_network.go` | `buildPacket`/`doTCPRequest`/`doUDPRequest`/`networkUDPSend`/`networkListen`/`networkTryListen` 全切 `ctx.Resolver.Resolve`；guards `ctx.Adapter`→`ctx.Resolver`；`rememberHeaderErr` + 新增 `resolveDescribeError` helper；删 `loadAdapterModule` + 7 个 adapter 模块函数（保留历史注释） |
| `adapter/robot_adapter.go` | **整文件删除**（RobotAdapter 生产用类型） |
| `adapter/lua_adapter.go` | 删 `NewRobotAdapter` + `robotAdapterFnNames` + `errorScriptBytes` 字段及赋值；godoc 改「仅测试 oracle」 |
| `robot/robot.go` | 删 `r.adp` 字段；`NewRobot` 签名收窄（去 globalAdp）+ godoc 重写；`SetContext` `Adapter: r.adp`→`Resolver: r.resolver`；删 NewRobotAdapter 调用块 |
| `robot/manager.go` | 删 `ManagerConfig.Adapter` 字段（+ godoc）；`startBatch` NewRobot 调用去 `m.cfg.Adapter`；CodecResolver godoc 改「全 codec 路径」 |
| `cmd/agent/main.go` | 删 `NewLuaAdapter` 创建 + `adp.HeaderSize()` 日志 + `adp.Close()`；mgrCfg 去 `Adapter: adp`；godoc 改「全 codec 路径」 |
| `agent/task_runner.go` | 删 `NewLuaAdapter` 创建 + `defer adp.Close()`；mgrCfg 去 `Adapter: adp`；godoc 改「全 codec 路径」 |
| `engine/action.go` | 仅 godoc 注释更新（r.adp/RobotAdapter/Context.Adapter 已删，不再是「留给 Phase 2」） |

**未触碰**：`codec/`（T1 冻结）、`network/`（connectionPump agent，2-C3）、`conf/adapter/codec.lua`/`error.lua`
（测试 fixture 保留）、前端 / admin、`agent/config.go`（`AdapterScript` 字段成孤儿但无害，留给后续清理）。
未 git commit。

## 6. Self-review（对照 brief 验收清单）

- [x] `Context.Resolver`（替 Adapter）；业务 Lua API（buildPacket + 22 处）全走 `ctx.Resolver.Resolve(proto:service)`；
      loadAdapterModule 删；adapter Lua 模块下线。
- [x] `robot_adapter.go` 删；`lua_adapter.go` 瘦身（去 NewRobotAdapter + errorScriptBytes，留核心 oracle）；
      robot LState 无 codec/bit/zlib/crypto/adapter 模块。
- [x] `r.adp`/`NewRobotAdapter`/`ManagerConfig.Adapter`/启动 `NewLuaAdapter` 生产路径全删。
- [x] `codec/` + `adapter/schema_adapter_test` + `codec/decode_test` + `codec/engine_bench_test` + `codec/migration_test`
      一致性测试**仍绿**（LuaAdapter oracle 保留，已验证实际运行 PASS）。
- [x] go build/vet/test 全绿；生产代码零残留旧 codec 符号（仅注释 + 测试 + 历史 plan）。
- [x] 不写兼容兜底：Resolve nil 直接 fail loud（ErrEncodeFailed 带 service 串）/ 空串（DescribeError 非致命）。
- [x] 新字段全链路一致：Context.Resolver 从 robot.resolver → SetContext → ctx.Resolver → 各 Lua API Resolve。
- [x] 错误用 NewActionError（复用 ErrEncodeFailed）；日志中文；godoc 完整。
- [x] Windows 注：未对单文件 gofmt -w（autocrlf CRLF 环境注）；改动文件内容 canonical。
- [x] 未 git commit。

## 7. ⚠️ 运行时验证待办

单测仅覆盖 T1 字节级对拍（LuaAdapter oracle）+ resolver 路径分发（2-C2-Go action_resolver_test）。
以下需 controller/用户跑真实服务端验证（可与 2-A2.2 / 2-B.2 / 2-C1 / 2-C2-Go / 2-C3 运行时验证待办
**合并一次** `go run`）：

1. **业务 Lua encode 字节级一致**：sync_frame_data 等脚本的 `udp_send` / `tcp_request` / `tcp_send` /
   `tcp_listen` 走 Go SchemaAdapter encode（codec.json）与旧 Lua RobotAdapter encode（codec.lua）字节一致
   （T1 冻结对拍已证理论一致，2-C2-Lua 实战闭环）。
2. **业务 Lua routeKey 正确**：tcp_request_route / udp_request_route / try_*_listen 的 routeKey 由 Go
   SchemaAdapter 算（与 decode 侧 Go adapter + engine RegisterListen 一致，闭环双 codec）。
3. **多连接 codec 解析**：tcp:logic / tcp:battle / udp:battle 各连接业务 Lua encode/listen 用**正确**的 codec
   （非混用）。
4. **fail-loud 行为**：若某连接 codec 未配置，业务 Lua tcp_send/request/listen 应 ErrEncodeFailed（detail 带
   service 串）暴露配置遗漏（非静默兜底）；try_*_listen 同（虽是 drain 原语，但 routeKey 依赖 codec）。
5. **DescribeError 降级**：headerErr 描述经 resolver 任一 adapter 取，命中时 detail 含人类可读前缀；
   未命中（codec 未映射）→ 空串降级，headerErr 错误码本身仍按 NewServerError 上抛。

验证步骤（CLAUDE.md §验证流程）：
```
rm -f log/stressbot.log
go run ./cmd/agent -config conf/config.json
# 运行 2~5 分钟
# grep -i "error\|warn\|失败" log/stressbot.log | grep -v "headError"
# 期望：无「codec 未映射」类异常（生产 3 份 codec.json 已配齐 tcp:logic/tcp:battle/udp:battle）
```

## 8. Concerns

1. **DescribeError fail-loud 不对称**：`rememberHeaderErr` 的 DescribeError 在 Resolve nil 时返回空串
   （headerErr 描述降级，错误码本身仍上抛），与 encode/listen 的 fail-loud（ErrEncodeFailed）不对称。
   原因：headerErr 描述是增强信息而非核心路径，缺失非致命；与 2-C2-Go handleHeaderError 的策略一致
   （describeError nil→空串）。这是有意的设计分歧，非 bug。
2. **`resolveDescribeError` 的 server hint 双探测**：因 Resolver 不暴露枚举 + service 在业务 Lua API 入口
   可知但 proto 未知（同一 service 可能 tcp 也可能 udp），故按 `"tcp:"+service` / `"udp:"+service` 双探测。
   生产中 errors.json 全局同源，任一命中即可；双探测均未命中说明配置异常（service 完全无 codec），
   描述降级空串（错误码仍上抛）。若 Resolver 未来暴露枚举 API 可简化。
3. **T1 测试 oracle 保留决策**：`lua_adapter.go` 核心（LState 池 + codec.lua 字节码 + 接口方法）+
   `conf/adapter/codec.lua`/`error.lua` 保留为测试 fixture。T1 一致性测试（schema_adapter_test /
   decode_test / migration_test / engine_bench_test）用 LuaAdapter 作字节级真值，对拍 Go SchemaAdapter。
   生产路径已不构造 LuaAdapter（main.go/task_runner 删除 NewLuaAdapter 调用），保留无运行时开销。
4. **`agent/config.go` 的 `AdapterScript` 字段成孤儿**：T2-C2-Lua 后 `r.cfg.AdapterScript` 无引用
   （原仅 NewLuaAdapter 回退路径用）。字段无害（默认值 "conf/adapter/codec.lua"），留给后续清理
   （connectionPump / 2-D 或专门 config 清理批次），不在本任务文件边界内。
5. **`network/gnet.go` 注释残留**：line 63/333/337/369 的 godoc 仍提「per-Robot RobotAdapter」/
   `d.server.adp`，属 connectionPump agent（2-C3，任务 #6）的工作范围，本任务按 brief「不动 network/」
   保持不触碰，避免与并行 agent 冲突。
6. **gofmt CRLF**：本 worktree autocrlf=true，所有 .go 检出为 CRLF，`gofmt -l` 全标脏。改动文件内容
   canonical（`sed 's/\r$//' | gofmt -l` 空），未对单文件 gofmt -w（按 Windows 环境注）。

## 9. 2-D 接力（删 luaMu / withReleasedMu）

T2-C2-Lua 完成后，业务 LState 的 codec 依赖（codec.lua + bit/zlib/crypto + adapter 模块）**已全部下线**。
2-D 删 `luaMu` / `withReleasedMu` 的最后障碍清除：

| 当前（2-C2-Lua 后） | 2-D 改成 |
|---|---|
| `script/runtime.go` `Context.LuaMu *sync.Mutex` | 删（业务 LState 单所有者后无需串行） |
| `script/api_network.go` `withReleasedMu(ctx.LuaMu, ...)` | 删（withReleasedMu 函数本身也删） |
| `robot/robot.go` `luaMu sync.Mutex` + `NewRobot` 内 `globalAdp.NewRobotAdapter(r.l, &r.luaMu)` | 已删（本任务）；luaMu 字段待 2-D |
| `robot/robot.go` `executeLuaAction` / `executeLuaBoolean` 的 `luaMu.Lock`/`Unlock` | 删 |
| `robot/robot.go` `Start()` 内 `luaMu.Lock`/`Unlock`（SetContext） | 删 |

**前置条件**（须 2-A/B/C 全完 + 审计闸门）：
- 2-A（listen queue + script callback 下线 + QueueSize）：已完成（#1/#2/#8）。
- 2-B（声明式心跳 + 动态心跳迁移 + Lua builder 删除）：已完成（#3/#13/#14）。
- 2-C1（CodecResolver dial/decode）：已完成（#4）。
- 2-C2-Go（encode Go 侧）：已完成。
- 2-C2-Lua（encode Lua 侧 + 删 Lua codec 生产路径）：**本任务完成**。
- 2-C3（connectionPump 替换三协程）：进行中（#6）。

2-C3 connectionPump 完成后，业务 LState 真正单所有者（decodeLoop/connectionPump 完全在 Go 侧，
不触碰业务 LState），即可安全删 luaMu/withReleasedMu。审计闸门：grep `luaMu` / `withReleasedMu`
全仓零生产引用（仅注释 / 历史 plan），且 go build/vet/test 全绿。
