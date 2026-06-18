# T2-A2.2 报告 — frameData 迁移 + 下线 listen script callback + 启用禁用校验（破坏性，原子）

> 任务 brief：`plans/declarative-codec/briefs/t2-a2-2-brief.md`
> 工作目录：worktree 根 `D:\Gitee\stressbot\.claude\worktrees\declarative-codec`，分支 `worktree-declarative-codec`。
> **未 git commit / add**（遵 brief 硬约束）。

## Status

**DONE（待运行时验证）**。三步原子完成：①新增非阻塞 `network.try_tcp_listen` / `try_udp_listen` + `sync_frame_data.lua` 迁移；②`frameData` 去 `script` + 删 `listen_frame_data.lua`；③`createListenCallback` 删 Script 分支 + `RegisterListen` fail-loud。`go build ./...` / `go vet ./...` 干净，`script` / `robot` / `engine` / `network` 四包测试全绿（含新增 `TestValidateListenDef` 4 子用例）。全仓 `conf/` grep `listen_frame_data` 零残留。

> **战斗流程正确性无法单测覆盖**（需真实服务端 battle 帧交互）。本任务只保证编译/单测绿 + 静态迁移完整 + 语义自洽，**不宣称已验证战斗行为**。详见末节「运行时验证依赖」。

## 改动文件清单

| 文件 | 类型 | 说明 |
|---|---|---|
| `script/api_network.go` | 改 | 新增 `networkTryTCPListen` / `networkTryUDPListen` + 共享 `networkTryListen`（非阻塞单次 pop，不持 luaMu、不解析 proto）；`loadNetworkModule` 注册 `try_tcp_listen`/`try_udp_listen` + godoc 用法 |
| `robot/robot.go` | 改 | `RegisterListen` 调 `validateListenDef` fail-loud；新增纯函数 `validateListenDef`；删 `createListenCallback` 的 Script 分支（仅保留 store 闭包 + nil 兜底） |
| `robot/register_listen_test.go` | 改 | 新增 `TestValidateListenDef`（4 子用例：空 def/s2c+store/script 报错/nil cbDef） |
| `conf/scripts/sync_frame_data.lua` | 改 | `execute` 开头插入非阻塞消费最新 ack（`network.try_udp_listen("battle", {cmd=4,act=11})`），解析 byte[13..16] 小端 uint32 写 `battleAck`（与旧回调逐字一致）；更新注释 |
| `conf/flow/flow.json` | 改 | `frameData` listen def `{"script":"listen_frame_data.lua"}` → `{}`（纯缓存 listen，queueSize 由 listenRef 注册缺省 1） |
| `conf/scripts/listen_frame_data.lua` | 删 | 整文件删除（回调脚本不再被引用，`frameDataInvalidCount` 一并下线） |

未碰 `connectionPump` / `decodeLoop` / `listenLoop` 调度、心跳、`luaMu` / `withReleasedMu`、`udp_listen`/`tcp_listen` 阻塞语义、前端 / admin / agent / cmd、其他 listen 的 queueSize。

## 设计：try_* 非阻塞单次 pop

### 签名与返回语义

```
network.try_tcp_listen(service, route) → code(number), data(string|nil)
network.try_udp_listen(service, route) → code(number), data(string|nil)
```

| 情况 | code | data |
|---|---|---|
| 取到一条消息 | `0` | 原始 body 字符串（**不解析 proto**） |
| 队列空（无新消息） | `31`（`ErrListenTimeout`） | `nil` |
| 服务端 HeaderErr | HeaderErr 码 | 原始 body 字符串 |

### 与阻塞版 listen 的差异（核心）

- **非阻塞**：单次 `GetTCP/UDPListenResp`（per-queue 锁），不轮询、不 sleep、不 `withReleasedMu`。当前 luaMu 仍在，但 try_* 不依赖释放它（2-D 删锁前的过渡形态）。
- **不解析 proto**：try_* 是「原始 drain」原语，需 proto 解析的消费请用阻塞版 `tcp_listen`/`udp_listen`。
- **队列空返回超时码**（`31`），与阻塞超时同码便于脚本统一处理；**不**记 `LastActionError`（非错误路径，避免污染失败统计）。
- `recordBytes(0, RecvWireBytes)` 与 listen 一致累计接收字节。

### 注册点

`loadNetworkModule` 在 `tcp_listen`/`udp_listen` 之后注册 `try_tcp_listen`/`try_udp_listen`，godoc 注释块同步补 `try_*` 用法。

## sync_frame_data.lua：push → pull 迁移

在 `execute(r)` **开头**（读 `battleAck` 之前）插入：

```lua
local code, data = network.try_udp_listen("battle", {cmd=4, act=11})
if code == 0 and type(data) == "string" and #data >= 16 then
    local b1, b2, b3, b4 = string.byte(data, 13, 16)
    if b1 and b2 and b3 and b4 then
        robot.set("battleAck", b1 + b2 * 256 + b3 * 65536 + b4 * 16777216)
    end
end
```

- 解析逻辑与旧 `listen_frame_data.lua:42-53` **逐字一致**（byte[13..16] 小端 uint32）。
- **不保留 `frameDataInvalidCount`**（无读者，纯诊断，随回调下线）。无效数据（code!=0 / 短包 / 缺字节）静默跳过，`battleAck` 保持上轮值。
- 后续逻辑（读 `battleAck` → 构帧 → `udp_send` → `sleep 60`）原样不动。

**语义保持**：battleAck 追踪从「push 回调每帧写」迁到「sync loop 每轮非阻塞 pop 最新写」。`queueSize=1` 保最新 → battleAck 始终反映「最近收到的服务端 ack 帧」。松散「保最新」语义下二者等价（sync loop 只需最新值，不需每帧）。

## frameData 配置变更

`flow.json:2090` 原 `{"script":"listen_frame_data.lua"}` → `{}`（纯缓存 listen：无 proto、无 store、无 script）。listenRef（`ConnectBattleUDP` 节点 :385-394）不变，仍把 route `{cmd:4,act:11}` 注册到 `udp:battle` 连接的 `queueSize=1` queue，供 `try_udp_listen` 消费。flow.json JSON 合法性已校验。

## createListenCallback 瘦身 + RegisterListen fail-loud

- **`createListenCallback`** 删除整个 Script 分支（原 784-829，含 `RunCallbackScript` + luaMu Lock + Context 注入）。函数现在只两种返回：`s2cProto+Store` → Go-store 闭包；否则 `nil`（纯缓存 listen，消息仅入 queue）。godoc 同步更新。
- **`validateListenDef(listenName, cbDef)`** 新增纯函数（紧邻 `effectiveListenQueueSize`）：`cbDef.Script != ""` → 中文 `fmt.Errorf`（含 listen+script 上下文 + 迁移指引）。由 `RegisterListen` 在 `createListenCallback` 前调用，fail-loud 直接 return（不 continue、不静默）。
- 无兼容兜底：不写「忽略 script」、不写「script→store 自动迁移」。

## TDD

### RED → GREEN（robot 包）

- RED：`TestValidateListenDef` 4 子用例（纯缓存空 def 通过 / s2c+store 通过 / **script 非空报错** / nil cbDef 通过），引用尚未存在的 `validateListenDef`。
- GREEN：实现 `validateListenDef` + `RegisterListen` 调用接线 + 删 Script 分支。全部转绿。
- 错误子用例断言含 `frameData`、`listen_frame_data.lua`、`script`、`废弃` 四个子串（覆盖 listen+script 上下文 + 中文迁移指引关键词）。

### try_* 单测（说明）

- brief 允许「若无 Lua 测试设施则跳过 Go 单测但须报告」。script 包仅有 `api_json_test.go` / `runtime_cache_test.go`，**无现成 Lua 调用 harness**（构造 `Context` + NetSender mock + LState 成本高）。try_* 的核心逻辑（route 解析 / `recordBytes` / HeaderErr 分支）与 `networkListen` 同构，由编译期注册保证 + 静态语义自洽覆盖；运行时由 sync_frame_data.lua 实战验证（见末节）。**未单独为 try_* 写 Go 单测**，此项按 brief 允许的 fallback 处理。

## 验证

| 命令 | 结果 |
|---|---|
| `go build ./...` | 干净，无输出 |
| `go vet ./...` | 干净，无输出 |
| `go test ./script/... ./robot/... ./engine/... ./network/... -count=1` | **全绿**（script / robot / engine / network 四包 ok） |
| `go test ./robot/... -run ValidateListenDef -v` | `TestValidateListenDef` 4 子用例全 PASS |
| `sed 's/\r$//' f.go \| gofmt -l`（3 个改动 .go） | 全 canonical（无输出） |
| 全仓 grep `listen_frame_data`（conf/） | **零残留** |
| `python json.load(flow.json)` | 合法 JSON |
| `-race` | **未跑**：`CGO_ENABLED=0`（Windows 无 cgo），沿用 2-A1/A2.1 结构性论证（try_* 单次非阻塞 pop，per-queue 锁，无新增锁序交叉） |

### 静态确认清单

- [x] `network.try_tcp_listen` / `try_udp_listen` 非阻塞单次 pop（不轮询、不 sleep、不持 luaMu via withReleasedMu）；注册就位；返回原始 body（不解析 proto）。
- [x] `sync_frame_data.lua` 每轮开头消费最新 ack 写 battleAck（解析与旧回调逐字一致）；后续逻辑不动。
- [x] `frameData` listen def 无 script（`{}` 纯缓存）；`listen_frame_data.lua` 已删；`frameDataInvalidCount` 随之下线（grep conf/ 零引用）。
- [x] `createListenCallback` Script 分支已删；`RegisterListen` 对 `cbDef.Script != ""` fail-loud（中文，含 listen+script 上下文）。
- [x] 全仓 `conf/` grep `listen_frame_data` 零残留（仅 plans/briefs/docs 历史引用）。

## Self-review

- [x] **battleAck 追踪语义保留**：pull 取代 push，queueSize=1 保最新；sync loop 松散语义下与旧 push 等价。
- [x] **listenLoop 不再跑业务 Lua**：frameData 回调下线后，listenLoop 只做 decode→分发/缓存/Go-store，不触碰业务 LState（2-D 删锁的最后一处异步 Lua 清除）。
- [x] frameData route 仍注册（listenRef 不变），queueSize=1 缓存供 try_udp_listen 消费。
- [x] 不写兼容兜底：`ListenDef.script` 直接 fail-loud，不静默忽略、不自动迁移。
- [x] 不改 `udp_listen`/`tcp_listen` 阻塞语义（try_* 并存）。
- [x] 错误用 `fmt.Errorf` 带中文上下文（注册/配置错误，非 action 执行错误）；godoc 齐全。
- [x] try_* 不走 `withReleasedMu`（非阻塞瞬时）。
- [x] 原子性：5 步一起完成（flow 加载成功 + battleAck 不断流 + listen script 配置 fail-loud）。

## ⚠️ 运行时验证依赖（必须 controller/用户执行）

**战斗流程正确性无法单测覆盖**（需真实服务端 battle 帧交互）。implementer 只保证：编译/单测绿 + 静态迁移完整 + 语义自洽。以下项**依赖 controller/用户按 CLAUDE.md 验证流程运行时确认**：

1. **启动**：`rm -f log/stressbot.log`，`go run ./cmd/agent -config conf/config.json`，运行 **2~5 分钟**。
2. **battleAck pull 模型表现**：
   - battle 帧同步不卡（sync loop 60ms 间隔稳定，`try_udp_listen` 非阻塞 pop 无停滞）。
   - `BattleEnd ≥ 2`（战斗正常推进，不是 0/1 即提前断流）。
   - `battleAck` 被正常更新（sync_frame_data debug 日志里 battleAck 非恒 0）。
3. **错误日志**：`grep -i "error\|warn\|失败" log/stressbot.log | grep -v "headError"` 应无异常输出（特别留意无「已废弃的 script」「监听注册失败」相关 error）。
4. **try_udp_listen 空队列噪声**：queue 空时返回 code=31 但**不打日志**（设计如此，避免高频刷屏）——若发现 sync loop 因频繁空队列有性能问题，反馈到 2-C3 调度模型调整。

**不得宣称**「已验证战斗行为」「battleAck pull 模型已确认正确」。本任务交付的是机械正确性 + 静态迁移完整性。

## Concerns

1. **try_* 无独立 Go 单测**：script 包无现成 Lua 调用 harness（构造 Context + NetSender mock + LState 成本高）。按 brief 允许的 fallback：核心逻辑与 `networkListen` 同构（route 解析 / recordBytes / HeaderErr 分支），由编译期注册 + 静态自洽 + sync_frame_data.lua 实战覆盖。若未来 script 包补 Lua harness，建议为 try_* 的「空队列→code=31」「有消息→原始 body」「HeaderErr→透传码」三路径补单测。

2. **`RunCallbackScript` 未删除**：`script/runtime.go:450` 的 `RunCallbackScript` 仍保留（仅 robot.go 的 Script 分支删除，调用点归零）。按 `02-track-backend-integration.md` §2-A 收尾清单，`RunCallbackScript` 的删除归 2-D（「无调用后删除，避免未来误用」）。本任务范围仅 listen script callback 下线，不动 runtime.go。

3. **battleAck 消费频率 vs ack 到达频率**：旧 push 模型每收到一帧 ack 就更新；新 pull 模型每轮 sync loop（~60ms）pop 一次最新。若服务端 ack 到达频率显著高于 sync loop 频率（如 >16fps），queueSize=1 会丢弃中间 ack——但 battleAck 语义是「最新值」而非「每帧值」，sync loop 只需最新，故等价。若运行时验证发现 ack 丢失导致帧序号跳跃异常，需重新评估 queueSize 或消费节奏（属 2-C3 调度范畴）。

4. **`-race` 未跑**：本机 `CGO_ENABLED=0`。try_* 单次非阻塞 pop（per-queue 锁），无新增锁序交叉；并发安全沿用 2-A1/A2.1 结构性论证。CI 启用 cgo 后建议补跑 `go test -race ./script/... ./robot/... ./network/...`。

## 下一步

T2-A2.2 完成（待运行时验证）。2-A 全部子任务（A1 / A2.1 / A2.2）就位，listen 队列基础设施 + queueSize schema + script callback 下线三件套齐备，为 2-B（声明式心跳）/ 2-C（codec 重构）/ 2-D（删 luaMu）扫清 listen 异步 Lua 最后一处障碍。
