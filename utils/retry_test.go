package utils

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
)

func TestNewExponentialBackOffUsesPolicy(t *testing.T) {
	b := NewExponentialBackOff(RetryPolicy{
		Initial: time.Second,
		Max:     4 * time.Second,
		Factor:  2,
		Jitter:  0,
	})

	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second}
	for i, expected := range want {
		if got := b.NextBackOff(); got != expected {
			t.Fatalf("backoff[%d] = %s, want %s", i, got, expected)
		}
	}
}

func TestNewExponentialBackOffJitterStaysWithinBounds(t *testing.T) {
	b := NewExponentialBackOff(RetryPolicy{
		Initial: time.Second,
		Max:     time.Second,
		Factor:  2,
		Jitter:  0.5,
	})

	for i := 0; i < 100; i++ {
		got := b.NextBackOff()
		if got < 500*time.Millisecond || got > 1500*time.Millisecond {
			t.Fatalf("jittered backoff = %s, want within [500ms, 1.5s]", got)
		}
	}
}

func TestRetryWithStopRetriesUntilSuccess(t *testing.T) {
	var calls atomic.Int32
	b := &sequenceBackOff{durations: []time.Duration{0, 0}}
	var notified []time.Duration

	err := RetryWithStop(nil, func() error {
		if calls.Add(1) < 3 {
			return errors.New("temporary")
		}
		return nil
	}, func(_ error, wait time.Duration) {
		notified = append(notified, wait)
	}, b)

	if err != nil {
		t.Fatalf("RetryWithStop() error = %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("operation calls = %d, want 3", got)
	}
	if len(notified) != 2 {
		t.Fatalf("notify calls = %d, want 2", len(notified))
	}
}

func TestRetryWithStopDoesNotRunWhenAlreadyStopped(t *testing.T) {
	stop := make(chan struct{})
	close(stop)
	var calls atomic.Int32

	err := RetryWithStop(stop, func() error {
		calls.Add(1)
		return nil
	}, nil, &sequenceBackOff{})

	if !errors.Is(err, ErrRetryStopped) {
		t.Fatalf("RetryWithStop() error = %v, want ErrRetryStopped", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("operation calls = %d, want 0", got)
	}
}

func TestRetryWithStopInterruptsWait(t *testing.T) {
	stop := make(chan struct{})
	done := make(chan error, 1)
	started := make(chan struct{})

	go func() {
		done <- RetryWithStop(stop, func() error {
			close(started)
			return errors.New("temporary")
		}, nil, &sequenceBackOff{durations: []time.Duration{time.Hour}})
	}()

	<-started
	close(stop)
	select {
	case err := <-done:
		if !errors.Is(err, ErrRetryStopped) {
			t.Fatalf("RetryWithStop() error = %v, want ErrRetryStopped", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RetryWithStop did not return after stop")
	}
}

func TestRetryWithStopUnwrapsPermanentError(t *testing.T) {
	want := errors.New("permanent")
	var calls atomic.Int32

	err := RetryWithStop(nil, func() error {
		calls.Add(1)
		return backoff.Permanent(want)
	}, nil, &sequenceBackOff{durations: []time.Duration{0}})

	if !errors.Is(err, want) {
		t.Fatalf("RetryWithStop() error = %v, want %v", err, want)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("operation calls = %d, want 1", got)
	}
}

func TestRetryWithStopReturnsLastErrorWhenBackOffStops(t *testing.T) {
	want := errors.New("last")
	var calls atomic.Int32

	err := RetryWithStop(nil, func() error {
		calls.Add(1)
		return want
	}, nil, backoff.WithMaxRetries(&sequenceBackOff{durations: []time.Duration{0}}, 1))

	if !errors.Is(err, want) {
		t.Fatalf("RetryWithStop() error = %v, want %v", err, want)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("operation calls = %d, want 2", got)
	}
}

type sequenceBackOff struct {
	durations []time.Duration
	index     int
}

func (b *sequenceBackOff) Reset() {
	b.index = 0
}

func (b *sequenceBackOff) NextBackOff() time.Duration {
	if b.index >= len(b.durations) {
		return backoff.Stop
	}
	d := b.durations[b.index]
	b.index++
	return d
}
