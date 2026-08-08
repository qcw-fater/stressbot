# Normal 流程队伍生命周期修复设计

## 背景

`normal.json` 在 `GameOver` 后只清理本地战斗字段，未向逻辑服发送 `TeamLeave`。当前服务端会把完成结算的玩家返回原队伍；下一轮立即执行 `CreateNormalTeam` 时，逻辑服检测到非零 `teamId` 并返回业务错误 314「已经在队伍中」。100 个全新账号运行约 6 分钟时，`GameOver` 648/648 成功且无超时，但 `CreateNormalTeam` 2998 次中有 2275 次失败。

当前未提交的 `errorStrategy` → `onError` 迁移还遗漏了 `ConnectBattleUDP` 和 `RegisterBattle`。一旦这两个动作偶发失败，后续加载和帧同步动作仍可能继续，形成级联超时。

## 目标

- 成功结算后确认退队，再允许下一轮建队。
- 任一建队后动作失败时，关闭战斗连接、清理战斗状态并尝试退队。
- 退队未确认成功时保留精确的 64 位 `teamId`，下一轮只重试清理，不发起新的建队请求。
- 循环动作失败后通过显式状态门禁阻止后续依赖动作。
- 删除 normal 场景从不消费的纯缓存监听，消除队列覆盖告警。
- 不改变现有登录、选模式、选英雄、匹配、战斗和奖励业务语义。

## 非目标

- 不修改游戏服务器行为；`GameOver` 返回队伍是当前服务端的正常语义。
- 不修改共享的排位或搜打撤退队脚本。
- 不通过放宽超时掩盖上游失败。
- 不处理进程重启后本地完全丢失 `teamId`、但服务端仍残留队伍的老账号；验证按项目约定使用全新账号。

## 流程设计

### 每轮入口

`normalModel` 在业务请求前依次执行：

1. 幂等关闭 battle UDP/TCP。
2. 清理上一轮战斗字段，但保留 `teamId`。
3. 执行 `NormalLeaveTeam`；没有 `teamId` 时直接成功。
4. 只有退队成功、服务端明确返回“不在该队伍”或本地本就无队伍时，才继续 `RequestGameModeList` 和 `CreateNormalTeam`。

`NormalLeaveTeam` 失败采用 `skip`，因此直接收束当前 `normalModel`；外层无限循环下一轮仍从清理开始，不会带着旧队伍建新队。

### 正常结束

`GameOver` 成功后依次关闭 battle UDP/TCP、清理战斗字段并退队。退队确认成功后清除 `teamId` 和队伍派生状态。

### 错误恢复

从 `CreateNormalTeam` 到 `GameOver` 的所有可能失败动作统一配置：

```json
"onError": { "handler": "NormalRecovery", "strategy": "skip" }
```

`NormalRecovery` 依次：标记 `normalRoundActive=false`、关闭 battle UDP/TCP、清理战斗字段、尝试退队。退队失败时保留 `teamId`，由下轮入口重试。

`loadLoop` 和 `syncLoop` 会在内部吞掉 `skip`，所以在二者之后分别增加布尔门禁。只有 `normalRoundActive` 仍为 true 才进入加载后半段或结算链；恢复处理把它置 false 后，不会继续调用依赖动作。

### 退队脚本

新增 normal 专用 `normal_leave_team.lua`：

- `teamId` 原样传给 protobuf，禁止 `tonumber(teamId)`，避免雪花 ID 超过 2^53 后失真。
- 首次退队成功：清理队伍状态。
- 返回 311：normal 是单排队长，先取消匹配再重试一次退队。
- 返回 308：服务端已不承认本地队伍，清理失效的本地队伍状态。
- 其他错误：记录中文告警并把原错误返回执行器，使本轮 `skip`，同时保留 `teamId`。

### 监听收敛

保留 normal 实际使用的监听：匹配成功、开始匹配、开始加载、奖励、主状态更新、商城数据、商城限购数据、开始战斗、帧数据。删除英雄、队伍邀请/更新、社团和房间等未消费的纯缓存监听及其空定义。

## 验证

- 新增 `normalFlow.integration.test.ts` 固化成功链、恢复链、循环门禁、64 位 teamId 契约和监听集合。
- 先运行该测试并确认因缺少设计结构而失败，再实现配置和脚本并确认通过。
- 执行 `go build ./...`、`npx tsc -b`、完整 Vitest、FlowEditor 配置校验。
- 使用全新账号运行 normal 2–5 分钟，至少完成两轮；`CreateNormalTeam` 不再出现 314，关键链无错误/超时，日志无未消费监听队列告警。
