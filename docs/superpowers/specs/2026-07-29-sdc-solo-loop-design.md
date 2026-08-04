# 搜打撤单排基础循环设计

## 1. 背景

本地未提交的 `conf/flow/sdc.json` 与 `conf/scripts/sdc_*.lua` 已能完成登录、匹配、入局、帧同步和 `4:69 BattleMidwaySettlement` 上报，但最近一次运行在首轮撤离后停止闭环：

- 客户端在 `4:69` 回执后需要发送 `4:87 BattleMidwaySettlementExit`；旧配置误用 `4:85` 会等待回执超时。
- Logic 未完成玩家退出后的状态切换，`4:72 BattleReward` 等待超时。
- 随后的 `2:62 MainGameOver` 返回 288，下一轮建队和匹配分别返回 314、294。
- `sdcDwellDone` 在首次写入前被条件表达式读取，持续产生状态缺失告警。
- UDP 帧推送同时配置 Lua 回调并由主流程主动 pop，消费模型互相冲突。
- Logic 连接注册了多项当前流程不消费的高频推送，产生队列覆盖告警。

服务端契约已通过真实运行核对：`4:69` 负责提交可信中途结算，`4:87` 负责确认中途结算退出；Logic 完成结算后异步发送 `4:72 BattleReward`，玩家进入结算态后才能执行 `2:62 MainGameOver`。

## 2. 目标

本阶段只打通稳定的单排基础循环：

1. 登录 Logic。
2. 拉取搜打撤战备数据。
3. 创建单排队伍并开始匹配。
4. 进入 Battle，完成加载和开始游戏。
5. 按机器人身份派生 5～29 秒停留时间，并持续发送帧同步。
6. 主动撤离并完成结算、退出、奖励和返回大厅。
7. 清理本轮状态后进入下一轮，至少能稳定重复两轮。

## 3. 非目标

本阶段不实现以下内容：

- 搜索物资、开箱、拾取和背包变化。
- 真实移动、技能和目标选择策略。
- 根据场景推送构建复杂局内状态机。
- 多人组队、队友协调或补齐队友压测。
- 伪造非零收益、击杀或服务器权威统计。

以上内容在基础循环稳定后作为第二阶段单独设计。

## 4. 每轮控制流

每轮由初始化、准备、匹配、战斗和统一清理五段组成。

### 4.1 初始化

新增轮次初始化动作，显式设置：

- `sdcSetupReady=false`
- `sdcMatchStarted=false`
- `sdcDwellDone=false`

并清除上一轮的停留目标、开始时间、帧计数和临时结算状态。所有会被条件表达式读取的布尔值必须先存在，禁止依赖“缺失等于 false”的隐式行为。

### 4.2 准备

准备阶段依次执行退队兜底、拉取战备、选择英雄、创建队伍和准备。最后一个动作成功后才设置 `sdcSetupReady=true`。

任一步失败时，当前准备序列通过 `onError.strategy=skip` 结束；由于完成标志尚未写入，后续布尔门禁不会进入匹配阶段，而是回到统一清理。这样避免内层 `skip` 返回外层后仍继续执行匹配。

### 4.3 匹配

只有 `sdcSetupReady` 为 true 才执行开始匹配。`SdcStartMatch` 在发送前将 `sdcMatchStarted` 重置为 false，成功后设为 true；失败时结束当前匹配分支。

只有 `sdcMatchStarted` 为 true 才进入 Battle。匹配或加载失败不允许带着不完整的 Battle 状态继续连接战斗服。

### 4.4 战斗与撤离

战斗阶段顺序固定为：

1. 连接 Battle TCP、UDP。
2. 注册 Battle。
3. 上报加载进度并等待开始游戏。
4. 持续发送帧同步，达到 5～29 秒目标后退出循环。
5. 关闭 Battle UDP。
6. 发送 `4:69 BattleMidwaySettlementC2S`，等待同路由回执。
7. 发送 `4:87 BattleMidwaySettlementExitC2S`，等待同路由回执。
8. 关闭 Battle TCP。
9. 在 Logic TCP 上等待 `4:72 BattleRewardS2C`。
10. 发送 `2:62 MainGameOverC2S(operation=0)` 返回大厅。
11. `GameOver` 成功后立即清除本地队伍标识；若 `GameOver` 失败，该清理动作不会执行。

`sdc_settle.lua` 只负责构造并发送完整花名册结算数据，不得提前关闭 Battle TCP，也不得删除 Battle 状态。花名册每行的 `serverFightIndex` 使用加载数据中的服务端战斗索引；只有当前玩家行携带 `sdcSettleData`。

### 4.5 统一清理

无论本轮成功或中途失败，外层流程最后都执行统一清理：

1. 幂等关闭 Battle UDP、TCP。
2. 尝试退出残留队伍。
3. 清除 Battle 和搜打撤轮次临时状态；通用清理列表不包含 `teamId`、`teamData`、`teamMemberCount` 和 `teamHeaderId`。

`teamId` 仅由确认成功的退队或返回大厅路径清理。若 `GameOver` 失败，必须保留 `teamId`，让下一轮开头继续退队，避免本地丢失队伍标识后永久卡在 314/294。

## 5. 监听模型

Logic 连接只保留本流程实际消费的持久监听：

- `3:1 MatchSucceedS2C`
- `4:6 BattleStartLoadingS2C`
- `4:72 BattleRewardS2C`

Battle TCP 保留 `4:10 BattleStartGameS2C` 纯缓存监听；Battle UDP 的 `4:11` 改为纯缓存监听，由 `sdc_sync_frame.lua` 使用 `try_udp_listen` 主动消费最新 ACK。不得同时配置 Lua 回调和主动 pop。

未被本流程消费的英雄、商店、邀请和房间推送不注册监听，避免容量为 1 的队列持续覆盖并输出告警。

## 6. 错误处理

- 准备、匹配或 Battle 前置动作失败：结束当前分支，进入统一清理。
- `4:69` 或 `4:87` 失败：不得继续假装撤离成功，结束战斗分支并清理连接。
- `BattleReward` 最多等待 60 秒；超时后仍尝试 `GameOver`，兼容 Logic 已完成结算但奖励推送丢失的情况。
- `GameOver` 使用 `onError.strategy=skip`；成功后执行独立队伍状态清理动作，失败则保留 `teamId` 并在下一轮重试退队。
- 失败轮次执行短暂退避，防止持续刷建队、匹配请求和告警日志。
- 清理动作必须幂等且不向外返回错误，确保所有失败路径都能执行完整清理。

## 7. 变更范围

预计修改：

- `conf/flow/sdc.json`
- `conf/scripts/sdc_settle.lua`
- 调整 `conf/scripts/sdc_can_enter.lua` 的成功诊断日志级别，不改变门禁语义
- 保留并校验当前 `conf/proto/generate_battle.proto` 中 `settlementFighterIndex` 与 `BattleMidwaySettlementExit` 的协议同步改动

不修改通用正常局、排位流程及其共享业务脚本逻辑。

## 8. 验收与验证

静态验证：

1. `go build ./...`
2. `cd cmd/web && npx.cmd tsc -b`
3. `cd cmd/web && npm.cmd run test`
4. 在 FlowEditor 导入 `conf/flow/sdc.json`，校验报告无错误。
5. 检查所有 node/action/listen/script/proto 引用存在且无悬空项。

运行验证：

1. 使用新账号、单机器人启动 `conf/flow/sdc.json`，运行 2～5 分钟。
2. 至少观察到两轮完整链路：匹配成功、开始加载、进入战斗、`4:69` 确认、`4:87` 确认、收到 `4:72`、`2:62` 成功、下一轮再次匹配。
3. 日志不得出现状态条件缺失、监听超时、288、294、314、异常队列覆盖或框架错误。
4. 若外部服务未在 5 分钟内提供第二次匹配，保留首轮成功证据，并明确区分环境等待与流程失败；不得据此声称完成两轮验证。

## 9. 后续阶段

基础循环验证稳定后，再单独设计轻量真实客户端行为。候选范围包括协议已确认的随机移动、普攻和交互帧；这些行为必须作为可失败的旁路，不得阻断撤离与清理主链路。
