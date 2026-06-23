package script

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stressbot/adapter"
	"stressbot/engine"
	"stressbot/errcode"
	"stressbot/protox"
	stresslog "stressbot/utils/log"

	lua "github.com/yuin/gopher-lua"
	"go.uber.org/zap"
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
	if got := ns.tcpSendCalls; got != 1 {
		t.Fatalf("TCPSend calls = %d, want 1", got)
	}
	if got := ns.lastTCPService; got != "logic" {
		t.Fatalf("TCPSend service = %q, want logic", got)
	}
	if want := []byte("tcp:"); !bytes.Equal(ns.lastTCPPacket, want) {
		t.Fatalf("TCPSend packet = %q, want %q", ns.lastTCPPacket, want)
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
	if got := ns.udpSendCalls; got != 1 {
		t.Fatalf("UDPSend calls = %d, want 1", got)
	}
	if got := ns.lastUDPService; got != "battle" {
		t.Fatalf("UDPSend service = %q, want battle", got)
	}
	if want := []byte("udp:body"); !bytes.Equal(ns.lastUDPPacket, want) {
		t.Fatalf("UDPSend packet = %q, want %q", ns.lastUDPPacket, want)
	}
}

func TestTCPSend_SendFailure_ReturnsErrTable(t *testing.T) {
	ns := &fakeNetSender{tcpSendErr: engine.NewActionError(errcode.ErrSendFailed, "send 失败 detail")}
	L := newTestState(t, context.Background(), ns, &fakeResolver{adp: &requestTestAdapter{}})
	defer L.Close()
	if err := L.DoString(`
		local network = require("network")
		e, extra = network.tcp_send("logic", {cmd=1,act=1}, nil)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}
	assertRequestErr(t, L, int(errcode.ErrSendFailed), "send 失败 detail")
	if got := L.GetGlobal("extra"); got != lua.LNil {
		t.Fatalf("失败 extra = %T(%v), want nil", got, got)
	}
	if got := ns.tcpSendCalls; got != 1 {
		t.Fatalf("TCPSend calls = %d, want 1", got)
	}
}

func TestUDPSend_SendFailure_ReturnsErrTable(t *testing.T) {
	ns := &fakeNetSender{udpSendErr: engine.NewActionError(errcode.ErrSendFailed, "send 失败 detail", errors.New("底层 send error"))}
	L := newTestState(t, context.Background(), ns, &fakeResolver{adp: &requestTestAdapter{}})
	defer L.Close()
	if err := L.DoString(`
		local network = require("network")
		e, extra = network.udp_send("battle", {cmd=1,act=1}, "body")
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}
	assertRequestErr(t, L, int(errcode.ErrSendFailed), "send 失败 detail")
	if got := L.GetGlobal("extra"); got != lua.LNil {
		t.Fatalf("失败 extra = %T(%v), want nil", got, got)
	}
	if got := ns.udpSendCalls; got != 1 {
		t.Fatalf("UDPSend calls = %d, want 1", got)
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

func TestTCPListen_EncodeFailure_ReturnsErrTable(t *testing.T) {
	ns := &fakeNetSender{}
	L := newTestState(t, context.Background(), ns, &fakeResolver{})
	defer L.Close()

	if err := L.DoString(`
		local network = require("network")
		e, d = network.tcp_listen("logic", {cmd=1,act=1}, "Game.X", 0, 1)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	assertRequestErr(t, L, int(errcode.ErrEncodeFailed), "resolver.Resolve(tcp:logic) nil")
	if got := L.GetGlobal("d"); got != lua.LNil {
		t.Fatalf("编码失败 data = %T(%v), want nil", got, got)
	}
	if got := ns.tcpListenCalls; got != 0 {
		t.Fatalf("GetTCPListenResp calls = %d, want 0", got)
	}
}

func TestTCPListen_EmptyRouteKey_ReturnsErrTableWithoutPolling(t *testing.T) {
	ns := &fakeNetSender{}
	L := newTestState(t, context.Background(), ns, &fakeResolver{adp: &requestTestAdapter{}})
	defer L.Close()

	if err := L.DoString(`
		local network = require("network")
		e, d = network.tcp_listen("logic", {cmd=1,act=1}, "Game.X", 0, 1)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	assertRequestErr(t, L, int(errcode.ErrEncodeFailed), "service=logic routeKey 解析失败")
	if got := L.GetGlobal("d"); got != lua.LNil {
		t.Fatalf("routeKey 失败 data = %T(%v), want nil", got, got)
	}
	if got := ns.tcpListenCalls; got != 0 {
		t.Fatalf("GetTCPListenResp calls = %d, want 0", got)
	}
}

func TestTCPListen_Canceled_ReturnsErrTable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ns := &fakeNetSender{}
	L := newTestState(t, ctx, ns, &fakeResolver{adp: &requestTestAdapter{expectedRouteKey: "1:1"}})
	defer L.Close()

	if err := L.DoString(`
		local network = require("network")
		e, d = network.tcp_listen("logic", {cmd=1,act=1}, "Game.X", 1, 50)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	assertRequestErr(t, L, int(errcode.ErrActionCanceled), "service=logic")
	if got := L.GetGlobal("d"); got != lua.LNil {
		t.Fatalf("取消 data = %T(%v), want nil", got, got)
	}
}

func TestTCPListen_ParseFailure_ReturnsErrTable(t *testing.T) {
	ns := &fakeNetSender{listenResp: &engine.NetExchange{Body: []byte("not-protobuf")}}
	L := newTestState(t, context.Background(), ns, &fakeResolver{adp: &requestTestAdapter{expectedRouteKey: "1:1"}})
	GetContext(L).Factory = newListenTestFactory(t)
	defer L.Close()

	if err := L.DoString(`
		local network = require("network")
		e, d = network.tcp_listen("logic", {cmd=1,act=1}, "listentest.Push", 1, 50)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	assertRequestErr(t, L, int(errcode.ErrParseFailed), "service=logic")
	if got := L.GetGlobal("d"); got != lua.LNil {
		t.Fatalf("解析失败 data = %T(%v), want nil", got, got)
	}
	if got := ns.tcpListenCalls; got != 1 {
		t.Fatalf("GetTCPListenResp calls = %d, want 1", got)
	}
}

func TestTCPListen_Timeout_ReturnsErrTable(t *testing.T) {
	ns := &fakeNetSender{}
	L := newTestState(t, context.Background(), ns, &fakeResolver{adp: &requestTestAdapter{expectedRouteKey: "1:1"}})
	defer L.Close()

	if err := L.DoString(`
		local network = require("network")
		e, d = network.tcp_listen("logic", {cmd=1,act=1}, "Game.X", 0, 50)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	assertRequestErr(t, L, int(errcode.ErrListenTimeout), "service=logic route=1:1")
	if got := L.GetGlobal("d"); got != lua.LNil {
		t.Fatalf("超时 data = %T(%v), want nil", got, got)
	}
}

func TestUDPListen_Timeout_ReturnsErrTable(t *testing.T) {
	ns := &fakeNetSender{}
	L := newTestState(t, context.Background(), ns, &fakeResolver{adp: &requestTestAdapter{expectedRouteKey: "1:1"}})
	defer L.Close()

	if err := L.DoString(`
		local network = require("network")
		e, d = network.udp_listen("battle", {cmd=1,act=1}, "Game.X", 0, 50)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	assertRequestErr(t, L, int(errcode.ErrListenTimeout), "service=battle route=1:1")
	if got := L.GetGlobal("d"); got != lua.LNil {
		t.Fatalf("超时 data = %T(%v), want nil", got, got)
	}
}

func TestTCPListen_HeaderErr_ReturnsErrTableAndBody(t *testing.T) {
	ns := &fakeNetSender{listenResp: &engine.NetExchange{HeaderErr: 101, Body: []byte("listen-body")}}
	L := newTestState(t, context.Background(), ns, &fakeResolver{adp: &requestTestAdapter{expectedRouteKey: "1:1", desc: "角色不存在"}})
	defer L.Close()

	if err := L.DoString(`
		local network = require("network")
		e, d = network.tcp_listen("logic", {cmd=1,act=1}, "", 1, 50)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	assertRequestErr(t, L, 101, "角色不存在: service=logic route=1:1")
	if got := lua.LVAsString(L.GetGlobal("d")); got != "listen-body" {
		t.Fatalf("data = %q, want listen-body", got)
	}
}

func TestUDPListen_HeaderError_ReturnsErrTableAndBody(t *testing.T) {
	ns := &fakeNetSender{listenResp: &engine.NetExchange{HeaderErr: 102, Body: []byte("udp-listen-body")}}
	resolver := &fakeResolver{adps: map[string]adapter.Adapter{
		"udp:battle": &requestTestAdapter{expectedRouteKey: "2:3", desc: "战斗错误"},
	}}
	L := newTestState(t, context.Background(), ns, resolver)
	defer L.Close()

	if err := L.DoString(`
		local network = require("network")
		e, d = network.udp_listen("battle", {cmd=2,act=3}, "", 1, 50)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	assertRequestErr(t, L, 102, "战斗错误: service=battle route=2:3")
	if got := lua.LVAsString(L.GetGlobal("d")); got != "udp-listen-body" {
		t.Fatalf("data = %q, want udp-listen-body", got)
	}
	if got := ns.udpListenCalls; got != 1 {
		t.Fatalf("GetUDPListenResp calls = %d, want 1", got)
	}
	if len(resolver.resolved) < 2 || resolver.resolved[0] != "udp:battle" || resolver.resolved[1] != "udp:battle" {
		t.Fatalf("resolved = %q, want err table detail to prefer udp:battle", strings.Join(resolver.resolved, ","))
	}
}

func TestTCPListen_RawBodySuccess_ReturnsNilAndString(t *testing.T) {
	ns := &fakeNetSender{listenResp: &engine.NetExchange{Body: []byte("listen-body")}}
	L := newTestState(t, context.Background(), ns, &fakeResolver{adp: &requestTestAdapter{expectedRouteKey: "1:1"}})
	defer L.Close()

	if err := L.DoString(`
		local network = require("network")
		e, d = network.tcp_listen("logic", {cmd=1,act=1}, "", 1, 50)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	if got := L.GetGlobal("e"); got != lua.LNil {
		t.Fatalf("成功 err = %T(%v), want nil", got, got)
	}
	if got := lua.LVAsString(L.GetGlobal("d")); got != "listen-body" {
		t.Fatalf("data = %q, want listen-body", got)
	}
}

func TestTryTCPListen_EmptyQueueReturnsNilNil(t *testing.T) {
	ns := &fakeNetSender{}
	L := newTestState(t, context.Background(), ns, &fakeResolver{adp: &requestTestAdapter{expectedRouteKey: "1:1"}})
	defer L.Close()

	if err := L.DoString(`
		local network = require("network")
		e, d = network.try_tcp_listen("logic", {cmd=1,act=1})
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	if got := L.GetGlobal("e"); got != lua.LNil {
		t.Fatalf("队列空 err = %T(%v), want nil", got, got)
	}
	if got := L.GetGlobal("d"); got != lua.LNil {
		t.Fatalf("队列空 data = %T(%v), want nil", got, got)
	}
	if got := ns.tcpListenCalls; got != 1 {
		t.Fatalf("GetTCPListenResp calls = %d, want 1", got)
	}
}

func TestTryUDPListen_EmptyQueueReturnsNilNil(t *testing.T) {
	ns := &fakeNetSender{}
	L := newTestState(t, context.Background(), ns, &fakeResolver{adp: &requestTestAdapter{expectedRouteKey: "2:3"}})
	defer L.Close()

	if err := L.DoString(`
		local network = require("network")
		e, d = network.try_udp_listen("battle", {cmd=2,act=3})
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	if got := L.GetGlobal("e"); got != lua.LNil {
		t.Fatalf("队列空 err = %T(%v), want nil", got, got)
	}
	if got := L.GetGlobal("d"); got != lua.LNil {
		t.Fatalf("队列空 data = %T(%v), want nil", got, got)
	}
	if got := ns.udpListenCalls; got != 1 {
		t.Fatalf("GetUDPListenResp calls = %d, want 1", got)
	}
}

func TestTryTCPListen_HeaderErrReturnsErrTableAndBody(t *testing.T) {
	ns := &fakeNetSender{listenResp: &engine.NetExchange{HeaderErr: 101, Body: []byte("try-body")}}
	L := newTestState(t, context.Background(), ns, &fakeResolver{adp: &requestTestAdapter{expectedRouteKey: "1:1", desc: "角色不存在"}})
	defer L.Close()

	if err := L.DoString(`
		local network = require("network")
		e, d = network.try_tcp_listen("logic", {cmd=1,act=1})
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	assertRequestErr(t, L, 101, "角色不存在: service=logic route=1:1")
	if got := lua.LVAsString(L.GetGlobal("d")); got != "try-body" {
		t.Fatalf("data = %q, want try-body", got)
	}
}

func TestTryUDPListen_RawBodySuccessReturnsNilAndString(t *testing.T) {
	ns := &fakeNetSender{listenResp: &engine.NetExchange{Body: []byte("udp-try-body")}}
	L := newTestState(t, context.Background(), ns, &fakeResolver{adp: &requestTestAdapter{expectedRouteKey: "2:3"}})
	defer L.Close()

	if err := L.DoString(`
		local network = require("network")
		e, d = network.try_udp_listen("battle", {cmd=2,act=3})
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	if got := L.GetGlobal("e"); got != lua.LNil {
		t.Fatalf("成功 err = %T(%v), want nil", got, got)
	}
	if got := lua.LVAsString(L.GetGlobal("d")); got != "udp-try-body" {
		t.Fatalf("data = %q, want udp-try-body", got)
	}
}

func TestTryTCPListen_EncodeFailureReturnsErrTable(t *testing.T) {
	ns := &fakeNetSender{}
	L := newTestState(t, context.Background(), ns, &fakeResolver{})
	defer L.Close()

	if err := L.DoString(`
		local network = require("network")
		e, d = network.try_tcp_listen("logic", {cmd=1,act=1})
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	assertRequestErr(t, L, int(errcode.ErrEncodeFailed), "resolver.Resolve(tcp:logic) nil")
	if got := L.GetGlobal("d"); got != lua.LNil {
		t.Fatalf("编码失败 data = %T(%v), want nil", got, got)
	}
	if got := ns.tcpListenCalls; got != 0 {
		t.Fatalf("GetTCPListenResp calls = %d, want 0", got)
	}
}

func TestTryTCPListen_EmptyRouteKeyReturnsErrTableWithoutPolling(t *testing.T) {
	ns := &fakeNetSender{}
	L := newTestState(t, context.Background(), ns, &fakeResolver{adp: &requestTestAdapter{}})
	defer L.Close()

	if err := L.DoString(`
		local network = require("network")
		e, d = network.try_tcp_listen("logic", {cmd=1,act=1})
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}

	assertRequestErr(t, L, int(errcode.ErrEncodeFailed), "service=logic routeKey 解析失败")
	if got := L.GetGlobal("d"); got != lua.LNil {
		t.Fatalf("routeKey 失败 data = %T(%v), want nil", got, got)
	}
	if got := ns.tcpListenCalls; got != 0 {
		t.Fatalf("GetTCPListenResp calls = %d, want 0", got)
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

func newListenTestFactory(t *testing.T) *protox.Factory {
	t.Helper()
	stresslog.ReplaceLogger(zap.NewNop())
	dir := t.TempDir()
	protoContent := `syntax = "proto3";

package listentest;

message Push {
  int32 seq = 1;
}
`
	protoPath := filepath.Join(dir, "listen_test.proto")
	if err := os.WriteFile(protoPath, []byte(protoContent), 0644); err != nil {
		t.Fatalf("写入测试 proto 失败: %v", err)
	}
	loader := protox.NewLoader([]string{dir}, []string{"listen_test.proto"})
	files, err := loader.Load()
	if err != nil {
		t.Fatalf("加载测试 proto 失败: %v", err)
	}
	return protox.NewFactory(protox.NewRegistry(files))
}
