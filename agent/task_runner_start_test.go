package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"stressbot/robot"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

type recordingRobotStopper struct {
	called  bool
	cleanup robot.CleanupStatus
}

type recordingDialerStopper struct {
	calls int
	err   error
}

func (s *recordingDialerStopper) Stop() error {
	s.calls++
	return s.err
}

func (s *recordingRobotStopper) StopAll() robot.CleanupStatus {
	s.called = true
	return s.cleanup
}

func TestFinishManagerStartFailureCleansRobotsAndReturnsFailed(t *testing.T) {
	wantCleanup := robot.UnknownCleanupStatus(robot.CleanupReasonStopWaitTimeout, "test cleanup")
	stopper := &recordingRobotStopper{cleanup: wantCleanup}
	sentinel := errors.New("pool rejected")

	result := finishManagerStartFailure(context.Background(), stopper, sentinel)

	if !stopper.called {
		t.Fatal("StopAll() was not called after manager start failure")
	}
	if result.Result != TaskFailed {
		t.Fatalf("result = %q, want %q", result.Result, TaskFailed)
	}
	if !strings.Contains(result.ErrorMsg, sentinel.Error()) {
		t.Fatalf("ErrorMsg = %q, want pool error", result.ErrorMsg)
	}
	if result.CleanupStatus.Message != wantCleanup.Message {
		t.Fatalf("CleanupStatus = %+v, want %+v", result.CleanupStatus, wantCleanup)
	}
}

func TestStopDialerCleansSynchronouslyWhenPoolRejects(t *testing.T) {
	stresslog.ReplaceLogger(zap.NewNop())
	stopper := &recordingDialerStopper{}
	sentinel := errors.New("pool rejected")

	stopDialerWithSubmit(stopper, func(func()) error { return sentinel })

	if stopper.calls != 1 {
		t.Fatalf("Stop() calls = %d, want 1", stopper.calls)
	}
}
