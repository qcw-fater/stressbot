package script

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"stressbot/adapter"
	"stressbot/engine"
	"stressbot/errcode"

	lua "github.com/yuin/gopher-lua"
)

// fakeAdapter 测试用 adapter.Adapter 桩：encode 返回 encodeBytes（nil 模拟编码失败），
// ExpectedRouteKey 返回 routeKey，DescribeError 返回 errDesc。
type fakeAdapter struct {
	encodeBytes   []byte   // EncodeTCP/EncodeUDP 返回值；nil 模拟编码失败
	routeKey      string   // ExpectedRouteKey 返回值
	errDesc       string   // DescribeError 返回值
	headerSize    int
	bodyLen       int
	encodeCalls   int
	routeKeyCalls int
}

func (a *fakeAdapter) HeaderSize() int { return a.headerSize }
func (a *fakeAdapter) BodyLength([]byte) int { return a.bodyLen }
func (a *fakeAdapter) EncodeTCP(route any, body []byte, secretKey []byte) []byte {
	a.encodeCalls++
	return a.encodeBytes
}
func (a *fakeAdapter) EncodeUDP(route any, body []byte, secretKey []byte) []byte {
	a.encodeCalls++
	return a.encodeBytes
}
func (a *fakeAdapter) DecodeTCP([]byte, []byte) (string, []byte, uint64) {
	return "", nil, 0
}
func (a *fakeAdapter) DecodeUDP([]byte, []byte) (string, []byte, uint64) {
	return "", nil, 0
}
func (a *fakeAdapter) ExpectedRouteKey(route any) string {
	a.routeKeyCalls++
	return a.routeKey
}
func (a *fakeAdapter) Close() {}
func (a *fakeAdapter) DescribeError(uint64) string { return a.errDesc }

var _ adapter.Adapter = (*fakeAdapter)(nil)

// doNet 执行一段 Lua 源码（自动前置 require("network")），用于直接驱动同步早期返回分支。
func doNet(t *testing.T, L *lua.LState, src string) error {
	t.Helper()
	return L.DoString(`local network = require("network"); ` + src)
}

// errTableCode 提取栈顶值中第 idx（0-based）位置的 err table code，非 err table 返回 (0,false)。
func errTableCode(t *testing.T, vals []lua.LValue, idx int) (int, bool) {
	t.Helper()
	if idx >= len(vals) {
		t.Fatalf("返回值不足：需 idx=%d，实际 %d 个", idx, len(vals))
	}
	c, _, ok := parseErrTable(vals[idx])
	return c, ok
}

// requireErrTable 断言 idx 位置是 err table 且 code==wantCode。
func requireErrTable(t *testing.T, vals []lua.LValue, idx int, wantCode errcode.ErrorCode) {
	t.Helper()
	code, ok := errTableCode(t, vals, idx)
	if !ok {
		t.Fatalf("vals[%d]=%v，期望 err table", idx, vals[idx])
	}
	if code != int(wantCode) {
		t.Fatalf("vals[%d] code=%d，want %d", idx, code, wantCode)
	}
}

// ---------------------------------------------------------------------------
// tcp_request / udp_request 早期返回（encode 失败 / codec 未映射）— yield 之前的分支，
// 可直接 L.DoString 驱动（无需协程调度器）。
// ---------------------------------------------------------------------------

// TestTCPRquest_EncodeFail_ResolverNil ctx.Resolver 为 nil → buildPacket 返回 nil →
// pushResult(ErrEncodeFailed, nil)，不进入 awaitYield。
func TestTCPRequest_EncodeFail_ResolverNil(t *testing.T) {
	L := newTestState(t, context.Background(), &fakeNetSender{}, nil)
	defer L.Close()

	// resolver nil：函数体开头 ctx.Resolver nil 判定走 RaiseError（network not available）。
	err := doNet(t, L, `return network.tcp_request('logic', 'C2S.Login', nil)`)
	if err == nil {
		t.Fatalf("期望 RaiseError（network not available），实际 nil")
	}
	if !strings.Contains(err.Error(), "network not available") {
		t.Fatalf("err %q 应含 'network not available'", err.Error())
	}
}

// TestTCPRequest_EncodeFail_CodecUnmapped resolver 非 nil 但 Resolve 返回 nil →
// buildPacket 返回 nil → pushResult(ErrEncodeFailed, nil)，直接返回（不走 awaitYield）。
func TestTCPRequest_EncodeFail_CodecUnmapped(t *testing.T) {
	resolver := &fakeResolver{adp: nil} // Resolve 一律返回 nil
	L := newTestState(t, context.Background(), &fakeNetSender{}, resolver)
	defer L.Close()

	err := doNet(t, L, `local e, d = network.tcp_request('logic', 'C2S.Login', nil); return e, d`)
	if err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	vals := popReturns(L, 2)
	requireErrTable(t, vals, 0, errcode.ErrEncodeFailed)
	if vals[1] != lua.LNil {
		t.Fatalf("encode 失败 data 应为 nil，实际 %v", vals[1])
	}
}

// TestTCPRequest_EncodeFail_AdapterEncodeNil codec 已映射但 EncodeTCP 返回 nil →
// pushResult(ErrEncodeFailed, nil)，直接返回。
func TestTCPRequest_EncodeFail_AdapterEncodeNil(t *testing.T) {
	adp := &fakeAdapter{encodeBytes: nil, routeKey: "S2C.Login"} // EncodeTCP 返回 nil
	resolver := &fakeResolver{adp: adp}
	L := newTestState(t, context.Background(), &fakeNetSender{}, resolver)
	defer L.Close()

	err := doNet(t, L, `local e, d = network.tcp_request('logic', 'C2S.Login', nil); return e, d`)
	if err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	vals := popReturns(L, 2)
	requireErrTable(t, vals, 0, errcode.ErrEncodeFailed)
	if vals[1] != lua.LNil {
		t.Fatalf("encode 失败 data 应为 nil，实际 %v", vals[1])
	}
	if adp.encodeCalls != 1 {
		t.Fatalf("EncodeTCP 应被调用 1 次，实际 %d", adp.encodeCalls)
	}
}

// TestTCPRequest_EmptyService service 空 → RaiseError。
func TestTCPRequest_EmptyService(t *testing.T) {
	adp := &fakeAdapter{encodeBytes: []byte("pkt"), routeKey: "S2C.Login"}
	resolver := &fakeResolver{adp: adp}
	L := newTestState(t, context.Background(), &fakeNetSender{}, resolver)
	defer L.Close()

	err := doNet(t, L, `return network.tcp_request('', 'C2S.Login', nil)`)
	if err == nil {
		t.Fatalf("期望 RaiseError（service 空），实际 nil")
	}
	if !strings.Contains(err.Error(), "tcp_request requires") {
		t.Fatalf("err %q 应含 'tcp_request requires'", err.Error())
	}
}

// TestUDPRequest_EncodeFail_CodecUnmapped resolver.Resolve("udp:...") 返回 nil →
// pushResult(ErrEncodeFailed, nil)，直接返回（不进入 awaitYield）。
func TestUDPRequest_EncodeFail_CodecUnmapped(t *testing.T) {
	resolver := &fakeResolver{adp: nil}
	L := newTestState(t, context.Background(), &fakeNetSender{}, resolver)
	defer L.Close()

	err := doNet(t, L, `local e, d = network.udp_request('battle', '3:1', 'body'); return e, d`)
	if err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	vals := popReturns(L, 2)
	requireErrTable(t, vals, 0, errcode.ErrEncodeFailed)
	if vals[1] != lua.LNil {
		t.Fatalf("encode 失败 data 应为 nil，实际 %v", vals[1])
	}
}

// TestUDPRequest_EncodeFail_AdapterEncodeNil codec 已映射但 EncodeUDP 返回 nil →
// pushResult(ErrEncodeFailed, nil)，直接返回。
func TestUDPRequest_EncodeFail_AdapterEncodeNil(t *testing.T) {
	adp := &fakeAdapter{encodeBytes: nil, routeKey: "3:1"}
	resolver := &fakeResolver{adp: adp}
	L := newTestState(t, context.Background(), &fakeNetSender{}, resolver)
	defer L.Close()

	err := doNet(t, L, `local e, d = network.udp_request('battle', '3:1', 'body'); return e, d`)
	if err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	vals := popReturns(L, 2)
	requireErrTable(t, vals, 0, errcode.ErrEncodeFailed)
}

// ---------------------------------------------------------------------------
// tcp_send / udp_send 完全同步（不 yield）— 覆盖 success / encode 失败 / send 错误。
// ---------------------------------------------------------------------------

// TestTCPSend_Success 编码+发送成功 → 压 lua.LNil。
func TestTCPSend_Success(t *testing.T) {
	adp := &fakeAdapter{encodeBytes: []byte("encoded"), routeKey: "C2S.Login"}
	resolver := &fakeResolver{adp: adp}
	ns := &fakeNetSender{}
	L := newTestState(t, context.Background(), ns, resolver)
	defer L.Close()

	err := doNet(t, L, `local e = network.tcp_send('logic', 'C2S.Login', nil); return e`)
	if err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	vals := popReturns(L, 1)
	if vals[0] != lua.LNil {
		t.Fatalf("send 成功应返回 nil，实际 %v", vals[0])
	}
	if ns.tcpSendCalls != 1 {
		t.Fatalf("TCPSend 应被调用 1 次，实际 %d", ns.tcpSendCalls)
	}
	if string(ns.lastTCPPacket) != "encoded" {
		t.Fatalf("发送的 packet=%q，want \"encoded\"", string(ns.lastTCPPacket))
	}
}

// TestTCPSend_EncodeFail buildPacket 返回 nil（resolver.Resolve nil）→ pushErr(ErrEncodeFailed)。
func TestTCPSend_EncodeFail_ResolverNil(t *testing.T) {
	// networkTCPSend 只要求 ctx.NetSender 非 nil（resolver 未在 guard 中），故 resolver nil
	// 会让 buildPacket 返回 nil。
	L := newTestState(t, context.Background(), &fakeNetSender{}, nil)
	defer L.Close()

	err := doNet(t, L, `local e = network.tcp_send('logic', 'C2S.Login', nil); return e`)
	if err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	vals := popReturns(L, 1)
	requireErrTable(t, vals, 0, errcode.ErrEncodeFailed)
}

// TestTCPSend_EncodeFail_AdapterEncodeNil EncodeTCP 返回 nil → pushErr(ErrEncodeFailed)。
func TestTCPSend_EncodeFail_AdapterEncodeNil(t *testing.T) {
	adp := &fakeAdapter{encodeBytes: nil, routeKey: "C2S.Login"}
	resolver := &fakeResolver{adp: adp}
	L := newTestState(t, context.Background(), &fakeNetSender{}, resolver)
	defer L.Close()

	err := doNet(t, L, `local e = network.tcp_send('logic', 'C2S.Login', nil); return e`)
	if err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	vals := popReturns(L, 1)
	requireErrTable(t, vals, 0, errcode.ErrEncodeFailed)
}

// TestTCPSend_SendErr 发送层返回错误 → err table（从 *ActionError 提取）。
func TestTCPSend_SendErr(t *testing.T) {
	adp := &fakeAdapter{encodeBytes: []byte("encoded"), routeKey: "C2S.Login"}
	resolver := &fakeResolver{adp: adp}
	ns := &fakeNetSender{
		tcpSendErr: engine.NewActionError(errcode.ErrConnNotFound, "conn-missing"),
	}
	L := newTestState(t, context.Background(), ns, resolver)
	defer L.Close()

	err := doNet(t, L, `local e = network.tcp_send('logic', 'C2S.Login', nil); return e`)
	if err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	vals := popReturns(L, 1)
	requireErrTable(t, vals, 0, errcode.ErrConnNotFound)
}

// TestUDPSend_Success 编码+发送成功 → 压 lua.LNil。
func TestUDPSend_Success(t *testing.T) {
	adp := &fakeAdapter{encodeBytes: []byte("udpkt"), routeKey: "3:1"}
	resolver := &fakeResolver{adp: adp}
	ns := &fakeNetSender{}
	L := newTestState(t, context.Background(), ns, resolver)
	defer L.Close()

	err := doNet(t, L, `local e = network.udp_send('battle', '3:1', 'body'); return e`)
	if err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	vals := popReturns(L, 1)
	if vals[0] != lua.LNil {
		t.Fatalf("send 成功应返回 nil，实际 %v", vals[0])
	}
	if ns.udpSendCalls != 1 {
		t.Fatalf("UDPSend 应被调用 1 次，实际 %d", ns.udpSendCalls)
	}
}

// TestUDPSend_EncodeFail_CodecUnmapped resolver.Resolve("udp:...") 返回 nil →
// pushErr(ErrEncodeFailed)。
func TestUDPSend_EncodeFail_CodecUnmapped(t *testing.T) {
	resolver := &fakeResolver{adp: nil}
	L := newTestState(t, context.Background(), &fakeNetSender{}, resolver)
	defer L.Close()

	err := doNet(t, L, `local e = network.udp_send('battle', '3:1', 'body'); return e`)
	if err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	vals := popReturns(L, 1)
	requireErrTable(t, vals, 0, errcode.ErrEncodeFailed)
}

// TestUDPSend_SendErr 发送层错误 → err table。
func TestUDPSend_SendErr(t *testing.T) {
	adp := &fakeAdapter{encodeBytes: []byte("udpkt"), routeKey: "3:1"}
	resolver := &fakeResolver{adp: adp}
	ns := &fakeNetSender{
		udpSendErr: engine.NewActionError(errcode.ErrSendFailed, "write-fail"),
	}
	L := newTestState(t, context.Background(), ns, resolver)
	defer L.Close()

	err := doNet(t, L, `local e = network.udp_send('battle', '3:1', 'body'); return e`)
	if err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	vals := popReturns(L, 1)
	requireErrTable(t, vals, 0, errcode.ErrSendFailed)
}

// ---------------------------------------------------------------------------
// try_tcp_listen / try_udp_listen 完全同步（非阻塞 pop，不 yield）— 覆盖队列空、
// codec 未映射、headerErr、正常消息 4 个分支。
// ---------------------------------------------------------------------------

// TestTryTCPListen_QueueEmpty GetTCPListenResp 返回 nil → (nil, nil)（非错误路径）。
func TestTryTCPListen_QueueEmpty(t *testing.T) {
	adp := &fakeAdapter{routeKey: "3:1"}
	resolver := &fakeResolver{adp: adp}
	ns := &fakeNetSender{listenResp: nil}
	L := newTestState(t, context.Background(), ns, resolver)
	defer L.Close()

	err := doNet(t, L, `local code, data = network.try_tcp_listen('logic', '3:1'); return code, data`)
	if err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	vals := popReturns(L, 2)
	if vals[0] != lua.LNil {
		t.Fatalf("队列空 code 应为 nil，实际 %v", vals[0])
	}
	if vals[1] != lua.LNil {
		t.Fatalf("队列空 data 应为 nil，实际 %v", vals[1])
	}
	if ns.tcpListenCalls != 1 {
		t.Fatalf("GetTCPListenResp 应被调用 1 次，实际 %d", ns.tcpListenCalls)
	}
}

// TestTryTCPListen_CodecUnmapped resolver.Resolve 返回 nil → pushResult(ErrEncodeFailed, nil)。
func TestTryTCPListen_CodecUnmapped(t *testing.T) {
	resolver := &fakeResolver{adp: nil}
	L := newTestState(t, context.Background(), &fakeNetSender{}, resolver)
	defer L.Close()

	err := doNet(t, L, `local code, data = network.try_tcp_listen('logic', '3:1'); return code, data`)
	if err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	vals := popReturns(L, 2)
	requireErrTable(t, vals, 0, errcode.ErrEncodeFailed)
}

// TestTryTCPListen_HeaderErr 命中消息但 HeaderErr 非零 → (err table, body)。
func TestTryTCPListen_HeaderErr(t *testing.T) {
	adp := &fakeAdapter{routeKey: "3:1"}
	resolver := &fakeResolver{adp: adp}
	ns := &fakeNetSender{listenResp: &engine.NetExchange{HeaderErr: 1001, Body: []byte("e"), RecvWireBytes: 5}}
	L := newTestState(t, context.Background(), ns, resolver)
	defer L.Close()

	err := doNet(t, L, `local code, data = network.try_tcp_listen('logic', '3:1'); return code, data`)
	if err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	vals := popReturns(L, 2)
	code, ok := errTableCode(t, vals, 0)
	if !ok {
		t.Fatalf("code=%v，期望 err table（HeaderErr）", vals[0])
	}
	if code != 1001 {
		t.Fatalf("code=%d，want 1001", code)
	}
	if s, ok := vals[1].(lua.LString); !ok || string(s) != "e" {
		t.Fatalf("data=%v，want \"e\"", vals[1])
	}
}

// TestTryTCPListen_NormalMessage 命中正常消息（HeaderErr=0）→ (nil, body)。
func TestTryTCPListen_NormalMessage(t *testing.T) {
	adp := &fakeAdapter{routeKey: "3:1"}
	resolver := &fakeResolver{adp: adp}
	ns := &fakeNetSender{listenResp: &engine.NetExchange{Body: []byte("hello"), RecvWireBytes: 5}}
	L := newTestState(t, context.Background(), ns, resolver)
	defer L.Close()

	err := doNet(t, L, `local code, data = network.try_tcp_listen('logic', '3:1'); return code, data`)
	if err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	vals := popReturns(L, 2)
	if vals[0] != lua.LNil {
		t.Fatalf("正常消息 code 应为 nil，实际 %v", vals[0])
	}
	if s, ok := vals[1].(lua.LString); !ok || string(s) != "hello" {
		t.Fatalf("data=%v，want \"hello\"", vals[1])
	}
}

// TestTryUDPListen_QueueEmpty UDP 对称：队列空 → (nil, nil)。
func TestTryUDPListen_QueueEmpty(t *testing.T) {
	adp := &fakeAdapter{routeKey: "3:1"}
	resolver := &fakeResolver{adp: adp}
	ns := &fakeNetSender{listenResp: nil}
	L := newTestState(t, context.Background(), ns, resolver)
	defer L.Close()

	err := doNet(t, L, `local code, data = network.try_udp_listen('battle', '3:1'); return code, data`)
	if err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	vals := popReturns(L, 2)
	if vals[0] != lua.LNil || vals[1] != lua.LNil {
		t.Fatalf("队列空应 (nil,nil)，实际 (%v,%v)", vals[0], vals[1])
	}
	if ns.udpListenCalls != 1 {
		t.Fatalf("GetUDPListenResp 应被调用 1 次，实际 %d", ns.udpListenCalls)
	}
}

// TestTryUDPListen_CodecUnmapped UDP 对称：codec 未映射 → err table。
func TestTryUDPListen_CodecUnmapped(t *testing.T) {
	resolver := &fakeResolver{adp: nil}
	L := newTestState(t, context.Background(), &fakeNetSender{}, resolver)
	defer L.Close()

	err := doNet(t, L, `local code, data = network.try_udp_listen('battle', '3:1'); return code, data`)
	if err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	vals := popReturns(L, 2)
	requireErrTable(t, vals, 0, errcode.ErrEncodeFailed)
}

// ---------------------------------------------------------------------------
// http_request / connect_* —— 经 awaitIO（在协程内 yield WaitSpec），故走 RunActionScript +
// recordingWaiter（WaitIO 分支同步跑 IOJob 并返回 renderer，等价于真实调度器）。
// ---------------------------------------------------------------------------

// runNetworkScript 把一段 Lua 源码包进 execute(r) 入口，编译进内存 RuntimePool，注入 ctx +
// waiter 后执行并返回 error。包进 execute 是因为 await_*（http_request / connect_*）的顶层
// 协程身份校验要求调用点必须在 action 协程内，chunk 主体不在协程内会触发 fail-loud。
func runNetworkScript(t *testing.T, ctx *Context, body string) error {
	t.Helper()
	wrapped := "function execute(r)\n" + body + "\nend"
	rp := newTestPool(t, map[string]string{"net.lua": wrapped})
	L := rp.Acquire()
	defer rp.Release(L)
	SetContext(L, ctx)
	_, _, _, err := rp.RunActionScript(L, "net.lua")
	return err
}

// TestHTTPRequest_Success http_request 成功（任意 status，含非 2xx）→ (nil, status, body)。
// 脚本内 assert 返回值契约，断言失败时主动 error 让 RunActionScript 报错。
func TestHTTPRequest_Success_WithChecks(t *testing.T) {
	ns := &fakeNetSender{httpExchange: &engine.HTTPExchange{
		StatusCode: 200, Body: []byte("ok"), NetLatency: 1,
	}}
	ctx := &Context{NetSender: ns, Waiter: &recordingWaiter{}}

	err := runNetworkScript(t, ctx, `
		local network = require("network")
		local e, status, body = network.http_request("http://x/", "GET", "json", {k="v"})
		if e ~= nil then error("e 应为 nil，实际 " .. tostring(e)) end
		if status ~= 200 then error("status 应为 200，实际 " .. tostring(status)) end
		if body ~= "ok" then error("body 应为 ok，实际 " .. tostring(body)) end
		return nil
	`)
	if err != nil {
		t.Fatalf("脚本断言失败或运行出错: %v", err)
	}
}

// TestHTTPRequest_404_PassesThrough HTTP 404 仍属「无框架传输错误」：err=nil + status=404。
func TestHTTPRequest_404_PassesThrough(t *testing.T) {
	ns := &fakeNetSender{httpExchange: &engine.HTTPExchange{
		StatusCode: 404, Body: []byte("nope"), NetLatency: 1,
	}}
	ctx := &Context{NetSender: ns, Waiter: &recordingWaiter{}}

	err := runNetworkScript(t, ctx, `
		local network = require("network")
		local e, status, body = network.http_request("http://x/missing")
		if e ~= nil then error("404 不应产生框架 err table，实际 " .. tostring(e)) end
		if status ~= 404 then error("status 应为 404，实际 " .. tostring(status)) end
		if body ~= "nope" then error("body 应为 nope，实际 " .. tostring(body)) end
		return nil
	`)
	if err != nil {
		t.Fatalf("脚本断言失败或运行出错: %v", err)
	}
}

// TestHTTPRequest_FrameworkErr NetSender 返回 error → (err table, 0, "")。
func TestHTTPRequest_FrameworkErr(t *testing.T) {
	ns := &fakeNetSender{
		httpErr: engine.NewActionError(errcode.ErrConnNotFound, "conn-gone"),
	}
	ctx := &Context{NetSender: ns, Waiter: &recordingWaiter{}}

	err := runNetworkScript(t, ctx, `
		local network = require("network")
		local e, status, body = network.http_request("http://x/")
		if type(e) ~= "table" then error("e 应为 err table，实际 " .. type(e)) end
		if e.code ~= ` + errCodeLua(errcode.ErrConnNotFound) + ` then error("code 错") end
		if status ~= 0 then error("status 应为 0，实际 " .. tostring(status)) end
		if body ~= "" then error("body 应为空串，实际 " .. tostring(body)) end
		return nil
	`)
	if err != nil {
		t.Fatalf("脚本断言失败或运行出错: %v", err)
	}
}

// TestConnectTCP_Success connect_tcp 成功 → err=nil。
func TestConnectTCP_Success(t *testing.T) {
	ns := &fakeNetSender{}
	ctx := &Context{NetSender: ns, Waiter: &recordingWaiter{}}

	err := runNetworkScript(t, ctx, `
		local network = require("network")
		local e = network.connect_tcp("logic", "127.0.0.1:8080")
		if e ~= nil then error("connect 成功 e 应为 nil，实际 " .. tostring(e)) end
		return nil
	`)
	if err != nil {
		t.Fatalf("脚本断言失败或运行出错: %v", err)
	}
	if ns.connectTCPCalls != 1 {
		t.Fatalf("ConnectTCP 应被调用 1 次，实际 %d", ns.connectTCPCalls)
	}
	if ns.lastConnectTCPService != "logic" || ns.lastConnectTCPAddress != "127.0.0.1:8080" {
		t.Fatalf("ConnectTCP 入参错误：service=%q address=%q",
			ns.lastConnectTCPService, ns.lastConnectTCPAddress)
	}
}

// TestConnectTCP_PreCanceled 拨号前 ctx 已取消 → 直接返回 ErrActionCanceled，
// 不投递后台 IOJob（pushErr 在 awaitIO 之前）。
func TestConnectTCP_PreCanceled(t *testing.T) {
	ns := &fakeNetSender{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 预取消
	scriptCtx := &Context{
		NetSender: ns,
		Ctx:       ctx,
		Waiter:    &recordingWaiter{},
	}

	err := runNetworkScript(t, scriptCtx, `
		local network = require("network")
		local e = network.connect_tcp("logic", "127.0.0.1:8080")
		if type(e) ~= "table" then error("预取消应返回 err table，实际 " .. type(e)) end
		if e.code ~= ` + errCodeLua(errcode.ErrActionCanceled) + ` then
			error("code 应为 ErrActionCanceled，实际 " .. tostring(e.code))
		end
		return nil
	`)
	if err != nil {
		t.Fatalf("脚本断言失败或运行出错: %v", err)
	}
	// 关键：预取消分支不应投递后台 ConnectTCP（awaitIO 未触发）。
	if ns.connectTCPCalls != 0 {
		t.Fatalf("预取消不应拨号，实际 ConnectTCP 调用 %d 次", ns.connectTCPCalls)
	}
}

// TestConnectTCP_DialErr 拨号返回 error → err table（从 ActionError 提取）。
func TestConnectTCP_DialErr(t *testing.T) {
	ns := &fakeNetSender{
		connectErr: engine.NewActionError(errcode.ErrConnClosed, "dial-fail"),
	}
	ctx := &Context{NetSender: ns, Waiter: &recordingWaiter{}}

	err := runNetworkScript(t, ctx, `
		local network = require("network")
		local e = network.connect_tcp("logic", "127.0.0.1:8080")
		if type(e) ~= "table" then error("拨号失败应返回 err table，实际 " .. type(e)) end
		if e.code ~= ` + errCodeLua(errcode.ErrConnClosed) + ` then
			error("code 应为 ErrConnClosed，实际 " .. tostring(e.code))
		end
		return nil
	`)
	if err != nil {
		t.Fatalf("脚本断言失败或运行出错: %v", err)
	}
	if ns.connectTCPCalls != 1 {
		t.Fatalf("ConnectTCP 应被调用 1 次，实际 %d", ns.connectTCPCalls)
	}
}

// TestConnectUDP_Success connect_udp 成功 → err=nil。
func TestConnectUDP_Success(t *testing.T) {
	ns := &fakeNetSender{}
	ctx := &Context{NetSender: ns, Waiter: &recordingWaiter{}}

	err := runNetworkScript(t, ctx, `
		local network = require("network")
		local e = network.connect_udp("battle", "127.0.0.1:9000")
		if e ~= nil then error("connect 成功 e 应为 nil，实际 " .. tostring(e)) end
		return nil
	`)
	if err != nil {
		t.Fatalf("脚本断言失败或运行出错: %v", err)
	}
	if ns.connectUDPCalls != 1 {
		t.Fatalf("ConnectUDP 应被调用 1 次，实际 %d", ns.connectUDPCalls)
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// popReturns 从 LState 栈顶弹出 n 个返回值（DoString 后栈上的 return 值）。
func popReturns(L *lua.LState, n int) []lua.LValue {
	vals := make([]lua.LValue, n)
	for i := n - 1; i >= 0; i-- {
		vals[i] = L.Get(-1)
		L.Pop(1)
	}
	return vals
}

// errCodeLua 把 errcode.ErrorCode 渲染为 Lua 数字字面量。
func errCodeLua(c errcode.ErrorCode) string {
	return strconv.Itoa(int(c))
}
