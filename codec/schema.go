// Package codec 是声明式协议编解码引擎的纯 Go 实现。
//
// 本文件（schema.go）只负责 schema 类型定义、JSON 反序列化（LoadSchema）
// 与结构校验（Validate）。算法注册表、compile、encode/decode 在后续文件中实现。
//
// 设计要点（与 plans/declarative-codec 总纲 §3.1 / T1 brief 对齐）：
//   - 不 import gopher-lua；与 adapter/ 完全解耦。
//   - 不做任何 codec.lua 兼容；畸形配置必须以中文错误显式失败。
//   - Validate 聚合多条错误后一次性返回，方便前端展示。
package codec

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// CodecSchema 是 codec.json 的根类型。
type CodecSchema struct {
	Version       int                 `json:"version"`
	EndianDefault string              `json:"endianDefault"` // "le" | "be"
	Frame         FrameSpec           `json:"frame"`
	Header        []Field             `json:"header"`
	RouteKeyTmpl  string              `json:"routeKeyTemplate"` // 如 "{cmd}:{act}"
	Pipeline      []PipelineStep      `json:"pipeline"`
	Heartbeat     *HeartbeatConfigDef `json:"heartbeat,omitempty"` // 连接级可选心跳；nil 表示不启用
}

// FrameSpec 描述帧的物理布局（不含字段类型——类型由 Header 中唯一的
// role:"length" 字段携带，frame 不再重复声明长度字段口径位置）。
type FrameSpec struct {
	HeaderSize            int  `json:"headerSize"`
	TrailerSize           int  `json:"trailerSize"`           // 默认 0
	LengthIncludesHeader  bool `json:"lengthIncludesHeader"`  // length 字段是否含 header
	LengthIncludesTrailer bool `json:"lengthIncludesTrailer"` // length 字段是否含 trailer
}

// Field 是 header 中的一个字段。Type/Role/Endian 见总纲 §3.1.1/§3.1.2。
type Field struct {
	Name   string       `json:"name"`
	Type   string       `json:"type"`
	Endian string       `json:"endian,omitempty"` // le|be；缺省回退 EndianDefault
	Offset int          `json:"offset"`
	Size   int          `json:"size"`
	Role   string       `json:"role"` // length|route|errorCode|flags|checksumOut|value|reserved
	Bits   []FlagBit    `json:"bits,omitempty"`
	From   string       `json:"from,omitempty"`   // role=checksumOut: "<step>.<output>"
	Source *ValueSource `json:"source,omitempty"` // role=value
	Repr   string       `json:"repr,omitempty"`   // type=bytes: hex|base64|ascii
}

// FlagBit 是 role:"flags" 字段的一个命名位。
type FlagBit struct {
	Name string `json:"name"`
	Bit  int    `json:"bit"`
}

// ValueSource 决定 role:"value" 字段的 encode 取值。
//
// v1 仅实现 const、route；state/counter/timestamp 在 Validate 阶段直接报「v1 不支持」。
type ValueSource struct {
	Kind  string `json:"kind"`
	Value int64  `json:"value,omitempty"` // const
	Key   string `json:"key,omitempty"`   // state / route
	Start int64  `json:"start,omitempty"` // counter (v1.1)
	Step  int64  `json:"step,omitempty"`  // counter (v1.1)
	Wrap  int64  `json:"wrap,omitempty"`  // counter 回绕 (v1.1)
	Unit  string `json:"unit,omitempty"`  // timestamp: s|ms (v1.1)
}

// PipelineStep 是 encode/decode 管线的一步。
type PipelineStep struct {
	Op       string         `json:"op"`   // compress|encrypt|checksum|hash
	Name     string         `json:"name"` // 供 flag/from/appliesWith 引用
	Algo     string         `json:"algo"` // 注册表键（Validate 阶段校验存在性）
	Flag     string         `json:"flag,omitempty"`
	Params   map[string]any `json:"params,omitempty"`
	KeyLen   int            `json:"keyLen,omitempty"` // encrypt
	Offset   *StepOffset    `json:"offset,omitempty"` // encrypt
	Produces []StepProduce  `json:"produces,omitempty"`
	Over     *OverSpec      `json:"over,omitempty"`    // 独立 checksum/hash 步
	OnError  string         `json:"onError,omitempty"` // fail(默认)|keep
	When     *StepCond      `json:"when,omitempty"`
}

// StepOffset 是 encrypt 步的单向偏移（每份 codec 单 transport）。
type StepOffset struct {
	Encode int `json:"encode"` // 缺省 0；如 udp:battle = 11
	Decode int `json:"decode"` // 缺省 0
}

// StepProduce 是某步声明的派生产物（如 bcc）。
type StepProduce struct {
	Name   string `json:"name"`   // 产物名
	Algo   string `json:"algo"`   // 计算算法（如 xor8）
	Region string `json:"region"` // ciphered|bodyPlain|bodyFinal|header|frame
}

// OverSpec 描述独立 checksum/hash 步的作用域。
type OverSpec struct {
	Kind       string `json:"kind"` // bodyPlain|bodyFinal|header|frame|range
	RangeStart int    `json:"rangeStart,omitempty"`
	RangeEnd   int    `json:"rangeEnd,omitempty"`
}

// StepCond 是 pipeline 步的结构化条件（encode 决策；decode 不重算）。
type StepCond struct {
	MinBodyLen  int     `json:"minBodyLen,omitempty"`
	OnlySmaller bool    `json:"onlySmaller,omitempty"`
	RequireKey  bool    `json:"requireKey,omitempty"`
	AppliesWith string  `json:"appliesWith,omitempty"`
	Guards      []Guard `json:"guards,omitempty"`
}

// Guard 是 when.guards 的一个条件项。
type Guard struct {
	Field string `json:"field"`
	Op    string `json:"op"` // eq|neq|gt|gte|lt|lte
	Value int64  `json:"value"`
}

// HeartbeatConfigDef 是 codec 连接级可选心跳配置。
// 心跳强绑定当前连接：transport/service 由 codec 文件名对应的连接名决定，不在本对象内重复配置。
type HeartbeatConfigDef struct {
	IntervalMs       int              `json:"intervalMs"`
	Route            any              `json:"route,omitempty"`
	C2SProto         string           `json:"c2sProto,omitempty"`
	Bindings         []FieldBind      `json:"bindings,omitempty"`
	HeartbeatFields  []HeartbeatField `json:"heartbeatFields,omitempty"`
	SkipWhenMissing  bool             `json:"skipWhenMissing,omitempty"`
	RequireSecretKey bool             `json:"requireSecretKey,omitempty"`
}

// FieldBind 是心跳 protobuf body 模式复用的字段绑定定义。
// 字段语义与 engine.FieldBind 同构，codec 包只负责承载和基础校验，不解释业务值。
type FieldBind struct {
	Field         string         `json:"field"`
	Type          string         `json:"type"`
	Value         any            `json:"value"`
	Source        string         `json:"source"`
	Path          string         `json:"path"`
	Values        []any          `json:"values"`
	Entries       []MapEntryBind `json:"entries"`
	Required      bool           `json:"required"`
	Filters       []FilterDef    `json:"filters"`
	Min           int            `json:"min"`
	Max           int            `json:"max"`
	Precision     int            `json:"precision"`
	Length        int            `json:"length"`
	Count         int            `json:"count"`
	Charset       string         `json:"charset"`
	ExcludeSource string         `json:"excludeSource"`
	Optional      bool           `json:"optional"`
	Wrap          bool           `json:"wrap"`
	StoreAs       string         `json:"storeAs"`
	KeySource     string         `json:"keySource"`
	Condition     string         `json:"condition"`
}

type MapEntryBind struct {
	Key   any       `json:"key"`
	Value FieldBind `json:"value"`
}

type FilterDef struct {
	Path   string `json:"path"`
	Op     string `json:"op"`
	Value  any    `json:"value"`
	Source string `json:"source"`
	Mode   string `json:"mode"`
}

// HeartbeatField 声明 raw-binary 心跳 body 的一个小端字段。
type HeartbeatField struct {
	Type       string   `json:"type"`
	Source     string   `json:"source"`
	Value      *int64   `json:"value,omitempty"`
	FloatValue *float64 `json:"floatValue,omitempty"`
	Key        string   `json:"key,omitempty"`
	Min        *int64   `json:"min,omitempty"`
	Max        *int64   `json:"max,omitempty"`
	Start      *int64   `json:"start,omitempty"`
	Step       *int64   `json:"step,omitempty"`
	Unit       string   `json:"unit,omitempty"`
}

const (
	BindState                   = "state"
	HeartbeatSourceCounter      = "counter"
	HeartbeatSourceFixed        = "fixed"
	HeartbeatSourceState        = "state"
	HeartbeatSourceStateCounter = "stateCounter"
	HeartbeatSourceTimestamp    = "timestamp"
	HeartbeatSourceRandomInt    = "randomInt"
)

// ---------- 集合（v1 冻结值） ----------

var validFieldTypes = map[string]int{
	// type → 固定宽度字节数；-1 表示需显式 size（如 bytes）。
	"u8": 1, "u16": 2, "u24": 3, "u32": 4, "u64": 8,
	"i8": 1, "i16": 2, "i24": 3, "i32": 4, "i64": 8,
	"f32": 4, "f64": 8,
	"bytes": -1,
}

var validRoles = map[string]struct{}{
	"length": {}, "route": {}, "errorCode": {}, "flags": {},
	"checksumOut": {}, "value": {}, "reserved": {},
}

var validOps = map[string]struct{}{
	"compress": {}, "encrypt": {}, "checksum": {}, "hash": {},
}

var validProduceRegions = map[string]struct{}{
	"ciphered": {}, "bodyPlain": {}, "bodyFinal": {}, "header": {}, "frame": {},
}

var validOverKinds = map[string]struct{}{
	"bodyPlain": {}, "bodyFinal": {}, "header": {}, "frame": {}, "range": {},
}

var validGuardOps = map[string]struct{}{
	"eq": {}, "neq": {}, "gt": {}, "gte": {}, "lt": {}, "lte": {},
}

var validOnError = map[string]struct{}{
	"fail": {}, "keep": {},
}

var validValueSourceKinds = map[string]bool{
	"const": true, "route": true,
	"state": false, "counter": false, "timestamp": false, // v1.1
}

var checksumFromRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)$`)

// ---------- LoadSchema ----------

// LoadSchema 读取 codec.json，json.Unmarshal 后调用 Validate。不做任何 codec.lua 兼容。
func LoadSchema(path string) (*CodecSchema, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 codec schema 文件失败 %q: %w", path, err)
	}
	var s CodecSchema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("解析 codec schema 文件失败 %q: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("codec schema 校验失败 %q: %w", path, err)
	}
	return &s, nil
}

// ---------- Validate ----------

// errCollector 聚合多条中文错误，最终一次性返回。
type errCollector struct {
	msgs []string
}

func (e *errCollector) addf(format string, args ...any) {
	e.msgs = append(e.msgs, fmt.Sprintf(format, args...))
}

func (e *errCollector) err() error {
	if len(e.msgs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(e.msgs, "; "))
}

// Validate 对 schema 做结构校验，聚合多条中文错误后返回。
func (s *CodecSchema) Validate() error {
	var ec errCollector
	s.validateBase(&ec)
	s.validateHeader(&ec)
	s.validateRouteKeyTemplate(&ec)
	s.validatePipeline(&ec)
	s.validateHeartbeat(&ec)
	return ec.err()
}

func (s *CodecSchema) validateBase(ec *errCollector) {
	if s.Version != 1 {
		ec.addf("codec schema version 必须为 1（当前 %d）", s.Version)
	}
	if s.EndianDefault != "le" && s.EndianDefault != "be" {
		ec.addf("endianDefault 必须为 le 或 be（当前 %q）", s.EndianDefault)
	}
	if s.Frame.HeaderSize <= 0 {
		ec.addf("frame.headerSize 必须大于 0（当前 %d）", s.Frame.HeaderSize)
	}
	if s.Frame.TrailerSize < 0 {
		ec.addf("frame.trailerSize 不能为负（当前 %d）", s.Frame.TrailerSize)
	}
	if strings.TrimSpace(s.RouteKeyTmpl) == "" {
		ec.addf("routeKeyTemplate 不能为空")
	}
}

func (s *CodecSchema) validateHeader(ec *errCollector) {
	headerSize := s.Frame.HeaderSize

	// 字段名唯一性 + 基础属性 + type/role 合法性。
	names := make(map[string]int) // name → first index
	for i := range s.Header {
		f := &s.Header[i]
		prefix := fmt.Sprintf("header 字段 %q (index %d)", f.Name, i)
		if strings.TrimSpace(f.Name) == "" {
			ec.addf("header 字段名不能为空 (index %d)", i)
			prefix = fmt.Sprintf("header 字段 (index %d)", i)
		} else if prev, dup := names[f.Name]; dup {
			ec.addf("header 字段名 %q 重复（与 index %d 冲突，字段名必须唯一）", f.Name, prev)
		} else {
			names[f.Name] = i
		}
		if f.Offset < 0 {
			ec.addf("%s：offset 不能为负（当前 %d）", prefix, f.Offset)
		}
		if f.Size <= 0 {
			ec.addf("%s：size 必须大于 0（当前 %d）", prefix, f.Size)
		}
		if f.Offset < 0 || f.Offset+f.Size > headerSize {
			ec.addf("%s：物理区间 [offset=%d, offset+size=%d) 越界（headerSize=%d）", prefix, f.Offset, f.Offset+f.Size, headerSize)
		}
		width, known := validFieldTypes[f.Type]
		if !known {
			ec.addf("%s：未知 type %q", prefix, f.Type)
		} else if width > 0 {
			if f.Size != width {
				ec.addf("%s：type %q 的 size 必须为 %d（当前 %d）", prefix, f.Type, width, f.Size)
			}
		} else { // bytes
			if f.Size <= 0 {
				ec.addf("%s：type bytes 必须显式指定 size>0", prefix)
			}
		}
		// endian（若指定）必须合法；缺省回退 EndianDefault 由编译层处理。
		if f.Endian != "" && f.Endian != "le" && f.Endian != "be" {
			ec.addf("%s：endian 必须为 le 或 be（当前 %q）", prefix, f.Endian)
		}
		if _, ok := validRoles[f.Role]; !ok {
			ec.addf("%s：未知 role %q", prefix, f.Role)
		}
	}

	// 物理区间不重叠（位域共享同一整数不算重叠，因为它们在同一个 Field 内）。
	type span struct {
		name       string
		start, end int
	}
	spans := make([]span, 0, len(s.Header))
	for i := range s.Header {
		f := &s.Header[i]
		if f.Offset < 0 || f.Size <= 0 {
			continue // 已报错
		}
		spans = append(spans, span{name: f.Name, start: f.Offset, end: f.Offset + f.Size})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i := 1; i < len(spans); i++ {
		if spans[i].start < spans[i-1].end {
			ec.addf("header 字段物理区间重叠：%s [offset=%d,+%d) 与 %s [offset=%d,+%d)",
				spans[i-1].name, spans[i-1].start, spans[i-1].end-spans[i-1].start,
				spans[i].name, spans[i].start, spans[i].end-spans[i].start)
		}
	}

	// role 统计与约束。
	var lengthCount, routeCount int
	routeNames := make(map[string]struct{})
	flagsBits := make(map[string][]FlagBit) // flag field name → bits（用于 pipeline flag 引用解析）
	for i := range s.Header {
		f := &s.Header[i]
		switch f.Role {
		case "length":
			lengthCount++
		case "route":
			routeCount++
			routeNames[f.Name] = struct{}{}
		case "flags":
			flagsBits[f.Name] = f.Bits
			validateFlagBits(ec, f)
		case "checksumOut":
			validateChecksumOut(ec, f)
		case "value":
			validateValueSource(ec, f)
		}
	}
	if lengthCount == 0 {
		ec.addf("header 缺少 role:\"length\" 字段（必须有且仅有 1 个）")
	} else if lengthCount > 1 {
		ec.addf("header 有 %d 个 role:\"length\" 字段（必须有且仅有 1 个）", lengthCount)
	}
	if routeCount == 0 {
		ec.addf("header 缺少 role:\"route\" 字段（至少 1 个）")
	}

	// 暂存 routeNames / flagsBits 供 pipeline 与 routeKeyTemplate 校验复用。
	s.validatePipelineRefs(ec, routeNames, flagsBits)
}

func validateFlagBits(ec *errCollector, f *Field) {
	bitWidth := f.Size * 8
	seenBit := make(map[int]string)
	seenName := make(map[string]int)
	for i := range f.Bits {
		b := &f.Bits[i]
		if b.Bit < 0 || b.Bit >= bitWidth {
			ec.addf("flags 字段 %q 的 bit %d 超出 [0,%d)（bit 位非法）", f.Name, b.Bit, bitWidth)
		}
		if prev, dup := seenBit[b.Bit]; dup {
			ec.addf("flags 字段 %q 的 bit %d 重复（与 %q 冲突，命名位不能重复）", f.Name, b.Bit, prev)
		} else {
			seenBit[b.Bit] = b.Name
		}
		if strings.TrimSpace(b.Name) == "" {
			ec.addf("flags 字段 %q 的 bit %d 名称为空", f.Name, b.Bit)
		} else if prev, dup := seenName[b.Name]; dup {
			ec.addf("flags 字段 %q 的命名位 %q 重复（与 bit %d 冲突）", f.Name, b.Name, prev)
		} else {
			seenName[b.Name] = b.Bit
		}
	}
}

func validateChecksumOut(ec *errCollector, f *Field) {
	if f.From == "" {
		ec.addf("checksumOut 字段 %q 缺少 from（需 <step>.<output>）", f.Name)
		return
	}
	if !checksumFromRe.MatchString(f.From) {
		ec.addf("checksumOut 字段 %q 的 from %q 不合法（需匹配 <step>.<output>）", f.Name, f.From)
	}
}

func validateValueSource(ec *errCollector, f *Field) {
	if f.Source == nil {
		// v1 不强制：value 字段缺 source 仅提示性，避免过度校验（brief 末注）。
		return
	}
	supported, known := validValueSourceKinds[f.Source.Kind]
	if !known {
		ec.addf("value 字段 %q 的 source.kind %q 未知", f.Name, f.Source.Kind)
		return
	}
	if !supported {
		ec.addf("value 字段 %q 的 source.kind=%q 不支持：v1 不支持的头字段取值源 kind=%q，留待 v1.1", f.Name, f.Source.Kind, f.Source.Kind)
	}
}

// ---------- routeKeyTemplate ----------

var routeKeyPlaceholderRe = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func (s *CodecSchema) validateRouteKeyTemplate(ec *errCollector) {
	if s.RouteKeyTmpl == "" {
		return // 已在 base 报
	}
	for _, m := range routeKeyPlaceholderRe.FindAllStringSubmatch(s.RouteKeyTmpl, -1) {
		// 占位名必须在 routeNames 中。routeNames 由 validatePipelineRefs 接收，
		// 但这里需要独立判断 —— 重算一遍 route 名字集合（轻量）。
		if !s.isRouteField(m[1]) {
			ec.addf("routeKeyTemplate 占位 {%s} 必须指向某个 role:\"route\" 字段（未知占位）", m[1])
		}
	}
}

func (s *CodecSchema) isRouteField(name string) bool {
	for i := range s.Header {
		f := &s.Header[i]
		if f.Role == "route" && f.Name == name {
			return true
		}
	}
	return false
}

// ---------- pipeline ----------

func (s *CodecSchema) validateHeartbeat(ec *errCollector) {
	hb := s.Heartbeat
	if hb == nil {
		return
	}
	if hb.IntervalMs <= 0 {
		ec.addf("heartbeat.intervalMs 必须大于 0（当前 %d）", hb.IntervalMs)
	}
	if hb.C2SProto != "" && len(hb.HeartbeatFields) > 0 {
		ec.addf("heartbeat 不能同时配置 c2sProto 与 heartbeatFields，须二选一")
	}
	if hb.C2SProto == "" && len(hb.Bindings) > 0 {
		ec.addf("heartbeat.bindings 只能在配置 c2sProto 时使用")
	}
}

func (s *CodecSchema) validatePipeline(ec *errCollector) {
	// step name 唯一。
	stepNames := make(map[string]int) // name → index
	for i := range s.Pipeline {
		st := &s.Pipeline[i]
		if strings.TrimSpace(st.Name) == "" {
			ec.addf("pipeline 步骤 (index %d) 缺少 name", i)
		} else if prev, dup := stepNames[st.Name]; dup {
			ec.addf("pipeline 步骤 name %q 重复（与 index %d 冲突，name 必须唯一）", st.Name, prev)
		} else {
			stepNames[st.Name] = i
		}
	}

	// collects all produces (stepName → set of produce names).
	produceMap := make(map[string]map[string]struct{})
	for i := range s.Pipeline {
		st := &s.Pipeline[i]
		prefix := fmt.Sprintf("pipeline 步骤 %q", st.Name)
		if _, ok := validOps[st.Op]; !ok {
			ec.addf("%s：未知 op %q（合法值：compress|encrypt|checksum|hash）", prefix, st.Op)
		}
		if strings.TrimSpace(st.Algo) == "" {
			ec.addf("%s：algo 不能为空", prefix)
		}
		if st.OnError != "" {
			if _, ok := validOnError[st.OnError]; !ok {
				ec.addf("%s：onError %q 不合法（合法值：fail|keep，空视为 fail）", prefix, st.OnError)
			}
		}
		// produces：name 唯一 + region 合法。
		pn := make(map[string]struct{})
		for j := range st.Produces {
			p := &st.Produces[j]
			if _, dup := pn[p.Name]; dup {
				ec.addf("%s：produces 名称 %q 在该步内重复（必须唯一）", prefix, p.Name)
			} else {
				pn[p.Name] = struct{}{}
			}
			if _, ok := validProduceRegions[p.Region]; !ok {
				ec.addf("%s：produces %q 的 region %q 不合法（合法值：ciphered|bodyPlain|bodyFinal|header|frame）", prefix, p.Name, p.Region)
			}
		}
		produceMap[st.Name] = pn
		// offset（encrypt）。
		if st.Op == "encrypt" {
			if st.Offset == nil {
				// 视为 {0,0}；无需报错。
			} else {
				if st.Offset.Encode < 0 {
					ec.addf("%s：encrypt offset.encode 不能为负（当前 %d）", prefix, st.Offset.Encode)
				}
				if st.Offset.Decode < 0 {
					ec.addf("%s：encrypt offset.decode 不能为负（当前 %d）", prefix, st.Offset.Decode)
				}
			}
		}
		// over（独立 checksum/hash 步）。
		if st.Over != nil {
			validateOver(ec, prefix, st.Over, s.Frame.HeaderSize)
		}
		// when。
		if st.When != nil {
			validateWhen(ec, prefix, st.When, stepNames)
		}
	}

	// flag 引用、checksumOut.from 引用解析依赖 flagsBits / routeNames，
	// 由 validatePipelineRefs（在 validateHeader 末调用）处理。
	_ = produceMap
}

// validatePipelineRefs 校验跨 header↔pipeline 的引用：flag、checksumOut.from、when.appliesWith。
// 在 validateHeader 末尾调用，因那时 routeNames/flagsBits 已就绪。
func (s *CodecSchema) validatePipelineRefs(ec *errCollector, routeNames map[string]struct{}, flagsBits map[string][]FlagBit) {
	// 构造 flagName → flagField 反查（全局 flag 名空间）。
	flagNameToField := make(map[string]string) // flag bit name → flags field name
	for fName, bits := range flagsBits {
		for _, b := range bits {
			if _, dup := flagNameToField[b.Name]; dup {
				// 同名命名位跨多个 flags 字段 —— 罕见但允许，引用时只需存在。
			}
			flagNameToField[b.Name] = fName
		}
	}
	_ = routeNames

	// 每个 flag 命名位至多被一个 step 绑定；flag 引用必须存在。
	boundFlag := make(map[string]string) // flag bit name → step name
	for i := range s.Pipeline {
		st := &s.Pipeline[i]
		if st.Flag == "" {
			continue
		}
		if _, ok := flagNameToField[st.Flag]; !ok {
			ec.addf("pipeline 步骤 %q 的 flag %q 未在任何 role:\"flags\" 字段的命名位中声明", st.Name, st.Flag)
			continue
		}
		if prev, dup := boundFlag[st.Flag]; dup {
			ec.addf("pipeline 步骤 %q 的 flag %q 已被步骤 %q 绑定（同一命名 flag 位至多被一个 step 绑定）", st.Name, st.Flag, prev)
		} else {
			boundFlag[st.Flag] = st.Name
		}
	}

	// 凡带 When 的 step 必须绑定 Flag（encode 决策需记录进 flag 位，decode 才能复现）。
	for i := range s.Pipeline {
		st := &s.Pipeline[i]
		if st.When != nil && st.Flag == "" {
			ec.addf("pipeline 步骤 %q 带有 when 但未绑定 flag（带 when 的步骤必须绑定 flag，否则 decode 无法复现 encode 决策）", st.Name)
		}
	}

	// checksumOut.from 指向的 <step>.<output>：step 必须存在、produce 必须存在。
	// 同时构造 stepName → produces 集合。
	stepProduces := make(map[string]map[string]struct{})
	for i := range s.Pipeline {
		st := &s.Pipeline[i]
		set := make(map[string]struct{})
		for _, p := range st.Produces {
			set[p.Name] = struct{}{}
		}
		stepProduces[st.Name] = set
	}
	for i := range s.Header {
		f := &s.Header[i]
		if f.Role != "checksumOut" || f.From == "" {
			continue
		}
		m := checksumFromRe.FindStringSubmatch(f.From)
		if m == nil {
			continue // 已在 validateChecksumOut 报错
		}
		stepName, produceName := m[1], m[2]
		produces, ok := stepProduces[stepName]
		if !ok {
			ec.addf("checksumOut 字段 %q 的 from %q 指向不存在的 step %q", f.Name, f.From, stepName)
			continue
		}
		if _, has := produces[produceName]; !has {
			ec.addf("checksumOut 字段 %q 的 from %q 指向 step %q 中不存在的 produce %q", f.Name, f.From, stepName, produceName)
		}
	}
}

func validateOver(ec *errCollector, prefix string, o *OverSpec, headerSize int) {
	if _, ok := validOverKinds[o.Kind]; !ok {
		ec.addf("%s：over.kind %q 不合法（合法值：bodyPlain|bodyFinal|header|frame|range）", prefix, o.Kind)
		return
	}
	if o.Kind == "range" {
		if o.RangeStart < 0 || o.RangeEnd < 0 || o.RangeEnd < o.RangeStart {
			ec.addf("%s：over range 区间非法 [rangeStart=%d, rangeEnd=%d]（需 >=0 且 rangeEnd>=rangeStart）", prefix, o.RangeStart, o.RangeEnd)
		}
	}
	_ = headerSize
}

func validateWhen(ec *errCollector, prefix string, w *StepCond, stepNames map[string]int) {
	if w.AppliesWith != "" {
		if _, ok := stepNames[w.AppliesWith]; !ok {
			ec.addf("%s：when.appliesWith %q 指向不存在的 step", prefix, w.AppliesWith)
		}
	}
	for i := range w.Guards {
		g := &w.Guards[i]
		if _, ok := validGuardOps[g.Op]; !ok {
			ec.addf("%s：guard (field=%q) 的 op %q 不合法（合法值：eq|neq|gt|gte|lt|lte）", prefix, g.Field, g.Op)
		}
	}
}
