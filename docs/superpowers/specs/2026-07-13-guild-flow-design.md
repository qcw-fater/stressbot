# 社团压测流程完善设计

## 1. 目标与设计边界

完善 `conf/flow/guild.json` 及相关 Lua，使社团压测在一次完整任务中形成由本任务在线机器人创建、管理和参与的社团池，同时保留现有 loop 和随机业务设计。

核心目标：

- 所有搜索和申请目标只能来自本任务社团池，不使用服务器历史社团作为候选。
- 约每 15 个机器人提供 1 名固定创建者，保证池中社长属于本任务在线机器人。
- 保留随机 `acceptApply`、`needApproval`、审核通过/拒绝、退出和踢人，持续覆盖无社团与社团内接口。
- 重复申请等服务端业务错误是允许的压测样本，不能造成机器人退出或永久失去后续循环。
- 简单判断和请求优先声明式节点；Lua 只承担 Redis 协调和必须过滤的推送处理。
- 不修改 `robot.get_index()` 的 Go 实现。本设计依赖其由其他改动保证：任务内全局唯一、从 0 连续递增、不受 `StartNumber` 影响。

## 2. 已确认的业务意图

现有流程的随机性应保留，而不是让所有机器人尽快加入社团：

- 无社团机器人持续搜索和申请多个社团。
- 社团可以随机不接受申请、直接加入或要求审批。
- 管理者随机通过或拒绝申请。
- 普通成员可能退出或被踢，再回到无社团路径。
- 因此获取列表、搜索、申请、审核、退出、踢人和社团内业务会在整个任务期间持续产生流量。

本次修复的核心是替换候选来源：从服务器历史列表改为本任务在线社团池，而不是重写业务随机策略。

## 3. 总体架构

采用“最小社团池覆盖层”：

1. `index % 15 == 0` 的机器人在无社团时负责创建社团。
2. 创建成功后将社团发布到任务级 Redis Hash。
3. 其他无社团机器人从整个任务池随机选择一个社团用于搜索或申请。
4. 已入社机器人按服务器返回的真实职位进入普通成员、小队长或会长业务。
5. 会长进入已入社流程时刷新池记录的在线时间。
6. 任务结束时由现有 `sharedstate` 按 `runId` 清理整个命名空间。

不使用严格队列、claim、token、成员绑定、申请去重、失败黑名单或历史社团回退。

## 4. 固定创建者

角色分配完全声明式：

```json
{
  "type": "boolean",
  "condition": "state:index % 15 == 0",
  "trueNext": "guildCreatorBootstrap",
  "falseNext": "guildParticipantActions"
}
```

`index` 已在 `NewRobot` 时注入 state，无需 Lua 分配节点，也不预先写 `groupNo` 或 `slot`。

固定创建者仅约束无社团路径：

- 无社团时持续尝试创建，不加入其他社团。
- 创建成功后成为真实会长并发布社团。
- 以后若失去社团，再次进入创建路径。
- 已入社后的权限判断始终依据 `playerData.guildInfo.mydata.position`，不能用 index 冒充实际职位。

## 5. 无社团流程

最终结构：

```text
outGuildBase
├─ GetGuildList                         # 仅覆盖列表接口，不作为加入候选
└─ outGuildIsCreator
   ├─ creator → guildCreatorBootstrap
   │  ├─ CreateGuild
   │  └─ GuildPublish
   └─ participant → guildParticipantActions (weighted)
      ├─ SearchTaskGuild
      │  ├─ SelectTaskGuild
      │  └─ hasTaskGuildTarget → SearchGuildList
      └─ JoinTaskGuild
         ├─ SelectTaskGuild
         └─ hasTaskGuildTarget → JoinGuild
```

参与者搜索与申请保持原相对倾向 `45:40`。weighted 权重无需凑到 100。

池为空时 `SelectTaskGuild` 正常返回并令 `taskGuildTargetReady=false`，本轮不发请求，下轮重新尝试。Redis 读取错误才算动作失败。

## 6. 创建与发布

`CreateGuild` 保持声明式 `tcpRequest`，并保留现有随机字段：

- `name`
- `icon`
- `publicity`
- `cups`
- `needApproval`
- `acceptApply`

因此池中自然存在不接受申请、直接加入和需要审批的社团。

创建节点使用 `onError.strategy=skip`。失败只结束当前创建分支，下轮继续；不能继续执行发布脏状态。

新增 `guild_publish.lua`，唯一职责是发布或刷新当前创建者的社团记录。它必须验证：

- `index % 15 == 0`；
- 当前 `guildId` 有效；
- 真实职位为会长（position 0）；
- 社团名存在。

同一脚本用于创建后发布和已入社循环中的在线刷新。

## 7. 任务社团池

使用一个任务级 Hash：

```text
guild:v3:pool
```

Hash field 为创建者 index（`0`、`15`、`30`……），value 为：

```lua
{
  guildId = "...",
  name = "...",
  leaderIndex = 15,
  leaderRoleId = 123456,
  updatedAtMs = 1784000000000,
}
```

约束：

- `guildId` 是 int64，Lua 和 Redis 中始终用字符串，禁止 `tonumber`。
- `sharedstate` 自动按本任务 `runId` 隔离，不额外拼历史批次标识。
- Hash TTL 可设 120 秒，但单 field 在线性以 `updatedAtMs` 为准，因为任一会长刷新都会延长整个 Hash。
- 随机选择时过滤超过 60 秒未更新的记录，以保证社长属于本任务且仍在线。

任务结束由现有 Admin/sharedstate 清理；不新增脚本级全量清理协议。

## 8. 随机选择目标

新增 `guild_select_target.lua`，唯一职责是：

1. 清除上次目标状态；
2. 读取 `guild:v3:pool`；
3. 过滤结构无效、缺少 ID/名称或在线时间过期的记录；
4. 从剩余记录直接随机选择一个；
5. 写入：
   - `taskGuildTargetId`
   - `taskGuildTargetName`
   - `taskGuildTargetReady`

明确不做：

- 重复申请过滤；
- 申请状态跟踪；
- 容量判断；
- `acceptApply`/`needApproval` 预判；
- 失败黑名单；
- claim 或固定成员绑定；
- 从服务器历史列表兜底。

重复申请 700 等属于允许的业务错误。

## 9. 搜索与加入声明式化

`SearchGuildList` 使用 `taskGuildTargetName`，不再生成随机名称。响应列表可存入 `guildSearchResult`，但不作为后续加入候选。

`JoinGuild` 改为声明式 `tcpRequest`，绑定：

- `uid ← taskGuildTargetId`
- 固定申请文案
- `account ← state.account`

响应 store：

- 整体写入 `lastGuildJoinResp`
- `mydata.guildUid → playerData.guildInfo.guildId`
- `baseInfo → playerData.guildInfo.baseInfo`
- `mydata → playerData.guildInfo.mydata`

待审核响应只有 uid、没有 mydata，因此不能写入本地已入社状态；直接加入时才会更新 guildId 和成员身份。

## 10. 已入社流程与真实职位

保留：

```text
GetGuildInfo
→ GuildRefreshPublish
→ GetGuildMemberList
→ judgeRole
```

`GuildRefreshPublish` 对非固定创建者或非会长安全 no-op。

三个简单 Lua 条件改为声明式：

```text
是否入社：guildId != 0
是否管理者：position == 0 || position == 1
是否会长：position == 0
```

`GetGuildInfo` 每轮刷新：

- `currentGuildInfo`
- `playerData.guildInfo.guildId`
- `baseInfo`
- `baseSetting`
- `mydata`
- `routeData`

任命或状态变化后，下一轮必须按服务器真实职位重新分流。

## 11. 现有业务随机性

### 普通成员

保留签到、排行榜、航路、奖励、低概率退出和低概率战斗等 weighted 动作。

### 小队长

保留获取申请列表、随机审核、低概率踢人和普通成员动作。

### 会长

保留任命小队长、图标、改名、公告、基础设置及管理者动作。

`guild_audit_join.lua` 继续 50% 通过、50% 拒绝；列表为空正常跳过，不做 Redis 配对或重试。

`guild_kick_member.lua` 只选择非自己且真实职位为普通成员（position 2）。没有候选正常跳过；伟大航路阶段返回 710 属正常业务错误。

`guild_appoint_member.lua` 只从普通成员中选人并任命为小队长。已存在小队长导致 716 属正常业务错误。不随机转让会长，避免破坏固定在线创建者职责。

`guild_exit.lua` 保持：无社团跳过、会长不退出、普通成员和小队长可按原低权重退出，且仅在服务端确认成功后清理本地状态。

## 12. 推送状态同步

listen Lua 在配置 `s2cProto` 后收到的 `msg` 已经是字段 table，不能再次调用 `proto.get_field_map(msg)`。

### 加入推送 21:15

只有 `msg.memberId == 当前 roleId` 且 `msg.data` 存在时，才用 `msg.data` 覆盖自己的完整 guildInfo。其他成员加入广播不能覆盖当前机器人的身份。

### 踢出/退出推送 21:16

当 `msg.playerId == 当前 roleId` 时，清理：

- `playerData.guildInfo`
- `currentGuildInfo`
- `guildMembers`
- `guildApplyList`

### 成员更新推送 21:14

只处理 `msg.member.guildData.playerId == 当前 roleId`，更新自己的 guildId、mydata 和社团名。

### 社团更新推送 21:13

改为声明式 listen store，更新 baseInfo、baseSetting、statistics、timeRecord 和 routData，无需 Lua。

实时 listen 已由 Robot 单线程任务队列安全执行，删除旧的 `guild_drain.lua`，不能保留两套处理机制。

## 13. 错误策略

### abort

登录和主连接关键节点：

- RequestZoneList
- PostLogin
- createNewRole
- SelectRole
- ConnectLogicTCP
- PlayerLogin
- RequestPlayerData

### skip

单轮可恢复分支：

- CreateGuild
- GuildPublish
- SelectTaskGuild（Redis错误）
- CreateNormalTeam
- StartMatch
- MatchSucceed
- ListenStartLoading
- ConnectBattleTCP
- ConnectBattleUDP
- RegisterBattle

### resume

搜索、申请、申请列表、审核、踢人、任命、签到、领奖和修改设置等随机业务动作。错误进入 monitor，但下一轮继续。

全部使用当前有效字段：

```json
"onError": { "strategy": "..." }
```

删除旧 `errorStrategy`，不做兼容性兜底。

允许出现的业务错误包括 700、705、706、710、716、718、748 等。它们不应被改写成框架错误或导致机器人停止。

## 14. 脚本范围

新增：

- `conf/scripts/guild_publish.lua`
- `conf/scripts/guild_select_target.lua`

修改：

- `conf/flow/guild.json`
- `listen_guild_join.lua`
- `listen_guild_kick_member.lua`
- `listen_guild_member_update.lua`
- 管理类脚本仅在确有契约、候选或注释问题时修改，不做无意义重写。

从 guild 流程解除引用并在全仓无其他引用时删除：

- `guild_drain.lua`
- `listen_guild_update.lua`
- `has_guild.lua`
- `is_guild_manager.lua`
- `is_guild_leader.lua`
- `guild_join.lua`

## 15. 其他配置修复

- `guild.json` 的 GMAddHero 当前 type 4 实际是加道具，应与已验证的新号初始化一致改为 HeroRoot（type 70），且只在 `initNewRole` 下执行。
- `unlockBasicFunction` 的说明改为符合实际的“领取等级奖励”，不能宣称直接解锁某系统。
- battle 子流程保留，但旧 `errorStrategy` 改成有效 `onError`。

## 16. 验证与成功标准

### 静态/工程验证

- JSON 严格解析；节点、action、listen、脚本引用完整。
- `guild.json` 中 `errorStrategy` 为零。
- 删除脚本无残留引用。
- Lua 经 gopher-lua 加载通过。
- 社团 Lua 中 guildId 不经过 `tonumber`。
- `go build ./...`
- `cd cmd/web && npx tsc -b`
- `cd cmd/web && npm run test`
- FlowEditor 打开 guild.json 后校验无错误。

### 运行验证

以至少 30 个机器人运行 3～5 分钟：

```text
go run ./cmd/agent -config conf/config.json -flow conf/flow/guild.json
```

必须观察到：

1. index 0、15 等固定创建者成功创建并发布社团。
2. 非创建者的搜索和申请目标仅来自当前任务池。
3. 同时出现不接受申请、直接加入、待审核、审核通过和审核拒绝。
4. 至少一个社团有在线会长、小队长和普通成员。
5. 退出或被踢后，机器人重新进入当前任务池申请路径。
6. 其他成员加入不会污染会长自身职位。
7. 单次业务错误不会导致活跃机器人持续减少。
8. 不出现 guildId 精度丢失、Lua callback 契约错误、脚本缺失或旧字段无效问题。

运行后按项目要求审查日志，区分允许的业务错误与框架/配置/状态污染问题。