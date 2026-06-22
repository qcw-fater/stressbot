package script

import (
	"context"
	"time"

	"stressbot/adapter"
	"stressbot/engine"
	"stressbot/state"

	lua "github.com/yuin/gopher-lua"
)

type fakeResolver struct {
	adp adapter.Adapter
}

func (r *fakeResolver) Resolve(server string) adapter.Adapter { return r.adp }

type fakeNetSender struct {
	tcpReqExchange *engine.NetExchange
	tcpReqErr      error
	udpReqExchange *engine.NetExchange
	udpReqErr      error
	tcpSendErr     error
	udpSendErr     error
	connectErr     error
	httpExchange   *engine.HTTPExchange
	httpErr        error
	listenResp     *engine.NetExchange
	tcpKey         []byte
	udpKey         []byte
}

func (f *fakeNetSender) TCPSend(string, []byte) (int, error) { return 0, f.tcpSendErr }

func (f *fakeNetSender) UDPSend(string, []byte) (int, error) { return 0, f.udpSendErr }

func (f *fakeNetSender) TCPRequest(string, []byte, string, ...time.Duration) (*engine.NetExchange, error) {
	return f.tcpReqExchange, f.tcpReqErr
}

func (f *fakeNetSender) UDPRequest(string, []byte, string, ...time.Duration) (*engine.NetExchange, error) {
	return f.udpReqExchange, f.udpReqErr
}

func (f *fakeNetSender) ConnectTCP(string, string) error { return f.connectErr }

func (f *fakeNetSender) ConnectUDP(string, string) error { return f.connectErr }

func (f *fakeNetSender) HTTPRequest(string, string, string, []byte) (*engine.HTTPExchange, error) {
	return f.httpExchange, f.httpErr
}

func (f *fakeNetSender) CloseTCP(string) {}

func (f *fakeNetSender) CloseUDP(string) {}

func (f *fakeNetSender) GetTCPListenResp(string, string) *engine.NetExchange { return f.listenResp }

func (f *fakeNetSender) GetUDPListenResp(string, string) *engine.NetExchange { return f.listenResp }

func (f *fakeNetSender) EnsureTCPListener(string, string, int) {}

func (f *fakeNetSender) EnsureUDPListener(string, string, int) {}

func (f *fakeNetSender) RegisterHeartbeat(engine.HeartbeatActionConfig) error { return nil }

func (f *fakeNetSender) GetTCPSecretKey(string) []byte { return f.tcpKey }

func (f *fakeNetSender) SetTCPSecretKey(string, []byte) {}

func (f *fakeNetSender) GetUDPSecretKey(string) []byte { return f.udpKey }

func (f *fakeNetSender) SetUDPSecretKey(string, []byte) {}

// newTestState 注册全部模块 + 注入 fake Context。
func newTestState(t interface{ Helper() }, ctx context.Context, ns engine.NetSender, resolver adapter.CodecResolver) *lua.LState {
	t.Helper()
	L := lua.NewState()
	registerAPIs(L)
	c := &Context{
		RobotID:               1,
		Account:               "test",
		Store:                 state.NewStore(),
		Resolver:              resolver,
		NetSender:             ns,
		Ctx:                   ctx,
		DefaultRequestTimeout: 10 * time.Second,
	}
	SetContext(L, c)
	return L
}

var _ engine.NetSender = (*fakeNetSender)(nil)
var _ adapter.CodecResolver = (*fakeResolver)(nil)
