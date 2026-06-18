# T1.3 报告 — compile.go 编译层（schema → 不可变编译产物）

## 状态

**DONE**。`go build ./codec/...`、`go vet ./codec/...`、`go test ./codec/...` 全绿；repo 整体 `go build ./...` 干净。

## 改动文件

- `codec/compile.go`（新增，~440 行）：编译产物类型 + 枚举 + `NewSchemaCodec` + `applies()` + `HeaderSize()`。
- `codec/compile_test.go`（新增，~280 行）：7 个测试覆盖 brief 全部用例。

未改任何 T1.1/T1.2 文件、未碰 codec 包外代码、未 import gopher-lua、未 git commit。

## 实现内容

### 编译产物类型（与 brief 逐字基线一致）

- `SchemaCodec`：仅导出常量字段（`headerSize`/`trailerSize`/`lengthIncludesHeader/Trailer`/`lengthField`/`fields`/`routeKeySegs`/`steps`/`errorMap`），构造后无任何可变状态。
- `compiledField`：offset/size/kind/endian/role/flagBits/checksumRef/source/name。
- `compiledStep`：op/impl/flagMask/encodeWhen/encOffset/decOffset/produces/onError/name。
- `compiledWhen`：enabled/minBodyLen/onlySmaller/requireKey/appliesWithIdx/guards；附 `applies(bodyLen, hasKey, route map[string]any) bool`。
- `compiledProduce`：用 `checksumImpl Checksum` + `hasherImpl Hasher` 两个分离字段（任一非 nil），避免 `impl any` 丧失类型安全。
- 枚举用 `int` 常量（`fieldKind`/`roleKind`/`stepOp`/`onErrorPolicy`/`produceRegion`/`segKind`），集合与 brief 完全一致。

### 编译期预解析（逐条兑现 brief）

1. `schema.Validate()` 失败直接透传其 error（中文）。
2. `EndianDefault` → `binary.LittleEndian`/`BigEndian`，字段 endian 缺省回退之。
3. header 字段分流：`role:"length"` 进 `lengthField`，其余进 `fields`；route 字段记 `routeFieldIdx`。
4. flags：构建全局 `bitName → mask`（`1<<bit`）和 `flagBits`。
5. `routeKeyTemplate` 切分为 `[]routeSeg`：字面量段 + `{name}` 段（name 必须是 route 字段名 → 存 `c.fields` 下标）。
6. 每个 step：
   - `op` 解析；`impl` 按 op 查对应注册表（compress→Compressor、encrypt→Cipher、checksum→Checksum、hash→Hasher）。**缺失 → fail loud** 中文信息 `步骤 %q 引用未知算法 %q`。
   - `flagMask`：step.Flag → mask（Validate 保证存在）。
   - `encOffset`/`decOffset`：仅 encrypt 步从 `step.Offset`（nil→0/0）；其它 op 留 0。
   - `produces`：algo 先查 checksum 注册表，未命中再查 hash 注册表，**均未命中 fail loud** `步骤 %q 的 produces %q 引用未知算法 %q`。
   - `encodeWhen`：编译 When（minBodyLen/onlySmaller/requireKey/appliesWithIdx/guards）；guards 的 `fieldIdx` 指向 route 字段下标，同时存 `fieldName` 供 `applies()` 在 route map 中按名取值。
7. `checksumOut.from` 解析为 `stepProduceRef{stepIdx, produceName}`，Validate 已保证存在，此处查表得下标；意外悬空 fail loud。
8. `errorMap`：nil 入参 → 空 map（非 nil）；非 nil 入参浅拷贝隔离可变性（防止调用方后续修改入参影响编译产物）。

### onlySmaller 约束（关键）

`compiledWhen.onlySmaller` 仅在编译期存值；`applies()` **不引用** onlySmaller。它仅在 T1.4 encode 的 compress 步内「先压缩、比对大小、变小才采用并置 flag、否则丢弃压缩结果」处理。测试 `TestCompiledWhen_OnlySmaller_NotInApplies` 显式验证：onlySmaller=true 的 gz step，`applies(4096, false, nil)` 仍返回 true（minBodyLen 满足），仅当 minBodyLen 不满足时返回 false。

## TDD 证据

### RED（先写测试，确认编译失败）

7 个测试覆盖 brief 列出的全部用例：
- 合法 schema 编译成功：断言 lengthField、fields 数量/role、routeKeySegs 段数与字段下标、steps 数量、xor_carry_rol step 的 impl 非 nil 且 encOffset/decOffset=0、gz step flagMask 非 0、checksumRef 解析正确。
- udp:battle 等价（encOffset=11/decOffset=0）。
- 缺算法 step（algo="nope"）→ 中文 error 含 "gz"/"nope"。
- produces 引用未知 checksum 算法（algo="ghosthash"）→ 中文 error 含 "enc"/"ghosthash"。
- Validate 失败（HeaderSize=0）→ error 透传。
- onlySmaller 存入 compiledWhen 且 applies() 不判。
- endian 默认回退（errCode 未指定 endian → EndianDefault）。

### GREEN

`go test ./codec/...`：**ok stressbot/codec 0.517s**（含本任务的 7 个 + T1.1/T1.2 已有测试）。

## Fail loud 用例

| 场景 | 错误信息（节选） |
|------|------------------|
| step.Algo 未知 | `codec schema 编译失败：步骤 "gz" 引用未知算法 "nope"` |
| produces.Algo 未知 | `codec schema 编译失败：步骤 "enc" 的 produces "bcc" 引用未知算法 "ghosthash"（checksum/hash 注册表均未命中）` |
| Validate 失败 | 直接透传 schema.Validate 的聚合错误 |
| checksumOut.from 悬空（理论，Validate 保证不发生） | `checksumOut 字段 %q 的 from %q 指向不存在的 step %q` |
| schema == nil | `codec schema 编译失败：schema 为空` |

## Self-review（对照 brief 验收）

- ✅ **不可变**：`SchemaCodec` 无任何可变字段；map/slice 构造后不再写入；`errorMap` 浅拷贝隔离入参。
- ✅ **热路径无解析**：所有字符串→索引/掩码/实现预解析在 `NewSchemaCodec` 内完成，T1.4/T1.5 不再查注册表/不做字符串解析（仅 `applies()` 在调用方 runtime 读 route map 值——这是必要的数据传递，非解析）。
- ✅ **缺算法 fail loud**：中文信息，含 step 名 + 算法名。
- ✅ **encOffset/decOffset 方向**：仅 encrypt 步从 `step.Offset` 取；udp:battle 等价 = 11/0；compress 步为 0/0。
- ✅ **routeKeySegs 切分**：`{cmd}:{act}` → 3 段，field 段存 c.fields 下标。
- ✅ **checksumRef/produces/appliesWith 解析为下标**。
- ✅ **onlySmaller 不进 applies()**。
- ✅ **HeaderSize() 只读访问器**已加。
- ✅ **未实现** encode/decode/BodyLength/ExpectedRouteKey（留给 T1.4/T1.5）。
- ✅ **未改** T1.1/T1.2 文件、未 import gopher-lua。

## 设计决策与说明

1. **`compiledProduce` 用分离字段**（`checksumImpl` + `hasherImpl`）而非 `impl any`：保留类型安全，T1.4/T1.5 执行时直接调用，无需类型断言。算法先查 checksum 注册表（v1 主路径，如 xor8），未命中再查 hash 注册表。

2. **`compiledGuard` 同时存 `fieldIdx` 和 `fieldName`**：brief 要求 `fieldIdx` 指向 route 字段下标，但 `applies()` 需要在 route map（字段名→值）中取值——没有 codec 反向引用时无法由 fieldIdx 反推名字。为保持 `compiledWhen` 自包含、可单元测试，guard 一并存字段名。`applies()` 的 route map key 约定为 route 字段名（T1.4 须遵循此约定）。

3. **`appliesWithIdx` 仅预解析下标**：`appliesWith` 依赖前一步是否生效，需要 T1.4 在 encode 流水线串行调用时把前一步的判定结果传进来。本任务只存下标，T1.4 负责串联判定（避免在 `applies()` 内重入 encode 流水线）。

4. **`errorMap` 浅拷贝**：编译产物须严格不可变；直接持有入参 map 会允许调用方后续修改污染编译产物。浅拷贝是必要隔离（value 为 string，不可变，浅拷贝已足够）。

## Concerns

- **`compiledGuard.fieldName` 是 brief 未列出的辅助字段**：为使 `applies()` 自包含且 route map 用字段名作 key，guard 必须知道字段名。如果 T1.4 选择用下标而非名字索引 route（例如 route 改为 `[]any`），需要调整此约定。已在报告「设计决策 #2」标注，留给 T1.4 确认 route map 形态。
- **`appliesWith` 串联判定由 T1.4 负责**：本任务 `applies()` 不实现 appliesWith 串联（需要前一步判定结果，且需要避免 encode 流水线重入）。T1.4 须在调用 `applies()` 前自行判断依赖 step 是否生效，并把 `appliesWithIdx` 的语义兑现。
- **`SchemaCodec` 字段未导出**：T1.4/T1.5 同包内可访问，包外不可。这是有意的（封装）；若 T4 需要导出更多访问器，后续再加。

## 报告路径

`plans/declarative-codec/reports/t1-3-report.md`
