package script

import (
	"context"
	"strings"
	"testing"

	"stressbot/engine"
	"stressbot/errcode"

	lua "github.com/yuin/gopher-lua"
)

type requestTestAdapter struct {
	expectedRouteKey string
	desc             string
}

func (a *requestTestAdapter) HeaderSize() int { return 0 }

func (a *requestTestAdapter) BodyLength([]byte) int { return 0 }

func (a *requestTestAdapter) EncodeTCP(_ any, body []byte, _ []byte) []byte {
	return append([]byte("tcp:"), body...)
}

func (a *requestTestAdapter) EncodeUDP(_ any, body []byte, _ []byte) []byte {
	return append([]byte("udp:"), body...)
}

func (a *requestTestAdapter) DecodeTCP([]byte, []byte) (string, []byte, uint64) { return "", nil, 0 }

func (a *requestTestAdapter) DecodeUDP([]byte, []byte) (string, []byte, uint64) { return "", nil, 0 }

func (a *requestTestAdapter) ExpectedRouteKey(any) string { return a.expectedRouteKey }

func (a *requestTestAdapter) Close() {}

func (a *requestTestAdapter) DescribeError(uint64) string { return a.desc }

func TestTCPSend_EncodeFailure_ReturnsErrTable(t *testing.T) {
	// resolver nil → buildPacket 命中 encode 失败分支，send API 应返回 err table。
	ns := &fakeNetSender{}
	L := newTestState(t, context.Background(), ns, nil)
	defer L.Close()
	if err := L.DoString(`
		local network = require("network")
		e = network.tcp_send("logic", {cmd=1,act=1}, nil)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}
	assertRequestErr(t, L, int(errcode.ErrEncodeFailed), "service=logic")
}

func TestUDPSend_EncodeFailure_ReturnsErrTable(t *testing.T) {
	// resolver nil → udp_send 保留 network not available 编程错误；codec 未映射用 resolver 返回 nil 覆盖。
	ns := &fakeNetSender{}
	L := newTestState(t, context.Background(), ns, &fakeResolver{})
	defer L.Close()
	if err := L.DoString(`
		local network = require("network")
		e = network.udp_send("battle", {cmd=1,act=1}, "body")
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}
	assertRequestErr(t, L, int(errcode.ErrEncodeFailed), "service=battle")
}

func TestTCPSend_SuccessReturnsNil(t *testing.T) {
	ns := &fakeNetSender{}
	L := newTestState(t, context.Background(), ns, &fakeResolver{adp: &requestTestAdapter{}})
	defer L.Close()
	if err := L.DoString(`
		local network = require("network")
		e = network.tcp_send("logic", {cmd=1,act=1}, nil)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}
	if got := L.GetGlobal("e"); got != lua.LNil {
		t.Fatalf("成功 err = %T(%v), want nil", got, got)
	}
}

func TestUDPSend_SuccessReturnsNil(t *testing.T) {
	ns := &fakeNetSender{}
	L := newTestState(t, context.Background(), ns, &fakeResolver{adp: &requestTestAdapter{}})
	defer L.Close()
	if err := L.DoString(`
		local network = require("network")
		e = network.udp_send("battle", {cmd=1,act=1}, "body")
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}
	if got := L.GetGlobal("e"); got != lua.LNil {
		t.Fatalf("成功 err = %T(%v), want nil", got, got)
	}
}

func TestTCPRequest_EncodeFailure_ReturnsErrTable(t *testing.T) {
	// resolver nil → buildPacket/Resolve 命中 encode 失败分支
	ns := &fakeNetSender{}
	L := newTestState(t, context.Background(), ns, nil)
	defer L.Close()
	if err := L.DoString(`
		local network = require("network")
		e, d = network.tcp_request("logic", {cmd=1,act=1}, nil, "Game.X", 5)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}
	errLV := L.GetGlobal("e")
	tb, ok := errLV.(*lua.LTable)
	if !ok {
		t.Fatalf("err 不是 table（resolver nil 应命中 encode 失败）: %T", errLV)
	}
	if int(lua.LVAsNumber(tb.RawGetString("code"))) == 0 {
		t.Fatalf("code 不应为 0")
	}
	if L.GetGlobal("d") != lua.LNil {
		t.Fatalf("失败 data 应 nil: %T", L.GetGlobal("d"))
	}
}

func TestUDPRequest_EncodeFailure_ReturnsErrTable(t *testing.T) {
	// resolver nil → doUDPRequest 命中 encode 失败分支
	ns := &fakeNetSender{}
	L := newTestState(t, context.Background(), ns, nil)
	defer L.Close()
	if err := L.DoString(`
		local network = require("network")
		e, d = network.udp_request("battle", {cmd=1,act=1}, "body", "Game.X", 5)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}
	errLV := L.GetGlobal("e")
	tb, ok := errLV.(*lua.LTable)
	if !ok {
		t.Fatalf("err 不是 table（resolver nil 应命中 encode 失败）: %T", errLV)
	}
	if int(lua.LVAsNumber(tb.RawGetString("code"))) == 0 {
		t.Fatalf("code 不应为 0")
	}
	if L.GetGlobal("d") != lua.LNil {
		t.Fatalf("失败 data 应 nil: %T", L.GetGlobal("d"))
	}
}

func TestTCPRequest_EmptyRouteKey_ReturnsErrTableWithoutSending(t *testing.T) {
	ns := &fakeNetSender{}
	L := newTestState(t, context.Background(), ns, &fakeResolver{adp: &requestTestAdapter{}})
	defer L.Close()

	if err := L.DoString(`
		local network = require("network")
		e, d = network.tcp_request("logic", {cmd=1,act=1}, nil, nil, 5)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	assertRequestErr(t, L, int(errcode.ErrEncodeFailed), "service=logic routeKey 解析失败")
	if got := ns.tcpReqCalls; got != 0 {
		t.Fatalf("TCPRequest calls = %d, want 0", got)
	}
	if L.GetGlobal("d") != lua.LNil {
		t.Fatalf("失败 data 应 nil: %T", L.GetGlobal("d"))
	}
}

func TestUDPRequest_EmptyRouteKey_ReturnsErrTableWithoutSending(t *testing.T) {
	ns := &fakeNetSender{}
	L := newTestState(t, context.Background(), ns, &fakeResolver{adp: &requestTestAdapter{}})
	defer L.Close()

	if err := L.DoString(`
		local network = require("network")
		e, d = network.udp_request_route("battle", {cmd=1,act=1}, {cmd=1,act=1}, "body", nil, 5)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	assertRequestErr(t, L, int(errcode.ErrEncodeFailed), "service=battle routeKey 解析失败")
	if got := ns.udpReqCalls; got != 0 {
		t.Fatalf("UDPRequest calls = %d, want 0", got)
	}
	if L.GetGlobal("d") != lua.LNil {
		t.Fatalf("失败 data 应 nil: %T", L.GetGlobal("d"))
	}
}

func TestTCPRequest_HeaderErr_ReturnsErrTableAndBody(t *testing.T) {
	ns := &fakeNetSender{tcpReqExchange: &engine.NetExchange{HeaderErr: 101, Body: []byte("raw-body")}}
	L := newTestState(t, context.Background(), ns, &fakeResolver{adp: &requestTestAdapter{expectedRouteKey: "1:2", desc: "角色不存在"}})
	defer L.Close()

	if err := L.DoString(`
		local network = require("network")
		e, d = network.tcp_request("logic", {cmd=1,act=2}, nil, nil, 5)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	assertRequestErr(t, L, 101, "角色不存在: service=logic route=1:2")
	if got := lua.LVAsString(L.GetGlobal("d")); got != "raw-body" {
		t.Fatalf("data = %q, want raw-body", got)
	}
	if got := ns.tcpReqCalls; got != 1 {
		t.Fatalf("TCPRequest calls = %d, want 1", got)
	}
}

func assertRequestErr(t *testing.T, L *lua.LState, wantCode int, wantDetailContains string) {
	t.Helper()
	errLV := L.GetGlobal("e")
	tb, ok := errLV.(*lua.LTable)
	if !ok {
		t.Fatalf("err 不是 table: %T", errLV)
	}
	if got := int(lua.LVAsNumber(tb.RawGetString("code"))); got != wantCode {
		t.Fatalf("code = %d, want %d", got, wantCode)
	}
	detail := lua.LVAsString(tb.RawGetString("detail"))
	if !strings.Contains(detail, wantDetailContains) {
		t.Fatalf("detail = %q, want contains %q", detail, wantDetailContains)
	}
}
