package robot

import (
	"context"
	"testing"

	"stressbot/network"
	"stressbot/protocol"
)

// stubResolver 测试用 CodecResolver：按显式 map 返回，未映射返回 nil（与生产 codecResolver 同语义）。
type stubResolver struct {
	byServer map[string]protocol.Adapter
}

func (s *stubResolver) Resolve(server string) protocol.Adapter { return s.byServer[server] }

// TestConnectTCP_ResolverNil_FailLoud 验证 Resolve 未命中 server → 拨号中止、返回 false、不触达 dialer。
//
// 覆盖 T2-C1 的 fail-loud 契约（brief §设计 3）：
//   - ConnectTCP(serviceName="unknown") → resolver.Resolve("tcp:unknown") == nil；
//   - 中文 Error 日志 + monitor.ConnFailed()；
//   - r.client.CloseTCP(serviceName) 已关；
//   - 不调用 r.dialer.DialTCP（r.dialer 故意留 nil，若误进 Dial 路径会 nil-deref panic，双重保险）。
//
// resolver 命中 + 真实 dial 注入正确性的运行时验证见「报告：运行时验证待办」。
func TestConnectTCP_ResolverNil_FailLoud(t *testing.T) {
	r := &Robot{
		id:      1,
		account: "bot_test",
		client:  network.NewClient("bot_test", 0, ""),
		ctx:     context.Background(),
		cancel:  func() {},
		// resolver 仅映射 tcp:logic；tcp:unknown 未映射 → nil。
		resolver: &stubResolver{byServer: map[string]protocol.Adapter{
			"tcp:logic": fakeAdapter{},
		}},
		// r.dialer 故意留 nil：fail-loud 路径在 Resolve 之后即返回，不会读 r.dialer。
		// 若误进 Dial 路径会 nil-deref panic，测试即失败。
	}

	// ConnectTCP 内部先 r.client.ConnectTCP（创建占位 Connection），再 Resolve(nil) fail-loud。
	// 期望：返回 false，不触达 dialer。
	got := r.ConnectTCP("unknown", "127.0.0.1:9999")
	if got {
		t.Fatalf("ConnectTCP(未映射 service) 应返回 false（fail-loud），实际 true")
	}

	// 占位 Connection 应已被 CloseTCP 清理。
	if conn := r.client.GetTCPConn("unknown"); conn != nil {
		t.Errorf("fail-loud 后占位连接应已清理，仍存在 %v", conn)
	}
}

// TestConnectUDP_ResolverNil_FailLoud 同上，UDP 路径。
func TestConnectUDP_ResolverNil_FailLoud(t *testing.T) {
	r := &Robot{
		id:      2,
		account: "bot_test2",
		client:  network.NewClient("bot_test2", 0, ""),
		ctx:     context.Background(),
		cancel:  func() {},
		resolver: &stubResolver{byServer: map[string]protocol.Adapter{
			"udp:battle": fakeAdapter{},
		}},
	}

	got := r.ConnectUDP("unknown", "127.0.0.1:9999")
	if got {
		t.Fatalf("ConnectUDP(未映射 service) 应返回 false（fail-loud），实际 true")
	}
	if conn := r.client.GetUDPConn("unknown"); conn != nil {
		t.Errorf("fail-loud 后占位连接应已清理，仍存在 %v", conn)
	}
}

// TestConnectTCP_ResolverHit_ResolveNonNil 验证 resolver 命中时 Resolve 非 nil（不验证完整 dial，
// dial 由真实 gnet 引擎驱动，留运行时验证）。直接验 Resolve 阶段，避免 nil dialer 干扰。
func TestConnectTCP_ResolverHit_ResolveNonNil(t *testing.T) {
	want := fakeAdapter{}
	r := &Robot{
		resolver: &stubResolver{byServer: map[string]protocol.Adapter{"tcp:logic": want}},
	}

	got := r.resolver.Resolve("tcp:logic")
	if got == nil {
		t.Fatal("Resolve(tcp:logic) 命中应返回非 nil")
	}
	gotUnknown := r.resolver.Resolve("tcp:unknown")
	if gotUnknown != nil {
		t.Errorf("Resolve(未映射) 应返回 nil，得到 %T", gotUnknown)
	}
}

// fakeAdapter 仅满足 protocol.Adapter 接口签名，方法不需要正确行为
// （fail-loud 路径在 codec 方法被调用之前即返回；resolver 命中路径只验证 Resolve 非 nil）。
type fakeAdapter struct{}

func (fakeAdapter) HeaderSize() int                                    { return 12 }
func (fakeAdapter) BodyLength([]byte) int                              { return 0 }
func (fakeAdapter) EncodeTCP(route any, body, secretKey []byte) []byte { return nil }
func (fakeAdapter) EncodeUDP(route any, body, secretKey []byte) []byte { return nil }
func (fakeAdapter) DecodeTCP(data, secretKey []byte) (string, []byte, uint64) {
	return "", nil, 0
}
func (fakeAdapter) DecodeUDP(data, secretKey []byte) (string, []byte, uint64) {
	return "", nil, 0
}
func (fakeAdapter) ExpectedRouteKey(route any) string { return "" }
func (fakeAdapter) DescribeError(uint64) string       { return "" }
func (fakeAdapter) Close()                            {}
