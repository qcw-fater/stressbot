package script

import (
	"testing"

	lua "github.com/yuin/gopher-lua"
)

func runLua(t *testing.T, code string) *lua.LState {
	t.Helper()
	L := lua.NewState()
	registerAPIs(L)
	if err := L.DoString(code); err != nil {
		L.Close()
		t.Fatalf("lua error: %v", err)
	}
	return L
}

func TestJSONDecodeObjectArrayScalars(t *testing.T) {
	L := runLua(t, `
		local json = require("json")
		t = json.decode('{"a":1,"b":"x","c":true,"d":null,"e":[1,2,3],"f":{"g":1.5}}')
	`)
	defer L.Close()

	tb := L.GetGlobal("t").(*lua.LTable)
	if got := tb.RawGetString("a"); got != lua.LNumber(1) {
		t.Errorf("a = %v", got)
	}
	if got := tb.RawGetString("b"); got != lua.LString("x") {
		t.Errorf("b = %v", got)
	}
	if got := tb.RawGetString("c"); got != lua.LBool(true) {
		t.Errorf("c = %v", got)
	}
	if got := tb.RawGetString("d"); got != lua.LNil {
		t.Errorf("d = %v", got)
	}
	arr := tb.RawGetString("e").(*lua.LTable)
	if arr.Len() != 3 || arr.RawGetInt(2) != lua.LNumber(2) {
		t.Errorf("e = %v len=%d", arr, arr.Len())
	}
	nested := tb.RawGetString("f").(*lua.LTable)
	if nested.RawGetString("g") != lua.LNumber(1.5) {
		t.Errorf("f.g = %v", nested.RawGetString("g"))
	}
}

func TestJSONDecodeRootScalarAndEscapes(t *testing.T) {
	// 注意：用 \\ 让 Lua 字面量里保留反斜杠，真正的转义交给 JSON 解码器处理
	L := runLua(t, `
		local json = require("json")
		n = json.decode('42')
		s = json.decode('"a\\tb\\u00e9"')
		arr = json.decode('[]')
		obj = json.decode('{}')
	`)
	defer L.Close()

	if L.GetGlobal("n") != lua.LNumber(42) {
		t.Errorf("n = %v", L.GetGlobal("n"))
	}
	if L.GetGlobal("s") != lua.LString("a\tbé") {
		t.Errorf("s = %q", L.GetGlobal("s"))
	}
	if _, ok := L.GetGlobal("arr").(*lua.LTable); !ok {
		t.Errorf("arr not table")
	}
	if _, ok := L.GetGlobal("obj").(*lua.LTable); !ok {
		t.Errorf("obj not table")
	}
}

func TestJSONDecodeBigInt(t *testing.T) {
	// 超过 2^53 的整数应转为字符串以避免精度丢失
	L := runLua(t, `
		local json = require("json")
		v = json.decode('9223372036854775807')
	`)
	defer L.Close()
	if got := L.GetGlobal("v"); got != lua.LString("9223372036854775807") {
		t.Errorf("bigint = %v (%T)", got, got)
	}
}

func TestJSONDecodeSafeIntBoundary(t *testing.T) {
	L := runLua(t, `
		local json = require("json")
		safe_pos = json.decode('9007199254740992')
		unsafe_pos = json.decode('9007199254740993')
		safe_neg = json.decode('-9007199254740992')
		unsafe_neg = json.decode('-9007199254740993')
	`)
	defer L.Close()

	if got := L.GetGlobal("safe_pos"); got != lua.LNumber(9007199254740992) {
		t.Errorf("safe_pos = %v (%T)", got, got)
	}
	if got := L.GetGlobal("unsafe_pos"); got != lua.LString("9007199254740993") {
		t.Errorf("unsafe_pos = %v (%T)", got, got)
	}
	if got := L.GetGlobal("safe_neg"); got != lua.LNumber(-9007199254740992) {
		t.Errorf("safe_neg = %v (%T)", got, got)
	}
	if got := L.GetGlobal("unsafe_neg"); got != lua.LString("-9007199254740993") {
		t.Errorf("unsafe_neg = %v (%T)", got, got)
	}
}

func TestJSONDecodeHugeIntegerAndScientificNumber(t *testing.T) {
	L := runLua(t, `
		local json = require("json")
		huge_uint = json.decode('18446744073709551615')
		sci = json.decode('1e20')
		decimal = json.decode('1.25')
	`)
	defer L.Close()

	if got := L.GetGlobal("huge_uint"); got != lua.LString("18446744073709551615") {
		t.Errorf("huge_uint = %v (%T)", got, got)
	}
	if got := L.GetGlobal("sci"); got != lua.LNumber(1e20) {
		t.Errorf("sci = %v (%T)", got, got)
	}
	if got := L.GetGlobal("decimal"); got != lua.LNumber(1.25) {
		t.Errorf("decimal = %v (%T)", got, got)
	}
}

func TestJSONDecodeNullFieldBehavior(t *testing.T) {
	L := runLua(t, `
		local json = require("json")
		obj = json.decode('{"keep":1,"drop":null}')
		root_null = json.decode('null')
	`)
	defer L.Close()

	obj := L.GetGlobal("obj").(*lua.LTable)
	if got := obj.RawGetString("keep"); got != lua.LNumber(1) {
		t.Errorf("keep = %v", got)
	}
	if got := obj.RawGetString("drop"); got != lua.LNil {
		t.Errorf("drop = %v", got)
	}
	if got := L.GetGlobal("root_null"); got != lua.LNil {
		t.Errorf("root_null = %v", got)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	L := runLua(t, `
		local json = require("json")
		out = json.encode(json.decode('{"k":"v","n":7}'))
	`)
	defer L.Close()
	// ConfigStd 排序 key，输出稳定
	if got := L.GetGlobal("out"); got != lua.LString(`{"k":"v","n":7}`) {
		t.Errorf("roundtrip = %v", got)
	}
}

func TestJSONDecodeInvalid(t *testing.T) {
	L := lua.NewState()
	defer L.Close()
	registerAPIs(L)
	err := L.DoString(`local json = require("json"); json.decode('{bad')`)
	if err == nil {
		t.Errorf("expected error for invalid json")
	}
}
