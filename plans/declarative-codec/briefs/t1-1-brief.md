# T1.1 Brief — codec schema 类型 + Validate + errors

> 你是 implementer。先读本 brief，它是你的需求（含须逐字使用的确切值）。可参考 `plans/declarative-codec/01-track-codec-engine.md` §3.2/§3.5/§4.1/§4.2 与总纲 `00-master.md` §3.1/§3.3，但**以本 brief 的确切值为准**。
> 工作目录：worktree 根（当前 cwd）。**不要 git commit**（提交由 controller 批量确认）。

## 目标

新建纯 Go 包 `codec/`（与 `adapter/` 解耦，**不得 import gopher-lua**），实现：

1. `codec/schema.go`：schema 类型 + JSON 反序列化 + `LoadSchema` + `Validate`。
2. `codec/errors.go`：`LoadErrorMap` + `DescribeError`。

本任务**不含**算法注册表、compile、encode/decode（后续任务）。本任务产出的类型与 Validate 是后续所有 T1 任务的基础。

## 确切类型（schema.go）

```go
package codec

type CodecSchema struct {
	Version       int        `json:"version"`
	EndianDefault string     `json:"endianDefault"`        // "le" | "be"
	Frame         FrameSpec  `json:"frame"`
	Header        []Field    `json:"header"`
	RouteKeyTmpl  string     `json:"routeKeyTemplate"`     // 如 "{cmd}:{act}"
	Pipeline      []PipelineStep `json:"pipeline"`
}

type FrameSpec struct {
	HeaderSize            int  `json:"headerSize"`
	TrailerSize           int  `json:"trailerSize"`           // 默认 0
	LengthIncludesHeader  bool `json:"lengthIncludesHeader"`
	LengthIncludesTrailer bool `json:"lengthIncludesTrailer"`
}

// FieldType: u8/u16/u24/u32/u64 | i8/i16/i24/i32/i64 | f32/f64 | bytes
type Field struct {
	Name   string      `json:"name"`
	Type   string      `json:"type"`
	Endian string      `json:"endian,omitempty"`   // le|be；缺省回退 EndianDefault
	Offset int         `json:"offset"`
	Size   int         `json:"size"`
	Role   string      `json:"role"`               // length|route|errorCode|flags|checksumOut|value|reserved
	Bits   []FlagBit   `json:"bits,omitempty"`     // role=flags
	From   string      `json:"from,omitempty"`     // role=checksumOut: "<step>.<outputName>"
	Source *ValueSource `json:"source,omitempty"`  // role=value
	Repr   string      `json:"repr,omitempty"`     // type=bytes: hex|base64|ascii
}

type FlagBit struct {
	Name string `json:"name"`
	Bit  int    `json:"bit"`
}

// Kind: const|route（v1 实现）；state|counter|timestamp（v1.1，Validate 报「v1 不支持」）
type ValueSource struct {
	Kind        string `json:"kind"`
	Value       int64  `json:"value,omitempty"`        // const
	Key         string `json:"key,omitempty"`          // state / route
	Start, Step int64  `json:"start,omitempty,step,omitempty"` // counter（v1.1）
	Wrap        int64  `json:"wrap,omitempty"`         // counter 回绕（v1.1）
	Unit        string `json:"unit,omitempty"`         // timestamp: s|ms（v1.1）
}

type PipelineStep struct {
	Op       string        `json:"op"`        // compress|encrypt|checksum|hash
	Name     string        `json:"name"`      // 供 flag/from/appliesWith 引用
	Algo     string        `json:"algo"`      // 注册表键
	Flag     string        `json:"flag,omitempty"`
	Params   map[string]any `json:"params,omitempty"`
	KeyLen   int           `json:"keyLen,omitempty"`    // encrypt
	Offset   *StepOffset   `json:"offset,omitempty"`    // encrypt
	Produces []StepProduce `json:"produces,omitempty"`
	Over     *OverSpec     `json:"over,omitempty"`      // 独立 checksum/hash 步
	OnError  string        `json:"onError,omitempty"`   // fail(默认)|keep
	When     *StepCond     `json:"when,omitempty"`
}

// 单向偏移（每份 codec 单 transport）：encode/decode 独立、可非对称
type StepOffset struct {
	Encode int `json:"encode"` // 缺省 0；如 udp:battle = 11
	Decode int `json:"decode"` // 缺省 0
}

type StepProduce struct {
	Name   string `json:"name"`   // 产物名
	Algo   string `json:"algo"`   // 计算算法（如 xor8）
	Region string `json:"region"` // ciphered|bodyPlain|bodyFinal|header|frame
}

type OverSpec struct {
	Kind                 string `json:"kind"` // bodyPlain|bodyFinal|header|frame|range
	RangeStart, RangeEnd int    `json:"rangeStart,omitempty,rangeEnd,omitempty"`
}

type StepCond struct {
	MinBodyLen  int     `json:"minBodyLen,omitempty"`
	OnlySmaller bool    `json:"onlySmaller,omitempty"`
	RequireKey  bool    `json:"requireKey,omitempty"`
	AppliesWith string  `json:"appliesWith,omitempty"`
	Guards      []Guard `json:"guards,omitempty"`
}

type Guard struct {
	Field string `json:"field"`
	Op    string `json:"op"`    // eq|neq|gt|gte|lt|lte
	Value int64  `json:"value"`
}
```

> JSON tag 注意：Go 的 `encoding/json` 单 tag 不支持 `a,omitempty,b,omitempty` 多键写法——上面 ValueSource/OverSpec 里那种是示意，实际用**每个字段独立一行一个标准 tag**（如 `Start int64 \`json:"start,omitempty"\``）。保证所有导出字段都有 camelCase 的 `json` tag，与总纲 §3.1 示例键名一致。

## 函数签名

```go
// LoadSchema 读 codec.json + json.Unmarshal + Validate。不做任何 codec.lua 兼容。
func LoadSchema(path string) (*CodecSchema, error)

// Validate 结构校验（见下），返回 error（聚合多条中文信息）。
func (s *CodecSchema) Validate() error

// LoadErrorMap 读 errors.json：{"code":"中文描述"}，key 解析为 uint64。
func LoadErrorMap(path string) (map[uint64]string, error)

// DescribeError 未命中返回空串 ""。
func DescribeError(m map[uint64]string, code uint64) string
```

`errors.json` 示例（总纲 §3.3）：`{ "0": "成功", "19": "消息解密失败" }`。

## Validate 规则（逐条实现，错误信息中文，带字段名/step 名/引用名）

基础：
- `Version == 1`；否则报「codec schema version 必须为 1」。
- `EndianDefault ∈ {le, be}`（允许空？否：非空且合法）。
- `Frame.HeaderSize > 0`、`Frame.TrailerSize >= 0`。
- `RouteKeyTmpl` 非空。

字段（Header）：
- 每个 `Field.Name` 非空且**唯一**。
- `Offset >= 0`、`Size > 0`、`Offset+Size <= HeaderSize`。
- 字段物理区间 `[Offset, Offset+Size)` **不重叠**（`role:"flags"` 的命名位共享同一整数字段不算重叠——它们是同一 Field 的 Bits，本就是同一段）。
- `Type` 合法（见 FieldType 集合）；固定宽度类型 `Size` 与 type 匹配（u8=1,u16=2,u24=3,u32=4,u64=8；i* 同理；f32=4,f64=8）；`bytes` 必须显式 `Size`。
- `Role` 合法（length|route|errorCode|flags|checksumOut|value|reserved）。

role：
- **必有且仅有 1 个 `role:"length"`**。
- **至少 1 个 `role:"route"`**。
- `role:"flags"`：`Bits` 各 `Bit` 在 `[0, Size*8)` 内、`Bit` 不重复、`Name` 不重复。
- `role:"checksumOut"`：`From` 非空且匹配 `^([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)$`。

routeKey 模板：
- `RouteKeyTmpl` 中所有 `{name}` 占位的 `name` 必须是某个 `role:"route"` 字段的 `Name`；未知占位报错。

pipeline：
- 每个 `PipelineStep.Name` **唯一**（含 Header/全局）。
- `Op` 合法（compress|encrypt|checksum|hash）。
- `Flag` 若非空，必须对应某个 `role:"flags"` 字段下的某个命名位 `Name`。
- **同一命名 flag 位至多被一个 step 绑定**（两个 step 不能绑同一个 flag Name）。
- **凡带 `When`（非 nil）的 step 必须绑定 `Flag`**（否则 encode 决策无处记录、decode 无法复现）。
- `encrypt` 步：`Offset` 若 nil 视为 `{0,0}`；`Offset.Encode/Decode >= 0`。
- `produces`：每个 `Name` 在该 step 内唯一；`Region` 合法（ciphered|bodyPlain|bodyFinal|header|frame）。
- `Over`（独立 checksum/hash 步）：`Kind` 合法（bodyPlain|bodyFinal|header|frame|range）；`range` 时 `RangeStart/RangeEnd` 合法。
- `When.AppliesWith` 若非空，必须指向已存在的 step `Name`。
- `OnError` 若非空，∈ {fail, keep}（空视为 fail，不报错）。
- `checksumOut.From` 指向的 `<step>.<output>`：该 step 必须存在，且其 `Produces` 含该 `output` 名。
- `Guards[].Op` ∈ {eq,neq,gt,gte,lt,lte}。

**v1 显式拒绝**（本任务要实现，即使后续任务才接算法注册表）：
- `ValueSource.Kind ∈ {state, counter, timestamp}` → 报「v1 不支持的头字段取值源 kind=<...>，留待 v1.1」。
- 未知 `ValueSource.Kind` → 报错。
- 本任务**不校验** `PipelineStep.Algo` 是否在算法注册表中（注册表在 T1.2 才建）——Algo 非空即可；算法存在性校验留给 T1.3 编译层。

> （`errorCode` role 字段无需额外约束；`reserved` 写 0、`value` 需 `Source`——可提示但 v1 不强制报错，避免过度校验。）

## 工作方式（TDD）

1. **RED 先行**：先写 `codec/schema_test.go`，覆盖 Validate 的畸形用例（参考 01-track §4.9 第一条清单）：字段越界/重叠、缺 length、多个 length、缺 route、未知 role/type、flag 引用缺失、when 无 flag、from/appliesWith 指向不存在、routeKeyTemplate 未知占位、v1 拒绝 state/counter/timestamp。再加 LoadSchema 成功用例（用总纲 §3.1 的 tcp:logic codec.json 作 fixture）与 LoadErrorMap/DescribeError 用例。运行应失败（类型/函数未实现）。
2. **GREEN**：实现 schema.go + errors.go，使测试通过。
3. `go build ./codec/...` 与 `go vet ./codec/...` 通过；`go test ./codec/...` 全绿、输出干净。
4. **不要** import gopher-lua；**不要**实现 encode/decode/compile/算法注册表；**不要**改 adapter/ 或其它包（本任务零对外行为变更）。
5. **不要 git commit**。

## 验收（self-review 对照）

- 类型字段与上方逐字一致；所有导出字段有 camelCase json tag。
- Validate 每条规则都有对应测试。
- v1 拒绝 state/counter/timestamp 的测试存在且通过。
- LoadSchema 不容忍 codec.lua；Version!=1 报错。
- DescribeError 未命中返回 `""`。
- 包 `codec` 无 gopher-lua 依赖，`go build ./codec/...` 通过。

## 报告

把完整报告写到 `plans/declarative-codec/reports/t1-1-report.md`：实现了什么、测试与结果、TDD 的 RED/GREEN 证据（命令 + 关键输出）、改动文件、self-review 发现、concerns。
返回（<15 行）：Status、改动文件清单、一行测试摘要、concerns、报告路径。
