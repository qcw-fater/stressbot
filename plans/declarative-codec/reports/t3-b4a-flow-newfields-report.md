# T3 Batch-4 任务 A — FlowEditor 同步 T2 新字段/动作（§3.6）报告

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）
> 任务：把前端 FlowEditor 追平 T2 后端新增的声明式心跳动作（tcpHeartbeat/udpHeartbeat）、
> `ListenRef.queueSize`、禁用 `ListenDef.script`。

## 1. 实现要点

### 1.1 类型追加（严格镜像 Go）

| 文件 | 追加 |
|---|---|
| `types/flow.ts` | `ListenRef.queueSize?: number`（镜像 Go `engine/flow.go:80` `QueueSize *int`，缺省 1） |
| `types/action.ts` | `ActionPattern` += `'tcpHeartbeat' \| 'udpHeartbeat'`；`ALL_ACTION_PATTERNS` 同步 |
| `types/action.ts` | `ActionDef` += `intervalMs?: number` / `heartbeatFields?: HeartbeatField[]` / `skipWhenMissing?: boolean`（镜像 `flow.go:183-185`） |
| `types/action.ts` | 新 `HeartbeatField` 接口 + `HeartbeatFieldType` / `HeartbeatFieldSource` 联合类型 + `ALL_HEARTBEAT_FIELD_TYPES` / `ALL_HEARTBEAT_FIELD_SOURCES` 常量 |
| `types/listen.ts` | 保留 `script?`（旧 flow.json round-trip 不丢），文档注释标注「已下线 / 校验报错」 |

`HeartbeatField` 逐字镜像 `engine/heartbeat.go:32-42`：

```ts
interface HeartbeatField {
  type: HeartbeatFieldType;     // u8/i8/u16/i16/u32/i32/u64/i64
  source: HeartbeatFieldSource; // fixed/state/stateCounter/counter/timestamp/randomInt
  value?: number;   // source=fixed
  key?: string;     // source=state|stateCounter
  min?: number; max?: number;       // source=randomInt
  start?: number; step?: number;    // source=counter
  unit?: 'ms' | 's';                // source=timestamp
}
```

### 1.2 心跳表单（tcpHeartbeat/udpHeartbeat）

- `PatternSelector.tsx`：新增「心跳」分组，含 `tcpHeartbeat`/`udpHeartbeat` + 中文 tooltip。
- `actionPrune.ts`：`PATTERN_FIELDS` 加心跳条目，切换 pattern 时正确保留/裁剪心跳字段。
- `DeclarativeForm.tsx`：`patternHas` 心跳声明 `service`/`route`（复用通用输入）；
  心跳的 `c2sProto`/`bindings`/`heartbeatFields`/`intervalMs`/`skipWhenMissing` 由专属 `HeartbeatForm` 渲染。
- 新组件 `HeartbeatFields.tsx`：心跳二进制布局表格（type 下拉 / source 下拉 / 按 source 动态展示 value|key|min,max|start,step|unit），增删/排序。
- `HeartbeatForm`（内联于 `DeclarativeForm.tsx`）：
  - `intervalMs`（InputNumber，min=1）
  - **body 三模式互斥**（Radio）：空 body / proto（c2sProto+bindings）/ raw-binary（heartbeatFields）。
    切到 proto 清 `heartbeatFields`+`skipWhenMissing`；切到 raw 清 `c2sProto`+`bindings`；切 empty 全清。
  - proto 模式：复用既有 `BindingsTable`（label 标注「与 tcpRequest 同语义」）。
  - raw 模式：`HeartbeatFields` 表格 + `skipWhenMissing` 开关（仅 raw 有意义）。

#### 心跳 proto bindings 允许的 type 子集（以 Go 为准的结论）

**结论：Go 对心跳 proto 模式的 bindings 不做任何子集限制，全部 17 种 BindingType 均允许**。

依据：`engine/heartbeat.go:58` 注释「proto 模式：字段绑定（复用 tcpSend bindFields 解析）」，
`engine/action.go:265-302` `BuildProtoBody` 通过 `ae.bindFields(msg, bindings, actionName)` 完整复用
`tcpSend`/`tcpRequest` 的 binding 解析路径（含 condition/optional/required/map 全套语义）。
`execHeartbeat`（`action.go:738-763`）只把 `def.Bindings` 透传给 `HeartbeatActionConfig.Bindings`，
无心跳专用过滤。心跳 proto 模式与 tcpRequest 在 binding 能力上完全等价。

因此 brief §3.6 计划写的「只开放 fixed/state/counter/timestamp」与 Go 实际不符（且 counter/timestamp
根本不是 BindingType，而是 HeartbeatFieldSource）——**本次实现以 Go 为准：proto 模式 bindings 复用既有
`BindingsTable` 全量 17 种 type，不做 UI 子集限制**。报告中作为 concerns 明示。

### 1.3 监听编辑器：queueSize + 禁用 script

- `ListenRefsTable.tsx`：新增「队列容量」列（InputNumber，min=1，placeholder「缺省 1」），写入 `ListenRef.queueSize`。
- `ListenEditor.tsx`：
  - **移除 `lua` Tabs**（「执行脚本」形态入口不可达，符合「classifyListen 的 'lua' 分支在 UI 不再可达」）。
  - 保留 `script?` 类型（round-trip 不丢字段）；若旧 flow.json 读入了 `script` 字段，
    顶部显式红色 Alert「监听脚本回调已下线」+「移除 script」按钮（不静默兜底）。
  - `activeKey` 把 classifyListen 的 'lua' 映射到 'silent'，避免无对应 tab。

### 1.4 flow 校验（refsCheck.ts）

新增校验（中文信息，无兜底）：

| Code | 规则 | 镜像 Go |
|---|---|---|
| `HEARTBEAT_NO_INTERVAL` | tcpHeartbeat/udpHeartbeat `intervalMs<=0` 或缺失 | `action.go:739` |
| `HEARTBEAT_DUAL_MODE` | `c2sProto` 与 `heartbeatFields` 同时配置（互斥） | `action.go:743` |
| `HEARTBEAT_FIELD_UNKNOWN_TYPE` | heartbeatFields.type 不在合法集合 | `heartbeat.go:69` |
| `HEARTBEAT_FIELD_UNKNOWN_SOURCE` | heartbeatFields.source 不在合法集合 | `heartbeat.go:194` |
| `HEARTBEAT_FIELD_NO_KEY` | source=state/stateCounter 缺 key | `heartbeat.go:143,158` |
| `HEARTBEAT_FIELD_FIXED_NO_VALUE` | source=fixed 缺 value | `heartbeat.go:137` |
| `HEARTBEAT_FIELD_RANDOM_NO_RANGE` | source=randomInt 缺 min/max 或 min>max | `heartbeat.go:183,188` |
| `LISTEN_QUEUE_INVALID` | `ListenRef.queueSize<=0`（未写合法，缺省 1） | `flow.go:78` fail-loud |
| `LISTEN_SCRIPT_DISABLED` | `ListenDef.script` 字段存在（任意值）即报错 | T2 后端 fail-loud |

- `PATTERNS_REQUIRE_SERVICE` / `PATTERNS_REQUIRE_ROUTE` 同步纳入 `tcpHeartbeat`/`udpHeartbeat`
  （缺 service/route 复用既有 `ACTION_NO_SERVICE`/`ACTION_NO_ROUTE`，镜像 `action.go:748-751` route 校验）。
- 删除原 `LISTEN_LUA_NO_SCRIPT`（语义已被 `LISTEN_SCRIPT_DISABLED` 取代：script 字段存在即报错，不论空非空）。
- proto 模式 bindings 校验复用既有 `checkBindings`（全量 type，与 tcpRequest 等价）。

## 2. 与 Go 对齐说明

- 所有新类型字段名 / json tag / 语义逐字镜像 `engine/flow.go` / `engine/heartbeat.go` / `engine/action.go`。
- 校验规则一一对应 `execHeartbeat` + `resolveHeartbeatField` 的 error 分支，中文信息风格一致。
- 禁止兼容兜底：`queueSize<=0` 报错、双模式冲突报错、script 存在报错 —— 无 `??` 兜底、无静默 clamp。
- proto bindings 不做心跳专用子集（Go 实际允许全集，见 §1.2 结论）。

## 3. 测试（TDD：先红后绿）

`refsCheck.test.ts` 新增 17 个用例（心跳 13 + queueSize 2 + script-disabled 1 + 真实样例 1），
覆盖：
- 空 body / proto / raw-binary / udp 四种合法形态
- `HEARTBEAT_NO_INTERVAL`（<=0 / 缺失）
- `HEARTBEAT_DUAL_MODE`（c2sProto + heartbeatFields 同配）
- 缺 service / 缺 route
- 4 种字段级错误（unknown type/source、no key、fixed no value、random no range）
- `LISTEN_QUEUE_INVALID`（queueSize=0）/ queueSize=2 合法
- `LISTEN_SCRIPT_DISABLED`（含空 script）
- 真实 flow.json 心跳样例（RegisterLogicHeartbeat 空 + RegisterBattleTCPHeartbeat raw）无 HEARTBEAT 错误

TDD 流程：先写全部测试 → `npx vitest run` 12 red → 实现校验 → 全绿。

## 4. 验证结果

```
cd cmd/web && npx tsc -b   →  exit 0（clean）
cd cmd/web && npm run test →  Test Files 20 passed | Tests 266 passed（baseline 248 + 新增 18）
```

新增测试数：refsCheck 249→266（+17 净新增，含替换 1 个旧 LISTEN_LUA_NO_SCRIPT 用例）。

## 5. 自审

- [x] 改动限于 `types/{flow,action,listen}.ts` + ActionEditor（含新 HeartbeatFields）+ 监听编辑器 + refsCheck。
- [x] 未动 services/、codecEditor/、后端。
- [x] 严格镜像 Go（先读 flow.go/heartbeat.go/action.go 确认字段与校验）。
- [x] 无兼容兜底（三处 fail-loud 均报错）。
- [x] `ALL_ACTION_PATTERNS` / `PATTERNS_REQUIRE_*` / `ALL_HEARTBEAT_*` 常量同步。
- [x] UI 文案中文，「心跳/队列容量/监听」无技术术语泄漏（type/source 是配置术语，允许）。
- [x] 类型安全（HeartbeatField 用联合类型 + 常量数组，校验用 Set 查表）。
- [x] 未 git commit。
- [x] tsc exit 0 + 全测试绿。

## 6. git diff --stat（限定范围）

```
 .../editors/ActionEditor/DeclarativeForm.tsx       | 127 ++++++++++-
 .../editors/ActionEditor/PatternSelector.tsx       |   3 +
 .../FlowEditor/editors/ActionEditor/actionPrune.ts |   4 +
 .../components/FlowEditor/listens/ListenEditor.tsx |  36 ++--
 .../FlowEditor/listens/ListenRefsTable.tsx         |  23 +-
 .../FlowEditor/validation/refsCheck.test.ts        | 236 ++++++++++++++++++++-
 .../components/FlowEditor/validation/refsCheck.ts  | 115 +++++++++-
 cmd/web/src/types/action.ts                        |  59 ++++++
 cmd/web/src/types/flow.ts                          |   3 +
 cmd/web/src/types/listen.ts                        |   7 +-
 10 files changed, 588 insertions(+), 25 deletions(-)

新增未跟踪：
 cmd/web/src/components/FlowEditor/editors/ActionEditor/HeartbeatFields.tsx
```
