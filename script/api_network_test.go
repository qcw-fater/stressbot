package script

import (
	"context"
	"testing"

	lua "github.com/yuin/gopher-lua"
)

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
