package robot

import (
	"context"
	"errors"
	"testing"
	"time"

	"stressbot/errcode"
	"stressbot/monitor"
	"stressbot/network"
	"stressbot/script"
)

// newResponseRobot ????? Client ??? Robot??? awaitResponse ??????????
func newResponseRobot(ctx context.Context) *Robot {
	r := &Robot{
		ctx:    ctx,
		client: network.NewClient("t", time.Second, monitor.TimingDetailLevel("")),
	}
	r.sched = newRobotScheduler(r)
	return r
}

func wantCode(t *testing.T, err error, want errcode.ErrorCode) {
	t.Helper()
	ae, ok := errors.AsType[*errcode.ActionError](err)
	if !ok {
		t.Fatalf("err ?? ActionError: %v", err)
	}
	if errcode.ErrorCode(ae.ErrorCode()) != want {
		t.Fatalf("???=%d?want %d", ae.ErrorCode(), want)
	}
}

// TestAwaitResponse_ConnNotFound ?????? service ? Err ? ErrConnNotFound?
func TestAwaitResponse_ConnNotFound(t *testing.T) {
	ctx := t.Context()
	r := newResponseRobot(ctx)

	out := r.sched.awaitResponse(ctx, &script.WaitSpec{
		Kind:     script.WaitResponse,
		Proto:    "tcp",
		Service:  "absent",
		RouteKey: "S2C.X",
		Packet:   []byte("req"),
		Duration: time.Second,
	})
	if out.Err == nil {
		t.Fatal("?????? Err")
	}
	wantCode(t, out.Err, errcode.ErrConnNotFound)
}

// TestAwaitResponse_SendFailed ???????? sendFunc??????? ???? Err?
func TestAwaitResponse_SendFailed(t *testing.T) {
	ctx := t.Context()
	r := newResponseRobot(ctx)
	r.client.ConnectTCP("logic") // ???????sendFunc=nil?

	out := r.sched.awaitResponse(ctx, &script.WaitSpec{
		Kind:     script.WaitResponse,
		Proto:    "tcp",
		Service:  "logic",
		RouteKey: "S2C.X",
		Packet:   []byte("req"),
		Duration: time.Second,
	})
	if out.Err == nil {
		t.Fatal("???????????? Err")
	}
	wantCode(t, out.Err, errcode.ErrSendFailed)
}

// TestAwaitResponse_UDPConnNotFound UDP ??????????? ErrConnNotFound??? proto ????
func TestAwaitResponse_UDPConnNotFound(t *testing.T) {
	ctx := t.Context()
	r := newResponseRobot(ctx)

	out := r.sched.awaitResponse(ctx, &script.WaitSpec{
		Kind:     script.WaitResponse,
		Proto:    "udp",
		Service:  "battle",
		RouteKey: "S2C.Y",
		Packet:   []byte("req"),
		Duration: time.Second,
	})
	wantCode(t, out.Err, errcode.ErrConnNotFound)
}

// TestAwaitResponse_CallerCtxCanceled 调用方 ctx（声明式动作传的流程 ctx）已取消时，
// 即便 Robot 生命周期 ctx 还活着也立即给出取消结果，且不发包。
func TestAwaitResponse_CallerCtxCanceled(t *testing.T) {
	r := newResponseRobot(t.Context()) // Robot ctx 未取消
	r.client.ConnectTCP("logic")

	callerCtx, cancel := context.WithCancel(context.Background())
	cancel()

	out := r.sched.awaitResponse(callerCtx, &script.WaitSpec{
		Kind:     script.WaitResponse,
		Proto:    "tcp",
		Service:  "logic",
		RouteKey: "S2C.X",
		Packet:   []byte("req"),
		Duration: time.Minute,
	})

	if !out.Canceled {
		t.Fatalf("调用方 ctx 取消应返回 Canceled，实际 %+v", out)
	}
	if out.Err != nil {
		t.Fatalf("取消不应另报 Err: %v", out.Err)
	}
}

// TestNetSenderTCPRequestHonorsCallerCtx 声明式请求经 engine.ActionNetSender 传入的 ctx
// 取消后立即返回 ACTION_CANCELED（而非等满超时窗口后的 RECV_TIMEOUT）。
func TestNetSenderTCPRequestHonorsCallerCtx(t *testing.T) {
	r := newResponseRobot(t.Context())
	r.client.ConnectTCP("logic")

	callerCtx, cancel := context.WithCancel(context.Background())
	cancel()

	exchange, err := (&netSenderAdapter{robot: r}).TCPRequest(callerCtx, "logic", []byte("req"), "S2C.X", time.Minute)

	wantCode(t, err, errcode.ErrActionCanceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v 未保留 context.Canceled", err)
	}
	if exchange == nil {
		t.Fatal("取消路径仍应返回 NetExchange 以便上层统计字节")
	}
}
