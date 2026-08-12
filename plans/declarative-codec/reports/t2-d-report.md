# T2-D 报告 — 删除 luaMu / withReleasedMu / RunCallbackScript（T2 闭环）

> 状态：**DONE**（已运行时验证 PASS，已提交）。
> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）。
> 依赖：2-A（listen 主流程消费化）/ 2-B（声明式心跳）/ 2-C（codec 移出业务 LState）全部完成。

## 1. 目标与边界

T2-A/B/C 已清除 3 个异步 Lua 入口（listen 脚本回调 / 心跳 Lua builder / codec Lua encode·decode）。
本任务删除为历史并发 Lua 入口服务的互斥锁与临时释放机制，使**业务 LState 成为唯一被 Robot 主流程
goroutine 触碰的 VM**（async Lua 入口 #4：业务阻塞 Lua 自身——单所有者模型下不再需要丢锁）。

**严格边界**：仅删锁与配套清理，**不做** codec 包契约改动、**不写**兼容兜底、**不把阻塞调用改成
后台 goroutine 等待。文件：`script/{api_network,api_share,api_utils,runtime}.go`、
`robot/{robot,manager,listen_resolver_test}.go`、`network/{connection,gnet,heartbeat}.go`、
`engine/{action,flow}.go`、`adapter/lua_adapter.go`、`agent/config.go`、`agent/task_runner.go`、
`cmd/agent/main.go`、前端 `cmd/web/.../luaApiSpec.ts`（spec 清理）、架构 docs。

## 2. 前置审计闸门（删锁前）

静态审计确认业务 LState 仅被主流程触碰：

- **listen 脚本回调**：`ListenDef.script` 仅作配置字段 + `validateListenDef` fail-loud；`createListenCallback`
  仅 Go 原生 parse + state store；`RunCallbackScript` 异步入口删除。✅
- **心跳 Lua builder**：`netSenderAdapter.RegisterHeartbeat` 的 `goBuilder` 为 Go-only
  （`BuildProtoBody` / `BuildHeartbeatBody` + `resolver.Encode`），pump 单 goroutine 同步调用，不碰业务 LState。✅
- **codec 生产路径**：`RobotAdapter` / `NewRobotAdapter` / `loadAdapterModule` / `Context.Adapter` /
  `*Locked` codec 方法生产零残留（2-C2-Lua 已删）；`adapter.LuaAdapter` 仅 T1 测试 oracle（独立 LState 池，
  不碰 `r.l`）。✅
- **异步 goroutine 访问业务 LState**：仅主流程 `RunActionScript` / `RunBooleanScript` 用 `h.robot.l`
  （executor goroutine 独占）。✅

闸门通过 → 删锁安全（锁已保护不了任何活路径）。

## 3. 实现内容

### 3.1 `script/api_network.go` — 删 `withReleasedMu` + 阻塞直调

- 删 `withReleasedMu` helper（+ `sync` import），9 处调用（connect/close/request/listen/HTTP）改为
  当前 Robot 主流程 goroutine 直接阻塞调用底层 `NetSender` 方法。
- `networkListen` 等待改为同时监听消息事件、`ctx.Done()` 与 deadline，取消即时唤醒；`timedOut`
  仅在未取消时置位（区分 timeout 与 cancel，比旧实现更准）。
- try_*_listen 注释清理（去 luaMu 描述，语义不变）。

### 3.2 `script/api_share.go` — 20 处 `withReleasedMu` → 直调

阻塞型 Redis 调用（set/get/del/exists/expire/incr/claim/release/owner/renew/queue×4/hash×6）全部改为
当前主流程 goroutine 直接阻塞；`opContext`/`defer cancel` 从闭包内移到函数级，per-call 作用域不变，
无 ctx 泄漏。返回值与错误处理保持不变。

### 3.3 `script/api_utils.go` — `utils.sleep` 直等

`withReleasedMu` 包装 → 直接 `select { <-time.After(d) | <-ctx.Done() }`（ctx 为空回退 `time.Sleep`）。

### 3.4 `script/runtime.go` — Context 与 callback 入口

- 删 `Context.LuaMu *sync.Mutex` 字段。
- 删 `RunCallbackScript` 函数（无生产/测试调用方）。
- 注释清理（`resetMetrics` / `loadScriptFn` / `registerAPIs` 去掉 `onMessage` / `callback` / adapter Lua 模块描述）。

### 3.5 `robot/robot.go` — 删 `luaMu` 字段与锁点

- 删 `luaMu sync.Mutex` 字段；删 `Start()` 内 `SetContext` 周围 `Lock/Unlock`；删 `executeLuaAction` /
  `executeLuaBoolean` 内 `Lock/defer Unlock`（主流程 goroutine 天然独占 `r.l`）。
- 删 `SetContext` 的 `LuaMu: &r.luaMu` 赋值。
- wallClock 注释从「抢到 luaMu 后开始」改为「主流程同步执行总耗时」（`start := time.Now()` 位置不变，计时无回归）。
- **`RegisterListen` 缺失 listen 定义恢复 HEAD 行为**（见 §4 review 修正）：`if !ok { stresslog.Error(...); continue }`
  （跳过整条 entry，不注册队列）——T2-D 在 listen 语义上零变更。

### 3.6 `network/*` / `engine/*` / `adapter/*` / `agent/*` / `cmd/agent/main.go` — 注释清理

把旧的 `luaMu` / `TryLock` / `RobotAdapter` / `心跳 builder 抢锁死锁` 等描述改为当前事实
（connectionPump + Go-only builder + Go resolver codec）。`agent/config.go` `AdapterScript` 默认值
`"conf/adapter/codec.lua"` → `""` + 注释标「旧字段」（T2-C2 遗留孤儿，无生产读取方，**不删字段**——
删字段是更宽 scope，记入 ledger 待后续）。

### 3.7 前端 + 架构 docs

- `cmd/web/.../luaApiSpec.ts`：删 stale `tcp_listen`/`register_*_heartbeat`/`utils.sleep` 的 luaMu/TryLock 文案。
- `CLAUDE.md` / `README.md` / `docs/{adapter-layer,flow-node-system,error-code-system,visual-flow-editor,monitoring-system}.md`：
  清理 stale luaMu / TryLock / RobotAdapter / LuaAdapter 生产路径描述，docs/adapter-layer.md 重写为
  CodecResolver + SchemaAdapter / Go codec 现状。

### 3.8 测试

- `robot/listen_resolver_test.go`：`TestRegisterListen_MissingListenDef_NoHardFail`（守护：缺失 listen 定义
  不在 RegisterListen 阶段 hard-fail）。

## 4. Review 流程与修正

- **并行 code review（high effort，6 角度分两批）**：1 Important + 2 Minor + 1 Refuted。
  - **Important（已修）**：首次实现把 `RegisterListen` 缺失 listen 定义改成「注册 nil-cb 队列」并自报「queue-only
    *again*」——但 HEAD（T2-D 前）真实行为是 `stresslog.Error + continue`（跳过）。spec reviewer 让「revert」，
    修复却引入第三种语义。**修正**：恢复 HEAD 精确行为（`Error + continue`），让 T2-D 在 listen 语义零变更；
    测试改名 `..._NoHardFail` 并如实标注「仅锁定不 hard-fail，不区分跳过 vs 注册」。
  - **Minor（不动，记 ledger）**：① `agent/config.go` `AdapterScript` 半截子清理（字段成孤儿但未删）；
    ② `networkListen` 保留轮询形态（altitude：删锁时本可顺手推进队列事件驱动，属 T2-D scope 外）。
  - **Refuted**：前端 `luaApiSpec.ts` 改动——spec reviewer 明确要求的清理，非越界碰 T3。
- **spec 合规 review**：3 轮（首次发现 docs stale + scope creep → 修 → 再发现 adapter-layer/error-code/visual-flow
  docs 残留 → 修 → ✅ 通过）。

## 5. 验证

- `go build ./...` ✅　`go vet ./script ./robot ./network ./engine ./adapter` ✅
- `go test ./script ./robot ./network ./engine ./adapter -count=1` ✅（5 包全过）
- 静态审计闸门：生产代码 `luaMu`/`withReleasedMu`/`Context.LuaMu`/`TryLock`/`RunCallbackScript`/
  `RobotAdapter`/`NewRobotAdapter`/`loadAdapterModule`/`*Locked` codec 方法**零残留**（仅 `EncodeTCP`/`DecodeTCP`
  合法方法名命中）。

## 6. 运行时验证（PASS，2026-06-20）

`rm -f log/stressbot.log` + `go run ./cmd/agent -config conf/config.json`，单 bot vs localhost 真实服务端：

- `CodecResolver 已加载 connections:3 headerSize:12`；bot 登录 → PostLogin/SelectRole 成功 → 连逻辑服 → 进战斗。
- `BattleEnd` count=2（删锁后完整战斗跑通，~46s 到达，BattleReward 正常结算）。
- CLAUDE.md step6 `grep -iaE "error|warn|失败" log/stressbot.log | grep -av headError`：**空**。
- 回归签名（`panic:`/`nil pointer`/`goroutine [running`/`CONN_DROPPED`/`codec 未映射`/`encode 失败`/
  `decode 失败`/`luaMu`/`释放 Lua 锁`）：**0**。
- 比上次 Phase-2 更干净（连服务端 `HeroAwake` 业务错误都没出现）。

**结论**：删锁后心跳 goroutine / connectionPump / 业务主流程三者独立运行正确，主流程阻塞期间
网络收发/心跳不受影响（codec 纯 Go、不依赖 luaMu）。**T2-D 闭环，T2 全轨结束。**

## 7. 文件清单

`script/{api_network,api_share,api_utils,runtime}.go`、`robot/{robot,manager,listen_resolver_test}.go`、
`network/{connection,gnet,heartbeat}.go`、`engine/{action,flow}.go`、`adapter/lua_adapter.go`、
`agent/config.go`、`agent/task_runner.go`、`cmd/agent/main.go`、`cmd/web/src/components/FlowEditor/lua/luaApiSpec.ts`、
`CLAUDE.md`、`README.md`、`docs/{adapter-layer,error-code-system,flow-node-system,visual-flow-editor,monitoring-system}.md`、
`plans/declarative-codec/{briefs/t2-d-brief.md, reports/t2-d-report.md}`。
