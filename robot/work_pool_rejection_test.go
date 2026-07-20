package robot

import (
	"context"
	"errors"
	"testing"
	"time"

	"stressbot/monitor"
	"stressbot/network"
	"stressbot/state"
)

func newRejectedStartRobot() *Robot {
	ctx, cancel := context.WithCancel(context.Background())
	return &Robot{
		id:       1,
		account:  "bot_1",
		state:    state.NewStore(),
		client:   network.NewClient("bot_1", time.Second, monitor.TimingRTTOnly),
		ctx:      ctx,
		cancel:   cancel,
		execDone: make(chan struct{}),
		done:     make(chan struct{}),
	}
}

func TestRobotStartReturnsPoolSubmissionError(t *testing.T) {
	r := newRejectedStartRobot()
	sentinel := errors.New("pool rejected")

	err := r.startWithSubmit(func(func()) error { return sentinel })

	if !errors.Is(err, sentinel) {
		t.Fatalf("startWithSubmit() error = %v, want %v", err, sentinel)
	}
	if r.running.Load() {
		t.Fatal("robot remains running after submission failure")
	}
	select {
	case <-r.done:
	default:
		t.Fatal("robot done channel was not closed after submission failure")
	}
	select {
	case <-r.execDone:
	default:
		t.Fatal("robot executor channel was not closed after submission failure")
	}
}

func TestManagerRollsBackRobotWhenStartFails(t *testing.T) {
	m := newBookkeepManager()
	r := newRejectedStartRobot()
	sentinel := errors.New("pool rejected")

	err := m.addAndStartRobot(0, r, func() error { return sentinel })

	if !errors.Is(err, sentinel) {
		t.Fatalf("addAndStartRobot() error = %v, want %v", err, sentinel)
	}
	if got := m.active.Load(); got != 0 {
		t.Fatalf("active = %d, want 0", got)
	}
	m.mu.RLock()
	robots := len(m.robots)
	_, indexed := m.robotIdx[r]
	m.mu.RUnlock()
	if robots != 0 || indexed {
		t.Fatalf("failed robot remains registered: robots=%d indexed=%v", robots, indexed)
	}
}

func TestCloseRobotsConcurrentCleansSynchronouslyWhenPoolRejects(t *testing.T) {
	r := newRejectedStartRobot()
	close(r.execDone)
	sentinel := errors.New("pool rejected")

	cleanup := closeRobotsConcurrentWithSubmit(
		[]*Robot{r},
		CleanupReasonAdminStop,
		func(func()) error { return sentinel },
	)

	if cleanup.Status != CleanupOK {
		t.Fatalf("cleanup status = %q, want %q; issues=%+v", cleanup.Status, CleanupOK, cleanup.Issues)
	}
	if cleanup.TotalRobots != 1 || cleanup.TimeoutRobots != 0 {
		t.Fatalf("cleanup totals = %+v, want one clean robot", cleanup)
	}
}

func TestRobotCleanupDoesNotSubmitExecutorWaiter(t *testing.T) {
	r := newRejectedStartRobot()
	close(r.execDone)
	submissions := 0

	cleanup := r.cleanupWithSubmit(CleanupReasonAdminStop, false, func(task func()) error {
		submissions++
		task()
		return nil
	})

	if cleanup.Status != CleanupOK {
		t.Fatalf("cleanup status = %q, want %q; issues=%+v", cleanup.Status, CleanupOK, cleanup.Issues)
	}
	if submissions != 1 {
		t.Fatalf("cleanup submissions = %d, want 1 connection cleanup task only", submissions)
	}
}

func TestStartDurationTimerReturnsPoolSubmissionError(t *testing.T) {
	m := newBookkeepManager()
	m.cfg.Duration = time.Second
	sentinel := errors.New("pool rejected")

	err := m.startDurationTimerWithSubmit(func(func()) error { return sentinel })

	if !errors.Is(err, sentinel) {
		t.Fatalf("startDurationTimerWithSubmit() error = %v, want %v", err, sentinel)
	}
}
