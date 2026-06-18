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

pending — 待 T1 冻结 loader 签名。

### T2 — 后端集成 + 删锁（T4 后）

pending — 2-A → 2-B → 2-C → 2-D。

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
