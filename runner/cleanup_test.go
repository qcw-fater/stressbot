package runner

import (
	"errors"
	"testing"

	"stressbot/internal/stresslog"

	"go.uber.org/zap"
)

type recordingDialerStopper struct{ calls int }

func (s *recordingDialerStopper) Stop() error {
	s.calls++
	return nil
}

func TestStopDialerCleansSynchronouslyWhenPoolRejects(t *testing.T) {
	stresslog.ReplaceLogger(zap.NewNop())
	stopper := &recordingDialerStopper{}
	stopDialerWithSubmit(stopper, func(func()) error { return errors.New("pool rejected") })
	if stopper.calls != 1 {
		t.Fatalf("Stop() calls = %d, want 1", stopper.calls)
	}
}
