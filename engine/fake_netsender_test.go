package engine

import (
	"context"
	"time"
)

// fakeNetSender 为 engine 单元测试提供 NetSender 空实现；各测试按需覆盖单个方法。
type fakeNetSender struct{}

func (f *fakeNetSender) TCPSend(string, []byte) (int, error) { return 0, nil }
func (f *fakeNetSender) UDPSend(string, []byte) (int, error) { return 0, nil }
func (f *fakeNetSender) TCPRequest(string, []byte, string, ...time.Duration) (*NetExchange, error) {
	return nil, nil
}
func (f *fakeNetSender) UDPRequest(string, []byte, string, ...time.Duration) (*NetExchange, error) {
	return nil, nil
}
func (f *fakeNetSender) TCPListen(context.Context, string, string, time.Duration) (*NetExchange, error) {
	return nil, nil
}
func (f *fakeNetSender) UDPListen(context.Context, string, string, time.Duration) (*NetExchange, error) {
	return nil, nil
}
func (f *fakeNetSender) ConnectTCP(string, string) error { return nil }
func (f *fakeNetSender) ConnectUDP(string, string) error { return nil }
func (f *fakeNetSender) HTTPRequest(string, string, string, []byte) (*HTTPExchange, error) {
	return nil, nil
}
func (f *fakeNetSender) CloseTCP(string)                              {}
func (f *fakeNetSender) CloseUDP(string)                              {}
func (f *fakeNetSender) GetTCPListenResp(string, string) *NetExchange { return nil }
func (f *fakeNetSender) GetUDPListenResp(string, string) *NetExchange { return nil }
func (f *fakeNetSender) EnsureTCPListener(string, string, int)        {}
func (f *fakeNetSender) EnsureUDPListener(string, string, int)        {}
func (f *fakeNetSender) GetTCPSecretKey(string) []byte                { return nil }
func (f *fakeNetSender) SetTCPSecretKey(string, []byte)               {}
func (f *fakeNetSender) GetUDPSecretKey(string) []byte                { return nil }
func (f *fakeNetSender) SetUDPSecretKey(string, []byte)               {}
