# T1.4 报告 — encode 引擎（SchemaCodec 执行方法）+ 帧访问器

> 任务：在 T1.3 的 `*SchemaCodec` 上追加 encode 执行方法与只读帧访问器。
> 核心验收：**字节级对拍 `conf/adapter/codec.lua` 经旧 `adapter.LuaAdapter` 的 encode 输出**。
> 工作目录：worktree 根。未 git commit。

---

## 1. 实现内容

### 1.1 新增文件

- `codec/engine.go`（+424 行）：encode 管线 + 帧访问器 + 字节读写 helper。
- `codec/engine_test.go`（+440 行）：对拍测试（TDD RED→GREEN）+ 结构性断言 + 并发安全。

### 1.2 新增方法（同包方法，加在 `*SchemaCodec` 上）

| 方法 | 说明 |
|---|---|
| `BodyLength(header []byte) int` | 读 length 字段，按 lengthIncludesHeader/Trailer 口径反算 body 长；header 过短 → 0 |
| `ExpectedRouteKey(route any) string` | 按 routeKeySegs 拼；field 段取 route 字段值（math.Floor 取整，对齐 codec.lua），route==nil 取 0 |
| `EncodeTCP(route, body, key) []byte` | TCP encode（codec 单 transport；offset 固化在 encrypt step.encOffset） |
| `EncodeUDP(route, body, key) []byte` | UDP encode（同管线；两者并存仅为将来 adapter.Adapter 9 方法接口） |

> `HeaderSize()` 已在 compile.go 定义，未重复实现。

### 1.3 encode 管线（逐字节复刻 codec.lua `_do_encode`）

1. **route 解析**：`route any` → `map[string]any`；数值字段 `math.Floor` 后取整（对齐 codec.lua）。
2. **管线正序执行** `c.steps`，维护 `work []byte` / `flags uint64` / `applied []bool` / `stash map[stepIdx]map[produceName]uint64`：
   - **候选生效判定**：`step.encodeWhen.applies(len(work), key 非空, routeMap)`；若 `appliesWithIdx>=0` 还需 `applied[appliesWithIdx]==true`（串行依赖由本处在调用 applies 后自行串接）。
   - **compress**：候选生效 → 压缩；`onlySmaller` 时仅当 `len(compressed)<len(work)` 才采用（替换 work、置 flag、applied=true）；否则丢弃、applied=false、不置 flag。
   - **encrypt**：候选生效 + `requireKey` 时 `len(key)==32` → **先算 produces（明文 region）**，再 `Encrypt(work, key, encOffset, nil)`；置 flag、applied=true。
   - **checksum/hash（独立步）**：按 over region 计算；v1 现协议不用，实现正确。
3. **写头**：`make([]byte, headerSize)` **整体置零**；按字段写：length（wire body 长，按 includes* 调整）、route（floor 后整数）、errorCode=0、flags（累计命名位）、checksumOut（stash 产物，未执行写 0）、value（const/route）、reserved=0（零初始化已保证）。
4. **拼接**：`header ++ body (++ trailer)`。

---

## 2. 对拍真值如何取

在测试中构造旧 `adapter.LuaAdapter` 作为真值 oracle：

```go
a, _ := adapter.NewLuaAdapter(2, "<root>/conf/adapter/codec.lua", "<root>/conf/adapter/error.lua")
expected := a.EncodeTCP(route, body, key)  // 或 EncodeUDP
```

`<root>` 由 `findRepoRoot` 从测试 CWD（`codec/`）向上查找含 `conf/adapter/codec.lua` 的目录定位。pool=2（**非 1**——见 §6 concern 1）。oracle 仅在测试代码 import adapter / gopher-lua，**生产 codec 包零 gopher-lua**（已验证：`go list -deps ./codec` 无 gopher-lua；`go list -test -deps ./codec` 经 adapter 传递引入，可接受）。

---

## 3. 对拍覆盖组合与结果（全 PASS）

`go test ./codec/... -count=1` → `ok stressbot/codec 0.7s`，全部用例 PASS。

### 3.1 TCP 对拍（`TestEncodeTCP_Parity_LuaAdapter`，11 子用例）

| 子用例 | 组合 | 结果 |
|---|---|---|
| small_encrypted | cmd!=0 + key，body=64（<2048 不压缩） | PASS |
| medium_encrypted | cmd!=0 + key，body=1024 | PASS |
| large_compressible_encrypted | cmd!=0 + key，低熵 body=4096（压缩变小→置 compressed） | PASS |
| large_incompressible_encrypted | cmd!=0 + key，高熵 body=4096（压缩变大→丢弃） | PASS |
| small_no_key | cmd!=0 无 key（不加密） | PASS |
| cmd0_with_key | cmd=0 + key（guard neq 0 不满足→不加密） | PASS |
| cmd0_no_key | cmd=0 + 无 key | PASS |
| empty_body_encrypted | 空 body（#data==0→不压缩不加密） | PASS |
| empty_body_no_key | 空 body + 无 key | PASS |
| one_byte_encrypted | 单字节 body | PASS |
| nil_route | route=nil（cmd=act=0→不加密） | PASS |
| act0_encrypted | act=0 cmd!=0（guard 只看 cmd→仍加密） | PASS |

### 3.2 UDP 对拍（`TestEncodeUDP_Parity_LuaAdapter`，9 子用例，offset 11）

| 子用例 | 组合 | 结果 |
|---|---|---|
| udp_small_encrypted_offset11 | body=64（前 11 明文 + bcc 排除前缀） | PASS |
| udp_medium_encrypted_offset11 | body=256 | PASS |
| udp_compressible_encrypted_offset11 | 低熵 body=4096 压缩 | PASS |
| udp_no_key | 无 key | PASS |
| udp_cmd0_with_key | cmd=0 + key | PASS |
| udp_body_shorter_than_offset | body=8 < offset 11（处理区域空，flag 仍置位，bcc=0） | PASS |
| udp_empty_body | 空 body | PASS |
| udp_body_equals_offset | body=11（处理区域空） | PASS |
| udp_body_one_byte_after_offset | body=12（处理区域 1 字节） | PASS |

### 3.3 结构性断言（非对拍，全部 PASS）

| 用例 | 断言 |
|---|---|
| TestEncodeUDP_PlaintextPrefix_Preserved | UDP 前 11 字节明文 == body[:11]，第 12 字节被加密 |
| TestEncodeTCP_FullEncryption | TCP offset 0 整 body 被加密 |
| TestEncode_HeaderZeroInit_ChecksumNotExecuted | cmd=0 时 bcc/errorCode/index/flags 字节全 0 |
| TestEncode_Flags_EncryptedAndCompressed | 低熵大 body → bit0+bit1 都置位，bodyLen<orig |
| TestEncode_Flags_CompressionRejectedWhenLarger | 高熵大 body → 仅 encrypted 位，bodyLen==orig |
| TestBodyLength | 与 oracle BodyLength 在 6 个长度上逐一对齐 |
| TestBodyLength_RoundtripWithEncode | encode 后 BodyLength 切帧 == frame-12 |
| TestExpectedRouteKey | 5 组 route（含 nil/zero/large）与 oracle 对齐 |
| TestExpectedRouteKey_FloorAlignment | 非整数 float64 经 math.Floor 截断与 oracle 一致 |
| TestBCC_TCP_XorOverPlaintextRegion | TCP bcc == xor8(plaintext body) |
| TestBCC_UDP_ExcludesPlaintextPrefix | UDP bcc == xor8(plaintext[11:]) |
| TestBCC_ParityWithOracle | bcc 字节与 oracle 一致 |
| TestHeaderSize | ==12（T1.3 回归） |
| TestEncode_ConcurrentSafe | 8 goroutine × 50 次 encode 无 panic/deadlock |

---

## 4. TDD RED / GREEN 证据

- **RED**：先写 `engine_test.go`，`go vet ./codec/...` 报 `ut.EncodeTCP undefined (type *SchemaCodec has no field or method EncodeTCP)` —— 测试驱动出待实现 API。
- **GREEN 迭代 1**：实现 engine.go，首次测试暴露 **bcc region 语义错误**（见 §5 修正 1）+ **测试 body 生成器可压缩性假设错误**（见 §5 修正 2）。
- **GREEN 迭代 2**：修正后全绿。`go build ./codec/...` / `go vet ./codec/...` / `go test ./codec/... -count=1` 全部干净。

---

## 5. 关键修正（实现中发现）

### 修正 1：bcc = xor8(**明文** body[encOffset:])，非「加密后」

brief §3.3 措辞「ciphered = 加密后的 body 的 [encOffset:]」与 codec.lua 实际行为冲突。核对 `adapter/lua_crypto.go:227`：

```go
bcc := computeBcc(data[offset:])   // data 是加密**前**的明文（line 204 读入，line 230 才 encrypt）
encryptFn(enc[offset:], key, bits) // 加密 enc（copy of data），不影响已算出的 bcc
```

注释 `lua_crypto.go:116-117` 明确：「加密前对明文部分调用一次，结果写入协议头的 bcc 字段」。

**以对拍为准**（brief 核心验收）：在 engine.go 中 `stashStep` 调用移到 `ciph.Encrypt` **之前**，对加密前的 `work[encOffset:]` 计算。RED 时 `one_byte_encrypted` 用例直接暴露：plaintext=`[0x42]`，oracle bcc=`0x42`，错误实现（ciphered=`0x1a`）bcc=`0x1a`。

### 修正 2：测试 body 生成器需高熵

初版 `genBody` 用 `seed*31 + i*7` LCG，gzip 实际可压缩（4096→595），导致 `TestEncode_Flags_CompressionRejectedWhenLarger` 假设失败。改用 xorshift32 → 高熵，gzip 压缩后变大 → onlySmaller 正确丢弃压缩结果。这暴露的是**测试假设**错误，非实现错误（oracle 与实现均压缩了 LCG body，字节一致）。

### 修正 3：Hasher/Checksum 接口签名

`Hasher.Hash` 与 `Checksum.Sum` 实际签名为 `(data, key, params)` / `(data, params)`（三参/两参），实现时按真实签名调用（`nil` params）。

---

## 6. Concerns

### Concern 1（oracle 限制，非本任务缺陷）：`adapter.NewLuaAdapter` poolSize=1 时 acquire 超时

poolSize=1 时 `NewLuaAdapter` 在 error.lua 加载循环第一次 `acquire()` 即 30s 超时（cacheMetaInfo 已正常释放单槽）。poolSize=2 正常。疑似单元素 channel 在 release→acquire 紧邻时的 select 竞态，与 codec 包无关。**测试固定用 pool=2 规避**。建议 T2/T4 切换到 SchemaAdapter 后另开任务修 LuaAdapter（或直接删除）。

### Concern 2（T1.3 遗留，本任务禁止改 compile.go）：`compiledStep` 未存 `Params` 与 `KeyLen`

- brief 假设 `step.Params` / `step.KeyLen` 存在，但 T1.3 的 `compiledStep` 未存这两个字段。
- **Params**：cipher `Encrypt(data, key, offset, params)` 的 params 传 `nil`。现协议 xor_carry_rol 的 cipher 默认 `rol=3`（ciphers.go:257），与 schema `params.rol=3` 一致 → 对拍正确。**风险**：未来 schema 用非默认 rol 时会静默用默认值。建议 T1.5（或单独补丁）在 compiledStep 加 `params map[string]any` 字段。
- **KeyLen**：`keyLenSatisfied` 当前硬编码 `len(key)==32`（codec.lua 语义）。与 schema `keyLen:32` 一致 → 对拍正确。**风险**：未来 keyLen 非 32 的协议会错。建议同样补 compiledStep.keyLen 字段。

这两个修正被「禁止改 compile.go」约束阻塞，已在 engine.go 注释明确标注，留 T1.5 处理。

### Concern 3：race 检测未跑

环境无 gcc（`cgo: C compiler "gcc" not found`），`-race` 不可用。并发安全论证靠结构性保证：`*SchemaCodec` 构造后无可变字段（compile.go 已注释「无任何可变字段」），`encode` 仅用局部变量 + 只读 codec 字段。`TestEncode_ConcurrentSafe`（8 goroutine × 50 次）验证无 panic/deadlock，但无法替代 race 检测。建议在有 gcc 的 CI 环境补跑 `-race`。

### Concern 4（v1 边界，符合 brief）：独立 checksum/hash 步的 `over` 未完整编译

`compiledStep` 未存 `OverSpec`，`regionForOver` 保守返回 work。v1 现协议不用独立 checksum/hash 步（bcc 走 encrypt 的 produces），不影响对拍。T1.5 若需支持全帧 CRC 类协议需补 over 编译。

---

## 7. Self-Review（对照 brief 验收清单）

| 验收项 | 状态 | 证据 |
|---|---|---|
| encode 对拍 codec.lua 字节级一致（TCP/UDP、加密/不加密、压缩/不压缩、空 body、cmd=0） | PASS | §3.1/§3.2 共 20 子用例 |
| bcc = xor8(body[encOffset:])，UDP 排除前 11 明文字节 | PASS | §5 修正 1；TestBCC_TCP/UDP |
| 头部零初始化；checksumOut 未执行步写 0 | PASS | TestEncode_HeaderZeroInit |
| BodyLength/ExpectedRouteKey 与 helpers/lua_adapter 行为一致 | PASS | TestBodyLength / TestExpectedRouteKey |
| 未实现 decode / 未加 adapter 包装 | PASS | 仅 encode + accessors |
| codec 不 import gopher-lua（生产） | PASS | `go list -deps ./codec` 无 gopher-lua |
| 未改 T1.1/T1.2/T1.3 文件 | PASS | 仅新增 engine.go + engine_test.go |
| `go build` / `go vet` / `go test` 全绿 | PASS | §1 |
| 未 git commit | PASS | — |

---

## 8. 改动文件

| 文件 | 类型 | 行数 |
|---|---|---|
| `codec/engine.go` | 新增 | ~424 |
| `codec/engine_test.go` | 新增 | ~440 |

未修改任何既有文件。
