package script

import (
	"testing"
	"time"

	"stressbot/engine"
	"stressbot/errcode"

	lua "github.com/yuin/gopher-lua"
)

// TestRequestResultValues covers the WaitResponse → (code, data) mapping for the cooperative
// await_*_request drive-loop（与同步 doTCPRequest/doUDPRequest 返回契约一致）。
func TestRequestResultValues(t *testing.T) {
	L := lua.NewState()
	defer L.Close()

	spec := &WaitSpec{Kind: WaitResponse, Service: "logic", RouteKey: "S2C.Login", Packet: []byte("req12")}

	t.Run("success", func(t *testing.T) {
		ctx := &Context{}
		out := WaitOutcome{Exchange: &engine.NetExchange{
			Body:   []byte("ok"),
			Timing: engine.RequestTiming{WireRTT: time.Millisecond},
		}}
		vals := requestResultValues(L, ctx, spec, out)
		if n, _ := vals[0].(lua.LNumber); int(n) != 0 {
			t.Fatalf("code=%v，want 0", vals[0])
		}
		if s, ok := vals[1].(lua.LString); !ok || string(s) != "ok" {
			t.Fatalf("data=%v，want \"ok\"", vals[1])
		}
	})

	t.Run("server_header_err", func(t *testing.T) {
		ctx := &Context{}
		out := WaitOutcome{Exchange: &engine.NetExchange{HeaderErr: 1001, Body: []byte("e")}}
		vals := requestResultValues(L, ctx, spec, out)
		if n, _ := vals[0].(lua.LNumber); int(n) != 1001 {
			t.Fatalf("code=%v，want 1001", vals[0])
		}
		if s, ok := vals[1].(lua.LString); !ok || string(s) != "e" {
			t.Fatalf("data=%v，want \"e\"（headerErr 仍回传 body）", vals[1])
		}
	})

	t.Run("request_timeout", func(t *testing.T) {
		ctx := &Context{}
		out := WaitOutcome{Err: engine.NewActionError(errcode.ErrRecvTimeout, "timeout")}
		vals := requestResultValues(L, ctx, spec, out)
		if n, _ := vals[0].(lua.LNumber); int(n) != int(errcode.ErrRecvTimeout) {
			t.Fatalf("code=%v，want ErrRecvTimeout(%d)", vals[0], errcode.ErrRecvTimeout)
		}
		if vals[1] != lua.LNil {
			t.Fatalf("超时 data 应为 nil，实际 %v", vals[1])
		}
	})

	t.Run("canceled", func(t *testing.T) {
		ctx := &Context{}
		out := WaitOutcome{Canceled: true}
		vals := requestResultValues(L, ctx, spec, out)
		if n, _ := vals[0].(lua.LNumber); int(n) != int(errcode.ErrActionCanceled) {
			t.Fatalf("code=%v，want ErrActionCanceled(%d)", vals[0], errcode.ErrActionCanceled)
		}
		if vals[1] != lua.LNil {
			t.Fatalf("取消 data 应为 nil，实际 %v", vals[1])
		}
	})

	t.Run("nil_exchange_defaults", func(t *testing.T) {
		ctx := &Context{}
		// Err 非空但 Exchange 为 nil：应用 spec.Packet 长度兜底 SendWireBytes，不 panic。
		out := WaitOutcome{Err: engine.NewActionError(errcode.ErrConnNotFound, "x")}
		vals := requestResultValues(L, ctx, spec, out)
		if n, _ := vals[0].(lua.LNumber); int(n) != int(errcode.ErrConnNotFound) {
			t.Fatalf("code=%v，want ErrConnNotFound(%d)", vals[0], errcode.ErrConnNotFound)
		}
	})
}
