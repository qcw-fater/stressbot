# 渐进式加压（RampUp）— 技术文档

> 本文档追踪渐进式加压功能的完整数据链路：从前端表单配置 → API 请求 → 后端校验与分配 → Agent 执行 → 运行时监控 → 历史归档与展示。

---

## 1. 概述

渐进式加压允许将 `totalBots` 个机器人分多个阶段逐步创建，而非一次性全部启动。每个阶段可独立配置：

| 字段 | 类型 | 说明 |
|------|------|------|
| `count` | int（必填 > 0） | 本阶段**新增**机器人数（增量值，各阶段之和须等于 totalBots） |
| `concurrency` | int?（可选） | 覆盖全局并发数；0 或空则用全局值 |
| `holdSec` | int?（可选） | 阶段间等待秒数，最后阶段可不填；最小值 30s（代码强制） |
| `reset` | bool?（可选） | 开始本阶段前清空所有已有机器人；第一阶段无意义（固定禁用） |

典型场景：
- 分批验证服务端承载能力（100 → 300 → 500 机器人）
- 重置阶段测试从零恢复的性能（500 机器人 → reset → 200 机器人）
- 调试模式下自动禁用（单 agent 分配，不支持 rampUp）

---

## 2. 前端启动配置

### 2.1 类型定义

**文件**: `cmd/web/src/types/api.ts`

```typescript
// 第 91-100 行
export interface RampUpStage {
  count: number;          // 本阶段新增 bot 数
  concurrency?: number;   // 覆盖全局并发
  holdSec?: number;       // 阶段间等待秒
  reset?: boolean;        // 开始前清空已有机器人
}

// 第 103-105 行
export interface RampUpConfig {
  stages: RampUpStage[];
}

// RobotConfig 内（第 87 行）
rampUp?: RampUpConfig;
```

### 2.2 Store 持久化

**文件**: `cmd/web/src/services/runtimeStore.ts`

| 字段 | 类型 | 默认值 | 持久化 |
|------|------|--------|--------|
| `rampUpEnabled` | `boolean` | `false` | localStorage `stressbot:runtime-form` v4 |
| `rampUpStages` | `RampUpStage[]` | `[{ count: 0, holdSec: 30 }]` | 同上 |

- 行 49-50：状态声明
- 行 80-81：setter（`setRampUpEnabled` / `setRampUpStages`）
- 行 284-292：`partialize` 选定两个字段持久化
- 打开启动弹窗时从 store 读取上次配置，关闭后自动恢复

### 2.3 表单 UI

**文件**: `cmd/web/src/components/runtime/TaskStartModal.tsx`（第 451-594 行）

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

- **调试模式互斥**（行 459）：`disabled={debugMode}` + 文案提示；切换调试模式时自动 `setRampUpEnabled(false)`（行 255）
- **总机器人数字段**（行 410-429）：开启 rampUp 后变为只读，显示 `rampUpSum`（各阶段 count 累加）
- **阶段重置禁用**（行 543-558）：第一阶段 `disabled={i === 0}`（无机器人可重置）
- **删除阶段**（行 583-588）：保留至少 1 个阶段（`rampUpStages.length > 1` 时才显示删除按钮）
- **合计显示**（行 589-594）：`rampUpSum > 0 ? 'success' : 'warning'` 颜色提示

### 2.4 计算逻辑

```typescript
// 行 216-218：rampUpSum — 各阶段 count 累加
const rampUpSum = rampUpStages.reduce((s: number, st) => s + (st.count || 0), 0);

// 行 223-235：peakBots — 峰值并发数（考虑 reset 阶段清空后的峰值）
// 即每个阶段累加 count，reset 时归零，取过程中最大值

// 行 238：rampUpValid — 校验全部阶段 count > 0
const rampUpValid = rampUpStages.every((st) => (st.count || 0) > 0);
```

### 2.5 提交组装

**文件**: `cmd/web/src/services/taskActions.ts`（行 262-272）

```typescript
// TaskStartModal 中的 startTask 调用
startTask({
  totalBots: rampUpEnabled ? rampUpSum : totalBots,
  robotConfig: {
    ...robotConfig,
    rampUp: rampUpEnabled ? { stages: rampUpStages } : undefined,
  },
  // ...
});
```

**文件**: `cmd/web/src/services/taskActions.ts`（行 157-161）

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

**文件**: `admin/types.go`

```go
// 第 129-133 行
type RampUpConfig struct {
    Stages []RampUpStage `json:"stages"`
}

// 第 136-145 行
type RampUpStage struct {
    Count       int  `json:"count"`
    Concurrency int  `json:"concurrency,omitempty"`
    HoldSec     int  `json:"holdSec,omitempty"`
    Reset       bool `json:"reset,omitempty"`
}

// RobotConfig 内（第 126 行）
RampUp *RampUpConfig `json:"rampUp,omitempty"`  // 指针类型
```

### 3.2 创建任务时的校验

**文件**: `admin/httpapi/task_routes.go`

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

### 3.3 分布式缩放：scaleRampUp

**文件**: `admin/task/distribution.go`

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

**调用位置**: `admin/command/dispatcher.go`

```go
TaskAssignment{
    RampUp: admintask.ScaleRampUp(rc.RampUp, taskTotalBots, a.TotalBots, assignments, a.AgentID),
    // ...
}
```

### 3.4 任务分配与下发

**文件**: `admin/httpapi/task_routes.go`、`admin/command/dispatcher.go`

- `handleStartTask()`（第 678-718 行）：调用 `assigner.Assign()` 计算分配方案
- `startTaskBackground()`（第 720-857 行）：并行推送到所有 Agent
- `TaskAssignment` 结构体含 `RampUp *RampUpConfig` 字段

---

## 4. Agent 执行

### 4.1 Agent 接收配置

**文件**: `agent/types.go`

```go
// 第 112 行
type TaskAssignment struct {
    RampUp *RampUpConfig `json:"rampUp,omitempty"`
    // ...
}
```

### 4.2 转换为 Manager 配置

**文件**: `agent/task/runner.go`

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
mgr := robot.NewManager(mgrCfg, flow, factory, dialer, luaPool)
mgr.OnStageReset = r.OnStageReset  // 阶段重置回调

// 根据是否有 rampUp 选择启动方式
if mgrCfg.RampUp != nil {
    startErr = mgr.StartWithRampUp()
} else {
    startErr = mgr.StartAll()
}
```

### 4.3 Manager 核心执行：StartWithRampUp

**文件**: `robot/manager.go`（第 155-251 行）

执行流程：

```
StartWithRampUp()
  │
  ├─ 计算总 bot 数 sum(stages.Count)
  │
  └─ 遍历每个 stage[i]:
       │
       ├─ 检查 ctx 取消（支持随时停止）
       │
       ├─ [如果 stage.Reset && len(robots) > 0]
       │    ├─ resetBots()         // 并发停止所有机器人，清空列表
       │    ├─ sleep(1s)           // flush 窗口，等待末次 IO 完成和计数器归位
       │    └─ OnStageReset(i)     // 回调 agent 上报阶段完成报告
       │
       ├─ 确定并发数：stage.Concurrency > 0 ? stage.Concurrency : 全局默认
       │  确定 holdSec：max(stage.HoldSec, 30)（最小 30 秒）
       │
       ├─ startBatch(offset, stage.Count, conc)
       │    │
       │    └─ 循环创建机器人:
       │         ├─ NewRobot(ID, Account, ...)
       │         ├─ robot.Start()
       │         └─ 限速: 每 conc 个暂停 1s（最后一批不等）
       │
       ├─ offset += stage.Count
       │
       └─ [如果不是最后阶段]
            └─ select { ctx.Done() | time.After(holdSec) }
               // 阶段保持等待，支持中途取消
```

### 4.4 resetBots — 阶段重置

**文件**: `robot/manager.go`（第 312-323 行）

```go
func (m *Manager) resetBots() {
    m.mu.Lock()
    robots := make([]*Robot, len(m.robots))
    copy(robots, m.robots)
    m.robots = m.robots[:0]       // 清空列表
    m.mu.Unlock()

    closeRobotsConcurrent(robots, nil)  // 并发 Close
    m.stopped.Store(0)                  // 重置停止计数器
}
```

要点：
- **并发关闭**：防止单个 robot 卡死阻塞阶段切换
- **保留 Manager 生命周期**：不取消 context、不关闭 doneCh，Manager 继续创建新机器人
- **重置 stopped 计数器**：避免跨阶段的停止数累积影响 `WaitDone` 的计数逻辑

### 4.5 startBatch — 批量创建机器人

**文件**: `robot/manager.go`（第 89-134 行）

```go
func (m *Manager) startBatch(fromIndex, count, conc int) (int, error) {
    created := 0
    for i := 0; i < count; i++ {
        // 检查取消
        if m.ctx.Err() != nil { return created, m.ctx.Err() }

        // 创建并启动机器人
        id := m.cfg.StartNumber + fromIndex + i
        account := fmt.Sprintf("%s%d", m.cfg.AccountPrefix, id)
        r := NewRobot(...)
        m.robots = append(m.robots, r)
        r.Start()
        created++

        // 限速：每 conc 个暂停 1s
        if conc > 0 && (i+1)%conc == 0 && (i+1) < count {
            select {
            case <-m.ctx.Done(): return created, m.ctx.Err()
            case <-time.After(time.Second):
            }
        }
    }
    return created, nil
}
```

### 4.6 阶段完成报告

**文件**: `admin/grpcapi/control_service.go`、`admin/task/completion.go`

Agent 在阶段重置时通过 `OnStageReset(stageIdx)` 回调向 Admin 上报阶段完成报告：

```go
func handleAgentTaskDone():
    isFinal := report.StageIndex <= 0   // -1 或 0 = 最终报告；> 0 = 阶段报告
```

- **阶段报告**（`StageIndex > 0`）：只刷新心跳，**不清空** `CurrentTaskID` / `LatestStress`，存入 `t.StageReports`，按 `(agentId, stageIndex)` 幂等去重
- **最终报告**（`StageIndex <= 0`）：走原有完成逻辑，检查是否所有 Agent 都已上报

### 4.7 中途取消处理

**文件**: `agent/task/runner.go`

```go
if startErr != nil {
    // 渐进式加压在 ctx cancel 后返回 context.Canceled，
    // 这是"用户主动停止"而非"失败"，按 TaskStopped 上报
    if ctx.Err() == context.Canceled || strings.Contains(startErr.Error(), context.Canceled.Error()) {
        return TaskStopped, ""
    }
    return TaskFailed, fmt.Sprintf("启动机器人失败: %v", startErr)
}
```

---

## 5. 运行时监控

### 5.1 前端阶段进度计算

**文件**: `cmd/web/src/components/monitoring/MonitorDock.tsx`（第 339-355 行）

```typescript
const rampUpStageInfo = useMemo(() => {
    if (!rampUpEnabled || rampUpStages.length === 0 || rampUpTotal === 0) return null;
    const running = latestStress?.robots?.running ?? 0;
    let cumulative = 0;
    for (let i = 0; i < rampUpStages.length; i++) {
        const prev = cumulative;
        cumulative += rampUpStages[i].count || 0;
        if (running <= cumulative) {
            const progress = Math.min(1, Math.max(0,
                (running - prev) / Math.max(1, rampUpStages[i].count)));
            return { current: i, progress };
        }
    }
    return { current: rampUpStages.length - 1, progress: 1 };
}, [rampUpEnabled, rampUpStages, rampUpTotal, latestStress?.robots?.running]);
```

**算法**：将当前 `running` 机器人数与各阶段累计阈值对比：
- `running <= 阶段 i 累计值` → 当前处于阶段 i，进度 = `(running - prev) / stage.count`
- `running > 最后阶段累计值` → 全部完成，进度 100%

### 5.2 分段进度条渲染

**文件**: `cmd/web/src/components/monitoring/MonitorDock.tsx`（第 423-455 行）

```
  渐进式 阶段 2/3         错 12
  ████████████░░░░░░░░░████████
  │──阶段1──│ │──阶段2──│ │─阶段3─│
  已完成     当前(脉冲)   未开始
```

- 每段宽度按 `stage.count / rampUpTotal` 比例分配
- 已完成阶段：`fill = 100%`（绿色 `--color-success`）
- 当前阶段：按 `progress` 填充 + 脉冲动画（蓝色 `md-stage-fill--active`）
- 未开始阶段：空

### 5.3 相关 CSS

**文件**: `cmd/web/src/components/monitoring/MonitorDock.css`（第 151-178 行）

```css
.md-stage-bar        { display: flex; gap: 2px; height: 6px; border-radius: 3px; }
.md-stage-segment    { height: 100%; border-radius: 1px; background: light-success; }
.md-stage-fill       { height: 100%; background: success; transition: width 0.6s; }
.md-stage-fill--active { background: blue; animation: md-stage-pulse 2s infinite; }
```

---

## 6. 历史归档

### 6.1 数据库 Schema

**文件**: `admin/history_schema.go`

| 表 | 字段 | 类型 | 说明 |
|---|---|---|---|
| `task_history` | `stage_count` | `INT DEFAULT 0` | 配置的阶段数（非上报数） |
| `task_report` | `stage_index` | `INT DEFAULT -1` | -1=整体最终；>0=连续 1-based 段落号 |
| `task_aggregated` | `stage_index` | `INT DEFAULT -1`（主键 `(task_id, stage_index)`） | 同上 |
| `task_timeseries` | `stage_index` | `INT DEFAULT -1` | 采样点所属段落号（仅 reset 任务 >0） |
| `task_config_archive` | `robot_config` | `JSON` | 完整 RobotConfig（含 rampUp） |
| `task_meta` | `(task_id, stage_index)` 主键 | — | 统一元数据：`starred` / `tags(JSON)` / `note` / `updated_at`。`stage_index=-1` 为任务级（所有任务），`>=1` 为 reset 各段落。行按需懒创建 |

> `task_history` **不再有** `starred/tags/note` 列——这三项已统一迁入 `task_meta`，与 `task_report/aggregated/timeseries` 的 `(task_id, stage_index)`（`-1`=整体）键约定保持一致。

索引：`task_report.idx_task_stage(task_id, stage_index)`、`task_timeseries.idx_task_stage_elapsed(task_id, stage_index, elapsed_sec)`、`task_meta.idx_starred(starred)`。

> 旧库升级：所有列/索引/主键变更已在 `deploy/upgrade.sql` 末尾以 `INFORMATION_SCHEMA` 守卫的存储过程
> `sb_upgrade_stage_history()` 提供，可重复执行（幂等），已在本地 Docker MySQL 9.7 验证。`deploy/` 不纳入版本控制。

### 6.2 归档写入

**文件**: `admin/history.go` — `Archive()` 方法

```go
// 行 149-152：从配置中计算 stageCount
stageCount := 0
if task.Config.RobotConfig.RampUp != nil {
    stageCount = len(task.Config.RobotConfig.RampUp.Stages)
}

// 行 159-173：写入 task_history（含 stage_count）
INSERT INTO task_history (..., stage_count) VALUES (..., ?)
ON DUPLICATE KEY UPDATE ..., stage_count=VALUES(stage_count)

// 行 205-215：阶段完成报告写入 task_report（含 stage_index）
// 遍历 task.StageReports，每条带 stage_index

// 行 233-240：完整 RobotConfig 写入 task_config_archive
// robot_config JSON 列保留完整 rampUp 配置
```

### 6.3 归档读取

**文件**: `admin/history.go`

- `List()`（第 280-301 行）：SELECT 含 `stage_count`，Scan 到 `HistoryRecord.StageCount`
- `Get()`（第 334-342 行）：SELECT 含 `stage_count`，Scan 到 `HistoryDetail.StageCount`

**文件**: `admin/types.go`

```go
// HistoryRecord（第 526 行）
StageCount int `json:"stageCount,omitempty"`
```

### 6.4 前端历史列表展示

**文件**: `cmd/web/src/components/modules/history/HistoryModal.tsx`（第 355-359 行）

列表卡片中，当 `stageCount > 0` 时在 metrics 行显示蓝色"X 阶段"标签：

```tsx
{!!r.stageCount && r.stageCount > 0 && (
    <span className="hp-metric">
        <span className="hp-metric__k">阶段</span>
        <span className="hp-metric__v" style={{ color: 'var(--color-blue)' }}>
            {r.stageCount} 阶段
        </span>
    </span>
)}
```

### 6.5 前端历史详情展示

**文件**: `cmd/web/src/components/modules/history/HistoryDetailView.tsx`

**头部标识**（第 267 行）：在摘要行中插入 `渐进式 N 阶段 ·`

**折叠式阶段时间线**（第 533-562 行）：

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
- chevron 点击展开/折叠，`transform: rotate(90deg)` 动画
- 每阶段：编号圆点 + 虚线连接 + 详细参数
- `reset` 标记显示为橙色 antd `<Tag color="warning">`

### 6.6 阶段段落历史（含 reset 的渐进式加压）

含 `reset=true` 的任务，每次 reset 前 Agent 会快照并 `Reset()` 采集器，因此整段被切分为多个**阶段段落**。
设计要点：

- **段号映射**（`admin/stage_plan.go`）：`buildStagePlan` 把 reset 边界（`OnStageReset` 上报的 0-based 配置下标，如 `{2,4}`）
  映射为连续 1-based 段落号（`{1,2}`）。段落 N 覆盖配置阶段范围 `[Sx, Sy]`，`PeakBots` = 段内各阶段增量之和。
  最后一段（`IsFinal`）的指标取自任务最终报告。
- **归档**（`admin/history.go` `Archive`）：
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

**前端阶段组**（`HistoryModal.tsx` `StageGroup`）：渲染为一张玻璃卡片，左侧竖条沿用普通记录的蓝→绿渐变
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
  │
  ▼  POST /sbot/tasks/{id}/start
后端 startTaskBackground (admin/httpapi/task_routes.go)
  │  assigner.Assign() → 每个 Agent 分配方案
  │  scaleRampUp(rampUp, total, assigned)  // 按比例缩放
  │  TaskAssignment.RampUp = scaled
  │
  ▼  Agent RPC 推送 (JSON)
Agent task.Runner.Run (agent/task/runner.go)
  │  转换为 robot.RampUpConfig
  │  mgr.OnStageReset = callback
  │  mgr.StartWithRampUp()
  │
  ▼
robot/manager.StartWithRampUp (robot/manager.go:155)
  │  遍历 stages:
  │    reset? → resetBots() + sleep(1s) + OnStageReset(i) → Admin 存 StageReport
  │    startBatch(offset, count, conc)  // 限速创建机器人
  │    非最后? → sleep(holdSec), select ctx.Done()
  │
  ├──► 运行时: MonitorDock.tsx
  │    rampUpStageInfo 推算当前阶段/进度
  │    分段进度条展示
  │
  └──► 任务结束: history.Archive()
       │  stage_count = len(config.Stages)  → task_history
       │  stage_reports                      → task_report (stage_index)
       │  robot_config JSON                  → task_config_archive
       │
       ▼  前端历史面板
       HistoryModal: 列表卡片 "X 阶段"
       HistoryDetailView: 头部标识 + 折叠式阶段时间线
```

---

## 8. 关键约束与设计决策

| 约束 | 说明 |
|------|------|
| 阶段 count 之和 = totalBots | 后端强制校验，前端提交时 totalBots 自动取 sum |
| holdSec 最小 30s | `StartWithRampUp` 中 `if holdSec < 30 { holdSec = 30 }` |
| 第一阶段不支持 reset | 前端 Switch `disabled={i === 0}`，后端不做额外校验 |
| 调试模式禁用 rampUp | 前端 Switch `disabled={debugMode}`，切换时自动关闭 |
| reset 后 1s flush | 给机器人末次 IO 和计数器留出归位窗口 |
| ctx 取消即时响应 | 每个关键节点（阶段开始前、限速等待、阶段保持）都 `select ctx.Done()` |
| 并发关闭 reset 机器人 | `closeRobotsConcurrent` 防止单个 robot 卡死阻塞阶段切换 |
| stopped 计数器重置 | `resetBots` 中 `m.stopped.Store(0)` 避免跨阶段累积 |
| scaleRampUp 保证总和 | floor + 按余量补齐，确保 `sum(counts) == assignedBots` |
| 阶段报告幂等去重 | `(agentId, stageIndex)` 作为唯一键，重复上报覆盖 |
| 中途取消按 Stopped 上报 | Agent 检测 `context.Canceled` 时返回 `TaskStopped` 而非 `TaskFailed` |

---

## 9. 涉及文件索引

| 文件 | 职责 |
|------|------|
| `cmd/web/src/types/api.ts` | 前端类型定义：RampUpStage / RampUpConfig / HistoryRecord.stageCount |
| `cmd/web/src/services/runtimeStore.ts` | rampUpEnabled / rampUpStages 状态持久化 |
| `cmd/web/src/components/runtime/TaskStartModal.tsx` | 启动表单：Switch + 阶段表格 + 校验 |
| `cmd/web/src/services/taskActions.ts` | 组装 FormData 提交 robotConfig（含 rampUp） |
| `cmd/web/src/services/tasksApi.ts` | createTask / startTask API 封装 |
| `admin/types.go` | Go 类型：RampUpConfig / RampUpStage / HistoryRecord.StageCount |
| `admin/httpapi/task_routes.go`、`admin/task/distribution.go`、`admin/task/completion.go` | 创建任务校验 + RampUp 分配 + 阶段报告处理 |
| `admin/history.go` | 归档 stage_count + config archive + List/Get 读取 |
| `admin/history_schema.go` | DB DDL：stage_count / stage_index 列 |
| `agent/types.go` | Agent 端 TaskAssignment.RampUp 类型 |
| `agent/task/runner.go` | 解析 rampUp 配置 + 调用 StartWithRampUp + 取消处理 |
| `robot/manager.go` | StartWithRampUp / resetBots / startBatch 核心执行 |
| `cmd/web/src/components/monitoring/MonitorDock.tsx` | 运行时阶段进度计算 + 分段进度条 |
| `cmd/web/src/components/monitoring/MonitorDock.css` | 分段进度条样式 + 脉冲动画 |
| `cmd/web/src/components/modules/history/HistoryModal.tsx` | 列表卡片阶段标签 |
| `cmd/web/src/components/modules/history/HistoryDetailView.tsx` | 折叠式阶段时间线 |
| `cmd/web/src/components/modules/history/HistoryPanel.css` | rampUp 折叠区域样式 |
