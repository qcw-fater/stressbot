# 声明式 Codec 重构 — 进度账本（subagent-driven 执行）

> Source of truth. 信任本账本与 `git log`，超过对会话记忆的信任。
> Worktree: `D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）
> 起始 BASE：`8cc8a8b`（origin/master，本 worktree 创建时 HEAD，0 commits ahead）

## 已确认决策

- **工作区**：新建 worktree（`worktree-declarative-codec` 分支，隔离，不碰 master）。
- **节奏**：顺序 T1 → T4 → T2 → T3；同一时刻只跑一个 implementer。
- **提交策略**：implementer **不自动 commit**；每任务 implement + review（基于工作树 diff，按任务文件范围）；我在批次边界把文件清单拿给用户确认后，按 git 技能 commit。最终合流到 master 走单独确认。
- **freeze 默认值**（已采用，未阻塞）：
  - `DescribeError(code)` 未命中 → 返回空串 `""`。
  - Admin preview 端点 schema：见 03/04 轨道（`POST /sbot/codec/preview` 入参 `{schema, mode, transport, route, bodyHex, keyHex, frameHex}`）。
  - config codec 映射落点：`standalone.adapter.codecs`（`server串 → codec 文件`），未显式声明按 `<proto>_<service>_codec.json` 推断，缺文件 fail loud。

## 全局约束（bind 所有 implementer）

- **禁止兼容性兜底**：不写 codec.lua/error.lua 自动迁移、不用 `??` fallback；新字段全链路一致。配置/运行错误一律 fail loud，中文信息。
- **codec 绑定粒度**：每连接 `<proto>:<service>` 一份 codec 文件（`<proto>_<service>_codec.json`）+ 共享 `errors.json`。resolver key = `server` 串。不做 runtime fallback。
- **Adapter 签名零改动**：decode 仍 3-tuple `(routeKey, body, headerErr)`，不返回 err/headerFields。
- **每份 codec 单 transport**：encrypt offset 为单向 `{encode, decode}`（不是 tcp/udp 四元组）。`udp:battle` = `{encode:11, decode:0}`。
- **Go 最佳实践**：错误用 `NewActionError` 体系（本轨道若需新框架错误码先在 errcode/codes.go 注册）；包注释/godoc；日志中文；`go build ./...` + `go vet` 通过。
- **commit**：本阶段 implementer **不要 git commit**。完成后报告改动文件，由 controller 批量拿给用户确认。
- **gofmt / 换行（Windows 环境注）**：本 worktree `core.autocrlf=true`，工作树所有 `.go`（及 `.md`）检出为 CRLF，`gofmt -l` 会把**全部工作树文件**标记为「需格式化」（CRLF→LF）。这是环境现象，非缺陷——git 提交时 autocrlf 自动归一为 LF，提交后的 blob 是 LF 且 gofmt-clean。**不要**对单个文件跑 `gofmt -w` 去「修」CRLF（会让该文件在工作树里与同级文件换行不一致）。校验内容是否 canonical：`sed 's/\r$//' file.go > /tmp/x.go && gofmt -l /tmp/x.go`（空即 canonical，仅换行差异）。

## 轨道与任务

### T1 — Go codec 引擎（关键路径，最先）

| 任务 | 内容 | 状态 |
|---|---|---|
| T1.1 | `codec/schema.go` 类型 + JSON load + `Validate`；`codec/errors.go` `LoadErrorMap`+`DescribeError` | ✅ done |
| T1.2 | 4 张算法注册表 + 迁移 lua_crypto/lua_zlib 算法 + 元数据导出 | ✅ done |
| T1.3 | `codec/compile.go`：schema → 不可变编译产物（全预解析） | ✅ done |
| T1.4 | `codec/engine.go` encode + `adapter.NewSchemaAdapter` 封装 + HeaderSize/BodyLength/ExpectedRouteKey | ✅ done（encode 部分；wrapper 移到 T1.5） |
| T1.5 | `codec/engine.go` decode（3-tuple，flag 驱动反序，onError fail/keep） | ✅ done |
| T1.6 | 迁移产物：`tcp_logic_codec.json` / `tcp_battle_codec.json` / `udp_battle_codec.json` + `errors.json` | ✅ done |
| T1.7 | 对拍测试 vs 旧 LuaAdapter + benchmark + race + 冻结交接说明 | ✅ done |

### T4 — 配置加载与分发（T1 后）

**进行中** — T1 已冻结。 seam：T4 建 resolver/loader/admin 基础设施；runtime 接线（main.go/task_runner/Manager/Robot/Dialer）归 T2。

| 任务 | 内容 | 状态 |
|---|---|---|
| T4.1 | `adapter/codec_resolver.go`：CodecResolver 接口 + impl + `NewCodecResolver` + `LoadCodecResolver`（逐连接加载、同文件 dedup、fail loud、errors.json 可选） | ✅ done |
| T4.2 | admin `POST /sbot/codec/preview`（包 codec.Preview）+ `GET /sbot/codec/algorithms`（包 codec.Algorithms） | ✅ done |
| T4.3 | admin 多 codec 分发（multipart 上传/下载、configFiles、baseline、TaskConfig.Codecs map） | ✅ done |

### T2 — 后端集成 + 删锁（T4 后）

进行中 — 顺序 2-A1 → 2-A2 → 2-B → 2-C1 → 2-C2 → 2-C3 → 2-D（删锁须 2-A/B/C 全完 + 审计）。

| 任务 | 内容 | 状态 |
|---|---|---|
| 2-A1 | listen queue 基础设施：`network/listenQueue` 环形队列 + `Connection.listenMsg`→`listenQueues`，默认容量 1≡单槽，零配置面 | ✅ done（工作树未提交，待批次确认） |
| 2-A2.1 | `ListenRef.QueueSize` schema + 注册 API 收敛为 `RegisterListen(routeKey, cb, queueSize)` + `NetSender.Ensure*` 加 queueSize 形参（安全非破坏，默认 1≡现状） | ✅ done（工作树未提交，待批次确认） |
| 2-A2.2 | 下线 listen 脚本回调：frameData 迁到主流程非阻塞 pop（`network.try_udp_listen`）消费 + 删 `createListenCallback` Script 分支 + `ListenDef.script` fail-loud（破坏性，原子） | ✅ done（工作树未提交，待批次确认 + **运行时验证**） |
| 2-B | flow action 声明式心跳（tcpHeartbeat/udpHeartbeat，Go-only builder） | pending |
| 2-C1 | CodecResolver 替换 per-robot adapter（dial/decode 侧） | pending |
| 2-C2 | encode 侧全部按连接（server 串）解析 | pending |
| 2-C3 | 删业务 LState codec 依赖 + connectionPump 替换三协程 | pending |
| 2-D | 删 luaMu / withReleasedMu（须 2-A/B/C 全完 + 审计闸门） | pending |

### T3 — 前端（T1 schema + T4 文件名后）

pending。

## 完成记录

- T1.1: complete — schema+Validate+errors，59/59 测试通过，review clean（仅 gofmt minor，已修）。工作树未提交（待 T1 批次确认）。文件：codec/schema.go, codec/errors.go, codec/schema_test.go, codec/testdata/*。
- T1.2: complete — 4 注册表 + 19 算法迁移 + 元数据导出，35 测试通过，review clean；xor_carry_rol/xor8(bcc)/rc4/crc16/xxtea/pkcs7/hash 逐字同 lua_crypto。文件：codec/registry.go, ciphers.go, compressors.go, checksums.go, hashes.go, registry_test.go。**T1.4 注意**：块密码（aes_ecb/cbc/xxtea）会改变 body 长度，encode 需处理；v1 仅用 xor_carry_rol（定长），不影响对拍。Hasher.Hash 返回原始摘要字节（非 hex）。
- T1.3: complete — compile.go 编译层，7 测试通过，review clean（immutability / onlySmaller∉applies / 四注册表 fail-loud / udp:battle encOffset 11-0 / routeKeySegs 全过）。文件：codec/compile.go, compile_test.go。**T1.4 接力点**：(a) applies(route map[string]any) 用字段名 key（compiledGuard 存了 fieldName）；(b) appliesWith 串行依赖在 encode 内实现（存了 appliesWithIdx）；(c) onlySmaller 在 compress 步内「先压→比对→变小才采用并置 flag」；(d) produces region：ciphered=body[encOffset:]、bodyPlain=管线前、bodyFinal=管线后、header/frame=头写就后；(e) 块密码会变长，v1 不测。**adapter.NewSchemaAdapter 包装移到 T1.5**（encode+decode 都齐后再建，避免半成品 wrapper）。
- T1.4: complete — engine.go encode + BodyLength/ExpectedRouteKey，20 TCP/UDP 对拍 + 14 结构断言全字节级同 codec.lua oracle（review 用 mutation 测试证明断言非空转）。文件：codec/engine.go, engine_test.go。**T1.5 必做修复**：(1) **bcc 真值修正**——bcc 在**加密前**对明文 body[encOffset:] 计算（lua_crypto.go:227 + codec.lua:11 已证），不是密文；decode 须在**解密后**对 body[decOffset:] 重算并比对 checksumOut 字段。（2）**Params/KeyLen 缺失（真潜伏 bug）**——compiledStep（compile.go:264）未存 Params/KeyLen，engine.go:186 传 nil params（rol 默认 3）、:302 硬编码 len(key)==32；T1.5 须给 compiledStep 加 params/keyLen（改 compile.go compileStep），encode+decode 都改用之。当前协议 rol=3/keyLen=32 故对拍通过，但非默认 schema 会静默错码。**总纲 §3.1「ciphered region」措辞误导**（实为明文区），doc nit，代码正确——批次提交时顺手修总纲措辞。
- T1.5: complete — engine.go decode（3-tuple/flag 反序/onError fail-keep，对拍 codec.lua decode）+ Params/KeyLen 修复（compiledStep 加 params/keyLen，encode 用之，非默认 rol=5/aes keyLen=16 测试证明生效，encode 对拍未回退）+ adapter/schema_adapter.go 9 方法包装（var _ Adapter 断言，仅 import codec，无生产循环）。全 repo `go test ./...` 绿。文件：codec/engine.go, compile.go, adapter/schema_adapter.go, decode_test.go, decode_helpers_test.go, engine_test.go, adapter/schema_adapter_test.go, testdata/aes_ecb_codec.json。**T1.7 必做（reviewer 标记，非阻塞）**：(a) **UDP 压缩+加密 对拍缺口**——decode_test.go:171 该用例被排除；engine 已支持 onError:keep，T1.7 须加 UDP 变体 schema（gz 步 onError:keep）并把 `udp_compressible_encrypted_offset11` 纳入对拍矩阵，证与 codec.lua lenient 行为字节一致，闭环而非文档豁免。(b) **godoc 不准**——engine.go verifyProducesAfterDecrypt 注释把「codec.lua decode 根本不校验 bcc」与「不对称时跳过」混为一谈；实际对称路径 engine 比 codec.lua 更严（会校验）。改成：codec.lua decode 完全不校验 bcc；engine 在 encOffset==decOffset 时额外校验（fail/keep），不对称时无法校验故跳过（与 codec.lua 一致）。
- T1.6: complete — conf/adapter/{tcp_logic,tcp_battle,udp_battle}_codec.json + errors.json（667 条）+ codec/migration_test.go；tcp_logic 对拍 LuaAdapter 6 case 字节一致；字段布局对 codec.lua 18/18 一致；UDP 产物 = 已证对的 T1.4 UDP 变体（normalized JSON 同 + 单 offset 改动）。review clean。**T1.7 顺带**：(c) errors.json 全量校验——T1.6 只抽样 8 条+计数，T1.7 加「解析 error.lua errors 表，断言全 667 对 verbatim 一致」测试（不依赖 LuaAdapter/zap）。
- T1.7: complete — 三个 review 遗留全闭环 + 对拍矩阵收口 + benchmark + preview helper + 冻结交接文档。(a) UDP 压缩+加密 用 gz onError=keep 变体纳入对拍（newSchemaCodecUDPKeepGzip + TestDecodeUDP_Parity_CompressibleEncrypted_Offset11，2 case 字节级一致 codec.lua lenient）；(b) verifyProducesAfterDecrypt godoc 修正（仅注释）；(c) TestMigration_ErrorMap_FullVerbatimVsErrorLua 纯文本解析 error.lua 667 对 verbatim PASS。对拍矩阵最终覆盖：encode TCP 13+UDP 9 / decode TCP 10+UDP 5+UDP 压密 2 / 失败语义 5 / 访问器与结构断言全。Benchmark（Win10+i5-9400F）：encode ~1.35×–15×、decode ~1.77×–7.6×、allocs/op −54%–−90%。codec/preview.go + preview_test.go（12 测试 PASS，纯 Go 零 gopher-lua，畸形输入填 Error 不 panic）。冻结交接 plans/declarative-codec/reports/t1-freeze-handoff.md。全 repo go build/vet/test 绿；-race 未跑（无 cgo，结构性论证）。文件：codec/{preview.go,preview_test.go,engine_bench_test.go}、codec/{engine.go(godoc),decode_helpers_test.go,decode_test.go,migration_test.go}、reports/t1-{7-report,freeze-handoff}.md。**T1 全轨冻结**。
- **T1 批次已提交**（4 commits，worktree-declarative-codec，未 push）：`6e73a30` codec 引擎 / `2362111` SchemaAdapter 包装 / `b2f5c6a` 各连接 codec.json+errors.json / `bb37787` 设计与执行记录。base `8cc8a8b`。
- T4.1: complete — adapter/codec_resolver.go：CodecResolver 接口（Resolve(server串)→Adapter，缺映射 nil）+ NewCodecResolver（防御性拷贝）+ LoadCodecResolver（逐连接 LoadSchema+NewSchemaAdapter、同文件 dedup fileCache、空映射/缺文件/解析失败 fail loud 中文、errors.json 可选、排序遍历稳定错误）。13 测试用真实 conf/adapter 产物全 PASS。self-review clean（无 gopher-lua、仅 adapter/、不动 runtime/admin）。文件：adapter/codec_resolver.go, codec_resolver_test.go。
- T4.2: complete — admin/codec_handlers.go：handleCodecPreview（POST /sbot/codec/preview 包 codec.Preview，两步 unmarshal，坏 body/坏 schema→400，预览语义 200 即使 Error 非空）+ handleCodecAlgorithms（GET /sbot/codec/algorithms 包 codec.Algorithms）；路由注册于 handlers.go:108-109。9 测试 PASS（encode/decode 往返、畸形 schema→200+Error、坏 body→400、algorithms 清单含 xor_carry_rol/gzip/xor8）。纯计算无副作用，仅 admin/。self-review clean。文件：admin/codec_handlers.go, codec_handlers_test.go, handlers.go(+2 路由)。
- T4.3: complete — admin 多 codec 分发：TaskConfig 加 Codecs map[string][]byte + ErrorMap []byte（旧 AdapterScript/ErrorMapScript **保留**以维持 agent 编译，T2 删）；multipart 收多份 *_codec.json+errors.json；configFiles 经 buildConfigFiles 列多文件（不含 codec.lua）；baseline 读写多文件；下载多文件（legacy codec.lua/error.lua→404，无兜底）。review clean（admin-only、无旧→新迁移、无默认 codec 兜底、go build ./... 全绿）。4 测试用 T1.6 产物 PASS。文件：admin/types.go, handlers.go, codec_distribution_test.go。**T2 接力**：① 删 TaskConfig 旧字段 AdapterScript/ErrorMapScript；② agent/task_runner + agent/config.go 下载/加载多 codec 走 resolver；③ main.go(task_runner) 调 LoadCodecResolver → Manager；④ 删 RobotAdapter/NewRobotAdapter、Dialer server.adp 兜底。
- **T4 全轨完成**（T4.1+T4.2+T4.3 全 review 通过，go build/vet/test 全绿）。待批次提交确认。
- T2-A1: complete — `network/listenQueue`（固定容量环形队列：`Push` 满覆盖最旧+dropped / `Pop` FIFO / `Dropped` / `Clear` / `newListenQueue(cap<1)` panic 暴露编程错误）+ `Connection.listenMsg map[string]*Message`→`listenQueues map[string]*listenQueue`（常量 `defaultListenQueueSize=1` ≡ 旧单槽，逐字节等价）。改写三处：`dispatchListen` 缓存分支（按需建队列 + `Push`，覆盖时 Debug 日志按本仓惯例 `if stresslog.DebugEnabled()` 守卫——OnReceive/RequestResponse 同款）、`GetListenResp`（FIFO `Pop`）、`listenLoop` ctx.Done（`clear(map)`）。并发模型：`Connection.mu` 仅保护 `listenQueues` map 键查找/创建，per-queue `mu` 串行 Push/Pop，**c.mu 释放后才 Push/Pop**，无锁序交叉/无死锁。review（backend-review skill）：无 🔴；修 2 🟡（dispatchListen Debug 热路径缺 `DebugEnabled` 守卫、测试里 `_ = fmt.Sprintf` noop + 多余 `fmt` import）+ 🔵 rangeint×5；遗留 🔵（`listenQueue.mu` 未用 hat 位、`Clear()` 为 2-C3 connectionPump 预留暂未在 prod 调用）不阻塞。go build/vet/test 全绿（network 14 函数 PASS，含容量1等价单槽、满覆盖最旧、dropped 累计、并发 smoke cap1/cap4）。**2-A2 接力**：① `engine.ListenRef` 加 `QueueSize *int json:"queueSize,omitempty"` + flow 校验（缺省 1、`<=0` 报错、同连接同 `routeKey` 重复注册须**完全一致**才幂等，否则 fail loud）；② `AddListener`/`ListenResponse`/`RegisterListen` 收敛为带 queueSize 注册入口（`RegisterListen(routeKey, cb, queueSize)`），`engine.NetSender.EnsureTCPListener/EnsureUDPListener` 加 `queueSize int` 入参，`robot.netSenderAdapter` 同步；③ 删 `createListenCallback` 的 `cbDef.Script != ""` 分支 + `ListenDef.script` 进禁用清单（v2 配置出现 `script` 直接 fail loud）；④ ranked/team（`teamNotifyInvite`/`teamJoin`/`teamUpdateInfo`）→ 主流程 `network.wait_listen`/声明式 `tcpListen` 消费、guild（`listen_guild_*`）→ 主流程消费，迁移完才启用 `script` 禁用校验。新字段**全链路一致**落地（schema→注册→队列），不在 2-A1 留半接。文件：network/listen_queue.go(新)、network/listen_queue_test.go(新)、network/connection.go(改)、network/connection_test.go(新)、briefs/t2-a1-brief.md、reports/t2-a1-report.md。
- T2-A2.1: complete — `engine.ListenRef.QueueSize *int`（unset→1、>0→取值、<=0 fail loud 中文报错）+ `robot.effectiveListenQueueSize(ref)` 纯函数（robot 包首个测试，5 子用例）+ `Connection.RegisterListen(routeKey, cb, queueSize) error`（唯一注册入口：预创建队列 cap=queueSize / 冲突 fail-loud（queueSize 或 cb 模式不一致）/ 幂等（一致）/ CAS 启动 listenLoop / nil receiver+已关闭返回 error）；删 `AddListener`/`ListenResponse`（源码零残留，仅 plans/docs 历史引用）；`NetSender.EnsureTCP/UDPListener` 加 `queueSize int` 形参，`netSenderAdapter` 透传+冲突记 Error 日志，Lua `ensure_tcp/udp_listener` 传 1（签名不变）；`robot.RegisterListen` 改造为分组逐条注册+错误聚合。TDD RED→GREEN，go build/vet/test 全绿（network 22 函数含 8 个新 RegisterListen 用例 / robot effectiveListenQueueSize 5 子用例 / engine 既有）。**review（controller / backend-review skill）**：无 🔴；🟡 `RegisterListen` 幂等分支 godoc「no-op」与实际「重写 cb」不符，已修为准确措辞；🔵（CAS 启动 loop 两处轻微重复、`defaultListenQueueSize` 在 network/robot 双包各一份）低风险不阻塞；复跑 go build/vet/test（含 script 包）全绿。**不变量**：queueSize=1（缺省）预创建队列容量 1 ≡ 2-A1 按需建容量 1，零回退。**遗留**（concerns）：engine/flow.go `FieldBind` 预存 gofmt 对齐漂移（HEAD 即有，非本次引入，gofmt diff 不涉及 ListenRef/QueueSize，按 Windows 环境注不顺手修）；effective 校验在运行时注册点（非加载点，engine 包无后端 flow 校验，前端校验归 T3）；`defaultListenQueueSize` 在 network/robot 两包各定义一份（值锁死 1）；-race 未跑（无 cgo，沿用 2-A1 结构性论证）。**2-A2.2 接力**：配置迁移（ranked/team/guild 高频 route 显式 queueSize）+ 删 `createListenCallback` 的 Script 分支 + 启用 `ListenDef.script` 禁用校验（破坏性，须原子提交）。文件：engine/flow.go(改)、engine/action.go(改)、network/connection.go(改)、network/connection_test.go(改)、robot/robot.go(改)、robot/register_listen_test.go(新)、script/api_network.go(改)、briefs/t2-a2-1-brief.md、reports/t2-a2-1-report.md。
- T2-A2.2: complete（**⚠️ 待运行时验证**）— 下线最后一个 listen 脚本回调，业务 LState 不再被 listenLoop 触碰（2-D 删锁的最后一处异步 Lua 清除）。用户定 **Option A（非阻塞 pop 保留追踪）**：①`script/api_network.go` 新增 `network.try_tcp/udp_listen(service, route)`（`networkTryListen`：单次非阻塞 pop，**不轮询/不 sleep/不持 luaMu**；空队列→code=31 且**不记 LastActionError**避免污染失败统计；有消息→原始 body 字符串，不解析 proto）+ 注册；②`conf/scripts/sync_frame_data.lua` 每轮**开头** `try_udp_listen` 取最新 ack 帧，解析 byte[13..16] 小端 uint32 写 `battleAck`（与旧回调逐字一致）；③`conf/flow/flow.json` `frameData` 去 `script`→`{}`（纯缓存 listen，queueSize=1 保最新）；④删 `conf/scripts/listen_frame_data.lua`（conf/ grep 零残留）；⑤`robot.go` 删 `createListenCallback` Script 分支（仅留 store 分支+nil）+ `validateListenDef(cbDef)` 纯函数对 `Script!=""` fail-loud（中文，`RegisterListen` 调用）。`RunCallbackScript` 删除归 2-D（本任务仅删调用点）。TDD：`robot/register_listen_test.go` `TestValidateListenDef` 4 子用例；try_* 无独立 Go 单测（script 包无 Lua harness，编译期+静态自洽+实战覆盖）。go build/vet/test 全绿（script/robot/engine/network）。**review（controller / backend-review skill）**：无 🔴；🟡 `sync_frame_data.lua` 双 `local code` 遮蔽（line 13 新 vs line 47 原）已修（改 ackCode/ackData）；🔵（try_* 无 Go 单测、`RunCallbackScript` 死代码待 2-D 删）不阻塞。**不变量**：battleAck pull 模型（queueSize=1 保最新）≡ 旧 push 回调的「最新值」语义；listenLoop 不再跑业务 Lua。**⚠️ 运行时验证待办**：战斗流程正确性无法单测覆盖——需 controller/用户按 CLAUDE.md 跑 `go run ./cmd/agent` 2~5 分钟确认 `BattleEnd≥2`、battleAck 非恒 0、战斗不卡、无 error/warn。**2-B 接力**：声明式心跳（tcpHeartbeat/udpHeartbeat，Go-only builder，下线 register_*_heartbeat 的 Lua builder + TryLock(luaMu)）。文件：script/api_network.go(改)、robot/robot.go(改)、robot/register_listen_test.go(改)、conf/flow/flow.json(改)、conf/scripts/sync_frame_data.lua(改)、conf/scripts/listen_frame_data.lua(删)、briefs/t2-a2-2-brief.md、reports/t2-a2-2-report.md。
