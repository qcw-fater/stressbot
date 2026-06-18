# T1.1 报告 — codec schema 类型 + Validate + errors

> 实现 T1.1（Track 1 基础）。TDD：先 RED（schema_test.go 编译失败），后 GREEN（errors.go + schema.go）。
> 工作目录：worktree-declarative-codec。**未 git commit**（controller 批量确认）。

## 实现了什么

### 新增文件（包 `codec/`，纯 Go，不 import gopher-lua）

1. **`codec/schema.go`** — schema 类型 + JSON 反序列化 + `Validate`。
   - 类型（与 brief §确切类型 逐字一致，所有导出字段带 camelCase json tag）：
     `CodecSchema`、`FrameSpec`、`Field`、`FlagBit`、`ValueSource`、
     `PipelineStep`、`StepOffset`、`StepProduce`、`OverSpec`、`StepCond`、`Guard`。
   - `LoadSchema(path string) (*CodecSchema, error)`：读文件 → `json.Unmarshal` → `Validate`；不做任何 codec.lua 兼容。
   - `func (s *CodecSchema) Validate() error`：聚合多条中文错误，一次性返回（`; ` 分隔）。
   - JSON tag 修正点：brief 中 `ValueSource`/`OverSpec` 用了 `a,omitempty,b,omitempty` 示意写法，按 brief 注释改为每字段独立单 tag（`Start int64 \`json:"start,omitempty"\`` 等）。
2. **`codec/errors.go`** — `LoadErrorMap(path) (map[uint64]string, error)` + `DescribeError(m, code) string`。
   - key 用 `strconv.ParseUint` 解析为 uint64；非数字 key 报错。
   - **`DescribeError` 未命中返回 `""`**（brief 冻结默认，nil map 同样返回 `""`）。
3. **`codec/schema_test.go`** — 59 个测试，覆盖 brief §工作方式 与 §验收 的全部用例。
4. **`codec/testdata/tcp_logic_codec.json`** — 总纲 §3.1 的 `tcp:logic` codec（`LoadSchema` 成功用例 fixture）。
5. **`codec/testdata/errors.json`** — 总纲 §3.3 示例（`{0:成功, 1:数据库错误, 2:协议解析错误, 19:消息解密失败}`）。

### Validate 规则覆盖（逐条对应 brief）

**基础**：`Version==1`；`EndianDefault ∈ {le,be}`（非空）；`HeaderSize>0`；`TrailerSize>=0`；`RouteKeyTmpl` 非空。

**字段（Header）**：
- `Name` 非空且唯一。
- `Offset>=0`、`Size>0`、`Offset+Size<=HeaderSize`。
- 物理区间 `[Offset, Offset+Size)` 不重叠（按 start 排序后扫描相邻；位域共享同一整数不算重叠——bits 在同一 Field 内）。
- `Type` 合法（`u8/u16/u24/u32/u64/i8/i16/i24/i32/i64/f32/f64/bytes`）；固定宽度类型 size 必须匹配（u32→4 等）；`bytes` 需显式 size>0。
- `Role` 合法（`length|route|errorCode|flags|checksumOut|value|reserved`）。
- `Endian`（若指定）必须 `le|be`。

**role**：必有且仅 1 个 `length`；≥1 个 `route`；`flags` 的 bits 在 `[0, Size*8)`、bit 不重复、name 不重复；`checksumOut.from` 非空且匹配 `^([A-Za-z_]\w*)\.([A-Za-z_]\w*)$`。

**routeKeyTemplate**：所有 `{name}` 占位必须是某 `role:"route"` 字段名（未知占位或指向非 route 字段均报错）。

**pipeline**：
- `Name` 唯一；`Op ∈ {compress,encrypt,checksum,hash}`；`Algo` 非空（**不校验算法存在性**——留给 T1.3 编译层，符合 brief 明示边界）。
- `Flag` 非空时必须对应某 `flags` 字段的命名位；**同一命名 flag 位至多被一个 step 绑定**。
- **凡带 `When`（非 nil）的 step 必须绑定 `Flag`**（encode 决策无处记录、decode 无法复现）。
- `encrypt`：`Offset` nil 视为 `{0,0}`；`Encode/Decode>=0`。
- `produces`：name 在该 step 内唯一；`Region ∈ {ciphered,bodyPlain,bodyFinal,header,frame}`。
- `Over`（独立 checksum/hash 步）：`Kind ∈ {bodyPlain,bodyFinal,header,frame,range}`；`range` 时校验 `RangeStart>=0`、`RangeEnd>=0`、`RangeEnd>=RangeStart`。
- `When.AppliesWith` 非空时必须指向已存在 step。
- `OnError` 非空时 `∈ {fail,keep}`（空视为 fail，不报错）。
- `Guards[].Op ∈ {eq,neq,gt,gte,lt,lte}`。

**跨 header↔pipeline 引用**：`checksumOut.from` 的 `<step>.<output>`：step 必须存在、其 `Produces` 必须含该 output 名。

**v1 显式拒绝**：`ValueSource.Kind ∈ {state,counter,timestamp}` → 报「v1 不支持的头字段取值源 kind=<...>，留待 v1.1」；未知 `Kind` 报错。
> 对 `role:"value"` 字段缺 `Source`、`reserved` 写 0 等，brief 末注明确「v1 不强制报错，避免过度校验」，未加额外约束。

## 测试与结果

### TDD RED 证据

```
$ go test ./codec/...
# stressbot/codec [stressbot/codec.test]
codec\schema_test.go:13:21: undefined: CodecSchema
codec\schema_test.go:14:10: undefined: CodecSchema
codec\schema_test.go:17:10: undefined: FrameSpec
...
codec\schema_test.go:34:74: too many errors
FAIL    stressbot/codec [build failed]
FAIL    stressbot/codec
```
（实现 schema.go/errors.go 前的状态——类型/函数均未定义。）

### TDD GREEN 证据

```
$ go build ./codec/...        # BUILD OK
$ go vet   ./codec/...        # VET OK
$ go clean -testcache && go test ./codec/...
ok  	stressbot/codec	0.428s
```
59 个测试全部 PASS（`go test -v` 显示 59 条 `--- PASS:`，无 FAIL/SKIP）。

### 验收命令全绿

| 命令 | 结果 |
|---|---|
| `go build ./codec/...` | BUILD OK |
| `go vet ./codec/...` | VET OK（无告警） |
| `go test ./codec/...` | `ok stressbot/codec 0.428s`（59/59 PASS） |
| `go build ./...`（全量） | exit 0（codec/ 之外零改动确认） |
| `go list -deps ./codec/... \| grep gopher-lua` | 无输出（无 gopher-lua 依赖） |

### 测试用例清单（按 brief §4.9 / 验收）

- 基础：`VersionMustBe1`、`EndianDefaultRequired/Legal`、`HeaderSizePositive`、`TrailerSizeNonNegative`、`RouteKeyTemplateRequired`、`ValidBaseline`。
- 字段：`FieldNameUnique/NonEmpty`、`FieldOffsetNonNegative`、`FieldSizePositive`、`FieldBoundsOffsetPlusSize`、`FieldOverlap`、`FieldTypeUnknown`、`FieldTypeSizeMismatch`、`FieldTypeBytesRequiresSize`、`FieldRoleUnknown`。
- role：`ExactlyOneLength`、`NoLength`、`NoRoute`、`FlagsBitOutOfRange/Duplicate/NameDuplicate`、`ChecksumOutFromMalformed/Empty`。
- routeKeyTemplate：`RouteKeyTemplateUnknownPlaceholder`、`RouteKeyTemplateNonRoutePlaceholder`。
- pipeline：`StepNameDuplicate`、`StepOpUnknown`、`StepAlgoRequired`、`StepFlagMissing`、`StepFlagSharedByTwoSteps`、`StepWhenRequiresFlag`、`EncryptOffsetNegative`、`ProduceNameDuplicateInStep`、`ProduceRegionUnknown`、`OverKindUnknown`、`OverRangeInvalid/NegativeStart`、`WhenAppliesWithMissing`、`OnErrorUnknown`、`GuardOpUnknown`、`ChecksumOutFromPointsMissingStep/Produce`。
- v1 拒绝：`V1RejectsStateSource`、`V1RejectsCounterSource`、`V1RejectsTimestampSource`、`V1RejectsUnknownSourceKind`、`SourceKindRouteAccepted`（正向）。
- LoadSchema：`Success`、`MissingFile`、`BadJSON`、`InvalidFailsValidate`。
- LoadErrorMap/DescribeError：`LoadErrorMap_Success/BadKey/MissingFile`、`DescribeError_Hit/MissReturnsEmpty/NilMap`。

## 改动文件

新增（全部在 worktree 内）：
- `codec/schema.go`
- `codec/errors.go`
- `codec/schema_test.go`
- `codec/testdata/tcp_logic_codec.json`
- `codec/testdata/errors.json`
- `plans/declarative-codec/reports/t1-1-report.md`（本报告）

未改动：`adapter/`、`engine/`、`robot/`、`network/`、`conf/` 或任何其它包。`go build ./...` 全绿确认零对外行为变更。

## self-review 发现

对照 brief §验收 逐项核对：

- [x] 类型字段与 brief 逐字一致；所有导出字段有 camelCase json tag。
- [x] Validate 每条规则都有对应测试（见上方清单）。
- [x] v1 拒绝 state/counter/timestamp 的测试存在且通过。
- [x] LoadSchema 不容忍 codec.lua；`Version!=1` 报错（`TestLoadSchema_InvalidFailsValidate` + `TestValidate_VersionMustBe1`）。
- [x] `DescribeError` 未命中返回 `""`（`TestDescribeError_MissReturnsEmpty` + `NilMap`）。
- [x] 包 `codec` 无 gopher-lua 依赖，`go build ./codec/...` 通过（`go list -deps` 验证）。
- [x] 未 import gopher-lua；未实现 encode/decode/compile/算法注册表；未改 adapter/ 或其它包。
- [x] 未 git commit。
- [x] 错误聚合（非短路）：单测只改一条规则时仍能精确命中对应子串，便于 T3 前端定位。

实现过程中一处权衡：`DescribeError(nil, …)` 返回 `""` 是 brief 的隐含要求（nil map 未命中），显式加了 nil 分支与测试固化，避免后续调用方传 nil 时 panic。

## concerns

1. **`Over.range` 校验未关联 `headerSize`/body 长度**：brief 仅要求「RangeStart/RangeEnd 合法」，且 body 长度在 Validate 阶段未知，故仅校验非负与 `End>=Start`。若 T1.3 编译层需要更紧的区间约束（如 `End<=headerSize` when Kind 对应 header），可在此之上补——本层保持宽松符合 brief。
2. **brief 注释里 ValueSource/OverSpec 的多键 JSON tag 写法**：明确按 brief 自己的修正注释处理为「每字段独立单 tag」，未引入自定义 UnmarshalJSON，行为与标准 `encoding/json` 一致。无功能影响，仅风格说明。
3. **`flagNameToField` 跨多 flags 字段同名位**：若两个 `role:"flags"` 字段都定义了同名命名位（schema 层未禁），引用解析能命中但无法区分属于哪个字段。本任务不做强约束（现协议只有一个 flags 字段）；若 T3 前端编辑器需要全局唯一 flag 名，可在 Validate 加一条规则——目前不影响 T1.3。
4. **`role:"value"` 字段缺 `Source` 不报错**：brief 末注明确「v1 不强制报错，避免过度校验」，已遵守。若后续发现 encode 阶段缺 source 会 panic，可回到此层补「value 必须有 source」规则。
