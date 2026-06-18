# T1.3 Brief — compile.go 编译层（schema → 不可变编译产物）

> 你是 implementer。先读本 brief（含须逐字使用的确切值）。参考 `plans/declarative-codec/01-track-codec-engine.md` §3.3（编译产物结构）、§4.3、总纲 §3.1.4（方向语义/失败语义）。
> 工作目录：worktree 根。**不要 git commit**。

## 目标

新增 `codec/compile.go`：把 `*CodecSchema`（T1.1）**一次性编译**成不可变的 `*SchemaCodec`，期间完成所有「字符串 → 索引/掩码/实现」预解析。**算法存在性校验在本任务首次落地**（T1.1 刻意推迟）：缺算法编译期 fail loud。

本任务**只做编译**，**不实现** encode/decode/BodyLength/ExpectedRouteKey 执行方法（T1.4/T1.5 在 `engine.go` 里以同包方法形式追加）。`SchemaCodec` 本任务只定义结构体 + 构造函数 + 必要的只读访问器（如 `HeaderSize()`）。

## 新增文件

- `codec/compile.go`：编译产物类型 + `NewSchemaCodec` 构造 + 预解析逻辑。
- `codec/compile_test.go`：TDD。

## 编译产物类型（逐字基线，可按实现需要增辅助类型，但下列字段语义不变）

```go
package codec

// SchemaCodec：编译产物，构造后只读 → 并发安全、无锁（兑现不变量 2）。
type SchemaCodec struct {
	headerSize            int
	trailerSize           int
	lengthIncludesHeader  bool
	lengthIncludesTrailer bool
	lengthField           compiledField   // 来自 role:"length"（单一来源）
	fields                []compiledField // route/errorCode/flags/checksumOut/value/reserved
	routeKeySegs          []routeSeg      // 模板预解析：literal | {fieldIdx}
	steps                 []compiledStep
	errorMap              map[uint64]string
	// 无任何可变字段
}

type compiledField struct {
	offset, size int
	kind         fieldKind        // 由 Type 解析：u8/u16/.../bytes
	endian       binary.ByteOrder // le|be（缺省回退 EndianDefault）
	role         roleKind         // length|route|errorCode|flags|checksumOut|value|reserved
	flagBits     []int            // role:flags 持有的位索引（来自 Bits[].Bit）
	checksumRef  stepProduceRef   // role:checksumOut：预解析 (stepIdx, produceName)
	source       compiledValueSource // role:value（v1 const/route）
	name         string           // 原字段名（调试/错误信息）
}

type stepProduceRef struct {
	stepIdx    int    // -1 表示无引用
	produceName string
}

type compiledValueSource struct {
	kind  string // const|route（v1；state/counter/timestamp 已在 Validate 拒绝）
	value int64  // const
	key   string // route
}

// routeKey 模板段：字面量或字段引用
type routeSeg struct {
	literal  string // segKindLiteral 时有效
	fieldIdx int    // segKindField 时：指向 fields 中某 route 字段下标
}

type compiledStep struct {
	op         stepOp          // compress|encrypt|checksum|hash
	impl       any             // Cipher/Compressor/Checksum/Hasher（注册表 eager 查得）
	flagMask   uint64          // 无 flag 则 0（无条件步）
	encodeWhen compiledWhen    // **encode-only**；decode 路径不引用（契约 A）
	encOffset  int             // 单向 encode 偏移（契约 C；每份 codec 单 transport）
	decOffset  int             // 单向 decode 偏移
	produces   []compiledProduce
	onError    onErrorPolicy
	name       string
}

type compiledProduce struct {
	name   string
	algo   string // 原算法名（调试）
	impl   Checksum/Hasher 实现（按 produces.Algo 查 checksum/hash 注册表）
	region produceRegion // ciphered|bodyPlain|bodyFinal|header|frame
}

// compiledWhen：encode 期判定 step 是否生效（decode 不用——decode 纯看 flag 位）
type compiledWhen struct {
	enabled        bool // 无 When 时为「无条件」
	minBodyLen     int
	onlySmaller    bool   // 仅 compress：需在 encode 步内比对压缩前后大小，**不能在 applies() 预判**
	requireKey     bool
	appliesWithIdx int    // -1 无；指向依赖 step 的下标
	guards         []compiledGuard
}
type compiledGuard struct {
	fieldIdx int    // 指向某 route 字段下标
	op       string // eq|neq|gt|gte|lt|lte
	value    int64
}
```

> `fieldKind`/`roleKind`/`stepOp`/`onErrorPolicy`/`produceRegion` 可用 `int` 常量枚举或 `string`，自选干净的实现；但**枚举值集合与上表一致**。`compiledProduce.impl` 类型——上面对照写法不合法，实际用 `impl any` 或分别存 `checksumImpl Checksum`/`hasherImpl Hasher`，自选；要点：produces.Algo 必须能查到 checksum 或 hash 注册表实现，缺则编译期 fail。

## 构造函数

```go
// NewSchemaCodec 先 Validate，再编译。任何解析失败（缺算法、from/appliesWith 引用悬空等）
// 返回 error（中文信息，带 step/field/算法名）。
func NewSchemaCodec(schema *CodecSchema, errorMap map[uint64]string) (*SchemaCodec, error)

// 只读访问器（本任务可加，执行方法留给 engine.go）
func (c *SchemaCodec) HeaderSize() int
```

## 编译期必须完成的预解析（逐条）

1. 调 `schema.Validate()`；失败直接返回其 error。
2. 定位 `lengthField`：role:"length" 字段（Validate 保证恰一个）→ compiledField。
3. `EndianDefault` → `binary.LittleEndian`/`BigEndian`；各字段 endian 缺省回退之；解析 `kind` 由 `Type`，`endian` 由字段/默认。
4. flags：收集 `role:"flags"` 字段的 `Bits`，建 `bitName → mask`（1<<bit）；同时记录每字段 flagBits。
5. routeKey 模板解析：把 `RouteKeyTmpl` 切成 `[]routeSeg`——字面量段 + `{name}` 段（name 必须是某 route 字段名，Validate 已保证），`{name}` 段存指向该 route 字段下标。
6. 每个 step → compiledStep：
   - `op` 解析。
   - **`impl`**：按 `step.Algo` 查对应注册表（compress→Compressor、encrypt→Cipher、checksum→Checksum、hash→Hasher）。**缺失 → fail loud**：`fmt.Errorf("codec schema 编译失败：步骤 %q 引用未知算法 %q", step.Name, step.Algo)`（中文）。这正是 T1.1 推迟的算法存在性校验。
   - `flagMask`：step.Flag → 查 bitName→mask；无 Flag 则 0。
   - `encodeWhen`：编译 When（见下）。**带 When 的 step 必须有 Flag**（Validate 已保证），此处只编译。
   - `encOffset`/`decOffset`：encrypt 步从 `step.Offset`（nil→{0,0}）；其它 op 留 0。
   - `produces`：每个 produce 的 algo 查 checksum/hash 注册表（缺失 fail loud），存 impl + region；name 留调试。
   - `onError`：fail（默认，空）/keep → policy。
7. `checksumOut` 字段的 `From`(`"stepName.outputName"`) → 解析为 `stepProduceRef{stepIdx, produceName}`（Validate 已保证存在，此处查表得下标；若意外悬空 fail loud）。

## compiledWhen 的 onlySmaller 约束（重要）

`onlySmaller`（compress「仅当变小才采用」）**无法在编译期或 applies() 预判**——它取决于 encode 时压缩后的实际字节数。因此：
- `compiledWhen.applies(ctx)` 用于判定 `minBodyLen/requireKey/guards/appliesWith`（这些都可在 encode 入口判）。
- `onlySmaller` 不进 `applies()`；它在 T1.4 encode 的 compress 步内「先压缩、比对大小、变小才采用并置 flag、否则丢弃压缩结果」专门处理。
- 本任务只把 `onlySmaller` 存进 `compiledWhen`，并提供一个注释明确的 `applies(bodyLen int, hasKey bool, route map[string]any) bool`（不含 onlySmaller 判定）供 T1.4 调用。guards 的 fieldIdx 指向 route 字段，encode 时从 `route[name]` 读值比对。

## 关键约束

- **不可变**：`SchemaCodec` 构造后无任何可变状态；map/slice 构造后不再写入。这是「任意 goroutine 并发调用无锁」的前提。
- **fail loud**：缺算法、悬空引用一律编译期 error，绝不静默降级。
- 不改 T1.1/T1.2 文件；不实现 encode/decode/BodyLength/ExpectedRouteKey（T1.4/T1.5）；不 import gopher-lua。
- `NewSchemaCodec` 不读文件（文件读取是 LoadSchema 的职责）；它接收已 Load 的 `*CodecSchema`。

## 工作方式（TDD）

1. RED：`compile_test.go` 覆盖：
   - 合法 schema（用 T1.1 的 `testdata/tcp_logic_codec.json` 经 LoadSchema 喂入）编译成功；断言 lengthField、fields 数量/role、routeKeySegs 段数与字段下标、steps 数量、xor_carry_rol step 的 impl 非 nil 且 encOffset/decOffset=0、gz step flagMask 非 0。
   - 构造一个 udp:battle 等价 schema（encrypt offset encode=11/decode=0）断言 encOffset=11/decOffset=0。
   - 缺算法 schema（把某 step.Algo 改成 `"nope"`）→ NewSchemaCodec 返回含算法名的中文 error。
   - Validate 失败的 schema → 错误透传。
   - onlySmaller 存入 compiledWhen 且 applies() 不判 onlySmaller（构造 onlySmaller=true 的 compress 步，applies 在 bodyLen 满足时仍 true）。
2. GREEN：实现 compile.go。
3. `go build ./codec/...`、`go vet ./codec/...`、`go test ./codec/...` 全绿、输出干净。
4. **不要 git commit。**

## 验收（self-review）

- 编译产物只读、无可变字段。
- 所有字符串→索引/掩码/实现预解析在 NewSchemaCodec 内完成；热路径（T1.4/T1.5）不再查注册表/不做字符串解析。
- 缺算法 fail loud（中文，带 step 名 + 算法名）。
- encOffset/decOffset 正确分方向；udp:battle = 11/0。
- routeKeySegs 正确切分；checksumRef/produces/appliesWith 引用解析为下标。
- onlySmaller 不进 applies()。

## 报告

写完整报告到 `plans/declarative-codec/reports/t1-3-report.md`：实现内容、编译期做了哪些预解析、fail loud 用例、TDD RED/GREEN 证据、改动文件、self-review、concerns。
返回（<15 行）：Status、改动文件、一行测试摘要、concerns、报告路径。
