# T1 冻结交接说明（declarative-codec）

> 状态：**T1 全轨冻结**（T1.1–T1.7 全部完成、review clean、全 repo `go build`/`go vet`/`go test ./... -count=1` 绿）。
> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`，待 controller 把 T1 批次拿给用户确认后提交）。
> 适用对象：T2（后端集成 + 删 luaMu/LuaAdapter）、T3（前端 codec 编辑器）、T4（配置加载与分发）。
> 一句话总览：纯 Go、不可变、并发安全的 `Adapter` 实现已就绪，`codec/` 包零 gopher-lua 依赖，与 `conf/adapter/codec.lua` 在全 encode/decode 矩阵上字节级一致；新引擎在 encode 上快 ~1.35×–15×、decode 快 ~1.77×–7.6×，allocs/op 普遍降 50%–90%。

---

## 1. 给 T2（后端集成 + 删锁）的契约

### 1.1 Adapter 包装（`adapter/schema_adapter.go`）

```go
// 仅 import codec，零 gopher-lua。
func NewSchemaAdapter(schema *codec.CodecSchema, errorMap map[uint64]string) (Adapter, error)
```

- 返回的 `*SchemaAdapter` **实现 `adapter.Adapter` 全 9 方法，签名零改动**（编译期断言 `var _ Adapter = (*SchemaAdapter)(nil)`）。
- **`DecodeTCP` / `DecodeUDP` 仍返回 3-tuple `(routeKey string, body []byte, headerErr uint64)`，不返回 err、不返回 headerFields**——T2 在 `network` / `engine` / `tcp_request` 处**无需为头字段接线**。
- **`Close()` 幂等 no-op**（`codec.SchemaCodec` 编译产物无锁、无可变状态、无资源需释放）。
- **并发安全**：`*SchemaAdapter` 持有的 `*codec.SchemaCodec` 构造后只读，任意 goroutine 并发调用 9 方法**无需加锁**——T2 删 `luaMu` 即可。
  - 注：`-race` 未跑（环境无 gcc/cgo），并发安全为结构性论证（SchemaCodec 无可变字段，encode/decode 全部用局部变量；`TestEncode_ConcurrentSafe`/`TestDecode_ConcurrentSafe` 8 goroutine × 50 次往返跑通）。

### 1.2 替换路径

- 现有 `RobotAdapter`（`adapter/robot_adapter.go`）+ `LuaAdapter`（`adapter/lua_adapter.go`）→ 在 T2-D 阶段删除。
- `luaMu` 串行锁全删（codec 不再需要 LState）。
- T2 替换点：`robot` 包构造 adapter 处 + `network` 包调用 encode/decode 处。9 方法签名一致，行为字节级一致（对拍已证），应是无行为回退的纯替换。

### 1.3 错误描述

- `DescribeError(code uint64) string`：命中返回中文描述，**未命中返回空串 `""`**（v1 冻结默认；不要改成 panic/unknown 字符串）。

---

## 2. 给 T4（配置加载与分发）的契约

### 2.1 Loader 签名

```go
// codec 包：
func LoadSchema(path string) (*CodecSchema, error)              // codec/<proto>_<service>_codec.json
func LoadErrorMap(path string) (map[uint64]string, error)        // 共享 errors.json（扁平 {code:desc}）
func NewSchemaCodec(schema *CodecSchema, errorMap map[uint64]string) (*SchemaCodec, error)
// adapter 包：
func NewSchemaAdapter(schema *codec.CodecSchema, errorMap map[uint64]string) (Adapter, error)
```

- **schema 编译失败（缺字段、未知算法、悬空引用等）返回中文 error**——T4 loader 应 fail loud（按总纲「禁止兼容性兜底」），不要 fallback 到旧 LuaAdapter。
- **`errorMap` 允许 nil**（→ `DescribeError` 永远返回空串）；但生产应传共享 `errors.json`。

### 2.2 文件命名与绑定粒度

- **每连接一份 codec 文件**：`<proto>_<service>_codec.json`（如 `tcp_logic_codec.json` / `tcp_battle_codec.json` / `udp_battle_codec.json`）。
- **共享一份 errors.json**：所有连接共用（`conf/adapter/errors.json`，667 条 code→desc，已对 `error.lua` 全量 verbatim 校验）。
- **resolver key = `server` 串**（即 `<proto>:<service>` 形态，由 T4 决定具体串拼法）。
- **无 runtime fallback**：缺文件 / 缺连接 → fail loud。

### 2.3 配置落点（已采用决策，progress-ledger §「已确认决策」）

- `standalone.adapter.codecs`：`server 串 → codec 文件路径` 的 map。
- 未显式声明的连接按 `<proto>_<service>_codec.json` 推断；缺文件 fail loud。

### 2.4 迁移产物（已就位，T4 直接用）

`conf/adapter/` 下：

| 文件 | 用途 | 验证状态 |
|---|---|---|
| `tcp_logic_codec.json` | TCP 逻辑服（encOffset=0/decOffset=0） | T1.6 encode 对拍 codec.lua 6 case 字节一致；T1.7 未回归 |
| `tcp_battle_codec.json` | TCP 战斗服（同上语义） | T1.6 与 tcp_logic 字节一致断言 |
| `udp_battle_codec.json` | UDP 战斗服（encOffset=11/decOffset=0） | T1.6 encOffset=11 明文前缀 + decOffset=0 decode 行为断言 |
| `errors.json` | 共享错误码（667 条） | T1.7 全量 verbatim 校验通过 |

---

## 3. 给 T3（前端 codec 编辑器）的契约

### 3.1 Schema 定义

- 类型与字段全集：见 `codec/schema.go` 的 `CodecSchema` / `FrameSpec` / `Field` / `PipelineStep` / `StepOffset` / `StepProduce` / `OverSpec` / `StepCond` / `Guard` / `ValueSource` / `FlagBit`。
- 校验规则：见 `codec/schema.go` 的 `Validate()`（聚合多条中文错误一次性返回，方便前端展示）。T3 据此做 Monaco JSON Schema 草案。
- 字段语义集（v1 冻结值）：
  - `Field.Type`：`u8/u16/u24/u32/u64/i8/i16/i24/i32/i64/f32/f64/bytes`。
  - `Field.Role`：`length`（必有且仅 1 个）/`route`(≥1)/`errorCode`/`flags`/`checksumOut`/`value`/`reserved`。
  - `PipelineStep.Op`：`compress/encrypt/checksum/hash`。
  - `PipelineStep.OnError`：`fail`(默认)/`keep`。
  - `ValueSource.Kind`：`const/route`（v1 支持）；`state/counter/timestamp` → v1 显式拒绝（报「v1 不支持，留 v1.1」）。
  - `StepProduce.Region`：`ciphered/bodyPlain/bodyFinal/header/frame`。

### 3.2 算法清单（下拉/参数表单）

```go
func codec.Algorithms() []AlgoMeta  // 按 op 分组稳定排序：cipher → compress → checksum → hash
```

- 返回 `AlgoMeta{Name, Op, Description, Params[]AlgoParam}`，供 T3 前端下拉与 `GET /sbot/codec/algorithms`（T4 包装）使用。
- v1 算法清单（19 个）：
  - **cipher**：`none`、`xor`、`xor_carry_rol`、`rc4`、`aes_cbc`、`aes_ctr`、`aes_ecb`、`xxtea`
  - **compress**：`gzip`
  - **checksum**：`xor8`、`sum8`、`crc16`、`crc32`、`crc32c`
  - **hash**：`md5`、`sha1`、`sha256`（+HMAC 变体由非空 key 触发）

### 3.3 Preview 端点支撑（T4 `POST /sbot/codec/preview`）

```go
// 纯 Go + codec，零 gopher-lua，不接网络/文件/任务状态。
type PreviewField struct {
    Name   string `json:"name"`
    Value  uint64 `json:"value"`
    Offset int    `json:"offset"`
    Size   int    `json:"size"`
}
type PreviewResult struct {
    Mode      string         `json:"mode"`                 // "encode"|"decode"
    FrameHex  string         `json:"frameHex,omitempty"`    // encode 出参
    BodyHex   string         `json:"bodyHex,omitempty"`     // decode 出参
    RouteKey  string         `json:"routeKey,omitempty"`
    HeaderErr uint64         `json:"headerErr,omitempty"`
    Fields    []PreviewField `json:"fields,omitempty"`
    Error     string         `json:"error,omitempty"`       // schema 编译/运行错误（中文）
}
func codec.Preview(schema *codec.CodecSchema, mode, transport string, route map[string]any, bodyHex, keyHex, frameHex string) PreviewResult
```

- 入参语义：`mode=encode` 需 `route+bodyHex+keyHex`；`mode=decode` 需 `frameHex+keyHex`（route 忽略）。
- `transport` 入参当前**不影响单 codec 计算**（codec 单 transport，offset 已在 schema 里），保留为 T3/T4 语义清晰。
- **畸形输入（坏 schema / 坏 hex / 未知 mode/transport）填 `Error` 返回，不 panic**——T4 端点直接把 `PreviewResult` 序列化为 JSON 返回即可。

---

## 4. 对拍结论（验收核心）

### 4.1 encode 对拍（字节级一致）

- TCP：13 case（加密/不加密、压缩/不压缩、空 body、cmd=0、nil route、act=0、单字节 body…）— T1.4 全 PASS，T1.7 未回归。
- UDP：9 case（encOffset=11、body 短于/等于/长于 offset、空 body、cmd=0、无 key…）— T1.4 全 PASS。
- 生产 codec.json：`tcp_logic_codec.json` 6 case 对拍 codec.lua 字节一致（T1.6）。

### 4.2 decode 对拍（3-tuple 完全一致）

- TCP：10 case（含 headerErr 非零、短帧、HeaderOnly）— T1.5 全 PASS。
- UDP：5 case（默认 onError=fail 变体）+ **2 case UDP 压缩+加密**（T1.7 新增，用 gz onError=keep 变体复刻 codec.lua lenient）— T1.7 全 PASS。

### 4.3 失败语义（engine 比 codec.lua 更严，可配置）

- 坏 gzip / 篡改 bcc / 缺 key × `onError=fail/keep`：T1.5 全覆盖。
- TCP 对称路径（encOffset=decOffset=0）：engine 额外校验 bcc（codec.lua decode 不校验）——可检测篡改。
- UDP 非对称路径（encOffset=11/decOffset=0）：engine 跳过 bcc 校验（数学上无法校验），与 codec.lua 一致。

---

## 5. Benchmark 倍率（量化验收，总纲 §0 第 1 项）

新 Go 引擎（`codec.SchemaCodec`）相对旧 Lua 路径（`adapter.LuaAdapter`）：

| 操作 | body 形态 | Go ns/op | Lua ns/op | 速度倍率 | Go allocs | Lua allocs | allocs 降幅 |
|---|---|---:|---:|---:|---:|---:|---:|
| Encode | 64B 高熵（仅加密） | 632.6 | 9519 | **~15.0×** | 5 | 49 | −90% |
| Encode | 2KB 低熵（压+密） | 40678 | 55039 | **~1.35×** | 7 | 55 | −87% |
| Encode | 16KB 低熵（压+密） | 78966 | 114079 | **~1.44×** | 7 | 55 | −87% |
| Decode | 64B 高熵（仅加密） | 638.7 | 4877 | **~7.6×** | 5 | 23 | −78% |
| Decode | 2KB 低熵（压+密） | 4151 | 15188 | **~3.7×** | 12 | 34 | −65% |
| Decode | 16KB 低熵（压+密） | 42046 | 74595 | **~1.77×** | 19 | 41 | −54% |

环境：Windows 10 + Intel i5-9400F @ 2.90GHz，`go test -bench -benchmem -benchtime=1s`。
结论：小 body 提速最显著（Lua 调用开销主导）；大 body 压缩成本主导，提速收敛到 ~1.4×–1.8×，但 allocs/op 仍降 50%+。**encode/decode 均零 Lua**（codec 包不 import gopher-lua）。

---

## 6. 已知遗留与边界（T2/T3/T4 必读）

1. **bcc decode 仅对称校验**：codec.lua decode 完全不校验 bcc；engine 在 `encOffset==decOffset`（TCP 现协议）时额外校验（更严），非对称（UDP）时跳过（与 codec.lua 一致）。详见 `codec/engine.go` `verifyProducesAfterDecrypt` godoc（T1.7 已修正注释）。
2. **UDP 压缩+加密 用 onError:keep 对齐 codec.lua**：codec.lua decode_udp 对 UDP-encode（offset 11）帧会因 offset 不对称解出乱码 gzip 流，pcall 吞错返回乱码 body（lenient）。生产 `udp_battle_codec.json` 用 onError=fail（更严，会阻断 routeKey）；若需严格复刻 codec.lua 行为，用 onError=keep 变体（T1.7 对拍已证字节一致）。
3. **块密码变长**：`aes_cbc/aes_ctr/aes_ecb/xxtea` 会改变 body 长度。v1 现协议用 `xor_carry_rol`（定长，对拍覆盖）；块密码路径编译/单测通过（`testdata/aes_ecb_codec.json` + `TestParams_KeyLen16_AesEcb`）但**未进入 codec.lua 对拍矩阵**（codec.lua 无块密码）。T3 编辑器允许选块密码，T4 切换前需用真实服务端帧验证。
4. **`-race` 未跑**：环境无 gcc/cgo。并发安全为结构性论证（SchemaCodec 不可变 + encode/decode 全局部变量 + 8×50 并发往返测试通过）。
5. **errors.json 未命中返回空串**（不返回 `未知错误(N)`，与 codec.lua `describe_error` 不同）——v1 冻结决策。T2/T3 不要改成 panic 或 unknown 字符串。

---

## 7. 文件清单（T1 轨道产出）

### 7.1 新增 codec/ 包

- `codec/schema.go` + `schema_test.go` — 类型 + LoadSchema + Validate（T1.1）。
- `codec/errors.go` — LoadErrorMap + DescribeError（T1.1）。
- `codec/registry.go` + `registry_test.go` — 四张注册表 + AlgoMeta/Algorithms（T1.2）。
- `codec/ciphers.go` / `compressors.go` / `checksums.go` / `hashes.go` — 19 算法迁移（T1.2）。
- `codec/compile.go` + `compile_test.go` — 编译层 SchemaCodec（T1.3）。
- `codec/engine.go` — encode + decode + 访问器（T1.4/T1.5）。
- `codec/engine_test.go` + `decode_test.go` + `decode_helpers_test.go` — 对拍矩阵 + 失败语义 + Params/KeyLen（T1.4/T1.5）。
- `codec/preview.go` + `preview_test.go` — Preview helper（T1.7）。
- `codec/engine_bench_test.go` — encode/decode 基准（T1.7）。
- `codec/migration_test.go` — 生产 codec.json/errors.json 自验（T1.6）+ 全量 verbatim 校验（T1.7）。
- `codec/testdata/{tcp_logic,aes_ecb}_codec.json + errors.json` — 对拍 fixture。

### 7.2 adapter 包装

- `adapter/schema_adapter.go` + `schema_adapter_test.go` — NewSchemaAdapter 9 方法委托（T1.5）。

### 7.3 生产迁移产物（conf/adapter/）

- `conf/adapter/tcp_logic_codec.json` / `tcp_battle_codec.json` / `udp_battle_codec.json` — 每连接 codec 文件（T1.6）。
- `conf/adapter/errors.json` — 共享错误码（667 条，T1.6 生成 + T1.7 全量校验）。

### 7.4 计划与报告

- `plans/declarative-codec/briefs/t1-{1..7}-brief.md` — 各任务 brief。
- `plans/declarative-codec/reports/t1-{1..7}-report.md` — 各任务报告。
- `plans/declarative-codec/reports/t1-freeze-handoff.md` — 本文件（T1 冻结交接）。

---

## 8. 冻结后的变更纪律

T1 冻结后，**T2/T3/T4 不得修改 codec/ 包的对外契约**（`CodecSchema`/`SchemaCodec`/`NewSchemaAdapter`/`Preview`/`Algorithms`/`LoadSchema`/`LoadErrorMap` 的签名与语义）。如确需扩展（如 v1.1 state/counter/timestamp），开新 brief + 新轨道，不在 T2–T4 内顺手改。

冻结点的「不变量」清单（任何回归都视为冻结破坏）：

- `go build ./...` + `go vet ./...` + `go test ./... -count=1` 全绿。
- encode/decode 对 codec.lua 字节级一致（对拍矩阵全 PASS）。
- `codec/` 包零 `gopher-lua` import（生产代码）。
- `DecodeTCP`/`DecodeUDP` 3-tuple 签名不变。
- `errors.json` 未命中 → 空串（不 panic / 不 unknown）。
