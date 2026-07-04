package script

import (
	"bytes"
	"context"
	"testing"
	"time"

	"stressbot/adapter"
	"stressbot/engine"
	"stressbot/state"

	lua "github.com/yuin/gopher-lua"
)

type fakeResolver struct {
	adp      adapter.Adapter
	adps     map[string]adapter.Adapter
	resolved []string
}

func (r *fakeResolver) Resolve(server string) adapter.Adapter {
	if r == nil {
		return nil
	}
	r.resolved = append(r.resolved, server)
	if r.adps != nil {
		return r.adps[server]
	}
	return r.adp
}

type fakeNetSender struct {
	tcpReqExchange        *engine.NetExchange
	tcpReqErr             error
	udpReqExchange        *engine.NetExchange
	udpReqErr             error
	tcpReqCalls           int
	udpReqCalls           int
	tcpSendErr            error
	udpSendErr            error
	tcpSendCalls          int
	udpSendCalls          int
	lastTCPService        string
	lastUDPService        string
	lastTCPPacket         []byte
	lastUDPPacket         []byte
	tcpSendBytes          int
	udpSendBytes          int
	connectErr            error
	connectTCPCalls       int
	connectUDPCalls       int
	lastConnectTCPService string
	lastConnectTCPAddress string
	lastConnectUDPService string
	lastConnectUDPAddress string
	httpExchange          *engine.HTTPExchange
	httpErr               error
	listenResp            *engine.NetExchange
	tcpListenCalls        int
	udpListenCalls        int
	tcpKey                []byte
	udpKey                []byte
}

func (f *fakeNetSender) TCPSend(service string, packet []byte) (int, error) {
	f.tcpSendCalls++
	f.lastTCPService = service
	f.lastTCPPacket = append([]byte(nil), packet...)
	if f.tcpSendBytes != 0 {
		return f.tcpSendBytes, f.tcpSendErr
	}
	return len(packet), f.tcpSendErr
}

func (f *fakeNetSender) UDPSend(service string, packet []byte) (int, error) {
	f.udpSendCalls++
	f.lastUDPService = service
	f.lastUDPPacket = append([]byte(nil), packet...)
	if f.udpSendBytes != 0 {
		return f.udpSendBytes, f.udpSendErr
	}
	return len(packet), f.udpSendErr
}

func (f *fakeNetSender) TCPRequest(string, []byte, string, ...time.Duration) (*engine.NetExchange, error) {
	f.tcpReqCalls++
	return f.tcpReqExchange, f.tcpReqErr
}

func (f *fakeNetSender) UDPRequest(string, []byte, string, ...time.Duration) (*engine.NetExchange, error) {
	f.udpReqCalls++
	return f.udpReqExchange, f.udpReqErr
}

func (f *fakeNetSender) ConnectTCP(service, address string) error {
	f.connectTCPCalls++
	f.lastConnectTCPService = service
	f.lastConnectTCPAddress = address
	return f.connectErr
}

func (f *fakeNetSender) ConnectUDP(service, address string) error {
	f.connectUDPCalls++
	f.lastConnectUDPService = service
	f.lastConnectUDPAddress = address
	return f.connectErr
}

func (f *fakeNetSender) HTTPRequest(string, string, string, []byte) (*engine.HTTPExchange, error) {
	return f.httpExchange, f.httpErr
}

func (f *fakeNetSender) CloseTCP(string) {}

func (f *fakeNetSender) CloseUDP(string) {}

func (f *fakeNetSender) GetTCPListenResp(string, string) *engine.NetExchange {
	f.tcpListenCalls++
	return f.listenResp
}

func (f *fakeNetSender) GetUDPListenResp(string, string) *engine.NetExchange {
	f.udpListenCalls++
	return f.listenResp
}

func (f *fakeNetSender) EnsureTCPListener(string, string, int) {}

func (f *fakeNetSender) EnsureUDPListener(string, string, int) {}

func (f *fakeNetSender) GetTCPSecretKey(string) []byte { return f.tcpKey }

func (f *fakeNetSender) SetTCPSecretKey(_ string, key []byte) {
	f.tcpKey = append([]byte(nil), key...)
}

func (f *fakeNetSender) GetUDPSecretKey(string) []byte { return f.udpKey }

func (f *fakeNetSender) SetUDPSecretKey(_ string, key []byte) {
	f.udpKey = append([]byte(nil), key...)
}

func TestFakeNetSenderSecretKeyCopiesInput(t *testing.T) {
	f := &fakeNetSender{}

	tcpKey := []byte{1, 2, 3}
	f.SetTCPSecretKey("logic", tcpKey)
	tcpKey[0] = 9
	if got, want := f.GetTCPSecretKey("logic"), []byte{1, 2, 3}; !bytes.Equal(got, want) {
		t.Fatalf("TCP secret key = %v, want %v", got, want)
	}

	udpKey := []byte{4, 5, 6}
	f.SetUDPSecretKey("battle", udpKey)
	udpKey[0] = 9
	if got, want := f.GetUDPSecretKey("battle"), []byte{4, 5, 6}; !bytes.Equal(got, want) {
		t.Fatalf("UDP secret key = %v, want %v", got, want)
	}
}

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
