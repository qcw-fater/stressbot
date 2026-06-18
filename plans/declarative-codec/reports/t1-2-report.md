# T1.2 报告 — 算法注册表 + 迁移算法 + 元数据导出

Status: **DONE_WITH_CONCERNS**（一处块密码语义边界需 T1.4 知晓，详见 §6）

## 1. 实现内容

在 T1.1 建好的 `codec/` 包内新增 6 个文件（**未改** `schema.go` / `errors.go` / `schema_test.go`）：

| 文件 | 内容 |
|---|---|
| `codec/registry.go` | 4 个接口（`Cipher`/`Compressor`/`Checksum`/`Hasher`，与 brief 逐字）+ 4 张注册表（map+RWMutex）+ `Register*`/`Lookup*` + 元数据类型（`AlgoMeta`/`AlgoParam`）+ `Algorithms()` |
| `codec/ciphers.go` | cipher 实现 + init 注册 |
| `codec/compressors.go` | compressor 实现 + init 注册 |
| `codec/checksums.go` | checksum 实现 + init 注册 |
| `codec/hashes.go` | hasher 实现 + init 注册 |
| `codec/registry_test.go` | 30+ 测试（TDD RED→GREEN） |

## 2. 迁移算法清单（来源行号 → 注册名）

### cipher（8 个）
| 注册名 | 来源 | 说明 |
|---|---|---|
| `none` | 新增（直通） | 前 offset 字节明文 + `data[offset:]` 原样返回，`len(out)==len(data)` |
| `xor` | `lua_crypto.go:279` `encryptXor` | 循环 key XOR，自反 |
| `xor_carry_rol` | `lua_crypto.go:846` `EncryptXorCarryRol` / `:864` `DecryptXorCarryRol`（核心 `:339`/`:361`） | 现协议 NetEncrypt；`params.rol` 缺省 **3**，范围 [1,7] 钳位；key 非 32 字节返回原数据 |
| `rc4` | `lua_crypto.go:408` `applyRC4` | 自反，key 1~256 |
| `aes_ecb` | `lua_crypto.go:496`/`:521` + `:465` `pkcs7Pad`/`:476` `pkcs7Unpad` | PKCS#7 自动填充 |
| `aes_cbc` | `lua_crypto.go:558`/`:582` | 需 IV（`params.iv`，bytes 或 string） |
| `aes_ctr` | **本任务新增**（lua_crypto 无 CTR） | stdlib `crypto/cipher.NewCTR`；流模式，`len(out)==len(data)` |
| `xxtea` | `lua_crypto.go:628`/`:652` + `:699` 自动 4 对齐 | key 16 字节 |

### compress（2 个）
| 注册名 | 来源 |
|---|---|
| `none` | 新增（直通） |
| `gzip` | `lua_zlib.go:25`（WriterPool）/`:31`（Compress）/`:59`（ReaderPool）/`:61`（Decompress）；本层无阈值（阈值由 engine `when` 决定） |

### checksum（6 个）
| 注册名 | 来源 | 已知向量 |
|---|---|---|
| `none` | 新增（恒 0） | — |
| `xor8` | `lua_crypto.go:118` `computeBcc`（逐字） | `xor8(0x55,0xAA)=0xFF`（对拍 `TestBcc`） |
| `sum8` | 通用补齐（lua_crypto 无） | `sum8(1..10)=55` |
| `crc16` | `lua_crypto.go:748` `crc16CCITT` | `crc16("123456789")=0x29B1`（对拍 `TestCrc16`） |
| `crc32` | `lua_crypto.go:774` → stdlib `crc32.ChecksumIEEE` | `crc32("123456789")=0xCBF43926`（对拍 `TestCrc32`） |
| `crc32c` | stdlib `crc32.MakeTable(crc32.Castagnoli)` | `crc32c("123456789")=0xE3069283`（标准 check 值） |

### hash（3 个 + HMAC）
| 注册名 | 来源 | 输出 |
|---|---|---|
| `md5` | `lua_crypto.go:790` + HMAC `:818` | 原始 16 字节摘要（非 hex） |
| `sha1` | `lua_crypto.go:798` + HMAC 补齐（lua_crypto 只有 md5/sha256 的 HMAC 入口） | 原始 20 字节 |
| `sha256` | `lua_crypto.go:806` + HMAC `:828` | 原始 32 字节 |

> **Hasher.Hash 返回原始摘要字节**（非 hex），比 lua_crypto 的 Lua 入口（返回 hex 字符串）更底层通用；T1.4 engine 在写入 bytes 字段时可按需 hex/base64 编码。已在代码注释中说明。

## 3. offset 语义实现

`clampOffset(offset, len)` 把 offset 钳到 `[0, len(data)]`（负→0，越界→len）。所有 cipher 实现统一模式：

```go
out := make([]byte, len(data))   // 流密码：len 守恒
copy(out, data)                  // 前缀原样复制
off := clampOffset(offset, len(data))
coreTransform(out[off:], key, ...) // 仅变换 data[off:]
```

`xor_carry_rol` 的核心变换 `encryptXorCarryRol`/`decryptXorCarryRol` **逐字复制自 lua_crypto.go:339/361，未改一字**；offset 适配仅在外层（`out[off:]` 切片），核心循环不变。已在 `TestXorCarryRolOffsetPrefix` 验证 offset=11 前缀明文字节不变 + `len(out)==len(data)`。

## 4. 测试与结果（TDD）

**RED→GREEN 证据**：先写全部测试 → 首次编译失败（map 字面量缺 key / AlgoMeta 含 slice 不可比较 / `Hash` 调用漏第三参）→ 修正测试自身语法 → GREEN 一次通过（算法实现本身首次即通过，因逐字迁移）。算法逻辑层面无 RED 阶段——迁移来源已有 `lua_crypto_test.go` 覆盖，本任务测试直接断言已知向量。

```
go test ./codec/... -count=1
ok  stressbot/codec  0.467s
```

30 个测试全部 PASS，覆盖：
- `Algorithms()` 完整性（19 个算法）+ 顺序稳定（cipher→compress→checksum→hash，组内字母序）+ 两次调用结果一致
- `Lookup*` 命中/未命中
- cipher 流密码（`none`/`xor`/`xor_carry_rol`/`rc4`/`aes_ctr`）：offset∈{0,1,11,64,100} 往返 + 前缀明文断言 + `len(out)==len(data)`
- cipher 块密码（`aes_ecb`/`aes_cbc`/`xxtea`）：往返 + offset 前缀保留 + 填充长度
- `xor_carry_rol` 已知向量对拍（`TestXorCarryRolKnownVector`，手动计算 ROL8(x,3) 与实现逐字节比）+ 默认 rol=3 + 非法 key 返回原数据 + offset=11 前缀
- `rc4` key 长度 1/16/256（对拍 `TestRC4KeyLengths`）
- gzip 往返（高熵+低熵+空）+ 非法数据解压报错
- checksum 已知向量：`xor8(0x55,0xAA)=0xFF`、`crc16/crc32("123456789")`、`crc32c` 标准值、`sum8` 手算
- hash 已知向量：`md5("")`、`sha1("abc")`、`sha256("")` 的 hex + HMAC 与 `crypto/hmac` 直接调用一致 + HMAC ≠ plain

### 与 `lua_crypto_test.go` 对拍情况
| 对拍项 | lua_crypto_test | 本任务 | 结果 |
|---|---|---|---|
| `xor_carry_rol` 已知向量（手动 ROL8(3)） | `TestXorCarryRolEquivalence` | `TestXorCarryRolKnownVector` | ✅ 同一 expected 计算，PASS |
| `xor8` bcc `0x55^0xAA=0xFF` | `TestBcc` | `TestXor8BccVector` | ✅ PASS |
| offset=11 前缀保留 | `TestXorOffset` | `TestXorCarryRolOffsetPrefix` | ✅ PASS |
| rc4 key 长度 1/16/256 | `TestRC4KeyLengths` | `TestRc4KeyLengths` | ✅ PASS |
| crc16 `0x29B1` | `TestCrc16` | `TestCrc16KnownVector` | ✅ PASS |
| crc32 `0xCBF43926` | `TestCrc32` | `TestCrc32KnownVector` | ✅ PASS |
| md5/sha1/sha256 hex | `TestMd5`/`TestSha1`/`TestSha256` | `TestMd5Vector`/`TestSha1Vector`/`TestSha256Vector` | ✅ PASS（本层返回原始字节，测试中 hex.EncodeToString 比对） |

## 5. 验收 self-review

| 项 | 状态 |
|---|---|
| 四张表 + 4 接口 + Lookup + Algorithms 元数据齐全 | ✅ |
| v1 必做算法清单全部注册且可查到（19 个） | ✅ |
| `xor_carry_rol` 默认 rol=3；offset 适配后前缀明文不变、输出等长 | ✅ |
| `xor8` 与 lua_crypto `computeBcc` 一致（逐字复制 :118） | ✅ |
| 算法逻辑从 lua_crypto/lua_zlib 迁移，无重写偏差 | ✅（核心变换逐字复制；注释标明来源行号） |
| `codec/` 无 gopher-lua 依赖 | ✅ `go list -deps ./codec \| grep gopher-lua` 空 |
| 未改动 `adapter/` 及其他包 | ✅ `git diff HEAD` 无 tracked 改动（整个 codec/ 是新增 untracked） |
| `go build ./codec/...` / `go vet ./codec/...` / `go test ./codec/...` 全绿 | ✅ |
| `go build ./...` 无破坏 | ✅ |
| 未 git commit | ✅ |

## 6. Concerns（需 T1.4 engine 知晓）

1. **块密码不满足 `len(out)==len(data)`**：`aes_ecb`/`aes_cbc` 因 PKCS#7 填充、`xxtea` 因 4 字节对齐补零，密文长度 > 明文。brief 的 cipher offset 语义（`len(out)==len(data)`）是为流密码（现协议 `xor_carry_rol`）设计的。本实现保留 offset 前缀明文，但块密码步骤的长度变化必须由 T1.4 engine 在组装帧时处理（如 length 字段反映密文长度，不是明文长度）。已在各 cipher 注释中显式标注。**现协议未用块密码，不影响 v1 对拍。**

2. **`aes_ctr` 来源说明**：brief 表格写 `aes_cbc / aes_ctr / aes_ecb` "lua_crypto 已有"，但实际 `adapter/lua_crypto.go` 只有 ECB/CBC，无 CTR。本任务用 stdlib `crypto/cipher.NewCTR` 补齐（不引第三方依赖），算法本身是标准 AES-CTR，T1.7 无旧 Lua 实现可对拍（CTR 是新增能力，非迁移）。

3. **Hasher 返回原始摘要字节**（非 hex）：与 lua_crypto Lua 入口的 hex 字符串不同。这是更底层的接口选择，T1.4 写入 bytes 字段时按需编码。已在 hashes.go 顶部注释说明。

4. **gzip Decompress 池归还策略**：相比原 `lua_zlib.go`（总是归还），本实现仅在 read+close 均成功时归还 reader（失败时丢弃，避免污染池）。行为对外等价（失败本来就走错误路径），仅池管理更稳妥。

## 7. 改动文件

新增（全部 untracked）：
- `codec/registry.go`
- `codec/ciphers.go`
- `codec/compressors.go`
- `codec/checksums.go`
- `codec/hashes.go`
- `codec/registry_test.go`

未修改任何已存在文件（`schema.go`/`errors.go`/`schema_test.go` 保持 T1.1 原样；`adapter/` 完全未动）。
