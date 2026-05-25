# 前端 API 接口文档

> **文档对象**：负责 stressbot 分布式压测管理后台**前端**的工程师。
> **目标**：让你不需要阅读后端代码即可独立设计/开发完整的管理界面。
> **配套文档**：`docs/design-distributed-master.md`（系统架构总览）、`docs/admin-implementation.md`（后端实现细节，可选阅读）。

---

## 0. 文档结构索引

- §1 系统说明（前端工程师必读，建立心智模型）
- §2 概念词表
- §3 通用约定（Base URL、Content-Type、错误码、轮询策略）
- §4 接口分组与页面映射
- §5 任务管理 API
- §6 Agent 管理 API
- §7 压测指标 API
- §8 系统指标 API
- §9 历史压测记录 API
- §10 日志查看器 API
- §11 完整 TypeScript 类型定义（可直接复制到前端项目）
- §12 历史数据与轮询策略
- §13 推荐页面布局
- §14 错误处理策略
- §15 完整响应示例

> ⚠️ **变更说明（2026-05）**：自动升级流程（原 §9.5~§9.8）与二进制管理（原 §9.1~§9.4）已废弃。
> 升级是低频操作，新版本部署改为 **运维手动重启 Agent 进程**。前端不再提供升级 UI，仅在 Agents 抽屉里提供"全部停止"按钮（等价于停止当前 active 任务）。

---

## 0.1 后端对齐项（Backend Alignment）

> ✅ **已全部完成**（2026-04-30）：以下 8 项已按文档约定实现，前端 `services/api.ts` 中的 `adaptList()` 兼容层可以安全移除。
>
> 后端额外补充了文档中未列出但前端需要的字段：`Assignment.agentName`、`AgentBrief.cpuPercent/memPercent/numGoroutine`、`ClusterSystemSnapshot.onlineCount/offlineCount/hotAgentName`。
> 所有 DELETE 接口已统一为 `204 No Content`。

| 接口 | 状态 | 实际返回 |
|---|---|---|
| `GET /api/tasks` | ✅ 已对齐 | `{ total, items: TaskBrief[] }`，支持 `?state=&limit=&offset=` |
| `POST /api/tasks` | ✅ 已对齐 | `{ id }`（201 Created） |
| `POST /api/tasks/{id}/stop` | ✅ 已对齐 | `TaskBrief`（202 Accepted） |
| `GET /api/agents` | ✅ 已对齐 | `{ items: AgentBrief[] }`，含系统指标摘要 |
| `GET /api/metrics` | ✅ 已对齐 | 直接返回 `CollectorSnapshot`（方案 A） |
| `GET /api/metrics/agents` | ✅ 已对齐 | `{ items: [{agentId, agentName, snapshot, updatedAt}] }` |
| `GET /api/system/agents` | ✅ 已对齐 | `{ items: [{agentId, agentName, status, snapshot, updatedAt, isStale}] }` |

**统一约定**：所有列表接口都返回 `{ items, total? }`，不直接返回数组。所有 DELETE 接口返回 `204 No Content`。

---

## 1. 系统说明

### 1.1 系统组成

stressbot 分布式压测系统包含四类角色：

| 角色 | 数量 | 你负责的部分 | 备注 |
|---|---|---|---|
| **前端** | 1 个 Web 应用 | ✅ **本文档** | React + Ant Design + Vite |
| **Admin** | 1 个进程 | ❌ 后端同事开发 | 控制中枢，所有前端 API 都向它请求 |
| **Agent** | N 个进程，分布在 N 台压测服务器 | ❌ 后端同事开发 | 实际执行压测的"工人"节点 |
| **被压测的游戏服务器** | N 个 | ❌ 项目外，被测对象 | 你完全不需要关心 |

### 1.2 你与谁通信

```
┌─────────────────────┐
│  你的前端代码        │
└──────────┬──────────┘
           │ HTTP / JSON
           │ 仅与 Admin 通信
           ▼
┌─────────────────────┐
│  Admin (:8080)      │ ← 唯一通信对象
└─────────────────────┘
```

**前端无需直连 Agent**。Admin 已聚合好所有数据，前端只对 Admin 做 HTTP 请求。

### 1.3 关键概念：聚合 vs 单点

Admin 上的指标接口分为两类：

| 类型 | URL 模式 | 用途 |
|---|---|---|
| **聚合接口** | `/api/metrics`、`/api/system` | 整个集群所有 Agent 数据合并后的视图（用于"总览大盘"） |
| **单点接口** | `/api/metrics/agents`、`/api/metrics/agents/{id}` | 单个 Agent 的视图（用于"节点详情页"） |

> 聚合的语义不是简单平均：QPS 是求和、延迟分位数是合并桶后重新插值、Apdex 是合并 satisfied/tolerating 后重算。这些 Admin 都已处理好，前端拿到的就是正确的合并值。

### 1.4 系统的"动作"概念

**动作（Action）** 是压测系统中最重要的概念，在指标里几乎所有数据都按"动作"维度组织。

- 每个 Action 代表机器人执行的一次行为，例如：登录、创建队伍、选英雄、加载战斗、推帧、结算等
- Action 的名称由后端配置决定（即 `flow.json` 中 actions 段的 key），前端只是消费方
- 几种特殊 Action：
  - `callback:OnXxx`：Action 名以 `callback:` 开头，表示这是服务器主动推送的回调（不是机器人发起的）
  - Lua 动作（v2 起）：名称无前缀；`avgSendBytes` / `avgRecvBytes` 由 lua 脚本通过统一返回值约定
    `return code, send_bytes, recv_bytes` 上报，运行时透传给 `monitor.RecordAction`，与声明式动作走同一条
    per-action 字节统计路径。旧版本中 lua 动作的字节列恒为 0，已修复。详见 `docs/design-web-editor.md`
    §7.6 与 `.claude/skills/flow-config/SKILL.md` §4。

**前端展示**：每个动作一行（表格 / 折线图），列出样本数、成功率、QPS、延迟分位数、Apdex 等。这是压测大盘最核心的视图。

### 1.5 任务生命周期（前端建模）

```
[创建任务] ─→ pending  ─启动─→ starting ─→ running ─停止─→ stopping ─→ stopped (完成态)
                                  └ 异常 ─→ failed                       └ failed
```

| 状态 | 含义 | UI 处理建议 |
|---|---|---|
| `pending` | 已创建但未启动 | 灰色 chip，显示"启动"按钮 |
| `starting` | 正在向 Agent 推送任务 | 黄色 chip + spinner |
| `running` | 正常运行中 | 绿色 chip + 实时指标流 |
| `stopping` | 收到停止指令，等 Agent 收尾 | 黄色 chip + spinner |
| `stopped` | 已正常完成 | 灰色 chip，可查看历史报表 |
| `failed` | 启动失败 / 集群崩溃 | 红色 chip，显示 `errorMsg` |

**重要约束：任务单例**

> **任意时刻全集群最多有 1 个"执行中"任务**（即 `starting` / `running` / `stopping` 状态的任务最多 1 个）。
>
> - `pending` 任务可同时存在多个（用户可预创建多个草稿）
> - 当已有 active 任务时再调用 `POST /api/tasks/{id}/start` → 返回 `409 TASK_CONFLICT`
> - 错误响应 `details` 字段会带上 `activeTaskId` / `activeName` / `activeState` / `startedAt`
>
> **UI 处理**：
> - 任务列表页置顶显示 active 任务（高亮一栏 banner："当前正在执行：xxx，[查看详情]"）
> - 启动按钮在已有 active 时禁用，hover tooltip："等当前任务完成后才能启动"
> - 收到 409 时弹窗："已有任务 [activeName] 正在 [activeState]，请先停止"，按钮 "去查看" → 跳转该任务详情

### 1.6 Agent 生命周期（前端建模）

```
注册 ─→ idle ──分配任务──→ busy ──任务完成──→ idle
              └─ unhealthy（30s 心跳缺失）─→ offline（60s 心跳缺失）
                                              └─→ idle/busy（重新心跳后恢复）
```

| 状态 | UI 颜色 | 行为 |
|---|---|---|
| `idle` | 蓝色圆点 | "空闲" |
| `busy` | 绿色圆点 | "执行中" 显示当前 taskId |
| `unhealthy` | 橙色圆点 | "心跳异常" 警告 |
| `offline` | 红色圆点 | "离线" |

---

## 2. 概念词表

| 术语 | 含义 |
|---|---|
| **Admin** | 中央管理服务（你唯一通信对象） |
| **Agent** | 一台压测服务器上跑的工作节点 |
| **Bot / Robot** | 一个被模拟的玩家实例 |
| **Action** | 机器人执行的一次行为（登录、攻击、移动等） |
| **Apdex** | 性能满意度评分（0~1，越接近 1 越好） |
| **QPS** | 每秒动作数 |
| **P50/P90/P95/P99** | 延迟百分位（毫秒）。P99 = 99% 的请求快于此值 |
| **Latency Bucket** | 后端用于分位数聚合的固定桶，前端不需要直接展示，但响应中可能包含 |

---

## 3. 通用约定

### 3.1 Base URL

| 环境 | URL |
|---|---|
| 开发（vite dev） | `http://localhost:5173`（Vite 自动 proxy `/api` 到 Admin） |
| 生产 | 由 Admin 直接托管前端，前端打包到 `web/dist`，访问 Admin 同源 |

vite proxy 配置（参考）：

```typescript
// web/vite.config.ts
proxy: {
  '/api': { target: 'http://localhost:8080', changeOrigin: true },
}
```

### 3.2 通用请求头

```
Content-Type: application/json    // POST/PUT 请求
Accept:       application/json
```

### 3.3 错误响应统一格式

任何非 2xx 响应都遵循：

```json
{
  "code": "TASK_NOT_FOUND",
  "message": "task task-01 not found",
  "details": {}
}
```

**HTTP 状态码语义**：

| 状态 | 触发 |
|---|---|
| `200` | 成功（同步操作） |
| `201` | 创建成功（POST 创建资源） |
| `202` | 异步接受（已加入处理队列，但还未完成） |
| `400` | 参数错误（请求体格式、字段缺失、字段值非法） |
| `404` | 资源不存在 |
| `409` | 状态冲突（如已存在 active 任务时再启动新任务） |
| `500` | 服务端故障 |

**错误码常量**：

| code | 含义 | 推荐 UI |
|---|---|---|
| `TASK_NOT_FOUND` | 任务不存在 | "任务不存在或已删除" |
| `TASK_INVALID_STATE` | 任务状态不允许此操作 | 显示 message |
| `TASK_CONFLICT` | **已有 active 任务**（单例约束） | 弹窗"已有任务 [activeName] 在 [activeState]，去查看？"，details 含 activeTaskId |
| `AGENT_NOT_FOUND` | Agent 不存在 | "节点不存在" |
| `AGENT_BUSY` | Agent 正忙 | 提示选其他节点 |
| `AGENT_OFFLINE` | Agent 离线 | "节点离线，无法操作" |
| `CAPACITY_EXCEEDED` | 集群容量不足 | "总机器人数超过集群最大容量" + details.maxBots |
| `HISTORY_NOT_FOUND` | 历史任务不存在 | "记录不存在或已被删除" |
| `HISTORY_STARRED` | 试图删除收藏的历史任务 | "已收藏，需 force=true" |
| `INVALID_ARGUMENT` | 参数非法 | 显示 message |

### 3.4 时间格式

所有时间字段为 **RFC3339 字符串**：`2026-04-29T10:30:00.123+08:00`。
前端用 `dayjs` 或 `date-fns` 解析显示，建议显示本地时区。

### 3.5 数值类型

- 所有 `*Bytes` / `*MB` / `*Ms` 字段为数值
- 整数字段直接是整数；带小数的字段（如 P99、CPU%）是浮点数
- 没有特殊的 "null"，缺失数据用零值（如延迟桶无样本时 P99=0）

### 3.6 轮询频率推荐

| 接口 | 推荐间隔 | 触发条件 |
|---|---|---|
| `/api/agents` | 5s | 持续 |
| `/api/system` | 5s | 持续（独立于任务） |
| `/api/metrics` | 5s | 任务 running 时 |
| `/api/metrics`（无任务） | 30s | idle 期 |
| `/api/tasks` | 10s | 任务列表页 |
| `/api/tasks/{id}` | 5s | 任务详情页 |

> Admin 服务端 5s 一次接收 Agent 上报，所以前端 5s 轮询是最理想的。轮询间隔小于 5s 会拿到与上次相同的数据，浪费请求。

---

## 4. 接口分组与页面映射

| 页面 | 主要 API | 辅助 API |
|---|---|---|
| 首页 / 总览大盘 | `/api/agents`、`/api/system`、`/api/tasks` | — |
| 任务管理列表 | `/api/tasks` | — |
| 任务详情（运行中） | `/api/tasks/{id}`、`/api/metrics` | `/api/metrics/agents` |
| 任务创建表单 | `POST /api/tasks` | `/api/agents`（计算容量） |
| Agent 列表 | `/api/agents` | `/api/system`（指标摘要） |
| Agent 详情 | `/api/agents/{id}`、`/api/metrics/agents/{id}`、`/api/system/agents/{id}` | — |
| 系统资源大盘 | `/api/system`、`/api/system/agents` | — |
| **历史压测列表** | `/api/history` | `/api/history/tags` |
| **历史压测详情** | `/api/history/{id}` | `/api/history/{id}/timeseries`、`/api/history/{id}/agents` |
| **历史对比** | `/api/history/compare?ids=...` | — |
| **日志查看器** | `/api/logs/admin`、`/api/logs/agents/{id}` | `/api/logs/admin/files`、`/api/logs/agents/{id}/files` |

---

## 5. 任务管理 API

### 5.1 列出任务

```
GET /api/tasks
GET /api/tasks?state=running          // 可选过滤
GET /api/tasks?limit=20&offset=0      // 分页
```

**响应** `200 OK`：

> ✅ **已对齐**：返回 `{ total, items: TaskBrief[] }`，支持 `?state=&limit=&offset=` 过滤分页。

```typescript
type TasksListResponse = {
  total: number;
  items: TaskBrief[];
};

type TaskBrief = {
  id: string;                 // taskID（UUID）
  name: string;
  state: TaskState;
  totalBots: number;          // 集群总机器人数
  agentCount: number;         // 分配到几个 Agent
  createdAt: string;          // ISO 8601
  startedAt?: string;
  stoppedAt?: string;
};

type TaskState = 'pending' | 'starting' | 'running' | 'stopping' | 'stopped' | 'failed';
```

**用途**：任务列表页主数据源。

### 5.2 任务详情

```
GET /api/tasks/{id}
```

**响应** `200 OK`：

```typescript
type TaskDetail = TaskBrief & {
  config: {
    robotConfig: {
      // 必填
      authAddr: string;          // 必须 http:// 或 https:// 开头
      concurrency: number;       // 每秒新建机器人数
      timeoutSec: number;        // TCP 请求超时秒数
      // 业务可变
      accountPrefix?: string;    // 账号前缀，默认 "bot_"
      startNumber?: number;      // 账号编号起点，默认 0；账号 = accountPrefix + (startNumber + N)
      mainService?: string;      // 主连接服务名，默认 "logic"
      authExtra?: Record<string, string>; // Auth 扩展字段（前端不再预填，由用户手动添加）
      // 性能/超时
      heartbeatSec?: number;     // 心跳间隔秒，默认 5
      httpTimeoutSec?: number;   // HTTP 超时秒，默认 10
      apdexT?: number;           // Apdex 满意阈值毫秒，默认 100
      // 日志
      logLevel?: 'debug' | 'info' | 'warn' | 'error'; // 任务期临时切换 Agent 日志等级
    };
    deadline?: string;
    flowFiles: string[];        // 已上传的配置文件名
  };
  assignments: Assignment[];    // 集群分配快照
  errorMsg?: string;            // state=failed 时
  reports?: Record<string, TaskCompletionReport>; // agentId → report，task 终态时存在
};

type Assignment = {
  taskId: string;
  agentId: string;
  agentName: string;            // 冗余便于展示
  startNumber: number;          // 账号起始
  totalBots: number;            // 本节点机器人数
};

type TaskCompletionReport = {
  agentId: string;
  taskId: string;
  result: 'completed' | 'stopped' | 'failed';
  errorMsg?: string;
  finishedAt: string;
};
```

**用途**：任务详情页，展示任务配置、分配情况、完成报告。

**UI 推荐**：
- `assignments` 用表格展示：Agent 名 / 账号范围 / 机器人数
- `errorMsg` 高亮（红色文本块）
- `reports` 按 result 分类：completed 绿色、stopped 灰色、failed 红色

### 5.3 创建任务

```
POST /api/tasks
Content-Type: multipart/form-data
```

**请求 multipart 字段**：

| 字段 | 类型 | 必需 | 说明 |
|---|---|---|---|
| `name` | string | ✅ | 任务名 |
| `totalBots` | string (int) | ✅ | 集群总机器人数 |
| `flow.json` | file | ✅ | 流程定义 |
| `proto/<filename>` | file | ❌ | 多个 .proto 文件，字段名必须以 `proto/` 前缀 |
| `scripts/<filename>` | file | ❌ | 多个 .lua 文件，字段名必须以 `scripts/` 前缀 |
| `robotConfig` | string (JSON) | ✅ | RobotConfig 序列化字符串 |
| `deadline` | string (RFC3339) | ❌ | 自动停止时间 |

`robotConfig` JSON 示例：

```json
{
  "authAddr": "http://auth.example.com:20000",
  "concurrency": 50,
  "timeoutSec": 60,
  "accountPrefix": "bot_",
  "startNumber": 0,
  "mainService": "logic",
  "authExtra": {
    "version": "0.31.49.171222",
    "channel": "mine",
    "platform": "1000"
  },
  "heartbeatSec": 5,
  "httpTimeoutSec": 10,
  "apdexT": 100,
  "logLevel": "info"
}
```

**字段说明**：

| 字段 | 类型 | 必需 | 默认 | 说明 |
|---|---|---|---|---|
| `authAddr` | string | ✅ | — | Auth 服务地址，必须以 `http://` 或 `https://` 开头，否则后端会自动补 `http://` 并打 warning |
| `concurrency` | int | ✅ | — | 每秒新建机器人数 |
| `timeoutSec` | int | ✅ | — | TCP 请求超时秒数（兼容旧字段，作为通用兜底） |
| `accountPrefix` | string | ❌ | `bot_` | 账号前缀，用于区分压测批次 |
| `startNumber` | int | ❌ | `0` | 账号编号起点；admin 在分配时把它作为各 agent cursor 的起点，最终账号 = `accountPrefix + (startNumber + 全局序号)`。已有 `bot_0~bot_99` 在线时设 `100` 可避免账号撞车 |
| `mainService` | string | ❌ | `logic` | 主连接对应的服务标识，不同游戏命名不同 |
| `authExtra` | Record<string,string> | ❌ | `{}` | Auth 请求附带的扩展字段；lua 脚本通过 `robot.get(key)` 读取。**注意**：前端默认空，不再硬编码 version/channel/platform 推荐项，由用户在「高级设置 → 添加字段」中手动添加；不配置则脚本取到 nil 走兜底默认值 |
| `heartbeatSec` | int | ❌ | `5` | 心跳间隔秒数 |
| `httpTimeoutSec` | int | ❌ | `10` | HTTP 请求超时秒数（独立于 `timeoutSec`） |
| `apdexT` | int | ❌ | `100` | Apdex 满意阈值毫秒（动作响应 ≤ T 完全满意；> 4T 不满意） |
| `logLevel` | enum | ❌ | 沿用 Agent 配置 | `debug`/`info`/`warn`/`error`。任务期临时切换 Agent 进程日志等级，结束后**自动恢复**为 Agent 启动配置中的等级，不影响后续任务。前端「调试模式」会自动装填 `debug` |

**Admin 转发约定**：admin 收到 `RobotConfig` 后，**用合理默认值填充**未指定字段，再转换为 `TaskAssignment` 下发到 Agent：
- `accountPrefix` 空 → `"bot_"`
- `startNumber` < 0 → 归 0；admin 在调用 `Assigner.Assign(task, agents, startNumber)` 时把它作为各 agent cursor 的起点（每个 agent 收到的 `Assignment.StartNumber` 在该起点上累加）
- `mainService` 空 → `"logic"`
- `heartbeatSec`/`timeoutSec`/`httpTimeoutSec` ≤ 0 → 各自默认 → 转 duration 字符串（`"5s"` / `"60s"` / `"10s"`）
- `apdexT` ≤ 0 → `100`
- `authExtra` nil → 空 map

**前端启动时引导**：开发期 `EditorPage` 会从 `/conf/config.json` 读取单机配置同步到 RobotConfig 默认值（仅当用户未改过时），让用户首次打开就拿到与单机一致的连接参数。**例外**：`auth.extra` 不参与同步（用户诉求"完全手动控制"），单机配置里的 `version`/`channel`/`platform` 不会自动出现在 Web 端。

**响应** `201 Created`：

> ✅ **已对齐**：返回 `{ id }`（201 Created）。

```typescript
type CreateTaskResponse = {
  id: string;          // 新建的 taskID
};
```

**前端示例**：

```typescript
async function createTask(form: TaskFormState): Promise<string> {
  const fd = new FormData();
  fd.append('name', form.name);
  fd.append('totalBots', String(form.totalBots));
  fd.append('flow.json', form.flowFile);
  for (const f of form.protoFiles) fd.append(`proto/${f.name}`, f);
  for (const f of form.luaFiles)   fd.append(`scripts/${f.name}`, f);
  fd.append('robotConfig', JSON.stringify(form.robotConfig));

  const res = await fetch('/api/tasks', { method: 'POST', body: fd });
  const data = await res.json();
  return data.id;
}
```

**校验失败响应** `400`：

```json
{ "code": "INVALID_ARGUMENT", "message": "flow.json invalid: missing required action 'Auth'", "details": {} }
```

#### 5.3.1 调试模式（前端语义）

**前端语义**：调试模式（`editorStore.debugMode`）是开发/排障态的快速开关，启用后会自动装填一组保守预设：

| 字段 | 调试预设 | 说明 |
|---|---|---|
| `totalBots` | `1` | 单机器人，便于追踪单条流程 |
| `robotConfig.concurrency` | `1` | 串行新建，时序清晰 |
| `robotConfig.logLevel` | `"debug"` | Agent 任务期切到 debug 等级，打印完整收发包与字段绑定 |
| `taskName` | `debug · MMDD-HHmm` | 仅在仍为初始默认 `未命名任务` 时改名 |
| `skipCapacityCheck` | `true` | 跳过容量预检，无在线 Agent 仍可提交（服务端兜底） |

**后端无需新增字段**：调试模式完全在前端表达，提交到后端的依然是标准 `robotConfig` JSON 中的 `logLevel="debug"`。Agent 收到 `TaskAssignment.logLevel` 后调用 `stresslog.SetLogLevel(...)` 临时改写进程日志等级，任务结束（成功 / 失败 / 取消）后会**defer 恢复**为 Agent 启动时的等级，不污染后续任务。

**注意**：
- 关闭前端调试模式不会回滚 `robotConfig.logLevel`，让用户保留偏好；如需正式压测请手动改回 `info`。
- `logLevel` 字段对所有任务都可用，不依赖调试模式 —— 你可以单独为某次 200v200 压测开 debug 等级以收集详细日志。

### 5.4 启动任务

```
POST /api/tasks/{id}/start
```

无请求体。

**响应** `202 Accepted`：

```typescript
type StartTaskResponse = {
  taskId: string;
  assignments: Assignment[];   // 分配方案
};
```

任务状态会异步从 `starting` → `running`，前端轮询 `/api/tasks/{id}` 检测状态变化。

**常见错误**：

| 状态 | code | 触发 |
|---|---|---|
| `409` | `TASK_INVALID_STATE` | 任务不在 pending |
| `400` | `CAPACITY_EXCEEDED` | 集群空闲容量不足，details 含 `availableBots` |
| `400` | `INVALID_ARGUMENT` | 没有可用 Agent |

### 5.5 停止任务

```
POST /api/tasks/{id}/stop
```

无请求体。

**响应** `202 Accepted`：返回当前 task 简要信息。

> ✅ **已对齐**：返回 `TaskBrief`（202 Accepted），含 state、totalBots、agentCount、startedAt、stoppedAt 等字段。

任务状态变为 `stopping`，最终（< 1min）变为 `stopped`。

### 5.6 删除任务

```
DELETE /api/tasks/{id}
```

仅允许 `stopped` / `failed` 状态删除，其他状态返回 `409`。

**响应** `204 No Content`。

### 5.7 下载任务配置（前端无需直接调用）

```
GET /api/tasks/{id}/config/{path}
```

由 Agent 内部使用拉取配置文件。支持以下路径：
- `flow.json` — 流程定义
- `config.json` — 运行时配置（authAddr、concurrency、timeoutSec、deadline）
- `proto/<filename>` — proto 文件
- `scripts/<filename>` — Lua 脚本

前端可在任务详情中展示 `flowFiles` 字段，给"下载"按钮直接 a 标签链接到此 URL。

---

## 6. Agent 管理 API

### 6.1 列出所有 Agent

```
GET /api/agents
```

**响应** `200 OK`：

> ✅ **已对齐**：返回 `{ items: AgentBrief[] }`，每项含 `cpuPercent`/`memPercent`/`numGoroutine` 系统指标摘要。

```typescript
type AgentsListResponse = {
  items: AgentBrief[];
};

type AgentBrief = {
  agentId: string;
  name: string;
  address: string;          // "http://10.0.0.1:7070"
  appVersion: string;       // "v1.2.0"
  maxBots: number;
  status: AgentStatus;
  currentTaskId?: string;
  currentBots: number;      // 当前运行中机器人数

  staticInfo: StaticInfo;

  lastHeartbeatAt: string;
  stressUpdatedAt?: string;
  systemUpdatedAt?: string;

  // 系统指标摘要（用于列表页快速预览，不含完整 SystemSnapshot）
  cpuPercent?: number;
  memPercent?: number;
  numGoroutine?: number;
};

type StaticInfo = {
  hostname: string;
  os: 'linux' | 'windows' | 'darwin';
  arch: 'amd64' | 'arm64';
  numCpu: number;
  memTotalMB: number;
  goVersion: string;
  kernelVer: string;
  startedAt: string;
};

type AgentStatus = 'idle' | 'busy' | 'unhealthy' | 'offline';
```

**用途**：Agent 列表页主数据。

**UI 推荐字段**：
- 名称 / 状态指示灯（颜色见 §1.6）
- 地址 / OS / CPU 核数 / 内存
- 当前 CPU% / Mem% / Goroutine（彩色进度条 / 数值）
- AppVersion（用于核对版本是否一致；新版本由运维手动重启 Agent 部署）
- 当前任务（带链接到任务详情）

### 6.2 Agent 详情

```
GET /api/agents/{id}
```

**响应** `200 OK`：

```typescript
type AgentDetail = AgentBrief & {
  // 完整系统快照
  latestSystem?: SystemSnapshot;     // 见 §8 SystemSnapshot 完整结构
};
```

> 完整压测快照在 `/api/metrics/agents/{id}`（避免一次性塞太多）。

### 6.3 强制注销 Agent

```
DELETE /api/agents/{id}
```

仅允许 `offline` 状态删除（避免误删运行中节点）。

**响应** `204` 或 `409`（状态不允许）。

### 6.4 批量停止（前端语义）

> 系统是单 active 任务模型，"停止所有 Agent" 在功能上 ≡ "停止当前 active 任务"。
> 因此前端 AgentsDrawer 的「全部停止」按钮直接调 `POST /api/tasks/{activeId}/stop`（见 §5.5），无需新接口。
>
> **可选 🔴 后端 TODO（兜底场景）**：当 Admin 与 Agent 状态不一致（如 Admin 误判 task 已结束、但 Agent 仍持有 bot），前端无 active task 可停。此时若有 `POST /api/agents/stop-all`（强制让所有 Agent drain 到 idle）可作为运维兜底。当前优先级低，单任务模型下不会触发。

---

## 7. 压测指标 API

> 所有压测指标响应共享同一份 schema（`StressSnapshot`），方便复用组件。

### 7.1 集群聚合压测快照（推荐主数据源）

```
GET /api/metrics
GET /api/metrics?taskId=task-01    // 可选：指定任务（默认是当前 running 任务）
```

**响应** `200 OK`：

> ✅ **已对齐**（方案 A）：直接返回 `CollectorSnapshot`。无 active 任务时返回空快照（字段全 0）。

```typescript
type StressSnapshot = {
  // === 顶层 ===
  timestamp: string;          // ISO 时间
  uptimeSeconds: number;      // 任务/集群运行时长
  totalActions: number;       // 全部动作累计执行次数
  apdexT: number;             // Apdex T 阈值（毫秒）

  // === 全局视图 ===
  robots: RobotsView;         // 机器人状态
  connections: ConnectionsView;
  bandwidth: BandwidthView;

  // === 动作明细 ===
  actions: ActionMetric[];

  // === 集群专属 ===
  clusterInfo?: ClusterInfo;  // 仅在聚合接口出现
};

type RobotsView = {
  started: number;     // 累计启动数
  running: number;     // 当前在线数
  stopped: number;     // 正常停止数
  errored: number;     // 异常退出数
  // 恒等：started = running + stopped + errored
};

type ConnectionsView = {
  established: number; // 累计成功建立连接数
  failed: number;      // 连接失败数
  dropped: number;     // 运行中断连数
};

type BandwidthView = {
  // 全局收发字节数（"网卡级"）。统计点位于 network 层：
  //   - 出站：connection.Send 成功后累加 len(data)；
  //   - 入站：gnet.OnTraffic 解出完整帧后累加 totalLen。
  // 因此心跳、监听推送、所有失败/超时请求、UDP 单向发送都会被计入；
  // 而 actions[].avgSendBytes / avgRecvBytes 仍是 per-action 维度，仅在 RecordAction
  // 的 success 路径累加，两者口径不同、互不重复。
  totalSendBytes: number;
  totalRecvBytes: number;
  sendMBps: number;       // = totalSendBytes / 1024² / uptimeSec
  recvMBps: number;
};

type ActionMetric = {
  name: string;          // 动作名
  sampleCount: number;   // = success + failure + timeout（不含 skipped）
  successCount: number;
  failureCount: number;
  timeoutCount: number;
  skippedCount: number;  // 必填字段为空，未发送的次数
  executing: number;     // 当前正在执行该动作的并发数

  successRate: number;   // 0~1
  apdex: number;         // 0~1
  avgQps: number;        // 全程平均 QPS

  avgSendBytes: number;  // 仅成功样本
  avgRecvBytes: number;
  timeoutAvgMs: number;  // timeoutCount=0 时为 0

  latency: HistogramView; // 仅成功样本

  errors?: ErrorBucket[]; // 失败/超时时存在
};

type HistogramView = {
  count: number;
  minMs: number;
  maxMs: number;
  avgMs: number;
  p50Ms: number;
  p90Ms: number;
  p95Ms: number;
  p99Ms: number;
};

type ErrorBucket = {
  msg: string;       // 错误消息（已截断到 120 字符）
  count: number;
};

type ClusterInfo = {
  agentCount: number;          // 参与统计的 Agent 数
  agentIds: string[];          // 参与统计的 agentID
  staleAgentIds: string[];     // 上报数据 > 30s 未更新的 agent，仅参考
};
```

### 7.2 各 Agent 压测快照（per-agent 列表）

```
GET /api/metrics/agents
GET /api/metrics/agents?taskId=task-01
```

**响应** `200 OK`：

> ✅ **已对齐**：返回 `{ items: [{agentId, agentName, snapshot, updatedAt}] }`。

```typescript
type PerAgentMetrics = {
  items: Array<{
    agentId: string;
    agentName: string;
    snapshot: StressSnapshot;   // 与 §7.1 同 schema，无 clusterInfo
    updatedAt: string;
  }>;
};
```

**用途**：当前任务下每个 Agent 的实时压测视图。可用于"对比表"或"per-agent 折线"。

### 7.3 单个 Agent 压测快照

```
GET /api/metrics/agents/{agentId}
```

**响应** `200 OK`：直接是 `StressSnapshot`。

### 7.4 文本摘要（调试用）

```
GET /api/metrics/summary
Content-Type: text/plain
```

返回纯文本：

```
uptime: 2m30s
agents: 3 (busy=3, idle=0)
robots: started=300 running=298 stopped=0 errored=2
CreateNormalTeam: samples=850 success=842 timeout=0 failure=8 avg=45.7ms p99=450.6ms apdex=0.95 qps=5.7
SelectHero:       samples=842 success=840 timeout=0 failure=2 avg=12.3ms p99=25.1ms apdex=1.00 qps=5.6
```

前端可直接渲染 `<pre>` 给运维快速诊断使用。

### 7.5 指标计算公式（前端只读，了解即可）

#### Apdex

```
T = apdexT（默认 100ms）
satisfied   = 成功且延迟 < T 的样本数
tolerating  = 成功且 T ≤ 延迟 < 4T 的样本数
total       = success + failure + timeout

apdex = (satisfied + tolerating × 0.5) / total
```

| Apdex 范围 | 含义 | UI 颜色 |
|---|---|---|
| 0.94 ~ 1.00 | 优秀 | 绿色 |
| 0.85 ~ 0.93 | 良好 | 浅绿 |
| 0.70 ~ 0.84 | 一般 | 黄色 |
| 0.50 ~ 0.69 | 较差 | 橙色 |
| < 0.50 | 危险 | 红色 |

#### 延迟分位数

P50 / P90 / P95 / P99 由后端基于固定桶直方图插值计算。**集群聚合接口的分位数是合并后重新计算的，不是平均**。前端拿到值直接用即可。

#### QPS

- `avgQps` = 全程平均 = `sampleCount / uptime`
- **周期 QPS（瞬时）**：响应里**不存在**此字段，前端自行用相邻两次轮询的差分计算：

```typescript
const periodQps = (curr.sampleCount - prev.sampleCount) / ((currTime - prevTime) / 1000);
```

参考实现见 §13.3。

### 7.6 动作分类与 UI 处理

| 类别 | 名称特征 | 字段差异 | UI 处理 |
|---|---|---|---|
| 普通动作 | 任意名 | 完整字段 | 表格 + 详情 |
| 推送回调 | `callback:OnXxx` | `avgSendBytes=0`、`timeoutCount=0` | 加 "←推送" 标记，单独列出 |
| Lua 动作 | 任意名 | 与普通动作一致；脚本若未按新约定返回 `code, send, recv`，则 `avgSendBytes / avgRecvBytes` 为 0 | 与普通动作同列展示 |

> **Lua 动作字节归因（v2）**：lua 脚本统一返回 `(code, send_bytes, recv_bytes)`，
> `send / recv` 由 lua API 的多返回值累加（如 `network.tcp_send` 第 2 个返回值、
> `network.request` 第 3、4 个返回值）。引擎层 `RunActionScript` 把它们透传给
> `monitor.RecordAction`，所以 lua 动作和声明式动作走同一条 per-action 字节统计路径，
> ActionsTab 的 ↑avg / ↓avg 列对二者表现一致。仅 connect/init 类、纯本地计算等
> 无 IO 的 lua 动作字节为 0 才正常。

---

## 8. 系统指标 API

> 系统指标是 Agent 所在物理机的资源数据（CPU、内存、网络、线程等），与压测业务无关。即使任务未运行，Agent 也持续上报。

### 8.1 集群系统聚合

```
GET /api/system
```

**响应** `200 OK`：

```typescript
type ClusterSystemSnapshot = {
  timestamp: string;
  agentCount: number;          // 总 Agent 数
  onlineCount: number;         // 在线（idle/busy/unhealthy）
  offlineCount: number;

  // 资源汇总
  totalMemMB: number;          // 集群总内存
  usedMemMB: number;
  avgCpuPercent: number;       // 算术平均（非加权）
  maxCpuPercent: number;       // 最大值
  totalNetSendKBps: number;    // 网络速率求和
  totalNetRecvKBps: number;
  totalGoroutines: number;     // 求和
  totalThreads: number;
  totalFds: number;

  // 节点排序信息
  hotAgentId?: string;         // CPU 最高的节点
  hotAgentName?: string;
};
```

**UI 推荐**：放在系统资源大盘最顶部的"集群总览"卡片。

### 8.2 各 Agent 系统快照

```
GET /api/system/agents
```

**响应** `200 OK`：

> ✅ **已对齐**：返回 `{ items: [{agentId, agentName, status, snapshot, updatedAt, isStale}] }`。

```typescript
type PerAgentSystem = {
  items: Array<{
    agentId: string;
    agentName: string;
    status: AgentStatus;
    snapshot: SystemSnapshot;
    updatedAt: string;
    isStale: boolean;     // 上报 > 30s 未更新
  }>;
};
```

### 8.3 单个 Agent 系统快照

```
GET /api/system/agents/{agentId}
```

**响应** `200 OK`：

```typescript
type SystemSnapshot = {
  timestamp: string;

  // === CPU ===
  cpuPercent: number;          // 整体 CPU% (0~100)
  cpuPerCore: number[];        // 每个核心的 CPU%
  loadAvg1: number;            // Linux 1分钟负载，Windows 为 0
  loadAvg5: number;
  loadAvg15: number;

  // === 内存 ===
  memTotalMB: number;
  memUsedMB: number;
  memPercent: number;          // 0~100
  swapUsedMB: number;

  // === Agent 进程 ===
  processRssMB: number;        // 物理常驻内存
  processHeapMB: number;       // Go 堆
  processSysMB: number;        // 进程总占用
  numGoroutine: number;        // Goroutine 数
  numThread: number;           // OS 线程数
  numFd: number;               // 文件描述符（Windows 上可能为 0）

  // === 网络（速率，差分计算）===
  netSendKBps: number;
  netRecvKBps: number;

  // === GC ===
  gcCount: number;
  gcPauseAvgMs: number;
};
```

### 8.4 字段含义详解（前端展示参考）

| 字段 | 推荐展示 | 阈值参考 | 备注 |
|---|---|---|---|
| `cpuPercent` | 仪表盘 / 折线 | > 80% 黄、> 95% 红 | 短时高峰可忽略 |
| `cpuPerCore[]` | 横向 bar chart | — | 长度 = numCpu，可发现核心倾斜 |
| `loadAvg1/5/15` | 数值 | > numCpu × 1.5 警告 | Windows 隐藏 |
| `memPercent` | 进度条 | > 85% 警告 | 注意区分 OS 内存与 Go 堆 |
| `processHeapMB` | 数值 + 折线 | 持续上涨警告 | 内存泄漏诊断 |
| `numGoroutine` | 数值 + 折线 | 突增警告 | 异常协程泄漏指标 |
| `numThread` | 数值 | < 200 正常 | 极端情况下可能膨胀 |
| `netSendKBps`/`netRecvKBps` | 折线 | 与机器网卡上限对比 | 第一次采集为 0（基线初始化） |
| `gcCount` | 数值 | — | 增量看 GC 频率 |
| `gcPauseAvgMs` | 数值 | < 5ms 正常 | 长 STW 警告 |

---

## 9. 历史压测记录 API

> 历史功能存储所有终态（stopped / failed）任务的完整数据到 MySQL，供事后查看报告、版本对比、问题排查使用。
> 用户可对历史任务打标签（tags）、收藏（star）、加备注（note）。

### 9.1 历史列表

```
GET /api/history?limit=20&offset=0&state=stopped&tags=v1.2&starred=true
```

**Query 参数（全部可选）**：

| 参数 | 类型 | 说明 |
|---|---|---|
| `limit` | int | 每页大小（默认 20，最大 100） |
| `offset` | int | 偏移量 |
| `state` | string | 过滤：`stopped` / `failed`（不传则两者都返回） |
| `startedAfter` | RFC3339 | 起始时间 ≥ |
| `startedBefore` | RFC3339 | 起始时间 ≤ |
| `tags` | string（可重复） | 标签过滤（命中**任意一个**即匹配，如 `?tags=a&tags=b`） |
| `tagsAll` | string（可重复） | 标签过滤（必须**全部匹配**） |
| `starred` | bool | 仅显示收藏 |
| `search` | string | 模糊匹配 name + note（自动 LIKE %xxx%） |
| `orderBy` | string | 排序字段：`started_at desc`（默认）、`duration_sec desc`、`stopped_at desc` |

**响应** `200 OK`：

```json
{
  "total": 38,
  "items": [
    {
      "id": "task-abc",
      "name": "200v200 压测 v1.2",
      "state": "stopped",
      "totalBots": 200,
      "agentCount": 2,
      "createdAt": "2026-04-29T09:55:00+08:00",
      "startedAt": "2026-04-29T10:00:00+08:00",
      "stoppedAt": "2026-04-29T10:30:00+08:00",
      "durationSec": 1800,
      "errorMsg": "",
      "starred": true,
      "tags": ["benchmark", "v1.2"],
      "note": "Hash 冲突修复后回归",
      "configSummary": {
        "authAddr": "127.0.0.1:6000",
        "concurrency": 50,
        "timeoutSec": 30,
        "flowSizeKB": 12,
        "protoCount": 8,
        "scriptCount": 3
      }
    }
  ]
}
```

**UI 推荐**：
- 列表行展示：name、状态徽章、起止时间、时长、tags（多色 chip）、starred 图标
- 顶部筛选区：日期范围、状态、标签多选（autocomplete 来源 `/api/history/tags`）、搜索框、收藏过滤
- 行操作：查看详情 / 编辑标签 / 克隆 / 删除（starred 时隐藏删除）
- starred 行高亮置顶或加金色边框

### 9.2 全部使用过的标签

```
GET /api/history/tags
```

**响应** `200 OK`：

```json
{
  "tags": ["benchmark", "v1.0", "v1.1", "v1.2", "before-fix-hash", "stress-2k"]
}
```

**UI 推荐**：在列表筛选区的 tag 多选框作 autocomplete 数据源；编辑标签时也用此数据。

### 9.3 历史任务详情

```
GET /api/history/{id}
```

**响应** `200 OK`：（在列表项基础上扩展）

```json
{
  "id": "task-abc",
  "name": "200v200 压测 v1.2",
  "state": "stopped",
  "totalBots": 200,
  "agentCount": 2,
  "createdAt": "2026-04-29T09:55:00+08:00",
  "startedAt": "2026-04-29T10:00:00+08:00",
  "stoppedAt": "2026-04-29T10:30:00+08:00",
  "durationSec": 1800,
  "errorMsg": "",
  "starred": true,
  "tags": ["benchmark", "v1.2"],
  "note": "Hash 冲突修复后回归",
  "configSummary": { /* 同列表 */ },
  "assignments": [
    { "taskId": "task-abc", "agentId": "agent-1", "startNumber": 0,   "totalBots": 100 },
    { "taskId": "task-abc", "agentId": "agent-2", "startNumber": 100, "totalBots": 100 }
  ],
  "agentReports": [
    {
      "agentId": "agent-1",
      "agentName": "agent-gz-01",
      "result": "completed",
      "errorMsg": "",
      "finishedAt": "2026-04-29T10:30:01+08:00",
      "finalSnapshot": { /* monitor.CollectorSnapshot 完整结构 */ }
    }
  ],
  "finalSnapshot": { /* 集群聚合的完整 CollectorSnapshot */ },
  "finalSystem":   { /* 集群聚合的 ClusterSystemSnapshot */ }
}
```

**UI 推荐**：
- 顶部 banner：name、状态、tags、starred、note（可折叠 markdown 显示）+ 编辑按钮
- 三个 Tab：
  - **总览**：finalSnapshot 表（同 dashboard 大盘的动作表，但是终态值）
  - **per-Agent 对比**：表格列出每个 agentReport.finalSnapshot 的关键指标（successRate / P99 / Apdex），可发现异常节点
  - **趋势图**：拉 `/timeseries` 接口绘制 QPS / P99 / Apdex 等时序曲线
- 操作按钮：编辑标签 / 克隆为新任务 / 删除 / 下载配置 / 加入对比

### 9.4 更新标签 / 备注 / 收藏

```
PUT /api/history/{id}
Content-Type: application/json

{
  "starred": true,
  "tags": ["benchmark", "v1.2", "before-fix-xyz"],
  "note": "## 测试结论\n- P99 从 280ms 降到 120ms"
}
```

**所有字段都是可选**（部分更新）：

- `starred`：bool，省略则不修改
- `tags`：string[]，省略则不修改；传 `[]` 表示清空所有标签
- `note`：string（最大 8KB，支持 markdown），省略不修改

**响应** `200 OK`，返回更新后的完整 `HistoryDetail`。

**校验规则**：
- 单个 tag：1~32 字符，只允许字母数字 / 中文 / `-` / `_`
- 最多 10 个 tags
- note 最大 8KB

**UI 推荐**：
- 弹窗或抽屉式编辑表单
- tags 用 chip 输入框（输入时下拉提示已有 tags）
- note 用 markdown 编辑器（可选预览模式）

### 9.5 删除历史任务

```
DELETE /api/history/{id}?force=false
```

| 参数 | 默认 | 说明 |
|---|---|---|
| `force` | `false` | starred=true 时必须传 `?force=true`，否则返回 409 |

**成功响应** `204 No Content`。

**错误**：

| 状态 | code | 说明 |
|---|---|---|
| 404 | `HISTORY_NOT_FOUND` | 任务不存在 |
| 409 | `HISTORY_STARRED` | 该任务为收藏，需 `?force=true` |

**UI 推荐**：
- 列表行的删除按钮：starred 时显示锁图标提示"取消收藏后才能删除"
- 详情页的删除按钮：弹二次确认 modal；若 starred，文字"此任务已收藏，确认强制删除？"

### 9.6 时序数据（趋势图）

```
GET /api/history/{id}/timeseries
```

**响应** `200 OK`：

```json
{
  "taskId": "task-abc",
  "stress": [
    {
      "taskId": "task-abc",
      "sampledAt": "2026-04-29T10:00:10+08:00",
      "elapsedSec": 10,
      "dataType": "stress",
      "snapshot": { /* ClusterStressSnapshot */ }
    }
  ],
  "system": [
    {
      "taskId": "task-abc",
      "sampledAt": "2026-04-29T10:00:10+08:00",
      "elapsedSec": 10,
      "dataType": "system",
      "snapshot": { /* ClusterSystemSnapshot */ }
    }
  ]
}
```

> 采样间隔默认 10s（Admin 配置），1 小时任务 ≈ 360 个点 × 2 (stress+system) = 720 个点位。

**UI 推荐**（趋势图）：

- X 轴用 `elapsedSec`（任务相对秒数）或 `sampledAt`（绝对时间）
- 推荐绘制：
  - **QPS 趋势**：每个动作一条线（取 actions[] 中前 N 个核心动作）
  - **P99 趋势**：同上
  - **成功率趋势**：单条线
  - **CPU 趋势**：每个 Agent 一条线（取 system 数据 perAgent）
  - **内存趋势**：同上
  - **网络收发**：双 Y 轴
- 用 ECharts / Recharts 等图表库

### 9.7 任务配置归档（下载 / 克隆使用）

```
GET /api/history/{id}/config
```

**响应** `200 OK`：（与创建任务的 multipart 内容对齐，但用 JSON 返回）

```json
{
  "taskId": "task-abc",
  "name": "200v200 压测 v1.2",
  "totalBots": 200,
  "robotConfig": {
    "authAddr": "http://127.0.0.1:20000",
    "concurrency": 50,
    "timeoutSec": 60,
    "accountPrefix": "bot_",
    "startNumber": 0,
    "mainService": "logic",
    "authExtra": { "version": "1.0.0", "channel": "mine", "platform": "1000" },
    "heartbeatSec": 5,
    "httpTimeoutSec": 10,
    "apdexT": 100,
    "logLevel": "info"
  },
  "flowJson":   { /* 完整 flow.json 内容 */ },
  "protoFiles": {
    "auth.proto": "<base64>",
    "battle.proto": "<base64>"
  },
  "scripts": {
    "battle.lua": "<base64>",
    "heartbeat.lua": "<base64>"
  }
}
```

**UI 推荐**：
- "下载配置" 按钮：把 flowJson 打包成 zip 提供下载（前端可本地实现，或后端额外提供 `?download=zip`）
- 不需要在浏览器里展示完整 base64，只是用作克隆的中间数据

### 9.8 克隆历史任务

```
POST /api/history/{id}/clone
Content-Type: application/json

{
  "name": "200v200 v1.2 重测"
}
```

**Body**（可选）：

- `name`：新任务名（不传则默认 `<原name> (clone)`）

**响应** `201 Created`：

```json
{ "id": "task-new-uuid" }
```

> 克隆出的是 **pending 状态**的新任务，不会立即启动。前端可跳转到任务详情页让用户确认参数后再 Start。

**UI 推荐**：
- 详情页 / 列表行右键菜单："克隆"按钮
- 克隆成功后跳转 `/tasks/{newId}`（或 toast 提示并提供"立即启动"按钮）

### 9.9 多任务对比

```
GET /api/history/compare?ids=task-a,task-b,task-c
```

**Query**：

- `ids`：逗号分隔的任务 ID，**最多 5 个**

**响应** `200 OK`：

```json
{
  "tasks": [
    { /* HistoryDetail of task-a */ },
    { /* HistoryDetail of task-b */ },
    { /* HistoryDetail of task-c */ }
  ],
  "diff": {
    "actions": {
      "CreateNormalTeam": [78.5, 65.2, 60.1],
      "SelectHero":       [35.2, 32.0, 30.5],
      "StartBattle":      [120.3, 105.0, 98.0]
    }
  }
}
```

`diff.actions` 是 **同一个动作在多个任务上的 P99 对比**（按 ids 顺序排列），用于柱状图对比。

**错误**：

| 状态 | code | 说明 |
|---|---|---|
| 400 | `BAD_REQUEST` | ids 为空、超过 5 个、或包含不存在的任务 |

**UI 推荐**：
- 多任务对比页（独立路由 `/history/compare`）
- 顶部多选任务 chip → 调用接口
- 卡片对比：每个任务的 finalSnapshot 关键指标（QPS/P99/SuccessRate/Apdex）并排展示
- 关键动作对比柱状图（基于 `diff.actions`）：X 轴动作名，每个动作三个/N 个柱子代表不同任务

---

## 10. 日志查看器 API

> 日志查看器为运维和开发人员提供实时日志流浏览、历史日志文件下载能力。
> Admin 和 Agent 各自维护一个内存环形缓冲区（RingBuffer），通过游标（seq）实现增量拉取；
> 磁盘日志文件支持列表和下载，方便下载完整日志后离线排查。

### 10.1 架构概述

```
┌──────────┐    GET /api/logs/admin          ┌──────────┐
│          │ ──────────────────────────────→  │          │
│  前端    │    GET /api/logs/admin/files     │  Admin   │
│          │ ──────────────────────────────→  │  :8080   │
│          │                                   │          │
│          │    GET /api/logs/agents/{id}      │          │  代理转发
│          │ ──────────────────────────────→  │          │ ────→ Agent /agent/v1/logs
│          │    GET /api/logs/agents/{id}/files│          │ ────→ Agent /agent/v1/logs/files
│          │ ──────────────────────────────→  │          │
└──────────┘                                   └──────────┘
```

**关键设计**：
- **前端不直连 Agent**：所有日志请求统一发到 Admin，Agent 日志由 Admin 代理转发。
- **环形缓冲区**：固定大小，先进先出（Admin 5000 条，Agent 50000 条）；通过 `afterSeq` 游标实现增量拉取，避免重复。
- **磁盘日志**：与环形缓冲区独立，用于完整日志归档；文件列表按当前日志文件名前缀过滤（含轮转文件）。

### 10.2 查询 Admin 环形缓冲区日志

```
GET /api/logs/admin?afterSeq=0&limit=200
```

**Query 参数（全部可选）**：

| 参数 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `afterSeq` | uint64 | `0` | 游标：仅返回 seq > afterSeq 的条目。首次传 `0` 表示从头拉取，后续传上次返回的 `nextSeq` |
| `limit` | int | `200` | 单次最大返回条数，范围 1~500，超出自动修正为 200 |

**响应** `200 OK`：

```typescript
type LogQueryResult = {
  entries: LogEntry[];   // 日志条目数组（可能为空数组）
  hasMore: boolean;      // 是否还有更多条目未返回（true 时应立即用 nextSeq 再次请求）
  nextSeq: number;       // 下次请求使用的游标值（最后一条的 seq；无数据时为 0）
};

type LogEntry = {
  level: string;         // 日志等级："debug" / "info" / "warn" / "error"
  time: string;          // ISO 8601 时间戳（如 "2026-05-09T10:30:00.123+08:00"）
  caller?: string;       // 调用位置（如 "network/connection.go:142"），仅在日志配置开启 caller 时存在
  message: string;       // 日志消息文本
  service?: string;      // 服务标识（如 "logic"），仅特定日志携带
  fields?: LogField[];   // 结构化字段列表（zap 字段的键值对序列化结果）
};

type LogField = {
  key: string;           // 字段名（如 "agentId"、"error"）
  value: string;         // 字段值（字符串化后的值）
};
```

**特殊响应**：
- Admin 环形缓冲区未启用时，返回 `{ entries: [], hasMore: false, nextSeq: 0 }`（仍为 200 OK）。

**增量拉取示例**：

```typescript
let nextSeq = 0;

async function pollAdminLogs() {
  const res = await fetch(`/api/logs/admin?afterSeq=${nextSeq}&limit=200`);
  const data: LogQueryResult = await res.json();
  // 处理 data.entries ...
  nextSeq = data.nextSeq;
  if (data.hasMore) {
    // 立即再次拉取，直到 hasMore=false
    return pollAdminLogs();
  }
}
```

**推荐轮询间隔**：1~3 秒。

### 10.3 查询 Agent 环形缓冲区日志（代理转发）

```
GET /api/logs/agents/{id}?afterSeq=0&limit=200
```

**路径参数**：

| 参数 | 类型 | 说明 |
|---|---|---|
| `id` | string | Agent ID |

**Query 参数**：与 §10.2 完全相同（`afterSeq`、`limit`），Admin 透传到 Agent 的 `GET /agent/v1/logs`。

**响应** `200 OK`：与 §10.2 相同的 `LogQueryResult` 结构。

**错误响应**：

| 状态 | code | 说明 |
|---|---|---|
| 404 | `AGENT_NOT_FOUND` | Agent ID 不存在 |
| 503 | `AGENT_OFFLINE` | Agent 离线或不可达（message 含具体原因） |

**说明**：Admin 将此请求代理到 `http://{agentAddress}/agent/v1/logs?{原始query}`，透传 Agent 的 JSON 响应和状态码。

### 10.4 列出 Admin 日志文件

```
GET /api/logs/admin/files
```

**响应** `200 OK`：

```typescript
type LogFileInfo = {
  name: string;     // 文件名（如 "stressbot.log"、"stressbot.log.2026-05-08"）
  size: number;     // 文件大小（字节）
  modTime: string;  // 最后修改时间（格式 "2026-05-09 10:30:00"）
};

// 响应为 LogFileInfo[]
type ListLogFilesResponse = LogFileInfo[];
```

**说明**：
- 返回与当前 Admin 日志文件同目录、同文件名前缀的所有文件（含轮转文件）。
- 无文件时返回空数组 `[]`。

### 10.5 下载 Admin 日志文件

```
GET /api/logs/admin/files/{name}
```

**路径参数**：

| 参数 | 类型 | 说明 |
|---|---|---|
| `name` | string | 文件名（从 §10.4 获取） |

**响应** `200 OK`：

```
Content-Type: text/plain; charset=utf-8
Content-Disposition: attachment; filename="<name>"
```

响应体为日志文件原始内容（支持 Range 请求和 Last-Modified 缓存）。

**错误响应**：

| 状态 | code | 说明 |
|---|---|---|
| 400 | `INVALID_ARGUMENT` | 文件名非法（包含 `/`、`\` 或等于 `..`） |
| 404 | — | 文件不存在 |

### 10.6 列出 Agent 日志文件（代理转发）

```
GET /api/logs/agents/{id}/files
```

**路径参数**：

| 参数 | 类型 | 说明 |
|---|---|---|
| `id` | string | Agent ID |

**响应** `200 OK`：与 §10.4 相同的 `LogFileInfo[]` 结构。

**错误响应**：

| 状态 | code | 说明 |
|---|---|---|
| 404 | `AGENT_NOT_FOUND` | Agent ID 不存在 |
| 503 | `AGENT_OFFLINE` | Agent 离线或不可达 |

**说明**：Admin 代理转发到 `GET http://{agentAddress}/agent/v1/logs/files`。

### 10.7 下载 Agent 日志文件（代理转发）

```
GET /api/logs/agents/{id}/files/{name}
```

**路径参数**：

| 参数 | 类型 | 说明 |
|---|---|---|
| `id` | string | Agent ID |
| `name` | string | 文件名（从 §10.6 获取） |

**响应** `200 OK`：与 §10.5 相同（`text/plain` + `Content-Disposition` 下载）。

**错误响应**：

| 状态 | code | 说明 |
|---|---|---|
| 400 | `INVALID_ARGUMENT` | 文件名非法（包含 `/`、`\` 或等于 `..`） |
| 404 | `AGENT_NOT_FOUND` | Agent ID 不存在 |
| 503 | `AGENT_OFFLINE` | Agent 离线或不可达 |

**说明**：Admin 代理转发到 `GET http://{agentAddress}/agent/v1/logs/files/{name}`，透传 Agent 的响应头和响应体。

### 10.8 接口汇总

| 接口 | 方法 | 路径 | 数据来源 | 说明 |
|---|---|---|---|---|
| Admin 日志流 | GET | `/api/logs/admin` | Admin 内存 RingBuffer | 增量游标拉取 |
| Agent 日志流 | GET | `/api/logs/agents/{id}` | Agent RingBuffer（代理） | 透传 query |
| Admin 文件列表 | GET | `/api/logs/admin/files` | Admin 磁盘 | 按前缀过滤 |
| Admin 文件下载 | GET | `/api/logs/admin/files/{name}` | Admin 磁盘 | 支持 Range |
| Agent 文件列表 | GET | `/api/logs/agents/{id}/files` | Agent 磁盘（代理） | 透传响应 |
| Agent 文件下载 | GET | `/api/logs/agents/{id}/files/{name}` | Agent 磁盘（代理） | 透传响应 |

### 10.9 UI 推荐

**日志面板（LogsTab）**：

```
┌──────────────────────────────────────────────────────────────┐
│ [Admin 日志 ▾] [Agent: agent-gz-01 ▾]  [暂停] [清屏] [下载]  │
├──────────────────────────────────────────────────────────────┤
│ 🔍 [过滤关键字] [等级: 全部 ▾]                                │
├──────────────────────────────────────────────────────────────┤
│ 10:30:00.123 INFO  [network] 连接建立 agentId=agent-1        │
│ 10:30:00.456 WARN  [engine]   超时 action=Auth dur=850ms     │
│ 10:30:01.789 ERROR [robot]    登录失败 botId=bot_42 err=...  │
│ ...                                                          │
└──────────────────────────────────────────────────────────────┘
```

**推荐实现**：
- **数据源切换**：下拉选择 Admin 或指定 Agent，切换时重置游标为 0。
- **增量拉取**：每 1~3 秒用 `nextSeq` 轮询，`hasMore=true` 时立即追加请求。
- **前端过滤**：按关键字、等级在客户端过滤（环形缓冲区不做服务端过滤）。
- **暂停/恢复**：暂停时停止轮询但保持游标；恢复后从上次游标继续拉取（可能丢失暂停期间的旧日志，因为环形缓冲区会覆盖）。
- **文件下载**：「下载」按钮先调 `/files` 获取列表，单文件直接用 `/files/{name}` 下载；多文件时弹窗选择。

---

## 12. 完整 TypeScript 类型定义

> 复制以下代码到前端项目（`web/src/types/api.ts`），前端开发完整覆盖。
> 本文件结构与文档保持完全一致，新增字段两边同步。

```typescript
// === 基础枚举 ===
export type TaskState = 'pending' | 'starting' | 'running' | 'stopping' | 'stopped' | 'failed';
export type AgentStatus = 'idle' | 'busy' | 'unhealthy' | 'offline';
export type TaskResult = 'completed' | 'stopped' | 'failed';
// OS / Arch 用于 StaticInfo（Agent 自报），保留仅用于展示
export type OS = 'windows' | 'linux' | 'darwin';
export type Arch = 'amd64' | 'arm64';

// === 通用错误 ===
export type ApiError = {
  code: string;
  message: string;
  details?: Record<string, unknown>;
};

// === Task ===
export type TaskBrief = {
  id: string;
  name: string;
  state: TaskState;
  totalBots: number;
  agentCount: number;
  createdAt: string;
  startedAt?: string;
  stoppedAt?: string;
};

export type TaskDetail = TaskBrief & {
  config: TaskConfig;
  assignments: Assignment[];
  errorMsg?: string;
  reports?: Record<string, TaskCompletionReport>;
};

export type TaskConfig = {
  robotConfig: RobotConfig;
  deadline?: string;
  flowFiles: string[];
};

export type LogLevel = 'debug' | 'info' | 'warn' | 'error';

export type RobotConfig = {
  // ── 必填 ──
  authAddr: string;
  concurrency: number;
  timeoutSec: number;
  // ── 业务可变 ──
  accountPrefix?: string;                // 默认 "bot_"
  startNumber?: number;                  // 账号编号起点，默认 0
  mainService?: string;                  // 默认 "logic"
  authExtra?: Record<string, string>;    // 前端默认空，由用户手动添加 version/channel/platform 等
  // ── 性能/超时 ──
  heartbeatSec?: number;                 // 默认 5
  httpTimeoutSec?: number;               // 默认 10
  apdexT?: number;                       // 默认 100
  // ── 日志 ──
  logLevel?: LogLevel;
};

export type Assignment = {
  taskId: string;
  agentId: string;
  agentName: string;
  startNumber: number;
  totalBots: number;
};

export type TaskCompletionReport = {
  agentId: string;
  taskId: string;
  result: TaskResult;
  errorMsg?: string;
  finishedAt: string;
};

export type TasksListResponse = { total: number; items: TaskBrief[] };

// === Agent ===
export type StaticInfo = {
  hostname: string;
  os: OS;
  arch: Arch;
  numCpu: number;
  memTotalMB: number;
  goVersion: string;
  kernelVer: string;
  startedAt: string;
};

export type AgentBrief = {
  agentId: string;
  name: string;
  address: string;
  appVersion: string;
  maxBots: number;
  status: AgentStatus;
  currentTaskId?: string;
  currentBots: number;
  staticInfo: StaticInfo;
  lastHeartbeatAt: string;
  stressUpdatedAt?: string;
  systemUpdatedAt?: string;
  cpuPercent?: number;
  memPercent?: number;
  numGoroutine?: number;
};

export type AgentDetail = AgentBrief & {
  latestSystem?: SystemSnapshot;
};

export type AgentsListResponse = { items: AgentBrief[] };

// === Stress 指标 ===
export type StressSnapshot = {
  timestamp: string;
  uptimeSeconds: number;
  totalActions: number;
  apdexT: number;
  robots: RobotsView;
  connections: ConnectionsView;
  bandwidth: BandwidthView;
  actions: ActionMetric[];
  clusterInfo?: ClusterInfo;
};

export type RobotsView = {
  started: number;
  running: number;
  stopped: number;
  errored: number;
};

export type ConnectionsView = {
  established: number;
  failed: number;
  dropped: number;
};

export type BandwidthView = {
  totalSendBytes: number;
  totalRecvBytes: number;
  sendMBps: number;
  recvMBps: number;
};

export type ActionMetric = {
  name: string;
  sampleCount: number;
  successCount: number;
  failureCount: number;
  timeoutCount: number;
  skippedCount: number;
  executing: number;
  successRate: number;
  apdex: number;
  avgQps: number;
  avgSendBytes: number;
  avgRecvBytes: number;
  timeoutAvgMs: number;
  latency: HistogramView;
  errors?: ErrorBucket[];
};

export type HistogramView = {
  count: number;
  minMs: number;
  maxMs: number;
  avgMs: number;
  p50Ms: number;
  p90Ms: number;
  p95Ms: number;
  p99Ms: number;
};

export type ErrorBucket = {
  msg: string;
  count: number;
};

export type ClusterInfo = {
  agentCount: number;
  agentIds: string[];
  staleAgentIds: string[];
};

export type PerAgentMetrics = {
  items: Array<{
    agentId: string;
    agentName: string;
    snapshot: StressSnapshot;
    updatedAt: string;
  }>;
};

// === System 指标 ===
export type SystemSnapshot = {
  timestamp: string;
  cpuPercent: number;
  cpuPerCore: number[];
  loadAvg1: number;
  loadAvg5: number;
  loadAvg15: number;
  memTotalMB: number;
  memUsedMB: number;
  memPercent: number;
  swapUsedMB: number;
  processRssMB: number;
  processHeapMB: number;
  processSysMB: number;
  numGoroutine: number;
  numThread: number;
  numFd: number;
  netSendKBps: number;
  netRecvKBps: number;
  gcCount: number;
  gcPauseAvgMs: number;
};

export type ClusterSystemSnapshot = {
  timestamp: string;
  agentCount: number;
  onlineCount: number;
  offlineCount: number;
  totalMemMB: number;
  usedMemMB: number;
  avgCpuPercent: number;
  maxCpuPercent: number;
  totalNetSendKBps: number;
  totalNetRecvKBps: number;
  totalGoroutines: number;
  totalThreads: number;
  totalFds: number;
  hotAgentId?: string;
  hotAgentName?: string;
};

export type PerAgentSystem = {
  items: Array<{
    agentId: string;
    agentName: string;
    status: AgentStatus;
    snapshot: SystemSnapshot;
    updatedAt: string;
    isStale: boolean;
  }>;
};

// === 任务单例冲突错误 details ===
export type TaskConflictDetails = {
  activeTaskId: string;
  activeName: string;
  activeState: TaskState;     // 实际只可能是 starting/running/stopping
  startedAt: string;
};

// === History（历史压测）===
export type ConfigSummary = {
  authAddr: string;
  concurrency: number;
  timeoutSec: number;
  flowSizeKB: number;
  protoCount: number;
  scriptCount: number;
};

export type HistoryRecord = {
  id: string;
  name: string;
  state: 'stopped' | 'failed';   // 历史只记录终态
  totalBots: number;
  agentCount: number;
  createdAt: string;
  startedAt?: string;
  stoppedAt?: string;
  durationSec: number;
  errorMsg?: string;

  starred: boolean;
  tags: string[];
  note?: string;

  configSummary: ConfigSummary;
};

export type HistoryListResponse = {
  total: number;
  items: HistoryRecord[];
};

export type HistoryAgentReport = {
  agentId: string;
  agentName: string;
  result: TaskResult;
  errorMsg?: string;
  finishedAt: string;
  finalSnapshot: StressSnapshot;
};

export type HistoryDetail = HistoryRecord & {
  assignments: Array<{
    taskId: string;
    agentId: string;
    startNumber: number;
    totalBots: number;
  }>;
  agentReports: HistoryAgentReport[];
  finalSnapshot: StressSnapshot;
  finalSystem:   ClusterSystemSnapshot;
};

export type HistoryFilter = {
  state?: 'stopped' | 'failed';
  startedAfter?: string;
  startedBefore?: string;
  tags?: string[];        // 任意一个匹配
  tagsAll?: string[];     // 必须全部匹配
  starred?: boolean;
  search?: string;
  orderBy?: string;
  limit?: number;
  offset?: number;
};

export type UpdateHistoryRequest = {
  starred?: boolean;
  tags?: string[];
  note?: string;
};

export type HistoryTagsResponse = { tags: string[] };

export type TimeseriesPoint = {
  taskId: string;
  sampledAt: string;
  elapsedSec: number;
  dataType: 'stress' | 'system';
  snapshot: StressSnapshot | ClusterSystemSnapshot;
};

export type TimeseriesResponse = {
  taskId: string;
  stress: TimeseriesPoint[];
  system: TimeseriesPoint[];
};

export type HistoryConfigArchive = {
  taskId: string;
  name: string;
  totalBots: number;
  robotConfig: RobotConfig;
  flowJson: unknown;
  protoFiles: Record<string, string>;  // base64
  scripts:    Record<string, string>;  // base64
};

export type HistoryCloneRequest = {
  name?: string;
};

export type HistoryCompareResponse = {
  tasks: HistoryDetail[];
  diff: {
    actions: Record<string, number[]>;  // actionName -> [taskA_p99, taskB_p99, ...]
  };
};

// === Logs（日志查看器）===

export type LogField = {
  key: string;
  value: string;
};

export type LogEntry = {
  level: string;         // "debug" | "info" | "warn" | "error"
  time: string;          // ISO 8601
  caller?: string;
  message: string;
  service?: string;
  fields?: LogField[];
};

export type LogQueryResult = {
  entries: LogEntry[];
  hasMore: boolean;
  nextSeq: number;
};

export type LogFileInfo = {
  name: string;
  size: number;          // bytes
  modTime: string;       // "2026-05-09 10:30:00"
};
```

---

## 13. 历史数据与轮询策略

### 13.1 历史快照由前端管理

> Admin **不存储**历史时序数据。每次轮询拿到的都是"当前快照"。前端必须自行维护历史数组用于折线图。

推荐实现（React Hooks 示例）：

```typescript
import { useEffect, useState, useRef } from 'react';

export function usePolling<T>(
  fetcher: () => Promise<T>,
  intervalMs: number,
  maxHistory = 60
) {
  const [latest, setLatest] = useState<T | null>(null);
  const history = useRef<T[]>([]);

  useEffect(() => {
    let cancelled = false;
    const tick = async () => {
      try {
        const data = await fetcher();
        if (cancelled) return;
        history.current.push(data);
        if (history.current.length > maxHistory) {
          history.current.shift();
        }
        setLatest(data);
      } catch (err) {
        // 失败不入历史，只记录
        console.warn('poll failed', err);
      }
    };
    tick();                              // 立即一次
    const id = setInterval(tick, intervalMs);
    return () => { cancelled = true; clearInterval(id); };
  }, [fetcher, intervalMs, maxHistory]);

  return { latest, history: history.current };
}
```

### 13.2 折线图数据准备

5s 间隔保留 60 个样本 = 5 分钟历史，足够大盘观察。需要更长时间用户可设置 1min 间隔保留 60 个 = 1 小时。

```typescript
const { latest, history } = usePolling(
  () => fetch('/api/system').then(r => r.json()),
  5000,
  60
);

const cpuChartData = history.map(s => ({
  time: s.timestamp,
  cpu:  s.avgCpuPercent,
}));
```

### 13.3 计算瞬时 QPS

后端不提供瞬时 QPS，前端用相邻两次快照差分：

```typescript
function computePeriodQps(curr: StressSnapshot, prev: StressSnapshot | null) {
  if (!prev) return new Map<string, number>();
  const dt = (Date.parse(curr.timestamp) - Date.parse(prev.timestamp)) / 1000;
  const result = new Map<string, number>();

  const prevByName = new Map(prev.actions.map(a => [a.name, a.sampleCount]));
  for (const a of curr.actions) {
    const prevCount = prevByName.get(a.name) ?? 0;
    const diff = a.sampleCount - prevCount;
    result.set(a.name, dt > 0 && diff > 0 ? diff / dt : 0);
  }
  return result;
}
```

### 13.4 任务结束后的数据处理

任务结束（state=stopped/failed）后，`/api/metrics` 仍然返回该任务的最终快照（直到下一个任务启动）。前端可以据此显示"上次任务报告"：

- 检查 `task.state === 'stopped'` 后停止轮询
- 保存最后一份 `StressSnapshot` 到 localStorage 或后端（如果有归档接口）

---

## 14. 推荐页面布局

### 14.1 总览首页

```
┌────────────────────────────────────────────────────────────┐
│ 集群总览                                                    │
│ Agent: 3/3 在线   CPU 平均 45%    内存 12.3/64 GB           │
│ 当前任务: task-01 "200v200 压测"  机器人 298/300            │
│ 累计动作 52,843   全局 QPS  120/s                            │
└────────────────────────────────────────────────────────────┘
┌─────────────┬─────────────┬─────────────┐
│ CPU 折线    │ 网络折线    │ QPS 折线    │
└─────────────┴─────────────┴─────────────┘
┌────────────────────────────────────────────────────────────┐
│ 动作 Top 5（按 QPS / 失败率）                               │
└────────────────────────────────────────────────────────────┘
```

### 14.2 任务详情页

```
[任务名]  [状态 chip]  [启动/停止按钮]                       
─────────────────────────────────────────────
配置概览 | 分配方案表格 | 完成报告（终态显示）
─────────────────────────────────────────────
压测大盘:
  - 全局指标卡片（机器人数、连接、带宽）
  - 动作明细表（按 §7.1 actions[] 渲染）
    - 每行可展开看 latency 直方图、errors 列表
  - 推送回调单独分组
─────────────────────────────────────────────
[切换 per-agent 视图] → 用 §7.2 数据
```

### 14.3 Agent 详情页

```
[Agent 名]  [状态指示灯]  [当前任务链接]                    
─────────────────────────────────────────────
StaticInfo: hostname / OS / CPU / Mem / kernel / goVersion
─────────────────────────────────────────────
系统资源（实时）：
  - 卡片：CPU%、Mem%、Goroutine、Thread、FD、Network
  - 折线：CPU、Mem、Net Send/Recv（5min 历史）
─────────────────────────────────────────────
压测视角（仅 busy 状态）：
  - 该 Agent 的动作明细（§7.3）
─────────────────────────────────────────────
日志 / 事件（如有）
```

### 14.4 历史压测列表页 `/history`

```
┌──────────────────────────────────────────────────────────────┐
│ 筛选区                                                        │
│ [日期范围] [状态▾] [标签 multiselect▾] [搜索框] [仅看收藏 ☆] │
└──────────────────────────────────────────────────────────────┘
┌──────────────────────────────────────────────────────────────┐
│ ☆  [name]                  [tags chips]                       │
│    state · 时长 · 起止时间                                     │
│    机器人 200 · Agent 2 · 平均 P99 120ms · Apdex 0.95         │
│    [查看] [克隆] [编辑标签] [删除]                            │
└──────────────────────────────────────────────────────────────┘
分页栏 · 总数 N
```

> 推荐：starred 任务用金色边框置顶，普通任务按 startedAt desc 排列。

### 14.5 历史压测详情页 `/history/:id`

```
[name]  [tags chips]  [☆收藏] [编辑] [克隆] [加入对比] [删除]
─────────────────────────────────────────────────────
基本信息  起止时间  时长  机器人数  agent 数  errorMsg（若有）
note（markdown 渲染）
─────────────────────────────────────────────────────
[Tab 总览]  [Tab per-Agent]  [Tab 趋势图]
─────────────────────────────────────────────────────
总览：finalSnapshot 表（与运行期 dashboard 同样布局）
per-Agent：表格列出每个 agent 的 successRate / P99 / Apdex（异常节点高亮）
趋势图：从 /timeseries 拿到的时序点，绘制 QPS / P99 / SuccessRate / CPU / Mem 曲线
─────────────────────────────────────────────────────
[配置归档] 折叠面板：展示 ConfigSummary 主要参数 + [下载完整配置] 按钮
```

### 14.6 历史对比页 `/history/compare`

```
顶部：选择 2~5 个任务（chip 显示已选）
─────────────────────────────────────────────
卡片网格：每个任务一张卡片，并排展示
  [name + tags] [P99] [Apdex] [QPS] [SuccessRate]
─────────────────────────────────────────────
柱状图：动作 P99 横向对比
  X 轴：动作名（来自 diff.actions key）
  Y 轴：每个动作 N 个柱子（每任务一柱）
─────────────────────────────────────────────
[导出对比报告 PNG / CSV]
```

---

## 15. 错误处理策略

### 15.1 通用错误拦截

```typescript
async function api<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init);
  if (!res.ok) {
    const err: ApiError = await res.json().catch(() => ({
      code: 'NETWORK_ERROR',
      message: res.statusText,
    }));
    throw err;
  }
  if (res.status === 204) return undefined as T;
  return res.json();
}
```

### 15.2 全局 Toast 映射

```typescript
function showApiError(err: ApiError) {
  const messages: Record<string, string> = {
    TASK_NOT_FOUND:        '任务不存在或已被删除',
    TASK_INVALID_STATE:    `任务当前状态不允许此操作（${err.message}）`,
    TASK_CONFLICT:         (() => {
      const d = err.details as TaskConflictDetails | undefined;
      return d
        ? `已有任务"${d.activeName}"正在 ${d.activeState}，请先停止`
        : '已有任务在执行';
    })(),
    AGENT_BUSY:            'Agent 正忙，请选择其他节点',
    AGENT_OFFLINE:         'Agent 已离线',
    CAPACITY_EXCEEDED:     `集群容量不足，最多支持 ${err.details?.maxBots ?? '?'} 个机器人`,
    HISTORY_NOT_FOUND:     '历史记录不存在或已被删除',
    HISTORY_STARRED:       '已收藏的记录不能删除（请先取消收藏，或使用强制删除）',
  };
  message.error(messages[err.code] ?? err.message);
}
```

**TASK_CONFLICT 特殊处理**（推荐用 modal 而非 toast）：

```typescript
import { Modal } from 'antd';
import { useNavigate } from 'react-router-dom';

function handleTaskConflict(err: ApiError, navigate: ReturnType<typeof useNavigate>) {
  if (err.code !== 'TASK_CONFLICT') return false;
  const d = err.details as TaskConflictDetails;
  Modal.confirm({
    title: '已有任务在执行',
    content: `任务"${d.activeName}"当前处于 ${d.activeState} 状态，需先停止才能启动新任务。`,
    okText: '查看该任务',
    cancelText: '取消',
    onOk: () => navigate(`/tasks/${d.activeTaskId}`),
  });
  return true;
}
```

### 15.3 轮询失败处理

| 场景 | 策略 |
|---|---|
| 单次失败 | 静默吞掉（可能是临时网络抖动），下次仍尝试 |
| 连续失败 ≥ 3 次 | UI 顶部显示"连接 Admin 失败"红色横条 |
| 恢复成功 | 红条消失，可显示"已恢复" toast 一次 |

```typescript
let consecutiveFails = 0;
async function tick() {
  try {
    await fetcher();
    if (consecutiveFails >= 3) toast('已恢复连接');
    consecutiveFails = 0;
    setBannerVisible(false);
  } catch {
    consecutiveFails++;
    if (consecutiveFails >= 3) setBannerVisible(true);
  }
}
```

### 15.4 长时间无数据状态

| 场景 | UI |
|---|---|
| 任务 running 但 `actions.length === 0` 超过 10s | "正在等待 Agent 上报数据..." |
| Agent 全部 offline | 大盘空状态 + 引导 "请检查 Agent 服务状态" |
| 历史折线无数据 | "暂无数据" |

---

## 16. 完整响应示例

### 16.1 GET /api/metrics（集群聚合压测）

```json
{
  "timestamp": "2026-04-29T10:30:00.123+08:00",
  "uptimeSeconds": 150.5,
  "totalActions": 52843,
  "apdexT": 100,
  "robots": {
    "started": 300,
    "running": 298,
    "stopped": 0,
    "errored": 2
  },
  "connections": {
    "established": 596,
    "failed": 4,
    "dropped": 2
  },
  "bandwidth": {
    "totalSendBytes": 15728640,
    "totalRecvBytes": 31457280,
    "sendMBps": 1.59,
    "recvMBps": 3.21
  },
  "actions": [
    {
      "name": "Auth",
      "sampleCount": 300,
      "successCount": 296,
      "failureCount": 4,
      "timeoutCount": 0,
      "skippedCount": 0,
      "executing": 0,
      "successRate": 0.9867,
      "apdex": 0.99,
      "avgQps": 1.99,
      "avgSendBytes": 256,
      "avgRecvBytes": 512,
      "timeoutAvgMs": 0,
      "latency": {
        "count": 296,
        "minMs": 5.2,
        "maxMs": 89.3,
        "avgMs": 15.7,
        "p50Ms": 12.0,
        "p90Ms": 32.5,
        "p95Ms": 55.1,
        "p99Ms": 89.3
      },
      "errors": [
        { "msg": "auth server returned 403", "count": 4 }
      ]
    },
    {
      "name": "CreateNormalTeam",
      "sampleCount": 850,
      "successCount": 842,
      "failureCount": 8,
      "timeoutCount": 0,
      "skippedCount": 0,
      "executing": 9,
      "successRate": 0.9906,
      "apdex": 0.95,
      "avgQps": 5.65,
      "avgSendBytes": 45.2,
      "avgRecvBytes": 1230.5,
      "timeoutAvgMs": 0,
      "latency": {
        "count": 842,
        "minMs": 12.0,
        "maxMs": 450.6,
        "avgMs": 45.7,
        "p50Ms": 38.2,
        "p90Ms": 78.5,
        "p95Ms": 120.3,
        "p99Ms": 450.6
      }
    },
    {
      "name": "callback:OnMatchSucceed",
      "sampleCount": 780,
      "successCount": 780,
      "failureCount": 0,
      "timeoutCount": 0,
      "skippedCount": 0,
      "executing": 0,
      "successRate": 1.0,
      "apdex": 1.0,
      "avgQps": 5.18,
      "avgSendBytes": 0,
      "avgRecvBytes": 0,
      "timeoutAvgMs": 0,
      "latency": {
        "count": 0,
        "minMs": 0, "maxMs": 0, "avgMs": 0,
        "p50Ms": 0, "p90Ms": 0, "p95Ms": 0, "p99Ms": 0
      }
    }
  ],
  "clusterInfo": {
    "agentCount": 3,
    "agentIds": ["agent-1", "agent-2", "agent-3"],
    "staleAgentIds": []
  }
}
```

### 16.2 GET /api/system（集群系统聚合）

```json
{
  "timestamp": "2026-04-29T10:30:00+08:00",
  "agentCount": 3,
  "onlineCount": 3,
  "offlineCount": 0,
  "totalMemMB": 98304,
  "usedMemMB": 12345,
  "avgCpuPercent": 45.3,
  "maxCpuPercent": 78.2,
  "totalNetSendKBps": 2456.3,
  "totalNetRecvKBps": 4912.7,
  "totalGoroutines": 1284,
  "totalThreads": 96,
  "totalFds": 384,
  "hotAgentId": "agent-1",
  "hotAgentName": "agent-gz-01"
}
```

### 16.3 GET /api/agents

```json
{
  "items": [
    {
      "agentId": "agent-1",
      "name": "agent-gz-01",
      "address": "http://10.0.0.1:7070",
      "appVersion": "v1.2.0",
      "maxBots": 5000,
      "status": "busy",
      "currentTaskId": "task-01",
      "currentBots": 100,
      "staticInfo": {
        "hostname": "gz-stress-01",
        "os": "linux",
        "arch": "amd64",
        "numCpu": 16,
        "memTotalMB": 32768,
        "goVersion": "go1.23.4",
        "kernelVer": "5.15.0",
        "startedAt": "2026-04-29T09:00:00+08:00"
      },
      "lastHeartbeatAt": "2026-04-29T10:30:00+08:00",
      "stressUpdatedAt": "2026-04-29T10:29:55+08:00",
      "systemUpdatedAt": "2026-04-29T10:29:58+08:00",
      "cpuPercent": 78.2,
      "memPercent": 65.1,
      "numGoroutine": 512
    }
  ]
}
```

### 16.4 GET /api/tasks/{id}

```json
{
  "id": "task-01",
  "name": "200v200 压测",
  "state": "running",
  "totalBots": 300,
  "agentCount": 3,
  "createdAt": "2026-04-29T09:00:00+08:00",
  "startedAt": "2026-04-29T09:00:05+08:00",
  "config": {
    "robotConfig": {
      "authAddr": "http://auth.example.com:20000",
      "concurrency": 50,
      "timeoutSec": 60,
      "accountPrefix": "bot_",
      "startNumber": 0,
      "mainService": "logic",
      "authExtra": { "version": "1.0.0", "channel": "mine", "platform": "1000" },
      "heartbeatSec": 5,
      "httpTimeoutSec": 10,
      "apdexT": 100,
      "logLevel": "info"
    },
    "deadline": "2026-04-29T11:00:00+08:00",
    "flowFiles": ["flow.json", "proto/c2s.proto", "proto/s2c.proto", "scripts/battle.lua"]
  },
  "assignments": [
    { "taskId": "task-01", "agentId": "agent-1", "agentName": "agent-gz-01", "startNumber": 10000, "totalBots": 100 },
    { "taskId": "task-01", "agentId": "agent-2", "agentName": "agent-gz-02", "startNumber": 10100, "totalBots": 100 },
    { "taskId": "task-01", "agentId": "agent-3", "agentName": "agent-gz-03", "startNumber": 10200, "totalBots": 100 }
  ]
}
```

---

## 17. 开发交接清单

完成前端开发后请逐项验证：

- [ ] 任务列表 + 创建（multipart 上传）+ 启动 + 停止 + 删除
- [ ] 任务详情显示 assignments 和 reports
- [ ] Agent 列表（含状态指示灯）+ 详情页
- [ ] 系统资源大盘（集群总览 + per-agent 折线）
- [ ] 压测大盘（动作明细表 + 折线图 + 错误展示）
- [ ] AgentsDrawer：列表 + 删除离线 + 全部停止（等价于停止当前 active 任务）
- [ ] 全局错误处理（toast + 顶部红条）
- [ ] 轮询自动启停（页面切换时）
- [ ] 折线历史 5 分钟保留 + 自动滑动
- [ ] 任务结束后停止轮询，显示最终报告
- [ ] 响应式（桌面 + 平板）
- [ ] 中文 / 英文双语切换（如需）

---

## 18. 联系点

| 问题类型 | 联系对象 |
|---|---|
| 接口字段不清晰 / 缺失 | Admin 后端开发同事 |
| 数据看起来不对（如 P99 异常） | Admin 后端 + 查 `docs/admin-implementation.md` §4.5 聚合规则 |
| Agent 上报频率 | 见 `docs/agent-implementation.md` §9 配置 |
| 版本部署 / Agent 重启 | 运维手动操作（无自动升级） |
