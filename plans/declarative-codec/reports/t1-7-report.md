# T1.7 报告 — 对拍/基准闭环 + preview helper + 冻结交接（T1 收尾）

> 任务：闭环 T1.5/T1.6 三个 review 遗留、补齐对拍与基准、提供 `codec.Preview` helper、写冻结交接说明。
> Brief：`plans/declarative-codec/briefs/t1-7-brief.md`。
> Worktree：`worktree-declarative-codec`。
> 状态：**DONE** — 全部 5 项交付，`go build ./...` + `go vet ./...` + `go test ./... -count=1` 全绿（codec 包 204 个 RUN、0 FAIL），未 git commit。

---

## 1. 三个 review 遗留闭环

### (a) UDP 压缩+加密 对拍缺口 — **已闭环**

**问题**：T1.5 的 `TestDecodeUDP_Parity_LuaAdapter` 把 `udp_compressible_encrypted_offset11` 排除在矩阵外（当时认为 engine onError=fail 与 codec.lua lenient 分歧，故意豁免）。

**codec.lua 真值核对**（`conf/adapter/codec.lua:150-189`）：
- `encode_udp` 用 offset 11（前 11 明文，body[12:] 加密）；低熵 body 先 gzip 再加密。
- `decode_udp = decode_tcp`：net_decrypt 用 offset 0 解密整 body → offset 不对称 → 前 11 字节被「再加密」（keystream 错位 XOR 出乱码）。
- 随后 `pcall(zlib.decompress, body)` 乱码失败被吞 → **返回乱码 body，routeKey 正常**（lenient，不阻断）。

**闭环方式**：engine 的 gz 步 onError=keep 变体复刻此 lenient 行为（解压失败保留原字节）。新增 `newSchemaCodecUDPKeepGzip`（`codec/decode_helpers_test.go`）构造该变体，新增 `TestDecodeUDP_Parity_CompressibleEncrypted_Offset11`（2 case：4KB / 2KB 低熵 body），断言 frame flags 同时置 encrypted+compressed，且 (routeKey, body, headerErr) 与 codec.lua 字节级一致。

**结论**：对拍通过（probe + 正式测试均 PASS）。**engine 默认 onError=fail（生产 codec.json）行为不变**（更严，会阻断 routeKey）；keep 变体仅证明 engine 可复刻 codec.lua lenient，闭环缺口而非文档豁免。

### (b) godoc 修正 — **已闭环**

`codec/engine.go` `verifyProducesAfterDecrypt` 原注释把「codec.lua decode 根本不校验 bcc」与「不对称时跳过」混为一谈。已改为准确表述：
- codec.lua decode 完全不校验 bcc；
- engine 在 `encOffset == decOffset`（TCP 现协议）时**额外**校验（比 codec.lua 更严，按 step.onError fail/keep 处理）；
- 非对称时（UDP 11/0）数学上无法校验，跳过（与 codec.lua 一致）。

仅注释改动，生产逻辑零变更。

### (c) errors.json 全量校验 — **已闭环**

新增 `TestMigration_ErrorMap_FullVerbatimVsErrorLua`（`codec/migration_test.go`）：
- 纯文本解析 `conf/adapter/error.lua` 的 `errors = {...}` 表（正则 `^\s*\[(\d+)\]\s*=\s*"(.*)"\s*,?\s*$`，已人工核对全部 667 条均符合此格式，无转义引号、无多行字符串）。
- 加载 `conf/adapter/errors.json`，断言**条目数相等（667=667）**+ **每对 code→desc verbatim 一致**（`string==`）。
- 不依赖 LuaAdapter（其 DescribeError 路径在未初始化 zap logger 时 nil panic，与迁移正确性无关）。
- 结果：**667 对全 verbatim PASS**，闭环 T1.6 仅抽样 8 条的缺口。

---

## 2. 对拍矩阵最终覆盖（总纲 §5 主验收）

### 2.1 encode 对拍（`TestEncodeTCP_Parity_LuaAdapter` + `TestEncodeUDP_Parity_LuaAdapter`，13 + 9 case）

| 维度 | TCP | UDP |
|---|---|---|
| 加密/不加密 | ✓（有 key / 无 key / cmd=0 不加密） | ✓（有 key / 无 key / cmd=0） |
| 压缩/不压缩 | ✓（低熵 4KB 压缩 + 高熵 4KB 拒绝压缩） | ✓（低熵 4KB 压缩） |
| 空 body | ✓ | ✓ |
| 单字节 body | ✓ | — |
| cmd=0 | ✓（3 case） | ✓ |
| nil route | ✓ | — |
| offset | 0 | 11（前 11 明文 + bcc 排除前缀） |
| body 边界 | — | 短于/等于/长于 offset（8/11/12 B） |

### 2.2 decode 对拍（`TestDecodeTCP_Parity_LuaAdapter` + `TestDecodeUDP_Parity_LuaAdapter` + 新增 `TestDecodeUDP_Parity_CompressibleEncrypted_Offset11`）

| 维度 | TCP | UDP |
|---|---|---|
| 加密/压缩/cmd=0/空 body | ✓（10 case） | ✓（5 case） |
| UDP 压缩+加密（keep 变体） | — | ✓（T1.7 新增 2 case） |
| headerErr 非零透传 | ✓ | ✓ |
| 短帧（<headerSize） | ✓（4 case） | — |
| HeaderOnly（无 body） | ✓ | — |
| 失败语义（坏 gzip/篡改 bcc/缺 key × fail/keep） | ✓（5 case，非对拍） | — |

### 2.3 访问器与结构断言

- `BodyLength` 与 `LuaAdapter.BodyLength` 对齐 + encode 自洽（`TestBodyLength` + `TestBodyLength_RoundtripWithEncode`）。
- `ExpectedRouteKey` 与 `LuaAdapter.ExpectedRouteKey` 对齐 + math.floor 截断（`TestExpectedRouteKey` + `TestExpectedRouteKey_FloorAlignment`）。
- 头部零初始化：cmd=0 时 bcc/errorCode/flags 字节为 0（`TestEncode_HeaderZeroInit_ChecksumNotExecuted`）。
- flags 命名位：encrypted+compressed 双位置位（`TestEncode_Flags_EncryptedAndCompressed`）、高熵拒绝压缩（`TestEncode_Flags_CompressionRejectedWhenLarger`）。
- bcc 语义：TCP = xor8(plaintext)、UDP = xor8(plaintext[11:])（`TestBCC_*`）。
- UDP 明文前缀保留（`TestEncodeUDP_PlaintextPrefix_Preserved`）。

**对拍真值**：`adapter.LuaAdapter`（`conf/adapter/codec.lua + error.lua`，pool=2 避免 pool=1 acquire 死锁）。断言全 `bytes.Equal`/字段相等，非空转（mutation 测试在 T1.4 review 已证断言有效性）。

---

## 3. Benchmark 倍率（量化验收）

新 Go 引擎（`codec.SchemaCodec`）vs 旧 Lua 路径（`adapter.LuaAdapter`），`go test -bench -benchmem -benchtime=1s`，Windows 10 + Intel i5-9400F @ 2.90GHz：

| 操作 | body 形态 | Go ns/op | Lua ns/op | 速度倍率 | Go allocs/op | Lua allocs/op | allocs 降幅 |
|---|---|---:|---:|---:|---:|---:|---:|
| Encode | 64B 高熵（仅加密） | 632.6 | 9519 | **~15.0×** | 5 | 49 | −90% |
| Encode | 2KB 低熵（压+密） | 40678 | 55039 | **~1.35×** | 7 | 55 | −87% |
| Encode | 16KB 低熵（压+密） | 78966 | 114079 | **~1.44×** | 7 | 55 | −87% |
| Decode | 64B 高熵（仅加密） | 638.7 | 4877 | **~7.6×** | 5 | 23 | −78% |
| Decode | 2KB 低熵（压+密） | 4151 | 15188 | **~3.7×** | 12 | 34 | −65% |
| Decode | 16KB 低熵（压+密） | 42046 | 74595 | **~1.77×** | 19 | 41 | −54% |

**观察**：
- 小 body（64B）：Lua 调用开销主导，提速最显著（encode ~15×、decode ~7.6×）。
- 大 body（16KB）：压缩成本主导，提速收敛到 ~1.4×–1.8×，但 allocs/op 仍降 50%+。
- allocs/op 全面下降 54%–90%（Lua 每次 encode/decode 都分配 string/table，Go 用局部切片）。
- encode 大 body 时 Go 的 gzip 单次压缩仍占大头（78966 ns/op 中绝大部分是 gzip）；Lua 路径额外叠加 LState 调度 + zlib 模块开销。

bench 文件：`codec/engine_bench_test.go`（4 个 bench 函数 × 3 size = 12 个子 bench）。

---

## 4. Preview Helper 设计

`codec/preview.go` + `codec/preview_test.go`（12 个测试全 PASS）。

### 4.1 签名（与 brief 逐字一致）

```go
type PreviewField struct {
    Name   string `json:"name"`
    Value  uint64 `json:"value"`
    Offset int    `json:"offset"`
    Size   int    `json:"size"`
}
type PreviewResult struct {
    Mode      string         `json:"mode"`
    FrameHex  string         `json:"frameHex,omitempty"`
    BodyHex   string         `json:"bodyHex,omitempty"`
    RouteKey  string         `json:"routeKey,omitempty"`
    HeaderErr uint64         `json:"headerErr,omitempty"`
    Fields    []PreviewField `json:"fields,omitempty"`
    Error     string         `json:"error,omitempty"`
}
func Preview(schema *CodecSchema, mode, transport string, route map[string]any, bodyHex, keyHex, frameHex string) PreviewResult
```

### 4.2 设计要点

- **纯 Go + codec**：不 import gopher-lua；不读写文件、不接网络、不依赖任务状态。
- **不 panic**：nil schema / 编译失败 / 坏 hex / 未知 mode/transport → 填 `Error`（中文）返回。
- **transport 当前不影响计算**（codec 单 transport，offset 已在 schema）；保留入参为 T3/T4 语义清晰。
- **route 字段值支持 int/float/string**（string 经 `routePreviewFloorInt` 数值化，与 codec.lua `math.floor(route.cmd or 0)` 行为对齐）。
- **Fields 提取**：编译 schema 后，按 `schema.Header` 的 (name, offset, size, type, endian) 从 encode 出的 frame 头部（或 decode 入参 frame 头部）读出每个字段的数值化值，供前端逐字段展示。

### 4.3 测试覆盖（12 case）

- 合法 encode：FrameHex + 7 字段 Fields 值正确（cmd=100/act=7/flags 加密位/bcc 非零）。
- encode→decode 往返：BodyHex 还原 + RouteKey=100:7 + HeaderErr=0。
- route string 值：与 int 值产生同一帧。
- 短帧 decode：routeKey 空串不报错。
- nil schema / 畸形 schema（缺 role:length）/ 坏 bodyHex / 坏 keyHex / 坏 frameHex / 未知 mode / 未知 transport：均填中文 Error 不 panic。
- 无 key encode：flags=0（不加密不压缩）。

---

## 5. 冻结交接文档

`plans/declarative-codec/reports/t1-freeze-handoff.md`：给 T2/T3/T4 的契约清单，含：
- T2：`adapter.NewSchemaAdapter` 签名、9 方法零改动、decode 3-tuple、Close 幂等、并发安全结构性论证、删 luaMu 路径。
- T4：`LoadSchema`/`LoadErrorMap`/`NewSchemaCodec`/`NewSchemaAdapter` 签名、文件命名（`<proto>_<service>_codec.json` + 共享 errors.json）、resolver key = server 串、无 fallback、配置落点 `standalone.adapter.codecs`。
- T3：`CodecSchema` 类型集、`Validate` 规则、`Algorithms()` 算法清单（19 个）、`Preview` 端点支撑。
- 对拍结论（encode 13+9 case、decode 10+5+2 case、失败语义 5 case）。
- Benchmark 倍率表。
- 已知遗留 5 项（bcc decode 仅对称校验、UDP 压缩+加密 keep 对齐、块密码变长、-race 未跑、errors.json 未命中返空串）。
- 冻结后变更纪律 + 不变量清单。

---

## 6. TDD/验证证据

| 步骤 | 命令 | 结果 |
|---|---|---|
| 编译 | `go build ./...` | OK，无输出 |
| vet | `go vet ./...` | OK，无输出 |
| codec 测试 | `go test ./codec/ -count=1` | ok stressbot/codec — 204 RUN / 0 FAIL |
| 全 repo 测试 | `go test ./... -count=1` | 全绿（adapter/cmd/agent/codec/engine/protox/script/sharedstate/state） |
| (a) UDP 对拍 | `go test ./codec/ -run TestDecodeUDP_Parity_CompressibleEncrypted -v` | PASS（2/2 case 字节级一致） |
| (b) godoc | 人工核对 `verifyProducesAfterDecrypt` 注释 | 已改准 |
| (c) 全量校验 | `go test ./codec/ -run TestMigration_ErrorMap_FullVerbatim -v` | PASS（667 对 verbatim） |
| preview | `go test ./codec/ -run TestPreview -v` | PASS（12/12） |
| bench encode | `go test ./codec/ -bench BenchmarkSchemaCodec_Encode|BenchmarkLuaAdapter_Encode -benchmem` | 见 §3 表 |
| bench decode | `go test ./codec/ -bench BenchmarkSchemaCodec_Decode|BenchmarkLuaAdapter_Decode -benchmem` | 见 §3 表 |
| 回归 | `go test ./codec/ -run 'TestEncodeTCP_Parity|TestEncodeUDP_Parity|TestDecodeTCP_Parity|TestDecodeUDP_Parity|TestMigration' -v` | 全 PASS（T1.4–T1.6 未回退） |
| race | `go test -race ./codec/ ...` | **未跑**（环境无 gcc/cgo，`-race requires cgo`），并发安全为结构性论证 |

---

## 7. 改动文件

### 7.1 新增

- `codec/preview.go` — Preview helper（纯 Go + codec）。
- `codec/preview_test.go` — 12 个 preview 测试。
- `codec/engine_bench_test.go` — encode/decode 基准（4 bench × 3 size = 12 子 bench）。
- `plans/declarative-codec/reports/t1-7-report.md` — 本文件。
- `plans/declarative-codec/reports/t1-freeze-handoff.md` — 冻结交接说明。

### 7.2 修改

- `codec/engine.go` — (b) `verifyProducesAfterDecrypt` godoc 修正（仅注释，生产逻辑零变更）。
- `codec/decode_helpers_test.go` — 新增 `newSchemaCodecUDPKeepGzip` 变体构造器（仅供 (a) 对拍测试用）。
- `codec/decode_test.go` — (a) 把 `udp_compressible_encrypted_offset11` 纳入对拍矩阵（新增 `TestDecodeUDP_Parity_CompressibleEncrypted_Offset11`，2 case；同时清理原豁免注释）。
- `codec/migration_test.go` — (c) 新增 `TestMigration_ErrorMap_FullVerbatimVsErrorLua`（667 对 verbatim 校验）+ 加 `strconv` import。

### 7.3 未改

- T1.1–T1.6 的生产代码（`schema.go`/`compile.go`/`engine.go` 执行逻辑/`registry.go`/`ciphers.go`/`compressors.go`/`checksums.go`/`hashes.go`/`errors.go`/`adapter/schema_adapter.go`）零改动。
- 生产迁移产物（`conf/adapter/*_codec.json` + `errors.json`）零改动。

---

## 8. Self-review（对照 brief 验收清单）

- [x] (a) UDP 压缩+加密 对拍纳入并通过（keep 变体字节级一致）。
- [x] (b) godoc 已改准（codec.lua 完全不校验 bcc / engine 对称额外校验 / 非对称跳过）。
- [x] (c) errors.json 全量 verbatim 校验通过（667 对）。
- [x] 对拍矩阵齐全，全字节级 PASS（encode 13+9、decode 10+5+2、失败语义 5、访问器与结构断言）。
- [x] benchmark 有新 vs 旧 ns/op + allocs/op 倍率记录（encode ~1.35×–15×、decode ~1.77×–7.6×、allocs −54%–−90%）。
- [x] preview helper 编译、往返测试通过、畸形输入填 Error 不 panic（12 测试 PASS）。
- [x] 冻结交接文档准确反映当前代码（`t1-freeze-handoff.md`）。
- [x] 全 repo `go test ./... -count=1` 绿。
- [x] 未 `git commit`。
- [x] 未改 T1.1–T1.6 已 review 通过的行为（仅注释 + 新增测试 + 新增 preview/bench/doc）。

---

## 9. Concerns

1. **`-race` 未跑**：环境无 gcc/cgo（`go test -race` 报 `requires cgo`）。并发安全为结构性论证：SchemaCodec 编译产物无可变字段；encode/decode 全部使用局部变量；`TestEncode_ConcurrentSafe`/`TestDecode_ConcurrentSafe` 8 goroutine × 50 次往返通过。T2 接入后建议在具备 cgo 的 CI 环境补跑一次 `-race`。
2. **块密码变长未进 codec.lua 对拍矩阵**：v1 现协议用 `xor_carry_rol`（定长），块密码路径（aes_ecb 等）有单测（`testdata/aes_ecb_codec.json` + `TestParams_KeyLen16_AesEcb`）但 codec.lua 无块密码故无法对拍。若未来 schema 切块密码，T4 切换前需用真实服务端帧验证（已在冻结交接文档「已知遗留」第 3 项标注）。
3. **UDP 压缩+加密 keep 变体 vs 生产 fail 变体**：生产 `udp_battle_codec.json` 用 onError=fail（更严，解 UDP 自身 encode 帧会因 offset 不对称返空 routeKey）；keep 变体仅证明 engine 可复刻 codec.lua lenient。T2 接入时若发现服务端 UDP 回包确实是压缩+加密形态（实际不太可能，UDP 路径未设计支持压缩），需评估是否把生产 schema 改 keep。已在冻结交接「已知遗留」第 2 项标注。
4. **preview route 字段未导出访问 compiledField**：Fields 提取走 `schema.Header` 元数据 + 本地 `readFieldU64`，而非复用 engine 的 `readUint`。两套实现口径对齐（u8/u16/u24/u32/u64/f32/f64/bytes 一致；endian le/be 一致），单测已覆盖 cmd/act/flags/bcc 字段值正确。若 schema 引入更复杂 type（未来 v1.1），需同步更新 preview 的 `readFieldU64`。
