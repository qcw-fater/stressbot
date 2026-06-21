# T2-C2-Lua + T2-C3-cleanup Brief — Phase 2：Lua encode→resolver + 删 Lua codec 生产路径

> 你是 implementer（Phase 2，顺序——在 Phase 1 之后）。参考 `plans/declarative-codec/02-track-backend-integration.md` §2-C 切片 5/6、`reports/t2-c1-report.md` + `t2-c2-go-report.md`（resolver 已接 dial/decode/Go-encode）、`reports/t1-freeze-handoff.md`（SchemaAdapter 并发安全）、`progress-ledger.md` §「全局约束」+ §「gofmt/换行 环境注」。
> 工作目录：worktree 根。**不要 git commit。**

## 目标

闭环 2-C：把**业务 Lua 的 encode** 从 `Context.Adapter`（`*RobotAdapter`，Lua codec.lua）切到 **CodecResolver**（Go SchemaAdapter），并**删除 Lua codec 的全部生产路径**（`RobotAdapter`、`Context.Adapter`、`loadAdapterModule`、`r.adp`、`ManagerConfig.Adapter`、启动 `NewLuaAdapter`、业务 LState 的 bit/zlib/crypto 注入）。完成后 **Go runtime + 业务 Lua 全走 Go codec**，业务 LState 不再含 codec 模块 → 2-D 删锁最后障碍清除。

## 关键设计决策（已定）

- **`Context.Adapter *RobotAdapter` → `Context.Resolver adapter.CodecResolver`**：业务 Lua API（Go 代码 `api_network.go`）改为 `ctx.Resolver.Resolve("<proto>:<service>").<Method>`。**不改 NetSender.TCPSend 签名**（仍收已编码 packet）；encode 在 buildPacket 等 Go 函数内经 resolver 完成，Lua 脚本零改动。
- **`lua_adapter.go` + `codec.lua`/`error.lua` 保留为「测试专用 oracle」**：T1 一致性测试（`codec/decode_test.go`、`adapter/schema_adapter_test.go`、`codec/engine_bench_test.go`）用 `LuaAdapter` 作真值。**不删 `lua_adapter.go`**；只删它的 `NewRobotAdapter` 方法（返回已删的 `*RobotAdapter`）+ robot-LState 的 codec.lua 注入逻辑。生产路径不再创建 `LuaAdapter`。
- **`DescribeError` codec 无关**（共享 `errors.json`）：rememberHeaderErr 经 resolver 任一 adapter 取（nil→空串，非致命）。

## 现状（worktree 当前态，已核对；codegraph 索引滞后不可信，以本节为准）

- `script/runtime.go:42` `Context.Adapter *adapter.RobotAdapter`；`:516` `L.PreloadModule("adapter", loadAdapterModule)`。
- `script/api_network.go`：22 处 `ctx.Adapter.*Locked`/`DescribeError`（buildPacket:192、doTCPRequest、doUDPRequest、networkUDPSend、networkListen、networkTryListen、adapterEncode/Decode、rememberHeaderErr:133 等）；`loadAdapterModule`:1089。
- `robot/robot.go`：`NewRobot` 调 `globalAdp.NewRobotAdapter(r.l, &r.luaMu)`:161、`r.adp = robotAdp`:170、SetContext `Adapter: r.adp`:223。
- `robot/manager.go:44` `ManagerConfig.Adapter *adapter.LuaAdapter`（注释提 NewRobotAdapter 工厂）；NewRobot 形参 `globalAdp`。
- `cmd/agent/main.go:202` + `agent/task_runner.go:136`：`NewLuaAdapter(...)` 创建（双 codec 过渡，喂 ManagerConfig.Adapter）。
- `adapter/robot_adapter.go`：RobotAdapter 整文件（生产用）。
- `adapter/lua_adapter.go`：LuaAdapter + `NewRobotAdapter` 方法（生产用 NewRobotAdapter；LuaAdapter 核心被测试用）。
- `r.resolver`（2-C1 已就位）、`ActionExecutor.resolver`（2-C2-Phase1 已就位）、connectionPump（2-C3 已就位）——本任务不动这些。

## 范围

**做：**
1. **`Context.Adapter` → `Context.Resolver`**（`script/runtime.go`）：字段类型 `*adapter.RobotAdapter` → `adapter.CodecResolver`；godoc 更新。`robot/robot.go:223` SetContext `Adapter: r.adp` → `Resolver: r.resolver`。
2. **业务 Lua API encode → resolver**（`script/api_network.go`，22 处 + buildPacket + rememberHeaderErr）：
   - `buildPacket(ctx, service, route, msgData)`：`ctx.Resolver.Resolve("tcp:"+service).EncodeTCP(goRoute, msgData, secretKey)`（nil→返回 nil，调用方 fail loud / 记错）。
   - `doTCPRequest`/`doUDPRequest`：encode + ExpectedRouteKey 走 `ctx.Resolver.Resolve(proto+":"+service)`。
   - `networkUDPSend`：`Resolve("udp:"+service).EncodeUDP`。
   - `networkListen`/`networkTryListen`：`Resolve(proto+":"+service).ExpectedRouteKey`。
   - `adapterEncode`/`adapterDecode`（adapter Lua 模块函数，若保留）：走 resolver；**或随 loadAdapterModule 一起删**（见下）。
   - `rememberHeaderErr`：`DescribeError` 经 resolver 任一 adapter（如 `ctx.Resolver.Resolve` 一个已知 server；nil→空串）。建议抽 `Context` helper 或 `resolveDescribeError`。
3. **删 `loadAdapterModule` + 注册**（`api_network.go:1089` + `runtime.go:516`）：已确认 `conf/scripts` 零依赖 adapter 模块（grep 核实）。删 `loadAdapterModule` 函数 + `PreloadModule("adapter", ...)` 行 + adapter 模块函数（adapterEncode/Decode/ExpectedRouteKey 若仅服务该模块）。
4. **删 `adapter/robot_adapter.go` 整文件**（RobotAdapter 生产用，全删）。
5. **`adapter/lua_adapter.go` 瘦身**：删 `NewRobotAdapter` 方法（返回已删 `*RobotAdapter`）+ 其内部的 robot-LState codec.lua 注入（PreloadModule bit/zlib/crypto 到 robot L + 注册 `__robot_adapter_*`）；**保留** LuaAdapter 核心（LState 池、codec.lua encode/decode、DescribeError 缓存）供测试 oracle。
6. **`robot/robot.go`**：删 `r.adp` 字段、`NewRobotAdapter` 调用:161、`r.adp = robotAdp`:170、`NewRobot` 的 `globalAdp` 形参（签名收窄）。SetContext 用 `r.resolver`。
7. **`robot/manager.go`**：删 `ManagerConfig.Adapter` 字段 + `NewRobot` 调用处去掉 `m.cfg.Adapter` 实参。
8. **启动路径**（`cmd/agent/main.go` + `agent/task_runner.go`）：删 `NewLuaAdapter(...)` 创建 + `ManagerConfig.Adapter` 赋值 + 相关日志；resolver 接线（2-C1 已就位）保留。**双 codec 过渡态结束**（不再有 LuaAdapter 生产路径）。
9. **业务 LState 瘦身**：随 NewRobotAdapter 删除，robot LState 不再注入 `adapter`/`bit`/`zlib`/`crypto` 模块（仅保留 robot/proto/network/utils/log/json/share）。

**不做：**
- ❌ 不删 `adapter/lua_adapter.go` 核心（测试 oracle）、`conf/adapter/codec.lua`/`error.lua`（测试 fixture）。
- ❌ 不动 `codec/` 包（T1 冻结）、不动 T1 一致性测试（它们用 LuaAdapter，保留可用）。
- ❌ 不删 `luaMu`/`withReleasedMu`（→ 2-D；业务 Lua 阻塞 API 的 withReleasedMu 保留）。
- ❌ 不动 connectionPump/network/（2-C3 已就位）。
- ❌ 不动前端/admin。

## 关键约束

- **不写兼容兜底**：Resolve nil 直接报错/空串（按语义），不留静默回退。
- **新字段全链路一致**：Context.Resolver 从 robot.resolver → SetContext → ctx.Resolver → 各 Lua API Resolve。
- **保留测试 oracle**：lua_adapter.go 核心 + codec.lua/error.lua 留给 T1 测试；只删生产路径。
- 仅改 script/{runtime,api_network}.go、adapter/{robot_adapter(删),lua_adapter}.go、robot/{robot,manager}.go、cmd/agent/main.go、agent/task_runner.go + 受影响测试。
- Go 最佳实践：godoc；错误 NewActionError 体系；日志中文。
- **Windows 环境注**：`gofmt -l` 标 .go 脏是 autocrlf CRLF，**不要**对单文件 `gofmt -w`；校验 canonical 用 `sed 's/\r$//' f.go | gofmt -l`。
- **不要 git commit。**

## 工作方式（TDD）

1. 先读 `script/runtime.go`（Context 结构:32、PreloadModule:516）、`script/api_network.go`（22 处 ctx.Adapter、buildPacket:192、loadAdapterModule:1089、rememberHeaderErr:127）、`robot/robot.go`（NewRobot globalAdp 形参、r.adp、SetContext:223）、`robot/manager.go`（ManagerConfig.Adapter、NewRobot 调用）、`adapter/robot_adapter.go`（整文件）、`adapter/lua_adapter.go`（NewRobotAdapter 方法 + LuaAdapter 核心）、`cmd/agent/main.go` + `agent/task_runner.go`（NewLuaAdapter 创建）。
2. **静态确认**：grep `conf/scripts` 对 `adapter`/`bit`/`zlib`/`crypto`/`require.*adapter` 零依赖（确认 loadAdapterModule + 模块注入可删）；grep `NewRobot(` 全仓 caller（签名收窄影响）；grep `ManagerConfig.Adapter`/`r.adp`/`Context.Adapter` 残留。
3. RED（若有 Lua harness；否则编译期 + 静态）：buildPacket/doRequest 走 resolver（fake resolver 验证按 service Resolve）；Resolve nil→fail loud。
4. GREEN：Context.Resolver + 22 处转换 + 删 loadAdapterModule + 删 robot_adapter.go + lua_adapter 瘦身 + robot/manager/startup 清理。
5. `go build ./...`、`go vet ./...`、`go test ./... -count=1` 全绿（**含 codec/adapter 一致性测试**——LuaAdapter oracle 保留，应仍绿）。
6. **静态零残留**：grep 生产代码 `RobotAdapter`/`NewRobotAdapter`/`Context.Adapter`/`ctx.Adapter`/`ManagerConfig.Adapter`/`loadAdapterModule`（仅 lua_adapter.go 核心 + 测试 + 注释/plan 历史）；业务 LState 无 bit/zlib/crypto/adapter 注入。
7. **不要 git commit。**

## 验收（self-review）

- `Context.Resolver`（替 Adapter）；业务 Lua API（buildPacket + 22 处）全走 `ctx.Resolver.Resolve(proto:service)`；loadAdapterModule 删；adapter Lua 模块下线。
- `robot_adapter.go` 删；`lua_adapter.go` 瘦身（去 NewRobotAdapter，留核心 oracle）；robot LState 无 codec/bit/zlib/crypto/adapter 模块。
- `r.adp`/`NewRobotAdapter`/`ManagerConfig.Adapter`/启动 `NewLuaAdapter` 生产路径全删。
- `codec/` + `adapter/schema_adapter_test` + `codec/decode_test` + `codec/engine_bench_test` 一致性测试**仍绿**（LuaAdapter oracle 保留）。
- go build/vet/test 全绿；生产代码零残留旧 codec 符号。

## ⚠️ 运行时验证依赖

业务 Lua encode 走 resolver（如 sync_frame_data 的 udp_send、tcp_request 等）需真实服务端验证（单测只覆盖 Resolve nil/fail-loud）。报告列为「待运行时验证」（可与既有验证合并）。

## 报告

写 `plans/declarative-codec/reports/t2-c2-lua-cleanup-report.md`：Context.Resolver 转换、22 处 Lua API 改造、loadAdapterModule 下线、robot_adapter 删 + lua_adapter 瘦身（测试 oracle 保留）、startup 清理、业务 LState 瘦身、TDD、改动文件、self-review、**运行时验证待办**、concerns（DescribeError codec 无关取法、T1 测试 oracle 保留决策）、**2-D 接力**（删 luaMu/withReleasedMu——业务 LState 已单所有者）。
返回（<15 行）：Status、改动文件、测试摘要、运行时验证待办、concerns、报告路径。
