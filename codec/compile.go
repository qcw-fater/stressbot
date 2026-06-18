// Package codec — 编译层：schema → 不可变编译产物 SchemaCodec。
//
// 本文件（compile.go）只负责编译，不实现 encode/decode/BodyLength/ExpectedRouteKey
// 执行逻辑（T1.4/T1.5 在 engine.go 中以同包方法形式追加）。
//
// 编译期完成所有「字符串 → 索引/掩码/实现」预解析，使热路径（T1.4/T1.5）不再查注册表、
// 不再做字符串解析。算法存在性校验在本任务首次落地（T1.1 刻意推迟）。
//
// 设计契约（与 plans/declarative-codec 总纲 §3.1.4 / T1.3 brief 对齐）：
//   - SchemaCodec 构造后只读、无可变状态（invariant 2：无锁并发安全）。
//   - 缺算法 / 悬空引用 → 编译期 fail loud（中文信息，带 step/field/算法名）。
//   - 不 import gopher-lua；不改 T1.1/T1.2 文件。
//   - onlySmaller（compress「仅当变小才采用」）无法在编译期或 applies() 预判——
//     取决于 encode 时压缩后的实际字节数；它仅在 T1.4 encode 的 compress 步内处理，
//     applies() 不引用。
package codec

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// 枚举（int 常量；集合与 brief 一致）
// ---------------------------------------------------------------------------

// fieldKind：由 schema.Field.Type 解析。
type fieldKind int

const (
	kindUnknown fieldKind = iota
	kindU8
	kindU16
	kindU24
	kindU32
	kindU64
	kindI8
	kindI16
	kindI24
	kindI32
	kindI64
	kindF32
	kindF64
	kindBytes
)

// width 返回该 kind 的固定字节数；bytes 这种变长返回 0（调用方应使用 compiledField.size）。
func (k fieldKind) width() int {
	switch k {
	case kindU8, kindI8:
		return 1
	case kindU16, kindI16:
		return 2
	case kindU24, kindI24:
		return 3
	case kindU32, kindI32, kindF32:
		return 4
	case kindU64, kindI64, kindF64:
		return 8
	default:
		return 0 // bytes/unknown
	}
}

func parseFieldKind(t string) fieldKind {
	switch t {
	case "u8":
		return kindU8
	case "u16":
		return kindU16
	case "u24":
		return kindU24
	case "u32":
		return kindU32
	case "u64":
		return kindU64
	case "i8":
		return kindI8
	case "i16":
		return kindI16
	case "i24":
		return kindI24
	case "i32":
		return kindI32
	case "i64":
		return kindI64
	case "f32":
		return kindF32
	case "f64":
		return kindF64
	case "bytes":
		return kindBytes
	default:
		return kindUnknown
	}
}

// roleKind：header 字段语义角色。
type roleKind int

const (
	roleUnknown roleKind = iota
	roleLength
	roleRoute
	roleErrorCode
	roleFlags
	roleChecksumOut
	roleValue
	roleReserved
)

func parseRole(r string) roleKind {
	switch r {
	case "length":
		return roleLength
	case "route":
		return roleRoute
	case "errorCode":
		return roleErrorCode
	case "flags":
		return roleFlags
	case "checksumOut":
		return roleChecksumOut
	case "value":
		return roleValue
	case "reserved":
		return roleReserved
	default:
		return roleUnknown
	}
}

// stepOp：pipeline step 的算子。
type stepOp int

const (
	opUnknown stepOp = iota
	opCompress
	opEncrypt
	opChecksum
	opHash
)

func parseOp(op string) stepOp {
	switch op {
	case "compress":
		return opCompress
	case "encrypt":
		return opEncrypt
	case "checksum":
		return opChecksum
	case "hash":
		return opHash
	default:
		return opUnknown
	}
}

// onErrorPolicy：动作失败策略（v1：fail/keep；空视为 fail）。
type onErrorPolicy int

const (
	onErrorFail onErrorPolicy = iota // 默认
	onErrorKeep
)

func parseOnError(v string) onErrorPolicy {
	if v == "keep" {
		return onErrorKeep
	}
	return onErrorFail // 含空
}

// produceRegion：派生产物的作用域。
type produceRegion int

const (
	regionUnknown produceRegion = iota
	regionCiphered
	regionBodyPlain
	regionBodyFinal
	regionHeader
	regionFrame
)

func parseProduceRegion(r string) produceRegion {
	switch r {
	case "ciphered":
		return regionCiphered
	case "bodyPlain":
		return regionBodyPlain
	case "bodyFinal":
		return regionBodyFinal
	case "header":
		return regionHeader
	case "frame":
		return regionFrame
	default:
		return regionUnknown
	}
}

// segKind：routeKey 模板段类型。
type segKind int

const (
	segKindLiteral segKind = iota
	segKindField
)

// ---------------------------------------------------------------------------
// 编译产物类型（与 brief 逐字基线一致）
// ---------------------------------------------------------------------------

// SchemaCodec 是 schema 的不可变编译产物。构造后无任何可变状态，
// 任意 goroutine 并发调用无需加锁（兑现 invariant 2）。
type SchemaCodec struct {
	headerSize            int
	trailerSize           int
	lengthIncludesHeader  bool
	lengthIncludesTrailer bool
	lengthField           compiledField   // role:"length"（单一来源）
	fields                []compiledField // route/errorCode/flags/checksumOut/value/reserved
	routeKeySegs          []routeSeg      // 模板预解析：literal | {fieldIdx}
	steps                 []compiledStep
	errorMap              map[uint64]string
	// 无任何可变字段
}

// compiledField 是单个 header 字段的预解析形式。
type compiledField struct {
	offset, size int
	kind         fieldKind           // 由 Type 解析：u8/u16/.../bytes
	endian       binary.ByteOrder    // le|be（缺省回退 EndianDefault）
	role         roleKind            // length|route|errorCode|flags|checksumOut|value|reserved
	flagBits     []int               // role:flags 持有的位索引（来自 Bits[].Bit）
	checksumRef  stepProduceRef      // role:checksumOut：预解析 (stepIdx, produceName)
	source       compiledValueSource // role:value（v1 const/route）
	name         string              // 原字段名（调试/错误信息）
}

// stepProduceRef 指向某 step 的某个 produce。
type stepProduceRef struct {
	stepIdx     int // -1 表示无引用
	produceName string
}

// compiledValueSource 是 role:"value" 字段的取值源（v1：const|route）。
type compiledValueSource struct {
	kind  string // const|route（state/counter/timestamp 已在 Validate 拒绝）
	value int64  // const
	key   string // route
}

// routeSeg 是 routeKey 模板的一段：字面量或字段引用。
type routeSeg struct {
	segKind  segKind
	literal  string // segKindLiteral 时有效
	fieldIdx int    // segKindField 时：指向 c.fields 中某 route 字段下标
}

// compiledStep 是 pipeline step 的预解析形式。
type compiledStep struct {
	op         stepOp       // compress|encrypt|checksum|hash
	impl       any          // Cipher/Compressor/Checksum/Hasher（注册表 eager 查得）
	flagMask   uint64       // 无 flag 则 0（无条件步）
	encodeWhen compiledWhen // **encode-only**；decode 路径不引用（契约 A）
	encOffset  int          // 单向 encode 偏移（契约 C；每份 codec 单 transport）
	decOffset  int          // 单向 decode 偏移
	produces   []compiledProduce
	onError    onErrorPolicy
	name       string
	params     map[string]any // 来自 PipelineStep.Params（透传给算法 impl；T1.5 修复 T1.4 漏存）
	keyLen     int            // 来自 PipelineStep.KeyLen（encrypt key 长度要求；0 表示不校验；T1.5 修复）
}

// compiledProduce 是某 step 声明的派生产物（如 bcc）。
//
// impl 由 produces.Algo 决定：先查 checksum 注册表，未命中再查 hash 注册表，
// 均未命中则编译期 fail loud。两者之一非 nil。
type compiledProduce struct {
	name         string   // 产物名
	algo         string   // 原算法名（调试）
	checksumImpl Checksum // algo 在 checksum 注册表命中时非 nil
	hasherImpl   Hasher   // algo 在 hash 注册表命中时非 nil
	region       produceRegion
}

// compiledWhen 是 encode 期判定 step 是否生效的条件（decode 不重算——纯看 flag 位）。
//
// onlySmaller（compress「仅当变小才采用」）**无法在编译期或 applies() 预判**——
// 它取决于 encode 时压缩后的实际字节数。因此 onlySmaller 不进 applies()，
// 仅在 T1.4 encode 的 compress 步内「先压缩、比对大小、变小才采用并置 flag」专门处理。
type compiledWhen struct {
	enabled        bool // 无 When 时为「无条件」（applies 总返回 true）
	minBodyLen     int
	onlySmaller    bool // 仅 compress；applies() 不引用，仅 T1.4 encode 步内使用
	requireKey     bool
	appliesWithIdx int // -1 无；指向依赖 step 的下标
	guards         []compiledGuard
}

// compiledGuard 是 when.guards 的一项。
type compiledGuard struct {
	fieldIdx  int    // 指向某 route 字段下标
	fieldName string // route 字段名（applies() 在 route map 中按名取值）
	op        string // eq|neq|gt|gte|lt|lte
	value     int64
}

// applies 判定 encode 期 step 是否生效（**不含 onlySmaller 判定**）。
//
// 参数：
//   - bodyLen：当前待 encode 的明文 body 字节数。
//   - hasKey：调用方是否持有本 step 的 key（requireKey 用）。
//   - route：route 字段名 → 当前取值（guards 用，可为 nil）。
//
// onlySmaller（compress 变小才采用）由 T1.4 encode 步内处理，本函数不参与。
func (w *compiledWhen) applies(bodyLen int, hasKey bool, route map[string]any) bool {
	if !w.enabled {
		return true // 无条件
	}
	if bodyLen < w.minBodyLen {
		return false
	}
	if w.requireKey && !hasKey {
		return false
	}
	for _, g := range w.guards {
		if !g.satisfied(route) {
			return false
		}
	}
	// appliesWith 依赖（前置 step 是否生效）由 T1.4 在调用 applies 前自行串行判断
	// （需要前一步的判定结果），这里不重复实现——appliesWithIdx 仅作预解析下标。
	return true
}

// satisfied 判定单条 guard 是否满足。
func (g *compiledGuard) satisfied(route map[string]any) bool {
	if route == nil {
		// 无 route 上下文：保守视为不满足（guard 无法求值）。
		// T1.4 encode 总会传入 route。
		return false
	}
	// guard.fieldIdx 指向 compiledField；但本函数只有 route map（字段名→值），
	// 故需要由调用方在 applies() 中提供字段名→值映射。这里约定 route map 的 key
	// 为 route 字段名；guard 求值需要名字，由编译期一并存入（见 compiledGuard）。
	v, ok := g.routeValue(route)
	if !ok {
		return false
	}
	switch g.op {
	case "eq":
		return v == g.value
	case "neq":
		return v != g.value
	case "gt":
		return v > g.value
	case "gte":
		return v >= g.value
	case "lt":
		return v < g.value
	case "lte":
		return v <= g.value
	default:
		return false
	}
}

// routeValue 从 route map 中按 guard.fieldIdx 关联的字段名取 int64 值。
//
// 注意：compiledGuard.fieldIdx 指向 SchemaCodec.fields（route 字段下标）。
// 本方法无 codec 反向引用，故由调用方在 applies() 中映射——为保持 compiledWhen
// 自包含、可单元测试，guard 同时存字段名（编译期填入），applies() 接收的 route map
// 用字段名作 key。见编译期编译 guard 时填 name。
func (g *compiledGuard) routeValue(route map[string]any) (int64, bool) {
	if g.fieldName == "" {
		return 0, false
	}
	raw, ok := route[g.fieldName]
	if !ok {
		return 0, false
	}
	switch x := raw.(type) {
	case int:
		return int64(x), true
	case int8:
		return int64(x), true
	case int16:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case uint:
		return int64(x), true
	case uint8:
		return int64(x), true
	case uint16:
		return int64(x), true
	case uint32:
		return int64(x), true
	case uint64:
		return int64(x), true
	case float64:
		return int64(x), true
	case float32:
		return int64(x), true
	default:
		return 0, false
	}
}

// ---------------------------------------------------------------------------
// 只读访问器（执行方法留给 engine.go）
// ---------------------------------------------------------------------------

// HeaderSize 返回帧头字节数（来自 schema.Frame.HeaderSize）。
func (c *SchemaCodec) HeaderSize() int { return c.headerSize }

// ---------------------------------------------------------------------------
// NewSchemaCodec 构造
// ---------------------------------------------------------------------------

// NewSchemaCodec 把已 Load 的 *CodecSchema 一次性编译成不可变的 *SchemaCodec。
//
// 编译步骤（与 T1.3 brief「编译期必须完成的预解析」逐条对应）：
//  1. 调 schema.Validate()；失败直接返回其 error。
//  2. 解析 EndianDefault、frame 布局。
//  3. 收集 lengthField（role:"length"）与其它 fields；解析 endian/kind/role。
//  4. flags：建立 bitName → mask，并记录每 flags 字段 flagBits。
//  5. routeKey 模板解析为 []routeSeg。
//  6. 每个 step → compiledStep；查算法注册表（缺失 fail loud），编译 When、produces。
//  7. checksumOut.from 解析为 (stepIdx, produceName)。
//
// errorMap 直接采用入参（允许 nil → 空 map）。NewSchemaCodec 不读文件。
func NewSchemaCodec(schema *CodecSchema, errorMap map[uint64]string) (*SchemaCodec, error) {
	if schema == nil {
		return nil, fmt.Errorf("codec schema 编译失败：schema 为空")
	}
	if err := schema.Validate(); err != nil {
		return nil, err
	}

	c := &SchemaCodec{
		headerSize:            schema.Frame.HeaderSize,
		trailerSize:           schema.Frame.TrailerSize,
		lengthIncludesHeader:  schema.Frame.LengthIncludesHeader,
		lengthIncludesTrailer: schema.Frame.LengthIncludesTrailer,
	}

	// EndianDefault。
	defaultEndian, err := resolveEndian(schema.EndianDefault)
	if err != nil {
		// Validate 已保证合法，理论上不会进；保险起见 fail loud。
		return nil, fmt.Errorf("codec schema 编译失败：endianDefault 非法 %q：%w", schema.EndianDefault, err)
	}

	// stepName → stepIdx（用于 checksumOut.from 与 appliesWith 解析）。
	stepIdx := make(map[string]int, len(schema.Pipeline))
	for i := range schema.Pipeline {
		stepIdx[schema.Pipeline[i].Name] = i
	}
	// stepName → 该 step 的 produce 名字集合（用于 checksumOut.from 校验存在性）。
	produceLocByName := make(map[string]map[string]struct{}, len(schema.Pipeline))
	for i := range schema.Pipeline {
		st := &schema.Pipeline[i]
		m := make(map[string]struct{}, len(st.Produces))
		for j := range st.Produces {
			m[st.Produces[j].Name] = struct{}{}
		}
		produceLocByName[st.Name] = m
	}

	// ---- flags：bitName → mask；同时记 flagBits ----
	// Validate 保证同一命名位跨 flags 字段不冲突语义；这里构建全局名→位掩码映射。
	flagMaskByName := make(map[string]uint64)
	flagBitByName := make(map[string]int) // bit index
	for i := range schema.Header {
		f := &schema.Header[i]
		if f.Role != "flags" {
			continue
		}
		for _, b := range f.Bits {
			flagMaskByName[b.Name] = 1 << uint64(b.Bit)
			flagBitByName[b.Name] = b.Bit
		}
	}

	// ---- header fields ----
	routeFieldIdx := make(map[string]int) // route 字段名 → c.fields 下标
	for i := range schema.Header {
		f := &schema.Header[i]
		cf := compiledField{
			offset: f.Offset,
			size:   f.Size,
			kind:   parseFieldKind(f.Type),
			role:   parseRole(f.Role),
			name:   f.Name,
		}
		// endian：字段级优先，否则回退 EndianDefault。
		if f.Endian == "be" {
			cf.endian = binary.BigEndian
		} else if f.Endian == "le" {
			cf.endian = binary.LittleEndian
		} else {
			cf.endian = defaultEndian
		}
		// flags：记录位索引。
		if cf.role == roleFlags {
			cf.flagBits = make([]int, 0, len(f.Bits))
			for _, b := range f.Bits {
				cf.flagBits = append(cf.flagBits, b.Bit)
			}
		}
		// value：编译 source。
		if cf.role == roleValue && f.Source != nil {
			cf.source = compiledValueSource{
				kind:  f.Source.Kind,
				value: f.Source.Value,
				key:   f.Source.Key,
			}
		}
		// checksumOut：from → stepProduceRef；Validate 已保证引用存在。
		if cf.role == roleChecksumOut && f.From != "" {
			ref, err := resolveProduceRef(f.From, f.Name, stepIdx, produceLocByName)
			if err != nil {
				return nil, err
			}
			cf.checksumRef = ref
		}

		if cf.role == roleLength {
			c.lengthField = cf
		} else {
			if cf.role == roleRoute {
				routeFieldIdx[cf.name] = len(c.fields)
			}
			c.fields = append(c.fields, cf)
		}
	}

	// ---- routeKey 模板 ----
	segs, err := parseRouteKeyTemplate(schema.RouteKeyTmpl, routeFieldIdx)
	if err != nil {
		return nil, fmt.Errorf("codec schema 编译失败：routeKeyTemplate 解析：%w", err)
	}
	c.routeKeySegs = segs

	// ---- pipeline steps ----
	c.steps = make([]compiledStep, 0, len(schema.Pipeline))
	for i := range schema.Pipeline {
		st := &schema.Pipeline[i]
		cs, err := compileStep(st, flagMaskByName, routeFieldIdx, stepIdx)
		if err != nil {
			return nil, err
		}
		c.steps = append(c.steps, cs)
	}

	// ---- errorMap：nil → 空 map（保持非 nil 便于调用方安全读）----
	if errorMap == nil {
		c.errorMap = make(map[uint64]string)
	} else {
		// 浅拷贝以隔离可变性（调用方后续修改入参不应影响编译产物）。
		c.errorMap = make(map[uint64]string, len(errorMap))
		for k, v := range errorMap {
			c.errorMap[k] = v
		}
	}

	return c, nil
}

// resolveEndian 把 "le"/"be" 解析为 binary.ByteOrder。
func resolveEndian(s string) (binary.ByteOrder, error) {
	switch s {
	case "le":
		return binary.LittleEndian, nil
	case "be":
		return binary.BigEndian, nil
	default:
		return nil, fmt.Errorf("endian 必须为 le 或 be（当前 %q）", s)
	}
}

// resolveProduceRef 把 checksumOut.from "<step>.<output>" 解析为 stepProduceRef。
// Validate 已保证引用存在；此处若意外悬空仍 fail loud。
func resolveProduceRef(from, fieldName string, stepIdx map[string]int, produceLocByName map[string]map[string]struct{}) (stepProduceRef, error) {
	m := checksumFromRe.FindStringSubmatch(from)
	if m == nil {
		return stepProduceRef{stepIdx: -1}, fmt.Errorf("checksumOut 字段 %q 的 from %q 格式非法", fieldName, from)
	}
	stepName, produceName := m[1], m[2]
	si, ok := stepIdx[stepName]
	if !ok {
		return stepProduceRef{stepIdx: -1}, fmt.Errorf("checksumOut 字段 %q 的 from %q 指向不存在的 step %q", fieldName, from, stepName)
	}
	produces, ok := produceLocByName[stepName]
	if !ok {
		return stepProduceRef{stepIdx: -1}, fmt.Errorf("checksumOut 字段 %q 的 from %q 指向 step %q 无任何 produce", fieldName, from, stepName)
	}
	if _, has := produces[produceName]; !has {
		return stepProduceRef{stepIdx: -1}, fmt.Errorf("checksumOut 字段 %q 的 from %q 指向 step %q 中不存在的 produce %q", fieldName, from, stepName, produceName)
	}
	return stepProduceRef{stepIdx: si, produceName: produceName}, nil
}

// parseRouteKeyTemplate 把 "{cmd}:{act}" 切成 []routeSeg。
// 占位名必须是某 route 字段名（Validate 已保证）。
func parseRouteKeyTemplate(tmpl string, routeFieldIdx map[string]int) ([]routeSeg, error) {
	out := make([]routeSeg, 0, 4)
	var buf strings.Builder
	flushLiteral := func() {
		if buf.Len() > 0 {
			out = append(out, routeSeg{segKind: segKindLiteral, literal: buf.String()})
			buf.Reset()
		}
	}
	i := 0
	for i < len(tmpl) {
		ch := tmpl[i]
		if ch == '{' {
			end := strings.IndexByte(tmpl[i:], '}')
			if end < 0 {
				return nil, fmt.Errorf("占位符未闭合：%q", tmpl[i:])
			}
			flushLiteral()
			name := tmpl[i+1 : i+end]
			idx, ok := routeFieldIdx[name]
			if !ok {
				return nil, fmt.Errorf("占位符 {%s} 未指向任何 route 字段", name)
			}
			out = append(out, routeSeg{segKind: segKindField, fieldIdx: idx})
			i += end + 1
			continue
		}
		buf.WriteByte(ch)
		i++
	}
	flushLiteral()
	return out, nil
}

// compileStep 把单个 PipelineStep 编译为 compiledStep。
func compileStep(st *PipelineStep, flagMaskByName map[string]uint64, routeFieldIdx, stepIdx map[string]int) (compiledStep, error) {
	op := parseOp(st.Op)
	cs := compiledStep{
		op:      op,
		name:    st.Name,
		onError: parseOnError(st.OnError),
		params:  st.Params, // T1.5：透传给算法 impl（修复 T1.4 漏存导致 rol 等参数丢失）
		keyLen:  st.KeyLen, // T1.5：encrypt key 长度要求（修复 T1.4 漏存导致硬编码 32）
	}

	// flagMask：step.Flag → mask。
	if st.Flag != "" {
		mask, ok := flagMaskByName[st.Flag]
		if !ok {
			// Validate 已保证；保险起见 fail loud。
			return compiledStep{}, fmt.Errorf("codec schema 编译失败：步骤 %q 的 flag %q 未在任何 flags 字段命名位中声明", st.Name, st.Flag)
		}
		cs.flagMask = mask
	}

	// impl：按 op 查对应注册表；缺失 fail loud。
	switch op {
	case opCompress:
		impl, ok := LookupCompressor(st.Algo)
		if !ok {
			return compiledStep{}, fmt.Errorf("codec schema 编译失败：步骤 %q 引用未知算法 %q", st.Name, st.Algo)
		}
		cs.impl = impl
	case opEncrypt:
		impl, ok := LookupCipher(st.Algo)
		if !ok {
			return compiledStep{}, fmt.Errorf("codec schema 编译失败：步骤 %q 引用未知算法 %q", st.Name, st.Algo)
		}
		cs.impl = impl
		// offset：encrypt 步的 enc/dec 偏移。
		if st.Offset != nil {
			cs.encOffset = st.Offset.Encode
			cs.decOffset = st.Offset.Decode
		}
	case opChecksum:
		impl, ok := LookupChecksum(st.Algo)
		if !ok {
			return compiledStep{}, fmt.Errorf("codec schema 编译失败：步骤 %q 引用未知算法 %q", st.Name, st.Algo)
		}
		cs.impl = impl
	case opHash:
		impl, ok := LookupHasher(st.Algo)
		if !ok {
			return compiledStep{}, fmt.Errorf("codec schema 编译失败：步骤 %q 引用未知算法 %q", st.Name, st.Algo)
		}
		cs.impl = impl
	default:
		return compiledStep{}, fmt.Errorf("codec schema 编译失败：步骤 %q 未知 op %q", st.Name, st.Op)
	}

	// produces：algo 查 checksum/hash 注册表，缺失 fail loud。
	cs.produces = make([]compiledProduce, 0, len(st.Produces))
	for j := range st.Produces {
		p := &st.Produces[j]
		cp := compiledProduce{
			name:   p.Name,
			algo:   p.Algo,
			region: parseProduceRegion(p.Region),
		}
		if chk, ok := LookupChecksum(p.Algo); ok {
			cp.checksumImpl = chk
		} else if h, ok := LookupHasher(p.Algo); ok {
			cp.hasherImpl = h
		} else {
			return compiledStep{}, fmt.Errorf("codec schema 编译失败：步骤 %q 的 produces %q 引用未知算法 %q（checksum/hash 注册表均未命中）", st.Name, p.Name, p.Algo)
		}
		cs.produces = append(cs.produces, cp)
	}

	// encodeWhen：编译 When。
	if st.When != nil {
		cs.encodeWhen = compileWhen(st.When, routeFieldIdx, stepIdx)
	}

	return cs, nil
}

// compileWhen 把 StepCond 编译为 compiledWhen。
func compileWhen(w *StepCond, routeFieldIdx, stepIdx map[string]int) compiledWhen {
	cw := compiledWhen{
		enabled:     true,
		minBodyLen:  w.MinBodyLen,
		onlySmaller: w.OnlySmaller,
		requireKey:  w.RequireKey,
	}
	cw.appliesWithIdx = -1
	if w.AppliesWith != "" {
		if idx, ok := stepIdx[w.AppliesWith]; ok {
			cw.appliesWithIdx = idx
		}
	}
	cw.guards = make([]compiledGuard, 0, len(w.Guards))
	for _, g := range w.Guards {
		cg := compiledGuard{
			op:    g.Op,
			value: g.Value,
		}
		if idx, ok := routeFieldIdx[g.Field]; ok {
			cg.fieldIdx = idx
		}
		cg.fieldName = g.Field // 供 applies() 在 route map 中按字段名取值
		cw.guards = append(cw.guards, cg)
	}
	return cw
}
