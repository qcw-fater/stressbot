package robot

import (
	"sync/atomic"
	"testing"
	"time"

	"stressbot/engine"
	"stressbot/monitor"
	"stressbot/network"
)

// TestWaitMeasuresListenWaitFromFrameArrival 监听等待的终点是帧被内核收到的时刻，
// 而不是本轮轮询发现它的时刻。
//
// 回归的是调度误差：计时终点必须取帧到达时刻，而不是 owner 被事件唤醒后的时刻。
func TestWaitMeasuresListenWaitFromFrameArrival(t *testing.T) {
	ctx := t.Context()
	r := newWaitRobot(ctx)

	wake := make(chan struct{}, 1)
	var ready atomic.Pointer[engine.NetExchange]
	frameAt := time.Now().Add(40 * time.Millisecond)
	check := func() *engine.NetExchange {
		return ready.Load()
	}
	timer := time.AfterFunc(time.Until(frameAt), func() {
		ready.Store(&engine.NetExchange{Body: []byte("hit"), RecvFrameAt: frameAt})
		wake <- struct{}{}
	})
	defer timer.Stop()

	out := r.sched.wait(ctx, time.Now().Add(2*time.Second), wake, check)

	if out.ListenWaitKind != engine.ListenWaitMeasured {
		t.Fatalf("ListenWaitKind = %v, want Measured", out.ListenWaitKind)
	}
	if out.ListenWait <= 0 || out.ListenWait > 150*time.Millisecond {
		t.Fatalf("ListenWait = %v，应接近帧到达耗时(~40ms)而非轮询间隔(200ms)", out.ListenWait)
	}
}

// TestWaitMarksAlreadyQueuedMessageAsReady 消息在开始等待前就已在队列里时，
// 等待时长不可测，标记为 Ready 而不是产出一个接近 0 的样本。
func TestWaitMarksAlreadyQueuedMessageAsReady(t *testing.T) {
	ctx := t.Context()
	r := newWaitRobot(ctx)

	arrived := time.Now().Add(-3 * time.Second)
	check := func() *engine.NetExchange {
		return &engine.NetExchange{Body: []byte("cached"), RecvFrameAt: arrived}
	}

	out := r.sched.wait(ctx, time.Now().Add(time.Second), nil, check)

	if out.ListenWaitKind != engine.ListenWaitReady {
		t.Fatalf("ListenWaitKind = %v, want Ready", out.ListenWaitKind)
	}
	if out.ListenWait != 0 {
		t.Fatalf("ListenWait = %v, want 0（不可测不产样本）", out.ListenWait)
	}
}

// TestWaitWithoutFrameTimestampIsUnknown 网络层没给到达时刻时不臆造样本。
func TestWaitWithoutFrameTimestampIsUnknown(t *testing.T) {
	ctx := t.Context()
	r := newWaitRobot(ctx)

	check := func() *engine.NetExchange {
		return &engine.NetExchange{Body: []byte("no-timestamp")}
	}

	out := r.sched.wait(ctx, time.Now().Add(time.Second), nil, check)

	if out.ListenWaitKind != engine.ListenWaitUnknown {
		t.Fatalf("ListenWaitKind = %v, want Unknown", out.ListenWaitKind)
	}
}

func TestNetSenderTCPListenUsesConnectionNotification(t *testing.T) {
	ctx := t.Context()
	r := newWaitRobot(ctx)
	r.client = network.NewClient("event-listen-test", time.Second, monitor.TimingRTTOnly)
	if !r.client.ConnectTCP("logic") {
		t.Fatal("创建 TCP 连接占位失败")
	}
	conn := r.client.GetTCPConn("logic")
	if err := conn.RegisterListen("push", nil, 4); err != nil {
		t.Fatalf("RegisterListen() error = %v", err)
	}

	frameAt := time.Now().Add(20 * time.Millisecond)
	timer := time.AfterFunc(time.Until(frameAt), func() {
		conn.OnReceive("push", []byte("payload"), 0, 11, network.MessageTiming{RecvFrameAt: frameAt})
	})
	defer timer.Stop()

	exchange, err := (&netSenderAdapter{robot: r}).TCPListen(ctx, "logic", "push", time.Second)
	if err != nil {
		t.Fatalf("TCPListen() error = %v", err)
	}
	if exchange == nil || string(exchange.Body) != "payload" || exchange.RecvWireBytes != 11 {
		t.Fatalf("TCPListen() exchange = %+v", exchange)
	}
}
