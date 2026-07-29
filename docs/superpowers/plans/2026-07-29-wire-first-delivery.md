# Wire-First 状态平面交付报告（对应计划 v3：2026-07-29-wire-first-state-plane.md）

## 1. 交付范围与文件地图

| 组件 | 文件 | 状态 |
|---|---|---|
| C1 `WireValue` + wire 扫描器 + 结构校验器 | `protox/wirevalue.go`（新） | 完成 |
| C3 语义矩阵差分验证（L1） | `protox/wirediff_test.go`（新） | 完成，全绿 |
| C4 Overlay 写模型（含墓碑） | `state/overlay.go`（新）、`state/store.go` | 完成 |
| C5 state 读侧惰性值兜底 | `state/store.go`（GetList/GetMap/Pick* 物化直通） | 完成（见 §4 偏差） |
| C6 响应包装 ownedRaw/dirty | `script/api_proto.go`（respHandle）、`api_network.go`、`api_robot.go` | 完成 |
| C7 影子验证（L2/L3）+ 降级 + `/debug/wire` | `protox/wireshadow.go`（新） | 完成 |
| C9 去重缓存 wire-only | `protox/dedup.go`、`factory.go`、`dedup_http.go` | 完成 |
| C10 批量 span 保留规划器 | `protox/wirevalue.go`（`PlanWireRetention`） | 完成 |
| 引擎接线（响应存储 wire-first + 降级回落） | `engine/action.go` | 完成 |
| Go-store 监听接线（wire 共享/快照 + 降级回落） | `robot/robot.go` | 完成 |
| 唯一脚本用法改动 | `conf/scripts/request_player_data.lua` | 完成 |

## 2. Σwire 常驻消息审计（M0 产出，容量模型输入）

来源：`conf/flow/*.json` 全部 store 映射与 listens 扫描（2026-07-29）。

**整存点（`field==""` → 直接常驻 `WireValue`，体积 = body 字节）**：

| 存储点 | setter | 消息 | 场景 | 体积量级 |
|---|---|---|---|---|
| 全部 flow 的 PlayerLogin | `loginResp` | `Game.LoginPlayerS2C` | 每机器人一次 | 小-中（KB 级） |
| 脚本 `robot.set(resp)` | `playerData` | `Game.LoginPlayerDataS2C` | 每机器人一次 | **大（~74KB wire / 旧 ~600KB 解码态）——主收益** |
| guild.json GetGuildInfo | `currentGuildInfo` | `Game.GuildInfoS2C` | 每机器人 | 中 |
| guild.json JoinGuild | `lastGuildJoinResp` | `Game.GuildJoinS2C` | 每机器人 | 中 |
| client/flow/rank/guild shopDataUpdate 监听 | `systemShopData` | `Game.SystemShopDataS2C` | 广播，WireCache 去重共享 | 中（共享后摊薄） |

**路径式存储点（保留规划器决策 copy/share）**：

- `Game.MainGetGameModeListS2C.gameModeMap`、`Game.TeamCreateS2C.teamId`、
  `Game.LotteryGetInfoS2C.lotteryInfoList`、`Game.MailGetListS2C.mails`、
  `Game.FriendGetListS2C.FriendDetailInfoList` 等（client/flow）；
- guild.json：`GuildInfoS2C`/`GuildJoinS2C`/`GuildCreateS2C` 多路径入 `playerData.guildInfo.*`、
  `GuildUpdateNotifyS2C` 监听 9 条路径映射（同响应多 span → 规划器整体决策）；
- `MainStateUpdateS2C.status`、`SystemShopLimitDataS2C.shopGroupData`（高频监听，标量/小子树）。

**脚本消费型监听（解码瞬态，不留存）**：`hero_update.lua`、`listen_frame_data.lua`、
`listen_guild_join/kick_member/member_update.lua`（kick 用到 `set_path(x, nil)` → 墓碑已覆盖）。

## 3. 验证结果（2026-07-29）

- `go build ./...` / `go vet`（protox/state/engine/robot/script）：干净。
- `go test ./... -count=1`：除 `stressbot/codec` 两个**预先存在**的数据漂移失败
  （`errors.json` 738 条 vs `error.lua` 639 条，与本次改动无关）外全绿；
  `script` 全量 492s 通过。
- L1 差分 fuzz：语义矩阵逐行用例（合并/oneof/last-wins/packed 混排/map 重 key/
  错误 wire type/空出现/非法拒绝）+ 200 轮随机消息×拼接变异×全路径语料双侧比对
  + 随机损坏字节合法性判定等价，全绿。
- 新增定向测试：`protox/wireretention_test.go`（规划器 4 例）、`state/overlay_test.go`
  （墓碑/最小书脊 2 例）、`script/api_wire_resp_test.go`（干净复用快照/改写重编码/
  子消息置脏传播/整存后嵌套写+删除 4 例）。

## 4. 与计划的偏差（评审记录）

1. **C2 PathProgram 未做独立编译器**：现实现为 `navSplitCached`（路径→分段全局缓存）
   + 每层 `Fields().ByName()`（O(1) 描述符查找）。字段号程序化编译的增益是省字符串查找,
   属 CPU 微优化；wire 扫描本身（线性扫层）才是主成本。若 M5 压测 CPU 不达标再补。
   路径合法性错误由影子验证 L2 首访兜住（失配即降级+告警），非静默。
2. **C5 state API 未做形态收敛（Lookup/Iterate/Pick 重命名）**：以等价方式落地——
   现有 `GetList/GetMap/PickMapKey/PickMapValue/GetPath` 内部对
   `ValueMaterializer`/`PathNavigator` 做物化/导航兜底，消费方（engine bindings、
   filter `navigatePathValues`、Lua API）已全部经这些入口，无直接类型依赖泄漏。
3. **C8 单步投影 trie 顺延**（计划自身标注"可顺延至 M2 初"+"实测顶不住再单点评审"）：
   当前每路径独立扫描。K 路径×bytes 的扫描成本待 M5 压测 CPU 数据决定是否投入。
4. **overlay 每机器人硬上界告警未单独实现**：overlay 体积 ∝ 脚本实际写入量,
   与旧"展开 map"形态的脚本写入同源同量级（旧形态从未失控）；监控经 `/debug/wire`
   与既有 MemStats 面板覆盖。若压测发现脚本写爆再加硬顶。

## 5. M5 容量门禁执行清单（待压测环境执行）

前置：部署本分支 agent；`/debug/wire`、`/debug/dedup`、pprof 端口可达。

1. **5000 人基线（影子全开）**：
   - 跑满一轮完整业务循环（登录→组队→战斗→结算→公会）；
   - `/debug/wire`：`mismatches == 0`、`degraded` 空、`checks` 覆盖全部实际路径；
   - `/debug/dedup`：广播命中率 ≥ 80%，`rawBytes` 有界；
   - live heap 对比旧版：整存部分应显著下降（playerData 每机器人 ~600KB → ~74KB）。
2. **8000 人**：增长率 ≤ 1MB/min 且趋零；无 mailbox drop；稳态 CPU ≤ 80%。
3. **10000 人**：RSS p99 ≤ 12GiB；其余同上。
4. **影子稳态关闭评估**：全部路径过 L2 且 L3 零失配后 `SetWireShadowEnabled(false)`,
   复测 CPU 回收量。
5. **最终容量 = min(N_memory, N_cpu)**；任何 schema 出现 degraded → 修 wire 扫描器
   缺陷后重跑（降级回落保证正确性,但会失去该 schema 的内存收益）。

## 6. 回滚开关

- 影子验证失配 → **自动**按 schema 降级解码路径（无需人工）；
- 全局回退：`SetWireShadowEnabled` 仅控验证；如需整体禁用 wire-first,
  回滚本分支即可（state/引擎接口向后兼容,无持久化格式变更）。
