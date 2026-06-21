# Track 1 — Go 声明式 Codec 引擎 + 算法注册表 + errors.json

> 依赖：无（关键路径，立即开工）
> 产出：`NewSchemaAdapter` + `CodecSchema` 格式 + 算法注册表 + `errors.json` 加载 + 对拍测试
> 对外契约：见总纲 §3。本轨道**冻结**该契约后，T2/T3/T4 方可据此并行。
> 行为目标：本轨道**不改变任何对外行为**——只新增一套纯 Go 实现，旧 `LuaAdapter` 暂时仍在。切换在 T2/T4 完成。

---

## 1. 目标

实现一个纯 Go、无状态、不可变、并发安全的 `Adapter` 实现，由 `codec.json` schema 驱动，完整复刻 `conf/adapter/codec.lua` 的行为（字节级一致），并把 `error.lua` 迁移为 `errors.json` 的 Go map 查找。

---

## 2. 现状参考（已读码）

| 事实 | 位置 |
|---|---|
| `Adapter` 接口 9 方法 | `adapter/adapter.go:9` |
| 旧 `LuaAdapter`（待 T2/T4 删） | `adapter/lua_adapter.go` |
| 帧切割纯 Go（可复用） | `adapter/helpers.go:19` `ReadBodyLength` + `BodyLengthInfo:11` |
| **加密算法已是 Go 原生**（抽取非重写） | `adapter/lua_crypto.go:841` `EncryptXorCarryRol` / `:875` `DecryptXorCarryRol` |
| gzip 压缩 Go 实现 | `adapter/lua_zlib.go`（`RegisterZlibModule:15` 内部用 Go gzip） |
| **bcc 仅校验密文区域明文**（已核对） | `lua_crypto.go:160` / `:227` `computeBcc(data[offset:])` → `bcc=xor8(body[encOffset:])` |
| **加解密偏移非对称**（已核对） | `encode_udp` offset=11；`decode` 恒 offset 0（`codec.lua:172`） |
| 现协议 codec 逻辑（复刻目标） | `conf/adapter/codec.lua`（12B 头、gzip 阈值 2048、xor_carry_rol rol=3、UDP encode offset 11/decode 0、bcc、routeKey `cmd:act`） |
| 错误码表（迁移目标） | `conf/adapter/error.lua`（`errors={[code]=str}` + `describe_error`） |
| crypto 算法测试（可复用为种子） | `adapter/lua_crypto_test.go` |

---

## 3. 设计

### 3.1 新包结构（已定：新建 `codec/` 包）

新增 `codec/` 包（与 `adapter/` 解耦，彻底剥离 gopher-lua 依赖）：

```
codec/
  schema.go      // CodecSchema / Field / PipelineStep 类型 + JSON 反序列化 + Validate
  compile.go     // 编译期：schema → 不可变编译产物（compiledField/compiledStep/路由段/flag 掩码/from 引用解析）
  engine.go      // SchemaCodec：持有编译产物，实现帧切割 + encode/decode/expectedRouteKey（纯执行，零 schema 解析）
  registry.go    // Cipher / Compressor / Checksum / Hasher 接口 + 四张注册表
  ciphers.go     // none / xor_carry_rol / rc4 / aes_* / xxtea（迁移自 lua_crypto.go）
  compressors.go // none / gzip（迁移自 lua_zlib.go）
  checksums.go   // none / xor8 / sum8 / crc16 / crc32 / crc32c
  hashes.go      // md5 / sha1 / sha256（+HMAC）
  errors.go      // errors.json 加载 + DescribeError map
  engine_test.go // 单测 + 对拍测试 + 基准
```

`adapter/` 侧新增薄封装 `adapter.NewSchemaAdapter(schema, errorMap) (Adapter, error)`，内部委托给 `codec.SchemaCodec`，实现现有 `Adapter` 接口（**签名零改动**）。

> **结构总原则（compile/execute 分离）**：`compile.go` 在 `NewSchemaAdapter` 一次性把所有"字符串→索引/偏移/掩码"解析完（字段名、algo 名、`from` 引用、flag 名、routeKey 模板占位符全部预解析；algo 缺失在编译期即 fail）。`engine.go` 热路径**零 schema 解析、零字符串查表**，只剩纯算术 + 算法派发——这是 perf 目标与无状态性共同的根基。

### 3.2 类型（schema.go）

> 与总纲 §3.1 契约一一对应。字段类型集见总纲 §3.1.1，角色/取值源见 §3.1.2，pipeline/注册表见 §3.1.3。

```go
type CodecSchema struct {
    Version       int
    EndianDefault string        // "le" | "be"
    Frame         FrameSpec
    Header        []Field
    RouteKeyTmpl  string        // "{cmd}:{act}"
    Pipeline      []PipelineStep
}

type FrameSpec struct {
    HeaderSize            int
    TrailerSize           int  // 默认 0；>0 时帧含尾部（v1 仅支持定长 trailer）
    LengthIncludesHeader  bool // length 字段值是否含 header
    LengthIncludesTrailer bool // length 字段值是否含 trailer
}
// LengthField 结构已删除：长度字段的物理位置（offset/size/type/endian）唯一来源是
// Header 中 role=="length" 的那个 Field。SchemaCodec 在构造期定位它（lengthField *Field），
// 供 BodyLength 切帧与 encode 写长度共用，杜绝「frame.lengthField 与 role:length 双声明漂移」。

// FieldType: u8/u16/u24/u32/u64 | i8/i16/i24/i32/i64 | f32/f64 | bytes
type Field struct {
    Name, Type, Endian string
    Offset, Size       int
    Role               string      // length|route|errorCode|flags|checksumOut|value|reserved
    Bits               []FlagBit    // role=flags：命名位
    From               string       // role=checksumOut：产物引用 "<step>.<outputName>"（如 "enc.bcc"）
    Source             *ValueSource  // role=value：encode 取值源
    Repr               string        // type=bytes 文本呈现：hex|base64|ascii
}

type FlagBit struct { Name string; Bit int }

// Kind: const|route（v1 实现）；state|counter|timestamp（v1.1，Validate 报「v1 不支持」）
type ValueSource struct {
    Kind  string
    Value int64        // const
    Key   string       // state / route
    Start, Step int64  // counter（v1.1）
    Wrap  int64        // counter 回绕上限（0=不绕）（v1.1）
    Unit  string       // timestamp：s|ms（v1.1）
}

type PipelineStep struct {
    Op       string         // compress|encrypt|checksum|hash
    Name     string         // 供 flag/from/appliesWith 引用
    Algo     string         // 注册表键
    Flag     string         // 绑定到 flags 字段的命名位
    Params   map[string]any
    KeyLen   int            // encrypt：要求 key 长度
    Offset   *StepOffset    // encrypt：单向明文前缀偏移（每份 codec 单 transport）
    Produces []StepProduce  // 该步派生产物（如 encrypt 产 bcc）
    Over     *OverSpec      // 独立 checksum/hash 步：作用域
    OnError  string         // decode 失败策略：fail(默认)|keep
    When     *StepCond
}

// 加解密偏移：encode/decode 独立、非对称（复刻 codec.lua：UDP encode=11，decode 恒 0）。
// 每份 codec 只描述一种 transport（决策 #8），故无需 tcp/udp 区分。
type StepOffset struct {
    Encode int  // 缺省 0；如 udp:battle = 11
    Decode int  // 缺省 0
}

// 该步的派生产物：算法对其处理区域计算出的值，写入引用它的字段
type StepProduce struct {
    Name   string // 产物名（字段 from 引用 "<step>.<name>"）
    Algo   string // 计算算法（如 xor8）
    Region string // ciphered(=本步 body[offset:]) | bodyPlain | bodyFinal | header | frame
}

// 独立 checksum/hash 步的作用域（与 cipher 无关的全帧校验用）
type OverSpec struct {
    Kind             string // bodyPlain|bodyFinal|header|frame|range
    RangeStart, RangeEnd int // Kind=range
}

type StepCond struct {
    MinBodyLen  int
    OnlySmaller bool      // compress：仅当变小才采用
    RequireKey  bool      // encrypt：需有合法 key
    AppliesWith string    // 仅当指定步生效时本步才生效
    Guards      []Guard
}

type Guard struct { Field, Op string; Value int64 } // Op: eq|neq|gt|gte|lt|lte
```

> **v1 边界（已确认）**：
> - `TrailerSize`/`includesTrailer`：**v1 实现**（分帧口径）。
> - decode 返回值固定为 `(routeKey, body, headerErr)` 三件套（**不**增 headerFields；expose/头字段暴露延后 v1.1）。
> - `op:"hash"` 步：注册表 + 引擎执行 v1 支持，但现协议不用。
> - `ValueSource.Kind ∈ {state,counter,timestamp}`：**`schema.Validate` 在 v1 直接报「v1 不支持」**（避免引入每连接状态、保住"无状态单例"主张）。理由见总纲 §3.1.2 / §前提。

### 3.3 引擎（compile.go 编译 + engine.go 执行）

引擎严格分两阶段、**绝不混**：编译期把 schema 翻译成不可变产物，执行期是纯函数。每个 Layer 0 契约都在编译期落成一个 O(1) 机械查表；algo 缺失 / `from` 指向不存在的产物 / `when` 步未绑 flag 等**全部在编译期 fail**，热路径见不到。

**编译产物**（`NewSchemaAdapter` 一次构建，此后不可变 → 并发安全、无锁，兑现不变量 2）：

```go
type SchemaCodec struct {
    headerSize, trailerSize int
    lengthIncludesHeader, lengthIncludesTrailer bool
    lengthField  compiledField        // 定位自 role:"length"（单一来源，契约②）
    fields       []compiledField      // route/errorCode/flags/checksumOut/value/reserved
    routeKeySegs []routeSeg           // 模板预解析：literal | {fieldIdx}
    steps        []compiledStep       // 预编译 pipeline
    errorMap     map[uint64]string
    // 无任何可变字段
}
type compiledField struct {
    offset, size int; kind fieldKind; endian binary.ByteOrder; role roleKind
    flagBits    []int                 // role:flags 持有的位索引
    checksumRef stepProduceRef        // role:checksumOut：预解析 (stepIdx, produceName)（契约 B）
    source      compiledValueSource   // role:value（v1 const/route）
}
type compiledStep struct {
    op stepOp; impl any               // Cipher/Compressor/Checksum/Hasher（注册表 eager 查得）
    flagMask    uint64                // 无 flag 则 0（无条件步）
    encodeWhen  predicate             // **encode-only**；decode 路径不引用（契约 A 结构性隔离）
    encOffset, decOffset int          // 单向 encode/decode 偏移（契约 C；每份 codec 单 transport）
    produces    []compiledProduce     // {name, algo, region}（契约 B）
    onError     onErrorPolicy
}
```

**执行**（热路径，纯函数）：

- `HeaderSize() int` → `schema.Frame.HeaderSize`
- `BodyLength(header []byte) int` → 复用/迁移 `helpers.ReadBodyLength` 逻辑（读 `Frame.LengthField`，含 `includesHeader/includesTrailer` 口径）
- `EncodeTCP(route, body, key)` / `EncodeUDP(...)`：
  1. 复制/取 body；解析每个 pipeline 步的「是否生效」（按 `When`：`compress` 看 `minBodyLen/onlySmaller`；`encrypt` 看 `requireKey/minBodyLen/guards`）；
  2. 正序跑 pipeline：每步生效则执行并把其 `flag` 命名位置入 flags 累计值；`encrypt` 用 `offset.Encode` 决定明文前缀长度（仅加密 `body[encOffset:]`）；每步按 `produces` 对其 `region` 计算产物（如 `enc` 产 `bcc`=`xor8(body[encOffset:])`，即 `region:"ciphered"`——**这就是 codec.lua `computeBcc(data[offset:])` 的语义**），暂存供 `checksumOut` 字段写入；
  3. 分配 `headerSize` 字节缓冲**整体置零**后按 `header` 写头：`length`=wire body 长（口径 `frame.lengthIncludes*`）、`route`=`route[name]`、`errorCode`=0、`flags`=累计命名位、`checksumOut`=`from` 指向的产物（`<step>.<name>`；该步未执行则写 0）、`value`=按 `source` 求值（**v1 仅 const/route**）、`reserved`=0（零初始化已保证）；
  4. 拼 `header .. body (.. trailer)` 返回。
- `DecodeTCP(data, key)` / `DecodeUDP(...)`：
  1. `len < HeaderSize` 直接返回空；
  2. 读头：`errorCode`→headerErr、`route` 各字段、`flags`（解析命名位）；
  3. body = header 之后到 trailer 之前；
  4. 反序跑 pipeline：**decode 纯 flag 驱动**——某步执行当且仅当其 `flag` 命名位在解码出的 flags 中置位（encrypt 另要求运行时 key 在场），**不重新求值 `when`/`guards`**（`when` 是 encode-only 决策，见总纲 §3.1.4）：`encrypt` 用 `offset.Decode`（**缺省 0，与 encode 偏移独立**）解密 → `compress` 解压；`checksumOut` 字段可选校验（重算比对）；
  5. **失败语义**：任一步失败（解压报错 / 解密后异常 / 校验不过）按该步 `OnError`：`fail`(默认)→ 返回**空 `routeKey`**（帧被丢弃 + warn，不返回 err、签名零改动）、**不返回乱码 body**；`keep`→ 保留原字节继续；
  6. 按 `RouteKeyTmpl` 用 `route` 字段拼 `routeKey`，返回 `(routeKey, body, headerErr)`（与现 `Adapter` 签名一致，无 err 返回）。
- `ExpectedRouteKey(route any)`：仅按 `RouteKeyTmpl` + `route` 字段拼键（与 decode 同源），`route==nil` 时各字段取 0。

> **加解密偏移非对称（核对修正）**：`codec.lua` 的 `encode_udp` 用 offset 11（前 11 字节明文供服务端查密钥表），但 `decode` 恒用 offset 0（`codec.lua:172`，整 body 全解）。每份 codec 单 transport（决策 #8），故偏移为单向 `{encode, decode}`（缺省 0）；引擎必须**逐字节复刻**：`udp:battle` 那份 = `{encode:11, decode:0}`。
> **bcc 区域（核对修正）**：`bcc = xor8(body[encOffset:])`（`lua_crypto.go:160/227` `computeBcc(data[offset:])`），排除明文前缀。建为 `encrypt` 步 `produces` 的 `region:"ciphered"` 产物，**不是整 body 的独立 checksum**。
> **route 取值**：`route` 运行时是 `ActionDef.Route` 反序列化结果（`map[string]any`，数值 `float64`）。引擎按 `role:"route"` 字段 `name` 读 `route[name]` 并数值化（`math.Floor` 对齐 codec.lua）。替代旧 `helpers.RouteToLuaValue`。

#### Layer 1 已定决策

1. **头缓冲**：每调用 `make([]byte, headerSize)`（12B 不值得上池；池还要处理并发归还）。
2. **decode 时 flag 置位但 key 无效**：走 `onError`（默认 `fail` 丢帧）——**不**复刻 codec.lua:171 的静默跳过（那是把加密乱码往下传）。对拍不构造此畸形帧即不受影响（现网握手后 key 恒在）。
3. **registry 落位**：新建 `codec/` 包（见 §3.1），彻底剥离 gopher-lua。

> **支持但非关键路径**：独立 `checksum`/`hash` 步的 `over`（`bodyPlain`/`bodyFinal` 需在 pipeline 边界快照 body）——现协议不用（bcc 走 produce），引擎实现但不挡关键路径。

### 3.4 算法注册表（registry.go / ciphers.go / compressors.go / checksums.go / hashes.go）

四张注册表，均为「名字 → 实现」的开放 map，新增算法 = 实现接口 + 注册一行（前端只读清单同步，见 T3）：

```go
type Cipher interface {
    // offset：前 offset 字节保持明文；只处理 data[offset:]
    Encrypt(data, key []byte, offset int, params map[string]any) (out []byte, err error)
    Decrypt(data, key []byte, offset int, params map[string]any) (out []byte, err error)
}
type Compressor interface {
    Compress(data []byte) ([]byte, error)
    Decompress(data []byte) ([]byte, error)
}
type Checksum interface {
    // 对给定区域算校验值（最多 8 字节，按目标字段 size 截取/对齐）
    Sum(data []byte, params map[string]any) uint64
}
type Hasher interface {
    Hash(data, key []byte, params map[string]any) []byte // key 非空走 HMAC
}

var ciphers     = map[string]Cipher{}
var compressors = map[string]Compressor{}
var checksums   = map[string]Checksum{}
var hashers     = map[string]Hasher{}
```

> **bcc 实现路径（核对修正）**：bcc 是 `encrypt` 步的 `produces` 产物，`{name:"bcc", algo:"xor8", region:"ciphered"}`。引擎对该步的处理区域 `body[encOffset:]` 调 `checksums["xor8"].Sum(...)`，结果供 `checksumOut` 字段写入。**不再建为整 body 的独立 checksum 步**——校验作用域必须等于绑定步实际处理的那段，否则 UDP(offset 11) 会多算前 11 明文字节 → 字节错。独立 `checksum`/`hash` 步（带 `over`）仍保留，用于与 cipher 无关的全帧 CRC 类协议。Track 1 对拍必须验证 bcc 与 codec.lua 字节一致。

**v1 落地清单**（迁移自 `adapter/lua_crypto.go` + `lua_zlib.go`，多数已有 Go 实现/测试）：

| 注册表 | 必做（现协议 + lua_crypto 已有） | 可选（按需补） |
|---|---|---|
| cipher | `none` `xor` `xor_carry_rol` `rc4` `aes_cbc` `aes_ctr` `aes_ecb` `xxtea` | `3des` 等 |
| compress | `none` `gzip` | `zlib` `snappy` `lz4` `zstd` |
| checksum | `none` `xor8` `sum8` `crc16` `crc32` `crc32c` | — |
| hash | `md5` `sha1` `sha256`（+ HMAC） | `sha512` 等 |

- `xor_carry_rol`：迁移 `adapter/lua_crypto.go:841` `EncryptXorCarryRol`/`:875` `DecryptXorCarryRol`（`params.rol` 默认 3）。
- `gzip`：迁移 `adapter/lua_zlib.go`。
- `rc4`/`aes_*`/`xxtea`/`crc*`/hash：`lua_crypto.go` 已有对应 Go 实现，抽出为注册表项并复用 `lua_crypto_test.go` 作种子。

### 3.5 errors.json（errors.go）

- `LoadErrorMap(path string) (map[uint64]string, error)`：读 JSON `{"code":"desc"}`，key 解析为 uint64。
- `DescribeError(code uint64) string`：map 查找；未命中返回约定值（见总纲开放问题 #4，建议 `fmt.Sprintf("未知错误码:%d", code)`）。
- 由 `NewSchemaAdapter(schema, errorMap)` 持有；`Adapter.DescribeError` 委托之。

---

## 4. 实施切片（按顺序落地）

### 4.1 Schema 类型与 JSON 加载

- [ ] 新建 `codec/` 包，先只引入纯数据类型与 JSON 加载，不接入 runtime。
- [ ] 定义 `CodecSchema` / `FrameSpec` / `Field` / `FlagBit` / `ValueSource` / `PipelineStep` / `StepOffset` / `StepProduce` / `OverSpec` / `StepCond` / `Guard`，字段与总纲 §3.1 一一对应。
- [ ] 明确 JSON tag 使用 camelCase：`endianDefault`、`routeKeyTemplate`、`headerSize`、`trailerSize`、`lengthIncludesHeader`、`lengthIncludesTrailer`、`onError` 等。
- [ ] `LoadSchema(path string) (*CodecSchema, error)` 只负责读文件 + JSON parse + 调 `Validate`；不做兼容旧 `codec.lua`。
- [ ] `Version` 必须显式为 `1`；未知版本直接中文报错。

### 4.2 Schema.Validate 结构校验

- [ ] 基础校验：`frame.headerSize>0`、`trailerSize>=0`、`endianDefault in {le,be}`、`routeKeyTemplate` 非空、header 字段名非空且唯一。
- [ ] 字段校验：`offset>=0`、`size>0`、`offset+size<=headerSize`、字段不重叠；位域共享同一整数字段可通过同一个 `role:"flags"` 字段的 `bits` 表达，不允许两个 Field 物理重叠。
- [ ] 类型校验：`u8/u16/u24/u32/u64/i8/i16/i24/i32/i64/f32/f64/bytes`；固定宽度类型的 `size` 必须匹配，`bytes` 必须显式 size。
- [ ] role 校验：必有且仅有一个 `length`；至少一个 `route`；`flags` 字段 bits 名称唯一、bit 范围合法；`checksumOut.from` 语法必须是 `<step>.<produce>`。
- [ ] routeKey 模板校验：所有 `{name}` 占位必须来自 `role:"route"` 字段；模板中不允许未知占位。
- [ ] pipeline 校验：step `name` 唯一；`op` 合法；`algo` 存在于对应注册表；`flag` 若填写必须存在于 flags 命名位；同一 flag 位最多绑定一个 step。
- [ ] 方向语义校验：凡带 `when` 的 step 必须绑定 `flag`，否则 decode 无法复现 encode 决策；decode 侧绝不重算 when。
- [ ] 引用校验：`checksumOut.from` 必须指向存在 step 的 `produces`；`appliesWith` 必须指向存在 step；`produces.algo` 必须存在于 checksum/hash 注册表中。
- [ ] v1 拒绝项：`ValueSource.Kind in {state,counter,timestamp}` 直接报「v1 不支持」；未知 source kind 报错。
- [ ] 错误信息统一中文，包含字段名/step 名/引用名，方便 T3 直接展示。

### 4.3 编译层 compile.go

- [ ] `NewSchemaCodec(schema, errorMap)` 先调用 `Validate`，再把 schema 编译为不可变 `SchemaCodec`。
- [ ] 预解析字段：长度字段、route 字段、errorCode 字段、flags 字段、checksumOut 字段、value/reserved 字段，全部转为 `compiledField`。
- [ ] 预解析 routeKey 模板为 `[]routeSeg`，热路径不做字符串解析。
- [ ] 预解析 flags：命名位 → bit mask；pipeline step 的 `flag` → `flagMask`。
- [ ] 预解析 `from` / `produces` 引用为 step index + produce index；热路径不做 map 查找。
- [ ] 预解析算法注册表：step compile 时拿到具体 Cipher/Compressor/Checksum/Hasher 实现；运行期不按字符串查注册表。
- [ ] 编译产物无可变字段；map/slice 在构造后只读，保证任意 goroutine 并发调用无锁。

### 4.4 算法注册表与元数据

- [ ] 四张注册表：cipher / compressor / checksum / hasher；注册发生在包 init 或显式 Register 函数，但运行期只读。
- [ ] 先落现协议关键算法：`xor_carry_rol`、`gzip`、`xor8`、`none`。
- [ ] 再迁移 lua_crypto 已有算法：`xor`、`rc4`、`aes_cbc`、`aes_ctr`、`aes_ecb`、`xxtea`、`sum8`、`crc16`、`crc32`、`crc32c`、`md5`、`sha1`、`sha256`。
- [ ] 每个算法提供元数据：`name`、`op`、参数名、参数类型、默认值、说明；供 T4 `GET /sbot/codec/algorithms` 返回、T3 下拉使用。
- [ ] 算法实现从 `adapter/lua_crypto.go` / `lua_zlib.go` 抽取，保持 Go 逻辑一致；T1 完成后旧 Lua 绑定文件是否删除交给 T2/T4 合流切换。

### 4.5 Encode 实现

- [ ] `EncodeTCP` / `EncodeUDP` 输入固定为 `(route, body, secretKey)`；route 支持 `map[string]any`、struct/map 常见 JSON 反序列化形态，数值按 codec.lua 语义转整数。
- [ ] pipeline encode 正序执行；`when` 只在 encode 判断。
- [ ] `compress` 支持 `minBodyLen`、`onlySmaller`；只有最终采用压缩结果时才置 flag。
- [ ] `encrypt` 支持 `requireKey`、`keyLen`、guards、单向 encode offset；只处理 `body[offset:]`，前缀保持明文。
- [ ] `produces` 计算时机与 region 明确：`ciphered` 对该步实际处理区域计算，`bodyPlain`/`bodyFinal`/`header`/`frame` 按定义取快照。
- [ ] header 缓冲每次 `make([]byte, headerSize)` 且零初始化；未写字段自然为 0。
- [ ] length 写 pipeline 后 body 长，并按 `lengthIncludesHeader` / `lengthIncludesTrailer` 调整口径。
- [ ] `flags` 写累计命名位；`checksumOut` 写 `<step>.<produce>` 产物，若对应 step 未执行则写 0。
- [ ] trailer v1 为定长零值尾部；如 schema 需要非零 trailer 内容，延后，不在 v1 表达。

### 4.6 Decode 实现

- [ ] `DecodeTCP` / `DecodeUDP` 保持 3-tuple：`(routeKey string, body []byte, headerErr uint64)`，不返回 err，不返回 headerFields。
- [ ] 短帧、长度不合法、trailer 越界直接返回空 routeKey。
- [ ] 先读 header：route 字段、errorCode、flags；body 为 header 后到 trailer 前。
- [ ] pipeline decode 反序执行；是否执行只看 header flags，绝不重算 `when`、`guards`、`minBodyLen`、`onlySmaller`。
- [ ] encrypt decode 使用 `offset.Decode`，与 encode offset 独立；`udp:battle` 的 encode=11、decode=0 必须对拍覆盖。
- [ ] compress decode 解压失败时按 `onError` 处理。
- [ ] checksumOut 校验失败按对应 step 的 `onError` 处理。
- [ ] `onError=fail` 返回空 routeKey 且 body 不外泄；`onError=keep` 保留当前字节继续后续步骤。
- [ ] 最后用预编译 routeKey 模板拼 routeKey，返回还原后的 body 与 headerErr。

### 4.7 Adapter 封装与 errors.json

- [ ] 在 `adapter/` 新增 `NewSchemaAdapter(schema *codec.CodecSchema, errorMap map[uint64]string) (Adapter, error)` 薄封装；对外仍满足现有 9 方法。
- [ ] `Close()` 对 Go schema adapter 为 no-op 且幂等。
- [ ] `LoadErrorMap(path string)` 读取 `errors.json`，格式为 `{"code":"中文描述"}`；key 解析成 uint64。
- [ ] `DescribeError(code)` 未命中返回总纲约定值；建议 `未知错误码:N`。
- [ ] `errors.json` 可选；缺失时 errorMap 为空，但 loader 缺 `codec.json` 必须失败。

### 4.8 Admin preview 支撑函数

- [ ] T1 暴露纯计算 preview helper，供 T4 Admin endpoint 调用，避免 T3 自己实现算法。
- [ ] encode preview 入参：schema、route、bodyHex、keyHex、协议方向 tcp/udp；出参 frameHex、header 字段解释、flags、routeKey 示例。
- [ ] decode preview 入参：schema、frameHex、keyHex、协议方向 tcp/udp；出参 routeKey、headerErr、bodyHex、header 字段解释。
- [ ] preview 不读写文件、不接网络、不依赖任务状态；只调用 `NewSchemaAdapter` 和 encode/decode。

### 4.9 测试与对拍

- [ ] schema validate 单测：字段越界、重叠、缺 length、多个 length、缺 route、未知 role/type/algo、flag 引用缺失、when 无 flag、from/appliesWith 指向不存在、v1 不支持 source。
- [ ] 算法单测：从现有 `lua_crypto_test.go` 迁移种子；覆盖加密/解密互逆、checksum/hash、gzip 压缩/解压。
- [ ] codec.lua 对拍测试：旧 `LuaAdapter` vs 新 `SchemaCodec`，覆盖 TCP/UDP encode/decode、无 key/有 key、cmd=0、不压缩/压缩、空 body、headerErr、routeKey、BodyLength。
- [ ] UDP 必测两类：UDP encode offset=11；UDP 加密响应 decode offset=0，防止偏移非对称遗漏。
- [ ] 失败语义单测：坏 gzip、坏 checksum、缺 key 解密，验证 `fail` 返回空 routeKey、`keep` 保留原字节。
- [ ] 并发单测：同一个 adapter 多 goroutine 并发 encode/decode，配合 `-race` 验证无共享可变状态。
- [ ] benchmark：小/中/大 body 的 encode/decode `ns/op`、`allocs/op`，与旧 LuaAdapter 对比并记录倍率。

### 4.10 冻结与交接

- [ ] T1 完成后冻结 `codec.json` schema、`errors.json` 格式、`LoadSchema` / `LoadErrorMap` / `NewSchemaAdapter` / 算法元数据结构。
- [ ] 把各连接 codec 文件（`tcp_logic_codec.json`/`tcp_battle_codec.json`/`udp_battle_codec.json`）+ 共享 `errors.json` 作为 T4 迁移产物提交到 `conf/adapter/`。
- [ ] 通知 T2/T3/T4：Adapter 签名零改动、decode 无 err/headerFields、Go adapter 并发安全、算法清单由 T1 导出。

## 5. 任务清单

- [ ] 1. 新建 `codec/` 包，定义 `CodecSchema`/`FrameSpec`/`Field`/`ValueSource`/`PipelineStep` 等类型（总纲 §3.1.1-3）+ JSON 反序列化。
- [ ] 2. 实现 `schema.Validate()`：`frame.headerSize>0`；各字段 `offset+size ≤ headerSize` 且不重叠（位域共享整数除外）；`type` 合法、`size` 与 `type` 匹配；**必有且仅有 1 个 `role:"length"` 字段**（其 offset/size/type/endian 为 framer 唯一长度来源，`frame` 不再重复声明）、≥1 个 `route`；`role` 合法；`flags` 命名位与 pipeline `flag` 引用一致、**每个命名 flag 位至多被一个步绑定**；**凡带 `when` 的步必须绑定 `flag`**（否则 decode 无法复现 encode 决策，见总纲 §3.1.4 方向语义）；pipeline `algo` 在对应注册表；`checksumOut.from`(`<step>.<output>`) 指向某步存在的 `produces` 产物；`appliesWith` 指向存在的步；`encrypt.offset` 非负（仅告警）。**v1 显式拒绝**：`value.source.kind ∈ {state,counter,timestamp}` → 报「v1 不支持，留 v1.1」。错误信息中文。
- [ ] 3. 实现四张算法注册表 + v1 清单（§3.4）：`none`/`xor`/`xor_carry_rol`/`rc4`/`aes_cbc`/`aes_ctr`/`aes_ecb`/`xxtea`、`gzip`、`xor8`/`sum8`/`crc16`/`crc32`/`crc32c`、`md5`/`sha1`/`sha256`(+HMAC)。迁移 `lua_crypto.go`/`lua_zlib.go` 并复用 `lua_crypto_test.go`。
- [ ] 4. 导出**算法元数据**：每算法 `{name, op, params:[{name,type,default,description}]}`，供前端下拉/参数表单与 `GET /sbot/codec/algorithms`（T3/T4）使用。
- [ ] 5. 实现 `SchemaCodec`（帧切割含 trailer 口径 + encode/decode/expectedRouteKey + 命名 flags + 取值源），构造期预计算偏移/掩码/from 引用/routeKey 模板/算法实现。
- [ ] 6. 实现 `errors.go`（加载 + DescribeError）。
- [ ] 7. 实现 `adapter.NewSchemaAdapter(schema, errorMap) (adapter.Adapter, error)` 薄封装。
- [ ] 8. 手写各连接 codec 文件（`tcp_logic_codec.json`/`tcp_battle_codec.json`/`udp_battle_codec.json`，每份单 transport、offset 单向）与共享 `errors.json`（对拍输入 + T4 迁移产物，提交到 `conf/adapter/`）。
- [ ] 9. **对拍测试**（核心验收）：一批 route/body/key 组合，旧 `codec.lua`（经现有 `LuaAdapter`）vs 新 `SchemaCodec`，断言：
  - encode 输出字节完全一致（含 flags 命名位 / bcc=`xor8(body[encOffset:])` / 加密 / 压缩 / **UDP encode offset=11**）；
  - decode 的 `(routeKey, body, headerErr)` 完全一致；
  - **必含 UDP 双向**：① UDP encode 帧（offset 11，前 11 明文 + bcc 排除前缀）；② **UDP 加密响应帧的 decode（offset 0，整 body 全解）**——否则偏移非对称这条会溜过去；
  - 覆盖：无 key、有 key、cmd=0（不加密）、body<阈值（不压缩）、body≥阈值（压缩）、TCP、空 body、超短帧；
  - **失败语义用例**（新增、非对拍）：损坏的压缩 body / 篡改的校验位 → `OnError:fail` 返回空 routeKey（帧丢弃，body 不外泄）；`OnError:keep` 保留原样。
- [ ] 10. 各算法单测（迁移 `lua_crypto_test.go`）+ **encode/decode 基准（量化验收）**：对若干 body 尺寸（小/中/大）测 `ns/op` 与 `allocs/op`，与经 `LuaAdapter` 的旧路径对比，**记录倍率并设目标**（如 encode `allocs/op` 显著下降、大 body 提速达成既定倍率），不只写"预期低分配"。

---

## 5. 验收

- [ ] 对拍测试全绿（字节级一致）。
- [ ] `schema.Validate` 对畸形 schema 给出清晰中文错误。
- [ ] `go test ./codec/...` 通过；基准显示 encode/decode 零 Lua、低分配。
- [ ] 契约（总纲 §3）冻结并通知 T2/T3/T4。

---

## 6. 交接给其他轨道的接口

- **给 T2**：`adapter.NewSchemaAdapter` 返回的 `Adapter` 可被任意 goroutine 并发调用（无锁）；T2 用它替换 `RobotAdapter` 并删 `luaMu`。**`DecodeTCP`/`DecodeUDP` 签名不变**（仍 3-tuple，无 headerFields/err）——T2 在 `network`/`engine`/`tcp_request` 处**无需为头字段接线**。
- **给 T4**：`codec.LoadSchema(path)` / `codec.LoadErrorMap(path)` 的签名；文件按连接命名 `<proto>_<service>_codec.json` + 共享 `errors.json`；T4 据此改 loader（枚举连接逐份加载）与分发。
- **给 T3**：`codec.json` schema 的完整字段定义与校验规则；T3 据此做前端编辑器与校验（建议导出一份 JSON Schema 草案供 Monaco 使用）。
