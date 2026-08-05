package engine

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"

	"stressbot/state"
)

// ──────────────────────────────────────────────────────────────────────────
// appendLE：类型掩码 + 小端打包
// ──────────────────────────────────────────────────────────────────────────

func TestAppendLE(t *testing.T) {
	cases := []struct {
		name string
		typ  string
		v    int64
		want []byte
	}{
		{"u8", "u8", 0xAB, []byte{0xAB}},
		{"i8_负数", "i8", -1, []byte{0xFF}},
		{"u16_小数", "u16", 0x0102, []byte{0x02, 0x01}},
		{"i16_负数", "i16", -1, []byte{0xFF, 0xFF}},
		{"u16_超范围掩码", "u16", 0x10003, []byte{0x03, 0x00}}, // 0x10003 & 0xFFFF = 0x0003
		{"i16_大负数", "i16", -0x7FFF, []byte{0x01, 0x80}},   // -32767 → 0x8001 LE
		{"u32_掩码", "u32", 0x1_0000_0003, []byte{0x03, 0x00, 0x00, 0x00}},
		{"i32_负数位模式", "i32", -1, []byte{0xFF, 0xFF, 0xFF, 0xFF}},
		{"u32_大数", "u32", 0x12345678, []byte{0x78, 0x56, 0x34, 0x12}},
		{"u64_全1", "u64", -1, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}},
		{"u64_0x0102030405060708", "u64", 0x0102030405060708, []byte{0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01}},
		// f32/f64：v 为 IEEE754 位模式（resolveHeartbeatField 对浮点产出），appendLE 按宽度还原小端字节。
		{"f32_1.0", "f32", int64(math.Float32bits(1.0)), []byte{0x00, 0x00, 0x80, 0x3f}},
		{"f64_1.0", "f64", int64(math.Float64bits(1.0)), []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf0, 0x3f}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := appendLE(nil, c.typ, c.v)
			if err != nil {
				t.Fatalf("appendLE err: %v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("len=%d want=%d got=%x", len(got), len(c.want), got)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("byte[%d]=%#x want=%#x full=%x", i, got[i], c.want[i], got)
				}
			}
		})
	}
}

func TestAppendLE_UnknownType(t *testing.T) {
	_, err := appendLE(nil, "u128", 0)
	if err == nil {
		t.Fatal("appendLE 未知 type 应返回错误")
	}
	if !strings.Contains(err.Error(), "type") {
		t.Fatalf("错误信息应含 type 上下文: %v", err)
	}
}

func TestAppendLE_WidthByType(t *testing.T) {
	widths := map[string]int{"u8": 1, "i8": 1, "u16": 2, "i16": 2, "u32": 4, "i32": 4, "u64": 8, "i64": 8, "f32": 4, "f64": 8}
	for typ, w := range widths {
		got, err := appendLE(nil, typ, 1)
		if err != nil {
			t.Fatalf("appendLE(%s) err: %v", typ, err)
		}
		if len(got) != w {
			t.Fatalf("appendLE(%s) width=%d want=%d", typ, len(got), w)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────
// BuildHeartbeatBody：逐源解析
// ──────────────────────────────────────────────────────────────────────────

//go:fix inline
func i64ptr(v int64) *int64 { return new(v) }

//go:fix inline
func f64ptr(v float64) *float64 { return new(v) }

func TestBuildHeartbeatBody_Fixed(t *testing.T) {
	fields := []HeartbeatField{{Type: "u16", Source: "fixed", Value: i64ptr(0x0102)}}
	body, skip, err := BuildHeartbeatBody(fields, state.NewStore(), nil, false)
	if err != nil || skip {
		t.Fatalf("unexpected: body=%x skip=%v err=%v", body, skip, err)
	}
	want := []byte{0x02, 0x01}
	if string(body) != string(want) {
		t.Fatalf("body=%x want=%x", body, want)
	}
}

func TestBuildHeartbeatBody_FixedMissingValue(t *testing.T) {
	fields := []HeartbeatField{{Type: "u16", Source: "fixed"}}
	_, _, err := BuildHeartbeatBody(fields, state.NewStore(), nil, false)
	if err == nil {
		t.Fatal("fixed 缺 value 应报错")
	}
	if !strings.Contains(err.Error(), "value") {
		t.Fatalf("错误信息应含 value: %v", err)
	}
}

func TestBuildHeartbeatBody_StateHit(t *testing.T) {
	st := state.NewStore()
	st.Set("battleId", int64(0x0102030405060708))
	fields := []HeartbeatField{{Type: "u64", Source: "state", Key: "battleId"}}
	body, skip, err := BuildHeartbeatBody(fields, st, nil, false)
	if err != nil || skip {
		t.Fatalf("unexpected: body=%x skip=%v err=%v", body, skip, err)
	}
	want := []byte{0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01}
	if string(body) != string(want) {
		t.Fatalf("body=%x want=%x", body, want)
	}
}

func TestBuildHeartbeatBody_StateStringInteger(t *testing.T) {
	st := state.NewStore()
	st.Set("battleSession", "664714659830361091")
	fields := []HeartbeatField{{Type: "i64", Source: "state", Key: "battleSession"}}
	body, skip, err := BuildHeartbeatBody(fields, st, nil, false)
	if err != nil || skip {
		t.Fatalf("unexpected: body=%x skip=%v err=%v", body, skip, err)
	}
	want := make([]byte, 8)
	binary.LittleEndian.PutUint64(want, uint64(664714659830361091))
	if string(body) != string(want) {
		t.Fatalf("body=%x want=%x", body, want)
	}
}

func TestBuildHeartbeatBody_StateMissing_SkipWhenMissing(t *testing.T) {
	fields := []HeartbeatField{{Type: "u64", Source: "state", Key: "missing"}}
	body, skip, err := BuildHeartbeatBody(fields, state.NewStore(), nil, true)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !skip {
		t.Fatalf("SkipWhenMissing 缺失应 skip=true, body=%x", body)
	}
}

func TestBuildHeartbeatBody_StateMissing_NoSkipError(t *testing.T) {
	fields := []HeartbeatField{{Type: "u64", Source: "state", Key: "missing"}}
	_, _, err := BuildHeartbeatBody(fields, state.NewStore(), nil, false)
	if err == nil {
		t.Fatal("state 缺失无 skip 应报错")
	}
}

func TestBuildHeartbeatBody_StateCounter(t *testing.T) {
	st := state.NewStore()
	st.Set("seq", int64(4))
	fields := []HeartbeatField{{Type: "u16", Source: "stateCounter", Key: "seq"}}
	body1, _, err := BuildHeartbeatBody(fields, st, nil, false)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	want1 := []byte{0x05, 0x00}
	if string(body1) != string(want1) {
		t.Fatalf("body1=%x want=%x", body1, want1)
	}
	body2, _, err := BuildHeartbeatBody(fields, st, nil, false)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	want2 := []byte{0x06, 0x00}
	if string(body2) != string(want2) {
		t.Fatalf("body2=%x want=%x", body2, want2)
	}
}

func TestBuildHeartbeatBody_Counter(t *testing.T) {
	priv := map[int]int64{0: 0x0007}
	fields := []HeartbeatField{{Type: "u32", Source: "counter"}}
	body, _, err := BuildHeartbeatBody(fields, state.NewStore(), priv, false)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	want := []byte{0x07, 0x00, 0x00, 0x00}
	if string(body) != string(want) {
		t.Fatalf("body=%x want=%x", body, want)
	}
}

func TestBuildHeartbeatBody_Counter_DefaultStart(t *testing.T) {
	priv := map[int]int64{}
	fields := []HeartbeatField{{Type: "u8", Source: "counter"}}
	body, _, err := BuildHeartbeatBody(fields, state.NewStore(), priv, false)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(body) != 1 || body[0] != 0 {
		t.Fatalf("body=%x want=[0]", body)
	}
}

func TestBuildHeartbeatBody_Timestamp_Millis(t *testing.T) {
	fields := []HeartbeatField{{Type: "u64", Source: "timestamp", Unit: "ms"}}
	body, _, err := BuildHeartbeatBody(fields, state.NewStore(), nil, false)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(body) != 8 {
		t.Fatalf("body len=%d want=8", len(body))
	}
	var got uint64
	for i := 7; i >= 0; i-- {
		got = got<<8 | uint64(body[i])
	}
	if got == 0 {
		t.Fatalf("timestamp ms 不应为 0")
	}
}

func TestBuildHeartbeatBody_Timestamp_Seconds(t *testing.T) {
	bodyMs, _, _ := BuildHeartbeatBody(
		[]HeartbeatField{{Type: "u64", Source: "timestamp", Unit: "ms"}}, state.NewStore(), nil, false)
	fields := []HeartbeatField{{Type: "u64", Source: "timestamp", Unit: "s"}}
	bodyS, _, err := BuildHeartbeatBody(fields, state.NewStore(), nil, false)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	ms := binary.LittleEndian.Uint64(bodyMs)
	s := binary.LittleEndian.Uint64(bodyS)
	if s < ms/1000-2 || s > ms/1000+2 {
		t.Fatalf("s=%d 与 ms/1000=%d 不匹配", s, ms/1000)
	}
}

func TestBuildHeartbeatBody_RandomInt_Range(t *testing.T) {
	fields := []HeartbeatField{{Type: "u8", Source: "randomInt", Min: i64ptr(10), Max: i64ptr(20)}}
	for range 50 {
		body, _, err := BuildHeartbeatBody(fields, state.NewStore(), nil, false)
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if len(body) != 1 {
			t.Fatalf("body len=%d want=1", len(body))
		}
		v := int64(body[0])
		if v < 10 || v > 20 {
			t.Fatalf("randomInt=%d 超出 [10,20]", v)
		}
	}
}

func TestBuildHeartbeatBody_RandomInt_MissingMin(t *testing.T) {
	fields := []HeartbeatField{{Type: "u8", Source: "randomInt", Max: i64ptr(20)}}
	_, _, err := BuildHeartbeatBody(fields, state.NewStore(), nil, false)
	if err == nil {
		t.Fatal("randomInt 缺 min 应报错")
	}
}

func TestBuildHeartbeatBody_RandomInt_MissingMax(t *testing.T) {
	fields := []HeartbeatField{{Type: "u8", Source: "randomInt", Min: i64ptr(10)}}
	_, _, err := BuildHeartbeatBody(fields, state.NewStore(), nil, false)
	if err == nil {
		t.Fatal("randomInt 缺 max 应报错")
	}
}

// ──────────────────────────────────────────────────────────────────────────
// BuildHeartbeatBody：f32/f64 浮点字段（fixed / state / 非法 source）
// ──────────────────────────────────────────────────────────────────────────

func TestBuildHeartbeatBody_F32_Fixed(t *testing.T) {
	fields := []HeartbeatField{{Type: "f32", Source: "fixed", FloatValue: new(1.0)}}
	body, skip, err := BuildHeartbeatBody(fields, state.NewStore(), nil, false)
	if err != nil || skip {
		t.Fatalf("unexpected: body=%x skip=%v err=%v", body, skip, err)
	}
	want := []byte{0x00, 0x00, 0x80, 0x3f} // math.Float32bits(1.0)=0x3F800000 LE
	if string(body) != string(want) {
		t.Fatalf("body=%x want=%x", body, want)
	}
}

func TestBuildHeartbeatBody_F64_Fixed(t *testing.T) {
	fields := []HeartbeatField{{Type: "f64", Source: "fixed", FloatValue: new(1.0)}}
	body, skip, err := BuildHeartbeatBody(fields, state.NewStore(), nil, false)
	if err != nil || skip {
		t.Fatalf("unexpected: body=%x skip=%v err=%v", body, skip, err)
	}
	want := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf0, 0x3f} // Float64bits(1.0)=0x3FF0000000000000 LE
	if string(body) != string(want) {
		t.Fatalf("body=%x want=%x", body, want)
	}
}

func TestBuildHeartbeatBody_F32_State(t *testing.T) {
	st := state.NewStore()
	st.Set("posX", float64(1.5))
	fields := []HeartbeatField{{Type: "f32", Source: "state", Key: "posX"}}
	body, skip, err := BuildHeartbeatBody(fields, st, nil, false)
	if err != nil || skip {
		t.Fatalf("unexpected: body=%x skip=%v err=%v", body, skip, err)
	}
	want := []byte{0x00, 0x00, 0xc0, 0x3f} // float32(1.5)=0x3FC00000 LE
	if string(body) != string(want) {
		t.Fatalf("body=%x want=%x", body, want)
	}
}

func TestBuildHeartbeatBody_F32_FixedMissingFloatValue(t *testing.T) {
	fields := []HeartbeatField{{Type: "f32", Source: "fixed"}} // 缺 FloatValue
	_, _, err := BuildHeartbeatBody(fields, state.NewStore(), nil, false)
	if err == nil {
		t.Fatal("f32 fixed 缺 floatValue 应报错")
	}
	if !strings.Contains(err.Error(), "floatValue") {
		t.Fatalf("错误信息应含 floatValue: %v", err)
	}
}

func TestBuildHeartbeatBody_Float_IllegalSource(t *testing.T) {
	// f32/f64 仅支持 fixed/state；stateCounter 等整型语义对浮点无意义 → err（不静默兜底）
	fields := []HeartbeatField{{Type: "f32", Source: "stateCounter", Key: "seq"}}
	_, _, err := BuildHeartbeatBody(fields, state.NewStore(), nil, false)
	if err == nil {
		t.Fatal("f32 + stateCounter 应报错")
	}
}

func TestBuildHeartbeatBody_F32_StateNonNumeric(t *testing.T) {
	st := state.NewStore()
	st.Set("bad", "not-a-number")
	fields := []HeartbeatField{{Type: "f32", Source: "state", Key: "bad"}}
	_, _, err := BuildHeartbeatBody(fields, st, nil, false)
	if err == nil {
		t.Fatal("f32 state 非数值应报错")
	}
}

func TestBuildHeartbeatBody_F32_StateMissing_SkipWhenMissing(t *testing.T) {
	fields := []HeartbeatField{{Type: "f32", Source: "state", Key: "missing"}}
	body, skip, err := BuildHeartbeatBody(fields, state.NewStore(), nil, true)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !skip {
		t.Fatalf("SkipWhenMissing 缺失应 skip=true, body=%x", body)
	}
}

func TestBuildHeartbeatBody_UnknownSource(t *testing.T) {
	fields := []HeartbeatField{{Type: "u8", Source: "unknownSource"}}
	_, _, err := BuildHeartbeatBody(fields, state.NewStore(), nil, false)
	if err == nil {
		t.Fatal("未知 source 应报错")
	}
}

func TestBuildHeartbeatBody_EmptyFields(t *testing.T) {
	body, skip, err := BuildHeartbeatBody(nil, state.NewStore(), nil, false)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if skip {
		t.Fatal("空 fields 不应 skip")
	}
	if len(body) != 0 {
		t.Fatalf("空 fields body len=%d want=0", len(body))
	}
}

// ──────────────────────────────────────────────────────────────────────────
// 组合布局：构造一个类 build_battle_tcp_heart 的 4 字段布局（证明覆盖 2-B.2 动态布局）
// 字段：u16 stateCounter(packageIndex) / i64 state(battleId) / u8 state(fighterIndex) / i64 state(session)
// 期望字节序与手算一致。
// ──────────────────────────────────────────────────────────────────────────

func TestBuildHeartbeatBody_ComboLayout_BattleTcpHeartShape(t *testing.T) {
	st := state.NewStore()
	st.Set("packageIndex", int64(99))
	st.Set("battleId", int64(0x0102030405060708))
	st.Set("fighterIndex", int64(0xAB))
	st.Set("session", int64(0x0807060504030201))

	fields := []HeartbeatField{
		{Type: "u16", Source: "stateCounter", Key: "packageIndex"}, // 99→自增到 100
		{Type: "i64", Source: "state", Key: "battleId"},
		{Type: "u8", Source: "state", Key: "fighterIndex"},
		{Type: "i64", Source: "state", Key: "session"},
	}
	body, _, err := BuildHeartbeatBody(fields, st, nil, false)
	if err != nil {
		t.Fatalf("err=%v", err)
	}

	// 手算期望（小端）：
	//   u16(100)                  = 64 00
	//   i64(0x0102030405060708)   = 08 07 06 05 04 03 02 01
	//   u8(0xAB)                  = ab
	//   i64(0x0807060504030201)   = 01 02 03 04 05 06 07 08
	want := []byte{
		0x64, 0x00,
		0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01,
		0xab,
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
	}
	if len(body) != len(want) {
		t.Fatalf("len=%d want=%d body=%x", len(body), len(want), body)
	}
	for i := range want {
		if body[i] != want[i] {
			t.Fatalf("byte[%d]=%#x want=%#x\n body=%x\n want=%x", i, body[i], want[i], body, want)
		}
	}
}
