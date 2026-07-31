package robot

import (
	"context"
	"testing"
	"time"

	"stressbot/engine"
)

// TestWaitMeasuresListenWaitFromFrameArrival 监听等待的终点是帧被内核收到的时刻，
// 而不是本轮轮询发现它的时刻。
//
// 回归的是轮询取整误差：若以「发现时刻」为终点，测出来的是 pollMs 的整数倍，
// 轮询间隔越大偏得越多，服务端推送的真实延迟被埋掉。
func TestWaitMeasuresListenWaitFromFrameArrival(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newWaitRobot(ctx)

	// 帧在等待开始后 40ms 到达，但轮询间隔 200ms——发现它至少要等到 200ms。
	frameAt := time.Now().Add(40 * time.Millisecond)
	check := func() *engine.NetExchange {
		if time.Now().Before(frameAt) {
			return nil
		}
		return &engine.NetExchange{Body: []byte("hit"), RecvFrameAt: frameAt}
	}

	out := r.sched.wait(time.Now().Add(2*time.Second), 200, check)

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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newWaitRobot(ctx)

	arrived := time.Now().Add(-3 * time.Second)
	check := func() *engine.NetExchange {
		return &engine.NetExchange{Body: []byte("cached"), RecvFrameAt: arrived}
	}

	out := r.sched.wait(time.Now().Add(time.Second), 5, check)

	if out.ListenWaitKind != engine.ListenWaitReady {
		t.Fatalf("ListenWaitKind = %v, want Ready", out.ListenWaitKind)
	}
	if out.ListenWait != 0 {
		t.Fatalf("ListenWait = %v, want 0（不可测不产样本）", out.ListenWait)
	}
}

// TestWaitWithoutFrameTimestampIsUnknown 网络层没给到达时刻时不臆造样本。
func TestWaitWithoutFrameTimestampIsUnknown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newWaitRobot(ctx)

	check := func() *engine.NetExchange {
		return &engine.NetExchange{Body: []byte("no-timestamp")}
	}

	out := r.sched.wait(time.Now().Add(time.Second), 5, check)

	if out.ListenWaitKind != engine.ListenWaitUnknown {
		t.Fatalf("ListenWaitKind = %v, want Unknown", out.ListenWaitKind)
	}
}
