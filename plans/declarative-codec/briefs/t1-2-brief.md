# T1.2 Brief — 算法注册表 + 迁移算法 + 元数据导出

> 你是 implementer。先读本 brief（含须逐字使用的确切值）。可参考 `plans/declarative-codec/01-track-codec-engine.md` §3.4/§4.4、总纲 §3.1.3、以及现有 `adapter/lua_crypto.go`、`adapter/lua_zlib.go`、`adapter/lua_crypto_test.go`。
> 工作目录：worktree 根。**不要 git commit**。

## 目标

在 T1.1 建好的 `codec/` 包内，新增**四张算法注册表**及其实现，供后续 T1.3 编译层按 `PipelineStep.Algo` / `StepProduce.Algo` 查找实现。本任务**不改** T1.1 的 schema/errors 文件，只**新增**文件。

## 新增文件

- `codec/registry.go`：4 个接口 + 4 张注册表 + `Register*` 函数 + 查找 helper + **元数据导出**。
- `codec/ciphers.go`：cipher 实现（迁移自 `adapter/lua_crypto.go`）。
- `codec/compressors.go`：compressor 实现（迁移自 `adapter/lua_zlib.go`）。
- `codec/checksums.go`：checksum 实现（迁移自 `adapter/lua_crypto.go`）。
- `codec/hashes.go`：hasher 实现（迁移自 `adapter/lua_crypto.go` 或标准库）。
- `codec/registry_test.go`（及按需 `ciphers_test.go` 等）：TDD 测试。

## 接口（registry.go，逐字）

```go
package codec

// Cipher：offset 为明文前缀长度——前 offset 字节保持明文，仅处理 data[offset:]。
// 返回值长度必须等于 len(data)（前缀原样 + 处理后的尾部拼接）。
type Cipher interface {
	Encrypt(data, key []byte, offset int, params map[string]any) (out []byte, err error)
	Decrypt(data, key []byte, offset int, params map[string]any) (out []byte, err error)
}
type Compressor interface {
	Compress(data []byte) ([]byte, error)
	Decompress(data []byte) ([]byte, error)
}
// Sum 对给定区域算校验值（最多 8 字节语义；调用方按目标字段 size 截取/对齐）。
type Checksum interface {
	Sum(data []byte, params map[string]any) uint64
}
// Hash：key 非空走 HMAC 变体。
type Hasher interface {
	Hash(data, key []byte, params map[string]any) []byte
}
```

注册表与查找：

```go
// 四张表：名字 → 实现。init() 阶段注册，运行期只读。
// 查找缺失返回 (nil, false)——由调用方（T1.3 编译期）fail loud，本层不 panic。
func RegisterCipher(name string, c Cipher)
func RegisterCompressor(name string, c Compressor)
func RegisterChecksum(name string, c Checksum)
func RegisterHasher(name string, h Hasher)
func LookupCipher(name string) (Cipher, bool)
func LookupCompressor(name string) (Compressor, bool)
func LookupChecksum(name string) (Checksum, bool)
func LookupHasher(name string) (Hasher, bool)
```

并发安全：注册表用包级 `map` + `sync.RWMutex` 保护（注册在 init，查找在热路径，RWMutex 读多写极少）。或用 `sync.Map`——但注册集中、查找热，`map+RWMutex` 更合适。

## 元数据导出（registry.go）

供 T4 `GET /sbot/codec/algorithms` 与 T3 前端下拉使用：

```go
type AlgoParam struct {
	Name        string `json:"name"`
	Type        string `json:"type"`        // int|string|bool|bytes
	Default     any    `json:"default,omitempty"`
	Description string `json:"description,omitempty"`
}
type AlgoMeta struct {
	Name        string      `json:"name"`
	Op          string      `json:"op"`          // cipher|compress|checksum|hash
	Description string      `json:"description,omitempty"`
	Params      []AlgoParam `json:"params,omitempty"`
}
// 返回所有已注册算法的元数据，按 op 分组稳定排序（名字字母序）。
func Algorithms() []AlgoMeta
```

实现方式建议：注册时同时登记 `AlgoMeta`（`RegisterCipher(name, impl, meta)` 或单独 `RegisterCipherMeta`）。自选一种干净方式，但 `Algorithms()` 必须能返回与注册表一致的清单。

## v1 必做算法清单（迁移来源 + 确切名）

从 `adapter/lua_crypto.go` / `adapter/lua_zlib.go` **抽取已有 Go 实现**（不是重写），保持算法逻辑逐字节一致（T1.7 对拍依赖此）：

| 注册表 | 名字 | 来源/说明 |
|---|---|---|
| cipher | `none` | 直通（offset 仍按规则：前缀明文 + data[offset:] 原样返回） |
| cipher | `xor` | 单字节/循环 key xor（lua_crypto 已有） |
| cipher | `xor_carry_rol` | **现协议**：`adapter/lua_crypto.go:841` `EncryptXorCarryRol` / `:875` `DecryptXorCarryRol`；`params.rol` 缺省 **3** |
| cipher | `rc4` | lua_crypto 已有 |
| cipher | `aes_cbc` / `aes_ctr` / `aes_ecb` | lua_crypto 已有 |
| cipher | `xxtea` | lua_crypto 已有 |
| compress | `none` | 直通 |
| compress | `gzip` | `adapter/lua_zlib.go`（现协议阈值 2048，但阈值由 engine `when` 决定，本层只做无阈值 gzip） |
| checksum | `none` | 恒 0 |
| checksum | `xor8` | **bcc 用**：lua_crypto `computeBcc` 的 xor8 逻辑 |
| checksum | `sum8` / `crc16` / `crc32` / `crc32c` | lua_crypto 已有 / 标准库 `hash/crc32` |
| hash | `md5` / `sha1` / `sha256` | 标准库；key 非空走 HMAC（`crypto/hmac`） |

> 可选算法（`zlib`/`snappy`/`lz4`/`zstd`/`3des`/`sha512`）**v1 不实现**——别引入新依赖。注册表是开放的，但本任务只落上表。

## 关键约束

- **cipher 的 offset 语义**：`Encrypt/Decrypt(data, key, offset, ...)` 必须保持 `data[:offset]` 不变，只变换 `data[offset:]`，返回 `len==len(data)`。`xor_carry_rol` 现有实现可能不带 offset——你需要在其外层包一个 offset 适配（前缀保留 + 对 `data[offset:]` 调核心变换），**核心变换逻辑不改**。
- **不修改** `adapter/` 下任何文件。迁移 = 复制/适配算法逻辑到 `codec/`，源文件留给 T2/T4 切换时删。
- **不 import gopher-lua**。
- `xor8` 要与 `lua_crypto.go:160/227` `computeBcc` 的 xor8 一致（T1.7 对 bcc 校验）。
- 不要在本任务里做 encode/decode/compile——只建注册表 + 算法实现 + 元数据。

## 工作方式（TDD）

1. RED：先写测试。每个算法至少覆盖：cipher 的 Encrypt→Decrypt 往返（含 offset>0，验证前缀字节不变、`len(out)==len(data)`）；与 `adapter/lua_crypto_test.go` 中已有向量对拍（若该测试有 `xor_carry_rol`/`rc4`/`aes`/`xxtea` 的已知输入输出，直接拿来断言）；checksum 已知向量；gzip 往返；hash/HMAC 已知向量；`Algorithms()` 返回清单完整且含现协议所需算法。
2. GREEN：实现各文件，注册到表。
3. `go build ./codec/...`、`go vet ./codec/...`、`go test ./codec/...` 全绿、输出干净。
4. **不要 git commit。**

## 验收（self-review）

- 四张表 + 4 个接口 + Lookup + Algorithms 元数据齐全。
- v1 必做算法清单全部注册且可查到。
- `xor_carry_rol` 默认 rol=3；offset 适配后前缀明文字节不变、输出等长。
- `xor8` 与 lua_crypto computeBcc 一致。
- 算法逻辑从 lua_crypto/lua_zlib 迁移，无重写偏差；与已有测试向量对拍通过。
- codec/ 无 gopher-lua 依赖；未改动 adapter/。

## 报告

写完整报告到 `plans/declarative-codec/reports/t1-2-report.md`：实现内容、迁移了哪些算法（源文件:行）、测试与结果、TDD RED/GREEN 证据、与 lua_crypto_test 对拍情况、改动文件、self-review 发现、concerns。
返回（<15 行）：Status、改动文件、一行测试摘要、concerns、报告路径。
