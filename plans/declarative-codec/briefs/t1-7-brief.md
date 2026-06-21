# T1.7 Brief — 对拍/基准闭环 + preview helper + 冻结交接（T1 收尾）

> 你是 implementer。先读本 brief。T1 的收尾任务：闭环三个 review 遗留、补齐对拍与基准、提供 preview helper、写冻结交接说明。
> 参考：`plans/declarative-codec/01-track-codec-engine.md` §4.8/§4.9/§4.10、总纲 §5（全局验收）、`progress-ledger.md`（T1.1-T1.6 完成记录与遗留项）。
> 工作目录：worktree 根。**不要 git commit**。

## 目标（五件事）

### 1. 闭环三个 review 遗留（小改）

- **(a) UDP 压缩+加密 对拍缺口**（来自 T1.5 review）：`codec/decode_test.go` 当前把 `udp_compressible_encrypted_offset11` 排除在 UDP decode 对拍矩阵外。**改为纳入**：构造一个 UDP 变体 schema，其 gzip 步 `onError:"keep"`（engine 已支持），把该用例加回矩阵，证明与 codec.lua decode（lenient，pcall 吞 gzip 失败返 garbled body）**字节一致**。若 codec.lua 对合法 UDP 压缩+加密帧也是正常解压返回，则对拍合法帧即可；若 codec.lua 行为是 keep，用 keep 变体对拍。以对拍通过为准。
- **(b) godoc 修正**（来自 T1.5 review）：`codec/engine.go` `verifyProducesAfterDecrypt` 的注释把「codec.lua decode 根本不校验 bcc」与「不对称时跳过」混为一谈。改为准确表述：**codec.lua decode 完全不校验 bcc；engine 在 `encOffset==decOffset` 时额外校验（按 step.onError fail/keep），不对称时（如 UDP 11/0）数学上无法校验故跳过（与 codec.lua 一致）。**
- **(c) errors.json 全量校验**（来自 T1.6 review）：新增测试，**解析 `conf/adapter/error.lua` 的 `errors = {...}` 表**（纯文本解析，不依赖 LuaAdapter/zap），断言 `conf/adapter/errors.json` 全部 667 对 code→desc **verbatim 一致**（覆盖 T1.6 仅抽样 8 条的缺口）。

### 2. 对拍测试套件收口（总纲 §5 主验收）

确保 `codec/` 下对拍测试覆盖矩阵齐全（多数已在 T1.4/T1.5/T1.6，查漏补缺）：
- encode：TCP/UDP、加密/不加密、压缩/不压缩、cmd=0、空 body、UDP offset 11、TCP offset 0、bcc。
- decode：同矩阵 + headerErr 非零 + 失败语义（坏 gzip/坏 bcc/缺 key × fail/keep）。
- BodyLength、ExpectedRouteKey 与 helpers/lua_adapter 一致。
对拍真值 = `adapter.LuaAdapter`（`conf/adapter/codec.lua`+`error.lua`，pool≥2 避免 pool=1 死锁）。断言全 `bytes.Equal`/字段相等，非空转。

### 3. 基准（量化验收，总纲 §0 第 1 项）

新增 `codec/engine_bench_test.go`（或合并入现有 bench 文件）：
- `BenchmarkSchemaCodec_Encode` / `BenchmarkSchemaCodec_Decode`：小/中/大 body（如 64B/2KB/16KB）× 加密/压缩组合。
- 对照基准 `BenchmarkLuaAdapter_Encode` / `BenchmarkLuaAdapter_Decode`（旧路径，经 adapter.LuaAdapter）。
- 报告 `ns/op` 与 `allocs/op`，**记录新 Go 引擎相对旧 Lua 路径的倍率**（写进报告；总纲要「明确倍率」而非「预期更快」）。allocs/op 下降、大 body 提速是预期。

### 4. preview helper（T4 的 `POST /sbot/codec/preview` 调用，T1 §4.8）

新增纯计算 preview（无文件/网络/任务副作用），供 T4 Admin endpoint 包装。建议放 `codec/preview.go`：

```go
type PreviewField struct {
	Name   string `json:"name"`
	Value  uint64 `json:"value"`        // 数值化（按字段 size）
	Offset int    `json:"offset"`
	Size   int    `json:"size"`
}
type PreviewResult struct {
	Mode      string        `json:"mode"`       // "encode"|"decode"
	FrameHex  string        `json:"frameHex,omitempty"`   // encode 出参
	BodyHex   string        `json:"bodyHex,omitempty"`    // decode 出参
	RouteKey  string        `json:"routeKey,omitempty"`
	HeaderErr uint64        `json:"headerErr,omitempty"`
	Fields    []PreviewField `json:"fields,omitempty"`
	Error     string        `json:"error,omitempty"`      // schema 编译/运行错误（中文）
}

// Preview：schema 编译 + 单次 encode 或 decode。schema 编译失败或参数非法 → 填 Error 返回（不 panic）。
// mode=encode: route+bodyHex+keyHex → FrameHex(+Fields 逐字段解释)
// mode=decode: frameHex+keyHex → BodyHex+RouteKey+HeaderErr(+Fields)
func Preview(schema *CodecSchema, mode, transport string, route map[string]any, bodyHex, keyHex, frameHex string) PreviewResult
```

> transport 当前不影响单 codec 计算（codec 单 transport，offset 已在 schema 里）；保留入参为 T3/T4 语义清晰。route 字段值支持 int/float/string（数值化）。加 `codec/preview_test.go`：合法 encode/decode 往返、畸形 schema 填 Error、坏 hex 填 Error。

### 5. 冻结交接说明（T1 §4.10）

写 `plans/declarative-codec/reports/t1-freeze-handoff.md`，**给 T2/T3/T4 的契约清单**（一句话级，准确）：
- `adapter.NewSchemaAdapter(schema *codec.CodecSchema, errorMap map[uint64]string) (Adapter, error)`；Adapter 9 方法签名零改动；decode 3-tuple 不返回 err；`Close()` no-op 幂等。
- `codec.LoadSchema(path)` / `codec.LoadErrorMap(path)` / `codec.NewSchemaCodec`；schema Validate 中文错误。
- `codec.Algorithms() []AlgoMeta`（供 `GET /sbot/codec/algorithms` 与 T3 下拉）；`codec.Preview(...)`（供 `POST /sbot/codec/preview`）。
- codec 绑定粒度：每连接 `<proto>:<service>` 一份 `*_codec.json` + 共享 `errors.json`；resolver key = `server` 串；无 fallback。
- 已对拍 codec.lua 字节一致（encode/decode 全矩阵）；benchmark 倍率（填实测）。
- 已知遗留/边界：bcc decode 仅对称校验（codec.lua 不校验）；UDP 压缩+加密 用 onError:keep 对齐；块密码变长（v1 不用）；`-race` 未跑（无 gcc），并发安全为结构性论证（不可变 SchemaCodec）。

## 关键约束

- 不改 T1.1-T1.6 已 review 通过的**行为**（encode/decode 对拍不能回退）；仅做：闭环 (a)/(b)/(c)、补对拍/基准、加 preview、写文档。`(b)` 是注释改；`(a)` 是加测试 + 可能加一个 keep 变体 helper（不改 engine 生产逻辑）；`(c)` 是加测试。
- preview 不引入 gopher-lua；纯 Go + codec。
- `go build ./...`、`go vet ./...`、`go test ./... -count=1` 全绿、输出干净。
- **不要 git commit。** 本任务完成后 T1 全部就绪，controller 会拿 T1 批次给你确认提交。

## 验收（self-review）

- (a) UDP 压缩+加密 对拍纳入并通过；(b) godoc 已改准；(c) errors.json 全量 verbatim 校验通过。
- 对拍矩阵齐全，全字节级 PASS。
- benchmark 有新 vs 旧 ns/op+allocs/op 倍率记录。
- preview helper 编译、往返测试通过、畸形输入填 Error 不 panic。
- 冻结交接文档准确反映当前代码。
- 全 repo `go test ./... -count=1` 绿。

## 报告

写完整报告到 `plans/declarative-codec/reports/t1-7-report.md`：三遗留闭环情况、对拍矩阵最终覆盖、benchmark 倍率表、preview helper 设计、冻结交接文档路径、TDD/验证证据、改动文件、self-review、concerns。
返回（<15 行）：Status、改动文件、一行测试摘要、concerns、报告路径。
