# T2-C1 Brief — CodecResolver 替换 per-robot adapter（dial/decode 侧）

> 你是 implementer。先读本 brief。**前置必读**：`plans/declarative-codec/02-track-backend-integration.md` §2-C（接线表 + 实施切片 2/3）、`plans/declarative-codec/reports/t4-1-report.md`（CodecResolver/LoadCodecResolver）、`plans/declarative-codec/reports/t1-freeze-handoff.md`（SchemaAdapter 并发安全/无状态）、`plans/declarative-codec/progress-ledger.md` §「全局约束」+ §「gofmt/换行 环境注」。
> 工作目录：worktree 根 `D:\Gitee\stressbot\.claude\worktrees\declarative-codec`。**不要 git commit。**

## 目标

把 **dial/decode 侧**从 per-robot 单一 Lua adapter（`r.adp`，`RobotAdapter`）切到 **CodecResolver**（按 `<proto>:<service>` 解析的 Go `SchemaAdapter`）：拨号前按 server 串从 resolver 取 adapter 注入 Connection，decode 用连接固定的 Go adapter。**删 `Dialer.dial` 的 `server.adp` 兜底**。

## 边界（Cut A——最小可编译；track doc 2-C1/2-C2 切法）

**本任务（2-C1）只动 dial/decode + 启动接线 + 删 dial 兜底。`r.adp`（RobotAdapter）保留**——encode/心跳/listen routeKey/业务 Lua `Context.Adapter` 仍走它（→ 2-C2 切 encode、2-C3 删 Lua 整条）。故 2-C1 后短期 Robot 同时持 `r.adp`（Lua，encode 用）+ `r.resolver`（Go，dial/decode 用）——**双 codec 过渡态**（decode Go codec.json / encode Lua codec.lua，T1 已证字节级一致，2-C2 闭环）。

**做（2-C1）：**
1. 启动路径（`cmd/agent/main.go` 单机 + `agent/task_runner.go` agent）：从 adapter 目录推断 `codecs` map（`*_codec.json` → `<proto>:<service>`），调 `adapter.LoadCodecResolver(codecDir, codecs, errorsFile)` 构造 resolver；传给 `ManagerConfig.CodecResolver`（新字段）+ 给 `NewDialer` 一个元信息 adapter（resolver 任取一个，见下「Dialer 元信息源」）。
2. `ManagerConfig`（`robot/manager.go`）：**新增** `CodecResolver adapter.CodecResolver` 字段；**保留** `Adapter *adapter.LuaAdapter`（仍需派生 RobotAdapter 给 Lua/encode 用，2-C3 删）。
3. `NewRobot`（`robot/robot.go`）：签名**增加** `resolver adapter.CodecResolver` 入参；`Robot` **新增** `resolver` 字段；**保留** `globalAdp` + `r.adp`（Lua 派生路径不动）。
4. `Robot.DialTCP/DialUDP`（`robot.go:396/443`）：改为 `adp := r.resolver.Resolve(proto+":"+service)`；**nil → fail loud**（中文 error，带 server 串 + 连接上下文，返回 `ErrConnNotFound` 或新配置类码——优先复用现有码，避免新码除非必要）；非 nil 传给 `r.dialer.DialTCP/DialUDP(ctx, addr, conn, adp)`。
5. `network/gnet.go:366-368`：**删 `d.server.adp` 兜底**。`dial` 收到 `adp==nil` 不再 fallback，直接当错误（或由上层 Robot 保证非 nil——Robot 已 Resolve 校验，故 dial 内 adp 必非 nil；兜底删除后 dial 可保留 nil-guard 记 Error 或信任上层）。`DialTCP/DialUDP/dial` 的 `adp adapter.Adapter` 入参保留（Robot 传入）。
6. `Connection` / `StartDecodeLoop` / `c.adp` / decode 调用（`connection.go:482/489/540/542`）：**不动**——adp 由 dial 时按 server 串一次性解析注入，连接生命周期内固定（plan 02:208「decode 侧不碰 resolver」）。

**不做（明确推迟）：**
- ❌ 不动 encode 侧：`ActionExecutor.adp`（`action.go:105/897/1042/1044/1080/1127/1202`）、心跳 goBuilder `ns.robot.adp`（`robot.go:1254/1295`）、listen routeKey `h.robot.adp`（`robot.go:749`）—— **2-C2**。
- ❌ 不动业务 Lua：`Context.Adapter`（`runtime.go:42`）、`api_network.go` 19 处 `*Locked`、`loadAdapterModule`、`NewRobotAdapter`、`RobotAdapter`/`LuaAdapter` 整文件 —— **2-C3**。
- ❌ 不删 `r.adp` 字段 / `NewRobotAdapter` / `ManagerConfig.Adapter`（Lua/encode 还用）—— **2-C3**。
- ❌ 不动 connectionPump / decodeLoop 调度模型 —— **2-C3**。
- ❌ 不动前端/admin。

## 关键背景（已普查核对，file:line 见 t2-c1 普查报告）

- **T4 `adapter.CodecResolver`** 已就绪未接 runtime：`adapter/codec_resolver.go` 接口 `Resolve(server string) Adapter`（缺映射→nil，调用方 fail loud）+ `LoadCodecResolver(codecDir, codecs map[string]string, errorsFile string)`（逐连接 LoadSchema+NewSchemaAdapter、同文件 dedup、空 map/缺文件 fail loud）。
- **SchemaAdapter 并发安全/无状态**（T1 冻结契约）：resolver 返回的 `*SchemaAdapter` 任意 goroutine 并发调用 9 方法无需加锁——这是 decodeLoop 直接用 `c.adp.DecodeTCP/UDP`（Go adapter）无 luaMu 的前提。
- **当前 dial/decode 链**：`Robot.DialTCP(…, r.adp)` → `Dialer.DialTCP(ctx, addr, conn, adp)` → `dial`（`adp==nil` 兜底 `d.server.adp`）→ `conn.StartDecodeLoop(adp, isUDP)` → `c.adp=adp` → `decodeLoop` 调 `c.adp.DecodeTCP/UDP`。`r.adp` 是 per-robot `RobotAdapter`（Lua，自动加锁版本）。
- **生产 codec 产物**：`conf/adapter/{tcp_logic,tcp_battle,udp_battle}_codec.json` + `errors.json`（T1.6），文件名 `<proto>_<service>_codec.json` → server `<proto>:<service>`。
- **Dialer 元信息源**：`NewDialer(adp, hbInterval)` 的 `adp` 仅用于 `OnTraffic` 的 `HeaderSize()`/`BodyLength()`（`gnet.go:163/178`，gnet event loop 热路径，**先于** Connection.adp 注入）。多 codec 场景若 HeaderSize 不一致则此单 adp 不够——**但当前协议 HeaderSize 全局一致**（3 份 codec.json 同 frame spec，T1.6 同源生成）。故 2-C1：Dialer 取 resolver 任一 adapter 作元信息源（如 `resolver.Resolve("tcp:logic")` 或 map 首个），**HeaderSize-per-connection 下沉留到 2-C3 connectionPump**。

## 设计

### 1. 启动路径构造 resolver（main.go + task_runner.go）

新增 helper（放 `adapter/` 或启动路径内联，推荐 `adapter` 包导出 `InferCodecMap(codecDir string) (map[string]string, error)` 便于复用+单测）：
- 扫 `codecDir` 下 `*_codec.json`（不含 errors.json），文件名去 `_codec.json` 后缀 → 按首个 `_` 拆 `<proto>_<service>` → server `<proto>:<service>` → map[server]filename。
- 空目录/无 codec 文件 → error（中文 fail loud）。
- main.go 单机：`codecDir = paths.Adapter`（conf/adapter）；`errorsFile = filepath.Join(paths.Adapter, "errors.json")`（不存在 LoadCodecResolver 内部处理可选）；`codecs, _ := adapter.InferCodecMap(codecDir)`；`resolver, err := adapter.LoadCodecResolver(codecDir, codecs, errorsFile)`。
- task_runner agent：同构（codecDir 指向任务下发的 adapter 目录，`conf/adapter/`）。
- **Dialer 元信息 adapter**：`metaAdp := resolver.Resolve(<任一 server，如 codecs 首个 key>)`；若 nil（map 空——已被 InferCodecMap 拦）报错。传 `network.NewDialer(metaAdp, hbInterval)`。
- **保留** 原 `NewLuaAdapter` 路径？——encode/心跳/listen/Lua 2-C1 仍需 RobotAdapter（派生自 LuaAdapter）。故 `main.go`/`task_runner` **仍调 NewLuaAdapter** 构造 `adp`（喂 ManagerConfig.Adapter 派生 RobotAdapter + 喂... 不再喂 Dialer，Dialer 改吃 metaAdp）。即启动路径**同时**构造 LuaAdapter（给 Robot 派生 + encode）和 CodecResolver（给 dial/decode）。双 codec 过渡，2-C3 删 LuaAdapter。

### 2. ManagerConfig + NewRobot（robot/manager.go, robot/robot.go）

- `ManagerConfig`：加 `CodecResolver adapter.CodecResolver \`json:"-"\``；保留 `Adapter *adapter.LuaAdapter`。
- `Manager`（`manager.go:129` NewRobot 调用）：多传 `m.cfg.CodecResolver`。
- `NewRobot` 签名：加 `resolver adapter.CodecResolver` 入参（建议放 globalAdp 后）；`Robot` 加 `resolver` 字段（`robot.go:54` 附近）；`r.resolver = resolver`（NewRobot 内）。**不动** `r.adp = robotAdp`（`:151`）。

### 3. Robot.DialTCP/DialUDP resolve（robot.go:396/443）

```go
// ConnectTCP 内（原 r.dialer.DialTCP(r.ctx, address, conn, r.adp)）：
adp := r.resolver.Resolve("tcp:" + service)
if adp == nil {
    return engine.NewActionError(errcode.ErrConnNotFound,
        "tcp:"+service+" 无 codec 配置（resolver 未映射）")
}
if err := r.dialer.DialTCP(r.ctx, address, conn, adp); err != nil { ... }
```
（UDP 同理 `"udp:"+service`。`service` 来自 ConnectTCP/UDP 的入参——确认 ConnectTCP(service, address) 签名已有 service。）nil→fail loud，**不兜底**。

### 4. 删 dial 兜底（gnet.go:366-368）

```go
// 原：if adp == nil { adp = d.server.adp }
// 改：adp 由上层 Robot Resolve 保证非 nil；dial 内若 adp==nil 记 Error 并返回错误（防御性，不兜底）。
```
`d.server.adp`（EventServer.adp）保留作 OnTraffic 元信息源（NewDialer 注入的 metaAdp）；只是 dial 不再拿它当 decode adp 兜底。

### 5. Connection（不动）

`StartDecodeLoop(adp, isUDP)` / `c.adp` / `decodeLoop` 调 `c.adp.DecodeTCP/UDP` 全部不变——adp 由 dial 注入（现在是 Go SchemaAdapter，无 luaMu）。

## 不变量

- **decode 行为零回退**：dial 时 Resolve 出的 Go SchemaAdapter 注入 Connection，decode 用它；字节级与旧 Lua RobotAdapter 一致（T1 冻结对拍）。
- **encode/心跳/listen/Lua 不动**：仍走 `r.adp`（Lua RobotAdapter），行为不变。
- **双 codec 过渡**：decode Go（codec.json）/ encode Lua（codec.lua），T1 已证字节一致；2-C2 把 encode 也切 resolver 后闭环。
- **fail loud**：缺 codec 映射（Resolve nil）、adapter 目录无 codec 文件 → 中文 error，不静默兜底。

## 关键约束

- **不写兼容兜底**：Resolve nil 直接报错；InferCodecMap 空目录报错；删 dial 兜底。
- **新字段全链路一致**：CodecResolver 从启动 → ManagerConfig → NewRobot → Robot.resolver → Dial Resolve，一处不漏。
- **保留 r.adp / NewRobotAdapter / ManagerConfig.Adapter**（2-C1 不删，Lua/encode 还用；2-C3 删）。
- Go 最佳实践：godoc；错误用 NewActionError 体系（优先复用 ErrConnNotFound 等，避免不必要新码）；日志中文。
- 仅改 cmd/agent/main.go、agent/task_runner.go、robot/{manager,robot}.go、network/gnet.go（+ 可选 adapter/ InferCodecMap helper）。**不动 engine/action.go（encode）、script/、network/connection.go、network/heartbeat.go**。
- **Windows 环境注**：`gofmt -l` 标 .go 脏是 autocrlf CRLF，**不要**对单文件 `gofmt -w`；校验 canonical 用 `sed 's/\r$//' f.go | gofmt -l`。
- **不要 git commit。**

## 工作方式（TDD）

1. **先读**：`adapter/codec_resolver.go`（LoadCodecResolver 签名）、`cmd/agent/main.go`（runStandalone 构造 adp + ManagerConfig + NewDialer，:197-296）、`agent/task_runner.go`（:125-242）、`robot/manager.go`（ManagerConfig + NewRobot 调用 :129）、`robot/robot.go`（NewRobot 签名 + Robot 字段 + DialTCP/UDP :396/443 + ConnectTCP/UDP service 入参）、`network/gnet.go`（NewDialer + dial 兜底 :332-368 + OnTraffic 元信息 :163/178）。**确认 3 份 codec.json HeaderSize 一致**（grep frame/headerSize）。
2. RED：
   - `adapter/codec_resolver_test.go`（或新 test）：`InferCodecMap` 用 T1.6 产物 `conf/adapter/` 推断出 `{tcp:logic, tcp:battle, udp:battle}`；空目录→error。
   - `robot`（若有 dial 测试）或集成：Robot.DialTCP 对未映射 server（如 `tcp:xxx`）返回 fail-loud error；对映射 server Resolve 出非 nil Go adapter 注入 Connection。
3. GREEN：InferCodecMap + 启动接 resolver + ManagerConfig/NewRobot + Dial Resolve + 删 dial 兜底。
4. `go build ./...`、`go vet ./...`、`go test ./adapter/... ./robot/... ./network/... ./engine/... -count=1` 全绿。
5. 全仓确认：`r.resolver` 全链路就位；`d.server.adp` 兜底已删；`r.adp` 保留（encode/Lua 用）。
6. **不要 git commit。**

## 验收（self-review）

- `InferCodecMap` + `LoadCodecResolver` 在 main.go/task_runner 接上 runtime（T4 resolver 首次接线）。
- `ManagerConfig.CodecResolver` + `Robot.resolver` 全链路；`Robot.DialTCP/UDP` 按 `<proto>:<service>` Resolve（nil→fail loud）注入 Connection。
- `Dialer.dial` `server.adp` 兜底删除；Dialer 元信息源用 resolver 任一 adapter（HeaderSize 一致前提）。
- decode 用连接固定 Go SchemaAdapter（无 luaMu）；encode/心跳/listen/Lua 仍走 `r.adp`（**未动**，2-C2/2-C3）。
- go build/vet/test 全绿。

## ⚠️ 运行时验证依赖

多连接 codec 解析（`tcp:logic`/`tcp:battle`/`udp:battle` 各用正确 codec）+ dial 注入正确性需真实服务端连接验证（单测只能覆盖 Resolve nil/fail-loud）。报告列为「待运行时验证」。

## 报告

写完整报告到 `plans/declarative-codec/reports/t2-c1-report.md`：实现内容、Cut A 边界（dial/decode + 启动 + 删兜底；保留 r.adp）、InferCodecMap 推断、resolver 全链路、Dial Resolve + fail-loud、双 codec 过渡态说明、TDD、改动文件、self-review、**运行时验证待办**、concerns（HeaderSize 一致假设、双 codec 过渡、Dialer 元信息源取法）、**2-C2 接力**（encode 侧 ActionExecutor/心跳/listen 全切 resolver，闭环双 codec）。

返回（<15 行）：Status、改动文件、一行测试摘要、运行时验证待办、concerns、报告路径。
