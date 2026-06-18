# T1.4 Brief — encode 引擎（SchemaCodec 执行方法）+ 帧访问器

> 你是 implementer。先读本 brief。参考 `plans/declarative-codec/01-track-codec-engine.md` §3.3（encode 执行）、§3.1.4（方向语义/头部零初始化/产物计算时机）、总纲 §3.1。
> 参考现有实现（**对拍基准**）：`adapter/helpers.go`（`ReadBodyLength`/`BodyLengthInfo`）、`adapter/lua_adapter.go`（BodyLength/ExpectedRouteKey 今日实现）、`conf/adapter/codec.lua`（encode 真值）。
> 工作目录：worktree 根。**不要 git commit**。

## 目标

新增 `codec/engine.go`，在 T1.3 的 `*SchemaCodec` 上追加 **encode 执行方法**与**只读帧访问器**。本任务**不含 decode**（T1.5）与 `adapter.NewSchemaAdapter` 包装（T1.5）。同包跨文件加方法，合法。

## 新增文件

- `codec/engine.go`：encode 管线 + 帧访问器 + 字节读写 helper。
- `codec/engine_test.go`：TDD（**对拍 codec.lua encode 为核心验收**）。

## 方法签名（同包方法，加在 *SchemaCodec 上）

```go
// 帧访问器
func (c *SchemaCodec) HeaderSize() int                        // T1.3 已有；如已在 compile.go 则勿重复
func (c *SchemaCodec) BodyLength(header []byte) int            // 读 length 字段，按 lengthIncludes* 口径返回 body 长
func (c *SchemaCodec) ExpectedRouteKey(route any) string       // routeKeyTmpl + route 字段拼键；route==nil 各字段取 0

// encode（TCP/UDP 同一管线；codec 单 transport，offset 在各 encrypt step 的 encOffset 上）
func (c *SchemaCodec) EncodeTCP(route any, body []byte, secretKey []byte) []byte
func (c *SchemaCodec) EncodeUDP(route any, body []byte, secretKey []byte) []byte
```

> EncodeTCP/EncodeUDP 内部都调同一个内部 `encode(route, body, key)`（codec 单 transport，方向已固化在各 step 的 encOffset）。两者并存仅为满足将来 `adapter.Adapter` 接口（9 方法）。

## encode 管线（逐字节复刻 codec.lua `_do_encode`/`encode_tcp`/`encode_udp`）

1. **route 解析**：`route any`（运行时是 `map[string]any`，数值为 `float64`）→ 按 `role:"route"` 字段 `name` 取值，`math.Floor` 后转整数（对齐 codec.lua）。供 header route 字段写入与 `encodeWhen.applies`/guards。
2. **管线正序执行**（`c.steps`），维护：
   - `body []byte`（可被 compress/encrypt 替换）；
   - `flags uint64`（累计置位）；
   - `applied []bool`（每步是否生效，供 `appliesWith` 依赖判定）；
   - `produces stash`（每步按 region 计算的产物，供 checksumOut 字段取用）。
   每步：
   - 判定 `applies`：`step.encodeWhen.applies(len(body), len(key)>0&&requireKey 满足, routeMap)`；若 `appliesWithIdx>=0` 还需 `applied[appliesWithIdx]==true`。两者皆满足才「候选生效」。
   - **compress 的 onlySmaller 特判**：候选生效后先压缩，仅当 `!onlySmaller || len(compressed)<len(body)` 才**真正采用**（替换 body、置 flag、记 applied=true）；否则丢弃压缩结果、applied=false、不置 flag。
   - **encrypt**：候选生效且（`!requireKey || len(key)>=step.keyLen`）才执行；用 `step.impl.(Cipher).Encrypt(body, key, step.encOffset, step.Params)`（前 encOffset 字节明文、处理 `body[encOffset:]`）。置 flag、applied=true。key 长度不满足且 requireKey → 不执行、applied=false（不置 flag）。
   - **checksum/hash（独立步）**：按 `over` 作用域计算，结果暂存（写头时按字段需求取用；v1 现协议不用独立步，但实现要正确）。
   - 步执行后，按其 `produces` 计算并 stash 产物：
     - `ciphered` = 该步实际处理区域 `body[encOffset:]`（**加密后**的 body 的 `[encOffset:]`，即 bcc=xor8(body[encOffset:]) 的语义，复刻 `lua_crypto.go:160 computeBcc(data[offset:])`）；
     - `bodyPlain` = 管线执行**前**的原始 body 快照（在管线开始时存一份）；
     - `bodyFinal` = 全部管线步**之后**的 body；
     - `header`/`frame` = 头写就后（这些 region 若有 produce，在写头后再回填对应字段——v1 现协议不用，实现可注释明确）。
3. **写头**：`make([]byte, c.headerSize)` **整体置零**，再按字段写：
   - `length`：写「pipeline 后 wire body 长」按 `lengthIncludesHeader/Trailer` 口径调整（现协议 `false/false` → 纯 body 长）。
   - `route`：写 `routeMap[name]`（数值化）。
   - `errorCode`：encode 写 0。
   - `flags`：写累计 `flags`。
   - `checksumOut`：写其 `from` 指向的 stash 产物；若该步未执行（applied=false）→ 写 0。
   - `value`：按 `source`（const/route）写；`reserved`：0（零初始化已保证）。
   - 多字节字段按 `kind`+`endian` 用 `binary.<Endian>.PutUint*`；`bytes` 原样拷；按字段 `size` 截断/对齐。
4. **拼接返回**：`header ++ body`（`++ trailer` 若 trailerSize>0，v1 trailer 为 0/零值）。

## BodyLength（帧切割用，gnet OnTraffic 调）

读 header 中 `lengthField` 的值，按 `lengthIncludesHeader/Trailer` **反算**出 body 字节数（现协议 length=body 长，直接返回）。复刻 `adapter/helpers.go` `ReadBodyLength` 语义。`len(header) < headerSize` 或读出非法（<=0 或过大）→ 返回 -1 或 0 由调用方判（对齐现 helpers 行为，读实现确认）。

## ExpectedRouteKey

按 `routeKeySegs` 拼：literal 段原样 + field 段取 route 字段值（route==nil 取 0）→ 字符串。与 decode 同源。

## 关键约束

- **逐字节对拍 codec.lua**：encode 输出（含 flags 命名位 / bcc=`xor8(body[encOffset:])` / xor_carry_rol 加密 / gzip / UDP offset 11）必须与 `conf/adapter/codec.lua` 经现有 LuaAdapter 的 encode **完全一致**。这是本任务核心验收。
- **头部零初始化**：未写字节恒 0；未执行步的 checksumOut 写 0。
- 不改 T1.1/T1.2/T1.3 文件；不加 decode/adapter 包装；不 import gopher-lua。
- 块密码（aes_ecb/cbc/xxtea）会改变 body 长度——管线要能跑通，但 v1 对拍只测 xor_carry_rol（定长）；不要为块密码长度变化做特殊优化，保持自然实现即可。
- 现协议 gzip 阈值 2048、onlySmaller=true：encode 应只在 body>=2048 且压缩变小时压缩并置 compressed flag。

## 工作方式（TDD，对拍为王）

1. RED：`engine_test.go`：
   - 用旧 LuaAdapter（`adapter/lua_adapter.go` + `conf/adapter/codec.lua` + `conf/adapter/error.lua`）对一批 route/body/key 组合 encode，作为**真值**；断言 `SchemaCodec.EncodeTCP/UDP` 输出字节完全一致。组合覆盖：cmd!=0 + key（加密）、cmd!=0 无 key（不加密？现协议 requireKey，看 codec.lua）、cmd=0（guards neq 0 不满足→不加密）、body<2048（不压缩）、body>=2048（压缩）、空 body、UDP encode（offset 11）、TCP encode（offset 0）。
   - BodyLength/ExpectedRouteKey 用已知 header/route 断言。
   - 头部零初始化：构造无 checksumOut 执行的帧，断言对应字节为 0。
2. GREEN：实现 engine.go，对拍全绿。
3. `go build ./codec/...`、`go vet ./codec/...`、`go test ./codec/... -count=1` 全绿、输出干净。
4. **不要 git commit。**

> 对拍真值获取：在测试里构造一个 `adapter.NewLuaAdapter(poolSize, "conf/adapter/codec.lua", "conf/adapter/error.lua")`（读 lua_adapter.go 确认签名），调其 EncodeTCP/UDP 得到 expected bytes。这会 import adapter（仅测试），可接受。

## 验收（self-review）

- encode 对拍 codec.lua 字节级一致（TCP/UDP、加密/不加密、压缩/不压缩、空 body、cmd=0）。
- bcc = xor8(body[encOffset:])，UDP 排除前 11 明文字节。
- 头部零初始化；checksumOut 未执行步写 0。
- BodyLength/ExpectedRouteKey 与 helpers/lua_adapter 行为一致。
- 未实现 decode / 未加 adapter 包装。

## 报告

写完整报告到 `plans/declarative-codec/reports/t1-4-report.md`：实现内容、encode 管线步骤、对拍真值如何取、对拍覆盖组合与结果（逐组合 PASS）、TDD RED/GREEN、改动文件、self-review、concerns。
返回（<15 行）：Status、改动文件、一行测试摘要、concerns、报告路径。
