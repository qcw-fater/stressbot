# T2-C3-connectionPump Brief — 三协程合并为单 pump

> 你是 implementer，与另一 agent（2-C2-Go encode，engine/+robot/）**并行**跑。**严格文件边界：只改 `network/connection.go` + `network/gnet.go` + `network/heartbeat.go`，绝不碰 `engine/`、`robot/`、`script/`、`adapter/`**（另一 agent 在改 engine/robot）。
> 参考：`plans/declarative-codec/02-track-backend-integration.md` §2-C（切片 7 connectionPump + pump 伪代码）+ §4「不需要改动的点」、`reports/t1-freeze-handoff.md`（SchemaAdapter 并发安全）、`progress-ledger.md` §「全局约束」+ §「gofmt/换行 环境注」。
> 工作目录：worktree 根。**不要 git commit。**

## 目标

把每条连接的三个 goroutine（`decodeLoop` + `listenLoop` + 心跳 `runHeartbeat`）合并为**单一 `connectionPump`**：统一处理 inbound decode→response/listen 分发、heartbeat timer/control。pump 是 network 内部调度，**不泄漏到 flow/engine/Lua**。

## 现状（已读码核对）

- `decodeLoop`（`connection.go`，`StartDecodeLoop(adp,isUDP)` 启动）：从 `decodeCh` 消费 raw frame → `c.adp.DecodeTCP/UDP` → `decodeAndDispatch`（responseMap 优先，否则 `dispatchListen`）。per-connection goroutine，不持 robot luaMu（c.adp 是 Go SchemaAdapter）。
- `listenLoop`（`connection.go:285`，`AddListener`/`ListenResponse` CAS 启动）：从 `listenCh` 消费 → `dispatchListen`（cb!=nil 跑回调；cb==nil 写 listenQueues）。
- 心跳 `runHeartbeat`（`heartbeat.go:90`，`RegisterHeartbeat` 启动）：ticker → `cfg.Builder()` → `c.Send`。独立 goroutine。
- `gnet.go OnTraffic`：切帧 → 入 `decodeCh`（经 `EnqueueInbound`/类似）；`dial` 调 `conn.StartDecodeLoop(adp, isUDP)`（gnet.go:390）。
- robot/ 调 network/ 的接口：`Client.GetTCPConn/UDPConn`、`Dialer.DialTCP/UDP`、`Connection.RegisterListen/GetListenResp/RegisterHeartbeat/Send/SetOnDisconnect/SetOnClosed/Close` 等。pump 是这些背后的实现细节，**外层接口保持**。

## 范围（严格边界）

**只改 `network/connection.go` + `network/gnet.go` + `network/heartbeat.go`：**
- `connection.go`：新增 `connectionPump`（合并 decodeLoop+listenLoop+心跳）；`StartDecodeLoop(adp,isUDP)` → `StartPump(adp,isUDP)`（初始化 `inboundCh`/`controlCh`/`pumpDone`、固定 c.adp/isUDP、启动单 pump goroutine）。pump 主循环（参考 02-track 伪代码）：heartbeat-due 优先 → select(inboundCh→decode+dispatch / heartbeatTimer.C→sendHeartbeat / controlCh→register listen/heartbeat/stop / ctx.Done→drain+return)；inbound bounded batch（最多处理 N 条后回外层，防 timer/control 饥饿）。删 `decodeLoop`/`listenLoop`（逻辑并入 pump）；`dispatchListen`/`GetListenResp`/listenQueues 保留（pump 内调）。`listenCh`/`listenDone`/`WaitListenDone*` 收敛（pump 内部或保留兼容）。
- `gnet.go`：`OnTraffic` 只切帧 → `EnqueueInbound(frame)`（不调 decode）；`dial` 内 `conn.StartDecodeLoop(adp,isUDP)` → `conn.StartPump(adp,isUDP)`（gnet.go:390）。`EventServer.adp`（HeaderSize/BodyLength 元信息）保留（2-C1 已接 resolver meta adapter）。
- `heartbeat.go`：删独立 `runHeartbeat` goroutine；`RegisterHeartbeat`/`StopHeartbeat` 改为通过 pump `controlCh` 更新 pump-owned 心跳 runtime（timer + cfg）。`HeartbeatConfig`/`HeartbeatBuilder` 类型保留（pump 持有）。心跳到期 → 调 `cfg.Builder()` → `c.Send`。

**接口契约（与 2-C2-Go agent 并行的关键，必须保持）：**
- `Connection.RegisterHeartbeat(HeartbeatConfig{Interval time.Duration, Builder func() []byte})` **签名稳定**（另一 agent 的心跳闭包是 `func() []byte`，pump 调它；你只改 RegisterHeartbeat 内部实现——controlCh 驱动 pump 而非启独立 goroutine）。
- `Connection.RegisterListen(routeKey, cb, queueSize) error`、`GetListenResp`、`Send`、`Dialer.DialTCP/UDP`、`StartDecodeLoop→StartPump`（gnet.go 内部调用，gnet.go 是你的文件）—— 保持 robot/ 可调。
- `Connection.adp`/`isUDP`/`decodeCh`(或 inboundCh) 字段语义不变（c.adp 由 dial 注入 Go SchemaAdapter，pump 用它 decode）。

**不做：**
- ❌ 不碰 `engine/`、`robot/`、`script/`、`adapter/`（2-C2-Go agent / Phase 2）。
- ❌ 不改 decode 字节语义（c.adp.DecodeTCP/UDP 3-tuple 不变）。
- ❌ 不改 listen 队列语义（listenQueues/queueSize/GetListenResp 不变）。
- ❌ 不改心跳 builder 契约（`func() []byte`）。

## 关键约束

- **pump 是 network 内部**：不泄漏到 flow/engine/Lua 配置语义；外层只感知注册监听/消费监听/注册心跳。
- **两条硬约束**（02-track:253）：heartbeat due 优先检查 + inbound bounded batch（禁止一直 drain inbound 饿死 timer/control）。
- **不写兼容兜底**：pump 退出时 drain/释放 inbound buffer、停 timer、关 done，不泄漏 goroutine。
- **生命周期**：`Close`/`onClose`/`ctx.Done` 统一触发 pump 退出；主动/被动关闭保持现有 onDisconnect/onClosed 语义。`RegisterListen`/`GetListenResp`/`RegisterHeartbeat` 在连接关闭后明确报错/nil，不阻塞等 controlCh。
- 并发安全：c.adp 是 Go SchemaAdapter（无锁），pump 单 goroutine 串行处理 inbound；listen route queue 各自带 mu（GetListenResp 并发 pop 安全）。
- 仅改 network/connection.go + gnet.go + heartbeat.go。
- 日志中文；godoc。
- **Windows 环境注**：`gofmt -l` 标 .go 脏是 autocrlf CRLF，**不要**对单文件 `gofmt -w`。
- **不要 git commit。**

## 工作方式（TDD）

1. 先读 `network/connection.go`（StartDecodeLoop :482、decodeLoop、decodeAndDispatch :540、listenLoop :285、dispatchListen :372、GetListenResp、RegisterListen、listenQueues、Close/onClose、WaitListenDone*、Connection 结构）、`network/heartbeat.go`（RegisterHeartbeat/StopHeartbeat/runHeartbeat/heartbeatState/HeartbeatConfig）、`network/gnet.go`（OnTraffic 切帧+入队、dial→StartDecodeLoop :390、EventServer.adp）。
2. RED（network 包测试，若有 harness；或新增 connection_pump_test.go）：pump 消费 inbound→dispatch；心跳到期→调 builder→Send（fake builder）；controlCh 注册 listen/heartbeat；ctx.Done→pump 退出无泄漏；inbound bounded batch 不饿死 heartbeat。
3. GREEN：connectionPump + StartPump + 删 decodeLoop/listenLoop + heartbeat controlCh 化 + gnet OnTraffic 入队/dial StartPump。
4. `go build ./...`、`go vet ./...`、`go test ./network/... -count=1` 全绿。（engine/robot 由另一 agent 改，可能临时不可编译——若 `go build ./...` 因 engine/robot 失败，仅 build `./network/...` 验证本任务，报告中注明并行态。）

## 验收（self-review）

- 单 connectionPump 替代 decodeLoop+listenLoop+心跳 goroutine；heartbeat-due 优先 + inbound bounded batch；ctx.Done drain 无 goroutine 泄漏。
- `RegisterHeartbeat(HeartbeatConfig{Interval, Builder func() []byte})` 签名稳定（controlCh 驱动 pump）；`StartDecodeLoop→StartPump`（gnet 内部）；robot/ 外层接口（RegisterListen/GetListenResp/Send/Dial）保持。
- decode/listen/心跳 字节+队列语义零回退。
- 仅改 network/connection.go + gnet.go + heartbeat.go。
- go build/vet/test（network）绿。

## ⚠️ 运行时验证依赖

pump 调度（inbound 不饿死心跳、主流程阻塞时 pump 仍处理响应/推送/心跳、连接生命周期无泄漏）需真实服务端长跑验证。报告列为「待运行时验证」。

## 报告

写 `plans/declarative-codec/reports/t2-c3-pump-report.md`：pump 设计（合并三协程、heartbeat-due 优先、bounded batch、controlCh）、接口契约保持（与 2-C2-Go 并行）、生命周期/关闭、TDD、改动文件、self-review、**运行时验证待办**、concerns。
返回（<15 行）：Status、改动文件、测试摘要、运行时验证待办、concerns、报告路径。
