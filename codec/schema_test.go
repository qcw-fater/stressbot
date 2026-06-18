package codec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validSchema returns a deep, fully-valid CodecSchema mirroring
// testdata/tcp_logic_codec.json (master plan §3.1). Tests clone it and
// mutate a single field to exercise one Validate rule at a time.
func validSchema() *CodecSchema {
	return &CodecSchema{
		Version:       1,
		EndianDefault: "le",
		Frame: FrameSpec{
			HeaderSize:            12,
			TrailerSize:           0,
			LengthIncludesHeader:  false,
			LengthIncludesTrailer: false,
		},
		Header: []Field{
			{Name: "bodyLen", Offset: 0, Size: 4, Type: "u32", Endian: "le", Role: "length"},
			{Name: "errCode", Offset: 4, Size: 2, Type: "u16", Role: "errorCode"},
			{Name: "cmd", Offset: 6, Size: 1, Type: "u8", Role: "route"},
			{Name: "act", Offset: 7, Size: 1, Type: "u8", Role: "route"},
			{Name: "index", Offset: 8, Size: 2, Type: "u16", Role: "value", Source: &ValueSource{Kind: "const", Value: 0}},
			{Name: "flags", Offset: 10, Size: 1, Type: "u8", Role: "flags", Bits: []FlagBit{{Name: "encrypted", Bit: 0}, {Name: "compressed", Bit: 1}}},
			{Name: "bcc", Offset: 11, Size: 1, Type: "u8", Role: "checksumOut", From: "enc.bcc"},
		},
		RouteKeyTmpl: "{cmd}:{act}",
		Pipeline: []PipelineStep{
			{Op: "compress", Name: "gz", Algo: "gzip", Flag: "compressed", When: &StepCond{MinBodyLen: 2048, OnlySmaller: true}, OnError: "fail"},
			{Op: "encrypt", Name: "enc", Algo: "xor_carry_rol", Params: map[string]any{"rol": 3}, KeyLen: 32, Flag: "encrypted",
				Offset:   &StepOffset{Encode: 0, Decode: 0},
				When:     &StepCond{RequireKey: true, MinBodyLen: 1, Guards: []Guard{{Field: "cmd", Op: "neq", Value: 0}}},
				Produces: []StepProduce{{Name: "bcc", Algo: "xor8", Region: "ciphered"}},
				OnError:  "fail"},
		},
	}
}

// assertValid fails the test if the schema does not validate cleanly.
func assertValid(t *testing.T, s *CodecSchema) {
	t.Helper()
	if err := s.Validate(); err != nil {
		t.Fatalf("expected valid schema, got error: %v", err)
	}
}

// assertInvalid fails the test if the schema validates; subMsg (if non-empty)
// must appear in the aggregated error text.
func assertInvalid(t *testing.T, s *CodecSchema, subMsg ...string) {
	t.Helper()
	err := s.Validate()
	if err == nil {
		t.Fatalf("expected validation error, got nil")
	}
	for _, m := range subMsg {
		if !strings.Contains(err.Error(), m) {
			t.Fatalf("error %q missing substring %q", err.Error(), m)
		}
	}
}

// ---------- 基础校验 ----------

func TestValidate_ValidBaseline(t *testing.T) {
	assertValid(t, validSchema())
}

func TestValidate_VersionMustBe1(t *testing.T) {
	s := validSchema()
	s.Version = 2
	assertInvalid(t, s, "version 必须为 1")
}

func TestValidate_EndianDefaultRequired(t *testing.T) {
	s := validSchema()
	s.EndianDefault = ""
	assertInvalid(t, s, "endianDefault")
}

func TestValidate_EndianDefaultLegal(t *testing.T) {
	s := validSchema()
	s.EndianDefault = "middle"
	assertInvalid(t, s, "endianDefault")
}

func TestValidate_HeaderSizePositive(t *testing.T) {
	s := validSchema()
	s.Frame.HeaderSize = 0
	assertInvalid(t, s, "headerSize")
}

func TestValidate_TrailerSizeNonNegative(t *testing.T) {
	s := validSchema()
	s.Frame.TrailerSize = -1
	assertInvalid(t, s, "trailerSize")
}

func TestValidate_RouteKeyTemplateRequired(t *testing.T) {
	s := validSchema()
	s.RouteKeyTmpl = ""
	assertInvalid(t, s, "routeKeyTemplate")
}

// ---------- 字段（Header） ----------

func TestValidate_FieldNameUnique(t *testing.T) {
	s := validSchema()
	s.Header[1].Name = "bodyLen" // duplicate
	assertInvalid(t, s, "bodyLen", "唯一")
}

func TestValidate_FieldNameNonEmpty(t *testing.T) {
	s := validSchema()
	s.Header[1].Name = ""
	assertInvalid(t, s, "字段名")
}

func TestValidate_FieldOffsetNonNegative(t *testing.T) {
	s := validSchema()
	s.Header[1].Offset = -1
	assertInvalid(t, s, "offset")
}

func TestValidate_FieldSizePositive(t *testing.T) {
	s := validSchema()
	s.Header[1].Size = 0
	assertInvalid(t, s, "size")
}

func TestValidate_FieldBoundsOffsetPlusSize(t *testing.T) {
	s := validSchema()
	// errCode: offset 4 size 2 → 6 OK; bump size to push past 12.
	s.Header[1].Size = 9 // 4+9=13 > 12
	assertInvalid(t, s, "越界", "headerSize")
}

func TestValidate_FieldOverlap(t *testing.T) {
	s := validSchema()
	// Make errCode overlap cmd: errCode at offset 4, size 1 → [4,5); cmd at 6.
	// Force overlap by moving cmd to offset 5.
	s.Header[2].Offset = 5 // [5,6) overlaps errCode's [4,6)
	assertInvalid(t, s, "重叠")
}

func TestValidate_FieldTypeUnknown(t *testing.T) {
	s := validSchema()
	s.Header[1].Type = "u7"
	assertInvalid(t, s, "type", "u7")
}

func TestValidate_FieldTypeSizeMismatch(t *testing.T) {
	s := validSchema()
	s.Header[0].Size = 3 // u32 should be 4
	assertInvalid(t, s, "size", "u32")
}

func TestValidate_FieldTypeBytesRequiresSize(t *testing.T) {
	s := validSchema()
	s.Header[1].Type = "bytes"
	s.Header[1].Size = 0
	s.Header[1].Role = "reserved" // avoid role-specific constraints
	assertInvalid(t, s, "bytes", "size")
}

func TestValidate_FieldRoleUnknown(t *testing.T) {
	s := validSchema()
	s.Header[1].Role = "magic"
	assertInvalid(t, s, "role", "magic")
}

// ---------- role ----------

func TestValidate_ExactlyOneLength(t *testing.T) {
	s := validSchema()
	// Add a second length field in the reserved slot: reuse index field.
	s.Header[4].Role = "length"
	assertInvalid(t, s, "length")
}

func TestValidate_NoLength(t *testing.T) {
	s := validSchema()
	s.Header[0].Role = "reserved" // remove the only length
	assertInvalid(t, s, "length")
}

func TestValidate_NoRoute(t *testing.T) {
	s := validSchema()
	s.Header[2].Role = "reserved"
	s.Header[3].Role = "reserved"
	assertInvalid(t, s, "route")
}

func TestValidate_FlagsBitOutOfRange(t *testing.T) {
	s := validSchema()
	s.Header[5].Bits[0].Bit = 8 // u8 → valid [0,8)
	assertInvalid(t, s, "bit", "flags")
}

func TestValidate_FlagsBitDuplicate(t *testing.T) {
	s := validSchema()
	s.Header[5].Bits[1].Bit = 0 // duplicate of encrypted.bit=0
	assertInvalid(t, s, "bit", "重复")
}

func TestValidate_FlagsBitNameDuplicate(t *testing.T) {
	s := validSchema()
	s.Header[5].Bits[1].Name = "encrypted"
	assertInvalid(t, s, "bit", "名", "重复")
}

func TestValidate_ChecksumOutFromMalformed(t *testing.T) {
	s := validSchema()
	s.Header[6].From = "not-a-dotted-name"
	assertInvalid(t, s, "from")
}

func TestValidate_ChecksumOutFromEmpty(t *testing.T) {
	s := validSchema()
	s.Header[6].From = ""
	assertInvalid(t, s, "from")
}

// ---------- routeKeyTemplate ----------

func TestValidate_RouteKeyTemplateUnknownPlaceholder(t *testing.T) {
	s := validSchema()
	s.RouteKeyTmpl = "{cmd}:{unknown}"
	assertInvalid(t, s, "routeKeyTemplate", "unknown")
}

func TestValidate_RouteKeyTemplateNonRoutePlaceholder(t *testing.T) {
	s := validSchema()
	// {bodyLen} exists but is role:length, not route.
	s.RouteKeyTmpl = "{cmd}:{bodyLen}"
	assertInvalid(t, s, "routeKeyTemplate", "bodyLen")
}

// ---------- pipeline ----------

func TestValidate_StepNameDuplicate(t *testing.T) {
	s := validSchema()
	s.Pipeline[1].Name = "gz"
	assertInvalid(t, s, "gz", "唯一")
}

func TestValidate_StepOpUnknown(t *testing.T) {
	s := validSchema()
	s.Pipeline[0].Op = "scramble"
	assertInvalid(t, s, "op", "scramble")
}

func TestValidate_StepAlgoRequired(t *testing.T) {
	s := validSchema()
	s.Pipeline[0].Algo = ""
	assertInvalid(t, s, "algo")
}

func TestValidate_StepFlagMissing(t *testing.T) {
	s := validSchema()
	s.Pipeline[0].Flag = "nopenope"
	assertInvalid(t, s, "flag", "nopenope")
}

func TestValidate_StepFlagSharedByTwoSteps(t *testing.T) {
	s := validSchema()
	// Both steps bind "compressed" — duplicates the flag binding.
	s.Pipeline[1].Flag = "compressed"
	assertInvalid(t, s, "compressed", "至多被一个")
}

func TestValidate_StepWhenRequiresFlag(t *testing.T) {
	s := validSchema()
	s.Pipeline[0].Flag = "" // has When, no Flag
	assertInvalid(t, s, "when", "flag")
}

func TestValidate_EncryptOffsetNegative(t *testing.T) {
	s := validSchema()
	s.Pipeline[1].Offset = &StepOffset{Encode: -1, Decode: 0}
	assertInvalid(t, s, "offset", "encode")
}

func TestValidate_ProduceNameDuplicateInStep(t *testing.T) {
	s := validSchema()
	s.Pipeline[1].Produces = append(s.Pipeline[1].Produces, StepProduce{Name: "bcc", Algo: "xor8", Region: "ciphered"})
	assertInvalid(t, s, "bcc", "唯一")
}

func TestValidate_ProduceRegionUnknown(t *testing.T) {
	s := validSchema()
	s.Pipeline[1].Produces[0].Region = "void"
	assertInvalid(t, s, "region", "void")
}

func TestValidate_OverKindUnknown(t *testing.T) {
	s := validSchema()
	s.Pipeline = append(s.Pipeline, PipelineStep{Op: "checksum", Name: "over1", Algo: "crc32", Over: &OverSpec{Kind: "galaxy"}})
	assertInvalid(t, s, "over", "galaxy")
}

func TestValidate_OverRangeInvalid(t *testing.T) {
	s := validSchema()
	s.Pipeline = append(s.Pipeline, PipelineStep{Op: "checksum", Name: "over1", Algo: "crc32", Over: &OverSpec{Kind: "range", RangeStart: 10, RangeEnd: 5}})
	assertInvalid(t, s, "range")
}

func TestValidate_OverRangeNegativeStart(t *testing.T) {
	s := validSchema()
	s.Pipeline = append(s.Pipeline, PipelineStep{Op: "checksum", Name: "over1", Algo: "crc32", Over: &OverSpec{Kind: "range", RangeStart: -1, RangeEnd: 5}})
	assertInvalid(t, s, "range")
}

func TestValidate_WhenAppliesWithMissing(t *testing.T) {
	s := validSchema()
	s.Pipeline[0].When.AppliesWith = "ghost"
	assertInvalid(t, s, "appliesWith", "ghost")
}

func TestValidate_OnErrorUnknown(t *testing.T) {
	s := validSchema()
	s.Pipeline[0].OnError = "explode"
	assertInvalid(t, s, "onError", "explode")
}

func TestValidate_GuardOpUnknown(t *testing.T) {
	s := validSchema()
	s.Pipeline[1].When.Guards[0].Op = "matches"
	assertInvalid(t, s, "guard", "matches")
}

func TestValidate_ChecksumOutFromPointsMissingStep(t *testing.T) {
	s := validSchema()
	s.Header[6].From = "ghost.bcc"
	assertInvalid(t, s, "from", "ghost")
}

func TestValidate_ChecksumOutFromPointsMissingProduce(t *testing.T) {
	s := validSchema()
	s.Header[6].From = "enc.nope"
	assertInvalid(t, s, "from", "nope")
}

// ---------- v1 显式拒绝 ----------

func TestValidate_V1RejectsStateSource(t *testing.T) {
	s := validSchema()
	s.Header[4].Source = &ValueSource{Kind: "state", Key: "k"}
	assertInvalid(t, s, "v1 不支持", "state")
}

func TestValidate_V1RejectsCounterSource(t *testing.T) {
	s := validSchema()
	s.Header[4].Source = &ValueSource{Kind: "counter", Start: 0, Step: 1}
	assertInvalid(t, s, "v1 不支持", "counter")
}

func TestValidate_V1RejectsTimestampSource(t *testing.T) {
	s := validSchema()
	s.Header[4].Source = &ValueSource{Kind: "timestamp", Unit: "s"}
	assertInvalid(t, s, "v1 不支持", "timestamp")
}

func TestValidate_V1RejectsUnknownSourceKind(t *testing.T) {
	s := validSchema()
	s.Header[4].Source = &ValueSource{Kind: "magic"}
	assertInvalid(t, s, "source", "magic")
}

func TestValidate_SourceKindRouteAccepted(t *testing.T) {
	// role:value with source.kind=route is a valid v1 feature; ensure it passes.
	s := validSchema()
	s.Header[4].Source = &ValueSource{Kind: "route", Key: "cmd"}
	assertValid(t, s)
}

// ---------- LoadSchema ----------

func TestLoadSchema_Success(t *testing.T) {
	s, err := LoadSchema(filepath.Join("testdata", "tcp_logic_codec.json"))
	if err != nil {
		t.Fatalf("LoadSchema failed: %v", err)
	}
	if s.Version != 1 {
		t.Errorf("Version = %d, want 1", s.Version)
	}
	if s.Frame.HeaderSize != 12 {
		t.Errorf("HeaderSize = %d, want 12", s.Frame.HeaderSize)
	}
	if len(s.Header) != 7 {
		t.Errorf("len(Header) = %d, want 7", len(s.Header))
	}
	if len(s.Pipeline) != 2 {
		t.Errorf("len(Pipeline) = %d, want 2", len(s.Pipeline))
	}
}

func TestLoadSchema_MissingFile(t *testing.T) {
	_, err := LoadSchema(filepath.Join("testdata", "does_not_exist.json"))
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadSchema_BadJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSchema(p); err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestLoadSchema_InvalidFailsValidate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.json")
	// version 99 → Validate rejects.
	if err := os.WriteFile(p, []byte(`{"version":99,"endianDefault":"le","frame":{"headerSize":4},"header":[{"name":"bodyLen","offset":0,"size":4,"type":"u32","role":"length"}],"routeKeyTemplate":"x","pipeline":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSchema(p)
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("expected version validation error, got %v", err)
	}
}

// ---------- LoadErrorMap / DescribeError ----------

func TestLoadErrorMap_Success(t *testing.T) {
	m, err := LoadErrorMap(filepath.Join("testdata", "errors.json"))
	if err != nil {
		t.Fatalf("LoadErrorMap failed: %v", err)
	}
	want := map[uint64]string{0: "成功", 1: "数据库错误", 2: "协议解析错误", 19: "消息解密失败"}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("m[%d] = %q, want %q", k, m[k], v)
		}
	}
	if len(m) != len(want) {
		t.Errorf("len(m) = %d, want %d", len(m), len(want))
	}
}

func TestLoadErrorMap_BadKey(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(p, []byte(`{"not-a-number":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadErrorMap(p); err == nil {
		t.Fatal("expected error for non-numeric key, got nil")
	}
}

func TestLoadErrorMap_MissingFile(t *testing.T) {
	_, err := LoadErrorMap(filepath.Join("testdata", "missing.json"))
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestDescribeError_Hit(t *testing.T) {
	m := map[uint64]string{0: "成功", 19: "消息解密失败"}
	if got := DescribeError(m, 19); got != "消息解密失败" {
		t.Errorf("DescribeError(19) = %q, want 消息解密失败", got)
	}
}

func TestDescribeError_MissReturnsEmpty(t *testing.T) {
	m := map[uint64]string{0: "成功"}
	if got := DescribeError(m, 999); got != "" {
		t.Errorf("DescribeError(999) = %q, want empty string", got)
	}
}

func TestDescribeError_NilMap(t *testing.T) {
	if got := DescribeError(nil, 0); got != "" {
		t.Errorf("DescribeError(nil, 0) = %q, want empty string", got)
	}
}
