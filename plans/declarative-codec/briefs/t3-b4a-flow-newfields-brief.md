# T3 Batch-4 任务 A — FlowEditor 同步 T2 新字段/动作（§3.6）

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）。
> 依赖：T2 后端已加 `ListenRef.QueueSize`、`tcpHeartbeat`/`udpHeartbeat` 动作、`HeartbeatField`、禁用
> `ListenDef.script`。前端 TS 类型 + FlowEditor 落后，本任务追平。

## 1. 任务定位

T2 后端加了声明式心跳动作（`tcpHeartbeat`/`udpHeartbeat`，双模式 body）、`ListenRef.queueSize`、并把
`ListenDef.script` 改为 fail-loud。前端 FlowEditor 的 TS 类型 + 动作/监听编辑器 + 校验还是旧态：
`ActionPattern` 无心跳、`ListenRef` 无 queueSize、监听编辑器仍可填 script。本任务把前端追平 T2。

## 2. 现状（先读码 + Go 权威）

**Go 权威（镜像）**：
- `engine/flow.go:75-80` `ListenRef.QueueSize *int json:"queueSize,omitempty"`（缺省 1，<=0 fail-loud）。
- `engine/flow.go:170` `ActionDef.C2SProto`（已有）；`:183-185` `IntervalMs int` / `HeartbeatFields []HeartbeatField` / `SkipWhenMissing bool`。
- `engine/heartbeat.go:32` `HeartbeatField`（`type/source/key`；source ∈ state/stateCounter/counter/timestamp/fixed；type ∈ u8/u16/u24/u32/u64/i8.../f32/f64）。
- `engine/action.go:741-748` 校验：`intervalMs>0`；**c2sProto 与 heartbeatFields 互斥**（双模式二选一）；service 必填。

**前端现状**：
- `cmd/web/src/types/flow.ts:70` `ListenRef` 无 queueSize。
- `cmd/web/src/types/action.ts:7-16` `ActionPattern`/`ALL_ACTION_PATTERNS` 无 tcpHeartbeat/udpHeartbeat；`ActionDef`(105-121) 无 intervalMs/heartbeatFields/skipWhenMissing。
- `cmd/web/src/types/listen.ts:16-21` `ListenDef.script`（保留类型，但编辑器应禁用入口 + 校验报错）。
- `cmd/web/src/components/FlowEditor/editors/ActionEditor/`（动作表单，按 pattern 分派——**先读** DeclarativeForm 与各 pattern 表单，看 tcpRequest 等怎么渲染 service/route/c2sProto/bindings，心跳表单照此加）。
- `cmd/web/src/components/FlowEditor/validation/refsCheck.ts`（flow 校验；`PATTERNS_REQUIRE_SERVICE/ROUTE/ADDRESS` 等数组——心跳要纳入）。
- 监听编辑器（编辑 ListenRef / ListenDef 的组件——**先 grep 定位**，可能 ListenEditor 或 ListenRefsEditor）。
- `conf/flow/flow.json:782-826` 真实心跳动作样例（RegisterLogicHeartbeat 空 body / RegisterBattleTCPHeartbeat raw-binary heartbeatFields）。

## 3. 实现规格

### 3.1 类型（types/flow.ts + types/action.ts）

- `ListenRef` 加 `queueSize?: number`（镜像 Go `*int`，缺省 1）。
- `ActionPattern` += `'tcpHeartbeat' | 'udpHeartbeat'`；`ALL_ACTION_PATTERNS` 同步加。
- `ActionDef` 加 `intervalMs?: number`、`heartbeatFields?: HeartbeatField[]`、`skipWhenMissing?: boolean`。
- 新 `HeartbeatField` 接口（types/action.ts 或 heartbeat.ts）：`{ type: string; source: 'state'|'stateCounter'|'counter'|'timestamp'|'fixed'; key?: string; value?: number }`——**逐字镜像 `engine/heartbeat.go:32` 的 HeartbeatField**（先读它确认字段名/类型，勿臆测）。

### 3.2 动作编辑器：心跳表单（tcpHeartbeat/udpHeartbeat）

在 ActionEditor 的 pattern 分派里加心跳分支，字段：
- `service`（必填，复用现有 service 输入）、`route`（必填，复用 route 编辑）、`intervalMs`（数字，>0）。
- **body 模式三选一**（互斥，与 Go action.go:745 一致）：
  - **空 body**：都不填（如 RegisterLogicHeartbeat）。
  - **proto 模式**：`c2sProto` + `bindings: FieldBind[]`（复用现有 bindings 编辑器）。
  - **raw-binary 模式**：`heartbeatFields: HeartbeatField[]`（表格：每行 type 下拉 / source 下拉 / key 输入，增删）。
  - UI 用 Radio/Segmented 切模式；切到 proto 清 heartbeatFields、切到 raw-binary 清 c2sProto+bindings（互斥）。
- `skipWhenMissing`（开关，仅 raw-binary 有意义；proto 模式可置灰）。
- **bindings 子集**：proto 模式的 bindings UI 限制为心跳允许的 binding type 子集。先读 Go（engine 绑定解析）确认心跳 proto bindings 允许哪些 type——计划 §3.6 说「只开放 fixed/state/counter/timestamp」，若 Go 的 BindingType 无 counter/timestamp 则按 Go 实际允许集（多半 fixed/state），**以 Go 为准勿臆测**，禁用随机类/map/list 类/Lua 条件。

### 3.3 监听编辑器：queueSize + 禁用 script

- `ListenRef.queueSize`：在监听引用编辑处加可选「队列容量」数字输入（placeholder「缺省 1」；<=0 即时提示，校验交给 refsCheck）。
- `ListenDef.script`：**监听定义编辑器移除 script callback 配置入口**（不再提供「执行脚本」形态；classifyListen 的 'lua' 分支在 UI 不再可达）。**类型保留** script?（旧 flow.json round-trip 不丢），但若有 script 字段，校验报错（见 3.4）。

### 3.4 flow 校验（refsCheck.ts 或对应校验模块）

- 心跳动作（tcpHeartbeat/udpHeartbeat）：`intervalMs>0`、`service` 必填、`route` 必填、**c2sProto 与 heartbeatFields 互斥**（同时配置报错）、bindings type 子集合法。把 tcpHeartbeat/udpHeartbeat 纳入 `PATTERNS_REQUIRE_SERVICE`/`PATTERNS_REQUIRE_ROUTE`。
- `ListenRef.queueSize`：未填合法（缺省 1）；显式 `<=0` 报错。
- `ListenDef.script`：存在 script 字段 → 报错（「监听脚本回调已下线，请改用 s2cProto+store 或 silent」），与后端 fail-loud 一致。
- 校验信息中文。

## 4. 全局约束（bind）

- **严格镜像 Go**（engine/flow.go / heartbeat.go / action.go）——字段名/类型/校验规则逐一对齐，勿臆测；不确定先读 Go。
- **改动文件**：`types/{flow,action,listen}.ts` + ActionEditor 心跳表单（+ 可能新 HeartbeatFields 子组件）+ 监听编辑器（queueSize + 去script入口）+ refsCheck 校验。**严禁动** services/、codecEditor/、后端。
- **禁止兼容性兜底**：旧 flow.json 的 script 字段不静默接受（校验报错）；queueSize <=0 报错；心跳双模式冲突报错。不 `??` 兜。
- **UI 文本不暴露技术术语**：用「心跳」「队列容量」「监听」；心跳字段 type/source 是配置术语可。
- 类型安全；`ALL_ACTION_PATTERNS`/`PATTERNS_REQUIRE_*` 等常量同步；**不要 git commit**。
- 按 TDD：可抽的纯校验函数（如心跳互斥校验、queueSize 校验）先写测试。

## 5. 验证（全绿才算 DONE）

- `cd cmd/web && npx tsc -b` exit 0。
- `cd cmd/web && npm run test` 通过（既有 248 不回归 + 新纯校验单测；若有 refsCheck 测试，同步心跳/queueSize/script 用例）。
- 用 conf/flow/flow.json 的真实心跳动作（RegisterLogicHeartbeat 空 body、RegisterBattleTCPHeartbeat raw-binary）在前端打开校验，应无错。
- 自查 `git diff --stat`：改动限于上述文件。

## 6. 报告

写到 `plans/declarative-codec/reports/t3-b4a-flow-newfields-report.md`：实现要点、类型追加、心跳表单（三模式 + bindings 子集）、queueSize + script 禁用、校验规则、与 Go 对齐说明、测试、tsc/test 结果、自审、贴 `git diff --stat`。

返回消息只含：① 状态；② 改动文件清单；③ 一行测试摘要；④ concerns（尤其心跳 proto bindings 允许的 type 子集——以 Go 为准的结论）。有歧义先问。
