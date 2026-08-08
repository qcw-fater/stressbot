# Normal Flow Team Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 normal 场景在成功、网络失败和循环动作失败时都先完成队伍/连接清理，再进入下一轮，消除 314 重试风暴和级联超时。

**Architecture:** 用 normal 专用退队 Lua 保留 64 位 `teamId` 精度并把未完成清理显式返回为动作错误；用统一 `NormalRecovery` 处理所有建队后错误；用 `normalRoundActive` 门禁补足 loop 对 `skip` 的吞并语义。流程集成测试直接读取真实 `normal.json` 和 Lua 源码，锁定生命周期与监听契约。

**Tech Stack:** JSON TaskFlow、Lua、React/Vite TypeScript、Vitest、Go 1.23+

---

### Task 1: 添加 normal 生命周期回归测试（RED）

**Files:**
- Create: `cmd/web/src/components/FlowEditor/validation/normalFlow.integration.test.ts`
- Test: `cmd/web/src/components/FlowEditor/validation/normalFlow.integration.test.ts`

- [x] **Step 1: 写入失败测试**

测试必须直接读取 `../../conf/flow/normal.json` 和 `../../conf/scripts/normal_leave_team.lua`，断言：

```ts
expect(flow.nodes.normalModel.next?.slice(0, 4)).toEqual([
  'CloseBattleUDP', 'CloseBattleTCP', 'CleanupBattle', 'NormalLeaveTeam',
]);
expect(flow.nodes.startBattle.next?.slice(-5)).toEqual([
  'GameOver', 'CloseBattleUDP', 'CloseBattleTCP', 'CleanupBattle', 'NormalLeaveTeam',
]);
expect(flow.nodes.NormalRecovery.next).toEqual([
  'NormalMarkRoundFailed', 'CloseBattleUDP', 'CloseBattleTCP',
  'CleanupBattle', 'NormalLeaveTeam',
]);
expect(flow.nodes.normalAfterLoad).toMatchObject({
  type: 'boolean', condition: 'state:normalRoundActive', trueNext: 'normalLoadedBattle',
});
expect(flow.nodes.normalAfterSync).toMatchObject({
  type: 'boolean', condition: 'state:normalRoundActive', trueNext: 'normalSettlement',
});
```

还要遍历建队后的风险节点，断言 `onError` 为 `{ handler: 'NormalRecovery', strategy: 'skip' }`；断言退队脚本含取消匹配和失败返回、且不含 `tonumber(teamId)`；断言逻辑服监听仅包含实际消费者；最后用 `validateFlow` 确认没有配置错误。

- [x] **Step 2: 运行测试并确认按预期失败**

Run:

```powershell
Set-Location cmd/web
npm run test -- --run src/components/FlowEditor/validation/normalFlow.integration.test.ts
```

Expected: FAIL，首个失败指出 normalModel 缺少清理前置链或缺少 `normal_leave_team.lua`，而不是 TypeScript/路径错误。

### Task 2: 实现精确退队与恢复拓扑（GREEN）

**Files:**
- Create: `conf/scripts/normal_leave_team.lua`
- Modify: `conf/flow/normal.json`
- Test: `cmd/web/src/components/FlowEditor/validation/normalFlow.integration.test.ts`

- [x] **Step 1: 新增 normal 专用退队脚本**

实现 `currentTeamId`、`clearTeamState`、`leaveOnce` 和 `cancelMatch`。`execute` 必须遵循：无队伍返回 nil；成功清状态；311 取消匹配后再退一次；308 清理失效状态；其他错误保留 teamId 并 `return err`。任何位置都不得把 teamId 转成 Lua number。

- [x] **Step 2: 在 normal.json 添加动作和节点**

新增动作：

```json
"NormalLeaveTeam": { "pattern": "lua", "script": "normal_leave_team.lua" },
"NormalMarkRoundActive": {
  "pattern": "setState",
  "bindings": [{ "type": "fixed", "field": "normalRoundActive", "value": true }]
},
"NormalMarkRoundFailed": {
  "pattern": "setState",
  "bindings": [{ "type": "fixed", "field": "normalRoundActive", "value": false }]
},
"CloseBattleTCP": { "pattern": "tcpClose", "service": "battle" },
"CloseBattleUDP": { "pattern": "udpClose", "service": "battle" }
```

新增同名 action 节点；`NormalLeaveTeam` 使用 `{ "strategy": "skip" }`。新增 `NormalRecovery`、`normalAfterLoad`、`normalLoadedBattle`、`normalAfterSync`、`normalSettlement`，并把 `startBattle` 拆成“连接/加载循环 → 门禁 → 加载后链 → 帧循环 → 门禁 → 结算链”。

- [x] **Step 3: 把建队后失败统一接入恢复链**

以下节点统一使用：

```json
"onError": { "handler": "NormalRecovery", "strategy": "skip" }
```

节点集合：`CreateNormalTeam`、`SelectHero`、`StartMatch`、`MatchSucceed`、`ListenStartLoading`、`ConnectBattleTCP`、`ConnectBattleUDP`、`RegisterBattle`、`LoadProgress`、`BattleLoadOK`、`StartGame`、`SyncFrameData`、`BattleEnd`、`BattleReward`、`GameOver`。

- [x] **Step 4: 运行聚焦测试并确认通过**

Run:

```powershell
Set-Location cmd/web
npm run test -- --run src/components/FlowEditor/validation/normalFlow.integration.test.ts
```

Expected: PASS，normal 生命周期测试全部通过。

### Task 3: 删除未消费监听并保持配置有效

**Files:**
- Modify: `conf/flow/normal.json`
- Test: `cmd/web/src/components/FlowEditor/validation/normalFlow.integration.test.ts`

- [x] **Step 1: 收敛 ConnectLogicTCP.listenRefs**

保留：`matchPoll`、`teamStartMatch`、`loadingPoll`、`rewardPoll`、`stateUpdate`、`shopDataUpdate`、`shopLimitDataUpdate`。删除 `heroUpdate`、`teamUpdateInfo`、`teamNotifyInvite`、`teamJoin`、全部 guild 监听和全部 room 监听。

- [x] **Step 2: 删除对应空 listens 定义**

删除未消费监听的顶层定义，保留 battle 的 `battleStartGame` 与 `frameData`。

- [x] **Step 3: 运行聚焦测试与仓库配置测试**

Run:

```powershell
Set-Location cmd/web
npm run test -- --run src/components/FlowEditor/validation/normalFlow.integration.test.ts src/components/FlowEditor/validation/configFiles.test.ts
```

Expected: 两个测试文件全部 PASS，`validateFlow(normal.json)` 无 errors。

### Task 4: 全量静态验证

**Files:**
- Verify only

- [x] **Step 1: Go 全仓编译**

Run:

```powershell
$env:GOCACHE = 'D:\Gitee\stressbot\.tmp\gocache-normal-fix'
go build ./...
```

Expected: exit 0，无编译错误。

- [x] **Step 2: 前端 TypeScript 编译**

Run:

```powershell
Set-Location cmd/web
npx tsc -b
```

Expected: exit 0，无类型错误。

- [x] **Step 3: 完整 Vitest**

Run:

```powershell
Set-Location cmd/web
npm run test
```

Expected: 所有测试通过，0 failed。

### Task 5: 新账号多轮运行验证

**Files:**
- Verify: `conf/flow/normal.json`
- Inspect: `log/stressbot.log`
- Inspect: `metrics/metrics_*.csv`

- [x] **Step 1: 生成只改账号范围、机器人数量和时长的临时配置**

使用新的账号号段，显式传入 `-flow conf/flow/normal.json -proto conf/proto -scripts conf/scripts -adapter conf/adapter`，运行 2–5 分钟并确保至少有机器人完成两轮 `GameOver`。

- [x] **Step 2: 检查监控数据**

Expected: `CreateNormalTeam` 无 314；`NormalLeaveTeam` 成功次数与已完成轮次对应；关键动作无 timeout，失败后没有下游级联样本。

- [x] **Step 3: 检查日志**

Run:

```powershell
rg -n -i 'error|warn|失败|超时|监听队列已满' log/stressbot.log
```

Expected: 没有 314、级联超时、normal 未消费监听队列覆盖或其他异常；停止阶段的预期取消单独识别，不误报。

### Task 6: 交付前差异审查

**Files:**
- Review: `conf/flow/normal.json`
- Review: `conf/scripts/normal_leave_team.lua`
- Review: `cmd/web/src/components/FlowEditor/validation/normalFlow.integration.test.ts`

- [x] **Step 1: 路径级 diff 审查**

Run:

```powershell
git diff -- conf/flow/normal.json conf/scripts/normal_leave_team.lua cmd/web/src/components/FlowEditor/validation/normalFlow.integration.test.ts docs/superpowers/specs/2026-08-04-normal-flow-team-lifecycle-design.md docs/superpowers/plans/2026-08-04-normal-flow-team-lifecycle.md
```

Expected: 只包含已批准的 normal 生命周期、监听收敛、测试和文档变更；用户原有的 CN LBS、模式过滤和其他未提交优化保持不变。

- [x] **Step 2: 不自动提交**

项目 Git 约束要求只有用户明确要求时才创建 commit，因此本计划完成后保留工作区变更并报告验证证据。
