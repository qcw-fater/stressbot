package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

func TestHandleStopRejectsTaskWithoutActiveCancel(t *testing.T) {
	originalLogger := stresslog.GetLogger()
	stresslog.ReplaceLogger(zap.NewNop())
	if originalLogger != nil {
		t.Cleanup(func() {
			stresslog.ReplaceLogger(originalLogger)
		})
	}

	a := &Agent{
		currentTask: &TaskAssignment{TaskID: "task-a"},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/agent/v1/stop",
		strings.NewReader(`{"taskId":"task-a"}`),
	)

	a.handleStop(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
}

func TestHandleStopCancelsCurrentTask(t *testing.T) {
	stresslog.ReplaceLogger(zap.NewNop())
	taskContext, cancel := context.WithCancel(context.Background())
	a := &Agent{
		currentTask: &TaskAssignment{TaskID: "task-a"},
		taskCancel:  cancel,
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/agent/v1/stop",
		strings.NewReader(`{"taskId":"task-a"}`),
	)

	a.handleStop(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	select {
	case <-taskContext.Done():
	default:
		t.Fatal("current task context was not canceled")
	}
}

func TestHandleStopDoesNotCancelDifferentCurrentTask(t *testing.T) {
	stresslog.ReplaceLogger(zap.NewNop())
	taskContext, cancel := context.WithCancel(context.Background())
	a := &Agent{
		currentTask: &TaskAssignment{TaskID: "new-task"},
		taskCancel:  cancel,
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/agent/v1/stop",
		strings.NewReader(`{"taskId":"old-task"}`),
	)

	a.handleStop(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	select {
	case <-taskContext.Done():
		t.Fatal("new task context was canceled by an old stop request")
	default:
	}
}
