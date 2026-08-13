package engine

import (
	"strings"
	"testing"

	"stressbot/protocol"
)

// stubResolverE 测试用 CodecResolver：按显式 map 返回，未映射返回 nil（与生产 codecResolver 同语义）。
type stubResolverE struct {
	byServer map[string]protocol.Adapter
}

func (s *stubResolverE) Resolve(server string) protocol.Adapter { return s.byServer[server] }

// encodeSpyAdapter 记录被调用的方法 + 入参，用于断言 encode/ExpectedRouteKey/DescribeError
// 走的是 resolver.Resolve 出来的那个 adapter（而非历史 r.adp）。
type encodeSpyAdapter struct {
	encodeCalls      []encodeCall
	routeKeyCalls    []routeKeyCall
	describeCalls    []uint64
	headerSize       int
	routeKeyReturned string
	encodeReturned   []byte
	describeReturned string
}

type encodeCall struct {
	proto     string
	route     any
	body      []byte
	secretKey []byte
}

type routeKeyCall struct {
	route any
}

func (a *encodeSpyAdapter) HeaderSize() int       { return a.headerSize }
func (a *encodeSpyAdapter) BodyLength([]byte) int { return 0 }
func (a *encodeSpyAdapter) EncodeTCP(route any, body, secretKey []byte) []byte {
	a.encodeCalls = append(a.encodeCalls, encodeCall{proto: "tcp", route: route, body: body, secretKey: secretKey})
	return a.encodeReturned
}
func (a *encodeSpyAdapter) EncodeUDP(route any, body, secretKey []byte) []byte {
	a.encodeCalls = append(a.encodeCalls, encodeCall{proto: "udp", route: route, body: body, secretKey: secretKey})
	return a.encodeReturned
}
func (a *encodeSpyAdapter) DecodeTCP(data, secretKey []byte) (string, []byte, uint64) {
	return "", nil, 0
}
func (a *encodeSpyAdapter) DecodeUDP(data, secretKey []byte) (string, []byte, uint64) {
	return "", nil, 0
}
func (a *encodeSpyAdapter) ExpectedRouteKey(route any) string {
	a.routeKeyCalls = append(a.routeKeyCalls, routeKeyCall{route: route})
	return a.routeKeyReturned
}
func (a *encodeSpyAdapter) DescribeError(code uint64) string {
	a.describeCalls = append(a.describeCalls, code)
	return a.describeReturned
}
func (a *encodeSpyAdapter) Close() {}

// TestProtocolEncode_ResolverDispatchesByProtoService 验证 protocolEncode 按
// "<proto>:<service>" Resolve 出对应 adapter，并调用其 EncodeTCP/UDP。
//
// 这是 T2-C2 的核心契约：ActionExecutor 不再持单一 adp，而持 resolver；
// 每次编码按 def.Service + pattern(proto) 拼 server 串 Resolve 出该连接的 Go SchemaAdapter。
func TestProtocolEncode_ResolverDispatchesByProtoService(t *testing.T) {
	tcpAdp := &encodeSpyAdapter{encodeReturned: []byte("tcp-pkt"), routeKeyReturned: "tcp-rk"}
	udpAdp := &encodeSpyAdapter{encodeReturned: []byte("udp-pkt"), routeKeyReturned: "udp-rk"}
	resolver := &stubResolverE{byServer: map[string]protocol.Adapter{
		"tcp:logic":  tcpAdp,
		"udp:battle": udpAdp,
	}}
	ae := &ActionExecutor{resolver: resolver}

	// TCP 路径。
	got := ae.protocolEncode("tcp", "logic", "any-route", []byte("body"), []byte("k"))
	if string(got) != "tcp-pkt" {
		t.Fatalf("protocolEncode(tcp, logic) 返回 %q，want tcp-pkt", got)
	}
	if len(tcpAdp.encodeCalls) != 1 || tcpAdp.encodeCalls[0].proto != "tcp" {
		t.Fatalf("tcp adapter 未收到 1 次 EncodeTCP，got %+v", tcpAdp.encodeCalls)
	}

	// UDP 路径走不同 adapter。
	got = ae.protocolEncode("udp", "battle", "any-route", []byte("body"), []byte("k"))
	if string(got) != "udp-pkt" {
		t.Fatalf("protocolEncode(udp, battle) 返回 %q，want udp-pkt", got)
	}
	if len(udpAdp.encodeCalls) != 1 || udpAdp.encodeCalls[0].proto != "udp" {
		t.Fatalf("udp adapter 未收到 1 次 EncodeUDP，got %+v", udpAdp.encodeCalls)
	}

	// 另一 tcp service（未映射）走 nil 路径。
	got = ae.protocolEncode("tcp", "unknown", "any-route", nil, nil)
	if got != nil {
		t.Fatalf("未映射 service 应返回 nil（fail loud 由调用方处理），got %v", got)
	}
}

// TestProtocolEncode_ResolverNil_FailLoud 验证 protocolEncode 在 Resolve nil 时返回 nil，
// 调用方（execSend/execRequest）必须将其翻译为 ErrEncodeFailed fail-loud。
func TestProtocolEncode_ResolverNil_ReturnsNil(t *testing.T) {
	resolver := &stubResolverE{byServer: map[string]protocol.Adapter{}}
	ae := &ActionExecutor{resolver: resolver}
	got := ae.protocolEncode("tcp", "missing", "route", nil, nil)
	if got != nil {
		t.Fatalf("Resolve nil 时 protocolEncode 应返回 nil，got %v", got)
	}
}

// TestResolveAdapterForPattern 验证 pattern(proto) + service → server 串解析逻辑。
// 抽成纯方法 resolveAdapter 便于单测，供 encode/ExpectedRouteKey/DescribeError 共用。
func TestResolveAdapterForPattern(t *testing.T) {
	logic := &encodeSpyAdapter{}
	battle := &encodeSpyAdapter{}
	resolver := &stubResolverE{byServer: map[string]protocol.Adapter{
		"tcp:logic":  logic,
		"udp:battle": battle,
	}}
	ae := &ActionExecutor{resolver: resolver}

	tests := []struct {
		name    string
		proto   string
		service string
		want    protocol.Adapter
	}{
		{name: "tcp logic", proto: "tcp", service: "logic", want: logic},
		{name: "udp battle", proto: "udp", service: "battle", want: battle},
		{name: "tcp unknown nil", proto: "tcp", service: "unknown", want: nil},
		{name: "udp unknown nil", proto: "udp", service: "unknown", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ae.resolveAdapter(tt.proto, tt.service)
			if got != tt.want {
				t.Fatalf("resolveAdapter(%s,%s) = %p，want %p", tt.proto, tt.service, got, tt.want)
			}
		})
	}
}

// TestExpectedRouteKeyViaResolver 验证 ActionExecutor 暴露的 ExpectedRouteKey 路径
// 走 resolver.Resolve 出的 adapter（不再走 ae.adp）。
func TestExpectedRouteKeyViaResolver(t *testing.T) {
	logic := &encodeSpyAdapter{routeKeyReturned: "logic-route"}
	resolver := &stubResolverE{byServer: map[string]protocol.Adapter{"tcp:logic": logic}}
	ae := &ActionExecutor{resolver: resolver}

	got := ae.expectedRouteKey("tcp", "logic", "route-val")
	if got != "logic-route" {
		t.Fatalf("expectedRouteKey = %q，want logic-route", got)
	}
	if len(logic.routeKeyCalls) != 1 {
		t.Fatalf("logic adapter ExpectedRouteKey 应被调用 1 次，got %d", len(logic.routeKeyCalls))
	}
}

// TestDescribeErrorViaResolver 验证 headerErr 描述走 resolver.Resolve(proto:service) 出的 adapter。
func TestDescribeErrorViaResolver(t *testing.T) {
	battle := &encodeSpyAdapter{describeReturned: "battle-err"}
	resolver := &stubResolverE{byServer: map[string]protocol.Adapter{"udp:battle": battle}}
	ae := &ActionExecutor{resolver: resolver}

	got := ae.describeError("udp", "battle", 1001)
	if got != "battle-err" {
		t.Fatalf("describeError = %q，want battle-err", got)
	}
	if len(battle.describeCalls) != 1 || battle.describeCalls[0] != 1001 {
		t.Fatalf("battle adapter DescribeError 应收到 1001 一次，got %+v", battle.describeCalls)
	}
}

// TestResolveAdapterServerStringFormat 验证 server 串拼接格式（防 regression：用 ":" 分隔，不是其它）。
func TestResolveAdapterServerStringFormat(t *testing.T) {
	called := []string{}
	resolver := &stubResolverE{
		byServer: map[string]protocol.Adapter{},
	}
	// 用包装 resolver 记录入参。
	wrap := &recordingResolver{inner: resolver, seen: &called}
	ae := &ActionExecutor{resolver: wrap}
	_ = ae.resolveAdapter("tcp", "logic")
	_ = ae.resolveAdapter("udp", "battle")
	if len(called) != 2 {
		t.Fatalf("应记录 2 次 Resolve，got %d", len(called))
	}
	if called[0] != "tcp:logic" {
		t.Errorf("第 1 次 Resolve 入参 = %q，want %q", called[0], "tcp:logic")
	}
	if called[1] != "udp:battle" {
		t.Errorf("第 2 次 Resolve 入参 = %q，want %q", called[1], "udp:battle")
	}
}

type recordingResolver struct {
	inner protocol.CodecResolver
	seen  *[]string
}

func (r *recordingResolver) Resolve(server string) protocol.Adapter {
	*r.seen = append(*r.seen, server)
	return r.inner.Resolve(server)
}

// 兜底编译期断言：stubResolverE 实现 CodecResolver。
var _ protocol.CodecResolver = (*stubResolverE)(nil)

// _ 避免 strings 包未使用（保留 import 占位以便后续断言文案扩展）。
var _ = strings.Contains
