package robot

import (
	"context"
	"errors"
	"testing"
	"time"

	"stressbot/engine"
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
	ae, ok := errors.AsType[*engine.ActionError](err)
	if !ok {
		t.Fatalf("err ?? ActionError: %v", err)
	}
	if errcode.ErrorCode(ae.ErrorCode()) != want {
		t.Fatalf("???=%d?want %d", ae.ErrorCode(), want)
	}
}

// TestAwaitResponse_ConnNotFound ?????? service ? Err ? ErrConnNotFound?
func TestAwaitResponse_ConnNotFound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newResponseRobot(ctx)

	out := r.sched.awaitResponse(&script.WaitSpec{
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newResponseRobot(ctx)
	r.client.ConnectTCP("logic") // ???????sendFunc=nil?

	out := r.sched.awaitResponse(&script.WaitSpec{
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newResponseRobot(ctx)

	out := r.sched.awaitResponse(&script.WaitSpec{
		Kind:     script.WaitResponse,
		Proto:    "udp",
		Service:  "battle",
		RouteKey: "S2C.Y",
		Packet:   []byte("req"),
		Duration: time.Second,
	})
	wantCode(t, out.Err, errcode.ErrConnNotFound)
}
