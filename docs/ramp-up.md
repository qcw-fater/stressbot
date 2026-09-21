# 渐进式加压（RampUp）— 技术文档

> 本文档追踪渐进式加压功能的完整数据链路：从前端表单配置 → API 请求 → 后端校验与分配 → Agent 执行 → 运行时监控 → 历史归档与展示。

---

## 1. 概述

渐进式加压允许将 `totalBots` 个机器人分多个阶段逐步创建，而非一次性全部启动。每个阶段可独立配置：

| 字段 | 类型 | 说明 |
|------|------|------|
| `count` | int（必填 > 0） | 本阶段**新增**机器人数（增量值，各阶段之和须等于 totalBots） |
| `concurrency` | int?（可选） | 覆盖全局并发数；0 或空则用全局值 |
| `holdSec` | int?（可选） | 阶段间等待秒数，最后阶段可不填；**未填（0）回退默认 30s**，显式配置的值一律照办，不做下限钳制（`robot/manager.go:289-294`） |
| `reset` | bool?（可选） | 开始本阶段前清空所有已有机器人；第一阶段无意义（前端固定禁用） |

典型场景：
- 分批验证服务端承载能力（100 → 300 → 500 机器人）
- 重置阶段测试从零恢复的性能（500 机器人 → reset → 200 机器人）
- 调试模式下自动禁用（单 agent 分配，不支持 rampUp）

单机模式同样支持：`conf/stressbot.toml` 的 `[standalone.rampUp.stages]` 配置（`standalone/config.go:29`），配置了 stages 时走 `StartWithRampUp`（`standalone/app.go:103-118`）；加载期校验各阶段 count 之和必须等于 `standalone.bot.totalBots`（`standalone/config.go:84-91`）。分布式模式下 rampUp 随任务下发（robotConfig JSON 内）。

---

## 2. 前端启动配置

### 2.1 类型定义

**文件**: `cmd/web/src/types/api.ts`

```typescript
// RampUpStage / RampUpConfig（见 types/api.ts）
export interface RampUpStage {
  count: number;          // 本阶段新增 bot 数
  concurrency?: number;   // 覆盖全局并发
  holdSec?: number;       // 阶段间等待秒
  reset?: boolean;        // 开始前清空已有机器人
}

export interface RampUpConfig {
  stages: RampUpStage[];
}

// RobotConfig 内
rampUp?: RampUpConfig;

// 运行时快照（types/api.ts:420-423）：后端上报的当前阶段
export interface RampUpSnapshot {
  currentStage: number; // 当前阶段（1-based，0 = 未启用）
  totalStages: number; // 总阶段数（0 = 未启用）
}
// StressSnapshot.rampUp: RampUpSnapshot（types/api.ts:434）
```

### 2.2 Store 持久化

**文件**: `cmd/web/src/services/runtimeStore.ts`

| 字段 | 类型 | 默认值 | 持久化 |
|------|------|--------|--------|
| `rampUpEnabled` | `boolean` | `false` | localStorage `stressbot:runtime-form` v4 |
| `rampUpStages` | `RampUpStage[]` | `[{ count: 0, holdSec: 30 }]` | 同上 |

- 状态声明 `runtimeStore.ts:45-46`，默认值 `:142-143`，setter `:168-169`
- `partialize` 选定两个字段持久化（`:273-281`，注释「v4：新增 rampUpEnabled / rampUpStages 持久化」）
- 打开启动弹窗时从 store 读取上次配置，关闭后自动恢复

### 2.3 表单 UI

**文件**: `cmd/web/src/components/runtime/TaskStartModal.tsx`

UI 结构：

```
渐进式加压  [Switch]
  ┌─────┬──────────┬──────────┬──────────┬──────────┬─────┐
  │ 阶段 │ 新增机器人数 │ 并发覆盖  │ 间隔秒数  │ 阶段重置  │     │
  ├─────┼──────────┼──────────┼──────────┼──────────┼─────┤
  │ #1  │ [100   ] │ [     ] │ [30   ] │  (禁用)  │  🗑  │
  │ #2  │ [200   ] │ [50   ] │ [30   ] │ [Switch] │  🗑  │
  │ #3  │ [200   ] │ [30   ] │ [     ] │ [Switch] │  🗑  │
  └─────┴──────────┴──────────┴──────────┴──────────┴─────┘
  [+ 添加阶段]                     合计 500 台机器人
```

关键逻辑：

- **调试模式互斥**：调试模式自动 `setRampUpEnabled(false)`（`TaskStartModal.tsx:294`）
- **总机器人数字段**：开启 rampUp 后变为只读，显示各阶段 count 累加的提交值
- **阶段重置禁用**：第一阶段 `disabled={i === 0}`（无机器人可重置）
- **删除阶段**：保留至少 1 个阶段（`rampUpStages.length > 1` 时才显示删除按钮）
- **合计显示**：`rampUpSum > 0 ? 'success' : 'warning'` 颜色提示

### 2.4 计算逻辑

```typescript
// TaskStartModal.tsx:305-307 — rampUpSum：各阶段 count 累加
const rampUpSum = rampUpStages.reduce((s: number, st) => s + (st.count || 0), 0);

// TaskStartModal.tsx:311-313 — rampUpValid：全部阶段 count > 0
const rampUpValid =
  !rampUpEnabled ||
  (rampUpStages.length > 0 && rampUpSum > 0 && rampUpStages.every((st) => st.count > 0));

// TaskStartModal.tsx:318 — 提交值：渐进加压时取各阶段之和
const submitBots = requestedBots({ rampUpEnabled, rampUpStages, totalBots });
// requestedBots 实现见 services/taskCapacity.ts:39
```

### 2.5 提交组装

**文件**: `cmd/web/src/components/runtime/TaskStartModal.tsx`（`executeStart`，:351-374）

```typescript
const id = await startTask({
  name: taskName,
  totalBots: submitBots,                    // rampUpEnabled ? sum(stages.count) : totalBots
  robotConfig: {
    ...robotConfig,
    debugMode,
    rampUp: rampUpEnabled ? { stages: rampUpStages } : undefined,
  },
  deadline, flow, flowLayout, flowTemplateId,
});
```

**文件**: `cmd/web/src/services/taskActions.ts`（:261-265）

```typescript
const fd = new FormData();
fd.append('name', opts.name);
fd.append('totalBots', String(opts.totalBots));
fd.append('robotConfig', JSON.stringify(opts.robotConfig));  // rampUp 在 JSON 内
```

**文件**: `cmd/web/src/services/tasksApi.ts`

- `createTask(fd)` → `POST /sbot/tasks`（multipart/form-data）
- `startTask(id)` → `POST /sbot/tasks/{id}/start`

---

## 3. 后端任务创建与校验

### 3.1 类型定义

**文件**: `admin/task/types.go`

```go
// RobotConfig 内（admin/task/types.go:153-154）
RampUp *RampUpConfig `json:"rampUp,omitempty"`  // 指针类型

// admin/task/types.go:157-161
type RampUpConfig struct {
    Stages []RampUpStage `json:"stages"`
}

// admin/task/types.go:163-170
type RampUpStage struct {
    Count       int  `json:"count"`                 // 本阶段新增 bot 数（增量值）
    Concurrency int  `json:"concurrency,omitempty"` // 覆盖全局并发数，0 则用全局值
    HoldSec     int  `json:"holdSec,omitempty"`     // 阶段间等待秒数，0 则用 defaultStageHoldSec
    Reset       bool `json:"reset,omitempty"`       // 开始本阶段前清空所有已有机器人
}
```

### 3.2 创建任务时的校验

**文件**: `admin/httpapi/task_routes.go:167-186`

```go
if cfg.RobotConfig.RampUp != nil {
    // 1. stages 不能为空
    if len(cfg.RobotConfig.RampUp.Stages) == 0 { ... }
    sum := 0
    for _, s := range cfg.RobotConfig.RampUp.Stages {
        // 2. 每个 stage 的 count > 0
        if s.Count <= 0 { ... }
        // 3. 每个 stage 的 holdSec >= 0
        if s.HoldSec < 0 { ... }
        sum += s.Count
    }
    // 4. 各阶段 count 之和必须等于 totalBots
    if sum != totalBots { ... }
}
```

### 3.3 分布式缩放：ScaleRampUp

**文件**: `admin/task/distribution.go:128`

当多个 Agent 参与任务时，每个 Agent 分到的机器人数量按比例缩放。`ScaleRampUp` 将每个阶段的 `count` 按 `assignedBots / totalBots` 比例分配。

**算法**：

1. 单 Agent（`assignedBots == totalBots`）→ 原样返回，不缩放
2. 未分配（`assignedBots <= 0`）→ 返回全 0 阶段配置（保留结构，保留 Concurrency/Reset/HoldSec）
3. 多 Agent：
   - 对每个 stage：`exact = stage.Count × assignedBots / totalBots`
   - `floor` 取整作为基础分配
   - 计算小数余量 `frac = exact - floor`
   - 将 `remainder = assignedBots - sum(floors)` 个余量按余量从大到小分配给各阶段
   - **保证** `sum(counts) == assignedBots`，每个 `count >= 0`
4. Concurrency / Reset / HoldSec 等语义字段**原样保留**（不缩放）

**调用位置**: `admin/command/dispatcher.go:63`

```go
TaskAssignment{
    RampUp: admintask.ScaleRampUp(robotConfig.RampUp, task.TotalBots, assignment.TotalBots, assignments, assignment.AgentID),
    // ...
}
```

### 3.4 任务分配与下发

**文件**: `admin/httpapi/task_routes.go`、`admin/command/dispatcher.go`

- `handleStartTask()`（`task_routes.go:380`）：调用 `assigner.Assign()` 计算分配方案
- `startTaskBackground()`（`task_routes.go:465`）：经 gRPC 控制面推送到所有 Agent
- `TaskAssignment` 结构体含 `RampUp *RampUpConfig` 字段（`admin/task/types.go:235-236`，注释「已按比例缩放」）

---

## 4. Agent 执行

### 4.1 Agent 接收配置

**文件**: `agent/task/types.go:54`

```go
type TaskAssignment struct {
    RampUp *RampUpConfig `json:"rampUp,omitempty"`
    // ...
}
```

### 4.2 转换为 Manager 配置

**文件**: `agent/task/runner.go:221-243`

```go
if r.assignment.RampUp != nil && len(r.assignment.RampUp.Stages) > 0 {
    mgrCfg.RampUp = &robot.RampUpConfig{}
    for _, s := range r.assignment.RampUp.Stages {
        mgrCfg.RampUp.Stages = append(mgrCfg.RampUp.Stages, robot.RampUpStage{
            Count:       s.Count,
            Concurrency: s.Concurrency,
            HoldSec:     s.HoldSec,
            Reset:       s.Reset,
        })
    }
}
mgr := robot.NewManager(taskCtx, mgrCfg, flow, factory, dialer, luaPool)
mgr.OnStageReset = r.OnStageReset  // 阶段重置回调
// 每个阶段开始时（含第一阶段）上报阶段进度，驱动前端实时加压进度
mgr.OnStageChange = func(current, total int) {
    r.collector.SetRampUpStage(current, total)
}

// 根据是否有 rampUp 选择启动方式
if mgrCfg.RampUp != nil {
    startErr = mgr.StartWithRampUp()
} else {
    startErr = mgr.StartAll()
}
```

### 4.3 Manager 核心执行：StartWithRampUp

**文件**: `robot/manager.go:244-350`

执行流程：

```
StartWithRampUp()
  │  defer finishCreation()（成功/失败/早退都标记创建阶段结束）
  │
  ├─ 计算总 bot 数 sum(stages.Count)
  │
  └─ 遍历每个 stage[i]:
       │
       ├─ 检查 ctx 取消（支持随时停止；task → manager → robot 取消链）
       │
       ├─ [如果 stage.Reset && len(robots) > 0]        （manager.go:264-283）
       │    ├─ resetBots()         // 递增代号，并发停止所有机器人，清空列表
       │    ├─ sleep(1s)           // flush 窗口，等待末次 IO 完成和计数器归位（select ctx.Done）
       │    └─ OnStageReset(i)     // 回调 agent 上报阶段段落报告
       │
       ├─ 确定并发数：stage.Concurrency > 0 ? stage.Concurrency : 全局默认
       │  确定 holdSec：0（未填）→ defaultStageHoldSec(30)；显式配置一律照办
       │                                                （manager.go:289-294）
       │
       ├─ OnStageChange(i+1, len(stages))               （manager.go:305-307）
       │    // 上报当前阶段（1-based）与总阶段数 → monitor.SetRampUpStage
       │
       ├─ startBatch(offset, stage.Count, conc)
       │    │
       │    └─ 循环创建机器人:
       │         ├─ robotIdentity(fromIndex+i)  // 任务全局 id/index/account
       │         ├─ NewRobot(...) + addAndStartRobot
       │         └─ 限速: 每 conc 个暂停 1s（最后一批不等）
       │
       ├─ offset += stage.Count
       │
       └─ [如果不是最后阶段]
            └─ select { ctx.Done() | time.After(holdSec) }
               // 阶段保持等待，支持中途取消
```

### 4.4 resetBots — 阶段重置

**文件**: `robot/manager.go:447-467`

```go
func (m *Manager) resetBots() CleanupStatus {
    // 先递增代号：此后被关闭的旧 robot 的 onRobotDone 归入旧代，
    // 被 gen 检查挡下，不会误改新代 active / 误关 doneCh。
    m.generation.Add(1)

    m.mu.Lock()
    robots := make([]*Robot, len(m.robots))
    copy(robots, m.robots)
    m.robots = m.robots[:0]       // 清空列表
    m.robotIdx = make(map[*Robot]int, m.cfg.Count)  // 重建下标索引
    m.mu.Unlock()

    cleanup := closeRobotsConcurrent(robots, CleanupReasonRampReset)  // 并发 Close
    m.active.Store(0)              // 新代 active 基线归零（旧代回调因 gen 不符不会再减）
    return cleanup
}
```

要点：
- **并发关闭**：防止单个 robot 卡死阻塞阶段切换（整体 15s 超时强制推进，`closeRobotsTimeout`，`manager.go:352-357`）
- **保留 Manager 生命周期**：不取消 context、不关闭 doneCh，Manager 继续创建新机器人
- **代号隔离**：Manager 用 `active`（存活计数）/ `generation`（代号）/ `creationDone`（创建阶段结束）三个原子量替代旧的 started/stopped 对，消除 ramp-up 竞态——doneCh 关闭的唯一条件是 `creationDone==true && active==0`，同时消灭「错过完成事件」「阶段间瞬时归零」「旧代回调污染计数」三类问题（`manager.go:80-99`）

### 4.5 startBatch — 批量创建机器人

**文件**: `robot/manager.go:141-200`

```go
func (m *Manager) startBatch(fromIndex, count, conc int) (int, error) {
    created := 0
    gen := m.generation.Load()  // 本批全部归属同一代
    for i := range count {
        if m.ctx.Err() != nil { return created, m.ctx.Err() }

        // 任务全局身份：index = StartIndex + 本地序号，id = StartNumber + index
        id, index, account := m.cfg.robotIdentity(fromIndex + i)

        r, err := NewRobot(...)
        if err != nil {
            // resolver nil / LState 不可用等配置性问题：跳过该 robot、日志告警、继续创建
            continue
        }
        m.addAndStartRobot(gen, r, r.Start)  // 先登记 active 再 Start，杜绝秒退竞态
        created++

        // 限速：每 conc 个暂停 1s（最后一批不等，避免阶段切换多等 1s）
        if conc > 0 && (i+1)%conc == 0 && (i+1) < count {
            select {
            case <-m.ctx.Done(): return created, m.ctx.Err()
            case <-time.After(time.Second):
            }
        }
    }
    // 整批一个都没建起来还返回 nil 就是"静默空跑"，必须报错
    if created == 0 && count > 0 {
        return 0, fmt.Errorf("批次创建失败：请求 %d 个机器人，全部创建失败...")
    }
    return created, nil
}
```

### 4.6 阶段完成报告

**文件**: `admin/grpcapi/control_service.go`、`admin/task/completion.go:57-78`

Agent 在阶段重置时通过 `OnStageReset(stageIdx)` 回调向 Admin 上报阶段完成报告（gRPC 控制面）：

```go
// completion.go:62
isFinal := report.StageIndex <= 0   // -1 或 0 = 最终报告；> 0 = 阶段报告
```

- **阶段报告**（`StageIndex > 0`）：只刷新心跳，**不清空** `CurrentTaskID` / `LatestStress`，存入 `task.StageReports`，按 `(agentId, stageIndex)` 幂等去重
- **最终报告**（`StageIndex <= 0`）：走原有完成逻辑，检查是否所有 Agent 都已上报

### 4.7 中途取消处理

**文件**: `agent/task/runner.go:241-262`

```go
if startErr != nil {
    result := finishManagerStartFailure(ctx, mgr, startErr)
    if result.Result == Stopped {
        // 渐进式加压在 ctx cancel 后会从 StartWithRampUp 返回 context.Canceled，
        // 这是"用户主动停止"而非"失败"，按 Stopped 上报，避免历史归档误判为失败。
    }
    return result
}
```

任务取消沿 task → manager → robot 取消链立即传播，`StartWithRampUp` 在阶段开始前、限速等待、阶段保持各点都能尽快返回 `context.Canceled`（`manager.go` 注释 :232-234）。

---

## 5. 运行时监控

阶段进度由**后端上报**，不是前端按 running 数估算。

### 5.1 指标采集：SetRampUpStage

- `monitor.MetricsCollector.SetRampUpStage(current, total)`（`monitor/collector.go:694-708`）：原子记录当前阶段（1-based）与总阶段数，由 Manager 的 `OnStageChange` 回调在每个阶段开始时（含第一阶段）调用。
- 快照：`StressSnapshot.RampUp`（`monitor/snapshot.go:184-187,202`）输出 `{currentStage, totalStages}`，0 表示未启用。
- 集群聚合（`monitor/snapshot.go:780-807`）：`TotalStages` 取首个有效 Agent 的值（所有 Agent 阶段数相同）；`CurrentStage` 取各 Agent 的**最小值**——整体进度取决于最慢节点。

### 5.2 前端面板渲染

**文件**: `cmd/web/src/components/monitoring/MonitorDock.tsx`（`RampUpLoadView`，:314-380）+ `shared/liveMetrics.ts`（`deriveRampUp`，:268-273）

- `deriveRampUp` 从 `stress.rampUp` 快照取 `{currentStage, totalStages}`（`totalStages<=0` 视为未启用）。
- `RampUpLoadView` 结合本地 `rampUpStages` 配置计算各段起点：`stageStart = sum(stages[0..current-1].count)`，总目标 = 各段 count 之和，当前段进度 = `(runningRobots - stageStart) / stage.count`。

### 5.3 分段进度条渲染

```
  渐进式 阶段 2/3         错 12
  ████████████░░░░░░░░░████████
  │──阶段1──│ │──阶段2──│ │─阶段3─│
  已完成     当前(脉冲)   未开始
```

- 每段宽度按 `stage.count / rampUpTotal` 比例分配
- 已完成阶段：满填充（成功色）
- 当前阶段：按 `progress` 填充 + 脉冲动画（`md-stage-fill--active`）
- 未开始阶段：空

**相关 CSS**（`cmd/web/src/components/monitoring/MonitorDock.css:393-426`）：`.md-stage-bar` / `.md-stage-segment` / `.md-stage-fill` / `.md-stage-fill--active`（脉冲动画）。

---

## 6. 历史归档

### 6.1 数据库 Schema

**文件**: `admin/mysql/schema_definition.go`（内置 Go DDL，首版当前结构）

| 表 | 字段 | 类型 | 说明 |
|---|---|---|---|
| `task_history` | `stage_count` | `INT NOT NULL DEFAULT 0` | 配置的阶段数（非上报数）（schema_definition.go:24） |
| `task_report` | `stage_index` | `INT NOT NULL DEFAULT -1` | -1=整体最终；>0=连续 1-based 段落号（:49） |
| `task_aggregated` | `stage_index` | `INT DEFAULT -1`（主键 `(task_id, stage_index)`） | 同上（:55-58） |
| `task_timeseries` | `stage_index` | `INT DEFAULT -1` | 采样点所属段落号（仅 reset 任务 >0）（:65） |
| `task_config_archive` | `robot_config` | `JSON` | 完整 RobotConfig（含 rampUp） |
| `task_meta` | `(task_id, stage_index)` 主键 | — | 统一元数据：`starred` / `tags(JSON)` / `note` / `updated_at`。`stage_index=-1` 为任务级（所有任务），`>=1` 为 reset 各段落。行按需懒创建（:128-135） |

> `task_history` **没有** `starred/tags/note` 列——这三项统一在 `task_meta`，与 `task_report/aggregated/timeseries` 的 `(task_id, stage_index)`（`-1`=整体）键约定保持一致。

索引：`task_report.idx_task_stage(task_id, stage_index)`、`task_timeseries.idx_task_stage_elapsed(task_id, stage_index, elapsed_sec)`、`task_meta` 主键即 `(task_id, stage_index)`。

### 6.2 归档写入

**文件**: `admin/history/store.go` — `Archive()` 方法

```go
// store.go:106-108：从配置中计算 stageCount
stageCount := 0
if task.Config.RobotConfig.RampUp != nil {
    stageCount = len(task.Config.RobotConfig.RampUp.Stages)
}

// store.go:116-127：写入 task_history（含 stage_count），ON DUPLICATE KEY UPDATE
// 阶段完成报告写入 task_report（含 stage_index）；完整 RobotConfig 写入 task_config_archive
```

### 6.3 归档读取

**文件**: `admin/history/store.go`

- `List()`（:273-291）：SELECT 含 `stage_count`，Scan 到 `HistoryRecord.StageCount`；批量读取本页 `stage_count>0` 行的 robot_config 避免 N+1（:322-326）
- `Get()`（:428 起）：SELECT 含 `stage_count`

**文件**: `admin/history/types.go`

```go
// HistoryRecord
StageCount int `json:"stageCount,omitempty"`
```

### 6.4 前端历史列表展示

**文件**: `cmd/web/src/components/modules/history/HistoryModal.tsx`（:508-510）

列表卡片中，当 `stageCount > 0` 时在 metrics 行显示蓝色「阶段 N」标签。

### 6.5 前端历史详情展示

**文件**: `cmd/web/src/components/modules/history/HistoryDetailView.tsx`

**头部标识**（:708）：在摘要行中插入 `渐进式加压 · N 阶段 · 总计 X 机器人`。

**折叠式阶段时间线**：

1. 加载详情时额外调 `historyApi.getHistoryConfig(id)` 获取完整 `robotConfig`（含 `rampUp`）
2. 配置区内渲染折叠区域：

```
┌─ 渐进式加压 · 3 阶段 · 总计 500 机器人 ──── ▸ ─┐
│                                                  │  ← 折叠态（默认）
└──────────────────────────────────────────────────┘

点击展开后：

┌─ 渐进式加压 · 3 阶段 · 总计 500 机器人 ──── ▾ ─┐
│                                                  │
│  ① +100 机器人 · 并发 50 · 保持 30s              │
│  ┊ (虚线连接)                                    │
│  ② +200 机器人 · 并发 50 · 保持 30s              │
│  ┊                                               │
│  ③ +200 机器人 · 并发 30                         │
│                                                  │
└──────────────────────────────────────────────────┘
```

- 左侧渐变竖条（蓝→紫，延续 glass 卡片风格）
- chevron 点击展开/折叠
- 每阶段：编号圆点 + 虚线连接 + 详细参数
- `reset` 标记显示为橙色 antd `<Tag color="warning">`

### 6.6 阶段段落历史（含 reset 的渐进式加压）

含 `reset=true` 的任务，每次 reset 前 Agent 会快照并 `Reset()` 采集器，因此整段被切分为多个**阶段段落**。
设计要点：

- **段号映射**（`admin/task/stage_plan.go`）：`BuildStagePlan` 把 reset 边界（`OnStageReset` 上报的 0-based 配置下标，如 `{2,4}`）
  映射为连续 1-based 段落号（`{1,2}`）。段落 N 覆盖配置阶段范围 `[Sx, Sy]`，`PeakBots` = 段内各阶段增量之和。
  最后一段（`IsFinal`）的指标取自任务最终报告。
- **归档**（`admin/history/store.go` `Archive`）：
  - `task_report`：reset 段落报告按段号写入；最终报告同时写 `stage_index=-1`（兼容）与最后一段段号（末段可查节点报告）。
  - `task_aggregated`：`stage_index=-1` 为整体最终（reset 任务下 = 末段）；各中间段落由 `MergeSnapshots` 聚合该段全部 Agent 快照；末段 = 整体。
  - `task_timeseries`：`Sampler` 接入 `TaskStore`，按「已观测 reset 上报数 + 1」实时写入采样点段号，与归档段号严格对齐。
- **整体语义**：reset 任务的 `stage_index=-1` 实为**末段**数据，故前端列表父行渲染为**阶段组**（仅展开/收起），
  不提供会误导的「整体详情」；可点击的子记录即各段落详情，与普通详情结构一致。
- **统一元数据**（`task_meta`）：收藏 / 标签 / 备注按 `(task_id, stage_index)` 存储，`stage_index=-1` 为任务级、
  `>=1` 为各段落，**所有任务走同一张表、同一条读写路径**（`UpsertMeta(id, stageIndex)`，列表/详情用 `LEFT JOIN task_meta ... stage_index=-1`）。
  含 reset 的任务里收藏 / 标签 / 备注**分属各段落**——每段独立一份，在段落详情页编辑，列表段落节点同步显示
  （收藏★、标签、备注圆点）。`PUT /history/{id}?stageIndex=N`（缺省 `-1`）统一写入。删除仍以**整个任务**为单位
  （级联清理含 `task_meta`）。「收藏」筛选匹配「任务级或任一段落被收藏」。
- **非 reset 的渐进式加压**：仍是单条连续历史；时序不打段号（写 -1），趋势图阶段切换线由前端依据
  `rampUp` 累计 `holdSec` **近似**绘制（`HistoryDetailView.computeStageLines`，phase-1 近似）。

**HTTP API 扩展**（详见 `docs/frontend-api.md`）：

| 端点 | 新增参数 | 说明 |
|---|---|---|
| `GET /history?includeStages=true` | `includeStages` | reset 父记录返回 `children`（阶段段落子记录），`hasResetStages=true` |
| `GET /history/{id}?stageIndex=N` | `stageIndex` | 返回第 N 段详情（含段标签/范围、段落级收藏/标签/备注） |
| `PUT /history/{id}?stageIndex=N` | `stageIndex` | 更新第 N 段的收藏/标签/备注（写 `task_meta`；缺省 `-1`=任务级） |
| `GET /history/{id}/timeseries?stageIndex=N` | `stageIndex` | 仅返回第 N 段采样点 |
| `GET /history/{id}/agents?stageIndex=N` | `stageIndex` | 第 N 段节点报告 |
| `GET /history/compare?targets=a:-1,b:2` | `targets` | 支持阶段段落对比（旧 `ids=a,b` 仍兼容） |

**前端阶段组**（`HistoryModal.tsx` `StageGroup`，:578 起）：渲染为一张玻璃卡片，左侧竖条沿用普通记录的蓝→绿渐变
（保持整列视觉统一）；卡内为可折叠的任务标题带 + 段落**编号时间线**（复用加压时间线的圆点/连线语言）。
每个段落节点显示配置阶段范围、峰值机器人、并发与段落级收藏/标签/备注，可勾选参与对比、点击进入段落详情；
删除按钮位于标题带，作用于整个任务。

---

## 7. 数据流总览

```
前端 TaskStartModal
  │  rampUpEnabled (Switch)
  │  rampUpStages (Table: count/concurrency/holdSec/reset)
  │  totalBots = sum(stages.count)  // rampUp 开启时覆盖
  │  robotConfig.rampUp = { stages }
  │
  ▼  POST /sbot/tasks (FormData: robotConfig JSON)
后端 handleCreateTask (admin/httpapi/task_routes.go)
  │  校验: stages 非空, count>0, holdSec>=0, sum==totalBots
  │  writeBaselineFiles 写回 conf/ 基线
  │
  ▼  POST /sbot/tasks/{id}/start
后端 startTaskBackground (admin/httpapi/task_routes.go:465)
  │  assigner.Assign() → 每个 Agent 分配方案
  │  ScaleRampUp(rampUp, total, assigned)  // 按比例缩放（dispatcher.go:63）
  │  TaskAssignment.RampUp = scaled → gRPC 推送
  │
  ▼
Agent task.Runner.Run (agent/task/runner.go)
  │  转换为 robot.RampUpConfig
  │  mgr.OnStageReset = callback
  │  mgr.OnStageChange = collector.SetRampUpStage
  │  mgr.StartWithRampUp()
  │
  ▼
robot/manager.StartWithRampUp (robot/manager.go:244)
  │  遍历 stages:
  │    reset? → resetBots() + sleep(1s) + OnStageReset(i) → Admin 存 StageReport
  │    OnStageChange(i+1, total) → monitor.SetRampUpStage
  │    startBatch(offset, count, conc)  // 限速创建机器人
  │    非最后? → sleep(holdSec ｜ 未填 30s), select ctx.Done()
  │
  ├──► 运行时: MonitorDock.tsx RampUpLoadView
  │    stress.rampUp（后端上报 currentStage/totalStages）+ 本地 stages
  │    分段进度条展示
  │
  └──► 任务结束: history.Archive() (admin/history/store.go)
       │  stage_count = len(config.Stages)  → task_history
       │  stage_reports                      → task_report (stage_index)
       │  robot_config JSON                  → task_config_archive
       │  reset 任务段落聚合/时序按段号归档
       │
       ▼  前端历史面板
       HistoryModal: 列表卡片 "N 阶段" / StageGroup 阶段组
       HistoryDetailView: 头部标识 + 折叠式阶段时间线
```

---

## 8. 关键约束与设计决策

| 约束 | 说明 |
|------|------|
| 阶段 count 之和 = totalBots | 后端强制校验（分布式 `task_routes.go:184-186`、单机 `standalone/config.go:84-91`），前端提交时 totalBots 自动取 sum |
| holdSec 未填回退 30s | `manager.go:289-294`：0 表示未填，回退 `defaultStageHoldSec`(30)；**显式配置一律照办，不做下限钳制** |
| 第一阶段不支持 reset | 前端 Switch `disabled={i === 0}`，后端不做额外校验 |
| 调试模式禁用 rampUp | 前端切换调试模式时自动 `setRampUpEnabled(false)` |
| reset 后 1s flush | 给机器人末次 IO 和计数器留出归位窗口 |
| ctx 取消即时响应 | task → manager → robot 取消链；每个关键节点（阶段开始前、限速等待、阶段保持）都 `select ctx.Done()` |
| 并发关闭 reset 机器人 | `closeRobotsConcurrent`（15s 整体超时强制推进）防止单个 robot 卡死阻塞阶段切换 |
| 代号隔离生命周期计数 | `active` / `generation` / `creationDone` 原子量替代旧 started/stopped 对，消除 ramp-up 竞态（`manager.go:80-99`） |
| scaleRampUp 保证总和 | floor + 按余量补齐，确保 `sum(counts) == assignedBots` |
| 阶段报告幂等去重 | `(agentId, stageIndex)` 作为唯一键，重复上报覆盖 |
| 阶段进度后端上报 | `OnStageChange → SetRampUpStage → StressSnapshot.rampUp`，集群聚合取最小 currentStage（整体进度取决于最慢节点） |
| 中途取消按 Stopped 上报 | Agent 检测启动阶段 `context.Canceled` 时返回 Stopped 而非 Failed |

---

## 9. 涉及文件索引

| 文件 | 职责 |
|------|------|
| `cmd/web/src/types/api.ts` | 前端类型定义：RampUpStage / RampUpConfig / RampUpSnapshot / HistoryRecord.stageCount |
| `cmd/web/src/services/runtimeStore.ts` | rampUpEnabled / rampUpStages 状态持久化（v4） |
| `cmd/web/src/components/runtime/TaskStartModal.tsx` | 启动表单：Switch + 阶段表格 + 校验 |
| `cmd/web/src/services/taskCapacity.ts` | requestedBots：提交机器人数（渐进加压取各阶段之和） |
| `cmd/web/src/services/taskActions.ts` | 组装 FormData 提交 robotConfig（含 rampUp） |
| `cmd/web/src/services/tasksApi.ts` | createTask / startTask API 封装 |
| `admin/task/types.go` | Go 类型：RampUpConfig / RampUpStage / TaskAssignment.RampUp |
| `admin/httpapi/task_routes.go` | 创建任务校验 + writeBaselineFiles |
| `admin/task/distribution.go`、`admin/task/completion.go`、`admin/task/stage_plan.go` | RampUp 比例缩放 + 阶段报告处理 + 段号映射 |
| `admin/command/dispatcher.go` | 任务下发时调用 ScaleRampUp |
| `admin/history/store.go` | 归档 stage_count + config archive + List/Get 读取 |
| `admin/mysql/schema_definition.go` | DB DDL：stage_count / stage_index / task_meta |
| `agent/task/types.go` | Agent 端 TaskAssignment.RampUp 类型 |
| `agent/task/runner.go` | 解析 rampUp 配置 + 调用 StartWithRampUp + OnStageChange/取消处理 |
| `robot/manager.go` | StartWithRampUp / resetBots / startBatch 核心执行 |
| `standalone/config.go`、`standalone/app.go` | 单机模式 rampUp TOML 配置与启动 |
| `monitor/collector.go`、`monitor/snapshot.go` | SetRampUpStage + StressSnapshot.rampUp（集群聚合取最小） |
| `cmd/web/src/components/monitoring/MonitorDock.tsx` | 运行时分段进度条（RampUpLoadView） |
| `cmd/web/src/components/monitoring/MonitorDock.css` | 分段进度条样式 + 脉冲动画 |
| `cmd/web/src/components/modules/history/HistoryModal.tsx` | 列表卡片阶段标签 + StageGroup 阶段组 |
| `cmd/web/src/components/modules/history/HistoryDetailView.tsx` | 折叠式阶段时间线 |
