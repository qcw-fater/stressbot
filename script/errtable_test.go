package script

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"stressbot/engine"
	"stressbot/errcode"

	lua "github.com/yuin/gopher-lua"
)

func TestNewErrTable(t *testing.T) {
	L := lua.NewState()
	defer L.Close()
	tb := newErrTable(L, int(errcode.ErrEncodeFailed), "service=logic route=1:2 codec 未映射")
	if got := tb.RawGetString("code"); got != lua.LNumber(int(errcode.ErrEncodeFailed)) {
		t.Fatalf("code = %v, want %d", got, int(errcode.ErrEncodeFailed))
	}
	if got := lua.LVAsString(tb.RawGetString("detail")); got != "service=logic route=1:2 codec 未映射" {
		t.Fatalf("detail = %q", got)
	}
}

func TestParseErrTable(t *testing.T) {
	L := lua.NewState()
	defer L.Close()
	tb := newErrTable(L, 4, "recv 超时")
	if code, detail, ok := parseErrTable(tb); !ok || code != 4 || detail != "recv 超时" {
		t.Fatalf("parse table: ok=%v code=%d detail=%q", ok, code, detail)
	}
	if _, _, ok := parseErrTable(lua.LNil); ok {
		t.Fatalf("LNil 不应解析为 err table")
	}
	if _, _, ok := parseErrTable(lua.LNumber(54)); ok {
		t.Fatalf("LNumber 不应解析为 err table（fail-loud 旧式返回）")
	}
}

func TestParseErrTableRejectsMalformedTable(t *testing.T) {
	L := lua.NewState()
	defer L.Close()

	cases := []struct {
		name string
		set  func(tb *lua.LTable)
	}{
		{
			name: "missing code",
			set: func(tb *lua.LTable) {
				tb.RawSetString("detail", lua.LString("x"))
			},
		},
		{
			name: "string code",
			set: func(tb *lua.LTable) {
				tb.RawSetString("code", lua.LString("54"))
				tb.RawSetString("detail", lua.LString("x"))
			},
		},
		{
			name: "zero code",
			set: func(tb *lua.LTable) {
				tb.RawSetString("code", lua.LNumber(0))
				tb.RawSetString("detail", lua.LString("x"))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tb := L.CreateTable(0, 2)
			tc.set(tb)
			if code, detail, ok := parseErrTable(tb); ok {
				t.Fatalf("malformed table parsed as err table: code=%d detail=%q", code, detail)
			}
		})
	}
}

func TestErrTableFromActionErrPreservesWrappedActionError(t *testing.T) {
	L := lua.NewState()
	defer L.Close()

	wrapped := fmt.Errorf("包装: %w", engine.NewActionError(errcode.ErrRecvTimeout, "service=logic route=1:2 recv 超时"))
	tb := errTableFromActionErr(L, wrapped)
	if got := tb.RawGetString("code"); got != lua.LNumber(int(errcode.ErrRecvTimeout)) {
		t.Fatalf("code = %v, want %d", got, int(errcode.ErrRecvTimeout))
	}
	if got := lua.LVAsString(tb.RawGetString("detail")); got != "service=logic route=1:2 recv 超时" {
		t.Fatalf("detail = %q", got)
	}
}

func TestBuildActionError(t *testing.T) {
	// 框架码
	err := buildActionError(int(errcode.ErrRecvTimeout), "service=logic route=1:2", "match_succeed.lua")
	var ae *engine.ActionError
	if !errors.As(err, &ae) {
		t.Fatalf("err 不是 *ActionError: %T", err)
	}
	if ae.Code != errcode.ErrRecvTimeout {
		t.Fatalf("code=%v want %v", ae.Code, errcode.ErrRecvTimeout)
	}
	if !strings.Contains(ae.Detail, "script=match_succeed.lua") {
		t.Fatalf("detail 缺 script=: %q", ae.Detail)
	}
	// 业务码
	err = buildActionError(1004, "队伍已满: route=CreateTeam", "guild_join.lua")
	if !errors.As(err, &ae) {
		t.Fatal("业务码 err 不是 *ActionError")
	}
	if ae.Code != errcode.ErrorCode(1004) {
		t.Fatalf("code=%v want 1004", ae.Code)
	}
	if !strings.Contains(ae.Detail, "队伍已满") {
		t.Fatalf("detail 缺原因: %q", ae.Detail)
	}
}
