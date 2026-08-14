package task

import (
	"context"
	"errors"
	"strings"
	"testing"

	"stressbot/robot"
)

type recordingRobotStopper struct {
	called  bool
	cleanup robot.CleanupStatus
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
	if result.Result != Failed {
		t.Fatalf("result = %q, want %q", result.Result, Failed)
	}
	if !strings.Contains(result.ErrorMsg, sentinel.Error()) {
		t.Fatalf("ErrorMsg = %q, want pool error", result.ErrorMsg)
	}
	if result.CleanupStatus.Message != wantCleanup.Message {
		t.Fatalf("CleanupStatus = %+v, want %+v", result.CleanupStatus, wantCleanup)
	}
}

func TestFinishManagerStartFailureDoesNotInferCancellationFromMessage(t *testing.T) {
	stopper := &recordingRobotStopper{}
	startErr := errors.New("worker rejected: context canceled")

	result := finishManagerStartFailure(context.Background(), stopper, startErr)

	if result.Result != Failed {
		t.Fatalf("result = %q, want %q", result.Result, Failed)
	}
}
