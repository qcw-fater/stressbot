package codec

import (
	"encoding/binary"
	"strings"
	"testing"
)

// loadTestSchema 加载 testdata 下的合法 tcp schema（T1.1 已经覆盖 LoadSchema）。
func loadTestSchema(t *testing.T) *Schema {
	t.Helper()
	s, err := LoadSchema("testdata/tcp_logic_codec.json")
	if err != nil {
		t.Fatalf("LoadSchema 失败: %v", err)
	}
	return s
}

// ---------- 1. 合法 schema 编译成功 ----------

func TestNewSchemaCodec_HappyPath(t *testing.T) {
	s := loadTestSchema(t)
	c, err := NewSchemaCodec(s, nil)
	if err != nil {
		t.Fatalf("NewSchemaCodec 返回错误: %v", err)
	}

	if got := c.HeaderSize(); got != 12 {
		t.Fatalf("HeaderSize = %d, want 12", got)
	}
	if c.lengthField.role != roleLength {
		t.Errorf("lengthField.role = %v, want roleLength", c.lengthField.role)
	}
	if c.lengthField.name != "bodyLen" {
		t.Errorf("lengthField.name = %q, want bodyLen", c.lengthField.name)
	}
	if c.lengthField.kind.width() != 4 {
		t.Errorf("lengthField.kind width = %d, want 4", c.lengthField.kind.width())
	}
	if c.lengthField.endian != binary.LittleEndian {
		t.Errorf("lengthField.endian = LE expected")
	}

	// fields：route/errorCode/flags/checksumOut/value/reserved（不含 length）。
	if len(c.fields) != 6 {
		t.Fatalf("len(fields) = %d, want 6（length 已单独存）", len(c.fields))
	}
	// 校验 route 字段下标可被 routeKeySegs 引用。
	routeIdx := make(map[string]int) // route name → c.fields 下标
	for i, cf := range c.fields {
		if cf.role == roleRoute {
			routeIdx[cf.name] = i
		}
	}
	if _, ok := routeIdx["cmd"]; !ok {
		t.Fatalf("未在 fields 中找到 route 字段 cmd")
	}
	if _, ok := routeIdx["act"]; !ok {
		t.Fatalf("未在 fields 中找到 route 字段 act")
	}

	// routeKeySegs："{cmd}:{act}" → seg(field=cmd) literal ":" seg(field=act)
	if len(c.routeKeySegs) != 3 {
		t.Fatalf("len(routeKeySegs) = %d, want 3", len(c.routeKeySegs))
	}
	if c.routeKeySegs[0].fieldIdx != routeIdx["cmd"] {
		t.Errorf("routeKeySegs[0].fieldIdx = %d, want %d (cmd)", c.routeKeySegs[0].fieldIdx, routeIdx["cmd"])
	}
	if c.routeKeySegs[1].literal != ":" {
		t.Errorf("routeKeySegs[1].literal = %q, want :", c.routeKeySegs[1].literal)
	}
	if c.routeKeySegs[2].fieldIdx != routeIdx["act"] {
		t.Errorf("routeKeySegs[2].fieldIdx = %d, want %d (act)", c.routeKeySegs[2].fieldIdx, routeIdx["act"])
	}

	// steps：gz (compress) + enc (encrypt)。
	if len(c.steps) != 2 {
		t.Fatalf("len(steps) = %d, want 2", len(c.steps))
	}
	gz := c.steps[0]
	if gz.op != opCompress {
		t.Errorf("gz.op = %v, want opCompress", gz.op)
	}
	if gz.impl == nil {
		t.Errorf("gz.impl == nil")
	}
	if gz.flagMask == 0 {
		t.Errorf("gz.flagMask = 0，期望非 0（compressed 命名位）")
	}
	// compress 的 encOffset/decOffset 应为 0。
	if gz.encOffset != 0 || gz.decOffset != 0 {
		t.Errorf("gz encOffset=%d decOffset=%d，均应为 0", gz.encOffset, gz.decOffset)
	}
	// gz.when.onlySmaller 必须存进 compiledWhen。
	if !gz.encodeWhen.onlySmaller {
		t.Errorf("gz.encodeWhen.onlySmaller = false, want true")
	}

	enc := c.steps[1]
	if enc.op != opEncrypt {
		t.Errorf("enc.op = %v, want opEncrypt", enc.op)
	}
	if enc.impl == nil {
		t.Errorf("enc.impl == nil（xor_carry_rol）")
	}
	if enc.flagMask == 0 {
		t.Errorf("enc.flagMask = 0，期望非 0（encrypted 命名位）")
	}
	if enc.encOffset != 0 || enc.decOffset != 0 {
		t.Errorf("enc encOffset=%d decOffset=%d，均应为 0", enc.encOffset, enc.decOffset)
	}
	// enc 产生 bcc（xor8 checksum）。
	if len(enc.produces) != 1 {
		t.Fatalf("len(enc.produces) = %d, want 1", len(enc.produces))
	}
	if enc.produces[0].name != "bcc" {
		t.Errorf("enc.produces[0].name = %q, want bcc", enc.produces[0].name)
	}
	if enc.produces[0].checksumImpl == nil {
		t.Errorf("enc.produces[0].checksumImpl == nil（xor8）")
	}
	// enc.when.guards 指向 route 字段 cmd。
	if len(enc.encodeWhen.guards) != 1 {
		t.Fatalf("len(enc.encodeWhen.guards) = %d, want 1", len(enc.encodeWhen.guards))
	}
	if enc.encodeWhen.guards[0].fieldIdx != routeIdx["cmd"] {
		t.Errorf("enc.when.guards[0].fieldIdx = %d, want %d (cmd)", enc.encodeWhen.guards[0].fieldIdx, routeIdx["cmd"])
	}

	// checksumOut 字段 bcc 的 from "enc.bcc" 解析为 stepIdx=1, produceName="bcc"。
	var bccField *compiledField
	for i := range c.fields {
		if c.fields[i].role == roleChecksumOut {
			bccField = &c.fields[i]
			break
		}
	}
	if bccField == nil {
		t.Fatalf("未找到 checksumOut 字段")
	}
	if bccField.checksumRef.stepIdx != 1 {
		t.Errorf("bccField.checksumRef.stepIdx = %d, want 1 (enc)", bccField.checksumRef.stepIdx)
	}
	if bccField.checksumRef.produceName != "bcc" {
		t.Errorf("bccField.checksumRef.produceName = %q, want bcc", bccField.checksumRef.produceName)
	}

	// errorMap：nil 入参 → 空 map（非 nil）。
	if c.errorMap == nil {
		t.Errorf("errorMap == nil，应初始化为空 map")
	}
}

// ---------- 2. udp:battle 等价 schema：encOffset=11/decOffset=0 ----------

func TestNewSchemaCodec_EncryptOffset_UDP(t *testing.T) {
	s := loadTestSchema(t)
	// 把 enc 步的 offset 改成 udp:battle 语义：encode=11/decode=0。
	for i := range s.Pipeline {
		if s.Pipeline[i].Name == "enc" {
			s.Pipeline[i].Offset = &StepOffset{Encode: 11, Decode: 0}
		}
	}
	c, err := NewSchemaCodec(s, nil)
	if err != nil {
		t.Fatalf("NewSchemaCodec 失败: %v", err)
	}
	enc := c.steps[1]
	if enc.encOffset != 11 {
		t.Errorf("enc.encOffset = %d, want 11", enc.encOffset)
	}
	if enc.decOffset != 0 {
		t.Errorf("enc.decOffset = %d, want 0", enc.decOffset)
	}
	// gz（compress）不应受 encrypt offset 影响。
	if c.steps[0].encOffset != 0 || c.steps[0].decOffset != 0 {
		t.Errorf("gz offset 应为 0/0，得到 %d/%d", c.steps[0].encOffset, c.steps[0].decOffset)
	}
}

// ---------- 3. 缺算法 fail loud（中文，带 step 名 + algo 名）----------

func TestNewSchemaCodec_UnknownAlgo_FailLoud(t *testing.T) {
	s := loadTestSchema(t)
	s.Pipeline[0].Algo = "nope" // gz step → nope
	_, err := NewSchemaCodec(s, nil)
	if err == nil {
		t.Fatalf("期望缺算法报错，实际 nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "gz") {
		t.Errorf("错误信息未含 step 名 gz：%q", msg)
	}
	if !strings.Contains(msg, "nope") {
		t.Errorf("错误信息未含 algo 名 nope：%q", msg)
	}
	// 中文提示。
	if !containsHan(msg) {
		t.Errorf("错误信息应包含中文：%q", msg)
	}
}

// produces 引用未知 checksum 算法也 fail loud。
func TestNewSchemaCodec_UnknownProduceAlgo_FailLoud(t *testing.T) {
	s := loadTestSchema(t)
	s.Pipeline[1].Produces[0].Algo = "ghosthash"
	_, err := NewSchemaCodec(s, nil)
	if err == nil {
		t.Fatalf("期望 produces 缺算法报错，实际 nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "enc") {
		t.Errorf("错误信息未含 step 名 enc：%q", msg)
	}
	if !strings.Contains(msg, "ghosthash") {
		t.Errorf("错误信息未含 algo 名 ghosthash：%q", msg)
	}
}

// ---------- 4. Validate 失败的 schema 错误透传 ----------

func TestNewSchemaCodec_ValidateFailure_Passthrough(t *testing.T) {
	s := loadTestSchema(t)
	s.Frame.HeaderSize = 0 // 非法：必须 > 0
	_, err := NewSchemaCodec(s, nil)
	if err == nil {
		t.Fatalf("期望 Validate 失败透传，实际 nil")
	}
}

// ---------- 5. onlySmaller 存入 compiledWhen 且 applies() 不判 onlySmaller ----------

func TestCompiledWhen_OnlySmaller_NotInApplies(t *testing.T) {
	s := loadTestSchema(t)
	c, err := NewSchemaCodec(s, nil)
	if err != nil {
		t.Fatalf("NewSchemaCodec 失败: %v", err)
	}
	gz := c.steps[0]
	// gz.when = { minBodyLen: 2048, onlySmaller: true }；bodyLen 满足时应 true（即使 onlySmaller=true）。
	if gz.encodeWhen.minBodyLen != 2048 {
		t.Errorf("gz.encodeWhen.minBodyLen = %d, want 2048", gz.encodeWhen.minBodyLen)
	}
	got := gz.encodeWhen.applies(4096, false, nil)
	if !got {
		t.Errorf("gz.encodeWhen.applies(4096) = false, want true（onlySmaller 不应进 applies）")
	}
	// minBodyLen 不满足时 false。
	if gz.encodeWhen.applies(100, false, nil) {
		t.Errorf("gz.encodeWhen.applies(100) = true, want false（minBodyLen 未满足）")
	}
}

// ---------- 6. endian 默认回退 ----------

func TestNewSchemaCodec_EndianDefault(t *testing.T) {
	s := loadTestSchema(t) // endianDefault = le
	c, err := NewSchemaCodec(s, nil)
	if err != nil {
		t.Fatalf("NewSchemaCodec 失败: %v", err)
	}
	// errCode 字段未指定 endian → 回退 EndianDefault（le）。
	var errCodeField *compiledField
	for i := range c.fields {
		if c.fields[i].name == "errCode" {
			errCodeField = &c.fields[i]
			break
		}
	}
	if errCodeField == nil {
		t.Fatalf("未找到 errCode 字段")
	}
	if errCodeField.endian != binary.LittleEndian {
		t.Errorf("errCode.endian 未回退 EndianDefault(le)")
	}
}

// ---------- 辅助：判断是否含中文字符 ----------

func containsHan(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}
