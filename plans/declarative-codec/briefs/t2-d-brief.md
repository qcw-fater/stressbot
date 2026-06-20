# T2-D Brief — 删除 luaMu / withReleasedMu

## 状态与前置

权威恢复入口是 `plans/declarative-codec/progress-ledger.md`。截至 2026-06-18：T1 + T4 + T2-A/B/C 已完成、review clean、已提交并运行时验证通过。当前 T2 唯一 pending 是 **2-D：删除 luaMu / withReleasedMu**。

本任务必须在当前 worktree 执行：

`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`

不要切回主仓，不要自动 commit。完成后只报告改动文件、测试与审计结果；批次提交由 controller 征得用户确认后处理。

## 目标

T2-A/B/C 已清除 3 个异步 Lua 入口：listen 脚本回调、心跳 Lua builder、codec Lua encode/decode。业务 LState 现在应只由 Robot 主流程 goroutine 触碰。本任务删除为历史并发 Lua 入口服务的互斥锁与临时释放机制：

- 删除 `Robot.luaMu` 字段及主流程 action/boolean Lua 调用周围的 `Lock/Unlock`。
- 删除 `script.Context.LuaMu` 字段与初始化赋值。
- 删除 `withReleasedMu` helper 及全部调用点。
- 阻塞型 Lua API 直接阻塞当前 Robot 主流程 goroutine，并继续响应 `ctx.Done()`。
- 删除旧 listen callback 执行入口 `RunCallbackScript`（若仅剩无人调用）。
- 清理所有生产代码里的旧注释/日志：`luaMu`、`withReleasedMu`、`TryLock`、`释放 Lua 锁`、`获取 Lua 锁失败`、`RobotAdapter` 旧生产路径等。测试 oracle 注释若位于 `adapter/lua_adapter.go` 且明确说明仅测试用途，可保留或精简，但生产路径不能暗示仍存在 RobotAdapter。

## 前置审计闸门（未通过不得删锁）

先做静态审计并在报告中列出结果：

1. 确认 listen 脚本 callback 已下线：`ListenDef.script` 只允许作为配置字段 + fail-loud 校验，不允许有异步 callback 调用路径。
2. 确认 heartbeat Lua builder / `TryLock(luaMu)` / 保存 Lua function 的心跳注册路径归零。
3. 确认 codec 生产路径已切 Go resolver：`RobotAdapter` / `NewRobotAdapter` / `loadAdapterModule` / `Context.Adapter` / `*Locked` codec 方法无生产调用。
4. 全仓搜索异步 goroutine 访问业务 LState 的路径：只允许 Robot 主流程的 `RunActionScript` / `RunBooleanScript` 使用 `h.robot.l`；adapter 包 `LuaAdapter` 仅是 T1 测试 oracle，不属于业务 LState。

初始已知 grep（当前 worktree）仍有：

- `script/api_network.go`：`withReleasedMu` 定义 + 9 处调用，且多处历史 luaMu 注释。
- `script/api_share.go`：`withReleasedMu` 20 处调用 + 文件头历史注释。
- `script/api_utils.go`：`utils.sleep` 1 处 `withReleasedMu` + 历史注释。
- `script/runtime.go`：`RunCallbackScript` 仍存在；`Context.LuaMu` 预计仍存在。
- `robot/robot.go`：`luaMu` 字段、初始化、action/boolean 执行锁点、历史注释。
- `network/*` / `engine/action.go` / `adapter/lua_adapter.go` 有若干历史注释需按生产语义清理。

## 实施切片

### 1. 删除 `withReleasedMu`，保留阻塞与取消语义

- `script/api_network.go`：TCP/UDP connect/close/request/listen/HTTP 等阻塞 API 改为直接调用底层阻塞操作。必须继续遵守现有 `ctx.Done()` / timeout / cancel 语义，不新增后台 goroutine。
- `script/api_share.go`：Redis/share 阻塞调用直接阻塞当前 Lua 主流程 goroutine。保留现有返回值与错误处理；取消时仍由现有 context-aware share client 返回。
- `script/api_utils.go`：`utils.sleep` 改为直接 `select { case <-time.After(d): case <-ctx.Done(): }` 的当前 goroutine 等待，不释放锁。
- 删除 `withReleasedMu` helper 与 `sync` import（如不再需要）。

### 2. 删除 Context 与 Robot 的 Lua 锁

- 删除 `script.Context.LuaMu` 字段。
- 删除 `robot.SetContext` 初始化里的 `LuaMu: &r.luaMu`。
- 删除 `Robot.luaMu` 字段。
- 删除 `ExecuteLuaAction` / boolean Lua 条件周围 `Lock/Unlock`。
- 调整 wallClock/注释：Lua action 计时不再围绕“抢到 luaMu 后”描述；改为主流程同步执行的计时语义。

### 3. 删除旧 callback 执行入口

- 若 `RunCallbackScript` 无生产/测试调用，删除函数。
- 清理相关注释，避免未来误用旧 listen callback 入口。

### 4. 网络关闭与停止路径注释清理

- `network/connection.go` / `network/heartbeat.go` / `network/gnet.go` 中旧的 luaMu / RobotAdapter / 心跳 builder 抢锁注释改为当前事实：connectionPump + Go-only heartbeat builder + Go resolver codec。
- 不做大规模行为重构；只清理与删锁直接相关的过时约束。

### 5. 静态零残留验收

生产代码（历史 plan/docs 除外）应满足：

- `luaMu`、`withReleasedMu`、`Context.LuaMu`、`TryLock` 归零或仅保留在历史文档/测试 oracle 说明中；生产注释不应出现旧锁模型。
- `RunCallbackScript` 无残留。
- `RobotAdapter` / `NewRobotAdapter` / `loadAdapterModule` / `*Locked` codec 方法无生产路径残留；如果 `adapter/lua_adapter.go` 的注释提到历史项，必须明确是“已删，仅测试 oracle 背景”，避免误导。

## TDD / 验证要求

这是删除锁的重构任务，先用“静态审计 RED”锁定当前不满足项，再改到 GREEN：

1. 修改前记录 grep/audit 命中，确认当前确实存在待删除符号。
2. 修改后复跑针对性 grep，证明生产代码清零。
3. 跑 Go 验证：
   - `go test ./script ./robot ./network ./engine ./adapter -count=1`
   - `go test ./... -count=1`
   - `go vet ./...`
   - `go build ./...`
4. 如环境限制导致某项无法运行，如实报告命令、失败输出与原因。
5. 不要运行 `gofmt -w` 试图修 CRLF；本 worktree Windows `core.autocrlf=true` 会让 `gofmt -l` 全仓假阳性。只在确需格式化内容时使用不会造成大面积换行 churn 的方式，或报告文件 canonical 内容。

## 不要做

- 不要恢复任何兼容性 fallback。
- 不要新增迁移函数或旧 API 空实现。
- 不要把阻塞调用改成后台 goroutine 等待。
- 不要改 codec 包外部契约。
- 不要提交 commit。
- 不要触碰无关前端/T3 工作。

## 报告格式

- Status: DONE / DONE_WITH_CONCERNS / BLOCKED / NEEDS_CONTEXT
- 前置审计结果
- 实现摘要
- 验证命令与结果
- 静态 grep 零残留结果
- 文件变更清单
- 自审结论与 concern
