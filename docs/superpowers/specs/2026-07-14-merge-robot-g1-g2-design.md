# 设计：合并 Robot G1/G2 为单生命周期任务

- 日期：2026-07-14
- 状态：草案，待审阅
- 作者：qcw_fater（与 Claude 协作）
- 关联：单 Robot 运行时架构图（agent-v2）

## 1. 背景与动机

绘制新版单 Robot 运行时架构图时，定位到一个可优化的运行时结构：每个 Robot 当前为**两个**常驻 WorkPool 任务。

```text
G1 生命周期任务（GoWithStop）
└─ 内部 Go 出 G2
   └─ executor.Run(ctx)   ← 真正的业务执行
```

稳定态每 Robot 常驻 WorkPool 任务数 = `2 + C`（G1 + G2 + 每连接一个 connectionPump）。

G1 的真实职责经源码核对为：

1. 绑定 LState 的生命周期 context、注入 `script.Context`；
2. 启动内层 G2 执行 `executor.Run`；
3. 在 `select` 中等待 G2 的 `execDone` 或 WorkPool 的 `stopCh`；
4. G2 退出后统一 `cleanup` 并回调 `onDone`。

也就是说，G1 本身不执行业务、不收发网络，仅做「初始化 + 监督 G2 + 收尾」。这层监督在合并后可以由同一个 goroutine 串行完成。

**目标**：把 `executor.Run` 内联进原 G1 任务，删除内层 G2。稳定态常驻任务数 `2 + C` 降为 `1 + C`，并消除 G1/G2 之间的 `execDone` 监督层级。

## 2. 当前结构（核对自源码）

### 2.1 `Robot.Start()` — `robot/robot.go:190`

```go
func (r *Robot) Start() {
    if !r.running.CompareAndSwap(false, true) { return }

    utils.GetWorkPool().GoWithStop(func(stopCh <-chan struct{}) {   // G1
        defer r.running.Store(false)
        defer close(r.done)

        // 绑定 LState context、注入 script.Context（robot.go:199-219）
        // 指标 RobotStarted / RobotRunning（robot.go:221-226）

        utils.GetWorkPool().Go(func() {                              // G2
            defer close(r.execDone)
            if err := r.executor.Run(r.ctx); err != nil {
                // ctx.Err()==nil 记 Errored，否则 Stopped；成功 Stopped
            }
        })

        select {
        case <-r.execDone:
        case <-stopCh:
            r.cancel()
            <-r.execDone
        }

        cleanup := r.cleanup(CleanupReasonNatural, true)            // executorDone=true
        if r.onDone != nil { r.onDone(r, cleanup) }
    })
}
```

### 2.2 `Robot.cleanup()` 的 `executorDone` 分支 — `robot/robot.go:337`

`cleanup(reason, executorDone bool)` 是两条停止路径的收口：

- `executorDone=true`：调用方已知 Executor 退出，`waitDone` 直接置真，**跳过等待 `execDone`**；
- `executorDone=false`：起一个辅助 WorkPool 任务 `<-r.execDone` 后再继续，并受 `robotCloseTimeout` 兜底。

两条路径都随后执行 `CloseAllWithTimeout`、归还 LState、清空 state。`cleanupOnce.Do` 保证整个 cleanup 只有一个调用者真正执行。

### 2.3 关键不变量（合并必须全部保留）

| 不变量 | 来源 | 合并后处置 |
|---|---|---|
| `close(execDone)` 必须先于 `cleanup` | `cleanup(executorDone=false)` 会起辅助任务等待它 | 保留，合并任务在 `executor.Run` 返回后立即 `close(execDone)` |
| `cleanupOnce` 幂等 | `robot.go:339` | 不动，外部 `Close()`/`StopAll()`/reset 与自身 cleanup 并发仍安全 |
| `close(done)` 是最后一步 | `defer close(r.done)` | 保留 |
| `onDone` 恰好一次 | `Start` 末尾 | 保留 |
| WorkPool 停止信号能取消 Executor | `select{case <-stopCh: r.cancel()}` | 保留 `GoWithStop`，信号路径不变 |

### 2.4 Robot 的 ctx 不挂在 WorkPool 停止链路上 — `robot/robot.go:106`

```go
ctx, cancel := context.WithCancel(context.Background())   // 父级是 Background，不是池/Manager
```

每个 Robot 的 ctx 父级是 `context.Background()`，与 WorkPool 的 `stopCh` **没有父子关系**。Robot 的停止触发源是 `Robot.Stop()` / `Manager.StopAll()`（`m.cancel()` 派生到每个 `r.cancel()`），而**不是**池 Shutdown。

进程退出顺序（核对 `cmd/agent/main.go:340-378` 单机 / `agent/agent.go:590-639` Agent）：

```text
SIGTERM / 运行时长到
  → mgr.StopAll()          // 显式 cancel 所有 Robot → Executor 经 r.ctx 退出
  → ...资源清理...
  → utils.GetWorkPool().Shutdown()   // 最后才关池
```

因此 G1 的 `select{case <-stopCh}` 分支**仅在异常路径触发**（调用方未走 `StopAll` 而直接 `WorkPool.Shutdown`）。Robot 正常停止一律走 `StopAll → closeRobotsConcurrent → r.cleanup(false) → r.Stop() → r.cancel()`（`manager.go:357`、`robot.go:258`），Executor 在下一个 ctx 检查点退出。

> **注意**：`StopAll` 停 Robot **不是**靠 ctx 父子链（`m.cancel` 只取消 Manager 自己的 ctx，见 `manager.go:92`），而是靠 `closeRobotsConcurrent` 对每个 Robot 显式调 `cleanup→r.cancel`。所以 Robot ctx 当前父级是 `Background` 在正常路径下完全够用。

### 2.5 WorkPool 停止信号已对外暴露（channel 形态）— `utils/work_pool.go:114`

```go
func (p *WorkPool) StopChan() <-chan struct{} { return p.stopCh }
```

`GoWithStop` 把 `p.stopCh` 作为 `stopCh` 注入任务；`Shutdown()`（`utils/work_pool.go:211`）`close(p.stopCh)` 后 `wg.Wait()`。现有 `stopCh` 调用者（reporter/sampler/agent 等）继续使用。**本设计额外让 WorkPool 暴露 context 形态（§5.2），为 Robot 异常路径提供优雅关闭兜底。**

## 3. 目标结构（方案 B：ctx 父子链补优雅关闭）

设计目标：合并 G1/G2 为单任务，且**两种停止路径都通过 ctx 优雅退出**：

- 正常路径：`StopAll → closeRobotsConcurrent → r.cancel()`（不变）；
- 异常路径（直接 `WorkPool.Shutdown`，不 `StopAll`）：`Shutdown → p.cancel() → WorkPool ctx 取消 → 派生到 Robot ctx → Executor 退出`（**新增兜底**）。

合并后的 `Start()`：

```go
func (r *Robot) Start() {
    if !r.running.CompareAndSwap(false, true) { return }

    utils.GetWorkPool().Go(func() {                       // 单任务，停止信号全走 r.ctx
        defer r.running.Store(false)
        defer close(r.done)

        // 1. 绑定 LState context、注入 script.Context（原样保留）
        // 2. 指标 RobotStarted / RobotRunning（原样保留）

        // 3. 业务执行（原内层 G2 内联到此；r.ctx 在两条停止路径下都会被取消）
        if err := r.executor.Run(r.ctx); err != nil {
            // 与原 G2 完全一致的 Errored / Stopped 判定与指标
        }

        // 4. 通知并发 Close：Executor 已退出业务执行栈
        close(r.execDone)

        // 5. 统一收尾（executorDone=true，不再等待 execDone）
        cleanup := r.cleanup(CleanupReasonNatural, true)
        if r.onDone != nil { r.onDone(r, cleanup) }
    })
}
```

合并任务用 `Go`（不再需要 `GoWithStop`/`stopCh`）：停止信号经 ctx 父子链传导，无需任务内再 `select` 通道。

## 4. 为什么可行（基于源码核对）

1. **`executorDone=true` 正是为「同 goroutine 串行调用 cleanup」设计的**。当前 G1 已经以 `cleanup(Natural, true)` 调用，合并后照搬，绝不传 `false`（否则自己等自己的 `execDone` 死锁）。

2. **`execDone` 仍需保留，不删除**。外部停止路径 `r.cleanup(false)`（`Close`/`StopAll`/`resetBots` 经 `closeRobotsConcurrent`）会起辅助任务等待 `execDone`，以确认 Executor 已离开 Lua/业务栈再释放 LState。合并任务仍 `close(execDone)`，此协议不变。

3. **`r.l` 访问比现状更安全**。当前 G2 用 `r.l` 执行业务、G1 在 `cleanup` 释放 `r.l`，靠 `execDone` 顺序隔离；合并后 Executor 与 cleanup 在**同一 goroutine 串行**，`r.l` 生命周期天然无并发访问。

4. **ctx 父子链补异常路径优雅关闭**。Robot ctx 改以 `utils.GetWorkPool().Context()` 为父级后：
   - 正常路径 `StopAll` 行为完全不变（靠 `closeRobotsConcurrent` 显式 `r.cancel`，与父级无关）；
   - 异常路径直接 `Shutdown`：`p.cancel() → WorkPool ctx 取消 → Robot ctx 取消 → Executor 在 ctx 检查点退出 → close(execDone) → cleanup`，优雅退出，不再靠 `ShutdownTimeout` 兜底。
   - `NewRobot` 失败时 `cancel()`（`robot.go:152`）只取消 Robot 自己的 ctx，不波及 WorkPool ctx。
   - `GetWorkPool()` 懒初始化，创建 Robot 时 WorkPool 已就绪（`main` 在创建 Manager 前已 `InitWorkPool`），`Context()` 必可用。
   - Executor 所有阻塞点（request/listen/await/sleep/IO）均已接入 `r.ctx`（`scheduler.go`、`engine/action.go` 已确认）。

5. **Manager 接口零改动**。`StopAll`/`resetBots`/`closeRobotsConcurrent`/`onRobotDone`/`Done` 全部经 `Close()`→`cleanup` 与 `onDone` 协作，不感知 G1/G2 内部结构。

## 5. 改动清单

### 5.1 `utils/work_pool.go`（小改：新增 context 形态）

- `WorkPool` 结构体加 `ctx context.Context` + `cancel context.CancelFunc` 字段。
- `InitWorkPool`：`ctx, cancel := context.WithCancel(context.Background())`，随结构体一起赋值（与 `stopCh` 并存，双轨）。
- 新增 `func (p *WorkPool) Context() context.Context { return p.ctx }`。
- `Shutdown()`：在现有 `close(p.stopCh)` 之后增加 `p.cancel()`（顺序无关，二者幂等：`stopCh` 只 close 一次由 `stopped` CAS 保证，`cancel` 多次安全）。
- 现有 `StopChan()`/`GoWithStop` 及所有 `stopCh` 调用者**零影响**。

### 5.2 `robot/robot.go`

- `NewRobot`（`robot.go:106`）：`ctx, cancel := context.WithCancel(utils.GetWorkPool().Context())`，替代 `context.Background()`。
- 重写 `Start()`：用 `Go` 替代 `GoWithStop`；内联 `executor.Run`，删除内部 `Go`；保留 `close(execDone)`→`cleanup(Natural, true)`→`onDone`。
- 更新注释：移除「G1/G2」说法，改为「Robot 生命周期任务：初始化 + 业务执行 + cleanup」；更新稳定态常驻任务数 `2+C → 1+C`；注明 ctx 父级为 WorkPool ctx、停止信号经 ctx 传导。
- **保留** `execDone` 字段及其在 `cleanup(false)` 中的等待逻辑。

### 5.3 `robot/manager.go`

- **接口零改动**。
- 仅验证：`StopAll`、`resetBots`、`closeRobotsConcurrent`、`onRobotDone`、`Done`、`startDurationTimer` 在合并后行为一致。

### 5.4 测试（新增，覆盖现状未覆盖的生命周期路径）

`robot/` 下当前只有 `robotIdentity`、scheduler 协作式等待、listen 解析、dial/codec、心跳密钥等测试，**没有任何覆盖 `Start/Stop/Close` 生命周期与并发的测试**。必须新增（建议 `robot/lifecycle_test.go`）：

1. **Executor 自然完成**：`execDone` 关闭 → cleanup 成功 → `done` 关闭 → `onDone` 恰好一次。
2. **`Robot.Stop`**：ctx 取消 → Executor 退出 → LState 正常归还。
3. **`Robot.Close` 与自然完成并发**：无死锁、cleanup 只执行一次、LState 只归还一次、`onDone` 一次。
4. **`Manager.StopAll`**：所有 Robot 结束 → `doneCh` 关闭 → stopped/started 计数一致。
5. **Ramp-up reset**：清空旧 Robot 后仍能创建下一阶段 Robot。
6. **异常路径优雅关闭（方案 B 核心）**：**不调 `StopAll`，直接 `WorkPool.Shutdown()`** → WorkPool ctx 取消 → Executor 经 Robot ctx 优雅退出 → `done` 关闭（不靠 `ShutdownTimeout` 兜底）。
7. **池 `Shutdown` 期间辅助任务**：`cleanup` 内 `closeCh`/`waitCh` helper 提交可能返回 `ErrPoolStopped`，靠 `robotCloseTimeout` 兜底（现状行为，不回归）。
8. **cleanup 超时**：Executor 或连接未及时退出时隔离 LState，不归还可能仍在使用的运行时。
9. **race**：`go test -race ./robot/... ./utils/...`。

### 5.5 架构图（agent-v2）

稳定态常驻任务公式 `2 + C` 改为 `1 + C`；删除图中 G2 节点，把「G2 Actor Owner」并入生命周期任务。

## 6. 风险与缓解

| 风险 | 说明 | 缓解 |
|---|---|---|
| 自己等自己 `execDone` | 若误传 `executorDone=false` 或忘 `close(execDone)` | 强制 `cleanup(Natural, true)`；`close(execDone)` 紧跟 `executor.Run` 之后；新增测试覆盖 |
| `execDone` 误删 | 外部 `Close` 路径依赖它确认 Lua 栈退出 | 明确保留字段与 `cleanup(false)` 等待逻辑；改动清单标注「保留」 |
| 池 Shutdown 期间辅助任务提交失败 | `closeCh`/`waitCh` helper 经 `submit`，池已停止时返回 `ErrPoolStopped` | 现状已有行为（`Go` 忽略错误，靠 `robotCloseTimeout` 兜底）；合并不引入新风险；测试覆盖 |
| `r.l` 并发访问回归 | 合并后理论上同 goroutine 串行，但仍需验证 | `go test -race` 必跑 |
| 监督层级消失后池停止感知 | 去掉 G1 的 `select{<-stopCh}` 后，异常路径（直接 `Shutdown` 不 `StopAll`）失去 G1 的通道兜底 | 方案 B：Robot ctx 以 `WorkPool.Context()` 为父级，`Shutdown→p.cancel()` 经 ctx 父子链派生到 Robot，Executor 优雅退出；新增测试用例 6 覆盖 |
| WorkPool ctx 泄漏到上层依赖 | 暴露 `Context()` 后，未来调用者可能错误依赖其生命周期 | 文档与注释明确：`Context()` 仅用于「随池停止而取消」的派生 ctx，不替代业务自有超时 ctx；Robot 是其唯一合法消费者 |

## 7. 不在本设计范围

- 不改 `execDone` 协议、不改 `cleanup` 签名、不改 Manager 接口。
- 不改 `StopChan()`/`GoWithStop` 及其现有调用者（reporter/sampler/agent 等）；WorkPool 的 `stopCh` 与新 `ctx` 双轨并存，互不替换。
- 不合并 connectionPump（那是每连接独立 owner，不在本任务）。
- 不涉及分布式 Admin/Agent 层。

## 8. 验证流程

1. `go build ./...`
2. 新增 `robot/lifecycle_test.go` 全部通过；
3. `go test -race ./robot/... ./utils/... ./network/...`
4. 单机模式实跑：`rm -f log/stressbot.log` → `go run ./cmd/agent -config conf/config.json` 运行 2~5 分钟；
5. 日志审查：`grep -i "error\|warn\|失败\|超时\|隔离" log/stressbot.log` 应无新增异常（cleanup 超时/隔离 LState 的告警不新增）。

## 9. 预期收益

- 每 Robot 常驻 WorkPool 任务 `-1`；万级 Robot 规模下减少万级 goroutine 及其栈内存、调度开销。
- 消除 G1/G2 之间的 `execDone` 监督层级，`r.l` 访问从「跨 goroutine 顺序隔离」变为「同 goroutine 串行」，简化并发推理。
- 架构图公式更简洁：`1 + C`。
