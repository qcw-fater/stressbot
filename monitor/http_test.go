package monitor

import (
	"errors"
	"testing"
)

func TestStartHTTPServerReturnsPoolSubmissionError(t *testing.T) {
	sentinel := errors.New("pool rejected")

	stop, err := startHTTPServerWithSubmit(0, func(func()) error { return sentinel })

	if !errors.Is(err, sentinel) {
		t.Fatalf("startHTTPServerWithSubmit() error = %v, want %v", err, sentinel)
	}
	if stop != nil {
		t.Fatal("stop function must be nil when the server task was not submitted")
	}
}
