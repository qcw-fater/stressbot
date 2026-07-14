package robot

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"stressbot/engine"
	"stressbot/monitor"
	"stressbot/network"
	"stressbot/state"
	"stressbot/utils"
)

// lifecycleFakeHandler 是用于生命周期测试的 engine.ActionHandler 替身：
// ExecuteAction 可选阻塞（block），用于测试 Stop/Close 中断；命中 ctx.Done 即返回。
type lifecycleFakeHandler struct {
	actionCalls atomic.Int32
	block       chan struct{}
}

func (h *lifecycleFakeHandler) ExecuteAction(ctx context.Context, ad *engine.ActionDef) error {
	h.actionCalls.Add(1)
	if h.block != nil {
		select {
		case <-h.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (h *lifecycleFakeHandler) ExecuteBoolean(string) bool                  { return true }
func (h *lifecycleFakeHandler) RegisterListen([]engine.ListenRef) error     { return nil }
func (h *lifecycleFakeHandler) CooperativeSleep(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// newLifecycleRobot 构造一个最小可 Start 的 Robot：
//   - executor 使用 fake handler（最小 flow：单个无延迟 action），不依赖 protox/resolver/dialer；
//   - l == nil：跳过 LState 绑定与归还，cleanup 仍走完 CloseAllWithTimeout / state.Clear；
//   - ctx 以 WorkPool.Context() 为父级，模拟合并后 NewRobot 的目标行为。
func newLifecycleRobot(t *testing.T, handler *lifecycleFakeHandler) *Robot {
	t.Helper()
	flow := &engine.TaskFlow{
		DefaultDelayMs: -1,
		Nodes: map[string]*engine.Node{
			"main": {Type: engine.NodeAction, Action: "noop", DelayMs: -1},
		},
		Actions: map[string]*engine.ActionDef{
			"noop": {Name: "noop", Pattern: engine.PatternClearState},
		},
	}
	ctx, cancel := context.WithCancel(utils.GetWorkPool().Context())
	r := &Robot{
		id:       1,
		account:  "t",
		state:    state.NewStore(),
		client:   network.NewClient("t", time.Second, monitor.TimingDetailLevel("")),
		ctx:      ctx,
		cancel:   cancel,
		executor: engine.NewExecutor(flow, handler, "t"),
		execDone: make(chan struct{}),
		done:     make(chan struct{}),
	}
	r.sched = newRobotScheduler(r)
	return r
}

// TestRobotLifecycleNaturalCompletion Executor 自然完成后：done 关闭、onDone 恰好一次、action 执行一次。
func TestRobotLifecycleNaturalCompletion(t *testing.T) {
	h := &lifecycleFakeHandler{}
	r := newLifecycleRobot(t, h)
	var doneCount atomic.Int32
	r.onDone = func(_ *Robot, _ CleanupStatus) { doneCount.Add(1) }

	r.Start()

	select {
	case <-r.done:
	case <-time.After(2 * time.Second):
		t.Fatal("自然完成后 done 未在 2s 内关闭")
	}

	if c := doneCount.Load(); c != 1 {
		t.Fatalf("onDone 调用 %d 次，期望 1", c)
	}
	if c := h.actionCalls.Load(); c != 1 {
		t.Fatalf("action 执行 %d 次，期望 1", c)
	}
}

// TestRobotLifecycleStopInterrupts Robot.Stop 取消 ctx → 阻塞中的 action 退出 → done 关闭。
func TestRobotLifecycleStopInterrupts(t *testing.T) {
	h := &lifecycleFakeHandler{block: make(chan struct{})}
	r := newLifecycleRobot(t, h)
	r.Start()

	// 等 action 进入阻塞（ExecuteAction 已被调用一次）
	waitForActionEntered(t, h)

	r.Stop()

	select {
	case <-r.done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop 后 done 未在 2s 内关闭")
	}
}

// TestRobotLifecycleClose Robot.Close 触发 cleanup → 取消 → action 退出 → done 关闭。
func TestRobotLifecycleClose(t *testing.T) {
	h := &lifecycleFakeHandler{block: make(chan struct{})}
	r := newLifecycleRobot(t, h)
	r.Start()
	waitForActionEntered(t, h)

	// Close 阻塞至 cleanup 完成（含 CloseAllWithTimeout）；之后 Start 任务继续关 done。
	go func() { _ = r.Close() }()

	select {
	case <-r.done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close 后 done 未在 3s 内关闭")
	}
}

// waitForActionEntered 轮询直到 fake handler 的 ExecuteAction 至少被调用一次，
// 确保 action 已进入阻塞点（否则 Stop/Close 可能在 action 启动前生效，测试不够确定性）。
func waitForActionEntered(t *testing.T, h *lifecycleFakeHandler) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if h.actionCalls.Load() > 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("等待 action 进入执行超时")
}
