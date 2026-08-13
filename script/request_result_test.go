package script

import (
	"testing"
	"time"

	"stressbot/engine"
	"stressbot/errcode"

	lua "github.com/yuin/gopher-lua"
)

// TestRequestResultValues covers the WaitResponse → (err, data) mapping for the cooperative
// await_*_request drive-loop（err-table 契约：err 为 nil 成功 / table 失败）。
func TestRequestResultValues(t *testing.T) {
	L := lua.NewState()
	defer L.Close()

	spec := &WaitSpec{Kind: WaitResponse, Service: "logic", RouteKey: "S2C.Login", Packet: []byte("req12")}

	// parseErr 从 vals[0] 提取 err table 的 (code, detail)，ok=false 表示不是 err table。
	parseErr := func(t *testing.T, vals []lua.LValue) (code int, detail string, ok bool) {
		t.Helper()
		c, d, isErr := parseErrTable(vals[0])
		return c, d, isErr
	}

	t.Run("success", func(t *testing.T) {
		ctx := &Context{}
		out := WaitOutcome{Exchange: &engine.NetExchange{
			Body:   []byte("ok"),
			Timing: engine.RequestTiming{WireRTT: time.Millisecond},
		}}
		vals := requestResultValues(L, ctx, spec, out)
		if vals[0] != lua.LNil {
			t.Fatalf("err=%v，want nil（成功）", vals[0])
		}
		if s, ok := vals[1].(lua.LString); !ok || string(s) != "ok" {
			t.Fatalf("data=%v，want \"ok\"", vals[1])
		}
	})

	t.Run("server_header_err", func(t *testing.T) {
		ctx := &Context{}
		out := WaitOutcome{Exchange: &engine.NetExchange{HeaderErr: 1001, Body: []byte("e")}}
		vals := requestResultValues(L, ctx, spec, out)
		code, _, ok := parseErr(t, vals)
		if !ok {
			t.Fatalf("err=%v，want err table（HeaderErr）", vals[0])
		}
		if code != 1001 {
			t.Fatalf("code=%d，want 1001", code)
		}
		if s, ok := vals[1].(lua.LString); !ok || string(s) != "e" {
			t.Fatalf("data=%v，want \"e\"（headerErr 仍回传 body）", vals[1])
		}
	})

	t.Run("request_timeout", func(t *testing.T) {
		ctx := &Context{}
		out := WaitOutcome{Err: errcode.NewActionError(errcode.ErrRecvTimeout, "timeout")}
		vals := requestResultValues(L, ctx, spec, out)
		code, _, ok := parseErr(t, vals)
		if !ok {
			t.Fatalf("err=%v，want err table（ErrRecvTimeout）", vals[0])
		}
		if code != int(errcode.ErrRecvTimeout) {
			t.Fatalf("code=%d，want ErrRecvTimeout(%d)", code, errcode.ErrRecvTimeout)
		}
		if vals[1] != lua.LNil {
			t.Fatalf("超时 data 应为 nil，实际 %v", vals[1])
		}
	})

	t.Run("canceled", func(t *testing.T) {
		ctx := &Context{}
		out := WaitOutcome{Canceled: true}
		vals := requestResultValues(L, ctx, spec, out)
		code, _, ok := parseErr(t, vals)
		if !ok {
			t.Fatalf("err=%v，want err table（ErrActionCanceled）", vals[0])
		}
		if code != int(errcode.ErrActionCanceled) {
			t.Fatalf("code=%d，want ErrActionCanceled(%d)", code, errcode.ErrActionCanceled)
		}
		if vals[1] != lua.LNil {
			t.Fatalf("取消 data 应为 nil，实际 %v", vals[1])
		}
	})

	t.Run("nil_exchange_defaults", func(t *testing.T) {
		ctx := &Context{}
		// Err 非空但 Exchange 为 nil：应用 spec.Packet 长度兜底 SendWireBytes，不 panic。
		out := WaitOutcome{Err: errcode.NewActionError(errcode.ErrConnNotFound, "x")}
		vals := requestResultValues(L, ctx, spec, out)
		code, _, ok := parseErr(t, vals)
		if !ok {
			t.Fatalf("err=%v，want err table（ErrConnNotFound）", vals[0])
		}
		if code != int(errcode.ErrConnNotFound) {
			t.Fatalf("code=%d，want ErrConnNotFound(%d)", code, errcode.ErrConnNotFound)
		}
	})
}
