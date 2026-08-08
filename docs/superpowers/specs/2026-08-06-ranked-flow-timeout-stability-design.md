# 排位流程超时稳定性修复设计

## 背景

300 人本地压测和 15000 人正式环境压测均出现大量匹配确认错误与级联超时：逻辑服在 `ConfirmMatchSuccess` 阶段以 `PS_Matching` 返回业务码 288，客户端随后继续等待 `MatchEnterRoom` 和 `BattleStartLoading`，最终产生不可接受的 `ListenStartLoading` 180 秒超时。战斗服还会出现“等待加载超时, 战斗开始”，表示至少一名真人玩家没有在 60 秒内完成 `BattleLoadOkC2S`。

## 根因

逻辑连接上的 `3:1 MatchSucceed`、`3:5 MatchEnterRoom`、`4:6 BattleStartLoading` 是持久监听队列。某轮流程提前退出后，旧推送可能留在队列中。下一轮直接消费队首旧 `3:1`，把上一局的 `sessionId` 当成本局会话并立即确认；服务端当前仍是 `PS_Matching`，因此返回 288。当前 `wait_match_enter_room.lua` 又在超时后返回成功，导致流程继续发送无效 BP 并进入 `ListenStartLoading`，把上游失败扩大为 180 秒级联超时。

服务端当前匹配成功时先把玩家状态改为 `PS_MatchSucceed(5)` 并推送 `2:18 MainStateUpdateS2C`，之后才转发 `3:1`；但 200 人首轮长测证明，本地异步监听写入的 `playerStatus` 可能晚于 `3:1` 可见，不能把这个镜像状态作为硬门禁。可靠的新鲜度依据是业务关联 ID：`3:1` 中自己的 `actor.matchData.teamId` 必须等于本轮 state 的 `teamId`；`4:6` 中自己的 `record.FighterList[*].matchData.sessionId` 必须等于当前 `3:1` 保存的 `battleSession`。

## 设计

### 1. `MatchSucceed` 当前轮校验

- 使用真实时间维护原有 1860 秒总等待窗口，不能因丢弃旧消息而为每条消息重新获得完整超时。
- 循环消费 `3:1`，只在同时满足以下条件时接受：
  - actorList 中存在自己；
  - 自己的 `matchData.teamId` 与当前 state 的 `teamId` 以字符串形式精确相等，避免 int64 经 Lua number 丢精度。
- `playerStatus` 只写入诊断日志，不参与接收判定；旧 teamId 消息只记录 warn 并继续等待，不写入 `battleSession`、`battleArea`。

### 2. 上游失败立即终止依赖链

- `wait_match_enter_room.lua` 保持 60 秒上限以覆盖服务端 15 秒确认窗口和高负载调度余量。
- 超时必须返回原始错误，由 `WaitMatchEnterRoom.onError.strategy=skip` 结束当前排位战斗分支。
- 不再继续执行 BP、`ListenStartLoading` 或战斗连接。

### 3. `BattleStartLoading` 会话校验

- 使用真实时间维护原有 180 秒总业务窗口。
- 循环消费 `4:6`，从 FighterList 找到自己并提取 `matchData.sessionId`。
- 候选 session 与 `MatchSucceed` 保存的当前 `battleSession` 不相等时，视为旧推送并继续等待；校验通过后才写入 battleId、地址、密钥、fighterIndex 和结算数据。
- 关键字段缺失仍作为脚本检查错误返回，禁止带残缺状态进入战斗服。

### 4. 清理恢复

- `ranked_leave_team.lua` 将逻辑服 288 与团队服 311 都视作“仍处于匹配状态”。队长/单排先取消匹配，再重试一次退队；队员保留 teamId 等待队长清理。
- 退队重试仍失败时保留 teamId，避免错误清状态制造 308/314 循环。

### 5. 战斗加载止损

- `BattleLoadOK` 节点增加有限重试；重复 `4:9` 对服务端 `loadOKMap[index]=true` 是幂等的。
- 最终发送失败时以 `skip` 结束当前战斗分支，不再进入 `StartGame` 180 秒监听。

## 非目标

- 不清空持久监听队列：盲清存在丢掉当前轮刚到推送的竞态。
- 不在收到 288 后简单重试确认：这会掩盖旧 `3:1`，且可能错过服务端 15 秒确认窗口。
- 不修改业务服务器源码、超时参数或进程生命周期。

## 验证

- 新增排位流程集成测试，约束三段新鲜度校验、失败止损、288 清理恢复和 `BattleLoadOK` 重试配置。
- 前端流程校验无错误；执行 `go build ./...`、`npx tsc -b`、Vitest 全量测试。
- 不干预现有任务；确认无冲突后使用新账号运行 200 人至少 30 分钟。
- 客户端不得出现 `ListenStartLoading` 超时、错误后继续执行依赖动作或大量 288；审查逻辑服、房间服和战斗服日志，确认没有由压测流程缺失确认/LoadOK 造成的超时。
