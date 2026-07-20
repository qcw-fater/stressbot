package robot

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"stressbot/engine"
	"stressbot/script"

	lua "github.com/yuin/gopher-lua"
)

// newWaitRobot 构造仅含协作式等待所需字段的最小 Robot（绕过 NewRobot 的网络/Lua 依赖）。
func newWaitRobot(ctx context.Context) *Robot {
	r := &Robot{ctx: ctx}
	r.sched = newRobotScheduler(r)
	return r
}

// TestCooperativeWait_SleepDrainsTasks sleep 等待窗口内应 drain 已投递的任务（listen 回调），
// 而不是等到窗口结束才执行——这是协作式调度解决长阻塞饿死的核心行为。
func TestCooperativeWait_SleepDrainsTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newWaitRobot(ctx)

	var ran int32
	r.sched.enqueue(pendingTask{name: "cb", exec: func() { atomic.AddInt32(&ran, 1) }})

	start := time.Now()
	out := r.sched.wait(time.Now().Add(40*time.Millisecond), 0, nil)
	if out.Canceled || out.TimedOut || out.Exchange != nil {
		t.Fatalf("sleep 应返回空 outcome，实际 %+v", out)
	}
	if got := atomic.LoadInt32(&ran); got != 1 {
		t.Fatalf("等待窗口内任务未被 drain：ran=%d", got)
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Fatalf("sleep 提前返回：elapsed=%v", elapsed)
	}
}

// TestCooperativeWait_Canceled ctx 取消时立即返回 Canceled。
func TestCooperativeWait_Canceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := newWaitRobot(ctx)
	cancel()

	out := r.sched.wait(time.Now().Add(time.Second), 0, nil)
	if !out.Canceled {
		t.Fatalf("ctx 取消应返回 Canceled，实际 %+v", out)
	}
}

// TestCooperativeWait_ListenHit listen 轮询命中即返回对应 Exchange。
func TestCooperativeWait_ListenHit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newWaitRobot(ctx)

	want := &engine.NetExchange{Body: []byte("hit")}
	var calls int32
	check := func() *engine.NetExchange {
		if atomic.AddInt32(&calls, 1) >= 2 {
			return want
		}
		return nil
	}
	out := r.sched.wait(time.Now().Add(time.Second), 5, check)
	if out.Exchange != want {
		t.Fatalf("应返回命中的 Exchange，实际 %+v", out)
	}
}

// TestCooperativeSleep_DrainsAndCancels 节点延迟/wait 节点的协作式休眠：
// 休眠期间 drain 任务队列；ctx 取消时返回 ctx.Err()。
func TestCooperativeSleep_DrainsAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newWaitRobot(ctx)
	h := &robotActionHandler{robot: r}

	var ran int32
	r.sched.enqueue(pendingTask{name: "cb", exec: func() { atomic.AddInt32(&ran, 1) }})
	if err := h.CooperativeSleep(ctx, 30*time.Millisecond); err != nil {
		t.Fatalf("正常休眠不应返回 error: %v", err)
	}
	if atomic.LoadInt32(&ran) != 1 {
		t.Fatalf("休眠期间任务未被 drain：ran=%d", atomic.LoadInt32(&ran))
	}

	cancel()
	if err := h.CooperativeSleep(ctx, time.Second); err == nil {
		t.Fatal("ctx 取消应返回 error")
	}
	// d<=0 立即返回（此时 ctx 已取消 → 返回 ctx.Err()）。
	if err := h.CooperativeSleep(context.Background(), 0); err != nil {
		t.Fatalf("d<=0 且 ctx 正常应返回 nil: %v", err)
	}
}

// TestCooperativeWait_ListenTimeout listen 始终未命中则到时返回 TimedOut。
func TestCooperativeWait_ListenTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newWaitRobot(ctx)

	check := func() *engine.NetExchange { return nil }
	out := r.sched.wait(time.Now().Add(20*time.Millisecond), 5, check)
	if !out.TimedOut {
		t.Fatalf("listen 未命中应超时，实际 %+v", out)
	}
}

// TestAwaitIO_DispatchesAndDrains Class B 协作式 I/O：作业在后台协程跑阻塞调用，等待窗口内
// 执行器仍 drain 任务队列（listen 回调不被饿死），作业完成后返回其 IORenderer。
func TestAwaitIO_DispatchesAndDrains(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newWaitRobot(ctx)

	var ran int32
	r.sched.enqueue(pendingTask{name: "cb", exec: func() { atomic.AddInt32(&ran, 1) }})

	var jobRan int32
	spec := &script.WaitSpec{
		Kind:   script.WaitIO,
		IOName: "test.io",
		IOJob: func() script.IORenderer {
			atomic.AddInt32(&jobRan, 1)
			time.Sleep(20 * time.Millisecond) // 模拟阻塞 I/O 往返
			return func(L *lua.LState) []lua.LValue {
				return []lua.LValue{lua.LString("done")}
			}
		},
	}

	out := r.sched.awaitIO(spec)
	if out.IORender == nil {
		t.Fatal("awaitIO 应返回作业的 IORenderer")
	}
	if atomic.LoadInt32(&jobRan) != 1 {
		t.Fatalf("后台作业应执行一次，实际 %d", atomic.LoadInt32(&jobRan))
	}
	if atomic.LoadInt32(&ran) != 1 {
		t.Fatalf("等待 I/O 期间应 drain 任务队列，实际 ran=%d", atomic.LoadInt32(&ran))
	}

	L := lua.NewState()
	defer L.Close()
	vals := out.IORender(L)
	if len(vals) != 1 || vals[0].String() != "done" {
		t.Fatalf("renderer 应产出 [\"done\"]，实际 %+v", vals)
	}
}

func TestRunIOReturnsPoolSubmissionError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newWaitRobot(ctx)
	sentinel := errors.New("pool rejected")

	err := r.sched.runIOWithSubmit(func() {}, func(func()) error { return sentinel })

	if !errors.Is(err, sentinel) {
		t.Fatalf("runIOWithSubmit() error = %v, want %v", err, sentinel)
	}
}

// TestCooperativeWait_ListenHitMidTaskBatch 回归：listen 在一批回调任务处理期间就绪时，
// 应被 per-callback 检查及时捕获，而不是先把整批任务 drain 完才在边界查到。
// 旧实现（drain 批 + boundary check）会把整批跑完（ran≈N）才命中；内联版每处理完一个任务即
// 回 loop 顶 check，命中的时候只 drain 了极少数（ran≪N）。
func TestCooperativeWait_ListenHitMidTaskBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newWaitRobot(ctx)

	const batch = 30
	var ran int32
	for i := 0; i < batch; i++ {
		r.sched.enqueue(pendingTask{name: "cb", exec: func() { atomic.AddInt32(&ran, 1) }})
	}

	want := &engine.NetExchange{Body: []byte("hit")}
	var calls int32
	check := func() *engine.NetExchange {
		if atomic.AddInt32(&calls, 1) >= 2 {
			return want // 第二次起视为就绪（模拟消息在首批任务处理后到达）
		}
		return nil
	}

	out := r.sched.wait(time.Now().Add(time.Second), 100, check)
	if out.Exchange != want {
		t.Fatalf("应返回命中的 Exchange，实际 %+v", out)
	}
	if got := atomic.LoadInt32(&ran); got >= batch {
		t.Fatalf("listen 命中前不应 drain 整批任务：ran=%d（旧实现会 drain 完 %d 个才在边界命中）", got, batch)
	}
}
