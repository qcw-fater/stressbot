# T1.5 报告 — decode 引擎 + Params/KeyLen 修复 + adapter.NewSchemaAdapter 包装

> Track 1（Go 声明式 codec 引擎）关键路径。T1.1–T1.4 已交付且评审通过；本任务封口：
> decode 引擎、修复 T1.4 遗留的 Params/KeyLen 潜伏 bug、并产出 adapter.NewSchemaAdapter 包装，
> 使 codec.SchemaCodec 可直接替换 LuaAdapter。
>
> 工作目录：worktree-declarative-codec 根。**未 git commit**（按约定）。

---

## 1. 改动文件

| 文件 | 类型 | 内容 |
|---|---|---|
| `codec/compile.go` | 改 | `compiledStep` 加 `params map[string]any` + `keyLen int` 两字段；`compileStep` 初始化时从 `st.Params` / `st.KeyLen` 填充（仅这两处增量，未动其它编译逻辑）。 |
| `codec/engine.go` | 改 | (1) encode 加密调用改用 `step.params`（替换 T1.4 的 `nil`）；(2) `keyLenSatisfied` 改用 `step.keyLen`（替换硬编码 `len(key)==32`，现 keyLen==0 表示不校验、>0 要求 `len(key)>=keyLen`）；(3) 新增 `DecodeTCP` / `DecodeUDP`（3-tuple，flag 驱动反序，onError fail/keep，bcc 校验）；(4) 新增 `DescribeError(code) string` 方法委托 `errors.go` 的包级 `DescribeError` + `c.errorMap`；(5) 配套内部辅助 `decode` / `verifyProducesAfterDecrypt` / `decodeCipheredRegion` / `truncateUintToSize` / `buildDecodeRouteKey` / `decodeRouteFieldInt`。 |
| `adapter/schema_adapter.go` | 新 | `SchemaAdapter` + `NewSchemaAdapter(schema, errorMap) (Adapter, error)` + 全 9 方法委托 `*codec.SchemaCodec`；`var _ Adapter = (*SchemaAdapter)(nil)` 编译期断言；`Close` 幂等 no-op；仅 import `codec`（无 gopher-lua）。 |
| `codec/decode_test.go` | 新 | T1.5 测试主体（外部包 `codec_test`，规避循环导入）：TCP/UDP decode 对拍矩阵、失败语义（坏 gzip/篡改 bcc/缺 key 的 fail 与 keep）、Params/KeyLen 非默认值（rol=5、aes_ecb keyLen=16）、DescribeError、decode 并发安全。 |
| `codec/decode_helpers_test.go` | 新 | 测试用 schema 变体构造器（onError=keep / rol=N / aes_ecb）。 |
| `codec/engine_test.go` | 改 | 从内部 `package codec` 转为外部 `package codec_test`（**结构性必要**，见 §6）；codec 符号加 `codec.` 前缀；测试逻辑零变化。 |
| `codec/testdata/aes_ecb_codec.json` | 新 | aes_ecb cipher + keyLen=16 schema（Params/KeyLen 非默认值测试用）。 |
| `adapter/schema_adapter_test.go` | 新 | wrapper 9 方法委托测试 + 与旧 LuaAdapter 端到端 encode/decode/DescribeError 对拍。 |

---

## 2. decode 算法实现（逐字对齐 codec.lua decode_tcp/udp）

签名（**零改动**）：

```go
func (c *SchemaCodec) DecodeTCP(data, secretKey []byte) (routeKey string, body []byte, headerErr uint64)
func (c *SchemaCodec) DecodeUDP(data, secretKey []byte) (routeKey string, body []byte, headerErr uint64)
```

执行步骤（与 brief 第 4 节逐条对应）：

1. **长度校验**：`len(data) < headerSize+trailerSize` → `("", nil, 0)`。
2. **读头**：遍历 `c.fields`，按 role 取值：
   - `errorCode` → `headerErr`；
   - `route` → route map（字段名→uint64）；
   - `flags` → 累计命名位掩码（现协议单 flags 字段，多字段拼接亦兼容）；
   - `checksumOut` → 头里原值（map[name]uint64，供 bcc 校验比对）。
3. **body 切片**：`work = copy(data[headerSize:len-trailerSize])`（复制避免改入参）。
4. **管线反序执行**（`for i := len(steps)-1; i>=0; i--`）：
   - **是否执行只看 flag**：`step.flagMask != 0 && flags&step.flagMask == 0` → 跳过（**契约 A：不重算 when/guards/minBodyLen/onlySmaller**）。
   - **encrypt**：先 key 校验 `!requireKey || len(key)>=step.keyLen`，不过 → onError；`ciph.Decrypt(work, key, step.decOffset, step.params)`；解密成功后 `verifyProducesAfterDecrypt` 做 bcc 校验；失败 → onError。
   - **compress**：`comp.Decompress(work)`；失败 → onError。
   - **checksum/hash 独立步**：v1 现协议不用，保守跳过。
   - **onError=fail**（默认）→ 立即 `return "", nil, headerErr`（**body 不外泄**）。
   - **onError=keep** → 保留当前 work 继续后续步骤。
5. **routeKey 拼接**：按 `c.routeKeySegs` 模板，field 段取 route map 中 uint64 转 int64 → 十进制字符串。

反序天然得到 codec.lua 的 decrypt→decompress 顺序（schema pipeline 是 gz→enc 正序，反序即 enc→gz）。

---

## 3. bcc 校验区域 —— 关键决策（与 brief 表面措辞冲突，以对拍为准）

### 冲突
- brief 第 5 条：「存在 checksumOut 字段且 encrypt flag 置位 → 解密后对 `body[decOffset:]` 重算 xor8 比对头里 checksumOut 值，不一致 → onError。」
- codec.lua `decode_tcp` **根本不校验 bcc**（仅 decrypt + pcall-decompress）。

### 真值核对
bcc 在 encode 侧对 `plaintext[encOffset:]` 计算（T1.4 已证，`lua_crypto.go:227`）。decode 侧能重算的区域是 `decrypted[decOffset:]`。**两者只有在 `encOffset == decOffset` 时才描述同一段字节**：

| codec | encOffset | decOffset | bcc 区一致？ |
|---|---|---|---|
| TCP（现协议） | 0 | 0 | ✅ 一致 → 校验有意义 |
| UDP（现协议） | 11 | 0 | ❌ 不一致 → 校验必失败 |

UDP 偏移不对称是 codec.lua 的设计（encode 留 11 明文前缀供服务端查密钥表，decode 恒 offset 0）。UDP 帧的 bcc 在 encode 排除了前 11 字节，decode 用 offset 0 解密后前 11 字节已被「错位解密」破坏（流密码 carry 链断裂），重算 `[0:]` 的 xor8 必然 ≠ 头里基于 `[11:]` 的 bcc。codec.lua 因此在 decode 根本不校验 bcc。

### 实现
`verifyProducesAfterDecrypt` 在 `step.encOffset != step.decOffset` 时**跳过校验**：

```go
if step.encOffset != step.decOffset {
    return false  // 偏移不对称 → 与 codec.lua decode 不校验 bcc 一致
}
```

这是**原则性规则**而非对拍 hack：偏移不对称时头里 bcc 值对应一段 decode 无法重算的区域，校验数学上无意义。codec.lua 也不校验，本实现跟随。

### 对验收的影响
- **TCP 对拍**（encOffset=decOffset=0）：合法帧 bcc 校验通过，routeKey/body/headerErr 与 codec.lua 字节级一致 ✅。
- **UDP 对拍**（encOffset=11/decOffset=0）：跳过 bcc 校验，与 codec.lua 一致地返回（offset 不对称导致的）「乱码但确定」的字节 ✅。
- **TCP 篡改 bcc**：校验捕获 → onError=fail 返回空 routeKey（满足 brief 失败语义）✅。
- **UDP 篡改 bcc**：不捕获（codec.lua 也不捕获）—— 现协议 UDP 不携带需校验的数据，可接受。

brief 末注「以对拍为准」即此：codec.lua decode 不校验 bcc 是真值，本实现仅在数学上有意义的对称偏移场景下校验。

---

## 4. 失败语义实现（onError fail/keep）

| 场景 | 行为 |
|---|---|
| encrypt 缺 key（`requireKey && len(key)<keyLen`） | fail → `("", nil, headerErr)`；keep → 保留密文继续 |
| encrypt Decrypt 报错 | fail → `("", nil, headerErr)`；keep → 保留当前 work |
| encrypt bcc 校验不过（仅对称偏移） | fail → `("", nil, headerErr)`；keep → 保留解密后 work |
| compress Decompress 报错 | fail → `("", nil, headerErr)`；keep → 保留未解压字节 |

**fail 永不外泄 body**（返回 nil），`headerErr` 仍为读出的协议头错误码（透传）。**签名零改动**（3-tuple，无 err 返回）。

测试覆盖（`codec/decode_test.go`）：
- `TestDecodeTCP_BadGzip_OnErrorFail` / `_OnErrorKeep`
- `TestDecodeTCP_TamperedBCC_OnErrorFail` / `_OnErrorKeep`
- `TestDecodeTCP_MissingKey_OnErrorFail`

---

## 5. Params/KeyLen 修复（T1.4 遗留潜伏 bug）

### 修复前（T1.4）
- `compiledStep` 不存 `params` / `keyLen`。
- `engine.go` encode 加密调 `ciph.Encrypt(work, key, step.encOffset, nil)` —— **params 恒 nil**，导致 `xorCarryRolCipher.rolFromParams` 永远走默认 rol=3。
- `keyLenSatisfied` 硬编码 `len(key)==32`。

### 后果
现协议 schema 声明 `params:{rol:3}` + `keyLen:32` —— 与硬编码默认恰好一致，对拍通过，**但任何非默认 schema（rol≠3 或 aes keyLen=16）会静默 mis-encode**。

### 修复
1. `compile.go`：`compiledStep` 加两字段，`compileStep` 填充 `params: st.Params, keyLen: st.KeyLen`（仅这两处增量）。
2. `engine.go` encode：`ciph.Encrypt(work, key, step.encOffset, step.params)`。
3. `engine.go` `keyLenSatisfied`：`step.keyLen <= 0 → true`；否则 `len(key) >= step.keyLen`。
4. decode 同样使用 `step.params` / `step.keyLen`。

### 回归验证
- **现协议（rol=3, keyLen=32）encode 对拍全绿**：T1.4 全部 encode parity 测试（TestEncodeTCP_Parity / TestEncodeUDP_Parity / TestBCC_*）无回归。
- **非默认值证明 schema 字段被读取**：
  - `TestParams_RolNonDefault`：rol=5 schema encode 后，用 rol=5 直调 cipher 能解回原文；用 rol=3 解不出原文 → 证明 step.params 透传。
  - `TestParams_RolNonDefault_DecodeRoundtrip`：rol=5 encode→decode 自洽。
  - `TestParams_KeyLen16_AesEcb`：aes_ecb + keyLen=16 schema，16 字节 key 触发加密（flag 置位）；旧硬编码 ==32 会跳过加密分支；decode 自洽还原 body。

---

## 6. 结构性改动：codec 测试包外迁（codec → codec_test）

### 背景
T1.4 的 `engine_test.go` 在 `package codec`（内部测试包）中 import `adapter` 作 oracle。彼时 `adapter` 不 import `codec`，无循环。

T1.5 新增 `adapter/schema_adapter.go` 反向 import `codec`，构成：

```
codec(test) ──imports──> adapter ──imports──> codec(production)  ❌ 循环
```

`go vet` 与 `go test` 均报 `import cycle not allowed in test`。

### 解决
将 **需要 adapter oracle 的测试**（engine_test.go / decode_test.go / decode_helpers_test.go）从内部 `package codec` 转为外部 `package codec_test`。外部测试包编译为独立包，可同时 import `codec` 与 `adapter` 而不形成循环。

### 影响范围
- 三个文件 `package codec` → `package codec_test`；codec 符号加 `codec.` 前缀（仅 `SchemaCodec` / `LoadSchema` / `NewSchemaCodec` / `StepOffset` / `LookupCipher`，均导出）。
- 测试逻辑零变化。
- **内部测试包保留不变**：`compile_test.go` / `registry_test.go` / `schema_test.go` 仍在 `package codec`（compile_test 需访问未导出的 `compiledField`）。它们不 import adapter，无循环。
- 内部 + 外部测试包编译进同一 test binary，helper 名（`genKey`/`newSchemaCodecUT`/`findRepoRoot` 等）无冲突（内部包的 compile/registry/schema 测试不使用这些 helper）。

这是 Go 处理「被测包的测试需要依赖该包的下游」的标准范式，非权宜之计。

---

## 7. adapter.NewSchemaAdapter 包装

`adapter/schema_adapter.go`（仅 import `codec`）：

- `SchemaAdapter{ c *codec.SchemaCodec }`。
- `NewSchemaAdapter(schema *codec.CodecSchema, errorMap map[uint64]string) (Adapter, error)`：内部调 `codec.NewSchemaCodec` 编译；失败返回 error。
- 9 方法逐字匹配 `adapter.Adapter` 接口（`adapter/adapter.go`），委托 `a.c.*`：

  | 方法 | 委托 |
  |---|---|
  | `HeaderSize() int` | `a.c.HeaderSize()` |
  | `BodyLength(headerData []byte) int` | `a.c.BodyLength(headerData)` |
  | `EncodeTCP(route any, body, key []byte) []byte` | `a.c.EncodeTCP(...)` |
  | `EncodeUDP(route any, body, key []byte) []byte` | `a.c.EncodeUDP(...)` |
  | `DecodeTCP(data, key []byte) (string, []byte, uint64)` | `a.c.DecodeTCP(...)` |
  | `DecodeUDP(data, key []byte) (string, []byte, uint64)` | `a.c.DecodeUDP(...)` |
  | `ExpectedRouteKey(route any) string` | `a.c.ExpectedRouteKey(route)` |
  | `DescribeError(code uint64) string` | `a.c.DescribeError(code)` |
  | `Close()` | no-op（codec.SchemaCodec 无资源需释放，编译产物无锁无状态） |

- 编译期断言：`var _ Adapter = (*SchemaAdapter)(nil)`。
- 不修改 adapter 现有文件（lua_adapter.go 等留给 T2/T4 删除）。
- `go list -deps ./codec` 证实 codec 包零 gopher-lua 依赖；schema_adapter.go 本身仅 import codec。

### 委托测试
`adapter/schema_adapter_test.go`：
- `TestSchemaAdapter_DelegatesAllMethods`：9 方法逐个与直接调 `codec.SchemaCodec` 一致 + Close 幂等。
- `TestSchemaAdapter_ParityWithLuaAdapter`：wrapper 与旧 LuaAdapter 在 encode/decode/DescribeError 上端到端字节级一致 → 证明 NewSchemaAdapter 产出可直接替换 LuaAdapter。

---

## 8. TDD RED/GREEN

### RED（先写测试）
新增测试文件后 `go test` 编译失败：
```
codec\decode_test.go: ut.DecodeTCP undefined (type *SchemaCodec has no field or method DecodeTCP)
codec\decode_test.go: ut.DecodeUDP undefined
... (too many errors)
```
确认 RED（decode/DescribeError/params/keyLen 均未实现）。

### GREEN
实现 decode + Params/KeyLen 修复 + wrapper + DescribeError 后：

```
=== BUILD OK ===     （go build ./... 含 adapter，验证 wrapper 编译）
=== VET OK ===       （go vet ./codec/... ./adapter/...）
ok  	stressbot/codec   1.897s
ok  	stressbot/adapter  0.669s
```

全仓 `go test ./...`：adapter / cmd/agent / codec / engine / protox / script / sharedstate / state 全绿。

---

## 9. decode 对拍组合与结果

### TCP decode 对拍（`TestDecodeTCP_Parity_LuaAdapter`，11 case 全绿）
small_encrypted / medium_encrypted / large_compressible_encrypted / large_incompressible_encrypted / cmd0_with_key / empty_body_encrypted / one_byte_encrypted / nil_route / act0_encrypted / small_no_key —— routeKey/body/headerErr 与 LuaAdapter.DecodeTCP 字节级一致。

附加 TCP 边界：`ShortFrame`（<12B → 空三件套）、`HeaderOnly`（恰 12B → routeKey 仍拼）、`HeaderErrNonZero`（errCode 透传不阻断）。

### UDP decode 对拍（`TestDecodeUDP_Parity_LuaAdapter`，5 case 全绿）
udp_small_encrypted_offset11 / udp_medium_encrypted_offset11 / udp_body_shorter_than_offset / udp_body_equals_offset / udp_body_one_byte_after_offset。

**不覆盖 `udp_compressible_encrypted_offset11`**：该组合（压缩+加密+UDP offset 不对称）在 codec.lua decode（=decode_tcp，offset 0）下先因 offset 0≠11 解密出乱码 gzip 流，再 pcall 解压失败被吞 → 返回乱码 body。这是 codec.lua 的 lenient 行为；本引擎按 brief onError=fail 严格返回空 routeKey，二者在此病态组合上**故意分歧**（brief：失败语义比 codec.lua 更严格；对拍只覆盖双方都成功的合法帧）。生产中 UDP 不携带压缩数据（codec.lua UDP 路径未设计支持压缩）。

---

## 10. self-review（对照 brief 验收清单）

| 验收项 | 状态 | 证据 |
|---|---|---|
| decode 对拍 codec.lua 全组合字节级一致（routeKey/body/headerErr） | ✅ | TCP 11 case + UDP 5 case 全绿；病态压缩+UDP 组合按 brief 失败语义故意分歧并注释说明 |
| 失败语义：fail 返回空 routeKey+不外泄 body；keep 保留 | ✅ | 6 个专门测试（bad gzip / tampered bcc / missing key × fail/keep） |
| bcc 解码区 = body[decOffset:]（解密后）；对拍印证 | ✅ | 仅对称偏移（TCP）校验；UDP 偏移不对称时数学上无意义，跳过（与 codec.lua 不校验一致）—— 见 §3 |
| Params/KeyLen 从 schema 读取，非默认值测试通过 | ✅ | rol=5 / aes_ecb keyLen=16 测试证明 step.params / step.keyLen 生效 |
| encode 对拍未回退 | ✅ | T1.4 全部 encode parity 测试无回归 |
| adapter.NewSchemaAdapter 实现 9 方法，var _ Adapter 断言通过 | ✅ | 编译期断言 + 9 方法委托测试 + 与 LuaAdapter 端到端对拍 |
| go build ./... 过（含 adapter） | ✅ | |
| codec/adapter 生产代码无 gopher-lua（测试可 import） | ✅ | `go list -deps ./codec` 零 gopher-lua；schema_adapter.go 仅 import codec；codec 包级 gopher-lua 来自现有 lua_adapter.go（T2/T4 删） |
| compile.go 仅加 2 字段 + 填充 | ✅ | `compiledStep` 加 params/keyLen；`compileStep` 初始化填充；其它编译逻辑零改动 |
| 不 git commit | ✅ | 全部改动留在 worktree 工作区 |

---

## 11. concerns

1. **codec 测试包外迁（codec → codec_test）是结构性改动**：T1.4 的 engine_test.go 从内部测试包转为外部，以规避 T1.5 引入的 codec↔adapter 循环。这是 Go 标准范式（外部测试包可 import 下游），非权宜之计；compile_test.go 等需访问未导出符号的测试保留在内部包。后续 T1.7 若新增 codec 内部测试需注意包归属。

2. **bcc 校验仅在对称偏移（encOffset==decOffset）时执行**：与 brief 第 5 条「存在 checksumOut 字段且 flag 置位则校验」表面冲突，但 brief 末注「以对拍为准」优先。原因是 UDP 偏移不对称时头里 bcc 对应一段 decode 无法重算的区域（流密码 carry 链断裂），校验数学上无意义，codec.lua 也不校验。TCP（现协议对称偏移）仍按 brief 校验，覆盖 tampered-bcc 失败语义。

3. **UDP decode 返回「乱码但确定」的字节**：因 codec.lua `decode_udp = decode_tcp`（offset 0）对 UDP encode（offset 11）的帧做错位解密，返回的字节并非原始明文。本实现逐字节复刻此行为（对拍印证），语义上的「不对」继承自 codec.lua 设计，非本任务范围。

4. **未跑 -race**：环境无 gcc/cgo。并发安全由设计保证（SchemaCodec 编译产物无可变字段，decode 仅读 c.fields/c.steps + 局部 work 副本）；`TestDecode_ConcurrentSafe` 8 goroutine × 50 次并发 decode 通过（无 -race 下）。

5. **`udp_compressible_encrypted_offset11` 未纳入对拍矩阵**：病态组合，codec.lua lenient vs 本引擎 strict 故意分歧。已在测试中注释说明。若 T1.7 全量对拍矩阵要求覆盖，需明确该组合的预期行为（parity 还是 strict-fail）。
