# T1.6 报告 — 迁移产物：各连接 codec.json + 共享 errors.json

> 状态：DONE
> 工作目录：worktree `worktree-declarative-codec` 根。**未 git commit。**

## 1. 产出文件（conf/adapter/，生产位置）

| 文件 | 用途 | 备注 |
|---|---|---|
| `conf/adapter/tcp_logic_codec.json` | `tcp:logic` 连接 codec | TCP，offset `{encode:0, decode:0}` |
| `conf/adapter/tcp_battle_codec.json` | `tcp:battle` 连接 codec | 与 tcp_logic **逐字节相同**（两份独立文件、内容一致，符合决策 #8「每连接一份」） |
| `conf/adapter/udp_battle_codec.json` | `udp:battle` 连接 codec | 仅 encrypt step `offset.encode` 改为 11（其余完全相同） |
| `conf/adapter/errors.json` | 共享错误码描述 | 667 条 code→中文描述，来自 error.lua 的 `errors` 表 |

未删除/修改 `conf/adapter/codec.lua`、`conf/adapter/error.lua`（T4 切换 commit 删除）。

## 2. codec.json 与 codec.lua 逐项核对

以 `conf/adapter/codec.lua` 为协议真值（T1.4/T1.5 已对拍证明），逐项核对生产 codec.json：

| 参数 | codec.lua 真值 | codec.json | 一致 |
|---|---|---|---|
| headerSize | `HEADER_SIZE = 12` | `frame.headerSize: 12` | ✓ |
| body 长度字段 | offset 0, uint32_le, `includes_header=false` | `bodyLen` offset 0 size 4 type u32 endian le role length | ✓ |
| errCode | offset 4, uint16_le（read_uint16_le data,4） | `errCode` offset 4 size 2 type u16 role errorCode | ✓ |
| cmd | offset 6, uint8 | `cmd` offset 6 size 1 type u8 role route | ✓ |
| act | offset 7, uint8 | `act` offset 7 size 1 type u8 role route | ✓ |
| index | offset 8, uint16_le，写 0（write_uint16_le(0)） | `index` offset 8 size 2 type u16 role value source const 0 | ✓ |
| flags | offset 10, uint8；FLAG_ENCRYPT=1(bit0), FLAG_COMPRESS=2(bit1) | `flags` offset 10 size 1 type u8 role flags bits=[{encrypted,0},{compressed,1}] | ✓ |
| bcc | offset 11, uint8（仅加密时设置） | `bcc` offset 11 size 1 type u8 role checksumOut from enc.bcc | ✓ |
| routeKey | `cmd .. ":" .. act`（math.floor） | `routeKeyTemplate: "{cmd}:{act}"` | ✓ |
| gzip 阈值 | `GZIP_THRESHOLD = 2048`，`#data >= 2048` 才尝试 | compress step `when.minBodyLen: 2048` | ✓ |
| gzip onlySmaller | `#compressed < #data` 才采用 | compress step `when.onlySmaller: true` | ✓ |
| 加密算法 | `crypto.encrypt_xor_carry_rol(data, key, offset, 3)` | encrypt step `algo: xor_carry_rol`, `params.rol: 3` | ✓ |
| keyLen | `#key ~= 32` 才加密 | encrypt step `keyLen: 32` | ✓ |
| 加密 guard | `if cmd ~= 0 and #data > 0 and secret_key and #secret_key == 32` | encrypt step `when: {requireKey:true, minBodyLen:1, guards:[{field:cmd, op:neq, value:0}]}` | ✓ |
| bcc region | `computeBcc(data[offset:])`（加密前明文 [offset:]，lua_crypto.go:160/227） | encrypt step `produces:[{name:bcc, algo:xor8, region:ciphered}]`（ciphered = body[encOffset:]） | ✓ |
| TCP enc/dec offset | encode_tcp offset 0；decode_tcp offset 0 | TCP 两份 `offset:{encode:0,decode:0}` | ✓ |
| UDP enc offset | `UDP_ENC_OFFSET = 11`（encode_udp） | udp_battle `offset.encode: 11` | ✓ |
| UDP dec offset | `decode_udp = decode_tcp`（offset 0，codec.lua:189） | udp_battle `offset.decode: 0` | ✓ |

**结论：所有协议参数与 codec.lua 完全一致，无任何差异。** brief 示例与 codec.lua 真值之间无冲突。

### 三份 codec 文件差异（已用 diff 验证）

- `tcp_logic_codec.json` ≡ `tcp_battle_codec.json`（逐字节相同）。
- `tcp_logic_codec.json` 与 `udp_battle_codec.json` **仅**第 20 行 `offset.encode` 不同（0 vs 11）。

### 与 T1.4/T1.5 对拍 fixture 的关系

`codec/testdata/tcp_logic_codec.json`（T1.4 字节级对拍 proven fixture）与生产 `conf/adapter/tcp_logic_codec.json` **语义完全相同**，仅 JSON 格式差异（fixture 用 pretty-print 的 `frame` 块，生产文件按 brief 用单行 `frame`）。`LoadSchema` 经 `json.Unmarshal` 解析后两者编译产物等价；`TestMigration_TCPLogic_ParityWithLuaAdapter` 直接对**生产文件**再次对拍 LuaAdapter，证明生产 codec.json 字节级一致。

## 3. errors.json

- **条目数：667**（与 `grep -cE '^\s*\[[0-9]+\]\s*=' conf/adapter/error.lua` 计数一致，不漏不重）。
- 格式：扁平 `{"<code字符串>": "<中文描述>"}`（总纲 §3.3）。
- 生成方式：一次性 Go 脚本（`.tmp_t16/gen_errors.go`，**已删除不入库**）解析 error.lua 的 `errors = { ... }` 块，正则提取 `[N] = "desc"`，Lua 字符串反转义，按 code 升序输出。
- 首条 `"0": "成功"`，末条 `"2016": "快捷消息重复装备"`，与 error.lua 首末一致。
- 脚本运行时做了重复 code 检测（duplicate → fail），无重复。

## 4. 自验结果（codec/migration_test.go）

新增 `codec/migration_test.go`（外部测试包 `codec_test`，与 engine_test.go 同包），5 个测试：

| 测试 | 验证内容 | 结果 |
|---|---|---|
| `TestMigration_AllCodecsCompile` | 3 份 codec.json 经 LoadSchema+Validate+NewSchemaCodec 全部成功；errors.json 可加载且非空 | PASS |
| `TestMigration_TCP_OffsetZero_TCPLogicAndBattleIdentical` | TCP 两份 EncodeTCP 字节相同；encOffset=0 表现为整 body 加密；decode 还原（routeKey/headerErr/body） | PASS |
| `TestMigration_UDP_EncOffset11_DecOffset0` | encOffset=11：前 11 明文保留、第 12 字节加密、bcc=xor8(body[11:])；decOffset=0：decode offset-0 加密帧（tcp_logic encode 模拟服务端回包）能还原 | PASS |
| `TestMigration_ErrorMap_Coverage` | 结构校验（key 合法、value 非空）+ 计数对齐（667==667）+ 跨区段抽样核对（0/27/256/700/707/1080/1800/2016） | PASS |
| `TestMigration_TCPLogic_ParityWithLuaAdapter` | 生产 tcp_logic_codec.json 对 6 个 case（加密/压缩/无 key/cmd=0/空 body）与 LuaAdapter oracle 字节级对拍 | PASS |

### encOffset/decOffset 断言方式说明

brief 要求「udp_battle encOffset 11/decOffset 0；tcp 两份 0/0」。`compiledStep.encOffset/decOffset` 为未导出字段，本任务约束「不改 codec/ 或 adapter/ 代码」，故未加导出访问器。改为**行为级**断言：
- encOffset=11 → EncodeUDP 输出 body 前 11 字节明文保留 + bcc=xor8(body[11:])；
- encOffset=0 → EncodeTCP 整 body 加密；
- decOffset=0 → decode offset-0 加密帧能还原。

这比读字段值更强，直接证字节级行为。

### UDP encode/decode 非对称语义（重要，已写入测试注释）

codec.lua:189 `decode_udp = decode_tcp`，decode 恒用 offset 0。UDP encode 用 offset 11（前 11 明文供服务端查密钥表）。流密码 keystream 位置不对齐 → **客户端 UDP encode 出的帧，客户端自己 decode 无法还原**（这是设计如此，服务端用专属 decode 路径）。故 UDP 不做 encode→decode 自环；decode 测试用 tcp_logic 的 offset-0 加密帧模拟「服务端回包形态」交 udp codec（decOffset=0）解码，验证 decOffset=0 行为正确。

## 5. 对拍结果（tcp_logic，生产文件）

`TestMigration_TCPLogic_ParityWithLuaAdapter`：6 个 case 全部字节级一致（加密 small/medium/compressible、无 key、cmd=0、空 body）。证明生产 `conf/adapter/tcp_logic_codec.json` 与 codec.lua 字节级等价。tcp_battle 内容与 tcp_logic 相同，udp_battle 仅 encOffset 不同（T1.4 已对 offset 11 做过对拍），故未重复对拍。

## 6. 验证命令

```
go build ./...              # 通过，无错误
go vet ./codec/...          # 通过，无警告
go test ./codec/... -count=1  # ok stressbot/codec 1.042s（含 T1.1-T1.5 全部测试 + T1.6 新增）
```

## 7. 改动文件

新增（未 git commit）：
- `conf/adapter/tcp_logic_codec.json`
- `conf/adapter/tcp_battle_codec.json`
- `conf/adapter/udp_battle_codec.json`
- `conf/adapter/errors.json`
- `codec/migration_test.go`

未修改任何 codec/ 或 adapter/ Go 代码。未删除 codec.lua / error.lua。

## 8. Self-Review（brief 验收清单）

- [x] 3 份 codec.json + 1 份 errors.json 在 `conf/adapter/`。
- [x] 每份 codec.json 经 LoadSchema+Validate+NewSchemaCodec 成功（无错误）。
- [x] udp_battle encOffset 11/decOffset 0；tcp 两份 0/0（行为级断言）。
- [x] errors.json 覆盖 error.lua 全部 code→desc（667==667，无漏无重）。
- [x] 字段布局与 codec.lua 一致（逐项核对，无差异）。
- [x] 未删 codec.lua/error.lua；未改 codec/adapter Go 代码；未 git commit。

## 9. Concerns

1. **LuaAdapter.DescribeError 在测试环境 nil-panic**：`callDescribeError` 失败时调 `utils/log.Error`，而 zap logger 在未 `InitLog` 的测试环境下为 nil → panic。这是 oracle 自身限制（与 errors.json 迁移无关），故 error 覆盖测试改用结构+计数+抽样核对，未走 oracle.DescribeError。T2/T4 若要在测试中用 LuaAdapter.DescribeError 需先初始化 logger（或在 adapter 层加 nil-guard）。**不阻塞本任务**。
2. **UDP encode/decode 非对称**：客户端 UDP encode(offset 11) 与 decode(offset 0) 无法自环（设计如此，服务端专属路径）。migration_test 已在注释中说明并用 tcp_logic offset-0 帧模拟服务端回包验证 decOffset=0。T2 connectionPump 落地时需注意：UDP 收到的服务端帧按 offset 0 解（codec.lua 现状），不要误用 encOffset。
3. **三份 codec 内容高度重复**：tcp_logic 与 tcp_battle 完全相同，udp_battle 仅差一行。决策 #8 明确「每连接一份、自解释、互不影响」，故保留三份独立文件；T4 CodecResolver 也允许显式去重（多连接指向同一文件）。若后续确认 tcp:logic 与 tcp:battle 永远同协议，可在 config 层显式指向同一份。
