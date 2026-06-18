# T1.5 Brief — decode 引擎 + Params/KeyLen 修复 + adapter.NewSchemaAdapter 包装

> 你是 implementer。先读本 brief。参考 `plans/declarative-codec/01-track-codec-engine.md` §3.3（decode 执行）、§3.2（adapter 包装）、§3.5（DescribeError）、总纲 §3.1.4（方向语义/失败语义）、§3.2（adapter 接口）。
> 对拍基准：`adapter/lua_adapter.go`（Decode 行为）、`conf/adapter/codec.lua`（decode 真值）、`adapter/lua_crypto.go`。
> 工作目录：worktree 根。**不要 git commit**。

## 目标（三件事）

1. **decode 引擎**：`codec/engine.go` 增加 `SchemaCodec.DecodeTCP/DecodeUDP`（3-tuple，flag 驱动反序，onError fail/keep）。
2. **Params/KeyLen 修复（T1.4 遗留的真潜伏 bug）**：给 `compiledStep`（compile.go）补 `params map[string]any` + `keyLen int`，`compileStep` 从 `PipelineStep.Params/KeyLen` 填充；encode（engine.go）改用 `step.params`/`step.keyLen` 替换 T1.4 的 nil params 与硬编码 `len(key)==32`；decode 同样用之。
3. **adapter 包装**：新增 `adapter/schema_adapter.go`，`NewSchemaAdapter(schema, errorMap) (Adapter, error)` 实现 `adapter.Adapter` 全 9 方法，委托 `*codec.SchemaCodec`。

## 改动文件

- `codec/compile.go`：`compiledStep` 加 `params`/`keyLen` 字段；`compileStep` 填充（**仅这两处增量改动，不动其它编译逻辑**）。
- `codec/engine.go`：加 decode 方法；encode 改用 step.params/keyLen（替换 nil/hardcode）；加 `func (c *SchemaCodec) DescribeError(code uint64) string`（委托 errors.go 的 DescribeError + c.errorMap，供 wrapper）。
- `adapter/schema_adapter.go`（新）：SchemaAdapter + NewSchemaAdapter + 9 方法。
- `codec/engine_test.go`：加 decode 对拍测试；加 Params/KeyLen 非默认值测试（如 rol=5、keyLen=16 用 aes，验证 encode 用了正确参数——可与 LuaAdapter 对拍或已知向量）。
- 可选 `adapter/schema_adapter_test.go`：wrapper 委托测试。

## decode 算法（逐字复刻 codec.lua `decode_tcp`/`decode_udp`）

签名固定 3-tuple（**签名零改动**）：

```go
func (c *SchemaCodec) DecodeTCP(data, secretKey []byte) (routeKey string, body []byte, headerErr uint64)
func (c *SchemaCodec) DecodeUDP(data, secretKey []byte) (routeKey string, body []byte, headerErr uint64)
```

1. `len(data) < headerSize`（或 +trailerSize）→ 返回 `("", nil, 0)`。
2. 读头：route 字段值、`errorCode`→`headerErr`、`flags`（解析命名位掩码）。
3. `body = data[headerSize : len(data)-trailerSize]`。
4. **管线反序执行**，是否执行**只看该步 flag 位是否在解码 flags 中置位**（**绝不重算 when/guards/minBodyLen/onlySmaller**）；encrypt 另要求 key 在场（`!requireKey || len(key)>=step.keyLen`）：
   - 反序：先 decrypt（若 encrypted 位置位）→ 再 decompress（若 compressed 位置位）。与 codec.lua decode 顺序一致。
   - encrypt：`step.impl.(Cipher).Decrypt(body, key, step.decOffset, step.params)`。
   - compress：`step.impl.(Compressor).Decompress(body)`。
5. **checksumOut 校验（bcc 真值，T1.4 已证）**：若存在 checksumOut 字段且其产生步（encrypt）的 flag 置位，则在**解密后**对 `body[decOffset:]`（现协议 decOffset=0 → 整 body）用 produces 算法（xor8）重算，与头里 checksumOut 字段值比对。不一致 → 按该步 `onError` 处理。
6. **失败语义**：任一步失败（解压报错 / 解密异常 / bcc 校验不过 / flag 置位但 key 缺失且 requireKey）按该步 `onError`：
   - `fail`（默认）→ 返回 **空 routeKey `""`**，body 不外泄（`nil` 或不返回乱码），`headerErr` 仍为读出的值。**不返回 err**（签名约束）。
   - `keep` → 保留当前字节继续后续步骤。
7. 用 `routeKeySegs` + 解出的 route 字段值拼 routeKey，返回 `(routeKey, body, headerErr)`。

> bcc 解码区 = `body[decOffset:]`（解密后明文）。与 encode 的 `body[encOffset:]`（加密前明文）是**不同帧/不同方向**：encode 是发包（offset 11）、decode 是收包（offset 0）。各自自洽。逐字节对拍 LuaAdapter.Decode 是最终判据——若你的 bcc 区理解有偏差，对拍会失败，以对拍为准。

## Params/KeyLen 修复

- `compile.go` `compiledStep` 增加：
  ```go
  params map[string]any  // 来自 PipelineStep.Params（透传给算法 impl）
  keyLen  int            // 来自 PipelineStep.KeyLen（encrypt key 长度要求；0 表示不校验）
  ```
- `compileStep` 填充：`params: st.Params`、`keyLen: st.KeyLen`。
- `engine.go` encode：`ciph.Encrypt(body, key, step.encOffset, step.params)`（替换 nil）；key 长度判定用 `step.keyLen`（替换硬编码 32）：`step.keyLen==0 || len(key)>=step.keyLen`。
- decode 同理用 `step.params`/`step.keyLen`。
- 加测试：构造 rol≠3（如 rol=5）的 xor_carry_rol schema，对拍 LuaAdapter（lua_crypto 支持 rol 参数？读 lua_crypto 确认；若 LuaAdapter 走 codec.lua 的 rol=3 固定，则用已知向量或 aes keyLen=16 对拍）——关键是证明 encode/decode **读取了 schema 的 params/keyLen** 而非硬编码。

## adapter.NewSchemaAdapter 包装（adapter/schema_adapter.go）

```go
package adapter

type SchemaAdapter struct{ c *codec.SchemaCodec }

// 编译 + 包装。LoadSchema/LoadErrorMap 由调用方（T4 loader）先做；本函数收 *CodecSchema。
func NewSchemaAdapter(schema *codec.CodecSchema, errorMap map[uint64]string) (Adapter, error) {
	sc, err := codec.NewSchemaCodec(schema, errorMap)
	if err != nil { return nil, err }
	return &SchemaAdapter{c: sc}, nil
}

func (a *SchemaAdapter) HeaderSize() int { return a.c.HeaderSize() }
func (a *SchemaAdapter) BodyLength(header []byte) int { return a.c.BodyLength(header) }
func (a *SchemaAdapter) ExpectedRouteKey(route any) string { return a.c.ExpectedRouteKey(route) }
func (a *SchemaAdapter) EncodeTCP(route any, body, key []byte) []byte { return a.c.EncodeTCP(route, body, key) }
func (a *SchemaAdapter) EncodeUDP(route any, body, key []byte) []byte { return a.c.EncodeUDP(route, body, key) }
func (a *SchemaAdapter) DecodeTCP(data, key []byte) (string, []byte, uint64) { return a.c.DecodeTCP(data, key) }
func (a *SchemaAdapter) DecodeUDP(data, key []byte) (string, []byte, uint64) { return a.c.DecodeUDP(data, key) }
func (a *SchemaAdapter) DescribeError(code uint64) string { return a.c.DescribeError(code) }
func (a *SchemaAdapter) Close() {}  // no-op，幂等
```

> 先读 `adapter/adapter.go` 确认 `Adapter` 接口的**确切方法集与签名**（9 方法），包装须逐字匹配。加编译期断言 `var _ Adapter = (*SchemaAdapter)(nil)`。

## 关键约束

- **decode 对拍 codec.lua**：`DecodeTCP/UDP` 的 `(routeKey, body, headerErr)` 与 LuaAdapter.Decode **完全一致**（覆盖加密/压缩/cmd=0/空 body/UDP decode offset 0/headerErr 非零）。核心验收。
- **失败语义**：坏 gzip / 篡改 bcc / 缺 key 解密 → `fail` 返回空 routeKey（body 不外泄）；`keep` 保留原字节。加专门测试。
- **签名零改动**：Decode 仍 3-tuple，不返回 err。
- compile.go 仅加 2 字段 + 填充，不改其它；engine.go encode 行为**不变**（对拍仍全绿——改 params/keyLen 后 rol=3/keyLen=32 输出应与之前一致）。
- `adapter/schema_adapter.go` 是 adapter 包新文件；不改 adapter 现有文件（lua_adapter.go 等留给 T2/T4 删）。
- 不 import gopher-lua（adapter/schema_adapter.go 只 import codec）。
- **不要 git commit。**

## 工作方式（TDD）

1. RED：decode 对拍测试（LuaAdapter.Decode oracle，组合矩阵）+ 失败语义测试（坏 gzip/坏 bcc/缺 key）+ Params/keyLen 非默认测试 + wrapper 委托测试。
2. GREEN：实现 decode + 修复 + wrapper，全绿。
3. `go build ./...`（含 adapter，验证 wrapper 编译）、`go vet ./codec/... ./adapter/...`、`go test ./codec/... ./adapter/... -count=1` 全绿、输出干净。
4. 确认 encode 对拍仍全绿（Params 修复未回退 T1.4）。
5. **不要 git commit。**

## 验收（self-review）

- decode 对拍 codec.lua 全组合字节级一致（routeKey/body/headerErr）。
- 失败语义：fail 返回空 routeKey+不外泄 body；keep 保留。
- bcc 解码区 = body[decOffset:]（解密后）；对拍印证。
- Params/KeyLen 从 schema 读取，非默认值测试通过；encode 对拍未回退。
- `adapter.NewSchemaAdapter` 实现 9 方法，`var _ Adapter` 断言通过；`go build ./...` 过。
- codec/adapter 生产代码无 gopher-lua（测试可 import）。

## 报告

写完整报告到 `plans/declarative-codec/reports/t1-5-report.md`：decode 对拍组合与结果、失败语义用例、Params/KeyLen 修复前后对比、wrapper 接口核对、TDD RED/GREEN、改动文件、self-review、concerns。
返回（<15 行）：Status、改动文件、一行测试摘要、concerns、报告路径。
