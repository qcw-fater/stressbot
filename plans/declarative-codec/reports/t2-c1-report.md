# T2-C1 报告 — CodecResolver 替换 per-robot adapter（dial/decode 侧）

> 状态：**DONE**（工作树未提交，待批次确认）。
> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）。
> ⚠️ 多连接 codec 解析 + dial 注入正确性需**运行时验证**（单测仅覆盖 Resolve nil/fail-loud）。

## 1. 实现内容

### 1.1 边界（Cut A — dial/decode + 启动 + 删兜底）

把 **dial/decode 侧**从 per-robot 单一 Lua adapter（`r.adp`，`RobotAdapter`）切到 **CodecResolver**
（按 `<proto>:<service>` server 串解析的 Go `SchemaAdapter`）：拨号前 Resolve 取 adapter 注入 Connection，
decode 用连接固定的 Go adapter。**删 `Dialer.dial` 的 `server.adp` 兜底**。

**r.adp（RobotAdapter）保留**——encode/心跳/listen routeKey/业务 Lua `Context.Adapter` 仍走它
（→ 2-C2 切 encode、2-C3 删 Lua 整条）。故 2-C1 后 Robot 同时持 `r.adp`（Lua，encode 用）+
`r.resolver`（Go，dial/decode 用）——**双 codec 过渡态**（decode Go codec.json / encode Lua codec.lua，
T1 已证字节级一致，2-C2 闭环）。

### 1.2 改动点

1. **`adapter/codec_resolver.go`（新增 `InferCodecMap` + `PickMetaAdapter`）**
   - `InferCodecMap(codecDir string) (map[string]string, error)`：扫 `*_codec.json`（排除 errors.json /
     codec.lua / error.lua），文件名去 `_codec.json` 后缀 → 按首个 `_` 拆 `<proto>_<service>` →
     server 串 `<proto>:<service>` → map[server]filename。空目录 / 无 codec 文件 → 中文 error（fail loud）。
     文件名无 `_` / 首末字符即 `_` → error（拆不出 proto:service）。
   - `PickMetaAdapter(resolver, codecMap)`：按 server 串排序取首个 Resolve，作为 gnet
     Dialer/EventServer 的 OnTraffic 元信息源（HeaderSize/BodyLength 帧切割）。codecMap 空 → nil
     （启动期 NewEventServer 首次 OnTraffic panic 暴露问题，不兜底）。

2. **`robot/manager.go`（`ManagerConfig` 加 `CodecResolver`）**
   - 新增 `CodecResolver adapter.CodecResolver \`json:"-"\``；**保留** `Adapter *adapter.LuaAdapter`
     （仍派生 RobotAdapter 给 encode/Lua 用，2-C3 删）。
   - `startBatch` 的 `NewRobot` 调用多传 `m.cfg.CodecResolver`。

3. **`robot/robot.go`（`NewRobot` 加 resolver 入参 + `Robot.resolver` 字段 + Dial Resolve）**
   - `NewRobot` 签名增加 `resolver adapter.CodecResolver`（放 `globalAdp` 后）；`resolver==nil` → error
     （dial/decode 侧 codec 未配置）。**保留** `globalAdp==nil` 检查 + `r.adp = robotAdp` 派生路径。
   - `Robot` 新增 `resolver adapter.CodecResolver` 字段；**保留** `adp *adapter.RobotAdapter`。
   - `ConnectTCP(serviceName, address)`：server = `"tcp:"+serviceName`；`adp := r.resolver.Resolve(server)`；
     **nil → fail loud**（中文 Error 日志带 server+service+addr + `monitor.ConnFailed()` + `CloseTCP` + 返回 false，
     不拨号）；非 nil → `r.dialer.DialTCP(ctx, addr, conn, adp)`。
   - `ConnectUDP` 同构（`"udp:"+serviceName`）。
   - fail-loud 用返回 false（与 `ConnectTCP/UDP` 现有 error 处理风格一致：返回 bool，错误日志 +
     monitor 计数），非 `NewActionError`（这些函数不在 ActionError 返回链上）。

4. **`network/gnet.go`（删 `dial` 兜底）**
   - 删 `if adp == nil { adp = d.server.adp }`。
   - 替换为**防御性 nil-guard**：adp==nil 返回中文 error（带 network+address+service 上下文），
     不静默回退默认 codec。`d.server.adp`（EventServer.adp）保留作 OnTraffic 元信息源（NewDialer 注入的 metaAdp）。
   - `DialTCP/DialUDP/dial` 的 `adp adapter.Adapter` 入参保留（Robot Resolve 后传入）。
   - godoc 更新：adp 由上层 Robot.ConnectTCP/UDP 经 CodecResolver.Resolve 注入（nil → 已 fail loud）。

5. **`cmd/agent/main.go`（单机：构造 resolver + 接 runtime）**
   - LuaAdapter 路径**保留**（encode/心跳/listen/Lua 仍用）。新增 `adapter.InferCodecMap(paths.Adapter)` +
     `adapter.LoadCodecResolver(paths.Adapter, codecMap, "errors.json")` 构造 resolver。
   - `ManagerConfig` 加 `CodecResolver: resolver`（`Adapter: adp` 保留）。
   - `NewDialer` 元信息源改用 `adapter.PickMetaAdapter(resolver, codecMap)`（Go SchemaAdapter）。

6. **`agent/task_runner.go`（Agent：同构）**
   - codec 目录 = 任务下发的 `confDir/adapter`（T4.3 分发的 `*_codec.json` + `errors.json`）。
   - 同构 InferCodecMap + LoadCodecResolver；`ManagerConfig.CodecResolver` + `NewDialer(PickMetaAdapter)`。

### 1.3 不变量

- **decode 行为零回退**：dial 时 Resolve 出的 Go SchemaAdapter 注入 Connection，decode 用它；
  字节级与旧 Lua RobotAdapter 一致（T1 冻结对拍）。
- **encode/心跳/listen/Lua 不动**：仍走 `r.adp`（Lua RobotAdapter），行为不变。
  - `r.actionExec = NewActionExecutor(..., r.adp, ...)`（encode，robot.go:179 未动）
  - `Context.Adapter: r.adp`（业务 Lua，robot.go:223 未动）
  - `h.robot.adp.ExpectedRouteKey(ref.Route)`（listen routeKey，robot.go:794 未动）
  - `ns.robot.adp`（心跳 encode，robot.go:1299 未动）
- **双 codec 过渡**：decode Go（codec.json）/ encode Lua（codec.lua），T1 已证字节一致。
- **fail loud**：缺 codec 映射（Resolve nil）、adapter 目录无 codec 文件 → 中文 error，不静默兜底。
- **HeaderSize 全局一致**（前提）：生产 3 份 codec.json 同 frame spec（headerSize=12，T1.6 同源生成），
  故 EventServer 持单一 metaAdp 即可；per-connection HeaderSize 下沉留到 2-C3 connectionPump。

## 2. InferCodecMap 推断规则

| 文件名 | stem（去 `_codec.json`） | 首个 `_` 拆 | server 串 |
|---|---|---|---|
| `tcp_logic_codec.json` | `tcp_logic` | tcp / logic | `tcp:logic` |
| `tcp_battle_codec.json` | `tcp_battle` | tcp / battle | `tcp:battle` |
| `udp_battle_codec.json` | `udp_battle` | udp / battle | `udp:battle` |

service 内部允许含 `_`（如 `tcp_rank_team_codec.json` → `tcp:rank_team`，按**首个** `_` 拆）。
`errors.json` / `codec.lua` / `error.lua` 不匹配 `*_codec.json` 后缀，不被收。

## 3. resolver 全链路

```
main.go runStandalone / task_runner Run
  ├─ InferCodecMap(adapterDir) → codecMap          # 扫 *_codec.json
  ├─ LoadCodecResolver(adapterDir, codecMap, "errors.json") → resolver
  ├─ ManagerConfig{Adapter: adp(Lua), CodecResolver: resolver(Go)}  # 双 codec 过渡
  ├─ NewDialer(PickMetaAdapter(resolver, codecMap), hbInterval)     # OnTraffic 元信息源
  └─ NewManager(mgrCfg, ...) → startBatch → NewRobot(..., globalAdp, resolver, ...)
        └─ Robot.resolver = resolver
              ├─ ConnectTCP(svc, addr): adp = resolver.Resolve("tcp:"+svc); nil→fail-loud; 非 nil→DialTCP(adp)
              └─ ConnectUDP(svc, addr): adp = resolver.Resolve("udp:"+svc); nil→fail-loud; 非 nil→DialUDP(adp)
                    └─ Dialer.dial(adp) → conn.StartDecodeLoop(adp) → c.adp = adp → decodeLoop 用 Go SchemaAdapter
```

## 4. TDD（RED → GREEN）

### 4.1 RED（测试先行，确认编译期失败）

- `adapter/codec_resolver_test.go` 新增 6 个 `InferCodecMap` 测试（undefined → build failed）。
- `robot/dial_resolver_test.go` 新增 3 个 Dial Resolve 测试（stubResolver / fakeAdapter）。

### 4.2 GREEN（实现 → 全 PASS）

| 测试 | 期望 | 结果 |
|---|---|---|
| `TestInferCodecMap_RealAdapterDir` | conf/adapter 推断出 {tcp:logic, tcp:battle, udp:battle} | PASS |
| `TestInferCodecMap_RoundTripWithLoader` | 推断 map 喂 LoadCodecResolver，三 server Resolve 非 nil | PASS |
| `TestInferCodecMap_EmptyDir` | 空目录 → 中文 error | PASS |
| `TestInferCodecMap_MissingDir` | 目录不存在 → error | PASS |
| `TestInferCodecMap_IgnoresNonCodecFiles` | 只有 errors.json/codec.lua/error.lua → error | PASS |
| `TestInferCodecMap_SkipsErrorsJson` | 1 codec + errors.json → 只推断 1 个 | PASS |
| `TestConnectTCP_ResolverNil_FailLoud` | Resolve nil → 返回 false、不触达 dialer、占位连接已清理 | PASS |
| `TestConnectUDP_ResolverNil_FailLoud` | UDP 同构 | PASS |
| `TestConnectTCP_ResolverHit_ResolveNonNil` | Resolve 命中非 nil / 未映射 nil | PASS |

## 5. 改动文件

| 文件 | 改动 |
|---|---|
| `adapter/codec_resolver.go` | 新增 `InferCodecMap` + `PickMetaAdapter`（+ godoc 更新 package 注释） |
| `adapter/codec_resolver_test.go` | +6 个 InferCodecMap 测试 |
| `robot/manager.go` | `ManagerConfig.CodecResolver` 字段 + `startBatch` 传参 |
| `robot/robot.go` | `NewRobot` 加 resolver 入参 + `Robot.resolver` 字段 + `ConnectTCP/UDP` Resolve fail-loud |
| `network/gnet.go` | 删 `dial` 的 `server.adp` 兜底 → 防御性 nil-guard（带上下文 error） |
| `cmd/agent/main.go` | 构造 resolver + 接 ManagerConfig + NewDialer(PickMetaAdapter) |
| `agent/task_runner.go` | 同构（任务下发 adapter 目录） |
| `robot/dial_resolver_test.go`（新） | 3 个 Dial Resolve fail-loud 测试 + stubResolver/fakeAdapter |
| `robot/main_test.go`（新） | robot 包 TestMain（初始化 stresslog + monitor） |

**未触碰**：`engine/action.go`（encode 侧）、`script/`（业务 Lua / Context.Adapter / loadAdapterModule /
RobotAdapter/LuaAdapter）、`network/connection.go`、`network/heartbeat.go`、`adapter/robot_adapter.go`、
`adapter/lua_adapter.go`、前端/admin。未 git commit。

## 6. Self-review（对照 brief 验收清单）

- [x] `InferCodecMap` + `LoadCodecResolver` 在 main.go/task_runner 接上 runtime（T4 resolver 首次接线）。
- [x] `ManagerConfig.CodecResolver` + `Robot.resolver` 全链路；`Robot.DialTCP/UDP` 按 `<proto>:<service>`
      Resolve（nil → fail loud）注入 Connection。
- [x] `Dialer.dial` `server.adp` 兜底删除；Dialer 元信息源用 `PickMetaAdapter`（resolver 任一 adapter，
      HeaderSize 一致前提）。
- [x] decode 用连接固定 Go SchemaAdapter（无 luaMu）；encode/心跳/listen/Lua 仍走 `r.adp`（**未动**，2-C2/2-C3）。
- [x] **保留** r.adp / NewRobotAdapter / ManagerConfig.Adapter（2-C1 不删）。
- [x] fail loud 不兜底：Resolve nil 中文 error + ConnFailed；InferCodecMap 空目录 error；dial 删兜底。
- [x] 新字段全链路一致：CodecResolver 启动 → ManagerConfig → NewRobot → Robot.resolver → Dial Resolve。
- [x] `go build ./...` + `go vet ./...` + `go test ./... -count=1` 全绿。
- [x] gofmt canonical（`sed 's/\r$//' f.go | gofmt -l` 空）—— 改动文件全部 CLEAN；
      `agent/task_runner.go` 的 DIRTY 是**预存 BOM**（HEAD 即有，`efbbbf`，非本次引入，按 Windows 环境注不顺手修）。
- [x] 未 git commit（禁止 add/commit）。

## 7. ⚠️ 运行时验证待办

单测只覆盖 InferCodecMap 推断 + Resolve nil/fail-loud。以下需 controller/用户跑真实服务端验证：

1. **多连接 codec 解析**：`tcp:logic` / `tcp:battle` / `udp:battle` 各连接 dial 时用**正确**的 codec
   （非混用）。验证方法：跑 `go run ./cmd/agent -config conf/config.json` 2~5 分钟，
   - 机器人能正常建连（无「TCP/UDP 连接无 codec 配置」error 日志）；
   - 请求-响应匹配正常（routeKey 正确）；
   - 监听推送 store/route 正确（listen routeKey 由 r.adp 算，与 decode 的 Go adapter 一致——T1 已证字节级一致）。
2. **dial 注入正确性**：连接生命周期内 `c.adp` 是 Resolve 出的 Go SchemaAdapter，
   decodeLoop 用它解码无字节回退（与旧 Lua RobotAdapter 行为对齐）。
3. **可与其他运行时验证合并**：2-A2.2（battle）、2-B.2（battle UDP 心跳）已列运行时验证待办，
   本任务的连接/codec 验证可同一次 `go run` 覆盖。
4. **Dialer 元信息源假设**：当前协议 HeaderSize 全局一致（3 份 codec.json 同 frame spec）。
   若未来某连接引入不同 HeaderSize，需把 per-connection HeaderSize 下沉到 Connection（2-C3 connectionPump 范围）。

验证步骤（CLAUDE.md §验证流程）：
```
rm -f log/stressbot.log
go run ./cmd/agent -config conf/config.json
# 运行 2~5 分钟
# grep -i "error\|warn\|失败" log/stressbot.log | grep -v "headError"  # 应无「无 codec 配置」类异常
```

## 8. Concerns

1. **HeaderSize 一致假设**：Dialer/EventServer 持单一 metaAdp（`PickMetaAdapter` 取 resolver 任一 adapter），
   依赖生产 3 份 codec.json headerSize=12 一致（T1.6 同源生成，已 grep 核对）。多 codec 但 HeaderSize 不一致
   的场景，per-connection HeaderSize 下沉到 Connection 留到 2-C3 connectionPump。当前不在 2-C1 范围。
2. **双 codec 过渡态**：decode Go（codec.json）/ encode Lua（codec.lua）。T1 已证 encode/decode 对 codec.lua
   字节级一致（TCP 13+UDP 9 encode / TCP 10+UDP 5+UDP 压密 2 decode / 生产 codec.json 6 case），
   故双 codec 过渡无字节回退风险。2-C2 把 encode 也切 resolver 后闭环，2-C3 删 Lua 整条。
3. **Dialer 元信息源取法**：`PickMetaAdapter` 按 server 串排序取首个（可复现）。理论上取任一均可
   （HeaderSize 一致前提）；排序取首个仅为可复现性，无功能差异。
4. **agent/task_runner.go 预存 BOM**：HEAD 即有（非本次引入），gofmt 标 DIRTY 仅因 BOM。
   按 Windows 环境注 + 「不顺手修预存格式问题」原则保留；若批次提交时 controller 认为该清理，
   单独一次 `sed -i '1s/^\xEF\xBB\xBF//' agent/task_runner.go` 即可（不属本任务范围）。
5. **Dial fail-loud 返回 error 而非 panic**：`Dialer.dial` 的 nil-guard 返回 error（由 Robot.ConnectTCP
   记 Warn 日志 + ConnFailed + 返回 false）。这是防御性设计——上层 Robot 已 Resolve 校验非 nil，
   dial 内 adp 必非 nil；nil-guard 仅防编程错误（如未来有非 Robot 路径直接调 DialTCP）。
6. **fakeAdapter 测试桩**：`robot/dial_resolver_test.go` 的 fakeAdapter 仅满足 adapter.Adapter 接口签名，
   方法无正确行为（fail-loud 路径在 codec 方法被调用之前即返回；resolver 命中路径只验 Resolve 非 nil，
   不调 codec 方法）。若 2-C2/2-C3 需测 encode/decode，应改用真实 SchemaAdapter（`LoadCodecResolver` 路径）。

## 9. 2-C2 接力（encode 侧全部按 server 串解析）

2-C1 后 Robot 持双 codec（decode Go / encode Lua）。2-C2 把 **encode 侧**也切 resolver，闭环双 codec：

| 当前（2-C1 后） | 2-C2 改成 |
|---|---|
| `ActionExecutor.adp`（`engine/action.go` `protocolEncode` / `ExpectedRouteKey`） | `ActionExecutor.resolver`；按 `<proto>:<service>` Resolve 后 Encode/ExpectedRouteKey（transport 由 pattern 推导 + `def.Service` 拼 server 串） |
| 心跳 goBuilder `ns.robot.adp`（`robot.go:1299`） | resolver.Resolve(server) 后 Encode |
| listen routeKey `h.robot.adp.ExpectedRouteKey`（`robot.go:794`） | resolver.Resolve(server).ExpectedRouteKey |
| `script.Context.Adapter`（`runtime.go:42`） | 删除或改 resolver helper（→ 2-C3） |

2-C2 完成后 encode/decode 全程无 Lua codec，2-C3 可删 `r.adp` / `NewRobotAdapter` / `ManagerConfig.Adapter` /
`adapter/robot_adapter.go` + connectionPump 替换三协程，2-D 删 luaMu/withReleasedMu。
